// Two-way sync between a request's URL and its query-parameter rows
// (HTTP-04, HTTP-06). Pure functions; the request editor calls
// `paramsToUrl` after a table edit and `urlToParams` after a URL edit.
// Rules (docs/frontend.md, Request editor):
//
// - The URL is split outside `{{variables}}`: the fragment starts at the
//   first `#`, the query at the first `?` before it. Query entries are split
//   on `&`, key and value on the first `=`. A `?`, `&`, `=` or `#` inside
//   `{{…}}` never splits.
// - Nothing is decoded or normalized: rows hold the text as typed (`%20`
//   stays `%20`, `+` stays `+`). Only the characters that would break the
//   query are percent-encoded when a row is written into the URL: `&` and
//   `#`, and `=` in a key. A row whose written form equals the URL entry
//   keeps its own text, so a row value `a&b` and the URL's `a%26b` agree.
// - Empty entries (`?`, `&&`, a trailing `&`) make no rows. A key without
//   `=` is a row with an empty value.
// - The URL holds the enabled rows that have a key or a value, in order.
//   Disabled rows and blank rows stay in `params` only.

import type { KeyValueDTO } from "./collection";
import { templateVars } from "./templateSyntax";

/** One `key=value` entry of a query string, as written. */
export interface QueryEntry {
  readonly key: string;
  readonly value: string;
  /** The entry's text in the URL: `key=value`, `key` or `key=`. */
  readonly raw: string;
}

export interface UrlParts {
  /** Everything before the query: scheme, host, path (may contain `{{vars}}`). */
  readonly base: string;
  /** The text after `?`, or null when the URL has no `?`. */
  readonly query: string | null;
  /** `#…` including the `#`, or "". */
  readonly fragment: string;
}

/**
 * Indexes of `chars` in `text` that are outside `{{variables}}`, from
 * `from` up to `to`.
 */
function delimiters(text: string, chars: string, from = 0, to = text.length): number[] {
  const vars = templateVars(text);
  const out: number[] = [];
  let v = 0;
  for (let i = from; i < to; i++) {
    while (v < vars.length && (vars[v]?.end ?? 0) <= i) v++;
    const span = vars[v];
    if (span !== undefined && span.start <= i) {
      i = span.end - 1;
      continue;
    }
    if (chars.includes(text[i] ?? "")) out.push(i);
  }
  return out;
}

/** Splits `url` into base, query and fragment, outside `{{variables}}`. */
export function splitUrl(url: string): UrlParts {
  let hash = -1;
  let question = -1;
  for (const i of delimiters(url, "?#")) {
    if (url[i] === "#") {
      hash = i;
      break;
    }
    if (question < 0) question = i;
  }
  const end = hash < 0 ? url.length : hash;
  const fragment = hash < 0 ? "" : url.slice(hash);
  if (question < 0) return { base: url.slice(0, end), query: null, fragment };
  return { base: url.slice(0, question), query: url.slice(question + 1, end), fragment };
}

/** The entries of a query string (the text after `?`), empty ones left out. */
export function parseQuery(query: string): QueryEntry[] {
  const entries: QueryEntry[] = [];
  let start = 0;
  const cuts = [...delimiters(query, "&"), query.length];
  for (const cut of cuts) {
    const raw = query.slice(start, cut);
    start = cut + 1;
    if (raw === "") continue;
    const eq = delimiters(raw, "=")[0];
    entries.push(
      eq === undefined
        ? { key: raw, value: "", raw }
        : { key: raw.slice(0, eq), value: raw.slice(eq + 1), raw },
    );
  }
  return entries;
}

/** The query entries of `url`. */
export function parseUrlQuery(url: string): QueryEntry[] {
  const { query } = splitUrl(url);
  return query === null ? [] : parseQuery(query);
}

const ENCODED: Readonly<Record<string, string>> = { "&": "%26", "#": "%23", "=": "%3D" };

function encode(text: string, chars: string): string {
  const at = delimiters(text, chars);
  if (at.length === 0) return text;
  let out = "";
  let last = 0;
  for (const i of at) {
    out += text.slice(last, i) + (ENCODED[text[i] ?? ""] ?? "");
    last = i + 1;
  }
  return out + text.slice(last);
}

/** A row's key as written into the query: `&`, `#` and `=` outside `{{vars}}` encoded. */
export function encodeQueryKey(key: string): string {
  return encode(key, "&#=");
}

/** A row's value as written into the query: `&` and `#` outside `{{vars}}` encoded. */
export function encodeQueryValue(value: string): string {
  return encode(value, "&#");
}

/** Whether a row is written into the URL: enabled, with a key or a value. */
export function isInUrl(row: KeyValueDTO): boolean {
  return row.enabled && (row.name !== "" || row.value !== "");
}

function keyMatches(row: KeyValueDTO, entry: QueryEntry): boolean {
  return encodeQueryKey(row.name) === entry.key;
}

function valueMatches(row: KeyValueDTO, entry: QueryEntry): boolean {
  return encodeQueryValue(row.value) === entry.value;
}

function rowMatches(row: KeyValueDTO, entry: QueryEntry): boolean {
  return keyMatches(row, entry) && valueMatches(row, entry);
}

function formatEntry(row: KeyValueDTO): string {
  const key = encodeQueryKey(row.name);
  return row.value === "" ? key : `${key}=${encodeQueryValue(row.value)}`;
}

/**
 * The URL after a table edit: `url` with its query rewritten from the rows
 * in URL (enabled, with a key or value). Returns `url` itself when its query
 * already says the same, so `?`, `&&` and `key=` survive untouched. Entries
 * that still match a row keep their text; other rows are written as
 * `key=value`, or `key` when the value is empty. With no rows the `?` goes.
 * The fragment stays at the end.
 */
export function paramsToUrl(url: string, params: readonly KeyValueDTO[] | null): string {
  const { base, query, fragment } = splitUrl(url);
  const entries = query === null ? [] : parseQuery(query);
  const rows = (params ?? []).filter(isInUrl);
  if (rows.length === entries.length && rows.every((r, i) => rowMatches(r, entries[i] as QueryEntry))) {
    return url;
  }
  let next = 0;
  const parts = rows.map((row) => {
    for (let j = next; j < entries.length; j++) {
      const entry = entries[j] as QueryEntry;
      if (rowMatches(row, entry)) {
        next = j + 1;
        return entry.raw;
      }
    }
    return formatEntry(row);
  });
  return parts.length === 0 ? base + fragment : `${base}?${parts.join("&")}${fragment}`;
}

/**
 * Pairs rows with entries: row index → entry index (or -1). Maximizes the
 * pairs with the same key and value (2 points) or the same key (1 point),
 * keeping order; rows and entries left between two pairs are then paired by
 * position, so editing a key in place keeps its row.
 */
function align(rows: readonly KeyValueDTO[], entries: readonly QueryEntry[]): number[] {
  const n = rows.length;
  const m = entries.length;
  const score = (i: number, j: number) => {
    const row = rows[i] as KeyValueDTO;
    const entry = entries[j] as QueryEntry;
    if (!keyMatches(row, entry)) return 0;
    return valueMatches(row, entry) ? 2 : 1;
  };
  // best[i][j]: the best score for rows[i:] and entries[j:].
  const best: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) {
    const bi = best[i] as number[];
    const bi1 = best[i + 1] as number[];
    for (let j = m - 1; j >= 0; j--) {
      const s = score(i, j);
      bi[j] = Math.max(bi1[j] as number, bi[j + 1] as number, s > 0 ? s + (bi1[j + 1] as number) : 0);
    }
  }
  const pair = new Array<number>(n).fill(-1);
  const anchors: [number, number][] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    const s = score(i, j);
    const here = (best[i] as number[])[j] as number;
    if (s > 0 && here === s + ((best[i + 1] as number[])[j + 1] as number)) {
      anchors.push([i, j]);
      i++;
      j++;
    } else if (here === ((best[i + 1] as number[])[j] as number)) {
      i++;
    } else {
      j++;
    }
  }
  anchors.push([n, m]);
  let pi = 0;
  let pj = 0;
  for (const [ai, aj] of anchors) {
    for (let k = 0; pi + k < ai && pj + k < aj; k++) pair[pi + k] = pj + k;
    if (ai < n) pair[ai] = aj;
    pi = ai + 1;
    pj = aj + 1;
  }
  return pair;
}

function newRow(entry: QueryEntry): KeyValueDTO {
  return { name: entry.key, value: entry.value, description: "", enabled: true, extra: "" };
}

/**
 * The rows after a URL edit: the query entries of `url` merged into
 * `params`. Rows not in the URL (disabled or blank) stay where they are.
 * The rows in URL are paired with the entries in order (see `align`): a
 * paired row takes the entry's key and value (keeping its own text where it
 * encodes to the same) and keeps its description, enabled flag and unknown
 * members; unpaired rows are deleted; unpaired entries become new rows
 * after the row before them. Returns `params` itself when nothing changed.
 */
export function urlToParams(url: string, params: readonly KeyValueDTO[] | null): KeyValueDTO[] | null {
  const all = params ?? [];
  const entries = parseUrlQuery(url);
  const inUrl = all.filter(isInUrl);
  const pair = align(inUrl, entries);

  // Entries no row took, by the ordinal of the row in URL they follow (-1: before the first).
  const taken = new Set(pair.filter((j) => j >= 0));
  const insertsAfter = new Map<number, QueryEntry[]>();
  let lastRow = -1;
  let e = 0;
  const flushInserts = (upTo: number) => {
    for (; e < upTo; e++) {
      const entry = entries[e] as QueryEntry;
      if (taken.has(e)) continue;
      const list = insertsAfter.get(lastRow) ?? [];
      list.push(entry);
      insertsAfter.set(lastRow, list);
    }
  };
  pair.forEach((j, ordinal) => {
    if (j < 0) return;
    flushInserts(j);
    e = j + 1;
    lastRow = ordinal;
  });
  flushInserts(entries.length);

  const out: KeyValueDTO[] = [];
  const pushInserts = (after: number) => {
    for (const entry of insertsAfter.get(after) ?? []) out.push(newRow(entry));
  };
  let ordinal = 0;
  let changed = false;
  for (const row of all) {
    if (!isInUrl(row)) {
      out.push(row);
      continue;
    }
    if (ordinal === 0) pushInserts(-1);
    const j = pair[ordinal] as number;
    if (j >= 0) {
      const entry = entries[j] as QueryEntry;
      const name = keyMatches(row, entry) ? row.name : entry.key;
      const value = valueMatches(row, entry) ? row.value : entry.value;
      if (name === row.name && value === row.value) out.push(row);
      else {
        out.push({ ...row, name, value });
        changed = true;
      }
    } else {
      changed = true;
    }
    pushInserts(ordinal);
    ordinal++;
  }
  if (ordinal === 0) pushInserts(-1);
  if (!changed && out.length === all.length) return params as KeyValueDTO[] | null;
  if (params === null && out.length === 0) return null;
  return out;
}
