import { useState } from "react";
import { shortcutLabel } from "../../lib/shortcuts";
import { useT } from "../../i18n/react";
import { createKeyValueRow, KeyValueTable, type KeyValueRow } from "../common/KeyValueTable";
import styles from "./KeyValuePreview.module.css";

// Skeleton preview of the key/value table editor: query params and
// headers with sample data. Mounted into the app shell until the real request
// pane exists.

// Sample rows, not UI text.
// i18n-ignore-start
const PARAMS: KeyValueRow[] = [
  createKeyValueRow({ key: "limit", value: "20", description: "Page size" }),
  createKeyValueRow({ key: "category", value: "audio" }),
  createKeyValueRow({ key: "cursor", value: "", enabled: false, description: "Cursor from previous page" }),
];

const HEADERS: KeyValueRow[] = [
  createKeyValueRow({ key: "Accept", value: "application/json" }),
  createKeyValueRow({ key: "Authorization", value: "Bearer {{token}}" }),
  createKeyValueRow({ key: "X-Request-Id", value: "{{requestId}}", enabled: false, description: "Trace id" }),
];
// i18n-ignore-end

/** Shows {{variables}} in the variable colour, through the renderValue hook. */
function highlightVariables(value: string) {
  return value.split(/(\{\{[^{}]*\}\})/).map((part, i) =>
    i % 2 === 1 ? (
      <span key={i} className={styles.variable}>
        {part}
      </span>
    ) : (
      part
    ),
  );
}

const countEnabled = (rows: readonly KeyValueRow[]) => rows.filter((r) => r.enabled && r.key !== "").length;

export function KeyValuePreview() {
  const t = useT();
  const [params, setParams] = useState(PARAMS);
  const [headers, setHeaders] = useState(HEADERS);

  return (
    <section className={styles.preview} aria-label={t("editor.preview.table")}>
      <div>
        <h2 className={styles.caption}>{t("editor.preview.paramsCount", { count: countEnabled(params) })}</h2>
        <KeyValueTable label={t("editor.request.queryParams")} rows={params} onChange={setParams} />
      </div>
      <div>
        <h2 className={styles.caption}>{t("editor.preview.headersCount", { count: countEnabled(headers) })}</h2>
        <KeyValueTable
          label={t("editor.request.headers")}
          rows={headers}
          onChange={setHeaders}
          placeholders={{ key: t("editor.request.placeholder.header") }}
          renderValue={highlightVariables}
        />
      </div>
      <p className={styles.hints}>
        {t("editor.preview.tableHints", {
          insert: shortcutLabel("table.insertRow"),
          delete: shortcutLabel("table.deleteRow"),
        })}
      </p>
    </section>
  );
}
