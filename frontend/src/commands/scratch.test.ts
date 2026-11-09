import { beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Tree: vi.fn<() => Promise<unknown>>(),
  CreateScratch: vi.fn<(input: unknown) => Promise<unknown>>(),
  SaveScratch: vi.fn<(input: unknown) => Promise<unknown>>(),
  DeleteScratch: vi.fn<(id: string) => Promise<unknown>>(),
  Flush: vi.fn<(id: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));

import type { NodeDTO, TreeDTO } from "../lib/collection";
import type { ExpansionPersistence } from "../lib/treeExpansion";
import { collectionReducer, createStores, initialCollectionState, selectIsScratch, type Stores, type WorkspaceInfo } from "../stores";
import { resetFakeWails } from "../test/fakeWails";
import { createScratchRequest, deleteScratchRequest, loadCollection, saveScratchTo, setExpansionPersistence } from "./collection";

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
  return { parentId: null, kind: "request", name: over.id, path: `${over.id}.json`, fileKind: "http", method: "GET", childIds: [], readOnly: "", ...over };
}

const SCRATCH = "req_00000000000000aa";

// api/ { list }, health; and scratch requests.
function tree(scratch: NodeDTO[] = [], extra: { apiChildren?: string[]; nodes?: NodeDTO[] } = {}): TreeDTO {
  return {
    workspaceId: "ws_1",
    path: "/p/shop",
    readOnly: "",
    rootIds: ["api", "health"],
    nodes: [
      node({ id: "api", kind: "folder", fileKind: "folder", method: "", childIds: extra.apiChildren ?? ["list"], path: "api" }),
      node({ id: "list", parentId: "api", path: "api/list.json" }),
      ...(extra.nodes ?? []),
      node({ id: "health", method: "HEAD" }),
    ],
    problems: [],
    scratch,
  };
}

const scratchNode = (over: Partial<NodeDTO> = {}) => node({ id: SCRATCH, name: "Scratch 1", path: "", ...over });

function openStores(): Stores {
  const stores = createStores();
  stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: shop });
  return stores;
}

beforeEach(() => {
  resetFakeWails();
  for (const fn of Object.values(service)) fn.mockReset();
  service.Tree.mockResolvedValue(tree([scratchNode()]));
  service.Flush.mockResolvedValue(undefined);
  const memory: ExpansionPersistence = { load: () => null, save: () => {} };
  setExpansionPersistence(memory);
});

describe("scratch requests in the collection store", () => {
  it("puts scratch nodes in nodes but not in the tree, and forgets them on remove", async () => {
    const stores = openStores();
    await loadCollection(stores);
    const s = stores.collection.getState();
    expect(s.scratchIds).toEqual([SCRATCH]);
    expect(s.rootIds).toEqual(["api", "health"]);
    expect(s.nodes[SCRATCH]).toMatchObject({ kind: "request", parentId: null, path: "", name: "Scratch 1" });
    expect(selectIsScratch(s, SCRATCH)).toBe(true);
    expect(selectIsScratch(s, "health")).toBe(false);

    const removed = collectionReducer(s, { type: "collection/remove", id: SCRATCH });
    expect(removed.scratchIds).toEqual([]);
    expect(removed.nodes[SCRATCH]).toBeUndefined();
    expect(removed.rootIds).toEqual(["api", "health"]);
    expect(initialCollectionState.scratchIds).toEqual([]);
  });
});

describe("createScratchRequest", () => {
  it("creates one through Go and opens it in a kept tab", async () => {
    const stores = openStores();
    await loadCollection(stores);
    const second = scratchNode({ id: "req_00000000000000bb", name: "Scratch 2", fileKind: "graphql", method: "POST" });
    service.CreateScratch.mockResolvedValueOnce({ tree: tree([scratchNode(), second]), nodeId: second.id });

    await expect(createScratchRequest(stores, "graphql")).resolves.toBe(second.id);

    expect(service.CreateScratch).toHaveBeenCalledWith({ kind: "graphql", name: "" });
    expect(stores.collection.getState().scratchIds).toEqual([SCRATCH, second.id]);
    expect(stores.tabs.getState()).toMatchObject({ order: [second.id], activeId: second.id, previewId: null });
  });
});

describe("saveScratchTo", () => {
  it("sends queued edits, saves into the folder and keeps the tab and draft with the new name", async () => {
    const stores = openStores();
    await loadCollection(stores);
    stores.tabs.dispatch({ type: "tabs/open", id: SCRATCH });
    stores.drafts.dispatch({ type: "drafts/open", id: SCRATCH, request: { name: "Scratch 1" } as never });
    const saved = node({ id: SCRATCH, parentId: "api", name: "Try it", path: "api/try-it.json" });
    service.SaveScratch.mockResolvedValueOnce({ tree: tree([], { apiChildren: ["list", SCRATCH], nodes: [saved] }), nodeId: SCRATCH });

    await expect(saveScratchTo(stores, SCRATCH, "api", "Try it")).resolves.toBe(SCRATCH);

    expect(service.SaveScratch).toHaveBeenCalledWith({ id: SCRATCH, parentId: "api", name: "Try it", index: -1 });
    const s = stores.collection.getState();
    expect(s.scratchIds).toEqual([]);
    expect(s.nodes[SCRATCH]).toMatchObject({ parentId: "api", path: "api/try-it.json" });
    expect(s.selectedId).toBe(SCRATCH);
    expect(s.expandedIds).toMatchObject({ api: true });
    expect(stores.tabs.getState().order).toEqual([SCRATCH]);
    expect(stores.drafts.getState().byId[SCRATCH]?.request.name).toBe("Try it");
  });

  it("saves to the top level with parentId null, and rereads the tree on failure", async () => {
    const stores = openStores();
    await loadCollection(stores);
    service.Tree.mockClear();
    const failure = { kind: "not_found", code: "collection.not_found", message: "Gone.", hint: "", details: {}, causes: [] };
    service.SaveScratch.mockRejectedValueOnce(failure);
    await expect(saveScratchTo(stores, SCRATCH, null, "")).rejects.toBe(failure);
    expect(service.SaveScratch).toHaveBeenCalledWith({ id: SCRATCH, parentId: "", name: "", index: -1 });
    expect(service.Tree).toHaveBeenCalledTimes(1);
  });
});

describe("deleteScratchRequest", () => {
  it("closes its tab without remembering it and applies the tree", async () => {
    const stores = openStores();
    await loadCollection(stores);
    stores.tabs.dispatch({ type: "tabs/open", id: SCRATCH });
    service.DeleteScratch.mockResolvedValueOnce(tree([]));

    await deleteScratchRequest(stores, SCRATCH);

    expect(service.DeleteScratch).toHaveBeenCalledWith(SCRATCH);
    expect(stores.tabs.getState()).toMatchObject({ order: [], closed: [] });
    expect(stores.collection.getState().scratchIds).toEqual([]);
    expect(stores.collection.getState().nodes[SCRATCH]).toBeUndefined();
  });
});
