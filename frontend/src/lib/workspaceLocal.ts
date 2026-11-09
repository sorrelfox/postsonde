// Small per-workspace UI state kept on this machine only: which folders
// of the tree are expanded, which tabs are open (NAV-01, STO-06).
// Framework-free: no React, no stores.
//
// This is local data that must stay out of the project (docs/file-format.md),
// and the settings file is global, so it lives in the workspace's local
// directory, in ui-state.json (docs/workspace.md). Go sends the saved values
// with the workspace when it opens (WorkspaceDTO.uiState);
// commands/workspace.ts hands them to seedWorkspaceLocal, so loading stays
// synchronous, and every save goes back to Go (WorkspaceService.SaveUIState).
// Older builds kept the same values in localStorage, one key per workspace
// id: they are still read while the local directory has none. Callers
// depend on WorkspaceLocalStore only.

import { saveWorkspaceUIState } from "./workspace";

export interface WorkspaceLocalStore<T> {
  /** The value saved for a workspace, or null when none was saved or it cannot be read. Never throws. */
  load(workspaceId: string): T | null;
  /** Replaces the value saved for a workspace. Never throws. */
  save(workspaceId: string, value: T): void;
}

/**
 * A WorkspaceLocalStore over localStorage under `keyPrefix + workspaceId`,
 * as JSON. `decode` checks what was read back (untrusted: it may be hand
 * edited or from another build) and returns null to ignore it; `encode`
 * may trim the value before it is written.
 */
export function localStorageWorkspaceStore<T>(
  keyPrefix: string,
  decode: (raw: unknown) => T | null,
  encode: (value: T) => unknown = (value) => value,
  storage: () => Storage = () => window.localStorage,
): WorkspaceLocalStore<T> {
  return {
    load(workspaceId) {
      try {
        const raw = storage().getItem(keyPrefix + workspaceId);
        if (raw === null) return null;
        return decode(JSON.parse(raw));
      } catch {
        return null;
      }
    },
    save(workspaceId, value) {
      try {
        storage().setItem(keyPrefix + workspaceId, JSON.stringify(encode(value)));
      } catch {
        // Storage unavailable or full: the value is not remembered; the app goes on.
      }
    },
  };
}

/** The strings of `raw` if it is an array, else null. */
export function decodeStringArray(raw: unknown): string[] | null {
  if (!Array.isArray(raw)) return null;
  return raw.filter((item): item is string => typeof item === "string");
}

/** The view state of a workspace: its folder and the JSON text of each key. */
interface Seeded {
  readonly path: string;
  readonly values: Map<string, string>;
}

/** By workspace id: the workspaces opened in this window (a save may still come for one just left). */
const seeded = new Map<string, Seeded>();

/** Saves run one after another, in the order they were made. */
let queue: Promise<void> = Promise.resolve();

type Writer = (workspaceId: string, path: string, key: string, value: string) => Promise<void>;

let writer: Writer = (workspaceId, path, key, value) => saveWorkspaceUIState(workspaceId, path, key, value);

/** Replaces where saves go (tests). Returns the previous writer. */
export function setWorkspaceLocalWriter(next: Writer): Writer {
  const previous = writer;
  writer = next;
  return previous;
}

/** What seedWorkspaceLocal needs of an opened workspace. */
export interface SeedWorkspace {
  readonly id: string;
  readonly path: string;
  readonly uiState?: Readonly<Record<string, string | undefined>> | null;
}

/**
 * Takes the view state Go sent with an opened workspace. A workspace that
 * is already known (same id and folder) keeps what this window holds,
 * which includes its latest saves; one with the same id in another folder
 * (a copy) replaces it.
 */
export function seedWorkspaceLocal(workspace: SeedWorkspace): void {
  const known = seeded.get(workspace.id);
  if (known !== undefined && known.path === workspace.path) return;
  const values = new Map<string, string>();
  for (const [key, value] of Object.entries(workspace.uiState ?? {})) {
    if (typeof value === "string") values.set(key, value);
  }
  seeded.set(workspace.id, { path: workspace.path, values });
}

/** Forgets every seeded workspace (tests). */
export function resetWorkspaceLocal(): void {
  seeded.clear();
}

/** Resolves when every save made so far has reached Go (or failed). */
export function workspaceLocalIdle(): Promise<void> {
  return queue;
}

/**
 * A WorkspaceLocalStore over the workspace's local directory, under `key`.
 * `decode` checks what was read back (it may be hand edited or from another
 * build) and returns null to ignore it; `encode` may trim the value before
 * it is saved. While the local directory has nothing for `key`, `legacy`
 * (the localStorage store of older builds) is read instead. Saves for a
 * workspace that was never seeded are dropped. Saving never throws; a
 * failed save is logged.
 */
export function workspaceLocalStore<T>(
  key: string,
  decode: (raw: unknown) => T | null,
  encode: (value: T) => unknown = (value) => value,
  legacy?: WorkspaceLocalStore<T>,
): WorkspaceLocalStore<T> {
  return {
    load(workspaceId) {
      const raw = seeded.get(workspaceId)?.values.get(key);
      if (raw === undefined) return legacy?.load(workspaceId) ?? null;
      try {
        return decode(JSON.parse(raw));
      } catch {
        return null;
      }
    },
    save(workspaceId, value) {
      const target = seeded.get(workspaceId);
      if (target === undefined) return;
      let text: string;
      try {
        text = JSON.stringify(encode(value));
      } catch {
        return;
      }
      if (target.values.get(key) === text) return;
      target.values.set(key, text);
      const { path } = target;
      queue = queue.then(async () => {
        try {
          await writer(workspaceId, path, key, text);
        } catch (err: unknown) {
          console.warn(`workspace: could not save the view state "${key}"`, err);
        }
      });
    },
  };
}
