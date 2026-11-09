// The schema of a GraphQL request's endpoint (GQL-10, GQL-21): what Go
// has cached, read when the editor opens and whenever the endpoint may
// have changed, and a fetch by introspection that runs only when the user
// asks (Fetch schema / Refresh). A request with a schema file (GQL-11)
// gets the file's schema instead: Go reads it again when it changed, so
// the info is also re-read when the window regains focus, and Refresh
// re-reads the file. A request can opt in to refreshing by itself
// (GQL-19, useGraphqlSchemaRefresh), which calls autoFetch. See
// docs/graphql.md.
import { useCallback, useEffect, useRef, useState } from "react";
import { isCancelled, toAppError, type AppError } from "../lib/errors";
import {
  fetchGraphqlSchema,
  graphqlSchemaInfo,
  GRAPHQL_DOMAIN,
  reloadGraphqlSchemaFile,
  type SchemaInfoDTO,
} from "../lib/graphqlSchema";
import { startOperation, type OperationHandle } from "../lib/operations";
import type { RequestId, Stores } from "../stores";
import { waitForQueuedEdits } from "./autosave";

/** How long the endpoint must stay unchanged before its cached schema is looked up, in ms. */
export const SCHEMA_INFO_DELAY_MS = 300;

export interface GraphqlSchema {
  /** What is cached for the endpoint, or null before the first answer. */
  readonly info: SchemaInfoDTO | null;
  /** A fetch is running. */
  readonly fetching: boolean;
  /** Go's stage of the running fetch ("Sending the introspection query"), or "". */
  readonly stage: string;
  /** Why the last fetch failed, or null. */
  readonly error: AppError | null;
  /**
   * How the last fetch's schema compared with the one cached before it
   * (GQL-19), while the endpoint stays the same; null before a fetch.
   */
  readonly lastFetch: LastFetch | null;
  /**
   * Fetches the schema by introspection, replacing the cached one; for a
   * schema from a file, reads the file again.
   */
  fetch(): void;
  /**
   * The schema auto-refresh's fetch (GQL-19): as fetch, marked automatic
   * in lastFetch; nothing for a schema from a file, which is re-read when
   * it changes anyway, or while a fetch runs.
   */
  autoFetch(): void;
  /** Cancels a running fetch. */
  cancel(): void;
}

export interface LastFetch {
  /** The schema key the fetch was for. */
  readonly key: string;
  /** "new", "changed" or "unchanged" (SchemaInfoDTO.change). */
  readonly change: string;
  /** The fetch was the auto-refresh's, not the user's click. */
  readonly auto: boolean;
}

// Every mounted editor re-reads its schema info after any fetch succeeds:
// requests that share an endpoint share its schema.
const fetchedListeners = new Set<() => void>();

function notifyFetched(): void {
  for (const listener of [...fetchedListeners]) listener();
}

/**
 * The schema of request `id`. `url`, `environment` (the active
 * environment's ID) and `schemaFile` (the request's stored schema file)
 * are only change signals: Go renders the endpoint and reads the file
 * itself. Nothing here fetches on its own.
 */
export function useGraphqlSchema(
  stores: Stores,
  id: RequestId,
  url: string,
  environment: string | null,
  schemaFile = "",
  delayMs: number = SCHEMA_INFO_DELAY_MS,
): GraphqlSchema {
  const [info, setInfo] = useState<SchemaInfoDTO | null>(null);
  const [fetching, setFetching] = useState(false);
  const [stage, setStage] = useState("");
  const [error, setError] = useState<AppError | null>(null);
  const [lastFetch, setLastFetch] = useState<LastFetch | null>(null);
  const [refreshes, setRefreshes] = useState(0);
  const handle = useRef<OperationHandle | null>(null);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    const listener = () => setRefreshes((n) => n + 1);
    fetchedListeners.add(listener);
    return () => {
      mounted.current = false;
      fetchedListeners.delete(listener);
      handle.current?.cancel();
      handle.current?.dispose();
      handle.current = null;
    };
  }, []);

  // The cached schema's summary, looked up after the endpoint settles.
  useEffect(() => {
    let live = true;
    let call: ReturnType<typeof graphqlSchemaInfo> | null = null;
    const timer = setTimeout(() => {
      void (async () => {
        await waitForQueuedEdits(stores, id);
        if (!live) return;
        call = graphqlSchemaInfo(id);
        try {
          const next = await call;
          if (!live) return;
          setInfo(next);
          // Another endpoint (or environment): the note was about the old one.
          setLastFetch((last) => (last !== null && last.key !== next.key ? null : last));
        } catch (err: unknown) {
          if (live && !isCancelled(err)) console.warn("graphql: the schema could not be read", toAppError(err));
        }
      })();
    }, delayMs);
    return () => {
      live = false;
      clearTimeout(timer);
      if (call !== null) void call.cancel();
    };
  }, [stores, id, url, environment, schemaFile, refreshes, delayMs]);

  // A schema file may change outside the app: look again on focus.
  const fromFile = schemaFile !== "";
  useEffect(() => {
    if (!fromFile) return;
    const onFocus = () => setRefreshes((n) => n + 1);
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
  }, [fromFile]);

  const run = useCallback((auto: boolean) => {
    if (handle.current !== null) return;
    if (auto && fromFile) return;
    setFetching(true);
    setError(null);
    setStage("");
    if (fromFile) {
      // Reading a file is quick: a plain call, not an operation.
      const pending: OperationHandle = { opId: () => null, cancel: () => undefined, dispose: () => undefined };
      handle.current = pending;
      void (async () => {
        await waitForQueuedEdits(stores, id);
        if (!mounted.current || handle.current !== pending) return;
        try {
          const next = await reloadGraphqlSchemaFile(id);
          if (mounted.current) setInfo(next);
        } catch (err: unknown) {
          if (mounted.current && !isCancelled(err)) setError(toAppError(err));
        } finally {
          if (handle.current === pending) handle.current = null;
          if (mounted.current) setFetching(false);
        }
      })();
      return;
    }
    // Taken before the edits are flushed, so a second click is ignored.
    const pending: OperationHandle = { opId: () => null, cancel: () => undefined, dispose: () => undefined };
    handle.current = pending;
    void (async () => {
      await waitForQueuedEdits(stores, id);
      if (!mounted.current || handle.current !== pending) return;
      handle.current = startOperation<SchemaInfoDTO>(GRAPHQL_DOMAIN, () => fetchGraphqlSchema(id), {
        onProgress: (ev) => {
          if (mounted.current) setStage(ev.message);
        },
        onData: (items) => {
          const next = items[items.length - 1];
          if (!mounted.current || next === undefined) return;
          setInfo(next);
          setLastFetch(next.change === "" ? null : { key: next.key, change: next.change, auto });
        },
        onDone: (result) => {
          handle.current = null;
          if (!mounted.current) return;
          setFetching(false);
          setStage("");
          if (result.outcome === "error") setError(result.error);
          if (result.outcome === "ok") notifyFetched();
        },
      });
    })();
  }, [stores, id, fromFile]);

  const fetch = useCallback(() => run(false), [run]);
  const autoFetch = useCallback(() => run(true), [run]);
  const cancel = useCallback(() => handle.current?.cancel(), []);

  return { info, fetching, stage, error, lastFetch, fetch, autoFetch, cancel };
}
