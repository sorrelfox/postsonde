// Merging the data of a stream's events into one text (SSE-04): token
// streams (LLM completions and the like) send a word or less per event,
// which is unreadable as a list. The merged view joins the pieces: the
// data as it is, or a string field of JSON data. "Auto" knows the common
// token-stream shapes and leaves out JSON that carries no text (a
// "message_start", a usage report) and the "[DONE]" sentinel. See
// docs/sse.md.

/** One event of the stream, as the merged view reads it. */
export interface MergeEvent {
  /** The event type ("message" unless the stream set one). */
  readonly event: string;
  readonly data: string;
}

/** How a piece of text is taken from an event's data. */
export type MergeExtract = "auto" | "data" | "field";

/** What joins consecutive pieces. */
export type MergeJoin = "none" | "newline";

export interface MergeOptions {
  /** Only events of this type; "" for every type. */
  readonly eventType: string;
  readonly extract: MergeExtract;
  /** The JSON field for "field": a dotted path, array indexes as numbers ("choices.0.delta.content", "choices[0].text", "$.delta.text"). */
  readonly path: string;
  readonly join: MergeJoin;
}

export const DEFAULT_MERGE_OPTIONS: MergeOptions = { eventType: "", extract: "auto", path: "", join: "none" };

export interface MergeResult {
  /** The merged text. */
  readonly text: string;
  /** Events that gave a piece of text. */
  readonly pieces: number;
  /** Events of the chosen type that gave none (no such field, not JSON for "field", "[DONE]"). */
  readonly skipped: number;
  /** Events of the chosen type. */
  readonly matched: number;
}

/**
 * The string fields "auto" looks for in JSON data, first match wins:
 * OpenAI chat completions and the Responses API, legacy completions,
 * Anthropic content deltas, Ollama chat and generate, Hugging Face TGI,
 * and plain {"text"}, {"content"}, {"token"}, {"delta"} objects.
 */
export const AUTO_PATHS: readonly string[] = [
  "choices.0.delta.content",
  "choices.0.text",
  "delta.text",
  "delta.partial_json",
  "message.content",
  "response",
  "token.text",
  "text",
  "content",
  "token",
  "delta",
];

/** The sentinel OpenAI-style streams end with; never part of the text. */
export const DONE_SENTINEL = "[DONE]";

/** Splits a path into keys: "a[0].b" and "$.a.0.b" are ["a", "0", "b"]; "" is []. */
export function parsePath(path: string): string[] {
  let p = path.trim();
  if (p.startsWith("$")) p = p.slice(1);
  p = p.replace(/\[(\d+)\]/g, ".$1").replace(/\[["']([^"']*)["']\]/g, ".$1");
  return p.split(".").filter((k) => k !== "");
}

/** The value at `keys` inside `value`, or undefined. */
function lookup(value: unknown, keys: readonly string[]): unknown {
  let v = value;
  for (const k of keys) {
    if (Array.isArray(v)) {
      if (!/^\d+$/.test(k)) return undefined;
      v = v[Number(k)];
    } else if (v !== null && typeof v === "object") {
      if (!Object.prototype.hasOwnProperty.call(v, k)) return undefined;
      v = (v as Record<string, unknown>)[k];
    } else {
      return undefined;
    }
  }
  return v;
}

const JSON_START = /^\s*[[{"]/;

function parseJSON(data: string): { ok: true; value: unknown } | { ok: false } {
  // Only objects, arrays and strings: "12" or "true" as data are text.
  if (!JSON_START.test(data)) return { ok: false };
  try {
    return { ok: true, value: JSON.parse(data) as unknown };
  } catch {
    return { ok: false };
  }
}

/** The piece of text one event gives, or null for none. */
export function extractPiece(data: string, extract: MergeExtract, keys: readonly string[] = []): string | null {
  if (extract === "data") return data;
  if (data.trim() === DONE_SENTINEL) return null;
  const parsed = parseJSON(data);
  if (extract === "field") {
    if (!parsed.ok) return null;
    const v = lookup(parsed.value, keys);
    return typeof v === "string" ? v : typeof v === "number" || typeof v === "boolean" ? String(v) : null;
  }
  // auto
  if (!parsed.ok) return data;
  if (typeof parsed.value === "string") return parsed.value;
  for (const path of AUTO_PATHS) {
    const v = lookup(parsed.value, parsePath(path));
    if (typeof v === "string") return v;
  }
  return null;
}

/** Joins the events' pieces as `options` say. */
export function mergeEvents(events: Iterable<MergeEvent>, options: MergeOptions = DEFAULT_MERGE_OPTIONS): MergeResult {
  const keys = parsePath(options.path);
  const sep = options.join === "newline" ? "\n" : "";
  const parts: string[] = [];
  let matched = 0;
  let skipped = 0;
  for (const ev of events) {
    if (options.eventType !== "" && ev.event !== options.eventType) continue;
    matched++;
    const piece = options.extract === "field" && keys.length === 0 ? null : extractPiece(ev.data, options.extract, keys);
    if (piece === null) skipped++;
    else parts.push(piece);
  }
  return { text: parts.join(sep), pieces: parts.length, skipped, matched };
}
