// The HTTP request editor in the pseudo-locale (UI-10): the URL
// bar, the sub-tabs, the Auth tab with several auth types and the Settings
// tab. Every text they show must come from the catalogue, so no English text
// of the "editor" namespace may be left (docs/frontend.md#localization).
import { act, fireEvent, render, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: () => undefined });
const pending = () => Object.assign(new Promise(() => undefined), { cancel: () => undefined });

const service = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: unknown) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
  InheritedAuth: vi.fn<(id: string) => unknown>(),
  InheritedSettings: vi.fn<(id: string) => unknown>(),
}));
const http = vi.hoisted(() => ({
  Variables: vi.fn<(id: string) => unknown>(),
  HeaderPreview: vi.fn<(input: unknown) => unknown>(),
  JWTPreview: vi.fn<(input: unknown) => unknown>(),
  NetrcFile: vi.fn<(input: unknown) => unknown>(),
  NetrcCheck: vi.fn<(input: unknown) => unknown>(),
  OAuth2Token: vi.fn<(input: unknown) => unknown>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: { ValidateJSON: () => cancellable({ valid: true, error: null }) },
}));

import type { Namespace } from "../../i18n";
import { DEFAULT_LOCALE, PSEUDO_LOCALE, setLocale, t } from "../../i18n";
import { pseudoString } from "../../i18n/pseudo";
import type { AuthDTO, RequestDTO } from "../../lib/collection";
import { createStores, StoreProvider, type TreeNode } from "../../stores";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { englishLeftovers, isPseudo, visibleTexts } from "../../test/pseudoLocale";
import { RequestEditor } from "./RequestEditor";

/** The namespace this test checks. */
const CONVERTED: readonly Namespace[] = ["editor"];

/** The method picker (MethodPicker), whose text is of namespace editorProtocols: checked on its own. */
function methodPicker(): Element[] {
  const picker = document.querySelector("[data-request-id] select");
  return picker === null ? [] : [picker.closest("label") ?? picker];
}

function foreignElements(): Element[] {
  return methodPicker();
}

function expectNoEnglish(root: Element, minPseudo: number) {
  expect(englishLeftovers([root], CONVERTED, { exclude: foreignElements() })).toEqual([]);
  expect(visibleTexts([root], foreignElements()).filter(isPseudo).length).toBeGreaterThanOrEqual(minPseudo);
  expect(englishLeftovers(methodPicker(), ["editorProtocols"])).toEqual([]);
}

const auth = (over: Partial<AuthDTO> = {}): AuthDTO => ({
  type: "inherit",
  username: "",
  password: "",
  prefix: null,
  token: "",
  key: "",
  value: "",
  in: "",
  disabled: false,
  localSecrets: [],
  extra: "",
  ...over,
});

const REQUEST: RequestDTO = {
  id: "req",
  kind: "http",
  name: "Orders",
  description: "",
  method: "GET",
  url: "{{baseUrl}}/orders",
  params: [{ name: "limit", value: "20", description: "", enabled: true, extra: "" }],
  pathParams: null,
  headers: [{ name: "Accept", value: "application/json", description: "", enabled: true, extra: "" }],
  body: { mode: "none", type: "", text: "", fields: null, parts: null, path: "", contentType: "", extra: "" },
  query: "",
  variables: "",
  operationName: "",
  connectionParams: "",
  schemaFile: "",
  address: "",
  sendMode: "",
  lineEnding: "",
  message: "",
  subprotocols: null,
  messageFormat: "",
  service: "",
  proto: { files: [], importPaths: [], inherit: false, extra: "" },
  metadata: [],
  bodyExamples: [],
  auth: auth(),
  settings: {
    timeoutMs: null,
    followRedirects: null,
    maxRedirects: null,
    followOriginalMethod: null,
    forwardAuthorization: null,
    verifyTls: null,
    connectTimeoutMs: null,
    urlEncoding: "",
    httpVersion: "",
    sendCookies: null,
    storeCookies: null,
    tls: null,
    deadlineMs: null,
    extra: "",
  },
  extra: "",
};

const node = { id: "req", kind: "request", parentId: null, name: "Orders", path: "req.json", fileKind: "http", readOnly: "", method: "GET" } as TreeNode;

async function renderEditor() {
  service.Request.mockImplementation(() => Promise.resolve(structuredClone(REQUEST)));
  const stores = createStores();
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes: [node], rootIds: ["req"] });
  render(
    <StoreProvider stores={stores}>
      <RequestEditor id="req" />
    </StoreProvider>,
  );
  await act(settle);
  return document.querySelector("[data-request-id]") as HTMLElement;
}

/** The sub-tab whose label is `label` in the pseudo-locale (a count may follow it). */
function tab(editor: HTMLElement, label: string): HTMLElement {
  const found = within(editor)
    .getAllByRole("tab")
    .find((el) => el.textContent?.startsWith(pseudoString(label)));
  if (found === undefined) throw new Error(`no sub-tab ${label}`);
  return found;
}

async function openTab(editor: HTMLElement, label: string) {
  fireEvent.click(tab(editor, label));
  await act(settle);
}

beforeEach(() => {
  resetFakeWails();
  service.Request.mockReset();
  service.UpdateRequest.mockReset().mockResolvedValue(undefined);
  service.Flush.mockReset().mockResolvedValue(undefined);
  service.InheritedAuth.mockReset().mockImplementation(() =>
    cancellable({ type: "bearer", disabled: false, folderId: "fld_users", folderName: "Users", auth: auth({ type: "bearer", token: "{{token}}" }) }),
  );
  service.InheritedSettings.mockReset().mockImplementation(() => cancellable({ folders: [] }));
  http.Variables.mockReset().mockImplementation(() => cancellable({ variables: [], environment: "" }));
  http.HeaderPreview.mockReset().mockImplementation(pending);
  http.NetrcFile.mockReset().mockImplementation(() => cancellable({ path: "/home/ann/.netrc", fallback: "", source: "home" }));
  http.OAuth2Token.mockReset().mockImplementation(() =>
    cancellable({
      available: true,
      key: "k1",
      grant: "client_credentials",
      status: "valid",
      token: "••••••wxyz",
      tokenType: "Bearer",
      scope: "orders:read",
      expiresAt: Date.UTC(2026, 8, 23, 13, 0),
      obtainedAt: Date.UTC(2026, 8, 23, 12, 0),
      refreshable: true,
      folderName: "",
      sendsIdToken: false,
      idToken: {
        token: "••••••sig0",
        algorithm: "RS256",
        issuer: "https://idp.test",
        subject: "user-42",
        audience: ["shop"],
        expiresAt: Date.UTC(2026, 8, 23, 13, 0),
        issuedAt: Date.UTC(2026, 8, 23, 12, 0),
        name: "",
        email: "",
        expired: false,
        error: "",
      },
    }),
  );
  setLocale(PSEUDO_LOCALE);
});

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
});

describe("the request editor in the pseudo-locale", () => {
  it("shows the URL bar and the sub-tabs from the catalogue", async () => {
    const editor = await renderEditor();
    expect(within(editor).getByRole("tablist", { name: pseudoString("Request sections") })).toBeTruthy();
    for (const label of ["Params", "Headers", "Body", "Auth", "Tests", "Settings", "Notes"]) expect(tab(editor, label)).toBeTruthy();
    expect(within(editor).getByRole("button", { name: pseudoString("Repeat…") })).toBeTruthy();
    expect(within(editor).getByRole("button", { name: pseudoString("Copy as cURL") })).toBeTruthy();
    expectNoEnglish(editor, 10);
  });

  it("shows the Auth tab from the catalogue, for several auth types", async () => {
    const editor = await renderEditor();
    await openTab(editor, "Auth");
    const section = () => editor.querySelector("[data-auth-type]") as HTMLElement;
    // Inherit: the note and the inherited fields.
    const inherited = t("editor.auth.inherited.label", { field: t("editor.auth.field.token") });
    expect(await within(editor).findByRole("group", { name: inherited })).toBeTruthy();
    expectNoEnglish(editor, 10);
    for (const type of ["bearer", "basic", "apikey", "jwt", "oauth1", "awsv4", "hawk", "netrc"]) {
      const radio = section().querySelector(`input[type="radio"][value="${type}"]`) as HTMLInputElement;
      fireEvent.click(radio);
      await act(settle);
      expect(section().dataset.authType).toBe(type);
      expectNoEnglish(editor, 10);
    }
  });

  it("shows the Extract tab from the catalogue (TEST-05)", async () => {
    const editor = await renderEditor();
    expect(tab(editor, "Extract")).toBeTruthy();
    await openTab(editor, "Extract");
    expect(within(editor).getByText(pseudoString("Add rule"))).toBeTruthy();
    expectNoEnglish(editor, 10);

    fireEvent.click(within(editor).getByRole("button", { name: pseudoString("Add rule") }));
    await act(settle);
    expect(within(editor).getByRole("table", { name: pseudoString("Extraction rules") })).toBeTruthy();
    expectNoEnglish(editor, 10);
  });

  it("shows the OAuth 2.0 fields and the stored token from the catalogue", async () => {
    const editor = await renderEditor();
    await openTab(editor, "Auth");
    const section = () => editor.querySelector("[data-auth-type]") as HTMLElement;
    fireEvent.click(section().querySelector('input[type="radio"][value="oauth2"]') as HTMLInputElement);
    await act(settle);
    await waitFor(() => expect(section().querySelector("[data-oauth2-token]")).not.toBeNull());
    expectNoEnglish(editor, 20);
    // The authorization code grant's fields and note.
    const grant = within(section()).getByRole("combobox", { name: pseudoString("OAuth 2.0 grant type") }) as HTMLSelectElement;
    fireEvent.change(grant, { target: { value: "authorization_code" } });
    await act(settle);
    expect(within(section()).getByRole("textbox", { name: pseudoString("Authorization URL") })).toBeTruthy();
    expectNoEnglish(editor, 20);
    // The password and implicit grants' fields and notes (AUTH-09, AUTH-10).
    for (const g of ["password", "implicit"]) {
      fireEvent.change(grant, { target: { value: g } });
      await act(settle);
      expectNoEnglish(editor, 20);
    }
    expect(section().querySelector("[data-oauth2-id-token]")).not.toBeNull();
  });

  it("shows the Settings tab from the catalogue", async () => {
    const editor = await renderEditor();
    await openTab(editor, "Settings");
    expect(within(editor).getByRole("region", { name: pseudoString("Redirects") })).toBeTruthy();
    expectNoEnglish(editor, 20);
  });
});
