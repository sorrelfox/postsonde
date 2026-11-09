import { useCallback, useEffect, useRef, useState, type DragEvent } from "react";
import type { NodeId, Stores, VisibleRow } from "../../stores";
import {
  cannotMoveReason,
  DRAG_EXPAND_DELAY_MS,
  dropAtEnd,
  dropPositionAt,
  dropTarget,
  isSortedByName,
  TREE_DRAG_TYPE,
  type MoveTarget,
} from "./move";

/**
 * What the tree draws during a drag: a line between rows, or a highlighted
 * folder row (id null: the whole tree, for the top level when it is sorted
 * by name).
 */
export type DropIndicator =
  | { readonly kind: "line"; readonly index: number; readonly level: number }
  | { readonly kind: "into"; readonly id: NodeId | null };

interface Drop {
  readonly target: MoveTarget;
  readonly indicator: DropIndicator;
  /** A collapsed folder the pointer rests on the middle of: it expands after a delay. */
  readonly expand: NodeId | null;
}

export interface TreeDragOptions {
  stores: Stores;
  rows: readonly VisibleRow[];
  indexById: ReadonlyMap<NodeId, number>;
  /** Drag and drop is off without it (no actions, or a read-only project). */
  onMove: ((id: NodeId, parentId: NodeId | null, index: number) => void) | null;
}

export interface TreeDrag {
  /** The item being dragged, or null. */
  readonly draggingId: NodeId | null;
  readonly indicator: DropIndicator | null;
  /** Whether row `id` can be dragged now. */
  readonly canDrag: (id: NodeId) => boolean;
  /** Handlers for the tree's scroll container (the rows bubble to it). */
  readonly handlers: {
    readonly onDragStart: (e: DragEvent<HTMLElement>) => void;
    readonly onDragOver: (e: DragEvent<HTMLElement>) => void;
    readonly onDragLeave: (e: DragEvent<HTMLElement>) => void;
    readonly onDrop: (e: DragEvent<HTMLElement>) => void;
    readonly onDragEnd: (e: DragEvent<HTMLElement>) => void;
  };
}

function sameIndicator(a: DropIndicator | null, b: DropIndicator | null): boolean {
  if (a === null || b === null) return a === b;
  if (a.kind === "into" && b.kind === "into") return a.id === b.id;
  if (a.kind === "line" && b.kind === "line") return a.index === b.index && a.level === b.level;
  return false;
}

/**
 * Native HTML5 drag and drop of tree rows (COL-04, move). Only drags that
 * started on a row of this tree are handled: anything else (files from
 * the OS, text) gets no drop effect here, so it is never taken for a move.
 * Over a row, dropPositionAt says before, after or into; the drop is
 * checked with dropTarget (refused into the item itself or its folders,
 * read-only targets, a symbolic link out of its folder, or a drop that
 * changes nothing), and a refused one shows no indicator and the "none"
 * cursor. Below the last row is the end of the top level. Resting on a
 * collapsed folder expands it after DRAG_EXPAND_DELAY_MS.
 */
export function useTreeDrag({ stores, rows, indexById, onMove }: TreeDragOptions): TreeDrag {
  const dragged = useRef<NodeId | null>(null);
  const [draggingId, setDraggingId] = useState<NodeId | null>(null);
  const [indicator, setIndicator] = useState<DropIndicator | null>(null);
  const expandTimer = useRef<{ id: NodeId; timer: ReturnType<typeof setTimeout> } | null>(null);

  const stopExpand = useCallback(() => {
    if (expandTimer.current !== null) clearTimeout(expandTimer.current.timer);
    expandTimer.current = null;
  }, []);

  const reset = useCallback(() => {
    dragged.current = null;
    stopExpand();
    setDraggingId(null);
    setIndicator(null);
  }, [stopExpand]);

  useEffect(() => stopExpand, [stopExpand]);

  const canDrag = useCallback(
    (id: NodeId) => onMove !== null && cannotMoveReason(stores.collection.getState(), id) === null,
    [onMove, stores],
  );

  /** Where a drop at the event's point would move the dragged item, or null. */
  const dropAt = (e: DragEvent<HTMLElement>, id: NodeId): Drop | null => {
    const state = stores.collection.getState();
    const rowEl = e.target instanceof Element ? e.target.closest<HTMLElement>("[data-node-id]") : null;
    const overId = rowEl?.getAttribute("data-node-id") ?? null;
    if (rowEl === null || overId === null) {
      const target = dropAtEnd(state, id);
      if (target === null) return null;
      const indicator: DropIndicator = isSortedByName(state, null) ? { kind: "into", id: null } : { kind: "line", index: rows.length, level: 1 };
      return { target, indicator, expand: null };
    }
    const index = indexById.get(overId);
    const row = index === undefined ? undefined : rows[index];
    if (index === undefined || row === undefined) return null;
    const rect = rowEl.getBoundingClientRect();
    const fraction = rect.height > 0 ? (e.clientY - rect.top) / rect.height : Number.NaN;
    const position = dropPositionAt(row.kind, Number.isFinite(fraction) ? fraction : 0.5);
    const target = dropTarget(state, id, overId, position, row.expanded);
    if (target === null) return null;
    if (isSortedByName(state, target.parentId)) {
      // A folder sorted by name has no places between its items: the drop goes into it (COL-05).
      const expand = target.parentId === overId && position === "into" && !row.expanded ? overId : null;
      return { target, indicator: { kind: "into", id: target.parentId }, expand };
    }
    if (target.parentId === overId) {
      // Into the folder: the middle of its row, or just after it when it is open (before its first item).
      const intoFirst = position === "after";
      return {
        target,
        indicator: intoFirst ? { kind: "line", index: index + 1, level: row.level + 1 } : { kind: "into", id: overId },
        expand: !row.expanded && !intoFirst ? overId : null,
      };
    }
    return { target, indicator: { kind: "line", index: position === "before" ? index : index + 1, level: row.level }, expand: null };
  };

  const onDragStart = (e: DragEvent<HTMLElement>) => {
    const rowEl = e.target instanceof Element ? e.target.closest<HTMLElement>("[data-node-id]") : null;
    const id = rowEl?.getAttribute("data-node-id") ?? null;
    if (id === null || !canDrag(id)) {
      e.preventDefault();
      return;
    }
    e.dataTransfer.setData(TREE_DRAG_TYPE, id);
    e.dataTransfer.effectAllowed = "move";
    dragged.current = id;
    setDraggingId(id);
  };

  const onDragOver = (e: DragEvent<HTMLElement>) => {
    const id = dragged.current;
    // Not a drag of this tree's rows: leave it alone (no preventDefault: no drop here).
    if (id === null) return;
    const drop = dropAt(e, id);
    if (drop === null) {
      e.dataTransfer.dropEffect = "none";
      stopExpand();
      setIndicator((prev) => (prev === null ? prev : null));
      return;
    }
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    setIndicator((prev) => (sameIndicator(prev, drop.indicator) ? prev : drop.indicator));
    if (drop.expand === null) {
      stopExpand();
    } else if (expandTimer.current?.id !== drop.expand) {
      stopExpand();
      const folderId = drop.expand;
      expandTimer.current = {
        id: folderId,
        timer: setTimeout(() => {
          expandTimer.current = null;
          stores.collection.dispatch({ type: "collection/setExpanded", id: folderId, expanded: true });
        }, DRAG_EXPAND_DELAY_MS),
      };
    }
  };

  const onDragLeave = (e: DragEvent<HTMLElement>) => {
    if (dragged.current === null) return;
    const next = e.relatedTarget;
    if (next instanceof Node && e.currentTarget.contains(next)) return;
    stopExpand();
    setIndicator(null);
  };

  const onDrop = (e: DragEvent<HTMLElement>) => {
    const id = dragged.current;
    if (id === null) return;
    e.preventDefault();
    const drop = dropAt(e, id);
    reset();
    if (drop !== null && onMove !== null) onMove(id, drop.target.parentId, drop.target.index);
  };

  const onDragEnd = () => reset();

  return { draggingId, indicator, canDrag, handlers: { onDragStart, onDragOver, onDragLeave, onDrop, onDragEnd } };
}
