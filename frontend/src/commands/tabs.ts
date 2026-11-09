// Tab commands (NAV-01): open tree requests and folders as tabs (a
// reusable preview tab or a kept one), switch, pin, close and reopen them,
// and remember the open tabs per workspace so they come back when the app
// starts. See docs/frontend.md (Tabs).
//
// Reordering touches only the tabs store, so components dispatch
// `tabs/move` themselves.

import { workspaceLocalTabs, type SavedTabs, type TabsPersistence } from "../lib/tabsPersistence";
import type { NodeId, Stores, TabId, TabsState } from "../stores";
import { onWorkspaceLeaving } from "./workspaceLeave";

let persistence: TabsPersistence = workspaceLocalTabs();

/** Replaces where open tabs are remembered (tests). Returns the previous backend. */
export function setTabsPersistence(next: TabsPersistence): TabsPersistence {
  const previous = persistence;
  persistence = next;
  return previous;
}

/**
 * Called with the ids of tabs that are about to close, and the stores.
 * Listeners run synchronously, before the tabs close and before their
 * drafts are dropped, so they can still read the drafts (e.g. to flush a
 * pending auto-save). Async work they start must not rely on the
 * drafts still being in the store.
 */
export type TabClosedListener = (ids: readonly TabId[], stores: Stores) => void;

const tabClosedListeners = new Set<TabClosedListener>();

/**
 * The hook point for work that must happen when tabs close: auto-save
 * registers here to flush the pending save of each closed item.
 * Returns the unsubscribe function.
 */
export function onTabClosed(listener: TabClosedListener): () => void {
  tabClosedListeners.add(listener);
  return () => {
    tabClosedListeners.delete(listener);
  };
}

export interface OpenTabOptions {
  /**
   * Open as the preview tab, which the next preview replaces (a single
   * click in the tree). Without it the tab is kept, and opening a preview
   * tab this way keeps it (a double-click or Enter).
   */
  readonly preview?: boolean;
}

function notifyClosing(stores: Stores, ids: readonly TabId[]): void {
  for (const listener of [...tabClosedListeners]) {
    try {
      listener(ids, stores);
    } catch (err: unknown) {
      console.warn("tabs: a tab-closed listener failed", err);
    }
  }
}

function openNodeTab(stores: Stores, id: NodeId, preview: boolean): void {
  stores.collection.dispatch({ type: "collection/select", id });
  const tabs = stores.tabs.getState();
  const replaced = preview && !tabs.order.includes(id) ? tabs.previewId : null;
  if (replaced !== null) {
    // The preview tab closes (not remembered for reopening): flush it like any close.
    notifyClosing(stores, [replaced]);
    stores.tabs.dispatch({ type: "tabs/open", id, preview: true });
    stores.drafts.dispatch({ type: "drafts/discard", ids: [replaced] });
    return;
  }
  stores.tabs.dispatch({ type: "tabs/open", id, preview });
  if (!preview) stores.tabs.dispatch({ type: "tabs/keep", id });
}

/** Selects a request in the tree and opens (or focuses) its tab. */
export function openRequest(stores: Stores, id: NodeId, options: OpenTabOptions = {}): void {
  openNodeTab(stores, id, options.preview === true);
  // The request editor loads the draft when the tab shows it (useRequestDraft).
}

/** Selects a folder in the tree and opens (or focuses) its DIR tab. */
export function openFolder(stores: Stores, id: NodeId, options: OpenTabOptions = {}): void {
  openNodeTab(stores, id, options.preview === true);
}

/** Makes the preview tab `id` a normal tab. */
export function keepTab(stores: Stores, id: TabId): void {
  stores.tabs.dispatch({ type: "tabs/keep", id });
}

/** Pins or unpins an open tab. Pinned tabs stay on the left, and pinning keeps a preview tab. */
export function setTabPinned(stores: Stores, id: TabId, pinned: boolean): void {
  stores.tabs.dispatch({ type: "tabs/setPinned", id, pinned });
}

/** Makes an open tab the active one and selects its item in the tree. */
export function activateTab(stores: Stores, id: TabId): void {
  if (!stores.tabs.getState().order.includes(id)) return;
  stores.tabs.dispatch({ type: "tabs/activate", id });
  if (id in stores.collection.getState().nodes) {
    stores.collection.dispatch({ type: "collection/select", id });
  }
}

export interface CloseTabsOptions {
  /** Remember the tabs for reopening (default true). */
  readonly remember?: boolean;
}

/**
 * Closes tabs and drops their drafts. Ids that are not open are ignored.
 * The `onTabClosed` listeners run first. If the active tab closes, its right
 * neighbour becomes active, else its left one. Pinned tabs close too: only
 * the bulk commands below leave them open.
 */
export function closeTabs(stores: Stores, ids: readonly TabId[], options: CloseTabsOptions = {}): void {
  const open = new Set(stores.tabs.getState().order);
  const closing = [...new Set(ids)].filter((id) => open.has(id));
  if (closing.length === 0) return;
  notifyClosing(stores, closing);
  stores.tabs.dispatch({ type: "tabs/close", ids: closing, remember: options.remember });
  stores.drafts.dispatch({ type: "drafts/discard", ids: closing });
}

/** Closes every tab except `id` (and pinned tabs), and activates `id`. */
export function closeOtherTabs(stores: Stores, id: TabId): void {
  const { order, pinnedIds } = stores.tabs.getState();
  if (!order.includes(id)) return;
  closeTabs(stores, order.filter((other) => other !== id && !(other in pinnedIds)));
  activateTab(stores, id);
}

/** Closes every tab except pinned ones. */
export function closeAllTabs(stores: Stores): void {
  const { order, pinnedIds } = stores.tabs.getState();
  closeTabs(stores, order.filter((id) => !(id in pinnedIds)));
}

/** Closes the unpinned tabs to the right of `id`. */
export function closeTabsToTheRight(stores: Stores, id: TabId): void {
  const { order, pinnedIds } = stores.tabs.getState();
  const at = order.indexOf(id);
  if (at === -1) return;
  closeTabs(stores, order.slice(at + 1).filter((other) => !(other in pinnedIds)));
}

/**
 * Reopens the most recently closed tab whose item is still in the tree and
 * not open: at its old position, pinned again if it was, active and
 * selected in the tree. Closed tabs whose item is gone are skipped and
 * forgotten. Returns false when there was nothing to reopen.
 */
export function reopenClosedTab(stores: Stores): boolean {
  const { closed, order } = stores.tabs.getState();
  const { nodes } = stores.collection.getState();
  for (let k = closed.length - 1; k >= 0; k--) {
    const { id } = closed[k];
    if (!(id in nodes) || order.includes(id)) continue;
    stores.tabs.dispatch({ type: "tabs/reopen", id });
    stores.collection.dispatch({ type: "collection/select", id });
    return true;
  }
  stores.tabs.dispatch({ type: "tabs/forgetClosed" });
  return false;
}

/** Delay before a change of the open tabs is saved. */
export const TABS_SAVE_DELAY_MS = 300;

function savedTabs(state: TabsState): SavedTabs {
  return {
    order: state.order,
    activeId: state.activeId,
    pinnedIds: state.order.filter((id) => id in state.pinnedIds),
    previewId: state.previewId,
    closed: state.closed,
  };
}

/**
 * Remembers the open tabs per workspace and restores them:
 *
 * - When a project's tree is loaded for the first time (status "ready" for
 *   a workspace whose tabs were not restored yet), its saved tabs (order,
 *   active, pinned, preview and closed tabs) are restored. Tabs whose node
 *   no longer exists are dropped. The closed tabs of another project are
 *   forgotten.
 * - When the tree reloads (another node set), tabs whose node is gone are
 *   closed through `closeTabs`, without being remembered. Names and badges
 *   need nothing: the tab bar reads them from the collection store.
 * - The first edit of the preview tab's request (its draft turns dirty)
 *   keeps the tab.
 * - Changes to the tabs are saved 300ms after the last one, and when the
 *   page is hidden, before the project is left (onWorkspaceLeaving, STO-06),
 *   when the project changes or on stop. They go to the workspace's local
 *   directory (lib/tabsPersistence.ts), never into the project folder.
 *   Nothing is saved while no tree is loaded, so closing a project (which
 *   clears the tree, then closes its tabs) keeps its saved tabs.
 *
 * Returns the stop function.
 */
export function followTabs(stores: Stores): () => void {
  let restoredFor: string | null = null;
  let lastNodes: unknown = null;
  let lastTabs = stores.tabs.getState();
  let pending: { workspaceId: string; tabs: SavedTabs } | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;

  const flush = () => {
    if (timer !== null) clearTimeout(timer);
    timer = null;
    if (pending === null) return;
    persistence.save(pending.workspaceId, pending.tabs);
    pending = null;
  };

  const onCollection = () => {
    const tree = stores.collection.getState();
    if (tree.status !== "ready" || tree.workspaceId === null) {
      // Another project (or none): the next ready tree restores its own tabs.
      if (restoredFor !== null && tree.workspaceId !== restoredFor) {
        flush();
        restoredFor = null;
        lastNodes = null;
      }
      return;
    }
    if (tree.nodes === lastNodes && restoredFor === tree.workspaceId) return;
    lastNodes = tree.nodes;

    if (restoredFor !== tree.workspaceId) {
      flush();
      restoredFor = tree.workspaceId;
      const saved = persistence.load(tree.workspaceId);
      const current = stores.tabs.getState();
      const order = [...(saved?.order ?? []), ...current.order].filter((id) => id in tree.nodes);
      stores.tabs.dispatch({
        type: "tabs/restore",
        order,
        activeId: current.activeId ?? saved?.activeId ?? null,
        pinnedIds: [...(saved?.pinnedIds ?? []), ...Object.keys(current.pinnedIds)],
        previewId: current.previewId ?? saved?.previewId ?? null,
        closed: saved?.closed ?? [],
      });
      return;
    }

    const gone = stores.tabs.getState().order.filter((id) => !(id in tree.nodes));
    if (gone.length > 0) closeTabs(stores, gone, { remember: false });
  };

  const onDrafts = () => {
    const { previewId } = stores.tabs.getState();
    if (previewId !== null && stores.drafts.getState().byId[previewId]?.dirty === true) keepTab(stores, previewId);
  };

  const onTabs = () => {
    const tabs = stores.tabs.getState();
    if (tabs === lastTabs) return;
    lastTabs = tabs;
    const tree = stores.collection.getState();
    if (restoredFor === null || tree.workspaceId !== restoredFor) return;
    pending = { workspaceId: restoredFor, tabs: savedTabs(tabs) };
    if (timer !== null) clearTimeout(timer);
    timer = setTimeout(flush, TABS_SAVE_DELAY_MS);
  };

  const stopCollection = stores.collection.subscribe(onCollection);
  const stopTabs = stores.tabs.subscribe(onTabs);
  const stopDrafts = stores.drafts.subscribe(onDrafts);
  // Before another project opens: save this one's tabs while Go still has it open.
  const stopLeaving = onWorkspaceLeaving((leaving) => {
    if (leaving === stores) flush();
  });
  onCollection();

  const onHidden = () => {
    if (document.visibilityState === "hidden") flush();
  };
  window.addEventListener("pagehide", flush);
  document.addEventListener("visibilitychange", onHidden);

  return () => {
    stopCollection();
    stopTabs();
    stopDrafts();
    stopLeaving();
    window.removeEventListener("pagehide", flush);
    document.removeEventListener("visibilitychange", onHidden);
    flush();
  };
}
