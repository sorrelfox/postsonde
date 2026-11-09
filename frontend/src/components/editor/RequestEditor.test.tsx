import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { EditorView } from "@codemirror/view";
import { beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: { request: unknown; revision: number }) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
  InheritedAuth: () => Object.assign(Promise.resolve({ type: "none", disabled: false, folderId: "", folderName: "" }), { cancel: () => undefined }),
  InheritedSettings: vi.fn<(id: string) => unknown>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));
const http = vi.hoisted(() => ({
  Send: vi.fn<(input: { requestId: string }) => Promise<unknown>>(),
  Repeat: vi.fn<(input: { requestId: string; count: number; intervalMs: number }) => Promise<unknown>>(),
  Variables: vi.fn<(id: string) => Promise<unknown>>(),
  CurlCommand: vi.fn<(input: unknown) => Promise<unknown>>(),
  HeaderPreview: vi.fn<(input: { requestId: string }) => Promise<unknown>>(),
  Snippet: vi.fn<(input: unknown) => Promise<unknown>>(),
  SnippetLanguages: vi.fn<() => Promise<unknown>>(),
  Resolution: vi.fn<(id: string) => Promise<unknown>>(),
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));
const sse = vi.hoisted(() => ({
  Connect: vi.fn<(input: { requestId: string; allowUnresolved: boolean }) => Promise<unknown>>(),
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/sse", () => ({
  SSEService: sse,
}));
vi.mock(
  "../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../../test/fakeWails")).fakeOpsBindings,
);
const pickSchemaFile = vi.hoisted(() => vi.fn<() => unknown>());
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/workspace", async (importOriginal) => {
  const actual = await importOriginal<{ WorkspaceService: object }>();
  return { ...actual, WorkspaceService: { ...actual.WorkspaceService, PickSchemaFile: pickSchemaFile } };
});
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: {
    ValidateJSON: () => Object.assign(Promise.resolve({ valid: true, error: null }), { cancel: () => undefined }),
  },
}));

import type { AssertionDTO } from "../../lib/assertions";
import type { KeyValueDTO, RequestDTO } from "../../lib/collection";
import type { HeaderPreviewDTO, PreviewHeaderDTO } from "../../lib/http";
import { PREVIEW_DELAY_MS } from "./AutoHeaders";
import type { VariableDTO, VariablesDTO } from "../../lib/templateVariables";
import { requestShowResolution } from "../../lib/resolution";
import { resetHttpSends } from "../../commands/httpSends";
import { undoRequestEdit } from "../../commands/undoRedo";
import { jumpToAssertion } from "../../commands/assertionJump";
import { httpResponse } from "../../test/httpResponse";
import { resetSseStreams } from "../../commands/sseStreams";
import { shortcutBindings } from "../../commands/shortcuts";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { emit, fakeRuntime, opsCancel, resetFakeWails, settle } from "../../test/fakeWails";
import { TabBar } from "../tabs/TabBar";
import { TabPanel } from "../tabs/TabPanel";

/** A resolved call, as the bindings return it (with cancel). */
const cancellable = (dto: VariablesDTO) => Object.assign(Promise.resolve(dto), { cancel: () => undefined });

const row = (name: string, value: string, over: Partial<KeyValueDTO> = {}): KeyValueDTO => ({
  name,
  value,
  description: "",
  enabled: true,
  extra: "",
  ...over,
});

function request(over: Partial<RequestDTO> = {}): RequestDTO {
  return {
    id: "list",
    kind: "http",
    name: "List products",
    description: "",
    method: "GET",
    url: "{{baseUrl}}/v1/products?limit=20",
    params: [row("limit", "20", { description: "Page size", extra: '{"x-note":"kept"}' }), row("cursor", "", { enabled: false })],
    pathParams: null,
    headers: [row("Accept", "application/json")],
    body: { mode: "raw", type: "json", text: '{"a":1}', fields: null, parts: null, path: "", contentType: "", extra: '{"x-body":true}' },
    query: "",
    variables: "",
    operationName: "",
    connectionParams: "",
    schemaFile: "",
    address: "",
    sendMode: "",
    lineEnding: "",
    message: "",
    subprotocols: [],
    messageFormat: "",
    service: "",
    proto: { files: [], importPaths: [], inherit: false, extra: "" },
    metadata: [],
    bodyExamples: [],
    auth: { type: "inherit", username: "", password: "", prefix: null, token: "", key: "", value: "", in: "", disabled: false, localSecrets: [], extra: "" },
    settings: { timeoutMs: null, followRedirects: null, maxRedirects: null, followOriginalMethod: null, forwardAuthorization: null, verifyTls: null, connectTimeoutMs: null, urlEncoding: "", httpVersion: "", sendCookies: null, storeCookies: null, tls: null, deadlineMs: null, extra: "" },
    extra: '{"x-custom":{"a":1}}',
    ...over,
  };
}

const node = (over: Partial<TreeNode> & { id: string }): TreeNode =>
  ({
    kind: "request",
    parentId: null,
    name: over.id,
    path: `${over.id}.json`,
    fileKind: "http",
    readOnly: "",
    method: "GET",
    ...over,
  }) as TreeNode;

async function setup(
  opts: { dto?: RequestDTO; nodes?: TreeNode[]; open?: string[]; projectReadOnly?: string } = {},
): Promise<Stores> {
  const dto = opts.dto ?? request();
  service.Request.mockImplementation((id) =>
    id === dto.id ? Promise.resolve(structuredClone(dto)) : Promise.resolve(request({ id, name: id, url: `/${id}` })),
  );
  const stores = createStores();
  const nodes = opts.nodes ?? [node({ id: "list", name: "List products" }), node({ id: "other", name: "Other" })];
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({
    type: "collection/load",
    workspaceId: "ws_1",
    nodes,
    rootIds: nodes.map((n) => n.id),
    readOnly: opts.projectReadOnly ?? "",
  });
  const open = opts.open ?? ["list", "other"];
  stores.tabs.dispatch({ type: "tabs/restore", order: open, activeId: open[0] ?? null });
  render(
    <StoreProvider stores={stores}>
      <TabBar />
      <TabPanel />
    </StoreProvider>,
  );
  await act(settle);
  return stores;
}

const editor = () => document.querySelector("[data-request-id]") as HTMLElement;
const urlField = () => within(editor()).getByRole("textbox", { name: "URL" });
const urlView = () => {
  const view = EditorView.findFromDOM(urlField());
  if (view === null) throw new Error("the URL field is not a CodeMirror editor");
  return view;
};
const urlText = () => urlView().state.doc.toString();
/** Replaces the URL as typing would. */
const typeUrl = (url: string) =>
  act(() => {
    const view = urlView();
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: url }, userEvent: "input.type" });
  });
const methodField = () => within(editor()).getByRole("combobox", { name: "Method" }) as HTMLSelectElement;
/** The last request sent to UpdateRequest. */
const sent = () => {
  const calls = service.UpdateRequest.mock.calls;
  return calls[calls.length - 1]?.[0] as { request: RequestDTO; revision: number };
};

beforeEach(() => {
  resetFakeWails();
  resetHttpSends();
  resetSseStreams();
  http.Send.mockReset().mockResolvedValue({ opId: "op_send" });
  http.Repeat.mockReset().mockResolvedValue({ opId: "op_run" });
  sse.Connect.mockReset().mockResolvedValue({ opId: "op_sse" });
  http.Variables.mockReset().mockImplementation(() => cancellable({ variables: [], environment: "" }));
  // No preview unless a test gives one: the editor shows its own Content-Type.
  http.HeaderPreview.mockReset().mockImplementation(() => Object.assign(new Promise(() => undefined), { cancel: () => undefined }));
  service.Request.mockReset();
  service.UpdateRequest.mockReset().mockResolvedValue(undefined);
  service.Flush.mockReset().mockResolvedValue(undefined);
  service.InheritedSettings.mockReset().mockImplementation(() =>
    Object.assign(Promise.resolve({ folders: [] }), { cancel: () => undefined }),
  );
});

const STANDARD = ["GET", "POST", "PUT", "PATCH", "DELETE", "QUERY", "HEAD", "OPTIONS"];

describe("RequestEditor", () => {
  it("opens an HTTP request tab with the values of its file", async () => {
    await setup();
    expect(service.Request).toHaveBeenCalledWith("list");
    expect(methodField().value).toBe("GET");
    expect([...methodField().options].map((o) => o.textContent)).toEqual([...STANDARD, "Custom…"]);
    expect(urlText()).toBe("{{baseUrl}}/v1/products?limit=20");
    // No placeholders: no path table, no headings.
    expect(within(editor()).queryByRole("grid", { name: "Path variables" })).toBeNull();
    expect(within(editor()).queryByText("Query params")).toBeNull();

    const sections = within(editor()).getByRole("tablist", { name: "Request sections" });
    const tabs = within(sections).getAllByRole("tab");
    // Counts: enabled rows with a name.
    expect(tabs.map((t) => t.textContent)).toEqual(["Params1", "Headers1", "Body", "Auth", "Tests", "Extract", "Examples", "Settings", "Notes", "Variables", "Code"]);
    expect(tabs[0]?.getAttribute("aria-selected")).toBe("true");
    const params = within(editor()).getByRole("grid", { name: "Query params" });
    expect(within(params).getByDisplayValue("limit")).toBeTruthy();
    expect(within(params).getByDisplayValue("Page size")).toBeTruthy();

    expect(within(editor()).getByRole("button", { name: "Send" })).toBeTruthy();
    expect(service.UpdateRequest).not.toHaveBeenCalled();
  });

  it("auto-saves method and URL changes with the whole request, unknown members included", async () => {
    const stores = await setup();
    fireEvent.change(methodField(), { target: { value: "POST" } });
    expect(sent()).toEqual({ request: request({ method: "POST" }), revision: 1, baseVersion: 0 });
    // The badges follow at once.
    expect(stores.collection.getState().nodes.list).toMatchObject({ method: "POST" });
    expect(screen.getByRole("tab", { name: "POST List products" })).toBeTruthy();

    await act(settle);
    typeUrl("{{baseUrl}}/v1/items");
    await act(settle);
    const last = sent();
    expect(last.revision).toBe(2);
    // The query went, so the enabled limit row went with it; the disabled row stays.
    expect(last.request).toEqual(
      request({ method: "POST", url: "{{baseUrl}}/v1/items", params: [row("cursor", "", { enabled: false })] }),
    );
    expect(last.request.extra).toBe('{"x-custom":{"a":1}}');
    expect(last.request.body).toEqual(request().body);
    expect(urlText()).toBe("{{baseUrl}}/v1/items");
  });

  it("saves param and header row edits, keeping each row's unknown members", async () => {
    await setup();
    const params = within(editor()).getByRole("grid", { name: "Query params" });
    fireEvent.change(within(params).getByDisplayValue("20"), { target: { value: "50" } });
    expect(sent().request.params).toEqual([
      row("limit", "50", { description: "Page size", extra: '{"x-note":"kept"}' }),
      row("cursor", "", { enabled: false }),
    ]);

    // The first key cell of the empty row at the end adds a row.
    const keys = within(params).getAllByPlaceholderText("key");
    fireEvent.change(keys[keys.length - 1] as HTMLElement, { target: { value: "sort" } });
    await act(settle);
    expect(sent().request.params?.map((r) => [r.name, r.extra])).toEqual([
      ["limit", '{"x-note":"kept"}'],
      ["cursor", ""],
      ["sort", ""],
    ]);

    fireEvent.click(within(editor()).getByRole("tab", { name: /Headers/ }));
    const headers = within(editor()).getByRole("grid", { name: "Headers" });
    const toggles = within(headers).getAllByRole("checkbox");
    fireEvent.click(toggles[0] as HTMLElement);
    await act(settle);
    expect(sent().request.headers).toEqual([row("Accept", "application/json", { enabled: false })]);
    expect(sent().request.params?.length).toBe(3);
    expect(within(editor()).getByRole("tab", { name: /Headers/ }).textContent).toBe("Headers");
  });

  it("lists the headers its folders add, struck through when a row of the same name overrides them (COL-03)", async () => {
    service.InheritedSettings.mockImplementation(() =>
      Object.assign(
        Promise.resolve({
          folders: [
            { folderId: "fld_a", folderName: "Catalog", headers: [row("accept", "*/*"), row("X-Team", "core")], variables: [], unreadable: false },
          ],
        }),
        { cancel: () => undefined },
      ),
    );
    await setup();
    fireEvent.click(within(editor()).getByRole("tab", { name: /Headers/ }));
    await act(settle);
    expect(service.InheritedSettings).toHaveBeenCalledWith("list");
    const list = within(editor()).getByRole("list", { name: "Inherited headers" });
    const items = within(list).getAllByRole("listitem");
    expect(items.map((li) => li.hasAttribute("data-overridden"))).toEqual([true, false]);
    expect(items[1]?.textContent).toContain("Catalog");
  });

  describe("automatic headers (HTTP-09)", () => {
    const h = (name: string, value: string, source: string, extra: Partial<PreviewHeaderDTO> = {}): PreviewHeaderDTO => ({
      name,
      value,
      source,
      masked: false,
      reason: "",
      ...extra,
    });
    const preview = (over: Partial<HeaderPreviewDTO> = {}): HeaderPreviewDTO => ({
      headers: [
        h("Host", "api.test", "url"),
        h("Content-Length", "7", "body"),
        h("Accept", "application/json", "request"),
        h("Authorization", "Bearer ••••••", "auth", { masked: true }),
        h("Content-Type", "application/json", "body"),
        h("Cookie", "sid=abc", "cookies"),
      ],
      suppressed: [],
      digest: false,
      httpVersion: "http1",
      unresolved: [],
      ...over,
    });
    const give = (dto: HeaderPreviewDTO) =>
      http.HeaderPreview.mockImplementation(() => Object.assign(Promise.resolve(dto), { cancel: () => undefined }));
    const list = () => within(editor()).getByRole("list", { name: "Automatic headers" });
    const items = () => within(list()).getAllByRole("listitem");
    const openHeaders = async (dto?: RequestDTO) => {
      await setup(dto === undefined ? {} : { dto });
      fireEvent.click(within(editor()).getByRole("tab", { name: /Headers/ }));
      await act(settle);
    };

    it("lists the headers Go says a send adds, not the typed ones, and that no User-Agent is sent", async () => {
      give(preview());
      await openHeaders();
      expect(http.HeaderPreview).toHaveBeenCalledWith({ requestId: "list" });
      expect(items().map((li) => li.getAttribute("data-state"))).toEqual(["auto", "auto", "auto", "auto", "auto", "absent"]);
      const text = items().map((li) => li.textContent);
      expect(text[0]).toContain("Hostapi.test");
      expect(text[2]).toContain("AuthorizationBearer ••••••");
      expect(text[5]).toContain("User-Agentnot sent");
      expect(text.some((t) => t?.startsWith("Accept"))).toBe(false);
      expect((within(list()).getByRole("checkbox", { name: "Send Host" }) as HTMLInputElement).disabled).toBe(true);
      expect((within(list()).getByRole("checkbox", { name: "Send Content-Length" }) as HTMLInputElement).disabled).toBe(true);
      expect((within(list()).getByRole("checkbox", { name: "Send Content-Type" }) as HTMLInputElement).disabled).toBe(false);
      expect(items()[0]?.getAttribute("title")).toContain("Taken from the URL");
      expect(within(list()).queryByRole("button", { name: "Override Content-Length" })).toBeNull();
    });

    it("turns the body's Content-Type, the auth header and the jar's cookies off and on", async () => {
      give(preview());
      await openHeaders();
      fireEvent.click(within(list()).getByRole("checkbox", { name: "Send Content-Type" }));
      await act(settle);
      expect(sent().request.settings.omitHeaders).toEqual(["Content-Type"]);
      fireEvent.click(within(list()).getByRole("checkbox", { name: "Send Authorization" }));
      await act(settle);
      expect(sent().request.settings.omitHeaders).toEqual(["Content-Type", "Authorization"]);
      fireEvent.click(within(list()).getByRole("checkbox", { name: "Send Cookie" }));
      await act(settle);
      expect(sent().request.settings.sendCookies).toBe(false);
    });

    it("shows turned-off and overridden headers struck through, and turns one back on", async () => {
      const dto = request({ settings: { ...request().settings, omitHeaders: ["content-type"], sendCookies: false } });
      give(
        preview({
          headers: [h("Host", "api.test", "url")],
          suppressed: [
            h("Content-Type", "application/json", "body", { reason: "omitted" }),
            h("Authorization", "Bearer ••••••", "auth", { reason: "overridden", masked: true }),
            h("Cookie", "sid=abc", "cookies", { reason: "omitted" }),
          ],
        }),
      );
      await openHeaders(dto);
      expect(items().map((li) => li.getAttribute("data-state"))).toEqual(["auto", "omitted", "overridden", "omitted", "absent"]);
      expect(items()[1]?.querySelector("s")?.textContent).toBe("application/json");
      expect(items()[1]?.textContent).toContain("off");
      expect((within(list()).getByRole("checkbox", { name: "Send Authorization" }) as HTMLInputElement).disabled).toBe(true);
      fireEvent.click(within(list()).getByRole("checkbox", { name: "Send Content-Type" }));
      await act(settle);
      expect(sent().request.settings.omitHeaders).toBeNull();
      fireEvent.click(within(list()).getByRole("checkbox", { name: "Send Cookie" }));
      await act(settle);
      expect(sent().request.settings.sendCookies).toBeNull();
    });

    it("overrides an automatic header with a row of the same name, without a masked value", async () => {
      give(preview());
      await openHeaders();
      fireEvent.click(within(list()).getByRole("button", { name: "Override Host" }));
      fireEvent.click(within(list()).getByRole("button", { name: "Override Authorization" }));
      fireEvent.click(within(list()).getByRole("button", { name: "Add User-Agent" }));
      await act(settle);
      expect(sent().request.headers?.map((r) => [r.name, r.value, r.enabled])).toEqual([
        ["Accept", "application/json", true],
        ["Host", "api.test", true],
        ["Authorization", "", true],
        ["User-Agent", "", true],
      ]);
    });

    it("asks again after an edit, and keeps the local Content-Type while there is no answer", async () => {
      await openHeaders();
      expect(items()).toHaveLength(1);
      expect(items()[0]?.textContent).toContain("Content-Typeapplication/json");
      give(preview({ headers: [h("Host", "api.test", "url"), h("X-New", "1", "client")] }));
      const headers = within(editor()).getByRole("grid", { name: "Headers" });
      fireEvent.click(within(headers).getAllByRole("checkbox")[0] as HTMLElement);
      await act(async () => {
        await new Promise((r) => setTimeout(r, PREVIEW_DELAY_MS + 50));
        await settle();
      });
      expect(http.HeaderPreview).toHaveBeenCalledTimes(2);
      expect(items().map((li) => li.textContent?.slice(0, 5))).toEqual(["Hosta", "X-New", "User-"]);
    });

    it("says why when Go cannot compute them", async () => {
      http.HeaderPreview.mockImplementation(() =>
        Object.assign(Promise.reject(new Error("The URL is not valid.")), { cancel: () => undefined }),
      );
      await openHeaders();
      expect(within(editor()).getByRole("note").textContent).toContain("The URL is not valid.");
    });
  });

  describe("implicit Content-Type", () => {
    const implicit = () => within(editor()).queryByRole("list", { name: "Automatic headers" });
    const body = (over: Partial<RequestDTO["body"]>): RequestDTO["body"] => ({
      mode: "raw",
      type: "json",
      text: "",
      fields: null,
      parts: null,
      path: "",
      contentType: "",
      extra: "",
      ...over,
    });
    const openHeaders = async (dto: RequestDTO) => {
      await setup({ dto });
      fireEvent.click(within(editor()).getByRole("tab", { name: /Headers/ }));
    };

    it.each([
      [body({ mode: "raw", type: "json" }), "application/json"],
      [body({ mode: "raw", type: "xml" }), "application/xml"],
      [body({ mode: "raw", type: "yaml" }), "application/yaml"],
      [body({ mode: "urlencoded", type: "" }), "application/x-www-form-urlencoded"],
      [body({ mode: "multipart", type: "" }), "multipart/form-data; boundary=…"],
      [body({ mode: "file", type: "" }), "application/octet-stream"],
      [body({ mode: "file", type: "", path: "fixtures/avatar.PNG" }), "image/png"],
      [body({ mode: "file", type: "", path: "a.png", contentType: "image/x-mine" }), "image/x-mine"],
    ])("shows the type of a %o body, read-only and not counted", async (b, want) => {
      await openHeaders(request({ body: b }));
      const list = implicit();
      expect(list).not.toBeNull();
      const item = within(list as HTMLElement).getByRole("listitem");
      expect(item.textContent).toContain("auto");
      expect(item.textContent).toContain(`Content-Type${want}`);
      expect(item.getAttribute("title")).toBe("Set from the body type; add a Content-Type header to override");
      expect(within(list as HTMLElement).queryByRole("textbox")).toBeNull();
      expect(within(editor()).getByRole("tab", { name: /Headers/ }).textContent).toBe("Headers1");
    });

    it.each([body({ mode: "none", type: "" }), body({ mode: "raw", type: "csv" }), body({ mode: "graphql2", type: "" })])(
      "shows nothing for a %o body",
      async (b) => {
        await openHeaders(request({ body: b }));
        expect(implicit()).toBeNull();
      },
    );

    it("marks it overridden by an enabled Content-Type row, and follows edits", async () => {
      await openHeaders(request({ headers: [row("content-type", "text/plain")] }));
      const item = () => within(implicit() as HTMLElement).getByRole("listitem");
      expect(item().textContent).toContain("overridden");
      expect(item().querySelector("s")?.textContent).toBe("application/json");

      const headers = within(editor()).getByRole("grid", { name: "Headers" });
      fireEvent.click(within(headers).getAllByRole("checkbox")[0] as HTMLElement);
      await act(settle);
      expect(item().textContent).toContain("auto");
      expect(item().querySelector("s")).toBeNull();

      fireEvent.click(within(editor()).getByRole("tab", { name: "Body" }));
      fireEvent.click(within(editor()).getByRole("radio", { name: "Form URL-encoded" }));
      await act(settle);
      fireEvent.click(within(editor()).getByRole("tab", { name: /Headers/ }));
      expect(item().textContent).toContain("application/x-www-form-urlencoded");
    });
  });

  it("suggests header names and values and saves the accepted ones", async () => {
    await setup();
    fireEvent.click(within(editor()).getByRole("tab", { name: /Headers/ }));
    const headers = within(editor()).getByRole("grid", { name: "Headers" });
    const newName = within(headers).getByRole("combobox", { name: "Key, row 2" }) as HTMLInputElement;
    newName.focus();
    fireEvent.change(newName, { target: { value: "content-t" } });
    const names = screen.getByRole("listbox", { name: "Header names" });
    const first = within(names).getAllByRole("option")[0] as HTMLElement;
    expect(first.textContent).toContain("Content-Type");
    // Headers the client sets itself carry a hint.
    expect(first.querySelector("[title]")?.getAttribute("title")).toMatch(/body type/);
    fireEvent.keyDown(newName, { key: "Enter" });
    await act(settle);
    expect(sent().request.headers).toEqual([row("Accept", "application/json"), row("Content-Type", "")]);

    // Values depend on the row's header.
    const value = within(headers).getByRole("combobox", { name: "Value, row 2" }) as HTMLInputElement;
    value.focus();
    fireEvent.change(value, { target: { value: "xml" } });
    const values = screen.getByRole("listbox", { name: "Header values" });
    expect(within(values).getAllByRole("option").map((o) => o.firstElementChild?.textContent)).toEqual(["application/xml", "text/xml", "image/svg+xml"]);
    fireEvent.keyDown(value, { key: "ArrowDown" });
    fireEvent.keyDown(value, { key: "Enter" });
    await act(settle);
    expect(sent().request.headers).toEqual([row("Accept", "application/json"), row("Content-Type", "text/xml")]);

    // Accept is a list: only the item being typed is completed.
    const accept = within(headers).getByRole("combobox", { name: "Value, row 1" }) as HTMLInputElement;
    accept.focus();
    fireEvent.change(accept, { target: { value: "application/json, text/pl" } });
    fireEvent.keyDown(accept, { key: "Enter" });
    await act(settle);
    expect(sent().request.headers?.[0]).toEqual(row("Accept", "application/json, text/plain"));
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it.each(STANDARD)("picks the standard method %s", async (method) => {
    const stores = await setup({ dto: request({ method: method === "GET" ? "POST" : "GET" }) });
    fireEvent.change(methodField(), { target: { value: method } });
    await act(settle);
    expect(sent().request.method).toBe(method);
    expect(methodField().value).toBe(method);
    expect(stores.collection.getState().nodes.list).toMatchObject({ method });
    const badge = { GET: "GET", POST: "POST", PUT: "PUT", PATCH: "PATCH", DELETE: "DEL", QUERY: "QUERY", HEAD: "HEAD", OPTIONS: "OPT" }[method];
    expect(screen.getByRole("tab", { name: `${badge} List products` })).toBeTruthy();
  });

  it("keeps a method it does not offer until another is picked", async () => {
    const stores = await setup({ dto: request({ method: "PROPFIND" }), nodes: [node({ id: "list", method: "PROPFIND" })], open: ["list"] });
    expect(methodField().value).toBe("PROPFIND");
    expect([...methodField().options].map((o) => o.value)).toEqual([...STANDARD, "PROPFIND", "custom…"]);
    expect(screen.getByRole("tab", { name: "PROPF list" })).toBeTruthy();
    expect(service.UpdateRequest).not.toHaveBeenCalled();

    fireEvent.change(methodField(), { target: { value: "GET" } });
    expect(sent().request.method).toBe("GET");
    // Still offered, so the change can be undone.
    expect([...methodField().options].map((o) => o.value)).toEqual([...STANDARD, "PROPFIND", "custom…"]);
    await act(settle);
    fireEvent.change(methodField(), { target: { value: "PROPFIND" } });
    await act(settle);
    expect(sent().request.method).toBe("PROPFIND");
    expect(stores.collection.getState().nodes.list).toMatchObject({ method: "PROPFIND" });
  });

  describe("custom methods", () => {
    const customField = () => within(editor()).getByRole("textbox", { name: "Custom method" }) as HTMLInputElement;
    const openCustom = () => fireEvent.change(methodField(), { target: { value: "custom…" } });

    it("turns the picker into a text field and saves a valid token as typed", async () => {
      const stores = await setup();
      openCustom();
      expect(document.activeElement).toBe(customField());
      expect(customField().value).toBe("");
      fireEvent.change(customField(), { target: { value: "PURGE" } });
      fireEvent.keyDown(customField(), { key: "Enter" });
      await act(settle);
      expect(sent().request.method).toBe("PURGE");
      expect(stores.collection.getState().nodes.list).toMatchObject({ method: "PURGE" });
      expect(document.activeElement).toBe(methodField());
      expect(methodField().value).toBe("PURGE");
      expect(screen.getByRole("tab", { name: "PURGE List products" })).toBeTruthy();

      // Editing a custom method starts from it.
      openCustom();
      expect(customField().value).toBe("PURGE");
    });

    it("rejects what is not a method token and cancels with Escape", async () => {
      await setup();
      openCustom();
      fireEvent.change(customField(), { target: { value: "MY METHOD" } });
      expect(customField().getAttribute("aria-invalid")).toBe("true");
      const described = document.getElementById(customField().getAttribute("aria-describedby") ?? "");
      expect(described?.textContent).toMatch(/letters, digits/);
      fireEvent.keyDown(customField(), { key: "Enter" });
      // Still editing, nothing saved.
      expect(customField().value).toBe("MY METHOD");
      fireEvent.change(customField(), { target: { value: "CONNECT" } });
      fireEvent.keyDown(customField(), { key: "Enter" });
      expect(customField()).toBeTruthy();
      fireEvent.keyDown(customField(), { key: "Escape" });
      expect(methodField().value).toBe("GET");
      expect(service.UpdateRequest).not.toHaveBeenCalled();
    });

    it("saves on blur when valid, and drops an invalid text", async () => {
      await setup();
      openCustom();
      fireEvent.change(customField(), { target: { value: "a b" } });
      fireEvent.blur(customField());
      expect(methodField().value).toBe("GET");
      expect(service.UpdateRequest).not.toHaveBeenCalled();

      openCustom();
      fireEvent.change(customField(), { target: { value: "M-SEARCH" } });
      fireEvent.blur(customField());
      await act(settle);
      expect(sent().request.method).toBe("M-SEARCH");
      expect(service.UpdateRequest).toHaveBeenCalledTimes(1);
    });

    it("keeps the case typed and warns that methods are case-sensitive", async () => {
      await setup();
      openCustom();
      fireEvent.change(customField(), { target: { value: "get" } });
      expect(customField().getAttribute("aria-invalid")).toBe("false");
      fireEvent.keyDown(customField(), { key: "Enter" });
      await act(settle);
      expect(sent().request.method).toBe("get");
      const described = document.getElementById(methodField().getAttribute("aria-describedby") ?? "");
      expect(described?.textContent).toBe("Methods are case-sensitive: get is sent as typed, not as GET, and servers may reject it.");

      fireEvent.change(methodField(), { target: { value: "GET" } });
      expect(methodField().getAttribute("aria-describedby")).toBeNull();
    });

    it("offers no custom entry when read-only", async () => {
      await setup({ nodes: [node({ id: "list", readOnly: "newer_version" })], open: ["list"] });
      expect([...methodField().options].map((o) => o.value)).toEqual(STANDARD);
    });
  });

  describe("path variables", () => {
    const pathGrid = () => within(editor()).queryByRole("grid", { name: "Path variables" });
    const pathKeys = () =>
      (within(pathGrid() as HTMLElement).getAllByRole("textbox", { name: /^Key/ }) as HTMLInputElement[]).map((el) => el.value);

    it("lists the URL's placeholders in a table whose names follow the URL", async () => {
      await setup({ dto: request({ url: "{{baseUrl}}/orgs/:org/users/{id}?limit=20", pathParams: [row("id", "42", { description: "User" })] }) });
      expect(pathGrid()).not.toBeNull();
      expect(within(editor()).getByText("Path variables", { selector: "h3" })).toBeTruthy();
      expect(pathKeys()).toEqual(["org", "id"]);
      const grid = pathGrid() as HTMLElement;
      // Names are fixed: read-only keys, no toggles, no delete buttons, no empty row.
      expect(within(grid).getAllByRole("textbox", { name: /^Key/ }).every((el) => (el as HTMLInputElement).readOnly)).toBe(true);
      expect(within(grid).queryAllByRole("checkbox")).toHaveLength(0);
      expect(within(grid).queryAllByRole("button")).toHaveLength(0);
      expect(within(grid).getByDisplayValue("42")).toBeTruthy();
      // Params counts the query row and both path variables.
      expect(within(editor()).getByRole("tab", { name: /Params/ }).textContent).toBe("Params3");
      // Showing the table saves nothing.
      expect(service.UpdateRequest).not.toHaveBeenCalled();
    });

    it("saves value edits with rows for every placeholder", async () => {
      await setup({ dto: request({ url: "/orgs/:org/users/{id}", params: [], pathParams: [row("id", "42", { extra: '{"x":1}' })] }) });
      const grid = pathGrid() as HTMLElement;
      fireEvent.change(within(grid).getByRole("textbox", { name: "Value, row 1" }), { target: { value: "acme" } });
      await act(settle);
      expect(sent().request).toEqual(
        request({ url: "/orgs/:org/users/{id}", params: [], pathParams: [row("org", "acme"), row("id", "42", { extra: '{"x":1}' })] }),
      );
    });

    it("adds and removes rows as the URL changes, keeping removed values while the tab is open", async () => {
      const stores = await setup({ dto: request({ url: "/users/:id", params: [], pathParams: [row("id", "42", { description: "User" })] }) });
      typeUrl("/users/:id/posts/{post}");
      await act(settle);
      expect(pathKeys()).toEqual(["id", "post"]);
      expect(sent().request.pathParams).toEqual([row("id", "42", { description: "User" }), row("post", "")]);

      typeUrl("/users");
      await act(settle);
      expect(pathGrid()).toBeNull();
      expect(sent().request.pathParams).toEqual([]);
      expect(sent().request.url).toBe("/users");

      // Typed again: the value comes back.
      typeUrl("/users/{id}");
      await act(settle);
      expect(pathKeys()).toEqual(["id"]);
      expect(sent().request.pathParams).toEqual([row("id", "42", { description: "User" })]);

      // Query and fragment text is not a placeholder.
      typeUrl("/users/{id}?q=:x#/:y");
      await act(settle);
      expect(pathKeys()).toEqual(["id"]);
      expect(sent().request.params?.map((r) => r.name)).toEqual(["q"]);

      // Closing the tab drops the kept values.
      act(() => stores.drafts.dispatch({ type: "drafts/discard", ids: ["list"] }));
      expect(stores.drafts.getState().pathParamStash).toEqual({});
    });

    it("highlights placeholders in the URL field", async () => {
      await setup({ dto: request({ url: "http://localhost:8080/users/:id/{{v}}/{post}", params: [] }) });
      const marks = [...urlField().querySelectorAll(".cm-pathParam")].map((el) => el.textContent);
      expect(marks).toEqual([":id", "{post}"]);
      typeUrl("http://localhost:8080/:org");
      expect([...urlField().querySelectorAll(".cm-pathParam")].map((el) => el.textContent)).toEqual([":org"]);
    });

    it("is read-only in a read-only request", async () => {
      await setup({ dto: request({ url: "/users/:id", params: [] }), nodes: [node({ id: "list", readOnly: "symlink" })], open: ["list"] });
      const value = within(pathGrid() as HTMLElement).getByRole("textbox", { name: "Value, row 1" }) as HTMLInputElement;
      expect(value.readOnly).toBe(true);
    });
  });

  it("copies the request as cURL after its edits reached Go, from the button and the shortcut (GEN-01)", async () => {
    http.CurlCommand.mockReset().mockResolvedValue({ command: "curl https://api.test/edited", masked: false, unresolved: [] });
    await setup();
    typeUrl("https://api.test/edited");
    fireEvent.click(within(editor()).getByRole("button", { name: "Copy as cURL" }));
    await act(settle);
    expect(service.UpdateRequest).toHaveBeenCalled();
    expect(service.UpdateRequest.mock.invocationCallOrder[0]).toBeLessThan(http.CurlCommand.mock.invocationCallOrder[0] ?? 0);
    expect(http.CurlCommand).toHaveBeenCalledWith({ requestId: "list", includeSecrets: false });
    expect(fakeRuntime.Clipboard.SetText).toHaveBeenCalledWith("curl https://api.test/edited");

    act(() => {
      shortcutBindings.get("request.copyCurl")?.(new KeyboardEvent("keydown"));
    });
    await act(settle);
    expect(http.CurlCommand).toHaveBeenCalledTimes(2);
  });

  it("shows the body editor without touching the body", async () => {
    await setup();
    fireEvent.click(within(editor()).getByRole("tab", { name: "Body" }));
    const types = within(editor()).getByRole("radiogroup", { name: "Body type" });
    expect((within(types).getByRole("radio", { name: "Raw" }) as HTMLInputElement).checked).toBe(true);
    expect((within(editor()).getByRole("combobox", { name: "Raw body language" }) as HTMLSelectElement).value).toBe("json");
    expect(within(editor()).getByRole("textbox", { name: "Request body" }).textContent).toBe('{"a":1}');
    expect(service.UpdateRequest).not.toHaveBeenCalled();
  });

  it("remembers the sub-tab and the draft per tab, without loading again", async () => {
    const stores = await setup();
    fireEvent.click(within(editor()).getByRole("tab", { name: /Headers/ }));
    typeUrl("/edited");

    act(() => stores.tabs.dispatch({ type: "tabs/activate", id: "other" }));
    await act(settle);
    expect(urlText()).toBe("/other");
    expect(within(editor()).getByRole("tab", { name: /Params/ }).getAttribute("aria-selected")).toBe("true");

    act(() => stores.tabs.dispatch({ type: "tabs/activate", id: "list" }));
    await act(settle);
    expect(urlText()).toBe("/edited");
    expect(within(editor()).getByRole("tab", { name: /Headers/ }).getAttribute("aria-selected")).toBe("true");
    expect(service.Request.mock.calls.map((c) => c[0])).toEqual(["list", "other"]);
    expect(stores.drafts.getState().subTabs).toEqual({ list: "headers" });
  });

  it("opens the Variables sub-tab from a chip's hover, on the name (DBG-08)", async () => {
    const resolution = {
      environment: "",
      url: "https://h.test/v1/products?limit=20",
      variables: [
        {
          name: "baseUrl", kind: "variable", resolved: true, value: "https://h.test", secret: false, generated: false,
          scope: "Global", source: "global", count: 1,
          scopes: [{ scope: "Global", source: "global", value: "https://h.test", secret: false, wins: true }],
        },
      ],
    };
    http.Resolution.mockReset().mockImplementation(() => Object.assign(Promise.resolve(resolution), { cancel: () => undefined }));
    await setup();
    act(() => requestShowResolution(urlView().dom, "baseUrl"));
    await act(settle);
    expect(within(editor()).getByRole("tab", { name: "Variables" }).getAttribute("aria-selected")).toBe("true");
    const row = within(editor()).getByRole("table", { name: "Variables of this request" }).querySelector('[data-name="baseUrl"]');
    expect(row?.hasAttribute("data-focused")).toBe(true);
    expect(http.Resolution).toHaveBeenCalledWith("list");
  });

  it("moves between sub-tabs with the arrow keys", async () => {
    await setup();
    const params = within(editor()).getByRole("tab", { name: /Params/ });
    act(() => params.focus());
    fireEvent.keyDown(params, { key: "ArrowLeft" });
    const code = within(editor()).getByRole("tab", { name: "Code" });
    expect(code.getAttribute("aria-selected")).toBe("true");
    expect(document.activeElement).toBe(code);
    fireEvent.keyDown(code, { key: "ArrowLeft" });
    const variablesTab = within(editor()).getByRole("tab", { name: "Variables" });
    expect(document.activeElement).toBe(variablesTab);
    fireEvent.keyDown(variablesTab, { key: "ArrowLeft" });
    const notes = within(editor()).getByRole("tab", { name: "Notes" });
    expect(notes.getAttribute("aria-selected")).toBe("true");
    expect(document.activeElement).toBe(notes);
    fireEvent.keyDown(notes, { key: "ArrowLeft" });
    const settings = within(editor()).getByRole("tab", { name: "Settings" });
    expect(settings.getAttribute("aria-selected")).toBe("true");
    expect(document.activeElement).toBe(settings);
    fireEvent.keyDown(settings, { key: "ArrowLeft" });
    const examplesTab = within(editor()).getByRole("tab", { name: "Examples" });
    expect(document.activeElement).toBe(examplesTab);
    fireEvent.keyDown(examplesTab, { key: "ArrowLeft" });
    const extractTab = within(editor()).getByRole("tab", { name: "Extract" });
    expect(document.activeElement).toBe(extractTab);
    fireEvent.keyDown(extractTab, { key: "ArrowLeft" });
    const testsTab = within(editor()).getByRole("tab", { name: "Tests" });
    expect(document.activeElement).toBe(testsTab);
    fireEvent.keyDown(testsTab, { key: "ArrowLeft" });
    const auth = within(editor()).getByRole("tab", { name: "Auth" });
    expect(document.activeElement).toBe(auth);
    fireEvent.keyDown(auth, { key: "ArrowLeft" });
    const body = within(editor()).getByRole("tab", { name: "Body" });
    expect(body.getAttribute("aria-selected")).toBe("true");
    expect(document.activeElement).toBe(body);
    fireEvent.keyDown(body, { key: "Home" });
    expect(document.activeElement).toBe(params);
  });

  it("shows the request as code in the Code tab, masked, after the queued edits reached Go (GEN-12)", async () => {
    http.SnippetLanguages.mockReset().mockResolvedValue([{ id: "curl", name: "cURL", syntax: "shell" }]);
    http.Snippet.mockReset().mockImplementation(() =>
      Object.assign(
        Promise.resolve({ language: "curl", mode: "resolved", code: "curl https://api.test/v1/products", masked: true, revealed: false, unresolved: [] }),
        { cancel: () => undefined },
      ),
    );
    const stores = await setup();
    fireEvent.click(within(editor()).getByRole("tab", { name: "Code" }));
    await act(settle);
    expect(http.Snippet).toHaveBeenCalledWith({ requestId: "list", language: "curl", mode: "resolved", revealSecrets: false });
    expect(within(editor()).getByLabelText("cURL code").textContent).toBe("curl https://api.test/v1/products");
    // The sub-tab is UI state, never saved; the reveal is not state of the draft at all.
    expect(stores.drafts.getState().subTabs).toEqual({ list: "code" });
  });

  it("edits the request's Markdown notes in the Notes tab and saves them as its description (COL-07)", async () => {
    const imported = "Imported from Postman.\n\n```js\npm.test(\"ok\", () => {});\n```\n";
    await setup({ dto: request({ description: imported }) });
    fireEvent.click(within(editor()).getByRole("tab", { name: "Notes" }));
    const panel = editor().querySelector("[data-notes-mode]") as HTMLElement;
    expect(panel.getAttribute("data-notes-mode")).toBe("preview");
    expect(panel.querySelector("pre code")?.textContent).toBe('pm.test("ok", () => {});');

    fireEvent.click(within(panel).getByRole("button", { name: "Edit" }));
    const view = EditorView.findFromDOM(within(editor()).getByRole("textbox", { name: "Notes" })) as EditorView;
    expect(view.state.doc.toString()).toBe(imported);
    act(() => view.dispatch({ changes: { from: 0, insert: "# Users\n\n" }, userEvent: "input.type" }));
    await act(settle);
    expect(sent().request).toEqual(request({ description: `# Users\n\n${imported}` }));
  });

  it("is read-only, with a notice, for a read-only request or project", async () => {
    await setup({ nodes: [node({ id: "list", readOnly: "newer_version" })], open: ["list"] });
    expect(within(editor()).getByRole("note").textContent).toContain("newer version of PostSonde");
    expect(methodField().disabled).toBe(true);
    expect(urlView().state.readOnly).toBe(true);
    typeUrl("/x");
    expect(urlText()).toBe("{{baseUrl}}/v1/products?limit=20");
    const params = within(editor()).getByRole("grid", { name: "Query params" });
    // Only the two file rows (no empty row to add one), none editable.
    const keyCells = within(params).getAllByPlaceholderText("key") as HTMLInputElement[];
    expect(keyCells.map((el) => [el.value, el.readOnly])).toEqual([
      ["limit", true],
      ["cursor", true],
    ]);
    expect(within(params).queryAllByRole("button", { name: /Delete/ })).toHaveLength(0);
    expect(service.UpdateRequest).not.toHaveBeenCalled();
  });

  it("is read-only in a read-only project", async () => {
    await setup({ projectReadOnly: "newer_version", open: ["list"] });
    expect(within(editor()).getByRole("note").textContent).toContain("This project was saved by a newer version");
    expect(urlView().state.readOnly).toBe(true);
  });

  it("shows a load error with a retry", async () => {
    service.Request.mockRejectedValueOnce({ kind: "not_found", code: "collection.not_found", message: "The request is no longer in the project.", hint: "", details: {}, causes: [] });
    const stores = createStores();
    stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
    stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes: [node({ id: "list" })], rootIds: ["list"] });
    stores.tabs.dispatch({ type: "tabs/restore", order: ["list"], activeId: "list" });
    render(
      <StoreProvider stores={stores}>
        <TabPanel />
      </StoreProvider>,
    );
    await act(settle);
    expect(screen.getByRole("tabpanel").textContent).toContain("The request is no longer in the project.");
    service.Request.mockResolvedValueOnce(request());
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await act(settle);
    expect(urlText()).toBe("{{baseUrl}}/v1/products?limit=20");
  });
});

describe("RequestEditor URL and params sync", () => {
  const paramsGrid = () => within(editor()).getByRole("grid", { name: "Query params" });
  const cell = (label: string) => within(paramsGrid()).getByRole("textbox", { name: label }) as HTMLInputElement;
  /** The Params table as name=value strings, "!" before disabled rows, "# description" after. */
  const tableRows = () => {
    const keys = within(paramsGrid()).getAllByPlaceholderText("key") as HTMLInputElement[];
    const values = within(paramsGrid()).getAllByPlaceholderText("value") as HTMLInputElement[];
    const descriptions = within(paramsGrid()).getAllByPlaceholderText("description") as HTMLInputElement[];
    const toggles = within(paramsGrid()).queryAllByRole("checkbox") as HTMLInputElement[];
    return toggles.map((t, i) => {
      const desc = descriptions[i]?.value ? ` # ${descriptions[i]?.value}` : "";
      return `${t.checked ? "" : "!"}${keys[i]?.value}=${values[i]?.value}${desc}`;
    });
  };
  const calls = () => service.UpdateRequest.mock.calls.length;

  it("updates the rows when the URL is edited, keeping descriptions, unknown members and disabled rows", async () => {
    const s = await setup();
    const stores = () => s;
    typeUrl("{{baseUrl}}/v1/products?limit=50&q=a%20b+c&flag#top");
    await act(settle);
    expect(calls()).toBe(1);
    expect(sent().request.url).toBe("{{baseUrl}}/v1/products?limit=50&q=a%20b+c&flag#top");
    // New entries go right after the row before them, ahead of the disabled row.
    expect(sent().request.params).toEqual([
      row("limit", "50", { description: "Page size", extra: '{"x-note":"kept"}' }),
      row("q", "a%20b+c"),
      row("flag", ""),
      row("cursor", "", { enabled: false }),
    ]);
    expect(tableRows()).toEqual(["limit=50 # Page size", "q=a%20b+c", "flag=", "!cursor="]);
    expect(within(editor()).getByRole("tab", { name: /Params/ }).textContent).toBe("Params3");

    // Removing an entry deletes its row only.
    typeUrl("{{baseUrl}}/v1/products?q=a%20b+c&flag#top");
    await act(settle);
    expect(calls()).toBe(2);
    expect(tableRows()).toEqual(["q=a%20b+c", "flag=", "!cursor="]);
    // A path-only change leaves the rows as they are.
    const rowsBefore = stores().drafts.getState().byId.list?.request.params;
    typeUrl("{{baseUrl}}/v2/products?q=a%20b+c&flag#top");
    await act(settle);
    expect(calls()).toBe(3);
    expect(stores().drafts.getState().byId.list?.request.params).toBe(rowsBefore);
  });

  it("splits a pasted URL into rows, dropping line breaks and outer spaces", async () => {
    await setup({ dto: request({ url: "", params: [] }), open: ["list"] });
    const view = urlView();
    const data = "  https://api.example.com/v1/search?\n  q={{term}}&page=2&sort=a%26b \n";
    act(() => {
      fireEvent.paste(view.contentDOM, { clipboardData: { getData: (t: string) => (t === "text/plain" ? data : "") } });
    });
    await act(settle);
    expect(urlText()).toBe("https://api.example.com/v1/search?q={{term}}&page=2&sort=a%26b");
    expect(calls()).toBe(1);
    expect(sent().request.params).toEqual([row("q", "{{term}}"), row("page", "2"), row("sort", "a%26b")]);
    expect(tableRows()).toEqual(["q={{term}}", "page=2", "sort=a%26b"]);
  });

  it("does not insert a line break on Enter", async () => {
    await setup();
    const view = urlView();
    act(() => view.dispatch({ selection: { anchor: view.state.doc.length } }));
    fireEvent.keyDown(view.contentDOM, { key: "Enter", keyCode: 13 });
    fireEvent.keyDown(view.contentDOM, { key: "Enter", keyCode: 13, shiftKey: true });
    // A line break typed some other way is dropped too.
    act(() => view.dispatch({ changes: { from: 9, insert: "\n" }, userEvent: "input.type" }));
    expect(urlText()).toBe("{{baseUrl}}/v1/products?limit=20");
    expect(view.state.doc.lines).toBe(1);
    expect(service.UpdateRequest).not.toHaveBeenCalled();
  });

  it("highlights {{variables}} in the URL", async () => {
    await setup({ dto: request({ url: "{{baseUrl}}/users/{{ id }}?x=\\{{no}}" }), open: ["list"] });
    const marked = [...urlField().querySelectorAll(".cm-templateVariable")].map((el) => el.textContent);
    expect(marked).toEqual(["{{baseUrl}}", "{{ id }}"]);
  });

  it("marks unresolved {{variables}} red and reloads them when the environments change (ENV-11)", async () => {
    const variable = (name: string, over: Partial<VariableDTO> = {}): VariableDTO => ({
      name,
      value: `${name}-value`,
      secret: false,
      resolved: true,
      scope: "Environment: dev",
      source: "environment",
      ...over,
    });
    http.Variables.mockImplementation(() => cancellable({ variables: [variable("baseUrl")], environment: "dev" }));
    await setup({
      dto: request({
        url: "{{baseUrl}}/{{missing}}",
        headers: [row("X-Token", "{{token}}")],
        body: { mode: "raw", type: "json", text: '{"a": "{{baseUrl}}", "b": "{{missing}}"}', fields: null, parts: null, path: "", contentType: "", extra: "" },
      }),
      open: ["list"],
    });
    expect(http.Variables).toHaveBeenCalledWith("list");
    const unresolvedInUrl = () => [...urlField().querySelectorAll("[data-unresolved]")].map((el) => el.textContent);
    expect(unresolvedInUrl()).toEqual(["{{missing}}"]);

    const headers = () => within(editor()).getByRole("grid", { name: "Headers" });
    const value = () => within(headers()).getByLabelText("Value, row 1");
    const bodyChips = () =>
      [...within(editor()).getByRole("textbox", { name: /body/i }).querySelectorAll(".cm-templateVariable")].map((el) => [
        el.textContent,
        el.hasAttribute("data-unresolved"),
      ]);
    fireEvent.click(within(editor()).getByRole("tab", { name: /Headers/ }));
    expect(headers().querySelector("[data-unresolved]")?.textContent).toBe("{{token}}");
    expect(value().getAttribute("title")).toContain("{{token}}: Not defined in dev");

    // The body's code editor draws chips too.
    fireEvent.click(within(editor()).getByRole("tab", { name: /Body/ }));
    await act(settle);
    expect(bodyChips()).toEqual([
      ["{{baseUrl}}", false],
      ["{{missing}}", true],
    ]);
    fireEvent.click(within(editor()).getByRole("tab", { name: /Headers/ }));

    // Another environment defines them: nothing is red any more.
    http.Variables.mockImplementation(() =>
      cancellable({
        variables: [variable("baseUrl"), variable("missing"), variable("token", { value: "", secret: true })],
        environment: "prod",
      }),
    );
    act(() => emit("environments:changed", { activeId: "env_prod" }));
    await act(settle);
    expect(http.Variables).toHaveBeenCalledTimes(2);
    expect(headers().querySelector("[data-unresolved]")).toBeNull();
    expect(value().getAttribute("title")).toBe("{{token}} = •••••• (Environment: dev)");
    expect(unresolvedInUrl()).toEqual([]);
    // The body editor's kept state gets the new set when it comes back.
    fireEvent.click(within(editor()).getByRole("tab", { name: /Body/ }));
    await act(settle);
    expect(bodyChips()).toEqual([
      ["{{baseUrl}}", false],
      ["{{missing}}", false],
    ]);
  });

  it("rewrites the URL when rows are edited, toggled or deleted, with one save per edit", async () => {
    await setup({
      dto: request({
        url: "{{baseUrl}}/v1/products?limit=20#frag",
        params: [
          row("limit", "20", { description: "Page size", extra: '{"x-note":"kept"}' }),
          row("cursor", "abc", { enabled: false }),
        ],
      }),
      open: ["list"],
    });

    fireEvent.change(cell("Value, row 1"), { target: { value: "50" } });
    await act(settle);
    expect(calls()).toBe(1);
    expect(sent().request.url).toBe("{{baseUrl}}/v1/products?limit=50#frag");
    expect(urlText()).toBe("{{baseUrl}}/v1/products?limit=50#frag");

    // Enabling the disabled row adds it to the URL.
    const toggles = () => within(paramsGrid()).getAllByRole("checkbox");
    fireEvent.click(toggles()[1] as HTMLElement);
    await act(settle);
    expect(calls()).toBe(2);
    expect(urlText()).toBe("{{baseUrl}}/v1/products?limit=50&cursor=abc#frag");

    // Disabling keeps the row in params, not in the URL.
    fireEvent.click(toggles()[0] as HTMLElement);
    await act(settle);
    expect(calls()).toBe(3);
    expect(sent().request.url).toBe("{{baseUrl}}/v1/products?cursor=abc#frag");
    expect(sent().request.params).toEqual([
      row("limit", "50", { enabled: false, description: "Page size", extra: '{"x-note":"kept"}' }),
      row("cursor", "abc"),
    ]);

    // A value with & and # is encoded in the URL, and the row keeps its text.
    fireEvent.change(cell("Value, row 2"), { target: { value: "a&b#c" } });
    await act(settle);
    expect(urlText()).toBe("{{baseUrl}}/v1/products?cursor=a%26b%23c#frag");
    expect(sent().request.params?.[1]).toEqual(row("cursor", "a&b#c"));
    // Editing the URL elsewhere keeps the row's own text.
    typeUrl("{{baseUrl}}/v1/items?cursor=a%26b%23c#frag");
    await act(settle);
    expect(sent().request.params?.[1]).toEqual(row("cursor", "a&b#c"));

    // Deleting the last enabled row drops the query, keeping the fragment.
    const before = calls();
    fireEvent.click(within(paramsGrid()).getByRole("button", { name: "Delete row 2" }));
    await act(settle);
    expect(calls()).toBe(before + 1);
    expect(urlText()).toBe("{{baseUrl}}/v1/items#frag");
    expect(sent().request.params).toEqual([
      row("limit", "50", { enabled: false, description: "Page size", extra: '{"x-note":"kept"}' }),
    ]);

    // A description is saved without touching the URL.
    fireEvent.change(cell("Description, row 1"), { target: { value: "How many" } });
    await act(settle);
    expect(sent().request.url).toBe("{{baseUrl}}/v1/items#frag");
    expect(sent().request.params?.[0]?.description).toBe("How many");
  });

  it("adds a row from the table's empty row to the URL", async () => {
    await setup();
    const keys = within(paramsGrid()).getAllByPlaceholderText("key");
    fireEvent.change(keys[keys.length - 1] as HTMLElement, { target: { value: "sort" } });
    await act(settle);
    expect(urlText()).toBe("{{baseUrl}}/v1/products?limit=20&sort");
    const values = within(paramsGrid()).getAllByPlaceholderText("value");
    fireEvent.change(values[2] as HTMLElement, { target: { value: "name" } });
    await act(settle);
    expect(urlText()).toBe("{{baseUrl}}/v1/products?limit=20&sort=name");
  });

  it("keeps the caret in the URL where it was when the rows rewrite the query", async () => {
    await setup();
    const view = urlView();
    act(() => view.dispatch({ selection: { anchor: 5, head: 11 } }));
    fireEvent.change(cell("Value, row 1"), { target: { value: "100" } });
    await act(settle);
    expect(urlText()).toBe("{{baseUrl}}/v1/products?limit=100");
    expect(view.state.selection.main).toMatchObject({ anchor: 5, head: 11 });
  });

  describe("sending (HTTP-20)", () => {
    const button = (name: string) => within(editor()).getByRole("button", { name });

    it("sends the request after its queued edits reached Go, and cancels it", async () => {
      await setup();
      typeUrl("https://example.com/items");
      fireEvent.click(button("Send"));
      await act(settle);
      expect(http.Send).toHaveBeenCalledWith({ requestId: "list" });
      const order = service.UpdateRequest.mock.invocationCallOrder;
      const saved = order[order.length - 1] ?? 0;
      expect(http.Send.mock.invocationCallOrder[0]).toBeGreaterThan(saved);
      expect(sent().request.url).toBe("https://example.com/items");

      // Running: Send is Cancel, which cancels the operation.
      fireEvent.click(button("Cancel"));
      await act(settle);
      expect(opsCancel).toHaveBeenCalledWith("op_send");
      act(() => emit("http:op:done", { opId: "op_send", seq: 1, status: "cancelled", error: null, items: 0, batches: 0, elapsedMs: 5 }));
      expect(button("Send")).toBeTruthy();
    });

    it("repeats the request from the Repeat dialog and stops the run (HTTP-27)", async () => {
      await setup();
      fireEvent.click(button("Repeat…"));
      const dialog = screen.getByRole("dialog", { name: "Repeat the request" });
      const start = () => within(dialog).getByRole("button", { name: "Start" }) as HTMLButtonElement;
      fireEvent.change(within(dialog).getByRole("textbox", { name: "Times" }), { target: { value: "0" } });
      expect(start().disabled).toBe(true);
      expect(within(dialog).getByText(/Enter a whole number/)).toBeTruthy();
      fireEvent.change(within(dialog).getByRole("textbox", { name: "Times" }), { target: { value: "3" } });
      fireEvent.change(within(dialog).getByRole("textbox", { name: /Wait between/ }), { target: { value: "0.5" } });
      fireEvent.click(start());
      await act(settle);
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(http.Repeat).toHaveBeenCalledWith({ requestId: "list", count: 3, intervalMs: 500 });
      expect(http.Send).not.toHaveBeenCalled();

      // Running: Send is Stop, which cancels the run's operation.
      fireEvent.click(button("Stop"));
      await act(settle);
      expect(opsCancel).toHaveBeenCalledWith("op_run");
      act(() => emit("http.repeat:op:done", { opId: "op_run", seq: 1, status: "cancelled", error: null, items: 0, batches: 0, elapsedMs: 5 }));
      expect(button("Send")).toBeTruthy();

      // The dialog offers the last run's options; at an interval, until stopped.
      fireEvent.click(button("Repeat…"));
      const again = screen.getByRole("dialog", { name: "Repeat the request" });
      expect((within(again).getByRole("textbox", { name: "Times" }) as HTMLInputElement).value).toBe("3");
      fireEvent.click(within(again).getByRole("radio", { name: "At an interval" }));
      fireEvent.change(within(again).getByRole("textbox", { name: /Every/ }), { target: { value: "2" } });
      fireEvent.click(within(again).getByRole("button", { name: "Start" }));
      await act(settle);
      expect(http.Repeat).toHaveBeenLastCalledWith({ requestId: "list", count: 0, intervalMs: 2000 });
    });

    it("connects an sse request instead of sending it, and stops it (SSE-01)", async () => {
      await setup({
        dto: request({ kind: "sse", url: "https://example.com/events", body: { mode: "none", type: "", text: "", fields: null, parts: null, path: "", contentType: "", extra: "" } }),
        nodes: [node({ id: "list", name: "Events", fileKind: "sse" })],
        open: ["list"],
      });
      expect(within(editor()).queryByRole("button", { name: "Send" })).toBeNull();
      // Its events are checked (TEST-08); nothing is extracted from them.
      expect(within(editor()).getByRole("tab", { name: "Tests" })).toBeTruthy();
      expect(within(editor()).queryByRole("tab", { name: "Extract" })).toBeNull();
      fireEvent.click(button("Connect"));
      await act(settle);
      expect(sse.Connect).toHaveBeenCalledWith({ requestId: "list", allowUnresolved: false, lastEventId: "" });
      expect(http.Send).not.toHaveBeenCalled();

      // Connected: Connect is Stop, which cancels the operation.
      fireEvent.click(button("Stop"));
      await act(settle);
      expect(opsCancel).toHaveBeenCalledWith("op_sse");
      act(() => emit("sse:op:done", { opId: "op_sse", seq: 1, status: "cancelled", error: null, items: 0, batches: 0, elapsedMs: 5 }));
      expect(button("Connect")).toBeTruthy();

      // Mod+Enter connects too.
      act(() => {
        shortcutBindings.get("request.send")?.(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true }));
      });
      await act(settle);
      expect(sse.Connect).toHaveBeenCalledTimes(2);
    });

    it("edits an sse request's reconnection policy in Settings, left out of the file when off (SSE-03)", async () => {
      await setup({
        dto: request({ kind: "sse", url: "https://example.com/events" }),
        nodes: [node({ id: "list", name: "Events", fileKind: "sse" })],
        open: ["list"],
      });
      fireEvent.click(within(editor()).getByRole("tab", { name: "Settings" }));
      const box = within(editor()).getByRole("checkbox", { name: /Reconnect with Last-Event-ID/ }) as HTMLInputElement;
      const attempts = within(editor()).getByLabelText("Attempts") as HTMLInputElement;
      expect(box.checked).toBe(false);
      expect(attempts.disabled).toBe(true);
      fireEvent.click(box);
      await act(settle);
      expect(sent().request.settings.reconnect).toEqual({ enabled: true, maxAttempts: null, delayMs: null, extra: "" });
      fireEvent.change(attempts, { target: { value: "0" } });
      fireEvent.change(within(editor()).getByLabelText("Delay"), { target: { value: "250" } });
      await act(settle);
      expect(sent().request.settings.reconnect).toEqual({ enabled: true, maxAttempts: 0, delayMs: 250, extra: "" });
      expect(within(editor()).getByText(/keeps trying until you stop it/)).toBeTruthy();
      fireEvent.change(attempts, { target: { value: "x" } });
      expect(within(editor()).getByRole("alert").textContent).toContain("whole number");
      fireEvent.change(attempts, { target: { value: "" } });
      fireEvent.change(within(editor()).getByLabelText("Delay"), { target: { value: "" } });
      fireEvent.click(box);
      await act(settle);
      expect(sent().request.settings.reconnect).toBeNull();
    });

    it("shows no reconnection policy for an http request", async () => {
      await setup();
      fireEvent.click(within(editor()).getByRole("tab", { name: "Settings" }));
      expect(within(editor()).queryByRole("checkbox", { name: /Reconnect/ })).toBeNull();
    });

    it("sends with the request.send shortcut (Mod+Enter) while the editor is open", async () => {
      await setup();
      const handler = shortcutBindings.get("request.send");
      expect(handler).toBeDefined();
      act(() => {
        handler?.(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true }));
      });
      await act(settle);
      expect(http.Send).toHaveBeenCalledTimes(1);
      // A second press while it runs sends nothing more.
      act(() => {
        handler?.(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true }));
      });
      await act(settle);
      expect(http.Send).toHaveBeenCalledTimes(1);
    });
  });

  describe("settings (HTTP-21)", () => {
    const timeoutField = () => within(editor()).getByRole("textbox", { name: "Timeout" }) as HTMLInputElement;

    it("saves the request's own timeout: milliseconds, 0 for none, empty for the default", async () => {
      await setup();
      fireEvent.click(within(editor()).getByRole("tab", { name: "Settings" }));
      expect(timeoutField().value).toBe("");
      expect(timeoutField().placeholder).toBe("default (none)");

      fireEvent.change(timeoutField(), { target: { value: "1500" } });
      await act(settle);
      expect(sent().request.settings.timeoutMs).toBe(1500);
      expect(within(editor()).getByText(/Stops the request after 1.5 s\./)).toBeTruthy();

      fireEvent.change(timeoutField(), { target: { value: "0" } });
      await act(settle);
      expect(sent().request.settings.timeoutMs).toBe(0);

      fireEvent.change(timeoutField(), { target: { value: "" } });
      await act(settle);
      expect(sent().request.settings.timeoutMs).toBeNull();

      const calls = service.UpdateRequest.mock.calls.length;
      fireEvent.change(timeoutField(), { target: { value: "2.5s" } });
      await act(settle);
      expect(within(editor()).getByRole("alert").textContent).toContain("whole milliseconds");
      expect(service.UpdateRequest.mock.calls.length).toBe(calls);
    });

    it("names the default timeout from the settings", async () => {
      const stores = await setup();
      act(() => stores.settings.dispatch({ type: "settings/load", settings: { defaultTimeoutMs: 30000 } }));
      fireEvent.click(within(editor()).getByRole("tab", { name: "Settings" }));
      expect(timeoutField().placeholder).toBe("default (30 s)");
      expect(within(editor()).getByText(/Uses the default timeout, 30 s\./)).toBeTruthy();
    });
  });

  describe("redirect settings (HTTP-22)", () => {
    const box = (name: RegExp) => within(editor()).getByRole("checkbox", { name }) as HTMLInputElement;
    const maxField = () => within(editor()).getByRole("textbox", { name: "Maximum" }) as HTMLInputElement;

    it("starts from the safe defaults and saves each option", async () => {
      await setup();
      fireEvent.click(within(editor()).getByRole("tab", { name: "Settings" }));
      expect(box(/Follow redirects/).checked).toBe(true);
      expect(maxField().value).toBe("");
      expect(maxField().placeholder).toBe("default (10)");
      expect(box(/Keep the original method/).checked).toBe(false);
      expect(box(/Forward Authorization/).checked).toBe(false);
      expect(within(editor()).getByText(/Follows up to 10 redirects, then fails\./)).toBeTruthy();

      fireEvent.change(maxField(), { target: { value: "0" } });
      await act(settle);
      expect(sent().request.settings.maxRedirects).toBe(0);
      expect(within(editor()).getByText(/A limit of 0 follows no redirect/)).toBeTruthy();

      fireEvent.change(maxField(), { target: { value: "3" } });
      await act(settle);
      expect(sent().request.settings.maxRedirects).toBe(3);

      fireEvent.change(maxField(), { target: { value: "-1" } });
      await act(settle);
      expect(within(editor()).getByRole("alert").textContent).toContain("whole number");
      expect(sent().request.settings.maxRedirects).toBe(3);
      fireEvent.change(maxField(), { target: { value: "" } });
      await act(settle);
      expect(sent().request.settings.maxRedirects).toBeNull();

      fireEvent.click(box(/Keep the original method/));
      await act(settle);
      expect(sent().request.settings.followOriginalMethod).toBe(true);

      fireEvent.click(box(/Forward Authorization/));
      await act(settle);
      expect(sent().request.settings.forwardAuthorization).toBe(true);
      expect(within(editor()).getByText(/The Authorization header goes to every host/)).toBeTruthy();
      fireEvent.click(box(/Forward Authorization/));
      await act(settle);
      expect(sent().request.settings.forwardAuthorization).toBeNull();

      fireEvent.click(box(/Follow redirects/));
      await act(settle);
      expect(sent().request.settings.followRedirects).toBe(false);
      expect(maxField().disabled).toBe(true);
      expect(box(/Keep the original method/).disabled).toBe(true);
      expect(within(editor()).getByText(/Redirects are not followed/)).toBeTruthy();
      fireEvent.click(box(/Follow redirects/));
      await act(settle);
      expect(sent().request.settings.followRedirects).toBeNull();
    });
  });

  describe("URL encoding, HTTP version and cookies (HTTP-24, HTTP-25, HTTP-26)", () => {
    const select = (name: string) => within(editor()).getByRole("combobox", { name }) as HTMLSelectElement;
    const box = (name: RegExp) => within(editor()).getByRole("checkbox", { name }) as HTMLInputElement;

    it("starts from the defaults and stores only what differs", async () => {
      await setup();
      fireEvent.click(within(editor()).getByRole("tab", { name: "Settings" }));
      expect(select("URL encoding").value).toBe("auto");
      expect(within(editor()).getByText(/Encodes like a browser/)).toBeTruthy();
      expect(select("HTTP version").value).toBe("auto");
      expect(box(/Send cookies from the jar/).checked).toBe(true);
      expect(box(/Store cookies from the response/).checked).toBe(true);

      fireEvent.change(select("URL encoding"), { target: { value: "rfc3986" } });
      await act(settle);
      expect(sent().request.settings.urlEncoding).toBe("rfc3986");
      expect(within(editor()).getByText(/Encodes every character RFC 3986 does not allow/)).toBeTruthy();
      fireEvent.change(select("URL encoding"), { target: { value: "none" } });
      await act(settle);
      expect(sent().request.settings.urlEncoding).toBe("none");
      fireEvent.change(select("URL encoding"), { target: { value: "auto" } });
      await act(settle);
      expect(sent().request.settings.urlEncoding).toBe("");

      fireEvent.change(select("HTTP version"), { target: { value: "http2" } });
      await act(settle);
      expect(sent().request.settings.httpVersion).toBe("http2");
      expect(within(editor()).getByText(/h2c with prior knowledge/)).toBeTruthy();
      fireEvent.change(select("HTTP version"), { target: { value: "http1" } });
      await act(settle);
      expect(sent().request.settings.httpVersion).toBe("http1");
      fireEvent.change(select("HTTP version"), { target: { value: "auto" } });
      await act(settle);
      expect(sent().request.settings.httpVersion).toBe("");

      fireEvent.click(box(/Send cookies from the jar/));
      await act(settle);
      expect(sent().request.settings.sendCookies).toBe(false);
      fireEvent.click(box(/Store cookies from the response/));
      await act(settle);
      expect(sent().request.settings.storeCookies).toBe(false);
      fireEvent.click(box(/Send cookies from the jar/));
      await act(settle);
      expect(sent().request.settings.sendCookies).toBeNull();
      expect(sent().request.settings.storeCookies).toBe(false);
    });

    it("shows the settings a file has", async () => {
      await setup({
        dto: request({ settings: { ...request().settings, urlEncoding: "none", httpVersion: "http1", sendCookies: false, storeCookies: false } }),
      });
      fireEvent.click(within(editor()).getByRole("tab", { name: "Settings" }));
      expect(select("URL encoding").value).toBe("none");
      expect(select("HTTP version").value).toBe("http1");
      expect(box(/Send cookies from the jar/).checked).toBe(false);
      expect(box(/Store cookies from the response/).checked).toBe(false);
    });
  });

  describe("TLS verification (HTTP-23)", () => {
    const tlsSelect = () => within(editor()).getByRole("combobox", { name: "Certificates" }) as HTMLSelectElement;
    const badge = () => within(editor()).queryByRole("button", { name: /TLS unverified/ });

    it("is on by default and marks the URL bar while the request turns it off", async () => {
      await setup();
      expect(badge()).toBeNull();
      fireEvent.click(within(editor()).getByRole("tab", { name: "Settings" }));
      expect(tlsSelect().value).toBe("default");
      expect(within(editor()).getByRole("option", { name: "Default (verify)" })).toBeTruthy();

      fireEvent.change(tlsSelect(), { target: { value: "off" } });
      await act(settle);
      expect(sent().request.settings.verifyTls).toBe(false);
      expect(badge()?.getAttribute("data-tls-off")).toBe("request");
      expect(within(editor()).getByText(/Certificate verification is off for this request/)).toBeTruthy();

      fireEvent.change(tlsSelect(), { target: { value: "default" } });
      await act(settle);
      expect(sent().request.settings.verifyTls).toBeNull();
      expect(badge()).toBeNull();
    });

    it("marks requests without their own setting while the app setting is off, and the badge opens Settings", async () => {
      const stores = await setup();
      act(() => stores.settings.dispatch({ type: "settings/load", settings: { verifyTls: false } }));
      expect(badge()?.getAttribute("data-tls-off")).toBe("settings");

      fireEvent.click(badge() as HTMLElement);
      expect(within(editor()).getByRole("tab", { name: "Settings" }).getAttribute("aria-selected")).toBe("true");
      expect(within(editor()).getByRole("option", { name: "Default (don't verify)" })).toBeTruthy();

      fireEvent.change(tlsSelect(), { target: { value: "on" } });
      await act(settle);
      expect(sent().request.settings.verifyTls).toBe(true);
      expect(badge()).toBeNull();
    });
  });
});

describe("RequestEditor Tests sub-tab (TEST-01)", () => {
  const tests = () => within(editor()).getByRole("table", { name: "Assertions" });
  const openTests = () => fireEvent.click(within(editor()).getByRole("tab", { name: /^Tests/ }));
  const assertion = (over: Partial<AssertionDTO> = {}): AssertionDTO => ({
    target: "status",
    expr: "",
    op: "equals",
    expected: "200",
    enabled: true,
    extra: "",
    ...over,
  });

  it("adds, edits, reorders, disables and deletes assertions, saved with the request and undoable", async () => {
    const stores = await setup();
    openTests();
    expect(within(editor()).getByText(/No assertions yet/)).toBeTruthy();

    fireEvent.click(within(editor()).getByRole("button", { name: "Add assertion" }));
    await act(settle);
    expect(sent().request.assertions).toEqual([assertion()]);
    // The new row's target picker has focus.
    expect(document.activeElement).toBe(within(tests()).getByRole("combobox", { name: "Target, assertion 1" }));

    fireEvent.click(within(editor()).getByRole("button", { name: "Add assertion" }));
    fireEvent.change(within(tests()).getByRole("combobox", { name: "Target, assertion 2" }), { target: { value: "jsonpath" } });
    fireEvent.change(within(tests()).getByRole("textbox", { name: "Expression, assertion 2" }), { target: { value: "$.id" } });
    fireEvent.change(within(tests()).getByRole("combobox", { name: "Operator, assertion 2" }), { target: { value: "exists" } });
    await act(settle);
    expect(sent().request.assertions).toEqual([assertion(), assertion({ target: "jsonpath", expr: "$.id", op: "exists", expected: "" })]);
    // exists takes no expected value.
    expect(within(tests()).queryByRole("textbox", { name: "Expected value, assertion 2" })).toBeNull();
    // The sub-tab counts the enabled assertions.
    expect(within(editor()).getByRole("tab", { name: /^Tests/ }).textContent).toBe("Tests2");

    // A target without a name or path drops the one typed; undo brings it back.
    fireEvent.change(within(tests()).getByRole("combobox", { name: "Target, assertion 2" }), { target: { value: "body" } });
    expect(within(tests()).queryByRole("textbox", { name: "Expression, assertion 2" })).toBeNull();
    await act(settle);
    expect(sent().request.assertions?.[1]).toEqual(assertion({ target: "body", op: "exists", expected: "" }));
    act(() => {
      undoRequestEdit(stores, "list");
    });
    await act(settle);
    expect(sent().request.assertions?.[1]).toEqual(assertion({ target: "jsonpath", expr: "$.id", op: "exists", expected: "" }));

    // Move the second one up with the button, and back down with Alt+Down.
    fireEvent.click(within(tests()).getByRole("button", { name: "Move assertion 2 up" }));
    await act(settle);
    expect(sent().request.assertions?.map((a) => a.target)).toEqual(["jsonpath", "status"]);
    fireEvent.keyDown(within(tests()).getByRole("combobox", { name: "Target, assertion 1" }), { key: "ArrowDown", altKey: true });
    await act(settle);
    expect(sent().request.assertions?.map((a) => a.target)).toEqual(["status", "jsonpath"]);

    // Disable, then delete the first.
    fireEvent.click(within(tests()).getByRole("checkbox", { name: "Enabled, assertion 1" }));
    await act(settle);
    expect(sent().request.assertions?.[0]?.enabled).toBe(false);
    expect(within(editor()).getByRole("tab", { name: /^Tests/ }).textContent).toBe("Tests1");
    fireEvent.click(within(tests()).getByRole("button", { name: /^Delete assertion 1/ }));
    await act(settle);
    expect(sent().request.assertions).toEqual([assertion({ target: "jsonpath", expr: "$.id", op: "exists", expected: "" })]);
  });

  it("keeps an assertion's unknown members and shows the last send's result per row", async () => {
    await setup({
      dto: request({
        assertions: [
          assertion({ extra: '{"x-note":"kept"}' }),
          assertion({ target: "header", expr: "X-Id", op: "equals", expected: "{{id}}" }),
        ],
      }),
    });
    openTests();
    fireEvent.change(within(tests()).getByRole("textbox", { name: "Expected value, assertion 1" }), { target: { value: "201" } });
    await act(settle);
    expect(sent().request.assertions?.[0]).toEqual(assertion({ expected: "201", extra: '{"x-note":"kept"}' }));
    expect(within(tests()).getByRole("textbox", { name: "Header name, assertion 2" })).toHaveProperty("value", "X-Id");

    fireEvent.click(within(editor()).getByRole("button", { name: "Send" }));
    await act(settle);
    act(() =>
      emit("http:op:data", {
        opId: "op_send",
        seq: 1,
        items: [
          httpResponse({
            tests: [
              { index: 0, target: "status", expr: "", op: "equals", expected: "201", outcome: "failed", actual: "200", exists: true, message: "Expected status code to equal 201, but it was 200.", code: "", schemaErrors: null },
              { index: 1, target: "header", expr: "X-Id", op: "equals", expected: "7", outcome: "passed", actual: '"7"', exists: true, message: 'Header X-Id equals "7": it was "7".', code: "", schemaErrors: null },
            ],
          }),
        ],
      }),
    );
    act(() => emit("http:op:done", { opId: "op_send", seq: 2, status: "ok", error: null, items: 1, batches: 1, elapsedMs: 5 }));
    expect(within(tests()).getByRole("img", { name: /^Failed in the last send: Expected status code to equal 201/ })).toBeTruthy();
    expect(within(tests()).getByRole("img", { name: /^Passed in the last send/ })).toBeTruthy();
  });

  it("opens at an assertion when a test result jumps to it (TEST-06)", async () => {
    const stores = await setup({ dto: request({ assertions: [assertion(), assertion({ target: "size", op: "lt", expected: "1000" })] }) });
    expect(within(editor()).getByRole("tab", { name: /^Params/ }).getAttribute("aria-selected")).toBe("true");
    act(() => jumpToAssertion(stores, "list", 1));
    await act(settle);
    expect(within(editor()).getByRole("tab", { name: /^Tests/ }).getAttribute("aria-selected")).toBe("true");
    const target = within(tests()).getByRole("combobox", { name: "Target, assertion 2" });
    expect(document.activeElement).toBe(target);
    expect(target.closest("[data-highlight]")?.getAttribute("data-assertion-index")).toBe("1");
  });

  it("takes a JSON Schema file or an inline schema for matches schema (TEST-07)", async () => {
    await setup({ dto: request({ assertions: [assertion({ target: "body", expected: "" })] }) });
    openTests();
    fireEvent.change(within(tests()).getByRole("combobox", { name: "Operator, assertion 1" }), { target: { value: "matches_schema" } });
    await act(settle);
    expect(sent().request.assertions?.[0]).toEqual(assertion({ target: "body", op: "matches_schema", expected: "" }));
    const expected = () => within(tests()).getByRole("textbox", { name: "Expected value, assertion 1" }) as HTMLInputElement;
    expect(expected().placeholder).toBe("schemas/user.json or { … }");

    // A file chosen in the dialog is stored as Go returns its path.
    pickSchemaFile.mockImplementation(() =>
      Object.assign(Promise.resolve({ path: "schemas/user.json", absolutePath: "/p/schemas/user.json", name: "user.json", size: 10, exists: true }), {
        cancel: () => undefined,
      }),
    );
    fireEvent.click(within(tests()).getByRole("button", { name: "Choose a JSON Schema file, assertion 1" }));
    await act(settle);
    expect(pickSchemaFile).toHaveBeenCalledTimes(1);
    expect(sent().request.assertions?.[0]?.expected).toBe("schemas/user.json");
    expect(expected().value).toBe("schemas/user.json");

    // The inline editor opens under the row; a path is shown with a note.
    const toggle = within(tests()).getByRole("button", { name: "Edit the inline JSON schema, assertion 1" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(within(tests()).getByText(/The expected value is a schema file path/)).toBeTruthy();
    const schemaEditor = () => within(tests()).getByRole("textbox", { name: "JSON schema, assertion 1" });
    const view = () => {
      const v = EditorView.findFromDOM(schemaEditor());
      if (v === null) throw new Error("the schema editor is not a CodeMirror editor");
      return v;
    };
    expect(view().state.doc.toString()).toBe("schemas/user.json");

    // Typing a schema saves it; invalid JSON says so; Format indents it.
    act(() => {
      view().dispatch({ changes: { from: 0, to: view().state.doc.length, insert: '{"type":' }, userEvent: "input.type" });
    });
    await act(settle);
    expect(sent().request.assertions?.[0]?.expected).toBe('{"type":');
    expect(within(tests()).getByRole("status").textContent).toMatch(/^Not valid JSON: /);
    act(() => {
      view().dispatch({ changes: { from: 0, to: view().state.doc.length, insert: '{"type":"object"}' }, userEvent: "input.type" });
    });
    await act(settle);
    expect(within(tests()).getByRole("status").textContent).toMatch(/^JSON Schema 2020-12/);
    fireEvent.click(within(tests()).getByRole("button", { name: "Format" }));
    await act(settle);
    expect(sent().request.assertions?.[0]?.expected).toBe('{\n  "type": "object"\n}');
    expect(view().state.doc.toString()).toBe('{\n  "type": "object"\n}');

    fireEvent.click(toggle);
    expect(within(tests()).queryByRole("textbox", { name: "JSON schema, assertion 1" })).toBeNull();
  });

  it("starts an empty matches schema row with an empty inline schema", async () => {
    await setup({ dto: request({ assertions: [assertion({ target: "body", op: "matches_schema", expected: "" })] }) });
    openTests();
    fireEvent.click(within(tests()).getByRole("button", { name: "Edit the inline JSON schema, assertion 1" }));
    await act(settle);
    expect(sent().request.assertions?.[0]?.expected).toBe("{}");
  });

  it("is read-only in a read-only project", async () => {
    await setup({ dto: request({ assertions: [assertion()] }), projectReadOnly: "newer_version" });
    openTests();
    expect(within(editor()).queryByRole("button", { name: "Add assertion" })).toBeNull();
    expect(within(tests()).getByRole("combobox", { name: "Target, assertion 1" })).toHaveProperty("disabled", true);
    expect(within(tests()).queryByRole("button", { name: /^Delete assertion/ })).toBeNull();
  });
});

describe("RequestEditor Extract sub-tab (TEST-05)", () => {
  const rules = () => within(editor()).getByRole("table", { name: "Extraction rules" });
  const openExtract = () => fireEvent.click(within(editor()).getByRole("tab", { name: /^Extract/ }));

  it("edits the rules, saved with the request, counts them and shows the last send's results", async () => {
    await setup({
      dto: request({
        extractions: [
          { source: "jsonpath", expr: "$.token", variable: "token", scope: "runtime", secret: true, persist: false, enabled: true, extra: "" },
          { source: "header", expr: "X-Missing", variable: "missing", scope: "runtime", secret: false, persist: false, enabled: true, extra: "" },
        ],
      }),
    });
    expect(within(editor()).getByRole("tab", { name: /^Extract/ }).textContent).toBe("Extract2");
    openExtract();
    fireEvent.change(within(rules()).getByRole("textbox", { name: "Variable name, rule 1" }), { target: { value: "accessToken" } });
    await act(settle);
    expect(sent().request.extractions?.[0]).toEqual({
      source: "jsonpath",
      expr: "$.token",
      variable: "accessToken",
      scope: "runtime",
      secret: true,
      persist: false,
      enabled: true,
      extra: "",
    });

    fireEvent.click(within(editor()).getByRole("button", { name: "Send" }));
    await act(settle);
    const result = (over: object) => ({
      index: 0,
      source: "jsonpath",
      expr: "$.token",
      variable: "accessToken",
      scope: "runtime",
      secret: true,
      persist: false,
      outcome: "set",
      value: "••••••",
      valueClipped: false,
      message: "Set the runtime variable accessToken from JSONPath $.token.",
      code: "",
      ...over,
    });
    act(() =>
      emit("http:op:data", {
        opId: "op_send",
        seq: 1,
        items: [
          httpResponse({
            extractions: [
              result({}),
              result({
                index: 1,
                source: "header",
                expr: "X-Missing",
                variable: "missing",
                secret: false,
                outcome: "failed",
                value: "",
                message: "Header X-Missing is not in the response.",
                code: "extract.not_found",
              }),
            ],
          }),
        ],
      }),
    );
    act(() => emit("http:op:done", { opId: "op_send", seq: 2, status: "ok", error: null, items: 1, batches: 1, elapsedMs: 5 }));
    expect(within(rules()).getByRole("img", { name: "Set in the last send: Set the runtime variable accessToken from JSONPath $.token." })).toBeTruthy();
    expect(within(rules()).getByRole("img", { name: "Failed in the last send: Header X-Missing is not in the response." })).toBeTruthy();
  });

  it("is not offered for an SSE request", async () => {
    await setup({ dto: request({ kind: "sse", url: "https://example.com/events" }) });
    expect(within(editor()).queryByRole("tab", { name: /^Extract/ })).toBeNull();
  });
});
