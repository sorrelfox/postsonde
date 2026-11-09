// Workspace commands (STO-06): load the open project and the recent
// list at startup, follow workspace:changed, open, create, switch or close
// a project through the Go WorkspaceService, and manage the recent list.
// See docs/workspace.md.
//
// Leaving a project (opening or creating another, closing it) first runs
// the onWorkspaceLeaving listeners (commands/workspaceLeave.ts): queued
// edits are sent and written and the view state (tabs, expanded folders)
// is saved while Go still has the project open. Then the new project's
// view state, which Go sends with it, is seeded (lib/workspaceLocal.ts)
// before the tree loads, so its tabs come back.
//
// Failed calls reject with an AppError (lib/errors.ts), so components can
// show the message and react to the code (WORKSPACE_CODES).

import { toAppError } from "../lib/errors";
import * as ws from "../lib/workspace";
import { seedWorkspaceLocal } from "../lib/workspaceLocal";
import type { RecentWorkspace, Stores, WorkspaceInfo } from "../stores";
import { closeTabs } from "./tabs";
import { leaveWorkspace } from "./workspaceLeave";

function toInfo(dto: ws.WorkspaceDTO | null): WorkspaceInfo | null {
  if (dto === null) return null;
  const { id, name, path, localDir, localStatus, previousPath, newerFormat } = dto;
  return { id, name, path, localDir, localStatus, previousPath, newerFormat };
}

function toRecent(dtos: readonly ws.RecentWorkspaceDTO[]): RecentWorkspace[] {
  return dtos.map(({ id, name, path, openedAt, available }) => ({ id, name, path, openedAt, available }));
}

/**
 * Makes `dto` the open workspace. When the project changes, the tree, the
 * tabs and the drafts of the previous one are dropped.
 */
function applyCurrent(stores: Stores, dto: ws.WorkspaceDTO | null): void {
  const before = stores.workspace.getState().current;
  const next = toInfo(dto);
  // Before the tree loads: the tabs and the expansion are restored from it.
  if (dto !== null) seedWorkspaceLocal(dto);
  if (before !== null && (next === null || next.path !== before.path || next.id !== before.id)) {
    // The tree first: with no tree loaded, closing the tabs is not saved as
    // the project's open tabs (followTabs), so they come back next time.
    stores.collection.dispatch({ type: "collection/clear" });
    closeTabs(stores, stores.tabs.getState().order);
  }
  stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: next });
}

async function run<T>(call: () => Promise<T>): Promise<T> {
  try {
    return await call();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/** Re-reads the recent list. Failures only log: the list is a convenience. */
export async function refreshRecentWorkspaces(stores: Stores): Promise<void> {
  try {
    stores.workspace.dispatch({ type: "workspace/setRecent", recent: toRecent(await ws.getRecentWorkspaces()) });
  } catch (err: unknown) {
    console.warn("workspace: could not load the recent projects", toAppError(err));
  }
}

/**
 * Loads the open workspace (Go reopens the last one at startup) and the
 * recent list. If Go cannot be asked, the welcome screen is shown.
 */
export async function loadWorkspace(stores: Stores): Promise<void> {
  const recent = refreshRecentWorkspaces(stores);
  try {
    applyCurrent(stores, (await ws.getCurrentWorkspace()).workspace);
  } catch (err: unknown) {
    console.warn("workspace: could not load the open project", toAppError(err));
    applyCurrent(stores, null);
  }
  await recent;
}

/** Applies workspace:changed events as they arrive. Returns the unsubscribe function. */
export function followWorkspaceChanges(stores: Stores): () => void {
  return ws.onWorkspaceChanged((ev) => {
    applyCurrent(stores, ev.workspace);
    void refreshRecentWorkspaces(stores);
  });
}

/**
 * Gets the open project ready to be left: queued edits written, tabs and
 * expanded folders saved. Does nothing when no project is open. Never
 * rejects.
 */
async function prepareToLeave(stores: Stores): Promise<void> {
  if (stores.workspace.getState().current !== null) await leaveWorkspace(stores);
}

/**
 * Opens the project at `path`, switching from the open one (its edits and
 * view state are saved first). Opening the open project again does
 * nothing more than refresh it. Rejects with an AppError.
 */
export async function openWorkspaceAt(stores: Stores, path: string): Promise<void> {
  if (stores.workspace.getState().current?.path !== path) await prepareToLeave(stores);
  const dto = await run(() => ws.openWorkspace(path));
  applyCurrent(stores, dto);
  await refreshRecentWorkspaces(stores);
}

/**
 * Asks for a folder with the native dialog and opens it. Resolves to false
 * when the user cancels the dialog. Rejects with an AppError.
 */
export async function chooseAndOpenWorkspace(stores: Stores): Promise<boolean> {
  const path = await run(() => ws.pickFolder("open"));
  if (path === "") return false;
  await openWorkspaceAt(stores, path);
  return true;
}

/** Asks for the folder of a new project. Resolves to "" when cancelled. Rejects with an AppError. */
export function chooseProjectFolder(): Promise<string> {
  return run(() => ws.pickFolder("create"));
}

/** Creates a project in the folder at `path` and opens it. Rejects with an AppError. */
export async function createWorkspaceAt(stores: Stores, path: string, name: string): Promise<void> {
  await prepareToLeave(stores);
  const dto = await run(() => ws.createWorkspace(path, name.trim()));
  applyCurrent(stores, dto);
  await refreshRecentWorkspaces(stores);
}

/** Closes the open project and returns to the welcome screen. Rejects with an AppError. */
export async function closeWorkspace(stores: Stores): Promise<void> {
  await prepareToLeave(stores);
  await run(() => ws.closeWorkspace());
  applyCurrent(stores, null);
  await refreshRecentWorkspaces(stores);
}

/** Removes a folder from the recent list. Rejects with an AppError. */
export async function forgetRecentWorkspace(stores: Stores, path: string): Promise<void> {
  await run(() => ws.removeRecentWorkspace(path));
  await refreshRecentWorkspaces(stores);
}

/** Empties the recent list except for the open project. Rejects with an AppError. */
export async function clearRecentWorkspaces(stores: Stores): Promise<void> {
  await run(() => ws.clearRecentWorkspaces());
  await refreshRecentWorkspaces(stores);
}

/** Shows a project folder (the open one or a recent one) in the file manager. Rejects with an AppError. */
export async function revealWorkspace(path: string): Promise<void> {
  await run(() => ws.revealWorkspaceFolder(path));
}
