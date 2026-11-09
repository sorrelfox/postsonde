// Key chords (UI-06): parsing, matching and display of keyboard
// shortcuts, as pure functions testable without a DOM. See docs/frontend.md
// (Keyboard shortcuts).
//
// A chord is written platform-neutrally: modifiers, then the key, joined by
// "+": "Mod+Enter", "Mod+Shift+F", "Alt+Shift+ArrowLeft", "?".
//
// - Mod is ⌘ on macOS and Ctrl on Windows and Linux. Ctrl is the Control key
//   everywhere (on Windows and Linux the same as Mod). Alt is ⌥ on macOS.
// - Modifiers must match exactly: Mod+W does not fire for Ctrl+Alt+W, so
//   AltGr (reported as Ctrl+Alt on Windows) never triggers a Mod chord.
// - Letters and digits match by character (event.key), so Mod+Z follows the
//   layout: on QWERTZ it is the key labelled Z, on Dvorak the key that types
//   "z". When the key does not type a Latin character (Russian, Greek…, or
//   ⌥ on macOS), they fall back to the physical US-QWERTY position
//   (event.code), so Ctrl+W still works on a Russian layout.
// - Symbols ("/", ",", "?") also match by character, and Shift is ignored
//   for them, because many layouts need Shift to type the symbol (German
//   "/" is Shift+7). A symbol chord therefore never names Shift. With a
//   non-Latin character they fall back to the US position (Russian "б" is
//   the Comma key).
// - Named keys ("Enter", "PageUp", "ArrowLeft", "F10", "ContextMenu",
//   "Space") match event.key, with Shift exact.
//
// Chords as written here, event.key values and aria-keyshortcuts are code,
// not text. What formatChord shows is: macOS symbols (⌘, ⇧, ↩) stay as they
// are, key names written as words ("Shift", "Enter", "Space") come from the
// catalogue (lib.keys.*, UI-10).

import { t, type MessageKey } from "../../i18n";

export function isMacPlatform(nav: Pick<Navigator, "platform" | "userAgent"> = navigator): boolean {
  return /mac|iphone|ipad|ipod/i.test(nav.platform || nav.userAgent);
}

/** True on macOS: Mod is ⌘. */
export const IS_MAC = typeof navigator !== "undefined" && isMacPlatform(navigator);

/** The parts of a keyboard event a chord is matched against (DOM and React events both fit). */
export interface KeyChord {
  key: string;
  code?: string;
  metaKey: boolean;
  ctrlKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
}

export type ChordKeyKind = "letter" | "digit" | "symbol" | "named";

/** A parsed chord. `key` is lower case for letters. */
export interface Chord {
  readonly mod: boolean;
  readonly ctrl: boolean;
  readonly alt: boolean;
  readonly shift: boolean;
  readonly key: string;
  readonly kind: ChordKeyKind;
}

const MODIFIERS = new Set(["Mod", "Ctrl", "Alt", "Shift"]); // i18n-ignore: chord syntax

/** US-QWERTY positions of the symbols chords may use, for non-Latin layouts. */
// i18n-ignore-start: KeyboardEvent.code values
const SYMBOL_CODES: Readonly<Record<string, string>> = {
  "/": "Slash",
  "?": "Slash",
  ",": "Comma",
  ".": "Period",
  ";": "Semicolon",
  "'": "Quote",
  "[": "BracketLeft",
  "]": "BracketRight",
  "\\": "Backslash",
  "`": "Backquote",
  "-": "Minus",
  "=": "Equal",
};
// i18n-ignore-end

/** Parses "Mod+Shift+F". Throws on an invalid chord (a bug in the registry). */
export function parseChord(text: string): Chord {
  // "Mod++" is not supported; no chord needs the plus key.
  const parts = text.split("+");
  const key = parts.pop() ?? "";
  const mods = new Set<string>();
  for (const part of parts) {
    if (!MODIFIERS.has(part) || mods.has(part)) throw new Error(`shortcuts: invalid modifier in "${text}"`);
    mods.add(part);
  }
  let kind: ChordKeyKind;
  let normalized = key;
  if (/^[A-Za-z]$/.test(key)) {
    kind = "letter";
    normalized = key.toLowerCase();
  } else if (/^[0-9]$/.test(key)) {
    kind = "digit";
  } else if (key in SYMBOL_CODES) {
    kind = "symbol";
    if (mods.has("Shift")) throw new Error(`shortcuts: a symbol chord cannot name Shift: "${text}"`);
  } else if (/^[A-Z][A-Za-z0-9]+$/.test(key)) {
    kind = "named";
  } else {
    throw new Error(`shortcuts: invalid key in "${text}"`);
  }
  return {
    mod: mods.has("Mod"),
    ctrl: mods.has("Ctrl"),
    alt: mods.has("Alt"),
    shift: mods.has("Shift"),
    key: normalized,
    kind,
  };
}

const PRINTABLE_ASCII = /^[\x21-\x7e]$/;

/** Whether the event is this chord on the given platform. */
export function matchesChord(e: KeyChord, chord: Chord, mac: boolean = IS_MAC): boolean {
  const wantMeta = mac && chord.mod;
  const wantCtrl = chord.ctrl || (!mac && chord.mod);
  if (e.metaKey !== wantMeta || e.ctrlKey !== wantCtrl || e.altKey !== chord.alt) return false;

  switch (chord.kind) {
    case "letter": {
      if (e.shiftKey !== chord.shift) return false;
      const key = e.key.toLowerCase();
      // A Latin layout decides by character, also when this position types a symbol (Dvorak).
      if (/^[a-z]$/.test(key) || PRINTABLE_ASCII.test(e.key)) return key === chord.key;
      return e.code === `Key${chord.key.toUpperCase()}`;
    }
    case "digit":
      if (e.shiftKey !== chord.shift) return false;
      if (/^[0-9]$/.test(e.key)) return e.key === chord.key;
      // Shifted digits on AZERTY type symbols; fall back to the position.
      return e.code === `Digit${chord.key}` || e.code === `Numpad${chord.key}`;
    case "symbol":
      if (e.key === chord.key) return true;
      if (e.key.length === 1 && PRINTABLE_ASCII.test(e.key)) return false;
      // A non-Latin character (or a dead key): use the US position.
      return e.code === SYMBOL_CODES[chord.key] && e.shiftKey === (chord.key === "?");
    case "named":
      if (e.shiftKey !== chord.shift) return false;
      return chord.key === "Space" ? e.key === " " : e.key === chord.key;
  }
}

/**
 * A string that is equal for two chords exactly when they match the same
 * key presses on the platform (Ctrl+X and Mod+X are the same off macOS).
 */
export function chordSignature(chord: Chord, mac: boolean = IS_MAC): string {
  const meta = mac && chord.mod;
  const ctrl = chord.ctrl || (!mac && chord.mod);
  const shift = chord.kind === "symbol" ? "any" : String(chord.shift);
  return `meta=${meta} ctrl=${ctrl} alt=${chord.alt} shift=${shift} key=${chord.key}`;
}

/** Keys macOS shows as symbols. */
const MAC_KEY_SYMBOLS: Readonly<Record<string, string>> = {
  Enter: "↩",
  Backspace: "⌫",
  Delete: "⌦",
  Escape: "⎋",
  Tab: "⇥",
  ArrowLeft: "←",
  ArrowRight: "→",
  ArrowUp: "↑",
  ArrowDown: "↓",
};

/** Keys shown as words, by event.key: macOS writes Page Up short. Other keys (F10, letters) show as they are. */
const MAC_KEY_NAMES: Readonly<Record<string, MessageKey>> = {
  PageUp: "lib.keys.pageUpShort",
  PageDown: "lib.keys.pageDownShort",
  ContextMenu: "lib.keys.contextMenu",
  Space: "lib.keys.space",
  Home: "lib.keys.home",
  End: "lib.keys.end",
};

const OTHER_KEY_NAMES: Readonly<Record<string, MessageKey>> = {
  Enter: "lib.keys.enter",
  Backspace: "lib.keys.backspace",
  Delete: "lib.keys.delete",
  Escape: "lib.keys.escape",
  Tab: "lib.keys.tab",
  Space: "lib.keys.space",
  PageUp: "lib.keys.pageUp",
  PageDown: "lib.keys.pageDown",
  Home: "lib.keys.home",
  End: "lib.keys.end",
  Insert: "lib.keys.insert",
  ContextMenu: "lib.keys.contextMenu",
};

const ARROWS: Readonly<Record<string, string>> = {
  ArrowLeft: "←",
  ArrowRight: "→",
  ArrowUp: "↑",
  ArrowDown: "↓",
};

function keyLabel(chord: Chord, mac: boolean): string {
  if (chord.kind === "letter") return chord.key.toUpperCase();
  const symbol = mac ? MAC_KEY_SYMBOLS[chord.key] : ARROWS[chord.key];
  if (symbol !== undefined) return symbol;
  const name = (mac ? MAC_KEY_NAMES : OTHER_KEY_NAMES)[chord.key];
  return name === undefined ? chord.key : t(name);
}

/** How the chord is shown: "⌘⇧F" on macOS, "Ctrl+Shift+F" elsewhere. */
export function formatChord(chord: Chord, mac: boolean = IS_MAC): string {
  const key = keyLabel(chord, mac);
  if (mac) {
    // Apple's modifier order: Control, Option, Shift, Command.
    return `${chord.ctrl ? "⌃" : ""}${chord.alt ? "⌥" : ""}${chord.shift ? "⇧" : ""}${chord.mod ? "⌘" : ""}${key}`;
  }
  const mods = [chord.ctrl || chord.mod ? t("lib.keys.ctrl") : "", chord.alt ? t("lib.keys.alt") : "", chord.shift ? t("lib.keys.shift") : ""];
  return [...mods.filter((m) => m !== ""), key].join("+");
}

/** Keys that only modify another key press: never a chord's key. */
// i18n-ignore: event.key values
const MODIFIER_KEYS = new Set(["Meta", "Control", "Alt", "AltGraph", "Shift", "CapsLock", "Fn", "FnLock", "OS", "Hyper", "Super"]);

/**
 * The chord a key press makes, written as the registry writes chords
 * ("Mod+Shift+K"), for recording a custom shortcut (UI-07). Null while only
 * modifiers are held, for keys a chord cannot name (dead keys, IME, "+",
 * non-Latin characters without a US position), and off macOS for the
 * Windows or Super key, which chords cannot use there.
 *
 * - Letters are taken from the character, or from the key position when
 *   the layout types no Latin letter (Russian, or ⌥ on macOS), as matching
 *   does, so the recorded chord matches the same key press.
 * - Symbols are written without Shift ("Mod+?", not "Mod+Shift+/"): they
 *   match whatever modifiers the layout needs to type them.
 */
export function chordFromEvent(e: KeyChord, mac: boolean = IS_MAC): string | null {
  if (MODIFIER_KEYS.has(e.key) || e.key === "Dead" || e.key === "Unidentified" || e.key === "Process") return null;
  if (!mac && e.metaKey) return null;
  let key: string | null = null;
  let kind: ChordKeyKind = "named";
  if (/^[a-z]$/i.test(e.key)) {
    key = e.key.toUpperCase();
    kind = "letter";
  } else if (/^[0-9]$/.test(e.key)) {
    key = e.key;
    kind = "digit";
  } else if (e.key in SYMBOL_CODES) {
    key = e.key;
    kind = "symbol";
  } else if (e.key === " ") {
    key = "Space"; // i18n-ignore: chord syntax
  } else if (e.key.length > 1 && /^[A-Z][A-Za-z0-9]+$/.test(e.key)) {
    key = e.key;
  } else {
    // A character outside the US layout's: fall back to the key position.
    const letter = /^Key([A-Z])$/.exec(e.code ?? "");
    const digit = /^Digit([0-9])$/.exec(e.code ?? "");
    const symbol = Object.entries(SYMBOL_CODES).find(([s, code]) => code === e.code && s !== "?");
    if (letter) {
      key = letter[1];
      kind = "letter";
    } else if (digit) {
      key = digit[1];
      kind = "digit";
    } else if (symbol) {
      key = e.shiftKey && symbol[0] === "/" ? "?" : symbol[0];
      kind = "symbol";
    }
  }
  if (key === null) return null;
  const mods: string[] = [];
  // i18n-ignore-start: chord syntax
  if (mac ? e.metaKey : e.ctrlKey) mods.push("Mod");
  if (mac && e.ctrlKey) mods.push("Ctrl");
  if (e.altKey) mods.push("Alt");
  if (e.shiftKey && kind !== "symbol") mods.push("Shift");
  // i18n-ignore-end
  return [...mods, key].join("+");
}

/**
 * A keyboard event that is `chord` on the platform: what the command
 * palette dispatches to run a scoped shortcut where focus was (NAV-05).
 */
export function chordEventInit(chord: Chord, mac: boolean = IS_MAC): KeyboardEventInit {
  let key = chord.key;
  let code: string;
  switch (chord.kind) {
    case "letter":
      key = chord.shift ? chord.key.toUpperCase() : chord.key;
      code = `Key${chord.key.toUpperCase()}`; // i18n-ignore: KeyboardEvent.code
      break;
    case "digit":
      code = `Digit${chord.key}`; // i18n-ignore: KeyboardEvent.code
      break;
    case "symbol":
      code = SYMBOL_CODES[chord.key] ?? "";
      break;
    case "named":
      key = chord.key === "Space" ? " " : chord.key;
      code = chord.key;
      break;
  }
  return {
    key,
    code,
    metaKey: mac && chord.mod,
    ctrlKey: chord.ctrl || (!mac && chord.mod),
    altKey: chord.alt,
    shiftKey: chord.shift || (chord.kind === "symbol" && chord.key === "?"),
    bubbles: true,
    cancelable: true,
  };
}

/** The chord for aria-keyshortcuts: "Meta+Shift+F" or "Control+Shift+F". */
export function ariaChord(chord: Chord, mac: boolean = IS_MAC): string {
  const meta = mac && chord.mod;
  const ctrl = chord.ctrl || (!mac && chord.mod);
  const key = chord.kind === "letter" ? chord.key.toUpperCase() : chord.key;
  // i18n-ignore: aria-keyshortcuts names
  return [meta ? "Meta" : "", ctrl ? "Control" : "", chord.alt ? "Alt" : "", chord.shift ? "Shift" : "", key]
    .filter((p) => p !== "")
    .join("+");
}
