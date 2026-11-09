// Settings store: UI preferences. commands/settings.ts loads them with
// `settings/load` at startup and after hand edits of the settings file, and
// saves changes to it (docs/settings.md). This reducer does no I/O.

export type Theme = "dark" | "light" | "system";

export const THEMES: readonly Theme[] = ["dark", "light", "system"];

/** Bounds for the request/response split, as a fraction of the width. */
export const MIN_SPLIT_RATIO = 0.2;
export const MAX_SPLIT_RATIO = 0.8;

/**
 * Bounds for the default request timeout in milliseconds (HTTP-21), as the
 * settings file checks them. 0, outside the range, means no timeout.
 */
export const MIN_DEFAULT_TIMEOUT_MS = 1000;
export const MAX_DEFAULT_TIMEOUT_MS = 24 * 60 * 60 * 1000;

/** Whether `ms` is a default timeout the settings file accepts: 0 (none) or a whole number in the bounds. */
export function isValidDefaultTimeout(ms: unknown): ms is number {
  return (
    typeof ms === "number" &&
    Number.isInteger(ms) &&
    (ms === 0 || (ms >= MIN_DEFAULT_TIMEOUT_MS && ms <= MAX_DEFAULT_TIMEOUT_MS))
  );
}

/**
 * Bounds and default of the largest response body kept, in MiB (HTTP-37,
 * network.maxResponseBodyMb), as the settings file checks them.
 */
export const MIN_MAX_RESPONSE_BODY_MB = 1;
export const MAX_MAX_RESPONSE_BODY_MB = 16384;
export const DEFAULT_MAX_RESPONSE_BODY_MB = 512;

/** Whether `mb` is a response body limit the settings file accepts. */
export function isValidMaxResponseBody(mb: unknown): mb is number {
  return typeof mb === "number" && Number.isInteger(mb) && mb >= MIN_MAX_RESPONSE_BODY_MB && mb <= MAX_MAX_RESPONSE_BODY_MB;
}

/** Where the response pane sits (UI-02): right of the request, or under it. */
export type ResponsePosition = "beside" | "below";

export const RESPONSE_POSITIONS: readonly ResponsePosition[] = ["beside", "below"];

/** Editor settings (UI-08), with the bounds the settings file checks. */
export const DEFAULT_EDITOR_FONT_FAMILY = "JetBrains Mono"; // i18n-ignore: a font name
export const MIN_EDITOR_FONT_SIZE = 8;
export const MAX_EDITOR_FONT_SIZE = 32;
export const MIN_EDITOR_TAB_SIZE = 1;
export const MAX_EDITOR_TAB_SIZE = 8;
export const MAX_EDITOR_FONT_FAMILY_LENGTH = 64;

/**
 * Characters a font name may not contain: those that would end or escape the
 * CSS value, and anything Go's unicode.IsPrint rejects (all but letters,
 * marks, numbers, punctuation, symbols and the ASCII space).
 */
const FONT_FAMILY_FORBIDDEN = /["'\\,;{}<>]|[^\p{L}\p{M}\p{N}\p{P}\p{S} ]/u;

/** Whether `name` is a font name the settings file accepts: one font, trimmed, at most 64 characters. */
export function isValidFontFamily(name: unknown): name is string {
  return (
    typeof name === "string" &&
    name !== "" &&
    name.trim() === name &&
    [...name].length <= MAX_EDITOR_FONT_FAMILY_LENGTH &&
    !FONT_FAMILY_FORBIDDEN.test(name)
  );
}

function isWholeIn(value: unknown, lo: number, hi: number): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= lo && value <= hi;
}

/** Whether `px` is an editor font size the settings file accepts. */
export function isValidFontSize(px: unknown): px is number {
  return isWholeIn(px, MIN_EDITOR_FONT_SIZE, MAX_EDITOR_FONT_SIZE);
}

/** Whether `n` is a tab size the settings file accepts. */
export function isValidTabSize(n: unknown): n is number {
  return isWholeIn(n, MIN_EDITOR_TAB_SIZE, MAX_EDITOR_TAB_SIZE);
}

/** Custom keyboard shortcuts (UI-07): chords by shortcut id, as the settings file keeps them. */
export type ShortcutOverrides = Readonly<Record<string, readonly string[]>>;

/** Bounds of the "shortcuts" section, as the settings file checks them. */
export const MAX_SHORTCUT_OVERRIDES = 200;
export const MAX_SHORTCUT_CHORDS = 4;

const SHORTCUT_ID = /^[a-z][A-Za-z0-9]*(\.[a-z][A-Za-z0-9]*)+$/;
const SHORTCUT_CHORD = /^((Mod|Ctrl|Alt|Shift)\+)*([!-*,-~]|[A-Z][A-Za-z0-9]{1,19})$/;

/**
 * Whether `value` has the shape of the "shortcuts" section: at most 200 ids,
 * each with 1 to 4 different chords. Chords are checked only for their
 * shape here; the registry (lib/shortcuts) ignores ones it cannot use.
 */
export function isValidShortcutOverrides(value: unknown): value is ShortcutOverrides {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const entries = Object.entries(value);
  if (entries.length > MAX_SHORTCUT_OVERRIDES) return false;
  return entries.every(
    ([id, keys]) =>
      id.length <= 64 &&
      SHORTCUT_ID.test(id) &&
      Array.isArray(keys) &&
      keys.length >= 1 &&
      keys.length <= MAX_SHORTCUT_CHORDS &&
      keys.every((k, i) => typeof k === "string" && SHORTCUT_CHORD.test(k) && keys.indexOf(k) === i),
  );
}

function sameShortcutOverrides(a: ShortcutOverrides, b: ShortcutOverrides): boolean {
  const ids = Object.keys(a);
  if (ids.length !== Object.keys(b).length) return false;
  return ids.every((id) => {
    const x = a[id];
    const y = b[id];
    return y !== undefined && x.length === y.length && x.every((k, i) => k === y[i]);
  });
}

/** The longest custom theme file name the settings file accepts, in bytes (as in Go). */
export const MAX_CUSTOM_THEME_ID_LENGTH = 128;

/**
 * Whether `id` is a custom theme the settings file accepts (UI-05,
 * ui.customTheme): "" for none, or a .json file name in the themes folder,
 * without a directory, not starting with "." or "_" (themes.ValidID in Go).
 */
export function isValidCustomThemeId(id: unknown): id is string {
  if (typeof id !== "string") return false;
  if (id === "") return true;
  return (
    new TextEncoder().encode(id).length <= MAX_CUSTOM_THEME_ID_LENGTH &&
    id.endsWith(".json") &&
    id !== ".json" &&
    !id.startsWith(".") &&
    !id.startsWith("_") &&
    id.trim() === id &&
    !/[/\\:*?"<>|]|[^\p{L}\p{M}\p{N}\p{P}\p{S} ]/u.test(id)
  );
}

/** The locale setting that follows the OS language (UI-10). */
export const LOCALE_SYSTEM = "system";

/** The longest language tag the settings file accepts, in bytes (as in Go). */
export const MAX_LOCALE_LENGTH = 35;

const LANGUAGE_TAG = /^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*$/;

/**
 * Whether `locale` is a UI language the settings file accepts (ui.locale):
 * "system", or a language tag such as "en" or "en-US" (validLocale in Go).
 * Whether the app has messages for it is decided when it is applied.
 */
export function isValidLocale(locale: unknown): locale is string {
  return (
    typeof locale === "string" &&
    (locale === LOCALE_SYSTEM || (locale.length <= MAX_LOCALE_LENGTH && LANGUAGE_TAG.test(locale)))
  );
}

export interface SettingsState {
  readonly theme: Theme;
  /** The custom theme's file name in the themes folder (UI-05); "" for none. */
  readonly customTheme: string;
  /** The UI language (UI-10): "system" or a language tag. */
  readonly locale: string;
  readonly sidebarCollapsed: boolean;
  /** Share of the editor area taken by the request pane (0.2–0.8). */
  readonly splitRatio: number;
  /** Timeout of a request without its own, in ms; 0 for none. */
  readonly defaultTimeoutMs: number;
  /**
   * Whether TLS certificates are verified for requests without their own
   * verifyTls (HTTP-23). On by default; off is marked on every such request.
   */
  readonly verifyTls: boolean;
  /** The largest response body kept, in MiB (HTTP-37); a longer one is cut there. */
  readonly maxResponseBodyMb: number;
  /** Where the response pane sits (UI-02). */
  readonly responsePosition: ResponsePosition;
  /** Share of the height taken by the request pane while the response is below it (0.2–0.8). */
  readonly splitRatioBelow: number;
  /** Editor font: the name of one font, or "monospace" / "ui-monospace" (UI-08). */
  readonly editorFontFamily: string;
  /** Editor font size in px. */
  readonly editorFontSize: number;
  /** Tab width and indent unit, in spaces. */
  readonly editorTabSize: number;
  readonly editorWordWrap: boolean;
  readonly editorLineNumbers: boolean;
  readonly editorMatchBrackets: boolean;
  /** Custom keyboard shortcuts (UI-07); `{}` when every shortcut has its default chords. */
  readonly shortcuts: ShortcutOverrides;
}

/** The editor settings, as the editors take them (components/editor/preferences.ts). */
export type EditorSettings = Pick<
  SettingsState,
  | "editorFontFamily"
  | "editorFontSize"
  | "editorTabSize"
  | "editorWordWrap"
  | "editorLineNumbers"
  | "editorMatchBrackets"
>;

export const initialSettingsState: SettingsState = {
  theme: "system",
  customTheme: "",
  locale: LOCALE_SYSTEM,
  sidebarCollapsed: false,
  splitRatio: 0.5,
  defaultTimeoutMs: 0,
  verifyTls: true,
  maxResponseBodyMb: DEFAULT_MAX_RESPONSE_BODY_MB,
  responsePosition: "beside",
  splitRatioBelow: 0.5,
  editorFontFamily: DEFAULT_EDITOR_FONT_FAMILY,
  editorFontSize: 12,
  editorTabSize: 2,
  editorWordWrap: false,
  editorLineNumbers: true,
  editorMatchBrackets: true,
  shortcuts: {},
};

/** The editor settings of `state`. */
export function editorSettingsOf(state: SettingsState): EditorSettings {
  return {
    editorFontFamily: state.editorFontFamily,
    editorFontSize: state.editorFontSize,
    editorTabSize: state.editorTabSize,
    editorWordWrap: state.editorWordWrap,
    editorLineNumbers: state.editorLineNumbers,
    editorMatchBrackets: state.editorMatchBrackets,
  };
}

export type SettingsAction =
  /** Applies stored settings. Unknown or invalid values keep the current value. */
  | { type: "settings/load"; settings: Partial<Record<keyof SettingsState, unknown>> }
  | { type: "settings/setTheme"; theme: Theme }
  /** Sets the custom theme ("" for none); an invalid file name changes nothing. */
  | { type: "settings/setCustomTheme"; id: string }
  | { type: "settings/setSidebarCollapsed"; collapsed: boolean }
  | { type: "settings/toggleSidebar" }
  /** Sets the split of the current layout: `splitRatio` beside, `splitRatioBelow` below. */
  | { type: "settings/setSplitRatio"; ratio: number; position?: ResponsePosition };

function clampSplitRatio(ratio: number, fallback: number): number {
  if (!Number.isFinite(ratio)) return fallback;
  return Math.max(MIN_SPLIT_RATIO, Math.min(MAX_SPLIT_RATIO, ratio));
}

function isTheme(value: unknown): value is Theme {
  return typeof value === "string" && (THEMES as readonly string[]).includes(value);
}

function isResponsePosition(value: unknown): value is ResponsePosition {
  return typeof value === "string" && (RESPONSE_POSITIONS as readonly string[]).includes(value);
}

const bool = (value: unknown, fallback: boolean) => (typeof value === "boolean" ? value : fallback);
const ratio = (value: unknown, fallback: number) =>
  typeof value === "number" ? clampSplitRatio(value, fallback) : fallback;

export function settingsReducer(state: SettingsState, action: SettingsAction): SettingsState {
  switch (action.type) {
    case "settings/load": {
      const s = action.settings;
      const next: SettingsState = {
        theme: isTheme(s.theme) ? s.theme : state.theme,
        customTheme: isValidCustomThemeId(s.customTheme) ? s.customTheme : state.customTheme,
        locale: isValidLocale(s.locale) ? s.locale : state.locale,
        sidebarCollapsed: bool(s.sidebarCollapsed, state.sidebarCollapsed),
        splitRatio: ratio(s.splitRatio, state.splitRatio),
        defaultTimeoutMs: isValidDefaultTimeout(s.defaultTimeoutMs) ? s.defaultTimeoutMs : state.defaultTimeoutMs,
        verifyTls: bool(s.verifyTls, state.verifyTls),
        maxResponseBodyMb: isValidMaxResponseBody(s.maxResponseBodyMb) ? s.maxResponseBodyMb : state.maxResponseBodyMb,

        responsePosition: isResponsePosition(s.responsePosition) ? s.responsePosition : state.responsePosition,
        splitRatioBelow: ratio(s.splitRatioBelow, state.splitRatioBelow),
        editorFontFamily: isValidFontFamily(s.editorFontFamily) ? s.editorFontFamily : state.editorFontFamily,
        editorFontSize: isValidFontSize(s.editorFontSize) ? s.editorFontSize : state.editorFontSize,
        editorTabSize: isValidTabSize(s.editorTabSize) ? s.editorTabSize : state.editorTabSize,
        editorWordWrap: bool(s.editorWordWrap, state.editorWordWrap),
        editorLineNumbers: bool(s.editorLineNumbers, state.editorLineNumbers),
        editorMatchBrackets: bool(s.editorMatchBrackets, state.editorMatchBrackets),
        shortcuts:
          isValidShortcutOverrides(s.shortcuts) && !sameShortcutOverrides(s.shortcuts, state.shortcuts)
            ? s.shortcuts
            : state.shortcuts,
      };
      const unchanged = (Object.keys(next) as (keyof SettingsState)[]).every((k) => next[k] === state[k]);
      return unchanged ? state : next;
    }

    case "settings/setTheme":
      return state.theme === action.theme ? state : { ...state, theme: action.theme };

    case "settings/setCustomTheme":
      return state.customTheme === action.id || !isValidCustomThemeId(action.id)
        ? state
        : { ...state, customTheme: action.id };

    case "settings/setSidebarCollapsed":
      return state.sidebarCollapsed === action.collapsed ? state : { ...state, sidebarCollapsed: action.collapsed };

    case "settings/toggleSidebar":
      return { ...state, sidebarCollapsed: !state.sidebarCollapsed };

    case "settings/setSplitRatio": {
      const key = (action.position ?? state.responsePosition) === "below" ? "splitRatioBelow" : "splitRatio";
      const value = clampSplitRatio(action.ratio, state[key]);
      return value === state[key] ? state : { ...state, [key]: value };
    }

    default:
      return state;
  }
}
