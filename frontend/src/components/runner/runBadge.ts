// The badge and the skip note of a request of a folder run, shared by the
// list of the run's requests and its results.
import type { MessageKey } from "../../i18n";
import { methodBadge, type Badge } from "../tree/badges";

// i18n-ignore-start: file kinds, matched to their short labels, which are protocol tokens
const KIND_LABELS: Readonly<Record<string, string>> = {
  graphql: "GQL",
  sse: "SSE",
  websocket: "WS",
  socketio: "SIO",
  grpc: "GRPC",
  tcp: "TCP",
};
// i18n-ignore-end

/** The badge of a run's request: an HTTP request's method, else its kind. */
export function runBadge(r: { readonly kind: string; readonly method: string }): Badge {
  if (r.kind === "http" && r.method !== "") return methodBadge(r.method);
  return { label: KIND_LABELS[r.kind] ?? (r.kind === "" ? "?" : r.kind.toUpperCase().slice(0, 5)), tone: "dir" };
}

/** A result's outcome in words, by RunResultDTO.outcome. */
export const OUTCOME_TEXT: Readonly<Record<string, MessageKey>> = {
  passed: "runner.outcome.passed",
  failed: "runner.outcome.failed",
  error: "runner.outcome.error",
  skipped: "runner.outcome.skipped",
};

/** Why a run does not send a request, by RunItemDTO.skip. */
export const SKIP_TEXT: Readonly<Record<string, MessageKey>> = {
  kind: "runner.skip.kind",
  subscription: "runner.skip.subscription",
  unknown: "runner.skip.unknown",
  invalid: "runner.skip.invalid",
};
