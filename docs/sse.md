# Server-Sent Events

PostSonde connects to `text/event-stream` endpoints (SSE-01..04). A request of
kind `sse` is sent like an HTTP request, through the HTTP client; its
response body is parsed by `internal/core/realtime/sse`, written with the
standard library only, as it arrives, and every event is shown live in the
request's event log.

```
sse request ──▶ HTTPService build (vars, auth, folder headers, settings)
                  │  http.NewStream: + Accept: text/event-stream
                  ▼
httpclient.SendStream ──▶ head ──▶ "open" item (status, headers)
                  │ body, as read (gzip/deflate undone)
                  ▼
sse.Parser.Feed(chunk) ──▶ []Event                    ──▶ "event" items
         ├─▶ Options.OnComment(Comment)                ──▶ "comment" items (heartbeats)
         └─▶ Options.OnField(Field)                    ──▶ "retry" items, "notice" for ignored id/retry
                  │ in stream order, batched
                  ▼
sse:op:data ──▶ commands/sseStreams.ts ──▶ MessageLog (Events tab)
```

| Piece | Where |
|---|---|
| Request kind `sse` | `projectfile.SSERequest` ([file-format.md](file-format.md#sse-request-kind-sse)) |
| Parser | `internal/core/realtime/sse` ([below](#parser-sse)) |
| Streaming send | `httpclient.SendStream` ([http-client.md](http-client.md#streaming-responses)) |
| Building the request | `http.NewStream` in `internal/app/services/http/stream.go` |
| Service | `SSEService` in `internal/app/services/sse` ([Client](#client)) |
| UI | `commands/sseStreams.ts`, `RequestEditor` (`RequestSettings`), `SseResponsePanel`, `SseMergedView`, `lib/sseMerge.ts` ([UI](#ui)) |

## Request kind `sse`

An SSE stream is a request kind of its own, not a mode of HTTP requests:
it is created from New request (SSE),
shows the `SSE` badge in the tree, and its tab has **Connect** instead of
Send and the event log instead of the response view. Its file has the
members of an `http` request (`method`, `url`, `params`, `pathParams`,
`headers`, `body`, `auth`, `settings`), so it is edited with the same
editor and built with the same code: `projectfile.SSERequest.HTTP()` turns
it into the `HTTPRequest` it is sent as. `method` defaults to GET and is
left out of the file then; a POST with a JSON body (a streaming API that
takes a prompt) works too. Existing files are untouched: `sse` is a new
kind, and older versions show an `sse` file as a read-only item of an
unknown kind.

## Client

`SSEService.Connect({requestId, allowUnresolved, lastEventId})`
(`internal/app/services/sse`, bound) builds the request and starts an
operation of the `sse` domain ([events.md](events.md#event-streams-sse)),
returning its `opId` at once.

- **Building.** `http.NewStream` loads the request (edits not written yet
  included) through `collection.LoadHTTPSource`, which marks an `sse`
  request (`HTTPSource.SSE`), and builds it with `HTTPService`'s own build
  step: `{{variables}}` of the active environment and folders, the
  resolved auth (inherited from folders too), the folders' headers, the
  request's settings and the app's TLS default. Undefined variables fail
  the call with `template.unresolved`, all names together, so the UI asks
  whether to connect anyway (ENV-11). Without an enabled `Accept` row,
  `Accept: text/event-stream` is added. The project's cookie jar sends and
  stores cookies as for any HTTP send. `HTTPService.Send` refuses an `sse`
  request (`http.sse_request`).
- **Connecting.** `httpclient.SendStream` sends it on the operation's
  context (the dialer's user-operation guard, PRV-05). The request's
  timeout (`settings.timeoutMs`, or the app's default) bounds connecting
  and waiting for the response headers only; after that the stream runs
  until the server ends it or the user stops it. Redirects follow the
  request's settings.
- **The response.** The first item is `open`: the method and URL as sent
  (secrets masked), the status, protocol and headers. A status other than
  2xx is not read as a stream: up to 64 KiB of its body is shown as a
  `notice` and the operation fails with `sse.bad_status`. A `Content-Type`
  other than `text/event-stream` is read anyway, after a `notice` saying
  so. A `Content-Encoding` of gzip or deflate is undone (only sent when the
  request asked for it); any other fails with `sse.unsupported_encoding`.
- **Items.** The body is read 32 KiB at a time and fed to the parser. What
  a chunk produced is sent in stream order (the parser reports comments
  and fields while parsing and events after, so they are sorted by their
  offset in the stream), all with the time the chunk arrived:
  - `event`: `index` (from 1), `event` (`message` unless set), `id` (the
    last event ID at dispatch, so it carries over), `data` (the data lines
    joined by LF; cut at 1 MiB with `truncated`, `size` is the full
    length), `retryMs` when the event's block had a valid `retry`, and
    `line`, the line its block starts on.
  - `comment`: the text after the colon (heartbeats).
  - `retry`: every valid `retry` field, with its value in `retryMs`, since
    it takes effect even in a block that dispatches nothing.
  - `notice`: an ignored `retry` (not digits) or `id` (contains NULL).
  - `reconnect`: a connection ended and the stream reconnects
    ([Reconnecting](#reconnecting)).
  - `end`, last: `reason` (`closed` by the server, `stopped`, `error`),
    `events`, `bytes` (decoded body bytes), `id` (the last event ID),
    `retryMs` (the latest the server set), `discarded` (the stream ended
    inside an event, which is dropped), `reconnects` and `elapsedMs`;
    counts are over all connections.
- **Done.** `sse:op:done` is `ok` when the server ended the stream,
  `cancelled` after `OpsService.Cancel` (Stop, the tab closed, the app
  quitting), `error` otherwise: a connection or TLS error, a timeout before
  the headers (named with where it was set, as for HTTP), a dropped
  connection (`httpclient.body_incomplete`) when not reconnecting,
  `sse.bad_status`, `sse.reconnect_failed`, or a parser limit
  (`sse.line_too_long`, `sse.event_too_big`, 16 MiB each).
- **Reconnecting** (SSE-03): see [below](#reconnecting).
- **In a folder run** (RUN-01) an sse request is not connected through
  `Connect`: `Probe` opens it once, without reconnecting, and reads it
  until the server ends it or the run's window passes, counting the
  events (`sse.no_response` when nothing answered in time;
  [runner.md](runner.md#running)).

The item shapes are pinned by
`internal/app/services/sse/testdata/dto/stream.golden`.

### Reconnecting

A stream resumes from its last event with the `Last-Event-ID` header, as
the WHATWG standard's `EventSource` does, in two ways:

- **Automatically**, when the request's `settings.reconnect.enabled` is
  on ([file-format.md](file-format.md#sse-request-kind-sse); off by default,
  so files written before it behave as before). When a connection ends,
  whether the server closed it or it dropped (`httpclient.body_incomplete`,
  a reset, a timeout before the headers of a later attempt), the operation
  does not end: it sends a `reconnect` item and, after the wait, sends the
  request again (as built at Connect; the cookie jar as it is then) with
  `Last-Event-ID: <last event ID>` when there is one, replacing a row of
  that name the request has. The new connection's items (`open`, events…)
  follow in the same operation, the event `index` and the counts carry on,
  and the parser starts with that ID, so events without an `id` field
  carry it as the standard says.
- **By hand**: `ConnectInputDTO.lastEventId` makes the first connection
  send it. The response pane's **Resume** does this after a stream with a
  last event ID ended.

The **wait** is the server's latest `retry`, else the policy's `delayMs`
(3000 ms by default). Attempts in a row that fail, that is end without a
single byte of the stream (refused, a non-stream error, a server that
closes at once), double it each time, up to one minute (a larger `retry`
from the server is kept as is): with 3000 ms, 3 s, 6 s, 12 s… A connection
that delivered anything starts the count again, so a server that closes
after every event is followed for as long as it keeps sending. After
`maxAttempts` failed attempts in a row (5 by default; 0 is no limit) the
stream gives up with `sse.reconnect_failed`, whose kind is the last
attempt's error's (`protocol` when it connected but got nothing).

It **does not reconnect** after:

- a status other than 2xx: `sse.bad_status`, as for the first connection;
- **204 No Content**, the standard's way for a server to say "stop": a
  `notice` says so and the stream ends as `closed`, done `ok`;
- a response whose Content-Type is not `text/event-stream` (read anyway,
  then a notice "Not reconnecting…");
- a parser limit or an undecodable compressed stream.

**Stop always wins**: `OpsService.Cancel` ends the stream at once, also
while it waits to reconnect (the timer is dropped, nothing more is sent),
with the end reason `stopped`.

The `reconnect` item: `reason` (`closed` by the server or `error`),
`text` (the error's message, or "The server ended the stream."), `id`
(the Last-Event-ID the next attempt sends), `attempt` (from 1, attempts in
a row without a byte), `maxAttempts` (0: no limit) and `delayMs`. The
`end` item adds `reconnects`, the reconnections made. The service's clock
is injectable (`SSEService.after`), so `reconnect_test.go` checks the
waits exactly: a stream dropped mid-event resumes after the server's
`retry` with the last ID, a normal close resumes too, 204 stops, failed
attempts back off and give up, a non-2xx answer to a reconnect stops, and
Stop during the wait ends the stream.

| Code | Kind | When |
|---|---|---|
| `sse.bad_status` | `protocol` | The response is not 2xx. Details: `status`. |
| `sse.unsupported_encoding` | `unsupported`, `protocol` | A `Content-Encoding` other than gzip, deflate or identity, or a compressed stream that does not decode. Details: `encoding`. |
| `sse.reconnect_failed` | the last attempt's, or `protocol` | Every reconnection attempt the policy allows failed in a row (SSE-03). Details: `attempts`. |
| `sse.unavailable` | `unsupported` | The service was built without what connecting needs. |
| `http.not_sse` | `invalid_argument` | `NewStream` for a request that is not `sse`. |
| `http.sse_request` | `invalid_argument` | `HTTPService.Send` for an `sse` request. |

## UI

- **Editor.** An `sse` tab shows the request editor (method, URL with the
  Params table, Headers, Body, Auth, Settings), whose button is
  **Connect** (⌘↩ too) and **Stop** while the stream runs
  ([frontend.md](frontend.md#request-editor)). Connect first waits for the
  queued edits to reach Go, then calls `SSEService.Connect`; undefined
  variables ask "Connect with an undefined variable?" as for WebSocket.
- **Stream slot.** `commands/sseStreams.ts` keeps one `SseStream` per
  request outside React: its state (status, response head, events, data
  bytes, last event ID, retry, times, why it ended) and a `MessageSource`
  of the stream. Switching tabs keeps the stream running; closing the tab
  cancels its operation and drops the log (`onTabClosed`). A new Connect
  clears the log.
- **Response pane.** `components/response/SseResponsePanel`: "Not
  connected" before the first connect, the error when the stream failed
  before the server answered, otherwise the status line (status, live
  time, events, data size, last event ID, retry, Stop) and the tabs
  **Events** and **Headers**. Events is the shared, virtualized
  `MessageLog`: an event row shows its event type as the badge and its
  data as the preview; the detail pane shows the data (pretty JSON when it
  parses) with `event`, `index`, `id`, `retry` and `line`. Comments and
  retry fields are received rows of their own kind, the open, notices,
  reconnects ("The server ended the stream. Reconnecting in 3000 ms with
  Last-Event-ID 42 (attempt 1 of 5)."), the end and errors system rows.
  The log's filters (direction, kind, search) and follow mode work as for
  WebSocket.
- **Reconnecting** (SSE-03). The request's Settings sub-tab has a
  Reconnect group: "Reconnect with Last-Event-ID", Attempts and Delay
  ([Reconnecting](#reconnecting)). While the stream waits, the status line
  says "Reconnecting" with a countdown and the attempt, Stop stays, and
  the count of reconnections made follows. After a stream with a last
  event ID ended (stopped, closed, failed), **Resume** connects again
  sending it; the log and the counts carry on instead of starting over
  (the event `index` restarts at 1 in the resumed operation).
- **Merged** (SSE-04). The Merged tab joins the data of the events into
  one text, for token streams (an LLM completion sends a word or less per
  event). `lib/sseMerge.ts` does the joining; the tab reads the events
  from the stream's log, so it follows the stream live and holds what the
  log holds (the last 50,000 messages; a note says when older events were
  dropped; an event's data is cut at 1 MiB as in the log). Options:
  - **Events**: all types, or one type seen in the stream.
  - **Text**: *Auto* (the default) takes the text of the common token
    stream shapes from JSON data, trying in order `choices.0.delta.content`
    (OpenAI chat), `choices.0.text` (completions), `delta.text` (Anthropic
    content deltas), `delta.partial_json`, `message.content` (Ollama chat),
    `response` (Ollama generate), `token.text` (TGI), then `text`,
    `content`, `token` and `delta` when they are strings; a JSON string is
    taken as its value; JSON without any of them (a `message_start`, a
    usage report) and the `[DONE]` sentinel give nothing; data that is not
    a JSON object, array or string is taken as it is. *Data as is* joins
    every event's data unchanged. *JSON field* takes one field, a dotted
    path with array indexes as numbers (`choices.0.delta.content`,
    `choices[0].text`, `$.delta.text`); events without it are left out.
  - **Join with**: nothing (token streams) or new lines.
  - **Copy** puts the text on the clipboard. A line above the text says
    how many of the chosen events gave text and how many were left out.

## Assertions

TEST-08. An sse request's assertions (its Tests sub-tab, rendered when
it connects, like its URL) are checked against its events when the
stream ends: the `end` item's `tests`, shown in the response view's Tests
tab. An event is a message whose body is its data, with the headers
`Event` (its type) and `Id`; the stream's `status` and `header` are the
response's. `SSEService.CheckAssertions({opId})` (Check now) checks a
running stream or one of the last that ended, with the assertions as they
are then; a folder run checks them on the events of its window
([runner.md](runner.md)). See [assertions.md](assertions.md#streams).

## Parser: `sse`

The parser follows the WHATWG HTML standard, section "Interpreting an event
stream", exactly. It is incremental: feed it the body as it arrives, in
chunks of any size, and it returns the events each chunk completes. The
result never depends on where the chunks are split (tested byte by byte, in
random splits and by fuzzing).

```go
p := sse.NewParser(sse.Options{
	OnComment: func(c sse.Comment) { ... }, // optional
	OnField:   func(f sse.Field) { ... },   // optional
})
for chunk := range body {
	events, err := p.Feed(chunk) // events dispatched by this chunk, in order
	...
}
discarded := p.Close()           // end of stream: an incomplete event is dropped
id := p.LastEventID()            // Last-Event-ID for the reconnect
retry, ok := p.Retry()           // reconnection time, if the server set one

// Or over an io.Reader:
for ev, err := range sse.Events(resp.Body, sse.Options{}) { ... }
```

| Item | Purpose |
|---|---|
| `NewParser(Options) *Parser` | A parser at the start of a stream. Not safe for concurrent use. |
| `Feed([]byte) ([]Event, error)` | Parse the next chunk. On a limit error it returns the events before the error and stays failed. |
| `Close() bool` | End of stream. Reports whether an incomplete event was discarded. `Feed` after `Close` fails with `sse.closed`. |
| `LastEventID()`, `Retry()`, `Offset()`, `Err()` | Parser state: the committed last event ID, the latest valid `retry`, bytes fed, the failure. |
| `Events(io.Reader, Options) iter.Seq2[Event, error]` | Reads 32 KiB at a time and yields events as soon as they are complete; a read or limit error is yielded once and ends the iteration. |
| `Options{MaxLineSize, MaxEventSize, OnComment, OnField, LastEventID}` | Limits (0 = 16 MiB default, negative = none), timeline callbacks, called during `Feed`, and the last event ID the stream starts with (the one a reconnection sent, SSE-03). |
| `Event{Type, Data, ID, Retry, Start, End}` | `Type` is `message` unless an `event` field set it. `ID` is the last event ID at dispatch. `Retry` is set when this event's block had a valid `retry`. `Start` and `End` are the first line of the block and the blank line that dispatched it. |
| `Comment{Text, Pos}` | A line starting with `:`; `Text` is everything after the colon. |
| `Field{Name, Value, Pos, Ignored}` | Every field line, applied or not. `Ignored` is `unknown_field`, `id_contains_null` or `invalid_retry`, or empty. |
| `Position{Offset, Line}` | Byte offset in the raw stream (counting the BOM and line breaks) and line number from 1. |

### Rules

- **Decoding.** UTF-8 with replacement as the WHATWG decoder does it: one
  U+FFFD per maximal invalid subpart (`"\xE2\x82A"` is `"�A"`). One
  U+FEFF BOM is dropped at the start of the stream only; a second one, or
  one later in the stream, is kept (a BOM before a field name makes it an
  unknown field).
- **Lines** end with CRLF, LF or CR. A CR at the end of a chunk ends its
  line immediately, so an event is dispatched without waiting for the next
  chunk; if that chunk starts with LF, the LF belongs to the same line
  break and does not make a blank line.
- **Fields.** A line starting with `:` is a comment. Otherwise the name runs
  to the first `:` (the whole line when there is none, with an empty
  value), and one U+0020 space after the colon is removed. Names are
  case-sensitive.
  - `event`: sets the event type buffer.
  - `data`: appends the value and LF to the data buffer.
  - `id`: sets the last event ID buffer, unless the value contains U+0000.
    The buffer is not reset by a dispatch, so an ID carries over to later
    events.
  - `retry`: one or more ASCII digits set the reconnection time in
    milliseconds, immediately (even if the block is never dispatched).
    Anything else, an empty value, or a value too large for a
    `time.Duration` is ignored.
  - anything else is ignored (and reported through `OnField`).
- **Blank line.** The last event ID buffer becomes the last event ID
  (`LastEventID`), even when no event is dispatched. If the data buffer is
  empty, the event type buffer is cleared and nothing is dispatched.
  Otherwise the final LF is removed and an `Event` is produced; the data and
  type buffers are cleared.
- **End of stream.** Lines after the last blank line, including an
  unfinished line, are discarded. A `retry` in them has already taken
  effect; an `id` in them was never committed.

### Limits and errors

| Code | Kind | When |
|---|---|---|
| `sse.line_too_long` | `protocol` | A line (without its line break, with a leading BOM) is longer than `MaxLineSize`. Detected as soon as the unfinished line grows past the limit. Details: `limit`, `line`, `offset`. |
| `sse.event_too_big` | `protocol` | The data buffer (values plus an LF each) would grow past `MaxEventSize`. Details: `limit`, `line`, `offset`. |
| `sse.closed` | `invalid_argument` | `Feed` after `Close`. |
| `sse.read_failed` | from the cause | `Events` could not read the stream. |

### Tests

`parser_test.go` has inputs modelled on the examples in the standard (the
stock ticker, the comment/`id`/incomplete-event stream, empty `data` lines,
`data:test` vs `data: test`, event types) and edge cases: CR, LF and CRLF
mixes including a CRLF split across chunks, BOM handling, invalid UTF-8,
`id` with NULL, invalid `retry`, positions and limits. Every case is parsed
whole, byte by byte and in random splits, and must give the same events,
comments, fields and state. `FuzzParse` does the same with fuzzed input and
checks events are valid UTF-8 without line breaks; `FuzzDecodeUTF8` checks
the decoder.

```
go test ./internal/core/realtime/sse/
go test -run '^$' -fuzz FuzzParse -fuzztime 60s ./internal/core/realtime/sse/
```
