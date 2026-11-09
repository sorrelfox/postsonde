// Bytes typed as hex, for validating a composer while typing (TCP-01). The
// rules are those of internal/core/realtime/hexbytes, which checks again
// when sending; keep the two in step. See docs/tcp.md.

import { t } from "../i18n";

export type HexParseResult =
  | { ok: true; bytes: Uint8Array }
  | {
      ok: false;
      /** "hexbytes.invalid_char", "hexbytes.odd_digits" or "hexbytes.missing_digits", as Go reports them. */
      code: string;
      /** Position of the offending character, counted in characters (code points) from 0. */
      offset: number;
      message: string;
    };

const SEPARATORS = new Set([" ", "\t", "\n", "\r", "\f", "\v", ",", ":", ";"]);

function hexValue(c: string): number {
  if (c.length !== 1) return -1;
  const code = c.charCodeAt(0);
  if (code >= 48 && code <= 57) return code - 48; // 0-9
  if (code >= 97 && code <= 102) return code - 87; // a-f
  if (code >= 65 && code <= 70) return code - 55; // A-F
  return -1;
}

function describeChar(c: string): string {
  const cp = c.codePointAt(0) ?? 0;
  // Control and other invisible characters are shown as U+XXXX, as Go does.
  if (/[\p{L}\p{M}\p{N}\p{P}\p{S}]/u.test(c) || c === " ") return t("lib.bytes.char", { char: c });
  return `U+${cp.toString(16).toUpperCase().padStart(4, "0")}`;
}

/**
 * Parses hex bytes: pairs of digits in any case, groups separated by
 * whitespace, commas, colons or semicolons, an optional 0x at the start of
 * a group and \x before any pair ("DE AD be ef", "0xDE,0xAD", "deadbeef",
 * "\xde\xad"). An odd number of digits in a group is an error.
 */
export function parseHex(text: string): HexParseResult {
  const chars = Array.from(text); // code points, so offsets match Go's rune counts
  const out: number[] = [];
  let inGroup = false;
  let half = -1; // value of a pending digit
  let halfAt = 0;
  let prefixAt = -1;

  const oddDigits = (at: number): HexParseResult => ({
    ok: false,
    code: "hexbytes.odd_digits",
    offset: at,
    message: t("lib.hex.oddDigits", { position: at + 1 }),
  });
  const missingDigits = (at: number): HexParseResult => ({
    ok: false,
    code: "hexbytes.missing_digits",
    offset: at,
    message: t("lib.hex.missingDigits", { position: at + 1 }),
  });

  for (let i = 0; i < chars.length; ) {
    const c = chars[i] ?? "";
    const next = chars[i + 1];
    if (SEPARATORS.has(c)) {
      if (half >= 0) return oddDigits(halfAt);
      if (prefixAt >= 0) return missingDigits(prefixAt);
      inGroup = false;
      i++;
      continue;
    }
    if (half < 0 && !inGroup && c === "0" && (next === "x" || next === "X")) {
      inGroup = true;
      prefixAt = i;
      i += 2;
      continue;
    }
    if (half < 0 && c === "\\" && next === "x") {
      if (prefixAt >= 0) return missingDigits(prefixAt);
      inGroup = true;
      prefixAt = i;
      i += 2;
      continue;
    }
    const v = hexValue(c);
    if (v < 0) {
      return {
        ok: false,
        code: "hexbytes.invalid_char",
        offset: i,
        message: t("lib.hex.invalidChar", { char: describeChar(c), position: i + 1 }),
      };
    }
    if (half >= 0) {
      out.push((half << 4) | v);
      half = -1;
    } else {
      half = v;
      halfAt = i;
    }
    inGroup = true;
    prefixAt = -1;
    i++;
  }
  if (half >= 0) return oddDigits(halfAt);
  if (prefixAt >= 0) return missingDigits(prefixAt);
  return { ok: true, bytes: Uint8Array.from(out) };
}
