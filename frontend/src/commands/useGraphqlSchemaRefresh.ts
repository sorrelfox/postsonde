// The opt-in schema auto-refresh of a GraphQL request (GQL-19), run by
// the request's editor while it is open. Both triggers are off unless
// the request's settings turn them on, so by default the schema is
// fetched only from the strip's button (PRV-05):
//
// - after a send whose response is the server's signal that its schema
//   changed: it refused the document in validation although the cached
//   schema accepted it (lib/graphqlSchemaRefresh.ts), at most once per
//   ON_SEND_MIN_GAP_MS;
// - every intervalMinutes while the editor is open and the window is
//   not hidden.
//
// A schema from a file is never fetched: Go re-reads the file when it
// changes. See docs/graphql.md.
import { useEffect, useRef } from "react";
import { readGraphqlResponse } from "../lib/graphqlResponse";
import { ON_SEND_MIN_GAP_MS, responseSignalsSchemaChange, type SchemaRefresh } from "../lib/graphqlSchemaRefresh";
import type { HttpSendState } from "./httpSends";
import type { GraphqlSchema } from "./useGraphqlSchema";

export interface SchemaRefreshInput {
  readonly schema: GraphqlSchema;
  readonly refresh: SchemaRefresh;
  /** The request's send slot state (useHttpSend). */
  readonly send: HttpSendState;
  /**
   * The cached schema accepts the query: no schema diagnostics in the
   * editor's last check, or no schema cached at all.
   */
  readonly schemaAccepts: boolean;
}

export function useGraphqlSchemaRefresh({ schema, refresh, send, schemaAccepts }: SchemaRefreshInput): void {
  const fromFile = schema.info?.source === "file";
  const { autoFetch } = schema;

  // Every intervalMinutes while the editor is open.
  const minutes = fromFile ? 0 : refresh.intervalMinutes;
  useEffect(() => {
    if (minutes <= 0) return;
    const timer = setInterval(() => {
      if (document.visibilityState !== "hidden") autoFetch();
    }, minutes * 60_000);
    return () => clearInterval(timer);
  }, [minutes, autoFetch]);

  // After a send the server answered with a sign of a changed schema.
  const latest = useRef({ onSend: refresh.onSend, fromFile, schemaAccepts, autoFetch });
  latest.current = { onSend: refresh.onSend, fromFile, schemaAccepts, autoFetch };
  const seen = useRef(send.response);
  const lastAuto = useRef(0);
  const response = send.response;
  const status = send.status;
  const run = send.run;
  useEffect(() => {
    if (response === seen.current || status !== "ok") return;
    seen.current = response;
    const { onSend, fromFile: file, schemaAccepts: accepts, autoFetch: fetchNow } = latest.current;
    // A repeat run's responses (HTTP-27) are not the user's single sends.
    if (!onSend || file || !accepts || run !== null || response === null) return;
    if (!responseSignalsSchemaChange(readGraphqlResponse(response))) return;
    const now = Date.now();
    if (now - lastAuto.current < ON_SEND_MIN_GAP_MS) return;
    lastAuto.current = now;
    fetchNow();
  }, [response, status, run]);
}
