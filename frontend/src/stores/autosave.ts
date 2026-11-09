// Auto-save store (STO-03): which request edits are on their way to
// disk, which failed, and when the last one was saved. Go owns the writes
// (docs/collection.md#auto-save); commands/autosave.ts sends the edits and
// applies Go's collection:saved and collection:save-failed events here.
//
// `revision` is the auto-save sequence number the command sent with an edit
// (increasing per request for the whole app session), not the draft's
// revision: drafts start again at 0 when a tab reopens.

import type { RequestId } from "./drafts";
import { omitKeys } from "./util";

export interface SaveFailure {
  readonly revision: number;
  readonly message: string;
  readonly hint: string;
  readonly code: string;
  /** When the failure was reported, ISO 8601. */
  readonly at: string;
}

export interface AutosaveState {
  /** The latest revision sent for each request whose save is not confirmed yet. */
  readonly pending: Readonly<Record<RequestId, number>>;
  /** The last failed save of each request that has not been saved since. */
  readonly failed: Readonly<Record<RequestId, SaveFailure>>;
  /** When each request was last saved, RFC 3339. */
  readonly savedAt: Readonly<Record<RequestId, string>>;
  /** When any request was last saved, or null. */
  readonly lastSavedAt: string | null;
}

export const initialAutosaveState: AutosaveState = { pending: {}, failed: {}, savedAt: {}, lastSavedAt: null };

export type AutosaveAction =
  /** An edit with `revision` was sent to Go. */
  | { type: "autosave/queued"; id: RequestId; revision: number }
  /** Go wrote the edit with `revision` (collection:saved). Older pending revisions are saved too. */
  | { type: "autosave/saved"; id: RequestId; revision: number; at: string }
  /** Sending or writing the edit with `revision` failed. */
  | {
      type: "autosave/failed";
      id: RequestId;
      revision: number;
      error: { readonly message: string; readonly hint: string; readonly code: string };
      at: string;
    }
  /**
   * Forgets the pending saves and the failure of `id`: its edits were set
   * aside because its file changed on disk (STO-05), so Go reports nothing
   * more for them.
   */
  | { type: "autosave/abandon"; id: RequestId }
  /** Hides the failures of `ids` (all when omitted) without retrying them. */
  | { type: "autosave/dismiss"; ids?: readonly RequestId[] };

export function autosaveReducer(state: AutosaveState, action: AutosaveAction): AutosaveState {
  switch (action.type) {
    case "autosave/queued": {
      if (state.pending[action.id] === action.revision) return state;
      return { ...state, pending: { ...state.pending, [action.id]: action.revision } };
    }

    case "autosave/saved": {
      const { id, revision, at } = action;
      const pending = (state.pending[id] ?? Infinity) <= revision ? omitKeys(state.pending, [id]) : state.pending;
      const failed = (state.failed[id]?.revision ?? Infinity) <= revision ? omitKeys(state.failed, [id]) : state.failed;
      return { pending, failed, savedAt: { ...state.savedAt, [id]: at }, lastSavedAt: at };
    }

    case "autosave/failed": {
      const { id, revision, error, at } = action;
      const current = state.failed[id];
      if (current !== undefined && current.revision > revision) return state;
      const failure: SaveFailure = { revision, message: error.message, hint: error.hint, code: error.code, at };
      const pending = (state.pending[id] ?? Infinity) <= revision ? omitKeys(state.pending, [id]) : state.pending;
      return { ...state, pending, failed: { ...state.failed, [id]: failure } };
    }

    case "autosave/abandon": {
      const pending = omitKeys(state.pending, [action.id]);
      const failed = omitKeys(state.failed, [action.id]);
      return pending === state.pending && failed === state.failed ? state : { ...state, pending, failed };
    }

    case "autosave/dismiss": {
      const failed = action.ids === undefined ? {} : omitKeys(state.failed, action.ids);
      if (Object.keys(failed).length === Object.keys(state.failed).length) return state;
      return { ...state, failed };
    }

    default:
      return state;
  }
}

export type RequestSaveStatus = "idle" | "saving" | "saved" | "error";

/** The save state of one request: a failure wins over a newer pending edit, so it stays visible. */
export function selectRequestSaveStatus(state: AutosaveState, id: RequestId): RequestSaveStatus {
  if (state.failed[id] !== undefined) return "error";
  if (state.pending[id] !== undefined) return "saving";
  return state.savedAt[id] !== undefined ? "saved" : "idle";
}

export interface SaveSummary {
  readonly status: RequestSaveStatus;
  readonly pendingCount: number;
  readonly failedCount: number;
  readonly lastSavedAt: string | null;
  /** The most recent failure, or null. */
  readonly failure: SaveFailure | null;
}

/** For the status bar. New object on each call: use with `shallowEqual`. */
export function selectSaveSummary(state: AutosaveState): SaveSummary {
  const failures = Object.values(state.failed);
  const pendingCount = Object.keys(state.pending).length;
  let failure: SaveFailure | null = null;
  for (const f of failures) {
    if (failure === null || f.at > failure.at) failure = f;
  }
  const status: RequestSaveStatus =
    failure !== null ? "error" : pendingCount > 0 ? "saving" : state.lastSavedAt !== null ? "saved" : "idle";
  return { status, pendingCount, failedCount: failures.length, lastSavedAt: state.lastSavedAt, failure };
}
