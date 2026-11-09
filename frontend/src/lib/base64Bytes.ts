// Bytes typed as Base64, for validating a composer while typing (WS-04).
// The rules are those of parseBase64 in internal/app/services/websocket,
// which checks again when sending; keep the two in step. See
// docs/websocket.md.
import { t } from "../i18n";
import type { HexParseResult } from "./hexBytes";

/** Same shape as a hex parse; the error code is "ws.invalid_base64". */
export type Base64ParseResult = HexParseResult;

const WHITESPACE = new Set([" ", "\t", "\n", "\r", "\f", "\v"]);

function sextet(c: string): number {
  const code = c.length === 1 ? c.charCodeAt(0) : -1;
  if (code >= 65 && code <= 90) return code - 65; // A-Z
  if (code >= 97 && code <= 122) return code - 71; // a-z
  if (code >= 48 && code <= 57) return code + 4; // 0-9
  if (c === "+" || c === "-") return 62;
  if (c === "/" || c === "_") return 63;
  return -1;
}

function describeChar(c: string): string {
  const cp = c.codePointAt(0) ?? 0;
  if (/[\p{L}\p{M}\p{N}\p{P}\p{S}]/u.test(c) || c === " ") return t("lib.bytes.char", { char: c });
  return `U+${cp.toString(16).toUpperCase().padStart(4, "0")}`;
}

/**
 * Parses Base64 as people paste it: the standard or the URL alphabet (even
 * mixed), whitespace anywhere, "=" padding optional but only at the end.
 * Offsets count characters (code points) from 0, as Go's do.
 */
export function parseBase64(text: string): Base64ParseResult {
  const chars = Array.from(text);
  const values: number[] = [];
  let pad = 0;
  let padAt = -1;
  let last = -1;
  const fail = (offset: number, message: string): Base64ParseResult => ({ ok: false, code: "ws.invalid_base64", offset, message });

  for (let i = 0; i < chars.length; i++) {
    const c = chars[i] ?? "";
    if (WHITESPACE.has(c)) continue;
    if (c === "=") {
      if (pad === 0) padAt = i;
      pad++;
      if (pad > 2) return fail(i, t("lib.base64.tooMuchPadding", { position: i + 1 }));
      last = i;
      continue;
    }
    if (pad > 0) return fail(padAt, t("lib.base64.paddingInside", { position: padAt + 1 }));
    const v = sextet(c);
    if (v < 0) return fail(i, t("lib.base64.invalidChar", { char: describeChar(c), position: i + 1 }));
    values.push(v);
    last = i;
  }
  if (values.length % 4 === 1) {
    return fail(last, t("lib.base64.partialByte", { position: last + 1 }));
  }
  if (pad > 0 && (values.length + pad) % 4 !== 0) {
    return fail(padAt, t("lib.base64.paddingLength", { position: padAt + 1 }));
  }
  const out = new Uint8Array(Math.floor((values.length * 3) / 4));
  let o = 0;
  for (let i = 0; i < values.length; i += 4) {
    const a = values[i] ?? 0;
    const b = values[i + 1] ?? 0;
    const c = values[i + 2] ?? 0;
    const d = values[i + 3] ?? 0;
    const n = (a << 18) | (b << 12) | (c << 6) | d;
    if (o < out.length) out[o++] = (n >> 16) & 0xff;
    if (o < out.length && i + 2 < values.length) out[o++] = (n >> 8) & 0xff;
    if (o < out.length && i + 3 < values.length) out[o++] = n & 0xff;
  }
  return { ok: true, bytes: out };
}
