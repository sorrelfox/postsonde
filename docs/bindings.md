# Go ↔ UI bindings

The UI calls Go only through Wails **bound services** and the TypeScript
bindings generated from them. Every feature follows the same pattern. The
reference implementation is the system service:

| Piece             | Reference                                               |
|-------------------|---------------------------------------------------------|
| Service           | `internal/app/services/system/system.go` (`SystemService`) |
| DTOs and mapping  | `internal/app/services/system/dto.go`                   |
| Tests             | `internal/app/services/system/system_test.go`           |
| Registration      | `internal/app/services.go` (`services`, `serviceOptions`) |
| Errors            | `internal/core/errs`, `internal/app/apperr`; see [errors.md](errors.md) |
| Generated binding | `frontend/bindings/github.com/sorrelfox/postsonde/internal/app/services/system/` |
| Frontend wrapper  | `frontend/src/lib/system.ts`, `frontend/src/lib/calls.ts`, `frontend/src/lib/errors.ts` |
| Usage             | `frontend/src/components/SystemDemo.tsx`                |

## One service per domain

- Each domain gets one package, `internal/app/services/<domain>` (`system`,
  `workspace`, `http`, `graphql`, `settings`, …), holding exactly one bound
  struct named `<Domain>Service`. The binding is exported to TypeScript under
  the same name (`SystemService.Info()`).
- The constructor is `New(<dependencies>, logger *slog.Logger) *<Domain>Service`.
  Dependencies are core packages, stores and plain values, passed in. A
  service does not reach for globals and does not look things up in Wails;
  the app layer resolves them (the build version and Wails version reach
  `system.New` as a `system.Build`).
- Services are thin: validate input, call `internal/core`, map the result to
  a DTO. Protocol and storage logic stays in core.
- A service package imports the standard library and `internal/core`. It
  imports Wails only when it needs a runtime feature (events, dialogs), and
  Wails types never appear in its method signatures.
- **Every exported method is bound.** Keep helpers unexported. `ServiceName`,
  `ServiceStartup`, `ServiceShutdown` and `ServeHTTP` are lifecycle hooks and
  are not bound; implement `ServiceName()` to name the service in logs.
- Register the service in `services()` in `internal/app/services.go` with
  `application.NewServiceWithOptions(<domain>.New(...), opts)`, where `opts`
  is `serviceOptions(logger)`. The options carry the error marshaler (see
  [errors.md](errors.md)). Keep the call inside that function: the bindings
  generator finds services by statically following these calls from
  `cmd/postsonde`.

## Method signatures

```go
func (s *SystemService) Wait(ctx context.Context, ms int) (WaitResultDTO, error)
```

- `ctx context.Context` is always the first parameter. Wails supplies it and
  leaves it out of the TypeScript signature (`Wait(ms: number)`).
- Other parameters are primitives or input DTOs. Prefer one input DTO
  (`<Verb>InputDTO`) over more than about three parameters.
- Return at most one value plus an `error`: `(TDTO, error)`, or just `error`.
  A result that is not a DTO (a lone `string` or `bool`) is fine when it
  really is one value.

## DTO rules

DTOs are the only types a method takes or returns. The generated TypeScript
models are made from them, so they are the contract with the UI.

- **Plain structs in the service package**, in `dto.go`, named `<Thing>DTO`
  (inputs: `<Verb>InputDTO`). Fields are strings, numbers, booleans, other
  DTOs, slices and `map[string]T`. No interfaces, `any`, channels, functions
  or embedded core types.
- **No core types leak into the UI.** Core types never appear in method
  signatures or DTO fields; the generator would otherwise emit them as
  models and couple the UI to core internals. Map explicitly with functions
  next to the DTOs: `to<Thing>DTO(core.Thing) <Thing>DTO` and
  `(in <Verb>InputDTO) toCore() core.Thing`. DTOs never hold pointers into
  core data.
- **JSON tag on every field, camelCase**: `json:"goVersion"`. No
  `omitempty`: every field is always present, so the TypeScript type has no
  optional fields. A value that can be absent is a pointer field owned by
  the DTO (`*string` becomes `string | null`), or an empty string or zero
  when that is unambiguous.
- **`time.Time` becomes an RFC 3339 string** in UTC
  (`t.UTC().Format(time.RFC3339Nano)`; the zero time is `""`). Durations are
  integers in milliseconds with an `Ms` suffix (`elapsedMs`).
- **`[]T` is never nil.** Build slices with `make([]T, 0, n)` so they
  marshal as `[]`, not `null`; the same goes for maps (`{}`).
- **Numbers**: JavaScript numbers are exact up to 2^53. IDs and anything
  that can be larger are strings.
- Pin each DTO's JSON shape with a golden test
  (`internal/app/services/system/testdata/dto/*.golden`).

## Context and cancellation

- Wails creates a context for every call and cancels it when the frontend
  calls `.cancel()` on the returned `CancellablePromise` (or
  `.cancelOn(abortSignal)`).
- Long-running methods must honour it: pass `ctx` into core (HTTP requests,
  dials, file work) or `select` on `ctx.Done()`, and return the context error
  wrapped as an `errs` error
  (`errs.Wrap(ctx.Err(), errs.KindOf(ctx.Err()), "The wait was cancelled.")`,
  which gives kind `cancelled` or `timeout`). Return promptly and release
  resources; `SystemService.Wait` is the model.
- Once cancelled, the promise rejects with the runtime's `CancelError`
  whatever Go returns; the Go result is discarded.
- The frontend keeps the promise to cancel it later, cancels calls a
  component no longer needs (on unmount), and ignores cancellations with
  `isCancelled(err)` from `src/lib/errors.ts` (re-exported by `calls.ts`).
- Test cancellation in Go with table cases for a context cancelled before
  and during the call and for a deadline, asserting `errors.Is(err,
  context.Canceled)` (or `DeadlineExceeded`) and that the call returned early.
  Run with `go test -race`.
- Work that streams results over time (responses, sockets, gRPC streams,
  local servers) is a long-running operation with events instead: see
  [events.md](events.md).

## Errors

Methods return errors built with `internal/core/errs` (a kind, a
user-facing message, an optional hint, code and details, and the cause).
They never convert them: the marshaler in `serviceOptions` turns every
returned error into an `apperr.ErrorDTO`, which Wails puts in the rejected
promise's `cause`. The UI reads it with `toAppError(reason)` from
`src/lib/errors.ts`. Do not define per-service error DTOs. The kinds, the
JSON shape, the Wails details and the rules are in [errors.md](errors.md).

## Frontend usage

- Only `frontend/src/lib/` imports from `frontend/bindings/`. Each domain
  has one module, `src/lib/<domain>.ts`, that re-exports the DTO types and
  wraps the calls, returning the `CancellablePromise` unchanged so callers
  can cancel.
- Components never call bindings directly. They call `src/lib/` functions,
  or store actions that call them; see [frontend.md](frontend.md) for the
  store layout.
- `src/lib/calls.ts` has the shared call helpers (the `CancellablePromise`
  type, `isCancelled`, `errorMessage`). `src/lib/errors.ts` has the error
  helpers (`toAppError`, `formatError`, `errorTitle`).

```ts
// src/lib/system.ts
import { SystemService, type InfoDTO } from
  "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/system";
export type { InfoDTO };
export function getInfo(): CancellablePromise<InfoDTO> {
  return SystemService.Info();
}

// in a component or store action
const call = wait(3000);
call.then(onDone, (err: unknown) => { if (!isCancelled(err)) onError(toAppError(err)); });
// later
void call.cancel();
```

## Adding a service in 5 steps

1. **Service.** Create `internal/app/services/<domain>/` with `<domain>.go`
   (`<Domain>Service`, `New(...)`, methods taking `ctx` first) and `dto.go`
   (DTOs plus the core ↔ DTO mapping functions).
2. **Tests.** `<domain>_test.go`: table-driven, including cancellation cases
   for long-running methods, the kinds, codes and hints of the errors
   returned, and golden JSON for the DTOs. Run
   `go test -race ./internal/app/...`.
3. **Register.** Add
   `application.NewServiceWithOptions(<domain>.New(...), opts)` to
   `services()` in `internal/app/services.go`.
4. **Generate.** Run `wails3 task bindings` and commit the changes in
   `frontend/bindings/` together with the Go code.
5. **Wrap.** Add `frontend/src/lib/<domain>.ts` wrapping the calls, and use it
   from stores and components.

## Generating and checking the bindings

- Command (flags in `BINDINGS_FLAGS` in the root `Taskfile.yml`):

  ```sh
  wails3 generate bindings -clean=true -ts -i ./cmd/postsonde
  ```

  `-ts` writes TypeScript, `-i` makes models plain interfaces (no classes),
  `-clean` empties `frontend/bindings/` first so removed services disappear.
  `time.Time` would map to `string` by default, but DTOs convert times
  themselves (see above).
- Output: `frontend/bindings/<Go import path>/` with `index.ts`, one
  `<service>.ts` per service and `models.ts` for the DTOs. The generator
  also writes `frontend/bindings/github.com/wailsapp/wails/v3/internal/event*`,
  the data types of the events registered with `application.RegisterEvent`,
  and (since Wails v3.0.0-beta.33) `frontend/bindings/events/index.ts`, a
  name constant per registered event (`CollectionChanged` for
  `collection:changed`). `src/lib/` keeps its own event name constants, so
  nothing imports that module yet; `-noevents` would drop it, but the
  data types with it.
- **The bindings are committed**, so `tsc --noEmit`, the frontend build and
  CI work on a fresh checkout without the Wails CLI or a cgo toolchain (the
  Wails Vite plugin in `vite.config.ts` fails the build if the event
  bindings are missing). Do not edit them by hand.
- `wails3 build` and `wails3 dev` regenerate them before building the
  frontend (task `common:generate:bindings`). If that changes files, the Go
  services changed without regenerating: commit the result.
- `wails3 task bindings` regenerates them; `wails3 task bindings:check`
  regenerates and fails if `frontend/bindings/` then differs from git (for
  CI). Generating loads the Go packages, so it needs the same toolchain
  as a build (on macOS without an accepted Xcode license, set
  `DEVELOPER_DIR=/Library/Developer/CommandLineTools`).
- Calls go by numeric method ID, a hash of the Go package path, type and
  method name. Renaming or moving a service or method changes the ID, so
  the bindings must be regenerated; so does changing the module path, which
  changes every ID. Three tests pin `SystemService` IDs and need the new
  numbers then: `internal/app/wails_errors_test.go`,
  `frontend/src/lib/errors.test.ts` and
  `frontend/src/components/SystemDemo.test.tsx`.
