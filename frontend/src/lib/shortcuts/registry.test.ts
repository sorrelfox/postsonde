import { afterEach, describe, expect, it, vi } from "vitest";
import { chordSignature, parseChord } from "./keys";
import {
  ariaShortcut,
  checkShortcutBinding,
  findConflicts,
  getShortcut,
  getShortcutOverrides,
  isShortcutCustomized,
  matchesShortcut,
  setShortcutOverrides,
  SHORTCUT_GROUPS,
  SHORTCUTS,
  shortcutChords,
  shortcutKeys,
  shortcutLabel,
  shortcutLabels,
  subscribeShortcutOverrides,
  type ShortcutDefinition,
} from "./registry";

const PLATFORMS = [
  ["macOS", true],
  ["Windows and Linux", false],
] as const;

describe("the shortcut registry", () => {
  it("has unique ids, valid chords and known groups", () => {
    expect(new Set(SHORTCUTS.map((s) => s.id)).size).toBe(SHORTCUTS.length);
    for (const def of SHORTCUTS) {
      expect(def.keys.length).toBeGreaterThan(0);
      for (const k of def.keys) expect(() => parseChord(k)).not.toThrow();
      expect(SHORTCUT_GROUPS).toContain(def.group);
    }
  });

  it("has the core shortcuts of UI-06", () => {
    const core: Record<string, string> = {
      "request.send": "Mod+Enter",
      "request.new": "Mod+N",
      "tab.close": "Mod+W",
      "request.beautify": "Mod+Shift+F",
      "environment.switch": "Mod+E",
      "settings.open": "Mod+,",
      "help.toggle": "Mod+/",
    };
    for (const [id, chord] of Object.entries(core)) {
      expect(getShortcut(id).scope).toBe("global");
      expect(getShortcut(id).keys[0]).toBe(chord);
    }
  });

  it("opens a new window with Mod+Shift+N anywhere (APP-06)", () => {
    expect(getShortcut("window.new")).toMatchObject({ scope: "global", group: "general", keys: ["Mod+Shift+N"] });
    expect(shortcutLabel("window.new", true)).toBe("⇧⌘N");
    expect(shortcutLabel("window.new", false)).toBe("Ctrl+Shift+N");
  });

  it("reopens closed tabs with Mod+Shift+T anywhere (NAV-01)", () => {
    expect(getShortcut("tab.reopenClosed")).toMatchObject({ scope: "global", group: "tabs", keys: ["Mod+Shift+T"] });
    expect(shortcutLabel("tab.reopenClosed", true)).toBe("⇧⌘T");
    expect(shortcutLabel("tab.reopenClosed", false)).toBe("Ctrl+Shift+T");
    const e = { key: "T", code: "KeyT", metaKey: false, ctrlKey: true, altKey: false, shiftKey: true };
    expect(matchesShortcut(e, "tab.reopenClosed", false)).toBe(true);
  });

  it("prettifies a GraphQL query with Mod+Shift+F inside the GraphQL editor (GQL-18)", () => {
    expect(getShortcut("graphql.prettify")).toMatchObject({ scope: "graphql", keys: ["Mod+Shift+F"] });
    expect(shortcutLabel("graphql.prettify", true)).toBe("⇧⌘F");
    expect(shortcutLabel("graphql.prettify", false)).toBe("Ctrl+Shift+F");
  });

  it("toggles the GraphQL docs with Mod+Shift+D (GQL-14)", () => {
    expect(getShortcut("graphql.docs")).toMatchObject({ scope: "global", keys: ["Mod+Shift+D"] });
    expect(shortcutLabel("graphql.docs", true)).toBe("⇧⌘D");
    expect(shortcutLabel("graphql.docs", false)).toBe("Ctrl+Shift+D");
  });

  it("fills fields and shows the docs at the cursor inside the GraphQL editor (GQL-15, GQL-17)", () => {
    expect(getShortcut("graphql.fillFields")).toMatchObject({ scope: "graphql", keys: ["Mod+Alt+F"] });
    expect(shortcutLabel("graphql.fillFields", true)).toBe("⌥⌘F");
    expect(shortcutLabel("graphql.fillFields", false)).toBe("Ctrl+Alt+F");
    expect(getShortcut("graphql.docsAtCursor")).toMatchObject({ scope: "graphql", keys: ["Mod+Alt+D"] });
    // Matched by key code: ⌥ changes the character on macOS (⌥F types "ƒ").
    const e = { key: "ƒ", code: "KeyF", metaKey: true, ctrlKey: false, altKey: true, shiftKey: false };
    expect(matchesShortcut(e, "graphql.fillFields", true)).toBe(true);
  });

  it("marks the shortcuts that are not built yet", () => {
    const placeholders = SHORTCUTS.filter((s) => s.placeholder !== undefined).map((s) => s.id);
    expect(placeholders.sort()).toEqual([]);
  });

  describe.each(PLATFORMS)("on %s", (_name, mac) => {
    it("has no two shortcuts on the same chord in the same scope", () => {
      expect(findConflicts(SHORTCUTS, mac)).toEqual([]);
    });

    it("overrides a global chord in a scope only on purpose", () => {
      const global = new Map<string, string>();
      for (const def of SHORTCUTS.filter((s) => s.scope === "global")) {
        for (const c of shortcutChords(def, mac)) global.set(chordSignature(c, mac), def.id);
      }
      const overrides: string[] = [];
      for (const def of SHORTCUTS.filter((s) => s.scope !== "global")) {
        for (const c of shortcutChords(def, mac)) {
          const other = global.get(chordSignature(c, mac));
          if (other !== undefined) overrides.push(`${def.id} > ${other}`);
        }
      }
      // Inside a table ⌘Enter adds a row; in a message composer it sends the message; in an editor ⌘/ toggles a comment;
      // in a GraphQL query editor ⇧⌘F prettifies the query instead of a JSON body;
      // in a request editor ⌘Z undoes an edit (after an Undo-delete toast had its turn, commands/undoRedo.ts).
      expect(overrides.sort()).toEqual([
        "composer.send > request.send",
        "editor.toggleComment > help.toggle",
        "graphql.prettify > request.beautify",
        "request.undo > collection.undoDelete",
        "table.insertRow > request.send",
      ]);
    });
  });

  it("opens the command palette with Mod+Shift+P and toggles focus mode with Mod+Shift+Enter (NAV-05, UI-09)", () => {
    expect(getShortcut("palette.open")).toMatchObject({ scope: "global", keys: ["Mod+Shift+P"] });
    expect(shortcutLabel("palette.open", true)).toBe("⇧⌘P");
    expect(shortcutLabel("palette.open", false)).toBe("Ctrl+Shift+P");
    expect(getShortcut("view.focusMode")).toMatchObject({ scope: "global", keys: ["Mod+Shift+Enter"] });
    expect(shortcutLabel("view.focusMode", true)).toBe("⇧⌘↩");
  });

  it("keeps CodeMirror's own keymaps fixed", () => {
    expect(SHORTCUTS.filter((s) => s.fixed).map((s) => s.id)).toEqual(["editor.find", "editor.selectNext", "editor.toggleComment"]);
    expect(SHORTCUTS.filter((s) => s.fixed).every((s) => s.scope === "editor")).toBe(true);
  });

  it("finds conflicts", () => {
    const defs: ShortcutDefinition[] = [
      { id: "a", labelKey: "lib.shortcuts.action.tab.close", group: "general", keys: ["Mod+W"], scope: "global" },
      { id: "b", labelKey: "lib.shortcuts.action.sidebar.toggle", group: "general", keys: ["Ctrl+W"], scope: "global" },
      { id: "c", labelKey: "lib.shortcuts.action.table.insertRow", group: "tables", keys: ["Mod+W"], scope: "table" },
    ];
    // Ctrl is ⌃ on macOS, so only Windows and Linux clash.
    expect(findConflicts(defs, true)).toEqual([]);
    expect(findConflicts(defs, false)).toEqual([{ scope: "global", chord: "Ctrl+W", ids: ["a", "b"] }]);
  });

  it("labels and matches by id", () => {
    expect(shortcutLabel("tab.close", true)).toBe("⌘W");
    expect(shortcutLabel("tab.close", false)).toBe("Ctrl+W");
    expect(shortcutLabels("help.toggle", true)).toEqual(["⌘/", "?"]);
    // Ctrl+Shift+PageUp and Mod+Shift+PageUp are one chord off macOS.
    expect(shortcutLabels("tabs.moveLeft", false)).toEqual(["Ctrl+Shift+PageUp", "Alt+Shift+←"]);
    expect(shortcutLabels("tabs.moveLeft", true)).toEqual(["⌃⇧PgUp", "⇧⌘PgUp", "⌥⇧←"]);
    expect(ariaShortcut("table.deleteRow", false)).toBe("Control+Shift+Backspace Control+Shift+Delete");
    const e = { key: "Enter", metaKey: false, ctrlKey: true, altKey: false, shiftKey: false };
    expect(matchesShortcut(e, "request.send", false)).toBe(true);
    expect(matchesShortcut(e, "request.send", true)).toBe(false);
    expect(() => getShortcut("nope")).toThrow();
  });
});

describe("custom shortcuts (UI-07)", () => {
  afterEach(() => {
    setShortcutOverrides({});
  });

  it("applies overrides to matching, labels and conflicts, and notifies", () => {
    const listener = vi.fn();
    const unsubscribe = subscribeShortcutOverrides(listener);
    const applied = setShortcutOverrides({ "request.send": ["Mod+Shift+K"] });
    expect(applied).toEqual({ "request.send": ["Mod+Shift+K"] });
    expect(listener).toHaveBeenCalledTimes(1);
    expect(isShortcutCustomized("request.send")).toBe(true);
    expect(shortcutKeys(getShortcut("request.send"))).toEqual(["Mod+Shift+K"]);
    expect(shortcutLabel("request.send", true)).toBe("⇧⌘K");
    const enter = { key: "Enter", metaKey: false, ctrlKey: true, altKey: false, shiftKey: false };
    const k = { key: "K", code: "KeyK", metaKey: false, ctrlKey: true, altKey: false, shiftKey: true };
    expect(matchesShortcut(enter, "request.send", false)).toBe(false);
    expect(matchesShortcut(k, "request.send", false)).toBe(true);

    // The same overrides again: same object, no notification.
    expect(setShortcutOverrides({ "request.send": ["Mod+Shift+K"] })).toBe(applied);
    expect(listener).toHaveBeenCalledTimes(1);

    // A clash made by an override shows up like any other.
    setShortcutOverrides({ "sidebar.toggle": ["Mod+W"] });
    expect(findConflicts(SHORTCUTS, true)).toEqual([{ scope: "global", chord: "⌘W", ids: ["sidebar.toggle", "tab.close"] }]);
    unsubscribe();
    setShortcutOverrides({});
    expect(listener).toHaveBeenCalledTimes(2);
    expect(matchesShortcut(enter, "request.send", false)).toBe(true);
  });

  it("drops unknown ids, fixed shortcuts and invalid chords", () => {
    const applied = setShortcutOverrides({
      "no.such": ["Mod+K"],
      "editor.find": ["Mod+Shift+K"],
      "request.new": ["Cmd+N", "Mod+Alt+N"],
      "tab.close": ["Mod+"],
    });
    expect(applied).toEqual({ "request.new": ["Mod+Alt+N"] });
    expect(getShortcutOverrides()).toBe(applied);
    expect(isShortcutCustomized("editor.find")).toBe(false);
  });

  it("checks a new chord against the shortcuts of the same scope, reserved keys and typing", () => {
    const errors = (id: string, chord: string, mac: boolean) =>
      checkShortcutBinding(id, [chord], mac).filter((p) => p.level === "error").map((p) => p.message);
    const warnings = (id: string, chord: string, mac: boolean) =>
      checkShortcutBinding(id, [chord], mac).filter((p) => p.level === "warning").map((p) => p.message);

    expect(checkShortcutBinding("request.send", ["Mod+Shift+K"], true)).toEqual([]);
    // Same scope: refused, naming the other one.
    expect(errors("sidebar.toggle", "Mod+W", true)).toEqual(["⌘W is already “Close tab”."]);
    expect(checkShortcutBinding("sidebar.toggle", ["Mod+W"], true)[0]?.otherId).toBe("tab.close");
    expect(errors("tree.rename", "Mod+D", false)).toEqual(["Ctrl+D is already “Duplicate the item”."]);
    // Reserved by the OS or by every text field.
    expect(errors("sidebar.toggle", "Mod+Q", true)).toEqual(["⌘Q quits the app."]);
    expect(errors("sidebar.toggle", "Mod+Q", false)).toEqual([]);
    expect(errors("sidebar.toggle", "Alt+F4", false)).toEqual(["Alt+F4 closes the window."]);
    expect(errors("sidebar.toggle", "Mod+C", false)).toEqual(["Ctrl+C copies in text fields and editors."]);
    // A plain character would be taken from typing.
    expect(errors("sidebar.toggle", "K", true)).toEqual(["K types a character. Add ⌘, ⌃ or ⌥."]);
    expect(errors("sidebar.toggle", "Shift+K", false)).toEqual(["Shift+K types a character. Add Ctrl or Alt."]);
    // Fixed ones cannot change.
    expect(errors("editor.find", "Mod+Shift+K", true)).toHaveLength(1);
    // Another scope takes the chord where it applies: allowed, with a warning.
    expect(errors("sidebar.toggle", "Mod+F", true)).toEqual([]);
    expect(warnings("sidebar.toggle", "Mod+F", true)).toEqual([
      "⌘F is “Find and replace” in a code editor, so this one does not run there.",
    ]);
    expect(warnings("tree.rename", "Mod+B", true)).toEqual([
      "⌘B is also “Show or hide the sidebar”, which this one replaces in the collection tree.",
    ]);
    // A global chord without ⌘ or Ctrl does not run while typing.
    expect(warnings("sidebar.toggle", "F8", false)).toEqual(["F8 does not run while you type in a text field."]);
    // Its own default is fine.
    expect(checkShortcutBinding("help.toggle", ["?"], true)).toEqual([]);
  });

  it("compares with the chords in effect", () => {
    setShortcutOverrides({ "tab.close": ["Mod+Shift+W"] });
    expect(errors2("sidebar.toggle", "Mod+W")).toEqual([]);
    expect(errors2("sidebar.toggle", "Mod+Shift+W")).toEqual(["⇧⌘W is already “Close tab”."]);
  });
});

function errors2(id: string, chord: string): string[] {
  return checkShortcutBinding(id, [chord], true)
    .filter((p) => p.level === "error")
    .map((p) => p.message);
}
