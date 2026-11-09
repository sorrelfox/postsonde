import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ChangedEventDTO, SettingsDTO } from "./settings";

const service = vi.hoisted(() => ({
  Get: vi.fn<() => Promise<unknown>>(),
  Update: vi.fn<(patch: unknown) => Promise<unknown>>(),
  Problems: vi.fn<() => Promise<unknown>>(),
  Location: vi.fn<() => Promise<unknown>>(),
  OpenFile: vi.fn<() => Promise<void>>(),
  RevealFile: vi.fn<() => Promise<void>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/settings", () => ({
  SettingsService: service,
}));

import { emit, listenerCount, resetFakeWails } from "../test/fakeWails";
import type { AppError } from "./errors";
import { goSettingsPersistence, openSettingsFile, revealSettingsFile, toPatchDTO, toPersisted, toProblems } from "./settings";

function settingsDTO(ui: Partial<SettingsDTO["ui"]> = {}, defaultTimeoutMs = 0): SettingsDTO {
  return {
    version: 1,
    ui: { theme: "system", customTheme: "", locale: "system", sidebarCollapsed: false, splitRatio: 0.5, responsePosition: "beside", splitRatioBelow: 0.5, ...ui },
    network: { defaultTimeoutMs, verifyTls: true, maxResponseBodyMb: 512 },
    editor: DEFAULT_EDITOR,
    shortcuts: {},
  };
}

const DEFAULT_EDITOR: SettingsDTO["editor"] = {
  fontFamily: "JetBrains Mono",
  fontSize: 12,
  tabSize: 2,
  wordWrap: false,
  lineNumbers: true,
  matchBrackets: true,
};

/** toPersisted of the default editor section. */
const PERSISTED_EDITOR = {
  editorFontFamily: "JetBrains Mono",
  editorFontSize: 12,
  editorTabSize: 2,
  editorWordWrap: false,
  editorLineNumbers: true,
  editorMatchBrackets: true,
};

const invalidTheme = {
  kind: "invalid_argument",
  code: "settings.invalid_value",
  message: 'The setting "ui.theme" has an invalid value, "blue".',
  hint: 'Use one of "system", "dark", "light" in settings.json. PostSonde uses the default, "system", until then.',
  details: { field: "ui.theme", value: '"blue"', expected: 'one of "system", "dark", "light"' },
  causes: [],
};

function changed(source: string, ui: Partial<SettingsDTO["ui"]> = {}, problems: unknown[] = []): ChangedEventDTO {
  return { settings: settingsDTO(ui), problems: problems as ChangedEventDTO["problems"], source };
}

beforeEach(() => {
  resetFakeWails();
  service.Get.mockReset().mockResolvedValue(settingsDTO());
  service.Update.mockReset().mockResolvedValue(settingsDTO());
  service.Problems.mockReset().mockResolvedValue([]);
  service.OpenFile.mockReset().mockResolvedValue(undefined);
  service.RevealFile.mockReset().mockResolvedValue(undefined);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("mapping", () => {
  it("flattens the sections into the store's keys", () => {
    expect(toPersisted(settingsDTO({ theme: "dark", sidebarCollapsed: true, splitRatio: 0.3, responsePosition: "below" }, 30000))).toEqual({
      theme: "dark",
      customTheme: "",
      locale: "system",
      sidebarCollapsed: true,
      splitRatio: 0.3,
      responsePosition: "below",
      splitRatioBelow: 0.5,
      defaultTimeoutMs: 30000,
      verifyTls: true,
      maxResponseBodyMb: 512,
      ...PERSISTED_EDITOR,
      shortcuts: {},
    });
    const editor = { ...DEFAULT_EDITOR, fontFamily: "Menlo", tabSize: 4, wordWrap: true };
    expect(toPersisted({ ...settingsDTO(), editor })).toMatchObject({ editorFontFamily: "Menlo", editorTabSize: 4, editorWordWrap: true });
  });

  it("leaves the editor keys out when an older backend sends no editor section", () => {
    const { editor: _, ...old } = settingsDTO();
    const persisted = toPersisted(old as SettingsDTO);
    expect(persisted.editorFontSize).toBeUndefined();
    expect(persisted.theme).toBe("system");
  });

  it("builds a patch with null for untouched fields, dropping unknown keys and wrong types", () => {
    const noUi = { theme: null, customTheme: null, locale: null, sidebarCollapsed: null, splitRatio: null, responsePosition: null, splitRatioBelow: null };
    const noEditor = { fontFamily: null, fontSize: null, tabSize: null, wordWrap: null, lineNumbers: null, matchBrackets: null };
    expect(toPatchDTO({ splitRatio: 0.3 })).toEqual({
      ui: { ...noUi, splitRatio: 0.3 },
      network: null,
      editor: null,
    });
    expect(toPatchDTO({ theme: "light", sidebarCollapsed: false, other: 1 })).toEqual({
      ui: { ...noUi, theme: "light", sidebarCollapsed: false },
      network: null,
      editor: null,
    });
    expect(toPatchDTO({ responsePosition: "below", splitRatioBelow: 0.6 })).toEqual({
      ui: { ...noUi, responsePosition: "below", splitRatioBelow: 0.6 },
      network: null,
      editor: null,
    });
    expect(toPatchDTO({ defaultTimeoutMs: 0 })).toEqual({ ui: null, network: { defaultTimeoutMs: 0, verifyTls: null, maxResponseBodyMb: null }, editor: null });
    expect(toPatchDTO({ verifyTls: false })).toEqual({ ui: null, network: { defaultTimeoutMs: null, verifyTls: false, maxResponseBodyMb: null }, editor: null });
    expect(toPatchDTO({ maxResponseBodyMb: 1024 })).toEqual({ ui: null, network: { defaultTimeoutMs: null, verifyTls: null, maxResponseBodyMb: 1024 }, editor: null });

    expect(toPatchDTO({ editorFontFamily: "Menlo", editorTabSize: 4, editorLineNumbers: false })).toEqual({
      ui: null,
      network: null,
      editor: { ...noEditor, fontFamily: "Menlo", tabSize: 4, lineNumbers: false },
    });
    expect(toPatchDTO({ editorFontSize: "14", editorWordWrap: "yes" })).toBeNull();
    expect(toPatchDTO({ verifyTls: "no" })).toBeNull();
    expect(toPatchDTO({ defaultTimeoutMs: "30" })).toBeNull();
    expect(toPatchDTO({ theme: 5, splitRatio: "wide", other: true })).toBeNull();
    expect(toPatchDTO({})).toBeNull();
  });

  it("maps the custom shortcuts (UI-07) both ways, replacing them as a whole", () => {
    const shortcuts = { "request.send": ["Mod+Shift+Enter"] };
    expect(toPersisted({ ...settingsDTO(), shortcuts }).shortcuts).toEqual(shortcuts);
    expect(toPersisted({ ...settingsDTO(), shortcuts: null }).shortcuts).toEqual({});
    const { shortcuts: _, ...old } = settingsDTO();
    expect(toPersisted(old as SettingsDTO).shortcuts).toBeUndefined();

    expect(toPatchDTO({ shortcuts })).toEqual({ ui: null, network: null, editor: null, shortcuts });
    expect(toPatchDTO({ shortcuts: {} })).toEqual({ ui: null, network: null, editor: null, shortcuts: {} });
    expect(toPatchDTO({ shortcuts: ["Mod+K"] })).toBeNull();
    expect(toPatchDTO({ theme: "dark" })).not.toHaveProperty("shortcuts");
  });

  it("fills in missing error fields", () => {
    const [p] = toProblems([{ ...invalidTheme, details: null, causes: null }]);
    expect(p).toMatchObject({ kind: "invalid_argument", code: "settings.invalid_value", details: {}, causes: [] });
    expect(toProblems(null)).toEqual([]);
  });
});

describe("goSettingsPersistence", () => {
  it("loads the settings from Go and reports the file's problems", async () => {
    service.Get.mockResolvedValue(settingsDTO({ theme: "light", splitRatio: 0.35 }));
    service.Problems.mockResolvedValue([invalidTheme]);
    const onProblems = vi.fn<(problems: readonly AppError[]) => void>();

    const saved = await goSettingsPersistence({ onProblems }).load();
    expect(saved).toEqual({
      theme: "light",
      customTheme: "",
      locale: "system",
      sidebarCollapsed: false,
      splitRatio: 0.35,
      responsePosition: "beside",
      splitRatioBelow: 0.5,
      defaultTimeoutMs: 0,
      verifyTls: true,
      maxResponseBodyMb: 512,
      ...PERSISTED_EDITOR,
      shortcuts: {},
    });
    expect(onProblems).toHaveBeenCalledTimes(1);
    expect(onProblems.mock.calls[0]?.[0]).toEqual([invalidTheme]);
  });

  it("warns about problems on the console by default", async () => {
    service.Problems.mockResolvedValue([invalidTheme]);
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    await goSettingsPersistence().load();
    expect(warn).toHaveBeenCalledTimes(1);
    expect(String(warn.mock.calls[0]?.[0])).toContain('The setting "ui.theme" has an invalid value');
  });

  it("never rejects on load: Go failing gives no settings and a warning", async () => {
    service.Get.mockRejectedValue(Object.assign(new Error("raw"), { cause: { kind: "io", message: "Disk trouble." } }));
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    await expect(goSettingsPersistence().load()).resolves.toEqual({});
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("saves a patch through Update, and nothing when no known key is left", async () => {
    const persistence = goSettingsPersistence();
    await persistence.save({ splitRatio: 0.3, other: true });
    expect(service.Update).toHaveBeenCalledWith({
      ui: { theme: null, customTheme: null, locale: null, sidebarCollapsed: null, splitRatio: 0.3, responsePosition: null, splitRatioBelow: null },
      network: null,
      editor: null,
    });
    await persistence.save({ other: true });
    expect(service.Update).toHaveBeenCalledTimes(1);
  });

  it("rejects a failed save with the AppError from Go", async () => {
    const dto = { ...invalidTheme, details: { field: "ui.splitRatio" } };
    service.Update.mockRejectedValue(Object.assign(new Error("go: raw text"), { cause: dto }));
    await expect(goSettingsPersistence().save({ splitRatio: 9 })).rejects.toMatchObject({
      kind: "invalid_argument",
      code: "settings.invalid_value",
      details: { field: "ui.splitRatio" },
    });
  });

  it("applies changes made to the file, but not the echo of its own saves", () => {
    const onChange = vi.fn<(settings: Readonly<Record<string, unknown>>) => void>();
    const persistence = goSettingsPersistence({ onProblems: () => {} });
    const unsubscribe = persistence.subscribe?.(onChange);
    expect(unsubscribe).toBeTypeOf("function");

    emit("settings:changed", changed("file", { theme: "dark", splitRatio: 0.25 }));
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith({
      theme: "dark",
      customTheme: "",
      locale: "system",
      sidebarCollapsed: false,
      splitRatio: 0.25,
      responsePosition: "beside",
      splitRatioBelow: 0.5,
      defaultTimeoutMs: 0,
      verifyTls: true,
      maxResponseBodyMb: 512,
      ...PERSISTED_EDITOR,
      shortcuts: {},
    });

    emit("settings:changed", changed("app", { splitRatio: 0.7 }));
    expect(onChange).toHaveBeenCalledTimes(1);

    unsubscribe?.();
    expect(listenerCount()).toBe(0);
    emit("settings:changed", changed("file", { theme: "light" }));
    expect(onChange).toHaveBeenCalledTimes(1);
  });

  it("reports problems from events only when they change", async () => {
    const onProblems = vi.fn<(problems: readonly AppError[]) => void>();
    service.Problems.mockResolvedValue([invalidTheme]);
    const persistence = goSettingsPersistence({ onProblems });
    await persistence.load();
    const unsubscribe = persistence.subscribe?.(() => {});

    emit("settings:changed", changed("app", {}, [invalidTheme])); // same as at load
    expect(onProblems).toHaveBeenCalledTimes(1);

    emit("settings:changed", changed("file", {}, [])); // fixed by hand: reported as none
    expect(onProblems).toHaveBeenCalledTimes(2);
    expect(onProblems.mock.calls[1]?.[0]).toEqual([]);

    emit("settings:changed", changed("file", {}, [invalidTheme])); // broken again
    expect(onProblems).toHaveBeenCalledTimes(3);
    unsubscribe?.();
  });

  it("reports nothing for a file without problems", async () => {
    const onProblems = vi.fn<(problems: readonly AppError[]) => void>();
    await goSettingsPersistence({ onProblems }).load();
    expect(onProblems).not.toHaveBeenCalled();
  });
});

describe("the settings file", () => {
  it("opens and reveals it through Go", async () => {
    await openSettingsFile();
    await revealSettingsFile();
    expect(service.OpenFile).toHaveBeenCalledTimes(1);
    expect(service.RevealFile).toHaveBeenCalledTimes(1);
  });

  it("rejects with the AppError from Go", async () => {
    const dto = { kind: "unsupported", code: "settings.open_failed", message: "The settings file cannot be opened here.", causes: [] };
    service.OpenFile.mockRejectedValue(Object.assign(new Error("raw"), { cause: dto }));
    await expect(openSettingsFile()).rejects.toMatchObject({ kind: "unsupported", code: "settings.open_failed" });
  });
});
