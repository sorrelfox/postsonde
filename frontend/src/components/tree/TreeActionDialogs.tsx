import { useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { moveNode, renameNode } from "../../commands/collection";
import { useT, type Translate } from "../../i18n/react";
import { END } from "../../lib/collection";
import { toAppError } from "../../lib/errors";
import { useCollection, useStores, useWorkspace, type CollectionState, type NodeId, type TreeNode } from "../../stores";
import { Dialog } from "../common/Dialog";
import { IconTrash } from "../common/icons";
import { nodeBadge } from "./badges";
import { FolderSelect } from "./FolderSelect";
import { moveIntoError, movedPath } from "./move";
import { folderOptions, renamePreview, TOP_LEVEL, validateRename } from "./newItem";
import fieldStyles from "./NewItemDialog.module.css";
import styles from "./TreeActionDialogs.module.css";

/** The message of a failed tree operation: the error and its hint. */
export function failureText(err: unknown): string {
  const appErr = toAppError(err);
  return appErr.hint === "" ? appErr.message : `${appErr.message} ${appErr.hint}`;
}

export interface RenameDialogProps {
  /** The item to rename; null: closed. */
  id: NodeId | null;
  onClose: () => void;
}

/**
 * The Rename dialog (COL-04): the context menu's rename, next to the
 * inline rename in the tree (F2). The name field starts with the current
 * name selected; below it, where the file or directory moves. Names are
 * checked as for new items (validateRename); a failure from Go is shown
 * under the field.
 */
export function RenameDialog({ id, onClose }: RenameDialogProps) {
  if (id === null) return null;
  return <RenameForm key={id} id={id} onClose={onClose} />;
}

function RenameForm({ id, onClose }: { id: NodeId; onClose: () => void }) {
  const t = useT();
  const stores = useStores();
  const state = useCollection((s) => s);
  const node: TreeNode | undefined = state.nodes[id];
  const [name, setName] = useState(node?.name ?? "");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const nameRef = useRef<HTMLInputElement>(null);
  const baseId = useId();
  const errorId = `${baseId}-error`;
  const pathId = `${baseId}-path`;

  // After the dialog focused the field: select the name, so typing replaces it.
  useLayoutEffect(() => nameRef.current?.select(), []);

  const isFolder = node?.kind === "folder";
  const nameError = validateRename(state, id, name);
  const preview = renamePreview(state, id, name);
  const shownError = nameError !== null && nameError !== "" ? nameError : failure;

  const confirm = async () => {
    if (nameError !== null || busy || node === undefined) return;
    setBusy(true);
    setFailure(null);
    try {
      await renameNode(stores, id, name.trim());
      onClose();
    } catch (err: unknown) {
      setFailure(failureText(err));
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      title={isFolder ? t("tree.rename.folderTitle") : t("tree.rename.requestTitle")}
      confirmLabel={t("tree.rename.confirm")}
      onCancel={onClose}
      onConfirm={() => void confirm()}
      confirmDisabled={nameError !== null || node === undefined}
      busy={busy}
      initialFocus={nameRef}
    >
      <div className={fieldStyles.field}>
        <label className={fieldStyles.label} htmlFor={`${baseId}-name`}>
          {t("tree.name")}
        </label>
        <input
          id={`${baseId}-name`}
          ref={nameRef}
          className={fieldStyles.input}
          value={name}
          maxLength={1000}
          spellCheck={false}
          autoComplete="off"
          readOnly={busy}
          aria-invalid={shownError !== null || undefined}
          aria-describedby={[shownError !== null ? errorId : "", pathId].filter(Boolean).join(" ")}
          onChange={(e) => {
            setName(e.target.value);
            setFailure(null);
          }}
        />
        {shownError !== null && (
          <span className={fieldStyles.error} id={errorId} role="alert">
            {shownError}
          </span>
        )}
      </div>
      {node !== undefined && (
        <div className={styles.pathBox} id={pathId}>
          <span className={styles.pathNote}>
            {preview === null
              ? isFolder
                ? t("tree.rename.folderKeeps")
                : t("tree.rename.fileKeeps")
              : isFolder
                ? t("tree.rename.folderRenamed")
                : t("tree.rename.fileRenamed")}
          </span>
          <span className={styles.path}>
            {preview === null ? (
              `${node.path}${isFolder ? "/" : ""}`
            ) : (
              <>
                {preview.from} <span className={styles.arrow}>→</span> {preview.to}
              </>
            )}
          </span>
        </div>
      )}
    </Dialog>
  );
}

export interface MoveDialogProps {
  /** The item to move; null: closed. */
  id: NodeId | null;
  onClose: () => void;
}

/**
 * The Move to folder dialog (COL-04, move): the keyboard alternative to
 * dragging an item into another folder. The folder picker of the New item
 * dialogs lists the top level and every folder; the ones the item cannot
 * go into (itself and its folders, read-only folders, any other folder
 * for a symbolic link) are disabled. It starts on the item's own folder,
 * and Move stays disabled until another one is chosen. The item goes last
 * in the chosen folder and keeps its file or directory name; below the
 * picker, where it moves. A failure from Go is shown in the dialog.
 */
export function MoveDialog({ id, onClose }: MoveDialogProps) {
  if (id === null) return null;
  return <MoveForm key={id} id={id} onClose={onClose} />;
}

function MoveForm({ id, onClose }: { id: NodeId; onClose: () => void }) {
  const t = useT();
  const stores = useStores();
  const state = useCollection((s) => s);
  const projectName = useWorkspace((s) => s.current?.name) ?? t("tree.project");
  const node: TreeNode | undefined = state.nodes[id];
  const current = node?.parentId ?? TOP_LEVEL;
  const [parentId, setParentId] = useState<NodeId>(current);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const baseId = useId();
  const errorId = `${baseId}-error`;
  const pathId = `${baseId}-path`;

  const options = useMemo(
    () =>
      folderOptions(state, projectName).map((o) =>
        o.id === current ? { ...o, disabled: false } : { ...o, disabled: moveIntoError(state, id, o.id === TOP_LEVEL ? null : o.id) !== null },
      ),
    [state, projectName, id, current],
  );
  // The chosen folder went away or cannot take the item any more: back to the item's own folder.
  const target = options.some((o) => o.id === parentId && !o.disabled) ? parentId : current;
  const targetPath = target === TOP_LEVEL ? "" : (state.nodes[target]?.path ?? "");
  const isFolder = node?.kind === "folder";
  const unchanged = target === current;

  const confirm = async () => {
    if (unchanged || busy || node === undefined) return;
    setBusy(true);
    setFailure(null);
    try {
      await moveNode(stores, id, target === TOP_LEVEL ? null : target, END);
      onClose();
    } catch (err: unknown) {
      setFailure(failureText(err));
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      title={isFolder ? t("tree.move.folderTitle") : t("tree.move.requestTitle")}
      confirmLabel={t("tree.move.confirm")}
      onCancel={onClose}
      onConfirm={() => void confirm()}
      confirmDisabled={unchanged || node === undefined}
      busy={busy}
    >
      <div className={fieldStyles.field}>
        <label className={fieldStyles.label} htmlFor={`${baseId}-folder`}>
          {t("tree.move.to", { name: node?.name ?? "" })}
        </label>
        <FolderSelect
          id={`${baseId}-folder`}
          options={options}
          value={target}
          disabled={busy}
          describedBy={[failure !== null ? errorId : "", pathId].filter(Boolean).join(" ")}
          onChange={(next) => {
            setParentId(next);
            setFailure(null);
          }}
        />
        {failure !== null && (
          <span className={fieldStyles.error} id={errorId} role="alert">
            {failure}
          </span>
        )}
      </div>
      {node !== undefined && (
        <div className={styles.pathBox} id={pathId}>
          <span className={styles.pathNote}>
            {unchanged
              ? t("tree.move.unchanged")
              : isFolder
                ? t("tree.move.folderMoves")
                : t("tree.move.fileMoves")}
          </span>
          <span className={styles.path}>
            {unchanged ? (
              `${node.path}${isFolder ? "/" : ""}`
            ) : (
              <>
                {node.path} <span className={styles.arrow}>→</span> {movedPath(node.path, targetPath)}
              </>
            )}
          </span>
        </div>
      )}
    </Dialog>
  );
}

/** Items listed in the delete confirmation before "and N more". */
export const DELETE_LIST_LIMIT = 8;

export interface FolderContents {
  /** Folders inside it, at any depth. */
  readonly folders: number;
  /** Requests (and files that did not load) inside it, at any depth. */
  readonly items: number;
  /** The items in display order, depth first. */
  readonly list: readonly TreeNode[];
}

/** What deleting folder `id` also deletes. */
export function folderContents(state: CollectionState, id: NodeId): FolderContents {
  let folders = 0;
  const list: TreeNode[] = [];
  const visit = (ids: readonly NodeId[], depth: number) => {
    for (const childId of ids) {
      const node: TreeNode | undefined = state.nodes[childId];
      if (node === undefined || depth > 1000) continue;
      if (node.kind === "folder") {
        folders++;
        visit(node.childIds, depth + 1);
      } else {
        list.push(node);
      }
    }
  };
  const root: TreeNode | undefined = state.nodes[id];
  if (root?.kind === "folder") visit(root.childIds, 0);
  return { folders, items: list.length, list };
}

/** The delete confirmation's sentence: "The folder and the 2 folders and 5 requests in it move to this project's trash. …" */
export function describeContents(t: Translate, { folders, items }: FolderContents): string {
  const f = t("tree.delete.folders", { count: folders });
  const r = t("tree.delete.requests", { count: items });
  if (folders > 0 && items > 0) return t("tree.delete.bodyBoth", { folders: f, requests: r });
  if (folders > 0) return t("tree.delete.bodyFolders", { folders: f });
  if (items > 0) return t("tree.delete.bodyRequests", { requests: r });
  return t("tree.delete.bodyEmpty");
}

export interface DeleteFolderDialogProps {
  /** The folder to delete; null: closed. */
  id: NodeId | null;
  onCancel: () => void;
  onConfirm: (id: NodeId) => void;
}

/**
 * The confirmation before deleting a folder that has contents (COL-04):
 * what moves to the trash, with the requests listed (at most
 * DELETE_LIST_LIMIT, then "and N more"), and a destructive "Delete folder"
 * button. Cancel has focus first and a click on the scrim does nothing
 * (Dialog's danger variant).
 */
export function DeleteFolderDialog({ id, onCancel, onConfirm }: DeleteFolderDialogProps) {
  const t = useT();
  const state = useCollection((s) => s);
  const descId = useId();
  if (id === null) return null;
  const node: TreeNode | undefined = state.nodes[id];
  if (node === undefined) return null;
  const contents = folderContents(state, id);
  const shown = contents.list.slice(0, DELETE_LIST_LIMIT);
  const more = contents.list.length - shown.length;

  return (
    <Dialog
      open
      variant="danger"
      title={t("tree.delete.title", { name: node.name })}
      describedBy={descId}
      confirmLabel={t("tree.delete.confirm")}
      confirmIcon={<IconTrash size={14} strokeWidth={1.9} />}
      onCancel={onCancel}
      onConfirm={() => onConfirm(id)}
    >
      <p id={descId} className={styles.message}>
        {describeContents(t, contents)}
      </p>
      {shown.length > 0 && (
        <ul className={styles.list} aria-label={t("tree.delete.list")}>
          {shown.map((item) => {
            const badge = nodeBadge(item);
            return (
              <li key={item.id} className={styles.item}>
                <span className={styles.badge} data-tone={badge?.tone}>
                  {badge?.label}
                </span>
                <span className={styles.itemName}>{item.name}</span>
              </li>
            );
          })}
          {more > 0 && <li className={`${styles.item} ${styles.more}`}>{t("tree.delete.more", { count: more })}</li>}
        </ul>
      )}
    </Dialog>
  );
}
