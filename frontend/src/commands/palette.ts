// The command palette (NAV-05): every entry of the shortcut registry
// (lib/shortcuts) as an action that can be run by name, whether it is
// available where focus was, and running it. See docs/frontend.md
// (Command palette). CommandPalette.tsx renders it.
//
// - A global action runs the handler bound to its id (commands/shortcuts),
//   as its key would. It is available while a handler is bound: "Send
//   request" only while a request editor is open.
// - A scoped action (tree, tab bar, table, code editor, composer, GraphQL
//   query editor, request editor, log entry) is available when the element that had
//   focus before the palette opened is inside that scope. Running it puts
//   focus back there and presses its chord on that element, so the
//   component (or CodeMirror) handles it exactly as the key.

import { t } from "../i18n";
import { fuzzyMatch, normalizeQuery } from "../lib/fuzzy";
import {
  chordEventInit,
  IS_MAC,
  parseChord,
  scopeLabel,
  scopePlace,
  shortcutGroupLabel,
  shortcutName,
  SHORTCUTS,
  shortcutKeys,
  type ShortcutDefinition,
} from "../lib/shortcuts";
import { scopesOf, shortcutBindings, type ShortcutBindings } from "./shortcuts";
import { EDIT_HISTORY_ATTRIBUTE } from "./undoRedo";

/** Shortcuts the palette does not list: opening the palette from the palette does nothing useful. */
export const NOT_IN_PALETTE: readonly string[] = ["palette.open"];

export interface PaletteOptions {
  bindings?: ShortcutBindings;
  mac?: boolean;
  shortcuts?: readonly ShortcutDefinition[];
}

export interface Availability {
  readonly available: boolean;
  /** Why not, when it is not available. */
  readonly reason: string;
}

function isElement(target: unknown): target is Element {
  return typeof target === "object" && target !== null && typeof (target as Element).closest === "function";
}

/** Whether `def` can run now, with focus (before the palette opened) on `target`. */
export function actionAvailability(
  def: ShortcutDefinition,
  target: Element | null,
  bindings: ShortcutBindings = shortcutBindings,
): Availability {
  if (def.placeholder !== undefined) return { available: false, reason: t("commands.palette.comingSoon") };
  if (def.scope === "global") {
    if (bindings.get(def.id) !== undefined) return { available: true, reason: "" };
    return { available: false, reason: def.whenKey !== undefined ? t(def.whenKey) : t("commands.palette.notAvailable") };
  }
  const place = scopePlace(def.scope);
  if (target === null || !target.isConnected) return { available: false, reason: t("commands.palette.onlyIn", { place }) };
  if (def.scope === "request") {
    const inRequest = target.closest(`[${EDIT_HISTORY_ATTRIBUTE}]`) !== null && target.closest(".cm-editor") === null;
    return inRequest ? { available: true, reason: "" } : { available: false, reason: t("commands.palette.onlyInRequest", { place }) };
  }
  return scopesOf(target).includes(def.scope) ? { available: true, reason: "" } : { available: false, reason: t("commands.palette.onlyIn", { place }) };
}

export interface PaletteItem {
  readonly def: ShortcutDefinition;
  readonly available: boolean;
  readonly reason: string;
  /** Matched character positions in the label, for highlighting. */
  readonly hits: readonly number[];
  readonly score: number;
}

/**
 * The palette's list for `query`: every listed action whose label (or
 * group and label) matches, available ones first, then by score; before
 * anything is typed, the recently run ones first, then registry order.
 */
export function paletteItems(
  query: string,
  target: Element | null,
  recent: readonly string[] = [],
  options: PaletteOptions = {},
): PaletteItem[] {
  const { bindings = shortcutBindings, shortcuts = SHORTCUTS } = options;
  const q = normalizeQuery(query);
  const items: PaletteItem[] = [];
  shortcuts.forEach((def, order) => {
    if (NOT_IN_PALETTE.includes(def.id)) return;
    let hits: readonly number[] = [];
    let score = -order / 1000;
    if (q !== "") {
      const label = shortcutName(def);
      const onLabel = fuzzyMatch(q, label);
      const onGroup = onLabel === null ? fuzzyMatch(q, `${shortcutGroupLabel(def.group)} ${scopeLabel(def.scope)} ${label}`) : null;
      if (onLabel === null && onGroup === null) return;
      hits = onLabel?.positions ?? [];
      score = onLabel !== null ? onLabel.score + 20 : (onGroup?.score ?? 0);
    } else {
      const r = recent.indexOf(def.id);
      if (r >= 0) score = 100 - r;
    }
    const { available, reason } = actionAvailability(def, target, bindings);
    items.push({ def, available, reason, hits, score });
  });
  return items.sort((a, b) => Number(b.available) - Number(a.available) || b.score - a.score);
}

/** What happened when an action ran. */
export type RunResult = "ran" | "declined" | "unavailable";

/**
 * Runs the action `def` with `target` as the element that had focus before
 * the palette opened. Call it after the palette has closed and given focus
 * back. "declined": the handler had nothing to do (it returned false, as
 * "Switch environment" does without environments).
 */
export function runPaletteAction(def: ShortcutDefinition, target: Element | null, options: PaletteOptions = {}): RunResult {
  const { bindings = shortcutBindings, mac = IS_MAC } = options;
  if (!actionAvailability(def, target, bindings).available) return "unavailable";
  const init = chordEventInit(parseChord(shortcutKeys(def)[0]), mac);
  if (def.scope === "global") {
    const handler = bindings.get(def.id);
    if (handler === undefined) return "unavailable";
    return handler(new KeyboardEvent("keydown", init)) === false ? "declined" : "ran";
  }
  if (!isElement(target)) return "unavailable";
  if (target instanceof HTMLElement && target !== document.activeElement) target.focus({ preventScroll: true });
  const event = new KeyboardEvent("keydown", init);
  target.dispatchEvent(event);
  return "ran";
}

// Requests to open the palette, from outside it (the quick switcher's ">"
// prefix). Callbacks, so a small listener set in commands/, not a store.
type PaletteListener = (query: string) => void;
const paletteListeners = new Set<PaletteListener>();

/** Opens the command palette with `query` typed in. Does nothing while no palette is mounted. */
export function openCommandPalette(query = ""): boolean {
  if (paletteListeners.size === 0) return false;
  for (const listener of [...paletteListeners]) listener(query);
  return true;
}

/** Called by the mounted palette. Returns the unsubscribe function. */
export function onCommandPaletteRequest(listener: PaletteListener): () => void {
  paletteListeners.add(listener);
  return () => paletteListeners.delete(listener);
}

/** The recently run action ids, most recent first, at most `max`. */
export function touchRecentAction(recent: readonly string[], id: string, max = 8): string[] {
  return [id, ...recent.filter((r) => r !== id)].slice(0, max);
}
