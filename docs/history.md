# Request history

PostSonde keeps a local log of every request the user sends (NAV-02): the
request as sent and a size-capped snapshot of the response, with secrets
masked before anything is written. It lives in the open project's local
directory, never in the project folder (STO-04), so it is never committed
and each copy of a project has its own. The History sidebar view searches,
filters, restores and clears it ([the history panel](#the-history-panel),
NAV-03). A request's own earlier responses (HTTP-39) and the response
examples saved from them (HTTP-38) come from it too:
[below](#response-examples).

| Piece | Where |
|---|---|
| Store: segments, index, retention, crash repair, snapshots | `internal/core/history` |
| Recording a send (entries, masking) | `internal/app/services/http/history.go` |
| An entry as a response example (HTTP-38) | `history.Example` (`internal/core/history/example.go`), `HistoryService.Example` (`internal/app/services/history/example.go`) |
| The view of a stored response | `HTTPService.StoredResponse` (`internal/app/services/http/stored.go`) |
| Search and filters, clearing a request's entries | `Store.List`, `Store.DeleteRequest` (`internal/core/history/store.go`) |
| The editor's form of a request, restoring an entry (NAV-03) | `history.Template`, `MaskTemplate` (`template.go`), `history.Restore` (`restore.go`), `HistoryService.Restore` (`internal/app/services/history/restore.go`) |
| The history panel | `frontend/src/components/history/HistoryPanel.tsx`, `commands/history.ts`, `commands/useHistoryList.ts`, `lib/history.ts` |
| Bound service, `history:changed`, `Recorder` | `internal/app/services/history` (`HistoryService`) |
| Wiring | `services()` in `internal/app/services.go` |

## What is recorded

Every send of the HTTP service, whatever its outcome:

- **HTTP and GraphQL requests** of the collection (`HTTPService.Send`),
  kind `http` or `graphql`, with the request's ID and its name at the time.
- **Replayed webhook requests** (`SendPrepared`, MOCK-08), kind `replay`,
  without a request ID.
- **Runs** of repeated or interval sends (`HTTPService.Repeat`, HTTP-27)
  add one entry when they end, not one per send: the run's last send
  that was not cut short by stopping it
  ([http-client.md](http-client.md#repeat-and-interval-sending-http-27)).
- **Folder runs** (RUN-01) add one entry per request they sent, not one
  per send, when they end: each request's last send that was not cut
  short by stopping the run, in the order the requests were first sent
  ([runner.md](runner.md#history)). The runs themselves, every result
  without the responses, are in the run history (RUN-09,
  [runner.md](runner.md#run-history)).
- Sends that got a response (outcome `ok`, any status), that **failed**
  (`error`: connection refused, TLS, timeout...; the error code and
  message are kept) and that were **cancelled** (`cancelled`).

A request that cannot be built (an undefined variable, an invalid
header) is refused before anything is sent and is not recorded. Not
recorded either: SSE streams, WebSocket, TCP and gRPC traffic, GraphQL
subscriptions, and the app's own requests such as a schema
introspection (GQL-10).

An entry holds:

| Part | Content |
|---|---|
| Summary | ID (`h_` and 16 hex digits), start time, kind, request ID and name, the name of the environment that was active (`environment`, "" for none), method, URL (after variables, before redirects), outcome, status code and text, error code and message, duration, request and response body sizes, `masked` |
| Template | the request as its editor had it (`template`, NAV-03): method, URL, query and path parameter rows, header rows and body (mode, raw type and text, form fields, multipart parts, file path) of an HTTP request; URL, method, header rows, query, variables and operation name of a GraphQL one. `{{variables}}` as written, masked by value (below); texts over 64 KiB are clipped and named in `clipped`. No auth, settings, assertions or examples. Replays, and entries written before it was recorded, have none |
| Request | the first hop as it went over the wire: method, URL, protocol, every header field written, the body. When the send failed before a response, the request as built: the enabled header rows and a raw body (a multipart or file body is not read again) |
| Response | protocol, status, final URL and the number of redirects, headers, Content-Type, the body as received, `truncated` when the client stopped reading at its limit; the final hop's `timings` (DNS, connect, TLS, send, wait, receive in milliseconds, -1 for a phase that did not happen, and the total) and its `remoteAddr`/`localAddr`, for the HAR export. Entries written before these were recorded have none |

**Bodies** keep their first 64 KiB (`history.MaxBody`) with their full
size and `clipped`: as text when the bytes are UTF-8, else as Base64
(`base64: true`). A line over 1 MiB (`MaxEntryBytes`, only possible with
huge headers) loses its bodies (and the template's body, query and
variables), then its headers, and is marked `trimmed`.

The entry is written after the response went to the UI, on the send's
goroutine; a write that fails is logged and never fails the send. The
project is bound when the send starts: a send that ends after a project
switch is recorded in the project it was sent from.

## Secrets

Nothing is written unmasked. The recorder uses the timeline's masker
(`httpclient.SecretMasker`, [privacy.md](privacy.md#secrets-in-the-timeline))
with the same secrets: the values of secret variables wherever they
appear, also URL-escaped; the value an API key auth added; the Digest
password; the credentials of `Authorization` and `Proxy-Authorization`
after the scheme, and a Digest `response` hash. For the history it adds
the credentials of every `Authorization` and `Proxy-Authorization` field
sent (and a Basic password decoded, and the password of URL userinfo) as
values, so a server that echoes them back does not leave them in the
response snapshot. The masker applies to the URL, the request and
response headers, both bodies (masked in full before they are clipped, so
a secret is never cut in half and half kept), the final URL and the error
message. `masked` says a secret was hidden. The same limits as in the
timeline apply: masking is by value, and a secret sent transformed
(hashed, Base64 in a body) is not recognised. Cookies are not secrets.
A value typed for a `{{$prompt ... secret}}` (DYN-07) is a secret of the
send like a secret variable's; one typed for a prompt without `secret` is
recorded as sent ([templates.md](templates.md#prompts-at-send-time)).

The **template** is masked with the same masker, but by value only
(`MaskText`): a `{{token}}` reference stays as written, since the value it
stood for is the secret, while a secret typed into a row or the body is
masked. A header row `Authorization` or `Proxy-Authorization` without a
reference is also masked by name (`Bearer ••••••`), as in the timeline.

## Files

```
<local>/history/00000001.jsonl
<local>/history/00000002.jsonl   ...
```

`<local>` is the workspace's local directory ([workspace.md](workspace.md)).
The directory is created `0700` on the first write, the files `0600`.
Each line is one JSON record, `{"v":1,"entry":{...}}` for an entry or
`{"v":1,"deleted":"<id>"}` for a deletion. Lines are only ever appended.

- **Rotation.** A new segment starts when the next line would take the
  current one past 4 MiB (`DefaultSegmentBytes`).
- **Retention.** The newest 1000 entries (`DefaultMaxEntries`) whose lines
  add up to at most 64 MiB (`DefaultMaxBytes`) are kept; the oldest go
  first, the newest is always kept. A segment file is removed once none of
  its entries is kept, oldest first (so a deletion line never outlives the
  entry it deletes). The files hold at most the limit plus one segment,
  plus deletion lines. The limits are constants for now (a setting is
  not handled yet, below).
- **Index.** Opening reads every segment and keeps each live entry's
  summary and its position in memory; `List` works on the index, `Get`
  reads one line. Retention is applied again at open, so a lower limit
  takes effect at once. Entries past the limit whose segment still exists
  come back if the limit is raised.
- **Crash safety.** Every line goes to the file in a single write. A last
  line cut short by a crash (no newline at the end) is dropped and cut from
  the file when the store opens, so the next entry starts on a line of its
  own; a write that fails is cut back the same way. Lines that are not
  JSON, and lines of a newer format (`v` above 1), are skipped. Nothing is
  fsynced: a crash of the app loses nothing written, a power cut may lose
  the last entries.
- **Concurrency.** One `Store` per directory (the `Registry` hands them
  out), with one lock around the index and the file; sends that end at the
  same time are written one after the other.

## Service

`HistoryService` (TypeScript: `HistoryService.*`), always on the open
project; without one the methods fail with `workspace.none_open`.

| Method | Returns |
|---|---|
| `List({text, deep, method, status, kind, environment, since, until, requestId, offset, limit})` | `PageDTO`: `entries` (`SummaryDTO`, newest first), `total` matching, `all` kept, `environments` (the names in the kept entries, sorted, for the filter). `text` matches the method, URL, request name, status, error and environment, ignoring case; with `deep` also the header fields (`Name: value`), text bodies and final URL of the request and response and the template's fields; `status` is `1xx` to `5xx` or `error` (no response); `kind` `http`, `graphql` or `replay`; `environment` a name; `since` (included) and `until` (not) RFC 3339; `limit` is 50 by default, at most 500. See [the history panel](#the-history-panel) |
| `Get(id)` | `EntryDTO`: the summary with `request`, `response` (null without one) and `trimmed` |
| `Restore(id)` | `RestoreDTO`: entry `id` as a request to open again, in the request editor's shapes (`collection.KeyValueDTO`, `BodyDTO`), with `templated` and `hidden`; nothing is written ([below](#restore)) |
| `Delete(id)` | nothing |
| `ClearRequest(requestId)` | how many entries of the request were removed |
| `Clear()` | nothing; removes every entry and file |
| `ExportHAR({ids, requestId, title})` | `ExportHARResultDTO` (`saved`, `path`, `entries`, `masked`): the chosen entries, a request's or all of them as a HAR 1.2 file picked in the save dialog, as kept (masked). See [har.md](har.md#history-entries) |
| `Example({id, name})` | `collection.ResponseExampleDTO`: entry `id` as a response example named `name` (blank: its status, `201 Created`), masked again; nothing is written ([below](#response-examples)) |

Times are RFC 3339 in UTC. After a send is recorded in the open project,
`Delete`, `ClearRequest` and `Clear`, Go emits **`history:changed`**
(`ChangedEventDTO`: `action` `added` with `id` and `entry`, `deleted` with
`id`, or `cleared`, with `requestId` when only that request's entries
went).
Error codes: `history.not_found` (details: `id`), `history.read_failed`,
`history.write_failed` (details: `path`), `history.unavailable`,
`history.export_empty`, `history.export_failed`, `history.no_response`
(`Example` of a failed or cancelled send; details: `id`),
`history.invalid_filter` (a `since` or `until` that is not RFC 3339, or
`ClearRequest` without a request; details: `field`, `value`).

## The history panel

The History view of the icon rail (NAV-02, NAV-03,
`components/history/HistoryPanel.tsx`) shows the open project's entries,
newest first, grouped as Today, Yesterday and earlier days (local time),
each with its method (or `GQL`), request name (a replay shows its URL),
status coloured by class (2xx green, 3xx and 4xx amber, 5xx and failures
red, cancelled grey), time and environment.

- **Search runs in Go**, over the store (`HistoryService.List`), not over
  the entries the UI has loaded: typing waits 250 ms, then asks for the
  first page. The summary search reads only the index in memory. *Also
  search headers and bodies* (`deep`) reads the entries whose summary does
  not match from their files, one segment file at a time (at most the 64
  MiB the history keeps), so it is slower on a large history; the page is
  still capped at 500. It searches the header fields, the text bodies
  (not Base64 ones) and the final URL, and the template's rows and texts.
- **No secret is found or shown.** Only what the history keeps is
  searched, and it was masked when it was recorded. Each text is masked
  again with the secrets PostSonde knows now (the ones `Example` uses,
  [below](#response-examples)) before the text is looked for in it, so an
  entry recorded before a value became secret does not match it either,
  and the URL and error of the entries returned are masked again too.
  Searching for `••••••` finds the entries where something was masked,
  nothing more.
- **Filters** (the filter button): method; status class (`1xx` to `5xx`,
  or *Failed or cancelled*); kind (HTTP, GraphQL, replayed requests);
  environment (the names the kept entries have); a date range (from and
  to, whole local days: `since` is the start of *from*, `until` the start
  of the day after *to*); and *Only “name”*, the request of the active
  tab. *Reset filters* clears them. The status line counts the matching
  entries and those kept (a polite live region).
- **Paging:** 50 entries per page; *Load more* asks Go for the next one
  (`offset`). When `history:changed` arrives, the loaded entries are read
  again (as many as are shown, at most 500), after 150 ms of quiet.
- **Keyboard:** the list is a `listbox` with a `group` per day and one Tab
  stop (roving). Up/Down, Home/End and PageUp/PageDown move, Down from the
  search field enters the list, Enter restores the entry (into its
  request when it can, else as a scratch request), Delete deletes it,
  Shift+F10 or the Menu key opens its context menu (also a right-click):
  *Restore into “name”*, *Open as scratch request*, *Delete entry* and
  *Clear history of “name”…*. A double-click restores.
- **Clear:** the trash button offers *Clear history of “name”…* (the
  active tab's request, `ClearRequest`) and *Clear all history…*
  (`Clear`); both ask first (danger) and cannot be undone. Deleting one
  entry does not ask.

### Restore

`HistoryService.Restore(id)` turns an entry into the request fields to
write (`history.Restore`), and the UI writes them
(`commands/history.ts`, `lib/history.ts` `restorePatch`):

- **What is restored.** An entry with a template gives the request as its
  editor had it: `{{variables}}` as written, the query and path parameter
  rows, header rows (disabled ones too) and the body; a GraphQL one its
  URL, method, header rows, query, variables and operation name
  (`templated: true`). The auth, settings, assertions, extractions and
  examples are not part of it: the request keeps its own. An entry
  without one (a replay, or one recorded by an older PostSonde) gives the
  request **as sent** (`templated: false`): variables resolved, the query
  string only in the URL (the UI makes the parameter rows from it), no
  auth, and without the header fields the client, the transport, the
  cookie jar or the auth wrote (`Host`, `Content-Length`,
  `Transfer-Encoding`, `Connection`, `Accept-Encoding`, `Cookie`,
  `Authorization`, `Proxy-Authorization`, a PostSonde `User-Agent`,
  HTTP/2 pseudo-headers); a text body becomes a raw body typed from its
  Content-Type, a Base64 one is not restored. A GraphQL entry as sent is
  read back from its JSON body (or the query string of a GET). The toast
  says when an entry was restored as sent.
- **Into the request it was sent from** (the default when it still exists,
  is of the same kind and is not read-only): its draft is loaded first
  when its tab is not open, the fields are applied as **one edit** of its
  undo history (`editRequest` with `history: "separate"`), auto-save
  writes it, and its tab opens. The toast's *Undo* (or ⌘Z in its editor, outside a text
  panel) takes it back.
- **As a scratch request** (always offered; the only choice for a replay
  or a request that is gone): a new scratch request of the entry's kind,
  named "*name* (history)", is created, filled the same way and opened
  ([collection.md](collection.md#scratch-requests)); *Save to…* moves it
  into the project.
- **Masked secrets are never written back.** Go masks the fields again
  with the secrets known now, then takes every `••••••` out and lists
  where it found one in `hidden` (`section`, row `name`, `reason`
  `masked`); a text cut at 64 KiB is listed as `clipped`, a binary body
  as `binary`. Restoring into the request, a hidden URL (or query row)
  keeps the request's URL and rows, a hidden body, query or variables
  keeps the request's own, and a hidden row takes the value of the
  request's row of the same name. A scratch request gets what is left
  once the masks are out (`Bearer `, `{"password":""}`). The toast names
  the fields that were not restored.

## Response examples

A request's history is also where its **earlier responses** (HTTP-39)
come from, and **response examples** (HTTP-38) are made from them.

- **Which entry a response is.** Every send the history records gets its
  entry ID when the response arrives, before it goes to the UI:
  `ResponseDTO.historyId` ([http-client.md](http-client.md#service-httpservice)).
  The entry is appended just after. The responses of a run (repeated
  sends, HTTP-27, and the folder runner, RUN-01) and a send without an
  open project have none; a run's recorded sends are in the list below.
- **Earlier responses.** The response pane lists the request's newest
  entries with a response (`List({requestId, limit: 20})`, failed and
  cancelled sends left out; `commands/responseHistory.ts`), and keeps the
  list up to date from `history:changed`. Picking one shows it in the
  response view until the next send: the UI reads it with `Get` and has
  Go turn it into a `ResponseDTO` (`HTTPService.StoredResponse`), so its
  body is decoded, highlighted and pretty-printed like a received one.
  It is what the history kept: at most 64 KiB of the body (the view says
  so), no test results, no timing or connection, and the timeline has
  one exchange with the request as sent.
- **Saving an example.** *Save as example* on the latest response (by
  its `historyId`) or an earlier one calls `Example`: the entry's
  response (status, reason phrase, headers, body with its size and
  `clipped`) and its request as sent (method, URL, header fields without
  HTTP/2 pseudo-headers, a text body as a raw body typed from its
  Content-Type) become a `ResponseExampleDTO`. The UI adds it to the
  request's draft, named by its status and made unique (`200 OK (2)`),
  as one undoable edit, and auto-save writes it into the request file
  ([file-format.md](file-format.md#response-examples)). Nothing else is
  written.
- **Secrets.** The entry was masked when it was recorded (above).
  `Example` masks it **again** with the secrets PostSonde keeps now,
  the ones copied logs are masked with
  ([privacy.md](privacy.md#copied-and-saved-logs)): the values in the
  open project's local secret files (typed auth secrets, secret
  variables of every environment, folder and the globals) and the
  app-wide ones (certificate passphrases), by value and URL-escaped,
  plus the credentials of `Authorization` and `Proxy-Authorization` by
  name. So an entry recorded before a value became secret, or by an
  older PostSonde, still gives an example without it. A Base64 body is
  masked in its decoded bytes. The same limits hold: a secret sent
  transformed is not recognised.
- **Comparing.** *Compare…* shows two of the latest response, the earlier
  ones and the examples side by side, with the differences in status,
  headers and body marked (HTTP-40, [response-diff.md](response-diff.md)).
- **Mock routes.** *Create mock route…* on the response shown (the latest,
  an earlier one or an example) and *Mock* on each example make a mock
  server route that answers with it (MOCK-03,
  [mock-routes.md](mock-routes.md#routes-from-a-response)); a history
  entry goes through `Example` first, so the route is masked like an
  example, and then again with the secrets known now.
- **Size.** Examples keep what the entry kept, at most 64 KiB of each
  body (`MaxBody`, the same as `projectfile.ResponseExampleMaxBody`),
  inline in the request file; there are no side files.

## Response references

`{{$response REQUEST …}}` (DYN-06,
[templates.md](templates.md#response-references)) reads another request's
last response. The one received this session is kept in memory as
received; after a restart, or when the history has a newer one, the
request's newest entry **with a response** is read (`List({requestId})`
then `Get`), as it was recorded: masked and clipped, so a masked value is
not used and a body cut at 64 KiB is not read. A request a reference sends
first is recorded like any send, in its own entry, before the request
that needed it. Clearing a request's entries or the whole history
(`ClearRequest`, `Clear`) also drops the response kept in memory for it;
deleting one entry does not.

## Not handled yet

- Saving an entry straight into a folder (NAV-03): it opens as a scratch
  request, which *Save to…* moves into the project. Comparing or exporting
  entries from the panel: HAR export (EXP-04) is in the tree's context
  menu ([har.md](har.md#export)), comparing in the response pane
  ([response-diff.md](response-diff.md)).
- Restoring the auth, settings or body examples of a request: the
  template does not keep them.
- The list is paged, not virtualized: each page adds 50 rows to the DOM.
- A retention setting. Clearing the history with the other local data
  (PRV-13) is Settings, Privacy, Local data
  ([privacy.md](privacy.md#clearing-local-data)), which uses `Clear` for
  the open project.
- Two app instances with the same project open write to the same files
  without a lock: each sees the other's entries only after reopening, and
  `Get` may fail with `history.read_failed` for an entry whose position
  the other instance moved.
