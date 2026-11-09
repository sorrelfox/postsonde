import { useEffect, useId, useRef, useState } from "react";
import { saveRunReport } from "../../commands/runHistory";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { toAppError, type AppError } from "../../lib/errors";
import { RUN_REPORT_FORMATS, type RunReportFormat } from "../../lib/runner";
import { IconWarning } from "../common/icons";
import styles from "./FolderRunPanel.module.css";

const FORMAT_TEXT: Record<RunReportFormat, MessageKey> = {
  junit: "runner.report.junit",
  json: "runner.report.json",
  html: "runner.report.html",
};

export interface RunReportActionsProps {
  /** The run's ID: its operation ID, as the run history keeps it. */
  runId: string;
}

/**
 * Save report: JUnit XML, JSON or HTML (RUN-08), each asking where in the
 * native save dialog; Go masks the secrets. Where it was saved, or why it
 * could not be, shows next to the buttons.
 */
export function RunReportActions({ runId }: RunReportActionsProps) {
  const t = useT();
  const labelId = useId();
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState<string | null>(null);
  const [error, setError] = useState<AppError | null>(null);
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    setSaved(null);
    setError(null);
    return () => {
      live.current = false;
    };
  }, [runId]);

  const save = (format: RunReportFormat) => {
    setBusy(true);
    setSaved(null);
    setError(null);
    saveRunReport(runId, format).then(
      (res) => {
        if (!live.current) return;
        setBusy(false);
        if (res.saved) setSaved(res.path);
      },
      (err: unknown) => {
        if (!live.current) return;
        setBusy(false);
        setError(toAppError(err));
      },
    );
  };

  return (
    <div className={styles.reportActions}>
      <div className={styles.reportTools} role="group" aria-labelledby={labelId}>
        <span id={labelId} className={styles.label}>
          {t("runner.report.label")}
        </span>
        {RUN_REPORT_FORMATS.map((f) => (
          <button key={f} type="button" className={styles.tool} disabled={busy} onClick={() => save(f)}>
            {t(FORMAT_TEXT[f])}
          </button>
        ))}
      </div>
      <p className={styles.hint} role="status">
        {busy ? t("runner.report.saving") : saved !== null ? t("runner.report.saved", { path: saved }) : ""}
      </p>
      {error !== null && (
        <p className={styles.runError} role="alert">
          <IconWarning size={13} />
          {error.message}
        </p>
      )}
    </div>
  );
}
