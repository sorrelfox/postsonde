import { useId, useLayoutEffect, useRef, useState, type KeyboardEvent, type SyntheticEvent } from "react";
import { useT } from "../../i18n/react";
import { useCollection, type NodeId, type TreeNode } from "../../stores";
import styles from "./CollectionTree.module.css";
import { validateRename } from "./newItem";

export interface InlineRenameProps {
  node: TreeNode;
  /** Renames the item; resolves to the error to show, or null when done. */
  onCommit: (id: NodeId, name: string) => Promise<string | null>;
  /** Ends the edit without renaming; `refocus` gives focus back to the row. */
  onCancel: (id: NodeId, refocus: boolean) => void;
}

/**
 * The name field of a tree row being renamed inline (COL-04, F2): starts
 * with the whole name selected. Enter renames, Esc cancels, and leaving the
 * field renames when the name is valid and changed, else cancels. Names are
 * checked as for new items (validateRename); the reason a name is refused,
 * or Go's error, shows under the field and the edit stays open.
 * Key presses and clicks stay in the field: they do not reach the tree.
 */
export function InlineRename({ node, onCommit, onCancel }: InlineRenameProps) {
  const t = useT();
  const [value, setValue] = useState(node.name);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const errorId = useId();
  const nameError = useCollection((s) => validateRename(s, node.id, value));
  const shownError = nameError !== null && nameError !== "" ? nameError : failure;

  useLayoutEffect(() => {
    const input = inputRef.current;
    input?.focus({ preventScroll: true });
    input?.select();
  }, []);

  const commit = async (fromBlur: boolean) => {
    if (busy) return;
    if (nameError !== null) {
      // Unchanged, or invalid when leaving the field: nothing to rename.
      if (value.trim() === node.name || fromBlur) onCancel(node.id, !fromBlur);
      return;
    }
    setBusy(true);
    setFailure(null);
    const error = await onCommit(node.id, value.trim());
    if (error === null) return;
    setBusy(false);
    setFailure(error);
    if (!fromBlur) inputRef.current?.focus({ preventScroll: true });
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    e.stopPropagation();
    if (e.nativeEvent.isComposing) return;
    if (e.key === "Enter") {
      e.preventDefault();
      void commit(false);
    } else if (e.key === "Escape") {
      e.preventDefault();
      if (!busy) onCancel(node.id, true);
    }
  };

  const stop = (e: SyntheticEvent) => e.stopPropagation();

  return (
    <span className={styles.renameField}>
      <input
        ref={inputRef}
        className={styles.renameInput}
        value={value}
        aria-label={node.kind === "folder" ? t("tree.folderName") : t("tree.requestName")}
        aria-invalid={shownError !== null || undefined}
        aria-describedby={shownError !== null ? errorId : undefined}
        maxLength={1000}
        spellCheck={false}
        autoComplete="off"
        readOnly={busy}
        onChange={(e) => {
          setValue(e.target.value);
          setFailure(null);
        }}
        onKeyDown={onKeyDown}
        onBlur={() => void commit(true)}
        onClick={stop}
        onDoubleClick={stop}
        onContextMenu={stop}
        onPointerDown={stop}
      />
      {shownError !== null && (
        <span className={styles.renameError} id={errorId} role="alert">
          {shownError}
        </span>
      )}
    </span>
  );
}
