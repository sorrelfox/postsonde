// The history panel's intents (NAV-02, NAV-03, docs/history.md#the-history-panel):
// restoring an entry into the request it was sent from, as one undoable
// edit of its draft, or into a new scratch request; deleting an entry;
// clearing one request's history or all of it. The search and the filters
// are Go's (HistoryService.List); see useHistoryList.

import {
  clearHistory,
  clearRequestHistory,
  deleteHistoryEntry,
  restoreFromHistory,
  restorePatch,
  type RestoreDTO,
} from "../lib/history";
import type { HistorySummaryDTO } from "../lib/responseHistory";
import type { RequestId, Stores } from "../stores";
import { editRequest, loadRequestDraft } from "./autosave";
import { createScratchRequest } from "./collection";
import { openRequest } from "./tabs";
import { undoRequestEdit } from "./undoRedo";

/** Where an entry is restored: into the request it was sent from, or a new scratch request. */
export type RestoreTarget = "request" | "scratch";

export interface RestoreResult {
  /** The request that now holds the entry. */
  readonly requestId: RequestId;
  readonly target: RestoreTarget;
  /** What Go restored: `templated` and `hidden` say what the UI should tell. */
  readonly restored: RestoreDTO;
}

/** The request file kind an entry of `kind` is restored as. */
function fileKindOf(kind: string): "http" | "graphql" {
  return kind === "graphql" ? "graphql" : "http";
}

/**
 * Whether entry `e` can be restored into the request it was sent from: the
 * request is still in the project (or is a scratch request), of the same
 * kind, and can be written.
 */
export function canRestoreIntoRequest(stores: Stores, e: Pick<HistorySummaryDTO, "requestId" | "kind">): boolean {
  if (e.requestId === "" || e.kind === "replay") return false;
  const node = stores.collection.getState().nodes[e.requestId];
  if (node?.kind !== "request" || node.readOnly !== "") return false;
  const kind = node.fileKind === "" ? "http" : node.fileKind;
  return kind === fileKindOf(e.kind);
}

/**
 * Restores history entry `entry` (NAV-03). Into the request it was sent
 * from (`target` "request"): its draft is loaded when its tab is not open,
 * the restored fields are applied as one edit of its undo history (⌘Z
 * takes it back), auto-save writes it, and its tab opens. Into a new
 * scratch request named `scratchName` ("" lets Go name it): the request is
 * created, filled, then opened. Values the history hid are never written
 * as what is left of the mask (restorePatch). Resolves to null when the
 * request can no longer take it (deleted, made read-only, another project
 * opened); rejects with Go's error.
 */
export async function restoreHistoryEntry(
  stores: Stores,
  entry: HistorySummaryDTO,
  target: RestoreTarget,
  scratchName = "",
): Promise<RestoreResult | null> {
  const restored = await restoreFromHistory(entry.id);
  if (target === "request") {
    const id = restored.requestId;
    if (!canRestoreIntoRequest(stores, { requestId: id, kind: restored.kind })) return null;
    // Loaded before the tab opens, so the editor does not load it again
    // after the edit.
    if (stores.drafts.getState().byId[id] === undefined) await loadRequestDraft(stores, id);
    const current = stores.drafts.getState().byId[id]?.request ?? null;
    if (current === null) return null;
    editRequest(stores, id, restorePatch(restored, current), { history: "separate" });
    openRequest(stores, id);
    return { requestId: id, target, restored };
  }
  const id = await createScratchRequest(stores, fileKindOf(restored.kind), scratchName, false);
  if (id === "") return null;
  await loadRequestDraft(stores, id);
  editRequest(stores, id, restorePatch(restored, null), { history: "separate" });
  openRequest(stores, id);
  return { requestId: id, target, restored };
}

/** Takes back a restore into a request (its undo step). False when there is nothing to undo. */
export function undoRestore(stores: Stores, requestId: RequestId): boolean {
  return undoRequestEdit(stores, requestId);
}

/** Deletes one history entry. */
export async function deleteHistoryEntryCommand(id: string): Promise<void> {
  await deleteHistoryEntry(id);
}

/** Clears the history of request `requestId`; resolves with how many entries went. */
export async function clearRequestHistoryCommand(requestId: string): Promise<number> {
  return clearRequestHistory(requestId);
}

/** Clears the open project's whole history. */
export async function clearAllHistory(): Promise<void> {
  await clearHistory();
}
