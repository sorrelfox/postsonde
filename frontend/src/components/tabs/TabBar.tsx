import {
  memo,
  useCallback,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type MouseEvent,
  type PointerEvent,
  type WheelEvent,
} from "react";
import {
  activateTab,
  closeAllTabs,
  closeOtherTabs,
  closeTabs,
  closeTabsToTheRight,
  keepTab,
  reopenClosedTab,
  setTabPinned,
} from "../../commands/tabs";
import { useT } from "../../i18n/react";
import { shortcutLabel } from "../../lib/shortcuts";
import { DELETED_READ_ONLY, useCollection, useStores, useTabs, type TabId } from "../../stores";
import { ContextMenu, type ContextMenuItem } from "../common/ContextMenu";
import { IconClose, IconPin, IconPlus } from "../common/icons";
import { tabBadge } from "./tabBadge";
import styles from "./TabBar.module.css";
import { isCloseTabKey, isContextMenuKey, isPlainKey, tabMoveDirection } from "./tabKeys";
import { useScratchActions } from "./useScratchActions";

/** The id of the one tab panel (TabPanel); the active tab's `aria-controls`. */
export const TAB_PANEL_ID = "postsonde-tab-panel";

/** The DOM id of a tab, for `aria-labelledby` on the panel. */
export function tabElementId(id: TabId): string {
  return `postsonde-tab-${encodeURIComponent(id)}`;
}

/** A pointer must move this many px before a press on a tab becomes a drag. */
export const TAB_DRAG_THRESHOLD = 4;

interface DragState {
  readonly id: TabId;
  readonly pointerId: number;
  readonly startX: number;
  readonly el: HTMLElement;
  dragging: boolean;
  /** Where the tab would land: its index among the other tabs. */
  toIndex: number | null;
}

interface MenuState {
  readonly id: TabId;
  readonly x: number;
  readonly y: number;
}

export interface TabBarProps {
  /** Accessible name of the tab list. */
  label?: string;
}

function tabIdOf(target: EventTarget | null): TabId | null {
  if (!(target instanceof Element)) return null;
  const el = target.closest<HTMLElement>("[role='tab'][data-tab-id]");
  return el?.dataset.tabId ?? null;
}

/**
 * The open requests and folders as tabs (NAV-01), above the panes:
 * method or kind badge and name, a close button, the active tab
 * underlined in the accent colour (mockup). Names and badges come from the
 * collection store, so renames show up at once. The preview tab's name is
 * in italics; pinned tabs come first, narrower, with a pin instead of the
 * close button.
 *
 * - Mouse: press to activate, drag to reorder within the pinned or unpinned
 *   group (a marker shows where the tab lands; Escape cancels), double-click
 *   to keep a preview tab, middle-click or the close button to close
 *   (not a pinned tab), right-click for the tab menu (close, pin, reopen).
 * - Keyboard (WAI-ARIA tabs, selection follows focus): Left/Right (wrapping),
 *   Home/End; Delete closes; Shift+F10 or the Menu key opens the menu;
 *   Ctrl+Shift+PageUp/PageDown or Alt+Shift+Left/Right moves the tab (tabKeys.ts).
 * - Overflow scrolls horizontally (the wheel scrolls it too) and keeps the
 *   active tab in view.
 * - Scratch requests (COL-06): the "+" after the tabs opens the New scratch
 *   request menu; a scratch tab shows a "scratch" tag, and its menu has
 *   Save to… and Delete.
 */
export function TabBar({ label }: TabBarProps) {
  const t = useT();
  const stores = useStores();
  const order = useTabs((s) => s.order);
  const activeId = useTabs((s) => s.activeId);
  const pinnedIds = useTabs((s) => s.pinnedIds);
  const previewId = useTabs((s) => s.previewId);
  const hasClosed = useTabs((s) => s.closed.length > 0);
  const nodes = useCollection((s) => s.nodes);
  const scratchIds = useCollection((s) => s.scratchIds);
  const scratch = useScratchActions();
  // A tab whose node is gone is closed by followTabs; until then it is not shown.
  const tabs = useMemo(() => order.filter((id) => id in nodes), [order, nodes]);

  const listRef = useRef<HTMLDivElement>(null);
  const tabEls = useRef(new Map<TabId, HTMLDivElement>());
  const drag = useRef<DragState | null>(null);
  const pendingFocus = useRef(false);
  const [draggingId, setDraggingId] = useState<TabId | null>(null);
  /** The x of the drop marker in the list's scroll coordinates while a drag would move the tab, else null. */
  const [dropX, setDropX] = useState<number | null>(null);
  const [menu, setMenu] = useState<MenuState | null>(null);

  const tabbableId = activeId !== null && tabs.includes(activeId) ? activeId : (tabs[0] ?? null);

  const register = useCallback((id: TabId, el: HTMLDivElement | null) => {
    if (el) tabEls.current.set(id, el);
    else tabEls.current.delete(id);
  }, []);

  // Keep the active tab in view, and move focus to it after a keyboard close.
  useLayoutEffect(() => {
    const list = listRef.current;
    const el = activeId === null ? undefined : tabEls.current.get(activeId);
    if (list && el) {
      const left = el.offsetLeft;
      const right = left + el.offsetWidth;
      if (left < list.scrollLeft) list.scrollLeft = left;
      else if (right > list.scrollLeft + list.clientWidth) list.scrollLeft = right - list.clientWidth;
    }
  }, [activeId, tabs]);

  useLayoutEffect(() => {
    if (!pendingFocus.current) return;
    pendingFocus.current = false;
    if (tabbableId !== null) tabEls.current.get(tabbableId)?.focus({ preventScroll: true });
  });

  const focusTab = (id: TabId) => {
    activateTab(stores, id);
    tabEls.current.get(id)?.focus({ preventScroll: true });
  };

  /** Runs a close command; if focus was on a tab (or `refocus`), focus goes to the tab that is active after. */
  const closeThen = useCallback(
    (close: () => void, refocus = false) => {
      if (refocus || listRef.current?.contains(document.activeElement) === true) pendingFocus.current = true;
      close();
    },
    [],
  );

  const onCloseButton = useCallback((id: TabId) => closeThen(() => closeTabs(stores, [id])), [closeThen, stores]);

  const openMenuAt = (id: TabId, x: number, y: number) => setMenu({ id, x, y });

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const id = tabIdOf(e.target);
    if (id === null || e.defaultPrevented) return;
    const index = tabs.indexOf(id);
    const direction = tabMoveDirection(e);
    if (direction !== 0) {
      stores.tabs.dispatch({ type: "tabs/move", id, toIndex: stores.tabs.getState().order.indexOf(id) + direction });
    } else if (isContextMenuKey(e)) {
      const rect = e.currentTarget.ownerDocument.getElementById(tabElementId(id))?.getBoundingClientRect();
      openMenuAt(id, rect?.left ?? 0, rect?.bottom ?? 0);
    } else if (isCloseTabKey(e)) {
      closeThen(() => closeTabs(stores, [id]), true);
    } else if (e.key === "Escape" && drag.current?.dragging === true) {
      cancelDrag();
    } else if (isPlainKey(e) && (e.key === "ArrowLeft" || e.key === "ArrowRight")) {
      const step = e.key === "ArrowLeft" ? -1 : 1;
      focusTab(tabs[(index + step + tabs.length) % tabs.length]);
    } else if (isPlainKey(e) && e.key === "Home") {
      focusTab(tabs[0]);
    } else if (isPlainKey(e) && e.key === "End") {
      focusTab(tabs[tabs.length - 1]);
    } else if (isPlainKey(e) && (e.key === "Enter" || e.key === " ")) {
      activateTab(stores, id);
    } else {
      return;
    }
    e.preventDefault();
    e.stopPropagation();
  };

  const endDrag = () => {
    drag.current = null;
    setDraggingId(null);
    setDropX(null);
  };

  // Nothing moves until the drop, so a cancelled drag only has to forget itself.
  const cancelDrag = endDrag;

  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    const id = tabIdOf(e.target);
    if (id === null) return;
    if (e.button === 1) {
      // No autoscroll: a middle click closes the tab (auxclick).
      e.preventDefault();
      return;
    }
    if (e.button !== 0) return;
    activateTab(stores, id);
    const el = tabEls.current.get(id);
    if (!el) return;
    drag.current = { id, pointerId: e.pointerId, startX: e.clientX, el, dragging: false, toIndex: null };
  };

  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (!d || e.pointerId !== d.pointerId) return;
    if (!d.dragging) {
      if (Math.abs(e.clientX - d.startX) < TAB_DRAG_THRESHOLD) return;
      d.dragging = true;
      d.el.setPointerCapture?.(e.pointerId);
      setDraggingId(d.id);
    }
    // The tabs stay in place during the drag (moving DOM nodes could drop
    // the pointer capture); a marker shows where the tab lands. Its index is
    // the number of other tabs whose middle is left of the pointer.
    const state = stores.tabs.getState();
    const order = state.order;
    const others = order.filter((id) => id !== d.id);
    let toIndex = 0;
    others.forEach((id, k) => {
      const rect = tabEls.current.get(id)?.getBoundingClientRect();
      if (rect && rect.left + rect.width / 2 < e.clientX) toIndex = k + 1;
    });
    // A tab stays in its group: pinned tabs among the pinned ones, the others after them.
    const pinnedOthers = others.filter((id) => id in state.pinnedIds).length;
    toIndex = d.id in state.pinnedIds ? Math.min(toIndex, pinnedOthers) : Math.max(toIndex, pinnedOthers);
    if (toIndex === order.indexOf(d.id)) {
      d.toIndex = null;
      setDropX(null);
      return;
    }
    d.toIndex = toIndex;
    const next = tabEls.current.get(others[toIndex] ?? "");
    const last = tabEls.current.get(others[others.length - 1] ?? "");
    setDropX(next ? next.offsetLeft : last ? last.offsetLeft + last.offsetWidth : 0);
  };

  const onPointerUp = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (!d || e.pointerId !== d.pointerId) return;
    if (d.dragging && d.toIndex !== null) stores.tabs.dispatch({ type: "tabs/move", id: d.id, toIndex: d.toIndex });
    endDrag();
  };

  const onPointerCancel = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (!d || e.pointerId !== d.pointerId) return;
    cancelDrag();
  };

  const onAuxClick = (e: MouseEvent<HTMLDivElement>) => {
    const id = tabIdOf(e.target);
    if (id === null || e.button !== 1) return;
    e.preventDefault();
    if (id in stores.tabs.getState().pinnedIds) return;
    closeThen(() => closeTabs(stores, [id]));
  };

  const onDoubleClick = (e: MouseEvent<HTMLDivElement>) => {
    const id = tabIdOf(e.target);
    if (id !== null) keepTab(stores, id);
  };

  const onContextMenu = (e: MouseEvent<HTMLDivElement>) => {
    const id = tabIdOf(e.target);
    if (id === null) return;
    e.preventDefault();
    openMenuAt(id, e.clientX, e.clientY);
  };

  const onWheel = (e: WheelEvent<HTMLDivElement>) => {
    // A vertical wheel scrolls the tabs sideways.
    if (e.deltaX === 0 && e.deltaY !== 0) e.currentTarget.scrollLeft += e.deltaY;
  };

  const menuItems = useMemo<ContextMenuItem[]>(() => {
    if (menu === null) return [];
    const { id } = menu;
    const pinned = id in pinnedIds;
    const at = tabs.indexOf(id);
    const unpinned = tabs.filter((other) => !(other in pinnedIds));
    const items: ContextMenuItem[] = [
      {
        id: "close",
        label: t("tabs.tabBar.menu.close"),
        shortcut: id === activeId ? shortcutLabel("tab.close") : undefined,
        onSelect: () => closeThen(() => closeTabs(stores, [id]), true),
      },
      {
        id: "closeOthers",
        label: t("tabs.tabBar.menu.closeOthers"),
        disabled: !unpinned.some((other) => other !== id),
        onSelect: () => closeThen(() => closeOtherTabs(stores, id), true),
      },
      {
        id: "closeRight",
        label: t("tabs.tabBar.menu.closeRight"),
        disabled: !unpinned.some((other) => tabs.indexOf(other) > at),
        onSelect: () => closeThen(() => closeTabsToTheRight(stores, id), true),
      },
      {
        id: "closeAll",
        label: t("tabs.tabBar.menu.closeAll"),
        disabled: unpinned.length === 0,
        onSelect: () => closeThen(() => closeAllTabs(stores), true),
      },
    ];
    if (id === previewId) items.push({ id: "keep", label: t("tabs.tabBar.menu.keepOpen"), onSelect: () => keepTab(stores, id) });
    if (scratchIds.includes(id)) {
      items.push(
        {
          id: "saveTo",
          label: t("tabs.tabBar.menu.saveTo"),
          disabled: !scratch.canSave,
          separatorBefore: true,
          onSelect: () => scratch.saveTo(id),
        },
        { id: "deleteScratch", label: t("tabs.tabBar.menu.deleteScratch"), onSelect: () => scratch.remove(id) },
      );
    }
    items.push(
      {
        id: "pin",
        label: pinned ? t("tabs.tabBar.menu.unpin") : t("tabs.tabBar.menu.pin"),
        onSelect: () => setTabPinned(stores, id, !pinned),
      },
      {
        id: "reopen",
        label: t("tabs.tabBar.menu.reopen"),
        shortcut: shortcutLabel("tab.reopenClosed"),
        disabled: !hasClosed,
        onSelect: () => closeThen(() => void reopenClosedTab(stores), true),
      },
    );
    return items;
  }, [menu, tabs, pinnedIds, previewId, activeId, hasClosed, stores, closeThen, scratchIds, scratch, t]);

  return (
    <div className={styles.bar}>
      <div
        ref={listRef}
        className={styles.list}
        role="tablist"
        aria-label={label ?? t("tabs.tabBar.label")}
        aria-orientation="horizontal"
        data-shortcut-scope="tabs"
        onKeyDown={onKeyDown}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerCancel}
        onLostPointerCapture={onPointerCancel}
        onAuxClick={onAuxClick}
        onDoubleClick={onDoubleClick}
        onContextMenu={onContextMenu}
        onWheel={onWheel}
      >
        {tabs.map((id) => (
          <TabItem
            key={id}
            id={id}
            active={id === activeId}
            tabbable={id === tabbableId}
            dragging={id === draggingId}
            pinned={id in pinnedIds}
            preview={id === previewId}
            scratch={scratchIds.includes(id)}
            onClose={onCloseButton}
            register={register}
          />
        ))}
        {dropX !== null && <div className={styles.dropMarker} style={{ left: dropX }} aria-hidden="true" />}
      </div>
      <button
        type="button"
        className={styles.newTab}
        aria-label={t("tabs.tabBar.newScratch")}
        aria-haspopup="menu"
        title={t("tabs.tabBar.newScratch")}
        onClick={(e) => {
          const rect = e.currentTarget.getBoundingClientRect();
          scratch.openNewMenu(rect.left, rect.bottom);
        }}
      >
        <IconPlus size={14} />
      </button>
      {menu !== null && (
        <ContextMenu label={t("tabs.tabBar.menu.label")} x={menu.x} y={menu.y} items={menuItems} onClose={() => setMenu(null)} />
      )}
      {scratch.elements}
    </div>
  );
}

interface TabItemProps {
  id: TabId;
  active: boolean;
  tabbable: boolean;
  dragging: boolean;
  pinned: boolean;
  preview: boolean;
  /** A scratch request (COL-06), not in the project. */
  scratch: boolean;
  onClose: (id: TabId) => void;
  register: (id: TabId, el: HTMLDivElement | null) => void;
}

const TabItem = memo(function TabItem({ id, active, tabbable, dragging, pinned, preview, scratch, onClose, register }: TabItemProps) {
  const t = useT();
  const node = useCollection((s) => s.nodes[id]);
  const ref = useCallback((el: HTMLDivElement | null) => register(id, el), [register, id]);
  if (node === undefined) return null;
  const badge = tabBadge(node);
  const classes = [styles.tab];
  if (dragging) classes.push(styles.dragging);
  if (pinned) classes.push(styles.pinned);
  if (preview) classes.push(styles.preview);
  // The file was deleted outside PostSonde; the tab keeps its content (STO-05).
  const deleted = node.readOnly === DELETED_READ_ONLY;
  const states: string[] = [];
  if (scratch) states.push(t("tabs.tabBar.tab.state.scratch"));
  if (deleted) states.push(t("tabs.tabBar.tab.state.deleted"));
  if (pinned) states.push(t("tabs.tabBar.tab.state.pinned"));
  else if (preview) states.push(t("tabs.tabBar.tab.state.preview"));
  const ariaLabel = states.reduce(
    (text, state) => t("tabs.tabBar.tab.withState", { label: text, state }),
    t("tabs.tabBar.tab.label", { badge: badge.label, name: node.name }),
  );

  return (
    <div
      ref={ref}
      id={tabElementId(id)}
      className={classes.join(" ")}
      role="tab"
      aria-selected={active}
      aria-controls={active ? TAB_PANEL_ID : undefined}
      tabIndex={tabbable ? 0 : -1}
      aria-label={ariaLabel}
      title={scratch ? t("tabs.tabBar.tab.scratchTitle") : node.path}
      data-tab-id={id}
      data-pinned={pinned || undefined}
      data-preview={preview || undefined}
    >
      <span className={styles.badge} data-tone={badge.tone}>
        {badge.label}
      </span>
      <span className={styles.name}>{node.name}</span>
      {scratch && (
        <span className={styles.scratchTag} aria-hidden="true">
          {t("tabs.tabBar.tab.scratchTag")}
        </span>
      )}
      {deleted && (
        <span className={styles.deletedTag} aria-hidden="true">
          {t("tabs.tabBar.tab.deletedTag")}
        </span>
      )}
      {pinned ? (
        <span className={styles.pin} aria-hidden="true">
          <IconPin size={11} />
        </span>
      ) : (
        // Mouse only: keyboard users close with Delete or the menu, and a tab must not contain focusable controls.
        <button
        type="button"
        className={styles.close}
        tabIndex={-1}
        aria-hidden="true"
        title={t("tabs.tabBar.tab.close", { name: node.name })}
        data-close-tab={id}
        onPointerDown={(e) => e.stopPropagation()}
        onClick={(e) => {
          e.stopPropagation();
          onClose(id);
        }}
      >
        <IconClose size={11} strokeWidth={2.4} />
      </button>
      )}
    </div>
  );
});
