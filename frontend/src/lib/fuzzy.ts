// Fuzzy matching for the quick switcher (NAV-04): does a query's letters
// appear in a text in order, and how good a match is it. No dependencies,
// linear in the text length, so it filters thousands of items per key press.
// See docs/frontend.md (Quick switcher).

export interface FuzzyMatch {
  /** Higher is better. Only comparable between matches of the same query. */
  readonly score: number;
  /** Indices of the matched characters in the text, ascending. */
  readonly positions: readonly number[];
}

/** Characters after which a word starts. */
const SEPARATORS = new Set([" ", "/", "\\", "-", "_", ".", ":", "(", ")", "[", "]", "{", "}", ",", "?", "&", "="]);

const SCORE_MATCH = 16;
const BONUS_BOUNDARY = 10;
const BONUS_FIRST_CHAR = 6;
const BONUS_CONSECUTIVE = 8;
const BONUS_SUBSTRING = 12;
const PENALTY_GAP_START = 3;
const PENALTY_GAP_CHAR = 1;
const MAX_GAP_PENALTY = 8;

function isUpper(code: number): boolean {
  return code >= 65 && code <= 90;
}

function isLowerOrDigit(code: number): boolean {
  return (code >= 97 && code <= 122) || (code >= 48 && code <= 57);
}

/** Whether a word starts at index i: the start, after a separator, or a camelCase hump. */
export function isWordStart(text: string, i: number): boolean {
  if (i === 0) return true;
  if (SEPARATORS.has(text[i - 1])) return true;
  return isUpper(text.charCodeAt(i)) && isLowerOrDigit(text.charCodeAt(i - 1));
}

/** Prepares a query: lower case, without spaces (a space in the query matches anything). */
export function normalizeQuery(query: string): string {
  return query.toLowerCase().replace(/\s+/g, "");
}

function scorePositions(text: string, positions: readonly number[]): number {
  let score = 0;
  let prev = -2;
  for (const i of positions) {
    score += SCORE_MATCH;
    if (isWordStart(text, i)) score += i === 0 ? BONUS_BOUNDARY + BONUS_FIRST_CHAR : BONUS_BOUNDARY;
    if (i === prev + 1) {
      score += BONUS_CONSECUTIVE;
    } else if (prev >= 0) {
      score -= PENALTY_GAP_START + Math.min(MAX_GAP_PENALTY, (i - prev - 1) * PENALTY_GAP_CHAR);
    }
    prev = i;
  }
  // Among equal matches, a shorter text is the closer one.
  return score - Math.min(10, text.length / 8);
}

/**
 * Matches `query` (already normalized, see normalizeQuery) against `text`.
 * `lower` is text.toLowerCase(), passed in so callers can compute it once.
 * Returns null when the query's characters do not all appear in order.
 *
 * A contiguous occurrence is preferred (the one at a word start if there is
 * one). Otherwise the match is the shortest window that holds the query
 * (found forward, then narrowed backward) with its characters taken as
 * early as possible in that window.
 */
export function fuzzyMatch(query: string, text: string, lower: string = text.toLowerCase()): FuzzyMatch | null {
  const m = query.length;
  if (m === 0) return { score: 0, positions: [] };
  if (m > lower.length) return null;

  let at = lower.indexOf(query);
  if (at >= 0) {
    // Prefer an occurrence that starts a word.
    let best = at;
    while (at >= 0 && !isWordStart(text, at)) {
      at = lower.indexOf(query, at + 1);
      if (at >= 0 && isWordStart(text, at)) best = at;
    }
    const positions = Array.from({ length: m }, (_, k) => best + k);
    return { score: scorePositions(text, positions) + BONUS_SUBSTRING, positions };
  }

  // Forward: the earliest end of a match.
  let qi = 0;
  let end = -1;
  for (let i = 0; i < lower.length; i++) {
    if (lower.charCodeAt(i) === query.charCodeAt(qi) && ++qi === m) {
      end = i;
      break;
    }
  }
  if (end < 0) return null;

  // Backward from the end: the latest start, so the window is the shortest.
  qi = m - 1;
  let start = end;
  for (let i = end; i >= 0; i--) {
    if (lower.charCodeAt(i) === query.charCodeAt(qi) && --qi < 0) {
      start = i;
      break;
    }
  }

  const positions: number[] = [];
  qi = 0;
  for (let i = start; i <= end && qi < m; i++) {
    if (lower.charCodeAt(i) === query.charCodeAt(qi)) {
      positions.push(i);
      qi++;
    }
  }
  return { score: scorePositions(text, positions), positions };
}
