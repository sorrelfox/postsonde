// Theme selection (UI-04): sets data-theme on <html>, which switches the
// token set in tokens.css. 'system' follows the OS theme live.
//
// The preference itself belongs to the settings store (SET-01,
// settings.theme); this module only applies it. The last preference is also
// cached in localStorage so public/theme-init.js (loaded by index.html) can
// set the right theme before first paint. Keep THEME_STORAGE_KEY in sync with
// that script.
//
// What the OS theme is: the web view's prefers-color-scheme, unless an OS
// theme change reported by Go came later (setSystemDark, fed by
// lib/osTheme.ts). The latest of the two signals wins, because some web views
// (WebKitGTK on GNOME) miss OS theme changes. See docs/frontend.md, "Themes".
//
// A custom theme (UI-05, styles/customTheme.ts) pins the theme to its base
// with setThemeOverride while it is applied; the preference is kept and
// takes over again when the custom theme is removed.

export type ThemePreference = "dark" | "light" | "system";
export type Theme = "dark" | "light";

export const THEME_STORAGE_KEY = "postsonde.theme";
export const DEFAULT_THEME_PREFERENCE: ThemePreference = "system";

const DARK_QUERY = "(prefers-color-scheme: dark)";

let preference: ThemePreference = DEFAULT_THEME_PREFERENCE;
let systemQuery: MediaQueryList | null = null;
/** The OS theme from the latest change signal; null: ask the media query. */
let systemDark: boolean | null = null;
/** The base of the applied custom theme; null: follow the preference. */
let override: Theme | null = null;
const listeners = new Set<() => void>();

export function isThemePreference(value: unknown): value is ThemePreference {
  return value === "dark" || value === "light" || value === "system";
}

function systemTheme(): Theme {
  const dark = systemDark ?? window.matchMedia?.(DARK_QUERY).matches ?? false;
  return dark ? "dark" : "light";
}

/** Resolves a preference to the concrete theme to render. */
export function resolveTheme(pref: ThemePreference): Theme {
  return pref === "system" ? systemTheme() : pref;
}

/** Returns the theme currently applied to <html>. */
export function currentTheme(): Theme {
  return document.documentElement.dataset.theme === "light" ? "light" : "dark";
}

/**
 * Calls `listener` whenever the theme applied to <html> changes, whatever
 * changed it (a new preference, or the OS while 'system' follows it).
 * Returns the unsubscribe function. The CodeMirror editors and the title bar
 * use it.
 */
export function subscribeTheme(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** Reads the cached preference; falls back to the default. */
export function storedThemePreference(): ThemePreference {
  try {
    const v = window.localStorage.getItem(THEME_STORAGE_KEY);
    return isThemePreference(v) ? v : DEFAULT_THEME_PREFERENCE;
  } catch {
    return DEFAULT_THEME_PREFERENCE;
  }
}

function setThemeAttribute(theme: Theme): void {
  const root = document.documentElement;
  if (root.dataset.theme === theme) return;
  root.dataset.theme = theme;
  for (const listener of [...listeners]) listener();
}

/**
 * Records an OS theme change and re-renders if the preference is 'system'.
 * Called by the prefers-color-scheme listener and with what Go reports.
 */
export function setSystemDark(dark: boolean): void {
  systemDark = dark;
  if (preference === "system" && override === null) setThemeAttribute(systemTheme());
}

/**
 * Pins the applied theme to `base` (a custom theme's base), or with null
 * goes back to the preference. Returns the theme rendered right now.
 */
export function setThemeOverride(base: Theme | null): Theme {
  override = base;
  const theme = override ?? resolveTheme(preference);
  setThemeAttribute(theme);
  return theme;
}

// Listens to prefers-color-scheme for the app's whole life, not only while
// the preference is 'system', so switching back to 'system' uses the latest
// OS signal.
function listenToMediaQuery(): void {
  if (systemQuery || !window.matchMedia) return;
  systemQuery = window.matchMedia(DARK_QUERY);
  systemQuery.addEventListener("change", (e) => setSystemDark(e.matches));
}

/**
 * Applies a theme preference to the document and caches it. Calling it again
 * replaces the previous preference. Returns the theme rendered right now.
 */
export function applyTheme(
  pref: ThemePreference = DEFAULT_THEME_PREFERENCE,
): Theme {
  listenToMediaQuery();
  preference = pref;

  const theme = override ?? resolveTheme(pref);
  setThemeAttribute(theme);

  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, pref);
  } catch {
    // Storage unavailable: only the first-paint cache is lost.
  }
  return theme;
}

/** Test helper: forgets the preference, the reported OS theme and the media query. */
export function resetThemeForTests(): void {
  preference = DEFAULT_THEME_PREFERENCE;
  systemDark = null;
  override = null;
  systemQuery = null;
}
