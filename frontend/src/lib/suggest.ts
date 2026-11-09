// Suggestions for text fields: the item type the autocomplete popup
// (components/common/Autocomplete) shows, and the matching used to filter
// candidates as the user types. Framework-free. See docs/frontend.md
// (Suggestions).

/** One entry of a suggestion popup. */
export interface Suggestion {
  /** The field's whole text after accepting it. Also the item's key. */
  readonly value: string;
  /** What the list shows. Default: `value`. */
  readonly label?: string;
  /** A short description, shown dimmed after the label. */
  readonly detail?: string;
  /** A hint, e.g. what the HTTP client does with a header: an info mark with this text as its tooltip. */
  readonly note?: string;
}

/** The most items a popup shows. */
export const MAX_SUGGESTIONS = 8;

/** How well `candidate` matches the typed `query`. Lower is better; null is no match. */
export type MatchRank = 0 | 1 | 2 | 3 | 4;

const WORD_BREAK = /[-_/.+;=\s]/;

/**
 * Ranks a candidate against the typed text, case-insensitively:
 * 0 equal, 1 prefix, 2 prefix of a later word (after - _ / . + ; = or a
 * space: "enc" finds "Accept-Encoding"), 3 substring, 4 the query's
 * characters in order, starting with the first ("ctype" finds
 * "Content-Type", "con" does not find "Accept-Encoding"). An empty query
 * matches everything at rank 1.
 */
export function matchRank(candidate: string, query: string): MatchRank | null {
  const c = candidate.toLowerCase();
  const q = query.toLowerCase();
  if (q === "") return 1;
  if (c === q) return 0;
  if (c.startsWith(q)) return 1;
  const at = c.indexOf(q);
  if (at > 0) {
    for (let i = at; i >= 0; i = c.indexOf(q, i + 1)) {
      if (i > 0 && WORD_BREAK.test(c[i - 1] ?? "")) return 2;
    }
    return 3;
  }
  if (c[0] !== q[0]) return null;
  let j = 0;
  for (let i = 0; i < c.length && j < q.length; i++) {
    if (c[i] === q[j]) j++;
  }
  return j === q.length ? 4 : null;
}

/**
 * The candidates that match `query`, best first, at most `limit`. Equal
 * ranks keep the candidates' order. `textOf` gives the text matched.
 */
export function filterByMatch<T>(
  candidates: readonly T[],
  query: string,
  textOf: (item: T) => string,
  limit = MAX_SUGGESTIONS,
): T[] {
  const ranked: { item: T; rank: number; index: number }[] = [];
  candidates.forEach((item, index) => {
    const rank = matchRank(textOf(item), query);
    if (rank !== null) ranked.push({ item, rank, index });
  });
  ranked.sort((a, b) => a.rank - b.rank || a.index - b.index);
  return ranked.slice(0, Math.max(0, limit)).map((r) => r.item);
}
