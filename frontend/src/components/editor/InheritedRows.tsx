import { inheritedRows, useInheritedSettings, type InheritedFolders } from "../../commands/folderSettings";
import type { KeyValueDTO } from "../../lib/collection";
import { useT } from "../../i18n/react";
import type { RequestId } from "../../stores";
import styles from "./InheritedRows.module.css";

export interface InheritedRowsProps {
  /** What the item inherits (useInheritedSettings). */
  folders: InheritedFolders;
  kind: "headers" | "variables";
  /** The item's own rows of that kind: an enabled row of the same name overrides. */
  own: readonly KeyValueDTO[] | null;
}


/** What a secret variable's value shows as. */
const SECRET_MASK = "••••••";

/**
 * The headers or variables the folders above an item pass on (COL-03),
 * read-only above its own table: name, value and the folder they come
 * from. A row a nearer folder or the item itself overrides stays, struck
 * through, so what it would have been is still visible. Nothing is shown
 * when nothing is inherited.
 */
export function InheritedRows({ folders, kind, own }: InheritedRowsProps) {
  const t = useT();
  const rows = inheritedRows(folders, kind, own);
  if (rows.length === 0) return null;
  const headers = kind === "headers";
  return (
    <ul className={styles.inherited} aria-label={headers ? t("editor.inherited.headers") : t("editor.inherited.variables")}>
      {rows.map((row, i) => {
        const folder = row.folderName;
        let title = row.overridden
          ? headers
            ? t("editor.inherited.overridden.header", { folder })
            : t("editor.inherited.overridden.variable", { folder })
          : headers
            ? t("editor.inherited.from.header", { folder })
            : t("editor.inherited.from.variable", { folder });
        if (row.secret) title = t("editor.inherited.secret", { title });
        // A secret's value is not sent here; it is shown masked.
        const value = row.secret ? SECRET_MASK : row.value;
        return (
          <li
            key={`${row.folderId}-${i}`}
            className={styles.row}
            data-overridden={row.overridden || undefined}
            data-secret={row.secret || undefined}
            title={title}
          >
            <span className={styles.name}>{row.name}</span>
            <span className={styles.value}>{row.overridden ? <s>{value}</s> : value}</span>
            <span className={styles.from}>{row.overridden ? t("editor.inherited.overridden") : row.folderName}</span>
            <span className={styles.srOnly}>{title}</span>
          </li>
        );
      })}
    </ul>
  );
}

/**
 * The headers request `id` inherits from its folders, above its Headers
 * table (the HTTP and GraphQL editors). Loads them when mounted, so only
 * while the Headers tab shows.
 */
export function InheritedHeaders({ id, own }: { id: RequestId; own: readonly KeyValueDTO[] | null }) {
  const folders = useInheritedSettings(id);
  return <InheritedRows folders={folders} kind="headers" own={own} />;
}
