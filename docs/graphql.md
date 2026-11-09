# GraphQL requests

A GraphQL request (GQL-01) is a request of kind `graphql`: an endpoint, a
query, variables, extensions, files to upload, headers, auth and settings.
It is sent over HTTP (GQL-02) through the same pipeline as HTTP requests,
so timeouts, cookies, TLS options, environments and the response pane
work the same way.
Subscriptions run over WebSocket (graphql-transport-ws or the legacy
subscriptions-transport-ws) or Server-Sent Events, with connection params
and a live event log, and each request picks the transport of its queries,
mutations and subscriptions separately (GQL-31, GQL-32, GQL-35,
[graphql-subscriptions.md](graphql-subscriptions.md)). The
GraphQL engine (lexer, parser, printer, validation, completion) is in
[graphql-engine.md](graphql-engine.md); the file format is in
[file-format.md](file-format.md#graphql-request-kind-graphql).
To try a client without its server, the local GraphQL mock server
(SSRV-08, [servers.md](servers.md#graphql-mock-server)) serves a schema
from SDL or an introspection result with generated or static data; a
GraphQL request pointed at it can fetch its schema like any other.

```
GraphqlEditor ─ commands/httpSends ─▶ HTTPService.Send({requestId})
                                          │ collection.LoadHTTPSource: HTTPSource{GraphQL: …}
                                          ▼
                              request.BuildGraphQL ─▶ request.BuildHTTP (URL, headers, auth, settings)
                                          │  + the GraphQL body or query string, Accept
                                          ▼
                                   httpclient.Send ─▶ http:op:* ─▶ HttpResponsePanel
```

## Sending (GQL-02)

`request.BuildGraphQL(file, auth, options)` (`internal/core/request/graphql.go`)
maps the file onto the request `httpclient` sends. The endpoint, header
rows, auth and settings go through `BuildHTTP`, so `{{variables}}`, auth
headers and query parameters, timeouts, redirects and TLS verification
behave exactly as for an HTTP request ([http-client.md](http-client.md#sending-from-the-app)). Then:

| | POST (default, `method` missing or `"POST"`) | GET (`method` `"GET"`) |
|---|---|---|
| Query | `{"query", "variables", "operationName", "extensions"}` as the body, `Content-Type: application/json` (a Content-Type row overrides it); with files a [multipart request](#file-uploads-gql-07) | `query`, `variables`, `operationName` and `extensions` appended to the URL's query string, fully percent-encoded; a parameter of that name already in the URL wins |
| Variables | the JSON object, compacted; left out when there are none | the compacted JSON text as the `variables` parameter; left out when there are none |
| operationName | left out when empty | left out when empty |
| Extensions | the JSON object, compacted; left out when there are none | the compacted JSON text as the `extensions` parameter (GraphQL over HTTP); left out when there are none |
| Files | the multipart request spec's body | refused: `request.graphql_upload_get` |

- **Query:** its `{{variables}}` are rendered like every other field
  (ENV-10), e.g. `user(tenant: "{{tenant}}")`; GraphQL's own `$variables`
  use another syntax and are untouched. The operations and
  `operationName` come from the rendered text. A query that is blank
  after rendering is `request.graphql_query_missing`; an undefined
  `{{variable}}` in it has `field` `query`.
- **Variables:** the editor's text. Its `{{variables}}` are rendered first
  (so `"id": "{{userId}}"` or `"n": {{count}}` work), then it must be a JSON
  object; blank text and `null` send no variables. Anything else is
  `request.graphql_invalid_variables` (details: `field` `variables`, and
  `offset` for a syntax error). An undefined `{{variable}}` fails the send
  like one in a header, with `field` `variables`.
- **operationName** (`GraphQLOperationName`): the document is parsed with
  the Go parser. With exactly one operation, its name is sent (nothing for
  an anonymous one), whatever the file says, so a stale saved name cannot
  break a single-operation document. With several, the saved
  `operationName` is sent; the editor makes the user pick one.
- **Extensions** (GQL-06): see [Extensions](#extensions-gql-06).
- **Accept:** `application/graphql-response+json, application/json;q=0.9`
  (`request.GraphQLAccept`) unless an enabled `Accept` row sets one: the
  GraphQL-over-HTTP media type first, plain JSON for older servers. A
  document with a `@defer` or `@stream` directive anywhere (an operation
  or a fragment, found by the Go parser) sends
  `multipart/mixed;deferSpec=20220824, application/graphql-response+json;q=0.9, application/json;q=0.8`
  (`request.GraphQLIncrementalAccept`) instead, so the server can answer
  incrementally ([Incremental delivery](#incremental-delivery-gql-08)).
  An operation whose transport is SSE (GQL-35) sends `text/event-stream`
  and no files (`request.BuildGraphQLSSE`); the event stream is read as an
  incremental response
  ([graphql-subscriptions.md](graphql-subscriptions.md#transport-per-operation-gql-35)).
  An unknown transport in the settings is
  `request.graphql_invalid_transport`.
- **Mutations over GET:** sent anyway (the user asked for GET), with the
  warning `request.graphql_mutation_get` in the response's `warnings`;
  servers usually answer 405.
- **Method:** a file whose `method` is neither missing, `POST` nor `GET`
  (a hand edit) is `request.graphql_invalid_method`.

`HTTPService.Send` loads the request with `collection.LoadHTTPSource`, which
returns `HTTPSource.GraphQL` for a GraphQL request (pending edits and local
auth secrets merged, auth inheritance resolved, as for HTTP) and
`collection.not_http` only for the other kinds. The response is the usual
`ResponseDTO`; `application/graphql-response+json` counts as JSON, so the
body is pretty-printed.

## Extensions (GQL-06)

The Extensions tab holds the request's `extensions` object, e.g. an
automatic persisted query's hash:
`{"persistedQuery": {"version": 1, "sha256Hash": "{{hash}}"}}`. It is
stored as text (`extensions` in the file,
[file-format.md](file-format.md#graphql-request-kind-graphql)) and, like
the variables, has its `{{variables}}` rendered first and must then be a
JSON object; blank text and `null` send none. Anything else is
`request.graphql_invalid_extensions` (details `field` `extensions`,
`offset` for a syntax error). It is sent in the POST body, as the
`extensions` parameter of a GET, in a multipart request's `operations`,
in a subscription's `subscribe` (or legacy `start`) payload
([graphql-subscriptions.md](graphql-subscriptions.md#building-the-connection)),
and in the body of a GraphQL over SSE request.
The schema fetch (introspection) never sends it.

The tab is a JSON `CodeEditor` with the Connection tab's check: a syntax
error gets the marker and "Line 1, column 8", a value that is not an
object "The extensions must be a JSON object."

## File uploads (GQL-07)

A request with files is sent as `multipart/form-data` following the
[GraphQL multipart request spec](https://github.com/jaydenseric/graphql-multipart-request-spec)
(`internal/core/request/graphqlupload.go`), which servers such as Apollo
Server (graphql-upload), GraphQL Yoga, Hot Chocolate and graphene-file-upload
read:

```
--boundary
Content-Disposition: form-data; name="operations"

{"query":"mutation($file: Upload!, $docs: [Upload!]!) { … }","variables":{"docs":[null,null],"file":null},"extensions":{…}}
--boundary
Content-Disposition: form-data; name="map"

{"0":["variables.file"],"1":["variables.docs.0"],"2":["variables.docs.1"]}
--boundary
Content-Disposition: form-data; name="0"; filename="avatar.png"
Content-Type: image/png

<the file's bytes>
…
```

- **Rows.** The Files tab is a table of rows stored as multipart parts
  (`files` in the file, [file-format.md](file-format.md#graphql-request-kind-graphql)):
  the key is the **variable path** the file goes to, the value the file
  (Choose… opens the native dialog, `WorkspaceService.PickBodyFile`, and the
  path is stored as for a multipart body part: relative to the project
  root with `/` when inside it, else absolute), then an optional
  Content-Type (placeholder: the type guessed from the file name, as for
  multipart parts). Disabled rows and rows with neither a path nor a file
  are skipped; the tab's badge counts the others.
- **Paths** (`request.GraphQLUploadPath`) are dot-separated names and list
  indexes as in the spec's map: `variables.file` for a single file,
  `variables.files.0`, `variables.files.1` for a list, and nested
  `variables.input.files.0`. The `variables.` prefix may be left out
  (`input.files.0`). A blank path or an empty step (`a..b`) is
  `request.graphql_invalid_upload`, and so are two rows with the same path
  or one path inside another.
- **Operations.** The rendered variables (or `{}`) get `null` at each
  file's path; objects and lists on the way are created (a list for a
  numeric step, padded with `null`s), and an existing value is replaced.
  A value on the way that is neither an object, a list nor `null` (e.g.
  `"input": "text"` for `input.file`) is `request.graphql_invalid_upload`.
  Then `{"query", "variables", "operationName", "extensions"}` as for a
  JSON POST. Members may come out in another order (objects are
  re-encoded); numbers keep their digits.
- **Map and files.** One file part per distinct file (same path and
  Content-Type), named `0`, `1`, … in row order, listed in the map under
  every path it fills, so a file bound twice is sent once. The filename is
  the file's base name and its Content-Type the row's, or else the one
  guessed from the name (`request.FileMediaType`). Files are streamed from
  disk with a precomputed Content-Length, as for multipart bodies
  ([http-client.md](http-client.md)); a missing file or no file chosen is
  `request.body_file_missing` or `request.body_file_unset`, with `field`
  `files`, `row` and `part` (the path as typed) in the details.
- **Preflight header.** Apollo Server's CSRF prevention refuses a
  multipart request unless it has a non-simple header, so a request with
  files sends `Apollo-Require-Preflight: true`
  (`request.GraphQLPreflightHeader`), which other servers ignore. A header
  row of that name wins; the checkbox under the table ("Send
  Apollo-Require-Preflight: true with files") turns it off through
  `settings.omitHeaders`.
- **GET** cannot carry files: sending one with files is
  `request.graphql_upload_get`, and the tab says to switch to POST.
  Subscriptions ignore the files.

## Schema (GQL-10, GQL-21)

The editor's validation and autocomplete use the endpoint's schema. It is
fetched by introspection **only when the user asks** (Fetch schema /
Refresh in the schema strip, or a request's opt-in auto-refresh, off by
default: [Schema refresh](#schema-refresh-gql-19)), and cached per
endpoint in the workspace's local directory, so it keeps working offline
and after a restart. It can be saved as SDL
([Save as SDL](#save-as-sdl-gql-20)).

```
Schema strip ─ Fetch schema ─▶ GraphQLService.FetchSchema({requestId}) ─▶ graphql:op:* (SchemaInfoDTO)
                                  │ http.NewGraphQLPoster: the request's URL, headers, auth,
                                  │ settings, cookies and environment, as for Send
                                  ▼
                   POST FullIntrospectionQuery ── errors, no schema ──▶ POST ConservativeIntrospectionQuery
                                  │
                   schemacache.Parse ─▶ <local>/graphql-schemas/<hash>.json + memory
editor open, URL or environment change ─▶ GraphQLService.SchemaInfo({requestId}) (no network)
```

- **Endpoint.** A schema belongs to the request's URL with its
  `{{variables}}` rendered (`http.GraphQLEndpoint`), so switching the
  environment switches the schema. A URL with an undefined variable is
  looked up by its text.
- **Fetch.** `FetchSchema` runs as a `graphql` operation (kind
  `introspect`, so the dial guard lets it through: PRV-05). The query is
  always POSTed with `operationName` `IntrospectionQuery`, with the
  request's headers and auth. The full query is sent first; a response
  with errors and no `__schema` (an older server rejecting newer fields,
  `graphql.schema.server_errors`) is retried once with the conservative
  query. Progress reports the stage; the one data item is the new
  `SchemaInfoDTO`. Failures end the operation with an error and keep the
  cached schema: `graphql.introspection_http_status` (an HTTP error status
  without a GraphQL response; details `status`),
  `graphql.introspection_too_large` (cut at the response size limit), the
  `graphql.schema.*` codes of [graphql-engine.md](graphql-engine.md#conversion),
  and the HTTP client's errors. A request that cannot be built (an
  undefined variable, a bad header) fails `FetchSchema` itself. A schema
  with broken references is kept (`problems` counts them).
- **Cache** (`internal/core/graphql/schemacache`): one file per endpoint,
  `<local>/graphql-schemas/<first 16 bytes of SHA-256(endpoint), hex>.json`
  (`0600`, the directory `0700`) holding `{version, endpoint, fetchedAt,
  query, introspection}`, the schema in the `{"__schema": …}` shape of
  `schema.MarshalIntrospection`. A file that is unreadable, of another
  version or endpoint, or does not parse counts as missing. The service
  keeps the parsed schemas in memory by local directory and endpoint, so
  validation and completion never re-parse; a fetch replaces both.
- **`SchemaInfo({requestId})`** returns `SchemaInfoDTO{source, file,
  modifiedAt, error, problemList, endpoint, cached, key, fetchedAt,
  typeCount, query, problems}` without sending anything (`source`
  `"introspection"`; for a request with a schema file see
  [Schema from a file](#schema-from-a-file-gql-11)):
  `key` is what the editor passes to the validation and completion calls
  ("" without a schema), `typeCount` leaves out the `__` introspection
  types, `fetchedAt` is RFC 3339. `change` is set only on a fetch's data
  item: `"new"` (nothing was cached for the endpoint), `"changed"` or
  `"unchanged"` (the introspection JSON compared with the schema cached
  before the fetch, GQL-19); `""` from `SchemaInfo`.

**Schema strip** (`GraphqlSchemaStrip`, under the URL bar): a dot
(green with a schema, amber when it has broken references, grey
without), "Schema · 14 types · introspected 2 min ago"
(redrawn every 30 s; the tooltip names the endpoint, the time and the
query) or "No schema yet: fetch it to check and complete the query", and
the Fetch schema / Refresh button, "Fetching…"/"Refreshing…" with a
spinning icon while it runs, and Load from file… (GQL-11, below). A
failed fetch shows its message (hint in the tooltip). `useGraphqlSchema`
(`commands/`) asks `SchemaInfo` 300 ms after the URL, the active
environment or the request's `schemaFile` changes (after the queued edits
reached Go), and runs the fetch with `startOperation`, also after
flushing the edits; every open GraphQL editor re-reads its info after a
fetch, since requests share schemas by endpoint. For a request with a
schema file, the button is Reload and calls `ReloadSchemaFile` instead
([Schema from a file](#schema-from-a-file-gql-11)). With a schema, the
strip also has **Save SDL…** ([Save as SDL](#save-as-sdl-gql-20)), and
after a Refresh a note says "Schema changed" or "No changes" (from
`change`) until the endpoint changes.

## Schema refresh (GQL-19)

**Manual reload** is the strip's Refresh (introspection) or Reload (a
schema file). On top of that a request can opt in to refreshing by
itself, in the **Schema refresh** group at the top of its Settings tab
(`GraphqlSchemaRefreshSettings`). Both triggers are **off by default**
and stored as nothing, so the app sends no introspection the user did not
turn on (PRV-05, [privacy.md](privacy.md#go-core-user-initiated-traffic-only-prv-05)):

- **Refresh when the server rejects a query the schema accepts**
  (`settings.graphqlSchemaRefresh.onSend`): after a send of the request
  finishes (`status` ok; not a repeat run's sends, HTTP-27), the response
  is the server's signal that its schema changed when it refused the
  document in validation (errors and **no** `data` member, which the
  GraphQL spec reserves for errors raised before execution) with a
  validation error: an `extensions.code` such as
  `GRAPHQL_VALIDATION_FAILED`, or a validation message of graphql-js,
  graphql-java and the like (`Cannot query field "email" on type
  "User".`, `Unknown argument …`, `Unknown type …`, `Value "X" does not
  exist in "E" enum.`, `Validation error …`;
  `responseSignalsSchemaChange` in `lib/graphqlSchemaRefresh.ts`), **and**
  the cached schema accepted the query (the editor's last check found no
  schema problems in it, or nothing is cached). A query the cached schema
  rejects too is a typo, not a change. Then the schema is fetched again,
  at most once per 30 s (`ON_SEND_MIN_GAP_MS`), so a server that keeps
  refusing is not introspected after every send.
- **Refresh every** 5, 15, 30 or 60 minutes
  (`settings.graphqlSchemaRefresh.intervalMinutes`; another whole number
  from a hand edit is kept and shown): while the request's editor is open
  and the window is not hidden.

`useGraphqlSchemaRefresh` (`commands/`) runs both in the editor and calls
the schema hook's `autoFetch`, the same `FetchSchema` as Refresh (with
the request's headers, auth and environment), skipped while a fetch
runs. Its result note reads "Refreshed automatically: schema changed" or
"… no changes"; a failure shows like a failed Refresh. A request with a
schema file has nothing to refresh (Go re-reads the file when it
changes): the group is disabled and says so. Every GraphQL editor
re-reads its info after any fetch, as for Refresh. The file format is in
[file-format.md](file-format.md#settings).

## Save as SDL (GQL-20)

**Save SDL…** in the schema strip (introspected or file schema) saves the
schema as a GraphQL SDL file; nothing is sent:

```
Save SDL… ─▶ GraphQLService.SaveSchemaSDL({key, name}) ─▶ schema.SDL(cached schema)
                                                    └──▶ native save dialog ("<name>.graphql") ─▶ the file
```

- **Printing** is `schema.SDL` ([graphql-engine.md](graphql-engine.md#sdl-export-gql-20)):
  graphql-js's `printSchema` layout, built-in scalars, introspection types
  and spec directives left out, descriptions as block strings where that
  is exact. Introspection does not carry applied directives other than
  `@deprecated`, `@specifiedBy` and `@oneOf`, so those are the only ones
  written. The file loads back (as the request's schema file, GQL-11, or
  anywhere else) as the same schema: the tests compare it as
  introspection JSON.
- **The dialog** suggests the request's name with `.graphql`
  (`SchemaFileName`: the characters file systems refuse replaced; without
  a name, the endpoint's host, a schema file's own name, or `schema`).
  The OS asks before replacing a file. The file is written atomically
  (`atomicfile`, `0644`).
- **Result:** `SaveSchemaResultDTO{path, typeCount}`; `path` "" when the
  dialog was cancelled (nothing is written). The strip says "Saved to
  api.graphql" (the full path in the tooltip) or the error:
  `graphql.no_schema` (nothing cached for the key),
  `graphql.export_failed` (the file could not be written; details
  `path`), `graphql.unavailable` (no dialog wired).

## Schema from a file (GQL-11)

For offline work or an endpoint with introspection disabled, a request can
take its schema from a local SDL file (`.graphql`, `.graphqls`) instead.
Validation, autocomplete and the docs explorer then use the file's schema
exactly like an introspected one.

```
Schema strip ─ Load from file… ─▶ GraphQLService.PickSchemaFile() ─▶ native dialog
                                      │ stored path (relative to the project root when inside it)
                                      ▼
          editRequest({schemaFile}) ─▶ the request file's schemaFile (file-format.md)
                                      ▼
SchemaInfo({requestId}) ─▶ schema.ParseSDL(file) ─▶ key "file\0<abs path>" ─▶ Diagnostics, Complete, Docs*
```

- **Storage.** The path is the request's `schemaFile`
  ([file-format.md](file-format.md#graphql-request-kind-graphql)): relative
  to the project root with `/` when the file is inside the project (so the
  project can be moved or shared with its schema), absolute otherwise, like
  `.proto` files. The file's contents are never copied into the project or
  the schema cache. An empty `schemaFile` means introspection.
- **Allowed directories.** The path comes from the project, so the file is
  read only from the project folder or an allowed directory of the
  settings (PRV-10, `graphqlservice.WithAllowedDirs`): any other is
  `fileaccess.not_allowed` in `SchemaInfoDTO.error`, and a key of a file
  that is no longer allowed serves no schema
  ([privacy.md](privacy.md#local-file-access-prv-10)).
- **Reading.** `SchemaInfo` resolves the path (`graphql.schema_file_no_project`
  for a relative path with no project open) and parses the file with
  `schema.ParseSDL` ([graphql-engine.md](graphql-engine.md#from-sdl-gql-11)).
  The parsed file stays in the service's memory by absolute path with its
  size and modification time; every use (`SchemaInfo`, and `Diagnostics`,
  `Complete` and the docs calls through the key) stats the file and reads
  it again when it changed, so edits made in another editor apply on the
  next check. **Reload** in the strip calls `ReloadSchemaFile({requestId})`,
  which reads it even when it looks unchanged. The editor also asks
  `SchemaInfo` again when the window regains focus. Files over 32 MB are
  refused (`graphql.schema_file_too_large`).
- **Result.** `SchemaInfoDTO` with `source` `"file"`, `file` (the stored
  path), `modifiedAt` (also in `fetchedAt`), `typeCount` and `key`
  (`"file\0"` and the absolute path: a NUL never appears in an endpoint).
  A file that gives no schema has `cached` false, `key` "" and
  `error: {code, message, hint}`: `graphql.schema_file_missing`,
  `graphql.schema_file_unreadable`, `graphql.schema_file_too_large`,
  `graphql.schema_file_no_project` or `graphql.schema.sdl_syntax`. The
  file's syntax or type system problems are in `problemList`
  (`{message, line, column}`, at most 50) and counted in `problems`; with
  type system problems only (an unknown type, a broken interface
  implementation, …) the schema is still used.
- **Picking.** `PickSchemaFile()` shows the native open dialog (filter
  `*.graphql;*.graphqls;*.gql`, starting in the project root) and returns
  the path as stored, or "" when cancelled.
- **The strip** for a file schema: "Schema · 42 types · from file
  schema.graphql" (tooltip: the stored path and modification time), the
  dot amber when the file has problems and red when it gives no schema,
  **Reload**, **Change file…** and **Use introspection** (which clears
  `schemaFile`; each change is its own undo step), then "3 problems in the
  file: Ln 2, Col 7: Unknown type "Foo"." with every problem in its
  tooltip, or the error ("The schema file schema.graphql does not
  exist."). With introspection the strip offers **Load from file…** next to
  Fetch schema / Refresh. Read-only requests get Reload only.

## UI

`TabPanel` shows `components/editor/GraphqlEditor` for a request of kind
`graphql`; the tree and the tab show a `GQL` badge, and `App.tsx` puts the
HTTP response pane (`HttpResponsePanel`) next to it.

- **URL bar:** a POST/GET select styled as the method label (`POST` is saved
  as a missing `method`, `GET` as `"GET"`; the tree's method follows at
  once), the single-line endpoint field (`{{variables}}` drawn as chips) and
  Send, which becomes Cancel while the send runs. Send and ⌘↩
  (`request.send`) go through `commands/httpSends.ts` exactly as in the HTTP
  editor: queued edits reach Go first, and undefined `{{variables}}` ask
  "Send anyway" first. When the operation that runs is a subscription,
  Send becomes **Subscribe** and, while it runs, **Stop**: it runs over
  WebSocket or SSE and the response pane shows its live event log (GQL-34,
  [graphql-subscriptions.md](graphql-subscriptions.md#running-subscriptions-gql-33-gql-34)).
  A query or mutation set to WebSocket in the Connection tab runs the same
  way and the pane shows its result; one set to SSE is sent over HTTP
  asking for an event stream, and its answer shows as a normal
  (incremental) response (GQL-35,
  [graphql-subscriptions.md](graphql-subscriptions.md#transport-per-operation-gql-35)).
- **`{{variables}}`** (ENV-10, ENV-11): the editor sits in the tab panel's
  `TemplateVariablesProvider`, so the endpoint, Query, Variables and header
  fields draw every reference as a chip, red when it does not resolve,
  with its value and scope on hover (the chips of
  [frontend.md](frontend.md#template-variables)). The editor passes
  nothing for this: `CodeEditor` and `KeyValueTable` read the provider.
- **Sub-tabs:** Query, Variables, Extensions (GQL-06), Files (the
  uploads, GQL-07, with the count of rows sent), Connection (the transport
  of each operation type, GQL-35, and the connection params, GQL-33),
  Headers (with the count of enabled
  rows), Auth, Tests and Settings; arrow keys, Home and End move between them, and the
  selection is kept per request in `drafts.subTabs`.
- **Query:** the shared `CodeEditor` with the in-house GraphQL highlighting
  (`language="graphql"`, [frontend.md](frontend.md#code-editor-languages)).
  **Live diagnostics** (GQL-12): `useGraphqlDiagnostics` calls
  `GraphQLService.Diagnostics({key, text})` 300 ms after typing pauses,
  with the schema strip's key (the cached schema; "" for syntax only), and
  again when a schema arrives. Go parses the document and validates it
  against the cached schema (package `validate`, on the parser's partial
  tree, so schema errors show while typing too). Every problem gets a wavy
  underline (`components/editor/diagnostics.ts`: red for errors, amber
  for warnings, the line tinted, the messages on hover); the marks move
  with the text until the next result replaces them, so a fixed problem's
  mark goes with the next check. Under the editor the **problems bar**
  (`GraphqlProblemsBar`) says "Document is valid against
  the schema." (without a schema: "No syntax errors. Fetch the schema to
  check fields, arguments and types too.") or "3 problems" and their list,
  each "Ln 2, Col 5" button moving the cursor there. The result's
  operations feed the **operation picker**.
- **Autocomplete** (GQL-13): `components/editor/graphqlCompletion.ts`, a
  CodeMirror completion source over `GraphQLService.Complete({key, text,
  offset})`, the Go completion engine
  ([graphql-engine.md](graphql-engine.md#completion-graphqlcomplete)) with
  the cached schema, so it works offline; without a schema it offers
  keywords, built-in scalars and directives. It opens by itself after a
  name character, `$`, `@`, `...` or `(`, `{`, `[`, `:`, `,` (and on
  Ctrl-Space), asks Go again on every keystroke (Go filters and ranks, the
  popup keeps that order), and drops an answer the text has moved past.
  The popup shows a kind badge (F field, A argument, I input
  field, T type, E enum value, @ directive, $ variable, … fragment, K
  keyword), the label, the type (`[User!]!`, `Int = 10`, `on User`),
  deprecated items struck through, and the keyboard hints (↑↓ navigate ·
  ↵ accept · esc close); the side panel shows the name and type, the
  description, the deprecation reason and where the item belongs
  ("Query.repository · Argument"). Accepting inserts the item and Go's
  hint (`()` for required arguments, ` {  }` for a composite field, `: `
  for an argument) with the cursor inside it. The schema key reaches the
  source through a `StateField` (`setCompletionSchema`), since a query
  editor's state outlives the component (undo across tab switches). The picker shows only when the document has more
  than one operation: a select of the named operations, saved as
  `operationName`, with "Choose the operation to run" while the saved name
  is not one of them. With one operation there is nothing to pick (Go sends
  its name).
- **Prettify and Minify** (GQL-18): toolbar buttons, and ⇧⌘F
  (`graphql.prettify`) inside the query editor, whose frame is marked
  `data-shortcut-scope="graphql"`, so the chord is not Beautify JSON body
  there. Both call `formatGraphqlQuery` (`commands/graphql.ts`), which
  formats in Go and saves the result as an edit. A query with syntax errors
  is left alone: its first error is marked and the cursor moves there. Prettify keeps comments, Minify drops them. Disabled for a
  blank or read-only query and while Go works.
- **Variables** (GQL-03, GQL-04): a JSON `CodeEditor` saved as text
  (half-typed JSON is kept). `useGraphqlVariablesCheck` calls
  `FormatService.CheckGraphQLVariables(query, variables, operationName)`
  300 ms after the query, the variables or the picked operation change:
  - a JSON syntax error gets the error marker and a status line
    ("Line 1, column 8" jumps there); `{{variables}}` count as values;
  - valid JSON that is not an object (nor blank or `null`) gets "The
    variables must be a JSON object";
  - then the variables are compared with the operation that runs (the only
    one, or the picked one; nothing is compared while several operations
    have none picked). A list under the editor warns about each variable
    the operation declares but the object lacks, in red when it is
    required (non-null without a default: the server refuses the
    operation), in amber otherwise, and about each member the operation
    does not declare, with its position (click to jump to the member
    name). Blank text and `null` count as `{}`. A member whose name is a
    `{{variable}}` is never unknown, and while there is one only required
    variables are reported missing.
- **Connection:** the **Transport** of queries, mutations and
  subscriptions (GQL-35): HTTP, WebSocket (graphql-transport-ws),
  WebSocket legacy (subscriptions-transport-ws) or Server-Sent Events;
  subscriptions cannot pick HTTP. Saved in `settings.graphqlTransport`,
  the defaults (HTTP, and graphql-transport-ws for subscriptions) as
  nothing ([graphql-subscriptions.md](graphql-subscriptions.md#transport-per-operation-gql-35)).
  Under it the connection params (GQL-33), a JSON `CodeEditor`
  saved as text (`connectionParams`), sent as the `connection_init`
  payload when an operation runs over WebSocket, e.g.
  `{"authToken": "{{token}}"}`. Go's JSON check marks a syntax error; a
  value that is not an object gets a warning. HTTP and SSE ignore it.
- **Headers:** the automatic headers, read-only above the table:
  `Content-Type: application/json` (POST only; `multipart/form-data` with
  files), the Accept value (`text/event-stream` when the operation runs
  over SSE; the incremental one when the query has
  `@defer` or `@stream`: `usesIncrementalDelivery` in
  `lib/graphqlIncremental.ts` skips strings and comments as Go's parser
  does) and, with files, `Apollo-Require-Preflight:
  true` unless turned off, each shown struck through as "overridden" when
  an enabled row of that name exists; then the `KeyValueTable` with header suggestions.
- **Auth** and **Settings** are the HTTP editor's `AuthEditor` and
  `RequestSettings` (auth inheritance, timeout); Settings starts with the
  Schema refresh group ([Schema refresh](#schema-refresh-gql-19)).
- **Read-only** items and projects get the HTTP editor's notice; the mode
  select is disabled and the editors are read-only.

## Docs explorer (GQL-14)

The cached schema can be browsed next to the editor: the **Docs** button
at the end of the schema strip, or ⇧⌘D (`graphql.docs`, bound while a
GraphQL editor is mounted), opens `components/editor/GraphqlDocs` as a
side panel beside the sub-tabs. The choice holds for every GraphQL tab
while the app runs. It reads the schema cache through the key of the
schema strip, so it works offline; without a schema it says so and offers
Fetch schema.

```
GraphqlDocs ─ DocsOverview({key})       ─▶ roots, every type's name and kind, directives
            ─ DocsType({key, name})     ─▶ one type with its members (on each page)
            ─ DocsSearch({key, query})  ─▶ docs.Index over every type and member, best 100
```

- **Start page:** the schema's description, the root types
  (`query: Query`, `mutation: Mutation`, …), **All types** grouped by kind
  (Objects, Interfaces, Unions, Enums, Input objects, Scalars; open at once
  for schemas of up to 200 types, else collapsed with their counts) and
  the directives with their arguments and locations.
- **Type page:** the name with its kind badge (and "query root", `@oneOf`),
  the description, `specifiedBy` URL, Implements / Members or Possible
  types / Implemented by as links; the **fields** with `(…)` when they
  take arguments, their type (the named type is a link: `[`**`User`**`!]!`),
  a **deprecated** badge (the reason in its tooltip and under the
  description) and the description, then each argument's type and default;
  the **input fields** and **enum values** likewise. Descriptions are shown
  as plain text. Lists longer than 200 rows render 200 at a time ("Show
  200 more of 2800"), so a 3000-field root opens at once.
- **Navigation:** every type link pushes a page; **Back** and the
  **breadcrumbs** (Schema › Query › User) go back. A new schema key starts
  over at the start page; a Refresh reloads the pages shown.
- **Search:** "Search types and fields" asks Go 120 ms after typing pauses
  and lists the best 100 hits ("2800 matches, the best 100 shown"): a kind
  badge (T type, F field, I input field, E enum value, A argument),
  `Parent.name` with the matched letters marked (`lib/fuzzy.ts`, only for
  the marks), the type or kind, the first line of the description, and
  deprecated names struck through. ↑↓ move, ↵ or a click opens the hit's
  type scrolled to the member (tinted), Esc clears the search.
  `Type.member` searches the members of matching types (`user.log`,
  `Query.` lists the root fields); introspection types only show for a
  query starting with `__`.
- **Insert:** while the Query tab shows an editable query, each field
  has a **+** button that inserts its name at the cursor (replacing the
  selection) and focuses the editor; the edit is saved like typing. Next
  to it, a code button writes a whole operation or fragment for the
  field, and type pages have a **Fragment** button, to the **Depth** in
  the panel's header ([Schema actions](#schema-actions-gql-15-gql-16-gql-17)).

`GraphQLService.DocsOverview({key})` returns `DocsOverviewDTO{schema,
description, roots, types, directives}`: `roots` are `{operation, type}`
query first, `types` are `{name, kind}` sorted by name without the
introspection types (the members come per type, so a large schema is
never sent whole), `directives` `{name, description, locations, args,
repeatable}`. `DocsType({key, name})` returns `DocsTypeDTO{found, name,
kind, description, root, specifiedByUrl, isOneOf, interfaces,
possibleTypes, implementations, fields, inputFields, enumValues}`; fields
are `{name, description, args, type, typeName, typeKind, deprecated,
deprecationReason}`, arguments and input fields `{name, description, type,
typeName, typeKind, defaultValue, hasDefault, deprecated,
deprecationReason}` (`type` as written, `typeName` the named type to
open). Built-in scalars and introspection types are found even when the
server did not list them. `DocsSearch({key, query, limit})` returns
`DocsSearchDTO{schema, items, count}`, hits `{kind, name, type, member,
parent, detail, description, deprecated}`, at most `MaxDocsHits` (200).
Without a cached schema all three answer `schema`/`found` false with
empty lists.

The search is `internal/core/graphql/docs`
([graphql-engine.md](graphql-engine.md#docs-search-graphqldocs)): an
index of every type, field, argument, input field and enum value with
lowered names, built on the first search and kept in the service's
memory next to the parsed schema. On a generated schema of 3000 object
types of 60 fields (with arguments), 3000 enums and 3000 input objects
(about 380 000 entries) a search takes about 5 ms on an M2, and the
overview, the 3000-field root and a search together stay under 10 ms
(`TestDocsLargeSchemaIsFast`, `TestSearchLargeSchemaIsFast`,
`BenchmarkSearchLargeSchema`).

## Schema actions (GQL-15, GQL-16, GQL-17)

Three actions work from the cached schema, offline, in Go
(`internal/core/graphql/generate`,
[graphql-engine.md](graphql-engine.md#generated-documents-graphqlgenerate)):

```
query editor ─ Ctrl/Cmd-click, ⌥⌘D ─▶ DocsTarget({key, text, offset})        ─▶ the docs at the page
docs explorer ─ { } button, Fragment ─▶ GenerateOperation({key, kind, type,   ─▶ inserted at the query's end
                                          field, depth, text})
query editor ─ Fill fields, ⌥⌘F ────▶ FillFields({key, text, offset, depth}) ─▶ inserted at the cursor
```

- **Jump to docs** (GQL-15): Ctrl-click (Cmd-click on macOS) on a name in
  the query opens the docs panel at what it refers to: a field (its
  type's page, scrolled to the field and tinted; also for an alias), an
  argument (its field), a type in a type condition or variable
  definition, a fragment spread or definition (its type condition), an
  input object field, an enum value, or a directive and its arguments
  (the start page, scrolled to the directive). Meta fields open their
  type (`__typename` opens String). While the modifier is held, the name
  under the pointer is underlined like a link
  (`components/editor/graphqlDocsLink.ts`). A modified click on blanks or
  punctuation keeps CodeMirror's meaning (another cursor); on a name the
  schema does not know it just places the cursor. ⌥⌘D (Ctrl+Alt+D,
  `graphql.docsAtCursor`, scope `graphql`) does the same for the name at
  the cursor and says so when there is nothing to show. Each jump pushes
  a page on the docs' stack, so Back returns.
- **Operations from the docs** (GQL-16): each field of a root type has a
  code button, "Write a query for user into the query" (a mutation or
  subscription on those roots), and each field of another type "Write a
  fragment for posts into the query" (none for a field with required
  arguments: a fragment cannot declare variables); object, interface and
  union pages have a **Fragment** button for all their fields. The
  operation or fragment is written by Go with a name the query does not
  use yet (`User`, `User2`, `UserFields`), required arguments as
  variables of the operation, and goes at the end of the query after a
  blank line (or replaces a blank query); the cursor moves to it and the
  edit is saved like typing. A document with several operations then
  shows the operation picker.
- **Fill fields** (GQL-17): the **Fill fields** toolbar button, or ⌥⌘F
  (Ctrl+Alt+F, `graphql.fillFields`, scope `graphql`) in the query
  editor, writes the fields of the selection set at the cursor that are
  not selected yet, one per line, indented like its fields. It needs the
  cursor between the braces, not inside a name, arguments, a string or a
  comment; otherwise, when everything is selected, or without a schema, a
  line under the editor says why. The line goes when the query changes.
- **Depth**: operations, fragments and filled fields go **Depth**
  selection sets deep (1 to 5, default 2; 1 is leaf fields only). The
  select is in the query toolbar and in the docs panel's header; it is
  one choice for every GraphQL editor while the app runs. Recursion is
  always cut (a type already selected on the way down is not selected
  again), so a deep setting stays finite; a selection over 1500 fields
  is cut, and a line says so.

The app has no command palette yet (NAV-05); both shortcuts are listed in
the shortcut help (⌘/) under Requests, "In a GraphQL query editor".

`GraphQLService.DocsTarget({key, text, offset})` returns
`DocsTargetDTO{found, kind, type, member, directive, name, from, to}`
(UTF-16). `GenerateOperation({key, kind, type, field, depth, text})`
(`kind` `"operation"` for a field of a root type, `"fragment"` for a
type, `field` "" for all its fields) returns `GeneratedDTO{schema, text,
name, insert, from, to, cursor, truncated, reason, message}`: `insert`
replaces `[from, to)` of `text` and `cursor` is where the definition
starts after the edit. `FillFields({key, text, offset, depth})` returns
`FillFieldsDTO{schema, from, to, text, cursor, type, count, truncated,
reason, message}`. Offsets are UTF-16 code units. Neither rejects for
"nothing to do": `reason` (`unknown-type`, `unknown-field`,
`required-arguments`, `not-in-selection`, `nothing-to-add`) and `message`
say why, and `schema` is false without a cached schema. On the large
generated schema (3000 object types of 60 fields) each call takes a few
ms (`TestSchemaActionsLargeSchema`).

## Response (GQL-05)

A subscription's run shows its live event log instead, whichever of the
send and the subscription started last
([graphql-subscriptions.md](graphql-subscriptions.md#running-subscriptions-gql-33-gql-34)).

The response pane is `HttpResponsePanel` with `graphql` set (`App.tsx` sets
it for a request whose file kind is `graphql`), so status, headers, Raw,
Pretty and search are the HTTP response view's
([frontend.md](frontend.md#request-editor)). On top of that, `ResponseView`
gets a **GraphQL** view, the default for a GraphQL response:

- **Detection** (`readGraphqlResponse` in `lib/graphqlResponse.ts`): the
  body is JSON as Go saw it (`bodyLanguage` `json`, not hex, clipped,
  truncated, invalid or over the pretty-print limit) and a top-level object
  with a `data` member or an `errors` list. Anything else (an HTML error
  page, `{"message": "Not Found"}`, a huge body) opens in Pretty or Raw as
  for HTTP, with no GraphQL view.
- **Reading and printing:** a small JSON reader keeps every string and
  number as received, so the GraphQL view changes nothing but whitespace:
  big integers keep their digits, escapes and member order stay.
  `printGraphqlResponse` indents with two spaces like Go's
  `jsonfmt.Beautify`, so with nothing hidden the text is the Pretty text,
  and it reports the lines of the top-level `data` and `errors` members.
- **Summary:** "No errors", "1 error, partial data", "2 errors, no data",
  "1 error, data is null", green without errors, red with.
- **Errors list** above the JSON: each error's `message`, its `path`
  (`user.friends[0].name`), each valid location as a link "Line 3, column
  5", and an `extensions` summary (its `code`, or its keys).
- **Highlighting** (`components/response/graphqlRegions.ts`): line
  decorations on every line of `data` (a `--ok` bar at the left edge) and
  of `errors` (a `--bad` bar and a faint tint), recomputed when the text
  changes.
- **Hide extensions:** a toggle shown when the response or one of its
  errors has `extensions`. It leaves the top-level `extensions` and each
  error's `extensions` out of the printed JSON; an `extensions` field
  inside `data` is the server's data and stays. Raw is never filtered. The
  choice is kept for the next responses while the pane is open.

**Jump to the query** (`commands/graphqlJump.ts`): a location link calls
`jumpToQueryLocation(stores, id, {line, column})`, which keeps the
location as the request's pending jump, selects the Query sub-tab
(`drafts/setSubTab`) and notifies subscribers. The Query editor calls
`useQueryJump(id, view)`: once it has a view (at once, or when the Query
tab mounts after the switch) it takes the pending jump, puts the cursor
there, scrolls it into view and focuses the editor. Locations are 1-based,
and columns count source characters (code points), so
`queryLocationOffset` counts a character outside the BMP once although it
is two UTF-16 units in CodeMirror; a column past the end of the line is
its end, a line past the end of the query is the query's end.

## Incremental delivery (GQL-08)

A server that supports `@defer` and `@stream` answers such a query with
the initial result first and the deferred fragments and streamed list
items after it, as a `multipart/mixed` body (boundary `-`, each part one
`application/json` payload) or, for some servers (GraphQL Yoga, graphql-sse),
a `text/event-stream` one whose `next` events carry the payloads and
`complete` ends it. PostSonde shows those payloads as they arrive and
merged into the response the server would have sent without the
directives.

```
httpclient.SendTo ─ body bytes ─▶ spool (Raw, Save, history)
                             └──▶ incremental.Collector (Content-Type multipart/mixed or text/event-stream,
                                    │                      GraphQL requests only)
                                    │ split into payloads as the delimiters arrive, timed from the send,
                                    │ merged by incremental.Result
                                    ▼
          throttle (100 ms) ─▶ partial ResponseDTO{incremental.receiving: true} ─▶ http:op:data
          end of body       ─▶ final ResponseDTO{incremental.receiving: false}  ─▶ http:op:data, done
```

- **Reading** (`internal/core/graphql/incremental`): the multipart parser
  takes the boundary from the Content-Type (`-` when it names none) and
  hands a part out as soon as the delimiter after it is in (servers send
  `\r\n---` right after each payload, so nothing waits for the next
  part; `mime/multipart` would). Header lines are skipped, CRLF or LF
  line ends, a preamble, empty parts and the epilogue are allowed, and a
  last part without a delimiter is taken at the end of the body. Event
  streams go through the SSE parser (`realtime/sse`): `next` and unnamed
  events are payloads, `complete` ends the stream. A payload that is not
  a JSON object is listed with a problem and not merged.
- **Merging** (`incremental.Result`, both formats):
  - older (Apollo's `deferSpec=20220824`, graphql-js 17 alpha 2, and the
    first experiments with `path` at the top level): an entry with
    `path` and `data` is a deferred fragment, deep-merged into the object
    at `path`; one with `items` is a stream, whose `path` ends with the
    index of the first item, where the items are put;
  - newer (graphql-js 17 alpha 3 and later): `pending` announces
    `{id, path, label}`; `incremental` entries name an `id`, with `data`
    merged at the pending path plus `subPath`, or `items` appended to the
    list at the pending path; `completed` ends an id, with its `errors`.
  - The first payload is the initial result. `errors` of every payload and
    entry are appended, `extensions` members replace those of the same
    name, `hasNext` is tracked (`hasNext: false` completes the response).
    Objects merge member by member, lists by index. Data under a `null`
    (an error nulled the parent), an unknown id or a path that is not in
    the result is not merged: the entry gets a `problem`. The merged JSON
    keeps member order, number digits and string escapes as received
    (a small order-keeping JSON value, not `encoding/json` maps).
- **DTO**: `ResponseDTO.incremental` is `IncrementalDTO{format, receiving,
  parts, partCount, merged, mergedTooLarge, mergedSize, hasNext,
  completed, pending, errors}`, null for any other response. Each part is
  `{index, atMs, text, clipped, size, entries, hasNext, problem}`: `atMs`
  from the start of the send (fractions kept), `text` the payload
  two-space indented, `entries` what it did (`{kind: initial | defer |
  stream | pending | completed, path ("user.friends[2]"), label, id,
  items, errors, problem}`). Limits: 1000 parts listed (all counted),
  64 KB of text per part and 2 MB for all of them (then `clipped`), and a
  merged result over 4 MB is left out (`mergedTooLarge`). The body itself
  is kept as for any response, so Raw, Save and the history have it.
- **Live**: while the body arrives, `Send` emits partial responses (the
  head, the time and the body bytes so far (decompressed), `incremental` with `receiving: true`;
  no body, cookies, timeline or tests) after new parts, at most one per
  100 ms (`PartialInterval`; a part inside the interval goes out at its
  end), then the final response. The UI keeps the last one it got
  (`commands/httpSends.ts` already does), so nothing else changes on the
  event side ([events.md](events.md)). A cancelled send ends as usual;
  no partial response follows the done event. Repeat runs merge too but
  send no partial responses.
- **Only GraphQL requests** are read this way; an HTTP request answered
  with `multipart/mixed` keeps the plain body view. A GraphQL server that
  answers incrementally without being asked is read all the same.

**The view** (`components/response/IncrementalBody.tsx`): an incremental
response opens in the GraphQL view (Pretty is not offered for a
multipart body, Raw shows it as received) with a **Merged / Parts**
switch next to Hide extensions; the choice holds for the next responses
while the pane is open.

- A summary line: "Receiving… 2 parts", "4 parts in 225 ms, complete" (red
  with errors), "3 parts, ended before hasNext: false" (amber: the body
  ended without the last payload), with the pending entries and errors.
- **Merged**: the merged result in the GraphQL view of GQL-05 (summary,
  errors list with locations that jump to the query, `data`/`errors`
  marks, Hide extensions); as plain JSON when it has neither `data` nor
  `errors`; a note when it is too large.
- **Parts**: one row per payload, oldest first: "Part 2", its time from
  the send ("8.3 ms", "213 ms", "1.25 s"), the time since the part before
  ("+204 ms"), its size, `hasNext`, what it did ("Initial result",
  "Deferred data at user (Details)", "2 streamed items at user.friends, 1
  error", "Pending: user.friends", "Completed: user"), problems in amber,
  then its JSON. While parts arrive the list follows the newest one
  unless it was scrolled up.
- **While receiving**, `HttpResponsePanel` shows the response under a
  strip with a spinner, "Receiving parts…", the elapsed time and Cancel,
  instead of the progress screen; the partial and final responses render
  the same tree, so the view keeps its switches. The body view picked
  while receiving (GraphQL or Raw) also holds for the send's final
  response.

`GraphQLService.Diagnostics({key, text})` returns `DiagnosticsDTO{schema,
operations, diagnostics, count, syntaxCount}`: `schema` says whether a
cached schema was used (an unknown or empty key checks syntax only);
`operations` are `{name, type}` in source order (also those the parser
recovered from a document with errors); `diagnostics` are `{source
("syntax" or "schema"), code (graphql.syntax.<kind> or
graphql.validate.<rule>), severity, offset, endOffset, line, column,
message}` sorted by position, offsets and column in UTF-16 code units, at
most 200 (`count` has the total). The parsed schema comes from the
service's memory, so a check never re-parses it.

`GraphQLService.Complete({key, text, offset})` returns
`CompletionDTO{context, from, to, prefix, parentType, owner, expectedType,
schema, items, count}` for the UTF-16 cursor `offset`: `from`/`to` is the
range an item replaces (UTF-16), `items` are `{label, kind, detail,
documentation, deprecated, deprecationReason, insertText, suffix,
suffixCursor}`, best first, at most 300 (`count` has the total). It is
`complete.Complete` with the offsets converted; it takes well under the
50 ms budget per request on a large schema
(`TestCompleteLargeSchemaIsFast`: 3000 object types of 60 fields, 3000
enums and input objects, a 60-operation document; median about 0.4 ms on
an M2, `BenchmarkCompleteLargeSchema` about 3.5 ms per call including
the 300-item cap on the 3000-field root).

`FormatService.CheckGraphQL(text)` (syntax only, `GraphQLCheckDTO{valid,
operations, error, errorCount}`) is still bound, but the editor uses
`Diagnostics`.

`CheckGraphQLVariables(query, variables, operationName)` returns
`GraphQLVariablesCheckDTO{error, notObject, resolved, operation, declared,
missing, unknown}`: `error` is the JSON error as for `ValidateJSON`;
`declared` and `missing` are `{name, type, required}` (`type` as written,
without whitespace); `unknown` are `{name, offset, endOffset, line,
column}` with the member name's range in UTF-16 code units. The logic is
`request.CheckGraphQLVariables` (`internal/core/request/graphqlvars.go`),
which reads the object's members with `jsonfmt.TopLevelKeys`.

## Tests

- `internal/core/request/graphql_test.go`: POST body, headers and auth;
  a `null` variables text and an Accept row; GET query string, fragments
  and the mutation warning; the error codes; operations and the
  operationName rule.
- `internal/app/services/http/http_test.go` `TestSendGraphQL`: a test
  server receives the POST body and the GET query string, extensions
  included, with environment variables rendered.
- `internal/app/services/http/graphqlupload_test.go`
  `TestSendGraphQLUploads`: a test server parses a multipart request
  (operations, map, then the files in order): the variables with `null` at
  each path (a single file, a list created in the variables, a nested
  path, a path without `variables.`), the extensions, the map with a file
  bound twice sent once, filenames, Content-Types and contents, and the
  preflight header (on, overridden by a row, omitted).
- `internal/core/request/graphqlupload_test.go`: extensions in POST and
  GET, `GraphQLUploadPath`, `setNull`, the multipart body with a relative
  path under the project root and big numbers kept, and every upload
  error; `graphqlws_test.go`: extensions in the subscribe payload.
  `internal/app/services/collection/graphql_request_test.go`: extensions
  and files saved and read back through auto-save, left out when empty;
  the `graphql_upload` file-format example.
- `internal/app/services/collection/send_test.go`: `LoadHTTPSource` returns
  GraphQL requests and refuses other kinds.
- `internal/app/services/graphql/graphql_test.go`: a test server
  introspected through the real HTTP service and guarded dialer; the
  schema reused by a fresh service with the server closed (a restart
  offline); Refresh replacing the cache; the fallback to the
  conservative query; HTTP status, non-schema and non-JSON failures that
  leave the cache alone; `change` new, changed and unchanged across
  refreshes. `internal/core/graphql/schemacache`: save, load,
  bad files. `internal/app/services/http/graphql_test.go`: the endpoint
  and the introspection request.
- `internal/app/services/graphql/schemafile_test.go`: a schema file in the
  project serves `SchemaInfo`, diagnostics, completion and the docs
  explorer; a change on disk is picked up; Reload re-reads; type system
  problems (usable schema) and syntax errors (none) with positions; a
  missing file; a relative path without a project; `PickSchemaFile`
  inside and outside the project and cancelled.
  `GraphqlEditor.test.tsx` "schema from a file": Load from file stores the
  path and the strip and diagnostics switch to the file, a cancelled
  dialog, Reload with the problems, Use introspection, the error, and
  read-only requests.
- Schema refresh and SDL export (GQL-19, GQL-20):
  `internal/app/services/graphql/export_test.go` fetches a schema by
  introspection, saves it through the save dialog and loads the file back
  as a request's schema file: no problems, the same SDL, the docs
  explorer on it; a schema file's own name suggested, a cancelled dialog,
  no schema, an unwritable path, `SchemaFileName`.
  `internal/core/graphql/schema/print_test.go`: the round trip (see
  [graphql-engine.md](graphql-engine.md#sdl-export-gql-20)).
  `internal/app/services/collection/graphql_request_test.go`
  `TestGraphQLRequestSchemaRefresh`: the setting saved with unknown
  members and left out when off. `lib/graphqlSchemaRefresh.test.ts`: the
  setting and the change signal (validation errors without data; not
  execution errors, partial data, auth or syntax errors).
  `commands/useGraphqlSchemaRefresh.test.tsx`: nothing while off, the
  interval (not while hidden, not after unmounting), no refresh of a file
  schema, the refresh after a rejected send with its 30 s gap, and no
  refresh for data, a query the cached schema rejects, a repeat run or a
  failed send. `GraphqlEditor.test.tsx` "schema refresh and SDL export":
  Save SDL… with the request's name, a cancelled and a failed save, the
  change note, the Settings group (off by default, stored as nothing when
  off, disabled for a file schema), and the refresh after a rejected send
  only when it is on.
- `internal/app/services/graphql/diagnostics_test.go`: syntax only without
  a schema, an unknown field with the cached schema at its UTF-16 range
  after non-ASCII text, nothing once fixed, syntax and schema problems
  together, the cap. `frontend/src/components/editor/diagnostics.test.ts`:
  ranges, mapping through edits, clearing.
- `internal/app/services/graphql/complete_test.go`: fields with types
  and the UTF-16 range, arguments with their hint, enum values with
  deprecation, directives, syntax only without a schema, comments; the
  large-schema timing test and benchmark
  (`go test ./internal/app/services/graphql -run '^$' -bench Complete`).
  `frontend/src/components/editor/graphqlCompletion.test.ts`: the options
  in Go's order, the side panel, when the source asks Go, the popup and
  inserting a pick with its hint.
- `internal/app/services/format/format_test.go` `TestCheckGraphQL`,
  `TestCheckGraphQLVariables`.
- `internal/app/services/graphql/docs_test.go`: the overview (roots,
  sorted types without introspection types, empty lists without a
  schema), types (fields with argument types, interfaces, possible types
  and implementations, deprecated enum values, input objects,
  introspection types, unknown names), search (hits, limit, no schema) and
  the large-schema timing test. `internal/core/graphql/docs/docs_test.go`:
  match tiers, ranking, dotted queries, introspection types, the limit,
  summaries, and the timing test and benchmark on a large schema.
- Schema actions (GQL-15, GQL-16, GQL-17):
  `internal/core/graphql/generate/generate_test.go` (see
  [graphql-engine.md](graphql-engine.md#generated-documents-graphqlgenerate));
  `internal/app/services/graphql/generate_test.go`: the docs target with
  UTF-16 offsets; an operation for every root field and a fragment for
  every composite type of the GitHub subset schema at depths 1 to 3,
  each checked with the editor's own `Diagnostics`; names and insertion
  into a query and a blank one; the reasons; Fill fields with UTF-16
  offsets and again with nothing left; and the three actions on the large
  generated schema, validated and timed. `graphqlDocsLink.test.ts`: the
  modifier, the name at a position, the click handler.
  `GraphqlEditor.test.tsx` "schema actions": an operation from the docs
  at the end of the query, the shared depth, a reason shown; Fill fields
  by button and shortcut; no schema; ⌥⌘D opening the docs at the member.
  `GraphqlDocs.test.tsx`: the code buttons, the Fragment button, the
  depth select, jumps and a directive target.
- `frontend/src/components/editor/GraphqlDocs.test.tsx`: the start page,
  browsing with breadcrumbs and Back, arguments, defaults and
  deprecations, enum values, unknown types, the search (marks, Enter opens
  the member, nothing found, Escape), paged long lists, Insert, and the
  empty state. `frontend/src/lib/graphqlDocs.test.ts`: grouping, type
  references, marks, the page stack. `GraphqlEditor.test.tsx`: the Docs
  toggle and shortcut, inserting into the query at the cursor.
- `internal/core/request/graphqlvars_test.go`: the operation that runs,
  missing and unknown variables, invalid JSON, non-objects, `{{variable}}`
  member names; `internal/core/jsonfmt/keys_test.go`: member names and
  ranges.
- `frontend/src/components/editor/GraphqlEditor.test.tsx`: loading, auto-save
  of every field, the Extensions tab and its check, the Files tab (a path,
  Choose…, Content-Type, a cancelled dialog, the badge, the multipart
  automatic headers, the preflight checkbox, the GET warning), the operation picker, the diagnostics (marks, problems
  bar, jump, validation with the cached schema, marks cleared once fixed),
  completion with the cached schema's key, the variables marker, the variable
  warnings, the schema strip (cached schema, fetch, failure), the automatic
  headers, Send and Cancel, read-only, Prettify and Minify (buttons, the
  scoped shortcut, the marker for an invalid query).
- `frontend/src/lib/graphqlResponse.test.ts`: the JSON reader, detection
  (data only, errors only, both, `data: null`, non-GraphQL JSON and bodies
  not read whole), printing and its regions, hiding extensions, code-point
  columns.
- `frontend/src/components/response/GraphqlBody.test.tsx`: the GraphQL
  view for data, errors, partial data and `data: null`, the marked lines,
  Hide extensions, Raw and Pretty, the fallback for other JSON and for
  non-GraphQL requests.
- `internal/core/graphql/incremental`: `merge_test.go` merges the older
  format (defer into an object and into a list item, stream items at an
  index), the earliest top-level format, the newer format (pending,
  subPath, items appended, completed with errors), errors and
  extensions, data under a null, unknown ids and paths, and keeps
  literals and member order; `collector_test.go` splits multipart bodies
  fed in chunks of 1 to all bytes, LF-only bodies, preambles, empty parts
  and a missing close delimiter, hands a part out before the next one
  starts, reads event streams (`next`, unnamed, `complete`, a part that is
  not JSON), times parts and applies the limits.
- `internal/app/services/http/incremental_test.go`
  `TestSendGraphQLIncremental`: a test server streams four payloads
  (older multipart, newer multipart, newer event stream) and waits after
  each until the test has seen a partial response with it, so each part
  is shown before the next one exists; the Accept header, the final
  merged result, the parts' texts, `hasNext`, entries, and times at least
  the server's pause apart. Also: a REST request answered with multipart
  is left alone, a plain query keeps the usual Accept, cancelling while
  parts arrive, the throttle, and the DTO's JSON (golden).
  `internal/core/request/graphql_test.go`: the incremental Accept for
  `@defer` and `@stream` (not in comments or strings), an Accept row wins.
- `frontend/src/components/response/IncrementalBody.test.tsx`: the merged
  result in the GraphQL view (Hide extensions), the parts with times,
  gaps and entries, Raw, the view kept while partial responses replace
  each other and reset for the next send, an early end, a merged result
  too large, parts not merged, a REST response left alone; in the panel,
  the receiving strip with Cancel while partial responses arrive and the
  final response in the same view. `frontend/src/lib/graphqlIncremental.test.ts`:
  wording, times, the summary and `usesIncrementalDelivery`.
  `GraphqlEditor.test.tsx`: the incremental Accept among the automatic
  headers.
- `frontend/src/components/response/GraphqlJump.test.tsx`: a location link
  opens the Query tab from Variables and puts the cursor on the right
  character (a non-BMP character before it); jumps into an open editor.
