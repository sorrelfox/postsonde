import { useEffect, useId, useState } from "react";
import { formatDate } from "../../i18n";
import { useT } from "../../i18n/react";
import { selectSaveSummary, shallowEqual, useAutosave, useStores } from "../../stores";
import styles from "./StatusBar.module.css";

function formatTime(iso: string): string {
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : formatDate(date, { hour: "numeric", minute: "2-digit", second: "2-digit" });
}

/**
 * The auto-save state in the status bar: "Ready" before anything was
 * edited, "Saving…" while edits are on their way to disk, "Auto-saved"
 * (time on hover) once they are, and "Save failed" with the details a
 * click away.
 */
export function AutoSaveStatus() {
  const t = useT();
  const stores = useStores();
  const { status, pendingCount, failedCount, lastSavedAt, failure } = useAutosave(selectSaveSummary, shallowEqual);
  const [open, setOpen] = useState(false);
  const detailsId = useId();

  useEffect(() => {
    if (status !== "error") setOpen(false);
  }, [status]);

  if (status === "error" && failure !== null) {
    const label = t("shell.autosave.failed", { count: failedCount });
    const details = failure.hint === "" ? failure.message : `${failure.message} ${failure.hint}`;
    return (
      <span className={styles.item} role="status">
        <span className={`${styles.dot} ${styles.dotBad}`} />
        <button
          type="button"
          className={styles.statusButton}
          aria-expanded={open}
          aria-controls={open ? detailsId : undefined}
          title={details}
          onClick={() => setOpen((o) => !o)}
        >
          {label}
        </button>
        {open && (
          <>
            <span id={detailsId} className={styles.details}>
              {details}
            </span>
            <button
              type="button"
              className={styles.statusButton}
              onClick={() => stores.autosave.dispatch({ type: "autosave/dismiss" })}
            >
              {t("shell.autosave.dismiss")}
            </button>
          </>
        )}
      </span>
    );
  }

  if (status === "saving") {
    return (
      <span className={styles.item} role="status" title={t("shell.autosave.unsaved", { count: pendingCount })}>
        <span className={`${styles.dot} ${styles.dotBusy}`} />
        {t("shell.autosave.saving")}
      </span>
    );
  }

  if (status === "saved" && lastSavedAt !== null) {
    return (
      <span className={styles.item} role="status" title={t("shell.autosave.savedAt", { time: formatTime(lastSavedAt) })}>
        <span className={styles.dot} />
        {t("shell.autosave.saved")}
      </span>
    );
  }

  return (
    <span className={styles.item} role="status">
      <span className={styles.dot} />
      {t("shell.statusBar.ready")}
    </span>
  );
}
