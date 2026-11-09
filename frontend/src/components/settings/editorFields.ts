// Values for the settings screen's Editor page (UI-08).

import { t } from "../../i18n";
import { DEFAULT_EDITOR_FONT_FAMILY, MAX_EDITOR_FONT_SIZE, MIN_EDITOR_FONT_SIZE } from "../../stores";

export interface FontOption {
  /** What goes into editor.fontFamily. */
  readonly value: string;
  readonly label: string;
}

/**
 * The fonts offered in the Font menu: the bundled one, the system's
 * monospace font, and common coding fonts that may be installed. A font
 * that is not installed falls back to the bundled one. Any other font can
 * be set by hand in settings.json; the menu then shows it too.
 */
// Font names are not translated.
// i18n-ignore-start
export const FONT_FAMILIES: readonly string[] = [
  DEFAULT_EDITOR_FONT_FAMILY,
  "ui-monospace",
  "SF Mono",
  "Menlo",
  "Monaco",
  "Consolas",
  "Cascadia Code",
  "Fira Code",
  "Source Code Pro",
  "IBM Plex Mono",
  "Ubuntu Mono",
  "DejaVu Sans Mono",
  "Courier New",
];
// i18n-ignore-end

/** A font's name in the menu: its own name, but for the built-in font and the system's. */
function fontLabel(family: string): string {
  if (family === DEFAULT_EDITOR_FONT_FAMILY) return t("settings.font.builtIn", { font: family });
  if (family === "ui-monospace") return t("settings.font.systemMonospace");
  return family;
}

/** The fonts of the menu, labelled in the language in use. */
export function fontOptions(): readonly FontOption[] {
  return FONT_FAMILIES.map((value) => ({ value, label: fontLabel(value) }));
}

/** The menu's options for the font in effect: fontOptions(), plus that font when it is not one of them. */
export function fontOptionsFor(current: string): readonly FontOption[] {
  const options = fontOptions();
  if (FONT_FAMILIES.includes(current)) return options;
  return [...options, { value: current, label: t("settings.font.fromFile", { font: current }) }];
}

/** The tab sizes offered as buttons; the file takes any from 1 to 8. */
export const TAB_SIZE_OPTIONS: readonly number[] = [2, 4, 8];

/** The tab-size buttons for the size in effect: TAB_SIZE_OPTIONS, plus that size in order when it is not one of them. */
export function tabSizeOptionsFor(current: number): readonly number[] {
  if (TAB_SIZE_OPTIONS.includes(current)) return TAB_SIZE_OPTIONS;
  return [...TAB_SIZE_OPTIONS, current].sort((a, b) => a - b);
}

export type ParsedFontSize = { ok: true; px: number } | { ok: false; error: string };

/** Parses the font size field: a whole number of pixels in the bounds, with an optional "px". */
export function parseFontSize(text: string): ParsedFontSize {
  const error = t("settings.fontSize.invalid", { min: MIN_EDITOR_FONT_SIZE, max: MAX_EDITOR_FONT_SIZE });
  const match = /^\s*(\d+)\s*(px)?\s*$/i.exec(text);
  if (!match) return { ok: false, error };
  const px = Number(match[1]);
  if (px < MIN_EDITOR_FONT_SIZE || px > MAX_EDITOR_FONT_SIZE) return { ok: false, error };
  return { ok: true, px };
}
