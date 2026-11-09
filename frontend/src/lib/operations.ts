// Long-running operations: follow one operation's event stream and cancel
// it by ID. Framework-free; the React hook is commands/useOperation.ts.
// Conventions (event names, payloads, seq, batching): docs/events.md.
import { Events } from "@wailsio/runtime";
import type {
  DataEventDTO,
  DoneEventDTO,
  ProgressEventDTO,
  StartedDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/operation";
import { OpsService } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops";
import type { CancellablePromise } from "./calls";
import { isCancelled, toAppError, type AppError } from "./errors";

export type { DataEventDTO, DoneEventDTO, ProgressEventDTO, StartedDTO };

/** The phases of an operation's events: `<domain>:op:<phase>`. */
export type OperationPhase = "data" | "progress" | "done";

/** How an operation ended. */
export type OperationOutcome = "ok" | "error" | "cancelled";

/** Returns the event name for a domain's phase, e.g. `"demo:op:data"`. */
export function operationEventName(domain: string, phase: OperationPhase): string {
  return `${domain}:op:${phase}`;
}

/** The end of an operation, from its done event or a failed start. */
export interface OperationResult {
  /** The operation ID, or null when the start call itself failed. */
  opId: string | null;
  outcome: OperationOutcome;
  /**
   * The error when outcome is "error" (kind, code, message, hint, … as for
   * a rejected call), otherwise null.
   */
  error: AppError | null;
  /** Items and data events sent, and the time taken, as reported by Go. */
  items: number;
  batches: number;
  elapsedMs: number;
}

export interface OperationHandlers<T> {
  /** Go has started the operation. */
  onStarted?: (opId: string) => void;
  /** A batch of items (one data event). */
  onData?: (items: T[], event: DataEventDTO<T>) => void;
  /** The latest progress (at most one per flush interval). */
  onProgress?: (event: ProgressEventDTO) => void;
  /** An event was missed: seq jumped from expected to got. */
  onGap?: (expected: number, got: number) => void;
  /**
   * The operation ended. Called exactly once, after all its other events,
   * unless the handle is disposed first.
   */
  onDone?: (result: OperationResult) => void;
}

export interface OperationHandle {
  /** The operation ID once Go has returned it, else null. */
  opId(): string | null;
  /**
   * Asks Go to cancel the operation. Before the ID is known the request is
   * kept and sent as soon as it is. The operation still ends with onDone
   * (outcome "cancelled").
   */
  cancel(): void;
  /** Stops listening. Does not cancel: call cancel() first for that. */
  dispose(): void;
}

/** Cancels any operation by ID. Resolves to false if it was not running. */
export function cancelOperation(opId: string): CancellablePromise<boolean> {
  return OpsService.Cancel(opId);
}

/**
 * Starts an operation and routes its events to handlers.
 *
 * `start` calls the domain's start method (e.g. `DemoService.Stream`). The
 * domain's events are subscribed to before `start` is called and buffered
 * until the operation ID is known, so the first batches are not lost if
 * they arrive before the start call resolves. Events of other operations of
 * the same domain are ignored. Subscriptions end with the done event.
 */
export function startOperation<T>(
  domain: string,
  start: () => PromiseLike<StartedDTO>,
  handlers: OperationHandlers<T>,
): OperationHandle {
  let opId: string | null = null;
  let lastSeq = 0;
  let finished = false;
  let disposed = false;
  let cancelRequested = false;
  let buffer: { phase: OperationPhase; event: OpEvent }[] = [];

  const unsubscribe = (["data", "progress", "done"] as const).map((phase) =>
    Events.On(operationEventName(domain, phase), (ev) => receive(phase, ev.data)),
  );
  function stopListening() {
    for (const off of unsubscribe.splice(0)) off();
  }

  function receive(phase: OperationPhase, data: unknown) {
    if (finished || disposed || !isOpEvent(data)) return;
    if (opId === null) {
      buffer.push({ phase, event: data });
    } else if (data.opId === opId) {
      deliver(phase, data);
    }
  }

  function deliver(phase: OperationPhase, event: OpEvent) {
    if (finished || disposed) return;
    if (event.seq <= lastSeq) return; // duplicate or stale
    if (event.seq !== lastSeq + 1) handlers.onGap?.(lastSeq + 1, event.seq);
    lastSeq = event.seq;

    switch (phase) {
      case "data": {
        const data = event as DataEventDTO<T>;
        handlers.onData?.(data.items ?? [], data);
        break;
      }
      case "progress":
        handlers.onProgress?.(event as ProgressEventDTO);
        break;
      case "done": {
        const done = event as DoneEventDTO;
        const outcome = outcomeOf(done.status);
        finish({
          opId: done.opId,
          outcome,
          error: outcome === "error" ? toAppError(done.error) : null,
          items: done.items,
          batches: done.batches,
          elapsedMs: done.elapsedMs,
        });
        break;
      }
    }
  }

  function finish(result: OperationResult) {
    finished = true;
    buffer = [];
    stopListening();
    handlers.onDone?.(result);
  }

  let call: PromiseLike<StartedDTO>;
  try {
    call = start();
  } catch (err) {
    call = Promise.reject(err);
  }
  call.then(
    (started) => {
      opId = started.opId;
      // Go is running it now: send a cancel that was asked for earlier,
      // even if the caller has stopped listening meanwhile.
      if (cancelRequested) sendCancel(opId);
      if (disposed || finished) return;
      handlers.onStarted?.(opId);
      const pending = buffer;
      buffer = [];
      for (const { phase, event } of pending) {
        if (event.opId === opId) deliver(phase, event);
      }
    },
    (err: unknown) => {
      if (disposed || finished) return;
      const cancelled = isCancelled(err);
      finish({
        opId: null,
        outcome: cancelled ? "cancelled" : "error",
        error: cancelled ? null : toAppError(err),
        items: 0,
        batches: 0,
        elapsedMs: 0,
      });
    },
  );

  return {
    opId: () => opId,
    cancel() {
      if (finished || cancelRequested) return;
      cancelRequested = true;
      if (opId !== null) sendCancel(opId);
    },
    dispose() {
      disposed = true;
      buffer = [];
      stopListening();
    },
  };
}

/** Sends a cancel; the done event, not this call, reports the outcome. */
function sendCancel(opId: string) {
  cancelOperation(opId).then(undefined, () => {});
}

interface OpEvent {
  opId: string;
  seq: number;
}

function isOpEvent(data: unknown): data is OpEvent {
  if (typeof data !== "object" || data === null) return false;
  const e = data as Partial<Record<keyof OpEvent, unknown>>;
  return typeof e.opId === "string" && typeof e.seq === "number";
}

/** Maps the done event's status; anything unexpected counts as an error. */
function outcomeOf(status: string): OperationOutcome {
  switch (status) {
    case "ok":
    case "cancelled":
      return status;
    default:
      return "error";
  }
}
