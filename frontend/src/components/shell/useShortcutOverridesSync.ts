import { useLayoutEffect, useSyncExternalStore } from "react";
import { getShortcutOverrides, setShortcutOverrides, subscribeShortcutOverrides, type ShortcutOverrides } from "../../lib/shortcuts";
import { useSettings } from "../../stores";

/**
 * Hands the custom shortcuts (UI-07, the settings file's "shortcuts") to
 * the shortcut registry whenever they change: at startup once the file has
 * loaded, after a change on the settings screen and after a hand edit.
 * Every key match and label reads the registry, so they apply at once.
 * Called once, by App.
 */
export function useShortcutOverridesSync(): void {
  const shortcuts = useSettings((s) => s.shortcuts);
  useLayoutEffect(() => {
    setShortcutOverrides(shortcuts);
  }, [shortcuts]);
}

/**
 * The custom shortcuts in effect. A component that shows chords calls it
 * so it re-renders with the new labels when they change.
 */
export function useShortcutOverrides(): ShortcutOverrides {
  return useSyncExternalStore(subscribeShortcutOverrides, getShortcutOverrides);
}
