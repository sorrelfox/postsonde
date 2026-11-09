// The run history and run reports (RUN-08, RUN-09): the past runs of a
// folder, kept by Go in the project's local directory, reopened in its
// Run sub-tab, removed, and saved as JUnit XML, JSON or HTML reports.
// See docs/runner.md#reports-and-run-history.
import { useCallback, useEffect, useState } from "react";
import { toAppError, type AppError } from "../lib/errors";
import {
  deletePastRun,
  exportRunReport,
  listPastRuns,
  loadPastRun,
  type ExportRunResultDTO,
  type RunReportFormat,
  type RunSummaryDTO,
} from "../lib/runner";
import { pastFolderRunState, type FolderRunState } from "./folderRuns";

/** A past run reopened: its summary and its state for the panel. */
export interface PastRun {
  readonly summary: RunSummaryDTO;
  readonly state: FolderRunState;
  /** Results kept in the history (the list shows the last of them). */
  readonly kept: number;
}

/** The past runs of folder `folderId` ("" for the whole project), newest first. */
export async function folderRunHistory(folderId: string): Promise<RunSummaryDTO[]> {
  const all = (await listPastRuns()) ?? [];
  return all.filter((r) => r.folderId === folderId);
}

/** Past run `id`, to show in the panel. Rejects with `runner.run_not_found` when it is gone. */
export async function openPastRun(id: string): Promise<PastRun> {
  const record = await loadPastRun(id);
  return { summary: record.summary, state: pastFolderRunState(record), kept: (record.results ?? []).length };
}

/** Removes past run `id`. */
export async function removePastRun(id: string): Promise<void> {
  await deletePastRun(id);
}

/** Saves run `id` as a report in `format` where the user picks; `saved` is false when cancelled. */
export async function saveRunReport(id: string, format: RunReportFormat): Promise<ExportRunResultDTO> {
  return await exportRunReport(id, format);
}

export interface RunHistoryState {
  /** The folder's past runs, newest first; null until loaded. */
  readonly runs: readonly RunSummaryDTO[] | null;
  /** Why they could not be loaded; null otherwise. */
  readonly error: AppError | null;
  /** Loads them again. */
  readonly reload: () => void;
}

/**
 * The past runs of folder `folderId`, loaded when it changes and whenever
 * `reloadKey` does (a run ended). Only the latest load lands.
 */
export function useRunHistory(folderId: string, reloadKey: string): RunHistoryState {
  const [runs, setRuns] = useState<readonly RunSummaryDTO[] | null>(null);
  const [error, setError] = useState<AppError | null>(null);
  const [loads, setLoads] = useState(0);
  const reload = useCallback(() => setLoads((n) => n + 1), []);
  useEffect(() => {
    let live = true;
    folderRunHistory(folderId).then(
      (list) => {
        if (!live) return;
        setRuns(list);
        setError(null);
      },
      (err: unknown) => {
        if (live) setError(toAppError(err));
      },
    );
    return () => {
      live = false;
    };
  }, [folderId, reloadKey, loads]);
  return { runs, error, reload };
}
