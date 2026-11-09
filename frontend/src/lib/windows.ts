// Calls into the Go WindowService (internal/app/services/window): the
// calling window's ID, new windows, and the window:open-project event
// (APP-06). Every window shows the one open project; see docs/windows.md.
import { Events } from "@wailsio/runtime";
import {
  WindowService,
  type OpenProjectDTO,
  type WindowDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/window";
import type { CancellablePromise } from "./calls";

export type { OpenProjectDTO, WindowDTO };

/** Sent by Go when PostSonde was started again with another project: the window it names opens it. */
export const WINDOW_OPEN_PROJECT_EVENT = "window:open-project";

/** Error codes the UI reacts to (internal/app/services/window). */
export const WINDOW_CODES = {
  /** OpenInNewWindow: another project is open, and every window shows the same one. `details.path` is the folder. */
  projectDiffers: "window.project_differs",
} as const;

/** This window's ID and, the first time only, the project folder it was opened for (""). */
export function getCurrentWindow(): CancellablePromise<WindowDTO> {
  return WindowService.Current();
}

/** Opens another window on the open project (or the welcome screen when none is open). */
export function openNewWindow(): CancellablePromise<void> {
  return WindowService.NewWindow();
}

/**
 * Opens a new window on the project at `path`: the open one, or any while
 * none is open. Rejects with WINDOW_CODES.projectDiffers otherwise.
 */
export function openProjectInNewWindow(path: string): CancellablePromise<void> {
  return WindowService.OpenInNewWindow(path);
}

/** Calls `callback` with every window:open-project event. Returns the unsubscribe function. */
export function onOpenProject(callback: (event: OpenProjectDTO) => void): () => void {
  return Events.On(WINDOW_OPEN_PROJECT_EVENT, (ev) => callback(ev.data));
}
