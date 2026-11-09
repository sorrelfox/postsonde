import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { CollectionTree, TREE_FALLBACK_VIEWPORT, TREE_OVERSCAN, TREE_ROW_HEIGHT } from "./CollectionTree";

type Init = { id: string; parentId?: string | null; name?: string };

const folder = ({ id, parentId = null, name = id }: Init, childIds: string[]): TreeNode => ({
  kind: "folder",
  id,
  parentId,
  name,
  path: id,
  fileKind: "",
  readOnly: "",
  childIds,
});
const request = ({ id, parentId = null, name = id }: Init, method = "GET", fileKind = "http"): TreeNode => ({
  kind: "request",
  id,
  parentId,
  name,
  path: `${id}.json`,
  fileKind,
  readOnly: "",
  method,
});

// Shop API/ { Catalog/ { List products, Create product, Products (GQL), Media/ { Upload image } }, Stream (grpc), Broken (invalid) }, Internal/ { Health }
const NODES: TreeNode[] = [
  folder({ id: "shop", name: "Shop API" }, ["catalog", "stream", "broken"]),
  folder({ id: "catalog", parentId: "shop", name: "Catalog" }, ["list", "create", "gql", "media"]),
  request({ id: "list", parentId: "catalog", name: "List products" }),
  request({ id: "create", parentId: "catalog", name: "Create product" }, "POST"),
  request({ id: "gql", parentId: "catalog", name: "Products" }, "POST", "graphql"),
  folder({ id: "media", parentId: "catalog", name: "Media" }, ["upload"]),
  request({ id: "upload", parentId: "media", name: "Upload image" }, "POST"),
  { kind: "unknown", id: "stream", parentId: "shop", name: "Stream prices", path: "stream.json", fileKind: "grpc", readOnly: "unknown_kind" },
  { kind: "invalid", id: "broken", parentId: "shop", name: "broken", path: "broken.json", fileKind: "", readOnly: "invalid" },
  folder({ id: "internal", name: "Internal" }, ["health"]),
  request({ id: "health", parentId: "internal", name: "Health" }),
];

function setup(nodes: TreeNode[] = NODES, rootIds = ["shop", "internal"], expandedIds = ["shop", "catalog"]) {
  const stores: Stores = createStores();
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({
    type: "collection/load",
    workspaceId: "ws_1",
    nodes,
    rootIds,
    expandedIds,
    problems: [{ path: "broken.json", nodeId: "broken", code: "projectfile.syntax", message: "Not valid JSON at line 3" }],
  });
  const view = render(
    <StoreProvider stores={stores}>
      <CollectionTree label="Collections" />
    </StoreProvider>,
  );
  return { stores, view };
}

const items = () => screen.queryAllByRole("treeitem");
const names = () => items().map((el) => el.getAttribute("data-node-id"));
const item = (id: string) => document.querySelector(`[data-node-id="${id}"]`) as HTMLElement;
const focused = () => (document.activeElement as HTMLElement | null)?.getAttribute("data-node-id");
const press = (key: string) => fireEvent.keyDown(document.activeElement as Element, { key });

describe("CollectionTree", () => {
  it("badges a folder while its saved server listens", () => {
    const { stores } = setup();
    const saved = {
      id: "srv_1",
      type: "websocket",
      typeLabel: "WebSocket",
      name: "WebSocket",
      host: "",
      port: 8081,
      allowLan: false,
      tls: null,
      certificate: null,
      state: "stopped" as const,
      addresses: [],
      boundPort: 0,
      scope: "",
      warning: null,
      startedAt: null,
      stoppedAt: null,
      error: null,
      events: 0,
      mock: null,
      webSocket: null,
      webhook: null,
      sse: null,
      udp: null,
      grpcMock: null,
      socketIo: null,
      graphqlMock: null,
      diagnostics: [],
      folder: { workspaceId: "ws_1", folderId: "catalog", folderName: "Catalog" },
    };
    act(() => stores.servers.dispatch({ type: "servers/loaded", servers: [saved], types: [] }));
    expect(item("catalog").textContent).not.toContain("WS :");
    act(() =>
      stores.servers.dispatch({ type: "servers/set", servers: [{ ...saved, state: "running", boundPort: 8081, addresses: ["127.0.0.1:8081"] }] }),
    );
    expect(item("catalog").textContent).toContain("WS :8081");
    expect(item("catalog").getAttribute("aria-label")).toContain("server listening WS :8081");
    expect(item("shop").textContent).not.toContain("WS :");
  });

  it("renders nested folders with item counts and ARIA tree attributes", () => {
    setup();
    const tree = screen.getByRole("tree", { name: "Collections" });
    expect(tree).toBeTruthy();
    expect(names()).toEqual(["shop", "catalog", "list", "create", "gql", "media", "stream", "broken", "internal"]);

    const shop = item("shop");
    expect(shop.getAttribute("aria-level")).toBe("1");
    expect(shop.getAttribute("aria-expanded")).toBe("true");
    expect(shop.getAttribute("aria-setsize")).toBe("2");
    expect(shop.getAttribute("aria-posinset")).toBe("1");
    // Counts are recursive: every item below, not folders.
    expect(shop.getAttribute("aria-label")).toBe("Shop API, 6 items");
    expect(within(shop).getByText("6")).toBeTruthy();
    expect(within(item("media")).getByText("1")).toBeTruthy();
    expect(item("media").getAttribute("aria-expanded")).toBe("false");
    expect(item("upload")).toBeNull();

    const list = item("list");
    expect(list.getAttribute("aria-level")).toBe("3");
    expect(list.getAttribute("aria-posinset")).toBe("1");
    expect(list.getAttribute("aria-setsize")).toBe("4");
    expect(list.hasAttribute("aria-expanded")).toBe(false);
    // Deeper rows are indented further.
    expect(parseInt(list.style.paddingLeft)).toBeGreaterThan(parseInt(item("catalog").style.paddingLeft));
  });

  it("shows method and kind badges for mixed-protocol items, and warns on invalid ones", () => {
    setup();
    const badge = (id: string) => item(id).querySelector("[data-tone]") as HTMLElement;
    expect([badge("list").textContent, badge("list").dataset.tone]).toEqual(["GET", "get"]);
    expect([badge("create").textContent, badge("create").dataset.tone]).toEqual(["POST", "post"]);
    expect([badge("gql").textContent, badge("gql").dataset.tone]).toEqual(["GQL", "gql"]);
    expect([badge("stream").textContent, badge("stream").dataset.tone]).toEqual(["GRPC", "dir"]);
    expect(item("stream").getAttribute("title")).toMatch(/read-only/);
    expect(badge("broken").dataset.tone).toBe("warn");
    expect(item("broken").getAttribute("title")).toBe("Not valid JSON at line 3");
    expect(item("broken").getAttribute("aria-description")).toBe("Not valid JSON at line 3");
    expect(item("list").getAttribute("aria-label")).toBe("GET List products");
  });

  it("toggles folders and opens requests with the mouse", () => {
    const { stores } = setup();
    fireEvent.click(item("media"));
    expect(item("upload")).not.toBeNull();
    expect(stores.collection.getState().selectedId).toBe("media");

    // A click opens the preview tab, which the next click replaces.
    fireEvent.click(item("upload"));
    expect(stores.tabs.getState()).toMatchObject({ order: ["upload"], previewId: "upload" });
    expect(item("upload").getAttribute("aria-selected")).toBe("true");

    // The chevron only toggles.
    fireEvent.click(item("catalog").firstElementChild as Element);
    expect(item("list")).toBeNull();
    expect(stores.collection.getState().selectedId).toBe("upload");
  });

  it("opens a folder's DIR tab on double-click and leaves its expansion as it was", () => {
    const { stores } = setup();
    // A real double-click is two clicks (each toggles) and then dblclick.
    fireEvent.click(item("media"));
    fireEvent.click(item("media"));
    fireEvent.doubleClick(item("media"));
    expect(item("media").getAttribute("aria-expanded")).toBe("false");
    expect(stores.tabs.getState()).toMatchObject({ order: ["media"], activeId: "media" });
    expect(stores.collection.getState().selectedId).toBe("media");
    // A double-click on a request opens it once, as a kept tab.
    fireEvent.click(item("list"));
    fireEvent.click(item("list"));
    expect(stores.tabs.getState()).toMatchObject({ order: ["media", "list"], previewId: "list" });
    fireEvent.doubleClick(item("list"));
    expect(stores.tabs.getState()).toMatchObject({ order: ["media", "list"], activeId: "list", previewId: null });
  });

  it("moves a roving tabindex with the arrow keys, Home and End", () => {
    const { stores } = setup();
    const tabbable = items().filter((el) => el.tabIndex === 0);
    expect(tabbable.map((el) => el.dataset.nodeId)).toEqual(["shop"]);
    act(() => item("shop").focus());

    press("ArrowDown");
    expect(focused()).toBe("catalog");
    expect(item("catalog").tabIndex).toBe(0);
    expect(item("shop").tabIndex).toBe(-1);

    press("ArrowRight"); // expanded: to the first child
    expect(focused()).toBe("list");
    press("ArrowRight"); // not a folder: nothing
    expect(focused()).toBe("list");
    press("ArrowLeft"); // to the parent
    expect(focused()).toBe("catalog");
    press("ArrowLeft"); // collapse
    expect(item("catalog").getAttribute("aria-expanded")).toBe("false");
    expect(focused()).toBe("catalog");
    press("ArrowRight"); // expand
    expect(item("catalog").getAttribute("aria-expanded")).toBe("true");
    expect(focused()).toBe("catalog");

    press("End");
    expect(focused()).toBe("internal");
    press("ArrowDown"); // stays on the last row
    expect(focused()).toBe("internal");
    press("ArrowUp");
    expect(focused()).toBe("broken");
    press("Home");
    expect(focused()).toBe("shop");
    press("ArrowUp");
    expect(focused()).toBe("shop");

    // Space on a folder toggles it; on a request it opens the preview tab, like a click.
    press("End");
    press(" ");
    expect(item("health")).not.toBeNull();
    expect(stores.tabs.getState().order).toEqual([]);
    press("ArrowDown");
    press(" ");
    expect(stores.tabs.getState()).toMatchObject({ order: ["health"], previewId: "health" });
    expect(stores.collection.getState().selectedId).toBe("health");
    press("Enter"); // Enter keeps it.
    expect(stores.tabs.getState()).toMatchObject({ order: ["health"], previewId: null });
    // Enter opens a request's tab, and a folder's DIR tab without toggling it.
    press("ArrowUp");
    press("Enter");
    expect(stores.tabs.getState().order).toEqual(["health", "internal"]);
    expect(stores.tabs.getState().activeId).toBe("internal");
    expect(item("internal").getAttribute("aria-expanded")).toBe("true");
    expect(stores.collection.getState().selectedId).toBe("internal");
  });

  it("finds rows by typing the start of their name", () => {
    let now = 10_000;
    const clock = vi.spyOn(Date, "now").mockImplementation(() => now);
    setup();
    act(() => item("shop").focus());
    press("m");
    expect(focused()).toBe("media");
    press("e"); // "me": still Media
    expect(focused()).toBe("media");
    now += 1000; // a pause starts a new search
    press("i");
    expect(focused()).toBe("internal");
    press("s"); // "is": no match, focus stays
    expect(focused()).toBe("internal");
    clock.mockRestore();
  });

  it("moves focus to the folder when a collapse hides the focused row", () => {
    const { stores } = setup();
    act(() => item("shop").focus());
    press("ArrowDown");
    press("ArrowDown");
    expect(focused()).toBe("list");
    act(() => stores.collection.dispatch({ type: "collection/setExpanded", id: "catalog", expanded: false }));
    expect(item("catalog").tabIndex).toBe(0);
  });
});

describe("CollectionTree virtualization", () => {
  // 50 top-level folders × 100 requests = 5,050 rows when all expanded.
  function bigTree() {
    const nodes: TreeNode[] = [];
    const rootIds: string[] = [];
    for (let f = 0; f < 50; f++) {
      const id = `f${f}`;
      const childIds: string[] = [];
      for (let r = 0; r < 100; r++) {
        const rid = `${id}-r${r}`;
        childIds.push(rid);
        nodes.push(request({ id: rid, parentId: id, name: `Request ${f}.${r}` }, r % 2 === 0 ? "GET" : "POST"));
      }
      nodes.push(folder({ id, name: `Folder ${f}` }, childIds));
      rootIds.push(id);
    }
    return { nodes, rootIds };
  }

  it("keeps the DOM bounded for a 5,000-item tree and keeps focus across the window", () => {
    const { nodes, rootIds } = bigTree();
    const started = performance.now();
    setup(nodes, rootIds, rootIds);
    const elapsed = performance.now() - started;

    const tree = screen.getByRole("tree");
    const sizer = tree.firstElementChild as HTMLElement;
    expect(sizer.style.height).toBe(`${5050 * TREE_ROW_HEIGHT}px`);
    const bound = Math.ceil(TREE_FALLBACK_VIEWPORT / TREE_ROW_HEIGHT) + 2 * TREE_OVERSCAN + 1;
    expect(items().length).toBeLessThanOrEqual(bound);
    expect(items().length).toBeLessThan(5050 / 50);
    // Generous: a render that walked every row would be far slower in jsdom.
    expect(elapsed).toBeLessThan(2000);

    act(() => item("f0").focus());
    press("End");
    expect(focused()).toBe("f49-r99");
    const last = item("f49-r99");
    expect(last.getAttribute("aria-posinset")).toBe("100");
    expect(parseInt(last.style.top)).toBe(5049 * TREE_ROW_HEIGHT);
    expect(item("f0")).toBeNull();
    expect(items().length).toBeLessThanOrEqual(bound);

    press("Home");
    expect(focused()).toBe("f0");
    // Walk down past the rendered window: every step stays focused and rendered.
    for (let i = 0; i < 60; i++) press("ArrowDown");
    expect(focused()).toBe("f0-r59");
    expect(items().length).toBeLessThanOrEqual(bound + 1);

    press("ArrowLeft"); // to the parent, far above the window
    expect(focused()).toBe("f0");
    press("PageDown");
    expect(Number(item(focused() as string).getAttribute("aria-posinset"))).toBeGreaterThan(1);
  });

  it("keeps the focused row rendered when it is scrolled out of view", () => {
    const { nodes, rootIds } = bigTree();
    setup(nodes, rootIds, rootIds);
    act(() => item("f0").focus());
    press("ArrowDown");
    const tree = screen.getByRole("tree");
    fireEvent.scroll(tree, { target: { scrollTop: 3000 * TREE_ROW_HEIGHT } });
    expect(item("f0-r0")).not.toBeNull();
    expect(focused()).toBe("f0-r0");
    expect(item("f29-r73")).not.toBeNull(); // row 3000
    press("ArrowDown");
    expect(focused()).toBe("f0-r1");
  });
});
