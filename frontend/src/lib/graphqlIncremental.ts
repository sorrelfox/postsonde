// Incremental GraphQL responses (GQL-08, @defer and @stream): Go splits a
// multipart/mixed or text/event-stream body into its payloads, times them
// and merges them (internal/core/graphql/incremental); these helpers word
// what the response view shows. See docs/graphql.md.
import { t } from "../i18n";
import { formatUnit } from "./units";
import { formatDurationMs, type IncrementalDTO, type IncrementalEntryDTO, type IncrementalPartDTO } from "./http";

/** How the incremental view shows a response: the merged result, or the parts as they arrived. */
export type IncrementalMode = "merged" | "parts";

/** A time in milliseconds from the send: "8.5 ms" under 10 ms, "132 ms", then as formatDurationMs. */
export function formatPartTime(ms: number): string {
  if (ms < 10) return formatUnit("ms", Math.round(ms * 10) / 10);
  return formatDurationMs(Math.round(ms));
}

/** Where an entry applies: its path, or "the root"; with its label: "user (Details)". */
function where(e: IncrementalEntryDTO): string {
  const place = e.path !== "" ? e.path : t("lib.graphqlIncremental.root");
  return e.label !== "" ? t("lib.graphqlIncremental.withLabel", { place, label: e.label }) : place;
}

/**
 * What one entry of a part did, in words: "Initial result", "Deferred
 * data at user (Details)", "2 streamed items at user.friends", "Pending:
 * user.friends", "Completed: user (Details)", each with its errors.
 */
export function describeEntry(e: IncrementalEntryDTO): string {
  let text: string;
  switch (e.kind) {
    case "initial":
      text = t("lib.graphqlIncremental.initial");
      break;
    case "defer":
      text = t("lib.graphqlIncremental.defer", { where: where(e) });
      break;
    case "stream":
      text = t("lib.graphqlIncremental.stream", { count: e.items, where: where(e) });
      break;
    case "pending":
      text = t("lib.graphqlIncremental.pending", { where: where(e) });
      break;
    case "completed":
      text = e.id === "" && e.path === "" ? t("lib.graphqlIncremental.errors") : t("lib.graphqlIncremental.completed", { where: where(e) });
      break;
    default:
      text = e.kind;
  }
  if (e.errors > 0) text = t("lib.graphqlIncremental.withErrors", { text, count: e.errors });
  return text;
}

/** The time between a part and the one before it, "" for the first. */
export function partGap(parts: readonly IncrementalPartDTO[], i: number): string {
  const prev = parts[i - 1];
  const part = parts[i];
  if (prev === undefined || part === undefined) return "";
  return `+${formatPartTime(Math.max(0, part.atMs - prev.atMs))}`;
}

/**
 * The summary line: "Receiving… 2 parts", "4 parts in 225 ms, complete",
 * "3 parts, ended before hasNext: false", with the pending entries and
 * errors of the merged result.
 */
export function summarizeIncremental(inc: IncrementalDTO): { text: string; tone: "ok" | "warn" | "bad" | "busy" } {
  const parts = t("lib.graphqlIncremental.parts", { count: inc.partCount });
  const list = inc.parts ?? [];
  const last = list[list.length - 1];
  let text: string;
  if (inc.receiving) text = t("lib.graphqlIncremental.receiving", { parts });
  else if (!inc.completed) text = t("lib.graphqlIncremental.ended", { parts });
  else if (last !== undefined) text = t("lib.graphqlIncremental.completeIn", { parts, time: formatPartTime(last.atMs) });
  else text = t("lib.graphqlIncremental.complete", { parts });
  if (inc.pending > 0) text = t("lib.graphqlIncremental.withPending", { text, count: inc.pending });
  if (inc.errors > 0) text = t("lib.graphqlIncremental.withErrors", { text, count: inc.errors });
  const tone = inc.receiving ? "busy" : !inc.completed ? "warn" : inc.errors > 0 ? "bad" : "ok";
  return { text, tone };
}

/** The Accept value Go sends for a document with @defer or @stream: request.GraphQLIncrementalAccept. */
export const GRAPHQL_INCREMENTAL_ACCEPT = "multipart/mixed;deferSpec=20220824, application/graphql-response+json;q=0.9, application/json;q=0.8";

/**
 * Whether a GraphQL document uses @defer or @stream, as Go decides it
 * (request.UsesIncrementalDelivery): a directive of that name outside
 * comments and strings. Only for showing the Accept header Go will send.
 */
export function usesIncrementalDelivery(query: string): boolean {
  // Block strings, strings and comments go first, so "@defer" inside them does not count.
  const code = query.replace(/"""[\s\S]*?(?:"""|$)|"(?:[^"\\\n]|\\.)*"?|#[^\n\r]*/g, " ");
  return /@\s*(?:defer|stream)(?![_0-9A-Za-z])/.test(code);
}
