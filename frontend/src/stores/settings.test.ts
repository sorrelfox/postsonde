import { describe, expect, it } from "vitest";
import {
  editorSettingsOf,
  initialSettingsState,
  isValidDefaultTimeout,
  isValidFontFamily,
  isValidFontSize,
  isValidShortcutOverrides,
  isValidTabSize,
  MAX_DEFAULT_TIMEOUT_MS,
  MAX_SPLIT_RATIO,
  MIN_DEFAULT_TIMEOUT_MS,
  MIN_SPLIT_RATIO,
  settingsReducer,
  type SettingsAction,
  type SettingsState,
} from "./settings";

const reduce = (action: SettingsAction, state: SettingsState = initialSettingsState) => settingsReducer(state, action);

describe("settingsReducer", () => {
  it("starts with the system theme, an open sidebar and an even split", () => {
    expect(initialSettingsState).toEqual({
      theme: "system",
      customTheme: "",
      locale: "system",
      sidebarCollapsed: false,
      splitRatio: 0.5,
      defaultTimeoutMs: 0,
      verifyTls: true,
      maxResponseBodyMb: 512,
      responsePosition: "beside",
      splitRatioBelow: 0.5,
      editorFontFamily: "JetBrains Mono",
      editorFontSize: 12,
      editorTabSize: 2,
      editorWordWrap: false,
      editorLineNumbers: true,
      editorMatchBrackets: true,
      shortcuts: {},
    });
  });

  it("sets the theme, returning the same state when unchanged", () => {
    expect(reduce({ type: "settings/setTheme", theme: "dark" }).theme).toBe("dark");
    expect(reduce({ type: "settings/setTheme", theme: "system" })).toBe(initialSettingsState);
  });

  it("toggles and sets the sidebar", () => {
    const collapsed = reduce({ type: "settings/toggleSidebar" });
    expect(collapsed.sidebarCollapsed).toBe(true);
    expect(reduce({ type: "settings/toggleSidebar" }, collapsed).sidebarCollapsed).toBe(false);
    expect(reduce({ type: "settings/setSidebarCollapsed", collapsed: true }).sidebarCollapsed).toBe(true);
    expect(reduce({ type: "settings/setSidebarCollapsed", collapsed: false })).toBe(initialSettingsState);
  });

  it("clamps the split ratio and ignores non-finite values", () => {
    expect(reduce({ type: "settings/setSplitRatio", ratio: 0.3 }).splitRatio).toBe(0.3);
    expect(reduce({ type: "settings/setSplitRatio", ratio: 0 }).splitRatio).toBe(MIN_SPLIT_RATIO);
    expect(reduce({ type: "settings/setSplitRatio", ratio: 2 }).splitRatio).toBe(MAX_SPLIT_RATIO);
    expect(reduce({ type: "settings/setSplitRatio", ratio: Number.NaN })).toBe(initialSettingsState);
    expect(reduce({ type: "settings/setSplitRatio", ratio: 0.5 })).toBe(initialSettingsState);
  });

  it("loads valid values and keeps the current ones for invalid input", () => {
    const loaded = reduce({
      type: "settings/load",
      settings: {
        theme: "light",
        customTheme: "ocean.json",
        locale: "en-XA",
        sidebarCollapsed: true,
        splitRatio: 0.95,
        defaultTimeoutMs: 30000,
        verifyTls: false,
        maxResponseBodyMb: 2048,
        responsePosition: "below",
        splitRatioBelow: 0.1,
        editorFontFamily: "Menlo",
        editorFontSize: 16,
        editorTabSize: 4,
        editorWordWrap: true,
        editorLineNumbers: false,
        editorMatchBrackets: false,
      },
    });
    expect(loaded).toEqual({
      theme: "light",
      customTheme: "ocean.json",
      locale: "en-XA",
      sidebarCollapsed: true,
      splitRatio: MAX_SPLIT_RATIO,
      defaultTimeoutMs: 30000,
      verifyTls: false,
      maxResponseBodyMb: 2048,
      responsePosition: "below",
      splitRatioBelow: MIN_SPLIT_RATIO,
      editorFontFamily: "Menlo",
      editorFontSize: 16,
      editorTabSize: 4,
      editorWordWrap: true,
      editorLineNumbers: false,
      editorMatchBrackets: false,
      shortcuts: {},
    });

    const invalid = reduce({
      type: "settings/load",
      settings: {
        theme: "neon",
        customTheme: "../ocean.json",
        locale: "en_US",
        sidebarCollapsed: "yes",
        splitRatio: "wide",
        defaultTimeoutMs: 500,
        verifyTls: "off",
        responsePosition: "left",
        splitRatioBelow: "tall",
        editorFontFamily: "Menlo, monospace",
        editorFontSize: 12.5,
        editorTabSize: 0,
        editorWordWrap: 1,
        editorLineNumbers: "no",
        editorMatchBrackets: null,
      },
    });
    expect(invalid).toBe(initialSettingsState);
    expect(reduce({ type: "settings/load", settings: {} })).toBe(initialSettingsState);
  });

  it("accepts no default timeout or one from a second to a day", () => {
    expect(isValidDefaultTimeout(0)).toBe(true);
    expect(isValidDefaultTimeout(MIN_DEFAULT_TIMEOUT_MS)).toBe(true);
    expect(isValidDefaultTimeout(MAX_DEFAULT_TIMEOUT_MS)).toBe(true);
    for (const bad of [999, -1000, MAX_DEFAULT_TIMEOUT_MS + 1, 1500.5, Number.NaN, "30000", null]) {
      expect(isValidDefaultTimeout(bad)).toBe(false);
    }
  });

  it("sets the split of the layout in effect, or of the one given", () => {
    const below = reduce({ type: "settings/load", settings: { responsePosition: "below" } });
    const s1 = reduce({ type: "settings/setSplitRatio", ratio: 0.3 }, below);
    expect(s1).toMatchObject({ splitRatio: 0.5, splitRatioBelow: 0.3 });
    const s2 = reduce({ type: "settings/setSplitRatio", ratio: 0.9, position: "beside" }, s1);
    expect(s2).toMatchObject({ splitRatio: MAX_SPLIT_RATIO, splitRatioBelow: 0.3 });
    expect(reduce({ type: "settings/setSplitRatio", ratio: 0.3 }, s1)).toBe(s1);
  });

  it("accepts the font names, sizes and tab sizes the settings file does", () => {
    for (const ok of ["JetBrains Mono", "ui-monospace", "Source Code Pro", "Ｍｏｎｏ", "a".repeat(64)]) {
      expect(isValidFontFamily(ok)).toBe(true);
    }
    for (const bad of ["", " Menlo", "Menlo ", 'Me"nlo', "Me'nlo", "Menlo, monospace", "a;b", "a{b", "a\\b", "a<b", "a\nb", "a".repeat(65), 5]) {
      expect(isValidFontFamily(bad)).toBe(false);
    }
    expect([8, 12, 32].every(isValidFontSize)).toBe(true);
    expect([7, 33, 12.5, "12", Number.NaN].some(isValidFontSize)).toBe(false);
    expect([1, 2, 8].every(isValidTabSize)).toBe(true);
    expect([0, 9, 2.5, "4"].some(isValidTabSize)).toBe(false);
  });

  it("picks out the editor settings", () => {
    expect(editorSettingsOf(initialSettingsState)).toEqual({
      editorFontFamily: "JetBrains Mono",
      editorFontSize: 12,
      editorTabSize: 2,
      editorWordWrap: false,
      editorLineNumbers: true,
      editorMatchBrackets: true,
    });
  });

  it("loads the custom shortcuts (UI-07) as a whole, keeping the same object when they are equal", () => {
    const shortcuts = { "request.send": ["Mod+Shift+Enter"], "view.focusMode": ["F11", "Mod+Alt+Z"] };
    const loaded = reduce({ type: "settings/load", settings: { shortcuts } });
    expect(loaded.shortcuts).toEqual(shortcuts);
    // An equal copy (every settings:changed event brings a new one) changes nothing.
    expect(reduce({ type: "settings/load", settings: { shortcuts: structuredClone(shortcuts) } }, loaded)).toBe(loaded);
    // {} resets them; a missing section keeps them.
    expect(reduce({ type: "settings/load", settings: { shortcuts: {} } }, loaded).shortcuts).toEqual({});
    expect(reduce({ type: "settings/load", settings: {} }, loaded)).toBe(loaded);
    for (const bad of [
      ["Mod+K"],
      { "Request Send": ["Mod+K"] },
      { "request.send": [] },
      { "request.send": ["Mod+K", "Mod+K"] },
      { "request.send": ["Mod++"] },
      { "request.send": ["F1", "F2", "F3", "F4", "F5"] },
      { "request.send": "Mod+K" },
    ]) {
      expect(isValidShortcutOverrides(bad)).toBe(false);
      expect(reduce({ type: "settings/load", settings: { shortcuts: bad } }, loaded)).toBe(loaded);
    }
    expect(isValidShortcutOverrides({ "help.toggle": ["?", "Mod+/"], "tabs.moveLeft": ["Alt+Shift+ArrowLeft"] })).toBe(true);
  });

  it("ignores unknown actions", () => {
    expect(settingsReducer(initialSettingsState, { type: "nope" } as unknown as SettingsAction)).toBe(
      initialSettingsState,
    );
  });
});
