// The hook point for work that must finish before the open project is
// left (STO-06): switching to another project, creating one or closing it.
// Auto-save sends its queued edits, and the tabs and the tree save their
// view state, while Go still has the old project open, so nothing is sent
// to the new one by mistake. commands/workspace.ts runs the listeners; the
// followers register here. A module of its own so the followers do not
// import commands/workspace.ts (which imports commands/tabs.ts).

import { workspaceLocalIdle } from "../lib/workspaceLocal";
import type { Stores } from "../stores";

/** Called before the open project is left; may return a promise, which is awaited. */
export type WorkspaceLeavingListener = (stores: Stores) => void | Promise<void>;

const listeners = new Set<WorkspaceLeavingListener>();

/** Registers a listener. Returns the unsubscribe function. */
export function onWorkspaceLeaving(listener: WorkspaceLeavingListener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/**
 * Runs every listener and waits for them, then for the view-state saves
 * they made. Never rejects: a failing listener is logged and the switch
 * goes on.
 */
export async function leaveWorkspace(stores: Stores): Promise<void> {
  await Promise.all(
    [...listeners].map(async (listener) => {
      try {
        await listener(stores);
      } catch (err: unknown) {
        console.warn("workspace: a leaving listener failed", err);
      }
    }),
  );
  await workspaceLocalIdle();
}
