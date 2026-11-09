import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Tree: vi.fn<() => Promise<unknown>>(),
  Reload: vi.fn<() => Promise<unknown>>(),
  CreateFolder: vi.fn<(input: unknown) => Promise<unknown>>(),
  CreateRequest: vi.fn<(input: unknown) => Promise<unknown>>(),
  Rename: vi.fn<(input: unknown) => Promise<unknown>>(),
  Duplicate: vi.fn<(id: string) => Promise<unknown>>(),
  Delete: vi.fn<(id: string) => Promise<unknown>>(),
  Restore: vi.fn<(trashId: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));

import { COLLECTION_CHANGED_EVENT, type NodeDTO, type TreeDTO } from "../lib/collection";
import { localStorageExpansion, TREE_EXPANSION_KEY_PREFIX, type ExpansionPersistence } from "../lib/treeExpansion";
import { createStores, type Stores, type WorkspaceInfo } from "../stores";
import { deferred, emit, listenerCount, resetFakeWails, settle } from "../test/fakeWails";
import {
  createItem,
  deleteNode,
  duplicateNode,
  EXPANSION_SAVE_DELAY_MS,
  followCollection,
  loadCollection,
  openTreeItem,
  renameNode,
  restoreNode,
  setExpansionPersistence,
  toTreeNode,
} from "./collection";

const shop: WorkspaceInfo = {
  id: "ws_1",
  name: "Shop",
  path: "/p/shop",
  localDir: "/data/ws_1",
  localStatus: "existing",
  previousPath: "",
  newerFormat: false,
};

function node(over: Partial<NodeDTO> & { id: string }): NodeDTO {
  return {
    parentId: null,
    kind: "request",
    name: over.id,
    path: `${over.id}.json`,
    fileKind: "http",
    method: "GET",
    childIds: [],
    readOnly: "",
    ...over,
  };
}

// api/ { list, grpc (unknown), orders/ { broken (invalid) } }, health
function tree(over: Partial<TreeDTO> = {}): TreeDTO {
  return {
    workspaceId: "ws_1",
    path: "/p/shop",
    readOnly: "",
    rootIds: ["api", "health"],
    nodes: [
      node({ id: "api", kind: "folder", fileKind: "folder", method: "", childIds: ["list", "grpc", "orders"], path: "api" }),
      node({ id: "list", parentId: "api" }),
      node({ id: "grpc", parentId: "api", kind: "unknown", fileKind: "grpc", method: "", readOnly: "unknown_kind" }),
      node({ id: "orders", parentId: "api", kind: "folder", fileKind: "folder", method: "", childIds: ["broken"] }),
      node({ id: "broken", parentId: "orders", kind: "invalid", fileKind: "", method: "", readOnly: "invalid" }),
      node({ id: "health", method: "HEAD" }),
    ],
    problems: [
      {
        path: "api/orders/broken.json",
        nodeId: "broken",
        error: { kind: "invalid", code: "projectfile.syntax", message: "Not valid JSON", hint: "", details: {}, causes: [] },
      },
    ],
    ...over,
  } as TreeDTO;
}

function memoryExpansion(initial: Record<string, string[]> = {}): ExpansionPersistence & { saved: Record<string, string[]> } {
  const saved: Record<string, string[]> = { ...initial };
  return {
    saved,
    load: (id) => saved[id] ?? null,
    save: (id, ids) => {
      saved[id] = [...ids];
    },
  };
}

function openStores(workspace: WorkspaceInfo | null = shop): Stores {
  const stores = createStores();
  stores.workspace.dispatch({ type: "workspace/setCurrent", workspace });
  return stores;
}

let restore: ExpansionPersistence;

beforeEach(() => {
  resetFakeWails();
  service.Tree.mockReset();
  service.Tree.mockResolvedValue(tree());
  restore = setExpansionPersistence(memoryExpansion());
});

afterEach(() => {
  setExpansionPersistence(restore);
  vi.useRealTimers();
});

describe("toTreeNode", () => {
  it("maps folders, requests, unknown and invalid items", () => {
    const nodes = (tree().nodes ?? []).map(toTreeNode);
    expect(nodes.map((n) => n.kind)).toEqual(["folder", "request", "unknown", "folder", "invalid", "request"]);
    expect(nodes[0]).toMatchObject({ childIds: ["list", "grpc", "orders"], parentId: null, path: "api" });
    expect(nodes[1]).toMatchObject({ method: "GET", parentId: "api", fileKind: "http" });
    expect(nodes[2]).toMatchObject({ fileKind: "grpc", readOnly: "unknown_kind" });
    expect(toTreeNode(node({ id: "x", kind: "something-new" })).kind).toBe("unknown");
    expect(toTreeNode(node({ id: "f", kind: "folder", childIds: null })).kind === "folder").toBe(true);
  });
});

describe("loadCollection", () => {
  it("loads the tree with its problems, expanding the top-level folders the first time", async () => {
    const stores = openStores();
    await loadCollection(stores);
    const s = stores.collection.getState();
    expect(s.status).toBe("ready");
    expect(s.workspaceId).toBe("ws_1");
    expect(s.rootIds).toEqual(["api", "health"]);
    expect(Object.keys(s.nodes)).toHaveLength(6);
    expect(s.expandedIds).toEqual({ api: true });
    expect(s.problems).toEqual([
      { path: "api/orders/broken.json", nodeId: "broken", code: "projectfile.syntax", message: "Not valid JSON" },
    ]);
  });

  it("restores the expansion remembered for the workspace id", async () => {
    setExpansionPersistence(memoryExpansion({ ws_1: ["orders"], ws_2: ["api"] }));
    const stores = openStores();
    await loadCollection(stores);
    expect(stores.collection.getState().expandedIds).toEqual({ orders: true });
  });

  it("keeps the current expansion when the same project reloads", async () => {
    const stores = openStores();
    await loadCollection(stores);
    stores.collection.dispatch({ type: "collection/setExpanded", id: "orders", expanded: true });
    stores.collection.dispatch({ type: "collection/setExpanded", id: "api", expanded: false });
    await loadCollection(stores);
    expect(stores.collection.getState().expandedIds).toEqual({ orders: true });
  });

  it("clears the tree without a project, and keeps a failure in the store", async () => {
    const none = openStores(null);
    await loadCollection(none);
    expect(none.collection.getState().status).toBe("idle");
    expect(service.Tree).not.toHaveBeenCalled();

    vi.spyOn(console, "warn").mockImplementation(() => {});
    service.Tree.mockRejectedValue({ kind: "internal", code: "x", message: "disk gone", hint: "", details: {}, causes: [] });
    const stores = openStores();
    await loadCollection(stores);
    expect(stores.collection.getState().status).toBe("error");
    expect(stores.collection.getState().error).toBe("disk gone");
  });

  it("drops a tree that arrives after another project was opened", async () => {
    const call = deferred<TreeDTO>();
    service.Tree.mockReturnValueOnce(call.promise);
    const stores = openStores();
    const loading = loadCollection(stores);
    stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: { ...shop, id: "ws_2", path: "/p/other" } });
    stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_2" });
    call.resolve(tree());
    await loading;
    expect(stores.collection.getState().workspaceId).toBe("ws_2");
    expect(stores.collection.getState().nodes).toEqual({});
  });
});

describe("followCollection", () => {
  it("reloads on collection:changed and on another workspace, and unsubscribes on stop", async () => {
    const stores = openStores();
    const stop = followCollection(stores);
    await settle();
    expect(service.Tree).toHaveBeenCalledTimes(1);

    service.Tree.mockResolvedValue(tree({ rootIds: ["health"] }));
    emit(COLLECTION_CHANGED_EVENT, { anything: true });
    await settle();
    expect(service.Tree).toHaveBeenCalledTimes(2);
    expect(stores.collection.getState().rootIds).toEqual(["health"]);

    service.Tree.mockResolvedValue(tree({ workspaceId: "ws_2" }));
    stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: { ...shop, id: "ws_2", path: "/p/other" } });
    await settle();
    expect(service.Tree).toHaveBeenCalledTimes(3);
    expect(stores.collection.getState().workspaceId).toBe("ws_2");

    stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: null });
    await settle();
    expect(stores.collection.getState().status).toBe("idle");

    stop();
    expect(listenerCount()).toBe(0);
  });

  it("gives an open draft the new name after a rename", async () => {
    const stores = openStores();
    const stop = followCollection(stores);
    await settle();
    stores.drafts.dispatch({ type: "drafts/open", id: "health", request: { id: "health", name: "health", url: "/h" } });
    stores.drafts.dispatch({ type: "drafts/open", id: "list", request: { id: "list", name: "list" } });
    const listDraft = stores.drafts.getState().byId.list;

    const renamed = tree();
    renamed.nodes = (renamed.nodes ?? []).map((n) => (n.id === "health" ? { ...n, name: "Liveness" } : n));
    service.Tree.mockResolvedValue(renamed);
    emit(COLLECTION_CHANGED_EVENT, { workspaceId: "ws_1", op: "rename", nodeId: "health", trashId: "" });
    await settle();
    expect(stores.collection.getState().nodes.health?.name).toBe("Liveness");
    expect(stores.drafts.getState().byId.health).toEqual({
      request: { id: "health", name: "Liveness", url: "/h" },
      dirty: false,
      revision: 0,
    });
    expect(stores.drafts.getState().byId.list).toBe(listDraft);

    // Other operations, and renames in another workspace, leave drafts alone.
    renamed.nodes = (renamed.nodes ?? []).map((n) => (n.id === "health" ? { ...n, name: "Ready" } : n));
    emit(COLLECTION_CHANGED_EVENT, { workspaceId: "ws_1", op: "move", nodeId: "health", trashId: "" });
    emit(COLLECTION_CHANGED_EVENT, { workspaceId: "ws_9", op: "rename", nodeId: "health", trashId: "" });
    await settle();
    expect(stores.drafts.getState().byId.health?.request.name).toBe("Liveness");
    stop();
  });

  it("saves the expanded folders per workspace id, debounced, and flushes on stop", async () => {
    vi.useFakeTimers();
    const memory = memoryExpansion({ ws_2: ["api"] });
    setExpansionPersistence(memory);
    const stores = openStores();
    const stop = followCollection(stores);
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(EXPANSION_SAVE_DELAY_MS);
    // The first tree is shown with its default expansion, remembered as is.
    expect(memory.saved.ws_1).toEqual(["api"]);

    stores.collection.dispatch({ type: "collection/setExpanded", id: "orders", expanded: true });
    stores.collection.dispatch({ type: "collection/setExpanded", id: "api", expanded: false });
    expect(memory.saved.ws_1).toEqual(["api"]);
    await vi.advanceTimersByTimeAsync(EXPANSION_SAVE_DELAY_MS);
    expect(memory.saved.ws_1).toEqual(["orders"]);

    // Opening another project does not overwrite either project's expansion.
    service.Tree.mockResolvedValue(tree({ workspaceId: "ws_2" }));
    stores.collection.dispatch({ type: "collection/setExpanded", id: "api", expanded: true });
    stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: { ...shop, id: "ws_2", path: "/p/other" } });
    await vi.advanceTimersByTimeAsync(EXPANSION_SAVE_DELAY_MS);
    expect(memory.saved.ws_1.sort()).toEqual(["api", "orders"]);
    expect(stores.collection.getState().expandedIds).toEqual({ api: true });
    expect(memory.saved.ws_2).toEqual(["api"]);

    stores.collection.dispatch({ type: "collection/setExpanded", id: "orders", expanded: true });
    stop();
    expect(memory.saved.ws_2.sort()).toEqual(["api", "orders"]);
  });
});

describe("localStorageExpansion", () => {
  it("keeps one key per workspace and survives bad data", () => {
    const persistence = localStorageExpansion();
    window.localStorage.clear();
    expect(persistence.load("ws_1")).toBeNull();
    persistence.save("ws_1", ["a", "b"]);
    persistence.save("ws_2", []);
    expect(persistence.load("ws_1")).toEqual(["a", "b"]);
    expect(persistence.load("ws_2")).toEqual([]);
    window.localStorage.setItem(TREE_EXPANSION_KEY_PREFIX + "ws_3", "{not json");
    expect(persistence.load("ws_3")).toBeNull();
    window.localStorage.setItem(TREE_EXPANSION_KEY_PREFIX + "ws_4", JSON.stringify(["a", 3, null]));
    expect(persistence.load("ws_4")).toEqual(["a"]);
    const broken = localStorageExpansion(() => {
      throw new Error("no storage");
    });
    expect(broken.load("ws_1")).toBeNull();
    expect(() => broken.save("ws_1", ["a"])).not.toThrow();
    window.localStorage.clear();
  });
});

describe("openTreeItem", () => {
  it("opens a request or folder as a tab and only selects other items", async () => {
    const stores = openStores();
    await loadCollection(stores);
    openTreeItem(stores, "api");
    expect(stores.tabs.getState()).toMatchObject({ order: ["api"], activeId: "api" });
    expect(stores.collection.getState().selectedId).toBe("api");
    stores.tabs.dispatch({ type: "tabs/close", ids: ["api"] });
    openTreeItem(stores, "list");
    expect(stores.tabs.getState().order).toEqual(["list"]);
    expect(stores.collection.getState().selectedId).toBe("list");
    openTreeItem(stores, "grpc");
    expect(stores.tabs.getState().order).toEqual(["list"]);
    expect(stores.collection.getState().selectedId).toBe("grpc");
    openTreeItem(stores, "missing");
    expect(stores.collection.getState().selectedId).toBe("grpc");
  });
});

describe("createItem", () => {
  // The tree after creating "Refund" (a graphql request) in api/orders, which was collapsed.
  function withNewRequest(): TreeDTO {
    const t = tree();
    const nodes = (t.nodes ?? []).map((n) => (n.id === "orders" ? { ...n, childIds: ["broken", "refund"] } : n));
    nodes.splice(5, 0, node({ id: "refund", parentId: "orders", fileKind: "graphql", method: "POST", path: "api/orders/refund.json" }));
    return { ...t, nodes };
  }

  it("creates a request in the folder, reveals it and opens it in a kept tab", async () => {
    const stores = openStores();
    await loadCollection(stores);
    expect(stores.collection.getState().expandedIds).toEqual({ api: true });
    service.CreateRequest.mockResolvedValueOnce({ tree: withNewRequest(), nodeId: "refund" });

    await expect(createItem(stores, "orders", "Refund", { type: "request", kind: "graphql" })).resolves.toBe("refund");

    expect(service.CreateRequest).toHaveBeenCalledWith({ parentId: "orders", name: "Refund", kind: "graphql", index: -1 });
    const s = stores.collection.getState();
    expect(s.nodes.refund).toMatchObject({ kind: "request", fileKind: "graphql", parentId: "orders" });
    expect(s.expandedIds).toEqual({ api: true, orders: true });
    expect(s.selectedId).toBe("refund");
    expect(stores.tabs.getState()).toMatchObject({ order: ["refund"], activeId: "refund", previewId: null });
  });

  it("creates a folder at the top level and opens its tab", async () => {
    const stores = openStores();
    await loadCollection(stores);
    const t = tree();
    service.CreateFolder.mockResolvedValueOnce({
      tree: {
        ...t,
        rootIds: ["api", "health", "payments"],
        nodes: [...(t.nodes ?? []), node({ id: "payments", kind: "folder", fileKind: "folder", method: "", path: "payments" })],
      },
      nodeId: "payments",
    });

    await createItem(stores, null, "Payments", { type: "folder" });

    expect(service.CreateFolder).toHaveBeenCalledWith({ parentId: "", name: "Payments", index: -1 });
    expect(stores.collection.getState().rootIds).toEqual(["api", "health", "payments"]);
    expect(stores.tabs.getState()).toMatchObject({ order: ["payments"], activeId: "payments" });
  });

  it("reads the tree again and rethrows when Go fails", async () => {
    const stores = openStores();
    await loadCollection(stores);
    service.Tree.mockClear();
    const failure = { kind: "invalid", code: "collection.read_only", message: "The folder is read-only.", hint: "", details: {}, causes: [] };
    service.CreateFolder.mockRejectedValueOnce(failure);

    await expect(createItem(stores, "orders", "X", { type: "folder" })).rejects.toBe(failure);
    expect(service.Tree).toHaveBeenCalledTimes(1);
    expect(stores.tabs.getState().order).toEqual([]);
  });

  it("does not touch the stores when another project was opened meanwhile", async () => {
    const stores = openStores();
    await loadCollection(stores);
    const pending = deferred<unknown>();
    service.CreateRequest.mockReturnValueOnce(pending.promise);
    const created = createItem(stores, "orders", "Refund", { type: "request", kind: "http" });
    stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: { ...shop, id: "ws_2" } });
    pending.resolve({ tree: withNewRequest(), nodeId: "refund" });
    await created;
    expect(stores.collection.getState().nodes.refund).toBeUndefined();
    expect(stores.tabs.getState().order).toEqual([]);
  });
});

/** The tree with node `id` changed. */
function treeWithNode(id: string, over: Partial<NodeDTO>): TreeDTO {
  const t = tree();
  return { ...t, nodes: (t.nodes ?? []).map((n) => (n.id === id ? { ...n, ...over } : n)) };
}

/** The tree without folder api and everything in it. */
function treeWithoutApi(): TreeDTO {
  const t = tree();
  const gone = new Set(["api", "list", "grpc", "orders", "broken"]);
  return { ...t, rootIds: ["health"], nodes: (t.nodes ?? []).filter((n) => !gone.has(n.id)), problems: [] };
}

const failure = { kind: "invalid", code: "collection.read_only", message: "The folder is read-only.", hint: "", details: {}, causes: [] };

describe("renameNode", () => {
  it("renames through Go, applies the tree and gives an open draft the new name", async () => {
    const stores = openStores();
    await loadCollection(stores);
    stores.drafts.dispatch({ type: "drafts/open", id: "health", request: { name: "health" } as never });
    service.Rename.mockResolvedValueOnce({ tree: treeWithNode("health", { name: "Status" }), nodeId: "health" });

    await renameNode(stores, "health", "Status");

    expect(service.Rename).toHaveBeenCalledWith({ id: "health", name: "Status" });
    expect(stores.collection.getState().nodes.health.name).toBe("Status");
    expect(stores.drafts.getState().byId.health?.request.name).toBe("Status");
  });

  it("reads the tree again and rethrows when Go fails", async () => {
    const stores = openStores();
    await loadCollection(stores);
    service.Tree.mockClear();
    service.Rename.mockRejectedValueOnce(failure);
    await expect(renameNode(stores, "api", "X")).rejects.toBe(failure);
    expect(service.Tree).toHaveBeenCalledTimes(1);
  });
});

describe("duplicateNode", () => {
  it("reveals and selects the copy without opening it", async () => {
    const stores = openStores();
    await loadCollection(stores);
    const t = tree();
    const nodes = (t.nodes ?? []).map((n) => (n.id === "orders" ? { ...n, childIds: ["broken", "copy"] } : n));
    nodes.push(node({ id: "copy", parentId: "orders", name: "Broken copy" }));
    service.Duplicate.mockResolvedValueOnce({ tree: { ...t, nodes }, nodeId: "copy" });

    await expect(duplicateNode(stores, "broken")).resolves.toBe("copy");

    expect(service.Duplicate).toHaveBeenCalledWith("broken");
    const s = stores.collection.getState();
    expect(s.selectedId).toBe("copy");
    expect(s.expandedIds).toEqual({ api: true, orders: true });
    expect(stores.tabs.getState().order).toEqual([]);
  });
});

describe("deleteNode and restoreNode", () => {
  const entry = { id: "trash_1", nodeId: "api", kind: "folder", fileKind: "folder", name: "api", path: "api", parentId: null, index: 0 };

  it("closes the tabs of the deleted folder and brings them back where they were on restore", async () => {
    const stores = openStores();
    await loadCollection(stores);
    for (const id of ["list", "health", "api"]) openTreeItem(stores, id);
    stores.tabs.dispatch({ type: "tabs/setPinned", id: "list", pinned: true });
    openTreeItem(stores, "api");
    expect(stores.tabs.getState()).toMatchObject({ order: ["list", "health", "api"], activeId: "api" });

    service.Delete.mockResolvedValueOnce({ tree: treeWithoutApi(), entry });
    const deleted = await deleteNode(stores, "api");

    expect(service.Delete).toHaveBeenCalledWith("api");
    expect(deleted).toMatchObject({ trashId: "trash_1", nodeId: "api", name: "api", kind: "folder", workspaceId: "ws_1", activeId: "api" });
    expect(deleted.tabs).toEqual([
      { id: "list", index: 0, pinned: true },
      { id: "api", index: 2, pinned: false },
    ]);
    expect(stores.collection.getState().nodes.api).toBeUndefined();
    expect(stores.tabs.getState()).toMatchObject({ order: ["health"], activeId: "health" });

    service.Restore.mockResolvedValueOnce({ tree: tree(), nodeId: "api", toRoot: false });
    await expect(restoreNode(stores, deleted)).resolves.toBe("api");

    expect(service.Restore).toHaveBeenCalledWith("trash_1");
    expect(stores.tabs.getState()).toMatchObject({ order: ["list", "health", "api"], pinnedIds: { list: true }, activeId: "api" });
    expect(stores.collection.getState().selectedId).toBe("api");
  });

  it("keeps the active tab on restore when another tab was active at the delete", async () => {
    const stores = openStores();
    await loadCollection(stores);
    openTreeItem(stores, "list");
    openTreeItem(stores, "health");
    service.Delete.mockResolvedValueOnce({ tree: treeWithoutApi(), entry });
    const deleted = await deleteNode(stores, "api");
    expect(stores.tabs.getState().order).toEqual(["health"]);

    service.Restore.mockResolvedValueOnce({ tree: tree(), nodeId: "api", toRoot: false });
    await restoreNode(stores, deleted);
    expect(stores.tabs.getState()).toMatchObject({ order: ["list", "health"], activeId: "health" });
  });

  it("closes nothing and reads the tree again when Go fails", async () => {
    const stores = openStores();
    await loadCollection(stores);
    openTreeItem(stores, "list");
    service.Tree.mockClear();
    service.Delete.mockRejectedValueOnce(failure);
    await expect(deleteNode(stores, "api")).rejects.toBe(failure);
    expect(service.Tree).toHaveBeenCalledTimes(1);
    expect(stores.tabs.getState().order).toEqual(["list"]);
  });

  it("does not restore into another project", async () => {
    const stores = openStores();
    await loadCollection(stores);
    service.Delete.mockResolvedValueOnce({ tree: treeWithoutApi(), entry });
    const deleted = await deleteNode(stores, "api");
    stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: { ...shop, id: "ws_2" } });
    await expect(restoreNode(stores, deleted)).resolves.toBe("");
    expect(service.Restore).not.toHaveBeenCalled();
  });
});
