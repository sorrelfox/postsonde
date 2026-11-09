// Quantities with a unit, as the UI writes them ("250 ms", "1.24 s",
// "2 min 5 s", "3.2 KB"), in the language in use (UI-10): the unit comes
// from the catalogue (lib.units.*) and the number is written with the
// locale's digits and decimal sign, without grouping.
import { formatNumber, t } from "../i18n";

export type Unit = "ms" | "s" | "min" | "h" | "B" | "KB" | "MB" | "GB";

const UNITS = {
  ms: "lib.units.ms",
  s: "lib.units.s",
  min: "lib.units.min",
  h: "lib.units.h",
  B: "lib.units.B",
  KB: "lib.units.KB",
  MB: "lib.units.MB",
  GB: "lib.units.GB",
} as const;

/**
 * `value` with its unit: "250 ms". With `fractionDigits`, exactly that
 * many decimals ("1.20 s"); without, as many as the number has.
 */
export function formatUnit(unit: Unit, value: number, fractionDigits?: number): string {
  const shown =
    fractionDigits === undefined
      ? value
      : formatNumber(value, { minimumFractionDigits: fractionDigits, maximumFractionDigits: fractionDigits, useGrouping: false });
  return t(UNITS[unit], { value: shown });
}

/** Minutes and seconds: "2 min 5 s". */
export function formatMinSec(min: number, sec: number): string {
  return t("lib.units.minSec", { min, sec });
}
