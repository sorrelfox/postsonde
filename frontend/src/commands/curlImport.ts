// cURL import commands (IMP-01, docs/curl.md, docs/frontend.md "cURL
// import"):
//
// - replaceWithCurl: replaces an http request's draft with the request Go
//   mapped from a curl command, as one edit through editRequest, so ⌘Z
//   (NAV-07) puts the request back.
// - openImportCurlDialog: asks the mounted Import cURL dialog host
//   (useNewItemDialog) to open, optionally with a command filled in.
//
// The dialog listeners are callbacks, not plain data, so they live in this
// module instead of a store (as commands/toasts.ts does).

import type { RequestDTO } from "../lib/collection";
import { importPatch } from "../lib/curl";
import type { RequestId, Stores } from "../stores";
import { editRequest } from "./autosave";
import { editHistoryGeneration } from "./editHistory";
import { undoRequestEdit } from "./undoRedo";

type DialogListener = (text: string) => void;
const dialogListeners = new Set<DialogListener>();

/** Opens the Import cURL dialog with `text` in its command field. Returns false when no host is mounted. */
export function openImportCurlDialog(text = ""): boolean {
  if (dialogListeners.size === 0) return false;
  dialogListeners.forEach((l) => l(text));
  return true;
}

/** For the dialog host: called with the text whenever the dialog is asked for. */
export function onImportCurlDialog(listener: DialogListener): () => void {
  dialogListeners.add(listener);
  return () => {
    dialogListeners.delete(listener);
  };
}

/**
 * Replaces the draft of request `id` with `imported` (lib/curl.ts
 * importPatch: the name and description stay) as one separate undo step,
 * and shows the new method in the tree and tab badges at once. Returns the
 * undo of exactly this edit, which does nothing once another edit
 * followed it or the history was cleared; null when `id` has no draft.
 */
export function replaceWithCurl(stores: Stores, id: RequestId, imported: RequestDTO): (() => boolean) | null {
  const draft = stores.drafts.getState().byId[id];
  if (draft === undefined) return null;
  const current = draft.request as unknown as RequestDTO;
  editRequest(stores, id, importPatch(current, imported), { history: "separate" });
  stores.collection.dispatch({ type: "collection/setMethod", id, method: imported.method });
  const after = stores.drafts.getState().byId[id]?.request;
  // Nothing changed (the same command again): nothing to undo.
  if (after === undefined || after === draft.request) return () => false;
  const generation = editHistoryGeneration(stores, id);
  return () => {
    const now = stores.drafts.getState().byId[id]?.request;
    if (now !== after || editHistoryGeneration(stores, id) !== generation) return false;
    return undoRequestEdit(stores, id);
  };
}
