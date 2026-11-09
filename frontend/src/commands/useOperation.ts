// React hook for a long-running Go operation: starts it, follows its
// event stream and cancels it. It lives in commands/ because it calls Go
// through lib/ (stores/ may not import lib/). See docs/events.md.
import { useCallback, useEffect, useRef, useState } from "react";
import type { AppError } from "../lib/errors";
import {
  startOperation,
  type OperationHandle,
  type OperationOutcome,
  type OperationResult,
  type ProgressEventDTO,
  type StartedDTO,
} from "../lib/operations";

export type OperationStatus = "idle" | "starting" | "running" | OperationOutcome;

export interface OperationState<T> {
  status: OperationStatus;
  opId: string | null;
  /** Items received so far. */
  received: number;
  /** Data events received: one state update each. */
  batches: number;
  /** Events missed (seq gaps). Should stay 0. */
  gaps: number;
  progress: ProgressEventDTO | null;
  /** The last `keep` items received. */
  items: readonly T[];
  /** The error when status is "error", otherwise null. */
  error: AppError | null;
  result: OperationResult | null;
  /** performance.now() at start, at the latest update, and at the end. */
  startedAt: number | null;
  updatedAt: number | null;
  finishedAt: number | null;
}

export interface OperationControls<T, A extends unknown[]> extends OperationState<T> {
  /** True while starting or running. */
  running: boolean;
  /** Starts the operation, cancelling one that is still running. */
  start: (...args: A) => void;
  /** Cancels the running operation, if any. */
  cancel: () => void;
}

export interface UseOperationOptions<T = unknown> {
  /** How many of the latest items to keep in `items`; default 100. */
  keep?: number;
  /**
   * Called with each data event's items, before the state update: the way to
   * feed a store that keeps more than `keep` items (a MessageSource for the
   * message log). The latest function is used.
   */
  onItems?: (items: readonly T[]) => void;
}

const DEFAULT_KEEP = 100;

function initialState<T>(): OperationState<T> {
  return {
    status: "idle",
    opId: null,
    received: 0,
    batches: 0,
    gaps: 0,
    progress: null,
    items: [],
    error: null,
    result: null,
    startedAt: null,
    updatedAt: null,
    finishedAt: null,
  };
}

/**
 * Runs operations of one domain. `start` is the lib/ wrapper of the
 * domain's start method. The operation is cancelled if the component
 * unmounts while it runs.
 */
export function useOperation<T, A extends unknown[]>(
  domain: string,
  start: (...args: A) => PromiseLike<StartedDTO>,
  options: UseOperationOptions<T> = {},
): OperationControls<T, A> {
  const keep = options.keep ?? DEFAULT_KEEP;
  const onItems = options.onItems;
  const [state, setState] = useState<OperationState<T>>(initialState);
  const handle = useRef<OperationHandle | null>(null);
  const latest = useRef({ domain, start, keep, onItems });

  useEffect(() => {
    latest.current = { domain, start, keep, onItems };
  });

  useEffect(
    () => () => {
      handle.current?.cancel();
      handle.current?.dispose();
      handle.current = null;
    },
    [],
  );

  const begin = useCallback((...args: A) => {
    const previous = handle.current;
    if (previous) {
      previous.cancel();
      previous.dispose();
    }
    const { domain, start, keep } = latest.current;
    const startedAt = performance.now();
    setState({ ...initialState<T>(), status: "starting", startedAt, updatedAt: startedAt });

    // Handlers run asynchronously, after `current` is assigned below.
    let current: OperationHandle | null = null;
    const update = (fn: (s: OperationState<T>) => OperationState<T>) => {
      if (handle.current === current) setState(fn);
    };
    current = startOperation<T>(domain, () => start(...args), {
      onStarted: (opId) => update((s) => ({ ...s, opId, status: "running" })),
      onData: (items) => {
        if (handle.current !== current) return;
        latest.current.onItems?.(items);
        const at = performance.now();
        update((s) => ({
          ...s,
          received: s.received + items.length,
          batches: s.batches + 1,
          items: keep > 0 ? [...s.items, ...items].slice(-keep) : s.items,
          updatedAt: at,
        }));
      },
      onProgress: (progress) => update((s) => ({ ...s, progress })),
      onGap: () => update((s) => ({ ...s, gaps: s.gaps + 1 })),
      onDone: (result) => {
        const at = performance.now();
        update((s) => ({
          ...s,
          status: result.outcome,
          opId: result.opId ?? s.opId,
          error: result.outcome === "error" ? result.error : null,
          result,
          updatedAt: at,
          finishedAt: at,
        }));
        if (handle.current === current) handle.current = null;
      },
    });
    handle.current = current;
  }, []);

  const cancel = useCallback(() => {
    handle.current?.cancel();
  }, []);

  return {
    ...state,
    running: state.status === "starting" || state.status === "running",
    start: begin,
    cancel,
  };
}
