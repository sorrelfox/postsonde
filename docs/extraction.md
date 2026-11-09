# Extraction into variables

Extraction rules take values from a response into variables, so later
requests can use them: a login's token, an ID a create returned, a
session cookie (TEST-05). A rule is
data, like an assertion: a **source** in the response, its
**expression**, the **variable** it sets and where (**scope**), and two
flags, **secret** and **persist**. There is no scripting. To read a value
from another request's last response when a request is sent, without a
variable (and send that request first when its response is missing or
stale), use a response reference instead, `{{$response REQUEST jsonpath
$.token}}` (DYN-06, [templates.md](templates.md#response-references)).

| Piece | Where |
|---|---|
| Rules as stored | `projectfile.Extraction`, the `extractions` member of `http`, `graphql` and `sse` files ([file-format.md](file-format.md#extractions)) |
| Reading a value from a response | `internal/core/assert` (`Read`, `Query`, `Reading`): the same targets the assertions read |
| Rules, sources, scopes, outcomes, codes | `internal/core/extract` (`Rule`, `Evaluate`, `Check`, `Result`) |
| Applying the rules after a send, setting the variables, masking | `internal/app/services/http/extract.go` (`runExtractions`, `ExtractionResultDTO`) |
| Run results | `internal/app/services/runner` (`RunResultDTO.extractions`) |
| Binding wrappers, labels, summaries | `frontend/src/lib/extractions.ts` |
| The Extract sub-tab | `frontend/src/components/editor/ExtractionsEditor.tsx` |
| The response's Variables tab | `frontend/src/components/response/ResponseExtractions.tsx` |
| Jumping from a result to its rule | `frontend/src/commands/extractionJump.ts` |

```
send (Send, Repeat, a request of a folder run)
   │ prepare: the enabled rules, {{variables}} of each expression rendered
   │          (an undefined one makes that rule invalid, not the send)
   ▼
exchange: the response arrives (the whole body spooled, HTTP-37)
   │ extract.Evaluate: every rule's value, read with assert.Read
   │ found ──▶ runtime scope: runtimevars.Store.Set  (+ the run's Vars inside a run)
   │           run scope:     runner.Vars.Set        (skipped outside a run)
   │ secret values set ──▶ added to the send's masker (timeline, results, history)
   ▼
ResponseDTO.extractions ──▶ the Variables tab; in a run RunResultDTO.extractions
   │ runtimevars:changed ──▶ the Runtime section and the editors' chips reload
   ▼
the next request renders {{token}} with the new value
```

## Rules

A request has any number of rules, applied **in order** to every response
of the request (a later rule setting the same variable wins). Only the
rules are saved in the request file; the values never are.

| Source (`source`) | Expression (`expr`) | Value |
|---|---|---|
| `jsonpath` | RFC 9535 JSONPath ([jsonpath.md](jsonpath.md)) | A singular query (`$.access_token`, `$.items[0].id`) gives its value; any other (`$.items[*].id`) the array of the values it selects |
| `xpath` | XPath 1.0 ([xpath.md](xpath.md)) | The string-value of the one node selected, the array of several, or a number, boolean or string |
| `regex` | RE2 regular expression on the body | The first match: its first capture group, or the whole match without one |
| `header` | Header name, any case | The header's values joined with `", "` |
| `cookie` | Cookie name, exact | The value in the response's `Set-Cookie` headers; the last one wins |
| `status` | none | The status code, `"200"` |

The values are read by `assert.Read`, so a rule reads exactly what an
assertion with the same target and expression checks
([assertions.md](assertions.md#targets)). `{{variables}}` in an expression
are rendered with the request's variables before the send, as an
assertion's are.

**The value as text.** A variable holds text: a string as it is (no
quotes), a number as written in the body (`42`, `1e3`), `true`, `false`
and `null` as words, an array or object as compact JSON with its member
order kept and no HTML escaping (`["a","<b>"]`). A value over 1 MiB
(`extract.MaxValueBytes`) is refused (`extract.value_too_large`): a
variable is sent in requests and a persisted one written to a file.

**Variable names** are those a `{{reference}}` can spell, as for runtime
variables (`runtimevars.CheckName`): not empty, at most 256 bytes, no
spaces around it, no `{{` or `}}`.

## Scopes

| Scope (`scope`) | Sets | Outside a run | Inside a folder run |
|---|---|---|---|
| runtime (left out of the file) | the project's runtime variable ([environments.md](environments.md#runtime-variables)), for the session or kept (`persist`) | set | set, **and** the run's own variable of the name |
| `run` | the run's own variable (`runner.Vars`, [runner.md](runner.md#run-variables)) | **skipped** ("Not set: rid is a run variable, set during a folder run only.") | set; gone when the run ends |

Why a runtime rule also sets the run's variable: a run's own variables
win over the runtime ones (`… < runtime < run`), so a name the run
already holds (set by an earlier `run` rule, or by a data row later,
RUN-05) would hide the new value from the rest of the run. Setting both
makes a runtime rule mean "every later request sees this value", in a run
or not.

A run variable never outlives its run, so `persist` does nothing with the
`run` scope (the editor disables it and saving a rule set to run clears
it). A run variable is never written anywhere.

## When and how rules are applied

- **After every response** of an `http` or `graphql` request sent with
  `HTTPService.Send`, each send of `Repeat`, and each request of a folder
  run (`RunSession.Send`): all three go through `prepare` and `exchange`.
  Not SSE requests (their rules are kept but not applied, as their
  assertions), GraphQL subscriptions, or replayed webhook requests.
- **Any status**: a response is a response. A 401 from a login with a
  `jsonpath` rule simply finds nothing, and says so; use a `status` rule
  or an assertion for the status itself.
- **The whole body**, as the assertions read it: `r.Body`, or the spool's
  file when the body was larger than the head kept in memory (HTTP-37); a
  body over 256 MiB is not read back (`http.body_too_large`).
- **Before the next render**: the variables are set inside `exchange`,
  before the operation ends, so a request sent next (by hand, the next
  send of a repeat, the next request of a run) renders with them. The
  assertions of the same response were rendered before the send and do
  not see the new values.

## Results

`ResponseDTO.extractions` lists the enabled rules in order
(`ExtractionResultDTO`: `index` in the request's list counting disabled
rules, `source`, `expr` as rendered, `variable`, `scope`, `secret`,
`persist`, `outcome`, `value`, `valueClipped`, `message`, `code`), `[]`
without rules. A folder run copies them into `RunResultDTO.extractions`.

| `outcome` | Meaning | `code` |
|---|---|---|
| `set` | The value was found and the variable set. `value` is the value, cut after 1 KiB for the view (`valueClipped`), or `••••••` for a secret rule | |
| `failed` | The response has no such value, or it could not be set | `extract.not_found` ("Header X-Missing is not in the response.", "JSONPath $.nope selects nothing in the body.", "The regular expression /…/ does not match the body."), `extract.unreadable_body` ("JSONPath $.token cannot be read: the body is not valid JSON."), `extract.value_too_large`, `http.body_too_large`, the runtime variables' own code (`runtimevars.local_data_failed`), `http.no_runtime_variables` |
| `invalid` | The rule is wrong | `extract.unknown_source`, `extract.unknown_scope`, `runtimevars.invalid_name`, `assert.missing_expr`, `assert.invalid_regex`, `jsonpath.invalid_query`, `xpath.invalid_query`, `*.too_complex`, `template.unresolved` (an undefined `{{variable}}` in the expression) |
| `skipped` | A `run` rule outside a folder run | |

### Failure semantics

A rule that fails or is invalid **leaves its variable as it was**: it is
neither cleared nor set to `""`. A token refreshed by a login that failed
keeps the last good one, and the Variables tab says the rule failed.

- **A single send** (and each send of a repeat) ends `ok` with its
  response, as with failed assertions (TEST-06): the Variables tab's badge
  turns red ("2 of 3 variables set") and the row shows why. Failed
  extractions do not count in the Tests tab: they are not checks of the
  response, and a request that only extracts has no tests to fail.
- **In a folder run** a failed or invalid rule makes the request's result
  `failed`, like a failed assertion, so the summary lists it
  ("Variable token was not set: …") and **Stop at the first failure**
  stops the run before the requests that need the value are sent with a
  stale or undefined one. A run exists to check that a flow works, and a
  login that yields no token is a broken flow even when its status is
  200. A `skipped` rule never fails a result.

## Secrets

A rule marked **secret** sets a secret variable (runtime or run), which
the next requests mask like any secret variable: in their timeline,
history entries, a copied cURL command and a HAR export
(`TestExtractedSecretMasked`). The value is also masked from the moment
it is extracted:

- in the extraction results (`value` is `••••••`, and the value is masked
  in every message, and in the assertion results of the same response);
- in the extracting response's timeline and its **history entry**: the
  login response's body, headers and cookies are recorded with the token
  masked, so the history and a HAR export of it never hold it.

The response view of the extracting send still shows the body and headers
as received: that is the response the user asked to see.

A **persisted** secret runtime variable is written to the secrets file
next to the other secret values, never to the project
([environments.md](environments.md#runtime-variables)).

## UI

- **The Extract sub-tab** of the HTTP and GraphQL editors (after Tests,
  not for SSE requests; its badge counts the enabled rules): a table in
  the Tests sub-tab's style with, per rule, the enable checkbox, the
  source, the expression (a header or cookie name, a JSONPath…; none for
  the status code), the variable name between `{{` and `}}` (marked
  invalid while empty), the scope (**Runtime** or **Run only**, each with
  a title saying what it means), and two toggle buttons: **Secret** (the
  lock) and **Keep after closing the project** (the pin; disabled for Run
  only). After a send each row shows its last result (a tick, a cross, a
  warning or an info mark) with the message on hover. Rows move with the
  arrows or Alt+↑/↓ inside the row, and are deleted with the bin; focus
  follows. A hint under the table says how rules behave. Edits are
  auto-saved with the request.
- **The Variables tab** of the response (only for a request's own
  response, and only when it had rules; also in a run result's dialog):
  "N of M variables set" on the tab (green when none failed, red
  otherwise), then one row per rule: the outcome, the variable as
  `{{name}}` (a button that opens the Extract sub-tab at the rule), tags
  for the scope, Secret and Kept, where the value came from and the
  value, or why it was not set with the code.
- **The folder runner** lists a failed extraction under its result's row
  and in the summary's failures.
- The Environments view's Runtime section reloads when a rule sets a
  runtime variable (`runtimevars:changed`), as do the editors' chips.

## Tests

- `internal/core/extract/extract_test.go`: every source finding its value
  (strings, numbers, booleans, null, arrays, objects, a non-singular
  JSONPath, an XPath node-set and number, a capture group and a whole
  match), each one missing, a body of the wrong format, invalid
  expressions, unknown sources and scopes, bad variable names, the size
  limit, the order of results.
- `internal/core/storage/projectfile`: the `http_extractions` golden
  example (shown above in [file-format.md](file-format.md#extractions)),
  and rules of `http`, `graphql` and `sse` files round-tripping with an
  unknown source, scope and member.
- `internal/app/services/collection/extractions_test.go`: the rules saved
  and read back through the auto-save pipeline, the runtime scope left out
  of the file.
- `internal/app/services/http/extract_test.go`: an end-to-end
  case against an `httptest` login endpoint, as **two single sends** (the
  token, a user ID, a cookie and a regex extracted; the next request
  carries them; failed, invalid, skipped and disabled rules; a later
  failed extraction keeps the value; nothing in the project files) and as
  **two requests of a run** (a run rule, a runtime rule replacing a run
  variable of the name); the secret masked in the timeline, the history
  (both entries), cURL and HAR; no runtime variables; a long value cut.
- `internal/app/services/runner/extract_test.go`: a folder run of two
  iterations where each login's token reaches the next request, the run
  variable gone after the run, and a failed extraction stopping a run
  that stops on failure.
- Frontend: `lib/extractions.test.ts`, `ExtractionsEditor.test.tsx`
  (editing, flags, scope, reordering, deleting with focus, the jump,
  read-only, accessibility), the Extract sub-tab in
  `RequestEditor.test.tsx` (saved, counted, the last send's results),
  `ResponseExtractions.test.tsx` (the Variables tab, badge, jump,
  accessibility), `runner/RunExtractions.test.tsx` (results, summary, the
  result's dialog) and the pseudo-locale tests of the editor and the
  response.

## Not handled yet

- Extraction from SSE events, GraphQL subscription messages, WebSocket,
  Socket.IO, gRPC and TCP messages.
- An option to clear the variable when a rule fails, and a default value.
- A run variable marked secret is masked in the run's timelines and
  results, but not in a copied or saved app log (`knownSecrets` covers
  the runtime variables, not a run's).
- Rules are not exported to Postman (`postman.export_extractions`): a
  Postman collection sets variables from test scripts.
