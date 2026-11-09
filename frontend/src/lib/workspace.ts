// Calls into the Go WorkspaceService (internal/app/services/workspace):
// opening and creating projects, the recent list and the native folder
// dialog (STO-01, STO-04). See docs/workspace.md.
import { Events } from "@wailsio/runtime";
import {
  WorkspaceService,
  type BodyFileDTO,
  type CurrentDTO,
  type RecentWorkspaceDTO,
  type WorkspaceDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/workspace";
import type { CancellablePromise } from "./calls";

export type { BodyFileDTO, CurrentDTO, RecentWorkspaceDTO, WorkspaceDTO };

/** Sent by Go whenever the open workspace changes (opened, created, closed, reopened at startup). */
export const WORKSPACE_CHANGED_EVENT = "workspace:changed";

/** Error codes the UI reacts to (internal/core/workspace). */
export const WORKSPACE_CODES = {
  /** Open: the folder has no postsonde.json. `details.path` is the folder. */
  notAProject: "workspace.not_a_project",
  /** Create: the folder already has a postsonde.json. `details.path` is the folder. */
  alreadyAProject: "workspace.already_a_project",
  /** Open, Create, Close: other windows show the open project, so it cannot change (APP-06). */
  sharedByWindows: "workspace.shared_by_windows",
} as const;

/** What the folder dialog is for; it sets the dialog's wording. */
export type FolderPurpose = "open" | "create";

/** Returns the open workspace; `workspace` is null when none is. */
export function getCurrentWorkspace(): CancellablePromise<CurrentDTO> {
  return WorkspaceService.Current();
}

/** Opens the project in the folder at `path` (absolute). */
export function openWorkspace(path: string): CancellablePromise<WorkspaceDTO> {
  return WorkspaceService.Open(path);
}

/** Makes the folder at `path` a new project (writing only postsonde.json) and opens it. */
export function createWorkspace(path: string, name: string): CancellablePromise<WorkspaceDTO> {
  return WorkspaceService.Create({ path, name });
}

/** Closes the open workspace, so it is not reopened on the next start. */
export function closeWorkspace(): CancellablePromise<void> {
  return WorkspaceService.Close();
}

/** Recently opened workspaces, most recent first. */
export async function getRecentWorkspaces(): Promise<RecentWorkspaceDTO[]> {
  return (await WorkspaceService.Recent()) ?? [];
}

/** Removes `path` from the recent list; the folder is not touched. */
export function removeRecentWorkspace(path: string): CancellablePromise<void> {
  return WorkspaceService.RemoveRecent(path);
}

/** Empties the recent list except for the open project; folders are not touched. */
export function clearRecentWorkspaces(): CancellablePromise<void> {
  return WorkspaceService.ClearRecent();
}

/** Shows a project folder (the open one or a recent one) in the file manager. */
export function revealWorkspaceFolder(path: string): CancellablePromise<void> {
  return WorkspaceService.Reveal(path);
}

/**
 * Saves one value of the view state of a workspace (STO-06) in its local
 * directory: `value` is JSON text, "null" removes the key. The workspace is
 * named by ID and folder; it may be one that was just left.
 */
export function saveWorkspaceUIState(workspaceId: string, path: string, key: string, value: string): CancellablePromise<void> {
  return WorkspaceService.SaveUIState({ workspaceId, path, key, value });
}

/** Shows the native folder dialog. Resolves to the folder, or "" when cancelled. */
export function pickFolder(purpose: FolderPurpose): CancellablePromise<string> {
  return WorkspaceService.PickFolder(purpose);
}

/**
 * Shows the native file dialog for a body or multipart file (HTTP-14,
 * HTTP-15). Resolves to the chosen file, with the path to store (relative
 * to the open project when inside it); `path` is "" when cancelled.
 */
export function pickBodyFile(): CancellablePromise<BodyFileDTO> {
  return WorkspaceService.PickBodyFile();
}

/**
 * Shows the native file dialog for the JSON Schema file of a matches_schema
 * assertion (TEST-07). Resolves to the chosen file, its path stored like a
 * body file's; `path` is "" when cancelled.
 */
export function pickSchemaFile(): CancellablePromise<BodyFileDTO> {
  return WorkspaceService.PickSchemaFile();
}

/** Describes the body file stored as `path`: name, size and whether it exists. */
export function getBodyFile(path: string): CancellablePromise<BodyFileDTO> {
  return WorkspaceService.BodyFile(path);
}

/** Calls `callback` with every workspace:changed event. Returns the unsubscribe function. */
export function onWorkspaceChanged(callback: (event: CurrentDTO) => void): () => void {
  return Events.On(WORKSPACE_CHANGED_EVENT, (ev) => callback(ev.data));
}

/** The last element of a path, for either separator: "/a/shop-api" → "shop-api". */
export function folderName(path: string): string {
  const parts = path.split(/[\\/]+/).filter((p) => p !== "");
  return parts.length > 0 ? parts[parts.length - 1] : path;
}
