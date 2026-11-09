import { headerAssertions, type QuickAssertion } from "../../commands/quickAssert";
import { useT } from "../../i18n/react";
import type { HeaderDTO } from "../../lib/http";
import { useQuickAssertMenu } from "./QuickAssertMenu";
import styles from "./ResponseView.module.css";

/**
 * The response's Headers tab (HTTP-32): one row per header, in the order
 * Go sorted them (by name, repeated names in the order received). Values
 * wrap instead of being cut, so long ones (CSP, links) stay readable and
 * selectable.
 */
export function ResponseHeaders({
  headers,
  subject = "response",
  label,
  empty,
  onQuickAssert,
}: {
  headers: readonly HeaderDTO[];
  /** Whose headers they are, for the labels (a received request's for MOCK-08). */
  subject?: "response" | "request";
  /** The table's label; by default "Response headers" or "Request headers". */
  label?: string;
  /** What shows when there are none; by default "The response has no headers." (or request, or the label in lower case). */
  empty?: string;
  /** Adds an assertion to the request (TEST-04): a row's context menu offers "Assert header … equals this value". */
  onQuickAssert?: (a: QuickAssertion) => void;
}) {
  const t = useT();
  const quick = useQuickAssertMenu(onQuickAssert);
  if (headers.length === 0) {
    let text = empty;
    if (text === undefined && label === undefined) text = subject === "request" ? t("response.headers.emptyRequest") : t("response.headers.emptyResponse");
    if (text === undefined && label !== undefined) {
      const what = label.toLowerCase();
      text = subject === "request" ? t("response.headers.emptyRequestOf", { what }) : t("response.headers.emptyResponseOf", { what });
    }
    return <p className={styles.empty}>{text}</p>;
  }
  return (
    <div className={styles.headersBox}>
      <table
        className={styles.headers}
        aria-label={label ?? (subject === "request" ? t("response.headers.requestLabel") : t("response.headers.responseLabel"))}
      >
        <thead className={styles.srOnly}>
          <tr>
            <th scope="col">{t("response.headers.column.name")}</th>
            <th scope="col">{t("response.headers.column.value")}</th>
          </tr>
        </thead>
        <tbody>
          {headers.map((h, i) => (
            <tr
              key={i}
              tabIndex={quick.enabled ? 0 : undefined}
              title={quick.enabled ? t("response.quickAssert.hint") : undefined}
              onContextMenu={(e) => {
                const a = headerAssertions(h.name, h.value);
                quick.open(e, [
                  { id: "equals", label: t("response.quickAssert.headerEquals", { name: h.name }), assertion: a.equals },
                  { id: "exists", label: t("response.quickAssert.headerExists", { name: h.name }), assertion: a.exists },
                ]);
              }}
            >
              <th scope="row" className={styles.headerName}>
                {h.name}
              </th>
              <td className={styles.headerValue}>{h.value}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {quick.element}
    </div>
  );
}
