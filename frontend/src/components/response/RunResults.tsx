import type { ReactNode } from "react";
import { cancelRequest, type HttpRunState, type HttpSendState } from "../../commands/httpSends";
import { REPEAT_WAIT_STAGE, formatDurationMs, statusTone, type RepeatResultDTO } from "../../lib/http";
import type { RequestId } from "../../stores";
import { formatDate, t } from "../../i18n";
import { useT } from "../../i18n/react";
import { IconWarning } from "../common/icons";
import { formatSize } from "../messagelog/format";
import styles from "./RunResults.module.css";

export interface RunResultsProps {
  id: RequestId;
  state: HttpSendState;
  run: HttpRunState;
  /** The latest response the run carried, in its view; null before one arrived. */
  response: ReactNode;
}

/** "Sending 3 of 10", "Run stopped after 7 sends", …: the run's state in a few words. */
export function runHeadline(state: HttpSendState, run: HttpRunState): string {
  const { count } = run.options;
  switch (state.status) {
    case "starting":
    case "running": {
      const current = run.sent + 1;
      if (state.stage === REPEAT_WAIT_STAGE) {
        return count > 0 ? t("response.run.waitingOf", { current, count }) : t("response.run.waiting", { current });
      }
      return count > 0 ? t("response.run.sendingOf", { current, count }) : t("response.run.sending", { current });
    }
    case "cancelled":
      return t("response.run.stopped", { count: run.sent });
    case "error":
      return t("response.run.ended", { count: run.sent });
    default:
      return t("response.run.finished", { count: run.sent });
  }
}

/** "9 responses · 1 failed · avg 120 ms (80 ms to 300 ms)". */
export function runStats(run: HttpRunState): string {
  const parts = [t("response.run.responses", { count: run.responses })];
  if (run.problems > 0) parts.push(t("response.run.problems", { n: run.problems }));
  if (run.failed > 0) parts.push(t("response.run.failed", { n: run.failed }));
  if (run.responses > 0) {
    const avg = Math.round(run.totalMs / run.responses);
    parts.push(t("response.run.average", { avg: formatDurationMs(avg), min: formatDurationMs(run.minMs), max: formatDurationMs(run.maxMs) }));
  }
  return parts.join(" · ");
}

/**
 * A run's pane (HTTP-27): what it is doing with Stop, the counts and
 * times, the list of results (newest first: status, time, size, tests,
 * or the error of a send that failed), and below it the latest response
 * the run carried.
 */
export function RunResults({ id, state, run, response }: RunResultsProps) {
  const t = useT();
  const running = state.status === "starting" || state.status === "running";
  const rows = [...run.results].reverse();
  const hidden = run.sent - run.results.length;
  return (
    <div className={styles.run}>
      <div className={styles.header}>
        <div className={styles.summary}>
          <p className={styles.headline}>
            {running && <span className={styles.spinner} aria-hidden="true" />}
            {runHeadline(state, run)}
          </p>
          <p className={styles.stats}>{runStats(run)}</p>
          {/* Announced once, when the run ends: not on every send. */}
          <span className={styles.srOnly} role="status">
            {running ? "" : t("response.run.announce", { headline: runHeadline(state, run), stats: runStats(run) })}
          </span>
        </div>
        {running && (
          <button type="button" className={styles.stop} onClick={() => cancelRequest(id)}>
            {t("response.run.stop")}
          </button>
        )}
      </div>
      {state.status === "error" && state.error !== null && (
        <p className={styles.runError} role="alert">
          <IconWarning size={13} />
          {state.error.message}
        </p>
      )}
      {rows.length > 0 && (
        // A scrollable list: focusable so it scrolls with the keyboard.
        <ol className={styles.results} aria-label={t("response.run.results")} tabIndex={0}>
          {rows.map((r) => (
            <ResultRow key={r.index} result={r} shown={r.index === run.responseIndex && response !== null} />
          ))}
          {hidden > 0 && <li className={styles.more}>{t("response.run.hidden", { count: hidden })}</li>}
        </ol>
      )}
      {response !== null && (
        <div className={styles.response}>
          <p className={styles.responseOf}>{t("response.run.responseOf", { index: run.responseIndex })}</p>
          {response}
        </div>
      )}
    </div>
  );
}

const RESULT_TIME: Intl.DateTimeFormatOptions = { hour: "2-digit", minute: "2-digit", second: "2-digit" };

function ResultRow({ result: r, shown }: { result: RepeatResultDTO; shown: boolean }) {
  const t = useT();
  const time = formatDate(new Date(r.startedAt), RESULT_TIME);
  return (
    <li className={styles.row} data-shown={shown || undefined}>
      <span className={styles.index}>{r.index}</span>
      <span className={styles.time}>{time}</span>
      {r.error !== null ? (
        <>
          <span className={styles.failed}>{t("response.run.rowFailed")}</span>
          <span className={styles.duration}>{formatDurationMs(r.durationMs)}</span>
          <span className={styles.message}>{r.error.message}</span>
        </>
      ) : (
        <>
          <span className={styles.status} data-tone={statusTone(r.statusCode)}>
            {r.status !== "" ? r.status : String(r.statusCode)}
          </span>
          <span className={styles.duration}>{formatDurationMs(r.durationMs)}</span>
          <span className={styles.size}>{formatSize(r.size)}</span>
          {r.testsPassed + r.testsFailed > 0 && (
            <span className={styles.tests} data-tone={r.testsFailed > 0 ? "bad" : "ok"}>
              {r.testsFailed > 0
                ? t("response.run.testsFailed", { failed: r.testsFailed, total: r.testsPassed + r.testsFailed })
                : t("response.run.testsPassed", { count: r.testsPassed })}
            </span>
          )}
        </>
      )}
    </li>
  );
}
