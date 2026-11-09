// Where the open tabs are remembered per workspace, so they come back when
// the app starts (NAV-01). Framework-free: no React, no stores.
// commands/tabs.ts is the only caller. A WorkspaceLocalStore
// (./workspaceLocal.ts) under the key "tabs" of the workspace's local UI
// state (STO-06); what older builds kept in localStorage is still read.
//
// Stored JSON:
// - version 1 (no `version` member): { order, activeId }
// - version 2: { version: 2, order, activeId, pinnedIds, previewId, closed }
// Version 1 data still loads, with no pinned, preview or closed tabs.

import {
  decodeStringArray,
  localStorageWorkspaceStore,
  workspaceLocalStore,
  type WorkspaceLocalStore,
} from "./workspaceLocal";

export interface SavedClosedTab {
  readonly id: string;
  /** Its index among the open tabs when it closed. */
  readonly index: number;
  readonly pinned: boolean;
}

export interface SavedTabs {
  /** Open tab ids (tree node ids), left to right. */
  readonly order: readonly string[];
  readonly activeId: string | null;
  /** The pinned tabs among `order`. */
  readonly pinnedIds: readonly string[];
  /** The preview tab, an unpinned tab of `order`, or null. */
  readonly previewId: string | null;
  /** Recently closed tabs, the most recent last. */
  readonly closed: readonly SavedClosedTab[];
}

export type TabsPersistence = WorkspaceLocalStore<SavedTabs>;

export const TABS_KEY_PREFIX = "postsonde.tabs.";

/** The version written by this build. */
export const TABS_FORMAT_VERSION = 2;

/** At most this many tabs are remembered per workspace. */
export const MAX_SAVED_TABS = 500;

/** At most this many closed tabs are remembered per workspace. */
export const MAX_SAVED_CLOSED_TABS = 20;

function decodeClosed(raw: unknown): SavedClosedTab[] {
  if (!Array.isArray(raw)) return [];
  const closed: SavedClosedTab[] = [];
  for (const item of raw) {
    if (typeof item !== "object" || item === null) continue;
    const { id, index, pinned } = item as Record<string, unknown>;
    if (typeof id !== "string" || typeof index !== "number" || !Number.isInteger(index) || index < 0) continue;
    closed.push({ id, index, pinned: pinned === true });
  }
  return closed.slice(-MAX_SAVED_CLOSED_TABS);
}

/**
 * Reads back saved tabs of version 1 or 2: drops non-string and duplicate
 * ids, an active or preview id that is not open, pinned ids that are not
 * open and malformed closed entries. Returns null for anything else,
 * including a newer version.
 */
export function decodeSavedTabs(raw: unknown): SavedTabs | null {
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) return null;
  const record = raw as Record<string, unknown>;
  const version = record.version ?? 1;
  if (version !== 1 && version !== TABS_FORMAT_VERSION) return null;
  const ids = decodeStringArray(record.order);
  if (ids === null) return null;
  const order = [...new Set(ids)].slice(0, MAX_SAVED_TABS);
  const activeId = typeof record.activeId === "string" && order.includes(record.activeId) ? record.activeId : null;
  if (version === 1) return { order, activeId, pinnedIds: [], previewId: null, closed: [] };

  const pinnedIds = [...new Set(decodeStringArray(record.pinnedIds) ?? [])].filter((id) => order.includes(id));
  const previewId =
    typeof record.previewId === "string" && order.includes(record.previewId) && !pinnedIds.includes(record.previewId)
      ? record.previewId
      : null;
  return { order, activeId, pinnedIds, previewId, closed: decodeClosed(record.closed) };
}

function encodeSavedTabs({ order, activeId, pinnedIds, previewId, closed }: SavedTabs): unknown {
  const kept = order.slice(0, MAX_SAVED_TABS);
  return {
    version: TABS_FORMAT_VERSION,
    order: kept,
    activeId,
    pinnedIds: pinnedIds.filter((id) => kept.includes(id)),
    previewId,
    closed: closed.slice(-MAX_SAVED_CLOSED_TABS),
  };
}

/** The key of the open tabs in the workspace's local UI state. */
export const TABS_UI_STATE_KEY = "tabs";

/** Open tabs in localStorage, where builds before STO-06 kept them. Now read only as a fallback. */
export function localStorageTabs(storage: () => Storage = () => window.localStorage): TabsPersistence {
  return localStorageWorkspaceStore<SavedTabs>(TABS_KEY_PREFIX, decodeSavedTabs, encodeSavedTabs, storage);
}

/** Open tabs in the workspace's local directory (STO-06), falling back to localStorage. */
export function workspaceLocalTabs(): TabsPersistence {
  return workspaceLocalStore<SavedTabs>(TABS_UI_STATE_KEY, decodeSavedTabs, encodeSavedTabs, localStorageTabs());
}
