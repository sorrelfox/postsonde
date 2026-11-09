// Tabs store: which requests and folders are open as tabs, their order,
// which are pinned, which one is the preview tab, which is active, and the
// recently closed tabs (NAV-01). One tab per tree node, so a tab id is the
// node id; the tab's name and badge are read from the collection store, so
// a rename shows up without touching this store.
//
// Invariants: pinned tabs always come first in `order`; `previewId` is an
// open, unpinned tab or null.

import type { NodeId } from "./collection";
import { insertAt, omitKeys } from "./util";

export type TabId = NodeId;

/** A closed tab, remembered so it can be reopened where it was. */
export interface ClosedTab {
  readonly id: TabId;
  /** Its index in `order` when it closed. */
  readonly index: number;
  readonly pinned: boolean;
}

/** At most this many closed tabs are remembered (the oldest are dropped). */
export const MAX_CLOSED_TABS = 20;

export interface TabsState {
  /** Open tabs, left to right. Pinned tabs first. */
  readonly order: readonly TabId[];
  readonly pinnedIds: Readonly<Record<TabId, true>>;
  readonly activeId: TabId | null;
  /**
   * The preview tab: opened by a single click in the tree and replaced by
   * the next one, until it is kept (edited, double-clicked, pinned, moved).
   */
  readonly previewId: TabId | null;
  /** Recently closed tabs, the most recent last. */
  readonly closed: readonly ClosedTab[];
}

export const initialTabsState: TabsState = {
  order: [],
  pinnedIds: {},
  activeId: null,
  previewId: null,
  closed: [],
};

export type TabsAction =
  /**
   * Opens a tab (at `index`, default the end) and activates it. Only
   * activates if it is already open (a kept tab does not become a preview).
   * With `preview`, the new tab is the preview tab and takes the place of
   * the current preview tab, which closes without being remembered.
   */
  | { type: "tabs/open"; id: TabId; index?: number; preview?: boolean }
  | { type: "tabs/activate"; id: TabId }
  /** Makes the preview tab `id` a normal tab. */
  | { type: "tabs/keep"; id: TabId }
  /**
   * Closes tabs. If the active one closes, its right neighbour (else left)
   * becomes active. Closed tabs are remembered for reopening unless
   * `remember` is false.
   */
  | { type: "tabs/close"; ids: readonly TabId[]; remember?: boolean }
  /** Closes every unpinned tab except `id`, and activates `id`. */
  | { type: "tabs/closeOthers"; id: TabId }
  /** Moves a tab; the index is clamped to its group (pinned or unpinned). Keeps a preview tab. */
  | { type: "tabs/move"; id: TabId; toIndex: number }
  /** Pins (moves to the end of the pinned group) or unpins (start of the unpinned group). Pinning keeps a preview tab. */
  | { type: "tabs/setPinned"; id: TabId; pinned: boolean }
  /**
   * Reopens the most recently closed tab with id `id`: at its old index
   * (clamped to its group), pinned again if it was, and active. It and every
   * entry closed after it leave the closed list.
   */
  | { type: "tabs/reopen"; id: TabId }
  /** Forgets every closed tab. */
  | { type: "tabs/forgetClosed" }
  /**
   * Replaces all tabs (restored at startup). Duplicate ids are dropped;
   * pinned ids that are not open are ignored and pinned tabs move first; an
   * `activeId` that is not in `order` falls back to the first tab; a
   * `previewId` that is not an open unpinned tab is ignored.
   */
  | {
      type: "tabs/restore";
      order: readonly TabId[];
      activeId: TabId | null;
      pinnedIds?: readonly TabId[];
      previewId?: TabId | null;
      closed?: readonly ClosedTab[];
    };

function pinnedCount(order: readonly TabId[], pinnedIds: Readonly<Record<TabId, true>>): number {
  let n = 0;
  while (n < order.length && order[n] in pinnedIds) n++;
  return n;
}

function withoutClosed(closed: readonly ClosedTab[], id: TabId): readonly ClosedTab[] {
  return closed.some((entry) => entry.id === id) ? closed.filter((entry) => entry.id !== id) : closed;
}

function sameClosed(a: readonly ClosedTab[], b: readonly ClosedTab[]): boolean {
  return (
    a.length === b.length &&
    a.every((entry, i) => entry.id === b[i].id && entry.index === b[i].index && entry.pinned === b[i].pinned)
  );
}

export function tabsReducer(state: TabsState, action: TabsAction): TabsState {
  switch (action.type) {
    case "tabs/open": {
      if (state.order.includes(action.id)) {
        return state.activeId === action.id ? state : { ...state, activeId: action.id };
      }
      const closed = withoutClosed(state.closed, action.id);
      if (action.preview === true && state.previewId !== null) {
        const order = state.order.map((id) => (id === state.previewId ? action.id : id));
        return { ...state, order, activeId: action.id, previewId: action.id, closed };
      }
      const firstUnpinned = pinnedCount(state.order, state.pinnedIds);
      const index = Math.max(firstUnpinned, action.index ?? state.order.length);
      return {
        ...state,
        order: insertAt(state.order, action.id, index),
        activeId: action.id,
        previewId: action.preview === true ? action.id : state.previewId,
        closed,
      };
    }

    case "tabs/activate":
      if (state.activeId === action.id || !state.order.includes(action.id)) return state;
      return { ...state, activeId: action.id };

    case "tabs/keep":
      return state.previewId === action.id ? { ...state, previewId: null } : state;

    case "tabs/close": {
      const closing = new Set(action.ids);
      const order = state.order.filter((id) => !closing.has(id));
      if (order.length === state.order.length) return state;
      let activeId = state.activeId;
      if (activeId !== null && closing.has(activeId)) {
        const at = state.order.indexOf(activeId);
        const right = state.order.slice(at + 1).find((id) => !closing.has(id));
        const left = state.order.slice(0, at).reverse().find((id) => !closing.has(id));
        activeId = right ?? left ?? null;
      }
      let closed = state.closed;
      if (action.remember !== false) {
        // Rightmost first, so reopening them one by one puts each back at its index.
        const entries: ClosedTab[] = [];
        for (let i = state.order.length - 1; i >= 0; i--) {
          const id = state.order[i];
          if (closing.has(id)) entries.push({ id, index: i, pinned: id in state.pinnedIds });
        }
        closed = [...state.closed.filter((entry) => !closing.has(entry.id)), ...entries].slice(-MAX_CLOSED_TABS);
      }
      const previewId = state.previewId !== null && closing.has(state.previewId) ? null : state.previewId;
      return { order, pinnedIds: omitKeys(state.pinnedIds, closing), activeId, previewId, closed };
    }

    case "tabs/closeOthers": {
      if (!state.order.includes(action.id)) return state;
      const others = state.order.filter((id) => id !== action.id && !(id in state.pinnedIds));
      const closed = tabsReducer(state, { type: "tabs/close", ids: others });
      return tabsReducer(closed, { type: "tabs/activate", id: action.id });
    }

    case "tabs/move": {
      const from = state.order.indexOf(action.id);
      if (from === -1) return state;
      const without = state.order.filter((id) => id !== action.id);
      const pinned = pinnedCount(without, state.pinnedIds);
      const [min, max] = action.id in state.pinnedIds ? [0, pinned] : [pinned, without.length];
      const to = Math.max(min, Math.min(action.toIndex, max));
      if (to === from) return state;
      const previewId = state.previewId === action.id ? null : state.previewId;
      return { ...state, order: insertAt(without, action.id, to), previewId };
    }

    case "tabs/setPinned": {
      if (!state.order.includes(action.id) || (action.id in state.pinnedIds) === action.pinned) return state;
      const pinnedIds = action.pinned
        ? { ...state.pinnedIds, [action.id]: true as const }
        : omitKeys(state.pinnedIds, [action.id]);
      // Either way the tab lands on the pinned/unpinned boundary.
      const without = state.order.filter((id) => id !== action.id);
      const order = insertAt(without, action.id, pinnedCount(without, pinnedIds));
      const previewId = action.pinned && state.previewId === action.id ? null : state.previewId;
      return { ...state, order, pinnedIds, previewId };
    }

    case "tabs/reopen": {
      let k = state.closed.length - 1;
      while (k >= 0 && state.closed[k].id !== action.id) k--;
      if (k < 0) return state;
      const entry = state.closed[k];
      const closed = state.closed.slice(0, k);
      if (state.order.includes(entry.id)) return { ...state, activeId: entry.id, closed };
      const pinnedIds = entry.pinned ? { ...state.pinnedIds, [entry.id]: true as const } : state.pinnedIds;
      const pinned = pinnedCount(state.order, state.pinnedIds);
      const [min, max] = entry.pinned ? [0, pinned] : [pinned, state.order.length];
      const index = Math.max(min, Math.min(entry.index, max));
      return { ...state, order: insertAt(state.order, entry.id, index), pinnedIds, activeId: entry.id, closed };
    }

    case "tabs/forgetClosed":
      return state.closed.length === 0 ? state : { ...state, closed: [] };

    case "tabs/restore": {
      const unique = [...new Set(action.order)];
      const pinnedIds: Record<TabId, true> = {};
      for (const id of action.pinnedIds ?? []) if (unique.includes(id)) pinnedIds[id] = true;
      const order = [...unique.filter((id) => id in pinnedIds), ...unique.filter((id) => !(id in pinnedIds))];
      const activeId = action.activeId !== null && order.includes(action.activeId) ? action.activeId : (order[0] ?? null);
      const preview = action.previewId ?? null;
      const previewId = preview !== null && order.includes(preview) && !(preview in pinnedIds) ? preview : null;
      const closed = (action.closed ?? []).slice(-MAX_CLOSED_TABS);
      const same =
        activeId === state.activeId &&
        previewId === state.previewId &&
        order.length === state.order.length &&
        order.every((id, i) => id === state.order[i]) &&
        Object.keys(pinnedIds).length === Object.keys(state.pinnedIds).length &&
        Object.keys(pinnedIds).every((id) => id in state.pinnedIds) &&
        sameClosed(closed, state.closed);
      return same ? state : { order, pinnedIds, activeId, previewId, closed };
    }

    default:
      return state;
  }
}

export function selectIsPinned(state: TabsState, id: TabId): boolean {
  return id in state.pinnedIds;
}

export function selectIsPreview(state: TabsState, id: TabId): boolean {
  return state.previewId === id;
}
