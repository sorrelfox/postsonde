// External changes (STO-05): Go polls the open project's files and sends
// collection:external-change when files were added, edited, removed or
// renamed outside PostSonde (an editor, git pull). This module applies it:
//
// - the tree is loaded again (loadCollection);
// - an open draft without unsaved edits whose file changed is reloaded from
//   disk, and its undo history is cleared (loadRequestDraft does both);
// - an open draft with unsaved edits whose file changed is in conflict
//   (drafts.conflicts): its edits are held, not sent, until the user keeps
//   them (keepMine) or takes the file's version (takeTheirs);
// - a request whose file was removed while its tab is open keeps its tab
//   and content, read-only (collection.deletedIds), until the tab is closed
//   or the file is written back (restoreDeletedRequest);
// - environment or global variable changes reload the environments, and
//   the global variables panel (onExternalGlobalsChange).
//
// Another window's edits (APP-06, collection:edited) are handled the same
// way (applyEditedElsewhere): an open draft of the item without edits of
// its own is reloaded; one with edits Go has not taken is in conflict with
// the other window's version (drafts.otherWindow says so).
//
// The polling pauses while every window is hidden. See
// docs/collection.md#external-changes, docs/windows.md and
// docs/frontend.md.

import * as api from "../lib/collection";
import { toAppError, type AppError } from "../lib/errors";
import type { NodeId, RequestId, Stores, TreeNode } from "../stores";
import {
  dropQueuedEdits,
  hasQueuedEdits,
  holdEdits,
  loadFolderDraft,
  loadRequestDraft,
  sendDraft,
  setBaseVersion,
  splitVersion,
  waitForQueuedEdits,
} from "./autosave";
import { loadCollection } from "./collection";
import { clearEditHistory } from "./editHistory";
import { loadEnvironments } from "./environments";
import { activateTab, closeTabs, openRequest } from "./tabs";
import { fromOtherWindow } from "./windowId";

const globalsListeners = new Set<() => void>();

/**
 * Calls `listener` when the global variables in postsonde.json changed
 * outside PostSonde, for views that loaded them. Returns the unsubscribe
 * function.
 */
export function onExternalGlobalsChange(listener: () => void): () => void {
  globalsListeners.add(listener);
  return () => {
    globalsListeners.delete(listener);
  };
}

function currentWorkspaceId(stores: Stores): string | null {
  return stores.workspace.getState().current?.id ?? null;
}

/** Puts draft `id` in conflict with its file: its edits are held until resolved. */
function startConflict(stores: Stores, id: RequestId): void {
  // Go set aside what it had; the draft keeps the edits, and keeping them sends it again.
  holdEdits(stores, id, "disk");
}

/** The request or folder `id` as Go holds it now, with its version. */
function fetchItem(stores: Stores, id: RequestId): Promise<api.RequestDTO | api.FolderDTO> {
  const folder = stores.collection.getState().nodes[id]?.kind === "folder";
  return folder ? api.getFolder(id) : api.getRequest(id);
}

/**
 * Applies a collection:edited event (APP-06): another window's edit of
 * requests or folders that Go took. For each open draft of them, once this
 * window's own edits of it have reached Go: a draft without edits of its
 * own since, or whose edits Go has taken, is replaced by Go's version (the
 * other window's edit was made on top of this window's); a draft whose
 * edits Go refused is already in conflict. A draft edited while Go's
 * version is fetched keeps its edit, which Go refuses: a conflict. Never
 * rejects.
 */
export async function applyEditedElsewhere(stores: Stores, event: api.EditedEventDTO): Promise<void> {
  if (event.workspaceId !== currentWorkspaceId(stores) || !fromOtherWindow(stores, event.window)) return;
  await Promise.all(
    (event.ids ?? []).map(async (id) => {
      if (stores.drafts.getState().byId[id] === undefined) return;
      await waitForQueuedEdits(stores, id);
      const before = stores.drafts.getState();
      const draft = before.byId[id];
      if (draft === undefined || before.conflicts[id] === true) return;
      let dto: api.RequestDTO | api.FolderDTO;
      try {
        dto = await fetchItem(stores, id);
      } catch (err: unknown) {
        console.warn(`edited in another window: ${id} could not be reloaded`, toAppError(err));
        return;
      }
      const after = stores.drafts.getState();
      if (after.byId[id]?.revision !== draft.revision || after.conflicts[id] === true || hasQueuedEdits(stores, id)) return;
      const { data, version } = splitVersion(dto);
      // Saves of this window's edits are now the other window's to report.
      dropQueuedEdits(stores, id);
      stores.drafts.dispatch({ type: "drafts/replace", id, request: data });
      setBaseVersion(stores, id, version);
      clearEditHistory(stores, [id]);
    }),
  );
}

/** Applies a collection:external-change event. Never rejects. */
export async function applyExternalChange(stores: Stores, event: api.ExternalChangeDTO): Promise<void> {
  if (event.workspaceId !== currentWorkspaceId(stores)) return;
  const before = stores.collection.getState();
  const open = new Set(stores.tabs.getState().order);
  const deleted = (event.removed ?? [])
    .map((id) => before.nodes[id])
    .filter((node): node is TreeNode => node?.kind === "request" && open.has(node.id));
  await loadCollection(stores, { deleted });

  const conflicts = new Set(event.conflicts ?? []);
  // An added ID with a draft is a file that came back (a checkout): as if edited.
  const changed = [...new Set([...(event.modified ?? []), ...(event.added ?? [])])];
  const nodes = stores.collection.getState().nodes;
  await Promise.all(
    changed.map(async (id) => {
      const draft = stores.drafts.getState().byId[id];
      if (draft === undefined) return;
      if (draft.dirty || conflicts.has(id) || stores.drafts.getState().conflicts[id] === true) {
        startConflict(stores, id);
        return;
      }
      try {
        if (nodes[id]?.kind === "folder") await loadFolderDraft(stores, id);
        else await loadRequestDraft(stores, id);
      } catch (err: unknown) {
        console.warn(`external change: ${id} could not be reloaded`, err);
      }
    }),
  );

  if (event.environments) void loadEnvironments(stores);
  if (event.globals) {
    for (const listener of [...globalsListeners]) listener();
  }
}

/**
 * Resolves the conflict of `id` by keeping the draft: its edits are sent
 * and written over the file's new version, or over the other window's
 * (APP-06): they are sent as an edit of the version Go holds now.
 */
export async function keepMine(stores: Stores, id: RequestId): Promise<void> {
  try {
    await api.resolveConflict(id);
  } catch (err: unknown) {
    console.warn(`external change: resolving ${id} failed`, toAppError(err));
  }
  try {
    setBaseVersion(stores, id, splitVersion(await fetchItem(stores, id)).version);
  } catch (err: unknown) {
    console.warn(`external change: the version of ${id} could not be read`, toAppError(err));
  }
  stores.drafts.dispatch({ type: "drafts/resolve", id });
  sendDraft(stores, id);
}

/**
 * Resolves the conflict of `id` by taking the file's version: the draft's
 * edits are dropped, the draft is loaded from disk, and its undo history
 * (of the dropped content) is cleared. Rejects with an AppError when the
 * file cannot be read.
 */
export async function takeTheirs(stores: Stores, id: RequestId): Promise<void> {
  dropQueuedEdits(stores, id);
  let dto: api.RequestDTO | api.FolderDTO;
  try {
    await api.resolveConflict(id);
    dto = await fetchItem(stores, id);
  } catch (err: unknown) {
    throw toAppError(err);
  }
  const { data, version } = splitVersion(dto);
  stores.drafts.dispatch({ type: "drafts/replace", id, request: data });
  setBaseVersion(stores, id, version);
  clearEditHistory(stores, [id]);
}

/** Both versions of a draft in conflict, as pretty JSON, for a diff. */
export interface ConflictVersions {
  readonly mine: string;
  readonly theirs: string;
}

/** Reads the file's version of `id` to show next to the draft. Rejects with an AppError. */
export async function loadConflictVersions(stores: Stores, id: RequestId): Promise<ConflictVersions> {
  const draft = stores.drafts.getState().byId[id];
  let theirs: unknown;
  try {
    theirs = splitVersion(await fetchItem(stores, id)).data;
  } catch (err: unknown) {
    throw toAppError(err);
  }
  return { mine: prettyJson(draft?.request ?? {}), theirs: prettyJson(theirs) };
}

/** JSON with two-space indentation and the `extra` members expanded, so a diff reads well. */
export function prettyJson(value: unknown): string {
  return JSON.stringify(value, (key, v: unknown) => {
    if (key === "extra" && typeof v === "string") {
      if (v === "" || v === "{}") return undefined;
      try {
        return JSON.parse(v) as unknown;
      } catch {
        return v;
      }
    }
    return v;
  }, 2);
}

/**
 * Writes request `id`, whose file was removed outside PostSonde, back into
 * the folder it was in (the top level when that is gone too) with the
 * content of its tab, and makes it an item of the tree again. When another
 * item has taken its ID meanwhile, the file gets a new one and a tab of its
 * own. Rejects with an AppError.
 */
export async function restoreDeletedRequest(stores: Stores, id: RequestId): Promise<void> {
  const node = stores.collection.getState().nodes[id];
  const draft = stores.drafts.getState().byId[id];
  if (node === undefined || draft === undefined) return;
  let res: api.NodeResultDTO;
  try {
    res = await api.recreateRequest(draft.request as unknown as api.RequestDTO, node.parentId ?? "");
  } catch (err: unknown) {
    throw toAppError(err);
  }
  dropQueuedEdits(stores, id);
  await loadCollection(stores);
  if (res.nodeId === id) {
    stores.drafts.dispatch({ type: "drafts/replace", id, request: draft.request });
    return;
  }
  openRequest(stores, res.nodeId);
  activateTab(stores, res.nodeId);
  closeTabs(stores, [id], { remember: false });
}

/** Closes the tab of a request whose file was removed outside PostSonde; its content is dropped. */
export function closeDeletedRequest(stores: Stores, id: RequestId): void {
  closeTabs(stores, [id], { remember: false });
}

/** Tells Go whether this window is shown: Go polls while any window is. */
function syncWatching(): void {
  const warn = (err: unknown) => console.warn("external change: pausing or resuming the watcher failed", toAppError(err) as AppError);
  try {
    api.setWatching(document.visibilityState !== "hidden").catch(warn);
  } catch (err: unknown) {
    warn(err);
  }
}

/**
 * Applies collection:external-change events, pauses Go's polling while the
 * window is hidden, and forgets removed requests whose tab was closed.
 * Call once near the root. Returns the stop function.
 */
export function followExternalChanges(stores: Stores): () => void {
  const stopEvents = api.onExternalChange((event) => void applyExternalChange(stores, event));
  // Another window's edits (APP-06).
  const stopEdited = api.onEdited((event) => void applyEditedElsewhere(stores, event));
  const stopTabs = stores.tabs.subscribe(() => {
    const { deletedIds } = stores.collection.getState();
    if (deletedIds.length === 0) return;
    const open = new Set<NodeId>(stores.tabs.getState().order);
    const closed = deletedIds.filter((id) => !open.has(id));
    if (closed.length > 0) stores.collection.dispatch({ type: "collection/forgetDeleted", ids: closed });
  });
  document.addEventListener("visibilitychange", syncWatching);
  return () => {
    stopEvents();
    stopEdited();
    stopTabs();
    document.removeEventListener("visibilitychange", syncWatching);
  };
}
