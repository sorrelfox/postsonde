# HTTP client

`internal/core/httpclient` sends HTTP/REST requests with Go's `net/http`,
over the shared dialer ([network.md](network.md)). Its promise (HTTP-01
goal, PRV-05): **the request on the wire is the request the user
configured.** This page lists what the client adds (little, all visible) and
what `net/http` still decides.

```
Request (editor model)
   │ prepare: validate, map rows onto http.Request fields, keep the path raw
   ▼
http.Client ── CheckRedirect (Settings) ── no net/http cookie jar
   │
cookieTransport ── per hop: record Cookie sent; optional jar (docs/cookies.md)
   │
http.Transport ── DialContext = netx.Dialer ──▶ guard, route, proxy, trace
   │ TLS on the dialed connection (ALPN h2 / http/1.1), no env proxy, no gzip
   ▼
Response: status, headers, raw body bytes (capped)
```

## API

| Item | Purpose |
|---|---|
| `Request{Method, URL, Headers, Body, Settings, Digest}` | The request as the editor holds it. `Digest` (`*DigestCredentials{Username, Password}`) answers a Digest challenge, [below](#digest-authentication-auth-06). |
| `Header{Name, Value, Enabled}` | One header row. `Enabled` is false in the zero value: only enabled rows are sent. |
| `Body` interface (`Open() (io.ReadCloser, int64, error)`, `ContentType() string`) | Request bodies. `Open` may be called again for a 307/308 redirect; a length of -1 means unknown (sent chunked). |
| `RawBody{Data, Type}` | Raw bytes, sent byte for byte (raw and URL-encoded bodies). The multipart and file bodies of package `request` stream from disk ([Multipart and file bodies](#multipart-and-file-bodies)). |
| `Settings{Timeout, ResponseHeaderTimeout, FollowRedirects, MaxRedirects, KeepMethod, ForwardAuthorization, MaxBodyBytes}`, `DefaultSettings()` | Per-request execution settings. |
| `TransportOptions{RootCAs, InsecureSkipVerify, TLSConfig, MaxIdleConns, MaxIdleConnsPerHost, IdleConnTimeout, TLSHandshakeTimeout}` | Connection settings of a client. `TLSConfig` (from `tlsconf.Build`, [below](#tls-options)) replaces `RootCAs` and `InsecureSkipVerify`. |
| `NewTransport(d netx.Dialer, o) *http.Transport` | The transport, for other clients built on `net/http` (GraphQL, OAuth). |
| `New(d netx.Dialer, o) *Client`, `(*Client).Send(ctx, Request) (*Response, error)`, `CloseIdleConnections()` | Keep one `Client` and reuse it, so connections are reused. Safe for concurrent use. |
| `(*Client).SendStream(ctx, Request, StreamFunc) (*Response, error)`, `StreamFunc func(head *Response, body io.Reader) error` | Sends like `Send` but hands the body to the callback as it arrives instead of reading it whole, for responses that do not end on their own (SSE, [Streaming responses](#streaming-responses)). |
| `CookieJar` interface, `(*Client).WithCookieJar(jar) *Client` | An optional cookie jar (CK-01), on a copy of the client that shares its connections. [cookies.md](cookies.md). |
| `Response{Proto, StatusCode, Status, Headers, Trailers, Body, BodyTruncated, Size, ContentLength, URL, Duration, Timing, Conn, Redirects, Events, RequestCookie, Request}` | The response as received, with its timing, the Cookie header the final hop sent and the final request as written. |
| `Timing{DNS, Connect, Proxy, TLS, RequestWrite, Wait, Download, Total, Observed, Offsets}`, `Phases` (`PhaseDNS` … `PhaseDownload`, `PhaseProxy`), `PhaseOffsets` | Phase breakdown of one hop (DBG-03), below. |
| `Conn{Reused, WasIdle, IdleTime, LocalAddr, RemoteAddr, Protocol, TLS}`, `TLSInfo`, `CertInfo` | The connection of one hop (DBG-04, DBG-05). |
| `Hop{Kind, URL, StatusCode, Start, Timing, Conn, RequestCookie, Request, Proto, Status, Headers}` | A hop before the final one, a redirect (`HopRedirect`) or an answered auth challenge (`HopAuth`): its request as written and the response's head. |
| `TimelineEvent{At, Name, Detail}`, `Event*` constants | The raw trace of a send (DBG-01). |
| `SentRequest{Method, URL, Proto, Target, Headers, Body, BodySize, BodyTruncated}`, `Head()`, `Masked(Masker)`, `MaxRecordedBody` | A hop's request as written (DBG-01), [below](#request-as-sent-dbg-01). |
| `Masker` interface, `SecretMasker`, `NewSecretMasker(secrets)`, `Mask` | The masking hook for showing a `SentRequest` without its secrets. |
| `Code*`, `Hint*` constants | Error codes and hints, below. |

## Settings and defaults

| Setting | `DefaultSettings()` | Zero value | Why |
|---|---|---|---|
| `Timeout` (whole exchange, body included) | none | none | Postman and Insomnia default to no limit too. Long polls, slow endpoints and big downloads are legitimate, and the user can cancel at any time (HTTP-20). Connecting is still bounded by the dialer (30 s) and the TLS handshake by the transport (10 s). The app sets it per request or globally (HTTP-21, [below](#timeouts-http-21)). |
| `ResponseHeaderTimeout` (from the written request to the first response byte, per hop) | none | none | Off unless asked for, as above. Once headers arrive it stops, so a slow body is not cut. |
| `FollowRedirects` | true | false | Every tool follows redirects by default. The zero value is "off" so a bare `Settings{}` never sends more than one request. |
| `MaxRedirects` | 10 | 0 | PRV-11 wants redirects capped. 10 is Postman's and Go's default. Negative is an error. 0 follows none: the first 3xx response is returned, as with `FollowRedirects` false (HTTP-22). There is no "unlimited" (`TestSafeDefaultRedirectCap`). |
| `KeepMethod` | false | false | 301, 302 and 303 turn a method other than GET and HEAD into a GET without body, as browsers, curl and Postman do. On, the original method, body and body headers are sent again (HTTP-22, like Postman's "follow original HTTP method"). 307 and 308 keep them either way. |
| `ForwardAuthorization` | false | false | `Authorization` goes only to the request's origin (PRV-11). On, it is sent to every hop, other hosts and plain `http` included, like curl's `--location-trusted` (HTTP-22). `Proxy-Authorization` and `WWW-Authenticate` stay dropped. |
| `MaxBodyBytes` | 100 MiB | 100 MiB | Keeps a runaway response from exhausting memory in `Send`; Postman's default limit is 50 MB. Negative means no limit. The app sends with `SendTo`, which streams the body to disk, and sets it from the `network.maxResponseBodyMb` setting (default 512 MiB, [Large responses](#large-responses-http-37)). |

| `URLEncoding` | `URLEncodingWHATWG` | `URLEncodingAsTyped` | How the path and query are percent-encoded (HTTP-24, [below](#url-encoding-http-24)). The editor's default encodes as browsers do; the zero value keeps a bare `Settings{}` sending the URL as typed. |
| `HTTPVersion` | `HTTPVersionAuto` | `HTTPVersionAuto` | Which protocols are offered (HTTP-25, [below](#http-version-http-25)): h2 and http/1.1 through ALPN on https, HTTP/1.1 on plain http. `HTTPVersion1` and `HTTPVersion2` restrict it. |
| `SkipSendingCookies`, `SkipStoringCookies` | false | false | With a cookie jar (`WithCookieJar`), leave its cookies out of the request or keep the response's `Set-Cookie` values out of it (HTTP-26, [cookies.md](cookies.md#using-it-from-a-send)). Without a jar they change nothing. |

Negative timeouts or redirect limits, and an unknown `URLEncoding` or
`HTTPVersion`, are `invalid_argument` / `httpclient.invalid_setting`
(detail `field`).

## URL: what is kept

The URL is split by hand, not with `url.Parse`, so the path and query reach
the request line as typed, apart from the encoding of `Settings.URLEncoding`
([below](#url-encoding-http-24)). With `URLEncodingAsTyped`:

- **Kept:** percent-encoding as typed (`%2F` stays `%2F`, `%7e` stays
  lowercase), invalid escapes (`%zz`), reserved and unusual characters
  (`{}`, `|`, `;`, `+`), repeated parameters, parameter order, `?` with an
  empty query (`/p?`). Escapes are never decoded or re-encoded in any mode.
- **Encoded:** only bytes that cannot appear in a request line: control
  characters, space, DEL and non-ASCII bytes (UTF-8 bytes become `%XX`, e.g.
  `é` → `%C3%A9`). Every mode encodes these.
- **Not sent:** the fragment. An empty path becomes `/`.
- **Scheme:** `http` or `https`, any case; anything else is `unsupported`.
- **Userinfo** (`https://user:pass@host/`) is never sent in the request
  line. As in browsers and curl, it becomes `Authorization: Basic …`, unless
  an `Authorization` row is enabled.
- **Host:** kept as typed (case included). Internationalized names are sent
  as punycode by `net/http`.
- **Limitation:** a path starting with `//` goes through `URL.RawPath`, where
  Go's encoding applies (`{` becomes `%7B`, and invalid escapes are
  rejected).

How: the path is put in `URL.Opaque`, which `net/http` writes unchanged; the
query goes in `RawQuery`, which it never re-encodes. `Path` and `RawPath` are
set too, so relative redirects resolve.

### URL encoding (HTTP-24)

`Settings.URLEncoding` (file: `settings.urlEncoding`) picks which
characters of the path and query are percent-encoded, as `%XX` with
upper-case hex. `#` ends the URL (the fragment is never sent) and the first
`?` starts the query in every mode.

| Mode | File | Path: also encoded | Query: also encoded | Kept |
|---|---|---|---|---|
| `URLEncodingWHATWG` | `auto` (default) | `"` `<` `>` `` ` `` `^` `{` `}` | `"` `<` `>` `'` | `[ ] \| \ ;` and every other printable ASCII character, `%` as typed |
| `URLEncodingRFC3986` | `rfc3986` | everything but unreserved (`A-Z a-z 0-9 - . _ ~`), sub-delims (`! $ & ' ( ) * + , ; =`), `:`, `@`, `/`; a `%` not followed by two hex digits becomes `%25` | the same, `?` kept | valid `%XX` escapes |
| `URLEncodingAsTyped` | `none` | nothing more | nothing more | everything printable, invalid escapes included |

"Also" is on top of what every mode encodes (control characters, space,
DEL, non-ASCII). `auto` is the WHATWG URL Standard's path and
special-query percent-encode sets, what browsers send; it does not turn `\` into `/` or resolve `.` and `..` segments
as browsers do, so the path stays as typed otherwise. `rfc3986` is for
servers that reject `[`, `]`, `|` or `{` (strict proxies, some Java
servers); `none` is for testing how a server treats raw characters. The
wire goldens `url_encoding_auto`, `url_encoding_rfc3986` and
`url_encoding_none` send one URL with every such character in each mode,
and `TestURLEncodingModes` checks what an `httptest` server receives.
Values that must keep reserved characters literal (`&` or `=` inside a
query value) have to be encoded by the user in every mode: a mode never
guesses what is data and what is a delimiter.

## No hidden changes

Each row is covered by a wire test: `TestWire` sends to a raw TCP listener
and compares the bytes that arrive with `testdata/wire/*.golden`.

| `net/http` would… | httpclient does | Test (golden or test name) |
|---|---|---|
| add `User-Agent: Go-http-client/1.1` (`/2.0`) | send no User-Agent unless a row sets one (`Header["User-Agent"] = [""]`) | `no_default_headers`, `user_agent_*`, `TestHTTP2` |
| add `Accept-Encoding: gzip` and decompress silently | `DisableCompression`: nothing added, body bytes as sent | `accept_encoding_as_typed`, `TestNoDecompression` |
| canonicalize names set with `Header.Set` | set map keys as typed: `x-lower`, `X-MiXeD-CaSe` go out as spelled | `header_casing_duplicates_disabled` |
| merge rows into one key | keep duplicates as separate fields (`X-Dup: a`, `X-Dup: b`) and skip disabled rows | same |
| take `Host` from the URL | honour a `Host` row via `req.Host` | `host_override` |
| drop a `Content-Length` row and compute one | compute it from the body; a row must match, otherwise `content_length_mismatch` | `post_raw_body`, `content_length_row`, `TestPrepareErrors` |
| — | send `Transfer-Encoding: chunked` only when a row asks for it | `chunked`, `chunked_empty` |
| read `HTTP_PROXY`/`HTTPS_PROXY` (`ProxyFromEnvironment`) | `Proxy = nil`; proxies are dialers (the app's dialer takes the proxy of the settings or `ALL_PROXY`, shown in Settings, Network, Proxy; [network.md](network.md#proxy-settings)) | `TestNewTransport` |
| keep cookies in a jar | no jar unless `WithCookieJar` attaches one; then the jar's cookies follow the rows, a row wins over a jar cookie of the same name, and each hop's Cookie header is in `RequestCookie`; `SkipSendingCookies` and `SkipStoringCookies` leave it out per request ([cookies.md](cookies.md)) | `TestNoCookieJar`, `TestCookieJar`, `TestCookieSwitches` |
| follow 10 redirects | follow per `Settings` (`CheckRedirect`) | `TestRedirects` |
| add `Referer` on redirects | remove it, unless a row spelled `Referer` sets it | `TestRedirects` |
| turn URL userinfo into Basic auth behind the scenes | do it explicitly, only without an `Authorization` row | `userinfo_*` |

What httpclient itself adds, all of it visible to the user (HTTP-09):
`Host` from the URL, `Content-Length` from the body, `Content-Type` from the
body type when no `Content-Type` row is enabled (HTTP-16),
`Authorization` from URL userinfo, with `Request.Digest` the
`Authorization: Digest` answer to a 401 challenge (on a second request,
shown as its own hop), and, only with a cookie jar attached,
the jar's cookies in `Cookie` (shown in `RequestCookie` and the timeline).

### Header preview (HTTP-09)

`Preview(r Request, jar CookieJar) ([]Header, error)` (`preview.go`)
returns the header fields `Send` would write for `r` on HTTP/1.1, in the
order written, without connecting. It runs the same `prepare` as `Send`
(so the rules above apply: a `Host` row, the body's `Content-Length` or
`Transfer-Encoding: chunked`, its `Content-Type`, URL userinfo), adds the
jar's cookies with the same `addJarCookies` (unless
`SkipSendingCookies`), and writes the head with net/http's request writer,
the one the transport uses, into a writer that stops at the blank line:
the body is opened and read at most a few KiB, then closed. It cannot show
the `Authorization: Digest` answer (computed from the server's challenge),
redirect hops, or the HTTP/2 form. `TestPreviewMatchesSend` compares it
with `SentRequest.Headers` of a real send for a GET, an empty POST, raw,
large and chunked bodies, `Host`/`User-Agent`/`Content-Type` rows,
userinfo, and a jar with and without a `Cookie` row or switched off.

## Methods

`request.Method(m)` (`internal/core/request/method.go`, HTTP-01, HTTP-02)
checks the method before a send; httpclient checks it again.

| Rule | Why |
|---|---|
| Any RFC 9110 token is valid: letters, digits and ``!#$%&'*+-.^_`\|~``, at least one. `PURGE`, `PROPFIND` and `M-SEARCH` are sent like `GET`; `QUERY` is a standard method ([below](#query-http-03)). Otherwise `invalid_argument` / `request.invalid_method`. | Custom methods (HTTP-02) are part of HTTP. |
| The method is sent **as typed, case included**. `get` is not normalized to `GET`. | Methods are case-sensitive (RFC 9110 §9.1): `get` is a different, custom method, and servers may reject it. `request.CanonicalMethod(m)` returns the registered spelling (`GET` for `get`) so the editor can warn; it never changes what is sent. |
| `CONNECT` (in that spelling) is `unsupported` / `request.method_unsupported`. | It opens a tunnel instead of sending a request; proxies are dialers. |
| `request.BodyForMethod(method, body)` drops the body of `HEAD` and `TRACE` (exact spelling) and returns a `Warning{Code: "request.body_omitted"}` when there was one. | A client must not send content with them (RFC 9110 §9.3.2, §9.3.8); a HEAD request stays sendable while its body type is kept for switching back. |
| `GET`, `DELETE`, `OPTIONS` and custom methods send their body. | Allowed by RFC 9110 (no defined meaning) and by `net/http`; some APIs (search endpoints, bulk deletes) use it. |

`TestMethodsOnTheWire` sends each standard method and `PURGE`, `PROPFIND`,
`get`, `Patch` and `M-SEARCH` to an `httptest` server and checks
the method, the substituted path and the body that arrive.
`Content-Length` of an empty body follows `net/http` (below).

### QUERY (HTTP-03)

`QUERY` (draft-ietf-httpbis-safe-method-w-body) is a safe, idempotent
method with a body: a GET whose query does not fit in the URL. The picker
offers it (`request.StandardMethods`), its body is sent like a POST's, and
httpclient gives it the semantics of a safe method where `net/http` would
treat it like any custom method:

- **Redirects:** a 301 or 302 keeps `QUERY` and the body, since RFC 9110
  (sections 15.4.2 and 15.4.3) lets a client change only POST to GET there;
  `net/http` turns every method but GET and HEAD into a GET. A 303 is
  followed with a GET without body, as for any method, unless
  `Settings.KeepMethod`; 307 and 308 keep them anyway (`TestQueryMethod`).
- **Retries:** `net/http` resends a request on a new connection when a
  kept-alive one turns out closed only for GET, HEAD, OPTIONS, TRACE and
  requests with an `Idempotency-Key` field. A QUERY request gets an
  `Idempotency-Key` entry without values, `net/http`'s documented marker,
  which is never written (`query_method` golden, `TestQueryIdempotent`).

## Path parameters

`request.PathParams(url)`, `PathParamNames(url)` and
`SubstitutePath(url, rows, expand)` (`pathparams.go`, HTTP-05) find the
`:name` and `{name}` placeholders of the URL as typed and fill them from
the request's `pathParams` rows ([file-format.md](file-format.md)). The
sender calls `SubstitutePath` on the typed URL first, then renders the
URL's own `{{variables}}`. The editor's path table uses a TypeScript mirror
of the detection, `frontend/src/lib/pathParams.ts`; both are tested
against `internal/core/request/testdata/path_params.json` (detection on
both sides, substitution in Go).

**Detection**

- Only the **path**: from the first `/` after the authority
  (`scheme://userinfo@host:port`; without `://`, the text before the first
  `/`) to the first `?` or `#`. A port (`:8080`), an IPv6 literal
  (`[::1]`), the query and the fragment never hold placeholders.
- `{{variables}}` are skipped, and a `?`, `#` or `/` inside one does not
  end the path. Placeholders inside a variable's value are not found: the
  URL is searched as typed.
- `:name` starts right after a `/` and takes the letters, digits and `_`
  that follow: `/users/:id.json` is `id`; `/a:b`, `/::`, `/:` and
  `/:{{v}}` are nothing.
- `{name}` is one pair of braces around letters, digits, `_`, `-` and `.`
  (OpenAPI names such as `{user-id}`), anywhere in the path
  (`/report-{year}.csv`). `{{…}}`, `{}`, `{ id }` and `{id}}` are not
  placeholders.
- A name used more than once is one row; every use gets its value.

**Substitution**

| Rule | Why |
|---|---|
| A placeholder takes the value of the first row with its name, compared case-sensitively. A row's `disabled` flag is ignored. | Every placeholder needs a value; the table has no toggle. |
| `expand` renders the value's `{{variables}}` first; its error keeps its kind and code and gets the details `path_param` and `field: value`. | As for headers and form fields. |
| The value is **plain text**, encoded as one path segment: every byte except the RFC 3986 `pchar` characters (letters, digits, ``-._~!$&'()*+,;=:@``) becomes `%XX` with upper-case hex. `a/b?c#d e%` is sent as `a%2Fb%3Fc%23d%20e%25`; `é` as `%C3%A9`. | A value holding `/` must stay one segment. |
| Already-encoded text is encoded again: `a%2Fb` is sent as `a%252Fb`. | Like form fields, and unlike the query params: the value is never shown inside the URL, so there is no typed URL text to keep byte for byte. One rule, no guessing whether `%41` was meant literally. |
| A name with no row, or with an empty value (after expansion), keeps its placeholder, and `invalid_argument` / `request.path_param_missing` is returned with that URL; `path_params` lists the names, comma-separated. The send must stop. | Sending `/users/:id` literally is never what was meant. |
| Rows whose name is not in the URL are ignored. | |

## From the header table to `Headers`

`request.Headers(rows, expand)` (`internal/core/request`, HTTP-07) maps
a request file's `headers` rows ([file-format.md](file-format.md#rows)) onto
`[]Header`. The sender (`request.BuildHTTP`) calls it; nothing else decides which rows are
sent.

| Rule | Why |
|---|---|
| Rows with `"disabled": true` are skipped, and never validated. | The toggle keeps a row without sending it. |
| Rows whose name is empty as typed are skipped, whatever their value. | A row being filled in; the tab badge does not count it either. |
| The other rows keep their order, duplicates and the spelling typed; values are not trimmed. | "The request on the wire is the request the user configured." What `net/http` still changes is below. |
| `expand` (optional) renders `{{variables}}` in the name, then the value; the rendered text is validated and sent. An `expand` error keeps its kind and code (`template.unresolved`). | Validation must see what is sent. |
| A name must be an RFC 9110 token: letters, digits and the symbols ``!#$%&'*+-.^_`\|~``; no spaces, colons or non-ASCII. `request.invalid_header_name`. | Anything else is not a header name. |
| A value must not contain CR, LF, NUL or another control character except tab. `request.invalid_header_value`. | Header injection. `Send` checks this too, but without the row. |

Errors are `invalid_argument` with the details `row` (index in the table,
from 0, skipped rows included, so the editor can point at the row),
`field` (`name` or `value`) and `header` (the name as typed). Values never
appear in errors.

The editor's header suggestions (docs/frontend.md) mark `Host`,
`Content-Length`, `Transfer-Encoding`, `Connection`, `User-Agent` and
`Content-Type` with what the client does with them, from the tables above.

## Form bodies

`request.FormURLEncoded(fields, expand)` (`formbody.go`, HTTP-13)
encodes a `urlencoded` body's `fields` rows; `request.Body` sends the result
([json-tools.md](json-tools.md#sending-bodies)).

| Rule | Why |
|---|---|
| Rows with `"disabled": true` are skipped, and so are rows whose name and value are both empty. A row with only a value is sent as `=value`. | Same as the URL params table: only a row being filled in is left out. |
| The other rows keep their order and duplicates, joined with `&` as `name=value`; `=` is written even for an empty value (`flag=`). | The WHATWG serializer, which browsers use for forms. |
| Rows hold **plain text**, not pre-encoded text: `a&b` is sent as `a%26b`, `100%` as `100%25`, a typed `%20` as `%2520`. | Form editors (browsers, Postman, Insomnia) encode what is typed. Unlike the URL's params, the text is not visible in an encoded form anywhere, so there is nothing to keep byte for byte. |
| `expand` (optional) renders `{{variables}}` in the name, then the value; the rendered text is encoded. An `expand` error keeps its kind and code and gets the details `row`, `field` (`name` or `value`) and `form_field` (the name as typed). Values never appear in errors. | A variable holding `a&b` must not split the field. |
| Encoding: the `application/x-www-form-urlencoded` percent-encode set of the WHATWG URL standard. ASCII letters, digits and `*-._` stay; a space becomes `+`; every other byte of the UTF-8 text is `%XX` with upper-case hex (`~` is `%7E`, `é` is `%C3%A9`). | Not `url.QueryEscape`, which keeps `~` and encodes `*`. |

No fields (or only skipped ones) send a zero-length body, still with the
form Content-Type.

## Multipart and file bodies

`request.Body(body, expand, root)` maps the `multipart` (HTTP-14) and
`file` (HTTP-15) modes to bodies that are **streamed from disk**, never
loaded into memory; `root` is the project root that relative paths are
resolved against.

- **Streaming.** `FileBody.Open` opens the file and returns a reader of
  exactly its size. `MultipartBody.Open` returns the read end of an
  `io.Pipe`; a goroutine writes the parts with `mime/multipart`, copying
  each file part by part. Closing the reader early (a cancelled send)
  stops the goroutine and closes its file. Both can be opened again, so a
  307/308 redirect (and a transport retry, through `GetBody`) re-reads the
  files. `TestLargeFileUploadMemory` sends a 110 MB file both ways to a
  server that discards it and checks that the process allocates under
  16 MB (it measures about 0.1 MB).
- **Content-Length.** Both lengths are known before sending, so servers
  that require `Content-Length` work: a file body's is the file's size; a
  multipart body's is computed when it is mapped by running the very same
  writer over a counting writer that adds each file's size instead of
  reading it. A file that shrinks or grows between mapping and sending
  fails the send with `request.body_file_changed` (kind `io`) rather than
  sending a body that does not match.
- **Files are checked when the body is mapped.** A missing file is
  `not_found` with code `request.body_file_missing`; a folder or unreadable
  file `request.body_file_unreadable`; a file body with no file
  `request.body_file_unset`. Multipart errors carry the part's row
  (`row`) and typed name (`part`) and the message names both, e.g. `The
  file of multipart part 2 ("avatar") was not found: fixtures/avatar.png.`
  Every error has the stored path in `path`. Disabled parts are not
  checked. A relative path with no project root is reported as missing,
  never read from the working directory.
- **Boundary.** `mime/multipart`'s random boundary (60 hex digits from
  `crypto/rand`), generated once per mapped body and reused on every
  `Open`, so the `Content-Type` sent matches the body after a redirect.
- **Parts.** Disabled parts are skipped, and so are parts with an empty
  name, value and file (a row being filled in); the others keep their
  order and duplicates. `expand` renders a part's name and a text part's
  value; file contents and paths are never expanded. A text part has no
  `Content-Type` unless its `contentType` is set; a file part has
  `request.FileMediaType(file, contentType)`. The `contentType` of parts
  and file bodies is sent as typed; a line break or control character in
  it is `request.invalid_part_content_type`.
- **Names and filenames.** `Content-Disposition: form-data; name="…";
  filename="…"`, the filename being the base name of the path. Quoted as
  the HTML standard's multipart/form-data encoding does (and browsers and
  curl): `"` becomes `%22`, CR `%0D`, LF `%0A`, everything else, non-ASCII
  included, is sent as UTF-8. RFC 7578 (section 4.2) rules out
  `filename*`; Go's own writer escapes with backslashes instead, which
  servers following the HTML standard do not undo.
- **Extension types.** `request.MediaTypeByExtension` is a fixed table of
  about 30 common extensions (`.png`, `.json`, `.pdf`, `.csv`, …), not
  `mime.TypeByExtension`, which reads the system's MIME files and would
  make the same request differ between machines. The frontend has the
  same table; both are checked against the `extensions` of
  `internal/core/request/testdata/content_type.json`.
- **Paths.** `request.StoredFilePath(root, abs)` is what the file dialog
  stores: relative to the project root with `/` when the file is inside
  it, else absolute; `request.ResolveFilePath(root, stored)` reverses it
  ([file-format.md](file-format.md)). File contents are never copied into
  project files.

## Auto Content-Type

`request.ContentType(body)` (`contenttype.go`, HTTP-16) is the
`Content-Type` a body implies; `request.Body` sets it as the body's
`ContentType()`, and httpclient adds it unless an enabled `Content-Type`
row is sent (the check is on the names after expansion, so `{{ct}}`
rendering to `content-type` counts; `request.HasContentType` and
`request.ImplicitContentType(body, headers)` make the same decision for
callers that need it).

| Body mode | Content-Type |
|---|---|
| `none`, `""`, unknown | none |
| `raw` | `request.RawMediaType(type)`: `application/json`, `application/xml`, `text/html`, `text/plain` (also for `""`), `application/yaml`; none for an unknown type |
| `urlencoded` | `application/x-www-form-urlencoded` |
| `multipart` | `multipart/form-data; boundary=…`; `request.ContentType` returns it without the boundary, which is generated when the body is built (`MultipartBody.ContentType()`) |
| `file` | `request.FileMediaType(path, contentType)`: the body's `contentType` when set, else the type of the file name's extension, else `application/octet-stream` |

- **No charset** is added to any of them: JSON and YAML are UTF-8 by
  definition, the form encoding is ASCII, and adding a parameter the user
  did not ask for would be a hidden change. A row can set one.
- **Override:** any enabled row named `Content-Type`, in any letter case,
  replaces the implicit header, even with an empty value. A disabled row
  does not. With no body, nothing is added whatever the rows say, and an
  enabled row is sent as typed.
- The editor shows the implicit header above the Headers table
  ([frontend.md](frontend.md#request-editor)) with a TypeScript mirror of
  this rule, `frontend/src/lib/contentType.ts`. Both are tested against
  `internal/core/request/testdata/content_type.json`
  (`TestImplicitContentTypeFixture`, `contentType.test.ts`), so a case
  added there checks both. `TestContentTypeOnTheWire` checks what an
  `httptest` server receives.

## Body examples

A request can keep several named bodies and send one of them (HTTP-17).
The send path does not know about them: the active example's body is the
request's `body`, and switching examples (in the editor,
`commands/bodyExamples.ts`) moves the bodies between `body` and
`bodyExamples` ([file-format.md](file-format.md#body-examples)). So the
next send, Copy as cURL, history and HAR all see the active body.
`TestSendActiveBodyExample` sends before and after a switch;
`TestBodyExamples` (collection service) saves a switch through auto-save.
Response examples (HTTP-38) are another thing: responses saved with the
request, never sent ([history.md](history.md#response-examples),
[file-format.md](file-format.md#response-examples)).

## SOAP helper

SOAP over HTTP (HTTP-18) is an ordinary request: POST, a raw XML body
holding the envelope, and the headers the SOAP version needs. There is no
WSDL tooling. `internal/core/soap` writes the two pieces, and the body
editor's **SOAP…** panel applies them through
`FormatService.SOAPEnvelope` ([frontend.md](frontend.md#body-editor)):

| | SOAP 1.1 | SOAP 1.2 |
|---|---|---|
| Envelope namespace | `http://schemas.xmlsoap.org/soap/envelope/` | `http://www.w3.org/2003/05/soap-envelope` |
| `Content-Type` | `text/xml; charset=utf-8` | `application/soap+xml; charset=utf-8; action="…"` (no `action` without one) |
| `SOAPAction` | the action as a quoted string, `""` for none | none (a row of that name is removed) |

- **`soap.Envelope(version, text)`** wraps the current raw body: blank
  text gives an envelope with a placeholder comment in `Body`; text whose
  root element is an `Envelope` in either SOAP namespace is kept, with the
  other version's namespace URI replaced, so switching versions keeps what
  was typed; anything else (the operation's element) is indented into the
  `Body` of a new `soap:Envelope` with an empty `soap:Header`, and a
  leading XML declaration moves in front of the envelope. The root is
  found with `encoding/xml`'s raw tokenizer, with no entity expansion.
- **`soap.Headers(version, action)`** gives the rows above. The action is
  quoted as an HTTP quoted-string (`"` and `\` escaped) and used as typed,
  so `{{variables}}` in it are rendered when the request is sent, like any
  header.
- The editor sets the rows by name, ignoring case: the first row of that
  name gets the value and is enabled, later rows of the name go, a missing
  one is added; the body becomes raw XML. An explicit `Content-Type` row
  replaces the `application/xml` the raw XML body implies
  ([Auto Content-Type](#auto-content-type)). Nothing SOAP-specific is
  saved.
- `TestSendSOAP` (`internal/app/services/http`) sends requests made this
  way, in both versions, to an `httptest` SOAP echo service that checks the
  content type, the action and the envelope namespace and echoes the
  operation back.

## What `net/http` still decides

These are limits of `net/http`. A raw-socket HTTP/1.1 writer could lift them
if users need that.

- **Header order (HTTP/1.1):** Go writes the request line, `Host`,
  `User-Agent`, `Content-Length` or `Transfer-Encoding`, then the other
  headers **sorted by name** (values of one name in row order). Row order is
  **not** preserved on the wire. The model keeps the ordered list, so the raw
  request view (DBG-01) can show rows in the user's order.
- **Canonical spelling:** Go writes `Host`, `Content-Length` and
  `Transfer-Encoding` itself, in that spelling. A `User-Agent` row keeps the
  spelling typed. Go's own reactions to request headers only recognise the
  canonical spelling: `Connection: close` (closing after the response) and
  `Expect: 100-continue` (waiting before the body). The headers are still
  sent as typed.
- **HTTP/2:** names are lowercase (RFC 9113 §8.2) and order follows Go's map
  iteration, so it is random. Connection-specific headers (`Connection`,
  `Keep-Alive`, `Proxy-Connection`, `Transfer-Encoding`, `Upgrade`) are
  dropped, as the spec requires, and `Cookie` may be split into one field
  per cookie. The body is framed by DATA frames.
- **One of a kind:** at most one `Host` and one `User-Agent` row can be sent
  (`unsupported` / `httpclient.header_unsupported`). An empty `User-Agent`
  value sends none. `Trailer` rows are unsupported, and so is any
  `Transfer-Encoding` other than `chunked`.
- **Content-Length of an empty body:** sent as `0` for POST, PUT and PATCH
  (RFC 9110 §8.6), never for GET or HEAD, and not for other methods (so a
  matching `Content-Length: 0` row on GET is not sent).
- **Whitespace:** leading and trailing spaces of values are trimmed
  (optional whitespace, RFC 9110 §5.5).
- **Control characters** in names or values are rejected
  (`invalid_argument` / `httpclient.invalid_header`). The error names the
  header, never the value.
- **Redirects:**
  - Headers are copied to the next hop. `Authorization`,
    `Proxy-Authorization` and `WWW-Authenticate` are dropped, in any
    spelling, when the origin changes: another scheme, host or port,
    subdomains included, and stay dropped for the rest of the redirects
    (PRV-11; `checkRedirect` adds the subdomain, port and scheme cases to
    net/http's rule; `TestSafeDefaultAuthorizationNotForwarded`,
    `TestRedirectSensitiveHeaders`). A `Cookie` row follows net/http's rule:
    dropped when the host changes to one that is not a subdomain.
  - 301, 302 and 303 turn POST into GET and drop the body and its headers.
    307 and 308 keep the method and resend the body through `Body.Open`.
  - HTTP-22 options, applied in `checkRedirect` after net/http built the
    next hop and before the hop is recorded for the timeline:
    `KeepMethod` undoes the switch to GET (method, a fresh body through
    `GetBody`, and the `Content-Type`, `Content-Encoding`,
    `Content-Language` and `Content-Location` rows in their spelling);
    `ForwardAuthorization` puts the original `Authorization` row back on
    every hop, also after net/http dropped it. `MaxRedirects` 0 returns
    the first 3xx (`TestRedirectFollowAndLimit`, `TestRedirectKeepMethod`,
    `TestRedirectForwardAuthorization`).
  - A `Host` row is kept only for relative redirects.
- **TLS:** SNI and certificate checks use the URL's host, not a `Host` row.
- **Cancelled while connecting:** `Send` returns at once, but Go lets the
  dial finish in the background (bounded by the dialer timeout) and keeps
  the connection for reuse.
- **Responses:** `net/http` canonicalizes header names and loses their
  order, so `Response.Headers` is **sorted by name**, with values in the
  order received. The reason phrase is kept on HTTP/1.x (`299 Fine Thanks`).
  Byte-exact raw response capture needs a connection tap; the request, on
  the other hand, is recorded as written ([Request as sent](#request-as-sent-dbg-01)).

## Transport

| Field | Value | Note |
|---|---|---|
| `DialContext` | the `netx.Dialer`'s | `DialTLSContext` unset: TLS runs on the dialed connection, end to end through a proxy tunnel |
| `Proxy` | `nil` | never `ProxyFromEnvironment` (PRV-05); SOCKS/HTTP proxies are dialers. Only the pools of absolute-form requests to an HTTP proxy set it, to `HTTPProxy.ForwardURL` ([network.md](network.md#http-proxy)) |
| `DisableCompression` | `true` | |
| `ForceAttemptHTTP2` | `true` | a custom `DialContext` would otherwise disable HTTP/2; plain `http://` stays HTTP/1.1 (h2c only with `HTTPVersion2`, below) |
| `TLSClientConfig` | `MinVersion` TLS 1.2, system roots or `RootCAs`, `InsecureSkipVerify` off; or a clone of `TLSConfig` (TLS 1.2 floor when it has no minimum) | HTTP-23 turns verification off per client (`TestTLSVerificationSwitch`); NET-04 passes `RootCAs` from `tlsconf.Trust`; `ClientCertificate` (AUTH-30) sets `GetClientCertificate` |
| `TLSHandshakeTimeout` | 10 s | |
| `MaxIdleConns` / `MaxIdleConnsPerHost` | 100 / 10 | per host above Go's 2, for runners |
| `IdleConnTimeout` | 90 s | |
| `ExpectContinueTimeout` | 1 s | only for `Expect: 100-continue` |

TLS settings belong to a transport, so requests with different
verification settings (HTTP-23) use different `Client`s. The
app's `HTTPService` keeps one client per verification setting and trusted
CA pool: `WithTrust(trust.Roots)` gives the custom CA bundles (NET-04),
read on every send and cached by `tlsconf.Trust`, so a new pool (a bundle
added, removed or edited) builds a new client and drops the old one. A
bundle that can no longer be read fails an `https://` send at once
(`tlsconf.file_unreadable`, field `network.caBundles[i]`); `http://` sends
are not stopped. GraphQL requests and schema fetches share the same
clients. See [network.md](network.md#custom-ca-certificates).

Per-host client certificates (AUTH-30) need no client of their own either:
`TransportOptions.ClientCerts` (a `ClientCertFunc`, `host:port` to
`*ClientCert{Certificate, Description}`) makes each transport route every
`https` hop, redirects included, through a connection pool per
certificate (`certRouter` in `clientcert.go`, at most 32 pools), and the
hops without one through the base pool, so connections are never shared
between certificates. `TransportOptions.ClientCertificate` is a fixed
certificate for every TLS connection of a transport (gRPC uses it). The
certificate is always sent when the server asks, and the handshake adds a
`client_cert` event to the timeline; a lookup error (a matching entry that
cannot be loaded) fails the hop before it connects and is returned as is.
The app's `HTTPService` takes the selector with `WithClientCerts`, for
sends, GraphQL requests, subscriptions (`ClientCertForURL` for wss),
schema fetches and SSE streams. See
[network.md](network.md#client-certificates).

The HTTP version (HTTP-25) needs no client of its own: a `Client` keeps
one transport per `HTTPVersion`, the one above for `HTTPVersionAuto` and,
made on first use with the same dialer and options, one whose
`Transport.Protocols` holds only HTTP/1 (`HTTPVersion1`) or only HTTP/2 and
unencrypted HTTP/2 (`HTTPVersion2`). Their connections are pooled apart, so
an h2 connection is never reused for an HTTP/1.1-only request or the other
way round; `CloseIdleConnections` closes all of them, and `WithCookieJar`
copies share them.

### HTTP version (HTTP-25)

| `Settings.HTTPVersion` | File `httpVersion` | https | plain http |
|---|---|---|---|
| `HTTPVersionAuto` | `auto` (default) | ALPN offers `h2`, `http/1.1`; the server picks | HTTP/1.1 |
| `HTTPVersion1` | `http1` | ALPN offers `http/1.1` only | HTTP/1.1 |
| `HTTPVersion2` | `http2` | ALPN offers `h2` only | h2c with prior knowledge (RFC 9113 §3.3) |

A server that does not speak HTTP/2 fails an `HTTPVersion2` send with
`protocol` / `httpclient.http2_unsupported` ("The server at host:port does
not speak HTTP/2.", hint: set the HTTP version to Automatic or HTTP/1.1):
on https it refuses the ALPN offer (TLS alert `no_application_protocol`),
on plain http it answers the connection preface in HTTP/1.x or hangs up.
What is not possible with `net/http`: the `Upgrade: h2c` dance of RFC 7540
(removed in RFC 9113; curl's `--http2` over plain http), HTTP/1.0, and
HTTP/3. The protocol actually used is in `Response.Proto`, `Conn.Protocol`
and the `tls_done` event (ALPN). `TestHTTPVersion` sends every choice to an
HTTP/1.1-only TLS server, an HTTP/2 TLS server, a plain HTTP/1.1 server and
an h2c server.

### TLS options

`internal/core/tlsconf` is the one place that turns user TLS settings into
a `*tls.Config` (first used by gRPC, GRPC-23). The app-wide custom
CA bundles (NET-04) use its file reading and PEM checks through
`InspectBundle` and `Trust` (below); the per-host client certificates
(AUTH-30) use its certificate and key loading through `LoadClientCert`
and `ClientCerts` (below, and [network.md](network.md#client-certificates)).

| Item | Purpose |
|---|---|
| `Options{InsecureSkipVerify, ServerName, CAFiles, CAPEM, ReplaceSystemRoots, ClientCertFile, ClientCertPEM, ClientKeyFile, ClientKeyPEM, MinVersion}` | Plain data with JSON names (`caFiles`, `clientKeyFile`, ...). `ClientKeyPEM` is `json:"-"`: key material never serializes. |
| `Build(o, baseDir) (*Result, error)` | Reads the files (relative paths against `baseDir`; none allowed without one), validates, builds. |
| `Result{Config, ClientCert, CACerts, Warnings}` | `Config` has no `NextProtos`; the protocol client sets ALPN. |
| `Warning{Code, Message, Hint}` | Problems that do not stop a connection attempt. |
| `InspectBundle(path) (*Bundle, error)` | Checks one CA bundle file (NET-04) as `Build` checks `CAFiles`, and describes it: `Bundle{Path, Certs []BundleCert{Subject, Issuer, NotBefore, NotAfter, IsCA, SHA256}, Warnings}`. Warnings: `ca_expired`, `ca_not_yet_valid`, `ca_not_a_ca`, `ca_bundle_has_key`. |
| `NewTrust(files func() []string)`, `Trust.Roots() (*x509.CertPool, error)` | The app-wide trust: `nil` without bundles (system roots), else the system pool plus the bundles, rebuilt only when the list or a file's size or mtime changes. Errors carry field `network.caBundles[i]` and `HintBundle`. |
| `ClientCertEntry{ID, Host, CertFile, KeyFile, PFXFile}`, `ParseHostPattern`, `MatchClientCert`, `LoadClientCert(e, passphrase)`, `NewClientCerts(entries, passphrase)`, `ClientCerts.For/ForAddress/ForURL` | Per-host client certificates: host patterns with `*` and an optional port, PEM (encrypted PKCS#8 with the passphrase) or PKCS#12, loaded certificates cached until the entry, passphrase or a file changes. Errors keep the load codes and get `HintClientCertFix` and the details `host`, `clientCert`. |
| `tlstest` (test support) | `NewCA`, `NewCAValid(name, notBefore, notAfter)`, `CA.Issue(CertOptions{Client, DNSNames, IPs, NotBefore, NotAfter})`, `SelfSigned(CertOptions)` (signed by its own key, not a CA), `WriteFile`: ECDSA certificates generated at test time, so no key is committed. `Fixture(name)`, `FixturePassword`, `FixtureClientCAs`, `FixtureLeaf`, `MTLSConfig`: the committed PKCS#12 fixtures of `internal/core/pkcs12/testdata` and a server config requiring client certificates. |

Rules:

- **Zero value is safe:** verification on, system roots, TLS 1.2+, no
  client certificate. `MinVersion` is `""`, `"1.2"` or `"1.3"`; older
  versions are not offered.
- **Custom CAs:** every `CERTIFICATE` block of each file and of `CAPEM`,
  in order (other blocks and text around them are ignored). They are
  added to the system pool (`x509.SystemCertPool`; on macOS and Windows
  the platform verifier still applies) unless `ReplaceSystemRoots`, which
  trusts only them. `ReplaceSystemRoots` without any CA is an error.
- **Server name:** sets SNI and the name verified; a host name or IP, not
  `host:port` or a URL.
- **Client certificate:** from a file or PEM (not both); the source may
  hold intermediates after the leaf, and the key too (then no separate key
  is needed). Keys are PKCS#8, PKCS#1 or SEC 1 whatever the block type
  says. The key must match the leaf's public key. In `Build` (the gRPC
  request's own settings) encrypted keys (`ENCRYPTED PRIVATE KEY`, legacy
  `Proc-Type: 4,ENCRYPTED`) are `unsupported` and the hint says how to
  decrypt with openssl; DER and PKCS#12 (`.p12`/`.pfx`) are not read
  there. The per-host client certificates (`LoadClientCert`) take a
  passphrase: they decrypt `ENCRYPTED PRIVATE KEY` blocks and read
  PKCS#12 files with `internal/core/pkcs12` ([pkcs12.md](pkcs12.md)); a
  wrong passphrase is `pkcs12.wrong_password`, a missing one
  `tlsconf.client_key_encrypted` with "Enter the passphrase".
- **Always sent:** the certificate is returned from `GetClientCertificate`
  whenever the server asks, even if its issuer is not in the server's list
  of acceptable CAs (crypto/tls would silently send none), so the user sees
  the server's verdict.
- **Files:** at most 1 MiB (`MaxFileSize`).

| Code | Kind | When |
|---|---|---|
| `tlsconf.invalid_option` | `invalid_argument` | bad `minVersion` or `serverName`; a file and PEM for the same thing |
| `tlsconf.invalid_path` | `invalid_argument` | empty path; relative path without a base directory |
| `tlsconf.file_unreadable` | `not_found` / `permission_denied` / `io` | the file cannot be read (detail `path`) |
| `tlsconf.file_too_large` | `invalid_argument` | over 1 MiB |
| `tlsconf.ca_no_certificates` / `tlsconf.ca_invalid` | `parse` | a CA source without a PEM certificate / one that does not parse |
| `tlsconf.ca_required` | `invalid_argument` | `replaceSystemRoots` with no CA |
| `tlsconf.system_roots_unavailable` | `unsupported` | the system pool cannot be loaded |
| `tlsconf.client_cert_missing` / `tlsconf.client_key_missing` | `invalid_argument` | a key without certificate / a certificate without key |
| `tlsconf.client_cert_invalid` / `tlsconf.client_key_invalid` | `parse` | no PEM certificate or key, or it does not parse |
| `tlsconf.client_key_encrypted` | `unsupported` | password-protected key |
| `tlsconf.client_key_mismatch` | `invalid_argument` | the key is not the certificate's |

Every error has the detail `field` (`caFiles[1]`, `clientKeyPem`, ...).
Messages name files by path but never include PEM contents.

Warnings: `tlsconf.client_cert_expired`, `tlsconf.client_cert_not_yet_valid`
(the server will most likely refuse it; the call is still attempted),
`tlsconf.client_cert_not_for_client_auth` (extended key usage without
client auth), `tlsconf.verification_disabled`.

## Response

- `Body` holds the bytes as received and is never decompressed.
  Decompression for display is the viewer's job. `Body` is never nil.
- `MaxBodyBytes` caps it: reading stops once more than the cap has arrived.
  `BodyTruncated` is then set, `Body` holds the first `MaxBodyBytes`,
  `Size` is the bytes read (the cap + 1) and the connection is closed
  instead of reused. `ContentLength` is the length the server announced
  (or -1).
- `DecodedSize` is `len(Body)` for `Send`; `Encoding` and `DecodeError`
  are only set by `SendTo` (below).
- `SendTo(ctx, req, BodyTarget{Open, Decode, Progress})` (HTTP-37,
  `bodyto.go`) is `Send` with the body written to a writer while it
  arrives instead of kept in memory: `Open(head)` is called once the final
  response's headers are in and returns the writer (an error stops the
  send and is returned as is); the copy uses one 64 KiB buffer, so a body
  of any size costs constant memory (`TestSendToLargeBodyConstantMemory`
  streams 200 MiB and checks the allocations). `Settings.Timeout` covers
  the body as for `Send`. With `Decode`, a single `gzip`, `x-gzip` or
  `deflate` (zlib, or raw deflate as some servers send it)
  `Content-Encoding` is removed while receiving (`compress/*`); `br`,
  `zstd` and stacked codings are written as received (`Encoding` ""). A
  body that is not in the coding it claims is written as received, and
  one whose data breaks part way keeps what was decoded; both set
  `DecodeError`, neither fails the send. `MaxBodyBytes` applies to the
  bytes **written** (decompressed), so a gzip bomb stops at the limit;
  then `BodyTruncated` is set and reading stops. `Size` is always the
  bytes received on the wire, `DecodedSize` the bytes written, `Body`
  empty. `Progress(received)` is called about every 256 KiB
  (`ProgressStep`) and at the end. A failed write is
  `httpclient.body_write_failed` (kind of the cause, else `io`); after a
  failed or cancelled send the writer holds a partial body the caller
  discards. `Send` itself is unchanged.
- `URL` is the final URL after redirects, without userinfo or fragment.
  `Duration` runs from the start of the send to the end of the body; it
  equals `Timing.Total`.
- `Timing`, `Conn`, `Redirects` and `Events`: see
  [Timing and connection info](#timing-and-connection-info).

## Errors

Every error is an `*errs.Error` ([errors.md](errors.md)). Send errors carry
the detail `stage`: `before_send`, `connecting`, `waiting` or
`reading_body`. Messages name the `host:port`, never the path or query,
which may hold secrets.

| When | Kind | Code |
|---|---|---|
| Method not a token / CONNECT | `invalid_argument` / `unsupported` | `httpclient.invalid_method` / `httpclient.method_unsupported` |
| URL without scheme or valid host / other scheme | `invalid_argument` / `unsupported` | `httpclient.invalid_url` / `httpclient.scheme_unsupported` |
| Bad header name or value; CL + TE together | `invalid_argument` | `httpclient.invalid_header` |
| Second Host or User-Agent, Trailer, TE other than chunked | `unsupported` | `httpclient.header_unsupported` |
| Content-Length row ≠ body length | `invalid_argument` | `httpclient.content_length_mismatch` |
| Negative setting, unknown URL encoding or HTTP version | `invalid_argument` | `httpclient.invalid_setting` |
| `HTTPVersion2` and the server has no HTTP/2 | `protocol` | `httpclient.http2_unsupported` |
| `Body.Open` failed | kind of the cause | `httpclient.body_unavailable` |
| Dial failed (DNS, refused, unreachable, guard) | from netx | `netx.*`, unchanged ([network.md](network.md#errors)) |
| Cancelled by the caller, any stage | `cancelled` | `httpclient.cancelled` |
| `Settings.Timeout` or the caller's deadline | `timeout` | `httpclient.timeout` |
| `Settings.ResponseHeaderTimeout` | `timeout` | `httpclient.response_header_timeout` |
| I/O or TLS handshake timeout | `timeout` | `httpclient.connection_timeout` |
| Certificate: unknown authority / wrong host / expired / other | `tls` | `netx.tls_unknown_authority` / `tls_hostname_mismatch` / `tls_certificate_expired` / `tls_certificate_invalid` |
| Plain HTTP answer to https:// | `tls` | `netx.tls_server_not_tls` (hint: use an http:// URL) |
| Server requires a client certificate (TLS 1.3 alert `certificate required`, or, after it asked for one and none was sent, a reset, broken pipe, EOF or HTTP/2 "client conn could not be established" before any response: the alert races with the first write and read) | `tls` | `netx.tls_client_cert_required` (hint: add one in Settings, Network, Client certificates) |
| Server refused the client certificate or its absence (alerts `bad certificate`, `unknown certificate authority`, ...; TLS 1.2 `handshake failure` after asking for a certificate) | `tls` | `netx.tls_client_cert_rejected` |
| Per-host client certificate cannot be loaded (moved file, wrong passphrase) | from `tlsconf` / `pkcs12` | `tlsconf.file_unreadable`, `pkcs12.wrong_password`, ... with details `host`, `clientCert` |
| Other TLS failure (alert, version) | `tls` | `netx.tls_handshake_failed` |
| Connection reset by the server (any stage) | `network` | `netx.connection_reset` ("The server at host:port reset the connection while waiting for the response.") |
| Closed before any response / other connection error | `network` | `httpclient.connection_closed` / `httpclient.connection_failed` |
| Response is not HTTP | `protocol` | `httpclient.malformed_response` |
| Redirect limit exceeded | `protocol` | `httpclient.too_many_redirects` |
| Body cut short / read failed / broken chunked framing | `network` / `network` / `protocol` | `httpclient.body_incomplete` / `body_read_failed` / `malformed_body` |

Every error that has an actionable next step carries a hint. A response of any
status (4xx, 5xx) is not an error.

Resets and TLS failures are classified by `netx.Classify` and carry the
`netx.*` codes and hints every protocol client shares (DBG-09,
[network.md](network.md#connection-and-tls-failures)); the HTTP client keeps
its own hints where it can be more precise (the http:// URL for a plain
server, "in the request's Settings tab" for verification, the host in the URL for a
name mismatch). The Go constants `CodeTLSUnknownAuthority`, … and
`CodeConnectionReset` equal the netx codes. `TestNetworkFailureMessages`
provokes each failure for real (DNS through an injected resolver and a
`.invalid` name, a closed port, a server that resets with `SO_LINGER 0`,
certificates from `tlstest` that are signed by another CA, expired or
issued for another name, a request timeout) and checks the message and
hint the response panel shows.

## Streaming responses

`SendStream` is `Send` for a response that is read as it arrives: the SSE
client's event streams ([sse.md](sse.md#client)). It prepares, dials,
follows redirects, records the timeline and maps errors exactly as `Send`
does; only the body differs:

- Once the final response's headers are in, `fn(head, body)` is called
  with the response head (`Proto`, `StatusCode`, `Status`, `Headers`,
  `ContentLength`, `URL`; `Body` empty) and a reader of the raw body
  bytes, as received (never decompressed). It is called for any status.
- `Settings.Timeout` bounds connecting, sending and waiting for the headers
  only. It is stopped when the headers are in, so a stream can run for
  hours; the stream ends when the server ends it, `fn` returns, or `ctx`
  is cancelled. `MaxBodyBytes` does not apply (the caller keeps what it
  needs).
- A read error of the body is an `*errs.Error` with the codes of the body
  stage (`httpclient.cancelled` after cancellation, `body_incomplete` when
  the connection drops mid-stream, …); `io.EOF` ends a complete body.
- `SendStream` returns once `fn` has returned: the full `Response` (empty
  `Body`, `Size` the bytes `fn` read, the timing and timeline of the whole
  exchange) and `fn`'s error. When the request fails before the headers,
  `fn` is not called and the error is `Send`'s, with a nil response.

## Cancellation

`Send`'s context cancels the request at every stage. The client's own
timeouts are context causes (`context.WithTimeoutCause`, `WithCancelCause`),
so they are told apart from the caller's cancellation. `TestCancel` covers
all four stages. It checks that `Send` returns promptly, that the error is
`cancelled` and satisfies `errors.Is(err, context.Canceled)`, and that no
goroutine is left behind (it counts them and releases blocked dials and
handlers).

## Sending from the app

HTTP-20 (send and cancel) and HTTP-21 (timeouts) put the pieces above
together. Three layers, each testable on its own:

```
UI: Send / ⌘↩ ── wait for queued auto-save edits ── HTTPService.Send({requestId})
   │
collection.LoadHTTPSource: the request as the editor has it (pending edit or file,
   │ local auth secret merged), its auth resolved (own or folder, AUTH-01, with
   │ that item's local secret), the folders' variables and headers, the workspace
   ▼
request.BuildHTTP(file, auth, {Expand, Root, DefaultTimeout, FolderHeaders}) ── httpclient.Request
   │ fails at once, nothing sent: bound call rejects with the error
   ▼
operation "http"/"send" (docs/events.md): client.WithCookieJar(project jar).Send
   │ http:op:progress  stage messages from httptrace hooks
   │ http:op:data      one ResponseDTO (an incremental GraphQL response, GQL-08:
   │                   partial ones first, docs/graphql.md)
   ▼ http:op:done      ok (any status) / error (the send's error) / cancelled
```

### Building the request

`request.BuildHTTP` (`internal/core/request/build.go`) is the one mapping
from a request file to what is sent, in this order; the first error stops
it and is returned as it is:

1. `Method` checks the method.
2. `SubstitutePath` fills the path parameters, then `Expand` renders the
   URL's own `{{variables}}` (an error gets the detail `field: url`). The
   URL is trimmed, and a relative one (`/users`) is joined to the rendered
   `{{baseUrl}}` (`ExpandURL`, COL-08,
   [environments.md](environments.md#base-urls)); an empty one is
   `invalid_argument` / `request.url_missing`. The URL's query already holds the enabled Params
   rows (the file's `url` is the source; see
   [file-format.md](file-format.md)).
3. `Auth` maps the resolved auth; `WithAuthQuery` and `WithAuthHeaders`
   add it so rows typed in Params and Headers win.
4. `Headers` maps the header rows, then `InheritHeaders` adds the header
   rows of the request's folders (`HTTPOptions.FolderHeaders`, COL-03,
   [collection.md](collection.md#folder-settings)): a folder header is left
   out when the request's own enabled rows, or a nearer folder, send one of
   the same name (ignoring case); the kept ones come first, top-level
   folder first. A folder row's error has the extra detail `folder` (its
   ID) and names the folder in its message. Folder headers win over the
   auth's header like typed rows. `Body` maps the body (raw, URL-encoded,
   multipart and file bodies streamed from disk, relative paths against the
   project root); `BodyForMethod` drops the body of HEAD and TRACE with a
   warning.
5. Settings: `httpclient.DefaultSettings()`, then the request's
   `settings` members (`timeoutMs`, `followRedirects`, `maxRedirects` with
   0 following none, `followOriginalMethod` to `KeepMethod`,
   `forwardAuthorization` to `ForwardAuthorization`). TLS verification
   (HTTP-23): the request's `verifyTls` when it has one, else
   `HTTPOptions.DefaultInsecure` (the app setting `network.verifyTls`
   false); off sets `HTTPBuild.InsecureSkipVerify`, which picks a client
   with verification off, and `TLSSource` (`request` or `settings`). A
   negative `timeoutMs` or `maxRedirects` is `invalid_argument` /
   `request.invalid_setting` (detail `field`).

**Variables.** The HTTP service renders with `template.Resolver` over the
chain global < environment < collection < folder (ENV-06, [templates.md](templates.md#scopes-and-precedence)): the project file's
`variables`, the active environment (`environments.Store.Active()`), then
each folder's `variables` from the top-level folder (the collection) down. Disabled
variables are left out. Secret global and folder variables get their
values from the local side files (`requestsecrets.MergeVariables`, see
[environments.md](environments.md#folder-and-global-secret-variables)) and
keep their secret flag, so the timeline masks them; one without a value on
this computer is left out, so a reference to it is reported undefined
instead of sending `""`. The active environment is read
on every send, so switching it changes the next send (ENV-10). A cycle, an
empty name or a limit stops the send with the `template.*` error, before
anything is sent. Undefined names do not stop the build at once: they are
collected over every field (URL, path parameters, auth, headers, body,
GraphQL query and variables) and reported together as one
`template.unresolved` error listing them in `details.names`, which the UI
turns into a "Send anyway" question (ENV-11). `SendInputDTO.allowUnresolved`
sends them as written (`{{name}}` stays in the text) instead.

**Prompts (DYN-07).** `{{$prompt LABEL [DEFAULT] [secret]}}` references
take their values from `SendInputDTO.prompts` (by label). Those without
one are collected over every field like undefined names and reported
first, as one `template.prompt` error whose `details.prompts` lists
`{label, default, secret}` per label; the UI asks for them all in one
dialog and sends again with the answers (then asks "Send anyway" if names
are still undefined). Cancel aborts the send. A secret prompt's value is
masked in the timeline and the history; none is ever written to a file
([templates.md](templates.md#prompts-at-send-time)).

`HTTPService.Variables(id)` returns the variables request `id` can use
with the same chain, for the editor's chips: each name with its rendered
value (empty for a secret), `secret`, `resolved` (false when its value
uses an undefined name or loops), `scope` ("Global", "Environment: dev",
"Folder", "Dynamic") and `source` (`global`, `environment`, `folder`,
`dynamic`), plus the active environment's name. Every dynamic value name
(`$uuid`, `$timestamp`, …) that no scope defines is listed too, resolved,
with a description as its value ("A random UUID (v4), generated at send
time"). Dynamic references are generated by the resolver at send time, so
they are never undefined names and never trigger "Send anyway"; invalid
arguments (`{{$randomInt 5 1}}`) refuse with `template.invalid_dynamic`,
also with `allowUnresolved`. See [templates.md](templates.md#editor).

`HTTPService.Resolution(id)` returns, for request `id`, every reference it
uses with the value it is sent with, the scope that wins and every scope
that defines it, from the very build a send makes (the Variables sub-tab,
DBG-08, [templates.md](templates.md#resolution-view)).

**Response references (DYN-06).** `{{$response REQUEST SOURCE EXPR
[send=WHEN] [secret]}}` reads another request's last response. When a
reference asks for its request to be sent first (`send=missing`, an age
such as `send=10m`, `send=always`) and it is due, Send still returns its
operation at once: the operation reports the stage *Sending a referenced
request first*, sends that request (recorded in the history, its response
kept for references, not shown), then builds and sends the request with
the value. A loop, too deep a chain or a failed send-first ends the
operation with `http.response_cycle`, `http.response_too_deep` or
`http.response_send_failed`; a reference to a request without a response
that does not send it refuses the send at once with
`http.response_not_sent`. `HTTPService.ResponseValue(id, name)` returns
what reference `name` of request `id` reads from the stored response, for
the hover (never a send). See
[templates.md](templates.md#response-references).

**Cookies.** Every send takes the open project's jar from the app's shared
`cookies.Jars` (the one the cookie manager edits) and attaches it with
`WithCookieJar`, so jar cookies are sent and `Set-Cookie` is stored on
every hop, redirects included ([cookies.md](cookies.md)). When the jar
cannot be opened the request is sent without jar cookies and a warning is
logged.

### Automatic headers turned off (HTTP-09)

`settings.omitHeaders` names automatic headers the request does not send,
compared ignoring case: `Content-Type` sends the body without the type it
implies (the bytes, and a multipart body's boundary, are unchanged), and
the name of a header the Auth tab adds (`Authorization`, an API key's
header) leaves that header out; `Authorization` also drops the Digest
answer. `BuildHTTP` lists what it left out, and the automatic headers a
row overrides, in `HTTPBuild.Suppressed` (`Name`, `Value`, `Source`
`body` or `auth`, `Reason` `omitted` or `overridden`), and where each sent
header came from in `HTTPBuild.HeaderSources` (`request`, `folder`,
`auth`). `Host` and `Content-Length` cannot be turned off (a `Host` row
overrides the host; `Transfer-Encoding: chunked` replaces the length);
the jar's cookies are turned off with `sendCookies: false`. A copied curl
command follows the same build, except that curl's `-F` always sends a
multipart Content-Type.

### Timeouts (HTTP-21)

| Request `settings.timeoutMs` | App `network.defaultTimeoutMs` | `Settings.Timeout` | `TimeoutSource` |
|---|---|---|---|
| missing (null) | `0` | none | `""` |
| missing (null) | `N` | `N` ms | `settings` |
| `0` | any | none | `""` |
| `N` > 0 | any | `N` ms | `request` |

The default is read from the settings store on every send, so a change on
the settings screen applies to the next send. The timeout bounds the whole
exchange (connecting, sending, waiting and reading the body), as
`Settings.Timeout` above. When it fires, the send ends with `timeout` /
`httpclient.timeout`, and the service adds which timeout it was: the
message ends with "The request's timeout is 2 s." or "The default timeout
is 30 s.", the hint says where to change it (the request's Settings tab or
the settings screen), and the details carry `timeoutMs` and
`timeoutSource`.

### Redirects (HTTP-22)

| Request `settings` | Missing means | Effect |
|---|---|---|
| `followRedirects` | `true` | `false` returns the 3xx response. |
| `maxRedirects` | `10` | Most redirects followed; one more fails with `protocol` / `httpclient.too_many_redirects`. `0` follows none: the 3xx is returned. |
| `followOriginalMethod` | `false` | `true` keeps the method and body on 301, 302 and 303. |
| `forwardAuthorization` | `false` | `true` sends `Authorization` to other origins too. |

The editor's Settings tab (HTTP and GraphQL) has a "Follow redirects" box,
a "Maximum" field (empty for the default 10), "Keep the original method"
and "Forward Authorization to other hosts", which warns while it is on.
Unchecked boxes write nothing, so the file only names what differs from
the safe defaults. There is no app-wide redirect setting.

### TLS verification (HTTP-23)

| Request `settings.verifyTls` | App `network.verifyTls` | Verified | `insecureTlsSource` |
|---|---|---|---|
| missing (null) | `true` (default) | yes | `""` |
| missing (null) | `false` | no | `settings` |
| `true` | any | yes | `""` |
| `false` | any | no | `request` |

The app setting is read on every send (and GraphQL schema fetch), so the
settings screen's switch (Settings › Network › TLS) applies to the next
one. It covers HTTP and GraphQL requests; WebSocket and gRPC requests have
their own `verifyTls` only. Off is never the default and always visible:
the URL bar of the HTTP and GraphQL editors shows a "TLS unverified"
badge (opening the Settings tab) while it is off for the request, from
either place; the Settings tab says why; the response's status line
repeats the badge for a response received without verification
(`ResponseDTO.insecureTls`).

An admin policy with `requireTlsVerification` (SET-02) locks
`network.verifyTls` to `true` and refuses a send, stream, schema fetch or
subscription whose request sets `verifyTls: false`, before it connects,
with `settings.tls_verification_required`
([settings.md](settings.md#settings-locked-by-your-administrator-set-02)).

### URL encoding, HTTP version and cookies (HTTP-24, HTTP-25, HTTP-26)

| Request `settings` | Missing means | `httpclient.Settings` |
|---|---|---|
| `urlEncoding` | `auto` | `URLEncoding`: `auto` → `URLEncodingWHATWG`, `rfc3986` → `URLEncodingRFC3986`, `none` → `URLEncodingAsTyped` |
| `httpVersion` | `auto` | `HTTPVersion`: `auto` → `HTTPVersionAuto`, `http1` → `HTTPVersion1`, `http2` → `HTTPVersion2` |
| `sendCookies` | `true` | `false` sets `SkipSendingCookies` |
| `storeCookies` | `true` | `false` sets `SkipStoringCookies` |

`request.BuildHTTP` maps them (GraphQL and SSE requests too); an unknown
`urlEncoding` or `httpVersion` fails the build with `invalid_argument` /
`request.invalid_setting` (detail `field`). There is no app-wide setting
for any of them. The editor's Settings tab has a Protocol group with the
two menus and a Cookies group with "Send cookies from the jar" and "Store
cookies from the response" ([frontend.md](frontend.md)). A response
cookie that was not stored because of `storeCookies` has
`CookieDTO.jar` `skipped` ("Not stored" in the Cookies tab), and the
timeline's `request_options` event names every option that differs from
the defaults. `TestSendRequestOptions` sends a QUERY request with each
combination to an HTTP/2 TLS server through the service, with a project
jar.

### Service (`HTTPService`)

`internal/app/services/http`. `Send({requestId})` returns
`StartedDTO{opId}` at once; the UI cancels with `OpsService.Cancel(opId)`
(any stage: the operation's context is the send's). The service keeps one
`httpclient.Client` per TLS-verification setting, so connections are
reused across sends; `ServiceShutdown` closes their idle connections.
`WithVerifyTLS(func() bool)` gives it the app's `network.verifyTls`, read
on every send and every GraphQL schema fetch; without it, requests without
their own `verifyTls` verify.

`HeaderPreview({requestId})` (HTTP-09, `autoheaders.go`) returns
`HeaderPreviewDTO`: the header fields a send would write, built exactly as
`Send` builds the request (`build`: variables, folder headers, auth,
settings) and written by `httpclient.Preview` with the project's cookie
jar. Nothing is sent. Each field (`PreviewHeaderDTO`: `name`, `value`,
`masked`, `source`, `reason`) says where it comes from: `request` and
`folder` (header rows, from `HTTPBuild.HeaderSources`), `auth` (the Auth
tab), `body` (`Content-Type`, `Content-Length`, `Transfer-Encoding`),
`url` (`Host`, userinfo `Authorization`), `cookies` (the jar, alone or
appended to a `Cookie` row) or `client`. `suppressed` lists the automatic
headers not sent, with `reason` `overridden` (a row of the same name) or
`omitted` (`settings.omitHeaders`, or `sendCookies: false` for the jar's
cookies); `digest` says the `Authorization` field is Digest's answer;
`httpVersion` is the setting (`auto`, `http1`, `http2`); `unresolved`
lists undefined variables, left as written (they do not fail it). Values
are masked as the timeline masks them. Other build errors fail it as they
fail `Send`. `TestHeaderPreviewMatchesTimeline` sends the request after
each preview and compares with the timeline's request.

`Send` refuses an `sse` request with `http.sse_request` (kind
`invalid_argument`): an event stream is connected to through the SSE
service instead. That service gets the request built by `NewStream(ctx,
s, id, allowUnresolved)` (not bound), which builds it exactly as `Send`
does (the same `build` step: pending edits, variables, auth, folder
headers, settings, the same `template.unresolved` rules), adds `Accept:
text/event-stream` when no enabled `Accept` row is set, and sends it
with `SendStream`, the project's cookie jar and `Send`'s timeout
messages. For another kind of request it fails with `http.not_sse`.
[sse.md](sse.md#client).

- **Progress:** `Connecting`, then `Sending the request` (`GotConn`),
  `Waiting for the response` (`WroteRequest`), `Receiving the response`
  (`GotFirstResponseByte`), coalesced like all progress. While the body
  arrives, `Receiving the response` progress carries `done` (bytes
  received, as sent) and `total` (the `Content-Length`, 0 when not
  announced), about every 256 KiB (HTTP-37).
- **`ResponseDTO`:** `method`, `url` (as sent, before redirects),
  `finalUrl`, `redirects`, `proto` (`HTTP/1.1`, `HTTP/2.0`), `protocol`
  (the same as the UI names it: `HTTP/1.1`, `HTTP/2`), `statusCode`,
  `status`, `headers` (`{name, value}`, sorted by name), `cookies`
  (below), `contentType`,
  the body fields ([below](#response-body-http-31)), `bodyTruncated` (over
  the limit), `bodyLimit` (the limit in bytes), `size` (body bytes
  received, as sent), `bodySize` (bytes kept, decompressed),
  `contentEncoding` (`gzip`, `deflate` or "": the coding removed,
  [below](#large-responses-http-37)), `bodyId` and `fileName`
  (for saving the body, [below](#save-body-to-file-http-34)), `historyId`
  (the request history entry the send is recorded as, named when the
  response arrives, for saving it as a response example, HTTP-38; "" when
  it is not recorded: no project open, a run's responses;
  [history.md](history.md#response-examples)), `headerSize` (HTTP-30:
  the status line and headers written as HTTP/1.1 text, `HTTP/1.1 200
  OK\r\n`, `Name: value\r\n` per header and the blank line; for HTTP/2
  too, whose compressed header size `net/http` does not report),
  `durationMs`, `timeoutMs`, `timeoutSource`, `insecureTls` and
  `insecureTlsSource` (sent without certificate verification, and whether
  the request or the app setting turned it off: HTTP-23; the response's
  status line shows a "TLS unverified" badge), `warnings` (`{code, message}`), `timeline` (below), `tests` (below), `incremental` (null, or a GraphQL
  response delivered with `@defer`/`@stream`: [graphql.md](graphql.md#incremental-delivery-gql-08)). Pinned by
  `internal/app/services/http/testdata/dto/response.golden`.
- **`cookies`** (HTTP-32, `internal/app/services/http/cookies.go`): the
  final response's `Set-Cookie` headers, in the order received, each
  parsed with `cookies.ParseSetCookie` (the jar's own parser):
  `CookieDTO{raw, name, value, domain, path, expires (RFC 3339 UTC),
  maxAge, hasMaxAge, secure, httpOnly, sameSite, error, notes, jar,
  jarReason, jarMessage}`. `error` says why a value is not a cookie at
  all; `notes` lists attributes the parser ignored. `jar` is what the
  project's jar did: `stored`, `removed` (Max-Age ≤ 0 or a past Expires:
  a deletion), `rejected` (with the `cookies.Reason` and a sentence) or
  "" when no jar was used. To know that, each send wraps the jar in a
  `jarRecorder` (an `httpclient.CookieJar` over `(*cookies.Jar).StoreLines`)
  that keeps the outcomes of the latest hop's `Set-Cookie` values; they
  are used only when those values are exactly the final response's.
  Cookies set by redirects are stored as before but not listed (the
  timeline has them).
- **`timeline`** (DBG-01, `internal/app/services/http/timeline.go`):
  `TimelineDTO{hops, events, masked}`. `hops` are the redirects and
  answered Digest challenges, then the final exchange, each
  `TimelineHopDTO{kind, request, startMs, responseProto, statusCode,
  status, responseHead, timing, conn}` with `kind` `redirect`, `auth` or `final`
  (`ResponseDTO.redirects` counts only the redirects): `request` is the hop's
  `SentRequest`, masked, as `SentRequestDTO{method, url, proto, target,
  headers, head, body, bodyHex, bodyShown, bodyClipped, bodySize,
  bodyChunked}`, the body as text up to `MaxTimelineBody` (64 KiB, not
  splitting a UTF-8 sequence) or, when it is not UTF-8, a hex dump of as
  many bytes; `responseHead` is the status line and headers as HTTP/1.1
  text. `timing` (DBG-03, `connection.go`) is
  `HopTimingDTO{totalMs, phases}`: `totalMs` the hop's span (for the final
  hop, from its start to the last body byte), `phases` the six
  phases in wire order (`dns`, `connect`, `tls`, `request_write`, `wait`,
  `download`), plus `proxy` between `connect` and `tls` on a new
  connection through a proxy tunnel (SOCKS-12), as `PhaseDTO{phase, observed, startMs, durationMs, skipped}`,
  `startMs` from the hop's start (`Timing.Offsets`), both 0 and
  `skipped` saying why when not observed: `reused`, `no_lookup` (IP
  address), `no_tls` (http), `not_final` (download of a redirect or auth
  hop), or `""` when not known. `conn` (DBG-04, DBG-05) is
  `ConnInfoDTO{reused, wasIdle, idleMs, localAddr, remoteAddr, protocol,
  via, proxy, tls}`, `via` the proxy of the connection (NET-06, `""` for
  none), `proxy` null for a direct or reused connection or the hop
  through the proxy as `ProxyHopDTO{scheme, version, proxy, proxyAddr,
  target, auth, remoteDns, reply, replyText, forward, lookupMs, connectMs,
  tlsMs, handshakeMs}` (`reply` null when the proxy sent none: an
  absolute-form connection, a failure before the connect request;
  [network.md](network.md#the-proxy-hop)), `tls` null for http or `TLSInfoDTO{version, cipherSuite,
  serverName, alpn, resumed, certificates}`, each
  `CertificateDTO{subject, issuer, notBefore, notAfter, dnsNames,
  ipAddresses, validity, selfSigned}` (times RFC 3339 UTC, leaf first).
  `validity` is `valid`, `expired` or `not_yet_valid` at the time of the
  request (`httpclient.CertValid`, …, judged when the response's TLS
  state is described, whether or not verification was on), so a
  timeline read later still says what the certificate was when it was
  used; `selfSigned` is set when the issuer is the subject and the
  certificate's own key verifies its signature. They hold no secret and
  are not masked. `TestTimelinePhasesAndConn` sends twice to plain and TLS
  httptest servers: phases present and ordered, addresses, TLS details and
  the certificate, then the reused connection.
  `TestTimelineCertificateValidity` sends with verification off to a
  server with an expired self-signed certificate (`tlstest.SelfSigned`)
  and to one whose CA-issued leaf is not valid yet: TLS version, cipher,
  ALPN, the chain, each certificate's validity and self-signed flag. `events` are `Response.Events` with `atMs` (milliseconds,
  microsecond precision). `masked` says a value was hidden. The masker is
  built per send from the values of the **secret variables** the
  expansion substituted (`template.Ref.Secret`, nested ones included) and
  what the **auth** added (an API key's value, the Digest password;
  `Authorization` is masked by name, a Digest one only in its `response`). The auth values are found by mapping the auth again with the
  expansion's recorded results (`expansion.replay`), so a dynamic value in
  it (`{{$uuid}}`) matches what was sent. Response header values go
  through `MaskHeader` too. Memory: the DTO carries at most 64 KiB of body
  per hop, and the client's 1 MiB capture is dropped once the DTO is
  built; the frontend keeps one response per open request tab.
- **`tests`** (TEST-01, TEST-06, `assertions.go`): the results of the
  request's enabled assertions (`TestResultDTO{index, target, expr, op,
  expected, outcome, actual, exists, message, code}`), in the request's
  order, `[]` when it has none. Their `{{variables}}` are rendered before
  the send with the request's resolver (an undefined one makes that
  assertion `invalid`, it never refuses the send); they are evaluated
  after the response with `internal/core/assert` against the whole body
  (not the clipped DTO text), and secrets are masked in the
  results. A failed assertion leaves the operation `ok`. A body over
  1 MiB is read back from its spool file only when an assertion needs the
  body (`body`, `jsonpath`, `xpath`, `regex`); one over
  `MaxEvalBodyBytes` (256 MiB) is not read, and those assertions are
  `invalid` with `http.body_too_large` (HTTP-37).
  [assertions.md](assertions.md#after-every-send). The request's
  extraction rules then set their variables from the same response (the
  `extractions` of the DTO, TEST-05): [extraction.md](extraction.md).
- **GraphQL requests** (GQL-02) are sent by the same `Send`: the loader
  returns them as `HTTPSource.GraphQL`, and `request.BuildGraphQL` builds
  them (POST with a JSON body, or GET) on top of `BuildHTTP`; see
  [graphql.md](graphql.md#sending-gql-02).
- **Comparing responses** (HTTP-40, `compare.go`):
  `CompareResponses({left, right})` compares two stored responses (below)
  with `internal/core/respdiff`: status, header fields by name and the
  body, JSON by path and other text by line, as side-by-side rows with
  notices where a limit applies. See [response-diff.md](response-diff.md).
- **Stored responses** (HTTP-38, HTTP-39, `stored.go`):
  `StoredResponse(StoredResponseInputDTO)` returns the `ResponseDTO` of a
  response PostSonde kept instead of received, an earlier response from
  the request history or a response example: `method`, `url`,
  `requestHeaders` and `requestBody` (Base64 with `requestBodyBase64`) of
  the request as sent, `proto`, `finalUrl`, `redirects`, `statusCode`,
  `status` (the status line after the protocol, or only the reason
  phrase), `headers`, `body` (Base64 with `bodyBase64`), `bodySize`,
  `bodyClipped` and `durationMs`. The body goes through the same
  decoding, highlighting and pretty-printing as a received one; a clipped
  body is shown as its start (`bodyClipped`, `prettyState`
  `incomplete`). Cookies are parsed without a jar; `bodyId` and
  `historyId` are "", so nothing is kept for saving or paging; `tests`
  and `warnings` are empty; the timeline is one `final` hop with the
  request as sent and the response head, without timing or connection.
  Nothing is masked here: what is stored was masked when it was stored.
  A Base64 body that does not decode is `http.invalid_stored_response`.
  [history.md](history.md#response-examples).
- **Errors at once** (the bound call rejects, nothing is sent): the
  `collection.*` errors of loading (`collection.not_found`,
  `collection.not_http` for kinds other than `http` and `graphql`), `request.auth_inherit_failed`, secrets that
  cannot be read, and every error of `BuildHTTP` (and `BuildGraphQL`). `http.unavailable` when
  the service was built without a loader, registry or dialer.

### Repeat and interval sending (HTTP-27)

`internal/app/services/http/repeat.go`. `Repeat({requestId, count,
intervalMs, allowUnresolved, prompts})` sends one request again and again as one
operation of the `http.repeat` domain (its items are `RepeatResultDTO`,
not `ResponseDTO`, so it has a domain of its own; [events.md](events.md)).
Both forms the UI offers are the same run:

- **N times**: `count` 1 to 100,000 (`MaxRepeatCount`), `intervalMs` the
  wait, 0 for back to back.
- **Every X seconds**: `intervalMs` up to a day (`MaxRepeatIntervalMs`),
  `count` the optional cap, 0 until stopped. `count: 0` needs an
  interval.

Out of range is `http.repeat_invalid` (`invalid_argument`, `details.field`
`count` or `intervalMs`). The interval runs from the start of one send to
the start of the next; a send that takes longer is followed by the next
one at once, and sends never overlap.

**Each send is a normal send.** The first is built before `Repeat`
returns, so a request that cannot be built rejects the call exactly as
`Send` does (with the same `template.unresolved` question, ENV-11, and
the same `template.prompt` dialog, DYN-07: the values are asked once, and
every send of the run uses `RepeatInputDTO.prompts`); the
others are built again just before they go out (`prepare`, the step
`Send` uses), so variables, dynamic values (`{{$uuid}}`), auth, folder
headers, settings, cookies and assertions behave as for a single send,
and edits or environment changes made during the run are picked up. If a
later build fails (the request was deleted, a variable is gone) the run
ends with that error.

**Results.** Every send that finished is one `RepeatResultDTO{index,
startedAt, durationMs, statusCode, status, size, testsPassed, testsFailed,
error, response}`: a failed send (refused, timed out) has `error` (an
`ErrorDTO`, message masked like the timeline) and the run goes on. A
failed assertion or a 4xx/5xx response is still a response. `response`
is the whole `ResponseDTO` of that send, with its body kept for Save and
paging, on the first result, the last of a counted run, and otherwise at
most every 250 ms (every result when the interval is 250 ms or more): a
fast back-to-back run does not push every body through the bridge, and
the bodies not shown are dropped instead of evicting the other tabs' kept
bodies. Progress is `{done: sends listed, total: count (0 without), message}`
with the stage of the current send or `Waiting for the next send`
(`StageNextSend`). Results are batched up to 32 per event.

**Stopping.** `OpsService.Cancel(opId)` stops the run at any point:
during a send (the send's context is the run's, so the request is
aborted; it is not listed) or while waiting for the next one (the wait
selects on the context). The done event is then `cancelled`; a counted
run that sent them all is `ok`; `error` only when a send could not be
built. Tests (`repeat_test.go`) count the hits of an `httptest` server
for both forms and stop runs during a send, during a later send and
during an hour-long wait, without sleeping.

**History.** A run adds **one** entry to the request history, when it
ends: its last send that was not cut short by the stop (the cancelled one
only if none finished). A poll every few seconds would otherwise fill the
history and push out the entries of single sends; the run's own list has
every result.

**Folder runs** (RUN-01) send each request of a folder through the same
`prepare` and `exchange` steps, from `RunSession` (`run.go`), not bound:
the runner service builds each request with the run's variables above the
other scopes (`buildOptions.runVars`), keeps no body, and writes one
history entry per request when the run ends; `RunSession.Stream` builds
an SSE request of a run as `NewStream` does. See [runner.md](runner.md).

### Save body to file (HTTP-34)

`ResponseDTO.body` is decoded text, clipped at 1 MiB or a 64 KiB hex dump,
so it cannot be saved as is. Instead the service keeps the bytes received
(`internal/app/services/http/bodies.go`) and `SaveBody` writes them where
the user chooses (`save.go`):

- **Kept bodies.** Each send writes its body into a `spool` while it
  arrives ([Large responses](#large-responses-http-37)), kept afterwards
  in a `bodyStore` under a random ID, `ResponseDTO.bodyId`. It keeps the
  latest `MaxKeptBodies` (8) bodies for `BodyTTL` (1 hour); older ones are
  dropped when another is added or one is looked up. Bodies up to
  `SpoolThreshold` (1 MiB) stay in memory; larger ones are **spooled** to
  `<data>/tmp/response-bodies/<id>.body` (directory `0700`, files `0600`)
  as they arrive, so they are never whole in memory. The spool file is
  deleted when its body is dropped or its send fails or is cancelled, the
  whole directory at shutdown (`ServiceShutdown`) and at start (leftovers
  of a crash). Without a data directory everything stays in memory. The
  bytes kept are the body as received with a gzip or deflate
  `Content-Encoding` removed (`contentEncoding` says which); a body cut at
  the limit is saved as far as it was received and the UI says so.
- **`SaveBody({bodyId}) SaveBodyResultDTO{saved, path, size}`** shows the
  native save dialog (`app.saveFile`, Wails `SaveFileWithOptions`, attached
  to the window; the OS asks before replacing a file) with
  `ResponseDTO.fileName` suggested, then copies the bytes to a temporary
  file next to the target and renames it over the target, so a failed save
  never leaves a partial file under the chosen name. Cancelling returns
  `saved: false` and writes nothing. Errors: `not_found` /
  `http.body_gone` once the body is no longer kept (send again),
  `http.save_failed` when the file cannot be written (the kind of the
  underlying error: `not_found` for a folder that is gone,
  `permission_denied`, else `io`),
  `unsupported` / `http.unavailable` when the service was built without
  `WithSaveBody`.
- **`SaveExchange({bodyId, name}) SaveExchangeResultDTO{saved, path,
  size, masked}`** (EXP-06) saves the same response with its request as
  sent, as a HAR file, masked: [har.md](har.md#a-responses-exchange).
- **Suggested name** (`filename.go`, `suggestFileName`): the
  `Content-Disposition` file name (`filename*` decoded by
  `mime.ParseMediaType` wins over `filename`; an unquoted name with spaces
  is read by hand), else the last segment of the final URL's path
  (unescaped), else `response`. It is reduced to its last path element;
  control characters and `: * ? " < > |` become `_`; leading and trailing
  dots and spaces go; Windows device names (`CON`, `NUL`, `COM1`…) get a
  `_` prefix; it is cut to 200 bytes, keeping the extension. A name
  without extension gets one from the Content-Type: a fixed table (`.json`,
  `.xml`, `.html`, `.txt`, `.csv`, `.pdf`, `.png`, `.jpg`, `.zip`, `.bin`
  for `application/octet-stream`, …), `+json` → `.json`, `+xml` → `.xml`,
  other `text/*` → `.txt`, else none.

### Large responses (HTTP-37)

A send never holds a large body in memory (`largebody.go`, `bodies.go`):

- **Streamed to disk.** `send.run` sends with `httpclient.SendTo` into a
  `spool`: the first 1 MiB in memory, then a new spool file written
  through as the bytes arrive (64 KiB at a time). The first `HeadBytes`
  (1 MiB + 4) stay in memory for the view's text, the history snapshot
  (64 KiB) and assertions on small bodies. `TestSendLargeBodyStreamsToDisk`
  sends 200 MiB through the whole service and checks that it allocated a
  small fraction of it (about 13 MiB, server included) and that the file
  holds all of it.
- **Progress.** Bytes received and the announced length arrive as
  `Receiving the response` progress (above); the UI shows "12.4 MB of
  50.0 MB" with a bar, or "12.4 MB received".
- **Limit.** `WithMaxBodySize(func() int64)` gives the settings'
  `network.maxResponseBodyMb` (default 512 MiB, 1 to 16384,
  [settings.md](settings.md)), read on every send; it replaces the
  request's `MaxBodyBytes`. A longer body keeps what was received up to
  the limit, `bodyTruncated` is set, `bodyLimit` names the limit, and a
  warning `http.body_limit` says "The body is larger than the 512 MiB
  limit, so only its first 512 MiB were kept. Raise the limit in Settings
  › Network to receive all of it." The limit counts decompressed bytes.
- **Decompression while streaming.** A `gzip` or `deflate` body is
  decompressed on the way to the spool (`BodyTarget.Decode`): the view,
  the filter, the assertions and `SaveBody` see the decompressed bytes;
  `size` stays the bytes received, `bodySize` the bytes kept. A body
  whose decompression fails keeps what was decoded and says why in
  `bodyNote`; a coding PostSonde does not decode (`br`, `zstd`) is shown as
  received with a note. PostSonde still adds no `Accept-Encoding` of its
  own ([No hidden changes](#no-hidden-changes)).
- **Cancel or failure.** The spool is discarded with its file, so a
  cancelled 1 GB download leaves nothing behind
  (`TestSendCancelRemovesSpool`).
- **Pages.** `ReadBody({bodyId, offset, charset, hex}) BodyPageDTO{text,
  start, end, size, binary}` returns up to 1 MiB of the kept body from
  `offset`, decoded like `body` (or a 64 KiB hex dump with `hex`, offsets
  counted from the start of the body, `charset.HexDumpAt`). A page never
  splits a character: it starts at the next whole one (a UTF-8
  continuation byte, an odd UTF-16 offset or the second half of a
  surrogate pair is skipped) and ends before a cut one, so reading on from
  `end` gives the body without a gap. Errors: `http.body_gone`,
  `http.unavailable`. The response view pages with it
  ([frontend.md](frontend.md#request-editor)).
- **Filter and assertions** read the whole kept body back from its file,
  up to `MaxEvalBodyBytes` (256 MiB), since both parse it in memory.

### Response filter (HTTP-36)

`FilterBody` (`internal/app/services/http/filter.go`) narrows a response
body with a [JSONPath](jsonpath.md) query or an [XPath](xpath.md)
expression, for the filter bar of the response view
([frontend.md](frontend.md#request-editor)):

- **`FilterBody({bodyId, text, syntax, expression, charset, html})
  FilterBodyResultDTO`**. `syntax` is `jsonpath` or `xpath`. The body is
  the **bytes kept for SaveBody** (`bodyId`), not the view's text, so a
  body over the 1 MiB display clip is filtered whole, up to
  `MaxEvalBodyBytes` (256 MiB: the body is parsed in memory; a larger one
  is a `limit` error that says so, HTTP-37). They are decoded with
  `charset` (`ResponseDTO.charset`;
  a byte order mark wins; "" is UTF-8) like the view's text. With
  `bodyId` "" the given `text` is filtered instead: the UI does that for
  a body Go no longer keeps (`http.body_gone`). `html` parses an XPath
  body leniently (`xmldom` HTML mode, for `text/html`); XML is strict.
- **Order of work.** The expression is parsed first, so a typo is
  reported without reading the body; then the body is parsed, then
  evaluated with the packages' `DefaultLimits` and a 10 s timeout
  (`FilterTimeout`). The last parsed body is cached (one entry, by body ID,
  syntax, charset and mode), so typing a query does not parse a large
  body again for every character.
- **Result.** `valid`, `resultType` (`nodes`, or `string` / `number` /
  `boolean` for an XPath expression such as `count(//item)`), `count`
  (nodes, 1 for a scalar), `text`, `language` (`json`, `xml` or `text`),
  `paths` and `clipped`. A JSONPath node list is shown as a pretty JSON
  **array** of the values (always an array, as RFC 9535 returns a list),
  members in document order and numbers as written (`1.50e+3`), `<` `>`
  `&` not escaped. An XPath node-set is its nodes one after another,
  each indented like the Pretty view (`Document.IndentNode`): an element
  with its content, an attribute as `name="value"`, a text node as its
  text. A scalar is XPath's string of it. `paths` are the nodes'
  normalized (JSONPath) or canonical (XPath) paths, at most 1000
  (`MaxFilterPaths`); `text` is at most 1 MiB (`MaxFilterText`, then
  `clipped`).
- **Errors are results**, not rejections: `error{where, message, offset,
  line, column}`. `where` is `expression` (does not parse; `offset` in
  UTF-16 code units, for the UI to mark), `body` (not valid JSON or XML;
  `line` and `column`) or `limit` (too much work, over the timeout, or
  cancelled because a newer keystroke cancelled the call). The bound call
  rejects only with `http.body_gone` (`not_found`) and
  `http.invalid_filter` (`invalid_argument`: an unknown syntax, an
  expression over 64 KiB).

### Response body (HTTP-31)

`setBody` (`internal/app/services/http/body.go`) turns the bytes received
into what the response viewer shows. Decoding is done in Go with
`internal/core/charset` (standard library only):

1. **Language** (`bodyLanguage`): `json` for `application/json` and
   `+json`, `xml` for `application/xml`, `text/xml` and `+xml`, `html` for
   `text/html`, else `text`. Without a Content-Type the first bytes decide
   (`{` or `[` JSON, `<?xml` XML, `<!doctype html` or `<html` HTML); a
   sniffed JSON or XML body that does not parse falls back to `text`.
2. **Charset** (`charset`, `charsetSource`), the first that applies: a
   byte order mark (`bom`, UTF-8 or UTF-16; the BOM is not shown), the
   Content-Type `charset` (`header`), the XML declaration's `encoding`
   (`declaration`) or an HTML `<meta charset>` / `http-equiv` in the first
   1024 bytes (`meta`), else UTF-8 (`default`) when the media type is
   textual (`text/*`, JSON, XML, JavaScript, form, GraphQL, YAML, NDJSON)
   or the body is valid UTF-8 without NUL bytes. Supported: UTF-8,
   UTF-16LE/BE (`utf-16` without a BOM is little-endian), ISO-8859-1,
   US-ASCII and windows-1252. Invalid sequences become U+FFFD and set
   `bodyBinary`.
3. **Hex** (`bodyHex`): an unsupported declared charset, or a body that is
   neither textual nor valid UTF-8 (or has NUL bytes), is sent as a
   `hexdump -C` style dump of its first 64 KiB (`MaxHexBytes`), with
   `bodyNote` saying why (the charset's name, or "binary data" and the
   media type).
4. **Raw text** (`body`): the decoded text of the first 1 MiB
   (`MaxBodyText`), cut without splitting a character (a UTF-16 surrogate
   pair included); `bodyClipped` and `bodyShown` (the bytes it covers)
   say when it was cut.
5. **Pretty** (`bodyPretty`, `prettyState`, `prettyError`), for JSON and
   XML only: `formatted` (JSON with `jsonfmt.Beautify`, XML with
   `xmldom.Indent`, two spaces), `incomplete` (clipped or truncated, it
   cannot parse), `too_large` (decoded text over 256 KiB,
   `MaxPrettyBytes`: not formatted on arrival so the response arrives fast;
   the UI formats it on request with `FormatService.BeautifyJSON` or
   `IndentXML`), or `invalid` with the parser's message and position. HTML
   has no pretty-printer: it is highlighted as received.
6. **Preview type** (`previewType`, `previewType` in `preview.go`,
   HTTP-35): `image/png`, `image/jpeg`, `image/gif`, `image/webp`,
   `image/svg+xml`, `image/bmp`, `image/x-icon` or `text/html` from the
   Content-Type (aliases such as `image/jpg`, `image/vnd.microsoft.icon`
   and `application/xhtml+xml` included); without one, or for
   `application/octet-stream`, sniffed from the first 512 bytes
   (`http.DetectContentType`, plus `<svg` or `<?xml … <svg` for SVG and
   `<!doctype html` / `<html` for HTML). `""` means no preview.

**Preview bytes.** `PreviewBody({bodyId}) BodyPreviewDTO{data, size}`
returns a kept body's bytes, Base64, for the image preview: whole, since
the view decodes it into a `Blob`, so only up to 16 MiB
(`MaxPreviewBytes`); a larger body fails with `http.preview_too_large`
(`invalid_argument`) without being read, a dropped one with
`http.body_gone`. The HTML preview needs no call: it uses `body`, the
decoded text. How the UI renders both without scripts or network:
[frontend.md](frontend.md#request-editor) and
[privacy.md](privacy.md#response-preview-http-35-prv-04).

`xmldom.Indent` puts each element, comment, processing instruction and
DOCTYPE on its own line, indented by depth, copying the markup as written
(attributes, quotes, entity references, CDATA). Whitespace-only text
between elements becomes the indentation; an element holding any other
text (text-only or mixed content) is copied as it is, so no significant
whitespace changes. It parses the already decoded text
(`Options.Decoded`: the encoding declaration is kept but not used).

The frontend side (Send, Cancel, the Settings tab and the response view)
is in [frontend.md](frontend.md#request-editor).

## Timing and connection info

Every `Response` says where its time went (DBG-03) and which connection
carried it (DBG-04, DBG-05). `Send` records this with `net/http/httptrace`
hooks in the request context, on the client's monotonic clock
(`time.Now` differences; the clock is injectable for unit tests).

### Phases

`Response.Timing` covers the final hop. Each phase is bounded by two
events:

| Field | Starts at | Ends at | Notes |
|---|---|---|---|
| `DNS` | `DNSStart` | `DNSDone` | Not observed for an IP address (no lookup). A name answered from `/etc/hosts` is still observed, with a tiny duration. |
| `Connect` | the first `ConnectStart` | the `ConnectDone` that succeeded (the last one if all failed) | TCP only, DNS excluded. With several addresses (Happy Eyeballs: IPv6 refused, then IPv4) it covers every attempt and the fallback delay up to the one that worked. Through a proxy, the connection to the proxy. |
| `Proxy` | the proxy dialer's `DialDone` minus the hop's TLS and handshake time | that `DialDone` | Through a proxy tunnel only (SOCKS, HTTP CONNECT; SOCKS-12): an https:// proxy's TLS handshake and the proxy protocol (greeting, authentication and connect), from `netx.ProxyHop.Setup`. Not observed on a reused connection, nor for an absolute-form connection to an http:// proxy. |
| `TLS` | `TLSHandshakeStart` | `TLSHandshakeDone` | https only. |
| `RequestWrite` | `GotConn` | `WroteRequest` (headers and body written and flushed), or the first response byte if that came first | |
| `Wait` (TTFB) | the end of `RequestWrite` | `GotFirstResponseByte` | Zero when the server answered before the request was fully written. |
| `Download` | `GotFirstResponseByte` | the last body byte read, or the byte past `MaxBodyBytes` | Final hop only. Includes reading the response headers. |
| `Total` | the start of `Send` | the end of `Download` | The whole send, every hop included. Equals `Duration`. |

The phases never overlap, so their sum is at most `Total`. What is left
over is request preparation and the pool lookup (`GetConn`), the step from
the TLS handshake to `GotConn`, and the earlier hops.

`Offsets` holds when each observed phase started, from the start of its
hop (`Hop.Start`; for the final hop, the end of the last earlier hop, or
the start of `Send`), zero for a phase not observed. With the durations
they place the phases on a waterfall with the gaps between them shown as
they were. Each observed phase starts no earlier than the previous one
ended (`checkPhases` checks it against real servers).

`Observed` is the set of phases whose start and end events were both
seen. A phase not in it is zero because it did not happen, and the UI
should not show "0 ms":

| Not observed | Why | Show |
|---|---|---|
| `DNS`, `Connect`, `TLS` with `Conn.Reused` | reused connection | "reused" |
| `DNS` on a new connection | IP address in the URL (later also: a proxy resolves the name) | "no lookup" |
| `TLS` on a new connection | `http://` | "—" |
| `Download` | redirect hop | "—" |

### Connection

`Response.Conn` is the connection of the final hop:

- `Reused`, `WasIdle`, `IdleTime` come from `GotConnInfo`. On a reused
  connection `DNS`, `Connect` and `TLS` are zero and not observed;
  addresses, protocol and TLS session still describe the connection.
- `LocalAddr`, `RemoteAddr` are the socket's (`ip:port`), read from the
  connection `GotConn` hands over, so they are there for reused
  connections too.
- `Protocol` is `"h2"` or `"HTTP/1.1"`.
- `Via` (NET-06): the proxy the connection goes through (`netx.ViaOf`,
  `socks5h://proxy.lan:1080`), new or reused; `""` for a direct one.
- `Proxy` (SOCKS-12): the hop through a proxy of a new connection, as
  the proxy dialer reported it (`netx.ProxyHop`: proxy address, version,
  authentication, DNS mode, reply, timings); nil for a direct or reused
  connection.
- `TLS` (nil for http): version (`"TLS 1.3"`), cipher suite (IANA name),
  SNI (empty for an IP address), ALPN result, session resumption, and a
  summary of each peer certificate, leaf first: subject, issuer,
  `NotBefore`/`NotAfter`, DNS and IP SANs. Fingerprints, key details and
  the verified chain are left to DBG-05's certificate view.

Reuse, precisely:

- A request may start a dial and then get a connection that became idle
  first. The dial goes on for the pool, and its DNS and connect events are
  in `Events` but not in the phases. For the same reason, setup events
  that arrive after `GotConn` are ignored.
- `net/http` retries a request on a new connection when a reused one turns
  out to be closed by the server. The hop then starts over at the second
  `GetConn`: `Timing` and `Conn` describe the attempt that worked, and
  `Total` includes the failed one.

### HTTP/2

- h2 is negotiated through ALPN on https; plain http uses h2c only with
  `HTTPVersion2` ([HTTP version](#http-version-http-25)). The first request on
  a new h2 connection reports every phase. Later requests are new streams
  on it: `Reused` is true, and `WasIdle` only if no other stream was open.
- `Wait` ends when the stream's HEADERS frame arrives. That is not
  necessarily the first byte on the socket, since frames of other streams
  may come first. `WroteRequest` fires once the stream's last DATA (or
  HEADERS) frame is written.
- h2 hooks run on the connection's goroutines, and a racing dial runs on
  its own, so the recorder is guarded by a mutex.

### Redirects

Each followed redirect is a hop, timed like the final one, and so is each
answered Digest challenge ([below](#digest-authentication-auth-06)).
`Response.Redirects` lists the hops before the final one, in order:

- `Hop{Kind, URL, StatusCode, Start, Timing, Conn}`. `Kind` is `redirect`
  or `auth`. `Start` is an offset from the start of `Send`.
- In a hop, `Timing.Total` is the hop's span: from its start to the start
  of the next hop (`CheckRedirect`). It includes reading the redirect
  response. `net/http` drains up to 2 KB of the redirect body itself, with
  no hook, so `Download` is not observed on a hop.
- The hop spans and the final hop add up to the send's `Total`.
- The final hop usually reuses the redirect's connection (same host).

A hop also has its request as written (`Request`) and the redirect
response's `Proto`, `Status` and `Headers` (sorted like `Response.Headers`),
for the timeline and the redirect chain view (DBG-02).

### Timeline events

`Response.Events` is the raw trace of the whole send, every hop, in order
(DBG-01): `TimelineEvent{At, Name, Detail}`, with `At` an offset from the
start of `Send`.

| Name | Source | Detail |
|---|---|---|
| `get_conn` | httptrace | `host:port` |
| `dial_start` | netx | `tcp localhost:443`; through a proxy with ` via http://proxy.lan:3128` (or `socks5h://…`), never with credentials |
| `dns_start` / `dns_done` | httptrace | host / addresses or error |
| `connect_start` / `connect_done` | httptrace | `tcp ip:port` (one per address tried), plus the error |
| `dial_done` | netx | `local → remote` (the proxy's address through a proxy), or the error; ` via …` as for `dial_start` |
| `proxy` | `forwardRouter` (NET-02) | `http://proxy.lan:3128, absolute-form request`: the hop was sent to that HTTP proxy in absolute form ([network.md](network.md#http-proxy)) |
| `proxy_hop` | netx (SOCKS-12) | right after the `dial_done` of a dial through a proxy: `SOCKS5 proxy.lan:1080 (10.0.0.1:1080), auth username/password, remote DNS, reply 0 succeeded, connect 1.2ms, handshake 800µs` (`netx.ProxyHop.String`, [network.md](network.md#the-proxy-hop)); also after a failed dial, as far as it got |
| `tls_start` / `tls_done` | httptrace | version and ALPN, or the error |
| `client_cert` | `GetClientCertificate` (AUTH-30) | the server asked for a client certificate: the one sent (`"api client" (*.example.com)`), or `requested by the server; none is set for this host` (with `ClientCerts`) |
| `got_conn` | httptrace | `new` / `reused, idle 2s`, `local → remote` |
| `wrote_headers`, `wrote_request` | httptrace | the write error, if any |
| `got_1xx`, `first_byte` | httptrace | the 1xx status |
| `redirect` | Send | `302 → next host` |
| `auth_challenge` | Digest | `401 Digest SHA-256, qop auth`: the hop ends, the request is sent again with credentials |
| `auth_unanswered` | Digest | why a 401 was returned as it is: `credentials rejected`, `no Digest challenge`, an unsupported algorithm or qop, another origin, a body that cannot be sent again |
| `body_done` | Send | bytes read, and whether the body limit cut it |
| `cookies_sent` | cookie jar | names of the jar's cookies added to the hop (with a jar only) |
| `cookies_stored` | cookie jar | `stored a, b; ignored c: domain_mismatch`, or `none stored, off for this request: a, b` with `SkipStoringCookies` (with a jar only) |
| `request_options` | Send | the options that differ from `DefaultSettings`, once at the start: `HTTP/1.1 only`, `HTTP/2 only`, `URL encoding RFC 3986`, `URL not encoded`, `jar cookies not sent`, `response cookies not stored` (the cookie ones with a jar only); none when all are the defaults |
| `events_dropped` | Send | how many events beyond the 1,000 kept |

Details hold addresses and error messages, never the path or query.

### Request as sent (DBG-01)

`Response.Request` (the final hop) and `Hop.Request` (each redirect) are
`*SentRequest`: the request as the client wrote it, after variables, auth
and the cookie jar were applied, and after `net/http` added what it adds
itself. It is recorded, not rebuilt from `Request`:

- **Header fields** come from httptrace's `WroteHeaderField` hook, which
  `net/http` calls for each field it writes, with the value as written: on
  HTTP/1.1 `Host`, `User-Agent` (when not empty), `Content-Length` or
  `Transfer-Encoding`, then the other fields sorted by name in the user's
  spelling; on HTTP/2 the pseudo-header fields (`:authority`, `:method`,
  `:path`, `:scheme`), then the lower-cased fields, a `Cookie` split into one
  field per cookie, `content-length` when sent. The recorder keeps them per
  hop; a retry on a new connection (a reused connection the server had
  closed) starts the list over. The `Proxy-Authorization` field
  `net/http` adds for an HTTP proxy's credentials (absolute form, NET-02)
  is left out: it belongs to the proxy hop, and holds its password.
- **Method, URL and target** come from the request the transport gets
  (`cookieTransport`, which sees every hop): `URL` without userinfo and
  fragment, `Target` the HTTP/1.1 request-target (`URL.RequestURI()`, what
  `net/http` writes; the absolute form `http://host/path?q` for a hop sent
  to an HTTP proxy, NET-02) or the `:path` field on HTTP/2. `Proto` is `HTTP/2.0`
  when the first field is a pseudo-header, `HTTP/1.1` otherwise, and ""
  when nothing was written.
- **Body:** the transport reads the body through a capture
  (`captureBody` in `sent.go`) that keeps the first `MaxRecordedBody`
  (1 MiB) bytes and counts all of them (`BodySize`, `BodyTruncated`). A
  body sent again (a 307/308 redirect, a retry through `GetBody`) is
  captured again for its hop. Chunked framing is not part of `Body`.
- `Head()` writes the request line and the fields, CRLF line ends and the
  blank line. On HTTP/1.1 it is byte for byte what went out: `TestWire` and
  `TestWireBody` check `Head()` followed by `Body` against the bytes the raw
  server received for every case of the wire contract (the head and the
  decoded body for chunked requests). On HTTP/2 the fields are sent
  HPACK-compressed; `Head()` shows them as `name: value` lines without a
  request line (`TestSentRequestHTTP2` compares them with what the server
  received).

**Masking hook.** The client does not know which values are secrets, so a
`SentRequest` holds them in clear. `Masked(m Masker)` returns a copy with
`m.MaskText` applied to the URL, the target and the body, and
`m.MaskHeader(name, value)` to each header value. `SecretMasker`
(`NewSecretMasker(secrets)`) replaces each secret, and its query and path
escapings, by `Mask` (`••••••`), longest first, and always hides the
credentials of `Authorization` and `Proxy-Authorization` after the scheme
(`Bearer ••••••`, `Basic ••••••`), so a Basic password is hidden although
only its Base64 form is sent. Digest credentials keep their parameters,
which say what was answered, and lose only the `response` hash, which could
be used to guess the password offline (`Digest username="bob", realm="r",
…, response="••••••"`). The zero `SecretMasker` does only the latter.

**Token requests (AUTH-33).** An OAuth 2.0 auth's token request is a
`Send` of its own through the same client, made by the HTTP service before
the request (`internal/app/services/http/oauth2.go`). The service puts its
hops first in the send's `TimelineDTO` (kind `token`, with the token
response's body), shifts the request's own hops and events after it, and
masks both with the send's masker, which knows the client secret and the
tokens ([auth.md](auth.md#oauth-20-auth-08-auth-12-auth-33)).

### Digest authentication (AUTH-06)

`Request.Digest` makes the client answer a `401` with a Digest challenge
(RFC 7616, and RFC 2617 servers). It is done inside the client, by
`authTransport` (`authtransport.go`), the outermost `RoundTripper` of the
`http.Client`, around `cookieTransport`; the computation is in `digest.go`.

1. The hop is sent without credentials. Any response but `401` is returned.
2. On a `401`, the `WWW-Authenticate` fields are parsed (several challenges
   per field, quoted strings, token68 skipped). Of the Digest challenges
   the one with the strongest algorithm the client knows is picked:
   `SHA-512-256` > `SHA-256` > `MD5`, each also as `-sess`; no `algorithm`
   is MD5. `qop`: `auth` when offered (it does not need the body), else
   `auth-int` (the body is read again through `GetBody` and hashed); a
   challenge without `qop` is answered the RFC 2617 way (no `nc`, `cnonce`).
3. The 401's body is drained (up to 256 KiB, so the connection can be
   reused) and the hop ends in the recorder (`auth_challenge` event,
   `Hop.Kind` `auth`). The request is cloned with a fresh body from
   `GetBody` and `Authorization: Digest username, realm, uri, algorithm,
   nonce, nc, cnonce, qop, response, opaque[, userhash]`, and sent through
   the same context: the dial guard, cancellation, the timeouts, the traces
   and the cookie jar (a cookie set by the 401 is sent) apply as to any
   hop. `uri` is the request-target as written (`URL.RequestURI()`).
4. A second `401` is returned as it is (`auth_unanswered`, credentials
   rejected), unless its challenge has `stale=true`: the nonce expired,
   so it is answered once more with the new nonce.

Details:

- **Nonce count and cnonce.** `nc` counts the requests sent with a nonce
  in the send, from `00000001`; `cnonce` is 128 random bits in hex, new
  for each answer. Nonces are not kept across sends: each send starts with
  an unauthenticated request.
- **Redirects.** After a challenge, the next hops to the same origin
  (scheme, host, port) in the send are sent with credentials straight away,
  with the known challenge and the next `nc`; a `401` to those is answered
  like a first challenge. A challenge from another origin is not answered
  (`auth_unanswered`), like `Authorization` on redirects (PRV-11), unless
  `ForwardAuthorization` is set.
- **userhash** (RFC 7616 section 3.4.4): with `userhash=true` the username
  is sent as `H(username:realm)`. Otherwise a username with non-ASCII or
  control characters is sent as `username*=UTF-8''…` (RFC 8187), and
  `"` and `\` are escaped in the quoted string. Values are hashed as UTF-8.
- **Explicit credentials win.** A request that has an `Authorization`
  field (an enabled row, or URL userinfo) is sent as it is, and a 401 is not
  answered.
- **Not answered:** a challenge whose algorithm or qop is unknown (`SHA-1`,
  `auth-conf`), one without `nonce`, a request whose body cannot be read
  again, `Proxy-Authenticate` (no proxies yet). The 401 is then the
  response.
- **Tests** (`digest_test.go`) check the RFC 7616 section 3.9.1 and RFC
  2617 section 3.5 examples, challenge parsing and selection, username
  encodings and masking, and send to an in-process Digest server that
  checks the answer with its own computation and rejects replayed nonce
  counts: every algorithm (none, MD5, SHA-256, SHA-512-256, each `-sess`)
  with qop `auth`, `auth-int`, both and none, with and without `userhash`;
  a stale nonce; a wrong password; an `Authorization` row; same-origin and
  cross-origin redirects.

### netx trace and httptrace

httptrace is the source of truth for the phases and the addresses. The
netx trace feeds the timeline only:

- httptrace separates DNS from connect and reports each address tried.
  `net.Dialer` emits the events, reached through `ctx` because `Direct`
  passes it on. `netx.DialDone.Duration` lumps DNS and connect together.
- The addresses come from the connection `GotConn` reports. That works for
  reused connections, where no dial happens and netx sees nothing.
- netx sees each dialer hop, so through a proxy the timeline shows the
  proxy hop (`proxy_hop`, `Conn.Proxy`) and the `Proxy` phase comes from
  the proxy dialer's `DialDone` (SOCKS-12). With a proxy, httptrace's DNS
  and Connect describe the connection to the proxy (with `socks5h` there
  is no local lookup of the target), and `RemoteAddr` is the proxy's.
  `TestProxyHopTimeline` checks the hop, the phase order, a reused
  connection and an absolute-form one.
- Both traces compose with the caller's: `netx.WithTrace` and
  `httptrace.WithClientTrace` add hooks and never replace them. A caller
  can still put its own hooks in the context passed to `Send`
  (`TestTraceHooks`, `TestTimingLateHooks`).

### Limits

- **Errors:** a failed `Send` returns no `Response`, so no `Timing`. The
  error's `stage` detail says how far the request got. A caller that needs
  the partial timeline of a failed request (DBG-09) can add its own hooks
  to the context. Returning partial timing from `Send` can come with the
  send options of HTTP-37.
- **Late hooks:** after a cancellation, Go finishes a dial or TLS handshake
  in the background, and its hooks fire after `Send` returned. The
  recorder ignores them.
- **1xx:** the first byte of a `100 Continue` counts as the first response
  byte. With `Expect: 100-continue`, `Wait` ends at the 100, and the body
  upload and the server's work fall into `Download`.
- **Loopback:** the server can answer before `net/http` calls the
  `WroteRequest` hook. `Wait` is then zero or a few microseconds.
- **Events** are capped at 1,000 per send (about 60 hops).

## Hooks for callers

No extra API is needed to observe a send. Hooks in the context passed to
`Send` reach every layer:

- `netx.WithTrace` sees each dial (target, addresses, duration, error), the
  proxy hops included.
- `httptrace.WithClientTrace` sees DNS, connect, the TLS handshake,
  `GotConn`, the written headers and request, and the first response byte.

`Send`'s own traces (the recorder above, the stage, the header timeout)
compose with the caller's. `TestTraceHooks` shows both, with the second
request on a reused connection.

## Tests

`go test -race ./internal/core/httpclient`:

- `wire_test.go`: raw bytes on the wire against goldens (`-update` to
  regenerate, then review the diff), and binary bodies.
- `bodyto_test.go` (HTTP-37): a 200 MiB body through `SendTo` with the
  allocations measured (`runtime.MemStats`) and the progress calls
  counted, the limit (and exactly at it), gzip, x-gzip, zlib and raw
  deflate decoded, codings kept as received, a body that is not gzip, a
  gzip body cut in half, a gzip bomb stopped at the limit, a cancel
  mid-body, `Open` and write errors. The service side is
  `internal/app/services/http/largebody_test.go`: 200 MiB through `Send`
  to a spool file with progress events and allocations measured, the
  limit warning, a cancel that removes the partial file, a gzip body
  shown, saved and sized decompressed, assertions read back from the file
  and refused over 256 MiB, and `ReadBody` pages that never split a
  character.
- `client_test.go`: response capture, the body cap, no decompression, trailers,
  redirects, credentials across hosts, no cookie jar, HTTP/2 over TLS, TLS
  errors, malformed responses, timeouts, the guard and the trace hooks.
- `cancel_test.go`: cancellation at every stage, with the goroutine check.
- `safedefaults_test.go`: the PRV-11 defaults (TLS verification on,
  redirects capped, `Authorization` kept to the origin).
- `redirectopts_test.go`: every HTTP-22 option (follow, the limit with 0,
  keep the method on 301/302/303/307/308 with the timeline's recorded
  methods, forward `Authorization`) and the HTTP-23 switch against
  httptest servers. `internal/app/services/http/netsettings_test.go` runs
  them end to end from request file settings, with the app's TLS default.
- `cookies_test.go`: the cookie jar end to end: cookies set on redirects
  sent on the next hop, `RequestCookie` per hop, rows and jar, cross-host
  redirects, persistence across a restart.
- `options_test.go`: the request options against httptest servers: each
  URL encoding mode (HTTP-24), each HTTP version against HTTP/1.1, h2 and
  h2c servers with separate connection pools and `http2_unsupported`
  (HTTP-25), the cookie switches (HTTP-26), QUERY redirects and its
  idempotency marker (HTTP-03), and the `request_options` event.
  `internal/app/services/http` (`TestSendRequestOptions`) runs them from
  request file settings.
- `timing_test.go`: the phase arithmetic on scripted hooks with a fake
  clock (Happy Eyeballs, reuse, retries, early answers, redirects, the
  event cap). Then, against local TLS servers (HTTP/1.1 and h2, `localhost`
  and `127.0.0.1`, with a test certificate for `localhost`): every phase
  observed on a new connection and in order (`Offsets`), reuse on the second request, addresses,
  TLS version, ALPN and SNI, `Wait` and `Download` at least the server's
  sleeps, the body cap, per-hop redirect timing, and hooks firing after a
  cancelled `Send`.
- `request_test.go`, `errors_test.go`: validation, URL splitting, defaults and
  the error mapping.
