// This window's ID (APP-06, docs/windows.md), for the events Go sends to
// every window that belong to one: collection:saved and
// collection:save-failed carry the revisions of the window that made the
// edit, collection:edited is for the others, and window:open-project for
// one. commands/windows.ts learns the ID; this module has no imports, so
// the auto-save and external-change commands can use it without an import
// cycle. Until the ID is known every event counts as this window's, as
// with one window.

import type { Stores } from "../stores";

/** The window ID of each set of stores: one per window (tests make several). */
const windowIds = new WeakMap<Stores, number>();

/** Records that `stores` are the stores of window `id`. */
export function setOwnWindow(stores: Stores, id: number): void {
  windowIds.set(stores, id);
}

/** This window's ID, once Go has told it. */
export function ownWindow(stores: Stores): number | undefined {
  return windowIds.get(stores);
}

/**
 * Whether an event naming window `id` came from another window than the
 * one of `stores`. 0 (no window) and an unknown own ID count as this one.
 */
export function fromOtherWindow(stores: Stores, id: number | undefined): boolean {
  const own = windowIds.get(stores);
  return own !== undefined && id !== undefined && id !== 0 && id !== own;
}
