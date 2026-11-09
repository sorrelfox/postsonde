# Folder runner

The folder runner sends the requests of a folder, in order, from inside
the app (RUN-01), the ones chosen in the order chosen (RUN-02), for a
number of iterations with a delay between the requests (RUN-03),
or once per row of a CSV or JSON data file (RUN-05),
stopping at the first failure if asked (RUN-04), shows where each
request is while it runs and stops at once (RUN-06), and lists each
request's outcome with its assertion results
([assertions.md](assertions.md)), summed up at the end, each result
opening its request and response (RUN-07), keeps the runs that ended in
a run history to reopen (RUN-09) and saves them as JUnit XML, JSON or
HTML reports (RUN-08).

| Piece | Where |
|---|---|
| Plan, selection, steps, delay, stop on failure, run variables, limits | `internal/core/runner` (`Plan`, `Item`, `Plan.Select`, `Step`, `Plan.Steps`, `Pacer`, `Vars`, `Options`) |
| Data files: CSV and JSON rows, limits (RUN-05) | `internal/core/runner/data.go` (`ReadDataFile`, `ParseData`, `Data`, `Vars.SetRow`) |
| Choosing and previewing a data file | `internal/app/services/runner/data.go` (`RunnerService.PickDataFile`, `PreviewDataFile`) |
| The plan of a folder (tree order, skipped kinds) | `internal/app/services/collection/runplan.go` (`RunPlan`) |
| Sending a request of a run | `internal/app/services/http/run.go` (`RunSession`) |
| Reading an SSE request of a run | `internal/app/services/sse/probe.go` (`Probe`) |
| Bound service, `runner:op:*` events | `internal/app/services/runner` (`RunnerService.Plan`, `Start`, `Response`, `Release`) |
| The kept responses of runs, bounded | `internal/app/services/runner/responses.go` |
| A run's record, JUnit XML, JSON and HTML reports (RUN-08) | `internal/core/runner/report` (`Run`, `Summary`, `Builder`, `Masked`, `WriteJUnit`, `WriteJSON`, `WriteHTML`) |
| The run history on disk, retention (RUN-09) | `internal/core/runner/runstore` (`Store`: `Save`, `List`, `Get`, `Delete`, `Clear`) |
| History and reports of the service | `internal/app/services/runner/history.go` (`Runs`, `Run`, `DeleteRun`, `ClearRuns`, `ExportRun`) |
| Past runs and reports in the UI | `frontend/src/commands/runHistory.ts`, `components/runner/RunHistory.tsx`, `RunReportActions.tsx` |
| Binding wrappers | `frontend/src/lib/runner.ts` |
| Selection and order of the requests (pure) | `frontend/src/lib/runSelection.ts` |
| Run state, plan and selection per folder | `frontend/src/commands/folderRuns.ts` |
| The Run sub-tab | `frontend/src/components/runner/FolderRunPanel.tsx`, `RunDataFile.tsx`, `RunRequestList.tsx` (with the live status, `runProgress.ts`), `RunSummary.tsx`, `RunResultDialog.tsx` |

```
UI: folder's Run tab ── RunnerService.Plan: the list to choose and reorder
   │ (Choose data file… ── RunnerService.PickDataFile: the native dialog, the file's preview)
   │ wait for the requests' queued edits ── RunnerService.Start({folderId, iterations, delayMs, requestIds, stopOnFailure, prompts, dataFile})
   │
collection.RunPlan: the folder's items in tree order, subfolders at their place, each runnable or skipped
   │ Plan.Select(requestIds): the chosen items in the chosen order (all of them, in tree order, without requestIds)
   │ dataFile: read again, one iteration per row (runner.ReadDataFile)
   │ fails at once: runner.invalid_options, runner.empty, runner.invalid_selection,
   │ runner.data_* (the data file), collection.not_found / not_folder,
   │ template.prompt (the {{$prompt}} values of every request, asked once)
   ▼
operation "runner"/"run" (docs/events.md): for each iteration (Vars.SetRow: its row), for each item
   │ skipped item ──────────────────────────────▶ result {outcome: skipped, skip}
   │ Pacer.Wait (the delay, not before the first request)   runner:op:progress {done, total, "Waiting before the next request"}
   │ http, graphql: RunSession.Send (prepare + exchange, as Send)  ▶ result {passed | failed | error, tests}
   │ sse: RunSession.Stream + sse.Probe (read for the window)      ▶ result {passed | failed | error, events}
   │ stopOnFailure and the result failed or is an error ─▶ runner:op:progress {done, total, "Stopped at a failure"}, end
   ▼ runner:op:done   ok (got through, or stopped at a failure) / cancelled (OpsService.Cancel)

   (the run listed a result) record masked ── runstore.Save: <local>/runs/<opId>.json, before the done event

UI: a result's name ── RunnerService.Response(opId, index): the kept ResponseDTO ── the response view in a dialog
    next run of the folder / its tab closed ── RunnerService.Release(opId)
    Save report ── RunnerService.ExportRun({runId, format}): masked again, the native save dialog
    Past runs ── RunnerService.Runs / Run(id): the history's records, reopened in the panel
```

## The plan

`collection.RunPlan(s, ctx, folderID)` lists every item inside the folder
("" for the whole project), subfolders included, in the order the tree
shows them (the manual order, [collection.md](collection.md#order)), a
subfolder's items at its place. Each `runner.Item` has the request's ID,
name, file kind, method, the folders between the run's folder and it
(`Path`), and `Skip`:

| `Skip` | Items | Why |
|---|---|---|
| `""` | `http`, `graphql` and `sse` requests | sent |
| `kind` | `websocket`, `socketio`, `grpc`, `tcp` | a connection, not a request with one response |
| `subscription` | a GraphQL request whose operation is a subscription, or that runs over WebSocket (GQL-35) | never ends with one response (read from the pending edit when there is one) |
| `unknown` | a file of a kind this version does not know | |
| `invalid` | a file that could not be loaded | |

Skipped items are still listed in every iteration, with a note saying
why, so the results show the whole folder. `RunnerService.Plan(id)`
returns the plan without running it (`PlanDTO{items, runnable}`).

The list of items is taken when the run starts; each request is loaded
and built when its turn comes, so edits and environment changes made
during the run are picked up, as in a repeat run
([http-client.md](http-client.md#repeat-and-interval-sending-http-27)).

## Selection and order

RUN-02. `RunInputDTO.requestIds` names the requests a run sends, in the
order it sends them: `Plan.Select(ids)` keeps those items of the plan in
that order, and every iteration walks them. Without `requestIds` (null)
the run sends the whole plan in the tree's order, as before. The folder
is never changed: its manual order ([collection.md](collection.md#order))
stays what the tree shows.

- An ID that is not an item of the folder's plan (a request deleted or
  moved out since the UI loaded the plan), or an ID named twice, refuses
  the start with `runner.invalid_selection` (`details.requestId`,
  `details.reason`: `unknown` or `duplicate`). An empty list is
  `runner.empty` ("No requests are chosen for the run.").
- A skipped item may be named; it is then listed as skipped, as in a
  whole-folder run. The UI never names one: it cannot be chosen.
- `position` in the results is the place in the run's order, not in the
  tree.

**Kept for the session, not saved.** The choice and the order belong to
the folder's run slot in the window (`commands/folderRuns.ts`), like the
results: they survive switching tabs and every later run of that folder
in the session, and go when the folder's tab closes or the app quits.
They are not written to `_folder.json`. Postman's runner does the same (a
run's selection is not saved into the collection). A saved selection
would be a file change that shows up in git diffs for what is a
per-run choice, would need merging when two people run the same folder
differently, and would make the folder's order and the run's order two
orders to keep in step. A saved run configuration can come with the run
history (RUN-09) if it is wanted.

When the tree changes the Run tab loads the plan again and keeps the
choice: requests still in the folder keep their order and choice, a new
request goes in (chosen) right after the one before it in the tree, and
removed ones drop out (`mergeRunOrder` in `lib/runSelection.ts`).

## Data-driven runs

RUN-05. A run can go over the rows of a data file instead of a number of
iterations: one iteration per row, in the file's order, each sending the
chosen requests with the row's values as variables. A 100-row file makes
100 iterations.

**Formats** (`runner.ParseData`). The name picks the format: `.json` is
JSON; `.csv`, `.tsv` and `.txt` are CSV; any other is JSON when it starts
with `[`, CSV otherwise. A UTF-8 byte order mark is dropped; a file that
is not UTF-8 text is refused.

- **CSV** (`encoding/csv`): a header row names the columns (spaces around
  the names trimmed; an empty or repeated name is refused), then one row
  per line. Quoted fields may hold the delimiter, line breaks and `""` for
  a quote; CRLF and LF line ends both work (a CRLF inside a quoted field
  becomes LF); blank lines are skipped; values are taken as they are,
  spaces included. Every row has as many fields as the header, else
  `runner.data_invalid` names the line. **The delimiter** is a comma, a
  semicolon, a tab or a pipe: detected from the header line (the one of
  those found most often outside quotes; a comma when none is; a tab for
  `.tsv`), or chosen in the UI (`RunInputDTO.dataDelimiter`).
- **JSON**: an array of objects, one per row. A row's variables are its
  members; a member another row has but this one has not is **not set**
  by this row (the name falls back to the scopes below, or is undefined).
  Values are strings: a string as it is, a number, `true` or `false` as
  its JSON text (`1.50` stays `1.50`, `1e3` stays `1e3`), `null` as `""`,
  an object or array as its compact JSON text. Anything else (not an
  array, an item that is not an object, a syntax error, text after the
  array) is `runner.data_invalid` with the line and column.

**Limits:** 16 MiB (checked before reading), 10,000 rows (one iteration
each, the iteration limit) and 1,000 columns (`runner.data_too_large`,
`details.limit`: `bytes`, `rows` or `columns`); no rows is
`runner.data_empty`, a file that cannot be read `runner.data_unreadable`.
Every error names the file and has a hint.

**Variables.** At the start of each iteration `Vars.SetRow` makes the
row the run's data row: the run's own variables ([Run
variables](#run-variables)) look a name up in the values set during the
run first, then in the row, so the row sits above every other scope
(global < environment < collection < folder < runtime < run). The row
replaces the one before, and any run value set before under one of its
names, so each iteration starts with exactly its row; a value set during
the iteration (extraction, TEST-05) wins over the row until the next one.
Hovers and the timeline name the scope `Run`. Row values are not secrets:
they are shown and recorded as sent.

**Choosing the file.** `RunnerService.PickDataFile(delimiter)` shows the
native open dialog and returns a `DataPreviewDTO{path, name, format,
delimiter, columns, rows, rowCount}` (the first 5 rows; an empty `path`
when cancelled); `PreviewDataFile(path, delimiter)` reads it again, with
another delimiter or after it changed. A file that cannot be read fails
with its error and `details.path`, so the UI can try another delimiter.
`Start({dataFile, dataDelimiter})` reads the file again (a file edited
since the preview runs as it is now), makes one iteration per row and
ignores `iterations`. **Only a file chosen in the dialog in this session**
is read: Go keeps the paths picked, and another path is refused with
`runner.data_not_picked` (`permission_denied`). The file-access allowlist
(PRV-10) does not apply, as for a file body: the user chose the file
([privacy.md](privacy.md#local-file-access-prv-10)).

**Kept for the session, not saved.** The data file (its path, delimiter
and preview) belongs to the folder's run slot in the window, like the
selection ([above](#selection-and-order)): it is used by every later run
of the folder in the session and goes when the folder's tab closes or the
app quits. Its path is not written to `_folder.json`, where it would be a
path on one computer in a file meant to be shared, and Go forgets the
picked paths when the app quits. A run's record in the run history
names the file and its rows, never its path ([below](#run-history)).

## Stop on failure

RUN-04. With `stopOnFailure` the run ends after the first result that is
`failed` (an assertion failed or could not be evaluated, also on an SSE
request's events, or an SSE status other than 2xx) or `error` (the request could not be built or sent),
like Newman's `--bail`: nothing after it is sent, not even in a later
iteration. Skipped items never stop it. The last progress event says
`Stopped at a failure` (`StageStopped`) with `done` the results listed and
`total` what the run would have had; the done event is `ok`, since the
run did what it was asked. Without it (the default) the run goes on.

## Running

`RunnerService.Start(RunInputDTO{folderId, iterations, delayMs, streamMs,
allowUnresolved, prompts, requestIds, stopOnFailure, dataFile,
dataDelimiter})` reads the data file if there is one (its rows are the
iterations, [above](#data-driven-runs)), checks the options
(`runner.Options.Validate`: `iterations` 1 to 10,000, `delayMs` 0 to an
hour, `streamMs` 0 to 10 minutes, `runner.invalid_options` with
`details.field` otherwise), takes the plan (`runner.empty` for a folder
without items), selects `requestIds` from it (above) and returns the
operation ID at once.

- **Order.** `Plan.Steps(iterations)` yields every item of the plan (the
  selected one) in order, then again for the next iteration. Requests
  never overlap.
- **Delay.** `Pacer` waits `delayMs` before every request it sends but the
  first, from the end of one request to the start of the next (unlike a
  repeat run's interval, which is start to start). Skipped items are not
  waited for. The run's `SleepFunc` is injectable: tests record the
  delays instead of sleeping.
- **Each request is a normal send.** HTTP and GraphQL requests go through
  `RunSession.Send`: `prepare` (load, variables, auth, folder headers,
  settings, cookie jar, the assertions rendered) and `exchange` (send,
  timeline, assertions checked against the whole body), the same steps
  `HTTPService.Send` and `Repeat` use. The body's bytes are not kept in
  the body store (no Save or paging from a run's result); the response as
  the view shows it is ([Responses of a run](#responses-of-a-run)).
- **Assertions.** Each result carries the request's assertion results
  (`tests`, `TestResultDTO` as in the response's Tests tab) and their
  counts. A failed or invalid assertion makes the result `failed`; it
  stops the run only with `stopOnFailure`.
- **Extraction rules** (TEST-05, [extraction.md](extraction.md)). Each
  result carries what the request's rules set (`extractions`,
  `ExtractionResultDTO` as in the response's Variables tab); the next
  request of the run renders with the values. A rule that failed or is
  invalid makes the result `failed` too (the value the next requests need
  is missing), listed under the row and in the summary as "Variable
  token was not set: …"; a skipped one never does.
- **Errors.** A request that cannot be built (an undefined variable, a
  missing file) or sent (a refused connection, a timeout) is a result
  with `outcome: "error"` and its `ErrorDTO` (message masked like the
  timeline), and the run goes on (unless `stopOnFailure`). Undefined variables are not asked about
  during a run: each request that has one is listed with
  `template.unresolved` unless `allowUnresolved`.
- **Prompts** (DYN-07). Before the run starts, `RunSession.MissingPrompts`
  builds every HTTP and GraphQL request of the plan (sending nothing) and
  refuses the start with one `template.prompt` error listing the labels
  of all of them without an answer. The UI asks once (the prompt dialog's
  "run" form) and starts again with `prompts`, which every request of the
  run uses. SSE requests never ask, as outside a run.
- **SSE requests** are opened once (no reconnection, SSE-03) and read
  with `sse.ProbeInto` until the server ends the stream or `streamMs` has
  passed (5 s by default), whichever comes first. The result has the
  status and the number of events; a 2xx is `passed`, another status
  `failed`, no answer within the window `sse.no_response`. The request's
  assertions (TEST-08) are checked against the events read, rendered
  with the run's variables ([assertions.md](assertions.md#streams)): the
  result carries them in `tests` and counts them like an HTTP request's
  (a failed one fails it); a message-scoped one has its scope before its
  expression (`last: $.done`, `message 2:`), so the reports say it.
- **Stopping.** `OpsService.Cancel(opId)` stops the run at once: during a
  request (the request is aborted and not listed, and nothing after it
  is sent) or during a delay. The done event is `cancelled`; a run that
  got through is `ok`. There is no `error` outcome for the run itself
  except an internal failure.

### Results and events

Domain `runner`: `runner:op:data` carries `RunResultDTO` items (at most 64
per event), `runner:op:progress` `{done: results listed, total:
iterations × items, message}` with `message` `Sending`, `Waiting before
the next request` or, last, `Stopped at a failure` (`StageSending`,
`StageDelay`, `StageStopped`; the UI words them itself), and
`runner:op:done` the outcome ([events.md](events.md)).

`RunResultDTO{iteration, index, position, requestId, name, kind, method,
path, skip, outcome, startedAt, durationMs, statusCode, status, size,
testsPassed, testsFailed, tests, extractions, events, hasResponse, error}`; `outcome`
is `passed`, `failed`, `error` or `skipped`. A request without assertions
that got a response is `passed`: the status code is not judged, add a
status assertion for that. `hasResponse` says the response was kept for
`Response` (below).

### Responses of a run

RUN-07: clicking a result shows its request and response. The runner
keeps the `ResponseDTO` that `RunSession.Send` returns for every HTTP and
GraphQL result (`responses.go`), and `RunnerService.Response(opId,
index)` returns it: the status line, headers, cookies, the body as the
response view shows it (at most `MaxBodyText`, 1 MiB, with its pretty
form), the assertion results and the timeline, whose hops hold **the
request as sent** (method, URL, headers and body, secrets masked,
[http-client.md](http-client.md)). The UI shows it in the ordinary
response view. Skipped items, errors (no response) and SSE requests (the
stream is not kept) have none: `Response` fails with
`runner.response_gone` (`not_found`), as it does for a response dropped
since.

**The memory bound** is app-wide, not per run: at most
`MaxKeptResponses` (1,000) responses and about `MaxKeptResponseBytes`
(64 MiB, measured as each response's JSON size, what the UI would get)
over every run of every folder, the oldest dropped first. A run of
10,000 iterations keeps the responses of its last 1,000 requests at most;
a folder of large JSON bodies keeps fewer (64 1-MiB bodies with their
pretty form is about 64 MiB). A response larger than the whole budget is
not kept. The UI calls `RunnerService.Release(opId)` when it no longer
shows a run's results: when the next run of the folder has started, when
a start was refused, and when the folder's tab closes. A run the UI
never releases (a window closed) is dropped by the limits in time. A
result whose response is gone says so in its dialog; the run's own list
and counts are unaffected.

Why not the request history: a run adds one history entry per request
(its last send, below), so the entry of an earlier iteration's result is
not there, and history is on disk and shared with single sends. Why not
the body store (`HTTPService.SaveBody`): it keeps raw bytes for a short
TTL and a few bodies, for saving and paging, not the parsed response.
Keeping the DTO in memory with a count and size cap gives every recent
result exactly what it got, with no file written.

### History

A run adds **one entry per request** it sent to the request history
([history.md](history.md)), not one per send: the request's last send
that was not cut short by stopping the run (the cut-short one only when
it is the request's only send), written when the run ends, in the order
the requests were first sent. Recording every send of every iteration
would push the single sends out of the history; one entry per request
keeps each request's latest response at hand in the history while the
run's own list has every result. SSE requests are not recorded, as
outside a run. The runs themselves, with every result, are kept in the
run history ([below](#run-history)).

### Run variables

Every request of a run is rendered with the run's own variables
(`runner.Vars`, a `template.Scope` named `Run`) above every other scope,
the project's runtime variables (ENV-15,
[environments.md](environments.md#runtime-variables)) included:
global < environment < collection < folder < runtime < run
([templates.md](templates.md#scopes-and-precedence)). A run starts
with none; a data-driven run sets each iteration's row in them
([Data-driven runs](#data-driven-runs)), and a request's extraction rules
set them after its response (TEST-05,
[extraction.md](extraction.md#scopes)): a `run` rule sets the run's
variable only, a runtime rule sets the project's runtime variable and the
run's variable of the name, so the next request of the run sees the value
whatever the run held. A value set later in an iteration wins until the
next row. They live as long as the run and are never written to a file. A
runtime variable set by hand during the run counts for the run's next
request unless the run's own variables define the name.

## Reports and run history

RUN-08, RUN-09. When a run ends, `RunnerService` saves its **record**
(`report.Run`): the summary (the folder's and project's names, start,
end and time, how it ended: `ok`, `stopped` at a failure or
`cancelled`, iterations, requests per iteration, the delay, stop on
failure, the data file's name and rows, the counts by outcome, the time
in requests) and every result as the run listed it (iteration,
position, request ID, name, kind, method, folder path, skip reason,
outcome, start, time, status, size, each assertion's target, operator,
values and message, events, the error's kind, code, message and hint).
The record's ID is the run's operation ID, so the Run tab can save a
report of the run it just showed. It is saved **before the done event**,
so the UI finds it when the run ends.

### Run history

- **Where.** In the open project's local directory,
  `<local>/runs/` (`0700`, files `0600`, written atomically), never in the
  project folder: a run's results are this computer's, like the request
  history. Two files per run, `<id>.json` (the record) and
  `<id>.summary.json` (what the list reads), the summary written last so
  a listed run can always be read; a record left without its summary (a
  crash between the two) or a summary without its record is removed by
  the next listing.
- **What is kept.** A run that listed at least one result, whatever its
  end (a run stopped before its first result is not kept). The first
  20,000 results (`report.MaxResults`), the counts covering all of them
  (`omitted` says how many are not kept); each text cut to 4 KiB.
- **Secrets masked** before anything is written, with every secret the
  app keeps, as copied logs are ([privacy.md](privacy.md#run-history-and-reports)):
  names, paths, statuses, assertion values and messages, error messages
  and hints (`report.Run.Masked`, `logexport.Masker`).
- **Responses are not kept**, neither on disk nor in memory beyond the
  session's bounded store ([Responses of a run](#responses-of-a-run)). A
  run of 10,000 iterations would otherwise write gigabytes, a body can
  hold personal data no one meant to keep, and the history of requests
  already keeps each request's last response. A past run's result shows
  its outcome, status, time and assertions; its dialog says the response
  is not kept.
- **Retention.** The newest 50 runs of the project and at most 256 MiB
  of records (`runstore.DefaultMaxRuns`, `DefaultMaxBytes`); the oldest
  go first, and the newest run is always kept. Nothing else ages them.
- **Service.** `Runs()` lists the summaries, newest first
  (`RunSummaryDTO`); `Run(id)` returns one with its results
  (`RunRecordDTO`, `hasResponse` false); `DeleteRun(id)` and
  `ClearRuns()` remove them. Errors: `runner.run_not_found`
  (`not_found`), `runner.invalid_run_id`, `runner.history_unavailable`
  (no project open). A run whose record cannot be saved still runs; the
  failure is logged.

### Reports

`RunnerService.ExportRun({runId, format})` reads the record, masks it
**again** with the secrets known now (a secret typed after the run
counts), writes it in `format` and saves it where the user picks in the
native save dialog, suggesting `<folder>-run-<date>-<time>.<ext>`
(atomically, `0644`). Nothing is written when the dialog is cancelled
(`saved: false`). Errors: `runner.invalid_format`,
`runner.run_not_found`, `runner.save_failed` (`details.path`).

- **JUnit XML** (`junit`, `report.WriteJUnit`), the form test runners
  write and CI systems read as test reports:
  a `testsuites` root with the run's name and totals (`tests`,
  `failures`, `errors`, `skipped`, `time` in seconds, `timestamp`);
  **one `testsuite` per iteration** ("API, iteration 2", or the folder's
  name for one iteration) with its counts, time, the first request's
  timestamp and `properties` (`iteration`, and `dataFile` and `dataRow`
  for a data-driven run); **one `testcase` per request** with `name` (the
  request's, with the iteration when there are several, and "(2)" when
  the same name comes again, so each case is unique: many CI systems key
  cases by name and class name), `classname` (the folder and the folders down to
  the request, joined with `.`) and `time`. A failed result has
  `<failure message type>`: the first failed assertion's message, every
  failed assertion's message a line each, type `assertion`, `invalid
  assertion` or `status` (an SSE status other than 2xx); an error has
  `<error message type>` (the message, the hint, type the error code); a
  skipped item `<skipped message>`; the others `<system-out>` with the
  status, the time and each assertion's outcome. Per iteration rather than
  per folder because the iterations of a data-driven run are its rows, and
  a folder path is already in each case's class name. Characters XML
  cannot hold become U+FFFD; timestamps are UTC without a zone, as the Ant
  schema writes them. Its structure is checked against what CI test
  report parsers read (`report_test.go`, below), not by loading a report
  into a CI system.
- **JSON** (`json`, `report.WriteJSON`): the record as the history keeps
  it, `"schema": "postsonde.run/1"`, the summary's members and
  `results`.
- **HTML** (`html`, `report.WriteHTML`): one page on its own, the summary
  then a table of the results with why each failed; styles inline, no
  scripts, links or remote resources, every text escaped
  (`html/template`).

## UI

A folder's `DIR` tab has a **Run** sub-tab (`FolderRunPanel`), and the
tree's context menu on a folder has **Run folder…**, which opens that
tab on it (`openFolderRun`). The panel has:

- the options: Iterations (1 to 10,000) and Delay (seconds between
  requests, 0 to 3600, decimals allowed), checked as typed with the
  error under the fields, and **Stop at the first failure**; **Run
  folder** starts, and turns into **Stop** while the run runs. With a
  data file, Iterations is disabled and shows the number of rows ("One
  iteration per row of the data file.");
- **Data file** (`RunDataFile`, RUN-05): **Choose data file…** (the
  native dialog), then the file's name, format, rows and columns
  ("users.csv · CSV · 100 rows · 2 columns"), a **Delimiter** select for
  CSV (Detect, comma, semicolon, tab, pipe; changing it reads the file
  again), **Choose another…**, **Remove**, and a table of its first 5
  rows (`<table>` with a caption, column headers, in a focusable
  scrolling region). Why the file cannot be read shows under it
  (`role="alert"`) and keeps **Run folder** disabled until another
  delimiter or file reads. Locked while a run runs; kept for the session;
- **Requests** (`RunRequestList`): the folder's plan in the order of the
  run, a checkbox per request (named by its method or kind badge, name
  and folder path) to put it in or leave it out, "3 of 5 chosen",
  **Select all**, **Select none** and **Reset order** (every request, in
  the tree's order). Requests a run does not send are listed with why,
  their checkbox disabled. **Reordering:** Alt+↑ / Alt+↓ on a request's
  checkbox moves it one place (taken at the edges too), focus stays on it
  and a polite status says "Moved Create to position 3 of 4."; the hint
  under the heading, which every checkbox is described by, says so. With
  the mouse, the ↑ / ↓ buttons of a row (`tabIndex=-1`, `aria-hidden`: the
  keyboard has the keys) or drag and drop (a row's top or bottom half,
  an accent line where it lands; only the list's own drags). Everything
  is locked while a run runs. Nothing chosen disables **Run folder**
  ("Choose at least one request to run.");
- **live status** (RUN-06, `runProgress.ts`): while a run runs, each
  request of the list says where it is in the current iteration: its
  result ("Passed · 12 ms") once sent, "Sending…" (with a spinner, and
  `aria-current="step"` on its row) or "Waiting…" (the delay before it),
  or "Queued". The run's place comes from the progress events (`done`
  results of `iterations × requests`): the request at `done mod
  requests` of the run's order is the one being sent. Once the run ended
  each request shows its latest result, or "Not run" (stopped before it).
  A run started without the plan (the whole folder) has no order to
  follow and shows no status there;
- the run's state ("Running: 3 of 24 done", "Waiting before the next
  request: …", "Stopping the run…", "Run stopped after 7 of 24", "Run
  stopped at a failure after 2 of 24", "Run finished: 24 results"), while
  it runs "Iteration 2 of 3" and a progress bar (`role="progressbar"`,
  "3 of 24 done"), and the counts per outcome, announced once when the
  run ends (`role="status"`); why a run could not start or go on
  (`role="alert"`);
- **Stop** stops at once: the button turns into a disabled "Stopping…"
  as it is pressed and Go cancels the operation (the request in flight is
  aborted, a delay cut short); the done event follows within
  milliseconds;
- **the summary** (RUN-07, `RunSummary`), once a run ended: results,
  passed, failed, errors, skipped, the run's time (the done event's
  `elapsedMs`), the time in requests (sum), the average, the fastest and
  the slowest request (which opens its result), over every result, not
  only the listed ones; then the failures: each failed or errored result
  with its first failed assertion's message or its error, the name
  opening it (the first 20; "N more failures are not listed here");
- the results in the order sent, with a heading per iteration when there
  are several: the outcome, the method or kind badge, the name and the
  folder path, then the status, time and tests (or events), and under a
  row the failed assertions' messages, the error, or why it was skipped.
  The list keeps the last 5,000 results; the counts cover all of them.
  Each result's name is a button that opens it; the buttons are one Tab
  stop, ↑/↓/Home/End move between them;
- **a result** (`RunResultDialog`): a dialog titled "Result of Get user,
  iteration 2" with the outcome, badge, name, time, **Open request** (its
  tab; disabled when the request is gone from the project), the request
  as sent (method and URL of the first hop, masked; its headers and body
  are in the Timeline tab), then the response in `ResponseView` with the
  Tests tab (and the GraphQL view for a GraphQL request), fetched with
  `RunnerService.Response`. An error shows its message, a skipped item
  why, an SSE request its status and number of events, and a response no
  longer kept says so (`role="alert"`). Escape closes it and focus goes
  back to the result. A past run's result says its response is not kept
  and lists each assertion with its outcome and message;
- **Save report** (`RunReportActions`, RUN-08), under the summary of a
  run that ended with results, and of a past run: **JUnit XML**, **JSON**
  and **HTML** (a `role="group"` named "Save report"), each asking where
  in the save dialog, then "Report saved to …" (`role="status"`) or why
  not (`role="alert"`);
- **Past runs** (`RunHistory`, RUN-09), after the requests: the folder's
  runs in the project's history, newest first ("3 runs"), each with when
  it ran, how it ended (Finished, Stopped at a failure, Stopped), its
  counts, time and data file, **Open** and **Delete** (named "Open the run
  of 23 Sep 2026, 14:02"). The list loads when the tab opens and again
  when a run starts or ends. **Open** shows the past run in place of the
  latest one (`PastRunView`): "Past run of …" (the heading takes the
  focus), its state and counts, its data file, that responses are not
  kept, the summary, Save report and the results, all opening as above;
  the run shown is `aria-current` in the list. **Back to the latest run**
  (or starting a run, or deleting the run shown) puts the latest run back
  and focus on the Open button. A run that is gone says so
  (`role="alert"`) and the list loads again.

`commands/folderRuns.ts` keeps one `FolderRun` per folder outside React,
so switching tabs keeps a running run, its results, the plan and the
selection; closing the folder's tab stops the run and drops them. The
panel loads the plan (`FolderRun.loadPlan`) when it opens and whenever
the tree changes; only the latest load lands. A run started before the
plan has loaded (or when it could not be loaded, which the list says)
sends the whole folder, without `requestIds`. Before a run the queued
edits of the folder's requests reach Go. A `template.prompt` refusal opens the
prompt dialog and starts again with the answers; cancelling it puts the
panel back as it was.

## Extension points

What later work builds on:

- **Extraction into variables (TEST-05)** is done
  ([extraction.md](extraction.md)): the HTTP service sets the run's
  `Vars` through `RunOptions.Vars` (any scope with a
  `Set(name, template.Variable)` method) and the project's runtime store.
- **Reports and history of new result fields.** A field added to
  `RunResultDTO` that the history should keep goes into `report.Result`
  and its two mappings (`toReportResult`, `fromReportResult`), and into
  `Masked` when it can hold a secret; the record's `schema` changes when
  an old record could no longer be read.

## Tests

- `internal/core/runner/runner_test.go`: the steps over iterations, the
  selection (custom order, items left out, a skipped item named,
  unknown and repeated IDs, the plan left unchanged), stop on failure,
  the pacer's waits (recorded, never slept) and a stop during a wait,
  the limits, the run variables and their precedence.
- `internal/core/runner/data_test.go`: CSV (header, quoted fields with
  the delimiter, `""` and line breaks, CRLF, a BOM, spaces, blank lines,
  semicolons detected, a quoted header's delimiter not counted, `.tsv`,
  a pipe given, empty fields), JSON (numbers, booleans, null, strings,
  objects and arrays as text; members differing by row; detected without
  an extension; a BOM), every refusal with its line (field counts, bare
  and unclosed quotes, unnamed and repeated columns, not UTF-8, empty,
  not an array, an item not an object, syntax errors, cut short, text
  after the array), the limits (10,000 rows pass, one more, columns,
  bytes, also for JSON), reading from disk (missing, a folder, too
  large) and the row in `Vars` (below values set, replaced by the next
  row, names).
- `internal/app/services/runner/data_test.go`, against `httptest`: a
  100-row CSV file (BOM, CRLF, quotes) makes 100 iterations, the server
  gets each row's values in order and the assertions pass; JSON rows
  (numbers as written, a missing member falling back to the folder's
  variable, null as empty); the dialog (none, cancelled, a delimiter and
  another one, invalid delimiters, a file never picked refused, a bad
  file's path in the error); refused starts sending nothing, and a file
  changed since the preview read again.
- `internal/app/services/runner/runner_test.go`, against `httptest`
  servers and a project on disk: the plan (tree order, subfolders, every
  skip reason, the whole project), the order of the requests the server
  gets over three iterations with the delay between them (recorded, not
  slept), the results per request with their assertion results, a request
  with an undefined variable listed as an error, skipped kinds, stopping
  during a request and during an hour-long delay, SSE streams that end,
  stay open or answer 404, the prompts asked once, the history entries,
  a selection sent in its own order over two iterations with the folder
  unchanged, stopping at a failed assertion and at an error (and not
  without a failure or without bail), the responses of a run (kept for
  every HTTP result, none for skipped, errors and streams, the request as
  sent in the timeline, gone after Release), and the refused starts
  (nothing chosen, unknown and repeated IDs among them).
- `internal/core/runner/report/report_test.go`: the builder (counts,
  time, the results kept and omitted, texts cut on a character), masking
  (the record written holds no secret, the original unchanged), the
  JUnit golden file (`testdata/junit.golden`: two iterations, a data
  file, every outcome, a repeated name, markup and a control character
  in texts, a secret masked), the JUnit structure checked the way CI test
  report parsers read it (strict XML, a `testsuites` root, suites and cases with
  their attributes, times in seconds, at most one of failure, error and
  skipped per case, counts that add up, no unknown elements, unique
  class name and name), one iteration and an empty run, the HTML page
  (escaped, masked, no scripts or links), the JSON round trip, the
  reasons of each outcome.
- `internal/core/runner/runstore/runstore_test.go`: save, list (newest
  first), get, replace, modes `0600` and `0700`, invalid IDs, delete and
  clear, retention by count and by size (the newest kept), torn and
  damaged files cleaned up, leftovers of atomic writes left alone,
  concurrent saves and reads.
- `internal/app/services/runner/history_test.go`, against `httptest`: a
  run that ended listed with its summary and reopened with the same
  results, a secret in an assertion masked in the history's files and in
  what `Run` returns, nothing in the project; the statuses (stopped at a
  failure, cancelled with a result, cancelled before any: not kept) and
  the data file's name without its path; retention and clear; JUnit XML,
  JSON and HTML reports saved where the dialog says, secrets masked, the
  file name, a cancelled dialog, a bad format, a gone run, an unwritable
  folder; no history without a project, and runs still running then.
- `internal/app/services/runner/responses_test.go`: the store's limits by
  count and by size across runs (oldest first), release (and puts after
  it ignored), finished runs forgotten, concurrent runs and readers.
- `internal/app/services/http/run_test.go`: the run's variables win over
  the folder's, no body is kept, the history entry is written on Close.
- `frontend/src/lib/runSelection.test.ts`: merging the order with a
  changed plan, moving, the IDs a run sends.
- `frontend/src/commands/folderRuns.test.ts` and
  `frontend/src/components/runner/FolderRunPanel.test.tsx`: the run state,
  prompts, stopping, the plan loads (the latest wins, the choice kept),
  choosing and reordering with Alt+↑/↓ (focus kept, the move announced)
  and with the mouse, the run sent with the chosen IDs in order and
  stopping at a failure, Select all / none, Reset order, the live status
  of each request over two iterations with the progress bar, Stop
  (Stopping at once, the cancel sent, each request's latest result or
  Not run after), the timing and the summary (totals, run time, average,
  the failures listed), a failure and a result opened from the summary
  and with the keyboard from the results (roving focus, Escape back to
  the result), the request as sent and the response with its Tests tab,
  Open request, a response no longer kept, the previous run released
  once the next one started or the tab closed, the panel's options,
  results, accessibility and the pseudo-locale (the dialog and the live
  list included); a data file chosen, previewed (columns, rows, caption),
  the Iterations field showing the rows, the run sent with the file, the
  iteration of rows shown, the section locked while it runs, another
  delimiter read and sent, Remove; a cancelled dialog keeping the file; a
  file that cannot be read (the alert, Run disabled) read again with
  another delimiter; the data file in the pseudo-locale; Save report
  (the format sent, where it was saved, a cancelled dialog, an error),
  the folder's past runs (other folders' left out, reloaded when a run
  ends, none, not available), a past run reopened (focus on its heading,
  its summary and results, a result opened without asking for a
  response, its report, back to the latest run with focus on Open), a
  past run deleted while shown, one gone, and all of it in the
  pseudo-locale.
