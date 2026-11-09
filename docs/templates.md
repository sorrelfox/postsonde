# Templates: `{{variables}}`

`internal/core/template` is the one implementation of `{{var}}` syntax. The
editor (highlighting, hover, unresolved markers), every sender (HTTP,
GraphQL, gRPC, sockets), the mock servers and the SSE server (the dynamic
values of its events, [servers.md](servers.md#sse-server)) use it, so a text resolves the
same way everywhere. Standard library only. Templated mock responses
(MOCK-06) render with one scope of the request's values
(`{{request.params.id}}`, `{{request.body.user.name}}`) instead of the
environments: see [mock-routes.md](mock-routes.md#templates).

| Piece | Purpose |
|---|---|
| `Parse(src) []Segment` | Splits text into `Text`, `Var` and `Unterminated` segments with byte offsets. Never fails. |
| `Escape(s)` | Returns a text that renders to exactly `s` (for inserting literal values). |
| `Scope`, `MapScope`, `Vars` | A named set of variables. |
| `Chain` | Scopes from lowest to highest precedence; `Lookup` and `All` (with shadowed bindings). |
| `Resolver.Render(src) Result` | Substitutes variables, expanding nested ones; returns text, refs and problems. |
| `Result.Err()` | `nil`, or an `errs.InvalidArgument` error to return instead of sending. |
| `SplitPath(name)`, `Select(base, value, path)` | Dot notation: the base/path splits of a dotted name, longest base first; the text a path selects from a JSON value. |
| `IsDynamic(name)`, `DynamicNames()`, `DynamicDescription(name)` | Whether a name is a dynamic value with valid arguments (always resolved); the dynamic value names and what each generates, for completion. |
| `ParsePrompt(name)`, `PromptsOf(problems)` | A [prompt](#prompts-at-send-time) reference's label, default and secret flag; the prompts a render left unanswered, one per label. |
| `ParseFile(name)`, `FileReader`, `ErrFilesUnavailable` | A [file contents](#file-contents) reference's path; what reads the file (`Resolver.Files`); the cause of a `{{$file}}` rendered without one. |
| `ParseResponse(name)`, `ResponseReader`, `ErrResponsesUnavailable` | A [response reference](#response-references)'s request, source, expression, `send=` and secret flag; what reads it (`Resolver.Responses`); the cause of a `{{$response}}` rendered without one. |
| `IsPipe(name)`, `FilterNames()`, `FilterDescription(name)` | Whether a name is a valid [filter pipe](#filters-encoding-and-hashing) (`token \| base64`); the filters, for completion. |

## Syntax

| Written | Meaning |
|---|---|
| `{{name}}`, `{{ name }}` | A variable. Whitespace (space, tab, CR, LF) around the name is ignored. Names are case-sensitive and otherwise free-form (`auth.token`, `$uuid`). |
| `{{auth.token}}`, `{{list.0.id}}` | Dot notation: when no scope defines the dotted name itself, a field of the JSON value of `auth` (see [Dot notation](#dot-notation)). |
| `\{{name}}` | The literal text `{{name}}`. |
| `\\{{name}}` | A literal `\` followed by the variable. In a run of backslashes right before `{{`, each pair is one backslash and an odd one left over makes the braces literal. Backslashes anywhere else are plain text. |
| `{"a":{{{x}}}}` | In a run of three or more `{`, the last two open the variable, so JSON around a variable works. The first `}}` closes it. |
| `{{}}`, `{{ }}` | A variable with no name: a blocking problem. |
| `{{name` | Unterminated: no `}}` before the end or before the next `{{`. Sent as literal `{{` and reported as a warning. |

The backslash escape is ENV-14. It affects only backslashes directly in
front of `{{`, so paths and JSON escapes elsewhere are untouched. It works
the same in variable values, and the editor draws no chip for escaped
braces. Nested braces are not a way to build names: `{{base_{{env}}}}` is
an unterminated `{{` followed by `{{env}}`, as it always was; use a
variable whose value refers to others, or dot notation, instead.

## Segments and offsets

Segments cover the source in order without gaps. `Start`/`End` are byte
offsets (`src[Start:End]` is the raw text); a `Var` also has `NameStart`/
`NameEnd` for the name without whitespace. The editor converts byte offsets
to its own positions.

## Scopes and precedence

A `Chain` lists scopes from lowest to highest precedence, and a later scope
wins (ENV-06):

```
global < environment < collection < folder < runtime
```

The order is fixed; nothing configures it. What each level is in a
project (`http.ScopeChain`, built from `collection.VariablesSource`):

| Level | Where it is defined | Scope name (hover) | `Source` |
|---|---|---|---|
| global (ENV-02) | `variables` of `postsonde.json`, edited in the Environments view, "Global variables": the base environment every environment inherits and overrides (ENV-05, [environments.md](environments.md#base-environment)) | `Global` | `global` |
| environment (ENV-01) | the active environment's file | `Environment: <name>` | `environment` |
| collection (ENV-03) | `variables` of a **top-level** folder's `_folder.json` (its Variables tab, "Collection variables") | `Collection: <folder name>` | `collection` |
| folder (ENV-04) | `variables` of each deeper folder's `_folder.json`, from the top down: the folder nearest to the request wins | `Folder: <folder name>` | `folder` |
| runtime (ENV-15) | the open project's runtime variables, set in the Environments view or from responses by extraction rules ([extraction.md](extraction.md)), for the session or persisted in the local directory ([environments.md](environments.md#runtime-variables)) | `Runtime` | `runtime` |

A folder run adds its own variables (`Run`) after the runtime scope, for
the requests of that run only ([runner.md](runner.md#run-variables)):
`global < environment < collection < folder < runtime < run`. A data-driven run's row
(RUN-05) is part of that scope, below the values set during the run
([runner.md](runner.md#data-driven-runs)).

A collection is a top-level folder of the project: the project root has no
`_folder.json`, and its `postsonde.json` holds the global variables, so a
project is one workspace with any number of collections. A request directly
at the top level (or a scratch request) has no collection or folder scope.
Requests have no variables of their own. Disabled variables, and secret ones
without a value on this computer, are left out of their scope, so the name
falls through to the next scope down.

A **local value** (SEC-02, environment and global variables,
[environments.md](environments.md#local-values)) is not a level of its
own: it replaces the variable's shared value inside the variable's scope
(`template.Variable.Local` marks it), so the order above is unchanged and
a nearer scope still wins over a local value. In full: the highest level
that defines the name wins; within it, the local value wins over the
shared value. `TestLocalValuePrecedence` (internal/app/services/http)
checks both rules. Dynamic values (`{{$uuid}}`) come
after every scope: a scope that defines the name wins.

The editor's `VariableDTO` (`HTTPService.Variables`) says which scope a
value comes from (`scope`, `source`) and which lower scopes it shadows
(`overrides`, nearest first, from `Chain.All`; left out when no lower scope
defines the name), so the hover says "Folder: Admins" and "Overrides
Collection: API, Global". `local` is set when the value is a local one;
the hover then says "Environment: dev · local value". Tests:
`TestPrecedenceAcrossEveryLevel` (internal/app/services/http) defines one
name at every level and removes the levels one by one.

A scope has a `Name()` shown in hover ("Environment: dev") and `Lookup`.
Variables may be `Secret`; the flag carries over to every reference whose
value includes a secret, and to `Result.Secret`. `Lookup` is called once
per reference. Response references are not a scope: `Resolver.Responses`
reads them ([Response references](#response-references)).
Prompts are not a scope: their answers come with the send
([Prompts at send time](#prompts-at-send-time)).

## Rendering

- Every reference becomes a `Ref`: name, offsets, `Resolved`, the `Scope`
  that supplied the value, the rendered `Value`, `Secret`, and `Nested` refs
  found in the raw value (offsets relative to that value).
- **Variables inside values** are rendered with the same chain (ENV-13):
  `base = {{scheme}}://{{host}}`. Escapes in values apply too.
- **Unresolved** references stay in `Result.Text` as written, for previews,
  and are listed in `Result.Problems` with a `Path` (`url → path → missing`)
  and the offsets of the top-level reference that led to them.
- **Cycles** (`a → b → a`) are reported, not followed. The same name used
  twice side by side is not a cycle.
- **Limits** (zero means default): `MaxDepth` 10 levels of nesting,
  `MaxExpansions` 10,000 lookups per render, `MaxLength` 64 MiB for any value
  grown by substitution. Exceeding one is a blocking problem. The same
  problem reached again through repeated references is recorded once.

| Problem kind | Blocking | `Err()` code |
|---|---|---|
| `unresolved` | yes | `template.unresolved` (lists all names, `details.names`) |
| `cycle` | yes | `template.cycle` (`details.path`) |
| `too_deep` | yes | `template.too_deep` (`details.path`) |
| `empty_name` | yes | `template.empty_name` |
| `limit` | yes | `template.limit` |
| `invalid_dynamic` | yes | `template.invalid_dynamic` (`Problem.Message` says why) |
| `invalid_filter` | yes | `template.invalid_filter` (`Problem.Message` says why) |
| `prompt` | yes | `template.prompt` (`details.prompts`: what to ask for, see [Prompts](#prompts-at-send-time)) |
| `file` | yes | the reader's error as it is, `fileaccess.not_allowed` and so on, or `template.file_binary`, `template.file_unavailable` (`details.reference`, see [File contents](#file-contents)) |
| `response` | yes | the reader's error as it is, `http.response_not_sent`, `extract.not_found` and so on, or `template.response_unavailable` (`details.reference`, see [Response references](#response-references)) |
| `unterminated` | no | none |

Unresolved names take priority in `Err()`; otherwise the first blocking
problem decides. A single unresolved dotted reference whose base is defined
says which step failed: *The variable {{auth.user.email}} cannot be
resolved: {{auth.user}} has no field "email".* `details.problems` counts the blocking problems. Messages
contain names, never values.

**Senders must call `Result.Err()` and refuse to send when it is not nil.**
The one exception is an explicit user choice: the HTTP sender collects the
`unresolved` problems of every field, refuses with one
`template.unresolved` error, and sends the references as written only when
the user answered "Send anyway" (`allowUnresolved`, ENV-11,
[http-client.md](http-client.md#sending-from-the-app)). The WebSocket
service does the same for the URL, enabled header rows and subprotocols of
a connect ("Connect anyway") and for each message it sends (WS-11,
[websocket.md](websocket.md#variables)). The HTTP sender also asks for
[prompted values](#prompts-at-send-time) (`template.prompt`) before it
reports undefined names. Other blocking problems always refuse.

Every sender renders with the same chain: `http.ScopeChain(src)` builds it
from a `collection.VariablesSource` (the project's global variables as the
collection tree holds `postsonde.json`, the active environment read on each
call, the collection, then the folders from the top down, their pending
edits included), which `collection.LoadVariablesSource` returns for a
request of any kind; `http.RuntimeScope` then adds the project's runtime
variables (a snapshot of `runtimevars.Store`, taken per render).

## Dot notation

ENV-13. A reference with dots that no scope defines by its full name reads
a field of a variable's JSON value:

```
auth = {"token": "t0k", "user": {"id": 7, "tags": ["a", "b"]}, "on": true}

{{auth.token}}        t0k
{{auth.user.id}}      7
{{auth.user.tags.1}}  b
{{auth.user.tags}}    ["a","b"]
{{auth.on}}           true
```

- **Lookup order.** The full name first (`auth.token` defined as a
  variable wins, so the flat names the Insomnia importer creates keep
  working), then a dynamic value, then the longest defined prefix as the
  base: with `api.v2` and `api` both defined, `{{api.v2.url}}` reads
  `url` from `api.v2`. A name with whitespace (a dynamic value with
  arguments) or an empty part (`a..b`, `a.`) is never split.
- **The base** is resolved like any reference, from the same chain:
  variables inside its value are expanded first, escapes apply, and a
  cycle through a path (`loop = {"self": "{{loop.self}}"}`) is a `cycle`
  problem. Then its rendered value is parsed as JSON (leading and trailing
  whitespace allowed).
- **The path** is object keys (exact, case-sensitive) and array indexes
  (decimal from 0, no sign or leading zeros): `{{list.0.id}}`. A key with
  a dot in it cannot be reached.
- **The result**: a string is its text; a number, `true`, `false`, `null`,
  an object or an array is compact JSON as written (`1.50` stays `1.50`,
  keys keep their order).
- **Scope and secrecy** are the base's: `Ref.Base` names it, `Ref.Scope`
  is its scope, `Ref.Nested` its references, and the value is secret when
  anything in the base is, even a field without the secret in it.
- **Failure** (the base is not JSON, a field or element is missing, a step
  goes into a scalar) is an `unresolved` problem for the dotted name, with
  a `Problem.Message` such as `{{auth.user}} has no field "email"` (never
  a value). Being `unresolved`, it takes part in "Send anyway" like an
  undefined name. An undefined base is a plain undefined name.

`Chain.Names()` lists the names defined by the scopes that implement
`Lister` (`MapScope` does; dynamic scopes need not), sorted and without
duplicates, for the editor.

## Editor

The editor never resolves variables itself: `HTTPService.Variables(id)`
(for a request of any kind; the app sets `http.WithVariablesLoader`)
renders every name of a request's chain in Go and returns its value
(masked when secret), whether it resolves and its scope. The frontend then
only has to find the references (`lib/templateSyntax.ts`, a TypeScript
mirror of `Parse`) and look their names up, so a reference is red in the
editor exactly when a send would refuse it. Dynamic values are listed
under their names (`$uuid`, source `dynamic`, a description as the value);
a reference with arguments (`{{$randomInt 1 6}}`) is looked up by its first
word. Two edge cases differ from a send: invalid arguments are not red
(the send refuses with `template.invalid_dynamic`), and when a scope
defines a dynamic name such as `$timestamp`, `{{$timestamp +1d}}` is red
although the send generates it. A dotted name not in the list is looked
up as at send time ([Dot notation](#dot-notation)) by `lookupPath` in
`lib/templateVariables.ts`, from the base's listed value; the hover says
which step failed in Go's words. Two differences: a secret base's value is
not sent to the editor, so a path into it counts as resolved (the send
checks it), and numbers and objects are shown as JavaScript formats them
(`1.50` shows as `1.5`), while the send uses them as written. A
[filter pipe](#filters-encoding-and-hashing) is read by
`lib/templateFilters.ts` exactly as Go reads it (both run
`testdata/pipes.json`, and the filter table is checked against
`testdata/filters.json.golden`): it is red when a filter is unknown or has
bad arguments, or when its input or a key is; its hover shows the input's
scope, *sha256 of {{body}}, computed when sent* as the value (Go computes
it at send time), and a mask when the input or a key is secret. See
[frontend.md](frontend.md#template-variables).

**Hover.** In an HTTP, SSE or GraphQL request's editor the hover ends
with **Where does it come from?**, which opens the request's Variables
sub-tab on that name (the [resolution view](#resolution-view)).

**Completion.** Typing inside `{{` in any request field opens a list
(`lib/templateCompletion.ts`): the request's variables with their scope and
value (masked when secret), then every dynamic value with its description,
so the fake data and timestamp values are discoverable; after a `|` it
lists the filters. After `{{$response ` it lists the project's requests by
name and inserts the picked one's ID, then the sources, then `send=` and
`secret` ([Response references](#response-references)). The GraphQL query and gRPC message editors keep their
own completion instead.

## Resolution view

DBG-08, ENV-12. The **Variables** sub-tab of an HTTP or SSE
request (`ResolutionPanel`) lists every `{{reference}}` the request uses,
from `HTTPService.Resolution(id)` (`internal/app/services/http/resolution.go`):

- **Not a second resolver.** `Resolution` builds the request with `build`,
  the function `Send` builds it with: the same `ScopeChain` and
  `RuntimeScope`, the same `template.Resolver`, the same `ExpandURL`
  (a relative URL's `baseUrl` is listed). The expansion records the
  `template.Result` of every render it makes (`expansion.record`), and
  the view lists their `Ref`s, so it shows exactly what the build
  substituted. It builds like the header preview: undefined names and
  prompts are left as written and listed, no file is read, no token is
  asked for. A problem that stops every build (a loop, an invalid
  dynamic value) is the error, as for Send.
- **Per reference** (`ResolvedVariableDTO`): the name as written; its kind
  (`variable`, `path` for dot notation with its `base`, `dynamic`,
  `prompt`, `file`, `filter`, `response` for a
  [response reference](#response-references), with the stored value and
  a `description` of its `send=`); the value sent (`""` for a secret, which
  shows `••••••`, and for a value made at send time: a dynamic value, a
  prompt, a file, or a variable or pipe that uses one is `generated`,
  with the dynamic value's description); the winning `scope` and `source`
  (`global`, `environment`, `collection`, `folder`, `runtime`, `run`,
  `dynamic`), `local` for a local value; `scopes`, every scope that
  defines the name (for a dotted reference, its base) lowest precedence
  first, each with its value as defined (unrendered, `""` when secret),
  `local` and `wins`; `count`, how many times the request's fields use
  it; `via`, for a name only used inside other variables' values, which
  ones (`base = {{scheme}}://{{host}}` lists `scheme` via `base`); and for
  a reference that does not resolve, `problem` (the `template.ProblemKind`:
  `unresolved`, `prompt`, `file`, ...) and Go's `reason`. Plus the active
  environment's name and the URL as sent, secrets masked.
- **A folder run's own variables.** `RunSession.Resolution(ctx, id,
  RunOptions)` resolves a request of a run as `RunSession.Send` builds
  it, with the `Run` scope (`source: "run"`) above the runtime one.
- **UI.** A table: the reference (red when it does not resolve, its kind
  and `via` under it), the value sent, where it comes from ("· local
  value"), and the scopes that define it with the one sent marked and the
  others struck through; secrets are masked everywhere. It loads when
  the sub-tab opens and again 200 ms after an edit (once the edit reached
  Go), after a send, and after every `environments:changed` and
  `runtimevars:changed` (`commands/useResolution.ts`). The chip hover's
  **Where does it come from?** sends `postsonde:show-resolution`
  (`lib/resolution.ts`) from the editor; `RequestEditor`, marked
  `data-resolution-host`, opens the sub-tab and marks and scrolls to that
  row. Editors without the sub-tab (GraphQL, WebSocket, Socket.IO, the
  folder view) show no link.
- **Tests.** `TestResolutionMatchesTheSend` (internal/app/services/http)
  defines names won by every scope (global with a local value and a
  secret, environment with a local value, collection, folder, runtime
  with a secret, and a run's own variables), a nested value, a dotted
  reference, a filter pipe and a dynamic value, sends the request to a
  test server that echoes them, and checks that the resolution's value
  and scope of each is what the server got and what the hover
  (`Variables`) says, for a single send and for a run
  (`RunSession.Resolution` against `RunSession.Send`).
  `TestResolutionOfWhatCannotBeSentYet` covers undefined names, prompts,
  files and a loop. `ResolutionPanel.test.tsx`, `RequestEditor.test.tsx`
  and `templateVariables.test.ts` cover the UI.

## Dynamic values

A name that starts with `$` and that no scope defines can be a dynamic
value (DYN-01 to DYN-03). It is generated afresh for **every reference**
(two `{{$uuid}}` in one request differ) at send time, by the same
`Resolver.Render` the senders already call, so no sender needs changes.
Arguments follow the name, separated by whitespace; an argument with
spaces is written in double quotes. A dynamic reference resolves with
`Ref.Scope` = `"Dynamic"` (`DynamicScope`), is never secret, and its value
is used as is (never parsed for variables). `$prompt` is the exception:
its value is typed by the user ([Prompts](#prompts-at-send-time)). It also works inside variable
values: `request_id = {{$uuid}}`.

| Written | Value |
|---|---|
| `{{$uuid}}` (also `$guid`, `$randomUUID`) | A random version 4 UUID, lower case: `3f0c…-4…-[89ab]…` |
| `{{$timestamp}}` | Unix time in seconds |
| `{{$timestampMs}}` | Unix time in milliseconds |
| `{{$isoTimestamp}}` | ISO 8601 in UTC with milliseconds: `2026-09-19T14:05:09.123Z` |
| `{{$datetime}}`, `{{$datetime iso8601}}` | Same as `$isoTimestamp` |
| `{{$datetime FORMAT}}` | UTC in a custom format: `{{$datetime YYYY-MM-DD}}`, `{{$datetime "DD/MM/YYYY HH:mm"}}` |
| `{{$localDatetime FORMAT}}` | The same in the computer's time zone |
| `{{$randomInt}}` | A random integer from 0 to 1000 |
| `{{$randomInt MIN MAX}}` | A random integer from MIN to MAX, both included (any `int64`s, MIN ≤ MAX) |
| `{{$randomString}}`, `{{$randomString N}}` | N (default 16, at most 4096) random letters and digits `[A-Za-z0-9]` |
| `{{$randomHex}}`, `{{$randomHex N}}` | N (default 16, at most 4096) random lower-case hex digits |
| `{{$randomBoolean}}` | `true` or `false` |

### Prompts at send time

DYN-07. `{{$prompt LABEL [DEFAULT] [secret]}}` is a value the user types
when the request is sent:

```text
Authorization: Bearer {{$prompt "API token" secret}}
/users/{{$prompt "User id" 42}}
{{$prompt}}                         label "Value", no default
```

- **Arguments** follow the existing syntax: separated by whitespace, one
  with spaces in double quotes. The first is the label; then an optional
  default (filled in the dialog), and the bare word `secret` anywhere
  after the label (`"secret"` in quotes is a default). More arguments, an
  empty label or an unclosed quote are an `invalid_dynamic` problem.
- **The resolver never asks.** `Resolver.Prompts` holds the answers by
  label; a reference whose label has an answer resolves to it (`Ref.Scope`
  `Prompt`, `PromptScope`), and one without is a blocking `prompt` problem,
  left as written. `Err()` (`template.prompt`, after undefined names) sets
  `details.prompts` to a JSON array of `{label, default, secret}`, one per
  label in the order met (`PromptsOf`): a label marked secret anywhere is
  secret, the first non-empty default wins. The same label used twice, in
  any field or inside variable values, shares one answer.
- **Secrecy.** A `secret` prompt is `Ref.Secret`, so the value is masked
  in the timeline and the history like a secret variable's. A prompt
  without `secret` is sent and recorded in the history as sent, like a
  typed value; mark it `secret` to keep it out of the history.
- **Never stored.** Answers exist only in the send's input (the dialog's
  state, then `SendInputDTO.prompts` or `RepeatInputDTO.prompts`) and the
  resolver; nothing writes them to the project, environment or local
  files.
- **Where it asks.** HTTP and GraphQL-over-HTTP sends (`HTTPService.Send`)
  and repeat runs (`HTTPService.Repeat`, asked once, reused for every send
  of the run): the service collects the unanswered prompts of every field
  and refuses with one `template.prompt` error before anything else; the
  UI shows one dialog with all the fields (`PromptHost`), then sends again
  with the answers; Cancel aborts the send
  ([http-client.md](http-client.md#sending-from-the-app),
  [frontend.md](frontend.md#template-variables)). Previews that send
  nothing (Copy as curl, the header preview, HAR export, the JWT preview,
  the GraphQL endpoint) leave the reference as written and list it with
  the undefined names. SSE streams, GraphQL subscriptions, the GraphQL
  schema fetch (introspection), WebSocket, Socket.IO, TCP, gRPC and the
  mock servers do not ask: a prompt there refuses with `template.prompt`
  (its hint says to use a variable). They could ask the same way later:
  the error carries everything the dialog needs.
- The editor lists `$prompt` with the dynamic values, so a reference is
  never red; completion offers it.

### File contents

DYN-08. `{{$file PATH}}` is the text of a local file, read when the
request is sent:

```text
{{$file fixtures/user.json}}             relative to the project folder
{{$file ~/api/token.txt}}                in the home folder
{{$file "/Users/ann/My Data/body.xml"}}  a path with spaces, in double quotes
{{$file img/logo.png | base64}}          binary content through a filter
```

- **One argument**, the path, following the dynamic value syntax (in
  double quotes when it has spaces or a `|`). An unquoted `|` ends the
  path, so `{{$file logo.png|base64}}` is a pipe. No path, more than one,
  or an unclosed quote is an `invalid_dynamic` problem.
- **Which files.** Absolute, `~/…`, or relative to the open project's
  folder (a relative path without an open project is refused). The file
  must be in the project folder or in an allowed directory of the
  settings (`privacy.allowedDirs`, PRV-10), with links and `..` followed
  on disk first; see [privacy.md](privacy.md#local-file-access-prv-10).
  The template package does not decide this: `Resolver.Files` (a
  `FileReader`) reads the file, and the app passes a
  `fileaccess.Policy`.
- **As is.** The bytes are inserted unchanged (UTF-8, line endings and a
  final newline kept) and never parsed for variables: `{{` inside the file
  is sent as written. The reference is not secret (`Ref.Scope` `File`),
  so the content is recorded in the history like typed text.
- **Binary.** Content that is not valid UTF-8, or has a NUL byte, is
  refused (`template.file_binary`, with a hint showing the `| base64`
  form), unless the reference is itself the input of a
  [filter pipe](#filters-encoding-and-hashing), which turns it into text:
  `{{$file logo.png | base64}}`, `{{$file key.der | sha256}}`. A variable
  whose value holds `{{$file}}` does not count as that input.
- **Limits.** At most 10 MiB per file (`fileaccess.too_large`); the
  rendered text stays under `MaxLength`. Each reference reads the file
  afresh.
- **Problems.** A file that cannot be inserted is a blocking `file`
  problem, left as written; `Problem.Cause` is the reader's error, which
  `Err()` returns as it is (with `details.reference`):
  `fileaccess.not_allowed` (outside the allowlist), `fileaccess.not_found`,
  `fileaccess.too_large`, `fileaccess.unreadable`,
  `fileaccess.invalid_path`, `template.file_binary`. Without a reader
  it is `template.file_unavailable` (`ErrFilesUnavailable`).
- **Where it reads.** HTTP and GraphQL-over-HTTP sends and repeat runs,
  SSE requests, GraphQL subscriptions and schema fetches, and the
  user-initiated previews Copy as curl and HAR export (so the command
  reproduces the request). The header preview, computed after every edit,
  does not read files: it leaves the reference as written and lists it
  with the undefined names, as do the JWT preview, the GraphQL endpoint and
  the netrc check. WebSocket, Socket.IO, TCP, gRPC, the mock servers and
  the SSE server have no reader: `{{$file}}` there refuses with
  `template.file_unavailable`.
- The editor lists `$file` with the dynamic values (its description is
  the hover), so a reference is not red; whether the file is allowed is
  only known when the request is sent.

### Response references

DYN-06. `{{$response REQUEST SOURCE [EXPR] [send=WHEN] [secret]}}`
is a value from another request's **last response**, such as a login's
token. Unlike an [extraction rule](extraction.md), which sets a variable
when the login's response arrives, a reference reads the response when
the request that uses it is sent, and can send the login first.

```text
Authorization: Bearer {{$response req_5a0f93c7e2b14d68 jsonpath $.access_token}}
X-Session: {{$response req_5a0f93c7e2b14d68 header X-Session send=missing}}
{{$response req_5a0f93c7e2b14d68 jsonpath $.token send=10m secret}}
{{$response req_5a0f93c7e2b14d68 status}}
{{$response req_5a0f93c7e2b14d68 body | base64}}
{{$response req_c93e1a7b40f2d856 jsonpath "$.items[?@.name == 'a b'].id" send=always}}
```

- **The request** is named by its ID (`req_…`, the `id` of its file,
  [collection.md](collection.md)), so renaming or moving it does not break
  the reference. Nobody types it: the completion list after
  `{{$response ` lists the project's HTTP and GraphQL requests **by name**
  and inserts the ID, and the hover and the resolution view show the name.
  An ID that is not an HTTP or GraphQL request of the open project is
  `http.response_request_not_found` (an SSE request too: it has no
  response to read).
- **The source** is that of an extraction rule, read by the same code
  (`extract.Evaluate`, `assert.Read`): `jsonpath EXPR` (RFC 9535; a
  singular query gives its value, another the array of the values),
  `header NAME` (any case, values joined with `", "`), `cookie NAME`,
  `xpath EXPR`, `regex EXPR` (the first group, or the match), `status`
  (`200`), and `body`, the whole body as text. An expression with spaces
  or a `|` is written in double quotes; an unquoted `|` ends the
  reference, so `{{$response r body | base64}}` is a
  [pipe](#filters-encoding-and-hashing) whose input is the reference.
  The value is used as is: never parsed for variables.
- **`send=` says when the request is sent first** (the staleness rule):
  `never` (the default: the stored response is read, and a request
  without one refuses the send with `http.response_not_sent`, whose hint
  says to send it or to add `send=missing`); `missing` (sent first when it
  has no response yet); an age such as `30s`, `10m`, `1h30m`, `2d` (terms
  of up to 6 digits and `s`, `m`, `h` or `d`, at most 365 days: sent first
  when it has no response or its response arrived longer ago than that);
  `always` (sent first at every send).
- **`secret`** masks the value like a secret variable's. A value is
  secret anyway when it holds a value the referenced send masked: a
  secret variable it sent, or a value a secret extraction rule took from
  its response (the login's token), so it is masked in the timeline and
  the history of the request that uses it too.
- **Where it reads.** The last response of every request sent this
  session is kept in memory as received (`lastResponses`, the body up to
  4 MiB, `MaxReferencedBody`; 32 MiB in all, the oldest dropped first):
  every send, repeat and folder run request goes through `exchange`,
  which keeps it once its extraction rules ran. When the history has a
  newer response (or the request was not sent since PostSonde started),
  the [history's](history.md) newest entry with a response is read
  instead, as it was recorded: masked, and the body cut at 64 KiB. A
  value masked there (`••••••`) is `http.response_masked`, and with any
  `send=` but `never` it counts as no response, so the request is sent
  again. A body that is not kept whole is `http.response_body_unavailable`
  for the body sources (the headers and status still work). Clearing a
  request's history, or the whole history, drops what was kept for it
  (`ForgetResponses`, wired to `history:changed` "cleared").
- **Sending first.** `HTTPService.Send` builds the request once before
  its operation starts, as always, with a reader that only notes which
  requests must be sent first (a stand-in value for them, so prompts and
  undefined names are still reported at once). When there are some, the
  operation sends them, reporting the stage *Sending a referenced request
  first*, then builds the request again with their responses: each is
  built and sent as a send of its own (its variables, auth, cookies,
  extraction rules, its own history entry), but its response is not shown
  in its tab. A request is sent first **once per send**, however many
  references read it. Repeat runs (each send of the run) and folder runs
  (`RunSession.Send`) send first the same way, inside their operation.
  Previews never send: the header preview, Copy as curl, HAR export, the
  resolution view and the hover read the stored response, and leave a
  reference without one as written. SSE streams, GraphQL subscriptions,
  the schema fetch and the OAuth 2.0 token request read the stored
  response and never send. WebSocket, Socket.IO, TCP, gRPC and the mock
  servers have no reader: `template.response_unavailable`.
- **Loops and depth.** A request sent first may need another one sent
  first, up to `MaxResponseDepth` (5) requests deep
  (`http.response_too_deep`). Sending a request that is already being
  built for this send (A needs B sent first, B needs A sent first; or a
  request that needs itself sent first) is `http.response_cycle`, naming
  the requests: *The requests refer to each other's responses in a loop:
  A → B → A.* (`details.requests`). Reading a request's own **stored**
  response is no loop. A request sent first that fails (it cannot be
  built, or gets no response) is `http.response_send_failed`, naming it,
  with its error's code in `details.cause`.
- **Missing values** are blocking `response` problems (`ProblemResponse`)
  whose error is the reader's, with `details.reference`: a value the
  response does not have is the extraction code, `extract.not_found`
  (*JSONPath $.nope selects nothing in the body of the last response of
  "Log in" (status 401).*), `extract.unreadable_body`, or an invalid
  expression's (`jsonpath.invalid_query`). Invalid arguments are
  `template.invalid_dynamic`. The status of the response is no failure:
  a 401 is a response, and the value it lacks says so.

The template package only parses the reference (`ParseResponse`) and
asks `Resolver.Responses` (a `ResponseReader`); the HTTP service's reader
(`internal/app/services/http/responseref.go`) finds the response, decides
whether to send first, guards the loops and reads the value. In the
editor, `lib/templateResponse.ts` mirrors `ParseResponse` (both run
`testdata/responses.json`): a reference is red when it is not valid or
names no request of the project; its hover names the request, what it
reads and, from Go (`HTTPService.ResponseValue`, which never sends), the
stored value (masked when secret), the status and age of the response and
whether the next send sends the request first. The resolution view lists
it as kind `response` (source `response`, scope `Response: Log in`) with
the stored value, and reloads on `history:changed`.

### Fake data

Values for realistic test data (DYN-05), picked from small built-in word
lists (`fake.go`, no faker library) with the same `Resolver.Rand`, so a
seeded source repeats them. The names are Postman's, so imported
collections keep working ([import.md](import.md)). Host names use the
reserved `example.com`/`.org`/`.net` and `.test`, `.example`, `.invalid`
domains (RFC 2606), so a fake email or URL never reaches a real person or
server. Those marked *N* take an optional count, 1 to 100 (default 3);
`$randomPassword` an optional length (default 15).

| Kind | Names |
|---|---|
| People | `$randomFirstName` `Ada`, `$randomLastName`, `$randomFullName` `Ada Lovelace`, `$randomNamePrefix` `Dr.`, `$randomNameSuffix` `Jr.`, `$randomUserName` `Ada.Lovelace42`, `$randomJobTitle` `Senior Data Engineer` |
| Internet | `$randomEmail`, `$randomExampleEmail` `ada.lovelace@example.org`, `$randomDomainWord` `acme`, `$randomDomainSuffix` `test`, `$randomDomainName` `acme.test`, `$randomUrl` `https://acme.test`, `$randomProtocol`, `$randomIP`, `$randomIPV6`, `$randomMACAddress`, `$randomPassword` *N*, `$randomSemver` `2.14.3` |
| Places | `$randomCity`, `$randomStreetName` `Maple Avenue`, `$randomStreetAddress` `742 Maple Avenue`, `$randomCountry`, `$randomCountryCode` `FR`, `$randomLatitude`, `$randomLongitude` (6 decimals), `$randomPhoneNumber` `555-123-4567`, `$randomPhoneNumberExt` `555-123-4567 x89` |
| Business | `$randomCompanyName` `Lovelace Labs Inc`, `$randomCompanySuffix`, `$randomPrice` `1.00`–`1000.00`, `$randomCurrencyCode` `EUR`, `$randomLocale` `fr-FR`, `$randomAbbreviation` `HTTP` |
| Colours, dates | `$randomColor` `teal`, `$randomHexColor` `#1a2b3c`, `$randomWeekday`, `$randomMonth`, `$randomDatePast` (past year), `$randomDateFuture` (next year), `$randomDateRecent` (past day), ISO 8601 in UTC from `Resolver.Now` |
| Words | `$randomWord`, `$randomWords` *N*, `$randomLoremWord`, `$randomLoremWords` *N*, `$randomLoremSlug` *N* `lorem-ipsum-dolor`, `$randomLoremSentence`, `$randomLoremSentences` *N*, `$randomLoremLines` *N* (one sentence per line), `$randomLoremParagraph`, `$randomLoremText`, `$randomLoremParagraphs` *N* (blank lines between) |

### Filters: encoding and hashing

A reference can pass a value through encoding and hashing functions
(DYN-04), left to right, with `|`:

```text
{{ password | base64 }}
{{ "user:pass" | base64 }}              a quoted text as the input
{{ body | sha256 }}                     hex digest
{{ body | hmacSha256 signingKey base64 }}
{{ $uuid | sha1 | base64 }}             a dynamic value as the input
{{ auth.token | urlEncode }}            dot notation as the input
```

The part before the first `|` is the **input**: a variable name, resolved
like any reference (nested variables, dot notation, a dynamic value with
its arguments), or a text in double quotes (which cannot contain `"`,
`{{` or `}}`). Each part after a `|` is a filter and its arguments,
separated by whitespace; a `|` inside double quotes is not a separator.

| Filter | Result |
|---|---|
| `base64` | Base64, standard alphabet, padded |
| `base64url` | Base64, URL alphabet, unpadded (as in JWTs) |
| `base64Decode` | Decodes Base64, either alphabet, padded or not |
| `urlEncode` | Percent-encodes every byte but `A-Z a-z 0-9 - . _ ~` (a space is `%20`) |
| `urlDecode` | Decodes `%XX` sequences; a `+` stays a `+` |
| `hex`, `hexDecode` | Lower-case hex of the bytes, and back |
| `md5`, `sha1`, `sha256`, `sha512` `[ENC]` | The digest, as `hex` (default), `base64` or `base64url` |
| `hmacMd5`, `hmacSha1`, `hmacSha256`, `hmacSha512` `KEY [ENC]` | HMAC of the input with `KEY`: a variable name (usually a secret variable) or a quoted text; output as for the digests |

Rules:

- **Nothing that rendered before changes.** A name is a pipe only when no
  scope defines the whole name and it is not a valid dynamic value
  (`{{$datetime YYYY|MM}}` is still a date format).
- **Secrecy.** The result is secret (`Ref.Secret`, `Result.Secret`) when
  the input or a key is: a hash or an encoding of a secret is masked in the
  timeline like the secret itself. The input and keys are in `Ref.Nested`,
  so their own values are masked too.
- **Problems.** An undefined input or key is an ordinary undefined variable
  (`template.unresolved`, naming it). An unknown filter, missing key, extra
  argument or unknown output encoding, and a failing decode (`"%%" |
  base64Decode`), are a blocking `invalid_filter` problem: *The filter
  {{body | b64}} is not valid: "b64" is not a filter.* (code
  `template.invalid_filter`). A cycle through a pipe
  (`a = {{a | base64}}`) is a `cycle`.
- The digests and HMACs are computed over the bytes of the value (UTF-8),
  with the standard library's `crypto/*` packages.

**Offsets.** Every time value takes an optional last argument that moves
the time: one or more signed terms, `+1d`, `-2h`, `+1M-1d`. Units: `y`
years, `M` months (calendar steps), `w` weeks, `d` days, `h` hours, `m`
minutes, `s` seconds, `ms` milliseconds; at most 6 digits per term.
`{{$timestamp +1d}}`, `{{$isoTimestamp -30m}}`,
`{{$datetime "YYYY-MM-DD" +1w}}`, `{{$datetime +1d}}`.

**Format tokens** (Moment style, longest match first): `YYYY` `YY` year;
`MM` `M` month, `MMM` month name (`Sep`); `DD` `D` day; `ddd` weekday
(`Sat`); `HH` `H` 24-hour; `hh` `h` 12-hour; `mm` `m` minutes; `ss` `s`
seconds; `SSS` milliseconds; `A` `AM`/`PM`, `a` `am`/`pm`; `Z` `+01:00`,
`ZZ` `+0100`; `X` Unix seconds, `x` Unix milliseconds. Text in `[brackets]`
is literal (`[at]`), any other character is copied as is. Letters that are
tokens (`a`, `s`, `m`, `h`, `D`, …) must be bracketed to appear literally.

Rules:

- **Scopes win.** A scope that defines `$timestamp` supplies that value;
  the dynamic value is only the fallback, so existing projects keep working.
- An unknown `$name` (`{{$nope}}`, `{{$UUID}}`) is an ordinary undefined
  variable (`template.unresolved`).
- Invalid arguments (`{{$randomInt 5 1}}`, `{{$timestamp 1d}}`,
  `{{$uuid x}}`) are a blocking `invalid_dynamic` problem with a
  `Problem.Message`; `Err()` says, for example, *The dynamic value
  {{$randomInt 5 1}} is not valid: the minimum is greater than the
  maximum.* (code `template.invalid_dynamic`).
- **Editor and warnings.** `IsDynamic(name)` takes the name as in
  `Segment.Name` and reports whether it is a dynamic value with valid
  arguments, without generating anything. Code that marks unresolved
  variables treats such names as resolved when `Chain.Lookup` finds
  nothing. `DynamicNames()` lists the names for completion.
- **Deterministic tests.** `Resolver.Now` (default `time.Now`) is the
  clock and `Resolver.Rand` (default `crypto/rand.Reader`) the random
  source; tests pass a fixed time and a seeded `math/rand/v2.ChaCha8`.
  Random values are uniform (integers via `crypto/rand.Int`, strings by
  rejection sampling). A `Resolver` with the default `Rand` is safe for
  concurrent use; a caller-supplied `Rand` must be too, if shared.

## Tests

Table tests cover parsing, escaping, precedence, nesting, dot notation
(`path_test.go`), cycles, limits and errors. Every dynamic value, fake data
value (`fake_test.go`, with a zero random source so each pick is known)
and filter (`filter_test.go`, digests checked against `openssl dgst`) has a
test with a known output, and secrecy propagation through pipes is tested.
Response references (`response_test.go`) are parsed from
`testdata/responses.json`, which `lib/templateResponse.test.ts` runs too;
`internal/app/services/http/responseref_test.go` covers a login's token
read by JSONPath, header and status, sending
first with `send=missing`, an age and `always` (once per send, recorded
in the history), a chain of requests sent first, loops, too deep a chain,
a request sent first that fails, missing values and requests, secrets,
the history after a restart (a masked value sent again), clearing, the
resolution view, a repeat and a folder run. Native fuzz tests check the parser's invariants (contiguous
segments, name spans, `Parse(Escape(s))` gives back `s`) and that rendering
terminates within its limits:

```sh
go test -run='^$' -fuzz='^FuzzParse$'  -fuzztime=30s ./internal/core/template/
go test -run='^$' -fuzz='^FuzzRender$' -fuzztime=30s ./internal/core/template/
go test -run='^$' -fuzz='^FuzzSelect$' -fuzztime=30s ./internal/core/template/
go test -run='^$' -fuzz='^FuzzPipe$'   -fuzztime=30s ./internal/core/template/
```

A failing input is saved under `internal/core/template/testdata/fuzz/`;
commit it with the fix so it stays a regression test.
