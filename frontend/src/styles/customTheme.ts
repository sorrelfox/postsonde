// Custom themes (UI-05): applies a theme file's colours on top of the
// built-in token sets of tokens.css. Go reads and validates the files
// (internal/core/themes) and sends each colour as a known CSS custom
// property with a normalised hex value; this module checks both again and
// sets them with CSSOM style.setProperty on <html>, never by writing CSS
// text, so nothing in a theme file can add rules, load URLs or break out of
// the value (the CSP stays as it is, docs/privacy.md). The theme's base
// ("dark" or "light") picks the token set underneath, through
// setThemeOverride.
//
// The applied theme is also cached in localStorage, like the theme
// preference, so public/theme-init.js can apply it before first paint.
// Keep CUSTOM_THEME_STORAGE_KEY and the checks in sync with that script.
// See docs/themes.md.

import { setThemeOverride, type Theme } from "./theme";

export const CUSTOM_THEME_STORAGE_KEY = "postsonde.customTheme";

/**
 * The CSS custom properties a theme may set: the colour tokens of
 * tokens.css (not --shadow, which is not a colour) and the syntax colours.
 * Mirrors ColorTokens and EditorTokens in internal/core/themes.
 */
export const THEME_TOKENS: ReadonlySet<string> = new Set([
  "--bg0",
  "--bg1",
  "--bg2",
  "--bg3",
  "--line",
  "--line2",
  "--text",
  "--text2",
  "--text3",
  "--accent",
  "--accent-ink",
  "--accent-dim",
  "--m-get",
  "--m-post",
  "--m-put",
  "--m-patch",
  "--m-del",
  "--m-gql",
  "--m-dir",
  "--m-ws",
  "--ok",
  "--warn",
  "--bad",
  "--sel",
  "--scrim",
  "--tk-kw",
  "--tk-field",
  "--tk-arg",
  "--tk-var",
  "--tk-str",
  "--tk-num",
  "--tk-type",
  "--tk-dir",
  "--tk-punct",
  "--tk-cm",
  "--tk-key",
  "--tk-bool",
  "--tk-plain",
  "--tk-err",
]);

const HEX_COLOR = /^#[0-9a-f]{6}([0-9a-f]{2})?$/;

/** Whether `value` is a colour as Go normalises them: lower-case six or eight hex digits after the hash. */
export function isThemeColor(value: unknown): value is string {
  return typeof value === "string" && HEX_COLOR.test(value);
}

/** A custom theme ready to apply. */
export interface AppliedCustomTheme {
  /** The theme file's name, the settings' ui.customTheme. */
  readonly id: string;
  readonly base: Theme;
  /** Property → colour, only known tokens with valid colours. */
  readonly vars: ReadonlyMap<string, string>;
}

/** A theme as Go sends it (ThemeDTO), or as the first-paint cache holds it. */
export interface CustomThemeInput {
  readonly id: string;
  readonly base: string;
  readonly vars: readonly { readonly name: string; readonly value: string }[] | null;
}

/**
 * Checks a theme again before it touches the document: an unknown base
 * gives null; properties that are not known tokens and values that are not
 * normalised colours are left out and listed in `rejected`.
 */
export function sanitizeCustomTheme(input: CustomThemeInput): { theme: AppliedCustomTheme; rejected: string[] } | null {
  if (input.base !== "dark" && input.base !== "light") return null;
  const vars = new Map<string, string>();
  const rejected: string[] = [];
  for (const v of input.vars ?? []) {
    if (THEME_TOKENS.has(v.name) && isThemeColor(v.value)) vars.set(v.name, v.value);
    else rejected.push(v.name);
  }
  return { theme: { id: input.id, base: input.base, vars }, rejected };
}

/** The properties set by the theme applied now. */
let appliedProps: string[] = [];
let appliedId: string | null = null;

/** The id of the applied custom theme, or null. */
export function appliedCustomThemeId(): string | null {
  return appliedId;
}

/**
 * Applies a custom theme (null removes it): removes the properties of the
 * previous one, sets the new ones on <html>, pins the base theme and
 * caches it for first paint. Returns the properties it refused.
 */
export function applyCustomTheme(input: CustomThemeInput | null): string[] {
  const style = document.documentElement.style;
  for (const name of appliedProps) style.removeProperty(name);
  appliedProps = [];
  appliedId = null;

  const checked = input === null ? null : sanitizeCustomTheme(input);
  if (checked === null) {
    setThemeOverride(null);
    writeCache(null);
    // An unknown base refuses the whole theme.
    return input === null ? [] : ["base"];
  }
  const { theme, rejected } = checked;
  for (const [name, value] of theme.vars) {
    style.setProperty(name, value);
    appliedProps.push(name);
  }
  appliedId = theme.id;
  setThemeOverride(theme.base);
  writeCache(theme);
  if (rejected.length > 0) console.warn(`themes: ${theme.id}: ignored ${rejected.join(", ")}`);
  return rejected;
}

function writeCache(theme: AppliedCustomTheme | null): void {
  try {
    if (theme === null) {
      window.localStorage.removeItem(CUSTOM_THEME_STORAGE_KEY);
      return;
    }
    const cached = { id: theme.id, base: theme.base, vars: [...theme.vars].map(([name, value]) => ({ name, value })) };
    window.localStorage.setItem(CUSTOM_THEME_STORAGE_KEY, JSON.stringify(cached));
  } catch {
    // Storage unavailable: only the first-paint cache is lost.
  }
}

/** The custom theme cached for first paint, or null. Untrusted: applyCustomTheme checks it. */
export function storedCustomTheme(): CustomThemeInput | null {
  try {
    const raw = window.localStorage.getItem(CUSTOM_THEME_STORAGE_KEY);
    if (raw === null) return null;
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null) return null;
    const { id, base, vars } = parsed as Record<string, unknown>;
    if (typeof id !== "string" || typeof base !== "string" || !Array.isArray(vars)) return null;
    const list = vars.filter(
      (v): v is { name: string; value: string } =>
        typeof v === "object" && v !== null && typeof v.name === "string" && typeof v.value === "string",
    );
    return { id, base, vars: list };
  } catch {
    return null;
  }
}

/** Test helper: forgets what was applied without touching the document. */
export function resetCustomThemeForTests(): void {
  appliedProps = [];
  appliedId = null;
}
