import { useId, useState } from "react";
import { openPastRun, removePastRun, useRunHistory, type PastRun } from "../../commands/runHistory";
import { formatDate, t as translate, type MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { toAppError, type AppError } from "../../lib/errors";
import { formatDurationMs } from "../../lib/http";
import type { RunSummaryDTO } from "../../lib/runner";
import { IconWarning } from "../common/icons";
import styles from "./FolderRunPanel.module.css";

const STATUS_TEXT: Record<string, MessageKey> = {
  ok: "runner.history.status.ok",
  stopped: "runner.history.status.stopped",
  cancelled: "runner.history.status.cancelled",
};

const WHEN: Intl.DateTimeFormatOptions = { dateStyle: "medium", timeStyle: "short" };

/** When run `s` started, in the locale's style. */
export function pastRunWhen(s: RunSummaryDTO): string {
  return formatDate(new Date(s.startedAt), WHEN);
}

/** "4 passed · 2 failed · 1 error": the counts of a past run, those that are not 0 (passed always). */
export function pastRunCounts(s: RunSummaryDTO): string {
  const parts = [translate("runner.count.passed", { count: s.passed })];
  if (s.failed > 0) parts.push(translate("runner.count.failed", { count: s.failed }));
  if (s.errors > 0) parts.push(translate("runner.count.error", { count: s.errors }));
  if (s.skipped > 0) parts.push(translate("runner.count.skipped", { count: s.skipped }));
  return parts.join(" · ");
}

export interface RunHistoryProps {
  folderId: string;
  /** Changes when a run of the folder ended: the list loads again. */
  reloadKey: string;
  /** The past run shown, if any. */
  openId: string | null;
  /** A past run was opened; `opener` is the button that opened it. */
  onOpened: (run: PastRun, opener: HTMLElement) => void;
  /** Past run `id` was deleted. */
  onDeleted: (id: string) => void;
}

/**
 * Past runs (RUN-09): the folder's runs kept in the project's run history,
 * newest first, each with when it ran, how it ended, its counts and time
 * (and its data file), **Open** (shown in the panel in place of the latest
 * run) and **Delete**.
 */
export function RunHistory({ folderId, reloadKey, openId, onOpened, onDeleted }: RunHistoryProps) {
  const t = useT();
  const headingId = useId();
  const { runs, error: loadError, reload } = useRunHistory(folderId, reloadKey);
  const [error, setError] = useState<AppError | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const shown = error ?? loadError;

  const open = (id: string, opener: HTMLElement) => {
    setBusy(id);
    setError(null);
    openPastRun(id).then(
      (run) => {
        setBusy(null);
        onOpened(run, opener);
      },
      (err: unknown) => {
        setBusy(null);
        setError(toAppError(err));
        reload();
      },
    );
  };

  const remove = (id: string) => {
    setBusy(id);
    setError(null);
    removePastRun(id).then(
      () => {
        setBusy(null);
        onDeleted(id);
        reload();
      },
      (err: unknown) => {
        setBusy(null);
        setError(toAppError(err));
      },
    );
  };

  return (
    <section className={styles.data} aria-labelledby={headingId}>
      <div className={styles.requestsHead}>
        <h3 id={headingId} className={styles.requestsTitle}>
          {t("runner.history.title")}
        </h3>
        {runs !== null && runs.length > 0 && <span className={styles.requestsCount}>{t("runner.history.count", { count: runs.length })}</span>}
      </div>
      <p className={styles.hint}>{t("runner.history.hint")}</p>
      {shown !== null && (
        <p className={styles.runError} role="alert">
          <IconWarning size={13} />
          {shown.message}
        </p>
      )}
      {runs === null && loadError === null && <p className={styles.hint}>{t("runner.history.loading")}</p>}
      {runs !== null && runs.length === 0 && <p className={styles.hint}>{t("runner.history.empty")}</p>}
      {runs !== null && runs.length > 0 && (
        <ul className={styles.history} aria-labelledby={headingId}>
          {runs.map((r) => {
            const when = pastRunWhen(r);
            return (
              <li key={r.id} className={styles.historyRow} aria-current={r.id === openId ? "true" : undefined}>
                <span className={styles.historyWhen}>{when}</span>
                <span className={styles.outcome} data-outcome={r.status === "ok" && r.failed + r.errors === 0 ? "passed" : r.status === "cancelled" ? "skipped" : "failed"}>
                  {t(STATUS_TEXT[r.status] ?? "runner.history.status.ok")}
                </span>
                <span className={styles.counts}>{pastRunCounts(r)}</span>
                <span className={styles.duration}>{formatDurationMs(r.elapsedMs)}</span>
                {r.dataFile !== "" && (
                  <span className={styles.path}>{t("runner.history.data", { name: r.dataFile, rows: t("runner.data.rows", { count: r.dataRows }) })}</span>
                )}
                <span className={styles.historyTools}>
                  <button
                    type="button"
                    className={styles.tool}
                    disabled={busy !== null}
                    aria-label={t("runner.history.open", { when })}
                    onClick={(e) => open(r.id, e.currentTarget)}
                  >
                    {t("runner.history.openButton")}
                  </button>
                  <button
                    type="button"
                    className={styles.tool}
                    disabled={busy !== null}
                    aria-label={t("runner.history.delete", { when })}
                    onClick={() => remove(r.id)}
                  >
                    {t("runner.history.deleteButton")}
                  </button>
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
