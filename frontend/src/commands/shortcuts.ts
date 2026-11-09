// Running keyboard shortcuts (UI-06): one window key listener for
// every global shortcut of the registry (lib/shortcuts), and the handlers
// bound to their ids. See docs/frontend.md (Keyboard shortcuts).
//
// Components bind the handlers that need their local state (the help
// overlay, the sidebar, the environment switcher) with useShortcut;
// installShortcuts binds the ones that are plain commands (close and reopen
// tabs, a new window).

import {
  IS_MAC,
  matchesDefinition,
  SHORTCUTS,
  type ShortcutDefinition,
} from "../lib/shortcuts";
import { formatError } from "../lib/errors";
import type { Stores } from "../stores";
import { closeTabs, reopenClosedTab } from "./tabs";
import { showToast } from "./toasts";
import { newWindow } from "./windows";

/**
 * Runs a shortcut. Returning false means "not now" (e.g. there is nothing to
 * switch): the key press then goes on as if there were no shortcut.
 */
export type ShortcutHandler = (event: KeyboardEvent) => boolean | void;

export interface ShortcutBindings {
  /** Binds a handler to a shortcut id. The latest binding wins. Returns the unbind function. */
  bind(id: string, handler: ShortcutHandler): () => void;
  /** The handler bound last, if any. */
  get(id: string): ShortcutHandler | undefined;
}

export function createShortcutBindings(): ShortcutBindings {
  const stacks = new Map<string, ShortcutHandler[]>();
  return {
    bind(id, handler) {
      const stack = stacks.get(id) ?? [];
      // A wrapper, so binding the same function twice unbinds cleanly.
      const entry: ShortcutHandler = (e) => handler(e);
      stack.push(entry);
      stacks.set(id, stack);
      return () => {
        const i = stack.indexOf(entry);
        if (i >= 0) stack.splice(i, 1);
      };
    },
    get(id) {
      const stack = stacks.get(id);
      return stack?.[stack.length - 1];
    },
  };
}

/** The app's bindings, used by useShortcut and the window listener. */
export const shortcutBindings: ShortcutBindings = createShortcutBindings();

/** The attribute that marks where scoped shortcuts apply: data-shortcut-scope="table". */
export const SCOPE_ATTRIBUTE = "data-shortcut-scope";

/** The scope of a modal dialog: only shortcuts marked inDialog run inside it. */
export const DIALOG_SCOPE = "dialog";

/**
 * The scope of a shortcut recorder (the settings screen's Shortcuts page,
 * UI-07): while it has focus, every key press is left to it, so a chord can
 * be recorded even when it is already a shortcut.
 */
export const RECORD_SCOPE = "record";

/** The scopes around an element, innermost first. */
export function scopesOf(target: EventTarget | null): string[] {
  const scopes: string[] = [];
  let el = isElement(target) ? target.closest(`[${SCOPE_ATTRIBUTE}]`) : null;
  while (el !== null) {
    scopes.push(el.getAttribute(SCOPE_ATTRIBUTE) ?? "");
    el = el.parentElement?.closest(`[${SCOPE_ATTRIBUTE}]`) ?? null;
  }
  return scopes;
}

function isElement(target: EventTarget | null): target is Element {
  return target !== null && typeof (target as Element).closest === "function";
}

const NON_TEXT_INPUTS = new Set(["button", "checkbox", "color", "file", "hidden", "image", "radio", "range", "reset", "submit"]);

/**
 * Whether typing goes into the element: text inputs, text areas, selects
 * (type-ahead) and contenteditable elements such as CodeMirror's content.
 */
export function isEditableTarget(target: EventTarget | null): boolean {
  if (!isElement(target)) return false;
  const tag = target.tagName;
  if (tag === "TEXTAREA" || tag === "SELECT") return true;
  if (tag === "INPUT") return !NON_TEXT_INPUTS.has((target as HTMLInputElement).type);
  return target.closest('[contenteditable]:not([contenteditable="false"])') !== null;
}

export interface ShortcutKeyOptions {
  bindings?: ShortcutBindings;
  mac?: boolean;
  shortcuts?: readonly ShortcutDefinition[];
}

/**
 * Handles one keydown for the global shortcuts. Returns the id of the
 * shortcut that took the key press (and stopped it), or null.
 *
 * - Ignored: an event already handled (defaultPrevented), IME
 *   composition (isComposing, keyCode 229) and key presses inside a
 *   shortcut recorder (data-shortcut-scope="record").
 * - Inside an element with data-shortcut-scope, a scoped shortcut of that
 *   scope with the same chord wins: the event is left to the component.
 * - Inside a text field, only chords with Mod or Ctrl run; plain keys like
 *   "?" type.
 * - Inside a dialog, only shortcuts marked inDialog run.
 * - Key repeat is swallowed without running the shortcut again (holding ⌘W
 *   must not close every tab).
 * - A placeholder swallows its chord and does nothing.
 * - A shortcut without a bound handler, or whose handler returns false,
 *   lets the event through.
 */
export function handleShortcutKeyDown(e: KeyboardEvent, options: ShortcutKeyOptions = {}): string | null {
  const { bindings = shortcutBindings, mac = IS_MAC, shortcuts = SHORTCUTS } = options;
  if (e.defaultPrevented || e.isComposing || e.keyCode === 229) return null;

  const scopes = scopesOf(e.target);
  const scoped = new Set<string>(scopes);
  if (scoped.has(RECORD_SCOPE)) return null;
  for (const def of shortcuts) {
    if (def.scope !== "global" && scoped.has(def.scope) && matchesDefinition(e, def, mac)) return null;
  }

  const editable = isEditableTarget(e.target);
  const inDialog = scoped.has(DIALOG_SCOPE);
  for (const def of shortcuts) {
    if (def.scope !== "global" || !matchesDefinition(e, def, mac)) continue;
    if (inDialog && def.inDialog !== true) continue;
    if (editable && !allowedInEditable(e, mac)) continue;
    const handler = bindings.get(def.id);
    if (def.placeholder === undefined && handler === undefined) continue;
    if (!e.repeat && def.placeholder === undefined && handler?.(e) === false) continue;
    e.preventDefault();
    e.stopPropagation();
    return def.id;
  }
  return null;
}

/** In a text field a shortcut needs the primary modifier (⌘ or Ctrl), so typing is never taken. */
function allowedInEditable(e: KeyboardEvent, mac: boolean): boolean {
  return mac ? e.metaKey || e.ctrlKey : e.ctrlKey;
}

/** Binds the shortcuts that are plain commands on the stores. Returns the unbind function. */
export function bindCommandShortcuts(stores: Stores, bindings: ShortcutBindings = shortcutBindings): () => void {
  const unbind = [
    // ⌘W with no tab open does nothing, but is still taken: otherwise the
    // macOS menu's Close (⌘W) would close the window and quit the app.
    bindings.bind("tab.close", () => {
      const { activeId } = stores.tabs.getState();
      if (activeId !== null) closeTabs(stores, [activeId]);
    }),
    // Taken even with nothing to reopen, so the chord always behaves the same.
    bindings.bind("tab.reopenClosed", () => {
      reopenClosedTab(stores);
    }),
    // Another window on the open project (APP-06).
    bindings.bind("window.new", () => {
      newWindow().catch((err: unknown) => showToast({ message: formatError(err) }));
    }),
  ];
  return () => unbind.forEach((u) => u());
}

/**
 * Binds the command shortcuts and listens for key presses on the window, in
 * the capture phase so shortcuts run before components and CodeMirror see
 * the key. Returns the stop function.
 */
export function installShortcuts(stores: Stores, options: ShortcutKeyOptions & { target?: Window } = {}): () => void {
  const { target = window, ...keyOptions } = options;
  const unbind = bindCommandShortcuts(stores, keyOptions.bindings);
  const onKeyDown = (e: KeyboardEvent) => {
    handleShortcutKeyDown(e, keyOptions);
  };
  target.addEventListener("keydown", onKeyDown, true);
  return () => {
    target.removeEventListener("keydown", onKeyDown, true);
    unbind();
  };
}
