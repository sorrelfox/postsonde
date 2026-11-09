import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const settingsService = vi.hoisted(() => ({
  Get: vi.fn<() => Promise<unknown>>(),
  Update: vi.fn<(patch: unknown) => Promise<unknown>>(),
  Problems: vi.fn<() => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/settings", () => ({
  SettingsService: settingsService,
}));

import { localStorageSettings, UI_SETTINGS_STORAGE_KEY, type PersistedSettings, type SettingsPersistence } from "../lib/persist";
import { createStores } from "../stores";
import { emit, listenerCount, resetFakeWails } from "../test/fakeWails";
import {
  applySetting,
  commitSplitRatio,
  flushSettings,
  followSettingsChanges,
  getSettingsProblems,
  installGoSettings,
  loadSettings,
  SETTINGS_SAVE_DELAY_MS,
  setSettingsPersistence,
  setSettingsProblems,
  subscribeSettingsProblems,
  trackSettings,
} from "./settings";

beforeEach(() => {
  localStorage.clear();
  setSettingsPersistence(localStorageSettings());
});

/** A fake backend whose `change` simulates a hand edit of the settings file. */
function fakeBackend(saved: PersistedSettings = {}) {
  const bus: { listener: ((settings: PersistedSettings) => void) | null } = { listener: null };
  const save = vi.fn<SettingsPersistence["save"]>(() => Promise.resolve());
  const persistence: SettingsPersistence = {
    load: () => Promise.resolve(saved),
    save,
    subscribe(onChange) {
      bus.listener = onChange;
      return () => {
        bus.listener = null;
      };
    },
  };
  return { persistence, save, bus, change: (settings: PersistedSettings) => bus.listener?.(settings) };
}

describe("tracking and following changes", () => {
  it("saves theme and sidebar changes, debounced, but never settings it applied", async () => {
    vi.useFakeTimers();
    const backend = fakeBackend({ theme: "dark" });
    setSettingsPersistence(backend.persistence);
    const stores = createStores();
    const stopTracking = trackSettings(stores);
    const stopFollowing = followSettingsChanges(stores);

    await loadSettings(stores);
    backend.change({ splitRatio: 0.3, sidebarCollapsed: true });
    expect(stores.settings.getState()).toMatchObject({ theme: "dark", splitRatio: 0.3, sidebarCollapsed: true });
    vi.advanceTimersByTime(SETTINGS_SAVE_DELAY_MS);
    expect(backend.save).not.toHaveBeenCalled();

    stores.settings.dispatch({ type: "settings/setTheme", theme: "light" });
    stores.settings.dispatch({ type: "settings/toggleSidebar" });
    vi.advanceTimersByTime(SETTINGS_SAVE_DELAY_MS);
    expect(backend.save).toHaveBeenCalledTimes(1);
    expect(backend.save).toHaveBeenCalledWith({ theme: "light", sidebarCollapsed: false });

    stopTracking();
    stopFollowing();
    expect(backend.bus.listener).toBeNull();
    stores.settings.dispatch({ type: "settings/setTheme", theme: "dark" });
    vi.advanceTimersByTime(SETTINGS_SAVE_DELAY_MS);
    expect(backend.save).toHaveBeenCalledTimes(1);
  });

  it("drops invalid values from the file and keeps the current ones", () => {
    const backend = fakeBackend();
    setSettingsPersistence(backend.persistence);
    const stores = createStores();
    const stop = followSettingsChanges(stores);
    backend.change({ theme: "neon", splitRatio: 0.95 });
    expect(stores.settings.getState()).toMatchObject({ theme: "system", splitRatio: 0.8 });
    stop();
  });

  it("follows nothing when the backend cannot report changes", () => {
    const stores = createStores();
    const stop = followSettingsChanges(stores);
    expect(stop).toBeTypeOf("function");
    stop();
  });
});

/** A UI patch that changes nothing. */
const NO_UI = { theme: null, customTheme: null, locale: null, sidebarCollapsed: null, splitRatio: null, responsePosition: null, splitRatioBelow: null };

describe("the Go settings file", () => {
  const settingsDTO = (ui: Record<string, unknown>, defaultTimeoutMs = 0) => ({
    version: 1,
    ui: { theme: "system", sidebarCollapsed: false, splitRatio: 0.5, ...ui },
    network: { defaultTimeoutMs, verifyTls: true },
  });

  beforeEach(() => {
    resetFakeWails();
    settingsService.Get.mockReset().mockResolvedValue(settingsDTO({ theme: "light", splitRatio: 0.4 }));
    settingsService.Update.mockReset().mockResolvedValue(settingsDTO({}));
    settingsService.Problems.mockReset().mockResolvedValue([]);
  });

  it("loads from Go, saves through Update and applies hand edits live", async () => {
    installGoSettings();
    const stores = createStores();
    const stopTracking = trackSettings(stores);
    const stopFollowing = followSettingsChanges(stores);

    await loadSettings(stores);
    expect(stores.settings.getState()).toMatchObject({ theme: "light", splitRatio: 0.4 });

    stores.settings.dispatch({ type: "settings/setTheme", theme: "dark" });
    await flushSettings();
    expect(settingsService.Update).toHaveBeenCalledWith({
      ui: { ...NO_UI, theme: "dark" },
      network: null,
      editor: null,
    });

    // A hand edit of settings.json, then the echo of a save from this window.
    emit("settings:changed", { settings: settingsDTO({ theme: "system", splitRatio: 0.25 }), problems: [], source: "file" });
    expect(stores.settings.getState()).toMatchObject({ theme: "system", splitRatio: 0.25 });
    emit("settings:changed", { settings: settingsDTO({ splitRatio: 0.6 }), problems: [], source: "app" });
    expect(stores.settings.getState().splitRatio).toBe(0.25);
    await flushSettings();
    expect(settingsService.Update).toHaveBeenCalledTimes(1);

    stopTracking();
    stopFollowing();
    expect(listenerCount()).toBe(0);
  });

  it("moves settings saved in localStorage into the file once", async () => {
    localStorage.setItem(UI_SETTINGS_STORAGE_KEY, JSON.stringify({ splitRatio: 0.3 }));
    installGoSettings();
    await loadSettings(createStores());
    expect(settingsService.Update).toHaveBeenCalledWith({
      ui: { ...NO_UI, splitRatio: 0.3 },
      network: null,
      editor: null,
    });
    expect(localStorage.getItem(UI_SETTINGS_STORAGE_KEY)).toBeNull();
  });

  it("saves a change from the settings screen at once, and only then applies it", async () => {
    installGoSettings();
    const stores = createStores();
    const stopTracking = trackSettings(stores);

    await applySetting(stores, { defaultTimeoutMs: 30000 });
    expect(settingsService.Update).toHaveBeenCalledWith({ ui: null, network: { defaultTimeoutMs: 30000, verifyTls: null, maxResponseBodyMb: null }, editor: null });
    expect(stores.settings.getState().defaultTimeoutMs).toBe(30000);
    await flushSettings();
    expect(settingsService.Update).toHaveBeenCalledTimes(1); // not saved again by trackSettings
    stopTracking();
  });

  it("changes nothing when Go rejects the value", async () => {
    installGoSettings();
    const stores = createStores();
    const cause = {
      kind: "invalid_argument",
      code: "settings.invalid_value",
      message: 'The setting "network.defaultTimeoutMs" has an invalid value, -5000.',
      details: { field: "network.defaultTimeoutMs" },
      causes: [],
    };
    settingsService.Update.mockRejectedValue(Object.assign(new Error("raw"), { cause }));
    await expect(applySetting(stores, { defaultTimeoutMs: -5000 })).rejects.toMatchObject({
      code: "settings.invalid_value",
    });
    expect(stores.settings.getState().defaultTimeoutMs).toBe(0);
  });

  it("keeps the file's problems for the settings screen", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const problem = {
      kind: "invalid_argument",
      code: "settings.invalid_value",
      message: 'The setting "ui.theme" has an invalid value, "blue".',
      details: { field: "ui.theme" },
      causes: [],
    };
    settingsService.Problems.mockResolvedValue([problem]);
    const listener = vi.fn();
    const unsubscribe = subscribeSettingsProblems(listener);
    installGoSettings();
    const stores = createStores();
    const stopFollowing = followSettingsChanges(stores);

    await loadSettings(stores);
    expect(getSettingsProblems()).toMatchObject([{ code: "settings.invalid_value" }]);
    expect(listener).toHaveBeenCalledTimes(1);
    expect(warn).toHaveBeenCalledTimes(1);

    // Fixed by hand.
    emit("settings:changed", { settings: settingsDTO({}), problems: [], source: "file" });
    expect(getSettingsProblems()).toEqual([]);
    expect(listener).toHaveBeenCalledTimes(2);

    stopFollowing();
    unsubscribe();
    setSettingsProblems([]);
    warn.mockRestore();
  });
});

afterEach(async () => {
  await flushSettings();
  vi.useRealTimers();
});

describe("settings persistence", () => {
  it("round-trips the split ratio: save, then a new store loads it", async () => {
    const before = createStores();
    commitSplitRatio(before, 0.3);
    expect(before.settings.getState().splitRatio).toBe(0.3);
    await flushSettings();

    const after = createStores();
    await loadSettings(after);
    expect(after.settings.getState().splitRatio).toBe(0.3);
  });

  it("saves the clamped value, debounced, once per burst of commits", () => {
    vi.useFakeTimers();
    const save = vi.fn<SettingsPersistence["save"]>(() => Promise.resolve());
    setSettingsPersistence({ load: () => Promise.resolve({}), save });
    const stores = createStores();

    commitSplitRatio(stores, 0.4);
    commitSplitRatio(stores, 0.42);
    commitSplitRatio(stores, 0.95);
    expect(save).not.toHaveBeenCalled();
    vi.advanceTimersByTime(SETTINGS_SAVE_DELAY_MS);
    expect(save).toHaveBeenCalledTimes(1);
    expect(save).toHaveBeenCalledWith({ splitRatio: 0.8 });
  });

  it("saves the split of the layout in effect: splitRatioBelow while the response is below", () => {
    vi.useFakeTimers();
    const save = vi.fn<SettingsPersistence["save"]>(() => Promise.resolve());
    setSettingsPersistence({ load: () => Promise.resolve({}), save });
    const stores = createStores();
    stores.settings.dispatch({ type: "settings/load", settings: { responsePosition: "below" } });

    commitSplitRatio(stores, 0.35);
    vi.advanceTimersByTime(SETTINGS_SAVE_DELAY_MS);
    expect(save).toHaveBeenCalledWith({ splitRatioBelow: 0.35 });
    expect(stores.settings.getState()).toMatchObject({ splitRatio: 0.5, splitRatioBelow: 0.35 });

    commitSplitRatio(stores, 0.6, "beside");
    vi.advanceTimersByTime(SETTINGS_SAVE_DELAY_MS);
    expect(save).toHaveBeenLastCalledWith({ splitRatio: 0.6 });
  });

  it("keeps the current settings when the saved ones are invalid", async () => {
    setSettingsPersistence({ load: () => Promise.resolve({ splitRatio: "wide" }), save: () => Promise.resolve() });
    const stores = createStores();
    const initial = stores.settings.getState();
    await loadSettings(stores);
    expect(stores.settings.getState()).toBe(initial);
  });

  it("writes pending changes to the old backend before swapping", () => {
    const save = vi.fn<SettingsPersistence["save"]>(() => Promise.resolve());
    setSettingsPersistence({ load: () => Promise.resolve({}), save });
    commitSplitRatio(createStores(), 0.6);
    setSettingsPersistence(localStorageSettings());
    expect(save).toHaveBeenCalledWith({ splitRatio: 0.6 });
  });
});
