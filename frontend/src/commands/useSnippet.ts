import { useEffect, useRef, useState } from "react";
import { isCancelled, type CancellablePromise } from "../lib/calls";
import { toAppError } from "../lib/errors";
import { useStores, type RequestId } from "../stores";
import { requestSnippet, type SnippetDTO, type SnippetOptions, type SnippetSource } from "./snippets";
import { useHttpSend } from "./useHttpSend";

/** Wait after an edit of the request before asking Go again. */
export const SNIPPET_DELAY_MS = 200;

export interface SnippetState {
  /** The latest code, null before the first answer (or when it failed). */
  snippet: SnippetDTO | null;
  /** The options `snippet` was written with. */
  options: SnippetOptions | null;
  /** Why the latest call failed, "" when it did not. */
  error: string;
  /** A call for the current options or edit is on its way. */
  loading: boolean;
}

/**
 * Request `id` as code with `options` (GEN-12), from Go, asked again after
 * each edit of `request` (after SNIPPET_DELAY_MS, once the edits reached
 * Go), after each send (the cookie jar or the OAuth 2.0 token may have
 * changed) and at once when the options change. Only the latest answer is
 * shown. `request` is any value that changes with the request's content
 * (its draft).
 */
export function useSnippet(id: RequestId, request: unknown, options: SnippetOptions): SnippetState {
  const stores = useStores();
  const { state: send } = useHttpSend(id);
  const [state, setState] = useState<SnippetState>({ snippet: null, options: null, error: "", loading: true });
  const lastRequest = useRef<unknown>(null);
  const { language, mode, reveal } = options;

  useEffect(() => {
    let stopped = false;
    let call: CancellablePromise<SnippetDTO> | null = null;
    // An edit waits for more; opening, an option or a send asks at once.
    const edited = lastRequest.current !== null && lastRequest.current !== request;
    lastRequest.current = request;
    setState((s) => (s.loading ? s : { ...s, loading: true }));
    const timer = setTimeout(
      () => {
        void (async () => {
          try {
            const dto = await requestSnippet(
              stores,
              id,
              { language, mode, reveal },
              () => stopped,
              (c) => {
                call = c;
              },
            );
            if (!stopped && dto !== null) setState({ snippet: dto, options: { language, mode, reveal }, error: "", loading: false });
          } catch (err: unknown) {
            if (!stopped && !isCancelled(err)) setState({ snippet: null, options: null, error: toAppError(err).message, loading: false });
          }
        })();
      },
      edited ? SNIPPET_DELAY_MS : 0,
    );
    return () => {
      stopped = true;
      clearTimeout(timer);
      (call as CancellablePromise<SnippetDTO> | null)?.cancel();
    };
  }, [stores, id, request, send.response, language, mode, reveal]);

  return state;
}

/**
 * useSnippet for a gRPC or WebSocket editor (GEN-10, GEN-11): the code of
 * `source` with `options`, asked again SNIPPET_DELAY_MS after each change
 * of `request` (a value that changes with the editor's input) and at once
 * when the options change. Only the latest answer is shown.
 */
export function useSourceSnippet(source: SnippetSource, request: unknown, options: SnippetOptions): SnippetState {
  const [state, setState] = useState<SnippetState>({ snippet: null, options: null, error: "", loading: true });
  const lastRequest = useRef<unknown>(null);
  // The source of the latest render: `request` says when it changed.
  const latest = useRef(source);
  useEffect(() => {
    latest.current = source;
  });
  const { language, mode, reveal } = options;

  useEffect(() => {
    let stopped = false;
    let call: CancellablePromise<SnippetDTO> | null = null;
    const edited = lastRequest.current !== null && lastRequest.current !== request;
    lastRequest.current = request;
    setState((s) => (s.loading ? s : { ...s, loading: true }));
    const timer = setTimeout(
      () => {
        void (async () => {
          try {
            const c = latest.current.write({ language, mode, reveal });
            call = c;
            const dto = await c;
            if (!stopped) setState({ snippet: dto, options: { language, mode, reveal }, error: "", loading: false });
          } catch (err: unknown) {
            if (!stopped && !isCancelled(err)) setState({ snippet: null, options: null, error: toAppError(err).message, loading: false });
          }
        })();
      },
      edited ? SNIPPET_DELAY_MS : 0,
    );
    return () => {
      stopped = true;
      clearTimeout(timer);
      (call as CancellablePromise<SnippetDTO> | null)?.cancel();
    };
  }, [request, language, mode, reveal]);

  return state;
}
