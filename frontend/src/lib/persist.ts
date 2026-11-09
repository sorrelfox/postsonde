// Where UI settings are saved between sessions. Framework-free: no
// React, no stores. commands/settings.ts is the only caller.
//
// The app saves them in the Go settings file: goSettingsPersistence in
// ./settings implements SettingsPersistence over SettingsService, and
// main.tsx installs it. localStorageSettings is the backend until then and
// in tests; withLocalStorageMigration moves its settings into the file once.

/** Saved settings as read back: untrusted, so the reducer validates every value. */
export type PersistedSettings = Readonly<Record<string, unknown>>;

export interface SettingsPersistence {
  /** Returns the saved settings, or `{}` when there are none or they can't be read. Never rejects. */
  load(): Promise<PersistedSettings>;
  /** Merges `patch` into the saved settings. */
  save(patch: PersistedSettings): Promise<void>;
  /**
   * Optional: calls `onChange` with the saved settings whenever they change
   * outside this window (a hand edit of the settings file). Returns the
   * unsubscribe function.
   */
  subscribe?(onChange: (settings: PersistedSettings) => void): () => void;
}

export const UI_SETTINGS_STORAGE_KEY = "postsonde.ui";

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Settings kept as one JSON object in localStorage. Every access is wrapped
 * in try/catch: storage can be disabled, full or hold corrupt JSON, and none
 * of that may break the app. Writes are synchronous, so a save issued while
 * the page unloads still lands.
 */
export function localStorageSettings(
  key: string = UI_SETTINGS_STORAGE_KEY,
  storage: () => Storage = () => window.localStorage,
): SettingsPersistence {
  const read = (): Record<string, unknown> => {
    try {
      const raw = storage().getItem(key);
      if (raw === null) return {};
      const parsed: unknown = JSON.parse(raw);
      return isRecord(parsed) ? parsed : {};
    } catch {
      return {};
    }
  };

  return {
    load: () => Promise.resolve(read()),
    save(patch) {
      try {
        storage().setItem(key, JSON.stringify({ ...read(), ...patch }));
      } catch {
        // Storage unavailable or full: this save is lost, the app goes on.
      }
      return Promise.resolve();
    },
  };
}

/**
 * Moves the settings saved in localStorage (where they lived before the Go
 * settings file) into `target` once: the first `load()` saves them to
 * `target` and removes the key, then loads from `target`. If that save
 * fails, the key is kept and the move is tried again at the next start.
 */
export function withLocalStorageMigration(
  target: SettingsPersistence,
  key: string = UI_SETTINGS_STORAGE_KEY,
  storage: () => Storage = () => window.localStorage,
): SettingsPersistence {
  let pending = true;
  const removeKey = () => {
    try {
      storage().removeItem(key);
    } catch {
      // Storage unavailable: nothing to remove.
    }
  };
  const migrate = async () => {
    pending = false;
    const legacy = await localStorageSettings(key, storage).load();
    if (Object.keys(legacy).length > 0) {
      try {
        await target.save(legacy);
      } catch (err: unknown) {
        console.warn("settings: could not move the settings saved in localStorage to the settings file", err);
        return;
      }
    }
    removeKey();
  };

  const wrapped: SettingsPersistence = {
    async load() {
      if (pending) await migrate();
      return target.load();
    },
    save: (patch) => target.save(patch),
  };
  if (target.subscribe) {
    wrapped.subscribe = target.subscribe.bind(target);
  }
  return wrapped;
}

export interface DebouncedSaver {
  /** Queues `patch` (merged with earlier queued patches) and saves it after the delay. */
  save(patch: PersistedSettings): void;
  /** Saves the queued patch now. */
  flush(): Promise<void>;
  /** Drops the queued patch. */
  cancel(): void;
}

/**
 * Coalesces bursts of saves (key repeat, several quick drags) into one
 * write, `delayMs` after the last one.
 */
export function debounceSaves(persistence: SettingsPersistence, delayMs: number): DebouncedSaver {
  let pending: Record<string, unknown> | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;

  const clearTimer = () => {
    if (timer !== null) clearTimeout(timer);
    timer = null;
  };

  const flush = (): Promise<void> => {
    clearTimer();
    if (pending === null) return Promise.resolve();
    const patch = pending;
    pending = null;
    return persistence.save(patch).catch((err: unknown) => {
      // Until the error model can show it, a failed save only logs.
      console.warn("settings: save failed", err);
    });
  };

  return {
    save(patch) {
      pending = { ...pending, ...patch };
      clearTimer();
      timer = setTimeout(() => void flush(), delayMs);
    },
    flush,
    cancel() {
      clearTimer();
      pending = null;
    },
  };
}
