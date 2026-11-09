import {
  memo,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type MouseEvent,
} from "react";
import { openTreeItem, renameNode, setFolderSort } from "../../commands/collection";
import { canCopyAsCurl, copyAsCurl } from "../../commands/copyCurl";
import { exportFolderAsHar, exportHistoryAsHar, hasHistory } from "../../commands/exportHar";
import { exportAsPostman } from "../../commands/exportPostman";
import { exportAsZip } from "../../commands/exportZip";
import { openFolderRun } from "../../commands/folderRuns";
import { SCOPE_ATTRIBUTE } from "../../commands/shortcuts";
import { showToast } from "../../commands/toasts";
import { useT, type Translate } from "../../i18n/react";
import { matchesShortcut, shortcutLabel } from "../../lib/shortcuts";
import {
  countItems,
  flattenVisibleRows,
  selectFolderServerBadge,
  selectNodeProblem,
  selectSortMode,
  useCollection,
  useServers,
  useStores,
  type NodeId,
  type SortMode,
  type TreeNode,
  type VisibleRow,
} from "../../stores";
import { ContextMenu, type ContextMenuItem } from "../common/ContextMenu";
import { EmptyState } from "../common/EmptyState";
import { Highlighted } from "../common/Highlighted";
import { IconChevron, IconCollections, IconFilter, IconFolder, IconInfo, IconWarning } from "../common/icons";
import { useVirtualRows } from "../common/useVirtualRows";
import { nodeBadge, readOnlyHint } from "./badges";
import styles from "./CollectionTree.module.css";
import { buildFilterIndex, filterTree, type FilterHit } from "./filter";
import { InlineRename } from "./InlineRename";
import { cannotMoveReason, isSortedByName, stepTarget } from "./move";
import { failureText } from "./TreeActionDialogs";
import { canEditNode, type TreeActions } from "./useTreeActions";
import { useTreeDrag, type DropIndicator } from "./useTreeDrag";

/** Row height in px (the mockup's 30px rows). Rows are positioned by index, so every row has it. */
export const TREE_ROW_HEIGHT = 30;
/** Rows rendered above and below the visible window. */
export const TREE_OVERSCAN = 10;
/** Viewport height assumed until the scroll container has been measured (and in jsdom, where it is 0). */
export const TREE_FALLBACK_VIEWPORT = 600;
/** Indent per level, and the extra indent of an item under the folder chevron, in px (mockup). */
const INDENT = 14;
const ITEM_EXTRA_INDENT = 18;
const ROW_START = 8;
/** Type-ahead: keys typed within this many ms extend the search. */
const TYPE_AHEAD_MS = 500;

export interface CollectionTreeProps {
  /** Accessible name of the tree; "Collection" when not given. */
  label?: string;
  /**
   * Asks for the New folder, New request (COL-04), Import cURL (IMP-01) or
   * Import (IMP-10) dialog from the context menu, for the node it was opened on (null: the
   * empty area, the top level). Without it the tree has no context menu.
   */
  onNewItem?: (type: "folder" | "request" | "curl" | "import", from: NodeId | null) => void;
  /**
   * Rename (dialog), duplicate, move and delete (COL-04), from the context
   * menu, the keys (tree.duplicate, tree.delete, tree.moveUp,
   * tree.moveDown, tree.moveTo) and drag and drop. Without it those are
   * not offered, rows cannot be dragged, and F2 (tree.rename, inline)
   * does nothing.
   */
  actions?: TreeActions;
  /**
   * The sidebar filter (NAV-06): only the items whose name or method
   * matches, the folders above them (opened) and the contents of folders
   * that match are shown, with the matched letters highlighted. Blank: no
   * filter.
   */
  filter?: string;
  /** Clears the filter (the "No matches" state's button). */
  onClearFilter?: () => void;
}

/** The context menu's sort choices (COL-05): "Sort “Orders” by name", "Keep “Orders” in manual order", or the top level. */
function sortMenuLabel(t: Translate, nodes: Readonly<Record<NodeId, TreeNode>>, parentId: NodeId | null, mode: SortMode): string {
  if (parentId === null) return mode === "name" ? t("tree.menu.sortByNameTop") : t("tree.menu.sortManualTop");
  const folder = nodes[parentId]?.name ?? "";
  return mode === "name" ? t("tree.menu.sortByNameFolder", { folder }) : t("tree.menu.sortManualFolder", { folder });
}

/** Expansion changes made while filtering, for one query; they are dropped with it. */
interface FilterExpansion {
  readonly query: string;
  readonly overrides: Readonly<Record<NodeId, boolean>>;
}

interface TreeMenu {
  readonly x: number;
  readonly y: number;
  /** The row it was opened on, or null for the tree's empty area. */
  readonly id: NodeId | null;
}

/**
 * The request tree of the open project (COL-01, COL-02): nested folders
 * with item counts, and items of every protocol with their kind or method
 * badge. Virtualized with useVirtualRows: only the rows in view (plus overscan and the focused
 * row) are in the DOM. Keyboard: the WAI-ARIA tree pattern with a roving
 * tabindex. With onNewItem, a context menu offers New request and New
 * folder; with actions, Rename, Duplicate, Move and Delete too, F2 renames
 * the focused item inline, Alt+Up/Down reorder it, and rows can be dragged
 * into folders or between items (COL-04). The context menu also sorts a
 * folder by name or keeps its manual order (COL-05, a view preference),
 * and `filter` shows only what matches by name or method (NAV-06). See
 * docs/frontend.md (Collection tree, Sorting, Sidebar filter).
 */
export function CollectionTree({ label: labelProp, onNewItem, actions, filter: query = "", onClearFilter }: CollectionTreeProps) {
  const t = useT();
  const label = labelProp ?? t("tree.label");
  const stores = useStores();
  const status = useCollection((s) => s.status);
  const error = useCollection((s) => s.error);
  const projectReadOnly = useCollection((s) => s.readOnly !== "");
  const [menu, setMenu] = useState<TreeMenu | null>(null);
  const nodes = useCollection((s) => s.nodes);
  const rootIds = useCollection((s) => s.rootIds);
  const storeExpandedIds = useCollection((s) => s.expandedIds);
  const sortedByName = useCollection((s) => s.sortedByName);
  const selectedId = useCollection((s) => s.selectedId);

  // The sidebar filter (NAV-06). The index is built once per tree; filtering is one pass over it.
  const filterIndex = useMemo(() => buildFilterIndex(nodes, rootIds), [nodes, rootIds]);
  const filter = useMemo(() => filterTree(nodes, filterIndex, query), [nodes, filterIndex, query]);
  const filtering = filter !== null;
  // While filtering, the folders above the matches are open, and toggles
  // change only this view: the store's expansion is back as it was once the
  // filter is cleared.
  const [filterExpansion, setFilterExpansion] = useState<FilterExpansion>({ query: "", overrides: {} });
  const overrides = filter !== null && filterExpansion.query === filter.query ? filterExpansion.overrides : null;
  const expandedIds = useMemo(() => {
    if (filter === null) return storeExpandedIds;
    if (overrides === null) return filter.expandedIds;
    const out: Record<NodeId, true> = { ...filter.expandedIds };
    for (const [id, open] of Object.entries(overrides)) {
      if (open) out[id] = true;
      else delete out[id];
    }
    return out;
  }, [filter, overrides, storeExpandedIds]);

  const rows = useMemo(
    () => flattenVisibleRows(nodes, rootIds, expandedIds, { sortedByName, visible: filter?.visible }),
    [nodes, rootIds, expandedIds, sortedByName, filter],
  );
  const counts = useMemo(() => countItems(nodes, rootIds), [nodes, rootIds]);
  const indexById = useMemo(() => {
    const map = new Map<NodeId, number>();
    rows.forEach((row, i) => map.set(row.id, i));
    return map;
  }, [rows]);

  const treeId = useId();
  const rowEls = useRef(new Map<NodeId, HTMLDivElement>());
  const [focusedId, setFocusedId] = useState<NodeId | null>(null);
  // Set when the keyboard moved focus: the layout effect then focuses the row element.
  const pendingFocus = useRef(false);
  const typeAhead = useRef({ text: "", at: 0 });

  // The row that holds the roving tabindex: the focused node, or its nearest
  // visible ancestor once a collapse hid it, else the selection, else the first row.
  const focusIndex = useMemo(() => {
    if (rows.length === 0) return -1;
    // A row the filter hides does not hand focus to its folder: the selection or the first match takes it.
    let id: NodeId | null = filter !== null && focusedId !== null && !filter.visible.has(focusedId) ? null : focusedId;
    while (id !== null) {
      const i = indexById.get(id);
      if (i !== undefined) return i;
      id = nodes[id]?.parentId ?? null;
    }
    const selected = selectedId === null ? undefined : indexById.get(selectedId);
    if (selected !== undefined) return selected;
    if (filter !== null) {
      const firstHit = rows.findIndex((row) => filter.hits.has(row.id));
      if (firstHit >= 0) return firstHit;
    }
    return 0;
  }, [rows, indexById, nodes, focusedId, selectedId, filter]);

  // The item being renamed inline (F2), and the row index to focus once a delete removed the focused row.
  const [renamingId, setRenamingId] = useState<NodeId | null>(null);
  const [refocusIndex, setRefocusIndex] = useState<number | null>(null);
  // The item to focus again once a move (Alt+Up/Down) put its row at a new index.
  const [refocusId, setRefocusId] = useState<NodeId | null>(null);
  const renameIndex = renamingId === null ? undefined : indexById.get(renamingId);

  // The focused row, and the one being renamed, stay rendered even when
  // scrolled away, so focus and the edit survive virtualization.
  const virtual = useVirtualRows({
    count: rows.length,
    rowHeight: TREE_ROW_HEIGHT,
    overscan: TREE_OVERSCAN,
    fallbackViewport: TREE_FALLBACK_VIEWPORT,
    pinned: renameIndex === undefined ? [focusIndex] : [focusIndex, renameIndex],
  });
  const { height, rendered, scrollToIndex } = virtual;

  const registerRow = useCallback((id: NodeId, el: HTMLDivElement | null) => {
    if (el) rowEls.current.set(id, el);
    else rowEls.current.delete(id);
  }, []);

  const moveFocus = useCallback(
    (index: number) => {
      const row = rows[Math.max(0, Math.min(rows.length - 1, index))];
      if (!row) return;
      pendingFocus.current = true;
      setFocusedId(row.id);
      scrollToIndex(indexById.get(row.id) ?? 0);
    },
    [rows, indexById, scrollToIndex],
  );

  useLayoutEffect(() => {
    if (!pendingFocus.current || focusIndex < 0) return;
    pendingFocus.current = false;
    const row = rows[focusIndex];
    if (row) rowEls.current.get(row.id)?.focus({ preventScroll: true });
  });

  // A reload or collapse can leave focusedId pointing at a gone node; the
  // focus index above already falls back, so only keep state tidy.
  useEffect(() => {
    if (focusedId !== null && nodes[focusedId] === undefined) setFocusedId(null);
  }, [nodes, focusedId]);

  // An item renamed inline that went away (a reload, a delete elsewhere) ends the edit.
  useEffect(() => {
    if (renamingId !== null && renameIndex === undefined) setRenamingId(null);
  }, [renamingId, renameIndex]);

  // After a delete from the tree, focus goes to the row that took the deleted one's place.
  useEffect(() => {
    if (refocusIndex === null) return;
    setRefocusIndex(null);
    if (rows.length > 0) moveFocus(Math.min(refocusIndex, rows.length - 1));
  }, [refocusIndex, rows, moveFocus]);

  useEffect(() => {
    if (refocusId === null) return;
    setRefocusId(null);
    const index = indexById.get(refocusId);
    if (index !== undefined) moveFocus(index);
  }, [refocusId, indexById, moveFocus]);

  /** Opens or closes a folder: in the store, or while filtering only in the filtered view. */
  const toggle = useCallback(
    (id: NodeId) => {
      if (filter === null) {
        stores.collection.dispatch({ type: "collection/toggleExpanded", id });
        return;
      }
      const open = !(id in expandedIds);
      setFilterExpansion((prev) => ({
        query: filter.query,
        overrides: { ...(prev.query === filter.query ? prev.overrides : {}), [id]: open },
      }));
    },
    [stores, filter, expandedIds],
  );

  /** A click or Space: a folder toggles and is selected; a request opens in the preview tab. */
  const activate = useCallback(
    (id: NodeId) => {
      const node = stores.collection.getState().nodes[id];
      if (node?.kind === "folder") {
        toggle(id);
        stores.collection.dispatch({ type: "collection/select", id });
      } else {
        openTreeItem(stores, id, { preview: true });
      }
    },
    [stores, toggle],
  );

  const onRowClick = useCallback(
    (id: NodeId) => {
      setFocusedId(id);
      activate(id);
    },
    [activate],
  );

  // A double-click opens a folder's DIR tab, whose two clicks toggled the folder twice, so it
  // ends as it was; on a request it keeps the tab its clicks opened as the preview (NAV-01).
  const onRowDoubleClick = useCallback(
    (id: NodeId) => {
      const kind = stores.collection.getState().nodes[id]?.kind;
      if (kind === "folder" || kind === "request") openTreeItem(stores, id);
    },
    [stores],
  );

  const findByPrefix = (text: string, from: number): number => {
    const needle = text.toLowerCase();
    for (let k = 0; k < rows.length; k++) {
      const i = (from + k) % rows.length;
      if ((nodes[rows[i].id]?.name ?? "").toLowerCase().startsWith(needle)) return i;
    }
    return -1;
  };

  const hasMenu = onNewItem !== undefined || actions !== undefined;

  /** Whether item `id` can be renamed, duplicated or deleted from the tree. */
  const editable = useCallback(
    (id: NodeId) => actions !== undefined && canEditNode(stores.collection.getState().nodes[id], projectReadOnly),
    [actions, stores, projectReadOnly],
  );

  /** Deletes item `id` (asking first for a folder with contents); focus then moves to the row in its place. */
  const remove = useCallback(
    (id: NodeId) => {
      if (actions === undefined || !editable(id)) return;
      const index = indexById.get(id);
      void actions.remove(id).then((deleted) => {
        if (deleted && index !== undefined) setRefocusIndex(index);
      });
    },
    [actions, editable, indexById],
  );

  /** Whether item `id` can be moved from the tree (unknown and invalid items too, not out of a read-only folder). */
  const movable = useCallback(
    (id: NodeId) => actions !== undefined && cannotMoveReason(stores.collection.getState(), id) === null,
    [actions, stores],
  );

  /** Moves item `id` into `parentId` at `index`; with `refocus`, focus follows its row (keyboard moves). */
  const move = useCallback(
    (id: NodeId, parentId: NodeId | null, index: number, refocus = false) => {
      if (actions === undefined) return;
      void actions.move(id, parentId, index).then((moved) => {
        if (moved && refocus) setRefocusId(id);
      });
    },
    [actions],
  );

  /**
   * Why item `id` cannot be moved up or down in its folder now, though it
   * could move: the filter hides some of its neighbours, or its folder is
   * sorted by name (COL-05). Null when it can.
   */
  const reorderBlocked = useCallback(
    (id: NodeId): string | null => {
      if (filtering) return t("tree.reorder.filtered");
      const state = stores.collection.getState();
      const parentId = state.nodes[id]?.parentId ?? null;
      if (!isSortedByName(state, parentId)) return null;
      const choice = sortMenuLabel(t, state.nodes, parentId, "manual");
      if (parentId === null) return t("tree.reorder.sortedTop", { choice });
      return t("tree.reorder.sortedFolder", { folder: state.nodes[parentId]?.name ?? "", choice });
    },
    [filtering, stores, t],
  );

  /** Alt+Up (-1) or Alt+Down (+1): one place up or down in its folder; nothing at the edge. */
  const step = useCallback(
    (id: NodeId, delta: -1 | 1) => {
      if (reorderBlocked(id) !== null) return;
      const target = stepTarget(stores.collection.getState(), id, delta);
      if (target !== null) move(id, target.parentId, target.index, true);
    },
    [stores, move, reorderBlocked],
  );

  // Drag and drop is off while filtering: the rows between which an item would land are not all shown.
  const drag = useTreeDrag({
    stores,
    rows,
    indexById,
    onMove: actions !== undefined && !projectReadOnly && !filtering ? (id, parentId, index) => move(id, parentId, index) : null,
  });

  const commitRename = useCallback(
    async (id: NodeId, name: string): Promise<string | null> => {
      try {
        await renameNode(stores, id, name);
      } catch (err: unknown) {
        return failureText(err);
      }
      setRenamingId(null);
      pendingFocus.current = true;
      setFocusedId(id);
      return null;
    },
    [stores],
  );

  const cancelRename = useCallback((id: NodeId, refocus: boolean) => {
    setRenamingId((current) => (current === id ? null : current));
    if (!refocus) return;
    pendingFocus.current = true;
    setFocusedId(id);
  }, []);

  // The context menu (COL-04): a right-click on a row or the empty area, or
  // Shift+F10 / the Menu key on the focused row.
  const onContextMenu = (e: MouseEvent<HTMLElement>) => {
    if (!hasMenu || e.defaultPrevented) return;
    e.preventDefault();
    const id = e.target instanceof Element ? (e.target.closest("[data-node-id]")?.getAttribute("data-node-id") ?? null) : null;
    if (id !== null) setFocusedId(id);
    setMenu({ x: e.clientX, y: e.clientY, id });
  };

  const menuItems = useMemo<ContextMenuItem[]>(() => {
    if (menu === null) return [];
    // Focus the row first, so a dialog gives focus back to it when it closes.
    const focusRow = () => {
      const row = menu.id ?? rows[focusIndex]?.id;
      if (row !== undefined) rowEls.current.get(row)?.focus({ preventScroll: true });
    };
    const items: ContextMenuItem[] = [];
    if (onNewItem !== undefined) {
      const ask = (type: "folder" | "request" | "curl" | "import") => () => {
        focusRow();
        onNewItem(type, menu.id);
      };
      items.push(
        {
          id: "newRequest",
          label: t("tree.menu.newRequest"),
          shortcut: shortcutLabel("request.new"),
          disabled: projectReadOnly,
          onSelect: ask("request"),
        },
        { id: "newFolder", label: t("tree.menu.newFolder"), disabled: projectReadOnly, onSelect: ask("folder") },
        { id: "importCurl", label: t("tree.menu.importCurl"), disabled: projectReadOnly, onSelect: ask("curl") },
        { id: "importFile", label: t("tree.menu.importFile"), disabled: projectReadOnly, onSelect: ask("import") },
      );
    }
    if (menu.id === null) {
      items.push(
        { id: "exportHistoryHar", label: t("tree.menu.exportHistoryHar"), onSelect: () => void exportHistoryAsHar(stores, null) },
        { id: "exportPostmanProject", label: t("tree.menu.exportProjectPostman"), onSelect: () => void exportAsPostman(stores, null) },
      );
    }
    if (actions !== undefined && menu.id !== null) {
      const id = menu.id;
      const disabled = !editable(id);
      const state = stores.collection.getState();
      const canMove = movable(id);
      const symlink = state.nodes[id]?.readOnly === "symlink";
      const blocked = canMove ? reorderBlocked(id) : null;
      items.push(
        {
          id: "rename",
          label: t("tree.menu.rename"),
          disabled,
          onSelect: () => {
            focusRow();
            actions.rename(id);
          },
        },
        {
          id: "duplicate",
          label: t("tree.menu.duplicate"),
          shortcut: shortcutLabel("tree.duplicate"),
          disabled,
          onSelect: () => actions.duplicate(id),
        },
        ...(canCopyAsCurl(state.nodes[id])
          ? [{ id: "copyCurl", label: t("tree.menu.copyCurl"), onSelect: () => void copyAsCurl(stores, id) } satisfies ContextMenuItem]
          : []),
        ...(state.nodes[id]?.kind === "folder"
          ? [
              { id: "exportHar", label: t("tree.menu.exportHar"), onSelect: () => void exportFolderAsHar(stores, id) } satisfies ContextMenuItem,
              {
                id: "exportPostman",
                label: t("tree.menu.exportPostman"),
                onSelect: () => void exportAsPostman(stores, id),
              } satisfies ContextMenuItem,
              { id: "exportZip", label: t("tree.menu.exportZip"), onSelect: () => void exportAsZip(stores, id) } satisfies ContextMenuItem,
              { id: "runFolder", label: t("tree.menu.runFolder"), onSelect: () => openFolderRun(stores, id) } satisfies ContextMenuItem,
            ]
          : []),
        ...(hasHistory(state.nodes[id])
          ? [
              {
                id: "exportHistoryHar",
                label: t("tree.menu.exportHistoryHar"),
                onSelect: () => void exportHistoryAsHar(stores, id),
              } satisfies ContextMenuItem,
            ]
          : []),
        {
          id: "moveTo",
          label: t("tree.menu.moveTo"),
          shortcut: shortcutLabel("tree.moveTo"),
          disabled: !canMove || symlink,
          onSelect: () => {
            focusRow();
            actions.moveTo(id);
          },
        },
        {
          id: "moveUp",
          label: t("tree.menu.moveUp"),
          shortcut: shortcutLabel("tree.moveUp"),
          disabled: blocked !== null || stepTarget(state, id, -1) === null,
          title: blocked ?? undefined,
          onSelect: () => step(id, -1),
        },
        {
          id: "moveDown",
          label: t("tree.menu.moveDown"),
          shortcut: shortcutLabel("tree.moveDown"),
          disabled: blocked !== null || stepTarget(state, id, 1) === null,
          title: blocked ?? undefined,
          onSelect: () => step(id, 1),
        },
        {
          id: "delete",
          label: t("tree.menu.delete"),
          shortcut: shortcutLabel("tree.delete"),
          disabled,
          onSelect: () => {
            focusRow();
            remove(id);
          },
        },
      );
    }
    // Sorting (COL-05): a folder's own items, or those of the folder an item is in (the top level for the empty area).
    const collection = stores.collection.getState();
    const menuNode = menu.id === null ? undefined : collection.nodes[menu.id];
    const sortFolder = menuNode === undefined ? null : menuNode.kind === "folder" ? menuNode.id : menuNode.parentId;
    const current = selectSortMode(collection, sortFolder);
    const sortItem = (mode: SortMode): ContextMenuItem => ({
      id: mode === "name" ? "sortByName" : "sortManual",
      label: sortMenuLabel(t, collection.nodes, sortFolder, mode),
      checked: current === mode,
      separatorBefore: mode === "manual",
      onSelect: () => setFolderSort(stores, sortFolder, mode),
    });
    items.push(sortItem("manual"), sortItem("name"));
    return items;
  }, [menu, onNewItem, actions, editable, movable, step, stores, remove, projectReadOnly, rows, focusIndex, reorderBlocked, t]);

  const menuElement =
    menu !== null && menuItems.length > 0 ? (
      <ContextMenu label={t("tree.menu.label")} x={menu.x} y={menu.y} items={menuItems} onClose={() => setMenu(null)} />
    ) : null;

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (focusIndex < 0 || e.defaultPrevented) return;
    const row = rows[focusIndex];
    if (hasMenu && matchesShortcut(e, "tree.menu")) {
      e.preventDefault();
      e.stopPropagation();
      const rect = rowEls.current.get(row.id)?.getBoundingClientRect();
      setMenu({ x: (rect?.left ?? 0) + ROW_START, y: rect?.bottom ?? 0, id: row.id });
      return;
    }
    if (actions !== undefined) {
      const stepOrHint = (delta: -1 | 1) => () => {
        const blocked = reorderBlocked(row.id);
        if (blocked === null) step(row.id, delta);
        else if (!e.repeat) showToast({ message: blocked, icon: <IconInfo size={14} />, duration: 5000 });
      };
      const moveAction = matchesShortcut(e, "tree.moveUp")
        ? stepOrHint(-1)
        : matchesShortcut(e, "tree.moveDown")
          ? stepOrHint(1)
          : matchesShortcut(e, "tree.moveTo")
            ? () => {
                if (stores.collection.getState().nodes[row.id]?.readOnly !== "symlink") actions.moveTo(row.id);
              }
            : null;
      if (moveAction !== null) {
        // Taken even when the item cannot move, so Alt+Up/Down never move focus instead.
        e.preventDefault();
        e.stopPropagation();
        if (movable(row.id)) moveAction();
        return;
      }
      const action = matchesShortcut(e, "tree.rename")
        ? () => setRenamingId(row.id)
        : matchesShortcut(e, "tree.duplicate")
          ? () => actions.duplicate(row.id)
          : matchesShortcut(e, "tree.delete")
            ? () => remove(row.id)
            : null;
      if (action !== null) {
        // Taken even when the item cannot be edited, so Backspace never does anything else here.
        e.preventDefault();
        e.stopPropagation();
        if (!e.repeat && editable(row.id)) action();
        return;
      }
    }
    const pageRows = Math.max(1, Math.floor(height / TREE_ROW_HEIGHT) - 1);
    let handled = true;
    switch (e.key) {
      case "ArrowDown":
        moveFocus(focusIndex + 1);
        break;
      case "ArrowUp":
        moveFocus(focusIndex - 1);
        break;
      case "PageDown":
        moveFocus(focusIndex + pageRows);
        break;
      case "PageUp":
        moveFocus(focusIndex - pageRows);
        break;
      case "Home":
        moveFocus(0);
        break;
      case "End":
        moveFocus(rows.length - 1);
        break;
      case "ArrowRight":
        if (row.kind === "folder") {
          if (!row.expanded) toggle(row.id);
          else if (rows[focusIndex + 1]?.parentIndex === focusIndex) moveFocus(focusIndex + 1);
        }
        break;
      case "ArrowLeft":
        if (row.expanded) toggle(row.id);
        else if (row.parentIndex >= 0) moveFocus(row.parentIndex);
        break;
      case "Enter":
        // Opens a kept tab of a request or folder (NAV-01); folders toggle with Space and the arrows.
        openTreeItem(stores, row.id);
        break;
      case " ":
        activate(row.id);
        break;
      default:
        handled = false;
        if (e.key.length === 1 && !e.altKey && !e.ctrlKey && !e.metaKey && e.key.trim() !== "") {
          const now = Date.now();
          const ta = typeAhead.current;
          const extend = now - ta.at < TYPE_AHEAD_MS;
          ta.text = extend ? ta.text + e.key : e.key;
          ta.at = now;
          // A new search starts after the focused row; a longer one may match the focused row itself.
          const i = findByPrefix(ta.text, extend ? focusIndex : focusIndex + 1);
          if (i >= 0) moveFocus(i);
          handled = true;
        }
    }
    if (handled) {
      e.preventDefault();
      e.stopPropagation();
    }
  };

  if (status === "error") {
    return (
      <div className={styles.message}>
        <EmptyState placement="top" icon={<IconWarning size={28} strokeWidth={1.5} />} title={t("tree.empty.errorTitle")}>
          {error ?? undefined}
        </EmptyState>
      </div>
    );
  }
  if (status === "idle") {
    return (
      <div className={styles.message}>
        <EmptyState placement="top" icon={<IconCollections size={28} strokeWidth={1.5} />} title={t("tree.empty.noProjectTitle")}>
          {t("tree.empty.noProjectBody")}
        </EmptyState>
      </div>
    );
  }
  if (status === "ready" && rows.length === 0 && filter !== null && rootIds.length > 0) {
    return (
      <div className={styles.message}>
        <EmptyState placement="top" icon={<IconFilter size={28} strokeWidth={1.5} />} title={t("tree.empty.noMatchesTitle")}>
          {t("tree.empty.noMatchesBody", { query: filter.query })}{" "}
          {onClearFilter !== undefined && (
            <button type="button" className={styles.clearFilter} onClick={onClearFilter}>
              {t("tree.empty.clearFilter")}
            </button>
          )}
        </EmptyState>
        <div className={styles.srOnly} role="status">
          {t("tree.empty.noMatchesTitle")}
        </div>
      </div>
    );
  }
  if (status === "ready" && rows.length === 0) {
    return (
      <div className={styles.message} onContextMenu={onContextMenu}>
        <EmptyState placement="top" icon={<IconCollections size={28} strokeWidth={1.5} />} title={t("tree.empty.noRequestsTitle")}>
          {t("tree.empty.noRequestsBody")}
        </EmptyState>
        {menuElement}
      </div>
    );
  }

  const matchStatus =
    filter === null ? "" : t("tree.matchStatus", { count: filter.hits.size, query: filter.query });

  return (
    <>
      <div className={styles.srOnly} role="status">
        {matchStatus}
      </div>
      <div
        ref={virtual.ref}
        className={drag.indicator?.kind === "into" && drag.indicator.id === null ? `${styles.tree} ${styles.dropRoot}` : styles.tree}
        role="tree"
        aria-label={filter === null ? label : t("tree.labelFiltered", { label })}
        aria-busy={status === "loading" || undefined}
        onScroll={virtual.onScroll}
        onKeyDown={onKeyDown}
        onFocus={(e) => {
          // A row focused another way than the keyboard (a dialog or menu giving
          // focus back) becomes the focused row, so the keys act on it.
          const id = e.target.getAttribute("data-node-id");
          if (id !== null && id !== focusedId) setFocusedId(id);
        }}
        onContextMenu={onContextMenu}
        {...drag.handlers}
        {...{ [SCOPE_ATTRIBUTE]: "tree" }}
      >
        <div className={styles.sizer} style={{ height: rows.length * TREE_ROW_HEIGHT }}>
        {rendered.map((index) => {
          const row = rows[index];
          return (
            <TreeRow
              key={row.id}
              row={row}
              index={index}
              domId={`${treeId}-${index}`}
              tabbable={index === focusIndex}
              selected={row.id === selectedId}
              count={counts[row.id] ?? 0}
              onClick={onRowClick}
              onDoubleClick={onRowDoubleClick}
              onToggle={toggle}
              register={registerRow}
              renaming={row.id === renamingId}
              onRenameCommit={commitRename}
              onRenameCancel={cancelRename}
              draggable={row.id !== renamingId && drag.canDrag(row.id)}
              dragging={row.id === drag.draggingId}
              dropInto={drag.indicator?.kind === "into" && drag.indicator.id === row.id}
              hit={filter?.hits.get(row.id)}
            />
          );
        })}
        {drag.indicator?.kind === "line" && <DropLine indicator={drag.indicator} />}
        </div>
      </div>
      {menuElement}
    </>
  );
}

/** The line between rows where a dragged item would land, indented to the level it would have. */
function DropLine({ indicator }: { indicator: Extract<DropIndicator, { kind: "line" }> }) {
  return (
    <div
      className={styles.dropLine}
      style={{ top: indicator.index * TREE_ROW_HEIGHT, left: ROW_START + (indicator.level - 1) * INDENT }}
      aria-hidden="true"
    />
  );
}

interface TreeRowProps {
  row: VisibleRow;
  index: number;
  domId: string;
  tabbable: boolean;
  selected: boolean;
  count: number;
  onClick: (id: NodeId) => void;
  onDoubleClick: (id: NodeId) => void;
  onToggle: (id: NodeId) => void;
  register: (id: NodeId, el: HTMLDivElement | null) => void;
  /** The name is an input (inline rename, F2). */
  renaming: boolean;
  /** Renames the item; resolves to the error to show, or null when done. */
  onRenameCommit: (id: NodeId, name: string) => Promise<string | null>;
  /** Ends the inline rename without renaming; `refocus` gives focus back to the row. */
  onRenameCancel: (id: NodeId, refocus: boolean) => void;
  /** The row can be dragged (to move the item). */
  draggable: boolean;
  /** The row is being dragged. */
  dragging: boolean;
  /** A drop now would put the dragged item into this folder. */
  dropInto: boolean;
  /** How the row matches the sidebar filter: its matched letters are highlighted. */
  hit?: FilterHit;
}

const TreeRow = memo(function TreeRow({
  row,
  index,
  domId,
  tabbable,
  selected,
  count,
  onClick,
  onDoubleClick,
  onToggle,
  register,
  renaming,
  onRenameCommit,
  onRenameCancel,
  draggable,
  dragging,
  dropInto,
  hit,
}: TreeRowProps) {
  const t = useT();
  const node: TreeNode | undefined = useCollection((s) => s.nodes[row.id]);
  const workspaceId = useCollection((s) => s.workspaceId);
  // A folder's saved server while it listens (SRV-04): “WS :8081”.
  const serverBadge = useServers((s) => selectFolderServerBadge(s, workspaceId, row.id));
  const needsHint = node !== undefined && (node.readOnly !== "" || node.kind === "invalid");
  const problem = useCollection((s) => (needsHint ? selectNodeProblem(s, row.id)?.message : undefined));
  const ref = useCallback((el: HTMLDivElement | null) => register(row.id, el), [register, row.id]);
  if (node === undefined) return null;

  const depth = row.level - 1;
  const hint = readOnlyHint(node, problem);
  const isFolder = node.kind === "folder";
  const badge = nodeBadge(node);
  const classes = [styles.row];
  if (selected) classes.push(styles.selected);
  if (isFolder && depth === 0) classes.push(styles.topFolder);
  if (node.kind === "invalid") classes.push(styles.invalid);
  if (dragging) classes.push(styles.dragging);
  if (dropInto) classes.push(styles.dropInto);
  const nameElement = renaming ? (
    <InlineRename node={node} onCommit={onRenameCommit} onCancel={onRenameCancel} />
  ) : (
    <span className={styles.name}>
      {hit === undefined ? node.name : <Highlighted text={node.name} hits={hit.positions} className={styles.hit} />}
    </span>
  );

  return (
    <div
      ref={ref}
      id={domId}
      className={classes.join(" ")}
      style={{ top: index * TREE_ROW_HEIGHT, paddingLeft: ROW_START + depth * INDENT + (isFolder ? 0 : ITEM_EXTRA_INDENT) }}
      role="treeitem"
      aria-level={row.level}
      aria-setsize={row.setSize}
      aria-posinset={row.posInSet}
      aria-expanded={isFolder ? row.expanded : undefined}
      aria-selected={selected}
      aria-label={
        isFolder
          ? serverBadge !== null
            ? t("tree.row.folderListening", { name: node.name, count, server: serverBadge })
            : t("tree.row.folder", { name: node.name, count })
          : badge
            ? `${badge.label} ${node.name}`
            : node.name
      }
      aria-description={hint}
      tabIndex={tabbable ? 0 : -1}
      title={hint}
      data-node-id={node.id}
      data-kind={node.kind}
      draggable={draggable || undefined}
      onClick={() => onClick(node.id)}
      onDoubleClick={() => onDoubleClick(node.id)}
    >
      {isFolder ? (
        <>
          <span
            className={row.expanded ? `${styles.chevron} ${styles.open}` : styles.chevron}
            onClick={(e) => {
              // The chevron only toggles; it does not select.
              e.stopPropagation();
              onToggle(node.id);
            }}
            onDoubleClick={(e) => e.stopPropagation()}
          >
            <IconChevron direction="right" size={12} strokeWidth={2.4} />
          </span>
          <IconFolder className={styles.folderIcon} size={15} strokeWidth={1.9} />
          {nameElement}
          {node.readOnly !== "" && <IconWarning className={styles.warnIcon} size={12} />}
          {serverBadge !== null && (
            <span className={styles.serverBadge} title={t("tree.row.serverListening", { server: serverBadge })}>
              <span className={styles.serverDot} aria-hidden="true" />
              {serverBadge}
            </span>
          )}
          <span className={styles.count}>{count}</span>
        </>
      ) : (
        <>
          {badge && (
            <span className={styles.badge} data-tone={badge.tone} data-hit={hit?.method === true || undefined}>
              {badge.label}
            </span>
          )}
          {nameElement}
          {node.kind === "invalid" && <IconWarning className={styles.warnIcon} size={12} />}
        </>
      )}
    </div>
  );
});
