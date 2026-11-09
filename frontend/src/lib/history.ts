// Calls into Go for the history panel (NAV-02, NAV-03, docs/history.md):
// the search and filters run in Go over the open project's history
// (HistoryService.List), and so do clearing and making an entry into a
// request to restore (HistoryService.Restore, secrets masked and the masks
// taken out in Go). The pure helpers below turn a restored entry into an
// edit of a request draft and group the list by day.
import {
  HistoryService,
  type HiddenDTO,
  type ListInputDTO,
  type PageDTO as HistoryPageDTO,
  type RestoreDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/history";
import type { KeyValueDTO } from "./collection";
import type { HistorySummaryDTO } from "./responseHistory";
import { urlToParams } from "./urlParams";

export type { HiddenDTO, HistoryPageDTO, RestoreDTO };

/** How many entries a page of the panel loads. */
export const HISTORY_PAGE_SIZE = 50;
/** The most entries one List call returns (history.MaxPageSize). */
export const HISTORY_MAX_PAGE = 500;

/** The panel's filters; "" means any. */
export interface HistoryFilter {
  readonly text: string;
  /** Also search header fields, text bodies and the request as the editor had it. */
  readonly deep: boolean;
  readonly method: string;
  /** "1xx"…"5xx", or "error" for sends without a response. */
  readonly status: string;
  /** "http", "graphql" or "replay". */
  readonly kind: string;
  readonly environment: string;
  /** RFC 3339 bounds: since included, until not. */
  readonly since: string;
  readonly until: string;
  readonly requestId: string;
}

export const NO_FILTER: HistoryFilter = {
  text: "",
  deep: false,
  method: "",
  status: "",
  kind: "",
  environment: "",
  since: "",
  until: "",
  requestId: "",
};

/** One page of the entries `filter` selects, newest first. */
export async function listHistory(filter: HistoryFilter, offset: number, limit: number): Promise<HistoryPageDTO> {
  const input: ListInputDTO = { ...filter, text: filter.text.trim(), offset, limit };
  return HistoryService.List(input);
}

/** Removes every entry of the open project's history. */
export async function clearHistory(): Promise<void> {
  await HistoryService.Clear();
}

/** Removes the entries of request `requestId`; resolves with how many went. */
export async function clearRequestHistory(requestId: string): Promise<number> {
  return HistoryService.ClearRequest(requestId);
}

/** Removes history entry `id`. */
export async function deleteHistoryEntry(id: string): Promise<void> {
  await HistoryService.Delete(id);
}

/** History entry `id` as a request to open again, without any masked value. */
export async function restoreFromHistory(id: string): Promise<RestoreDTO> {
  return HistoryService.Restore(id);
}

/**
 * The start of local day `date` ("2026-09-23", as an `<input type="date">`
 * has it) as RFC 3339, or "" for "". With `nextDay`, the start of the day
 * after: the exclusive end of a range that includes `date`.
 */
export function dayBound(date: string, nextDay = false): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date);
  if (m === null) return "";
  const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]) + (nextDay ? 1 : 0));
  return Number.isNaN(d.getTime()) ? "" : d.toISOString();
}

/** A day group of the list: today, yesterday, or an earlier day (its local midnight). */
export interface HistoryGroup {
  readonly key: string;
  readonly day: "today" | "yesterday" | "earlier";
  /** Local midnight of the day. */
  readonly date: Date;
  readonly entries: readonly HistorySummaryDTO[];
}

function localDay(d: Date): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate());
}

/** Groups entries (newest first) by local day, in order. */
export function groupByDay(entries: readonly HistorySummaryDTO[], now: Date): HistoryGroup[] {
  const today = localDay(now).getTime();
  const yesterday = new Date(today);
  yesterday.setDate(yesterday.getDate() - 1);
  const groups: { key: string; day: HistoryGroup["day"]; date: Date; entries: HistorySummaryDTO[] }[] = [];
  for (const e of entries) {
    const date = localDay(new Date(e.time));
    const key = String(date.getTime());
    let group = groups[groups.length - 1];
    if (group?.key !== key) {
      const day = date.getTime() === today ? "today" : date.getTime() === yesterday.getTime() ? "yesterday" : "earlier";
      group = { key, day, date, entries: [] };
      groups.push(group);
    }
    group.entries.push(e);
  }
  return groups;
}

/** The colour class of an entry's status: 2xx ok, 3xx and 4xx warn, 5xx and failures bad. */
export function statusTone(e: HistorySummaryDTO): "ok" | "warn" | "bad" | "muted" {
  if (e.outcome === "cancelled") return "muted";
  if (e.outcome !== "ok") return "bad";
  if (e.statusCode >= 500) return "bad";
  if (e.statusCode >= 300) return "warn";
  return "ok";
}

/** Sections whose values keep the request's own when restoring into it. */
function hiddenIn(hidden: readonly HiddenDTO[], ...sections: string[]): boolean {
  return hidden.some((h) => sections.includes(h.section));
}

/**
 * The header or path parameter rows of a restore, with each row whose value
 * was hidden (a masked secret) given the value of the row of the same name
 * in `current` (the request's own), so a secret is never replaced by what
 * is left of the mask. Without a row of that name it keeps what is left.
 */
function mergeRows(rows: readonly KeyValueDTO[] | null, section: string, hidden: readonly HiddenDTO[], current: unknown): KeyValueDTO[] {
  const own = Array.isArray(current) ? (current as KeyValueDTO[]) : [];
  return (rows ?? []).map((row) => {
    if (!hidden.some((h) => h.section === section && h.name === row.name)) return row;
    const mine = own.find((r) => r.name.toLowerCase() === row.name.toLowerCase());
    return mine === undefined ? row : { ...row, value: mine.value };
  });
}

/**
 * The draft edit that restores `r` (NAV-03). `current` is the draft of the
 * request it is restored into, or null for a new scratch request. Values
 * the history hid (masked secrets, bodies cut short) are never written
 * from what is left of them into an existing request: a hidden URL, body,
 * query or variables keeps the request's own, a hidden row takes the
 * request's row of the same name. A scratch request gets what is left,
 * masks taken out by Go. An entry kept only as sent (not `templated`) has
 * its query parameters in the URL; its rows are made from it.
 */
export function restorePatch(r: RestoreDTO, current: Readonly<Record<string, unknown>> | null): Record<string, unknown> {
  const hidden = r.hidden ?? [];
  const keep = current !== null;
  const patch: Record<string, unknown> = { method: r.method };
  if (!(keep && hiddenIn(hidden, "url", "params"))) {
    patch.url = r.url;
    if (r.kind === "http") patch.params = r.templated ? (r.params ?? []) : (urlToParams(r.url, null) ?? []);
  }
  patch.headers = keep ? mergeRows(r.headers, "headers", hidden, current.headers) : (r.headers ?? []);
  if (r.kind === "graphql") {
    if (!(keep && hiddenIn(hidden, "query"))) {
      patch.query = r.query;
      patch.operationName = r.operationName;
    }
    if (!(keep && hiddenIn(hidden, "variables"))) patch.variables = r.variables;
    return patch;
  }
  if (r.templated) patch.pathParams = keep ? mergeRows(r.pathParams, "pathParams", hidden, current.pathParams) : (r.pathParams ?? []);
  if (!(keep && hiddenIn(hidden, "body", "bodyFields", "bodyParts"))) {
    patch.body = { ...r.body, fields: r.body.fields ?? [], parts: r.body.parts ?? [] };
  }
  return patch;
}
