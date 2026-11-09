import { useEffect, useState } from "react";
import { isCancelled } from "../lib/errors";
import { checkGraphqlVariables, type GraphQLVariablesCheckDTO } from "../lib/format";

/** How long typing must pause before the variables are checked, in ms. */
export const GRAPHQL_VARIABLES_CHECK_DELAY_MS = 300;

export interface GraphqlVariablesCheck {
  /** The variables text the result is for; null before the first result. */
  readonly text: string | null;
  /** Go's result, or null before the first one. */
  readonly result: GraphQLVariablesCheckDTO | null;
}

const NONE: GraphqlVariablesCheck = { text: null, result: null };

/**
 * Checks a GraphQL request's variables in Go
 * (FormatService.CheckGraphQLVariables) once typing pauses for `delayMs`
 * (GQL-03, GQL-04): the JSON syntax error, and the variables the operation
 * that runs declares but the object lacks or sets but the operation does
 * not declare. The query and the operation name count too, so picking
 * another operation checks again.
 *
 * The last result stays until the next one arrives, so the marker and the
 * list do not blink while typing. A check still running when an input
 * changes is cancelled and its result dropped; a failed check keeps the
 * last result.
 */
export function useGraphqlVariablesCheck(
  query: string,
  variables: string,
  operationName: string,
  delayMs: number = GRAPHQL_VARIABLES_CHECK_DELAY_MS,
): GraphqlVariablesCheck {
  const [check, setCheck] = useState<GraphqlVariablesCheck>(NONE);

  useEffect(() => {
    let call: ReturnType<typeof checkGraphqlVariables> | null = null;
    let live = true;
    const timer = setTimeout(() => {
      call = checkGraphqlVariables(query, variables, operationName);
      call.then(
        (result) => {
          if (live) setCheck({ text: variables, result });
        },
        (err: unknown) => {
          if (live && !isCancelled(err)) console.warn("graphql: the variables could not be checked", err);
        },
      );
    }, delayMs);
    return () => {
      live = false;
      clearTimeout(timer);
      if (call !== null) void call.cancel();
    };
  }, [query, variables, operationName, delayMs]);

  return check;
}
