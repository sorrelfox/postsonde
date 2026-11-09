import { useEffect, useState } from "react";
import { isCancelled } from "../lib/errors";
import { graphqlDiagnostics, type DiagnosticDTO, type OperationDTO } from "../lib/graphqlSchema";

/** How long typing must pause before the query is checked, in ms. */
export const GRAPHQL_CHECK_DELAY_MS = 300;

export interface GraphqlDiagnostics {
  /** The text the result is for; null before the first result. */
  readonly text: string | null;
  /** The schema key the result is for. */
  readonly schemaKey: string;
  /** Whether the document was validated against a schema. */
  readonly schema: boolean;
  /** The document's operations in source order. */
  readonly operations: readonly OperationDTO[];
  /** Syntax and schema problems, sorted by position. */
  readonly diagnostics: readonly DiagnosticDTO[];
  /** How many problems there are (the list may be capped). */
  readonly count: number;
}

const NONE: GraphqlDiagnostics = { text: null, schemaKey: "", schema: false, operations: [], diagnostics: [], count: 0 };

/**
 * Parses the GraphQL query `text` in Go and validates it against the
 * cached schema `schemaKey` ("" for syntax only) once typing pauses for
 * `delayMs` (GQL-01, GQL-12; GraphQLService.Diagnostics): the markers and
 * the problems bar, and the operations for the operation picker. Go's
 * parser is the only one, so the markers, the picker, prettify and the
 * operationName sent all agree.
 *
 * The last result stays until the next one arrives, so the markers and
 * the picker do not blink while typing. A check still running when the
 * text or the schema changes is cancelled and its result dropped; a
 * failed check keeps the last result.
 */
export function useGraphqlDiagnostics(text: string, schemaKey: string, delayMs: number = GRAPHQL_CHECK_DELAY_MS): GraphqlDiagnostics {
  const [check, setCheck] = useState<GraphqlDiagnostics>(NONE);

  useEffect(() => {
    if (text.trim() === "") {
      setCheck({ ...NONE, text, schemaKey, schema: schemaKey !== "" });
      return;
    }
    let call: ReturnType<typeof graphqlDiagnostics> | null = null;
    let live = true;
    const timer = setTimeout(() => {
      call = graphqlDiagnostics(schemaKey, text);
      call.then(
        (result) => {
          if (!live) return;
          setCheck({
            text,
            schemaKey,
            schema: result.schema,
            operations: result.operations ?? [],
            diagnostics: result.diagnostics ?? [],
            count: result.count,
          });
        },
        (err: unknown) => {
          if (live && !isCancelled(err)) console.warn("graphql: the query could not be checked", err);
        },
      );
    }, delayMs);
    return () => {
      live = false;
      clearTimeout(timer);
      if (call !== null) void call.cancel();
    };
  }, [text, schemaKey, delayMs]);

  return check;
}
