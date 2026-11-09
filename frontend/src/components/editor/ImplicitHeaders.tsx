import type { BodyDTO, KeyValueDTO } from "../../lib/collection";
import { implicitContentType } from "../../lib/contentType";
import { useT } from "../../i18n/react";
import styles from "./ImplicitHeaders.module.css";

export interface ImplicitHeadersProps {
  body: BodyDTO;
  headers: readonly KeyValueDTO[] | null;
}

/**
 * The headers PostSonde adds by itself, shown read-only above the Headers
 * table (HTTP-16): for now the auto Content-Type of the body mode
 * (lib/contentType.ts, a mirror of Go's rule). Nothing is shown when the
 * body implies no Content-Type. When an enabled Content-Type row overrides
 * it, the row stays, struck through and marked "overridden", so the value
 * the body would have set is still visible.
 */
export function ImplicitHeaders({ body, headers }: ImplicitHeadersProps) {
  const t = useT();
  const { value, overridden } = implicitContentType(body, headers);
  if (value === "") return null;
  const shown = body.mode === "multipart" ? `${value}; boundary=…` : value;
  const title = overridden ? t("editor.implicitHeaders.overridden") : t("editor.implicitHeaders.auto");
  return (
    <ul className={styles.implicit} aria-label={t("editor.autoHeaders.label")}>
      <li className={styles.row} data-overridden={overridden || undefined} title={title}>
        <span className={styles.badge}>{overridden ? t("editor.autoHeaders.badge.overridden") : t("editor.autoHeaders.badge.auto")}</span>
        <span className={styles.name}>Content-Type</span>
        <span className={styles.value}>
          {overridden ? <s>{shown}</s> : shown}
        </span>
        <span className={styles.srOnly}>{title}</span>
      </li>
    </ul>
  );
}
