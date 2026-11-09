# GraphQL subscriptions

PostSonde runs GraphQL subscriptions (and, if wanted, queries and mutations)
over WebSocket, with the `graphql-transport-ws` protocol or Apollo's legacy
`subscriptions-transport-ws` (GQL-31), or over Server-Sent Events (GQL-32).
The transports are written in Go with the standard library only, on top of
the WebSocket client ([websocket.md](websocket.md)) and the SSE parser
([sse.md](sse.md)). Each request picks the transport of its queries,
mutations and subscriptions separately ([below](#transport-per-operation-gql-35)).
The GraphQL editor runs a subscription with its connection params and shows
a live event log ([below](#running-subscriptions-gql-33-gql-34)).

## Transport: graphql-transport-ws

`internal/core/graphql/transportws` implements the client side of the
`graphql-transport-ws` protocol from the graphql-ws project (GQL-30).

```
transportws.Session
  wsclient.Client (subprotocol graphql-transport-ws) ──▶ OnMessage ──▶ parse + check ──▶ Subscription.C()
        ▲                                                                   │
        └──── connection_init, subscribe, complete, ping/pong ◀─────────────┴──▶ Options.OnEvent (every message)
```

```go
s, err := transportws.Dial(ctx, transportws.Options{
	WS:                    wsclient.Options{URL: "wss://api.example.com/graphql", Dialer: dialer},
	ConnectionParams:      json.RawMessage(`{"authToken":"..."}`),
	ConnectionInitTimeout: 3 * time.Second, // default
	OnEvent:               func(ev transportws.Event) { ... },
})
sub, err := s.Subscribe(ctx, transportws.SubscribeRequest{
	Query:     "subscription ($room: ID!) { messages(room: $room) { text } }",
	Variables: json.RawMessage(`{"room":"1"}`),
})
for r := range sub.C() { ... }   // next..., then error, complete or closed
err = sub.Unsubscribe()           // sends complete if still running
err = s.Close(ctx, 1000, "")
```

| Item | Purpose |
|---|---|
| `Dial(ctx, Options) (*Session, error)` | WebSocket handshake (ctx bounds it), subprotocol check, `connection_init`. Returns before `connection_ack`. |
| `Options{WS, ConnectionParams, ConnectionInitTimeout, PingInterval, EchoPingPayload, CloseTimeout, OnEvent}` | `WS.Subprotocols` is replaced; the other `WS` callbacks still run after the session's. |
| `Session.Subscribe(ctx, SubscribeRequest{Query, OperationName, Variables, Extensions})` | Waits for `connection_ack` (ctx bounds the wait only), sends `subscribe` with a new id. |
| `Subscription.C()`, `ID()`, `Unsubscribe()` | Results in order; `C` closes after the terminal result or on `Unsubscribe`. |
| `Result{Kind, At, Payload, Errors, HasErrors, Err}` | `next` (payload is the ExecutionResult as bytes, `Errors` its decoded `errors`), `error` (payload is the GraphQL error array), `complete`, `closed` (the session ended first; `Err` says why). |
| `Session.Ping(payload)`, `Close(ctx, code, reason)` | Manual ping; close handshake, ends active subscriptions with `closed`. |
| `State()`, `Acked()`, `AckPayload()`, `Done()`, `Err()`, `CloseInfo()`, `Client()` | `initialising`, `ready`, `closing`, `closed`; the WebSocket client for handshake details. |
| `Event{At, Direction, Type, ID, Payload, Data, Binary, Err, Note}` | Every protocol message sent or received, as on the wire, for the event and message logs. |
| `CloseCodeName(code)` | Meaning of a close code, e.g. `unauthorized` for 4401. |

### Protocol rules

- **Subprotocol:** only `graphql-transport-ws` is offered, and the server
  must select it. A 101 without it is closed with **4406** and `Dial` fails
  with `transportws.subprotocol_not_selected` (the server may only speak the
  legacy protocol).
- **Initialisation:** `connection_init` carries `ConnectionParams` (a JSON
  object or `null`; empty sends no payload). Without `connection_ack` within
  `ConnectionInitTimeout` (3 s, negative waits forever) the client closes
  with **4408** `Connection initialisation timeout`.
- **Subscribe before ack:** `Subscribe` blocks until the ack arrives, its
  context ends (`transportws.cancelled`) or the session ends (the session's
  error). Nothing is queued on the wire.
- **Ids:** decimal, monotonic per session (`1`, `2`, ...), never reused, so a
  late message for an old id cannot reach a new subscription.
- **Keep-alive:** a server `ping` is answered with `pong` (no payload, or the
  ping's with `EchoPingPayload`), also before the ack. `PingInterval` (off by
  default) sends pings; pongs are only logged.
- **Results:** `next`, then exactly one terminal result. Results are queued
  per subscription without limit, so a slow reader never stalls the
  connection or other subscriptions; `Unsubscribe` drops what is unread.
- **Unsubscribe** sends `complete` only for a subscription that is still
  running (not after `error`, `complete` or the end of the session).
- **Invalid messages** from the server close the connection with **4400**
  `Invalid message received`; the session fails with
  `transportws.invalid_message`, the event has `Err` set and later messages
  are logged with note `ignored`. Invalid means: not a JSON object, no string
  `type`, an unknown type, `connection_init` or `subscribe` from the server,
  a second `connection_ack`, `next`/`error`/`complete` before the ack or
  without a non-empty string `id`, a `next` payload that is not an object, an
  `error` payload that is not a non-empty array of objects with `message`,
  an `ack`/`ping`/`pong` payload that is not an object or `null`, or a binary
  message. Unknown fields are allowed.
- **Unknown ids:** `next`, `error` or `complete` for an id without an active
  subscription are ignored (they cross an `Unsubscribe`) and logged with note
  `unknown_id`.

### Close codes

| Code | Meaning | Error kind when the server closes |
|---|---|---|
| 1000, 1001 | normal closure, going away | `network` |
| 4400 | invalid message | `protocol` |
| 4401 | unauthorized (subscribe before ack) | `permission_denied` |
| 4403 | forbidden (connection params rejected) | `permission_denied` |
| 4406 | subprotocol not acceptable | `protocol` |
| 4408 | connection initialisation timeout | `timeout` |
| 4409 | subscriber for the id already exists | `protocol` |
| 4429 | too many initialisation requests | `protocol` |
| 4500 | internal server error | `network` |

4004 (bad response), 4005 (internal client error), 4499 (terminated) and 4504
(connection acknowledgement timeout) are the reference client's codes and
are named by `CloseCodeName` too. This client uses 4400 and 4408 as the
protocol document describes instead.

### Errors

| Code | Kind | When |
|---|---|---|
| `transportws.invalid_argument` | `invalid_argument` | Connection params, variables, extensions or ping payload not a JSON object. |
| `transportws.subprotocol_not_selected` | `protocol` | The server did not select the subprotocol; closed with 4406. |
| `transportws.invalid_message` | `protocol` | The server broke the protocol; closed with 4400. |
| `transportws.ack_timeout` | `timeout` | No `connection_ack` in time; closed with 4408. |
| `transportws.closed_by_server` | by close code (above) | Every close the server started, 1000 included; details `code`, `reason`. |
| `transportws.session_closed` | `cancelled` | `Close` was called: the result of active subscriptions, `Subscribe` afterwards. |
| `transportws.cancelled` | `cancelled` / `timeout` | `Subscribe`'s context ended before the ack. |
| `transportws.connection_error` | `permission_denied` | Legacy protocol only: the server answered `connection_init` with `connection_error` ([below](#transport-subscriptions-transport-ws-gql-31)). |
| `wsclient.*`, `wsframe.*` | from wsclient | Handshake failures, a dropped transport (`wsframe.abnormal_closure`), close timeouts. |

`Session.Err()` is nil after `Close` (or `Client().Abort()`); the `closed`
results then carry `transportws.session_closed`.

### Concurrency

`Session` and `Subscription` methods are safe for concurrent use. `OnEvent`
is serialized and in wire order; received events run on the read goroutine,
so `OnEvent` must not call `Close`. A sent event is reported just before its
message is written.

### Tests

`go test -race ./internal/core/graphql/transportws`, against an `httptest`
server that upgrades by hand and speaks the server side with wsframe:

- Happy path (params, ack payload, subscribe payload byte for byte, three
  `next`, `complete`, event log order), five concurrent subscriptions with
  interleaved results, unsubscribe mid-stream with a crossing `next`
  (unknown id), `next` with errors and an `error` result, pings both ways
  (before the ack, echo, interval), ack timeout ⇒ 4408, no subprotocol ⇒
  4406 and a `Dial` error, server closes with 4401/4403/4409/4500/1000
  (code, reason, kind, subscriptions end with `closed`), 18 kinds of invalid
  messages ⇒ 4400, subscribe waiting for the ack, context cancellation,
  local close, dropped transport, an unread subscription not stalling
  another.

## Transport: subscriptions-transport-ws (GQL-31)

The same package speaks Apollo's legacy protocol with
`Options.Protocol: transportws.ProtocolLegacy`: the `Session`, its
subscriptions, results, events and errors are the ones above; only the wire
differs.

| | graphql-transport-ws | subscriptions-transport-ws |
|---|---|---|
| Subprotocol | `graphql-transport-ws` | `graphql-ws` (`transportws.LegacySubprotocol`) |
| Start an operation | `subscribe` | `start` |
| Results | `next` | `data` (`ResultNext`) |
| Stop an operation (client) | `complete` | `stop` |
| Keep-alive | `ping`/`pong` both ways | `ka` from the server; the client sends none |
| Connection refused | a close code (4403) | `connection_error`, then the server closes |
| Leaving | the close frame | `connection_terminate`, then the close frame |

- **Subprotocol:** only `graphql-ws` is offered; a 101 without it is closed
  with 4406 and `Dial` fails with `transportws.subprotocol_not_selected`
  (the hint suggests the other protocol).
- **`connection_init`, `connection_ack` and the ack timeout** work as in
  the newer protocol (the payload is the connection params; 3 s, then 4408).
- **`ka`** is logged whenever it comes (servers send one right after the
  ack and then every few seconds). `Ping` fails with
  `transportws.invalid_argument` and `PingInterval` is ignored.
- **`connection_error`** ends the session with
  `transportws.connection_error` (kind `permission_denied`, the payload's
  `message` in the error) and the client closes with 1000.
- **`error`** carries one GraphQL error object or an array of them; both
  become `Result.Errors`. An `error` without an id (the reference server's
  answer to a message it could not read) belongs to no operation and is
  logged with note `unknown_id`.
- **`Close`** sends `connection_terminate` first, unless the session is
  already closing after a protocol violation.
- **Invalid messages** close with 4400 as in the newer protocol: anything
  but `connection_ack`, `connection_error`, `ka`, `data`, `error` and
  `complete` (so `ping`, `next` and the client's own types), `data` or
  `complete` without a string id, a `data` payload that is not an object,
  an `error` payload without a `message`.

Tests (`legacy_test.go`, against a test server speaking the server side):
the happy path (subprotocol, params, the `start` payload byte for byte with
extensions, `ka`, `data` with errors, `complete`, `connection_terminate`,
close 1000), stop mid-stream with a crossing `data`, `error` as object,
array and without an id, `connection_error`, a server that only speaks
`graphql-transport-ws`, seven kinds of invalid messages, `Ping`.

## Transport: GraphQL over SSE (GQL-32)

`internal/core/graphql/transportsse` reads the answer of graphql-sse's
"distinct connections" mode: one HTTP request per operation, `POST` with the
JSON body `{query, operationName, variables, extensions}` (or `GET` with
them in the query string) and `Accept: text/event-stream`. The caller sends
the request with the app's HTTP client and hands `Read` the response; the
events are parsed with `realtime/sse`, as the incremental delivery reader
does.

```go
out := transportsse.Read(transportsse.Head{Status: 200, ContentType: ct}, body, transportsse.Options{
	OnEvent: func(ev transportsse.Event) { ... }, // next, complete, keep-alive, response, ignored events
})
switch out.Kind { case transportsse.OutcomeComplete, OutcomeRejected /* out.Errors */, OutcomeFailed /* out.Err */ }
```

- **`next`** events (and events without a name, `message`) carry an
  ExecutionResult: its data must be a JSON object, else reading stops with
  `transportsse.invalid_event`. A `message` event without data is ignored.
- **`complete`** ends the operation. A `complete` without a data line is
  never dispatched by an event stream parser; it counts when the server
  ends the stream right after it.
- **Comments** (`:` lines) are the server's keep-alives: logged as
  `keep-alive`, in stream order with the events.
- **Other events** are logged with note `ignored`.
- **The stream ending** without `complete` fails with
  `transportsse.stream_ended` (kind `network`); a read error (a dropped
  connection, or the request cancelled by Stop) is returned as it is.
- **A response that is not an event stream** (or has a status other than
  2xx) is read whole (up to 16 MiB) and logged as one `response` event
  (note `not_event_stream`): a JSON object with `errors` and no `data` is
  the server rejecting the operation (`OutcomeRejected`, e.g. a validation
  error answered with 400), another JSON object with 2xx is the one result
  (`OutcomeComplete`), other statuses fail with `transportsse.http_status`
  (kind `permission_denied` for 401 and 403, `protocol` otherwise), and a
  2xx body that is not JSON with `transportsse.unexpected_response`.
- **Stop** cancels the request: the server sees the connection end, which
  is how the distinct connections mode stops an operation. The single
  connection mode (reservations, `PUT`/`DELETE` of operations on one
  stream) is not supported.

Tests (`transportsse_test.go`): events split across reads with comments
between them, multi-line data, ignored and unnamed events, `complete`
without data, the failures, every kind of non-stream response, and a real
HTTP server streaming events and seeing the request end when the reader
cancels it.

## Transport per operation (GQL-35)

A GraphQL request picks the transport of each operation type in its
settings, `settings.graphqlTransport` ([file-format.md](file-format.md#settings)):

| Member | Values | Default |
|---|---|---|
| `query`, `mutation` | `"http"`, `"ws"`, `"ws-legacy"`, `"sse"` | `"http"` |
| `subscription` | `"ws"`, `"ws-legacy"`, `"sse"` | `"ws"` |

`request.GraphQLTransportFor(settings, operationType)` resolves it (the
type of the operation that runs; an operation that cannot be told counts as
a query); an unknown value, or `"http"` for subscriptions, is
`request.graphql_invalid_transport` (details `field`
`settings.graphqlTransport.<type>`, `transport`). Files without the member
use the defaults, so they run as before. What each choice does:

| Transport | Queries and mutations | Subscriptions |
|---|---|---|
| `http` | Send (GQL-02), the normal response | refused |
| `ws`, `ws-legacy` | through the subscription slot; the pane shows the result as a GraphQL response, the messages on a second tab | Subscribe, the live log |
| `sse` | Send, with `Accept: text/event-stream` (`request.BuildGraphQLSSE`): the answer is read like an incremental response (GQL-08), so the pane shows the merged result, the parts, headers, timing and tests as for any send | Subscribe, the live log |

Files (GQL-07) go only over HTTP: the WebSocket and SSE builds leave them
out. The extensions (GQL-06) go in every payload: the POST body, the `GET`
query string, `subscribe` and `start`.

## Running subscriptions (GQL-33, GQL-34)

A GraphQL request whose operation that runs is a `subscription` gets
**Subscribe** instead of Send. It runs over the transport the request picks
for subscriptions ([above](#transport-per-operation-gql-35)): WebSocket with
the request's connection params as the `connection_init` payload, or SSE;
the response pane shows a live, timestamped log of every protocol message
until the server completes it or the user presses **Stop**; Subscribe again
starts over. A query or mutation set to run over WebSocket runs the same
way from **Send** and ends with its result.

```
GraphqlEditor ─ Subscribe ─▶ commands/graphqlSubscriptions ─▶ GraphQLService.Subscribe({requestId})
                                 │                               │ http.NewGraphQLSubscription: the transport, then
                                 │                               │ request.BuildGraphQLWS + wsclient.Options, or
                                 │                               │ request.BuildGraphQLSSE + the HTTP client (Stream)
                                 │                               ▼
                                 │              operation graphql.subscription ── transportws.Dial ─▶ Subscribe
                                 │                               │                or Stream.Open ─▶ transportsse.Read
                                 ◀── graphql.subscription:op:data (SubscriptionEventDTO, 50 ms batches)
             Stop ─▶ GraphQLService.StopSubscription({opId}) ─▶ complete/stop, close 1000 (or cancel the request) ─▶ op:done "cancelled"
HttpResponsePanel ─▶ GraphqlSubscriptionView: status line + MessageLog (result payloads via GraphqlBody)
```

### Building the connection

`request.BuildGraphQLWS(file, auth, options)` (`internal/core/request/graphqlws.go`)
builds the handshake like a send: the endpoint, header rows, folder
headers, auth (headers and query parameters), `{{variables}}`, the timeout
(it bounds the handshake) and TLS verification go through `BuildHTTP` as a
GET without a body. Then:

- **URL:** `http://` becomes `ws://` and `https://` `wss://`; `ws://` and
  `wss://` endpoints are kept. Any other scheme is
  `request.graphql_ws_scheme`. No `Accept` or `Content-Type` is added.
- **Query, variables, extensions, operationName** are rendered and
  checked as for HTTP ([graphql.md](graphql.md#sending-gql-02)); the
  extensions (GQL-06) go in the `subscribe` (or `start`) payload's
  `extensions`. `OperationType` is the type of the operation that runs.
  Files (GQL-07) cannot go over WebSocket and are ignored.
- **Connection params** (`connectionParams` in the file,
  [file-format.md](file-format.md#graphql-request-kind-graphql)): the
  Connection tab's text, `{{variables}}` rendered, then a JSON object (or
  `null`, or blank for no payload), compacted. Anything else is
  `request.graphql_invalid_connection_params` (details `field`
  `connectionParams`, `offset` for a syntax error).

`http.NewGraphQLSubscription(ctx, httpService, id, allowUnresolved)` loads
the request (pending edits included) and resolves its transport
(`request.GraphQLTransportOf`: the settings and the type of the operation
that runs). For WebSocket it reports undefined `{{variables}}` of every
field as one `template.unresolved` error unless `allowUnresolved` (the UI
asks "Subscribe anyway", ENV-11), and fills `wsclient.Options`: the app's
dialer, the project's cookie jar, `InsecureSkipVerify` from the request's
`verifyTls` or else `network.verifyTls`, and `RootCAs` from the custom CA
bundles (NET-04; a broken bundle fails a `wss` subscription and is ignored
with a warning for `ws`). For SSE it builds the request as Send does
(`request.BuildGraphQLSSE`, the same undefined-variable check) and returns
a `Stream` (as an sse request's, [sse.md](sse.md)): the app's HTTP client
with the trusted CAs and client certificates, the cookie jar, the request's
timeout for the response head. For HTTP it returns only the transport, which
the service refuses.

### Service

`GraphQLService.Subscribe({requestId, allowUnresolved})` returns the
operation ID at once (domain `graphql.subscription`, kind `subscribe`; the
operation's context carries the user-operation mark the dial guard wants,
PRV-05) and fails at once for a request that cannot be built, an unknown
transport (`request.graphql_invalid_transport`) or an operation that runs
over HTTP (`graphql.runs_over_http`: it is sent instead). The work:

1. sends a `connecting` item (with the transport and the operation type),
   then over WebSocket dials (`transportws.Dial`, the protocol, connection
   params, 3 s ack timeout) and subscribes with the rendered payload; over
   SSE logs the request body as a sent `request` message, sends the
   request and reads its answer (`transportsse.Read`);
2. streams every event as `SubscriptionEventDTO` items, batched every
   50 ms, at most 256 per event (`docs/events.md`);
3. for a query or mutation, merges its result payloads as incremental
   delivery over HTTP would be (`incremental.Result`, so `@defer` and
   `@stream` parts over WebSocket merge too) and sends the merged result
   as a `result` item once the server completed it;
4. ends `ok` when the server sends `complete`, `error` when it sends
   `error` (or over SSE rejects the operation with GraphQL errors:
   `graphql.subscription_error`, the first error's message, details
   `count`), closes the connection or the connection fails
   (`transportws.*`, `transportsse.*`, `wsclient.*`, `http.*`),
   `cancelled` when stopped. After `complete` or `error` the WebSocket
   client closes with 1000 (the legacy protocol sends
   `connection_terminate` first).

`GraphQLService.StopSubscription({opId})` cancels a running subscription
(`false` for an unknown or finished one, or an operation that is not a
subscription); `OpsService.Cancel` works too. Over WebSocket the work then
sends `complete` (`stop` in the legacy protocol) for the operation and
closes the connection with 1000 (3 s close handshake), so the log ends with
them; over SSE it cancels the request, which closes the connection. App
shutdown cancels running subscriptions the same way.

`SubscriptionEventDTO{at, kind, direction, type, id, payload, size, binary,
errorCount, note, url, method, transport, operationType, subprotocol,
status, contentType, close, error}`:

| `kind` | Sent when | Fields |
|---|---|---|
| `connecting` | before the handshake or the SSE request | `url` (ws/wss, or http/https for SSE; userinfo removed), `transport` (`graphql-transport-ws`, `graphql-ws`, `sse`), `operationType`, `method` (SSE) |
| `open` | the handshake succeeded, or the SSE response's head arrived | `subprotocol` (WebSocket); `status`, `contentType` (SSE) |
| `message` | every protocol message sent or received | `direction`, `type` (graphql-transport-ws: `connection_init`, `connection_ack`, `ping`, `pong`, `subscribe`, `next`, `error`, `complete`; graphql-ws: `connection_init`, `connection_ack`, `connection_error`, `ka`, `start`, `data`, `error`, `complete`, `stop`, `connection_terminate`; SSE: `request` (sent), `next`, `complete`, `keep-alive`, `response`, the name of an ignored event; `""` for a message that could not be parsed, whose `payload` is the whole message), `id`, `payload` (the JSON as on the wire; an SSE event's data or comment), `size` (bytes of the message), `errorCount` (GraphQL errors of `next`/`data`/`response`/`error`), `note` (`unknown_id`, `ignored`, `not_event_stream`), `error` (why a message broke the protocol) |
| `result` | a query or mutation completed | `payload` (the merged result, indented), `size`, `errorCount` |
| `closed` | the connection ended | `close{code, codeName, reason, wasClean, initiatedBy}` (WebSocket), `error` for a failure (handshake, dropped transport, an SSE stream that broke or ended early) |

`at` is RFC 3339 in UTC with fractional seconds: the time the message was
written or read.

### UI

- **Editor** (`GraphqlEditor`): the editor runs Go's diagnostics itself;
  the operation that runs (the only one, or the picked one,
  `runningOperationType` in `lib/graphqlSubscription.ts`) and the
  request's transports decide the URL bar's button (`runsOverSocket`,
  mirroring Go's `GraphQLTransportFor`): a subscription gets
  **Subscribe**, a query or mutation over WebSocket **Send** through the
  subscription slot, both **Stop** (Cancel for a query) while they run;
  ⌘↩ does the same. A query or mutation over HTTP or SSE is sent as
  usual; over SSE the Headers tab's automatic Accept shows
  `text/event-stream`. Copy as curl is hidden for runs through the slot.
- **Connection** sub-tab: the **Transport** pickers (Queries, Mutations,
  Subscriptions; subscriptions offer no HTTP; a stored value this version
  does not know shows as "(unknown)"), saved in `settings.graphqlTransport`
  with the defaults stored as nothing (`transportPatch`), and the
  connection params: a JSON `CodeEditor` saved as text, Go's JSON check
  marker and status line, and a warning when the value is not an object.
- **Slots** (`commands/graphqlSubscriptions.ts`): one per request, outside
  React, like the gRPC call slots: `start` flushes the queued edits,
  clears the log, asks about undefined variables and follows the
  operation; `stop` calls `StopSubscription`; data items go into the log
  (`subscriptionLogMessage`) and the counters (connected: the ack, or the
  SSE response's head; events: `next`, `data` and `response`; events with
  errors; bytes; the close), the transport and operation type of
  `connecting`, and the result (the `result` item, else the latest result
  payload; not logged); closing the tab stops the subscription and drops
  the log, which keeps the last 10,000 entries.
- **Response pane** (`GraphqlSubscriptionView`): `HttpResponsePanel` shows
  whichever of the request's HTTP send and subscription started last. A
  status line (Connecting…, Subscribed, Completed, Stopped, Failed with the
  error; live time; the transport; events; events with errors; bytes; the
  close code) with Stop or Subscribe again, then the shared `MessageLog`:
  time, ↑ sent / ↓ received / • system, the message type as the kind (the
  `error` type is red; ping, pong, ka and keep-alive muted, and hidden by
  the Keep-alives toggle), `id 1` or `2 errors` as the badge, size and the
  payload's preview. Selecting a received `next`, `data` or `response` (or
  `error`, an array or one legacy error object) opens it in the detail pane
  as the GraphQL response view shows a result (`GraphqlBody`,
  [graphql.md](graphql.md#response-gql-05)): the summary, the errors list
  with locations that jump to the query, and the JSON with the `data` and
  `errors` lines marked. For a query or mutation the pane has two tabs:
  **Response** (the result in `GraphqlBody`, "Waiting for the result"
  before it) and **Messages** (the log), and Send again instead of
  Subscribe again.

### Tests

- `internal/core/request/graphqlws_test.go`: scheme mapping, headers and
  auth, rendered variables and params, operation type, timeout and TLS,
  the error codes. `graphqltransport_test.go`: the transport per operation
  type and its errors, `BuildGraphQLSSE` (Accept, files left out, GET).
- `internal/app/services/http/graphqlws_test.go`: the WebSocket options,
  `network.verifyTls`, the CA bundles for wss and ws, undefined variables.
  `incremental_test.go`: a query set to SSE sent with
  `Accept: text/event-stream` and read as an incremental response.
- `internal/app/services/graphql/subscribe_test.go`, end to end through the
  HTTP service and the app's guarded dialer against an in-process
  graphql-transport-ws server written in the test: params, auth header and
  subscribe payload as received, the event log in order with timestamps,
  ping/pong, errors in `next`; stop (complete, 1000, `cancelled`) and
  restart twice; an `error` message; the server closing with 4403; a
  failed handshake; refused input; `StopSubscription` of unknown IDs.
  `subscribe_transport_test.go`, the same way against a legacy server and
  an SSE server: the legacy log (`ka`, `start`, `data`,
  `connection_terminate`), stop (`stop`, `connection_terminate`, 1000),
  an `error` object and `connection_error`; the SSE request (method,
  Accept, auth, body with extensions), keep-alives, stop (the server sees
  the request end), a validation error, a stream ending early, an invalid
  event, 403; a query over WebSocket and a mutation over SSE with their
  merged `result`; a query over HTTP and an unknown transport refused.
- `internal/app/services/collection/graphql_request_test.go`: the
  transports saved through the auto-save pipeline and read back.
- Frontend: `commands/graphqlSubscriptions.test.ts` (log mapping, the
  transport helpers, the counters and result, start, stop, restart, tab
  close, "Subscribe anyway"),
  `components/response/GraphqlSubscriptionView.test.tsx` (status line,
  log, the GraphQL detail pane, the pane following the latest run, a
  query's Response and Messages tabs),
  `GraphqlEditor.test.tsx` (Connection tab: params and transport pickers;
  Subscribe/Stop; a query over WebSocket through the slot; a query over
  SSE sent with the event stream Accept).
