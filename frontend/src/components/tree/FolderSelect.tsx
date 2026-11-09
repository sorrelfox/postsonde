import type { ChangeEvent } from "react";
import { useT } from "../../i18n/react";
import type { NodeId } from "../../stores";
import { IconChevron, IconFolder } from "../common/icons";
import { TOP_LEVEL, type FolderOption } from "./newItem";
import styles from "./NewItemDialog.module.css";

export interface FolderSelectProps {
  /** The select's id, for its label. */
  id: string;
  /** From folderOptions: the top level first, then every folder depth first. */
  options: readonly FolderOption[];
  /** The chosen folder's id, or TOP_LEVEL. */
  value: NodeId;
  onChange: (id: NodeId) => void;
  disabled?: boolean;
  describedBy?: string;
}

/**
 * The folder picker of the New folder, New request and Move to folder
 * dialogs (COL-04): a native `<select>`, invisible, over a field drawn as
 * in the design (folder icon, name, "top level", chevron). Nested folders
 * are indented; disabled options cannot be chosen.
 */
export function FolderSelect({ id, options, value, onChange, disabled, describedBy }: FolderSelectProps) {
  const t = useT();
  const selected = options.find((o) => o.id === value) ?? options[0];
  return (
    <span className={styles.select}>
      <IconFolder className={styles.folderIcon} size={15} strokeWidth={1.9} />
      <span className={styles.selectName}>{selected.name}</span>
      {selected.id === TOP_LEVEL && <span className={styles.selectHint}>{t("tree.folderSelect.topLevel")}</span>}
      <IconChevron className={styles.selectChevron} direction="down" size={12} strokeWidth={2.2} />
      <select
        id={id}
        className={styles.nativeSelect}
        value={value}
        disabled={disabled}
        aria-describedby={describedBy}
        onChange={(e: ChangeEvent<HTMLSelectElement>) => onChange(e.target.value)}
      >
        {options.map((o) => (
          <option key={o.id === TOP_LEVEL ? "(top)" : o.id} value={o.id} disabled={o.disabled}>
            {o.id === TOP_LEVEL ? t("tree.folderSelect.topLevelOption", { name: o.name }) : `${INDENT.repeat(o.depth - 1)}${o.name}`}
          </option>
        ))}
      </select>
    </span>
  );
}

/** Indents nested folders in the native select, which ignores padding on options. */
const INDENT = String.fromCodePoint(0xa0).repeat(4);
