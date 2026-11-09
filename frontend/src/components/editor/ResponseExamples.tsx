import { useState, type ChangeEvent } from "react";
import type { MockSource } from "../../commands/mockFromExample";
import { deleteResponseExample, renameResponseExample, showResponseExample } from "../../commands/responseHistory";
import { useHttpSend } from "../../commands/useHttpSend";
import { useT } from "../../i18n/react";
import { statusTone } from "../../lib/http";
import type { ResponseExampleDTO } from "../../lib/responseHistory";
import { useDrafts, useStores, type RequestId } from "../../stores";
import { formatSize } from "../messagelog/format";
import { formatSentAt } from "../response/ResponseSourceBar";
import { MockFromExampleDialog } from "../servers/MockFromExampleDialog";
import styles from "./ResponseExamples.module.css";

export interface ResponseExamplesProps {
  id: RequestId;
  examples: readonly ResponseExampleDTO[];
  readOnly: boolean;
}

/**
 * The Examples sub-tab of an HTTP or GraphQL request (HTTP-38): the
 * responses saved with it (Save as example in the response pane), one row
 * each with the name as an input, the status, when it was saved and the
 * body size; View shows it in the response pane until the next send,
 * Delete removes it; Mock makes a mock server route that answers with it
 * (MOCK-03, components/servers/MockFromExampleDialog). Renames and deletes are auto-saved edits of the
 * request (commands/responseHistory.ts); the file shape is in
 * docs/file-format.md#response-examples.
 */
export function ResponseExamples({ id, examples, readOnly }: ResponseExamplesProps) {
  const t = useT();
  const stores = useStores();
  const { state: send } = useHttpSend(id);
  // The request's method and URL as typed, for a mock route.
  const typed = useDrafts((s) => s.byId[id]?.request as unknown as { method?: string; url?: string } | undefined);
  const [mockSource, setMockSource] = useState<MockSource | null>(null);

  if (examples.length === 0) {
    return (
      <div className={styles.examples}>
        <p className={styles.empty}>{t("editor.responseExamples.empty")}</p>
      </div>
    );
  }

  const label = (e: ResponseExampleDTO, i: number) => (e.name.trim() === "" ? t("editor.responseExamples.unnamed", { n: i + 1 }) : e.name);
  const size = (e: ResponseExampleDTO) => {
    if (e.bodyClipped) return t("editor.responseExamples.clipped", { size: formatSize(e.bodySize) });
    const bytes = e.bodyEncoding === "" ? new TextEncoder().encode(e.body).length : Math.floor((e.body.length * 3) / 4);
    return formatSize(bytes);
  };

  return (
    <div className={styles.examples}>
      <div className={styles.tableWrap}>
        <table className={styles.table} aria-label={t("editor.responseExamples.label")}>
          <thead>
            <tr>
              <th scope="col">{t("editor.responseExamples.name")}</th>
              <th scope="col">{t("editor.responseExamples.status")}</th>
              <th scope="col">{t("editor.responseExamples.saved")}</th>
              <th scope="col">{t("editor.responseExamples.size")}</th>
              <th scope="col" aria-label={t("editor.responseExamples.actions")} />
            </tr>
          </thead>
          <tbody>
            {examples.map((e, i) => (
              <tr key={i}>
                <td>
                  <input
                    className={styles.name}
                    aria-label={t("editor.responseExamples.nameOf", { n: i + 1 })}
                    placeholder={t("editor.responseExamples.unnamed", { n: i + 1 })}
                    value={e.name}
                    readOnly={readOnly}
                    onChange={readOnly ? undefined : (ev: ChangeEvent<HTMLInputElement>) => renameResponseExample(stores, id, i, ev.target.value)}
                  />
                </td>
                <td className={`${styles.status} ${styles[statusTone(e.statusCode)] ?? ""}`}>
                  {e.statusText === "" ? String(e.statusCode) : `${e.statusCode} ${e.statusText}`}
                </td>
                <td>{e.savedAt === "" ? t("editor.responseExamples.savedUnknown") : formatSentAt(e.savedAt)}</td>
                <td>{size(e)}</td>
                <td>
                  <div className={styles.actions}>
                    <button
                      type="button"
                      className={styles.button}
                      aria-label={t("editor.responseExamples.view.label", { name: label(e, i) })}
                      title={t("editor.responseExamples.view.title")}
                      onClick={() => showResponseExample(stores, id, i, send.startedAt)}
                    >
                      {t("editor.responseExamples.view")}
                    </button>
                    <button
                      type="button"
                      className={styles.button}
                      aria-label={t("editor.responseExamples.mock.label", { name: label(e, i) })}
                      title={t("editor.responseExamples.mock.title")}
                      onClick={() => setMockSource({ kind: "example", example: e })}
                    >
                      {t("editor.responseExamples.mock")}
                    </button>
                    <button
                      type="button"
                      className={styles.button}
                      aria-label={t("editor.responseExamples.delete.label", { name: label(e, i) })}
                      disabled={readOnly}
                      onClick={() => deleteResponseExample(stores, id, i)}
                    >
                      {t("editor.responseExamples.delete")}
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className={styles.hint}>{t("editor.responseExamples.hint")}</p>
      <MockFromExampleDialog source={mockSource} method={typed?.method ?? ""} url={typed?.url ?? ""} onClose={() => setMockSource(null)} />
    </div>
  );
}
