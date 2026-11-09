// The filtered view of a MessageSource for the message log: which
// messages pass the direction and kind filters (and the query, when the log
// shows only matching messages), and which of those match the search.
// Updated incrementally: a flush tests only the new messages and trims the
// dropped ones from the front, so a 50,000-message log costs per frame what
// the frame added. A filter change recomputes once.
import {
  MESSAGE_DIRECTIONS,
  type LogMessage,
  type MessageDirection,
  type MessageSource,
} from "../../lib/messageSource";
import { searchableText } from "./format";

export interface LogFilter {
  /** Directions shown. */
  readonly directions: Readonly<Record<MessageDirection, boolean>>;
  /** Only this kind, or all kinds when null. */
  readonly kind: string | null;
  /** Kinds hidden by the log's kind toggles (pings, reconnect notes). */
  readonly hiddenKinds: readonly string[];
  /** Search text; "" searches nothing. Always case-insensitive. */
  readonly query: string;
  /** Treat the query as a regular expression. */
  readonly regex: boolean;
  /** Hide messages that don't match the query (a filter), instead of only highlighting matches. */
  readonly onlyMatching: boolean;
}

export const DEFAULT_LOG_FILTER: LogFilter = {
  directions: { sent: true, received: true, system: true },
  kind: null,
  hiddenKinds: [],
  query: "",
  regex: false,
  onlyMatching: false,
};

export interface CompiledQuery {
  /** Case-insensitive pattern (no g flag), or null when there is no valid query. */
  pattern: RegExp | null;
  /** Why the regular expression is invalid, else null. */
  error: string | null;
}

const escapeRegExp = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

export function compileQuery(query: string, regex: boolean): CompiledQuery {
  if (query === "") return { pattern: null, error: null };
  try {
    return { pattern: new RegExp(regex ? query : escapeRegExp(query), "i"), error: null };
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return { pattern: null, error: message.replace(/^Invalid regular expression: /, "") };
  }
}

export function matchesQuery(m: LogMessage, pattern: RegExp): boolean {
  return pattern.test(searchableText(m)) || (m.badge !== undefined && pattern.test(m.badge));
}

/** A growable list of ascending seqs that can drop from the front without shifting every time. */
export class SeqList {
  private items: number[] = [];
  private start = 0;

  get length(): number {
    return this.items.length - this.start;
  }

  at(i: number): number {
    return this.items[this.start + i];
  }

  push(seq: number): void {
    this.items.push(seq);
  }

  /** Removes seqs below `seq`; returns how many. */
  dropBelow(seq: number): number {
    const from = this.start;
    const end = this.items.length;
    let i = from;
    while (i < end && this.items[i] < seq) i++;
    this.start = i;
    if (this.start > 4096 && this.start * 2 > this.items.length) {
      this.items = this.items.slice(this.start);
      this.start = 0;
    }
    return i - from;
  }

  /** Index of the first seq >= `seq` (length when none). */
  lowerBound(seq: number): number {
    let lo = this.start;
    let hi = this.items.length;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      if (this.items[mid] < seq) lo = mid + 1;
      else hi = mid;
    }
    return lo - this.start;
  }

  /** Index of `seq`, or -1. */
  indexOf(seq: number): number {
    const i = this.lowerBound(seq);
    return i < this.length && this.at(i) === seq ? i : -1;
  }

  toArray(): number[] {
    return this.items.slice(this.start);
  }
}

export interface ViewSync {
  /** Rows removed from the front (dropped from the buffer). */
  removedRows: number;
  /** Rows appended at the end. */
  addedRows: number;
  /** True when the view started over (clear, or the view had fallen behind). */
  reset: boolean;
}

export class MessageView {
  readonly filter: LogFilter;
  readonly query: CompiledQuery;
  /** Seqs of the shown rows, ascending. */
  readonly rows = new SeqList();
  /** Seqs of shown rows that match the query, ascending. Empty without a query. */
  readonly hits = new SeqList();
  /** Rows ever appended to this view: counts new rows while the log is not following. */
  appendedRows = 0;
  private syncedNext = 0;
  private syncedClear = -1;

  constructor(
    private readonly source: MessageSource,
    filter: LogFilter,
  ) {
    this.filter = filter;
    this.query = compileQuery(filter.query, filter.regex);
    this.sync();
  }

  /** Applies what the source changed since the last sync. */
  sync(): ViewSync {
    const snap = this.source.getSnapshot();
    let reset = false;
    let removedRows = 0;
    if (snap.clearedAt !== this.syncedClear || this.syncedNext < snap.firstSeq) {
      reset = this.syncedClear !== -1;
      removedRows = this.rows.length;
      this.rows.dropBelow(Number.POSITIVE_INFINITY);
      this.hits.dropBelow(Number.POSITIVE_INFINITY);
      this.syncedClear = snap.clearedAt;
      this.syncedNext = snap.firstSeq;
    } else {
      removedRows = this.rows.dropBelow(snap.firstSeq);
      this.hits.dropBelow(snap.firstSeq);
    }
    const before = this.rows.length;
    const { directions, kind, onlyMatching } = this.filter;
    const hidden = this.filter.hiddenKinds.length > 0 ? new Set(this.filter.hiddenKinds) : null;
    const pattern = this.query.pattern;
    // An invalid regular expression (pattern null) hides nothing: the error is shown instead.
    const hideNonMatching = onlyMatching && pattern !== null;
    for (let seq = this.syncedNext; seq < snap.nextSeq; seq++) {
      const m = this.source.get(seq);
      if (m === undefined) continue;
      if (!directions[m.direction]) continue;
      if (kind !== null && m.kind !== kind) continue;
      if (hidden !== null && hidden.has(m.kind)) continue;
      const hit = pattern !== null && matchesQuery(m, pattern);
      if (hideNonMatching && !hit) continue;
      this.rows.push(seq);
      if (hit) this.hits.push(seq);
    }
    this.syncedNext = snap.nextSeq;
    const addedRows = this.rows.length - before;
    this.appendedRows += addedRows;
    return { removedRows, addedRows, reset };
  }

  /** The next (dir 1) or previous (dir -1) hit after `fromSeq`, wrapping; null without hits. */
  nextHit(fromSeq: number | null, dir: 1 | -1): number | null {
    const n = this.hits.length;
    if (n === 0) return null;
    if (fromSeq === null) return this.hits.at(dir === 1 ? 0 : n - 1);
    if (dir === 1) {
      const i = this.hits.lowerBound(fromSeq + 1);
      return this.hits.at(i < n ? i : 0);
    }
    const i = this.hits.lowerBound(fromSeq) - 1;
    return this.hits.at(i >= 0 ? i : n - 1);
  }
}

export function sameFilter(a: LogFilter, b: LogFilter): boolean {
  return (
    a.kind === b.kind &&
    a.hiddenKinds.length === b.hiddenKinds.length &&
    a.hiddenKinds.every((k, i) => b.hiddenKinds[i] === k) &&
    a.query === b.query &&
    a.regex === b.regex &&
    a.onlyMatching === b.onlyMatching &&
    MESSAGE_DIRECTIONS.every((d) => a.directions[d] === b.directions[d])
  );
}
