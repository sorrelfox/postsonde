import { describe, expect, it } from "vitest";
import {
  collectionReducer,
  countItems,
  DELETED_READ_ONLY,
  flattenVisibleRows,
  selectIsDeleted,
  selectNodeProblem,
  initialCollectionState,
  selectIsExpanded,
  selectRequestIdsInSubtree,
  selectSubtreeIds,
  selectSortMode,
  sortIdsByName,
  TOP_LEVEL,
  type CollectionAction,
  type CollectionState,
  type FolderNode,
  type RequestNode,
  type TreeNode,
} from "./collection";

const folder = (id: string, parentId: string | null, childIds: string[]): FolderNode => ({
  kind: "folder",
  id,
  parentId,
  name: id,
  path: id,
  fileKind: "",
  readOnly: "",
  childIds,
});
const request = (id: string, parentId: string | null): RequestNode => ({
  kind: "request",
  id,
  parentId,
  name: id,
  path: `${id}.json`,
  fileKind: "http",
  readOnly: "",
  method: "GET",
});

// api/ { list, orders/ { get } }, top-level health
const NODES: TreeNode[] = [
  folder("api", null, ["list", "orders"]),
  request("list", "api"),
  folder("orders", "api", ["get"]),
  request("get", "orders"),
  request("health", null),
];

const reduce = (state: CollectionState, action: CollectionAction) => collectionReducer(state, action);
const loaded = () => reduce(initialCollectionState, { type: "collection/load", nodes: NODES, rootIds: ["api", "health"] });

describe("collectionReducer", () => {
  it("loads a tree, keeping expansion and selection only for nodes that still exist", () => {
    const s = loaded();
    expect(Object.keys(s.nodes)).toHaveLength(5);
    expect(s.rootIds).toEqual(["api", "health"]);

    const before: CollectionState = { ...s, expandedIds: { api: true, gone: true }, selectedId: "gone" };
    const after = reduce(before, { type: "collection/load", nodes: NODES, rootIds: ["api", "health"] });
    expect(after.expandedIds).toEqual({ api: true });
    expect(after.selectedId).toBeNull();
  });

  it("adds nodes at the top level or under a folder, and ignores bad parents and duplicates", () => {
    const s = loaded();
    const top = reduce(s, { type: "collection/add", node: request("new", null), index: 0 });
    expect(top.rootIds).toEqual(["new", "api", "health"]);

    const child = reduce(s, { type: "collection/add", node: request("post", "orders") });
    expect((child.nodes.orders as FolderNode).childIds).toEqual(["get", "post"]);

    expect(reduce(s, { type: "collection/add", node: request("x", "list") })).toBe(s);
    expect(reduce(s, { type: "collection/add", node: request("x", "missing") })).toBe(s);
    expect(reduce(s, { type: "collection/add", node: request("health", null) })).toBe(s);
  });

  it("renames a node", () => {
    const s = loaded();
    expect(reduce(s, { type: "collection/rename", id: "list", name: "List all" }).nodes.list?.name).toBe("List all");
    expect(reduce(s, { type: "collection/rename", id: "list", name: "list" })).toBe(s);
    expect(reduce(s, { type: "collection/rename", id: "missing", name: "x" })).toBe(s);
  });

  it("sets a request's method", () => {
    const s = loaded();
    expect(reduce(s, { type: "collection/setMethod", id: "list", method: "POST" }).nodes.list).toMatchObject({ method: "POST" });
    expect(reduce(s, { type: "collection/setMethod", id: "list", method: "GET" })).toBe(s);
    expect(reduce(s, { type: "collection/setMethod", id: "api", method: "POST" })).toBe(s);
    expect(reduce(s, { type: "collection/setMethod", id: "missing", method: "POST" })).toBe(s);
  });

  it("removes a subtree and cleans up its parent, expansion and selection", () => {
    const s: CollectionState = { ...loaded(), expandedIds: { api: true, orders: true }, selectedId: "get" };
    const after = reduce(s, { type: "collection/remove", id: "orders" });
    expect(Object.keys(after.nodes).sort()).toEqual(["api", "health", "list"]);
    expect((after.nodes.api as FolderNode).childIds).toEqual(["list"]);
    expect(after.expandedIds).toEqual({ api: true });
    expect(after.selectedId).toBeNull();

    const top = reduce(s, { type: "collection/remove", id: "health" });
    expect(top.rootIds).toEqual(["api"]);
    expect(top.selectedId).toBe("get");
    expect(reduce(s, { type: "collection/remove", id: "missing" })).toBe(s);
  });

  it("expands only folders, and toggles", () => {
    const s = loaded();
    const open = reduce(s, { type: "collection/setExpanded", id: "api", expanded: true });
    expect(selectIsExpanded(open, "api")).toBe(true);
    expect(reduce(open, { type: "collection/setExpanded", id: "api", expanded: true })).toBe(open);
    expect(reduce(s, { type: "collection/setExpanded", id: "list", expanded: true })).toBe(s);
    expect(selectIsExpanded(reduce(open, { type: "collection/toggleExpanded", id: "api" }), "api")).toBe(false);
  });

  it("selects known nodes and clears the selection", () => {
    const s = loaded();
    const selected = reduce(s, { type: "collection/select", id: "list" });
    expect(selected.selectedId).toBe("list");
    expect(reduce(selected, { type: "collection/select", id: "missing" })).toBe(selected);
    expect(reduce(selected, { type: "collection/select", id: null }).selectedId).toBeNull();
    expect(reduce(s, { type: "collection/select", id: null })).toBe(s);
  });

  it("keeps requests deleted outside PostSonde for their tabs until forgotten (STO-05)", () => {
    const s = reduce(loaded(), { type: "collection/select", id: "list" });
    const without = NODES.filter((n) => n.id !== "list").map((n) => (n.id === "api" ? folder("api", null, ["orders"]) : n));
    const after = reduce(s, { type: "collection/load", nodes: without, rootIds: ["api", "health"], deleted: [request("list", "api")] });
    expect(after.deletedIds).toEqual(["list"]);
    expect(after.nodes.list).toMatchObject({ readOnly: DELETED_READ_ONLY, parentId: "api" });
    expect((after.nodes.api as FolderNode).childIds).toEqual(["orders"]);
    expect(selectIsDeleted(after, "list")).toBe(true);
    expect(after.selectedId).toBe("list");

    // Kept across reloads of the same project, until the file is back.
    const again = reduce(after, { type: "collection/load", nodes: without, rootIds: ["api", "health"] });
    expect(again.deletedIds).toEqual(["list"]);
    const back = reduce(again, { type: "collection/load", nodes: NODES, rootIds: ["api", "health"] });
    expect(back.deletedIds).toEqual([]);
    expect(back.nodes.list?.readOnly).toBe("");
    // Not for another project.
    expect(reduce(again, { type: "collection/load", workspaceId: "ws_other", nodes: without, rootIds: [] }).deletedIds).toEqual([]);

    const forgotten = reduce(again, { type: "collection/forgetDeleted", ids: ["list", "health"] });
    expect(forgotten.deletedIds).toEqual([]);
    expect(forgotten.nodes.list).toBeUndefined();
    expect(forgotten.nodes.health).toBeDefined();
    expect(forgotten.selectedId).toBeNull();
    expect(reduce(forgotten, { type: "collection/forgetDeleted", ids: ["list"] })).toBe(forgotten);
  });
});

describe("collection selectors", () => {
  it("lists a subtree depth first, and its requests", () => {
    const s = loaded();
    expect(selectSubtreeIds(s, "api")).toEqual(["api", "list", "orders", "get"]);
    expect(selectSubtreeIds(s, "missing")).toEqual([]);
    expect(selectRequestIdsInSubtree(s, "api")).toEqual(["list", "get"]);
  });
});

describe("collectionReducer: loading per workspace", () => {
  it("starts loading, loads, fails and clears", () => {
    const loading = reduce(initialCollectionState, { type: "collection/loading", workspaceId: "ws_1" });
    expect(loading.status).toBe("loading");
    expect(loading.workspaceId).toBe("ws_1");

    const ready = reduce(loading, {
      type: "collection/load",
      workspaceId: "ws_1",
      nodes: NODES,
      rootIds: ["api", "health"],
      readOnly: "newer_version",
      problems: [{ path: "x.json", nodeId: "list", code: "c", message: "broken" }],
      expandedIds: ["api", "list", "gone"],
    });
    expect(ready.status).toBe("ready");
    expect(ready.readOnly).toBe("newer_version");
    // Only folders that exist are expanded.
    expect(ready.expandedIds).toEqual({ api: true });
    expect(selectNodeProblem(ready, "list")?.message).toBe("broken");
    expect(selectNodeProblem(ready, "api")).toBeUndefined();

    // Loading the same workspace again keeps the tree on screen.
    expect(reduce(ready, { type: "collection/loading", workspaceId: "ws_1" })).toBe(ready);
    // A reload without expandedIds keeps the current expansion.
    const reloaded = reduce(ready, { type: "collection/load", workspaceId: "ws_1", nodes: NODES, rootIds: ["api", "health"] });
    expect(reloaded.expandedIds).toEqual({ api: true });

    // Another workspace starts from scratch.
    const other = reduce(ready, { type: "collection/loading", workspaceId: "ws_2" });
    expect(other.nodes).toEqual({});
    expect(other.expandedIds).toEqual({});
    expect(reduce(other, { type: "collection/failed", workspaceId: "ws_1", error: "late" })).toBe(other);
    const failed = reduce(other, { type: "collection/failed", workspaceId: "ws_2", error: "boom" });
    expect(failed.status).toBe("error");
    expect(failed.error).toBe("boom");

    expect(reduce(failed, { type: "collection/clear" })).toBe(initialCollectionState);
    expect(reduce(initialCollectionState, { type: "collection/clear" })).toBe(initialCollectionState);
  });
});

describe("countItems", () => {
  it("counts the items of each folder's whole subtree, not folders", () => {
    const s = loaded();
    expect(countItems(s.nodes, s.rootIds)).toEqual({ api: 2, orders: 1 });
    const withEmpty = reduce(s, { type: "collection/add", node: folder("empty", "orders", []) });
    expect(countItems(withEmpty.nodes, withEmpty.rootIds)).toEqual({ api: 2, orders: 1, empty: 0 });
  });
});

describe("flattenVisibleRows", () => {
  it("lists only rows under expanded folders, with ARIA level, set size and position", () => {
    const s = loaded();
    expect(flattenVisibleRows(s.nodes, s.rootIds, {}).map((r) => r.id)).toEqual(["api", "health"]);

    const rows = flattenVisibleRows(s.nodes, s.rootIds, { api: true, orders: true });
    expect(rows.map((r) => [r.id, r.level, r.posInSet, r.setSize, r.parentIndex, r.expanded])).toEqual([
      ["api", 1, 1, 2, -1, true],
      ["list", 2, 1, 2, 0, false],
      ["orders", 2, 2, 2, 0, true],
      ["get", 3, 1, 1, 2, false],
      ["health", 1, 2, 2, -1, false],
    ]);

    // An expanded folder inside a collapsed one stays hidden.
    expect(flattenVisibleRows(s.nodes, s.rootIds, { orders: true }).map((r) => r.id)).toEqual(["api", "health"]);
  });

  it("sorts the folders sorted by name, folders first, and shows only the visible nodes", () => {
    const s = loaded();
    const expanded = { api: true, orders: true } as const;
    // api is sorted: orders (a folder) before list; the top level keeps its manual order.
    expect(flattenVisibleRows(s.nodes, s.rootIds, expanded, { sortedByName: { api: true } }).map((r) => r.id)).toEqual([
      "api",
      "orders",
      "get",
      "list",
      "health",
    ]);
    const rows = flattenVisibleRows(s.nodes, s.rootIds, expanded, { visible: new Set(["api", "orders", "get"]) });
    expect(rows.map((r) => [r.id, r.posInSet, r.setSize])).toEqual([
      ["api", 1, 1],
      ["orders", 1, 1],
      ["get", 1, 1],
    ]);
  });
});

describe("sorting by name (COL-05)", () => {
  const named = (id: string, name: string, kind: "folder" | "request" = "request"): TreeNode =>
    kind === "folder" ? { ...folder(id, null, []), name } : { ...request(id, null), name };

  it("orders folders first, then by name ignoring case, with numbers in numeric order", () => {
    const nodes: Record<string, TreeNode> = Object.fromEntries(
      [named("s10", "Step 10"), named("s2", "step 2"), named("z", "Zeta", "folder"), named("a", "alpha"), named("b", "Beta", "folder")].map((n) => [
        n.id,
        n,
      ]),
    );
    expect(sortIdsByName(nodes, ["s10", "s2", "z", "a", "b", "gone"])).toEqual(["b", "z", "a", "s2", "s10"]);
  });

  it("sets a folder's sort mode, keeps it across reloads and drops it for removed folders", () => {
    let s = loaded();
    expect(selectSortMode(s, "api")).toBe("manual");
    s = reduce(s, { type: "collection/setSort", parentId: "api", mode: "name" });
    s = reduce(s, { type: "collection/setSort", parentId: null, mode: "name" });
    expect(s.sortedByName).toEqual({ api: true, [TOP_LEVEL]: true });
    expect(selectSortMode(s, null)).toBe("name");
    // Not a folder: ignored. Same mode again: the same state.
    expect(reduce(s, { type: "collection/setSort", parentId: "list", mode: "name" })).toBe(s);
    expect(reduce(s, { type: "collection/setSort", parentId: "api", mode: "name" })).toBe(s);

    const reloaded = reduce(s, { type: "collection/load", nodes: NODES, rootIds: ["api", "health"] });
    expect(reloaded.sortedByName).toEqual({ api: true, [TOP_LEVEL]: true });
    const other = reduce(s, { type: "collection/load", workspaceId: "ws_2", nodes: NODES, rootIds: ["api", "health"], sortedByName: ["orders", "gone", "list"] });
    expect(other.sortedByName).toEqual({ orders: true });

    expect(reduce(s, { type: "collection/remove", id: "api" }).sortedByName).toEqual({ [TOP_LEVEL]: true });
    expect(reduce(s, { type: "collection/setSort", parentId: "api", mode: "manual" }).sortedByName).toEqual({ [TOP_LEVEL]: true });
  });
});
