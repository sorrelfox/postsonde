// Calls into the Go DataDirService (internal/app/services/datadir): where
// the data directory is, choosing another one (SET-03), portable mode
// (APP-04) and restarting to switch. See docs/settings.md (Data directory).
import {
  DataDirService,
  type CandidateDTO,
  type InfoDTO,
  type PendingDTO,
  type PreviousDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/datadir";
import { toAppError } from "./errors";

export type { CandidateDTO as DataDirCandidate, InfoDTO as DataDirInfo, PendingDTO, PreviousDTO };

/** What a scheduled change does with the data. */
export type DataDirAction = "move" | "fresh" | "use";

/** Runs a binding call and rejects with an AppError. */
async function call<T>(run: () => PromiseLike<T>): Promise<T> {
  try {
    return await run();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/** Where the data directory is, how it was chosen, the change waiting for a restart and the problems found at start. */
export function getDataDirInfo(): Promise<InfoDTO> {
  return call(() => DataDirService.Info());
}

/**
 * Shows the native folder dialog and checks the folder. `path` is "" when
 * the dialog was cancelled; a folder that cannot be used rejects with the
 * reason (e.g. `datadir.not_empty`).
 */
export function pickDataDir(): Promise<CandidateDTO> {
  return call(() => DataDirService.Pick());
}

/** Schedules the change for the next start. */
export function scheduleDataDir(path: string, action: DataDirAction): Promise<InfoDTO> {
  return call(() => DataDirService.Schedule({ path, action }));
}

/** Drops the scheduled change. */
export function cancelDataDirChange(): Promise<InfoDTO> {
  return call(() => DataDirService.Cancel());
}

/** Deletes the copy of the data a move left in the old folder. */
export function deletePreviousDataDir(): Promise<InfoDTO> {
  return call(() => DataDirService.DeletePrevious());
}

/** Keeps the old folder and stops mentioning it. */
export function forgetPreviousDataDir(): Promise<InfoDTO> {
  return call(() => DataDirService.ForgetPrevious());
}

/** Shows the data directory ("data") or the old folder of a move ("previous") in the file manager. */
export function revealDataDir(which: "data" | "previous"): Promise<void> {
  return call(() => DataDirService.Reveal(which));
}

/** Quits PostSonde and starts it again. */
export function restartApp(): Promise<void> {
  return call(() => DataDirService.Restart());
}
