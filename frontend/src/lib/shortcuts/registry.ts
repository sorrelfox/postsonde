// The shortcut registry (UI-06): every keyboard shortcut of the app in
// one list. The help overlay shows this list, and the key handling reads it.
// See docs/frontend.md (Keyboard shortcuts).
//
// - Global shortcuts are run by the one window key listener
//   (commands/shortcuts.ts), which calls the handler bound to the id.
// - Scoped shortcuts (tab bar, tree, table, editor, composer, GraphQL
//   editor) are handled by their component (or by CodeMirror) while focus is
//   inside an element marked with
//   data-shortcut-scope. They are listed here so the help shows them, so
//   components match keys through this list, and so a global shortcut with
//   the same chord steps aside inside that element.
// - One exception: the "request" scope (undo and redo of request edits,
//   NAV-07) is handled by commands/undoRedo.ts in the bubble phase, inside
//   the element marked data-edit-history, not data-shortcut-scope. So the
//   global ⌘Z of an Undo-delete toast keeps going first there.
// - A placeholder reserves its chord until the feature lands: the key is
//   swallowed and nothing happens, and the help shows "Coming soon".
// - Every entry is also an action of the command palette (NAV-05,
//   commands/palette.ts), which runs it by name.
// - The user may replace the chords of every entry but the fixed ones
//   (UI-07): setShortcutOverrides applies the overrides of the settings
//   file, and every function below reads the chords in effect.
// - Text (labels, groups, scopes, what an entry needs) is held as message
//   keys (UI-10) and looked up where it is shown: t(def.labelKey) or
//   shortcutName(def), shortcutGroupLabel(group), scopeLabel(scope).

import { t, type MessageKey } from "../../i18n";
import { ariaChord, chordSignature, formatChord, IS_MAC, matchesChord, parseChord, type Chord, type KeyChord } from "./keys";

/** Where a shortcut applies. "global" works anywhere in the project window. */
export type ShortcutScope = "global" | "tabs" | "tree" | "table" | "editor" | "composer" | "graphql" | "request" | "log";

export type ShortcutGroup = "general" | "requests" | "tabs" | "tables" | "editor";

export interface ShortcutDefinition {
  readonly id: string;
  /** What the shortcut does, as the help, the palette and the settings show it. */
  readonly labelKey: MessageKey;
  readonly group: ShortcutGroup;
  /** Chords, the main one first (see keys.ts for the syntax). */
  readonly keys: readonly string[];
  readonly scope: ShortcutScope;
  /** Not built yet: the chord is reserved and the help says "Coming soon". The issue that builds it, if known. */
  readonly placeholder?: { readonly issue?: string };
  /** Also runs while a dialog (such as the help overlay) has focus. */
  readonly inDialog?: boolean;
  /** The chords cannot be changed (CodeMirror's own keymaps). */
  readonly fixed?: boolean;
  /** For a global shortcut bound only at times: what it needs, shown by the palette while it is unavailable. */
  readonly whenKey?: MessageKey;
}

/** Headings of the help overlay, in order. */
export const SHORTCUT_GROUPS: readonly ShortcutGroup[] = ["general", "requests", "tabs", "tables", "editor"];

const GROUP_LABELS: Readonly<Record<ShortcutGroup, MessageKey>> = {
  general: "lib.shortcuts.group.general",
  requests: "lib.shortcuts.group.requests",
  tabs: "lib.shortcuts.group.tabs",
  tables: "lib.shortcuts.group.tables",
  editor: "lib.shortcuts.group.editor",
};

/** Where a scoped shortcut works, as the help overlay says it ("In a code editor"); none for a global one. */
const SCOPE_LABELS: Readonly<Record<ShortcutScope, MessageKey | null>> = {
  global: null,
  tabs: "lib.shortcuts.scope.tabs",
  tree: "lib.shortcuts.scope.tree",
  table: "lib.shortcuts.scope.table",
  editor: "lib.shortcuts.scope.editor",
  composer: "lib.shortcuts.scope.composer",
  graphql: "lib.shortcuts.scope.graphql",
  request: "lib.shortcuts.scope.request",
  log: "lib.shortcuts.scope.log",
};

/** The same inside a sentence ("in a code editor"): its own message, as languages capitalise differently. */
const SCOPE_PLACES: Readonly<Record<ShortcutScope, MessageKey | null>> = {
  global: null,
  tabs: "lib.shortcuts.place.tabs",
  tree: "lib.shortcuts.place.tree",
  table: "lib.shortcuts.place.table",
  editor: "lib.shortcuts.place.editor",
  composer: "lib.shortcuts.place.composer",
  graphql: "lib.shortcuts.place.graphql",
  request: "lib.shortcuts.place.request",
  log: "lib.shortcuts.place.log",
};

/** What the shortcut does, in the language in use: "Send request". */
export function shortcutName(def: ShortcutDefinition): string {
  return t(def.labelKey);
}

/** A heading of the help overlay, in the language in use: "Requests". */
export function shortcutGroupLabel(group: ShortcutGroup): string {
  return t(GROUP_LABELS[group]);
}

/** Where a scoped shortcut works, in the language in use: "In a code editor"; "" for a global one. */
export function scopeLabel(scope: ShortcutScope): string {
  const key = SCOPE_LABELS[scope];
  return key === null ? "" : t(key);
}

/** Where a scoped shortcut works, inside a sentence: "in a code editor", "on an entry of a log or timeline"; "" for a global one. */
export function scopePlace(scope: ShortcutScope): string {
  const key = SCOPE_PLACES[scope];
  return key === null ? "" : t(key);
}

export const SHORTCUTS: readonly ShortcutDefinition[] = [
  // General
  { id: "help.toggle", labelKey: "lib.shortcuts.action.help.toggle", group: "general", keys: ["Mod+/", "?"], scope: "global", inDialog: true },
  // The command palette (NAV-05): runs any entry of this list by name.
  { id: "palette.open", labelKey: "lib.shortcuts.action.palette.open", group: "general", keys: ["Mod+Shift+P"], scope: "global" },
  { id: "jump.open", labelKey: "lib.shortcuts.action.jump.open", group: "general", keys: ["Mod+P"], scope: "global" },
  { id: "environment.switch", labelKey: "lib.shortcuts.action.environment.switch", group: "general", keys: ["Mod+E"], scope: "global" },
  { id: "sidebar.toggle", labelKey: "lib.shortcuts.action.sidebar.toggle", group: "general", keys: ["Mod+B"], scope: "global" },
  // Focus mode (UI-09): hides the rail, the sidebar and the console, for this session only.
  { id: "view.focusMode", labelKey: "lib.shortcuts.action.view.focusMode", group: "general", keys: ["Mod+Shift+Enter"], scope: "global" },
  { id: "settings.open", labelKey: "lib.shortcuts.action.settings.open", group: "general", keys: ["Mod+,"], scope: "global" },
  // Another window on the open project (APP-06, docs/windows.md).
  { id: "window.new", labelKey: "lib.shortcuts.action.window.new", group: "general", keys: ["Mod+Shift+N"], scope: "global" },
  { id: "notifications.focus", labelKey: "lib.shortcuts.action.notifications.focus", group: "general", keys: ["F6"], scope: "global" },
  // The console panel (DBG-06).
  { id: "console.toggle", labelKey: "lib.shortcuts.action.console.toggle", group: "general", keys: ["Mod+Shift+Y"], scope: "global" },
  // "Lock now" of the encrypted secrets (SEC-03), bound while they are unlocked.
  {
    id: "secrets.lock",
    labelKey: "lib.shortcuts.action.secrets.lock",
    group: "general",
    keys: ["Mod+Alt+L"],
    scope: "global",
    whenKey: "lib.shortcuts.when.secretsUnlocked",
  },
  // A focused entry of the console, the connection log, a message log or
  // the response timeline (DBG-07); copied with secrets masked.
  { id: "log.copyEntry", labelKey: "lib.shortcuts.action.log.copyEntry", group: "general", keys: ["Mod+C"], scope: "log" },
  { id: "log.menu", labelKey: "lib.shortcuts.action.log.menu", group: "general", keys: ["Shift+F10", "ContextMenu"], scope: "log" },

  // Requests
  {
    id: "request.send",
    labelKey: "lib.shortcuts.action.request.send",
    group: "requests",
    keys: ["Mod+Enter"],
    scope: "global",
    whenKey: "lib.shortcuts.when.httpRequest",
  },
  {
    id: "request.new",
    labelKey: "lib.shortcuts.action.request.new",
    group: "requests",
    keys: ["Mod+N"],
    scope: "global",
    whenKey: "lib.shortcuts.when.writableProject",
  },
  // Bound while an HTTP, GraphQL or SSE request editor is open (GEN-01).
  {
    id: "request.copyCurl",
    labelKey: "lib.shortcuts.action.request.copyCurl",
    group: "requests",
    keys: ["Mod+Shift+C"],
    scope: "global",
    whenKey: "lib.shortcuts.when.httpRequest",
  },
  // The sidebar filter (NAV-06). Mod+Shift+F is Beautify JSON body.
  { id: "collection.filter", labelKey: "lib.shortcuts.action.collection.filter", group: "requests", keys: ["Mod+Shift+E"], scope: "global" },
  { id: "tree.menu", labelKey: "lib.shortcuts.action.tree.menu", group: "requests", keys: ["Shift+F10", "ContextMenu"], scope: "tree" },
  { id: "tree.rename", labelKey: "lib.shortcuts.action.tree.rename", group: "requests", keys: ["F2"], scope: "tree" },
  { id: "tree.duplicate", labelKey: "lib.shortcuts.action.tree.duplicate", group: "requests", keys: ["Mod+D"], scope: "tree" },
  // i18n-ignore: key names in chords, not text (formatChord shows them)
  { id: "tree.delete", labelKey: "lib.shortcuts.action.tree.delete", group: "requests", keys: ["Delete", "Backspace"], scope: "tree" },
  // Move and reorder (COL-04), the keyboard alternative to drag and drop.
  { id: "tree.moveUp", labelKey: "lib.shortcuts.action.tree.moveUp", group: "requests", keys: ["Alt+ArrowUp"], scope: "tree" },
  { id: "tree.moveDown", labelKey: "lib.shortcuts.action.tree.moveDown", group: "requests", keys: ["Alt+ArrowDown"], scope: "tree" },
  { id: "tree.moveTo", labelKey: "lib.shortcuts.action.tree.moveTo", group: "requests", keys: ["Mod+Shift+M"], scope: "tree" },
  // Only while the Undo toast of a delete shows, and never in a text field or editor (their own undo).
  {
    id: "collection.undoDelete",
    labelKey: "lib.shortcuts.action.collection.undoDelete",
    group: "requests",
    keys: ["Mod+Z"],
    scope: "global",
    whenKey: "lib.shortcuts.when.undoDelete",
  },
  // Outside a code editor, which has its own history (the same keys, extensions.ts).
  { id: "request.undo", labelKey: "lib.shortcuts.action.request.undo", group: "requests", keys: ["Mod+Z"], scope: "request" },
  { id: "request.redo", labelKey: "lib.shortcuts.action.request.redo", group: "requests", keys: ["Mod+Shift+Z", "Mod+Y"], scope: "request" },
  {
    id: "request.beautify",
    labelKey: "lib.shortcuts.action.request.beautify",
    group: "requests",
    keys: ["Mod+Shift+F"],
    scope: "global",
    whenKey: "lib.shortcuts.when.jsonBody",
  },
  { id: "composer.send", labelKey: "lib.shortcuts.action.composer.send", group: "requests", keys: ["Mod+Enter"], scope: "composer" },
  // The GraphQL editor's query runs commands/graphql.ts formatGraphqlQuery on it (GQL-18).
  { id: "graphql.prettify", labelKey: "lib.shortcuts.action.graphql.prettify", group: "requests", keys: ["Mod+Shift+F"], scope: "graphql" },
  // Bound while a GraphQL editor is open (GQL-14).
  {
    id: "graphql.docs",
    labelKey: "lib.shortcuts.action.graphql.docs",
    group: "requests",
    keys: ["Mod+Shift+D"],
    scope: "global",
    whenKey: "lib.shortcuts.when.graphqlRequest",
  },
  // The GraphQL query editor's schema actions: fill fields (GQL-17) and the
  // keyboard way to Ctrl/Cmd-click a name for its docs (GQL-15).
  { id: "graphql.fillFields", labelKey: "lib.shortcuts.action.graphql.fillFields", group: "requests", keys: ["Mod+Alt+F"], scope: "graphql" },
  { id: "graphql.docsAtCursor", labelKey: "lib.shortcuts.action.graphql.docsAtCursor", group: "requests", keys: ["Mod+Alt+D"], scope: "graphql" },

  // Tabs
  { id: "tab.close", labelKey: "lib.shortcuts.action.tab.close", group: "tabs", keys: ["Mod+W"], scope: "global" },
  { id: "tab.reopenClosed", labelKey: "lib.shortcuts.action.tab.reopenClosed", group: "tabs", keys: ["Mod+Shift+T"], scope: "global" },
  // i18n-ignore: a key name in a chord, not text
  { id: "tabs.closeFocused", labelKey: "lib.shortcuts.action.tabs.closeFocused", group: "tabs", keys: ["Delete"], scope: "tabs" },
  {
    id: "tabs.moveLeft",
    labelKey: "lib.shortcuts.action.tabs.moveLeft",
    group: "tabs",
    keys: ["Ctrl+Shift+PageUp", "Mod+Shift+PageUp", "Alt+Shift+ArrowLeft"],
    scope: "tabs",
  },
  {
    id: "tabs.moveRight",
    labelKey: "lib.shortcuts.action.tabs.moveRight",
    group: "tabs",
    keys: ["Ctrl+Shift+PageDown", "Mod+Shift+PageDown", "Alt+Shift+ArrowRight"],
    scope: "tabs",
  },
  { id: "tabs.menu", labelKey: "lib.shortcuts.action.tabs.menu", group: "tabs", keys: ["Shift+F10", "ContextMenu"], scope: "tabs" },

  // Tables
  { id: "table.insertRow", labelKey: "lib.shortcuts.action.table.insertRow", group: "tables", keys: ["Mod+Enter"], scope: "table" },
  { id: "table.suggest", labelKey: "lib.shortcuts.action.table.suggest", group: "tables", keys: ["Ctrl+Space", "Alt+ArrowDown"], scope: "table" },
  { id: "table.deleteRow", labelKey: "lib.shortcuts.action.table.deleteRow", group: "tables", keys: ["Mod+Shift+Backspace", "Mod+Shift+Delete"], scope: "table" },

  // Editor (CodeMirror's own keymaps, components/editor/extensions.ts)
  { id: "editor.find", labelKey: "lib.shortcuts.action.editor.find", group: "editor", keys: ["Mod+F"], scope: "editor", fixed: true },
  { id: "editor.selectNext", labelKey: "lib.shortcuts.action.editor.selectNext", group: "editor", keys: ["Mod+D"], scope: "editor", fixed: true },
  { id: "editor.toggleComment", labelKey: "lib.shortcuts.action.editor.toggleComment", group: "editor", keys: ["Mod+/"], scope: "editor", fixed: true },
];

const byId = new Map(SHORTCUTS.map((s) => [s.id, s]));
const chordCache = new Map<string, Chord>();

function chord(text: string): Chord {
  let parsed = chordCache.get(text);
  if (parsed === undefined) {
    parsed = parseChord(text);
    chordCache.set(text, parsed);
  }
  return parsed;
}

/** Whether `text` is a chord the registry can use. */
export function isValidChord(text: string): boolean {
  try {
    chord(text);
    return true;
  } catch {
    return false;
  }
}

/** Custom chords by shortcut id (UI-07), as the settings file keeps them. */
export type ShortcutOverrides = Readonly<Record<string, readonly string[]>>;

const NO_OVERRIDES: ShortcutOverrides = Object.freeze({});
let overrides: ShortcutOverrides = NO_OVERRIDES;
const overrideListeners = new Set<() => void>();

/**
 * Replaces the custom chords in effect (the settings file's "shortcuts"
 * section). Entries for unknown ids, for fixed shortcuts and without a valid
 * chord are dropped; invalid chords inside an entry are skipped. Returns the
 * overrides applied, the same object as before when nothing changed.
 */
export function setShortcutOverrides(next: ShortcutOverrides | null | undefined): ShortcutOverrides {
  const applied: Record<string, readonly string[]> = {};
  for (const [id, keys] of Object.entries(next ?? {})) {
    const def = byId.get(id);
    if (def === undefined || def.fixed === true || !Array.isArray(keys)) continue;
    const valid = keys.filter((k): k is string => typeof k === "string" && isValidChord(k));
    if (valid.length > 0) applied[id] = Object.freeze([...valid]);
  }
  const ids = Object.keys(applied);
  const same =
    ids.length === Object.keys(overrides).length &&
    ids.every((id) => {
      const had = overrides[id];
      const keys = applied[id];
      return had !== undefined && had.length === keys.length && had.every((k, i) => k === keys[i]);
    });
  if (same) return overrides;
  overrides = ids.length === 0 ? NO_OVERRIDES : Object.freeze(applied);
  for (const listener of [...overrideListeners]) listener();
  return overrides;
}

/** The custom chords in effect; the same object until they change. */
export function getShortcutOverrides(): ShortcutOverrides {
  return overrides;
}

/** Calls `listener` whenever the custom chords change. Returns the unsubscribe function. */
export function subscribeShortcutOverrides(listener: () => void): () => void {
  overrideListeners.add(listener);
  return () => overrideListeners.delete(listener);
}

/** The chords in effect for a shortcut: its custom ones, else its defaults. */
export function shortcutKeys(def: ShortcutDefinition): readonly string[] {
  return overrides[def.id] ?? def.keys;
}

/** Whether the user changed the shortcut's chords. */
export function isShortcutCustomized(id: string): boolean {
  return overrides[id] !== undefined;
}

export function getShortcut(id: string): ShortcutDefinition {
  const def = byId.get(id);
  if (def === undefined) throw new Error(`shortcuts: unknown shortcut "${id}"`);
  return def;
}

/** The chords of a shortcut on the platform, without ones that are the same there (Ctrl and Mod off macOS). */
export function shortcutChords(def: ShortcutDefinition, mac: boolean = IS_MAC): Chord[] {
  const seen = new Set<string>();
  const chords: Chord[] = [];
  for (const text of shortcutKeys(def)) {
    const c = chord(text);
    const sig = chordSignature(c, mac);
    if (seen.has(sig)) continue;
    seen.add(sig);
    chords.push(c);
  }
  return chords;
}

/** Whether the event is one of the shortcut's chords. */
export function matchesDefinition(e: KeyChord, def: ShortcutDefinition, mac: boolean = IS_MAC): boolean {
  return shortcutKeys(def).some((text) => matchesChord(e, chord(text), mac));
}

/** Whether the event is the shortcut `id` (for components handling scoped shortcuts). */
export function matchesShortcut(e: KeyChord, id: string, mac: boolean = IS_MAC): boolean {
  return matchesDefinition(e, getShortcut(id), mac);
}

/** Every chord of the shortcut as shown on the platform: ["⌘/", "?"]. */
export function shortcutLabels(id: string, mac: boolean = IS_MAC): string[] {
  return shortcutChords(getShortcut(id), mac).map((c) => formatChord(c, mac));
}

/** The main chord as shown on the platform: "⌘B" or "Ctrl+B". */
export function shortcutLabel(id: string, mac: boolean = IS_MAC): string {
  return shortcutLabels(id, mac)[0];
}

/** The aria-keyshortcuts value: every chord, space-separated. */
export function ariaShortcut(id: string, mac: boolean = IS_MAC): string {
  return shortcutChords(getShortcut(id), mac)
    .map((c) => ariaChord(c, mac))
    .join(" ");
}

export interface ShortcutConflict {
  readonly scope: ShortcutScope;
  readonly chord: string;
  readonly ids: readonly [string, string];
}

/** Pairs of shortcuts in the same scope that react to the same key press on the platform. */
export function findConflicts(defs: readonly ShortcutDefinition[] = SHORTCUTS, mac: boolean = IS_MAC): ShortcutConflict[] {
  const conflicts: ShortcutConflict[] = [];
  const seen = new Map<string, string>();
  for (const def of defs) {
    for (const c of shortcutChords(def, mac)) {
      const slot = `${def.scope} ${chordSignature(c, mac)}`;
      const other = seen.get(slot);
      if (other !== undefined && other !== def.id) {
        conflicts.push({ scope: def.scope, chord: formatChord(c, mac), ids: [other, def.id] });
      } else {
        seen.set(slot, def.id);
      }
    }
  }
  return conflicts;
}

/**
 * Keys the operating system, the window or every text field keep for
 * themselves (UI-07): a custom chord may not take them. By platform, as
 * chords, with the message saying what they do ("{chord} quits the app.").
 */
/** A message saying what a reserved key does: "{chord} quits the app." */
type ReservedMessage = Extract<MessageKey, `lib.shortcuts.reserved.${string}`>;

export const RESERVED_CHORDS: Readonly<Record<"mac" | "other", Readonly<Record<string, ReservedMessage>>>> = {
  mac: {
    "Mod+Q": "lib.shortcuts.reserved.quit",
    "Mod+H": "lib.shortcuts.reserved.hide",
    "Mod+M": "lib.shortcuts.reserved.minimise",
    "Mod+R": "lib.shortcuts.reserved.reload",
    "Mod+C": "lib.shortcuts.reserved.copy",
    "Mod+V": "lib.shortcuts.reserved.paste",
    "Mod+X": "lib.shortcuts.reserved.cut",
    "Mod+A": "lib.shortcuts.reserved.selectAll",
    "Mod+Space": "lib.shortcuts.reserved.spotlight",
  },
  other: {
    "Alt+F4": "lib.shortcuts.reserved.close",
    "Mod+C": "lib.shortcuts.reserved.copy",
    "Mod+V": "lib.shortcuts.reserved.paste",
    "Mod+X": "lib.shortcuts.reserved.cut",
    "Mod+A": "lib.shortcuts.reserved.selectAll",
  },
};

export interface BindingProblem {
  /** An error blocks the chord; a warning is shown and the chord may still be used. */
  readonly level: "error" | "warning";
  readonly message: string;
  /** The other shortcut involved, if any. */
  readonly otherId?: string;
}

/**
 * What is wrong with giving shortcut `id` the chords `keys` (UI-07):
 * errors for an invalid chord, a fixed shortcut, a key used for typing, a
 * reserved key and another shortcut of the same scope on the same chord;
 * warnings when a shortcut of another scope takes the chord where it
 * applies, and when a global chord without ⌘ or Ctrl does not run while
 * typing. Other shortcuts are compared with the chords in effect.
 */
export function checkShortcutBinding(
  id: string,
  keys: readonly string[],
  mac: boolean = IS_MAC,
  defs: readonly ShortcutDefinition[] = SHORTCUTS,
): BindingProblem[] {
  const def = defs.find((d) => d.id === id) ?? getShortcut(id);
  if (def.fixed === true) {
    return [{ level: "error", message: t("lib.shortcuts.binding.fixed") }];
  }
  if (keys.length === 0) return [{ level: "error", message: t("lib.shortcuts.binding.empty") }];
  const problems: BindingProblem[] = [];
  const reserved = RESERVED_CHORDS[mac ? "mac" : "other"];
  const seen = new Set<string>();
  for (const text of keys) {
    if (!isValidChord(text)) {
      problems.push({ level: "error", message: t("lib.shortcuts.binding.invalid", { chord: text }) });
      continue;
    }
    const c = chord(text);
    const sig = chordSignature(c, mac);
    if (seen.has(sig)) continue;
    seen.add(sig);
    const shown = formatChord(c, mac);
    const isDefault = def.keys.includes(text);

    if (!c.mod && !c.ctrl && !c.alt && c.kind !== "named" && !isDefault) {
      problems.push({ level: "error", message: t(mac ? "lib.shortcuts.binding.typesCharacterMac" : "lib.shortcuts.binding.typesCharacter", { chord: shown }) });
      continue;
    }
    const reservedFor = Object.entries(reserved).find(([k]) => chordSignature(chord(k), mac) === sig);
    if (reservedFor !== undefined) {
      problems.push({ level: "error", message: t(reservedFor[1], { chord: shown }) });
      continue;
    }
    for (const other of defs) {
      if (other.id === def.id) continue;
      if (!shortcutChords(other, mac).some((oc) => chordSignature(oc, mac) === sig)) continue;
      if (other.scope === def.scope) {
        problems.push({ level: "error", message: t("lib.shortcuts.binding.taken", { chord: shown, shortcut: shortcutName(other) }), otherId: other.id });
      } else if (def.scope === "global") {
        problems.push({
          level: "warning",
          message: t("lib.shortcuts.binding.takenThere", { chord: shown, shortcut: shortcutName(other), place: scopePlace(other.scope) }),
          otherId: other.id,
        });
      } else if (other.scope === "global") {
        problems.push({
          level: "warning",
          message: t("lib.shortcuts.binding.replaces", { chord: shown, shortcut: shortcutName(other), place: scopePlace(def.scope) }),
          otherId: other.id,
        });
      }
    }
    if (def.scope === "global" && !c.mod && !c.ctrl && !isDefault) {
      problems.push({ level: "warning", message: t("lib.shortcuts.binding.notWhileTyping", { chord: shown }) });
    }
  }
  return problems;
}
