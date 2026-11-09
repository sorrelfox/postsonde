// GraphQL query commands (GQL-18): prettify and minify the query of a
// GraphQL editor in Go (internal/core/graphql/printer). The GraphQL editor
// (components/editor/GraphqlEditor.tsx) calls formatGraphqlQuery from its
// toolbar buttons and from the "graphql.prettify" shortcut (lib/shortcuts,
// scope "graphql"). See docs/graphql-engine.md (Printer).

import { isCancelled, toAppError } from "../lib/errors";
import { minifyGraphql, prettifyGraphql, type GraphQLErrorDTO, type GraphQLFormatDTO } from "../lib/format";

export type GraphqlFormat = "prettify" | "minify";

/** Where the query lives: the editor reads and writes it through this. */
export interface GraphqlQueryText {
  /** The query now, or undefined when it is gone (the tab closed). */
  get(): string | undefined;
  /** Saves the formatted query as an edit (undoable in the editor). */
  set(text: string): void;
}

export type GraphqlFormatOutcome =
  /** The query now holds the formatted text (or already did). */
  | { status: "formatted"; text: string }
  /** The query has syntax errors: nothing changed. `error` is the first; `count` counts them all. */
  | { status: "invalid"; error: GraphQLErrorDTO; count: number }
  /** Nothing to do: blank query, query gone, or the text changed while Go worked. */
  | { status: "skipped" }
  /** The call failed (not for syntax errors). */
  | { status: "failed"; message: string };

/**
 * Prettifies (comments kept) or minifies (comments dropped) a GraphQL query
 * in Go and saves the result through `query.set`. A query with syntax
 * errors is not changed; the outcome carries the first error's position in
 * UTF-16 offsets for the editor's marker. When the text changes while Go
 * works, the result is dropped.
 */
export async function formatGraphqlQuery(query: GraphqlQueryText, format: GraphqlFormat): Promise<GraphqlFormatOutcome> {
  const text = query.get();
  if (text === undefined || text.trim() === "") return { status: "skipped" };
  let result: GraphQLFormatDTO;
  try {
    result = await (format === "prettify" ? prettifyGraphql(text) : minifyGraphql(text));
  } catch (err) {
    if (isCancelled(err)) return { status: "skipped" };
    return { status: "failed", message: toAppError(err).message };
  }
  if (query.get() !== text) return { status: "skipped" };
  if (!result.valid || result.error !== null) {
    return result.error !== null
      ? { status: "invalid", error: result.error, count: result.errorCount }
      : { status: "skipped" };
  }
  if (result.text !== text) query.set(result.text);
  return { status: "formatted", text: result.text };
}
