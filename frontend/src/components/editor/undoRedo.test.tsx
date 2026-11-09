import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { undo, undoDepth } from "@codemirror/commands";
import { EditorView } from "@codemirror/view";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: { request: unknown; revision: number }) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
  InheritedAuth: () => Object.assign(Promise.resolve({ type: "none", disabled: false, folderId: "", folderName: "" }), { cancel: () => undefined }),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: { Send: vi.fn() },
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

import { historyDepth } from "../../commands/editHistory";
import { activateTab, closeTabs, openRequest } from "../../commands/tabs";
import { followEditHistory } from "../../commands/undoRedo";
import type { KeyValueDTO, RequestDTO } from "../../lib/collection";
import { IS_MAC } from "../../lib/shortcuts";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { TabBar } from "../tabs/TabBar";
import { TabPanel } from "../tabs/TabPanel";

const row = (name: string, value: string): KeyValueDTO => ({ name, value, description: "", enabled: true, extra: "" });

function request(over: Partial<RequestDTO> = {}): RequestDTO {
  return {
    id: "list",
    kind: "http",
    name: "List products",
    description: "",
    method: "GET",
    url: "/v1/products?limit=20",
    params: [row("limit", "20")],
    pathParams: null,
    headers: [row("Accept", "application/json")],
    body: { mode: "raw", type: "json", text: '{"a":1}', fields: null, parts: null, path: "", contentType: "", extra: "" },
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
    extra: "",
    ...over,
  };
}

const node = (id: string): TreeNode =>
  ({ kind: "request", parentId: null, name: id, path: `${id}.json`, fileKind: "http", readOnly: "", method: "GET", id }) as TreeNode;

let stopHistory: (() => void) | null = null;

async function setup(): Promise<Stores> {
  service.Request.mockImplementation((id) => Promise.resolve(id === "list" ? request() : request({ id, name: id, url: `/${id}` })));
  const stores = createStores();
  const nodes = [node("list"), node("other")];
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes, rootIds: ["list", "other"], readOnly: "" });
  stores.tabs.dispatch({ type: "tabs/restore", order: ["list", "other"], activeId: "list" });
  stopHistory = followEditHistory(stores);
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
const params = () => within(editor()).getByRole("grid", { name: "Query params" });
const urlView = () => {
  const view = EditorView.findFromDOM(within(editor()).getByRole("textbox", { name: "URL" }));
  if (view === null) throw new Error("the URL field is not a CodeMirror editor");
  return view;
};
const urlText = () => urlView().state.doc.toString();
const draft = (stores: Stores, id = "list") => stores.drafts.getState().byId[id]?.request as unknown as RequestDTO;
const sent = () => {
  const calls = service.UpdateRequest.mock.calls;
  return (calls[calls.length - 1]?.[0] as { request: RequestDTO } | undefined)?.request;
};

const mod = IS_MAC ? { metaKey: true } : { ctrlKey: true };
const pressUndo = (el: Element) => fireEvent.keyDown(el, { key: "z", code: "KeyZ", ...mod });
const pressRedo = (el: Element) => fireEvent.keyDown(el, { key: "Z", code: "KeyZ", shiftKey: true, ...mod });
const pressCtrlY = (el: Element) => fireEvent.keyDown(el, { key: "y", code: "KeyY", ...mod });

/** Switches to the other tab and back, so the editor unmounts and mounts again. */
const switchAway = (stores: Stores) =>
  act(async () => {
    activateTab(stores, "other");
    await settle();
    activateTab(stores, "list");
    await settle();
  });

beforeEach(() => {
  resetFakeWails();
  service.Request.mockReset();
  service.UpdateRequest.mockReset().mockResolvedValue(undefined);
  service.Flush.mockReset().mockResolvedValue(undefined);
});

afterEach(() => {
  stopHistory?.();
  stopHistory = null;
});

describe("undo and redo in the request editor (NAV-07)", () => {
  it("undoes and redoes a table edit after switching tabs, through auto-save", async () => {
    const stores = await setup();
    fireEvent.change(within(params()).getByDisplayValue("20"), { target: { value: "50" } });
    expect(draft(stores).params?.[0]?.value).toBe("50");
    expect(draft(stores).url).toBe("/v1/products?limit=50");

    await switchAway(stores);
    const cell = within(params()).getByDisplayValue("50");
    pressUndo(cell);
    await act(settle);
    expect(draft(stores).params?.[0]?.value).toBe("20");
    expect(draft(stores).url).toBe("/v1/products?limit=20");
    expect(sent()?.params?.[0]?.value).toBe("20");
    expect(within(params()).getByDisplayValue("20")).toBeTruthy();
    expect(urlText()).toBe("/v1/products?limit=20");

    pressRedo(within(params()).getByDisplayValue("20"));
    await act(settle);
    expect(draft(stores).params?.[0]?.value).toBe("50");
    pressUndo(within(params()).getByDisplayValue("50"));
    pressCtrlY(within(params()).getByDisplayValue("20"));
    await act(settle);
    expect(sent()?.params?.[0]?.value).toBe("50");
  });

  it("coalesces rapid typing in a cell into one step, and keeps a method switch apart", async () => {
    const stores = await setup();
    fireEvent.change(screen.getByRole("combobox", { name: "Method" }), { target: { value: "POST" } });
    const cell = () => within(params()).getAllByRole("textbox")[1] as HTMLElement;
    for (const v of ["2", "25", "250"]) fireEvent.change(cell(), { target: { value: v } });
    expect(historyDepth(stores, "list").undo).toBe(2);

    pressUndo(cell());
    expect(draft(stores).params?.[0]?.value).toBe("20");
    expect(draft(stores).method).toBe("POST");
    pressUndo(cell());
    expect(draft(stores).method).toBe("GET");
    // The tree and tab badges follow the method, as for an edit.
    expect(stores.collection.getState().nodes.list).toMatchObject({ method: "GET" });
  });

  it("keeps a code editor's own history across tab switches", async () => {
    const stores = await setup();
    act(() => {
      const view = urlView();
      view.dispatch({ changes: { from: view.state.doc.length, insert: "&page=2" }, userEvent: "input.type" });
    });
    expect(draft(stores).url).toBe("/v1/products?limit=20&page=2");

    await switchAway(stores);
    expect(urlText()).toBe("/v1/products?limit=20&page=2");
    expect(undoDepth(urlView().state)).toBe(1);
    act(() => {
      undo(urlView());
    });
    await act(settle);
    expect(urlText()).toBe("/v1/products?limit=20");
    expect(draft(stores).url).toBe("/v1/products?limit=20");
    expect(sent()?.url).toBe("/v1/products?limit=20");
  });

  it("leaves ⌘Z in a code editor to the editor", async () => {
    const stores = await setup();
    act(() => {
      const view = urlView();
      view.dispatch({ changes: { from: view.state.doc.length, insert: "&page=2" }, userEvent: "input.type" });
    });
    fireEvent.click(within(editor()).getByRole("tab", { name: /Headers/ }));
    const headers = within(editor()).getByRole("grid", { name: "Headers" });
    fireEvent.click(within(headers).getAllByRole("checkbox")[0] as HTMLElement);
    const e = pressUndo(urlView().contentDOM);
    await act(settle);
    expect(e).toBe(false); // taken by CodeMirror
    // The URL's typing is undone; the header edit, the newer step of the draft history, stays.
    expect(urlText()).toBe("/v1/products?limit=20");
    expect(draft(stores).url).toBe("/v1/products?limit=20");
    expect(draft(stores).headers?.[0]?.enabled).toBe(false);
  });

  it("brings back a body editor's history after a sub-tab switch", async () => {
    const stores = await setup();
    fireEvent.click(within(editor()).getByRole("tab", { name: "Body" }));
    const bodyView = () => EditorView.findFromDOM(within(editor()).getByRole("textbox", { name: "Request body" }))!;
    act(() => {
      bodyView().dispatch({ changes: { from: 0, to: bodyView().state.doc.length, insert: '{"a":2}' }, userEvent: "input.type" });
    });
    fireEvent.click(within(editor()).getByRole("tab", { name: /Headers/ }));
    await switchAway(stores);
    fireEvent.click(within(editor()).getByRole("tab", { name: "Body" }));
    expect(undoDepth(bodyView().state)).toBe(1);
    act(() => {
      undo(bodyView());
    });
    expect(draft(stores).body.text).toBe('{"a":1}');
  });

  it("undoes a bulk edit of a table in one step (HTTP-08)", async () => {
    const stores = await setup();
    const depth = historyDepth(stores, "list").undo;
    fireEvent.click(within(editor()).getByRole("button", { name: "Bulk edit Query params" }));
    const text = within(editor()).getByRole("textbox", { name: "Query params, bulk edit" });
    text.focus();
    for (const v of ["limit: 5", "limit: 50\npage: 2", "limit: 50\n// page: 2\nsort: name"]) {
      fireEvent.change(text, { target: { value: v } });
    }
    expect(draft(stores).params?.[0]?.value).toBe("20");
    fireEvent.mouseDown(within(editor()).getByRole("button", { name: "Bulk edit Query params" }));
    fireEvent.click(within(editor()).getByRole("button", { name: "Bulk edit Query params" }));
    await act(settle);
    expect(draft(stores).params?.map((p) => [p.name, p.value, p.enabled])).toEqual([
      ["limit", "50", true],
      ["page", "2", false],
      ["sort", "name", true],
    ]);
    expect(draft(stores).url).toBe("/v1/products?limit=50&sort=name");
    expect(historyDepth(stores, "list").undo).toBe(depth + 1);

    pressUndo(within(params()).getByDisplayValue("50"));
    await act(settle);
    expect(draft(stores).params?.map((p) => [p.name, p.value])).toEqual([["limit", "20"]]);
    expect(draft(stores).url).toBe("/v1/products?limit=20");
    expect(sent()?.params?.map((p) => p.name)).toEqual(["limit"]);
  });

  it("drops the history when the tab closes", async () => {
    const stores = await setup();
    fireEvent.change(within(params()).getByDisplayValue("20"), { target: { value: "50" } });
    act(() => {
      const view = urlView();
      view.dispatch({ changes: { from: view.state.doc.length, insert: "&x=1" }, userEvent: "input.type" });
    });
    await act(async () => {
      closeTabs(stores, ["list"]);
      await settle();
    });
    expect(historyDepth(stores, "list").undo).toBe(0);

    service.Request.mockImplementation((id) =>
      Promise.resolve(id === "list" ? request({ url: "/v1/products?limit=50&x=1", params: [row("limit", "50"), row("x", "1")] }) : request({ id, url: `/${id}` })),
    );
    await act(async () => {
      openRequest(stores, "list");
      await settle();
    });
    expect(undoDepth(urlView().state)).toBe(0);
    const calls = service.UpdateRequest.mock.calls.length;
    pressUndo(within(params()).getByDisplayValue("50"));
    await act(settle);
    expect(service.UpdateRequest.mock.calls.length).toBe(calls);
    expect(draft(stores).params?.[0]?.value).toBe("50");
  });
});
