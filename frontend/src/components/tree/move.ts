// Pure helpers for moving and reordering tree items (COL-04, move): which
// items can move, where to, the target of a keyboard step (Alt+Up/Down)
// and of a drop at a point of a row. They mirror the checks of Go's
// Tree.Move (docs/collection.md, What read-only items allow), so the UI
// refuses what Go would refuse. A folder shown sorted by name (COL-05)
// cannot be reordered by hand: items only go into it, and it shows them in
// their sorted place. See docs/frontend.md (Move and reorder).

import { t } from "../../i18n";
import { END } from "../../lib/collection";
import { selectSortMode, type CollectionState, type NodeId, type TreeNode } from "../../stores";

/** Where an item goes: a folder (null: the top level) and its index there, not counting the item itself (END: last). */
export interface MoveTarget {
  readonly parentId: NodeId | null;
  readonly index: number;
}

/** Where a drop lands relative to the row under the pointer. */
export type DropPosition = "before" | "after" | "into";

/** The drag data type of a tree item (its id); other drags, such as files from the OS, are ignored. */
export const TREE_DRAG_TYPE = "application/x-postsonde-tree-node";

/** Hovering a collapsed folder this long during a drag expands it. */
export const DRAG_EXPAND_DELAY_MS = 600;

/** The items of folder `parentId` (null: the top level), in display order. */
export function siblingIdsOf(state: CollectionState, parentId: NodeId | null): readonly NodeId[] {
  if (parentId === null) return state.rootIds;
  const parent: TreeNode | undefined = state.nodes[parentId];
  return parent?.kind === "folder" ? parent.childIds : [];
}

/** Whether folder `parentId` (null: the top level) is shown sorted by name, so its items cannot be reordered by hand (COL-05). */
export function isSortedByName(state: CollectionState, parentId: NodeId | null): boolean {
  return selectSortMode(state, parentId) === "name";
}

/** Whether items can be put into or taken out of folder `parentId` (null: the top level): its order can be written. */
export function isWritableContainer(state: CollectionState, parentId: NodeId | null): boolean {
  if (state.readOnly !== "") return false;
  if (parentId === null) return true;
  const node: TreeNode | undefined = state.nodes[parentId];
  return node?.kind === "folder" && node.readOnly === "";
}

/**
 * Why item `id` cannot be moved at all, or null when it can: not in a
 * read-only project, and not out of a folder whose order cannot be
 * written. Unknown, invalid and newer-version items can move (Go moves
 * the file unchanged).
 */
export function cannotMoveReason(state: CollectionState, id: NodeId): string | null {
  const node: TreeNode | undefined = state.nodes[id];
  if (node === undefined) return t("tree.moveCheck.gone");
  if (state.readOnly !== "") return t("tree.readOnlyProject");
  if (!isWritableContainer(state, node.parentId)) return t("tree.moveCheck.folderReadOnly");
  return null;
}

/** Whether `folderId` is `id` or inside it. */
export function isSelfOrDescendant(state: CollectionState, id: NodeId, folderId: NodeId | null): boolean {
  let current = folderId;
  for (let depth = 0; current !== null && depth < 1000; depth++) {
    if (current === id) return true;
    current = state.nodes[current]?.parentId ?? null;
  }
  return false;
}

/**
 * Why item `id` cannot go into folder `parentId` (null: the top level), or
 * null when it can.
 */
export function moveIntoError(state: CollectionState, id: NodeId, parentId: NodeId | null): string | null {
  const reason = cannotMoveReason(state, id);
  if (reason !== null) return reason;
  const node = state.nodes[id] as TreeNode;
  if (parentId !== null && state.nodes[parentId]?.kind !== "folder") return t("tree.moveCheck.notFolder");
  if (isSelfOrDescendant(state, id, parentId)) return t("tree.moveCheck.intoItself");
  if (!isWritableContainer(state, parentId)) return t("tree.moveCheck.targetReadOnly");
  if (node.readOnly === "symlink" && parentId !== node.parentId) return t("tree.moveCheck.symlink");
  return null;
}

/** The position of item `id` among its siblings, or -1. */
function indexInParent(state: CollectionState, id: NodeId): number {
  const node: TreeNode | undefined = state.nodes[id];
  if (node === undefined) return -1;
  return siblingIdsOf(state, node.parentId).indexOf(id);
}

/** Whether moving `id` to `target` would leave it where it is. */
export function isNoOpMove(state: CollectionState, id: NodeId, target: MoveTarget): boolean {
  const node: TreeNode | undefined = state.nodes[id];
  if (node === undefined || node.parentId !== target.parentId) return false;
  const current = indexInParent(state, id);
  const last = siblingIdsOf(state, node.parentId).length - 1;
  const index = target.index < 0 || target.index > last ? last : target.index;
  return index === current;
}

/**
 * Where Alt+Up (-1) or Alt+Down (+1) moves item `id`: one place up or down
 * in its folder. Null at the edge of its folder, when it cannot move, and
 * in a folder sorted by name.
 */
export function stepTarget(state: CollectionState, id: NodeId, delta: -1 | 1): MoveTarget | null {
  const node: TreeNode | undefined = state.nodes[id];
  if (node === undefined || cannotMoveReason(state, id) !== null || isSortedByName(state, node.parentId)) return null;
  const current = indexInParent(state, id);
  const index = current + delta;
  const count = siblingIdsOf(state, node.parentId).length;
  if (current < 0 || index < 0 || index >= count) return null;
  return { parentId: node.parentId, index };
}

/**
 * The drop position at `fraction` (0 top, 1 bottom) of a row: a folder's
 * top quarter is before it, its bottom quarter after it and the middle
 * into it; any other row's top half is before and its bottom half after.
 */
export function dropPositionAt(kind: TreeNode["kind"], fraction: number): DropPosition {
  if (kind === "folder") return fraction < 0.25 ? "before" : fraction > 0.75 ? "after" : "into";
  return fraction < 0.5 ? "before" : "after";
}

/**
 * Where dropping item `id` at `position` of row `overId` moves it, or null
 * when that drop is refused (into itself or one of its folders, a
 * read-only target, a symbolic link out of its folder) or would change
 * nothing. After an expanded folder that has items means before its first
 * item, which is where the drop line shows. Into a folder sorted by name
 * the item goes last in its manual order (it shows in its sorted place),
 * and a drop in the folder it is already in changes nothing.
 */
export function dropTarget(
  state: CollectionState,
  id: NodeId,
  overId: NodeId,
  position: DropPosition,
  overExpanded: boolean,
): MoveTarget | null {
  const over: TreeNode | undefined = state.nodes[overId];
  if (over === undefined) return null;
  let target: MoveTarget;
  if (position === "into" || (position === "after" && over.kind === "folder" && overExpanded && over.childIds.length > 0)) {
    if (over.kind !== "folder" || overId === id) return null;
    target = { parentId: overId, index: position === "into" ? END : 0 };
  } else {
    if (overId === id) return null;
    const siblings = siblingIdsOf(state, over.parentId).filter((s) => s !== id);
    const at = siblings.indexOf(overId);
    if (at < 0) return null;
    target = { parentId: over.parentId, index: position === "before" ? at : at + 1 };
  }
  return checkedTarget(state, id, target);
}

/** `target`, or null when the move is refused or changes nothing; END in a folder sorted by name. */
function checkedTarget(state: CollectionState, id: NodeId, target: MoveTarget): MoveTarget | null {
  if (isSortedByName(state, target.parentId)) {
    if (state.nodes[id]?.parentId === target.parentId) return null;
    target = { parentId: target.parentId, index: END };
  }
  if (moveIntoError(state, id, target.parentId) !== null || isNoOpMove(state, id, target)) return null;
  return target;
}

/** Dropping on the tree's empty area: the end of the top level, or null when refused or a no-op. */
export function dropAtEnd(state: CollectionState, id: NodeId): MoveTarget | null {
  return checkedTarget(state, id, { parentId: null, index: END });
}

/** The slash path an item gets in folder `parentFolderPath` ("" for the top level): its file or directory name is kept. */
export function movedPath(itemPath: string, parentFolderPath: string): string {
  const base = itemPath.slice(itemPath.lastIndexOf("/") + 1);
  return parentFolderPath === "" ? base : `${parentFolderPath}/${base}`;
}
