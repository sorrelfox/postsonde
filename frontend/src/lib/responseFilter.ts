// Response filter (HTTP-36): a JSONPath query on a JSON body or an XPath
// expression on an XML or HTML body, evaluated in Go on the bytes
// received (HTTPService.FilterBody), and what the filter bar remembers
// per request. Framework-free: no React, no stores.
//
// Each request's filter (its syntax when the user picked one, the
// expression, and its recent expressions) is view state, not part of the
// project, so it lives in the workspace's local UI state (STO-06,
// ./workspaceLocal.ts) under the key "response.filters":
//
//   { "<request id>": { "syntax": "xpath", "expression": "//item",
//                       "recent": [{ "syntax": "xpath", "expression": "//item" }] } }
//
// See docs/http-client.md.
import {
  HTTPService,
  type FilterBodyResultDTO,
  type FilterErrorDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http";
import { t } from "../i18n";
import type { CancellablePromise } from "./calls";
import type { ResponseDTO } from "./http";
import { workspaceLocalStore, type WorkspaceLocalStore } from "./workspaceLocal";

export type { FilterBodyResultDTO, FilterErrorDTO };

export type FilterSyntax = "jsonpath" | "xpath";

// i18n-ignore-start: names of query languages
export const FILTER_SYNTAXES: readonly { value: FilterSyntax; label: string }[] = [
  { value: "jsonpath", label: "JSONPath" },
  { value: "xpath", label: "XPath" },
];
// i18n-ignore-end

/** The error code FilterBody rejects with once Go no longer keeps the body's bytes. */
export const BODY_GONE_CODE = "http.body_gone";

/** Whether a response's body can be filtered: text, not empty, not a hex dump. */
export function canFilter(response: ResponseDTO): boolean {
  return response.body !== "" && !response.bodyHex;
}

/**
 * The syntax a body is filtered with unless the user picks one: JSONPath
 * for JSON, XPath for XML and HTML, and for text what it starts with.
 */
export function defaultFilterSyntax(response: ResponseDTO): FilterSyntax {
  switch (response.bodyLanguage) {
    case "json":
      return "jsonpath";
    case "xml":
    case "html":
      return "xpath";
  }
  return response.body.trimStart().startsWith("<") ? "xpath" : "jsonpath";
}

/** What an empty filter box suggests. */
export function filterPlaceholder(syntax: FilterSyntax): string {
  return syntax === "jsonpath" ? t("lib.responseFilter.placeholder.jsonpath") : t("lib.responseFilter.placeholder.xpath");
}

/**
 * Evaluates `expression` on the body of `response` in Go. With `shown`,
 * the text the view shows is filtered instead of the bytes received (for
 * a body Go no longer keeps).
 */
export function filterResponseBody(response: ResponseDTO, syntax: FilterSyntax, expression: string, shown = false): CancellablePromise<FilterBodyResultDTO> {
  const useText = shown || response.bodyId === "";
  return HTTPService.FilterBody({
    bodyId: useText ? "" : response.bodyId,
    text: useText ? response.body : "",
    syntax,
    expression,
    charset: response.charset,
    html: response.bodyLanguage === "html",
  });
}

/** One remembered expression. */
export interface RecentFilter {
  syntax: FilterSyntax;
  expression: string;
}

/** What is remembered for one request. */
export interface RequestFilter {
  /** The syntax the user picked; null follows the response's content type. */
  syntax: FilterSyntax | null;
  /** The expression in the box; "" for no filter. */
  expression: string;
  /** The latest expressions that gave a result, newest first. */
  recent: RecentFilter[];
}

export const EMPTY_REQUEST_FILTER: RequestFilter = { syntax: null, expression: "", recent: [] };

/** The key of the filters in the workspace's local UI state. */
export const RESPONSE_FILTERS_UI_STATE_KEY = "response.filters";

/** At most this many requests, recent expressions per request, and characters per expression are remembered. */
export const MAX_FILTERED_REQUESTS = 200;
export const MAX_RECENT_FILTERS = 8;
export const MAX_SAVED_EXPRESSION = 2000;

export type ResponseFilters = Record<string, RequestFilter>;

function isSyntax(v: unknown): v is FilterSyntax {
  return v === "jsonpath" || v === "xpath";
}

/** Reads the saved JSON (untrusted: it may be hand edited or from another build). */
export function decodeResponseFilters(raw: unknown): ResponseFilters | null {
  if (raw === null || typeof raw !== "object" || Array.isArray(raw)) return null;
  const out: ResponseFilters = {};
  for (const [id, value] of Object.entries(raw as Record<string, unknown>)) {
    if (id === "" || value === null || typeof value !== "object" || Array.isArray(value)) continue;
    const v = value as Record<string, unknown>;
    const recent: RecentFilter[] = [];
    if (Array.isArray(v.recent)) {
      for (const r of v.recent as unknown[]) {
        if (r === null || typeof r !== "object") continue;
        const { syntax, expression } = r as Record<string, unknown>;
        if (isSyntax(syntax) && typeof expression === "string" && expression !== "") recent.push({ syntax, expression });
      }
    }
    out[id] = {
      syntax: isSyntax(v.syntax) ? v.syntax : null,
      expression: typeof v.expression === "string" ? v.expression : "",
      recent: recent.slice(0, MAX_RECENT_FILTERS),
    };
  }
  return out;
}

/** The JSON saved: requests with nothing to remember are left out, long expressions are not kept. */
export function encodeResponseFilters(filters: ResponseFilters): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  // The most recently changed requests are last (see withRequestFilter); keep those.
  const entries = Object.entries(filters).filter(([, f]) => f.syntax !== null || f.expression !== "" || f.recent.length > 0);
  for (const [id, f] of entries.slice(-MAX_FILTERED_REQUESTS)) {
    const saved: Record<string, unknown> = {};
    if (f.syntax !== null) saved.syntax = f.syntax;
    if (f.expression !== "" && f.expression.length <= MAX_SAVED_EXPRESSION) saved.expression = f.expression;
    const recent = f.recent.filter((r) => r.expression.length <= MAX_SAVED_EXPRESSION).slice(0, MAX_RECENT_FILTERS);
    if (recent.length > 0) saved.recent = recent;
    out[id] = saved;
  }
  return out;
}

/** `filters` with the filter of `requestId` replaced, moved to the end (most recently changed). */
export function withRequestFilter(filters: ResponseFilters, requestId: string, filter: RequestFilter): ResponseFilters {
  const out: ResponseFilters = {};
  for (const [id, f] of Object.entries(filters)) if (id !== requestId) out[id] = f;
  out[requestId] = filter;
  return out;
}

/** `recent` with `entry` first, without duplicates, at most MAX_RECENT_FILTERS. */
export function addRecentFilter(recent: readonly RecentFilter[], entry: RecentFilter): RecentFilter[] {
  const rest = recent.filter((r) => r.syntax !== entry.syntax || r.expression !== entry.expression);
  return [entry, ...rest].slice(0, MAX_RECENT_FILTERS);
}

/** Filters in the workspace's local directory (STO-06). */
export function workspaceLocalResponseFilters(): WorkspaceLocalStore<ResponseFilters> {
  return workspaceLocalStore<ResponseFilters>(RESPONSE_FILTERS_UI_STATE_KEY, decodeResponseFilters, encodeResponseFilters);
}

/** Where the response view keeps the filters; tests may replace it. */
let store: WorkspaceLocalStore<ResponseFilters> = workspaceLocalResponseFilters();

export function responseFilterStore(): WorkspaceLocalStore<ResponseFilters> {
  return store;
}

/** Replaces the store (tests). Returns the previous one. */
export function setResponseFilterStore(next: WorkspaceLocalStore<ResponseFilters>): WorkspaceLocalStore<ResponseFilters> {
  const previous = store;
  store = next;
  return previous;
}

/** The filter remembered for a request of a workspace. */
export function loadRequestFilter(workspaceId: string, requestId: string): RequestFilter {
  return store.load(workspaceId)?.[requestId] ?? EMPTY_REQUEST_FILTER;
}

/** Remembers the filter of a request of a workspace. */
export function saveRequestFilter(workspaceId: string, requestId: string, filter: RequestFilter): void {
  store.save(workspaceId, withRequestFilter(store.load(workspaceId) ?? {}, requestId, filter));
}
