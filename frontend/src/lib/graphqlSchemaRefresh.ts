// The opt-in schema auto-refresh of a GraphQL request (GQL-19): the
// request's settings.graphqlSchemaRefresh, and when a response is the
// server's signal that its schema changed. Off by default: the app only
// sends what the user started (PRV-05), and a request refreshes by
// itself only once the user turned it on. See docs/graphql.md.
import type { GraphQLSchemaRefreshDTO, RequestSettingsDTO } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection";
import type { GraphqlResponse } from "./graphqlResponse";

/** The interval choices, in minutes; 0 is off. */
export const SCHEMA_REFRESH_INTERVALS = [0, 5, 15, 30, 60] as const;

/** The shortest pause between two refreshes after sends, in ms, so a server that keeps failing validation is not introspected after every send. */
export const ON_SEND_MIN_GAP_MS = 30_000;

export interface SchemaRefresh {
  readonly onSend: boolean;
  /** Minutes between refreshes while the editor is open; 0 is off. */
  readonly intervalMinutes: number;
}

/** The request's schema auto-refresh; both off when the file has none. */
export function schemaRefreshOf(settings: RequestSettingsDTO): SchemaRefresh {
  const r = settings.graphqlSchemaRefresh ?? null;
  const minutes = r !== null && Number.isInteger(r.intervalMinutes) && r.intervalMinutes > 0 ? r.intervalMinutes : 0;
  return { onSend: r?.onSend === true, intervalMinutes: minutes };
}

/**
 * The settings member for `next`, keeping the unknown members of
 * `current`: null (left out of the file) when both are off and nothing
 * unknown is kept.
 */
export function schemaRefreshPatch(current: GraphQLSchemaRefreshDTO | null | undefined, next: SchemaRefresh): GraphQLSchemaRefreshDTO | null {
  const extra = current?.extra ?? "";
  if (!next.onSend && next.intervalMinutes === 0 && extra === "") return null;
  return { onSend: next.onSend, intervalMinutes: next.intervalMinutes, extra };
}

/** extensions.code values servers use for a document their schema rejects. */
const VALIDATION_CODES = new Set(["GRAPHQL_VALIDATION_FAILED", "VALIDATION_ERROR", "ValidationError", "GRAPHQL_VALIDATION_ERROR"]);

/** Validation messages of graphql-js (and servers built on it), graphql-java and others. */
const VALIDATION_MESSAGES: readonly RegExp[] = [
  /^Cannot query field "/,
  /^Unknown argument "/,
  /^Unknown type "/,
  /^Unknown directive "/,
  /^Field "[^"]+" argument "[^"]+" of type "[^"]+" is required/,
  /^Value "[^"]*" does not exist in "[^"]+" enum/,
  /^Field "[^"]+" must not have a selection since type/,
  /^Field "[^"]+" of type "[^"]+" must have a selection of subfields/,
  /^Validation error/i,
];

/**
 * Whether a response says the server validated the document against
 * another schema: the server refused it before running it (errors and no
 * `data` member, as the GraphQL spec has for validation failures) with a
 * validation error such as `Cannot query field "x" on type "User".`.
 * The caller also checks that the cached schema accepted the document;
 * then the server's schema has changed since it was fetched.
 */
export function responseSignalsSchemaChange(response: GraphqlResponse | null): boolean {
  if (response === null || response.hasData || response.errors.length === 0) return false;
  return response.errors.some((e) => VALIDATION_CODES.has(e.extensions) || VALIDATION_MESSAGES.some((re) => re.test(e.message)));
}
