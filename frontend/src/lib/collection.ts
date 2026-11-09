// Calls into the Go CollectionService (internal/app/services/collection):
// the request tree of the open project, folders as directories and one
// JSON file per request (STO-02), the tree operations with the local
// trash (COL-04) and auto-saved request edits (STO-03). See
// docs/collection.md. The tree UI maps TreeDTO into the collection
// store.
import { Events } from "@wailsio/runtime";
import {
  CollectionService,
  type AuthDTO,
  type BodyDTO,
  type BodyExampleDTO,
  type ChangedEventDTO,
  type CurlDiagnosticDTO,
  type CurlImportDTO,
  type DeleteResultDTO,
  type DetectedImportDTO,
  type EditedEventDTO,
  type ExternalChangeDTO,
  type FilesDroppedDTO,
  type FolderDTO,
  type ImportCountsDTO,
  type ImportDiagnosticDTO,
  type ImportFormatDTO,
  type ImportInputDTO,
  type ImportItemDTO,
  type ImportLocationDTO,
  type ImportReportDTO,
  type ImportResultDTO,
  type InheritedFolderDTO,
  type InheritedSettingsDTO,
  type KeyValueDTO,
  type NodeDTO,
  type NodeResultDTO,
  type PartDTO,
  type ProblemDTO,
  type RequestDTO,
  type RequestSettingsDTO,
  type RestoreResultDTO,
  type SavedEventDTO,
  type SavedMessageDTO,
  type SaveFailedEventDTO,
  type TrashEntryDTO,
  type TreeDTO,
  type UpdateResultDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection";
import type { CancellablePromise } from "./calls";

export type {
  AuthDTO,
  BodyDTO,
  BodyExampleDTO,
  ChangedEventDTO,
  CurlDiagnosticDTO,
  CurlImportDTO,
  DeleteResultDTO,
  DetectedImportDTO,
  EditedEventDTO,
  ExternalChangeDTO,
  FilesDroppedDTO,
  FolderDTO,
  ImportCountsDTO,
  ImportDiagnosticDTO,
  ImportFormatDTO,
  ImportInputDTO,
  ImportItemDTO,
  ImportLocationDTO,
  ImportReportDTO,
  ImportResultDTO,
  InheritedFolderDTO,
  InheritedSettingsDTO,
  KeyValueDTO,
  NodeDTO,
  NodeResultDTO,
  PartDTO,
  ProblemDTO,
  RequestDTO,
  RequestSettingsDTO,
  RestoreResultDTO,
  SavedEventDTO,
  SavedMessageDTO,
  SaveFailedEventDTO,
  TrashEntryDTO,
  TreeDTO,
  UpdateResultDTO,
};

/** Sent by Go after every tree operation and after the trash is purged. */
export const COLLECTION_CHANGED_EVENT = "collection:changed";

/** Index that puts an item last. */
export const END = -1;

/** Kinds of request CreateRequest can create. */
export type NewRequestKind = "http" | "graphql" | "tcp" | "udp" | "websocket" | "grpc" | "sse" | "socketio";

/** Error codes the UI reacts to (internal/core/collection). */
export const COLLECTION_CODES = {
  /** The item is not, or no longer, in the project. */
  notFound: "collection.not_found",
  /** The item's file is read-only; `details.reason` says why. */
  readOnly: "collection.read_only",
  invalidTarget: "collection.invalid_target",
  trashNotFound: "collection.trash_not_found",
  /** The file changed on disk since it was read; see collection:external-change (STO-05). */
  changedOnDisk: "collection.changed_on_disk",
  /**
   * The edit was made from an older version than Go holds: another window
   * changed the item since (APP-06); see collection:edited.
   */
  editedElsewhere: "collection.edited_elsewhere",
} as const;

/**
 * The request tree of the open project, loaded from disk the first time
 * after a project is opened. Call again on workspace:changed. Rejects with
 * code `workspace.none_open` when no project is open.
 */
export function getCollectionTree(): CancellablePromise<TreeDTO> {
  return CollectionService.Tree();
}

/** Reads the request tree from disk again, for changes made outside PostSonde. */
export function reloadCollectionTree(): CancellablePromise<TreeDTO> {
  return CollectionService.Reload();
}

// Tree operations. Each resolves to the whole tree afterwards. After a
// rejection, call getCollectionTree: Go reads the files again.

/** Creates a folder in `parentId` ("" for the top level) at `index` (END: last). */
export function createFolder(parentId: string, name: string, index: number = END): CancellablePromise<NodeResultDTO> {
  return CollectionService.CreateFolder({ parentId, name, index });
}

/**
 * Reads a curl command (IMP-01, docs/curl.md): the request it maps to and
 * the parser's diagnostics. Needs no open project and writes nothing.
 */
export function parseCurl(text: string): CancellablePromise<CurlImportDTO> {
  return CollectionService.ParseCurl(text);
}

/**
 * Creates an http request from a curl command in `parentId` ("" for the top
 * level) at `index`, called `name` (blank: the suggested name). Rejects with
 * code `curl.invalid_command` for a command with an error.
 */
export function importCurl(parentId: string, name: string, text: string, index: number = END): CancellablePromise<NodeResultDTO> {
  return CollectionService.ImportCurl({ parentId, name, text, index });
}

/** Error codes of imports (IMP-10, docs/import.md) the UI reacts to. */
export const IMPORT_CODES = {
  /** The file changed after its preview: preview it again. */
  sourceChanged: "import.source_changed",
  unknownFormat: "import.unknown_format",
  // More files dropped at once than DetectImport looks at.
  tooManyFiles: "import.too_many_files",
} as const;

/** The formats PostSonde imports (docs/import.md). */
export async function importFormats(): Promise<ImportFormatDTO[]> {
  return (await CollectionService.ImportFormats()) ?? [];
}

/** Shows the native dialog to choose a file to import; "" when cancelled. */
export function pickImportFile(): CancellablePromise<string> {
  return CollectionService.PickImportFile();
}

/**
 * Reads the file at `path` in `format` ("": detected) and returns what an
 * import would create, with every diagnostic. Writes nothing.
 */
export function previewImport(path: string, format = ""): CancellablePromise<ImportReportDTO> {
  return CollectionService.PreviewImport({ path, format });
}

/**
 * Which format each file would be imported in (IMP-11, files dropped on
 * the window), in the order given: the same detection as previewImport.
 * A file that cannot be imported has `error` set. Rejects with
 * `import.too_many_files` for more files than Go looks at in one go.
 */
export async function detectImport(paths: readonly string[]): Promise<DetectedImportDTO[]> {
  return (await CollectionService.DetectImport({ paths: [...paths] })) ?? [];
}

/** Sent by Go when files were dropped on a file drop target of the window (IMP-11). */
export const COLLECTION_FILES_DROPPED_EVENT = "collection:files-dropped";

/** Calls `callback` with every collection:files-dropped event. Returns the unsubscribe function. */
export function onFilesDropped(callback: (event: FilesDroppedDTO) => void): () => void {
  return Events.On(COLLECTION_FILES_DROPPED_EVENT, (ev) => callback(ev.data));
}

/**
 * Imports the previewed file into the open project (a new folder in
 * `parentId`, or its items straight into it) and returns the tree and the
 * report. Rejects with `import.source_changed` when the file is no longer
 * the previewed one (`digest`).
 */
export function runImport(input: ImportInputDTO): CancellablePromise<ImportResultDTO> {
  return CollectionService.Import(input);
}

/** Creates an empty request in `parentId` ("" for the top level) at `index` (END: last). */
export function createRequest(
  parentId: string,
  name: string,
  kind: NewRequestKind,
  index: number = END,
): CancellablePromise<NodeResultDTO> {
  return CollectionService.CreateRequest({ parentId, name, kind, index });
}

/** Renames a folder or request, and its file. */
export function renameItem(id: string, name: string): CancellablePromise<NodeResultDTO> {
  return CollectionService.Rename({ id, name });
}

/** Copies a folder or request right after it; `nodeId` is the copy. */
export function duplicateItem(id: string): CancellablePromise<NodeResultDTO> {
  return CollectionService.Duplicate(id);
}

/**
 * Moves an item into `parentId` ("" for the top level) at `index`, its
 * position there not counting itself (END: last).
 */
export function moveItem(id: string, parentId: string, index: number = END): CancellablePromise<NodeResultDTO> {
  return CollectionService.Move({ id, parentId, index });
}

/** Moves an item into the project's local trash; `entry.id` undoes it with restoreItem. */
export function deleteItem(id: string): CancellablePromise<DeleteResultDTO> {
  return CollectionService.Delete(id);
}

/** Restores a deleted item into its folder, or to the top level when that folder is gone (`toRoot`). */
export function restoreItem(trashId: string): CancellablePromise<RestoreResultDTO> {
  return CollectionService.Restore(trashId);
}

/** The deleted items of the open project, most recently deleted first. */
export async function listTrash(): Promise<TrashEntryDTO[]> {
  return (await CollectionService.ListTrash()) ?? [];
}

/** Permanently removes trash entries deleted more than `olderThanMs` ago (0: all). Resolves to the count. */
export function purgeTrash(olderThanMs = 0): CancellablePromise<number> {
  return CollectionService.PurgeTrash(olderThanMs);
}

// Scratch requests (COL-06): kept in the workspace's local side files, not
// in the project. They come with every tree (`TreeDTO.scratch`) and are
// edited, auto-saved and sent by id like any request.

/** Creates an empty scratch request; `name` "" gives "Scratch N". `nodeId` is the new request. */
export function createScratch(kind: NewRequestKind, name = ""): CancellablePromise<NodeResultDTO> {
  return CollectionService.CreateScratch({ kind, name });
}

/** Deletes a scratch request for good. Resolves to the tree. */
export function deleteScratch(id: string): CancellablePromise<TreeDTO> {
  return CollectionService.DeleteScratch(id);
}

/**
 * Saves a scratch request into the folder `parentId` ("" for the top
 * level) as a normal request called `name` ("" keeps its name). It keeps
 * its id (`nodeId`).
 */
export function saveScratch(id: string, parentId: string, name: string, index: number = END): CancellablePromise<NodeResultDTO> {
  return CollectionService.SaveScratch({ id, parentId, name, index });
}

/** Calls `callback` with every collection:changed event. Returns the unsubscribe function. */
export function onCollectionChanged(callback: (event: ChangedEventDTO) => void): () => void {
  return Events.On(COLLECTION_CHANGED_EVENT, (ev) => callback(ev.data));
}

// Auto-save: the request editor sends each edit with updateRequest; Go
// writes it once edits pause and reports the result as collection:saved or
// collection:save-failed. See docs/collection.md#auto-save.

/** Sent by Go after a pending edit reached the disk. */
export const COLLECTION_SAVED_EVENT = "collection:saved";
/** Sent by Go when writing a pending edit failed; the edit stays pending. */
export const COLLECTION_SAVE_FAILED_EVENT = "collection:save-failed";

/** The content of a request, including edits that are not written yet. */
export function getRequest(id: string): CancellablePromise<RequestDTO> {
  return CollectionService.Request(id);
}

/**
 * Replaces a request's content; Go writes it after the save delay. Rejects
 * at once for a read-only or missing request or invalid content, and with
 * COLLECTION_CODES.editedElsewhere when `baseVersion`, the version the
 * edit was made from (RequestDTO.version or the last result), is not the
 * one Go holds (APP-06). Resolves to the new version. Without
 * `baseVersion` nothing is checked.
 */
export function updateRequest(request: RequestDTO, revision: number, baseVersion?: number): CancellablePromise<UpdateResultDTO> {
  return CollectionService.UpdateRequest({ request, revision, baseVersion });
}

/** The settings of a folder (COL-03), including edits that are not written yet. */
export function getFolder(id: string): CancellablePromise<FolderDTO> {
  return CollectionService.Folder(id);
}

/**
 * Replaces a folder's auth, headers and variables; Go writes them after the
 * save delay, like a request edit (same events and versions). Rejects at
 * once for a read-only, unreadable or missing folder or invalid content.
 */
export function updateFolder(folder: FolderDTO, revision: number, baseVersion?: number): CancellablePromise<UpdateResultDTO> {
  return CollectionService.UpdateFolder({ folder, revision, baseVersion });
}

/** What request or folder `id` inherits from the folders above it, the top-level folder first. */
export function getInheritedSettings(id: string): CancellablePromise<InheritedSettingsDTO> {
  return CollectionService.InheritedSettings(id);
}

/** Writes the pending edits of one request (or folder) now. Rejects with the write's error. */
export function flushRequest(id: string): CancellablePromise<void> {
  return CollectionService.Flush(id);
}

/** Writes every pending edit now. */
export function flushAllRequests(): CancellablePromise<void> {
  return CollectionService.FlushAll();
}

/** Calls `callback` with every collection:saved event. Returns the unsubscribe function. */
export function onRequestSaved(callback: (event: SavedEventDTO) => void): () => void {
  return Events.On(COLLECTION_SAVED_EVENT, (ev) => callback(ev.data));
}

/** Calls `callback` with every collection:save-failed event. Returns the unsubscribe function. */
export function onRequestSaveFailed(callback: (event: SaveFailedEventDTO) => void): () => void {
  return Events.On(COLLECTION_SAVE_FAILED_EVENT, (ev) => callback(ev.data));
}

// External changes (STO-05): Go polls the open project's files and reports
// edits made outside PostSonde. See docs/collection.md#external-changes.

/** Sent by Go when the open project's files changed outside PostSonde. */
export const COLLECTION_EXTERNAL_CHANGE_EVENT = "collection:external-change";

/** Calls `callback` with every collection:external-change event. Returns the unsubscribe function. */
export function onExternalChange(callback: (event: ExternalChangeDTO) => void): () => void {
  return Events.On(COLLECTION_EXTERNAL_CHANGE_EVENT, (ev) => callback(ev.data));
}

// Several windows (APP-06): every edit Go takes is announced to the other
// windows. See docs/windows.md#edits.

/** Sent by Go after it took an edit of a request or folder; `window` is the window it came from. */
export const COLLECTION_EDITED_EVENT = "collection:edited";

/** Calls `callback` with every collection:edited event. Returns the unsubscribe function. */
export function onEdited(callback: (event: EditedEventDTO) => void): () => void {
  return Events.On(COLLECTION_EDITED_EVENT, (ev) => callback(ev.data));
}

/** This window was hidden (false) or shown (true): Go polls while any window is shown. */
export function setWatching(active: boolean): CancellablePromise<void> {
  return CollectionService.SetWatching(active);
}

/** Ends the conflict of `id` in Go, so its edits are accepted again. */
export function resolveConflict(id: string): CancellablePromise<void> {
  return CollectionService.ResolveConflict(id);
}

/** Writes a request whose file was removed outside PostSonde back into folder `parentId` ("" or gone: the top level). */
export function recreateRequest(request: RequestDTO, parentId: string, index: number = END): CancellablePromise<NodeResultDTO> {
  return CollectionService.RecreateRequest({ parentId, index, request });
}
