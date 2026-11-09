// Key bindings of the tab bar. The chords live in the shortcut
// registry (lib/shortcuts: scope "tabs"), so the help lists them; these
// helpers match them. They apply only while focus is on a tab.

import { IS_MAC, matchesShortcut, type KeyChord } from "../../lib/shortcuts";

/**
 * Moving the focused tab: Ctrl+Shift+PageUp/PageDown (⌘⇧PageUp/PageDown also
 * works on macOS) or Alt+Shift+Left/Right (⌥⇧← / ⌥⇧→ on macOS).
 * Returns -1 (left), 1 (right) or 0 (not a move).
 */
export function tabMoveDirection(e: KeyChord, mac: boolean = IS_MAC): -1 | 0 | 1 {
  if (matchesShortcut(e, "tabs.moveLeft", mac)) return -1;
  if (matchesShortcut(e, "tabs.moveRight", mac)) return 1;
  return 0;
}

/** Closing the focused tab: Delete. */
export function isCloseTabKey(e: KeyChord, mac: boolean = IS_MAC): boolean {
  return matchesShortcut(e, "tabs.closeFocused", mac);
}

/** Opening the tab's context menu from the keyboard: the Menu key or Shift+F10. */
export function isContextMenuKey(e: KeyChord, mac: boolean = IS_MAC): boolean {
  return matchesShortcut(e, "tabs.menu", mac);
}

/** No modifiers: plain arrow, Home and End navigation between tabs. */
export function isPlainKey(e: KeyChord): boolean {
  return !e.shiftKey && !e.altKey && !e.ctrlKey && !e.metaKey;
}
