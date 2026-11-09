// Auto-save commands (STO-03): every edit of a request goes to Go at
// once, and Go writes it to disk once edits pause. There is no save button.
// See docs/collection.md#auto-save.
//
// - editRequest applies a patch to the draft and sends the whole request.
// - Sends of one request run one at a time, so Go gets them in order; edits
//   made while one is in flight collapse into the latest.
// - Go's collection:saved and collection:save-failed events update the
//   autosave store and mark the draft saved (followAutoSave).
// - flushRequests (closing tabs, through the onTabClosed hook of
//   commands/tabs.ts) and flushAllRequests (page hidden) send what is
//   queued and ask Go to write now.
// - A folder's settings (COL-03) are a draft too, with kind "folder": the
//   same queue sends them with updateFolder instead of updateRequest, and
//   Go answers with the same events (loadFolderDraft).
// - While a draft is in conflict with its file on disk (STO-05,
//   drafts.conflicts), its edits are queued but not sent, until
//   commands/externalChanges.ts resolves it (sendDraft or dropQueuedEdits).
// - Several windows (APP-06, docs/windows.md#edits): each edit is sent as
//   an edit of the version its draft was made from (Queue.version, from
//   loading the draft and from each accepted edit). Go refuses an edit of
//   a version another window has moved on (collection.edited_elsewhere):
//   the draft is then in conflict with that window's version, like a
//   change on disk. Save events of another window's edits are not this
//   window's (commands/windowId.ts).

import * as api from "../lib/collection";
import { toAppError, type AppError } from "../lib/errors";
import type { ConflictSource, RequestData, RequestId, Stores } from "../stores";
import { clearEditHistory, recordEdit, type HistoryMode } from "./editHistory";
import { onTabClosed } from "./tabs";
import { fromOtherWindow } from "./windowId";
import { onWorkspaceLeaving } from "./workspaceLeave";

interface Queue {
  /** The last auto-save revision handed out for this request. */
  seq: number;
  /** The latest edit not sent yet: a request, or a folder's settings. */
  next: { request: api.RequestDTO | api.FolderDTO; revision: number } | null;
  /** The send in flight, or null. */
  inflight: Promise<void> | null;
  /** Draft revision of each sent auto-save revision, until it is saved. */
  draftRevisions: Map<number, number>;
}

const queuesByStores = new WeakMap<Stores, Map<RequestId, Queue>>();

function queues(stores: Stores): Map<RequestId, Queue> {
  let map = queuesByStores.get(stores);
  if (map === undefined) {
    map = new Map();
    queuesByStores.set(stores, map);
  }
  return map;
}

function queueFor(stores: Stores, id: RequestId): Queue {
  const map = queues(stores);
  let q = map.get(id);
  if (q === undefined) {
    q = { seq: 0, next: null, inflight: null, draftRevisions: new Map() };
    map.set(id, q);
  }
  return q;
}

/**
 * The version in Go each draft is made from (APP-06), sent with its next
 * edit; missing while unknown (not checked). Kept apart from the queues, so
 * a draft never edited has no queue (nothing to flush).
 */
const versionsByStores = new WeakMap<Stores, Map<RequestId, number>>();

function versions(stores: Stores): Map<RequestId, number> {
  let map = versionsByStores.get(stores);
  if (map === undefined) {
    map = new Map();
    versionsByStores.set(stores, map);
  }
  return map;
}

function now(): string {
  return new Date().toISOString();
}

function reportFailure(stores: Stores, id: RequestId, revision: number, error: AppError): void {
  stores.autosave.dispatch({ type: "autosave/failed", id, revision, error, at: now() });
}

/** Whether the edits of `id` are held back: its file changed on disk (STO-05) or another window changed it (APP-06). */
function held(stores: Stores, id: RequestId): boolean {
  return stores.drafts.getState().conflicts[id] === true;
}

/**
 * Puts the draft of `id` in conflict (drafts.conflicts): Go set aside what
 * it had (a change on disk) or refused the edit (another window's): the
 * draft keeps the edits, and keeping them sends it again.
 */
export function holdEdits(stores: Stores, id: RequestId, source: ConflictSource): void {
  stores.drafts.dispatch({ type: "drafts/conflict", id, source });
  dropQueuedEdits(stores, id);
}

function pump(stores: Stores, id: RequestId, q: Queue): void {
  if (q.inflight !== null || q.next === null || held(stores, id)) return;
  const { request, revision } = q.next;
  q.next = null;
  const base = versions(stores).get(id);
  const sent: Promise<void> = (async () => {
    try {
      const res = isFolderDraft(request)
        ? await api.updateFolder(request, revision, base)
        : await api.updateRequest(request, revision, base);
      // The next edit is one of this version.
      if (typeof res?.version === "number") versions(stores).set(id, res.version);
    } catch (err: unknown) {
      // Refused at once (read-only, deleted, invalid): Go sends no event.
      const appErr = toAppError(err);
      // Its file changed on disk, or another window changed it: the
      // conflict (drafts.conflicts) holds the edit, and resolving it sends
      // the draft again. Not a failure.
      if (appErr.code === api.COLLECTION_CODES.changedOnDisk) {
        holdEdits(stores, id, "disk");
        return;
      }
      if (appErr.code === api.COLLECTION_CODES.editedElsewhere) {
        holdEdits(stores, id, "window");
        return;
      }
      console.warn(`autosave: ${id} was not saved`, appErr);
      reportFailure(stores, id, revision, appErr);
    }
  })().then(() => {
    if (q.inflight === sent) q.inflight = null;
    pump(stores, id, q);
  });
  q.inflight = sent;
}

/** A folder's settings (loadFolderDraft) rather than a request. */
function isFolderDraft(draft: api.RequestDTO | api.FolderDTO): draft is api.FolderDTO {
  return draft.kind === "folder";
}

/** Waits until nothing of `q` is queued or in flight. */
async function drain(q: Queue): Promise<void> {
  while (q.inflight !== null) await q.inflight;
}

/**
 * Sends `request`, the draft of `id` at `draftRevision`, to be auto-saved.
 * Use editRequest from editors; this is for callers that replace the whole
 * request.
 */
export function saveRequest(stores: Stores, id: RequestId, request: RequestData, draftRevision: number): void {
  const q = queueFor(stores, id);
  q.seq += 1;
  // Drafts hold what getRequest (loadRequestDraft) or getFolder
  // (loadFolderDraft) returned plus patches of its fields.
  q.next = { request: request as unknown as api.RequestDTO | api.FolderDTO, revision: q.seq };
  q.draftRevisions.set(q.seq, draftRevision);
  // A held edit is not being saved: the status bar does not say "Saving…".
  if (!held(stores, id)) stores.autosave.dispatch({ type: "autosave/queued", id, revision: q.seq });
  pump(stores, id, q);
}

/**
 * Sends the current draft of `id` if it has unsaved edits: after its
 * conflict was resolved by keeping it (STO-05), whatever was sent before
 * was set aside by Go.
 */
export function sendDraft(stores: Stores, id: RequestId): void {
  const draft = stores.drafts.getState().byId[id];
  if (draft === undefined || !draft.dirty) return;
  saveRequest(stores, id, draft.request, draft.revision);
}

/**
 * Forgets the edits of `id` queued and not sent, and the saves Go will not
 * report any more (it set them aside, STO-05): the version on disk was
 * taken, or the conflict started.
 */
export function dropQueuedEdits(stores: Stores, id: RequestId): void {
  const q = queues(stores).get(id);
  if (q !== undefined) {
    q.next = null;
    q.draftRevisions.clear();
  }
  stores.autosave.dispatch({ type: "autosave/abandon", id });
}

export interface EditOptions {
  /** How the edit joins the undo history (NAV-07, commands/editHistory.ts). Default "coalesce". */
  readonly history?: HistoryMode;
}

/**
 * Applies `patch` to the draft of `id`, marks it dirty and auto-saves it,
 * and records it in the request's undo history (NAV-07). Does nothing for
 * a request without an open draft.
 */
export function editRequest(stores: Stores, id: RequestId, patch: RequestData, options: EditOptions = {}): void {
  const prev = stores.drafts.getState().byId[id]?.request;
  applyRequestEdit(stores, id, patch);
  const next = stores.drafts.getState().byId[id]?.request;
  if (prev !== undefined && next !== undefined && next !== prev) {
    recordEdit(stores, id, prev, next, Object.keys(patch), options.history);
  }
}

/**
 * editRequest without recording the edit in the undo history: for undo and
 * redo themselves (commands/undoRedo.ts).
 */
export function applyRequestEdit(stores: Stores, id: RequestId, patch: RequestData): void {
  stores.drafts.dispatch({ type: "drafts/edit", id, patch });
  const draft = stores.drafts.getState().byId[id];
  if (draft === undefined || !draft.dirty) return;
  saveRequest(stores, id, draft.request, draft.revision);
}

/**
 * Splits the version (APP-06) off a request or folder as Go returned it:
 * drafts hold the content only. A missing version is 0.
 */
export function splitVersion(dto: api.RequestDTO | api.FolderDTO): { data: RequestData; version: number } {
  const { version, ...data } = dto;
  return { data: data as unknown as RequestData, version: version ?? 0 };
}

/** Records that the draft of `id` is made from `version` in Go (APP-06): the next edit is sent as an edit of it. */
export function setBaseVersion(stores: Stores, id: RequestId, version: number): void {
  versions(stores).set(id, version);
}

/**
 * Reads the version Go holds of request `id` for its draft, once the
 * draft's queued edits have reached Go: a rename moved it on (APP-06). A
 * draft edited meanwhile keeps the version it was made from (Go refuses
 * that edit: a conflict). Never rejects.
 */
export async function refreshBaseVersion(stores: Stores, id: RequestId): Promise<void> {
  await waitForQueuedEdits(stores, id);
  const revision = stores.drafts.getState().byId[id]?.revision;
  if (revision === undefined || held(stores, id)) return;
  let dto: api.RequestDTO;
  try {
    dto = await api.getRequest(id);
  } catch (err: unknown) {
    console.warn(`autosave: the version of ${id} could not be read`, toAppError(err));
    return;
  }
  if (stores.drafts.getState().byId[id]?.revision !== revision || held(stores, id) || hasQueuedEdits(stores, id)) return;
  setBaseVersion(stores, id, splitVersion(dto).version);
}

/** Whether edits of `id` are queued or in flight. */
export function hasQueuedEdits(stores: Stores, id: RequestId): boolean {
  const q = queues(stores).get(id);
  return q !== undefined && (q.next !== null || q.inflight !== null);
}

/** Opens `dto` as the draft of `id`, with its version, unless the draft has unsaved edits: those keep theirs. */
function openDraft(stores: Stores, id: RequestId, dto: api.RequestDTO | api.FolderDTO): void {
  const { data, version } = splitVersion(dto);
  // A clean draft that is already open gets replaced: a reload from disk,
  // after which the undo history of the old content no longer applies (NAV-07).
  const current = stores.drafts.getState().byId[id];
  if (current !== undefined && !current.dirty) clearEditHistory(stores, [id]);
  stores.drafts.dispatch({ type: "drafts/open", id, request: data });
  // A dirty draft keeps its content, and so the version it was made from.
  if (stores.drafts.getState().byId[id]?.request === data) setBaseVersion(stores, id, version);
}

/** Loads a request (with its unsaved edits) into a draft. Rejects with an AppError. */
export async function loadRequestDraft(stores: Stores, id: RequestId): Promise<void> {
  let dto: api.RequestDTO;
  try {
    dto = await api.getRequest(id);
  } catch (err: unknown) {
    throw toAppError(err);
  }
  openDraft(stores, id, dto);
}

/** Loads a folder's settings (with unsaved edits) into a draft (COL-03). Rejects with an AppError. */
export async function loadFolderDraft(stores: Stores, id: RequestId): Promise<void> {
  let dto: api.FolderDTO;
  try {
    dto = await api.getFolder(id);
  } catch (err: unknown) {
    throw toAppError(err);
  }
  openDraft(stores, id, dto);
}

/**
 * A tree operation renamed request `id` to `name` (Go wrote its pending
 * edits first): puts the name into its draft and into an edit queued but
 * not sent yet, so neither sends the old name back. An edit already in
 * flight cannot be changed: a rename command must await flushRequests for
 * the request before calling renameItem (docs/collection.md).
 */
export function applyRenamed(stores: Stores, id: RequestId, name: string): void {
  stores.drafts.dispatch({ type: "drafts/rename", id, name });
  const next = queues(stores).get(id)?.next;
  if (next !== undefined && next !== null && next.request.name !== name) {
    next.request = { ...next.request, name };
  }
}

/** Applies a collection:saved event. Another window's saves carry its revisions, not this window's: ignored. */
export function applySaved(stores: Stores, ev: api.SavedEventDTO): void {
  if (fromOtherWindow(stores, ev.window)) return;
  stores.autosave.dispatch({ type: "autosave/saved", id: ev.id, revision: ev.revision, at: ev.savedAt });
  const q = queues(stores).get(ev.id);
  if (q === undefined) return;
  const draftRevision = q.draftRevisions.get(ev.revision);
  for (const revision of [...q.draftRevisions.keys()]) {
    if (revision <= ev.revision) q.draftRevisions.delete(revision);
  }
  if (draftRevision !== undefined) {
    stores.drafts.dispatch({ type: "drafts/markSaved", id: ev.id, revision: draftRevision });
  }
}

/** Applies a collection:save-failed event, unless another window's edit failed. */
export function applySaveFailed(stores: Stores, ev: api.SaveFailedEventDTO): void {
  if (fromOtherWindow(stores, ev.window)) return;
  const appErr = toAppError(ev.error);
  console.warn(`autosave: writing ${ev.id} failed`, appErr);
  reportFailure(stores, ev.id, ev.revision, appErr);
}

function hasSomethingToFlush(stores: Stores, id: RequestId): boolean {
  const { pending, failed } = stores.autosave.getState();
  return queues(stores).has(id) || pending[id] !== undefined || failed[id] !== undefined;
}

/**
 * Sends the queued edits of `ids` and asks Go to write them now, for tabs
 * that are closing. Never rejects: a failed write arrives as
 * collection:save-failed.
 */
export async function flushRequests(stores: Stores, ids: readonly RequestId[]): Promise<void> {
  const toFlush = ids.filter((id) => hasSomethingToFlush(stores, id));
  await Promise.all(
    toFlush.map(async (id) => {
      const q = queues(stores).get(id);
      if (q !== undefined) await drain(q);
      try {
        await api.flushRequest(id);
      } catch (err: unknown) {
        console.warn(`autosave: flushing ${id} failed`, toAppError(err));
      }
    }),
  );
}

/**
 * Waits until the edits of `id` queued so far have reached Go (not the
 * disk): Go's view of the request, which sending reads, is then what the
 * editor shows. Never rejects.
 */
export async function waitForQueuedEdits(stores: Stores, id: RequestId): Promise<void> {
  const q = queues(stores).get(id);
  if (q !== undefined) await drain(q);
}

/** Sends every queued edit and asks Go to write everything pending now. Never rejects. */
export async function flushAllRequests(stores: Stores): Promise<void> {
  await Promise.all([...queues(stores).values()].map(drain));
  try {
    await api.flushAllRequests();
  } catch (err: unknown) {
    console.warn("autosave: flushing all edits failed", toAppError(err));
  }
}

/**
 * Applies Go's save events, flushes the requests of tabs that close
 * (onTabClosed) and flushes everything when the page is hidden or
 * unloaded. Call once near the root. Returns the unsubscribe function.
 */
export function followAutoSave(stores: Stores): () => void {
  const stopSaved = api.onRequestSaved((ev) => applySaved(stores, ev));
  const stopFailed = api.onRequestSaveFailed((ev) => applySaveFailed(stores, ev));
  // Runs before the tabs close and their drafts are dropped; the queues
  // keep their own copy of what is still to send. Folder tabs flush their
  // settings the same way; one never edited costs no call.
  const stopTabClosed = onTabClosed((ids, closing) => {
    if (closing === stores) void flushRequests(stores, ids);
  });
  const flush = () => {
    void flushAllRequests(stores);
  };
  // Before another project opens, every queued edit goes to Go while Go
  // still has this one open, and is written (STO-06).
  const stopLeaving = onWorkspaceLeaving((leaving) => (leaving === stores ? flushAllRequests(stores) : undefined));
  const onVisibility = () => {
    if (document.visibilityState === "hidden") flush();
  };
  window.addEventListener("pagehide", flush);
  document.addEventListener("visibilitychange", onVisibility);
  return () => {
    stopSaved();
    stopFailed();
    stopTabClosed();
    stopLeaving();
    window.removeEventListener("pagehide", flush);
    document.removeEventListener("visibilitychange", onVisibility);
  };
}
