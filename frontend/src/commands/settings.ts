// Settings persistence (SET-01): load the saved UI settings at startup,
// save changes back (debounced), and apply changes made to the settings file
// by hand while the app runs. The backend sits behind SettingsPersistence
// (lib/persist.ts): the Go settings file, installed by installGoSettings()
// in main.tsx; localStorage until then and in tests. The settings screen
// saves through applySetting and shows the file's problems. See
// docs/settings.md.

import {
  debounceSaves,
  localStorageSettings,
  withLocalStorageMigration,
  type DebouncedSaver,
  type PersistedSettings,
  type SettingsPersistence,
} from "../lib/persist";
import { formatError, type AppError } from "../lib/errors";
import {
  getSettingsLocation,
  getSettingsPolicy,
  goSettingsPersistence,
  NO_POLICY,
  openSettingsFile,
  revealSettingsFile,
  settingFileKey,
  warnSettingsProblems,
  type LocationDTO,
  type SettingsPolicy,
} from "../lib/settings";
import type { ResponsePosition, SettingsState, Stores } from "../stores";

export { openSettingsFile, revealSettingsFile };
export type { LocationDTO as SettingsLocation };

/** Quiet time after the last change before settings are written. */
export const SETTINGS_SAVE_DELAY_MS = 400;

let persistence: SettingsPersistence = localStorageSettings();
let saver: DebouncedSaver = debounceSaves(persistence, SETTINGS_SAVE_DELAY_MS);

/**
 * Replaces the settings backend (the Go settings file; tests: a fake).
 * Changes still queued for the old backend are written to it first.
 */
export function setSettingsPersistence(next: SettingsPersistence): void {
  void saver.flush();
  persistence = next;
  saver = debounceSaves(next, SETTINGS_SAVE_DELAY_MS);
}

/**
 * Makes the Go settings file the backend, moving settings saved in
 * localStorage by earlier builds into it on the first load. main.tsx calls
 * it once, before rendering.
 */
export function installGoSettings(): void {
  const onProblems = (next: readonly AppError[]) => {
    warnSettingsProblems(next);
    setSettingsProblems(next);
  };
  setSettingsPersistence(withLocalStorageMigration(goSettingsPersistence({ onProblems })));
  void getSettingsPolicy().then(
    (policy) => {
      for (const p of policy.problems) console.warn(`settings policy: ${formatError(p)}`, p);
      setSettingsPolicy(policy);
    },
    // No Go backend (tests, the browser): no policy. Go enforces it anyway.
    (err: unknown) => console.debug(`settings: could not load the administrator policy: ${formatError(err)}`),
  );
}

// The admin policy (SET-02), as Go read it at startup. It does not change
// while the app runs; NO_POLICY until it has loaded, and without Go.
let policy: SettingsPolicy = NO_POLICY;
const policyListeners = new Set<() => void>();

/** Records the admin policy and notifies subscribers. Exported for tests. */
export function setSettingsPolicy(next: SettingsPolicy): void {
  if (next === policy) return;
  policy = next;
  for (const listener of [...policyListeners]) listener();
}

/** The admin policy in effect; NO_POLICY when there is none. */
export function currentSettingsPolicy(): SettingsPolicy {
  return policy;
}

/** Calls `listener` whenever the policy changes. Returns the unsubscribe function. */
export function subscribeSettingsPolicy(listener: () => void): () => void {
  policyListeners.add(listener);
  return () => policyListeners.delete(listener);
}

/**
 * Whether the admin policy locks a setting, by its settings.json key
 * (`ui.theme`) or store key (`theme`), itself or with the member it is in
 * (`shortcuts` locks `shortcuts.request.send`).
 */
export function isSettingLocked(key: string, p: SettingsPolicy = policy): boolean {
  const fileKey = settingFileKey(key);
  return p.locked.some((k) => k === fileKey || fileKey.startsWith(`${k}.`));
}

/** `patch` without the settings the admin policy locks, and whether any were dropped. */
function withoutLocked(patch: Record<string, unknown>): [Record<string, unknown>, boolean] {
  if (policy.locked.length === 0) return [patch, false];
  const kept = Object.fromEntries(Object.entries(patch).filter(([key]) => !isSettingLocked(key)));
  return [kept, Object.keys(kept).length !== Object.keys(patch).length];
}

// What is wrong with the settings file (invalid values, a broken or newer
// file), as Go last reported it; the settings screen shows it. Kept here
// rather than in a store: it is read-only status of the file, not state
// the UI changes.
let problems: readonly AppError[] = [];
const problemListeners = new Set<() => void>();

/** Records the settings file's problems and notifies subscribers. Exported for tests. */
export function setSettingsProblems(next: readonly AppError[]): void {
  if (next === problems) return;
  problems = next;
  for (const listener of [...problemListeners]) listener();
}

/** The settings file's current problems; `[]` when it is fine. */
export function getSettingsProblems(): readonly AppError[] {
  return problems;
}

/** Calls `listener` whenever the problems change. Returns the unsubscribe function. */
export function subscribeSettingsProblems(listener: () => void): () => void {
  problemListeners.add(listener);
  return () => problemListeners.delete(listener);
}

/**
 * Saves a change made on the settings screen right away and applies it to
 * the store once the file has it. An invalid value rejects with the
 * AppError from Go (code `settings.invalid_value`) and changes nothing, in
 * the file or in the store. Changes still queued are written first, so
 * saves stay in order.
 */
export async function applySetting(stores: Stores, patch: Partial<SettingsState>): Promise<void> {
  await saver.flush();
  await persistence.save(patch);
  applySaved(stores, patch);
}

/** Where the settings file and the data directory are; null without the Go backend (tests, the browser). */
export async function loadSettingsLocation(): Promise<LocationDTO | null> {
  try {
    return await getSettingsLocation();
  } catch {
    return null;
  }
}

// True while saved settings are applied to the store, so trackSettings does
// not save them straight back.
let applying = false;

function applySaved(stores: Stores, settings: PersistedSettings): void {
  applying = true;
  try {
    stores.settings.dispatch({ type: "settings/load", settings });
  } finally {
    applying = false;
  }
}

/** Applies the saved settings to the store. Invalid or unknown values are ignored by `settings/load`. */
export async function loadSettings(stores: Stores): Promise<void> {
  applySaved(stores, await persistence.load());
}

/**
 * Settings that components change by dispatching to the store directly (the
 * title-bar theme toggle, which also turns a custom theme off, ⌘B) and that trackSettings saves. The split ratio
 * is saved by commitSplitRatio instead, once per drag.
 */
const TRACKED_KEYS = ["theme", "customTheme", "sidebarCollapsed"] as const satisfies readonly (keyof SettingsState)[];

/**
 * Saves the tracked settings whenever they change in the store, except
 * while saved settings are being applied. Returns the unsubscribe function.
 */
export function trackSettings(stores: Stores): () => void {
  let previous = stores.settings.getState();
  return stores.settings.subscribe(() => {
    const current = stores.settings.getState();
    const before = previous;
    previous = current;
    if (applying) return;
    const changed: Record<string, unknown> = {};
    for (const key of TRACKED_KEYS) {
      if (current[key] !== before[key]) changed[key] = current[key];
    }
    const [patch, dropped] = withoutLocked(changed);
    if (Object.keys(patch).length > 0) saver.save(patch);
    // A setting the admin policy locks goes back to its locked value.
    if (dropped) void loadSettings(stores);
  });
}

/**
 * Applies settings changed outside the app (a hand edit of the settings
 * file) to the store as they arrive. Returns the unsubscribe function; a
 * backend that cannot report changes makes this a no-op.
 */
export function followSettingsChanges(stores: Stores): () => void {
  return persistence.subscribe?.((settings) => applySaved(stores, settings)) ?? (() => {});
}

/**
 * Sets the request/response split of the current layout (`splitRatio`
 * beside, `splitRatioBelow` below, UI-02) and queues it for saving. Call it
 * when a change is committed (end of a drag, a key press), never per drag
 * frame.
 */
export function commitSplitRatio(stores: Stores, ratio: number, position?: ResponsePosition): void {
  const where = position ?? stores.settings.getState().responsePosition;
  stores.settings.dispatch({ type: "settings/setSplitRatio", ratio, position: where });
  const state = stores.settings.getState();
  const [patch, dropped] = withoutLocked(
    where === "below" ? { splitRatioBelow: state.splitRatioBelow } : { splitRatio: state.splitRatio },
  );
  if (dropped) {
    // Locked by the admin policy: back to the locked value.
    void loadSettings(stores);
    return;
  }
  saver.save(patch);
}

/** Writes queued changes now (the window is closing or hidden). */
export function flushSettings(): Promise<void> {
  return saver.flush();
}
