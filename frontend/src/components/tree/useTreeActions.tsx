import { useCallback, useMemo, useRef, useState, type ReactNode } from "react";
import { deleteNode, duplicateNode, moveNode, restoreNode, type DeletedNode } from "../../commands/collection";
import { isEditableTarget } from "../../commands/shortcuts";
import { showToast, toaster } from "../../commands/toasts";
import { useT } from "../../i18n/react";
import { shortcutLabel } from "../../lib/shortcuts";
import { useStores, type NodeId, type TreeNode } from "../../stores";
import { IconTrash, IconWarning } from "../common/icons";
import { useShortcut } from "../shell/useShortcut";
import { DeleteFolderDialog, failureText, MoveDialog, RenameDialog } from "./TreeActionDialogs";

/** What the tree's context menu and keys do to an item (COL-04). */
export interface TreeActions {
  /** Opens the Rename dialog (the tree renames inline itself, with F2). */
  readonly rename: (id: NodeId) => void;
  readonly duplicate: (id: NodeId) => void;
  /**
   * Deletes the item, after a confirmation for a folder with contents, and
   * shows the Undo toast. Resolves true once it is deleted, false when it
   * was cancelled or failed (the failure is shown as a toast).
   */
  readonly remove: (id: NodeId) => Promise<boolean>;
  /**
   * Moves the item into folder `parentId` (null: the top level) at `index`,
   * not counting itself (END: last): a drop or Alt+Up/Down. Resolves true
   * once it moved, false when it failed (the failure is shown as a toast).
   */
  readonly move: (id: NodeId, parentId: NodeId | null, index: number) => Promise<boolean>;
  /** Opens the Move to folder dialog. */
  readonly moveTo: (id: NodeId) => void;
}

export interface TreeActionsControl extends TreeActions {
  /** The dialogs; render them anywhere (they are portals). */
  readonly dialog: ReactNode;
}

/** Whether an item can be renamed, duplicated or deleted: a writable folder or request. */
export function canEditNode(node: TreeNode | undefined, projectReadOnly: boolean): boolean {
  return !projectReadOnly && node !== undefined && node.readOnly === "" && (node.kind === "folder" || node.kind === "request");
}

function showFailure(message: string): void {
  showToast({ message, icon: <IconWarning size={14} strokeWidth={1.9} /> });
}

/**
 * The rename, duplicate, move and delete actions of the Collections
 * sidebar (COL-04): the Rename and Move to folder dialogs, a failed move
 * as a toast, the confirmation before deleting a folder
 * with contents, and the Undo toast after a delete, whose action restores
 * the item from the trash with restoreNode. ⌘Z (collection.undoDelete) runs
 * the Undo of the newest delete while its toast shows, except in text
 * fields and editors, which keep their own undo.
 */
export function useTreeActions(): TreeActionsControl {
  const t = useT();
  const stores = useStores();
  const [renaming, setRenaming] = useState<NodeId | null>(null);
  const [moving, setMoving] = useState<NodeId | null>(null);
  const [confirming, setConfirming] = useState<{ id: NodeId; resolve: (ok: boolean) => void } | null>(null);
  /** The newest delete and its toast. */
  const lastDelete = useRef<{ toastId: string; deleted: DeletedNode } | null>(null);

  const undo = useCallback(
    async (deleted: DeletedNode) => {
      if (lastDelete.current?.deleted === deleted) lastDelete.current = null;
      try {
        await restoreNode(stores, deleted);
      } catch (err: unknown) {
        showFailure(t("tree.actions.restoreFailed", { name: deleted.name, reason: failureText(err) }));
      }
    },
    [stores, t],
  );

  const doDelete = useCallback(
    async (id: NodeId): Promise<boolean> => {
      const name = stores.collection.getState().nodes[id]?.name ?? "";
      let deleted: DeletedNode;
      try {
        deleted = await deleteNode(stores, id);
      } catch (err: unknown) {
        showFailure(t("tree.actions.deleteFailed", { name, reason: failureText(err) }));
        return false;
      }
      const toastId = showToast({
        message: t("tree.actions.deleted", { name: deleted.name }),
        icon: <IconTrash size={14} strokeWidth={1.9} />,
        action: { label: t("tree.actions.undo"), run: () => undo(deleted) },
        shortcut: shortcutLabel("collection.undoDelete"),
      });
      lastDelete.current = { toastId, deleted };
      return true;
    },
    [stores, undo, t],
  );

  const remove = useCallback(
    async (id: NodeId): Promise<boolean> => {
      const node = stores.collection.getState().nodes[id];
      if (node === undefined) return false;
      if (node.kind === "folder" && node.childIds.length > 0) {
        const ok = await new Promise<boolean>((resolve) => setConfirming({ id, resolve }));
        if (!ok) return false;
      }
      return doDelete(id);
    },
    [stores, doDelete],
  );

  const duplicate = useCallback(
    (id: NodeId) => {
      const name = stores.collection.getState().nodes[id]?.name ?? "";
      duplicateNode(stores, id).catch((err: unknown) => showFailure(t("tree.actions.duplicateFailed", { name, reason: failureText(err) })));
    },
    [stores, t],
  );

  const move = useCallback(
    async (id: NodeId, parentId: NodeId | null, index: number): Promise<boolean> => {
      const name = stores.collection.getState().nodes[id]?.name ?? "";
      try {
        await moveNode(stores, id, parentId, index);
        return true;
      } catch (err: unknown) {
        showFailure(t("tree.actions.moveFailed", { name, reason: failureText(err) }));
        return false;
      }
    },
    [stores, t],
  );

  useShortcut("collection.undoDelete", (e) => {
    const last = lastDelete.current;
    if (isEditableTarget(e.target) || last === null) return false;
    if (!toaster.toasts().some((toast) => toast.id === last.toastId)) {
      lastDelete.current = null;
      return false;
    }
    toaster.runAction(last.toastId);
  });

  const settle = (ok: boolean) => {
    confirming?.resolve(ok);
    setConfirming(null);
  };

  const dialog = (
    <>
      <RenameDialog id={renaming} onClose={() => setRenaming(null)} />
      <MoveDialog id={moving} onClose={() => setMoving(null)} />
      <DeleteFolderDialog id={confirming?.id ?? null} onCancel={() => settle(false)} onConfirm={() => settle(true)} />
    </>
  );

  const rename = useCallback((id: NodeId) => setRenaming(id), []);
  const moveTo = useCallback((id: NodeId) => setMoving(id), []);
  const actions = useMemo(() => ({ rename, duplicate, remove, move, moveTo }), [rename, duplicate, remove, move, moveTo]);
  return { ...actions, dialog };
}
