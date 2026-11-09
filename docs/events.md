# Event streaming for long-running operations

Some work produces results over time: a response body that downloads in
chunks, WebSocket or SSE messages, a gRPC stream, requests hitting a local
mock server. A bound method cannot return all of that at once, so these
operations stream their results to the UI as Wails events. Every domain
does it the same way, as described here. For the plain call conventions
see [bindings.md](bindings.md).

The reference implementation is the demo stream:

| Piece | Reference |
|---|---|
| Batcher (core) | `internal/core/stream` (`Batcher[T]`) |
| Operation IDs (core) | `internal/core/opid` (`opid.New`) |
| Registry, emitter, payloads | `internal/app/operation` (`Registry`, `Start`, `Emitter[T]`, `*EventDTO`) |
| Cancel service | `internal/app/services/ops` (`OpsService.Cancel`) |
| Demo operation | `internal/app/services/demo` (`DemoService.Stream`, `events.go`) |
| Frontend helper | `frontend/src/lib/operations.ts` (`startOperation`, `cancelOperation`) |
| React hook | `frontend/src/commands/useOperation.ts` (`useOperation`) |
| Demo UI | `frontend/src/components/StreamDemo.tsx` |

## How it works

```
UI                                      Go
startOperation("demo", start, h)
  Events.On("demo:op:data|progress|done")
  DemoService.Stream(2000, 5000)  ───▶  operation.Start(reg, spec, fn)
                                  ◀───  StartedDTO{opId}   (returns at once)
                                        fn(ctx, emit) in its own goroutine
                                          emit.Send(item) ─▶ Batcher (every 50 ms or 1000 items)
  h.onData(items)                 ◀───      demo:op:data     {opId, seq, items}
  h.onProgress(p)                 ◀───      demo:op:progress {opId, seq, done, total, message}
  cancelOperation(opId)           ───▶  OpsService.Cancel(opId): cancels fn's ctx
  h.onDone(result)                ◀───  demo:op:done     {opId, seq, status, error, items, batches, elapsedMs}
```

1. The start method validates its input, calls `operation.Start` and returns
   the `StartedDTO` straight away. The work runs in its own goroutine with a
   context of its own. It is not tied to the context of the start call,
   which ends as soon as the method returns. The context is marked as a
   user operation (`netx.WithUserOp`, kind `<domain>.<kind>` and the
   operation ID); the app's dialer refuses connections whose context lacks
   that mark, so pass `ctx` on to every dial ([network.md](network.md#user-initiated-dials-only)).
2. The work sends items and progress through an `Emitter[T]`. They go
   through a `stream.Batcher`, so however fast the work produces, the UI
   gets at most one data event and one progress event per interval (plus
   extra data events when a batch fills).
3. When the work returns, pending items are flushed and exactly one done
   event is sent. Then the operation is removed from the registry.

## Operation IDs

- Every long-running operation gets an ID from `opid.New()`: `op_` plus 16
  lowercase hex digits from `crypto/rand` (`op_3f9a0c12d4e5b678`). It is a
  string; the UI treats it as opaque.
- The start method returns it as `StartedDTO.opId`. Every event of the
  operation carries it, and the UI cancels with it.
- IDs are unique per process run and are never reused. They are not
  persisted.

## Event names

`<domain>:op:<phase>`, where `<domain>` is the service's domain (`demo`,
`http`, `http.repeat`, `graphql`, `graphql.subscription`, `sse`, `sio`, `realtime`, `grpc`, `servers`, `runner`) and `<phase>` is one of:

| Phase | Payload | Sent |
|---|---|---|
| `data` | `DataEventDTO[T]` | a batch of items; never empty |
| `progress` | `ProgressEventDTO` | the latest progress, at most once per flush, after that flush's data event |
| `done` | `DoneEventDTO` | exactly once, last |

- The names are per domain, not per operation, so a domain has three event
  names however many operations run. Subscribers filter by `opId`.
- Each domain declares its names as constants and registers them with
  their payload types in an `init` in its `events.go`
  (`application.RegisterEvent[operation.DataEventDTO[ItemDTO]](EventData)`).
  Registration types `ev.data` in the generated bindings
  (`frontend/bindings/github.com/wailsapp/wails/v3/internal/eventdata.d.ts`)
  and makes Wails reject an emit whose payload has the wrong Go type. The
  names must be constants and the call direct, or the generator cannot
  see them.
- One domain uses one item type `T` for all its operations. When a domain
  has operations that stream different item types, give each its own
  domain segment (`realtime.ws`, `realtime.sse`). HTTP does: a send is
  `http` (one `ResponseDTO`; an incremental GraphQL response, GQL-08, sends
  partial ones before it, [graphql.md](graphql.md#incremental-delivery-gql-08)), a repeat run `http.repeat`
  (`RepeatResultDTO` per send, [http-client.md](http-client.md#repeat-and-interval-sending-http-27)). GraphQL does: schema
  fetches are `graphql` (one `SchemaInfoDTO`), subscriptions
  `graphql.subscription` (`SubscriptionEventDTO`, at most 256 per batch;
  [graphql-subscriptions.md](graphql-subscriptions.md#running-subscriptions-gql-33-gql-34)).
  A folder run is `runner` (`RunResultDTO` per request of each
  iteration, at most 64 per batch; [runner.md](runner.md)).

## Payloads

All payloads follow the DTO rules in [bindings.md](bindings.md) and share
two fields:

- `opId`: the operation.
- `seq`: 1, 2, 3, … over all events of the operation (data, progress and
  done together). The UI uses it to detect a missed event.

| Field | Type | In | Meaning |
|---|---|---|---|
| `items` | `T[]` | data | the batch, in the order sent |
| `done`, `total` | number | progress | work done and expected, in the operation's unit; `total` is 0 when unknown |
| `message` | string | progress | short status line, or `""` |
| `status` | `"ok"` \| `"error"` \| `"cancelled"` | done | the outcome |
| `error` | `ErrorDTO` \| `null` | done | when `status` is `"error"`, the error as `apperr.ToDTO` builds it for a rejected call (kind, code, message, hint, details, causes; [errors.md](errors.md)), else `null`. `lib/operations.ts` passes it through `toAppError` |
| `items`, `batches` | number | done | items and data events sent in total |
| `elapsedMs` | number | done | start to finish |

The outcome in `done.status`:

- `cancelled` if the operation was cancelled (by `OpsService.Cancel` or at
  app shutdown), whatever the work returned;
- otherwise `error` if the work returned an error or panicked;
- otherwise `ok`.

A domain result that is more than a summary (a response's status line and
headers, say) is sent as its own item or as a separate bound call after the
done event. The done event stays the same for every domain.

The JSON shapes are pinned by golden tests in
`internal/app/operation/testdata/dto/`.

## Batching and throttling

Defaults (`stream.DefaultInterval`, `stream.DefaultMaxItems`):

- **Interval 50 ms.** Pending items are flushed every 50 ms: at most 20
  data events a second per operation. That looks live and leaves the UI
  thread free between updates. 2000 items a second arrive as about 20
  batches of 100.
- **MaxItems 1000.** A batch that reaches 1000 items is sealed and sent
  without waiting for the tick. This caps the size of one event payload.
  At most `rate × interval` items wait at any time.

Choosing them (`operation.Spec.Interval`, `Spec.MaxItems`):

- Keep the 50 ms interval for anything shown live (messages, request logs,
  progress). Use 100–250 ms for streams where only a counter or a chart
  changes, or when many operations run at once.
- Lower MaxItems when items are large (response body chunks: use a few
  dozen, so one event stays around a megabyte or less). Raise it only for
  tiny items at very high rates.
- The batcher never drops items, and the work never blocks on the UI.
  Pending items are held in memory, so a producer that could outrun the UI
  for a long time (a firehose socket) must cap what it keeps itself, for
  example by keeping the last N messages and counting the rest.
- Progress is coalesced: of all `emit.Progress` calls in one interval, only
  the latest is sent. Call it as often as convenient.

## Cancellation

- The UI cancels any operation with `OpsService.Cancel(opId)`
  (`cancelOperation(opId)` in `lib/operations.ts`). It returns `true` if the
  operation was running and `false` if it had already finished or never
  existed. The UI can ignore the result: the done event is the answer.
- Cancelling cancels the work's context. The work must return promptly once
  `ctx` is done (pass `ctx` to core calls, `select` on `ctx.Done()`). Items
  it sends while it winds down are still delivered. Then the done event
  follows with `status: "cancelled"`.
- **Why one shared `OpsService`** instead of a `Cancel` method on every
  domain service: there is one registry for all operations, so cancel-by-ID
  needs no domain, the UI needs a single binding, and domain services don't
  each reimplement it. Domain services only start operations.
- Cancelling the start call's promise (`.cancel()`) is not the way to stop
  an operation. The start call returns within milliseconds, and cancelling
  it could lose the ID of an operation that has already started.
  `startOperation` therefore waits for the ID and then cancels by ID.
- At app shutdown, `OpsService.ServiceShutdown` cancels every operation and
  waits up to 5 s for them to finish.
- A component that unmounts while its operation runs cancels it
  (`useOperation` does this).

## Input to a running operation

Some operations take input while they run: a client-streaming or bidi
gRPC call sends messages one at a time. The domain service then has plain
bound methods that name the operation by `opId`
(`GRPCService.SendMessage({opId, message})`, `GRPCService.CloseSend({opId})`,
[grpc.md](grpc.md#streams-from-the-client)); cancelling stays with
`OpsService.Cancel`. Conventions:

- The service keeps its running operations by ID from the moment `Start`
  returns until the work returns, and refuses an unknown or finished ID
  with a domain code (`grpc.call_not_running`), never by starting anything.
- The method answers once the input is handed over; what it caused is
  reported as items of the operation's own data events, in order with
  everything else (a `sent` item, then the replies), not in the method's
  result. The result only acknowledges (`{index, size}`).
- Input that arrives before the work has emitted its first item waits for
  it, so the first item stays first.

## Ordering and gaps

- All events of one operation are sent from one goroutine, in order: data
  and progress in flush order, then done. Wails delivers them in the order
  they were emitted.
- Events can arrive before the start call resolves in the UI.
  `startOperation` subscribes to the domain's events **before** calling
  the start method, buffers everything until it knows the ID, then replays
  that operation's events and drops the rest.
- The UI checks `seq`. An event with a `seq` not above the last one is a
  duplicate and is dropped. A jump reports a gap (`onGap`; `useOperation`
  counts them in `gaps`) and delivery continues. A gap means an event was
  lost between Go and the webview; it should not happen, so treat it as a
  bug to investigate, not a condition to recover from.
- After the done event, `startOperation` unsubscribes. Later events with
  that ID cannot exist.

## Frontend

- `lib/operations.ts` (framework-free):
  - `startOperation<T>(domain, start, handlers)` returns a handle with
    `opId()`, `cancel()` and `dispose()`.
  - The handlers are `onStarted`, `onData(items)`, `onProgress`, `onGap`
    and `onDone(result)`.
  - `onDone` is called exactly once, also when the start call fails
    (outcome `error`, `opId: null`).
  - `cancelOperation(opId)` cancels any operation.
- `commands/useOperation.ts` is the React hook:
  - `useOperation<T, Args>(domain, start, { keep })` returns the state
    plus `running`, `start(...args)` and `cancel()`.
  - The state is `status`, `opId`, `received`, `batches`, `gaps`,
    `progress`, the last `keep` `items`, `error`, `result` and timestamps.
  - Each data event is one state update.
  - `onItems` gets each data event's items before that update, for a store
    that keeps more than `keep` items (the message log's `MessageSource`).
  - Unmounting cancels a running operation and unsubscribes.
  - The hook lives in `commands/` because it calls Go through `lib/`, and
    `stores/` may not import `lib/` ([frontend.md](frontend.md)).
- Operation state that other components need (say, the response of the
  active tab) goes into a store. A command calls `startOperation` and
  dispatches from the handlers, at most once per data event.
- Tests mock the runtime and `OpsService` with `src/test/fakeWails.ts`; see
  `lib/operations.test.ts` and `commands/useOperation.test.tsx`.

## Adding a streaming operation

1. **Events.** In `internal/app/services/<domain>/events.go`, declare
   `Domain`, `EventData`, `EventProgress` and `EventDone` as constants and
   register them with their payload types in `init()`, as the demo does.
2. **Start method.** On the domain service (which gets the shared
   `*operation.Registry` in `New`):

   ```go
   func (s *HTTPService) Download(ctx context.Context, in DownloadInputDTO) (operation.StartedDTO, error) {
       req, err := in.toCore()            // validate first; errors return here
       if err != nil {
           return operation.StartedDTO{}, err
       }
       spec := operation.Spec{Domain: Domain, Kind: "download", MaxItems: 32}
       return operation.Start(s.ops, spec, func(ctx context.Context, emit *operation.Emitter[ChunkDTO]) error {
           return s.client.Download(ctx, req, func(c core.Chunk) error { return emit.Send(toChunkDTO(c)) })
       })
   }
   ```

   The work honours `ctx`, and its error becomes `done.error`.
3. **Register.** Pass the registry from `services()` in
   `internal/app/services.go` to the service's `New`.
4. **Test.** Record events with an `Emit` func (see
   `internal/app/services/demo/demo_test.go`): items in order, `seq`
   contiguous, the done status, and cancellation.
5. **Generate.** Run `wails3 task bindings` and commit `frontend/bindings/`.
6. **Wrap and use.** Add the start call to `src/lib/<domain>.ts` returning
   the `CancellablePromise<StartedDTO>`. Use it with `useOperation` or
   `startOperation`.

## Event streams (SSE)

A Server-Sent Events stream is an operation of the `sse` domain
(`SSEService.Connect`, [sse.md](sse.md#client)), like an HTTP send, not a
connection with events of its own like WebSocket's `ws:*`: it has one
direction and ends with its response, so `sse:op:data` carries its items
(`StreamItemDTO`: `open`, `event`, `comment`, `retry`, `notice`,
`reconnect`, then `end`; a stream that reconnects, SSE-03, stays one
operation over all its connections), Stop is `OpsService.Cancel`, and `sse:op:done` says how it
ended. It keeps the default 50 ms interval with at most 200 items per
data event, since an event's data can be up to 1 MiB. A stream can run
for hours: the UI's message log keeps the last 50,000 messages.

## Socket.IO connections

A Socket.IO connection is an operation of the `sio` domain
(`SocketIOService.Connect`, [socketio.md](socketio.md#service)), so its
dials carry the user-operation mark: `sio:op:data` carries its items
(`ItemDTO`: `open`, maybe `fallback`, `upgrade` or `upgrade_failed`,
`packet` for CONNECT, DISCONNECT and CONNECT_ERROR, `connected`, then
`event`, `ack`, `ack_timeout`, `ping`, `pong`, and `closed` last), Emit is
a call on the running operation (`SocketIOService.Emit` with its `opId`),
Disconnect is `OpsService.Cancel` (a graceful DISCONNECT and Engine.IO
close), and `sio:op:done` says how it ended: `cancelled`, `error` (a
refused namespace, a ping timeout, a dropped transport) or `ok` (the
server ended it). 50 ms interval, at most 200 items per data event.

## Other events

Not every stream is an operation. Events that belong to no operation have
their own names, `<domain>:<what>`, still declared as constants and
registered in the domain's `events.go`, and still batched through a
`stream.Batcher` when they can come fast: `tcp:data`, `ws:message`,
`servers:traffic`, `network:activity` (below), and `console:entries`, the app log for the console
(DBG-06). `ConsoleService` subscribes to the `logging.Ring` at startup;
every record goes into a batcher (100 ms, at most 250 records per event,
never dropped) and each flush sends `{entries}` in `seq` order, redacted
again at send time. The UI subscribes before it calls `List`, then drops a
`seq` it already has, so records logged between the two are neither lost
nor doubled ([frontend.md](frontend.md#console)). The ring's subscriber
runs under its lock and only adds to the batcher, so the emit itself runs
on the batcher's goroutine and may log without feeding back into the lock.

`http:oauth2-token-changed` (AUTH-13) is sent whenever the HTTP service
stores, refreshes or removes an OAuth 2.0 token, with `{key}` (the hash
the token is kept under, never a value); the Auth tab asks for the token's
status again ([auth.md](auth.md#token-store)).

`history:changed` (NAV-02) is sent once per change of the open project's
request history: `{action: "added", id, entry}` after a send was recorded,
`{action: "deleted", id}` and `{action: "cleared"}`. Entries dropped by the
retention limit are not announced ([history.md](history.md)).

`network:activity` (PRV-06) is throttled, not batched: `NetworkService`
subscribes to the `netlog.Log` at startup; a connection that starts or
changes state is marked and arms a 250 ms timer, and when it fires one
event carries `{active, conns}`: the marked connections plus every active
one (so byte counts refresh), oldest first, and how many are active. While
any is active the timer re-arms; once all have ended and been sent it
stops, so an idle app sends nothing. The UI subscribes before it calls
`List` and keeps the later view of each connection by id (state order,
then bytes), so the two can arrive in any order
([frontend.md](frontend.md#network-activity)).

`collection:external-change` (STO-05) is coalesced at the source rather
than batched: `CollectionService` polls the open project's files every
second (`fswatch.Watcher`) and reports only once a scan finds nothing new
since the previous one (at most 5 s while files keep changing), so a
`git pull` of many files is one event. It is sent after the tree was read
again, with the node IDs `{workspaceId, added, removed, modified, moved,
conflicts, globals, environments}`; the app's own writes are never
reported (the tree compares file hashes), and nothing is sent while every
window is hidden (`SetWatching(false)` from each). When global variables or
environment files changed it is followed by `environments:changed`, so
the variable chips reload. See
[collection.md](collection.md#external-changes).

`runtimevars:changed` (ENV-15) is sent after every change to the open
project's runtime variables, whoever made it (the Environments view, or
an extraction rule after a response, [extraction.md](extraction.md)): the registry's change hook sends it
with `{count}`. The Runtime section reloads its list and the editors
their variable chips. See
[environments.md](environments.md#runtime-variables).

`secrets:changed` (SEC-03) is sent with the vault's `StatusDTO` whenever
the stored secrets are turned on or off, reset, locked (by the user or the
idle lock) or unlocked, or their password changes: the vault's change
hook sends it, and the app drops the OAuth 2.0 tokens held in memory at
the same time. The status bar's lock and Settings, Privacy follow it; the
environments reload after an unlock. See
[privacy.md](privacy.md#encrypted-secrets).

`proxyscopes:changed` (SOCKS-08) is sent after `ProxyScopesService.SetScope`
changed the proxy of the open project or of one of its environments, with
`{level, environmentId}`. Before it goes out the app's proxy router reads
the proxy in effect again (so the idle pools close when it changed); the
settings screen reloads its proxy rows on it. See
[network.md](network.md#proxy-scopes).

`collection:files-dropped` (IMP-11) is sent by the app layer, not a
service: `internal/app/filedrop.go` listens for Wails' `WindowFilesDropped`
on every window and forwards each drop of files on an element with a
`data-file-drop-target` attribute as `{paths, target}` (the attribute's
value) **to that window only** (`WebviewWindow.DispatchWailsEvent`). Empty
drops are not sent. The UI imports the files dropped on the `import` target
([import.md](import.md#dropped-files)).

With several windows (APP-06) every other event still goes to every
window. `collection:saved` and `collection:save-failed` carry the `window`
the edit came from, since their `revision` is that window's counter;
`collection:edited` (`{workspaceId, ids, window}`) is sent after every edit
`UpdateRequest` or `UpdateFolder` took, for the other windows to reload
their drafts; `window:open-project` (`{window, path}`) asks one window to
open a project. A window drops the ones that name another window
(`commands/windowId.ts`). Operation events need no window: only the window
that started an operation knows its ID. See
[windows.md](windows.md#events).

## Demo

`DemoService.Stream(ratePerSec, durationMs)` sends `ratePerSec` items
(`{index, text}`) a second for `durationMs`, with progress, flushed every
50 ms. `components/StreamDemo.tsx` starts it and cancels it. It shows:

- items received and batches received (one state update each);
- items and batches per second;
- elapsed time, progress, the last item, and missed events;
- the connection message log ([frontend.md](frontend.md#message-log)):
  each item becomes a WebSocket-like message
  (`components/streamDemoMessages.ts`: a fixed mix of sent and received
  JSON, text, binary, ping and pong frames), with system messages when the
  stream starts and ends.

At the default 2000 items a second, batches per second should read about
20, not 2000.
