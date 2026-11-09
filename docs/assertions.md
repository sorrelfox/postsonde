# Assertions

`internal/core/assert` evaluates declarative checks against a response
(TEST-02, TEST-03):
rows of *target · operator · expected*, with no scripting; the
`matches_schema` operator validates the target against a JSON Schema
(TEST-07, [jsonschema.md](jsonschema.md)); `EvaluateStream` checks the
messages of a stream (TEST-08, [Streams](#streams)). It is pure core (standard
library plus this module's `jsonpath`, `jsonschema`, `xpath` and `xmldom`
packages). The request editor's Tests sub-tab (TEST-01) edits the
assertions of HTTP and GraphQL requests, stored in the request file
([file-format.md](file-format.md#assertions)); the HTTP service checks them
after every send and the response's Tests tab shows the results (TEST-06):
see [After every send](#after-every-send). The folder runner lists each
request's results too ([runner.md](runner.md)).

```go
results := assert.Evaluate(ctx, assert.Response{
	Status: 201, Header: header, Body: body, Time: elapsed,
}, []assert.Assertion{
	{Target: assert.TargetStatus, Op: assert.OpEquals, Expected: "201"},
	{Target: assert.TargetJSONPath, Expr: "$.items", Op: assert.OpIsType, Expected: "array"},
	{Target: assert.TargetTime, Op: assert.OpLess, Expected: "500ms"},
})
```

| Piece | Purpose |
|---|---|
| `Assertion` | `Target`, `Expr` (header or cookie name, JSONPath, XPath, regex), `Op`, `Expected` (text as typed; JSON tags `target`, `expr`, `op`, `expected`) |
| `Response` | `Status`, `Header` (`http.Header`), `Body`, `Time`, `Size` (0 means `len(Body)`; set it when `Body` is truncated) |
| `Evaluate(ctx, resp, list) []Result` | Every assertion, in order; the body is parsed once per format |
| `EvaluateWith(ctx, resp, list, Options) []Result` | The same with `Options.LoadSchema`, which reads the schema files of `matches_schema` (each schema is read and compiled once per call) |
| `EvaluateOne(ctx, resp, a) Result` | One assertion |
| `Result` | `Outcome`, `Actual` (as shown), `Exists`, `Message`, `Code` (for `invalid`), `SchemaErrors` (for a failed `matches_schema`: `jsonschema.Error`s with their JSONPath) |
| `IsInlineSchema(expected)` | Whether a `matches_schema` expected value is the schema itself or a file path |
| `Read(ctx, resp, []Query) []Reading` | The values of targets without an operator, read as `Evaluate` reads them: `Reading{Label, Exists, Value, Text, Problem, Code, Message}`, for extraction into variables ([extraction.md](extraction.md)) |

`Expected` is used as given: render `{{variables}}` in it (and in `Expr`)
with the request's resolver before evaluating.

## Targets

| `Target` | `Expr` | Value |
|---|---|---|
| `status` | | The status code, a number |
| `header` | Name, case-insensitive | The header's values joined with `", "`; missing when absent |
| `cookie` | Name, exact | The value of that cookie in the response's `Set-Cookie` headers; the last one wins; missing when not set |
| `jsonpath` | RFC 9535 JSONPath ([jsonpath.md](jsonpath.md)) | A **singular** query (`$.a.b`, `$[0]`) gives the value, or missing. Any other query (`$..id`, `$.items[*]`, filters) gives the **array** of selected values, or missing when it selects nothing |
| `xpath` | XPath 1.0 ([xpath.md](xpath.md)), lenient name matching | A node-set gives the string-value of its only node, the array of string-values of several, or missing when empty; `count()`, `sum()`… give a number, comparisons a boolean, `string()` a string |
| `regex` | RE2 regular expression | The first match in the body: its first capture group if the pattern has one, the whole match otherwise; missing without a match |
| `body` | | The whole body as text (use `jsonpath` with `$` for its JSON type) |
| `time` | | Response time in milliseconds (fractions kept: `120.5 ms`) |
| `size` | | Body size in bytes |

Values follow the JSON model of `jsonpath.Decode`: strings, numbers (kept
as written), booleans, null, arrays, objects (member order kept). Headers,
cookies, regex matches, XML text and the body are strings.

A body that is not JSON (for `jsonpath`) or XML (for `xpath`) **fails** the
assertion ("…, but the body is not valid JSON."): the response is wrong, not
the assertion.

## Operators

| `Op` | Passes when |
|---|---|
| `equals`, `not_equals` | Strings: exactly equal (a JSON-quoted expected `"abc"` is unquoted first). Numbers: numerically (`42` = `42.0` = `4.2e1`; exact to 256 bits, so big integers compare right). Booleans and null: `true`, `false`, `null`. Arrays and objects: `Expected` parsed as JSON and compared deeply (array order matters, object member order does not, numbers numerically) |
| `contains`, `not_contains` | Strings (and a number's text): substring. Arrays: an element equals `Expected` (as `equals`). Objects: a member has that name |
| `exists`, `not_exists` | The target is present (an empty header, cookie or `null` JSON value exists). `Expected` is ignored |
| `is_empty` | `""`, `null`, `[]` or `{}`. Numbers and booleans are never empty |
| `gt` `lt` `gte` `lte` | Both sides are numbers; a string target is read as a number when it is one (`"42"` from a header or XML). A target that is not a number fails. For `time`, `Expected` may have a unit: `500ms`, `1.5s`, `2m` |
| `matches` | The RE2 regular expression in `Expected` matches the value's text (arrays and objects as compact JSON). Anchor with `^…$` for a full match |
| `is_type` | The value's type is `Expected`: `string`, `number`, `boolean` (or `bool`), `null`, `array` or `object`, any case |
| `matches_schema` | The value matches the JSON Schema in `Expected` (TEST-07): see [JSON Schema check](#json-schema-check) |

Every operator except `exists` and `not_exists` **fails when the target is
missing**, with "…, but it does not exist." (so `not_equals` on a missing
header fails instead of passing silently).

## JSON Schema check

`matches_schema` validates a target against a JSON Schema with
[`internal/core/jsonschema`](jsonschema.md): draft 2020-12 by default,
draft-07 (and 2019-09, 06, 04) when the schema's `$schema` says so, with
**formats checked** (`date-time`, `email`, `uuid`…; the specification
makes them annotations by default, a test tool should not).

- **`Expected`** is the schema itself when it starts with `{` (or is
  `true` or `false`), else the **path of a schema file**: relative paths
  ("/" separators) are resolved against the project root, like body
  files; absolute paths are used as they are. `{{variables}}` are rendered
  in both, and in the file's path, not in the file's content. The request
  editor stores what the file dialog returns: relative when the file is
  inside the project. The file must be in the project folder or an
  allowed directory of the settings (PRV-10): any other makes the
  assertion `invalid` with `fileaccess.not_allowed`
  ([privacy.md](privacy.md#local-file-access-prv-10)).
- **The value**: on the `body` target, the body parsed as JSON (a body
  that is not JSON fails: "…, but the body is not valid JSON."); on
  `jsonpath`, the selected value (the array of values for a non-singular
  query); on the others, the target's value (`status`, `time`, `size` are
  numbers; headers, cookies, regex matches and XML text are strings). A
  target that does not exist fails.
- **Passed**: "Body matches the inline JSON schema.", "JSONPath $.user
  matches the JSON schema in schemas/user.json."
- **Failed**: the message quotes up to three schema errors with their
  JSONPath: "Expected body to match the inline JSON schema, but 2 errors:
  $ missing required property "email"; $.tags[2] must be string, not
  number." `SchemaErrors` has them all (at most 100), each with its
  JSON Pointer, JSONPath, the keyword's location in the schema and the
  message.
- **Invalid**: no schema (`assert.missing_schema`); a schema that is not
  JSON or not a valid schema (`jsonschema.invalid_schema`), a reference
  outside the schema (`jsonschema.unresolved_ref`), an RE2-incompatible
  pattern (`jsonschema.invalid_pattern`); a validation over the limits
  (`jsonschema.too_complex`, `jsonschema.too_deep`); a file that cannot be
  read (the loader's code: `http.schema_file_missing`,
  `http.schema_file_unreadable` after a send), or no loader at all
  (`assert.schema_file_unavailable`).

## Results and messages

| `Outcome` | Meaning |
|---|---|
| `passed` | `Message`: "Status code equals 200: it was 200." |
| `failed` | `Message` names the target, the expectation and the actual value: "Expected status code to equal 200, but it was 404.", "Expected header X-Id to exist, but it does not exist.", "Expected JSONPath $.id to be of type string, but it was number (42)." |
| `invalid` | The assertion cannot be evaluated; `Code` and `Message` say why |

Values in messages and `Actual`: strings quoted, other values as JSON, the
units `ms` and `bytes` for time and size, cut after 200 bytes with `…`.

| `Code` (invalid) | Cause |
|---|---|
| `assert.unknown_target`, `assert.unknown_operator` | Not one of the lists above |
| `assert.missing_expr` | No header or cookie name, JSONPath, XPath or regex |
| `assert.invalid_regex` | The `regex` target or the `matches` expected value does not compile |
| `assert.invalid_number` | `gt`/`lt`/`gte`/`lte` with an expected value that is not a number (or duration, for `time`) |
| `assert.invalid_type` | `is_type` with another type name |
| `assert.missing_schema` | `matches_schema` with an empty expected value |
| `assert.schema_file_unavailable` | `matches_schema` names a file, but the evaluation has no `Options.LoadSchema` |
| `jsonschema.*` | `matches_schema` with a schema that does not compile, or a validation over its limits ([jsonschema.md](jsonschema.md#errors)) |
| `jsonpath.invalid_query`, `xpath.invalid_query` | The query does not parse (message with the position) |
| `jsonpath.too_complex`, `xpath.too_complex` | Evaluation went over the packages' default work limits |

## After every send

`internal/app/services/http` (`assertions.go`) checks a request's
assertions on each `HTTPService.Send` of an `http` or `graphql` request
(not SSE streams, not GraphQL subscriptions, not replayed webhook requests):

1. **Before the send**, `prepareChecks` renders `{{variables}}` in `expr`
   and `expected` of every enabled assertion with the resolver the request
   is built with (the same scopes and precedence, ENV-06). `expected` of
   `exists` and `not_exists` is not rendered. An undefined variable, a
   cycle or another template problem makes **that assertion** `invalid`
   (code `template.unresolved`, … with the template's message); the send
   itself is not refused, unlike a `{{variable}}` in the URL or headers.
2. **After the response**, `runChecks` evaluates them with one
   `EvaluateWith` call against the **whole body as received** (up to the client's
   `MaxBodyBytes`, not the 1 MiB the response view shows; a truncated body
   sets `Size`), the status, the final response's headers (cookies from
   its `Set-Cookie`) and the total time. `matches_schema` files are read
   by `schemaLoader`: the path resolved against the request's project
   root (`http.schema_file_missing` when it does not exist, or when a
   relative path has no open project), a regular file of at most 8 MiB
   (`http.schema_file_unreadable`).
3. The results go into `ResponseDTO.tests` (`TestResultDTO`: `index`, the
   assertion's position in the request's list counting disabled ones, for
   the jump from the results to the row; `target`, `expr`, `op`,
   `expected` as rendered; `outcome`, `actual`, `exists`, `message`,
   `code`; `schemaErrors`, the `SchemaErrorDTO`s of a failed
   `matches_schema` with `path`, `instancePath`, `schemaPath`, `keyword`
   and `message`, `[]` otherwise). Secret values (the request's and those
   the assertions used) are masked (`••••••`) in `expr`, `expected`,
   `actual`, `message` and the schema errors' paths and messages. A
   response of a request without enabled assertions has `tests: []`.

The Tests sub-tab offers "matches schema" in the operator list. Its row
has two buttons after the expected value: **Choose a JSON Schema file**
(`WorkspaceService.PickSchemaFile`, the path stored like a body file's)
and **Edit the inline JSON schema**, which opens a JSON code editor on the
expected value in a full-width row under the assertion, says whether it
parses, and has a Format button. The response's Tests tab lists a failed
check's schema errors under its message: the JSONPath, the message and
the schema location.

Failures never fail the send: the operation ends `ok` with the response.
The history does not record results yet.

## Streams

Stream requests (TEST-08) have assertions too: `grpc` calls (unary and
every kind of streaming), `websocket` and `socketio` connections and `sse`
streams. `assert.EvaluateStream(ctx, Stream, list, Options)` checks them
against a `Stream`: its `Response` (what assertions without a message
scope read) and its received `Messages`, each a `Message{Body, Header,
Time}`.

```go
results := assert.EvaluateStream(ctx, assert.Stream{
	Response: assert.Response{Status: 0, Header: trailers, Time: elapsed},
	Messages: []assert.Message{{Body: []byte(`{"n":1}`)}, {Body: []byte(`{"n":2,"done":true}`)}},
}, []assert.Assertion{
	{Target: assert.TargetMessageCount, Op: assert.OpGreaterOrEqual, Expected: "1"},
	{Messages: assert.MessagesLast, Target: assert.TargetJSONPath, Expr: "$.done", Op: assert.OpEquals, Expected: "true"},
	{Messages: assert.MessagesAny, Target: assert.TargetBody, Op: assert.OpContains, Expected: "error"},
}, assert.Options{})
```

`Assertion.Messages` is the **message scope**; the target and operator are
the ones above, read from each message as if it were a response whose body
is the message:

| `Messages` | Passes when | Message |
|---|---|---|
| `""` | The stream as a whole satisfies it (`status`, `header`, `time`, `message_count`…) | As for a response |
| `any` | At least one message does | "Message 2's JSONPath $.n equals 2: it was 2." / "Expected some message's … but none of the 3 messages did." |
| `every` | Every message does; **fails without messages** | "Every message's … (3 messages)." / "Expected every message's …, but in message 3 it was false." |
| `none` | No message does (passes without messages) | "No message's … (3 messages)." / "Expected no message's …, but in message 3 it was false." |
| `first`, `last` | That message does; fails when there is none | "The last message's JSONPath $.done equals true: it was true." |
| `nth` | Message `Index` (from 1) does; fails when fewer came | "Expected message 5's …, but there were only 3 messages." |

The target `message_count` (`TargetMessageCount`) is the number of
messages received, a number, for the whole stream only. A message's
`body`, `jsonpath`, `xpath`, `regex` and `size` read its payload, `time`
is when it arrived since the start of the stream, and `header` reads the
message's own fields: an SSE event's `Event` (its type, `message` by
default) and `Id`, a WebSocket message's `Type` (`text` or `binary`), a
Socket.IO event's `Event` (its name). A message that is not JSON fails a
`jsonpath` check on it, as a body does. An assertion that is invalid on a
message (a regular expression that does not compile) is invalid as a
whole. Codes: `assert.not_a_stream` (a message scope or `message_count`
evaluated by `Evaluate`), `assert.unknown_scope`, `assert.invalid_index`
(`nth` under 1), `assert.message_not_kept` (see the log's limits below).

What each protocol checks:

| Request | The stream as a whole | A message |
|---|---|---|
| `grpc` | `status` the gRPC status code (0 OK, 5 NOT_FOUND…), `header` the response headers and trailers with `grpc-status` and `grpc-message`, `body` the last response message, `time` the call | A response message in the protobuf JSON mapping (its bytes when it does not decode) |
| `websocket` | `status` and `header` the upgrade's answer (101 and its headers; a refused upgrade's status), `time` how long it was open | A received message (not the sent ones, pings or pongs) |
| `socketio` | `time` | A received event: its arguments as a JSON array (`$[0].id`), its name as the `Event` header |
| `sse` | `status` and `header` the response (the last connection's after reconnects), `time` the stream | An event: its data, `Event` and `Id` headers |

**When.** `internal/app/streamcheck` records what a stream receives in a
`Log` (the first 10 000 messages and at most 32 MiB of them, the count of
all and the last one; a message past them is `assert.message_not_kept`
for `nth`, and `any`/`every`/`none` say they read only the kept ones) and
evaluates the request's assertions:

- **When the stream ends**: the gRPC call's `end` item, the WebSocket
  connection's last `closed` event (none while auto-reconnect waits to
  connect again), the Socket.IO `closed` item and the SSE `end` item carry
  `tests`, results with the shape of the HTTP `TestResultDTO` plus
  `messages` and `messageIndex` (`streamcheck.TestResultDTO`). Null on the
  other items; `[]` without enabled assertions.
- **On demand**: `GRPCService.CheckAssertions({opId})`,
  `WebSocketService.CheckAssertions({connId})`,
  `SocketIOService.CheckAssertions({opId})` and
  `SSEService.CheckAssertions({opId})` check a running stream, or one of
  the last 16 that ended (`streamcheck.not_found` for another). The UI's
  **Check now**.

The assertions are those of the request as it is then (pending edits
included), rendered with its variables as an HTTP send renders them, secret
values masked in the results (`http.StreamChecks`); an SSE stream's end
uses those rendered when it connected, with the variables it was built
with. A file schema of `matches_schema` is read as after a send. Loading
them can fail (the request was deleted): one invalid result says so. A
failed check never stops a stream.

In a folder run ([runner.md](runner.md)) an SSE request's assertions are
checked against the events read in the stream window, with the run's
variables; failed ones fail its result, and reports list them with the
scope in the expression ("last: $.done"). gRPC, WebSocket and Socket.IO
requests are not run by the runner.

**UI.** The Tests sub-tab of `grpc`, `websocket`, `socketio` and `sse`
requests edits the assertions with a message-scope picker before the
target (Whole stream, Any, Every, No, First, Last message, Message #, with
the number) and the message count among the targets of the whole stream.
The results show in the gRPC and SSE response views' Tests tab and under
the WebSocket and Socket.IO Tests sub-tab (`StreamTests`, with Check now),
like an HTTP response's, each jumping to its row; the rows show their last
result.

## Quick assertions

TEST-04: right-click a value of an HTTP or GraphQL response (or press the
context-menu key) for an assertion on it, added to the end of the
request's Tests sub-tab, which opens on the new row:

- **A JSON value** in the Body tab's Pretty or Raw view (the value under
  the pointer, or at the cursor from the keyboard): "Assert $.user.name
  equals this value" (a string as it is, a number, `true`, `false` or
  `null` as written, an object or array as compact JSON), "… exists" and
  "… is string". The JSONPath is built from the value's position:
  `.name` for identifiers, `['other name']` otherwise, `[n]` for
  indexes (`frontend/src/lib/jsonPathAt.ts`).
- **A header** in the Headers tab: "Assert header Content-Type equals this
  value", "… exists".
- **The status** in the status line: "Assert status code equals 201".

The edit is auto-saved like any other, one undo step; a toast says what
was added. A read-only request offers none. Not offered yet: the GraphQL
view's body, a filtered body, XML values (XPath), gRPC and stream
messages.

## Tests

`assert_test.go` has one table row per target and operator, passing and
failing, with the exact `Actual` and `Message`, and a test that fails when
a combination has no case; plus invalid assertions, wrong body formats,
truncation, `Size` override and result order. `schema_test.go` does the
same for `matches_schema` on every target (inline and file schemas, the
messages and schema errors), its invalid cases, the message limits and
loading each schema once. The HTTP service's `assertions_test.go` sends
requests with inline and file schemas (relative to a project root, with
`{{variables}}` and secrets) and checks the DTO. `stream_test.go` checks
every message scope over message sequences (passing and failing, with the
exact messages), message headers, a truncated log and schemas on
messages; `internal/app/streamcheck` its rendering, masking and the log's
limits. The services' `checks_test.go` run streams against the in-repo
test servers (gRPC server streaming and bidi on `grpctest`, WebSocket on
the echo server, Socket.IO on `siotest`, SSE on `httptest`) and check the
results at the end and on demand; `internal/app/streamchecks_test.go` does
it as the app wires the services, and the runner's `TestRunStreams` runs
an SSE request with assertions. In the frontend, `StreamTests.test.tsx`
and `QuickAssert.test.tsx` cover the scope picker, the results with Check
now and the quick-assertion menus; `jsonPathAt.test.ts` the paths.

## Not handled yet

- Extraction from streams. Extraction into variables (TEST-05) reads its
  values with the same targets (`assert.Read`), from HTTP responses only:
  see [extraction.md](extraction.md).
- Stream results in the request history; gRPC, WebSocket and Socket.IO
  requests in folder runs.
- Response schemas taken from an imported OpenAPI document (the
  `matches_schema` operator takes a schema or a file).
- Results in the request history, and assertions on a body in another
  charset than UTF-8 or with a `Content-Encoding` (the body is checked as
  received).
- XPath namespace maps and strict name matching (the evaluator uses the
  lenient default).
