# HAR import and export

HTTP Archive files exchange requests with browser DevTools and other
tools: IMP-07 (import) and EXP-04 (export).

| Piece | Where |
|---|---|
| Import: HAR 1.1/1.2 → import model | `internal/core/importer/harimport` (format `har`), registered in `defaultImporters()` ([import.md](import.md)) |
| HAR 1.2 types, history → HAR, request → HAR, `Validate`, `WriteFile` | `internal/core/har` |
| Export of history entries | `HistoryService.ExportHAR` (`internal/app/services/history/har.go`) |
| Export of a folder's requests | `HTTPService.ExportHAR` (`internal/app/services/http/har.go`) |
| UI | the tree's context menu (`frontend/src/commands/exportHar.ts`, `lib/har.ts`) |

Everything is local and standard library only.

## Import

`harimport.Importer` reads the files the network panels of Chrome, Edge,
Firefox and Safari write ("Save all as HAR", "Export HAR"), and those of
proxies such as Charles. It goes through the import pipeline: preview,
write into a new folder, report ([import.md](import.md)).

**Detect**: `Sure` for a JSON object with a `log` object holding
`creator` or `entries`; `Maybe` for other JSON named `*.har`; `No`
otherwise. **Parse** fails (`import.invalid_file`) for text that is not
JSON or has no `log.entries` list. A `log.version` other than 1.1 or 1.2,
or none, is read as 1.2 (`har.version`, info).

### Grouping and filtering

- Entries are grouped in a **folder per page** (`pageref` → the page's
  title; a page title that is a URL becomes `host/path`), and entries
  without a known page in a **folder per host**. A file whose entries all
  fall into one group puts the requests straight into the import's
  folder, named after the file.
- A recording holds everything the page loaded, so these are **left out
  and counted** in one `har.filtered` info on the file: static resources
  (Chrome's `_resourceType` image, font, stylesheet, script, media,
  manifest, texttrack; without `_resourceType`, a GET or HEAD whose
  response is an image, font, audio, video, CSS, JavaScript or Wasm), CORS
  preflights (`_resourceType: preflight`, or `OPTIONS` with
  `Access-Control-Request-Method`), and **repeats** (the same method, URL
  without fragment and body text as an earlier entry: polling becomes one
  request).
- Entries whose URL is not http, https, ws or wss (`data:`, `blob:`,
  `chrome-extension:`, `about:`…) are **skipped items** in their folder
  (`har.unsupported`, warning). So is an entry that is not an object with
  a `request`.

### What maps

| HAR | PostSonde |
|---|---|
| `request.method`, `request.url` | method; URL without its fragment, the query table read from the URL as written |
| `request.headers` | header rows, as named. HTTP/2 pseudo-headers (`:authority`…) and what the client writes itself (`Host`, `Content-Length`, `Connection`, `Keep-Alive`, `Transfer-Encoding`, `TE`, `Upgrade`, `Proxy-Connection`, `Accept-Encoding`: PostSonde asks for compression and decodes itself) are left out |
| `Cookie` header, `request.cookies` | **dropped** (`har.cookies_dropped`, warning, naming the cookies, never their values) |
| `Authorization: Bearer …`, `Token …` (any other scheme with a credential), a bare token | bearer auth (the scheme as prefix when it is not Bearer; an empty prefix for a bare token) |
| `Authorization: Basic …` | basic auth with the decoded username and password; credentials that do not decode are dropped (`har.auth_dropped`) |
| `Authorization: Digest …` | dropped (`har.auth_dropped`): it answered one challenge |
| `X-API-Key`, `API-Key`, `ApiKey`, `X-Auth-Token` | API key auth in that header when the request has no auth yet; else dropped (`har.auth_dropped`) |
| `Proxy-Authorization`, a second `Authorization` | dropped (`har.auth_dropped`) |
| `postData` `application/x-www-form-urlencoded` | URL-encoded fields, decoded from `text` (else from `params` as written) |
| `postData` `multipart/form-data` | multipart parts from `params` (Firefox), else parsed from `text` with its boundary (Chrome). A file part keeps its file name only (`har.file_part`, info). The `Content-Type` row is dropped: PostSonde writes its own boundary |
| `postData` with `encoding: base64` | not imported (`har.body_skipped`, warning); also a multipart body that does not parse |
| other `postData.text` | raw body; json, xml, html, yaml or text from the MIME type; the `Content-Type` row is kept |
| POST with a JSON body holding a `query` string | `graphql` request: query, `variables` pretty-printed (left out when null or `{}`), `operationName` (also its name). Persisted queries without a query text and batches stay http |
| `ws:`/`wss:` URL, `_resourceType: websocket` or `_webSocketMessages` | `websocket` request (http→ws, https→wss); `Sec-WebSocket-Protocol` becomes the subprotocols, other `Sec-WebSocket-*` rows are dropped. The messages sent become saved messages ("Sent message N", JSON when they are, Base64 for opcode 2; repeats and those past 50 left out); received ones are counted, not imported (`har.websocket_messages`, info). Credential headers are dropped (`har.auth_dropped`): WebSocket requests have no auth |
| `response.status`, `statusText`, `content.mimeType` | the description: "Recorded response: 200 OK (application/json)." |

Names: `METHOD /path` (decoded, without the query), `METHOD host` for the
root path, the operation name of a GraphQL request, `WebSocket /path`.
Timings, the response body, the cache and page timings are not imported.
Texts are imported as recorded: `{{…}}` in a recorded value would be read
as a PostSonde variable.

### Credentials in recordings

A recording from a logged-in browser holds session cookies and
`Authorization` headers. Project files are shared, so:

- **Cookies are dropped**, with a warning naming them. They are the
  browser's session, not part of the request; PostSonde's cookie jar
  keeps the cookies servers set when requests are sent from PostSonde
  ([cookies.md](cookies.md)).
- **Authorization and API key headers become typed auth**, whose secret
  the import writer keeps in the local side files
  (`requestsecrets.Store.MoveOut`, [import.md](import.md#writing)): the
  request works on this machine, and the project files hold no
  credential. The report says so (`har.auth`, info).
- Credentials that cannot become typed auth are **dropped with a
  warning**, never kept as a header row.
- Messages name headers and cookies, never their values.
- Bodies, query strings and other headers are imported as recorded: a
  password in a login form's body or a token in a URL stays there. Check
  such requests after the import and move the values to secret variables.

| Code | Severity | When |
|---|---|---|
| `har.filtered` | info | static resources, preflights and repeats left out (file level, with counts) |
| `har.version` | info | no HAR version, or not 1.1/1.2 |
| `har.cookies_dropped` | warning | the request's cookies were not imported |
| `har.auth` | info | a credential header became typed auth, its secret kept locally |
| `har.auth_dropped` | warning | a credential header was not imported |
| `har.file_part` | info | multipart parts upload files the recording only names |
| `har.body_skipped` | warning | a binary or unparseable body was not imported |
| `har.websocket_messages` | info | a WebSocket's recorded messages |
| `har.unsupported` | warning (skipped) | a URL PostSonde cannot send, or an entry without a request |

## Export

The exports write HAR 1.2 (`log.version` "1.2", `creator`
`{name: "PostSonde", version: <app version>}`), one page per file
(`page_1`, the title, `pageTimings` -1), entries with `pageref`, and ask
where to save with the native save dialog (suggested name: the title with
characters file systems refuse replaced, `.har`). The file is written
through a temporary file and renamed, so a failed write leaves nothing
under the chosen name. Nothing is written when the dialog is cancelled
(`saved: false`).

### History entries

`HistoryService.ExportHAR({ids, requestId, title})`: the entries `ids`,
else those of request `requestId`, else every entry of the open project's
history, oldest first. UI: "Export history as HAR…" in the context menu
of an HTTP or GraphQL request (its entries) and of the tree's empty area
(all of them).

| History | HAR |
|---|---|
| start time, request name | `startedDateTime` (ms, UTC), `comment` |
| request as sent (first hop): method, URL, protocol, header fields, body | `request`: `httpVersion` ("HTTP/1.1" when unknown), `headers`, `cookies` parsed from `Cookie`, `queryString` decoded from the URL, `postData` (MIME type from `Content-Type`; `params` for a URL-encoded body; a Base64 or clipped body says so in `comment`), `headersSize` -1, `bodySize` |
| response snapshot | `response`: status, reason phrase, protocol, headers, `cookies` from `Set-Cookie` (path, domain, expires, httpOnly, secure), `content` (full `size`, MIME type or `x-unknown`, the kept text, `encoding: base64` for a binary body, a `comment` when clipped to 64 KiB or cut at the client's limit), `redirectURL` from `Location` on a 3xx |
| redirects | the request is the first hop, the response and timings the last; the entry's `comment` says how many redirects and where to |
| a send without a response | `status` 0, empty response, `_error` with the (masked) error message, or "Cancelled." |
| remote and local address | `serverIPAddress`; `connection` is the local port |

**Timings** come from the phases recorded with each entry
(`history.Timings`, the final hop's `httpclient.Timing`,
[http-client.md](http-client.md)): `dns`, `connect`, `ssl`, `send`
(request write), `wait` (time to first byte), `receive` (download).
`connect` includes `ssl`, as HAR 1.2 has it; a phase that did not happen
(DNS for an IP address, TLS for http://, all three on a reused connection)
is -1. `blocked` is the rest of the recorded total (request preparation,
the connection pool, earlier redirect hops), -1 when nothing is left.
`time` is the sum of the non-negative phases, which equals the total.
Entries recorded before the phases were kept have `wait` = the duration
and -1 for the others.

**Secrets**: history entries are masked when they are recorded
([history.md](history.md#secrets)); the export writes them as kept and
cannot include a secret, so it has no "with secrets" choice. The result's
`masked` says an entry had one hidden. Cookies are exported as recorded
(cookies are not secrets, [privacy.md](privacy.md#har-files)).

### A folder's requests

`HTTPService.ExportHAR({name, requestIds, includeSecrets})`: each HTTP,
GraphQL and SSE request is built as a send builds it (active environment,
dynamic values, folder headers, auth, settings, the cookie jar's cookies),
exactly as Copy as cURL builds it (`snippet.FromBuild`), and written **not
sent**: `response.status` 0 with the comment "Not sent: exported from the
collection", timings 0 with `blocked`, `dns`, `connect` and `ssl` -1.
Undefined variables stay as written. Basic credentials become an
`Authorization: Basic` header; Digest auth answers a challenge and is
noted in the entry's comment instead. A file body is not read (its file
name is in `postData.comment`); multipart parts are `params` (file parts
by file name). Requests that cannot be built are left out and listed in
`skipped` (`requestId`, `code`, `message`); none built is
`http.export_empty`. UI: "Export as HAR…" in a folder's context menu; the
requests are the folder's, depth first in tree order.

**Secrets are masked by default** (PRV-09) with the same values Copy as
cURL masks ([privacy.md](privacy.md#secrets-in-copied-commands)): secret
variables' values wherever they appear, what the auth adds, the
credentials of every `Authorization` row, the Basic password. The toast
offers **Export with secrets**, which asks for a file again and writes
the real values (`includeSecrets: true`).

| Code | Kind | When |
|---|---|---|
| `history.export_empty` | invalid_argument | no history entry to export |
| `history.export_failed`, `http.export_failed` | io, not_found, permission_denied | the file could not be written (details: `path`) |
| `http.export_empty` | invalid_argument | none of the requests could be built |
| `http.body_gone` | not_found | a saved exchange's response is no longer kept (send again) |
| `http.exchange_too_large` | unsupported | a saved exchange's body is over 256 MiB (details: `size`, `limit`) |
| `history.unavailable`, `http.unavailable` | unsupported | no save dialog (tests, headless) |

### A response's exchange

EXP-06: **Save the exchange as HAR** (the file button in the response's
status line, next to Save body) writes the latest response of a request
with the request as it was sent, whole, as one HAR entry
(`HTTPService.SaveExchange({bodyId, name})`,
`internal/app/services/http/exchange.go`; `har.FromExchange`). HAR was
chosen over a format of PostSonde's own: browsers, proxies and other API
tools read it, PostSonde imports it back ([Import](#import)), and it
holds everything an exchange has.

| Part | What is written |
|---|---|
| request | the first hop as it went over the wire (DBG-01): method, URL, protocol, every header field written, cookies, the query, and the **whole body** as the client recorded it (at most 1 MiB, `httpclient.MaxRecordedBody`; a longer body is cut there and its `comment` says so) |
| response | the final response: status, protocol, headers, cookies, redirect URL, and the **whole body** as kept for Save body (decompressed, not clipped to 64 KiB like the history nor to 1 MiB like the view); text as it is, binary in Base64. A body cut at the size limit (HTTP-37) says so |
| timings, addresses | the final hop's phases and addresses, as for history entries; the entry's `comment` is the request's name and, after redirects, where they led |

The service keeps what it needs with the kept body (`keptExchange` in
`bodies.go`: the exchange as a masked history entry and the send's
masker), in memory only, so it is there as long as the body is (the last 8
responses, an hour) and goes with it; a response no longer kept is
`http.body_gone`. The page is titled with the request's name (the
suggested file is `<name>.har`, `exchange.har` without one). A body over
256 MiB (`MaxExchangeBody`) is refused with `http.exchange_too_large`:
HAR holds the body as JSON text, so it is masked and encoded in memory;
Save body saves it. Earlier responses and examples (HTTP-38, HTTP-39)
have no kept body: "Export history as HAR" exports them as the history
keeps them.

**Secrets** are always masked; there is no "with secrets" choice. First
with the send's secrets, as the timeline and the history mask them (the
secret variables, what the auth added, the credentials of every
Authorization field sent, the URL's password), in the URLs, headers and
both bodies; then again with every secret the app keeps, as copied logs
are ([privacy.md](privacy.md#copied-and-saved-logs)): the project's
local secret files, the stored OAuth 2.0 tokens, the runtime variables
and the app-wide secrets (`WithExchangeSecrets`). A Base64 body is masked
in its bytes. The result's `masked` says something was hidden, and the
note under the status line says so.

### Validation

`har.Validate(data)` checks a file against the members HAR 1.2 requires
(log version and creator, each page's and entry's required members,
request and response members and types, `send`/`wait`/`receive` not
negative) and returns the problems as `path: problem`. The export tests
validate every file they write.

## Tests

- `internal/core/importer/harimport/harimport_test.go`: golden files
  (`testdata/*.golden`, rewrite with `-update`) of a Chrome recording (one
  page: document, script and image, preflight, bearer XHR and fetch, a
  repeat, GraphQL, a form, multipart, a WebSocket with messages, a `data:`
  URL, Basic plus an API key, Digest, a failed request), a Firefox
  recording (two pages, a custom token scheme, form params, multipart
  params, an API key header, a `blob:` URL) and a proxy file without pages
  or version (grouped by host, a Base64 body); detection among the other
  formats; invalid files with their lines.
- `internal/app/services/collection/import_har_test.go`: both recordings
  through the default formats into a project; the typed auth is readable
  through the service, and no token, password, API key or cookie is in
  the project's files.
- `internal/core/har/har_test.go`: golden HAR files of history entries
  (timings, redirect, cookies, binary clipped body, failure,
  cancellation) and of requests masked and with secrets, each validated;
  timing arithmetic, `Validate`'s findings, file names, atomic writes.
- `internal/app/services/history/har_test.go`,
  `internal/app/services/http/har_test.go`: the services with a fake save
  dialog (all, one request's and chosen entries; masked and with secrets;
  skipped requests; cancel; unwritable path; no dialog), and
  `internal/app/services/http/history_test.go` checks the phases and
  addresses recorded with an entry.
- `internal/app/services/http/exchange_test.go`: a request body of 200
  KiB and a response body of 3 MiB saved whole, masked with the send's
  and the stored secrets (URL, headers, both bodies, an echoed header),
  twice, a binary body in Base64, a body no longer kept, no dialog;
  `TestFromExchangeComments` in `internal/core/har`.
- `frontend/src/components/response/SaveBody.test.tsx`: the exchange
  button and its note.
- `frontend/src/commands/exportHar.test.ts`,
  `frontend/src/components/tree/TreeActions.test.tsx`: the commands,
  toasts and context menu items.
