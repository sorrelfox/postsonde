// Custom themes (UI-05): the theme files Go found in the themes folder, kept
// here (not in a store: read-only status of files outside the project, like
// the settings file's problems), reloaded at startup and by the settings
// screen's Reload, and which one applies for ui.customTheme. See
// docs/themes.md.

import * as api from "../lib/themes";
import { toAppError, type AppError } from "../lib/errors";
import { applyCustomTheme, appliedCustomThemeId } from "../styles/customTheme";

export type CustomTheme = api.ThemeDTO;

export interface ThemesState {
  /** "idle" before the first load. */
  readonly status: "idle" | "loading" | "ready" | "error";
  /** The themes folder; "" when unknown. */
  readonly dir: string;
  readonly themes: readonly CustomTheme[];
  readonly error: AppError | null;
}

const INITIAL: ThemesState = { status: "idle", dir: "", themes: [], error: null };

let state: ThemesState = INITIAL;
const listeners = new Set<() => void>();

function set(next: ThemesState): void {
  state = next;
  for (const listener of [...listeners]) listener();
}

/** The themes as last loaded. */
export function getThemesState(): ThemesState {
  return state;
}

/** Calls `listener` whenever the themes change. Returns the unsubscribe function. */
export function subscribeThemes(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

let generation = 0;

/** Reads the themes folder again. Never rejects: a failure is kept in the state. */
export async function reloadThemes(): Promise<void> {
  const mine = ++generation;
  set({ ...state, status: "loading" });
  try {
    const dto = await api.listThemes();
    if (mine !== generation) return;
    set({ status: "ready", dir: dto.dir, themes: dto.themes ?? [], error: null });
  } catch (err: unknown) {
    if (mine !== generation) return;
    const appErr = toAppError(err);
    console.warn("themes: could not list the themes", appErr);
    set({ ...state, status: "error", error: appErr });
  }
}

/** Opens the themes folder in the file manager. Rejects with an AppError. */
export async function openThemesFolder(): Promise<void> {
  try {
    await api.openThemesFolder();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/** The theme file `id` among the loaded ones, or undefined. */
export function findTheme(themes: readonly CustomTheme[], id: string): CustomTheme | undefined {
  return themes.find((t) => t.id === id);
}

/**
 * Applies the custom theme `id` ("" for none) from the loaded themes. While
 * they are still loading the theme applied now is kept (the first-paint
 * cache), so a restart does not flash the built-in colours. A theme that is
 * missing or has errors is not applied: the built-in theme shows and the
 * settings screen says why.
 */
export function syncCustomTheme(id: string, themes: ThemesState = state): void {
  if (id === "") {
    if (appliedCustomThemeId() !== null) applyCustomTheme(null);
    return;
  }
  if (themes.status === "idle" || themes.status === "loading") return;
  const theme = findTheme(themes.themes, id);
  applyCustomTheme(theme !== undefined && theme.usable ? theme : null);
}

/** Test helper: back to the state before the first load. */
export function resetThemesForTests(): void {
  generation++;
  state = INITIAL;
  listeners.clear();
}
