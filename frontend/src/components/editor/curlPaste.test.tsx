import { act, fireEvent, render, within } from "@testing-library/react";
import { EditorView } from "@codemirror/view";
import { beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: { request: unknown; revision: number }) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
  ParseCurl: vi.fn<(text: string) => Promise<unknown>>(),
  InheritedAuth: () => Object.assign(Promise.resolve({ type: "none", disabled: false, folderId: "", folderName: "" }), { cancel: () => undefined }),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: {
    Send: vi.fn(),
    Variables: () => Object.assign(Promise.resolve({ variables: [], environment: "" }), { cancel: () => undefined }),
  },
}));
vi.mock(
  "../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../../test/fakeWails")).fakeOpsBindings,
);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: {
    ValidateJSON: () => Object.assign(Promise.resolve({ valid: true, error: null }), { cancel: () => undefined }),
  },
}));

import { onImportCurlDialog } from "../../commands/curlImport";
import { resetHttpSends } from "../../commands/httpSends";
import { toaster } from "../../commands/toasts";
import type { CurlImportDTO, KeyValueDTO, RequestDTO } from "../../lib/collection";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { TabPanel } from "../tabs/TabPanel";

const row = (name: string, value: string): KeyValueDTO => ({ name, value, description: "", enabled: true, extra: "" });

function request(over: Partial<RequestDTO> = {}): RequestDTO {
  return {
    id: "list",
    kind: "http",
    name: "List products",
    description: "Kept",
    method: "GET",
    url: "{{baseUrl}}/v1/products",
    params: [],
    pathParams: null,
    headers: [row("Accept", "application/json")],
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
    subprotocols: [],
    messageFormat: "",
    service: "",
    proto: { files: [], importPaths: [], inherit: false, extra: "" },
    metadata: [],
    bodyExamples: [],
    auth: { type: "inherit", username: "", password: "", prefix: null, token: "", key: "", value: "", in: "", disabled: false, localSecrets: [], extra: "" },
    settings: { timeoutMs: 5000, followRedirects: null, maxRedirects: null, followOriginalMethod: null, forwardAuthorization: null, verifyTls: null, connectTimeoutMs: null, urlEncoding: "", httpVersion: "", sendCookies: null, storeCookies: null, tls: null, deadlineMs: null, extra: '{"x":1}' },
    extra: '{"x-custom":true}',
    ...over,
  };
}

const imported: CurlImportDTO = {
  ok: true,
  name: "orders",
  request: request({
    id: "",
    name: "",
    description: "",
    method: "POST",
    url: "https://api.example.com/orders?page=2",
    params: [row("page", "2")],
    pathParams: [],
    headers: [row("content-type", "application/json")],
    body: { mode: "raw", type: "json", text: '{"a":1}', fields: [], parts: [], path: "", contentType: "", extra: "" },
    auth: { type: "basic", username: "ada", password: "pw", prefix: null, token: "", key: "", value: "", in: "", disabled: false, localSecrets: [], extra: "" },
    settings: { timeoutMs: null, followRedirects: null, maxRedirects: null, followOriginalMethod: null, forwardAuthorization: null, verifyTls: false, connectTimeoutMs: null, urlEncoding: "", httpVersion: "", sendCookies: null, storeCookies: null, tls: null, deadlineMs: null, extra: "" },
    extra: "",
  }),
  diagnostics: [
    { code: "curl.not_imported", severity: "warning", from: 60, to: 76, message: "“--http2” is not imported." },
    { code: "curl.ignored_option", severity: "info", from: 77, to: 79, message: "“-s” is ignored." },
  ],
};

const COMMAND = "curl 'https://api.example.com/orders?page=2' -H 'content-type: application/json' -u ada:pw -k --data-raw '{\"a\":1}'";

async function setup(over: { readOnly?: string } = {}): Promise<Stores> {
  const dto = request();
  service.Request.mockImplementation(() => Promise.resolve(structuredClone(dto)));
  const stores = createStores();
  const nodes = [{ kind: "request", parentId: null, id: "list", name: "List products", path: "list.json", fileKind: "http", readOnly: over.readOnly ?? "", method: "GET" } as TreeNode];
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes, rootIds: ["list"], readOnly: "" });
  stores.tabs.dispatch({ type: "tabs/restore", order: ["list"], activeId: "list" });
  render(
    <StoreProvider stores={stores}>
      <TabPanel />
    </StoreProvider>,
  );
  await act(settle);
  return stores;
}

const urlView = () => {
  const field = within(document.querySelector("[data-request-id]") as HTMLElement).getByRole("textbox", { name: "URL" });
  const view = EditorView.findFromDOM(field);
  if (view === null) throw new Error("no URL editor");
  return view;
};

async function paste(data: string) {
  act(() => {
    fireEvent.paste(urlView().contentDOM, { clipboardData: { getData: (t: string) => (t === "text/plain" ? data : "") } });
  });
  await act(settle);
}

const sent = () => {
  const calls = service.UpdateRequest.mock.calls;
  return calls[calls.length - 1]?.[0] as { request: RequestDTO; revision: number } | undefined;
};

beforeEach(() => {
  resetFakeWails();
  resetHttpSends();
  toaster.clear();
  service.Request.mockReset();
  service.UpdateRequest.mockReset().mockResolvedValue(undefined);
  service.Flush.mockReset().mockResolvedValue(undefined);
  service.ParseCurl.mockReset().mockResolvedValue(structuredClone(imported));
});

describe("pasting a cURL command into the URL bar (IMP-01)", () => {
  it("replaces the request, keeping its name and description, with an Undo toast listing the warnings", async () => {
    const stores = await setup();
    await paste(`  ${COMMAND}\n`);

    expect(service.ParseCurl).toHaveBeenCalledWith(`  ${COMMAND}\n`);
    expect(urlView().state.doc.toString()).toBe("https://api.example.com/orders?page=2");
    const saved = sent()?.request;
    expect(saved).toMatchObject({
      id: "list",
      name: "List products",
      description: "Kept",
      method: "POST",
      url: "https://api.example.com/orders?page=2",
      headers: [row("content-type", "application/json")],
      auth: { type: "basic", username: "ada", password: "pw" },
      extra: '{"x-custom":true}',
    });
    // The http settings are replaced; the rest of the settings stay.
    expect(saved?.settings).toMatchObject({ timeoutMs: null, verifyTls: false, extra: '{"x":1}' });
    expect(saved?.body).toMatchObject({ mode: "raw", type: "json", text: '{"a":1}' });
    expect(stores.collection.getState().nodes.list).toMatchObject({ method: "POST" });

    const [toast] = toaster.toasts();
    expect(toast?.action?.label).toBe("Undo");
    const { container } = render(<>{toast?.message}</>);
    expect(container.textContent).toContain("(1 warning)");
    expect(container.textContent).toContain("“--http2” is not imported.");
    expect(container.textContent).not.toContain("-s");

    act(() => toaster.runAction(toast?.id ?? ""));
    await act(settle);
    expect(sent()?.request).toMatchObject({ method: "GET", url: "{{baseUrl}}/v1/products", headers: [row("Accept", "application/json")] });
    expect(sent()?.request.settings.timeoutMs).toBe(5000);
    expect(urlView().state.doc.toString()).toBe("{{baseUrl}}/v1/products");
  });

  it("leaves a URL paste alone", async () => {
    await setup();
    await paste("https://example.com/curl");
    expect(service.ParseCurl).not.toHaveBeenCalled();
    // Inserted at the caret, as any paste.
    expect(urlView().state.doc.toString()).toBe("https://example.com/curl{{baseUrl}}/v1/products");
  });

  it("changes nothing for a command with an error, and offers the Import cURL dialog", async () => {
    service.ParseCurl.mockResolvedValue({
      ...structuredClone(imported),
      ok: false,
      diagnostics: [{ code: "curl.missing_url", severity: "error", from: 0, to: 4, message: "The command has no URL." }],
    });
    await setup();
    await paste("curl -X POST");
    expect(service.UpdateRequest).not.toHaveBeenCalled();
    expect(urlView().state.doc.toString()).toBe("{{baseUrl}}/v1/products");
    const [toast] = toaster.toasts();
    expect(toast?.message).toBe("This cURL command cannot be imported. The command has no URL.");

    const opened: string[] = [];
    const stop = onImportCurlDialog((text) => opened.push(text));
    act(() => toaster.runAction(toast?.id ?? ""));
    stop();
    expect(opened).toEqual(["curl -X POST"]);
  });

  it("does nothing special in a read-only request", async () => {
    await setup({ readOnly: "symlink" });
    await paste(COMMAND);
    expect(service.ParseCurl).not.toHaveBeenCalled();
    expect(service.UpdateRequest).not.toHaveBeenCalled();
  });
});
