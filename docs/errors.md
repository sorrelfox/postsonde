# Errors

Go and the UI share one error model: an error has a **kind**, an optional
finer **code**, a user-facing **message**, an optional **hint**, optional
**details** and a **cause chain**. Go code builds errors with
`internal/core/errs`. One function in the app layer, `apperr.Marshaler`,
turns any error into one JSON shape. The UI reads that shape with
`frontend/src/lib/errors.ts`.

```
core / service              app layer                    Wails v3.0.0                  UI
errs.New / errs.Wrap  ──▶  apperr.Marshaler      ──▶   RuntimeError            ──▶  toAppError(reason)
(*errs.Error)              (ServiceOptions.             message = err.Error()          → AppError
                            MarshalError)               cause   = ErrorDTO JSON
```

| Piece | Where |
|---|---|
| Kinds, `Error`, `KindOf`, `Chain` | `internal/core/errs` (standard library only) |
| `ErrorDTO`, `ToDTO`, `Marshaler` | `internal/app/apperr` |
| Hook into Wails | `serviceOptions` in `internal/app/services.go` |
| Wails round-trip test and golden bodies | `internal/app/wails_errors_test.go`, `internal/app/testdata/wails/` |
| `AppError`, `toAppError`, display helpers | `frontend/src/lib/errors.ts` |
| Example | `SystemService.Wait`, `frontend/src/components/SystemDemo.tsx` |

## Kinds

The set is small and stable. The UI chooses titles, icons and behaviour
(retry, focus a field) by kind, so adding a kind is an API change: update
`errs`, `apperr.defaultMessages`, `ERROR_KINDS` and `TITLES` in `errors.ts`,
and this table. Use a **code** for anything finer.

| Kind | Use when | Typical sources |
|---|---|---|
| `invalid_argument` | The input is wrong and the user can fix it. | Settings validation, a malformed URL or header in the editor, a bad argument to a service method. |
| `not_found` | Something the user referred to does not exist. | A request, collection or environment id; a file to import. |
| `already_exists` | Creating something whose name or id is taken. | A new collection, a renamed file. |
| `permission_denied` | The OS refused access, or PostSonde's own policy refused an action. | A settings or collection file, a keychain entry, binding a privileged port; a connection refused by the dialer guard (`netx.dial_denied`), or because no user action started it (`netx.not_user_initiated`, PRV-05; see [network.md](network.md#user-initiated-dials-only)). |
| `cancelled` | The user or the app cancelled the operation. | `.cancel()` on a call, closing a tab while a request runs. |
| `timeout` | A deadline expired. | Request timeout, dial or TLS handshake timeout in the dialer, a SOCKS5 or HTTP proxy that does not answer (`netx.socks_timeout`, `netx.http_proxy_timeout`). |
| `network` | Connecting to or talking with a remote host failed. | DNS, refused or reset connections, proxy errors; `netx.*` codes shared by every protocol client ([network.md](network.md#connection-and-tls-failures)), the SOCKS5 proxy's `netx.socks_*` codes (auth failed, reply codes such as refused by the destination; [network.md](network.md#socks5-errors)) and the HTTP proxy's `netx.http_proxy_*` codes (407 auth required or failed, CONNECT refused; [network.md](network.md#http-proxy-errors)). |
| `tls` | The TLS handshake or certificate verification failed. | Unknown authority, expired certificate, hostname mismatch, a plaintext server; `netx.tls_*` codes. |
| `protocol` | The peer broke the protocol. | A malformed HTTP response, a GraphQL response without `data` or `errors`, a bad WebSocket frame, a proxy that does not speak SOCKS5 or HTTP (`netx.socks_protocol_error`, `netx.http_proxy_protocol_error`). |
| `parse` | Content could not be parsed. | JSON bodies, GraphQL documents, imported collections, stored files. |
| `io` | Local file I/O failed for a reason other than `not_found`/`permission_denied`. | Disk full, a failed atomic rename in storage. |
| `internal` | A bug or an unexpected failure. | Errors that are not `*errs.Error` and that `KindOf` cannot classify. |
| `unsupported` | Valid, but PostSonde does not support it (yet). | An unknown auth scheme, an HTTP version, a platform feature. |

## Go

```go
import "github.com/sorrelfox/postsonde/internal/core/errs"

// A new error, with a code, an actionable hint and a detail.
return errs.New(errs.InvalidArgument, "The timeout must be a positive number.").
	WithCode("settings.invalid_value").
	WithHint("Enter a value from 1 to 600 seconds.").
	WithDetail("field", "timeoutSeconds")

// Wrapping a cause: the message is for the user, the cause is kept.
if err := atomicfile.Write(path, data); err != nil {
	return errs.Wrap(err, errs.IO, "Could not save the settings.")
}

// Context errors: KindOf maps context.Canceled to cancelled and
// DeadlineExceeded to timeout. errors.Is(err, context.Canceled) still holds.
if err := ctx.Err(); err != nil {
	return errs.Wrap(err, errs.KindOf(err), "The request was stopped.")
}
```

- `errs.New(kind, msg)`, `errs.Wrap(err, kind, msg)`. `WithHint`, `WithCode`
  and `WithDetail` return a modified **copy**, so a package-level sentinel is
  never changed by a caller.
- `Error()` is `message: cause` (like `fmt.Errorf("%s: %w")`). `Unwrap()`
  returns the cause.
- `errors.Is` matches by kind and code. A target `*errs.Error` matches when
  its `Kind` is empty or equal and its `Code` is empty or equal:
  `errors.Is(err, ErrNoRequest)` (a sentinel with a code), or
  `errors.Is(err, &errs.Error{Kind: errs.NotFound})`.
- `errs.KindOf(err)` classifies any error. The first `*errs.Error` in the
  tree decides. Otherwise it maps `context.Canceled` to `cancelled`;
  `context.DeadlineExceeded`, `os.ErrDeadlineExceeded` and `net.Error`
  timeouts to `timeout`; `crypto/tls` and `crypto/x509` handshake and
  verification errors to `tls`; other `net.Error`s (including `*url.Error`)
  to `network`; `fs.ErrNotExist`, `fs.ErrExist` and `fs.ErrPermission` to
  `not_found`, `already_exists` and `permission_denied`;
  `errors.ErrUnsupported` to `unsupported`. Anything else is `internal`.
- `errs.Chain(err)` lists the messages of `err` and its causes, outermost
  first. It follows `errors.Unwrap` and walks `errors.Join` trees in order.
  Each entry is only the part its cause does not repeat:
  `fmt.Errorf("open settings: %w", err)` contributes `"open settings"`.
- `errs.Redact(err, mask)` returns `err` with the message, hint and
  details of every `*Error` in it and the text of its other causes passed
  through `mask` (a secret masker's `MaskText`): for an error whose cause
  may quote a secret, such as a transport error repeating a URL. Kinds,
  codes and `errors.Is` are kept; `err` itself comes back when nothing
  changes ([privacy.md](privacy.md#secrets-in-errors)).
- Codes are `area.snake_case` (`system.wait_out_of_range`,
  `settings.invalid_value`). Export them as constants next to the code that
  returns them (`system.CodeWaitOutOfRange`).
- **Network failures use the shared classification.** DNS, refused, reset
  and TLS failures get their kind, `netx.*` code, message and hint from
  `netx.Classify` / `netx.ConnError` (DBG-09), whatever the protocol, so
  the same problem reads the same in the HTTP, WebSocket, gRPC and TCP
  views. A protocol client may reword the message or refine the hint, but
  keeps the code. See [network.md](network.md#connection-and-tls-failures).
  Failures through a SOCKS5 or HTTP proxy have their own `netx.socks_*`
  and `netx.http_proxy_*` codes and name the proxy, never its credentials
  ([network.md](network.md#socks5-errors),
  [network.md](network.md#http-proxy-errors)).
- **Services return `errs` errors and never convert them.** Wrap errors from
  core or libraries at the point where you can say something useful to the
  user. A service method does not build DTOs for errors.

## JSON shape

Every error a bound method returns reaches the UI as an `apperr.ErrorDTO`.
Every field is always present (no `omitempty`, see [bindings.md](bindings.md)):

```json
{
  "kind": "invalid_argument",
  "code": "system.wait_out_of_range",
  "message": "The wait time must be between 0 and 60000 ms, not -1 ms.",
  "hint": "Enter a value from 0 to 60000 ms.",
  "details": { "field": "ms", "max": "60000", "min": "0" },
  "causes": []
}
```

| Field | Type | Content |
|---|---|---|
| `kind` | string | One of the kinds above. An empty or unknown Go kind becomes `internal`. |
| `code` | string | The finer code, or `""`. |
| `message` | string | User-facing, never empty. It falls back to the kind's generic message (`apperr.DefaultMessage`). |
| `hint` | string | What the user can do, or `""`. |
| `details` | object of strings | Machine-readable context, `{}` when there is none. |
| `causes` | string array | `errs.Chain` without the message itself: diagnostics for a "details" disclosure or a bug report. `[]` when there are none. |

How `apperr.ToDTO` maps an error:

- The **first `*errs.Error`** in the tree supplies kind, code, message, hint
  and details.
- **Anything else** gets its kind from `errs.KindOf` and the kind's
  **generic message**. Raw library text (`pq: password authentication
  failed`, `open /x: permission denied`) never becomes the message; it
  appears only in `causes`.
- `Marshaler` logs every error it converts: `internal` at error level with
  the full chain, the others at debug level.

The golden files in `internal/app/apperr/testdata/error/` pin the shape of
representative errors. The frontend tests read them too.

## Crossing the Wails boundary (v3.0.0)

This is what the Wails source actually does, proved by
`TestWailsErrorRoundTrip`:

1. `BoundMethod.Call` passes each returned error to the method's marshaler.
   The result goes into `CallError{Message: err.Error(), Cause: <marshaler
   JSON>, Kind: "RuntimeError"}` (`pkg/application/bindings.go`). An argument
   that fails to decode gives `Kind: "TypeError"`, with the same marshaler
   used for the cause.
2. The HTTP transport answers `422` with the JSON of that `CallError`
   (`transport_http.go`, `httpError`).
3. The runtime (`@wailsio/runtime`, `runtime.js`) rejects with
   `new RuntimeError(body.message)` (or `TypeError`/`ReferenceError`) and
   sets `err.cause = body.cause`.

So structured data travels only in `cause`. `err.message` is Go's raw
`err.Error()`, cause chain included: never show it, use `toAppError`.

**The marshaler must be set per service.** `Bindings.Add` combines a
service's `ServiceOptions.MarshalError` with Wails' default marshaler only,
so `application.Options.MarshalError` is never used for bound methods. The
default marshaler is `json.Marshal(&err)`: exported fields for an
`*errs.Error`, `{}` for most other errors. Every service is therefore
registered in `services()` with
`application.NewServiceWithOptions(svc, serviceOptions(logger))`.
`TestServicesMarshalErrors` fails for a service registered without it. A
subtest of the round-trip test pins the upstream behaviour, so it tells us
when a Wails update fixes it.

A call cancelled from the UI rejects with the runtime's `CancelError`
whatever Go returns.

## Frontend

`frontend/src/lib/errors.ts` (components may import it, see
[frontend.md](frontend.md)):

| Export | Purpose |
|---|---|
| `AppError`, `ErrorKind`, `ERROR_KINDS` | The TypeScript side of `ErrorDTO` (hand-written: errors are not in method signatures, so the bindings generator does not emit them). |
| `toAppError(reason)` | Any rejection reason to an `AppError`. |
| `isAppError(v)` | Strict shape check. |
| `isCancelled(reason)` | True for `CancelError` or a `cancelled` error from Go. Also re-exported by `calls.ts`. |
| `errorTitle(kind)` | A short heading: `"Invalid input"`, `"Timed out"`… |
| `formatError(reason)` | One line: message, then the hint. |

How `toAppError` maps a reason:

- `CancelError`: `cancelled`.
- A bare `ErrorDTO` (the `error` of an operation's done event,
  [events.md](events.md)): read like a `cause`.
- A Wails `RuntimeError`/`TypeError`: the `ErrorDTO` in `cause`, or the
  first one if `cause` is an array (a method that returns several errors).
  Missing fields are filled in, and unknown kinds become `internal`.
- Any other `Error` or string (a failed fetch, a `ReferenceError` for an
  unknown binding, an error thrown by UI code): `internal` with its own
  message. Go errors always carry an `ErrorDTO`, so Go's raw `err.Error()`
  never ends up here.
- Anything else: `internal` with the generic message.

**Language (UI-10).** Go writes messages and hints in English. When the UI
language has a message for the error's `code` (`"<code>"`, and
`"<code>.hint"` for the hint), `toAppError` uses it instead, with the
error's `details` as `{placeholders}`; otherwise Go's text stays. English
has no such messages: Go's text is the English. `errorTitle` and the
cancelled and internal fallbacks come from the UI's catalogue. So a stable
`code` and useful `details` are what make an error translatable. See
[frontend.md](frontend.md#localization).

```ts
call.then(onDone, (reason: unknown) => {
  if (isCancelled(reason)) return;
  const err = toAppError(reason);
  show({ title: errorTitle(err.kind), message: err.message, hint: err.hint });
});
```

`errorMessage(err)` in `calls.ts` still works and returns
`toAppError(err).message`.

## Rules

- **Messages are for the user.** Write whole sentences that say what went
  wrong, in the user's terms ("Could not save the settings."), not Go
  internals ("atomicfile: rename failed"). Put the technical part in the
  cause.
- **Hints are actionable**: say what to do ("Enter a value from 0 to
  60000 ms.", "Check the proxy settings."). Leave the hint empty rather
  than repeat the message.
- **No secrets** in messages, hints, details or codes: no tokens,
  passwords, header or cookie values, and no URLs with credentials or
  secret query parameters. Causes come from libraries and may contain hosts,
  paths and URLs, so wrap errors whose text could carry a secret with a
  message of your own, and do not format secret values into errors in core.
  The redacting logger (`internal/core/logging`) is only a safety net for
  logs; it does not clean what reaches the UI.
- **Pick the most specific kind** the caller can act on. Use `internal` only
  for bugs. Do not add kinds for one feature; use a code.
- **Convert in one place.** Services return `errs` errors; `apperr` does the
  conversion. Do not add per-service error DTOs or set a different
  `MarshalError`.
- **Test** the kind (`errs.KindOf`), the code and the hint of the errors a
  method returns, as `TestWaitOutOfRange` does. Add a golden in `apperr` for
  a new shape of error. Update the goldens with
  `go test ./internal/app/... -update` and review the diff.
