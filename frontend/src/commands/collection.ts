// Collection commands (COL-01, COL-02): load the open project's request
// tree from the Go CollectionService into the collection store, again when
// another project is opened and when Go reports a change, remember which
// folders are expanded per workspace, open tree items, and create, rename,
// duplicate, move, delete and restore folders and requests (COL-04), and
// show a folder sorted by name or in its manual order (COL-05).
// See docs/collection.md and docs/frontend.md (Collection tree).

import * as api from "../lib/collection";
import { toAppError } from "../lib/errors";
import { workspaceLocalExpansion, type ExpansionPersistence } from "../lib/treeExpansion";
import { workspaceLocalTreeSort, type SortPersistence } from "../lib/treeSort";
import {
  selectSubtreeIds,
  type CollectionProblem,
  type NodeId,
  type SortMode,
  type ClosedTab,
  type Stores,
  type TabId,
  type TreeNode,
} from "../stores";
import { applyRenamed, flushRequests, refreshBaseVersion, setBaseVersion } from "./autosave";
import { loadEnvironments } from "./environments";
import { activateTab, closeTabs, openFolder, openRequest, setTabPinned, type OpenTabOptions } from "./tabs";
import { onWorkspaceLeaving } from "./workspaceLeave";

let expansion: ExpansionPersistence = workspaceLocalExpansion();

/** Replaces where expanded folders are remembered (tests). Returns the previous backend. */
export function setExpansionPersistence(next: ExpansionPersistence): ExpansionPersistence {
  const previous = expansion;
  expansion = next;
  return previous;
}

let sortChoices: SortPersistence = workspaceLocalTreeSort();

/** Replaces where the folders sorted by name are remembered (tests). Returns the previous backend. */
export function setSortPersistence(next: SortPersistence): SortPersistence {
  const previous = sortChoices;
  sortChoices = next;
  return previous;
}

/**
 * Shows folder `parentId` (null: the top level) in its manual order or
 * sorted by name (COL-05), and remembers the choice for the workspace at
 * once. A view preference: the project's files are not written, so the
 * manual order is back when the folder returns to it.
 */
export function setFolderSort(stores: Stores, parentId: NodeId | null, mode: SortMode): void {
  const before = stores.collection.getState();
  stores.collection.dispatch({ type: "collection/setSort", parentId, mode });
  const after = stores.collection.getState();
  if (after.sortedByName === before.sortedByName || after.workspaceId === null) return;
  sortChoices.save(after.workspaceId, Object.keys(after.sortedByName));
}

/** Maps one node of the Go tree. Unknown node kinds become "unknown" (read-only). */
export function toTreeNode(dto: api.NodeDTO): TreeNode {
  const base = {
    id: dto.id,
    parentId: dto.parentId ?? null,
    name: dto.name,
    path: dto.path,
    fileKind: dto.fileKind,
    readOnly: dto.readOnly,
  };
  switch (dto.kind) {
    case "folder":
      return { ...base, kind: "folder", childIds: dto.childIds ?? [] };
    case "request":
      return { ...base, kind: "request", method: dto.method };
    case "invalid":
      return { ...base, kind: "invalid" };
    default:
      return { ...base, kind: "unknown" };
  }
}

export function toCollectionProblem(dto: api.ProblemDTO): CollectionProblem {
  return { path: dto.path, nodeId: dto.nodeId, code: dto.error.code, message: dto.error.message };
}

function currentWorkspaceId(stores: Stores): string | null {
  return stores.workspace.getState().current?.id ?? null;
}

/**
 * The folders to expand when a project's tree is shown for the first time
 * in this window: the remembered ones, or else the top-level folders.
 */
function initialExpansion(workspaceId: string, tree: api.TreeDTO, nodes: readonly TreeNode[]): NodeId[] {
  const saved = expansion.load(workspaceId);
  if (saved !== null) return saved;
  const folders = new Set(nodes.filter((n) => n.kind === "folder").map((n) => n.id));
  return (tree.rootIds ?? []).filter((id) => folders.has(id));
}

/**
 * Puts a tree from Go into the store, unless another project was opened
 * meanwhile. The first tree shown for a project gets its remembered
 * expansion; a later one (a reload, an operation) keeps the current one.
 */
function applyTree(stores: Stores, workspaceId: string, tree: api.TreeDTO, deleted?: readonly TreeNode[]): void {
  if (currentWorkspaceId(stores) !== workspaceId || stores.collection.getState().workspaceId !== workspaceId) return;
  const nodes = (tree.nodes ?? []).map(toTreeNode);
  // Scratch requests (COL-06) go into `nodes` too, so tabs and editors find them.
  const scratch = (tree.scratch ?? []).map((dto) => toTreeNode({ ...dto, parentId: null, path: "" }));
  const isFirst = stores.collection.getState().status !== "ready";
  stores.collection.dispatch({
    type: "collection/load",
    workspaceId,
    nodes: [...nodes, ...scratch],
    rootIds: tree.rootIds ?? [],
    scratchIds: scratch.map((node) => node.id),
    readOnly: tree.readOnly,
    problems: (tree.problems ?? []).map(toCollectionProblem),
    expandedIds: isFirst ? initialExpansion(workspaceId, tree, nodes) : undefined,
    sortedByName: isFirst ? (sortChoices.load(workspaceId) ?? []) : undefined,
    deleted,
  });
}

export interface LoadCollectionOptions {
  /**
   * Requests whose file was removed outside PostSonde and whose tab stays
   * open (STO-05, commands/externalChanges.ts): kept in the store as
   * `deletedIds`.
   */
  readonly deleted?: readonly TreeNode[];
}

/**
 * Loads the tree of the open project; with none open, clears the store. A
 * failure is kept in the store (status "error") and logged. A result that
 * arrives after another project was opened is dropped.
 */
export async function loadCollection(stores: Stores, options: LoadCollectionOptions = {}): Promise<void> {
  const workspaceId = currentWorkspaceId(stores);
  if (workspaceId === null) {
    stores.collection.dispatch({ type: "collection/clear" });
    return;
  }
  stores.collection.dispatch({ type: "collection/loading", workspaceId });
  try {
    applyTree(stores, workspaceId, await api.getCollectionTree(), options.deleted);
  } catch (err: unknown) {
    const appErr = toAppError(err);
    console.warn("collection: could not load the request tree", appErr);
    stores.collection.dispatch({ type: "collection/failed", workspaceId, error: appErr.message });
  }
}

/**
 * Applies a collection:changed event: loads the tree again and, after a
 * rename of a request, gives its open draft the new name (applyRenamed),
 * so the next auto-save does not write the old name back, and the
 * request's new version (APP-06: the rename may come from another window).
 */
export async function applyCollectionChanged(stores: Stores, event: api.ChangedEventDTO): Promise<void> {
  await loadCollection(stores);
  if (event.op !== "rename" || event.workspaceId !== currentWorkspaceId(stores)) return;
  const node = stores.collection.getState().nodes[event.nodeId];
  if (node?.kind !== "request" || stores.drafts.getState().byId[event.nodeId] === undefined) return;
  applyRenamed(stores, event.nodeId, node.name);
  await refreshBaseVersion(stores, event.nodeId);
}

/** Delay before a change of the expanded folders is saved. */
export const EXPANSION_SAVE_DELAY_MS = 300;

/**
 * Loads the tree now, again whenever another project is opened or Go sends
 * collection:changed, and clears it when the project is closed. Saves the
 * expanded folders per workspace (debounced, flushed when the page is hidden
 * and on stop). Returns the stop function.
 */
export function followCollection(stores: Stores): () => void {
  let lastWorkspace = currentWorkspaceId(stores);
  let lastExpanded = stores.collection.getState().expandedIds;
  let pending: { workspaceId: string; ids: NodeId[] } | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;

  const flush = () => {
    if (timer !== null) clearTimeout(timer);
    timer = null;
    if (pending === null) return;
    expansion.save(pending.workspaceId, pending.ids);
    pending = null;
  };

  void loadCollection(stores);

  const stopWorkspace = stores.workspace.subscribe(() => {
    const id = currentWorkspaceId(stores);
    if (id === lastWorkspace) return;
    lastWorkspace = id;
    flush();
    void loadCollection(stores);
  });

  const stopExpansion = stores.collection.subscribe(() => {
    const state = stores.collection.getState();
    if (state.expandedIds === lastExpanded) return;
    lastExpanded = state.expandedIds;
    // Only a loaded tree's expansion is the user's; loading and clearing are not changes to save.
    if (state.status !== "ready" || state.workspaceId === null) return;
    if (pending !== null && pending.workspaceId !== state.workspaceId) flush();
    pending = { workspaceId: state.workspaceId, ids: Object.keys(state.expandedIds) };
    if (timer !== null) clearTimeout(timer);
    timer = setTimeout(flush, EXPANSION_SAVE_DELAY_MS);
  });

  const stopEvents = api.onCollectionChanged((event) => void applyCollectionChanged(stores, event));
  // Before another project opens: save this one's expansion while Go still has it open.
  const stopLeaving = onWorkspaceLeaving((leaving) => {
    if (leaving === stores) flush();
  });

  const onHidden = () => {
    if (document.visibilityState === "hidden") flush();
  };
  window.addEventListener("pagehide", flush);
  document.addEventListener("visibilitychange", onHidden);

  return () => {
    stopWorkspace();
    stopExpansion();
    stopEvents();
    stopLeaving();
    window.removeEventListener("pagehide", flush);
    document.removeEventListener("visibilitychange", onHidden);
    flush();
  };
}

/**
 * Opens a tree item: selects it and opens its tab, a request tab for a
 * request and a DIR tab for a folder (NAV-01). With `preview` the tab is the
 * reusable preview tab (a single click), else a kept tab. Unknown and
 * invalid items are only selected; they cannot be edited. Expanding folders
 * is up to the tree itself.
 */
export function openTreeItem(stores: Stores, id: NodeId, options: OpenTabOptions = {}): void {
  const node = stores.collection.getState().nodes[id];
  if (node === undefined) return;
  if (node.kind === "request") {
    openRequest(stores, id, options);
    return;
  }
  if (node.kind === "folder") {
    openFolder(stores, id, options);
    return;
  }
  stores.collection.dispatch({ type: "collection/select", id });
}

/**
 * What a new-item dialog creates: a folder, an empty request of a kind, or
 * an http request imported from a curl command (IMP-01).
 */
export type NewItem =
  | { readonly type: "folder" }
  | { readonly type: "request"; readonly kind: api.NewRequestKind }
  | { readonly type: "curl"; readonly text: string };

/** Expands every folder above `id`, so its row is shown. */
export function revealNode(stores: Stores, id: NodeId): void {
  const { nodes } = stores.collection.getState();
  let parentId = nodes[id]?.parentId ?? null;
  for (let depth = 0; parentId !== null && depth < 1000; depth++) {
    stores.collection.dispatch({ type: "collection/setExpanded", id: parentId, expanded: true });
    parentId = nodes[parentId]?.parentId ?? null;
  }
}

/**
 * Creates a folder, an empty request or an imported cURL command called `name` at the end of the
 * folder `parentId` (null: the top level) through CollectionService
 * (COL-04), applies the tree Go returns, reveals the new item and opens it
 * in a kept tab: a request in its editor, a folder as its DIR tab. Resolves
 * to the new item's id. On failure the tree is read again (Go may have
 * done some of the steps) and the error is rethrown for the dialog to show.
 */
export async function createItem(stores: Stores, parentId: NodeId | null, name: string, item: NewItem): Promise<NodeId> {
  const workspaceId = currentWorkspaceId(stores);
  let result: api.NodeResultDTO;
  try {
    result =
      item.type === "folder"
        ? await api.createFolder(parentId ?? "", name)
        : item.type === "curl"
          ? await api.importCurl(parentId ?? "", name, item.text)
          : await api.createRequest(parentId ?? "", name, item.kind);
  } catch (err: unknown) {
    void loadCollection(stores);
    throw err;
  }
  if (workspaceId === null || currentWorkspaceId(stores) !== workspaceId) return result.nodeId;
  applyTree(stores, workspaceId, result.tree);
  if (stores.collection.getState().nodes[result.nodeId] === undefined) return result.nodeId;
  revealNode(stores, result.nodeId);
  openTreeItem(stores, result.nodeId);
  return result.nodeId;
}

/**
 * Imports a previewed file (IMP-10, docs/import.md) through
 * CollectionService.Import, applies the tree Go returns, expands, reveals
 * and selects the new folder (or the folder the items went into), and
 * loads the environments again when some were created. Resolves to the
 * result with the report; null when another project was opened meanwhile.
 * Rejects with Go's error (the tree is read again).
 */
export async function importFile(stores: Stores, input: api.ImportInputDTO): Promise<api.ImportResultDTO | null> {
  const result = await treeOp(stores, () => api.runImport(input));
  if (result === null) return null;
  const id = result.nodeId;
  const node = id === "" ? undefined : stores.collection.getState().nodes[id];
  if (node !== undefined) {
    revealNode(stores, id);
    if (node.kind === "folder") stores.collection.dispatch({ type: "collection/setExpanded", id, expanded: true });
    stores.collection.dispatch({ type: "collection/select", id });
  }
  if (result.report.counts.environments > 0) void loadEnvironments(stores);
  return result;
}

/**
 * Creates an empty scratch request of `kind` (COL-06), kept in the
 * workspace's local side files, and opens it in a kept tab (not with
 * `open` false: the caller opens it once it has filled it). Go names it
 * "Scratch N" when `name` is blank. Resolves to its id; rejects with Go's
 * error.
 */
export async function createScratchRequest(stores: Stores, kind: api.NewRequestKind = "http", name = "", open = true): Promise<NodeId> {
  const result = await treeOp(stores, () => api.createScratch(kind, name));
  if (result === null) return "";
  if (open && stores.collection.getState().nodes[result.nodeId] !== undefined) openRequest(stores, result.nodeId);
  return result.nodeId;
}

/**
 * "Save to…" (COL-06): moves the scratch request `id` into the folder
 * `parentId` (null: the top level) as a normal request called `name`. Its
 * queued edits are sent first. It keeps its id, so its tab, draft and edit
 * history stay; the draft gets the new name. The request is revealed and
 * selected in the tree. Rejects with Go's error (the tree is read again).
 */
export async function saveScratchTo(stores: Stores, id: NodeId, parentId: NodeId | null, name: string): Promise<NodeId> {
  await flushRequests(stores, [id]);
  const result = await treeOp(stores, () => api.saveScratch(id, parentId ?? "", name));
  if (result === null) return "";
  const savedId = result.nodeId;
  const node = stores.collection.getState().nodes[savedId];
  if (node === undefined) return savedId;
  if (savedId === id && stores.drafts.getState().byId[id] !== undefined) applyRenamed(stores, id, node.name);
  revealNode(stores, savedId);
  stores.collection.dispatch({ type: "collection/select", id: savedId });
  return savedId;
}

/**
 * Deletes the scratch request `id` for good (COL-06): its queued edits are
 * sent first so none arrives after it is gone, then its tab closes and Go
 * removes its file. Rejects with Go's error.
 */
export async function deleteScratchRequest(stores: Stores, id: NodeId): Promise<void> {
  await flushRequests(stores, [id]);
  closeTabs(stores, [id], { remember: false });
  const workspaceId = currentWorkspaceId(stores);
  let tree: api.TreeDTO;
  try {
    tree = await api.deleteScratch(id);
  } catch (err: unknown) {
    void loadCollection(stores);
    throw err;
  }
  if (workspaceId !== null && currentWorkspaceId(stores) === workspaceId) applyTree(stores, workspaceId, tree);
}

/** The requests among `ids` (their queued edits are sent before a tree operation). */
function requestIds(stores: Stores, ids: readonly NodeId[]): NodeId[] {
  const { nodes } = stores.collection.getState();
  return ids.filter((id) => nodes[id]?.kind === "request");
}

/**
 * Runs a tree operation in Go and applies the tree it returns. On failure
 * the tree is read again (Go may have done some of the steps) and the error
 * is rethrown. Resolves to null when another project was opened meanwhile:
 * the result is not for the open one.
 */
async function treeOp<T extends { tree: api.TreeDTO }>(stores: Stores, call: () => Promise<T>): Promise<T | null> {
  const workspaceId = currentWorkspaceId(stores);
  let result: T;
  try {
    result = await call();
  } catch (err: unknown) {
    void loadCollection(stores);
    throw err;
  }
  if (workspaceId === null || currentWorkspaceId(stores) !== workspaceId) return null;
  applyTree(stores, workspaceId, result.tree);
  return result;
}

/**
 * Renames a folder or request (COL-04) through CollectionService, which
 * renames its file too. The queued edits of a request are sent first, so an
 * edit in flight cannot write the old name back, and its open draft gets
 * the new name. Tabs show the new name on their own: they read it from the
 * collection store. Rejects with Go's error (the tree is read again).
 */
export async function renameNode(stores: Stores, id: NodeId, name: string): Promise<void> {
  if (stores.collection.getState().nodes[id]?.kind === "request") await flushRequests(stores, [id]);
  const result = await treeOp(stores, () => api.renameItem(id, name));
  if (result === null) return;
  const node = stores.collection.getState().nodes[id];
  if (node?.kind !== "request" || stores.drafts.getState().byId[id] === undefined) return;
  applyRenamed(stores, id, node.name);
  // The rename moved the request's version on (APP-06): the draft's next
  // edit is one of the renamed request.
  if (result.version !== undefined && result.version > 0) setBaseVersion(stores, id, result.version);
}

/**
 * Duplicates a folder (with everything in it) or a request (COL-04): Go
 * puts the copy right after it, named "… copy". The queued edits of the
 * requests are sent first, so the copy has them. The copy is revealed and
 * selected, not opened. Resolves to the copy's id ("" when another project
 * was opened meanwhile); rejects with Go's error.
 */
export async function duplicateNode(stores: Stores, id: NodeId): Promise<NodeId> {
  await flushRequests(stores, requestIds(stores, selectSubtreeIds(stores.collection.getState(), id)));
  const result = await treeOp(stores, () => api.duplicateItem(id));
  if (result === null) return "";
  if (stores.collection.getState().nodes[result.nodeId] !== undefined) {
    revealNode(stores, result.nodeId);
    stores.collection.dispatch({ type: "collection/select", id: result.nodeId });
  }
  return result.nodeId;
}

/**
 * Moves a folder (with everything in it) or a request into folder
 * `parentId` (null: the top level) at `index`, its position there not
 * counting itself (END: last), or reorders it in its folder (COL-04). The
 * queued edits of its requests are sent first, so an edit in flight
 * cannot recreate the file at its old path. Ids do not change, so open
 * tabs and drafts keep working. The moved item is revealed and selected.
 * Rejects with Go's error (the tree is read again).
 */
export async function moveNode(stores: Stores, id: NodeId, parentId: NodeId | null, index: number = api.END): Promise<void> {
  await flushRequests(stores, requestIds(stores, selectSubtreeIds(stores.collection.getState(), id)));
  const result = await treeOp(stores, () => api.moveItem(id, parentId ?? "", index));
  if (result === null || stores.collection.getState().nodes[id] === undefined) return;
  revealNode(stores, id);
  stores.collection.dispatch({ type: "collection/select", id });
}

/** A delete that restoreNode can undo. */
export interface DeletedNode {
  /** The trash entry to restore. */
  readonly trashId: string;
  readonly nodeId: NodeId;
  readonly name: string;
  readonly kind: TreeNode["kind"];
  /** The project it was deleted from. */
  readonly workspaceId: string;
  /** The open tabs of the item and everything in it, with their places. */
  readonly tabs: readonly ClosedTab[];
  /** The active tab when it was deleted. */
  readonly activeId: TabId | null;
}

/**
 * Deletes a folder (with everything in it) or a request (COL-04): Go moves
 * it into the project's local trash. The queued edits of its requests are
 * sent and written first, so the trash has them; once Go has deleted it,
 * its tabs close. Resolves to what restoreNode needs to undo it; rejects
 * with Go's error (the tree is read again) and closes nothing.
 */
export async function deleteNode(stores: Stores, id: NodeId): Promise<DeletedNode> {
  const workspaceId = currentWorkspaceId(stores) ?? "";
  const subtree = selectSubtreeIds(stores.collection.getState(), id);
  await flushRequests(stores, requestIds(stores, subtree));

  // The tabs before the delete: Go's collection:changed may reload the tree,
  // which closes the tabs of gone items, before the call returns.
  const node = stores.collection.getState().nodes[id];
  const tabs = stores.tabs.getState();
  const inSubtree = new Set(subtree);
  const openTabs = tabs.order.flatMap((tabId, index) =>
    inSubtree.has(tabId) ? [{ id: tabId, index, pinned: tabId in tabs.pinnedIds }] : [],
  );

  let result: api.DeleteResultDTO;
  try {
    result = await api.deleteItem(id);
  } catch (err: unknown) {
    void loadCollection(stores);
    throw err;
  }
  const deleted: DeletedNode = {
    trashId: result.entry.id,
    nodeId: id,
    name: node?.name ?? result.entry.name,
    kind: node?.kind ?? "unknown",
    workspaceId,
    tabs: openTabs,
    activeId: tabs.activeId,
  };
  if (currentWorkspaceId(stores) !== workspaceId) return deleted;
  closeTabs(stores, subtree);
  applyTree(stores, workspaceId, result.tree);
  return deleted;
}

/**
 * Undoes deleteNode (COL-04): Go restores the item and everything in it
 * from the trash, into its folder at its old place (or at the end of the
 * top level when that folder is gone). The tabs that closed with it open
 * again where they were, pinned again, and the tab that was active then
 * is active again. The restored item is revealed and selected. Resolves
 * to its id ("" when it was not restored into the open project); rejects
 * with Go's error.
 */
export async function restoreNode(stores: Stores, deleted: DeletedNode): Promise<NodeId> {
  if (currentWorkspaceId(stores) !== deleted.workspaceId) return "";
  const activeBefore = stores.tabs.getState().activeId;
  const result = await treeOp(stores, () => api.restoreItem(deleted.trashId));
  if (result === null || result.nodeId === "") return "";
  const { nodes } = stores.collection.getState();
  if (nodes[result.nodeId] === undefined) return result.nodeId;

  // Restored items keep their ids unless another item took one meanwhile;
  // those tabs stay closed. Leftmost first, so each lands at its old index.
  const reopened = new Set<TabId>();
  for (const tab of [...deleted.tabs].sort((a, b) => a.index - b.index)) {
    if (nodes[tab.id] === undefined || stores.tabs.getState().order.includes(tab.id)) continue;
    stores.tabs.dispatch({ type: "tabs/open", id: tab.id, index: tab.index });
    if (tab.pinned) {
      setTabPinned(stores, tab.id, true);
      stores.tabs.dispatch({ type: "tabs/move", id: tab.id, toIndex: tab.index });
    }
    reopened.add(tab.id);
  }
  const active = deleted.activeId !== null && reopened.has(deleted.activeId) ? deleted.activeId : activeBefore;
  if (active !== null) activateTab(stores, active);

  revealNode(stores, result.nodeId);
  stores.collection.dispatch({ type: "collection/select", id: result.nodeId });
  return result.nodeId;
}
