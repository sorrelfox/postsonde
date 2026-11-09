// Folder runs (RUN-01 to RUN-04): one run slot per folder, holding its
// latest run: the options, the results as they arrive, the counts and the
// progress; and the folder's plan with the requests chosen for its runs
// and their order (RUN-02), for the session only. A run is one runner
// operation (docs/events.md). The slots live outside React, so switching
// tabs keeps a running run, its results and the selection; closing the
// folder's tab stops the run and drops them. See docs/runner.md.
import { useMemo, useSyncExternalStore } from "react";
import { toAppError, type AppError } from "../lib/errors";
import { PROMPT_CODE, promptsOf, type ResponseDTO, type SendAnswers } from "../lib/http";
import { startOperation, type OperationHandle, type StartedDTO } from "../lib/operations";
import {
  RUNNER_DELAY_STAGE,
  RUNNER_DOMAIN,
  RUNNER_STOPPED_STAGE,
  folderRunPlan,
  folderRunResponse,
  pickRunDataFile,
  previewRunDataFile,
  releaseFolderRun,
  startFolderRun,
  type DataDelimiter,
  type DataPreviewDTO,
  type RunItemDTO,
  type RunOutcome,
  type RunRecordDTO,
  type RunResultDTO,
} from "../lib/runner";
import {
  EMPTY_RUN_SELECTION,
  mergeRunOrder,
  moveRunItem,
  moveRunItemTo,
  selectedRunIds,
  type RunSelection,
} from "../lib/runSelection";
import type { NodeId, Stores, TreeNode } from "../stores";
import { waitForQueuedEdits } from "./autosave";
import { askPrompts } from "./prompts";
import { openFolder, onTabClosed } from "./tabs";

/** "idle" before the first run; "starting" until Go has accepted it; then how it ended. */
export type FolderRunStatus = "idle" | "starting" | "running" | "ok" | "error" | "cancelled";

/** How a run goes: `iterations` times the folder, `delayMs` between two requests, ending at the first failure with `stopOnFailure`. */
export interface FolderRunOptions {
  readonly iterations: number;
  readonly delayMs: number;
  readonly stopOnFailure?: boolean;
}

/** The results of each outcome so far. */
export type FolderRunCounts = Readonly<Record<RunOutcome, number>>;

/** The durations of the requests a run sent (every result but the skipped ones), for its summary (RUN-07). */
export interface FolderRunTiming {
  /** Results timed, and the sum, shortest and longest of their durations. */
  readonly sent: number;
  readonly totalMs: number;
  readonly minMs: number;
  readonly maxMs: number;
  /** The slowest result: its index (to open it while it is listed) and name. */
  readonly slowest: { readonly index: number; readonly name: string; readonly iteration: number } | null;
}

export interface FolderRunState {
  readonly status: FolderRunStatus;
  readonly options: FolderRunOptions;
  /** The latest MAX_FOLDER_RUN_RESULTS results, in the order sent; the counts cover all of them. */
  readonly results: readonly RunResultDTO[];
  readonly counts: FolderRunCounts;
  /** Results listed so far, and how many the run has in all (iterations × requests), from Go's progress. */
  readonly done: number;
  readonly total: number;
  /** The run waits the delay before its next request. */
  readonly waiting: boolean;
  /** Why the run could not start or go on; null otherwise. */
  readonly error: AppError | null;
  /** When the run started (Date.now()), null before one. */
  readonly startedAt: number | null;
  /** The run ended at a failure (stopOnFailure) before its last request. */
  readonly stopped: boolean;
  /** Stop was asked for and Go has not said the run ended yet. */
  readonly stopping: boolean;
  /** The run's operation ID once Go has started it: its results' responses are asked for by it. */
  readonly opId: string | null;
  /** The request IDs of an iteration of the run, in order; null when it sends the whole folder unseen (no plan). */
  readonly runOrder: readonly string[] | null;
  /** How long the run took, start to end, from Go's done event; null before it ends. */
  readonly elapsedMs: number | null;
  /** The time of the requests sent, over every result. */
  readonly timing: FolderRunTiming;
  /** The folder's requests (RunnerService.Plan) in the tree's order; null until loaded. */
  readonly plan: readonly RunItemDTO[] | null;
  /** Why the plan could not be loaded; null otherwise. */
  readonly planError: AppError | null;
  /** The requests chosen for the runs and their order: for the session, never saved. */
  readonly selection: RunSelection;
  /** The data file of the runs (RUN-05): for the session, never saved; null for none. */
  readonly data: FolderRunData | null;
  /** Why the data file could not be chosen or read; null otherwise. */
  readonly dataError: AppError | null;
  /** The data file is being chosen or read. */
  readonly dataBusy: boolean;
}

/** A data file chosen for the runs of a folder (RUN-05). */
export interface FolderRunData {
  readonly path: string;
  readonly name: string;
  readonly delimiter: DataDelimiter;
  /** The file as last read: columns, first rows, row count; null when it could not be read (dataError says why). */
  readonly preview: DataPreviewDTO | null;
}

/** How many results a run keeps for the list. */
export const MAX_FOLDER_RUN_RESULTS = 5000;

const NO_COUNTS: FolderRunCounts = { passed: 0, failed: 0, error: 0, skipped: 0 };

const NO_TIMING: FolderRunTiming = { sent: 0, totalMs: 0, minMs: 0, maxMs: 0, slowest: null };

/** The state of a run before it starts: what every start resets. */
const NO_RUN = {
  results: [],
  counts: NO_COUNTS,
  done: 0,
  total: 0,
  waiting: false,
  error: null,
  stopped: false,
  stopping: false,
  opId: null,
  runOrder: null,
  elapsedMs: null,
  timing: NO_TIMING,
} as const satisfies Partial<FolderRunState>;

const INITIAL: FolderRunState = {
  ...NO_RUN,
  status: "idle",
  options: { iterations: 1, delayMs: 0 },
  startedAt: null,
  plan: null,
  planError: null,
  selection: EMPTY_RUN_SELECTION,
  data: null,
  dataError: null,
  dataBusy: false,
};

/** The fields of `state` that belong to its run, not to the plan, the selection and the data file. */
function runPart(state: FolderRunState): Partial<FolderRunState> {
  const {
    plan: _plan,
    planError: _planError,
    selection: _selection,
    data: _data,
    dataError: _dataError,
    dataBusy: _dataBusy,
    ...run
  } = state;
  return run;
}

/** `state` with `items` added to its results and counts. */
export function addFolderRunResults(state: FolderRunState, items: readonly RunResultDTO[]): FolderRunState {
  if (items.length === 0) return state;
  const counts = { ...state.counts };
  let { sent, totalMs, minMs, maxMs, slowest } = state.timing;
  for (const r of items) {
    const outcome = r.outcome as RunOutcome;
    if (outcome in counts) counts[outcome]++;
    if (outcome === "skipped") continue;
    const ms = r.durationMs;
    minMs = sent === 0 ? ms : Math.min(minMs, ms);
    if (sent === 0 || ms > maxMs) slowest = { index: r.index, name: r.name, iteration: r.iteration };
    maxMs = sent === 0 ? ms : Math.max(maxMs, ms);
    totalMs += ms;
    sent++;
  }
  const all = state.results.concat(items);
  const results = all.length > MAX_FOLDER_RUN_RESULTS ? all.slice(all.length - MAX_FOLDER_RUN_RESULTS) : all;
  const last = items[items.length - 1];
  const timing = { sent, totalMs, minMs, maxMs, slowest };
  return { ...state, results, counts, timing, done: Math.max(state.done, last?.index ?? 0) };
}

/**
 * The state of past run `record` (RUN-09), to show with the panel's
 * summary and results: its options, results, counts (every result, the
 * ones not kept included) and time. It has no operation: responses are not
 * kept in the history.
 */
export function pastFolderRunState(record: RunRecordDTO): FolderRunState {
  const s = record.summary;
  const base: FolderRunState = {
    ...INITIAL,
    status: s.status === "cancelled" ? "cancelled" : "ok",
    options: { iterations: s.iterations, delayMs: s.delayMs, stopOnFailure: s.stopOnFailure },
    startedAt: s.startedAt,
    total: s.total,
    elapsedMs: s.elapsedMs,
    stopped: s.status === "stopped",
  };
  const state = addFolderRunResults(base, record.results ?? []);
  return { ...state, done: s.results, counts: { passed: s.passed, failed: s.failed, error: s.errors, skipped: s.skipped } };
}

/** Waits until the edits of the folder's requests have reached Go, so the run sends what the editors show. */
export type PrepareRun = () => Promise<void>;

/** One folder's latest run. */
export class FolderRun {
  readonly folderId: string;
  private state: FolderRunState = INITIAL;
  private readonly listeners = new Set<() => void>();
  private handle: OperationHandle | null = null;
  /** Bumped by every run and cancel, so a stale callback does nothing. */
  private generation = 0;
  /** Bumped by every plan load, so only the latest one lands. */
  private planLoads = 0;
  /** Bumped by every data file pick or read, so only the latest one lands. */
  private dataLoads = 0;
  private disposed = false;

  constructor(folderId: string) {
    this.folderId = folderId;
  }

  getState = (): FolderRunState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  private set(patch: Partial<FolderRunState>): void {
    this.state = { ...this.state, ...patch };
    for (const listener of [...this.listeners]) listener();
  }

  /** Whether a run is starting or running. */
  get running(): boolean {
    return this.state.status === "starting" || this.state.status === "running";
  }

  /**
   * Starts a run with `options`, unless one is running. `prepare` runs
   * first. Go asks for the {{$prompt}} values of all the requests at once
   * (DYN-07): they are asked here, then the run starts again with them.
   * The results arrive as state changes; cancel() stops the run.
   */
  start(options: FolderRunOptions, prepare?: PrepareRun): void {
    if (this.running) return;
    const generation = ++this.generation;
    const before = runPart(this.state);
    // The chosen requests in their order, once the plan is known; the
    // whole folder in the tree's order before.
    const { plan, selection, data } = this.state;
    const requestIds = plan === null ? undefined : selectedRunIds(plan, selection);
    // With a data file: one iteration per row (RUN-05), as Go counts them.
    const dataFile = data?.preview != null ? { path: data.path, delimiter: data.delimiter } : undefined;
    if (data?.preview != null) options = { ...options, iterations: data.preview.rowCount };
    this.set({ ...NO_RUN, status: "starting", options, startedAt: Date.now(), runOrder: requestIds ?? null });
    const mine = () => this.generation === generation;
    // Set when the user cancelled the prompt: the state goes back to what
    // it was before. (Undefined variables do not refuse a run: each
    // request that has one is listed with the error.)
    let declined = false;
    const call = async (): Promise<StartedDTO> => {
      let answers: SendAnswers = {};
      for (;;) {
        try {
          return await startFolderRun(this.folderId, { ...options, requestIds, dataFile }, answers);
        } catch (err: unknown) {
          const e = toAppError(err);
          if (!mine()) throw err;
          if (e.code === PROMPT_CODE && answers.prompts === undefined) {
            const values = await askPrompts(promptsOf(e.details), "run");
            if (values === null || !mine()) {
              declined = true;
              throw err;
            }
            answers = { ...answers, prompts: values };
            continue;
          }
          throw err;
        }
      }
    };
    void (async () => {
      try {
        await prepare?.();
      } catch (err: unknown) {
        console.warn("runner: the edits were not flushed before the run", toAppError(err));
      }
      if (!mine()) return;
      const handle = startOperation<RunResultDTO>(RUNNER_DOMAIN, call, {
        onStarted: (opId) => {
          if (!mine()) {
            // Dropped while it started: nobody shows its results.
            releaseResponses(opId);
            return;
          }
          // The results of the run before are gone from the panel now:
          // Go can drop their responses (kept until here, in case the
          // prompt was cancelled and they came back).
          releaseResponses(before.opId ?? null);
          this.set({ status: "running", opId });
        },
        onProgress: (ev) => {
          if (!mine() || !this.running) return;
          this.set({
            done: Math.max(this.state.done, ev.done),
            total: ev.total,
            waiting: ev.message === RUNNER_DELAY_STAGE,
            stopped: ev.message === RUNNER_STOPPED_STAGE,
          });
        },
        onData: (items) => {
          if (mine()) this.set(addFolderRunResults(this.state, items));
        },
        onDone: (result) => {
          if (!mine()) return;
          this.handle = null;
          if (declined) {
            this.set(before);
            return;
          }
          // Refused before it started: the results before are gone too.
          if (this.state.opId === null) releaseResponses(before.opId ?? null);
          this.set({ status: result.outcome, waiting: false, stopping: false, error: result.error, elapsedMs: result.elapsedMs });
        },
      });
      if (mine()) this.handle = handle;
      else {
        handle.cancel();
        handle.dispose();
      }
    })();
  }

  /** Stops the running run, if any. The state follows when Go has stopped it. */
  cancel(): void {
    if (!this.running) return;
    if (this.handle !== null) {
      // Go stops at once, during a request or a delay; the done event
      // follows within milliseconds. Until then the panel says Stopping.
      if (!this.state.stopping) this.set({ stopping: true });
      this.handle.cancel();
      return;
    }
    // Still flushing the edits: nothing was sent yet.
    this.generation++;
    this.set({ status: "cancelled", waiting: false });
  }

  /**
   * Loads the folder's plan (RunnerService.Plan) again: the requests of
   * the folder in the tree's order, each with why it is skipped. The
   * selection keeps its order and choices; requests added since go in at
   * their tree place, removed ones drop out.
   */
  loadPlan(): void {
    const load = ++this.planLoads;
    folderRunPlan(this.folderId).then(
      (p) => {
        if (load !== this.planLoads || this.disposed) return;
        const plan = p.items ?? [];
        const order = mergeRunOrder(
          plan.map((it) => it.requestId),
          this.state.selection.order,
        );
        const selection = order === this.state.selection.order ? this.state.selection : { ...this.state.selection, order };
        this.set({ plan, planError: null, selection });
      },
      (err: unknown) => {
        if (load !== this.planLoads || this.disposed) return;
        this.set({ planError: toAppError(err) });
      },
    );
  }

  private select(selection: RunSelection): void {
    if (selection !== this.state.selection) this.set({ selection });
  }

  /** Puts requests `ids` in the runs (`included`) or leaves them out. */
  setIncluded(ids: readonly string[], included: boolean): void {
    const excluded = { ...this.state.selection.excluded };
    let changed = false;
    for (const id of ids) {
      if (included && excluded[id] === true) {
        delete excluded[id];
        changed = true;
      } else if (!included && excluded[id] !== true) {
        excluded[id] = true;
        changed = true;
      }
    }
    if (changed) this.select({ ...this.state.selection, excluded });
  }

  /** Moves request `id` `delta` places in the order of the runs (-1 up, 1 down); false at an edge. */
  move(id: string, delta: number): boolean {
    const current = this.currentOrder();
    const order = moveRunItem(current, id, delta);
    if (order === current) return false;
    this.select({ ...this.state.selection, order });
    return true;
  }

  /** Moves request `id` just before (or after) request `target`. */
  moveTo(id: string, target: string, after: boolean): void {
    const current = this.currentOrder();
    const order = moveRunItemTo(current, id, target, after);
    if (order !== current) this.select({ ...this.state.selection, order });
  }

  /** Every request in, in the tree's order. */
  resetSelection(): void {
    this.select(EMPTY_RUN_SELECTION);
  }

  /**
   * Shows the native open dialog for a data file (RUN-05) and reads it
   * with the delimiter chosen: the runs then make one iteration per row.
   * A cancelled dialog keeps the file there was. A file that cannot be
   * read is kept with the reason (dataError), so another delimiter can be
   * tried; Run stays off until it reads.
   */
  chooseDataFile(): void {
    if (this.running) return;
    const load = ++this.dataLoads;
    const delimiter = this.state.data?.delimiter ?? "";
    this.set({ dataBusy: true });
    pickRunDataFile(delimiter).then(
      (preview) => {
        if (load !== this.dataLoads || this.disposed) return;
        if (preview.path === "") {
          this.set({ dataBusy: false });
          return;
        }
        this.set({ data: { path: preview.path, name: preview.name, delimiter, preview }, dataError: null, dataBusy: false });
      },
      (err: unknown) => {
        if (load !== this.dataLoads || this.disposed) return;
        const e = toAppError(err);
        const path = e.details.path ?? "";
        this.set({
          data: path === "" ? this.state.data : { path, name: e.details.name ?? path, delimiter, preview: null },
          dataError: e,
          dataBusy: false,
        });
      },
    );
  }

  /** Reads the data file again with CSV delimiter `delimiter` ("" to detect it). */
  setDataDelimiter(delimiter: DataDelimiter): void {
    const data = this.state.data;
    if (data === null || this.running) return;
    const load = ++this.dataLoads;
    this.set({ data: { ...data, delimiter }, dataBusy: true });
    previewRunDataFile(data.path, delimiter).then(
      (preview) => {
        if (load !== this.dataLoads || this.disposed) return;
        this.set({ data: { ...data, delimiter, preview }, dataError: null, dataBusy: false });
      },
      (err: unknown) => {
        if (load !== this.dataLoads || this.disposed) return;
        this.set({ data: { ...data, delimiter, preview: null }, dataError: toAppError(err), dataBusy: false });
      },
    );
  }

  /** Runs without a data file again: the iterations come from the Iterations field. */
  clearDataFile(): void {
    if (this.running) return;
    this.dataLoads++;
    this.set({ data: null, dataError: null, dataBusy: false });
  }

  /** The order of the runs over the whole plan. */
  private currentOrder(): readonly string[] {
    const plan = this.state.plan ?? [];
    return mergeRunOrder(
      plan.map((it) => it.requestId),
      this.state.selection.order,
    );
  }

  /** Stops a running run and forgets the listeners. */
  dispose(): void {
    this.disposed = true;
    this.generation++;
    const handle = this.handle;
    this.handle = null;
    handle?.cancel();
    handle?.dispose();
    releaseResponses(this.state.opId);
    this.listeners.clear();
  }
}

/** Lets Go drop the kept responses of run `opId` (null: none); failures do not matter. */
function releaseResponses(opId: string | null): void {
  if (opId === null) return;
  releaseFolderRun(opId).catch((err: unknown) => {
    console.warn("runner: the run's responses were not released", toAppError(err));
  });
}

/**
 * The response of result `index` of run `opId` (RUN-07), with the request
 * as sent in its timeline. Rejects with `runner.response_gone` when Go no
 * longer keeps it.
 */
export async function loadFolderRunResponse(opId: string, index: number): Promise<ResponseDTO> {
  return await folderRunResponse(opId, index);
}

/** The run slots of the folders. */
export class FolderRuns {
  private readonly runs = new Map<string, FolderRun>();
  private readonly unsubscribe: () => void;

  constructor() {
    // Closing a folder's tab stops its run and drops its results.
    this.unsubscribe = onTabClosed((ids) => this.drop(ids));
  }

  /** The run slot of folder `folderId`, created on first use. */
  get(folderId: string): FolderRun {
    let run = this.runs.get(folderId);
    if (run === undefined) {
      run = new FolderRun(folderId);
      this.runs.set(folderId, run);
    }
    return run;
  }

  /** Stops and forgets the runs of these folders. */
  drop(folderIds: readonly string[]): void {
    for (const id of folderIds) {
      const run = this.runs.get(id);
      if (run === undefined) continue;
      this.runs.delete(id);
      run.dispose();
    }
  }

  /** Stops every run and stops following tab closes. */
  dispose(): void {
    this.drop([...this.runs.keys()]);
    this.unsubscribe();
  }
}

let registry: FolderRuns | null = null;

/** The app's folder run slots, created on first use. */
export function folderRuns(): FolderRuns {
  registry ??= new FolderRuns();
  return registry;
}

/** Drops the registry, for tests. */
export function resetFolderRuns(): void {
  registry?.dispose();
  registry = null;
}

/** The run slot of folder `folderId` and its state, for a component. */
export function useFolderRun(folderId: string): { run: FolderRun; state: FolderRunState } {
  const run = useMemo(() => folderRuns().get(folderId), [folderId]);
  const state = useSyncExternalStore(run.subscribe, run.getState);
  return { run, state };
}

/** The requests under folder `id`, subfolders included. */
function requestIds(nodes: Readonly<Record<NodeId, TreeNode>>, id: NodeId): NodeId[] {
  const out: NodeId[] = [];
  const visit = (nodeId: NodeId, depth: number) => {
    const node = nodes[nodeId];
    if (node === undefined || depth > 1000) return;
    if (node.kind === "folder") node.childIds.forEach((c) => visit(c, depth + 1));
    else if (node.kind === "request") out.push(node.id);
  };
  visit(id, 0);
  return out;
}

/**
 * Starts a run of folder `id` with `options`: the queued edits of its
 * requests reach Go first. Does nothing while a run of it is running.
 */
export function runFolder(stores: Stores, id: NodeId, options: FolderRunOptions): void {
  folderRuns()
    .get(id)
    .start(options, async () => {
      const { nodes } = stores.collection.getState();
      await Promise.all(requestIds(nodes, id).map((r) => waitForQueuedEdits(stores, r)));
    });
}

/** Stops the running run of folder `id`, if any. */
export function stopFolderRun(id: NodeId): void {
  folderRuns().get(id).cancel();
}

/** Opens folder `id`'s tab on its Run sub-tab (the tree's "Run folder…"). */
export function openFolderRun(stores: Stores, id: NodeId): void {
  openFolder(stores, id);
  stores.drafts.dispatch({ type: "drafts/setSubTab", id, subTab: "run" });
}
