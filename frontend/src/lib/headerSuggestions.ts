// Header name and value suggestions for the Headers table (HTTP-07),
// from the built-in data in httpHeaders.ts. See docs/frontend.md (Request
// editor).

import { t } from "../i18n";
import { findHeader, HTTP_HEADERS } from "./httpHeaders";
import { filterByMatch, MAX_SUGGESTIONS, type Suggestion } from "./suggest";

/**
 * Header names matching the typed text (case-insensitively), best first.
 * Accepting one inserts its canonical spelling. Headers PostSonde's client
 * sets or handles itself carry a note.
 */
export function suggestHeaderNames(text: string, limit = MAX_SUGGESTIONS): Suggestion[] {
  return filterByMatch(HTTP_HEADERS, text.trim(), (h) => h.name, limit).map((h) => ({
    value: h.name,
    detail: t(h.descriptionKey),
    ...(h.clientKey !== undefined && { note: t(h.clientKey) }),
  }));
}

/**
 * Values for the header named `name` (matched case-insensitively) that
 * match the typed text; none for a header without known values.
 *
 * For a list header (Accept, Cache-Control, …) only the item after the last
 * comma is completed: "gzip, b" suggests "gzip, br", labelled "br". Items
 * already in the list are not suggested again.
 */
export function suggestHeaderValues(text: string, name: string, limit = MAX_SUGGESTIONS): Suggestion[] {
  const info = findHeader(name);
  const values = info?.values;
  if (info === undefined || values === undefined) return [];

  if (!info.list) {
    return filterByMatch(values, text.trim(), (v) => v.value, limit).map((v) => ({
      value: v.value,
      ...(v.descriptionKey !== undefined && { detail: t(v.descriptionKey) }),
    }));
  }

  const comma = text.lastIndexOf(",");
  const head = text.slice(0, comma + 1);
  const tail = text.slice(comma + 1);
  const space = /^\s*/.exec(tail)?.[0] ?? "";
  const query = tail.slice(space.length);
  const used = new Set(
    head
      .split(",")
      .map((item) => item.trim().toLowerCase())
      .filter((item) => item !== ""),
  );
  const candidates = values.filter((v) => !used.has(v.value.toLowerCase()));
  return filterByMatch(candidates, query.trim(), (v) => v.value, limit).map((v) => ({
    value: head + space + v.value,
    label: v.value,
    ...(v.descriptionKey !== undefined && { detail: t(v.descriptionKey) }),
  }));
}
