// Accessibility of the main screens and keyboard-only core flows (UI-11,
// docs/frontend.md#accessibility). The app shell is rendered as App renders
// it for an open project (tree, tab bar, request editor, response pane),
// with the Go services faked, then:
//
// - every screen and dialog passes the in-repo audit (test/a11y.ts);
// - the tree and the tab strips have the ARIA structure a screen reader
//   announces (roles, levels, positions, selection, labelled panels);
// - the core flows are done with the keyboard alone: focus moves only by
//   Tab, arrow keys and shortcuts, and keys are sent to the focused element.
//
// Nothing here replaces a check with a real screen reader (VoiceOver, NVDA,
// Orca); that stays manual.
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { EditorView } from "@codemirror/view";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/** A resolved binding call, with the cancel of Wails' CancellablePromise. */
const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: () => undefined });

/** A fake binding service: the given methods, and any other method resolving to null. */
function fakeService<T extends object>(methods: T): T {
  return new Proxy(methods, {
    get(target, prop) {
      if (prop in target) return target[prop as keyof T];
      if (typeof prop !== "string" || prop === "then") return undefined;
      const fn = vi.fn(() => cancellable(null));
      (target as Record<string, unknown>)[prop] = fn;
      return fn;
    },
  });
}

const collection = vi.hoisted(() => ({
  Tree: vi.fn<() => Promise<unknown>>(),
  CreateRequest: vi.fn<(input: unknown) => Promise<unknown>>(),
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: unknown) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
}));
const http = vi.hoisted(() => ({
  Send: vi.fn<(input: { requestId: string }) => Promise<unknown>>(),
}));
const servers = vi.hoisted(() => ({
  List: vi.fn<() => Promise<unknown>>(),
  Types: vi.fn<() => Promise<unknown>>(),
  Traffic: vi.fn<(id: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops", async () => (await import("../test/fakeWails")).fakeOpsBindings);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: fakeService({
    ...collection,
    InheritedAuth: () => cancellable({ type: "none", disabled: false, folderId: "", folderName: "" }),
    InheritedSettings: () => cancellable({ folders: [] }),
  }),
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: fakeService({
    ...http,
    Variables: () => cancellable({ variables: [], environment: "" }),
    HeaderPreview: () => Object.assign(new Promise(() => undefined), { cancel: () => undefined }),
    StoredResponse: () => cancellable(storedView()),
  }),
}));
// The request's earlier responses (HTTP-39): one in the history.
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/history", () => ({
  HistoryService: fakeService({
    List: () =>
      cancellable({
        entries: [
          { id: "h_1", time: "2026-09-23T10:15:02Z", kind: "http", requestId: "get", requestName: "Get order", method: "GET", url: "https://api.test/orders",
            outcome: "ok", statusCode: 404, status: "404 Not Found", errorCode: "", error: "", durationMs: 12, requestSize: 0, responseSize: 2, masked: false },
        ],
        total: 1,
        all: 1,
      }),
    Get: () =>
      cancellable({
        id: "h_1", time: "2026-09-23T10:15:02Z", durationMs: 12, outcome: "ok",
        request: { method: "GET", url: "https://api.test/orders", proto: "HTTP/1.1", headers: [], body: { text: "", base64: false, size: 0, clipped: false } },
        response: { proto: "HTTP/1.1", statusCode: 404, status: "404 Not Found", finalUrl: "https://api.test/orders", redirects: 0, headers: [], contentType: "",
          body: { text: "{}", base64: false, size: 2, clipped: false }, truncated: false },
        trimmed: false,
      }),
  }),
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/console", () => ({
  ConsoleService: fakeService({ List: () => cancellable({ entries: [], capacity: 2000 }) }),
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/network", () => ({
  NetworkService: fakeService({ List: () => cancellable({ conns: [], active: 0, capacity: 500 }) }),
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: fakeService({ ValidateJSON: () => cancellable({ valid: true, error: null }) }),
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/settings", () => ({
  SettingsService: fakeService({
    Location: () => cancellable({ path: "/data/settings.json", exists: true, dataDir: "/data" }),
    CABundles: () => cancellable([]),
    ClientCerts: () => cancellable([]),
    AllowedDirs: () => cancellable([]),
    // A manual proxy, so the URL and password rows show too.
    Proxy: () =>
      cancellable({
        mode: "manual",
        url: "socks5h://alice@proxy.lan:1080",
        hasPassword: true,
        envVar: "",
        envUrl: "",
        effective: "socks5h://alice@proxy.lan:1080",
        remoteDns: true,
        error: null,
      }),
  }),
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/proxyscopes", () => ({
  // A manual project proxy and an environment with no proxy (SOCKS-08), so every scope field shows.
  ProxyScopesService: fakeService({
    Scopes: () =>
      cancellable({
        open: true,
        project: { mode: "manual", url: "http://bob@proxy.corp:3128", hasPassword: true, bypass: ["*.corp"] },
        environments: [{ id: "env_1", name: "Staging", active: true, scope: { mode: "off", url: "", hasPassword: false, bypass: [] } }],
        effective: { proxy: "", level: "environment", source: "environment", remoteDns: false, bypass: [], noProxyVar: "", error: null },
        error: null,
      }),
  }),
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/datadir", () => ({
  DataDirService: fakeService({ Info: () => Promise.reject(new Error("not in this test")) }),
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/workspace", () => ({
  WorkspaceService: fakeService({ Recent: () => cancellable([]) }),
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/themes", () => ({
  ThemesService: fakeService({ List: () => cancellable({ dir: "/data/themes", themes: [] }) }),
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/environments", () => ({
  EnvironmentsService: fakeService({}),
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/cookies", () => ({
  CookiesService: fakeService({}),
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/grpc", () => ({
  GRPCService: fakeService({
    Describe: () => Object.assign(Promise.reject(new Error("No proto files in this test.")), { cancel: () => undefined }),
  }),
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: fakeService(servers),
}));

import { resetHttpSends } from "../commands/httpSends";
import { resetResponseHistories } from "../commands/responseHistory";
import { toLocalServer } from "../commands/servers";
import type { NodeDTO, RequestDTO, TreeDTO } from "../lib/collection";
import { IS_MAC } from "../lib/shortcuts";
import { createStores, StoreProvider, useCollection, useTabs, type Stores, type TreeNode, type WorkspaceInfo } from "../stores";
import { accessibleName, byRole, expectAccessible, pressTab, roleOf, tabTo } from "../test/a11y";
import { emit, resetFakeWails, settle } from "../test/fakeWails";
import { httpResponse } from "../test/httpResponse";
import { grpcMockServerDTO, runningDTO, sseServerDTO, tcpServerDTO, udpServerDTO, webhookServerDTO, wsServerDTO } from "../test/servers";
import { HttpResponsePanel } from "./response/HttpResponsePanel";
import { ServerDetails, ServerTraffic } from "./servers/ServerView";
import { AppShell } from "./shell/AppShell";
import { NoTabState } from "./tabs/NoTabState";
import { TabBar } from "./tabs/TabBar";
import { TabPanel } from "./tabs/TabPanel";

const shop: WorkspaceInfo = {
  id: "ws_1",
  name: "shop-api",
  path: "/p/shop",
  localDir: "/data/ws_1",
  localStatus: "existing",
  previousPath: "",
  newerFormat: false,
};

function dto(over: Partial<NodeDTO> & { id: string }): NodeDTO {
  return { parentId: null, kind: "request", name: over.id, path: `${over.id}.json`, fileKind: "http", method: "GET", childIds: [], readOnly: "", ...over };
}

// orders/ { Get order, List orders }, Health
const NODES: NodeDTO[] = [
  dto({ id: "orders", name: "Orders", kind: "folder", fileKind: "folder", method: "", path: "orders", childIds: ["get", "list"] }),
  dto({ id: "get", parentId: "orders", name: "Get order", path: "orders/get-order.json" }),
  dto({ id: "list", parentId: "orders", name: "List orders", path: "orders/list-orders.json" }),
  dto({ id: "health", name: "Health", path: "health.json" }),
];

function treeWith(extra: NodeDTO[] = []): TreeDTO {
  return {
    workspaceId: "ws_1",
    path: "/p/shop",
    readOnly: "",
    rootIds: ["orders", "health", ...extra.filter((e) => e.parentId === null).map((e) => e.id)],
    nodes: [...NODES, ...extra],
    problems: [],
    scratch: [],
  } as TreeDTO;
}

function toNodes(tree: TreeDTO): TreeNode[] {
  return (tree.nodes ?? []).map((n) =>
    n.kind === "folder"
      ? { kind: "folder", id: n.id, parentId: n.parentId ?? null, name: n.name, path: n.path, fileKind: n.fileKind, readOnly: n.readOnly, childIds: n.childIds ?? [] }
      : { kind: "request", id: n.id, parentId: n.parentId ?? null, name: n.name, path: n.path, fileKind: n.fileKind, readOnly: n.readOnly, method: n.method },
  );
}

function requestDTO(id: string, name: string, url = "https://api.test/orders"): RequestDTO {
  return {
    id,
    kind: "http",
    name,
    description: "",
    method: "GET",
    url,
    params: [],
    pathParams: [],
    headers: [],
    body: { mode: "", type: "", text: "", fields: [], parts: [], path: "", contentType: "", extra: "" },
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
    responseExamples: [
      { name: "Found", savedAt: "2026-09-23T10:15:02Z", request: null, statusCode: 200, statusText: "OK", headers: [], body: "{}", bodyEncoding: "", bodyClipped: false, bodySize: 0, extra: "" },
    ],
    auth: { type: "inherit", username: "", password: "", prefix: null, token: "", key: "", value: "", in: "", disabled: false, localSecrets: [], extra: "" },
    settings: { timeoutMs: null, followRedirects: null, maxRedirects: null, followOriginalMethod: null, forwardAuthorization: null, verifyTls: null, connectTimeoutMs: null, urlEncoding: "", httpVersion: "", sendCookies: null, storeCookies: null, tls: null, deadlineMs: null, extra: "" },
    extra: "",
  } as RequestDTO;
}

/** The project shell as App renders it (ProjectShell), for HTTP requests. */
function Project() {
  const activeId = useTabs((s) => s.activeId);
  const http = useCollection((s) => activeId !== null && s.nodes[activeId]?.kind === "request");
  return (
    <AppShell
      projectName="shop-api"
      projectPath="/p/shop"
      tabBar={<TabBar />}
      request={activeId !== null ? <TabPanel /> : <NoTabState />}
      response={activeId !== null && http ? <HttpResponsePanel key={activeId} id={activeId} graphql={false} /> : undefined}
    />
  );
}

async function renderProject(open: string[] = []): Promise<Stores> {
  const stores = createStores();
  stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: shop });
  const tree = treeWith();
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes: toNodes(tree), rootIds: tree.rootIds ?? [], readOnly: "", expandedIds: ["orders"] });
  if (open.length > 0) stores.tabs.dispatch({ type: "tabs/restore", order: open, activeId: open[0] ?? null });
  render(
    <StoreProvider stores={stores}>
      <Project />
    </StoreProvider>,
  );
  await act(settle);
  return stores;
}

const mod = IS_MAC ? { metaKey: true } : { ctrlKey: true };
/** Presses a key on the focused element, as the keyboard does. */
function press(key: string, init: KeyboardEventInit = {}): void {
  const target = document.activeElement ?? document.body;
  act(() => {
    fireEvent.keyDown(target, { key, ...init });
    fireEvent.keyUp(target, { key, ...init });
  });
}
/** Types into the focused text field (jsdom does not insert keys into inputs). */
function typeText(text: string): void {
  const el = document.activeElement;
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
    act(() => {
      fireEvent.change(el, { target: { value: text } });
    });
    return;
  }
  const view = el instanceof HTMLElement ? EditorView.findFromDOM(el) : null;
  if (view === null) throw new Error("typeText: focus is not in a text field");
  act(() => {
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text }, userEvent: "input.type" });
  });
}
/** Enter on the focused button: jsdom has no default actions, so the click a browser makes is made here. */
function activate(): void {
  const el = document.activeElement;
  press("Enter");
  if (el instanceof HTMLButtonElement) act(() => el.click());
}
const focused = () => document.activeElement as HTMLElement;
const describeFocus = () => `${roleOf(focused()) ?? focused().tagName} "${accessibleName(focused())}"`;

beforeEach(() => {
  localStorage.clear();
  resetFakeWails();
  resetHttpSends();
  resetResponseHistories();
  collection.Tree.mockReset().mockResolvedValue(treeWith());
  collection.CreateRequest.mockReset();
  collection.Request.mockReset().mockImplementation((id) =>
    Promise.resolve(requestDTO(id, NODES.find((n) => n.id === id)?.name ?? id)),
  );
  collection.UpdateRequest.mockReset().mockResolvedValue(undefined);
  collection.Flush.mockReset().mockResolvedValue(undefined);
  http.Send.mockReset().mockResolvedValue({ opId: "op_1" });
  servers.List.mockReset().mockResolvedValue([]);
  servers.Types.mockReset().mockResolvedValue([]);
  servers.Traffic.mockReset().mockResolvedValue([]);
});

afterEach(() => {
  vi.useRealTimers();
});

/** The view of a stored response (HTTPService.StoredResponse). */
function storedView() {
  return httpResponse({ statusCode: 404, status: "404 Not Found" });
}

/** Delivers a response to the send with operation id op_1. */
function respond() {
  act(() => {
    emit("http:op:data", { opId: "op_1", seq: 1, items: [httpResponse()] });
    emit("http:op:done", { opId: "op_1", seq: 2, status: "ok", error: null, items: 1, batches: 1, elapsedMs: 5 });
  });
}

describe("audit of the main screens (UI-11)", () => {
  it("app shell, tree, tab bar, request editor and response pane", async () => {
    await renderProject(["get", "health"]);
    expectAccessible();

    // Every sub-tab of the request editor.
    const sections = screen.getByRole("tablist", { name: "Request sections" });
    for (const tab of within(sections).getAllByRole("tab")) {
      act(() => tab.click());
      await act(settle);
      expectAccessible();
    }

    // The response, and every section of it.
    act(() => {
      fireEvent.click(screen.getByRole("button", { name: /^Send/ }));
    });
    await act(settle);
    respond();
    await act(settle);
    const response = screen.getByRole("tablist", { name: "Response sections" });
    for (const tab of within(response).getAllByRole("tab")) {
      act(() => tab.click());
      await act(settle);
      expectAccessible();
    }

    // An earlier response (HTTP-39) and an example (HTTP-38) in its place.
    const source = screen.getByRole("combobox", { name: "Response to show" });
    act(() => {
      fireEvent.change(source, { target: { value: "h:h_1" } });
    });
    await act(settle);
    expect(screen.getByText(/^Sent /)).toBeTruthy();
    expectAccessible();
    act(() => {
      fireEvent.change(source, { target: { value: "e:0" } });
    });
    await act(settle);
    expect(screen.getByText("Saved example")).toBeTruthy();
    expectAccessible();
  });

  it("the empty work area and the tree's context menu", async () => {
    await renderProject();
    expectAccessible();
    const tree = screen.getByRole("tree");
    const row = within(tree).getAllByRole("treeitem")[0]!;
    act(() => row.focus());
    press("F10", { shiftKey: true });
    expect(screen.getByRole("menu")).toBeTruthy();
    expectAccessible();
    press("Escape");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(focused()).toBe(row);
  });

  it("the history view with its filters and an entry's context menu", async () => {
    await renderProject(["get"]);
    fireEvent.click(screen.getByRole("button", { name: "History" }));
    await act(settle);
    const list = screen.getByRole("listbox", { name: "History entries" });
    expectAccessible();
    fireEvent.click(screen.getByRole("button", { name: "Show filters" }));
    expectAccessible();
    const option = within(list).getAllByRole("option")[0]!;
    act(() => option.focus());
    press("F10", { shiftKey: true });
    expect(screen.getByRole("menu", { name: "History entry" })).toBeTruthy();
    expectAccessible();
    press("Escape");
    expect(focused()).toBe(option);
  });

  it("every page of the settings screen", async () => {
    await renderProject(["get"]);
    press(",", mod);
    await act(settle);
    const nav = screen.getByRole("navigation", { name: "Settings" });
    for (const page of within(nav).getAllByRole("button")) {
      act(() => page.click());
      await act(settle);
      expect(screen.getByRole("heading", { level: 1 }).textContent).toBe(page.textContent);
      expectAccessible();
    }
  });

  it("dialogs, the command palette, the quick switcher and the shortcut help", async () => {
    await renderProject(["get"]);

    press("n", mod);
    expect(screen.getByRole("dialog", { name: "New request" })).toBeTruthy();
    expectAccessible();
    press("Escape");
    expect(screen.queryByRole("dialog")).toBeNull();

    press("p", { ...mod, shiftKey: true });
    expect(screen.getByRole("dialog", { name: "Commands" })).toBeTruthy();
    expectAccessible();
    press("Escape");
    expect(screen.queryByRole("dialog")).toBeNull();

    press("p", mod);
    expect(screen.getByRole("combobox", { name: /Jump to/ })).toBeTruthy();
    expectAccessible();
    press("Escape");

    press("/", mod);
    expect(screen.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeTruthy();
    expectAccessible();
    press("Escape");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it.each([
    ["mock HTTP", runningDTO],
    ["WebSocket", wsServerDTO],
    ["TCP", tcpServerDTO],
    ["UDP", udpServerDTO],
    ["SSE", sseServerDTO],
    ["webhook", webhookServerDTO],
    ["gRPC mock", grpcMockServerDTO],
  ])("the %s server's panels", async (_kind, make) => {
    const stores = createStores();
    const server = toLocalServer(make());
    stores.servers.dispatch({ type: "servers/loaded", servers: [server], types: [] });
    render(
      <StoreProvider stores={stores}>
        <ServerDetails server={stores.servers.getState().byId[server.id]!} />
        <ServerTraffic serverId={server.id} />
      </StoreProvider>,
    );
    await act(settle);
    expectAccessible();
  });
});

describe("what a screen reader is told (UI-11)", () => {
  it("the collection tree is a tree of levelled, positioned, selectable items", async () => {
    await renderProject();
    const tree = screen.getByRole("tree", { name: /Collection/ });
    const items = within(tree).getAllByRole("treeitem");
    expect(items.map((i) => [accessibleName(i), i.getAttribute("aria-level"), i.getAttribute("aria-posinset"), i.getAttribute("aria-setsize"), i.getAttribute("aria-expanded")])).toEqual([
      [expect.stringContaining("Orders"), "1", "1", "2", "true"],
      [expect.stringContaining("Get order"), "2", "1", "2", null],
      [expect.stringContaining("List orders"), "2", "2", "2", null],
      [expect.stringContaining("Health"), "1", "2", "2", null],
    ]);
    for (const item of items) expect(item.getAttribute("aria-selected")).toMatch(/^(true|false)$/);
    // One Tab stop: the rest are reached with the arrow keys.
    expect(items.filter((i) => i.tabIndex === 0)).toHaveLength(1);
  });

  it("every tab strip names its tabs, selects one and labels its panel with it", async () => {
    await renderProject(["get", "health"]);
    act(() => {
      fireEvent.click(screen.getByRole("button", { name: /^Send/ }));
    });
    await act(settle);
    respond();
    await act(settle);
    const lists = screen.getAllByRole("tablist");
    expect(lists.map((l) => accessibleName(l))).toEqual(["Open requests", "Request sections", "Response sections"]);
    for (const list of lists) {
      const tabs = within(list).getAllByRole("tab");
      const selected = tabs.filter((t) => t.getAttribute("aria-selected") === "true");
      expect(selected, accessibleName(list)).toHaveLength(1);
      expect(tabs.filter((t) => t.tabIndex === 0)).toEqual(selected);
      const panel = document.getElementById(selected[0]!.getAttribute("aria-controls") ?? "");
      expect(panel?.getAttribute("role")).toBe("tabpanel");
      expect(panel?.getAttribute("aria-labelledby")).toBe(selected[0]!.id);
      // Only tabs in a tab list: tools beside it are outside it.
      expect(within(list).queryAllByRole("button").filter((b) => b.getAttribute("role") !== "tab")).toEqual([]);
    }
  });

  it("announces toasts politely and shows errors as alerts", async () => {
    await renderProject(["get"]);
    const live = document.querySelector("[aria-live='polite']");
    expect(live).not.toBeNull();
    // A failed send is an alert in the response pane.
    http.Send.mockRejectedValueOnce({ kind: "network", code: "http.dial", message: "Could not connect.", hint: "", details: {}, causes: [] });
    act(() => {
      fireEvent.click(screen.getByRole("button", { name: /^Send/ }));
    });
    await act(settle);
    expect(screen.getByRole("alert").textContent).toContain("Could not connect.");
  });
});

describe("core flows with the keyboard alone (UI-11)", () => {
  it("creates a request, edits its URL, sends it and reads the response", async () => {
    const stores = await renderProject();

    // New request: ⌘N (Ctrl+N), type a name, Enter.
    press("n", mod);
    expect(screen.getByRole("dialog", { name: "New request" })).toBeTruthy();
    expect(roleOf(focused())).toBe("textbox");
    typeText("Create order");
    const created = dto({ id: "create", name: "Create order", path: "create-order.json" });
    collection.CreateRequest.mockResolvedValueOnce({ tree: treeWith([created]), nodeId: "create" });
    collection.Request.mockImplementation((id) => Promise.resolve(requestDTO(id, id === "create" ? "Create order" : id, "")));
    press("Enter");
    await act(settle);
    await act(settle);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(stores.tabs.getState().activeId).toBe("create");

    // Tab to the URL and type it.
    const url = tabTo(byRole("textbox", "URL"));
    typeText("https://api.test/orders");
    expect(EditorView.findFromDOM(url)?.state.doc.toString()).toBe("https://api.test/orders");

    // Send with ⌘↩ (Ctrl+Enter) from the URL.
    press("Enter", mod);
    await act(settle);
    expect(http.Send).toHaveBeenCalledWith({ requestId: "create" });
    respond();
    await act(settle);

    // Read it: Tab to the response sections, arrow to Headers.
    const responsePane = screen.getByRole("region", { name: "Response" });
    expect(responsePane.textContent).toContain("200");
    const bodyTab = tabTo((el) => roleOf(el) === "tab" && responsePane.contains(el));
    expect(accessibleName(bodyTab)).toMatch(/^Body/);
    press("ArrowRight");
    expect(accessibleName(focused())).toMatch(/^Headers/);
    expect(focused().getAttribute("aria-selected")).toBe("true");
    const panel = document.getElementById(focused().getAttribute("aria-controls") ?? "");
    expect(panel?.textContent).toContain("Content-Type");
    press("End");
    expect(focused().getAttribute("aria-selected")).toBe("true");
    press("Home");
    expect(accessibleName(focused())).toMatch(/^Body/);
  });

  it("navigates the tree, opens requests and switches tabs", async () => {
    const stores = await renderProject();
    const tree = screen.getByRole("tree");
    const first = tabTo((el) => roleOf(el) === "treeitem");
    expect(tree.contains(first)).toBe(true);
    expect(describeFocus()).toContain("Orders");

    press("ArrowDown");
    expect(describeFocus()).toContain("Get order");
    press("Enter");
    await act(settle);
    expect(stores.tabs.getState().activeId).toBe("get");

    // Type-ahead: "h" goes to Health; Left from a child goes to its folder.
    act(() => tree.querySelector<HTMLElement>("[role='treeitem'][tabindex='0']")?.focus());
    press("h");
    expect(describeFocus()).toContain("Health");
    press("Enter");
    await act(settle);
    expect(stores.tabs.getState().order).toEqual(["get", "health"]);
    press("ArrowUp");
    press("ArrowLeft");
    expect(describeFocus()).toContain("Orders");
    press("ArrowLeft");
    expect(focused().getAttribute("aria-expanded")).toBe("false");
    press("ArrowRight");
    expect(focused().getAttribute("aria-expanded")).toBe("true");

    // The tab bar: one Tab stop, arrows switch the active tab.
    const tab = tabTo(byRole("tab", /Health/));
    expect(tab.getAttribute("aria-selected")).toBe("true");
    press("ArrowLeft");
    expect(accessibleName(focused())).toMatch(/Get order/);
    expect(stores.tabs.getState().activeId).toBe("get");

    // The request's sections: arrows too.
    const params = tabTo(byRole("tab", /^Params/));
    expect(params.getAttribute("aria-selected")).toBe("true");
    press("ArrowRight");
    expect(accessibleName(focused())).toMatch(/^Headers/);
    expect(screen.getByRole("grid", { name: "Headers" })).toBeTruthy();
  });

  it("opens the settings, moves between pages and comes back", async () => {
    await renderProject(["get"]);
    act(() => screen.getByRole("textbox", { name: "URL" }).focus());
    press(",", mod);
    await act(settle);
    // Focus starts on the current page instead of being lost in the hidden work area.
    expect(focused().getAttribute("aria-current")).toBe("page");
    expect(accessibleName(focused())).toBe("General");
    tabTo(byRole("button", "Network"));
    activate();
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Network");
    tabTo(byRole("textbox", "Default timeout"));

    // Back to the work area with the sidebar toggle (⌘B, Ctrl+B).
    press("b", mod);
    await act(settle);
    expect(screen.queryByRole("navigation", { name: "Settings" })).toBeNull();
    // Focus is back in the work area, and Tab goes on from there.
    expect(screen.getByRole("main").contains(focused()) || focused() === screen.getByRole("main")).toBe(true);
    pressTab();
    expect(screen.getByRole("main").contains(focused())).toBe(true);
  });

  it("runs a command from the command palette and gets focus back", async () => {
    await renderProject(["get"]);
    const url = screen.getByRole("textbox", { name: "URL" });
    act(() => url.focus());
    press("p", { ...mod, shiftKey: true });
    const field = focused();
    expect(roleOf(field)).toBe("combobox");
    typeText("hide the sidebar");
    const listbox = document.getElementById(field.getAttribute("aria-controls") ?? "");
    expect(listbox?.getAttribute("role")).toBe("listbox");
    const active = document.getElementById(field.getAttribute("aria-activedescendant") ?? "");
    expect(active?.textContent).toMatch(/sidebar/i);
    press("Enter");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByRole("complementary")).toBeNull();
    expect(focused()).toBe(url);

    // Escape closes it without running anything, focus back where it was.
    press("p", { ...mod, shiftKey: true });
    press("Escape");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(focused()).toBe(url);
  });

  it("keeps Tab inside an open dialog", async () => {
    await renderProject(["get"]);
    press("n", mod);
    const dialog = screen.getByRole("dialog", { name: "New request" });
    const seen = new Set<Element>();
    for (let i = 0; i < 30; i++) {
      pressTab();
      seen.add(focused());
      expect(dialog.contains(focused()), describeFocus()).toBe(true);
    }
    for (let i = 0; i < 5; i++) {
      pressTab(true);
      expect(dialog.contains(focused())).toBe(true);
    }
    expect(seen.size).toBeGreaterThan(3);
  });
});
