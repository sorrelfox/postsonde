// Where the collection tree remembers which folders are shown sorted by
// name (COL-05), per workspace. Framework-free: no React, no stores.
// commands/collection.ts is the only caller.
//
// Sorting is a view preference, not part of the project: the manual order
// stays in each folder's `order` (docs/collection.md) and is what the tree
// shows again when a folder goes back to Manual order. So the choice is
// local data, a WorkspaceLocalStore (./workspaceLocal.ts) under the key
// "tree.sort" of the workspace's local UI state (STO-06):
//
//   { "topLevel": "name", "folders": { "<folder id>": "name" } }
//
// Only folders sorted by name are listed; everything else is in manual
// order. The backend can be swapped behind SortPersistence.

import { workspaceLocalStore, type WorkspaceLocalStore } from "./workspaceLocal";

/** The folder ids sorted by name; the empty string is the top level (TOP_LEVEL in the collection store). */
export type SortPersistence = WorkspaceLocalStore<readonly string[]>;

/** The key of the sort choices in the workspace's local UI state. */
export const TREE_SORT_UI_STATE_KEY = "tree.sort";

/** At most this many folders are remembered per workspace. */
export const MAX_SAVED_SORTED_FOLDERS = 5000;

interface SavedSort {
  topLevel?: "name";
  folders?: Record<string, "name">;
}

/** Reads the saved JSON (untrusted: it may be hand edited or from another build). */
export function decodeTreeSort(raw: unknown): string[] | null {
  if (raw === null || typeof raw !== "object" || Array.isArray(raw)) return null;
  const value = raw as Record<string, unknown>;
  const ids: string[] = [];
  if (value.topLevel === "name") ids.push("");
  const folders = value.folders;
  if (folders !== null && typeof folders === "object" && !Array.isArray(folders)) {
    for (const [id, mode] of Object.entries(folders as Record<string, unknown>)) {
      if (id !== "" && mode === "name") ids.push(id);
    }
  }
  return ids;
}

/** The JSON saved for the folder ids sorted by name. */
export function encodeTreeSort(ids: readonly string[]): SavedSort {
  const out: SavedSort = {};
  const folders: Record<string, "name"> = {};
  let count = 0;
  for (const id of ids) {
    if (id === "") {
      out.topLevel = "name";
    } else if (count < MAX_SAVED_SORTED_FOLDERS) {
      folders[id] = "name";
      count++;
    }
  }
  if (count > 0) out.folders = folders;
  return out;
}

/** Sort choices in the workspace's local directory (STO-06). */
export function workspaceLocalTreeSort(): SortPersistence {
  return workspaceLocalStore<readonly string[]>(TREE_SORT_UI_STATE_KEY, decodeTreeSort, encodeTreeSort);
}
