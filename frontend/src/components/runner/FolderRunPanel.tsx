import { Fragment, useEffect, useId, useRef, useState, type FormEvent, type KeyboardEvent, type RefObject } from "react";
import { runFolder, stopFolderRun, useFolderRun, type FolderRunState } from "../../commands/folderRuns";
import type { PastRun } from "../../commands/runHistory";
import { t } from "../../i18n";
import { useT } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import { extractionFailed } from "../../lib/extractions";
import { formatDurationMs, statusTone } from "../../lib/http";
import { MAX_DELAY_MS, MAX_ITERATIONS, type RunResultDTO } from "../../lib/runner";
import { selectedRunIds } from "../../lib/runSelection";
import { useCollection, useStores, type NodeId } from "../../stores";
import { IconWarning } from "../common/icons";
import styles from "./FolderRunPanel.module.css";
import { OUTCOME_TEXT, runBadge, SKIP_TEXT } from "./runBadge";
import { RunDataFile } from "./RunDataFile";
import { pastRunWhen, RunHistory } from "./RunHistory";
import { RunReportActions } from "./RunReportActions";
import { currentIteration } from "./runProgress";
import { RunRequestList } from "./RunRequestList";
import { RunResultDialog } from "./RunResultDialog";
import { RunSummary } from "./RunSummary";

export interface FolderRunPanelProps {
  folderId: NodeId;
}

/** A whole number of iterations from a field, or null when it is not one in [1, MAX_ITERATIONS]. */
export function parseIterations(text: string): number | null {
  const s = text.trim();
  if (!/^\d+$/.test(s)) return null;
  const n = Number(s);
  return n >= 1 && n <= MAX_ITERATIONS ? n : null;
}

/** Milliseconds from a seconds field, or null when it is not a number of seconds in [0, an hour]. */
export function parseDelay(text: string): number | null {
  const s = text.trim();
  if (s === "" || !/^\d*\.?\d*$/.test(s) || s === ".") return null;
  const ms = Math.round(Number(s) * 1000);
  return Number.isFinite(ms) && ms >= 0 && ms <= MAX_DELAY_MS ? ms : null;
}

/** "Running: 3 of 24 done", "Run stopped after 7 of 24", …: the run's state in a few words. */
export function folderRunHeadline(state: FolderRunState): string {
  const { done, total } = state;
  switch (state.status) {
    case "starting":
      return t("runner.status.starting");
    case "running":
      if (state.stopping) return t("runner.status.stopping");
      return state.waiting ? t("runner.status.waiting", { done, total }) : t("runner.status.running", { done, total });
    case "cancelled":
      return t("runner.status.cancelled", { done, total });
    case "error":
      return state.startedAt !== null && state.total === 0 ? t("runner.status.failedToStart") : t("runner.status.error");
    default:
      return state.stopped ? t("runner.status.stopped", { done, total }) : t("runner.status.ok", { count: done });
  }
}

/** "20 passed · 2 failed · 1 error · 1 skipped": the counts, those that are not 0 (passed always). */
export function folderRunCounts(state: FolderRunState): string {
  const { passed, failed, error, skipped } = state.counts;
  const parts = [t("runner.count.passed", { count: passed })];
  if (failed > 0) parts.push(t("runner.count.failed", { count: failed }));
  if (error > 0) parts.push(t("runner.count.error", { count: error }));
  if (skipped > 0) parts.push(t("runner.count.skipped", { count: skipped }));
  return parts.join(" · ");
}

/**
 * The Run sub-tab of a folder (RUN-01 to RUN-04, RUN-06, RUN-07): the
 * iterations, the delay between requests and whether to stop at the first
 * failure, the requests of the run to choose and reorder, each with where
 * it is while the run goes (RunRequestList), Run folder / Stop, the run's
 * state, progress and counts, once it ended its summary (RunSummary), and
 * the results in the order sent, grouped by iteration: each request with
 * its outcome, status, time and tests, and under it why it failed (the
 * failed assertions' messages, the error) or why it was skipped. A
 * result's name opens its request and response (RunResultDialog).
 */
export function FolderRunPanel({ folderId }: FolderRunPanelProps) {
  const t = useT();
  const stores = useStores();
  const baseId = useId();
  const { run, state } = useFolderRun(folderId);
  const [iterations, setIterations] = useState(String(state.options.iterations));
  const [delay, setDelay] = useState(String(state.options.delayMs / 1000));
  const [stopOnFailure, setStopOnFailure] = useState(state.options.stopOnFailure === true);
  const [opened, setOpened] = useState<RunResultDTO | null>(null);
  // A past run reopened from the history (RUN-09), shown in place of the latest run.
  const [past, setPast] = useState<PastRun | null>(null);
  const pastOpener = useRef<HTMLElement | null>(null);
  const pastHeading = useRef<HTMLHeadingElement>(null);
  const pastId = past?.summary.id ?? null;
  useEffect(() => {
    if (pastId !== null) pastHeading.current?.focus();
  }, [pastId]);
  const closePast = () => {
    setOpened(null);
    setPast(null);
    const opener = pastOpener.current;
    pastOpener.current = null;
    // Back to the Open button, or the history when it is gone.
    requestAnimationFrame(() => {
      if (opener?.isConnected === true) opener.focus();
    });
  };
  // With a data file the rows are the iterations (RUN-05): the field shows their number.
  const data = state.data;
  const dataReady = data === null || (data.preview !== null && !state.dataBusy);
  const iterationsValue = data !== null ? 1 : parseIterations(iterations);
  const delayValue = parseDelay(delay);
  const running = state.status === "starting" || state.status === "running";
  // Nothing chosen once the plan is known: nothing to run.
  const nothingChosen = state.plan !== null && selectedRunIds(state.plan, state.selection).length === 0;
  const valid = iterationsValue !== null && delayValue !== null && !nothingChosen && dataReady;

  // The folder's requests, again whenever the tree changes (added,
  // removed, moved or renamed requests).
  const nodes = useCollection((s) => s.nodes);
  useEffect(() => {
    run.loadPlan();
  }, [run, nodes]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (running) stopFolderRun(folderId);
    else if (valid && iterationsValue !== null && delayValue !== null) {
      setOpened(null);
      setPast(null);
      runFolder(stores, folderId, { iterations: iterationsValue, delayMs: delayValue, stopOnFailure });
    }
  };

  const field = (name: string) => `${baseId}-${name}`;
  const hidden = state.done - state.results.length;
  const ended = state.status === "ok" || state.status === "cancelled" || (state.status === "error" && state.done > 0);
  const multi = state.options.iterations > 1;
  return (
    <div className={styles.panel}>
      <p className={styles.intro}>{t("runner.intro")}</p>
      <form className={styles.form} aria-label={t("runner.options")} onSubmit={submit}>
        <label className={styles.field}>
          <span className={styles.label}>{t("runner.iterations")}</span>
          <input
            className={styles.input}
            inputMode="numeric"
            value={data !== null ? String(data.preview?.rowCount ?? "") : iterations}
            disabled={running || data !== null}
            aria-invalid={iterationsValue === null}
            aria-describedby={data !== null ? field("iterations-data") : iterationsValue === null ? field("iterations-error") : undefined}
            onChange={(e) => setIterations(e.target.value)}
          />
        </label>
        <label className={styles.field}>
          <span className={styles.label}>{t("runner.delay")}</span>
          <input
            className={styles.input}
            inputMode="decimal"
            value={delay}
            disabled={running}
            aria-invalid={delayValue === null}
            aria-describedby={delayValue === null ? `${field("delay-unit")} ${field("delay-error")}` : field("delay-unit")}
            onChange={(e) => setDelay(e.target.value)}
          />
          <span id={field("delay-unit")} className={styles.unit}>
            {t("runner.delay.unit")}
          </span>
        </label>
        <label className={styles.field} title={t("runner.stopOnFailure.hint")}>
          <input
            type="checkbox"
            className={styles.check}
            checked={stopOnFailure}
            disabled={running}
            onChange={(e) => setStopOnFailure(e.target.checked)}
          />
          <span>{t("runner.stopOnFailure")}</span>
        </label>
        <button
          type="submit"
          className={running ? styles.stop : styles.start}
          disabled={running ? state.stopping : !valid}
          aria-describedby={!running && nothingChosen ? field("none-chosen") : undefined}
        >
          {running ? (state.stopping ? t("runner.stopping") : t("runner.stop")) : t("runner.start")}
        </button>
      </form>
      {nothingChosen && !running && (
        <p id={field("none-chosen")} className={styles.hint}>
          {t("runner.requests.noneChosen")}
        </p>
      )}
      {iterationsValue === null && (
        <p id={field("iterations-error")} className={styles.invalid}>
          {t("runner.iterations.invalid", { max: MAX_ITERATIONS })}
        </p>
      )}
      {data !== null && (
        <p id={field("iterations-data")} className={styles.hint}>
          {data.preview === null && !state.dataBusy ? t("runner.data.unreadable") : t("runner.data.iterations")}
        </p>
      )}
      {delayValue === null && (
        <p id={field("delay-error")} className={styles.invalid}>
          {t("runner.delay.invalid", { max: MAX_DELAY_MS / 1000 })}
        </p>
      )}

      <RunDataFile run={run} state={state} locked={running} />

      <RunRequestList run={run} state={state} locked={running} />

      <RunHistory
        folderId={folderId}
        reloadKey={`${state.status}:${state.opId ?? ""}`}
        openId={past?.summary.id ?? null}
        onOpened={(p, opener) => {
          pastOpener.current = opener;
          setOpened(null);
          setPast(p);
        }}
        onDeleted={(id) => {
          if (past?.summary.id === id) closePast();
        }}
      />

      {past !== null && (
        <PastRunView past={past} headingRef={pastHeading} onClose={closePast} onOpen={setOpened} />
      )}

      {past === null && state.status !== "idle" && (
        <div className={styles.summary}>
          <p className={styles.headline}>
            {running && <span className={styles.spinner} aria-hidden="true" />}
            {folderRunHeadline(state)}
            {running && multi && state.total > 0 && (
              <span className={styles.iterationNow}>
                {t("runner.iterationOf", { n: currentIteration(state), total: state.options.iterations })}
              </span>
            )}
          </p>
          {running && (
            // Live (RUN-06): how far the run is, updated with every progress event.
            <div
              className={styles.progress}
              role="progressbar"
              aria-label={t("runner.progress")}
              aria-valuemin={0}
              aria-valuemax={Math.max(state.total, 1)}
              aria-valuenow={state.done}
              aria-valuetext={t("runner.progress.value", { done: state.done, total: state.total })}
            >
              <span className={styles.progressBar} style={{ width: `${state.total > 0 ? (100 * state.done) / state.total : 0}%` }} />
            </div>
          )}
          <p className={styles.counts}>{folderRunCounts(state)}</p>
          {/* Announced once, when the run ends: not on every result. */}
          <span className={styles.srOnly} role="status">
            {running ? "" : t("runner.announce", { status: folderRunHeadline(state), counts: folderRunCounts(state) })}
          </span>
        </div>
      )}
      {past === null && state.status === "error" && state.error !== null && (
        <p className={styles.runError} role="alert">
          <IconWarning size={13} />
          {state.error.message}
        </p>
      )}
      {past === null && ended && <RunSummary state={state} onOpen={setOpened} />}
      {past === null && ended && state.opId !== null && state.done > 0 && <RunReportActions runId={state.opId} />}
      {past === null && state.results.length > 0 && <ResultList state={state} hidden={hidden} onOpen={setOpened} />}
      {opened !== null && (
        <RunResultDialog
          result={opened}
          opId={past === null ? state.opId : null}
          iterations={(past?.state ?? state).options.iterations}
          past={past !== null}
          onClose={() => setOpened(null)}
        />
      )}
    </div>
  );
}

interface PastRunViewProps {
  past: PastRun;
  headingRef: RefObject<HTMLHeadingElement>;
  onClose: () => void;
  onOpen: (r: RunResultDTO) => void;
}

/**
 * A past run reopened from the run history (RUN-09), in place of the
 * latest run: when it ran and how it ended, its summary, Save report and
 * its results, which open without a response (not kept).
 */
function PastRunView({ past, headingRef, onClose, onOpen }: PastRunViewProps) {
  const t = useT();
  const headingId = useId();
  const { summary: s, state } = past;
  const omitted = s.omitted;
  return (
    <section className={styles.past} aria-labelledby={headingId}>
      <div className={styles.requestsHead}>
        <h3 id={headingId} ref={headingRef} tabIndex={-1} className={styles.requestsTitle}>
          {t("runner.past.title", { when: pastRunWhen(s) })}
        </h3>
        <div className={styles.requestsTools}>
          <button type="button" className={styles.tool} onClick={onClose}>
            {t("runner.past.close")}
          </button>
        </div>
      </div>
      <p className={styles.headline}>{folderRunHeadline(state)}</p>
      <p className={styles.counts}>{folderRunCounts(state)}</p>
      {s.dataFile !== "" && (
        <p className={styles.hint}>{t("runner.history.data", { name: s.dataFile, rows: t("runner.data.rows", { count: s.dataRows }) })}</p>
      )}
      <p className={styles.hint}>{t("runner.past.noResponses")}</p>
      {omitted > 0 && <p className={styles.hint}>{t("runner.past.omitted", { count: omitted })}</p>}
      <RunSummary state={state} onOpen={onOpen} />
      <RunReportActions runId={s.id} />
      {state.results.length > 0 && <ResultList state={state} hidden={past.kept - state.results.length} onOpen={onOpen} />}
    </section>
  );
}

/**
 * The results in the order sent. Each result's name is a button that
 * opens it; the buttons are one Tab stop, ↑ / ↓ / Home / End move between
 * them (roving tabindex).
 */
function ResultList({ state, hidden, onOpen }: { state: FolderRunState; hidden: number; onOpen: (r: RunResultDTO) => void }) {
  const t = useT();
  const listRef = useRef<HTMLOListElement>(null);
  const [active, setActive] = useState<number | null>(null);
  const { results } = state;
  // The Tab stop: the result last focused, while it is listed, else the first.
  const tabStop = active !== null && results.some((r) => r.index === active) ? active : (results[0]?.index ?? null);
  const multi = state.options.iterations > 1;

  const onKeyDown = (e: KeyboardEvent<HTMLOListElement>) => {
    if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    const buttons = [...(listRef.current?.querySelectorAll<HTMLButtonElement>("button[data-result]") ?? [])];
    const at = buttons.indexOf(e.target as HTMLButtonElement);
    if (at < 0) return;
    let next: number;
    switch (e.key) {
      case "ArrowDown":
        next = Math.min(at + 1, buttons.length - 1);
        break;
      case "ArrowUp":
        next = Math.max(at - 1, 0);
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = buttons.length - 1;
        break;
      default:
        return;
    }
    e.preventDefault();
    buttons[next]?.focus();
  };

  return (
    <ol ref={listRef} className={styles.results} aria-label={t("runner.results")} onKeyDown={onKeyDown}>
      {hidden > 0 && <li className={styles.more}>{t("runner.hidden", { count: hidden })}</li>}
      {results.map((r, i) => {
        const first = i === 0 || results[i - 1]?.iteration !== r.iteration;
        return (
          <Fragment key={r.index}>
            {first && multi && (
              <li className={styles.iteration} aria-hidden="true">
                {t("runner.iteration", { n: r.iteration })}
              </li>
            )}
            <ResultRow result={r} multi={multi} tabStop={r.index === tabStop} onFocus={() => setActive(r.index)} onOpen={() => onOpen(r)} />
          </Fragment>
        );
      })}
    </ol>
  );
}

interface ResultRowProps {
  result: RunResultDTO;
  /** The run has several iterations: the row names its own. */
  multi: boolean;
  tabStop: boolean;
  onFocus: () => void;
  onOpen: () => void;
}

function ResultRow({ result: r, multi, tabStop, onFocus, onOpen }: ResultRowProps) {
  const t = useT();
  const badge = runBadge(r);
  const total = r.testsPassed + r.testsFailed;
  const failedTests = (r.tests ?? []).filter((x) => x.outcome !== "passed");
  // A value the next requests need was not set (TEST-05): it fails the result too.
  const failedExtractions = (r.extractions ?? []).filter(extractionFailed);
  const path = (r.path ?? []).join(" / ");
  const outcome = t(OUTCOME_TEXT[r.outcome] ?? "runner.outcome.error");
  return (
    <li className={styles.row} data-outcome={r.outcome}>
      <div className={styles.line}>
        <span className={styles.outcome} data-outcome={r.outcome}>
          {outcome}
        </span>
        <span className={styles.badge} data-tone={badge.tone}>
          {badge.label}
        </span>
        <span className={styles.name}>
          <button
            type="button"
            className={styles.open}
            data-result={r.index}
            tabIndex={tabStop ? 0 : -1}
            title={t("runner.result.openTitle")}
            onFocus={onFocus}
            onClick={onOpen}
          >
            {r.name}
            {path !== "" && (
              <>
                {" "}
                <span className={styles.path}>{path}</span>
              </>
            )}
            {/* The iteration, for a screen reader: the headings are for the eye. */}
            {multi && <span className={styles.srOnly}>{` ${t("runner.iteration", { n: r.iteration })}`}</span>}
          </button>
        </span>
        {r.outcome !== "skipped" && r.error === null && (
          <>
            <span className={styles.status} data-tone={statusTone(r.statusCode)}>
              {r.status !== "" ? r.status : String(r.statusCode)}
            </span>
            <span className={styles.duration}>{formatDurationMs(r.durationMs)}</span>
            <span className={styles.tests} data-tone={r.testsFailed > 0 ? "bad" : total > 0 ? "ok" : undefined}>
              {r.kind === "sse"
                ? t("runner.events", { count: r.events })
                : total === 0
                  ? t("runner.tests.none")
                  : r.testsFailed > 0
                    ? t("runner.tests.failed", { failed: r.testsFailed, total })
                    : t("runner.tests.passed", { count: r.testsPassed })}
            </span>
          </>
        )}
        {r.error !== null && <span className={styles.duration}>{formatDurationMs(r.durationMs)}</span>}
      </div>
      {r.outcome === "skipped" && <p className={styles.note}>{t(SKIP_TEXT[r.skip] ?? "runner.skip.kind")}</p>}
      {r.error !== null && <p className={styles.errorText}>{toAppError(r.error).message}</p>}
      {failedTests.map((x) => (
        <p key={x.index} className={styles.errorText}>
          {x.message}
        </p>
      ))}
      {failedExtractions.map((x) => (
        <p key={`extract-${x.index}`} className={styles.errorText}>
          {t("runner.extractionFailed", { variable: x.variable, message: x.message })}
        </p>
      ))}
    </li>
  );
}
