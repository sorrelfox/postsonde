// Calls into the Go GraphQLService (internal/app/services/graphql): the
// schema cached for a GraphQL request's endpoint (GQL-21), fetching it by
// introspection when the user asks (GQL-10), and the query editor's
// diagnostics and completions from it (GQL-12, GQL-13), and the docs
// explorer's pages and search (GQL-14), and the editor's schema actions:
// the docs target of a Ctrl/Cmd-click (GQL-15), operations and fragments
// written for the docs explorer (GQL-16) and fill fields (GQL-17). A request can take its schema
// from a local SDL file instead (GQL-11): the file is picked with the
// native dialog, stored as the request's schemaFile, and read by Go
// whenever the schema info is asked for. A fetch runs as a graphql
// operation (docs/events.md): the schema's summary arrives as one data
// item, then the done event. See docs/graphql.md.
import {
  GraphQLService,
  type CompletionDTO,
  type CompletionItemDTO,
  type DiagnosticDTO,
  type DiagnosticsDTO,
  type DocsDirectiveDTO,
  type DocsEnumValueDTO,
  type DocsFieldDTO,
  type DocsHitDTO,
  type DocsInputValueDTO,
  type DocsOverviewDTO,
  type DocsSearchDTO,
  type DocsTargetDTO,
  type DocsTypeDTO,
  type DocsTypeNameDTO,
  type FillFieldsDTO,
  type GeneratedDTO,
  type OperationDTO,
  type SaveSchemaResultDTO,
  type SchemaErrorDTO,
  type SchemaInfoDTO,
  type SchemaProblemDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/graphql";
import { t } from "../i18n";
import type { CancellablePromise } from "./calls";
import type { StartedDTO } from "./operations";

export type {
  CompletionDTO,
  CompletionItemDTO,
  DiagnosticDTO,
  DiagnosticsDTO,
  DocsDirectiveDTO,
  DocsEnumValueDTO,
  DocsFieldDTO,
  DocsHitDTO,
  DocsInputValueDTO,
  DocsOverviewDTO,
  DocsSearchDTO,
  DocsTargetDTO,
  DocsTypeDTO,
  DocsTypeNameDTO,
  FillFieldsDTO,
  GeneratedDTO,
  OperationDTO,
  SaveSchemaResultDTO,
  SchemaErrorDTO,
  SchemaInfoDTO,
  SchemaProblemDTO,
};

/** The domain of schema fetches: their events are `graphql:op:data|progress|done`. */
export const GRAPHQL_DOMAIN = "graphql";

/** What is cached for the endpoint of request `requestId`. Never sends anything. */
export function graphqlSchemaInfo(requestId: string): CancellablePromise<SchemaInfoDTO> {
  return GraphQLService.SchemaInfo({ requestId });
}

/**
 * Starts introspecting the endpoint of request `requestId` as Go has it
 * (the edits sent so far included). Resolves with the operation ID at
 * once; a request that cannot be built rejects instead.
 */
export function fetchGraphqlSchema(requestId: string): CancellablePromise<StartedDTO> {
  return GraphQLService.FetchSchema({ requestId });
}

/**
 * Reads the schema file of request `requestId` again, even when it looks
 * unchanged (the strip's Reload for a file schema), and returns its info.
 */
export function reloadGraphqlSchemaFile(requestId: string): CancellablePromise<SchemaInfoDTO> {
  return GraphQLService.ReloadSchemaFile({ requestId });
}

/**
 * Shows the native dialog for a .graphql or .graphqls file and resolves
 * with its path as a request stores it (relative to the project root when
 * inside it), or "" when the user cancels.
 */
export async function pickGraphqlSchemaFile(): Promise<string> {
  return (await GraphQLService.PickSchemaFile()) ?? "";
}

/**
 * Saves the schema `key` (SchemaInfoDTO.key) as SDL (GQL-20): Go prints
 * it and asks where with the native save dialog, suggesting `name` (the
 * request's) as the file name. Resolves with the path written, "" when
 * the user cancelled. Nothing is sent.
 */
export function saveGraphqlSchemaSdl(key: string, name: string): CancellablePromise<SaveSchemaResultDTO> {
  return GraphQLService.SaveSchemaSDL({ key, name });
}

/** The file name of a stored schema file path: "api/schema.graphql" gives "schema.graphql". */
export function schemaFileName(path: string): string {
  const parts = path.split(/[\\/]/);
  return parts[parts.length - 1] || path;
}

/**
 * Parses `text` in Go and validates it against the cached schema `key`
 * (SchemaInfoDTO.key; "" for syntax only): its diagnostics and operations.
 */
export function graphqlDiagnostics(key: string, text: string): CancellablePromise<DiagnosticsDTO> {
  return GraphQLService.Diagnostics({ key, text });
}

/**
 * The completions at `offset` (UTF-16, a CodeMirror position) of `text`
 * from the cached schema `key` ("" for syntax only). Never sends anything.
 */
export function graphqlComplete(key: string, text: string, offset: number): CancellablePromise<CompletionDTO> {
  return GraphQLService.Complete({ key, text, offset });
}

/**
 * The docs explorer's start page for the cached schema `key`: its roots,
 * every type's name and kind, and its directives. Never sends anything.
 */
export function graphqlDocsOverview(key: string): CancellablePromise<DocsOverviewDTO> {
  return GraphQLService.DocsOverview({ key });
}

/** One type of the cached schema `key` with its members (found false for an unknown name). */
export function graphqlDocsType(key: string, name: string): CancellablePromise<DocsTypeDTO> {
  return GraphQLService.DocsType({ key, name });
}

/** Searches the types and members of the cached schema `key` in Go: the best `limit` hits and the match count. */
export function graphqlDocsSearch(key: string, query: string, limit: number): CancellablePromise<DocsSearchDTO> {
  return GraphQLService.DocsSearch({ key, query, limit });
}

/**
 * What the name at `offset` (UTF-16) of `text` refers to in the cached
 * schema `key`, as the docs explorer opens it (found false for anything else).
 */
export function graphqlDocsTarget(key: string, text: string, offset: number): CancellablePromise<DocsTargetDTO> {
  return GraphQLService.DocsTarget({ key, text, offset });
}

/** Selection depths the schema actions offer (generate.MaxDepth), and the default. */
export const GRAPHQL_DEPTHS: readonly number[] = [1, 2, 3, 4, 5];
export const DEFAULT_GRAPHQL_DEPTH = 2;

/** What to write from the docs explorer: an operation for a root field, or a fragment on a type (for one field, or all with field ""). */
export interface GenerateRequest {
  readonly kind: "operation" | "fragment";
  readonly type: string;
  readonly field: string;
}

/**
 * Writes an operation or fragment from the cached schema `key`, `depth`
 * selection sets deep, to go into the query `text`: Go picks a name the
 * query does not use and says where it goes (GeneratedDTO.insert, from, to).
 */
export function graphqlGenerate(key: string, request: GenerateRequest, depth: number, text: string): CancellablePromise<GeneratedDTO> {
  return GraphQLService.GenerateOperation({ key, kind: request.kind, type: request.type, field: request.field, depth, text });
}

/** The fields to fill in at `offset` (UTF-16) of `text` from the cached schema `key`, `depth` selection sets deep. */
export function graphqlFillFields(key: string, text: string, offset: number, depth: number): CancellablePromise<FillFieldsDTO> {
  return GraphQLService.FillFields({ key, text, offset, depth });
}

/**
 * How long ago `iso` was, for the schema strip: "just now", "2 min ago",
 * "3 h ago", "5 days ago". An unreadable time gives "".
 */
export function schemaAge(iso: string, now: number = Date.now()): string {
  const at = Date.parse(iso);
  if (Number.isNaN(at)) return "";
  const s = Math.max(0, Math.floor((now - at) / 1000));
  if (s < 60) return t("lib.graphqlSchema.age.justNow");
  const min = Math.floor(s / 60);
  if (min < 60) return t("lib.graphqlSchema.age.minutes", { count: min });
  const h = Math.floor(min / 60);
  if (h < 24) return t("lib.graphqlSchema.age.hours", { count: h });
  return t("lib.graphqlSchema.age.days", { count: Math.floor(h / 24) });
}
