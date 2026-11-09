// Path parameters (HTTP-05): the `:name` and `{name}` placeholders in the
// path of a URL, and the rows of the editor's path table. Pure functions.
//
// Detection mirrors Go's request.PathParams (internal/core/request/
// pathparams.go); both run internal/core/request/testdata/path_params.json.
// Go fills in the values when sending (request.SubstitutePath); the editor
// only lists the names. Rules: docs/http-client.md (Path parameters) and
// docs/frontend.md (Request editor).

import type { KeyValueDTO } from "./collection";
import { templateVars } from "./templateSyntax";

export interface PathParamSpan {
  /** The name without its `:` or braces. */
  readonly name: string;
  /** `url.slice(start, end)` is the placeholder as typed (UTF-16 offsets). */
  readonly start: number;
  readonly end: number;
}

const IDENT = /[A-Za-z0-9_]/;
const BRACE_NAME = /[A-Za-z0-9_.-]/;

/**
 * The placeholders in the path of `url` (as typed), in order, duplicates
 * included. The path runs from the first `/` after the authority to the
 * first `?` or `#`; `{{variables}}` are skipped.
 */
export function findPathParams(url: string): PathParamSpan[] {
  // varEnd[i]: end of the {{variable}} holding index i, or 0.
  const varEnd = new Array<number>(url.length).fill(0);
  for (const v of templateVars(url)) {
    for (let i = v.start; i < v.end; i++) varEnd[i] = v.end;
  }
  const inVar = (i: number) => (varEnd[i] ?? 0) > 0;

  let end = url.length;
  let scheme = -1;
  for (let i = 0; i < url.length; i++) {
    if (inVar(i)) {
      i = (varEnd[i] ?? 0) - 1;
      continue;
    }
    const c = url[i];
    if (c === "?" || c === "#") {
      end = i;
      break;
    }
    if (scheme < 0 && url.startsWith("://", i)) scheme = i;
  }

  let start = -1;
  for (let i = scheme >= 0 ? scheme + 3 : 0; i < end; i++) {
    if (inVar(i)) {
      i = (varEnd[i] ?? 0) - 1;
      continue;
    }
    if (url[i] === "/") {
      start = i;
      break;
    }
  }
  if (start < 0) return [];

  const out: PathParamSpan[] = [];
  for (let i = start; i < end; i++) {
    if (inVar(i)) {
      i = (varEnd[i] ?? 0) - 1;
      continue;
    }
    const c = url[i];
    if (c === ":") {
      if (url[i - 1] !== "/") continue;
      let j = i + 1;
      while (j < end && IDENT.test(url[j] ?? "")) j++;
      if (j > i + 1) {
        out.push({ name: url.slice(i + 1, j), start: i, end: j });
        i = j - 1;
      }
    } else if (c === "{") {
      if (url[i - 1] === "{" || i + 1 >= end || url[i + 1] === "{") continue;
      let j = i + 1;
      while (j < end && BRACE_NAME.test(url[j] ?? "")) j++;
      if (j === i + 1 || j >= end || url[j] !== "}" || url[j + 1] === "}") continue;
      out.push({ name: url.slice(i + 1, j), start: i, end: j + 1 });
      i = j;
    }
  }
  return out;
}

/** The distinct placeholder names of `url`, in order of first use: the path table's rows. */
export function pathParamNames(url: string): string[] {
  return [...new Set(findPathParams(url).map((p) => p.name))];
}

/** Rows of the path table put aside while their names are not in the URL, by name. */
export type PathParamStash = Readonly<Record<string, KeyValueDTO>>;

export interface PathParamsSync {
  /** The rows for the URL's names, in URL order; `rows` itself when nothing changed. */
  readonly rows: readonly KeyValueDTO[] | null;
  /** Rows dropped because their names left the URL (to stash). */
  readonly removed: readonly KeyValueDTO[];
}

/**
 * The path rows for `url`: one per name, in URL order. A name keeps its
 * row from `rows` (the first one of that name), or gets the stashed row
 * back, or a new empty row. Rows whose names are not in the URL are
 * `removed`. When the names already match `rows`, `rows` comes back as is.
 */
export function syncPathParams(
  url: string,
  rows: readonly KeyValueDTO[] | null,
  stash: PathParamStash = {},
): PathParamsSync {
  const names = pathParamNames(url);
  const current = rows ?? [];
  if (current.length === names.length && current.every((r, i) => r.name === names[i])) {
    return { rows, removed: [] };
  }
  const byName = new Map<string, KeyValueDTO>();
  const removed: KeyValueDTO[] = [];
  const wanted = new Set(names);
  for (const row of current) {
    if (wanted.has(row.name) && !byName.has(row.name)) byName.set(row.name, row);
    else if (!wanted.has(row.name)) removed.push(row);
  }
  const next = names.map(
    (name) => byName.get(name) ?? stash[name] ?? { name, value: "", description: "", enabled: true, extra: "" },
  );
  return { rows: next, removed };
}
