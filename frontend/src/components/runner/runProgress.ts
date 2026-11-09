// What the Run sub-tab shows of a run as it goes and once it ended
// (RUN-06, RUN-07): the status of each request in the current iteration,
// and the failures for the summary. Pure functions of the run's state.
import type { FolderRunState } from "../../commands/folderRuns";
import type { RunResultDTO } from "../../lib/runner";

/** Where a request of the run is: waiting its turn, being sent, waiting the delay before it, done (with its latest result) or never reached. */
export type RequestRunStatus =
  | { readonly kind: "queued" }
  | { readonly kind: "sending" }
  | { readonly kind: "waiting" }
  | { readonly kind: "done"; readonly result: RunResultDTO }
  | { readonly kind: "notRun" };

/**
 * The status of each request of the run, by request ID: while it runs,
 * those of the current iteration before the one being sent are done with
 * this iteration's result, that one is being sent (or waited for), and
 * the rest are queued; once it ended, each has its latest result, or was
 * never reached (a stop). Empty before a run, and when the run sends the
 * whole folder without the plan (no order to follow).
 */
export function requestRunStatuses(state: FolderRunState): ReadonlyMap<string, RequestRunStatus> {
  const out = new Map<string, RequestRunStatus>();
  const order = state.runOrder;
  if (order === null || order.length === 0 || state.status === "idle") return out;
  const latest = new Map<string, RunResultDTO>();
  for (const r of state.results) latest.set(r.requestId, r);
  const running = state.status === "starting" || state.status === "running";
  if (!running) {
    for (const id of order) {
      const r = latest.get(id);
      out.set(id, r === undefined ? { kind: "notRun" } : { kind: "done", result: r });
    }
    return out;
  }
  const n = order.length;
  const iteration = Math.floor(state.done / n) + 1;
  const current = state.done % n;
  order.forEach((id, i) => {
    const r = latest.get(id);
    if (i < current && r !== undefined && r.iteration === iteration) out.set(id, { kind: "done", result: r });
    else if (i === current && state.status === "running") out.set(id, { kind: state.waiting ? "waiting" : "sending" });
    else out.set(id, { kind: "queued" });
  });
  return out;
}

/** The iteration the run is at while it runs (1 when unknown). */
export function currentIteration(state: FolderRunState): number {
  const n = state.runOrder?.length ?? 0;
  return n === 0 ? 1 : Math.min(Math.floor(state.done / n) + 1, state.options.iterations);
}

/** The results listed that failed or are errors, in the order sent. */
export function failedResults(state: FolderRunState): RunResultDTO[] {
  return state.results.filter((r) => r.outcome === "failed" || r.outcome === "error");
}
