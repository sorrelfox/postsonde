// Windows (APP-06): open another window, learn this window's ID, and open
// the project a window was started for. Every window shows the one open
// project and has its own tabs and drafts; Go holds what is shared. See
// docs/windows.md.
//
// followWindow learns this window's ID (commands/windowId.ts), which tells
// the events Go sends to every window apart.

import { toAppError, type AppError } from "../lib/errors";
import * as win from "../lib/windows";
import type { Stores } from "../stores";
import { ownWindow, setOwnWindow } from "./windowId";
import { openWorkspaceAt } from "./workspace";

export { fromOtherWindow, ownWindow, setOwnWindow } from "./windowId";

/** Opens another window on the open project. Rejects with an AppError. */
export async function newWindow(): Promise<void> {
  try {
    await win.openNewWindow();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Opens a new window on the project at `path`: the open project, or any
 * while none is open (every window then shows it). Rejects with an
 * AppError, WINDOW_CODES.projectDiffers for another project.
 */
export async function openInNewWindow(path: string): Promise<void> {
  try {
    await win.openProjectInNewWindow(path);
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Asks Go for this window's ID, opens the project the window was opened
 * for (the command line's, or a second start's), and opens the ones Go
 * asks this window to open later (window:open-project). A project that
 * does not open goes to `onError`. Call once the open workspace is loaded.
 * Returns the stop function.
 */
export function followWindow(stores: Stores, onError: (err: AppError) => void): () => void {
  let stopped = false;
  const open = async (path: string) => {
    try {
      await openWorkspaceAt(stores, path);
    } catch (err: unknown) {
      if (!stopped) onError(toAppError(err));
    }
  };
  const stopEvents = win.onOpenProject((event) => {
    const own = ownWindow(stores);
    if (own !== undefined && event.window === own) void open(event.path);
  });
  void (async () => {
    let current: win.WindowDTO;
    try {
      current = await win.getCurrentWindow();
    } catch (err: unknown) {
      console.warn("windows: could not ask for this window's ID", toAppError(err));
      return;
    }
    if (stopped) return;
    setOwnWindow(stores, current.id);
    if (current.openPath !== "") await open(current.openPath);
  })();
  return () => {
    stopped = true;
    stopEvents();
  };
}
