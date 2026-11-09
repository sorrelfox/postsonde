// The preset palette of environment colours (ENV-08). They are data, not UI
// styling: the chosen value is written to the environment file as "#rrggbb"
// and shared with the project, so it must be a literal colour rather than a
// design token. The digits are kept without the hash here (the hash is added
// by presetColor) so scripts/check-css-tokens.sh keeps flagging literal
// colours used as styling. The values and why they were chosen:
// docs/environments.md, "Colour".

import { t } from "../i18n";

export interface EnvironmentColorPreset {
  /** The colour's name, in the language in use (looked up when read). */
  readonly name: string;
  /** Six lower-case hex digits, without the hash. */
  readonly hex: string;
}

export const ENVIRONMENT_COLOR_PRESETS: readonly EnvironmentColorPreset[] = [
  {
    get name() {
      return t("lib.environmentColors.red");
    },
    hex: "d0342c",
  },
  {
    get name() {
      return t("lib.environmentColors.orange");
    },
    hex: "e07a1f",
  },
  {
    get name() {
      return t("lib.environmentColors.yellow");
    },
    hex: "c9a100",
  },
  {
    get name() {
      return t("lib.environmentColors.green");
    },
    hex: "1f9d55",
  },
  {
    get name() {
      return t("lib.environmentColors.teal");
    },
    hex: "0f9b9b",
  },
  {
    get name() {
      return t("lib.environmentColors.blue");
    },
    hex: "2f6fdf",
  },
  {
    get name() {
      return t("lib.environmentColors.purple");
    },
    hex: "8a4fd8",
  },
  {
    get name() {
      return t("lib.environmentColors.pink");
    },
    hex: "d6409f",
  },
  {
    get name() {
      return t("lib.environmentColors.grey");
    },
    hex: "7b8494",
  },
];

/** A preset's colour as stored: "#" and its six digits. */
export function presetColor(preset: EnvironmentColorPreset): string {
  return `#${preset.hex}`;
}

/** The preset with this colour (any case), or undefined for a custom one. */
export function findPreset(color: string): EnvironmentColorPreset | undefined {
  const c = color.toLowerCase();
  return ENVIRONMENT_COLOR_PRESETS.find((p) => presetColor(p) === c);
}
