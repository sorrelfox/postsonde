// Undo and redo of request edits (NAV-07). See docs/frontend.md (Undo and
// redo) and commands/editHistory.ts for what is recorded.
//
// Where ⌘Z / ⇧⌘Z (Ctrl+Z / Ctrl+Shift+Z or Ctrl+Y) go:
// - In a CodeMirror editor: that editor's own history (its keymap,
//   components/editor/extensions.ts). The editor keeps it across tab
//   switches through the editor states of commands/editHistory.ts.
// - Anywhere else inside a request's editor panel (the element marked with
//   data-edit-history, components/tabs/TabPanel.tsx): the request's draft
//   history, applied through the normal edit path so auto-save writes it.
// - The Undo toast of a delete (collection.undoDelete) still takes ⌘Z
//   first while it shows and focus is not in a text field: the window
//   shortcut listener runs in the capture phase, this one after everything
//   else, in the bubble phase.

import { matchesShortcut } from "../lib/shortcuts";
import type { RequestId, Stores } from "../stores";
import { applyRequestEdit } from "./autosave";
import { clearEditHistory, takeRedo, takeUndo, type HistoryStep } from "./editHistory";

/** Marks the element holding a request's editor; its value is the request id. */
export const EDIT_HISTORY_ATTRIBUTE = "data-edit-history";

function apply(stores: Stores, id: RequestId, fields: HistoryStep["before"]): void {
  applyRequestEdit(stores, id, fields);
  // The tree and tab badges show the method at once, as setRequestMethod does.
  const method = fields.method;
  if (typeof method === "string") stores.collection.dispatch({ type: "collection/setMethod", id, method });
}

/** Undoes the last edit of request `id`. Returns false when there is nothing to undo or no draft. */
export function undoRequestEdit(stores: Stores, id: RequestId): boolean {
  if (stores.drafts.getState().byId[id] === undefined) return false;
  const step = takeUndo(stores, id);
  if (step === null) return false;
  apply(stores, id, step.before);
  return true;
}

/** Redoes the last undone edit of request `id`. Returns false when there is nothing to redo or no draft. */
export function redoRequestEdit(stores: Stores, id: RequestId): boolean {
  if (stores.drafts.getState().byId[id] === undefined) return false;
  const step = takeRedo(stores, id);
  if (step === null) return false;
  apply(stores, id, step.after);
  return true;
}

function isElement(target: EventTarget | null): target is Element {
  return target !== null && typeof (target as Element).closest === "function";
}

/**
 * Handles one keydown for the draft history. Returns "undo" or "redo" when
 * it took the key press (and prevented its default), else null.
 */
export function handleUndoKeyDown(stores: Stores, e: KeyboardEvent): "undo" | "redo" | null {
  if (e.defaultPrevented || e.isComposing || e.keyCode === 229) return null;
  const undo = matchesShortcut(e, "request.undo");
  if (!undo && !matchesShortcut(e, "request.redo")) return null;
  const target = e.target;
  if (!isElement(target)) return null;
  // A CodeMirror editor has its own history (a read-only one has none, and
  // does not undo the request either).
  if (target.closest(".cm-editor") !== null) return null;
  const id = target.closest(`[${EDIT_HISTORY_ATTRIBUTE}]`)?.getAttribute(EDIT_HISTORY_ATTRIBUTE);
  if (id === undefined || id === null || stores.drafts.getState().byId[id] === undefined) return null;
  // Taken even with nothing to undo: a text field's own undo would change
  // the field behind the draft's back.
  e.preventDefault();
  if (undo) undoRequestEdit(stores, id);
  else redoRequestEdit(stores, id);
  return undo ? "undo" : "redo";
}

/**
 * Runs undo and redo for request editors and drops the history of requests
 * whose draft goes away (their tab closed, or they were deleted). Call once
 * while a project is open. Returns the stop function.
 */
export function followEditHistory(stores: Stores, target: Window = window): () => void {
  const onKeyDown = (e: KeyboardEvent) => {
    handleUndoKeyDown(stores, e);
  };
  target.addEventListener("keydown", onKeyDown);

  let open = new Set(Object.keys(stores.drafts.getState().byId));
  const stopDrafts = stores.drafts.subscribe(() => {
    const now = new Set(Object.keys(stores.drafts.getState().byId));
    const gone = [...open].filter((id) => !now.has(id));
    open = now;
    if (gone.length > 0) clearEditHistory(stores, gone);
  });
  return () => {
    target.removeEventListener("keydown", onKeyDown);
    stopDrafts();
  };
}
