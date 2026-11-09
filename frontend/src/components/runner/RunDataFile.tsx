import { useId } from "react";
import type { FolderRun, FolderRunState } from "../../commands/folderRuns";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { DATA_DELIMITERS, type DataDelimiter } from "../../lib/runner";
import { IconWarning } from "../common/icons";
import styles from "./FolderRunPanel.module.css";

/** The label of each delimiter choice. */
const DELIMITER_TEXT: Record<DataDelimiter, MessageKey> = {
  "": "runner.data.delimiter.detect",
  ",": "runner.data.delimiter.comma",
  ";": "runner.data.delimiter.semicolon",
  "\t": "runner.data.delimiter.tab",
  "|": "runner.data.delimiter.pipe",
};

/** The delimiters in the select, by option value (a tab cannot be an attribute value readably). */
const DELIMITER_VALUE: Record<DataDelimiter, string> = { "": "detect", ",": "comma", ";": "semicolon", "\t": "tab", "|": "pipe" };

function delimiterOf(value: string): DataDelimiter {
  return DATA_DELIMITERS.find((d) => DELIMITER_VALUE[d] === value) ?? "";
}

export interface RunDataFileProps {
  run: FolderRun;
  state: FolderRunState;
  /** A run is running: nothing can change. */
  locked: boolean;
}

/**
 * The data file of a folder's runs (RUN-05): Choose data file… opens the
 * native dialog; once chosen, its name, format, rows and columns, the CSV
 * delimiter (detected unless chosen), Choose another… and Remove, and a
 * table of its first rows. Why it cannot be read shows under it. Kept for
 * the session, like the selection of requests.
 */
export function RunDataFile({ run, state, locked }: RunDataFileProps) {
  const t = useT();
  const headingId = useId();
  const hintId = useId();
  const { data, dataError, dataBusy } = state;
  const preview = data?.preview ?? null;
  const columns = preview?.columns ?? [];
  const rows = preview?.rows ?? [];

  return (
    <section className={styles.data} aria-labelledby={headingId}>
      <div className={styles.requestsHead}>
        <h3 id={headingId} className={styles.requestsTitle}>
          {t("runner.data.title")}
        </h3>
        {data !== null && (
          <span className={styles.requestsCount}>
            {preview === null
              ? data.name
              : t("runner.data.summary", {
                  name: data.name,
                  format: t(preview.format === "json" ? "runner.data.format.json" : "runner.data.format.csv"),
                  rows: t("runner.data.rows", { count: preview.rowCount }),
                  columns: t("runner.data.columns", { count: columns.length }),
                })}
          </span>
        )}
        <div className={styles.requestsTools}>
          {data !== null && (data.preview === null || data.preview.format === "csv") && (
            <label className={styles.field}>
              <span className={styles.label}>{t("runner.data.delimiter")}</span>
              <select
                className={styles.select}
                value={DELIMITER_VALUE[data.delimiter]}
                disabled={locked || dataBusy}
                onChange={(e) => run.setDataDelimiter(delimiterOf(e.target.value))}
              >
                {DATA_DELIMITERS.map((d) => (
                  <option key={DELIMITER_VALUE[d]} value={DELIMITER_VALUE[d]}>
                    {t(DELIMITER_TEXT[d])}
                  </option>
                ))}
              </select>
            </label>
          )}
          <button type="button" className={styles.tool} disabled={locked || dataBusy} aria-describedby={hintId} onClick={() => run.chooseDataFile()}>
            {data === null ? t("runner.data.choose") : t("runner.data.chooseOther")}
          </button>
          {data !== null && (
            <button type="button" className={styles.tool} disabled={locked} onClick={() => run.clearDataFile()}>
              {t("runner.data.remove")}
            </button>
          )}
        </div>
      </div>
      <p id={hintId} className={styles.hint}>
        {t("runner.data.hint")}
      </p>
      {dataBusy && <p className={styles.hint}>{t("runner.data.reading")}</p>}
      {dataError !== null && (
        <p className={styles.runError} role="alert">
          <IconWarning size={13} />
          {dataError.message}
        </p>
      )}
      {preview !== null && rows.length > 0 && (
        <div className={styles.dataPreview} role="region" aria-label={t("runner.data.previewLabel")} tabIndex={0}>
          <table className={styles.dataTable}>
            <caption className={styles.dataCaption}>{t("runner.data.preview", { count: preview.rowCount, shown: rows.length })}</caption>
            <thead>
              <tr>
                {columns.map((c) => (
                  <th key={c} scope="col">
                    {c}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row, i) => (
                <tr key={i}>
                  {columns.map((c, j) => (
                    <td key={c}>{row?.[j] ?? ""}</td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
