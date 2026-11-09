// Request editor commands (HTTP-01): edits that touch more than the
// draft. Plain field edits go through editRequest / useAutoSave
// (commands/autosave.ts). See docs/frontend.md (Request editor).

import type { RequestId, Stores } from "../stores";
import { editRequest } from "./autosave";

/**
 * Changes the method of request `id`: auto-saves it like any edit and shows
 * the new method in the tree and tab badges at once, before Go writes the
 * file and the tree reloads. Does nothing for a request without a draft.
 */
export function setRequestMethod(stores: Stores, id: RequestId, method: string): void {
  if (stores.drafts.getState().byId[id] === undefined) return;
  editRequest(stores, id, { method }, { history: "separate" });
  stores.collection.dispatch({ type: "collection/setMethod", id, method });
}
