// Drafts store: the editable copy of each open request and whether it has
// unsaved changes. The file on disk (Go side) stays the saved version;
// saving is a side effect in commands/ that ends with `drafts/markSaved`.
//
// While an editor has focus, its CodeMirror state is the live text. The
// editor pushes changes here (`drafts/edit`); it does not re-read its own
// value from the store on every keystroke.

import type { NodeId } from "./collection";
import { omitKeys } from "./util";

export type RequestId = NodeId;

/**
 * The editable fields of a request. A placeholder until the request file
 * model exists on the Go side; then this becomes that (generated) type.
 */
export type RequestData = Readonly<Record<string, unknown>>;

export interface Draft {
  readonly request: RequestData;
  readonly dirty: boolean;
  /** Bumped on every edit, so a save that finishes late cannot clear newer edits. */
  readonly revision: number;
}

export interface DraftsState {
  readonly byId: Readonly<Record<RequestId, Draft>>;
  /**
   * The sub-tab shown in each open request's editor ("params", "headers",
   * "body"), so coming back to a tab shows it again. UI state only:
   * never saved, dropped with the draft.
   */
  readonly subTabs: Readonly<Record<RequestId, string>>;
  /**
   * Bodies the body editor switched away from, per request and body
   * mode, so switching back to a mode restores what it had ("raw" text
   * after a detour through "none"). Not saved: the file keeps only the
   * members of the current mode. Dropped with the draft.
   */
  readonly bodyStash: Readonly<Record<RequestId, Readonly<Record<string, unknown>>>>;
  /**
   * Path-parameter rows whose placeholder left the URL (HTTP-05), per
   * request and name, so the value comes back when the name is typed again.
   * Not saved: the file keeps rows for the URL's names only. Dropped with
   * the draft.
   */
  readonly pathParamStash: Readonly<Record<RequestId, Readonly<Record<string, unknown>>>>;
  /**
   * Drafts whose file changed on disk while they had unsaved edits
   * (STO-05): their edits are kept but not sent until the user keeps them
   * or takes the file's version (commands/externalChanges.ts). Dropped with
   * the draft.
   */
  readonly conflicts: Readonly<Record<RequestId, true>>;
  /**
   * The conflicts (a subset of `conflicts`) with a version another window
   * saved (APP-06), not with the file on disk: the notice says so. Dropped
   * with the conflict.
   */
  readonly otherWindow: Readonly<Record<RequestId, true>>;
}

export const initialDraftsState: DraftsState = {
  byId: {},
  subTabs: {},
  bodyStash: {},
  pathParamStash: {},
  conflicts: {},
  otherWindow: {},
};

/** What a draft is in conflict with: its file on disk, or another window's edit. */
export type ConflictSource = "disk" | "window";

export type DraftsAction =
  /** Starts a clean draft from the saved request. Refreshes a clean draft; never overwrites a dirty one. */
  | { type: "drafts/open"; id: RequestId; request: RequestData }
  /** Merges `patch` into the draft's fields and marks it dirty. */
  | { type: "drafts/edit"; id: RequestId; patch: RequestData }
  /** A save of `revision` finished. Clears `dirty` only if nothing was edited since. */
  | { type: "drafts/markSaved"; id: RequestId; revision: number }
  /**
   * The request was renamed on disk (a tree operation): sets the draft's
   * `name` without marking it dirty or changing its revision, so the next
   * edit does not send the old name back.
   */
  | { type: "drafts/rename"; id: RequestId; name: string }
  /** Shows `subTab` in the editor of request `id`. */
  | { type: "drafts/setSubTab"; id: RequestId; subTab: string }
  /** Keeps `body` as the last body of mode `mode` of request `id`, to restore when switching back. */
  | { type: "drafts/stashBody"; id: RequestId; mode: string; body: unknown }
  /** Forgets the stashed bodies of request `id`: another body example became active (HTTP-17). */
  | { type: "drafts/clearBodyStash"; id: RequestId }
  /** Keeps path-parameter `rows` (by their `name`) of request `id`, to restore when their names come back. */
  | { type: "drafts/stashPathParams"; id: RequestId; rows: readonly { readonly name: string }[] }
  /**
   * Replaces the draft with `request` as saved (clean), whatever it had:
   * the version on disk was taken over unsaved edits (STO-05). Bumps the
   * revision, so a save of the old content finishing late cannot mark it.
   * Ends a conflict.
   */
  | { type: "drafts/replace"; id: RequestId; request: RequestData }
  /**
   * The draft's file changed on disk while it had unsaved edits (STO-05),
   * or another window changed the item (`source` "window", APP-06). A
   * conflict already open keeps its source.
   */
  | { type: "drafts/conflict"; id: RequestId; source?: ConflictSource }
  /** Ends the conflict of `id` (the user chose a version). */
  | { type: "drafts/resolve"; id: RequestId }
  /** Drops drafts and their sub-tabs, e.g. when their tabs close or their requests are deleted. */
  | { type: "drafts/discard"; ids: readonly RequestId[] };

export function draftsReducer(state: DraftsState, action: DraftsAction): DraftsState {
  switch (action.type) {
    case "drafts/open": {
      const current: Draft | undefined = state.byId[action.id];
      if (current?.dirty === true || current?.request === action.request) return state;
      const draft: Draft = { request: action.request, dirty: false, revision: current?.revision ?? 0 };
      return { ...state, byId: { ...state.byId, [action.id]: draft } };
    }

    case "drafts/edit": {
      const current: Draft | undefined = state.byId[action.id];
      if (current === undefined) return state;
      const draft: Draft = {
        request: { ...current.request, ...action.patch },
        dirty: true,
        revision: current.revision + 1,
      };
      return { ...state, byId: { ...state.byId, [action.id]: draft } };
    }

    case "drafts/markSaved": {
      const current: Draft | undefined = state.byId[action.id];
      if (current === undefined || !current.dirty || current.revision !== action.revision) return state;
      return { ...state, byId: { ...state.byId, [action.id]: { ...current, dirty: false } } };
    }

    case "drafts/rename": {
      const current: Draft | undefined = state.byId[action.id];
      if (current === undefined || current.request.name === action.name) return state;
      const draft: Draft = { ...current, request: { ...current.request, name: action.name } };
      return { ...state, byId: { ...state.byId, [action.id]: draft } };
    }

    case "drafts/replace": {
      const current: Draft | undefined = state.byId[action.id];
      if (current === undefined) return state;
      const draft: Draft = { request: action.request, dirty: false, revision: current.revision + 1 };
      return {
        ...state,
        byId: { ...state.byId, [action.id]: draft },
        conflicts: omitKeys(state.conflicts, [action.id]),
        otherWindow: omitKeys(state.otherWindow, [action.id]),
      };
    }

    case "drafts/conflict": {
      if (state.byId[action.id] === undefined || state.conflicts[action.id] === true) return state;
      const conflicts = { ...state.conflicts, [action.id]: true as const };
      if (action.source === "window") return { ...state, conflicts, otherWindow: { ...state.otherWindow, [action.id]: true } };
      return { ...state, conflicts };
    }

    case "drafts/resolve": {
      const conflicts = omitKeys(state.conflicts, [action.id]);
      const otherWindow = omitKeys(state.otherWindow, [action.id]);
      return conflicts === state.conflicts && otherWindow === state.otherWindow ? state : { ...state, conflicts, otherWindow };
    }

    case "drafts/setSubTab":
      if (state.subTabs[action.id] === action.subTab) return state;
      return { ...state, subTabs: { ...state.subTabs, [action.id]: action.subTab } };

    case "drafts/stashBody": {
      const current = state.bodyStash[action.id] ?? {};
      if (current[action.mode] === action.body) return state;
      return {
        ...state,
        bodyStash: { ...state.bodyStash, [action.id]: { ...current, [action.mode]: action.body } },
      };
    }

    case "drafts/clearBodyStash": {
      const bodyStash = omitKeys(state.bodyStash, [action.id]);
      return bodyStash === state.bodyStash ? state : { ...state, bodyStash };
    }

    case "drafts/stashPathParams": {
      const current = state.pathParamStash[action.id] ?? {};
      const changed = action.rows.filter((row) => current[row.name] !== row);
      if (changed.length === 0) return state;
      const next: Record<string, unknown> = { ...current };
      for (const row of changed) next[row.name] = row;
      return { ...state, pathParamStash: { ...state.pathParamStash, [action.id]: next } };
    }

    case "drafts/discard": {
      const byId = omitKeys(state.byId, action.ids);
      const subTabs = omitKeys(state.subTabs, action.ids);
      const bodyStash = omitKeys(state.bodyStash, action.ids);
      const pathParamStash = omitKeys(state.pathParamStash, action.ids);
      const conflicts = omitKeys(state.conflicts, action.ids);
      const otherWindow = omitKeys(state.otherWindow, action.ids);
      return byId === state.byId &&
        subTabs === state.subTabs &&
        bodyStash === state.bodyStash &&
        pathParamStash === state.pathParamStash &&
        conflicts === state.conflicts &&
        otherWindow === state.otherWindow
        ? state
        : { byId, subTabs, bodyStash, pathParamStash, conflicts, otherWindow };
    }

    default:
      return state;
  }
}

export function selectDraft(state: DraftsState, id: RequestId): Draft | undefined {
  return state.byId[id];
}

/** Whether the draft of `id` is in conflict with its file on disk (STO-05) or another window's edit (APP-06). */
export function selectHasConflict(state: DraftsState, id: RequestId): boolean {
  return state.conflicts[id] === true;
}

/** What the draft of `id` is in conflict with, or null. */
export function selectConflictSource(state: DraftsState, id: RequestId): ConflictSource | null {
  if (state.conflicts[id] !== true) return null;
  return state.otherWindow[id] === true ? "window" : "disk";
}

export function selectIsDirty(state: DraftsState, id: RequestId): boolean {
  return state.byId[id]?.dirty === true;
}

/** New array on each call: use with `shallowEqual`. */
export function selectDirtyIds(state: DraftsState): RequestId[] {
  return Object.keys(state.byId).filter((id) => state.byId[id]?.dirty === true);
}
