// Pure formatting for the message log: previews, sizes, times, hex and JSON.
import { formatDate, formatNumber, t, type MessageKey } from "../../i18n";
import type { LogMessage, LogMessageInput } from "../../lib/messageSource";

/** Characters of a text preview. */
export const PREVIEW_CHARS = 240;
/** Bytes of a binary preview. */
export const PREVIEW_BYTES = 48;
/** Bytes of a binary payload that search looks at (as hex). */
export const SEARCH_BYTES = 1024;
/** Bytes shown in the detail hex dump. */
export const HEXDUMP_BYTES = 64 * 1024;
/** Bytes shown as Base64 in the detail view (a multiple of 3, so the text shown is the head of the whole). */
export const BASE64_BYTES = 96 * 1024;
/** Text up to this length is pretty-printed as JSON in the detail view. */
export const PRETTY_JSON_CHARS = 2 * 1024 * 1024;

const HEX: string[] = Array.from({ length: 256 }, (_, i) => i.toString(16).padStart(2, "0"));

export function isBinary(m: Pick<LogMessageInput, "data">): boolean {
  return m.data !== undefined;
}

/** Space-separated hex of the first `max` bytes. */
export function hexString(data: Uint8Array, max = data.length): string {
  const n = Math.min(max, data.length);
  const parts = new Array<string>(n);
  for (let i = 0; i < n; i++) parts[i] = HEX[data[i]];
  return parts.join(" ");
}

/** How binary payloads are shown (WS-06): hex bytes (a hexdump in the detail view) or Base64. */
export type BinaryView = "hex" | "base64";

/** Single-line preview: text with whitespace collapsed, or hex (or Base64) for binary. */
export function messagePreview(m: LogMessageInput, binaryView: BinaryView = "hex"): string {
  if (m.data !== undefined) {
    // PREVIEW_BYTES is a multiple of 3, so the Base64 of the head is the head of the Base64.
    const head = binaryView === "base64" ? base64(m.data.subarray(0, PREVIEW_BYTES)) : hexString(m.data, PREVIEW_BYTES);
    return m.data.length > PREVIEW_BYTES ? `${head} …` : head;
  }
  const text = m.text ?? "";
  const head = text.length > PREVIEW_CHARS * 2 ? text.slice(0, PREVIEW_CHARS * 2) : text;
  const line = head.replace(/\s+/g, " ").trim();
  return line.length > PREVIEW_CHARS || text.length > head.length ? `${line.slice(0, PREVIEW_CHARS)}…` : line;
}

const searchCache = new WeakMap<LogMessage, string>();

/**
 * The payload text search matches against: the text, or the hex and the
 * Base64 of the first SEARCH_BYTES bytes (one line each), so search finds
 * what either binary view shows.
 */
export function searchableText(m: LogMessage): string {
  if (m.data === undefined) return m.text ?? "";
  let s = searchCache.get(m);
  if (s === undefined) {
    s = `${hexString(m.data, SEARCH_BYTES)}\n${base64Head(m.data, SEARCH_BYTES - (SEARCH_BYTES % 3))}`;
    searchCache.set(m, s);
  }
  return s;
}

/** UTF-8 byte length of a string, without encoding it. */
export function utf8Length(s: string): number {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x80) n += 1;
    else if (c < 0x800) n += 2;
    else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
      const d = s.charCodeAt(i + 1);
      if (d >= 0xdc00 && d <= 0xdfff) {
        n += 4;
        i++;
      } else n += 3;
    } else n += 3;
  }
  return n;
}

/** The message's size in bytes, or null when it has no payload. */
export function messageSize(m: LogMessageInput): number | null {
  if (m.size !== undefined) return m.size;
  if (m.data !== undefined) return m.data.length;
  if (m.text !== undefined) return utf8Length(m.text);
  return null;
}

const ONE_DECIMAL: Intl.NumberFormatOptions = { minimumFractionDigits: 1, maximumFractionDigits: 1, useGrouping: false };

/** A size in the language in use: "512 B", "1.5 KB", "3.2 MB"; "" for null. */
export function formatSize(bytes: number | null): string {
  if (bytes === null) return "";
  if (bytes < 1024) return t("messagelog.size.bytes", { size: bytes });
  if (bytes < 1024 * 1024) return t("messagelog.size.kb", { size: formatNumber(bytes / 1024, ONE_DECIMAL) });
  return t("messagelog.size.mb", { size: formatNumber(bytes / (1024 * 1024), ONE_DECIMAL) });
}

/** An exact byte count in the language in use, digits grouped: "1 byte", "1,024 bytes". */
export function formatByteCount(n: number): string {
  return t("messagelog.byteCount", { count: n, n: formatNumber(n) });
}

const pad = (n: number, width = 2) => String(n).padStart(width, "0");

// fractionalSecondDigits is ES2021 (every web view has it); the ES2020 lib types lack it.
const CLOCK = { hour: "2-digit", minute: "2-digit", second: "2-digit", fractionalSecondDigits: 3, hourCycle: "h23" } as Intl.DateTimeFormatOptions;
const DATE_TIME: Intl.DateTimeFormatOptions = { year: "numeric", month: "2-digit", day: "2-digit", ...CLOCK };

/** Local wall-clock time with milliseconds, in the language in use: 14:03:27.042. */
export function formatClock(time: number): string {
  return formatDate(time, CLOCK);
}

/** Time since `start`: +0.042, +12.500, +3:07.250, +1:02:03.000. */
export function formatRelative(time: number, start: number | null): string {
  const ms = Math.max(0, Math.round(time - (start ?? time)));
  const frac = pad(ms % 1000, 3);
  const totalSec = Math.floor(ms / 1000);
  const sec = totalSec % 60;
  const min = Math.floor(totalSec / 60) % 60;
  const hours = Math.floor(totalSec / 3600);
  if (hours > 0) return `+${hours}:${pad(min)}:${pad(sec)}.${frac}`;
  if (min > 0) return `+${min}:${pad(sec)}.${frac}`;
  return `+${sec}.${frac}`;
}

/** Full local date and time in the language in use, for tooltips and the detail view. */
export function formatTimestamp(time: number): string {
  return formatDate(time, DATE_TIME);
}

/** Local date and time of a copied or saved entry, the same in every language (sorts, compares): 2026-09-19 10:00:00.100. */
export function entryTimestamp(time: number): string {
  const d = new Date(time);
  const clock = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${clock}`;
}

/** The text pretty-printed when it is a JSON object or array, else null. */
export function prettyJson(text: string): string | null {
  if (text.length > PRETTY_JSON_CHARS) return null;
  const t = text.trimStart();
  if (t[0] !== "{" && t[0] !== "[") return null;
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return null;
  }
}

/** Classic hex dump: offset, 16 hex bytes, printable ASCII. At most `max` bytes. */
export function hexDump(data: Uint8Array, max = HEXDUMP_BYTES): string {
  const n = Math.min(max, data.length);
  const lines: string[] = [];
  for (let off = 0; off < n; off += 16) {
    const end = Math.min(off + 16, n);
    let hex = "";
    let ascii = "";
    for (let i = off; i < off + 16; i++) {
      if (i < end) {
        hex += HEX[data[i]] + (i === off + 7 ? "  " : " ");
        ascii += data[i] >= 0x20 && data[i] < 0x7f ? String.fromCharCode(data[i]) : ".";
      } else {
        hex += i === off + 7 ? "    " : "   ";
      }
    }
    lines.push(`${off.toString(16).padStart(8, "0")}  ${hex} |${ascii}|`);
  }
  return lines.join("\n");
}

export function base64(data: Uint8Array): string {
  let s = "";
  for (let i = 0; i < data.length; i += 0x8000) {
    s += String.fromCharCode(...data.subarray(i, i + 0x8000));
  }
  return btoa(s);
}

/** The Base64 of the first `max` bytes. */
export function base64Head(data: Uint8Array, max = BASE64_BYTES): string {
  return base64(data.length > max ? data.subarray(0, max) : data);
}

/** The message key of a direction's label: t(DIRECTION_KEYS[direction]). */
export const DIRECTION_KEYS = {
  sent: "messagelog.direction.sent",
  received: "messagelog.direction.received",
  system: "messagelog.direction.system",
} as const satisfies Record<LogMessage["direction"], MessageKey>;

/**
 * One message in full, for copying or saving (DBG-07), its words in the
 * language in use (docs/frontend.md, Copying and saving logs): full time
 * (and the time since the first message), direction, kind and badge, size,
 * the frame details, then the whole payload: the text as it came, or the
 * Base64 of every byte of a binary one.
 */
export function messageEntryText(m: LogMessage, startTime: number | null = null): string {
  const size = messageSize(m);
  const head = [
    `${entryTimestamp(m.time)} (${formatRelative(m.time, startTime)})`,
    t(DIRECTION_KEYS[m.direction]),
    m.badge !== undefined ? `${m.kind} [${m.badge}]` : m.kind,
  ];
  if (size !== null) head.push(t("messagelog.byteCount", { count: size, n: size }));
  const lines = [head.join("  ")];
  for (const [k, v] of Object.entries(m.meta ?? {})) lines.push(`  ${k}: ${String(v)}`);
  if (m.data !== undefined) lines.push(t("messagelog.entry.binaryPayload"), base64(m.data));
  else if (m.text !== undefined && m.text !== "") lines.push(m.text);
  return lines.join("\n");
}

/** One message as a JSON value: a binary payload as Base64, times in ISO 8601 and epoch ms. */
export function messageEntryJson(m: LogMessage): Record<string, unknown> {
  const out: Record<string, unknown> = {
    seq: m.seq,
    time: new Date(m.time).toISOString(),
    timeMs: m.time,
    direction: m.direction,
    kind: m.kind,
  };
  if (m.badge !== undefined) out.badge = m.badge;
  const size = messageSize(m);
  if (size !== null) out.size = size;
  if (m.meta !== undefined && Object.keys(m.meta).length > 0) out.meta = m.meta;
  if (m.data !== undefined) out.base64 = base64(m.data);
  else if (m.text !== undefined) out.text = m.text;
  return out;
}

export type TextPart = { text: string; match: boolean };

/** Splits `text` into matching and non-matching parts for a global, case-insensitive pattern. */
export function highlightParts(text: string, pattern: RegExp | null): TextPart[] {
  if (pattern === null || text === "") return [{ text, match: false }];
  const parts: TextPart[] = [];
  const re = new RegExp(pattern.source, pattern.flags.includes("g") ? pattern.flags : `${pattern.flags}g`);
  let last = 0;
  let guard = 0;
  for (let m = re.exec(text); m !== null && guard < 1000; m = re.exec(text), guard++) {
    if (m[0].length === 0) {
      re.lastIndex++;
      continue;
    }
    if (m.index > last) parts.push({ text: text.slice(last, m.index), match: false });
    parts.push({ text: m[0], match: true });
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push({ text: text.slice(last), match: false });
  return parts.length > 0 ? parts : [{ text, match: false }];
}
