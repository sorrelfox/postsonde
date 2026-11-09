// Calls into the Go RunnerService (internal/app/services/runner): the
// folder runner (RUN-01, RUN-03). A run sends the requests of a folder in
// order, for a number of iterations with a delay between the requests, as
// one runner operation (docs/events.md) whose items are the result of each
// request of each iteration. See docs/runner.md.
import {
  RunnerService,
  type DataPreviewDTO,
  type ExportRunResultDTO,
  type PlanDTO,
  type RunInputDTO,
  type RunItemDTO,
  type RunRecordDTO,
  type RunResultDTO,
  type RunSummaryDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/runner";
import type { CancellablePromise } from "./calls";
import type { ResponseDTO, SendAnswers } from "./http";
import type { StartedDTO } from "./operations";

export type { DataPreviewDTO, ExportRunResultDTO, PlanDTO, RunInputDTO, RunItemDTO, RunRecordDTO, RunResultDTO, RunSummaryDTO };

/** The formats of a run report (RUN-08). */
export type RunReportFormat = "junit" | "json" | "html";

/** The report formats, in the order offered. */
export const RUN_REPORT_FORMATS: readonly RunReportFormat[] = ["junit", "json", "html"];

/** The past runs of the open project (RUN-09), newest first. */
export function listPastRuns(): CancellablePromise<RunSummaryDTO[] | null> {
  return RunnerService.Runs();
}

/** Past run `id` with its results (without responses). Rejects with `runner.run_not_found` when it is gone. */
export function loadPastRun(id: string): CancellablePromise<RunRecordDTO> {
  return RunnerService.Run(id);
}

/** Removes past run `id` from the history. */
export function deletePastRun(id: string): CancellablePromise<void> {
  return RunnerService.DeleteRun(id);
}

/**
 * Saves run `id` (a past run, or one that just ended: its operation ID)
 * as a report in `format` where the user picks in the native save dialog,
 * secrets masked (RUN-08). Resolves with `saved: false` when cancelled.
 */
export function exportRunReport(id: string, format: RunReportFormat): CancellablePromise<ExportRunResultDTO> {
  return RunnerService.ExportRun({ runId: id, format });
}

/** The domain of folder runs: their events are `runner:op:data|progress|done`. */
export const RUNNER_DOMAIN = "runner";

/** The progress message while a run waits the delay (Go's StageDelay). */
export const RUNNER_DELAY_STAGE = "Waiting before the next request"; // i18n-ignore: Go's stage value, compared, not shown

/** The last progress message of a run that stopped at a failure (Go's StageStopped). */
export const RUNNER_STOPPED_STAGE = "Stopped at a failure"; // i18n-ignore: Go's stage value, compared, not shown

/** The most iterations of a run (Go's runner.MaxIterations). */
export const MAX_ITERATIONS = 10_000;

/** The longest delay between two requests, an hour (Go's runner.MaxDelay). */
export const MAX_DELAY_MS = 60 * 60 * 1000;

/** What a result's outcome can be. */
export type RunOutcome = "passed" | "failed" | "error" | "skipped";

/** Why an item is not sent: its kind, a GraphQL subscription, an unknown kind or a file that did not load. */
export type RunSkip = "" | "kind" | "subscription" | "unknown" | "invalid";

/**
 * The requests a run of folder `folderId` sends ("" for the whole
 * project), in order, each with why it is skipped when it is. Nothing is
 * sent.
 */
export function folderRunPlan(folderId: string): CancellablePromise<PlanDTO> {
  return RunnerService.Plan(folderId);
}

/**
 * The response of result `index` of run `opId` (RUN-07), as a send
 * returns it: the request as sent is in its timeline. Rejects with
 * RESPONSE_GONE_CODE when it is not kept (any more).
 */
export function folderRunResponse(opId: string, index: number): CancellablePromise<ResponseDTO> {
  return RunnerService.Response(opId, index);
}

/** Go's code when a result's response is no longer kept. */
export const RESPONSE_GONE_CODE = "runner.response_gone";

/** Drops the kept responses of run `opId`: its results are no longer shown. */
export function releaseFolderRun(opId: string): CancellablePromise<void> {
  return RunnerService.Release(opId);
}

/** How a run goes (the part of RunInputDTO the Run sub-tab sets). */
export interface FolderRunInput {
  readonly iterations: number;
  readonly delayMs: number;
  /** The requests to send, in order (RUN-02); undefined for every request of the folder in the tree's order. */
  readonly requestIds?: readonly string[];
  /** End the run at the first result that failed or is an error (RUN-04). */
  readonly stopOnFailure?: boolean;
  /** A data file chosen with pickRunDataFile (RUN-05): one iteration per row, `iterations` unused. */
  readonly dataFile?: RunDataFile;
}

/** A data file of a run and its CSV delimiter ("" to detect it). */
export interface RunDataFile {
  readonly path: string;
  readonly delimiter: DataDelimiter;
}

/** The CSV delimiters a data file may use; "" detects it (Go's runner.Delimiters). */
export type DataDelimiter = "" | "," | ";" | "\t" | "|";

/** The delimiters to choose from, "" (detect) first. */
export const DATA_DELIMITERS: readonly DataDelimiter[] = ["", ",", ";", "\t", "|"];

/** Go's code when a data file named in a run was not chosen in this session. */
export const DATA_NOT_PICKED_CODE = "runner.data_not_picked";

/**
 * Shows the native open dialog for a data file (RUN-05) and reads it with
 * `delimiter`: its columns, first rows and number of rows. Resolves with
 * an empty `path` when the dialog was cancelled. Rejects with
 * `runner.data_invalid`, `runner.data_too_large`, `runner.data_empty` or
 * `runner.data_unreadable` (details.path: the file chosen).
 */
export function pickRunDataFile(delimiter: DataDelimiter): CancellablePromise<DataPreviewDTO> {
  return RunnerService.PickDataFile(delimiter);
}

/** Reads data file `path`, chosen with pickRunDataFile in this session, again with `delimiter`. */
export function previewRunDataFile(path: string, delimiter: DataDelimiter): CancellablePromise<DataPreviewDTO> {
  return RunnerService.PreviewDataFile(path, delimiter);
}

/**
 * Starts a run of folder `folderId`: `iterations` times the requests of
 * `run.requestIds` in that order (every request of the folder, in the
 * tree's order, without it), with `delayMs` between two requests, ending
 * at the first failure with `stopOnFailure`. Resolves with the operation
 * ID at once; each request's outcome arrives as a RunResultDTO item of
 * RUNNER_DOMAIN, and cancelling the operation stops the run. Rejects with
 * `runner.invalid_options`, `runner.empty`, `runner.invalid_selection`,
 * and with PROMPT_CODE when requests of the run have {{$prompt}} values
 * without an answer in `answers.prompts` (asked once for the whole run).
 */
export function startFolderRun(folderId: string, run: FolderRunInput, answers: SendAnswers = {}): CancellablePromise<StartedDTO> {
  const input: RunInputDTO = { folderId, iterations: run.iterations, delayMs: run.delayMs };
  if (run.requestIds !== undefined) input.requestIds = [...run.requestIds];
  if (run.stopOnFailure === true) input.stopOnFailure = true;
  if (run.dataFile !== undefined) {
    input.dataFile = run.dataFile.path;
    if (run.dataFile.delimiter !== "") input.dataDelimiter = run.dataFile.delimiter;
  }
  if (answers.allowUnresolved) input.allowUnresolved = true;
  if (answers.prompts !== undefined) input.prompts = { ...answers.prompts };
  return RunnerService.Start(input);
}
