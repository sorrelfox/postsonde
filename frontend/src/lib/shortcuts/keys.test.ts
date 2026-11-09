import { describe, expect, it } from "vitest";
import { ariaChord, chordEventInit, chordFromEvent, chordSignature, formatChord, isMacPlatform, matchesChord, parseChord, type KeyChord } from "./keys";

const ev = (over: Partial<KeyChord>): KeyChord => ({
  key: "",
  metaKey: false,
  ctrlKey: false,
  altKey: false,
  shiftKey: false,
  ...over,
});

const matches = (chord: string, e: Partial<KeyChord>, mac: boolean) => matchesChord(ev(e), parseChord(chord), mac);

describe("parseChord", () => {
  it("parses modifiers and the key", () => {
    expect(parseChord("Mod+Shift+F")).toEqual({ mod: true, ctrl: false, alt: false, shift: true, key: "f", kind: "letter" });
    expect(parseChord("Alt+Shift+ArrowLeft")).toMatchObject({ alt: true, shift: true, key: "ArrowLeft", kind: "named" });
    expect(parseChord("?")).toMatchObject({ mod: false, key: "?", kind: "symbol" });
    expect(parseChord("Mod+1")).toMatchObject({ key: "1", kind: "digit" });
  });

  it("rejects invalid chords", () => {
    expect(() => parseChord("Cmd+W")).toThrow();
    expect(() => parseChord("Mod+Mod+W")).toThrow();
    expect(() => parseChord("Mod+Shift+/")).toThrow(); // symbols never name Shift
    expect(() => parseChord("Mod+")).toThrow();
    expect(() => parseChord("Mod+enter")).toThrow();
  });
});

describe("matchesChord per platform", () => {
  it("uses ⌘ for Mod on macOS and Ctrl elsewhere", () => {
    expect(matches("Mod+W", { key: "w", code: "KeyW", metaKey: true }, true)).toBe(true);
    expect(matches("Mod+W", { key: "w", code: "KeyW", ctrlKey: true }, true)).toBe(false);
    expect(matches("Mod+W", { key: "w", code: "KeyW", ctrlKey: true }, false)).toBe(true);
    expect(matches("Mod+W", { key: "w", code: "KeyW", metaKey: true }, false)).toBe(false);
  });

  it("requires the exact modifiers", () => {
    expect(matches("Mod+W", { key: "w", ctrlKey: true, shiftKey: true }, false)).toBe(false);
    expect(matches("Mod+W", { key: "w", ctrlKey: true, metaKey: true }, false)).toBe(false);
    expect(matches("Mod+W", { key: "w" }, false)).toBe(false);
    expect(matches("Mod+Shift+F", { key: "F", code: "KeyF", ctrlKey: true, shiftKey: true }, false)).toBe(true);
    expect(matches("Mod+Shift+F", { key: "f", code: "KeyF", metaKey: true, shiftKey: true }, true)).toBe(true);
  });

  it("does not treat AltGr (Ctrl+Alt on Windows) as Mod", () => {
    // German layout: AltGr+Q types "@".
    expect(matches("Mod+K", { key: "@", code: "KeyQ", ctrlKey: true, altKey: true }, false)).toBe(false);
    expect(matches("Mod+E", { key: "€", code: "KeyE", ctrlKey: true, altKey: true }, false)).toBe(false);
  });

  it("keeps Ctrl literal on macOS and the same as Mod elsewhere", () => {
    expect(matches("Ctrl+Shift+PageUp", { key: "PageUp", ctrlKey: true, shiftKey: true }, true)).toBe(true);
    expect(matches("Ctrl+Shift+PageUp", { key: "PageUp", metaKey: true, shiftKey: true }, true)).toBe(false);
    expect(matches("Ctrl+Shift+PageUp", { key: "PageUp", ctrlKey: true, shiftKey: true }, false)).toBe(true);
  });
});

describe("matchesChord on other keyboard layouts", () => {
  it("follows the character on Latin layouts (QWERTZ, AZERTY, Dvorak)", () => {
    // QWERTZ: Z and Y are swapped; the key labelled Z is at KeyY.
    expect(matches("Mod+Z", { key: "z", code: "KeyY", ctrlKey: true }, false)).toBe(true);
    expect(matches("Mod+Y", { key: "z", code: "KeyY", ctrlKey: true }, false)).toBe(false);
    // AZERTY: W is where QWERTY has Z.
    expect(matches("Mod+W", { key: "w", code: "KeyZ", ctrlKey: true }, false)).toBe(true);
    // Dvorak: "w" is typed by the Comma position; KeyW types ",".
    expect(matches("Mod+W", { key: "w", code: "Comma", metaKey: true }, true)).toBe(true);
    expect(matches("Mod+W", { key: ",", code: "KeyW", metaKey: true }, true)).toBe(false);
  });

  it("falls back to the US position for non-Latin letters (Russian)", () => {
    expect(matches("Mod+W", { key: "ц", code: "KeyW", ctrlKey: true }, false)).toBe(true);
    expect(matches("Mod+E", { key: "у", code: "KeyE", metaKey: true }, true)).toBe(true);
    expect(matches("Mod+Shift+F", { key: "А", code: "KeyF", ctrlKey: true, shiftKey: true }, false)).toBe(true);
    expect(matches("Mod+W", { key: "ц", code: "KeyE", ctrlKey: true }, false)).toBe(false);
    // ⌥ turns letters into symbols on macOS; the position still counts.
    expect(matches("Alt+F", { key: "ƒ", code: "KeyF", altKey: true }, true)).toBe(true);
  });

  it("matches symbols by character, whatever Shift it takes", () => {
    // German: "/" is Shift+7, "?" is Shift+ß.
    expect(matches("Mod+/", { key: "/", code: "Digit7", ctrlKey: true, shiftKey: true }, false)).toBe(true);
    expect(matches("?", { key: "?", code: "Minus", shiftKey: true }, false)).toBe(true);
    // US: "?" is Shift+/.
    expect(matches("?", { key: "?", code: "Slash", shiftKey: true }, true)).toBe(true);
    expect(matches("Mod+/", { key: "?", code: "Slash", ctrlKey: true, shiftKey: true }, false)).toBe(false);
    expect(matches("Mod+,", { key: ",", code: "Comma", metaKey: true }, true)).toBe(true);
  });

  it("falls back to the US position for non-Latin symbols (Russian)", () => {
    // Russian: the Comma key types "б", the Slash key types ".".
    expect(matches("Mod+,", { key: "б", code: "Comma", ctrlKey: true }, false)).toBe(true);
    expect(matches("Mod+/", { key: ".", code: "Slash", ctrlKey: true }, false)).toBe(false);
    expect(matches("Mod+/", { key: "Dead", code: "Slash", ctrlKey: true }, false)).toBe(true);
  });

  it("matches named keys exactly", () => {
    expect(matches("Mod+Enter", { key: "Enter", code: "NumpadEnter", ctrlKey: true }, false)).toBe(true);
    expect(matches("Mod+Enter", { key: "Enter", ctrlKey: true, shiftKey: true }, false)).toBe(false);
    expect(matches("Shift+F10", { key: "F10", shiftKey: true }, false)).toBe(true);
    expect(matches("Space", { key: " ", code: "Space" }, false)).toBe(true);
  });
});

describe("chordSignature", () => {
  it("is equal when two chords react to the same presses", () => {
    expect(chordSignature(parseChord("Ctrl+W"), false)).toBe(chordSignature(parseChord("Mod+W"), false));
    expect(chordSignature(parseChord("Ctrl+W"), true)).not.toBe(chordSignature(parseChord("Mod+W"), true));
    expect(chordSignature(parseChord("Mod+Shift+W"), false)).not.toBe(chordSignature(parseChord("Mod+W"), false));
  });
});

describe("formatting", () => {
  it("shows ⌘ symbols on macOS and Ctrl+ names elsewhere", () => {
    const cases: [string, string, string][] = [
      ["Mod+B", "⌘B", "Ctrl+B"],
      ["Mod+Shift+F", "⇧⌘F", "Ctrl+Shift+F"],
      ["Mod+Enter", "⌘↩", "Ctrl+Enter"],
      ["Mod+Shift+Backspace", "⇧⌘⌫", "Ctrl+Shift+Backspace"],
      ["Mod+,", "⌘,", "Ctrl+,"],
      ["Alt+Shift+ArrowLeft", "⌥⇧←", "Alt+Shift+←"],
      ["Ctrl+Shift+PageUp", "⌃⇧PgUp", "Ctrl+Shift+PageUp"],
      ["?", "?", "?"],
      ["ContextMenu", "Menu", "Menu"],
    ];
    for (const [chord, mac, other] of cases) {
      expect(formatChord(parseChord(chord), true)).toBe(mac);
      expect(formatChord(parseChord(chord), false)).toBe(other);
    }
  });

  it("builds aria-keyshortcuts values", () => {
    expect(ariaChord(parseChord("Mod+B"), true)).toBe("Meta+B");
    expect(ariaChord(parseChord("Mod+B"), false)).toBe("Control+B");
    expect(ariaChord(parseChord("Mod+Shift+Backspace"), true)).toBe("Meta+Shift+Backspace");
    expect(ariaChord(parseChord("Mod+Shift+Backspace"), false)).toBe("Control+Shift+Backspace");
  });

  it("detects macOS", () => {
    expect(isMacPlatform({ platform: "MacIntel", userAgent: "" })).toBe(true);
    expect(isMacPlatform({ platform: "Win32", userAgent: "" })).toBe(false);
    expect(isMacPlatform({ platform: "Linux x86_64", userAgent: "" })).toBe(false);
    expect(isMacPlatform({ platform: "", userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X)" })).toBe(true);
  });
});

describe.each([
  ["macOS", true],
  ["Windows and Linux", false],
] as const)("recording and synthesizing chords on %s (UI-07, NAV-05)", (_name, mac) => {
  const mod = mac ? { metaKey: true } : { ctrlKey: true };

  it("records the chord of a key press as the registry writes it", () => {
    expect(chordFromEvent(ev({ key: "k", code: "KeyK", ...mod }), mac)).toBe("Mod+K");
    expect(chordFromEvent(ev({ key: "K", code: "KeyK", shiftKey: true, ...mod }), mac)).toBe("Mod+Shift+K");
    expect(chordFromEvent(ev({ key: "Enter", code: "Enter", shiftKey: true, ...mod }), mac)).toBe("Mod+Shift+Enter");
    expect(chordFromEvent(ev({ key: "F6", code: "F6" }), mac)).toBe("F6");
    expect(chordFromEvent(ev({ key: " ", code: "Space", ctrlKey: true }), mac)).toBe(mac ? "Ctrl+Space" : "Mod+Space");
    // Symbols never name Shift: "?" is whatever the layout needs.
    expect(chordFromEvent(ev({ key: "?", code: "Slash", shiftKey: true, ...mod }), mac)).toBe("Mod+?");
    // Only modifiers held: nothing yet.
    expect(chordFromEvent(ev({ key: mac ? "Meta" : "Control", ...mod }), mac)).toBeNull();
    expect(chordFromEvent(ev({ key: "Dead", code: "Quote" }), mac)).toBeNull();
    // A non-Latin layout (Russian "л" is the K key) falls back to the position.
    expect(chordFromEvent(ev({ key: "л", code: "KeyK", ...mod }), mac)).toBe("Mod+K");
  });

  it("records ⌥ chords by position, and refuses the Windows key off macOS", () => {
    expect(chordFromEvent(ev({ key: "ƒ", code: "KeyF", altKey: true, ...mod }), mac)).toBe("Mod+Alt+F");
    expect(chordFromEvent(ev({ key: "k", code: "KeyK", metaKey: true, ctrlKey: true }), mac)).toBe(mac ? "Mod+Ctrl+K" : null);
  });

  it("synthesizes a key press that matches the chord, for every chord it can record", () => {
    for (const text of ["Mod+K", "Mod+Shift+Enter", "Mod+Alt+F", "Ctrl+Space", "Alt+Shift+ArrowLeft", "Mod+/", "?", "Mod+,", "F2", "Mod+1", "Shift+F10"]) {
      const chord = parseChord(text);
      const init = chordEventInit(chord, mac);
      const e = ev({ ...init, key: init.key ?? "", code: init.code });
      expect(matchesChord(e, chord, mac), text).toBe(true);
      const recorded = chordFromEvent(e, mac);
      expect(recorded !== null && chordSignature(parseChord(recorded), mac) === chordSignature(chord, mac), text).toBe(true);
    }
  });
});
