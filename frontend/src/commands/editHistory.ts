// Edit history of open requests (NAV-07): what undo and redo walk back and
// forth, kept per request while its tab is open, across tab switches, and
// dropped when the tab closes or the request is reloaded from disk.
// See docs/frontend.md (Undo and redo).
//
// Two kinds of history per request:
// - The draft history: every edit that went through editRequest, as the
//   fields it changed before and after. Rapid edits of the same fields
//   (typing in a table cell, in a text field) coalesce into one step.
//   commands/undoRedo.ts applies its steps through the normal edit path,
//   so auto-save writes the result.
// - The editor states: each CodeMirror editor of the request keeps its
//   EditorState (text, selection and CodeMirror's own undo history) here
//   when it unmounts, and takes it back when it mounts again
//   (components/editor/editorHistory.tsx). Values are opaque here, so this
//   module needs no CodeMirror.
//
// Nothing here is saved or shared between projects: the data lives per
// Stores, like the auto-save queues.

import type { RequestData, RequestId, Stores } from "../stores";

/** Edits of the same fields closer together than this are one undo step. */
export const COALESCE_MS = 1000;

/** At most this many undo steps per request; the oldest go first. */
export const MAX_HISTORY = 200;

/**
 * How an edit joins the history. "coalesce" (the default) merges it into the
 * previous step when it follows quickly and touches the same fields, as for
 * typing. "separate" always makes it a step of its own, and the next edit
 * does not merge into it (a method or body mode switch).
 */
export type HistoryMode = "coalesce" | "separate";

/** One undo step: the changed fields as they were before and after. */
export interface HistoryStep {
  readonly before: Record<string, unknown>;
  readonly after: Record<string, unknown>;
}

interface MutableStep {
  before: Record<string, unknown>;
  after: Record<string, unknown>;
}

interface RequestHistory {
  past: MutableStep[];
  future: MutableStep[];
  /** When the last step was recorded or extended. */
  lastAt: number;
  /** The next edit starts a new step. */
  sealed: boolean;
  /** Editor states by editor key (opaque values, see the file comment). */
  editors: Map<string, unknown>;
  /** Editor keys of the editors mounted now. */
  inUse: Set<string>;
}

interface StoresHistory {
  byId: Map<RequestId, RequestHistory>;
  /** Bumped per request whenever its history is cleared. */
  generations: Map<RequestId, number>;
  listeners: Set<() => void>;
}

const byStores = new WeakMap<Stores, StoresHistory>();

function all(stores: Stores): StoresHistory {
  let h = byStores.get(stores);
  if (h === undefined) {
    h = { byId: new Map(), generations: new Map(), listeners: new Set() };
    byStores.set(stores, h);
  }
  return h;
}

function historyOf(stores: Stores, id: RequestId): RequestHistory {
  const map = all(stores).byId;
  let h = map.get(id);
  if (h === undefined) {
    h = { past: [], future: [], lastAt: 0, sealed: true, editors: new Map(), inUse: new Set() };
    map.set(id, h);
  }
  return h;
}

/**
 * Records an edit of request `id` that changed the draft from `prev` to
 * `next` through a patch of `keys`. Fields of `keys` that did not change are
 * left out; an edit that changed nothing is not recorded. Clears the redo
 * steps.
 */
export function recordEdit(
  stores: Stores,
  id: RequestId,
  prev: RequestData,
  next: RequestData,
  keys: readonly string[],
  mode: HistoryMode = "coalesce",
  now: number = Date.now(),
): void {
  const changed = keys.filter((k) => prev[k] !== next[k]);
  if (changed.length === 0) return;
  const h = historyOf(stores, id);
  h.future = [];
  const top = h.past[h.past.length - 1];
  const merge =
    mode === "coalesce" &&
    top !== undefined &&
    !h.sealed &&
    now - h.lastAt <= COALESCE_MS &&
    changed.some((k) => k in top.after);
  if (merge) {
    for (const k of changed) {
      if (!(k in top.before)) top.before[k] = prev[k];
      top.after[k] = next[k];
    }
  } else {
    const step: MutableStep = { before: {}, after: {} };
    for (const k of changed) {
      step.before[k] = prev[k];
      step.after[k] = next[k];
    }
    h.past.push(step);
    if (h.past.length > MAX_HISTORY) h.past.splice(0, h.past.length - MAX_HISTORY);
  }
  h.sealed = mode === "separate";
  h.lastAt = now;
}

/** Takes the step to undo (moving it to the redo steps), or null when there is none. */
export function takeUndo(stores: Stores, id: RequestId): HistoryStep | null {
  const h = all(stores).byId.get(id);
  const step = h?.past.pop();
  if (h === undefined || step === undefined) return null;
  h.future.push(step);
  h.sealed = true;
  return step;
}

/** Takes the step to redo (moving it back to the undo steps), or null when there is none. */
export function takeRedo(stores: Stores, id: RequestId): HistoryStep | null {
  const h = all(stores).byId.get(id);
  const step = h?.future.pop();
  if (h === undefined || step === undefined) return null;
  h.past.push(step);
  h.sealed = true;
  return step;
}

/** How many steps request `id` can undo and redo. */
export function historyDepth(stores: Stores, id: RequestId): { undo: number; redo: number } {
  const h = all(stores).byId.get(id);
  return { undo: h?.past.length ?? 0, redo: h?.future.length ?? 0 };
}

/**
 * Drops the whole history of `ids`: undo steps and editor states. For tabs
 * that close and requests reloaded from disk. Editors of these requests that
 * are mounted now start over (editHistoryGeneration changes).
 */
export function clearEditHistory(stores: Stores, ids: readonly RequestId[]): void {
  const h = all(stores);
  let changed = false;
  for (const id of ids) {
    h.byId.delete(id);
    h.generations.set(id, (h.generations.get(id) ?? 0) + 1);
    changed = true;
  }
  if (changed) for (const listener of [...h.listeners]) listener();
}

/** Bumped each time the history of `id` is cleared (for editors to start over). */
export function editHistoryGeneration(stores: Stores, id: RequestId): number {
  return all(stores).generations.get(id) ?? 0;
}

/** Called after any clearEditHistory. Returns the unsubscribe function. */
export function subscribeEditHistory(stores: Stores, listener: () => void): () => void {
  const h = all(stores);
  h.listeners.add(listener);
  return () => {
    h.listeners.delete(listener);
  };
}

/**
 * Where the editors of request `id` keep their states. Bound to the
 * current generation: after the history is cleared, a scope made before
 * stores nothing, so an editor that unmounts late (its tab just closed)
 * does not bring an old state back.
 */
export interface EditorStateScope {
  /** Claims `key` for a mounted editor. False when another mounted editor holds it. */
  acquire(key: string): boolean;
  /** The state kept under `key`, if any. */
  get(key: string): unknown;
  /** Keeps `state` under `key` and frees the key (the editor unmounted or changed document). */
  release(key: string, state: unknown): void;
}

export function editorStateScope(stores: Stores, id: RequestId): EditorStateScope {
  const generation = editHistoryGeneration(stores, id);
  const live = () => editHistoryGeneration(stores, id) === generation;
  return {
    acquire(key) {
      if (!live()) return false;
      const h = historyOf(stores, id);
      if (h.inUse.has(key)) return false;
      h.inUse.add(key);
      return true;
    },
    get(key) {
      return live() ? all(stores).byId.get(id)?.editors.get(key) : undefined;
    },
    release(key, state) {
      if (!live()) return;
      const h = historyOf(stores, id);
      h.inUse.delete(key);
      h.editors.set(key, state);
    },
  };
}
