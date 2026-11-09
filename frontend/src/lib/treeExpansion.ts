// Where the collection tree remembers which folders are expanded, per
// workspace. Framework-free: no React, no stores.
// commands/collection.ts is the only caller.
//
// Expanded folders are local, per-machine data: a WorkspaceLocalStore
// (./workspaceLocal.ts) under the key "tree.expanded" of the workspace's
// local UI state (STO-06); what older builds kept in localStorage is still
// read. The backend can be swapped behind ExpansionPersistence.

import {
  decodeStringArray,
  localStorageWorkspaceStore,
  workspaceLocalStore,
  type WorkspaceLocalStore,
} from "./workspaceLocal";

export interface ExpansionPersistence extends WorkspaceLocalStore<readonly string[]> {
  /** The expanded folder ids saved for a workspace, or null when none were ever saved. Never throws. */
  load(workspaceId: string): string[] | null;
}

export const TREE_EXPANSION_KEY_PREFIX = "postsonde.tree.expanded.";

/** At most this many ids are kept per workspace, so a huge tree cannot fill the storage quota. */
export const MAX_SAVED_EXPANDED_IDS = 5000;

export function localStorageExpansion(storage: () => Storage = () => window.localStorage): ExpansionPersistence {
  const store = localStorageWorkspaceStore<readonly string[]>(
    TREE_EXPANSION_KEY_PREFIX,
    decodeStringArray,
    (ids) => ids.slice(0, MAX_SAVED_EXPANDED_IDS),
    storage,
  );
  return {
    load: (workspaceId) => store.load(workspaceId) as string[] | null,
    save: (workspaceId, ids) => store.save(workspaceId, ids),
  };
}

/** The key of the expanded folders in the workspace's local UI state. */
export const TREE_EXPANSION_UI_STATE_KEY = "tree.expanded";

/** Expanded folders in the workspace's local directory (STO-06), falling back to localStorage. */
export function workspaceLocalExpansion(): ExpansionPersistence {
  const store = workspaceLocalStore<readonly string[]>(
    TREE_EXPANSION_UI_STATE_KEY,
    decodeStringArray,
    (ids) => ids.slice(0, MAX_SAVED_EXPANDED_IDS),
    localStorageExpansion(),
  );
  return {
    load: (workspaceId) => store.load(workspaceId) as string[] | null,
    save: (workspaceId, ids) => store.save(workspaceId, ids),
  };
}
