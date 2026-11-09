import { useEffect, useRef, useState } from "react";
import { isCancelled, type CancellablePromise } from "../lib/calls";
import { onEnvironmentsChanged } from "../lib/environments";
import { toAppError } from "../lib/errors";
import { loadResolution, type ResolutionDTO } from "../lib/resolution";
import { onHistoryChanged } from "../lib/responseHistory";
import { onRuntimeVariablesChanged } from "../lib/runtimeVariables";
import { useStores, type RequestId } from "../stores";
import { waitForQueuedEdits } from "./autosave";
import { useHttpSend } from "./useHttpSend";

/** Wait after an edit of the request before asking Go again. */
export const RESOLUTION_DELAY_MS = 200;

export interface ResolutionState {
  /** The latest resolution, null before the first answer (or when it failed). */
  readonly resolution: ResolutionDTO | null;
  /** Why the latest call failed, "" when it did not. */
  readonly error: string;
  readonly loading: boolean;
}

/**
 * The variable resolution of request `id` (DBG-08), from Go, asked again
 * after each edit of `request` (after RESOLUTION_DELAY_MS, once the edits
 * reached Go), after each send (extraction may have set runtime
 * variables), after every environments:changed and runtimevars:changed,
 * and after every history:changed (a new response of a request that a
 * {{$response}} reference reads).
 * Only the latest answer is shown. `request` is any value that changes
 * with the request's content (its draft).
 */
export function useResolution(id: RequestId, request: unknown): ResolutionState {
  const stores = useStores();
  const { state: send } = useHttpSend(id);
  const [state, setState] = useState<ResolutionState>({ resolution: null, error: "", loading: true });
  const [changes, setChanges] = useState(0);
  const lastRequest = useRef<unknown>(null);

  useEffect(() => {
    const reload = () => setChanges((n) => n + 1);
    const off = onEnvironmentsChanged(reload);
    const offRuntime = onRuntimeVariablesChanged(reload);
    // Another request's new response: what its {{$response}} references
    // read (DYN-06).
    const offHistory = onHistoryChanged(reload);
    return () => {
      off();
      offRuntime();
      offHistory();
    };
  }, []);

  useEffect(() => {
    let stopped = false;
    let call: CancellablePromise<ResolutionDTO> | null = null;
    const edited = lastRequest.current !== null && lastRequest.current !== request;
    lastRequest.current = request;
    setState((s) => (s.loading ? s : { ...s, loading: true }));
    const timer = setTimeout(
      () => {
        void (async () => {
          try {
            await waitForQueuedEdits(stores, id);
            if (stopped) return;
            call = loadResolution(id);
            const dto = await call;
            if (!stopped) setState({ resolution: dto, error: "", loading: false });
          } catch (err: unknown) {
            if (!stopped && !isCancelled(err)) setState({ resolution: null, error: toAppError(err).message, loading: false });
          }
        })();
      },
      edited ? RESOLUTION_DELAY_MS : 0,
    );
    return () => {
      stopped = true;
      clearTimeout(timer);
      (call as CancellablePromise<ResolutionDTO> | null)?.cancel();
    };
  }, [stores, id, request, send.response, changes]);

  return state;
}
