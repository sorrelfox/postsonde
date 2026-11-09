import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  debounceSaves,
  localStorageSettings,
  UI_SETTINGS_STORAGE_KEY,
  withLocalStorageMigration,
  type PersistedSettings,
  type SettingsPersistence,
} from "./persist";

describe("withLocalStorageMigration", () => {
  // A backend that keeps what it is given, like the settings file.
  const fileBackend = (initial: PersistedSettings = {}) => {
    let saved: PersistedSettings = initial;
    const save = vi.fn<SettingsPersistence["save"]>((patch) => {
      saved = { ...saved, ...patch };
      return Promise.resolve();
    });
    const persistence: SettingsPersistence = { load: () => Promise.resolve(saved), save };
    return { persistence, save };
  };

  it("moves the localStorage settings into the backend once, then removes them", async () => {
    localStorage.setItem(UI_SETTINGS_STORAGE_KEY, JSON.stringify({ splitRatio: 0.3 }));
    const { persistence, save } = fileBackend({ theme: "dark" });
    const migrating = withLocalStorageMigration(persistence);

    expect(await migrating.load()).toEqual({ theme: "dark", splitRatio: 0.3 });
    expect(save).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem(UI_SETTINGS_STORAGE_KEY)).toBeNull();

    await migrating.load();
    await withLocalStorageMigration(persistence).load();
    expect(save).toHaveBeenCalledTimes(1);
  });

  it("keeps the localStorage settings when the backend cannot save them", async () => {
    localStorage.setItem(UI_SETTINGS_STORAGE_KEY, JSON.stringify({ splitRatio: 0.3 }));
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const failing: SettingsPersistence = { load: () => Promise.resolve({}), save: () => Promise.reject(new Error("go")) };
    expect(await withLocalStorageMigration(failing).load()).toEqual({});
    expect(warn).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem(UI_SETTINGS_STORAGE_KEY)).not.toBeNull();
  });

  it("removes a corrupt key and passes save and subscribe through", async () => {
    localStorage.setItem(UI_SETTINGS_STORAGE_KEY, "{nope");
    const { persistence, save } = fileBackend();
    const unsubscribe = vi.fn();
    const subscribe = vi.fn<NonNullable<SettingsPersistence["subscribe"]>>(() => unsubscribe);
    const migrating = withLocalStorageMigration({ ...persistence, subscribe });

    await migrating.load();
    expect(save).not.toHaveBeenCalled();
    expect(localStorage.getItem(UI_SETTINGS_STORAGE_KEY)).toBeNull();

    await migrating.save({ theme: "light" });
    expect(save).toHaveBeenCalledWith({ theme: "light" });
    const onChange = () => {};
    expect(migrating.subscribe?.(onChange)).toBe(unsubscribe);
    expect(subscribe).toHaveBeenCalledWith(onChange);
    expect(withLocalStorageMigration(persistence).subscribe).toBeUndefined();
  });
});

beforeEach(() => localStorage.clear());
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("localStorageSettings", () => {
  it("round-trips saved settings to a new instance, merging patches", async () => {
    const first = localStorageSettings();
    await first.save({ splitRatio: 0.3 });
    await first.save({ other: true });
    expect(await localStorageSettings().load()).toEqual({ splitRatio: 0.3, other: true });
    expect(JSON.parse(localStorage.getItem(UI_SETTINGS_STORAGE_KEY) ?? "")).toEqual({ splitRatio: 0.3, other: true });
  });

  it("loads nothing when storage is empty, corrupt or not an object", async () => {
    expect(await localStorageSettings().load()).toEqual({});
    for (const raw of ["{nope", "[1,2]", "42", "null"]) {
      localStorage.setItem(UI_SETTINGS_STORAGE_KEY, raw);
      expect(await localStorageSettings().load()).toEqual({});
    }
  });

  it("never throws when storage is unavailable", async () => {
    const broken = localStorageSettings("k", () => {
      throw new Error("denied");
    });
    expect(await broken.load()).toEqual({});
    await expect(broken.save({ splitRatio: 0.4 })).resolves.toBeUndefined();

    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("quota");
    });
    await expect(localStorageSettings().save({ splitRatio: 0.4 })).resolves.toBeUndefined();
  });
});

describe("debounceSaves", () => {
  const fake = () => {
    const save = vi.fn<SettingsPersistence["save"]>(() => Promise.resolve());
    return { persistence: { load: () => Promise.resolve({}), save }, save };
  };

  it("writes one merged patch after the last change", () => {
    vi.useFakeTimers();
    const { persistence, save } = fake();
    const saver = debounceSaves(persistence, 300);
    saver.save({ splitRatio: 0.3 });
    vi.advanceTimersByTime(200);
    saver.save({ splitRatio: 0.4, theme: "dark" });
    vi.advanceTimersByTime(299);
    expect(save).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(save).toHaveBeenCalledTimes(1);
    expect(save).toHaveBeenCalledWith({ splitRatio: 0.4, theme: "dark" });
  });

  it("flushes now and cancels", async () => {
    vi.useFakeTimers();
    const { persistence, save } = fake();
    const saver = debounceSaves(persistence, 300);
    await saver.flush();
    expect(save).not.toHaveBeenCalled();

    saver.save({ splitRatio: 0.3 });
    await saver.flush();
    expect(save).toHaveBeenCalledWith({ splitRatio: 0.3 });
    vi.advanceTimersByTime(1000);
    expect(save).toHaveBeenCalledTimes(1);

    saver.save({ splitRatio: 0.6 });
    saver.cancel();
    vi.advanceTimersByTime(1000);
    expect(save).toHaveBeenCalledTimes(1);
  });

  it("logs a failed save instead of rejecting", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const saver = debounceSaves({ load: () => Promise.resolve({}), save: () => Promise.reject(new Error("disk")) }, 10);
    saver.save({ splitRatio: 0.3 });
    await expect(saver.flush()).resolves.toBeUndefined();
    expect(warn).toHaveBeenCalledTimes(1);
  });
});
