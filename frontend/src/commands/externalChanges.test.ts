import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Tree: vi.fn<() => Promise<unknown>>(),
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  Folder: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: { request: { id: string; url: string }; revision: number }) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
  FlushAll: vi.fn<() => Promise<void>>(),
  SetWatching: vi.fn<(active: boolean) => Promise<void>>(),
  ResolveConflict: vi.fn<(id: string) => Promise<void>>(),
  RecreateRequest: vi.fn<(input: unknown) => Promise<unknown>>(),
}));

const environments = vi.hoisted(() => ({
  List: vi.fn<() => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/environments", () => ({
  EnvironmentsService: environments,
}));

import {
  COLLECTION_CODES,
  COLLECTION_EXTERNAL_CHANGE_EVENT,
  type ExternalChangeDTO,
  type NodeDTO,
  type RequestDTO,
  type TreeDTO,
} from "../lib/collection";
import { createStores, DELETED_READ_ONLY, selectRequestSaveStatus, type Stores, type WorkspaceInfo } from "../stores";
import { emit, listenerCount, resetFakeWails, settle } from "../test/fakeWails";
import { editRequest, loadRequestDraft } from "./autosave";
import { loadCollection } from "./collection";
import { historyDepth } from "./editHistory";
import {
  applyExternalChange,
  closeDeletedRequest,
  followExternalChanges,
  keepMine,
  loadConflictVersions,
  onExternalGlobalsChange,
  restoreDeletedRequest,
  takeTheirs,
} from "./externalChanges";
import { openRequest } from "./tabs";

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

// api/ { list }, health
function tree(over: Partial<TreeDTO> = {}): TreeDTO {
  return {
    workspaceId: "ws_1",
    path: "/p/shop",
    readOnly: "",
    rootIds: ["api", "health"],
    nodes: [
      node({ id: "api", kind: "folder", fileKind: "folder", method: "", childIds: ["list"], path: "api" }),
      node({ id: "list", parentId: "api", path: "api/list.json" }),
      node({ id: "health" }),
    ],
    problems: [],
    scratch: [],
    ...over,
  };
}

function request(id: string, over: Partial<RequestDTO> = {}): RequestDTO {
  return { id, kind: "http", name: id, url: "/" + id, method: "GET", extra: "", ...over } as RequestDTO;
}

function change(over: Partial<ExternalChangeDTO> = {}): ExternalChangeDTO {
  return { workspaceId: "ws_1", added: [], removed: [], modified: [], moved: [], conflicts: [], globals: false, environments: false, ...over };
}

async function openProject(): Promise<Stores> {
  const stores = createStores();
  stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: shop });
  service.Tree.mockResolvedValueOnce(tree());
  await loadCollection(stores);
  return stores;
}

/** Opens request `id` in a tab with its draft loaded as `dto`. */
async function openDraft(stores: Stores, id: string, dto: RequestDTO = request(id)): Promise<void> {
  openRequest(stores, id);
  service.Request.mockResolvedValueOnce(dto);
  await loadRequestDraft(stores, id);
}

const stops: (() => void)[] = [];

beforeEach(() => {
  resetFakeWails();
  for (const fn of Object.values(service)) fn.mockReset();
  service.UpdateRequest.mockResolvedValue(undefined);
  service.Flush.mockResolvedValue(undefined);
  service.FlushAll.mockResolvedValue(undefined);
  service.SetWatching.mockResolvedValue(undefined);
  service.ResolveConflict.mockResolvedValue(undefined);
  environments.List.mockReset().mockResolvedValue({ environments: [], activeId: "", problems: [], readOnly: false });
});

afterEach(() => {
  for (const stop of stops.splice(0)) stop();
});

describe("external changes", () => {
  it("reloads the tree and a clean draft, and clears its undo history", async () => {
    const stores = await openProject();
    await openDraft(stores, "health");
    editRequest(stores, "health", { url: "/edited" });
    stores.drafts.dispatch({ type: "drafts/markSaved", id: "health", revision: 1 });
    expect(historyDepth(stores, "health").undo).toBe(1);

    service.Tree.mockResolvedValueOnce(tree({ nodes: tree().nodes?.map((n) => (n.id === "health" ? { ...n, name: "Health v2" } : n)) }));
    service.Request.mockResolvedValueOnce(request("health", { url: "/from-disk" }));
    await applyExternalChange(stores, change({ modified: ["health"] }));

    expect(stores.collection.getState().nodes.health?.name).toBe("Health v2");
    expect(stores.drafts.getState().byId.health).toMatchObject({ dirty: false, request: { url: "/from-disk" } });
    expect(service.Request).toHaveBeenLastCalledWith("health");
    expect(historyDepth(stores, "health").undo).toBe(0);
    expect(stores.drafts.getState().conflicts.health).toBeUndefined();
  });

  it("puts a dirty draft in conflict and holds its edits until they are kept", async () => {
    const stores = await openProject();
    await openDraft(stores, "health");
    editRequest(stores, "health", { url: "/mine" });
    await settle();
    expect(service.UpdateRequest).toHaveBeenCalledTimes(1);

    service.Tree.mockResolvedValueOnce(tree());
    await applyExternalChange(stores, change({ modified: ["health"], conflicts: ["health"] }));
    expect(stores.drafts.getState().conflicts.health).toBe(true);
    expect(stores.drafts.getState().byId.health).toMatchObject({ dirty: true, request: { url: "/mine" } });
    // The saves Go set aside are not "Saving…" any more.
    expect(selectRequestSaveStatus(stores.autosave.getState(), "health")).toBe("idle");

    // Edits made meanwhile are kept but not sent.
    editRequest(stores, "health", { url: "/mine-2" });
    await settle();
    expect(service.UpdateRequest).toHaveBeenCalledTimes(1);

    await keepMine(stores, "health");
    await settle();
    expect(service.ResolveConflict).toHaveBeenCalledWith("health");
    expect(stores.drafts.getState().conflicts.health).toBeUndefined();
    expect(service.UpdateRequest).toHaveBeenCalledTimes(2);
    expect(service.UpdateRequest.mock.calls[1]?.[0].request.url).toBe("/mine-2");
  });

  it("takes the file's version over the draft", async () => {
    const stores = await openProject();
    await openDraft(stores, "health");
    editRequest(stores, "health", { url: "/mine" });
    await settle();
    service.Tree.mockResolvedValueOnce(tree());
    await applyExternalChange(stores, change({ modified: ["health"], conflicts: ["health"] }));

    service.Request.mockResolvedValueOnce(request("health", { url: "/theirs" }));
    const versions = await loadConflictVersions(stores, "health");
    expect(versions.mine).toContain('"/mine"');

    service.Request.mockResolvedValueOnce(request("health", { url: "/theirs" }));
    await takeTheirs(stores, "health");
    expect(service.ResolveConflict).toHaveBeenCalledWith("health");
    expect(stores.drafts.getState().byId.health).toMatchObject({ dirty: false, request: { url: "/theirs" } });
    expect(stores.drafts.getState().conflicts.health).toBeUndefined();
    expect(historyDepth(stores, "health").undo).toBe(0);
    await settle();
    expect(service.UpdateRequest).toHaveBeenCalledTimes(1);
  });

  it("does not report an edit Go refuses because of the conflict as a failed save", async () => {
    const stores = await openProject();
    await openDraft(stores, "health");
    service.UpdateRequest.mockRejectedValueOnce(
      Object.assign(new Error("go"), {
        name: "RuntimeError",
        cause: { kind: "io", code: COLLECTION_CODES.changedOnDisk, message: "changed", hint: "", details: {}, causes: [] },
      }),
    );
    editRequest(stores, "health", { url: "/mine" });
    await settle();
    expect(stores.autosave.getState().failed.health).toBeUndefined();
  });

  it("keeps the tab of a request whose file was removed, read-only, until it is closed", async () => {
    const stores = await openProject();
    await openDraft(stores, "list");
    stops.push(followExternalChanges(stores));

    service.Tree.mockResolvedValueOnce(tree({ nodes: tree().nodes?.filter((n) => n.id !== "list").map((n) => (n.id === "api" ? { ...n, childIds: [] } : n)) }));
    await applyExternalChange(stores, change({ removed: ["list"] }));

    const state = stores.collection.getState();
    expect(state.deletedIds).toEqual(["list"]);
    expect(state.nodes.list).toMatchObject({ readOnly: DELETED_READ_ONLY, parentId: "api" });
    expect(state.nodes.api).toMatchObject({ childIds: [] });
    expect(stores.tabs.getState().order).toContain("list");
    expect(stores.drafts.getState().byId.list).toBeDefined();

    // Another reload keeps it.
    service.Tree.mockResolvedValueOnce(tree({ nodes: tree().nodes?.filter((n) => n.id !== "list") }));
    await loadCollection(stores);
    expect(stores.collection.getState().deletedIds).toEqual(["list"]);

    closeDeletedRequest(stores, "list");
    expect(stores.tabs.getState().order).not.toContain("list");
    expect(stores.collection.getState().deletedIds).toEqual([]);
    expect(stores.collection.getState().nodes.list).toBeUndefined();
  });

  it("writes a removed request back with the tab's content", async () => {
    const stores = await openProject();
    await openDraft(stores, "list");
    service.Tree.mockResolvedValueOnce(tree({ nodes: tree().nodes?.filter((n) => n.id !== "list") }));
    await applyExternalChange(stores, change({ removed: ["list"] }));

    service.RecreateRequest.mockResolvedValueOnce({ tree: tree(), nodeId: "list" });
    service.Tree.mockResolvedValueOnce(tree());
    await restoreDeletedRequest(stores, "list");
    expect(service.RecreateRequest).toHaveBeenCalledWith({ parentId: "api", index: -1, request: expect.objectContaining({ id: "list" }) });
    expect(stores.collection.getState().deletedIds).toEqual([]);
    expect(stores.collection.getState().nodes.list?.readOnly).toBe("");
    expect(stores.drafts.getState().byId.list).toMatchObject({ dirty: false });
  });

  it("closes nothing and reloads nothing for another project", async () => {
    const stores = await openProject();
    await applyExternalChange(stores, change({ workspaceId: "ws_other", modified: ["health"] }));
    expect(service.Tree).toHaveBeenCalledTimes(1);
  });

  it("reloads the environments and tells the global variables views", async () => {
    const stores = await openProject();
    const globals = vi.fn();
    const off = onExternalGlobalsChange(globals);
    service.Tree.mockResolvedValueOnce(tree());
    await applyExternalChange(stores, change({ environments: true, globals: true }));
    await settle();
    expect(environments.List).toHaveBeenCalled();
    expect(globals).toHaveBeenCalledTimes(1);
    off();
  });

  it("follows the events and pauses the watcher while the window is hidden", async () => {
    const stores = await openProject();
    const stop = followExternalChanges(stores);
    service.Tree.mockResolvedValueOnce(tree({ nodes: tree().nodes?.map((n) => (n.id === "health" ? { ...n, name: "Renamed" } : n)) }));
    emit(COLLECTION_EXTERNAL_CHANGE_EVENT, change({ moved: ["health"] }));
    await settle();
    expect(stores.collection.getState().nodes.health?.name).toBe("Renamed");

    const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    expect(service.SetWatching).toHaveBeenLastCalledWith(false);
    visibility.mockReturnValue("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    expect(service.SetWatching).toHaveBeenLastCalledWith(true);
    visibility.mockRestore();

    stop();
    expect(listenerCount()).toBe(0);
  });
});
