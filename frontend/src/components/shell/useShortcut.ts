import { useEffect, useRef } from "react";
import { installShortcuts, shortcutBindings, type ShortcutHandler } from "../../commands/shortcuts";
import { getShortcut } from "../../lib/shortcuts";
import { useStores } from "../../stores";

/**
 * Binds a handler to a global shortcut of the registry while the component
 * is mounted. The latest handler is always called, so it may close
 * over props and state. Return false from it to let the key press through.
 */
export function useShortcut(id: string, handler: ShortcutHandler, enabled = true): void {
  const latest = useRef(handler);
  latest.current = handler;
  useEffect(() => {
    getShortcut(id); // fail fast on an unknown id
    if (!enabled) return;
    return shortcutBindings.bind(id, (e) => latest.current(e));
  }, [id, enabled]);
}

/** Runs the global keyboard shortcuts while the shell is mounted. */
export function useGlobalShortcuts(): void {
  const stores = useStores();
  useEffect(() => installShortcuts(stores), [stores]);
}
