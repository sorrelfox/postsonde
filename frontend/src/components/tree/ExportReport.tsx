import { useT } from "../../i18n/react";
import { groupNotes, type ExportNoteDTO } from "../../lib/postmanExport";
import { severityLabel } from "./badges";
import styles from "./ExportReport.module.css";

export interface ExportReportProps {
  notes: readonly ExportNoteDTO[];
  /** Names the collection itself in the list. */
  collectionName: string;
}

/**
 * The report of a Postman export (EXP-03) or a zip export (EXP-02): per
 * item, what was left out or exported with a change. Shown in the report
 * dialog the export's toast opens.
 */
export function ExportReport({ notes, collectionName }: ExportReportProps) {
  const t = useT();
  return (
    <ul className={styles.report} aria-label={t("tree.export.report")}>
      {groupNotes(notes).map((group, i) => (
        <li key={i} className={styles.item}>
          <span className={styles.path}>
            {group.path === "" ? collectionName : group.path}
            {group.notes.some((n) => n.skipped) && <span className={styles.skipped}> {t("tree.export.notExported")}</span>}
          </span>
          <ul className={styles.notes}>
            {group.notes.map((n, j) => (
              <li key={j} className={styles.note} data-severity={n.severity}>
                <span className={styles.severity}>{severityLabel(n.severity)}</span>
                <span className={styles.message}>{n.message}</span>
              </li>
            ))}
          </ul>
        </li>
      ))}
    </ul>
  );
}
