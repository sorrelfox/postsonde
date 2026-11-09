// The assertion results of stream requests (TEST-08): gRPC calls,
// WebSocket and Socket.IO connections and SSE streams. Go checks a
// request's assertions against the messages its stream received when the
// stream ends (the end item's `tests`), and again on demand
// (CheckAssertions, "Check now"), with the assertions as they are then.
// The protocol sessions report here; the stream views' Tests tabs and the
// Tests sub-tab's result marks read it. See docs/assertions.md#streams.
import { useSyncExternalStore } from "react";
import { GRPCService } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/grpc";
import { SocketIOService } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/socketio";
import { SSEService } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/sse";
import { WebSocketService } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/websocket";
import type { StreamTestResultDTO } from "../lib/assertions";
import { toAppError, type AppError } from "../lib/errors";

export type StreamKind = "grpc" | "websocket" | "sse" | "socketio";

export interface StreamTestsState {
  /** The protocol of the latest stream, and its operation or connection ID once Go returned it. */
  readonly kind: StreamKind | null;
  readonly ref: string | null;
  /** The latest results: of the end of the stream, or of the last check on demand; null before any. */
  readonly tests: readonly StreamTestResultDTO[] | null;
  /** Whether the results are those of the end of the stream (false: checked while it ran). */
  readonly final: boolean;
  /** A check on demand is running. */
  readonly checking: boolean;
  /** Why the last check on demand failed. */
  readonly error: AppError | null;
}

const EMPTY: StreamTestsState = { kind: null, ref: null, tests: null, final: false, checking: false, error: null };

const states = new Map<string, StreamTestsState>();
const listeners = new Map<string, Set<() => void>>();

function get(requestId: string): StreamTestsState {
  return states.get(requestId) ?? EMPTY;
}

function set(requestId: string, patch: Partial<StreamTestsState>): void {
  states.set(requestId, { ...get(requestId), ...patch });
  for (const listener of [...(listeners.get(requestId) ?? [])]) listener();
}

/** A new stream of the request started: the results of the previous one are dropped. */
export function streamStarted(requestId: string, kind: StreamKind): void {
  set(requestId, { ...EMPTY, kind });
}

/** Go returned the stream's operation or connection ID. */
export function streamRef(requestId: string, ref: string): void {
  set(requestId, { ref });
}

/** The results Go sent at the end of the stream. */
export function streamEnded(requestId: string, tests: readonly StreamTestResultDTO[] | null | undefined): void {
  if (tests === null || tests === undefined) return;
  set(requestId, { tests, final: true, error: null });
}

function check(kind: StreamKind, ref: string): PromiseLike<StreamTestResultDTO[] | null> {
  switch (kind) {
    case "grpc":
      return GRPCService.CheckAssertions({ opId: ref });
    case "websocket":
      return WebSocketService.CheckAssertions({ connId: ref });
    case "sse":
      return SSEService.CheckAssertions({ opId: ref });
    case "socketio":
      return SocketIOService.CheckAssertions({ opId: ref });
  }
}

/**
 * Checks the request's assertions, as they are now, against the latest
 * stream's messages so far ("Check now"): while it runs, or after it ended
 * (Go keeps the last streams). Resolves when done; a failure is kept as
 * the state's error.
 */
export async function checkStreamTests(requestId: string): Promise<void> {
  const { kind, ref } = get(requestId);
  if (kind === null || ref === null) return;
  set(requestId, { checking: true, error: null });
  try {
    const tests = await check(kind, ref);
    if (get(requestId).ref !== ref) return;
    set(requestId, { tests: tests ?? [], checking: false, final: false });
  } catch (err: unknown) {
    if (get(requestId).ref !== ref) return;
    set(requestId, { checking: false, error: toAppError(err) });
  }
}

function subscribe(requestId: string, listener: () => void): () => void {
  let set = listeners.get(requestId);
  if (set === undefined) {
    set = new Set();
    listeners.set(requestId, set);
  }
  set.add(listener);
  return () => {
    set.delete(listener);
    if (set.size === 0) listeners.delete(requestId);
  };
}

/** The request's stream results, following changes. */
export function useStreamTests(requestId: string): StreamTestsState {
  return useSyncExternalStore(
    (listener) => subscribe(requestId, listener),
    () => get(requestId),
  );
}

/** Forgets every result, for tests. */
export function resetStreamTests(): void {
  states.clear();
  listeners.clear();
}
