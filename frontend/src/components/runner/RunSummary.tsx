import { useId } from "react";
import type { FolderRunState } from "../../commands/folderRuns";
import { formatNumber, t as translate, type MessageKey } from "../../i18n";
import { extractionFailed } from "../../lib/extractions";
import { useT } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import { formatDurationMs } from "../../lib/http";
import type { RunResultDTO } from "../../lib/runner";
import styles from "./FolderRunPanel.module.css";
import { OUTCOME_TEXT } from "./runBadge";
import { failedResults } from "./runProgress";

/** How many failures the summary lists; the results list has them all. */
export const MAX_LISTED_FAILURES = 20;

export interface RunSummaryProps {
  state: FolderRunState;
  /** Opens a result's request and response. */
  onOpen: (result: RunResultDTO) => void;
}

/** Why a result failed, in a line: its first failed assertion's message, its first failed extraction's (TEST-05), or its error. */
function failureText(r: RunResultDTO): string {
  if (r.error !== null) return toAppError(r.error).message;
  const test = (r.tests ?? []).find((x) => x.outcome !== "passed");
  if (test !== undefined) return test.message;
  const extraction = (r.extractions ?? []).find(extractionFailed);
  return extraction !== undefined ? translate("runner.extractionFailed", { variable: extraction.variable, message: extraction.message }) : "";
}

/**
 * The summary of a run that ended (RUN-07): the totals per outcome, the
 * run's time, the time of the requests (total, average, fastest, slowest,
 * the slowest one opening its result), and the failures: each failed or
 * errored result listed opens its request and response.
 */
export function RunSummary({ state, onOpen }: RunSummaryProps) {
  const t = useT();
  const headingId = useId();
  const { counts, timing, results } = state;
  const failures = failedResults(state);
  const failedCount = counts.failed + counts.error;
  const listed = failures.slice(0, MAX_LISTED_FAILURES);
  const slowest = timing.slowest === null ? undefined : results.find((r) => r.index === timing.slowest?.index);
  const multi = state.options.iterations > 1;
  const nameOf = (r: { name: string; iteration: number }) => (multi ? t("runner.summary.nameInIteration", { name: r.name, n: r.iteration }) : r.name);

  const facts: { key: string; label: MessageKey; value: string }[] = [
    { key: "results", label: "runner.summary.results", value: formatNumber(state.done) },
    { key: "passed", label: "runner.outcome.passed", value: formatNumber(counts.passed) },
    { key: "failed", label: "runner.outcome.failed", value: formatNumber(counts.failed) },
    { key: "error", label: "runner.summary.errors", value: formatNumber(counts.error) },
    { key: "skipped", label: "runner.outcome.skipped", value: formatNumber(counts.skipped) },
  ];
  if (state.elapsedMs !== null) facts.push({ key: "run", label: "runner.summary.runTime", value: formatDurationMs(state.elapsedMs) });
  if (timing.sent > 0) {
    facts.push(
      { key: "requests", label: "runner.summary.requestTime", value: formatDurationMs(timing.totalMs) },
      { key: "average", label: "runner.summary.average", value: formatDurationMs(Math.round(timing.totalMs / timing.sent)) },
      { key: "fastest", label: "runner.summary.fastest", value: formatDurationMs(timing.minMs) },
    );
  }

  return (
    <section className={styles.report} aria-labelledby={headingId}>
      <h3 id={headingId} className={styles.requestsTitle}>
        {t("runner.summary.title")}
      </h3>
      <dl className={styles.facts}>
        {facts.map((f) => (
          <div key={f.key} className={styles.fact} data-fact={f.key}>
            <dt>{t(f.label)}</dt>
            <dd>{f.value}</dd>
          </div>
        ))}
        {timing.sent > 0 && timing.slowest !== null && (
          <div className={styles.fact} data-fact="slowest">
            <dt>{t("runner.summary.slowest")}</dt>
            <dd>
              {slowest !== undefined ? (
                <button type="button" className={styles.open} onClick={() => onOpen(slowest)}>
                  {t("runner.summary.slowestValue", { name: nameOf(timing.slowest), time: formatDurationMs(timing.maxMs) })}
                </button>
              ) : (
                t("runner.summary.slowestValue", { name: nameOf(timing.slowest), time: formatDurationMs(timing.maxMs) })
              )}
            </dd>
          </div>
        )}
      </dl>
      {failedCount === 0 ? (
        <p className={styles.hint}>{t("runner.summary.noFailures")}</p>
      ) : (
        <>
          <h4 className={styles.failuresTitle}>{t("runner.summary.failures", { count: failedCount })}</h4>
          <ul className={styles.failures}>
            {listed.map((r) => (
              <li key={r.index} className={styles.failure}>
                <span className={styles.outcome} data-outcome={r.outcome}>
                  {t(OUTCOME_TEXT[r.outcome] ?? "runner.outcome.error")}
                </span>
                <button type="button" className={styles.open} onClick={() => onOpen(r)}>
                  {nameOf(r)}
                </button>
                <span className={styles.failureText}>{failureText(r)}</span>
              </li>
            ))}
          </ul>
          {failedCount > listed.length && <p className={styles.hint}>{t("runner.summary.moreFailures", { count: failedCount - listed.length })}</p>}
        </>
      )}
    </section>
  );
}
