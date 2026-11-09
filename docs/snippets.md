# Snippet panel

The **Code** sub-tab of the request editors shows the open request as
code (GEN-12, GEN-13): in a
language PostSonde writes ([Languages](#languages)), **resolved** (variables filled
in as a send fills them) or **templated** (`{{variables}}` kept as
written), with secrets masked unless the user reveals them in that view.
Copy puts the code shown on the clipboard. It builds on the snippet model
of Copy as cURL ([curl.md](curl.md#copy-as-curl)).

## Where

The **Code** sub-tab is the last sub-tab of the HTTP and SSE editor
(`RequestEditor`) and of the GraphQL editor (`GraphqlEditor`), after
Notes. A sub-tab rather than a dialog or a side panel: the code is a view
of the other sub-tabs, it follows every edit like the Headers tab's
preview does, it gets the whole section's height for long commands, and
the editors' sub-tab keys (arrows, Home, End) reach it with no new
shortcut. The sub-tab chosen is UI state (`drafts.subTabs`), never saved.
For a GraphQL operation that runs over WebSocket (a subscription over
`graphql-transport-ws`, or one running) the tab says it cannot be written
as an HTTP request instead of asking Go. The gRPC and WebSocket editors
have the same tab, last after Notes, with their own languages (grpcurl;
websocat and wscat): [gRPC and WebSocket commands](#grpc-and-websocket-commands).

## The panel

`components/editor/SnippetPanel.tsx`, `commands/snippets.ts`,
`commands/useSnippet.ts`:

| Control | What it does |
|---|---|
| **Language** | A select of the languages Go lists (`HTTPService.SnippetLanguages`, asked once per session). A language's name comes from the catalogue (`editor.snippet.language.<id>`) when it has an entry, else Go's `name`. |
| **Values: Resolved / Templated** | Toggle buttons (`aria-pressed`). Resolved is the default. Switching mode drops a reveal. |
| **Show secrets** | Resolved mode only; a toggle button (`aria-pressed`), enabled when something is masked or already shown. It asks Go again with `revealSecrets` and shows the real values. |
| **Copy** | Copies the code shown (`copySnippet`: the Wails clipboard, the browser's as fallback); the toast says "Copied, with secrets masked." or "Copied, with the real values of the secrets." when that applies. |
| Status line | Masked, shown ("in this view only"), filled in, or "kept as `{{placeholders}}`" (plus "Secrets typed in the auth are masked." when the templated code masks something), and the undefined variables left as written. |
| Code | A focusable, scrollable `<pre>` labelled "<language> code" ("cURL code"), plain text. |

**When it asks Go.** At once when the tab opens and when the language,
mode or reveal changes; `SNIPPET_DELAY_MS` (200 ms) after an edit, once
the queued edits reached Go (`waitForQueuedEdits`), like the header
preview; and after each send (the cookie jar or the OAuth 2.0 token may
have changed). Only the answer for the options shown is shown: after
Hide, the revealed code goes at once, not when the masked answer arrives.
A failure (a missing path parameter, a body too large, a NUL byte) shows
Go's message.

**Revealing is per view and never saved.** The reveal is state of the
panel component: nothing writes it to the drafts, the settings, the
project, `localStorage` or Go. It lasts while the Code tab of that
request stays open: switching sub-tab, closing the tab or switching mode
drops it, and the next panel starts masked. Go keeps nothing either:
every call builds the request anew (below).

## Go: `HTTPService.Snippet`

`internal/app/services/http/snippet.go`:

| Call | Result |
|---|---|
| `SnippetLanguages()` | `[]SnippetLanguageDTO` (`id`, `name`, `syntax`), in the order the panel lists them. |
| `Snippet(SnippetInputDTO{requestId, language, mode, revealSecrets})` | `SnippetDTO` (`language`, `mode`, `code`, `masked`, `revealed`, `unresolved`). `language` "" is `curl`; `mode` is `resolved` (also "") or `templated`. Fails with `snippet.unknown_language`, `http.snippet_mode`, and as Copy as cURL does. |

**Resolved** builds the request as the header preview does
(`headerPreviewBuild`) and writes it as Copy as cURL does: the active
environment's variables (local values included), dynamic values, path
parameters, folder headers, auth, settings and the jar's cookies, then
`snippet.FromBuild`. Secrets are masked with the values Copy as cURL
masks (`secretsOf`: the secret variables used, what the auth added or
signed with, every `Authorization` credential; [privacy.md](privacy.md#secrets-in-copied-commands)).
`revealSecrets` skips the masking of that very build, so the values shown
are the ones a send resolves: same scopes, same local side files, same
auth code (`TestSnippetModesAndSecretVariables` compares them with what a
test server receives). `revealed` says something would have been masked.

A relative request URL (`/users`, COL-08) is sent to `{{baseUrl}}`
([environments.md](environments.md#base-urls)): the resolved and masked
code hold the absolute URL, the templated code `{{baseUrl}}/users`.

**Templated** builds the request with `buildOptions.templated`:
`snippet.KeepReferences` as the Expand (every `{{reference}}` stays as
written, dynamic ones too) and the auth of `snippet.TemplateAuth`:

| Auth | Templated code |
|---|---|
| basic, bearer, apikey, digest | As typed, with each secret member typed as a value (or stored on another machine) as `••••••`; one made of `{{references}}` stays (`-u '{{user}}:{{pw}}'`, `Bearer {{token}}`). |
| jwt, oauth2 | `••••••` where the token goes: `Authorization: Bearer ••••••` (the auth's header name and prefix) or `?access_token=••••••`. No token is signed or taken from the store. |
| oauth1, awsv4, hawk | `••••••` where the signature goes: `Authorization: OAuth ••••••`, `Authorization: AWS4-HMAC-SHA256 ••••••`, `Authorization: Hawk ••••••`, or `oauth_signature=••••••` / `X-Amz-Signature=••••••` in the query. An OAuth 1.0 auth sent in the form body adds nothing. |
| netrc | `Authorization: Basic ••••••` (the file is not read). |

Then `snippet.FromTemplateBuild` (the URL as written when a reference is
in its scheme or host, `{{references}}` a build percent-encoded written
back, no jar cookies: they are not part of the request as written) and
`Request.MaskedTemplate`, which masks the values typed in the auth
wherever they appear and every `Authorization` or `Proxy-Authorization`
credential except one made only of references. So templated code never
holds the value of a secret variable, a local value, a typed auth secret
or anything made for one send; `revealSecrets` changes nothing there.
`unresolved` is empty.

**Nothing is fetched, read or asked** in either mode, as in the header
preview: no token request (a stored, unexpired OAuth 2.0 token or the
`Bearer …` placeholder in resolved mode), no `{{$file}}` read and no
netrc file read (left as written, or `Basic …`), no `{{$prompt}}` dialog
(left as written and listed in `unresolved`). A JWT key file is read as
for the header preview. Copy as cURL, an explicit one-off copy, still
reads `{{$file}}` and netrc files ([curl.md](curl.md#copy-as-curl)).

**Limits of the templated view.** A build step that needs a value can
fail on the reference kept in its place: a header name made of a
variable, GraphQL variables JSON with an unquoted reference
(`{"id": {{id}}}`), a body file path with a variable. The panel shows the
error; the resolved view still works.

## Languages

`snippet.Languages()`, in the panel's order. Each writes the model
(`snippet.Request`) with its own quoting and, where the client has one,
the option for each setting; what the client cannot do is left out, or
said in a comment when it changes what is sent. None reads a file: files
are referenced by path.

| ID | Name | Writes | Syntax |
|---|---|---|---|
| `curl` | cURL | A curl command for a POSIX shell (GEN-01, [curl.md](curl.md#copy-as-curl)). | `shell` |
| `curlCmd` | cURL (cmd) | The same command for the Windows command prompt (GEN-02). | `text` |
| `curlPowerShell` | cURL (PowerShell) | The same command, `curl.exe`, for PowerShell 7.3 or later (GEN-02). | `text` |
| `httpie` | HTTPie | An HTTPie 3 `http` command for a POSIX shell (GEN-03). | `shell` |
| `wget` | wget | A GNU Wget command for a POSIX shell (GEN-03). | `shell` |
| `http` | Raw HTTP | The HTTP/1.1 message (GEN-04). | `text` |
| `fetch` | JavaScript fetch | An ES module with top-level `await` (GEN-05). | `text` |
| `python` | Python requests | A `requests.request` call (GEN-06). | `text` |
| `go` | Go net/http | A `package main` program, gofmt-formatted (GEN-07). | `text` |
| `java` | Java HttpClient | A `Main` class with `java.net.http` (GEN-08). | `text` |
| `csharp` | C# HttpClient | A .NET program with top-level statements (GEN-09). | `text` |

No language has highlighting yet: the panel shows plain text, and no
CodeMirror language pack for these is installed.

Errors: `snippet.nul_byte` (a NUL byte in a shell argument),
`snippet.unsupported_char` (details `field`, `language`: a character the
language cannot write; below) and `snippet.body_unsupported` (a body the
language cannot send: a multipart form in wget).

### cURL for Windows

`CurlCmd` and `CurlPowerShell` write the options of `Curl` (the same
mapping, [curl.md](curl.md#copy-as-curl)); only the quoting and the line
continuation differ. `TestWindowsCurlRoundTrip` reads every command back
with the cURL importer's cmd or PowerShell tokenizer and checks the
request is the same; `TestQuoteWindows` reads back single words.

**cmd** (`curl`, lines continued with ` ^`), for the interactive prompt,
not a batch file (where `%` must be doubled). Two parsers read the line:
cmd.exe, which only knows `"…"` and `^`, then the C runtime of curl.exe,
which splits the arguments.

| Text | Written |
|---|---|
| Letters, digits, `- _ . / \ : = @ + ,` | As is (`C:\data\a.json`). |
| Anything else | `"…"`: inside, cmd.exe leaves `& \| < > ^ ( ) !` alone. |
| `"` | `""` (one quote for the C runtime, and cmd.exe stays in its quotes), the backslashes before it doubled; backslashes before the closing quote are doubled too. |
| `%` | `"%"`: out of the quotes, so a `%NAME%` would name a variable starting with a quote, which none does. |
| Line feed | `"^` and two line breaks, then `"` (cmd.exe's escaped line break, as Chrome writes it). |
| CR, other control characters, text that is not UTF-8 | `snippet.unsupported_char`: cmd.exe cannot carry them. Send such a body from a file. |

**PowerShell** (`curl.exe`, since `curl` is `Invoke-WebRequest` in
Windows PowerShell; lines continued with `` ` ``):

| Text | Written |
|---|---|
| Letters, digits, `- _ . / : = +` | As is. |
| Anything else | `'…'`, literal, `'` doubled; line breaks and tabs kept. |
| Other control characters, characters that are not printable, or `‘ ’ ‚ ‛` (which PowerShell reads as `'`) | `"…"` with backtick escapes: `` ` $ " “ ” „ `` escaped, `` `r `n `t `` and `` `u{…} `` for control characters. |

PowerShell 7.3 or later passes native arguments as written. Windows
PowerShell 5.1 (and 7.0 to 7.2 in legacy mode) does not escape a `"`
inside an argument and drops empty ones, so a command with a JSON body
reaches curl.exe changed there; the command is not written for it.

### HTTPie and wget

Both are POSIX shell commands, every word quoted with `snippet.Quote` as
the cURL command's are; a NUL byte fails with `snippet.nul_byte`.

**HTTPie** (`HTTPie`, HTTPie 3): `http`, the options, then the method and
URL, then one request item per line.

| Request | HTTPie |
|---|---|
| Header rows, cookies, implied Content-Type | `Name:value` items; an empty value `Name;` (sent empty); an empty User-Agent row `User-Agent:` (none sent). A value starting with `=` is escaped (`Name:\=…`), since `:=` is another separator. |
| Body in memory | `--raw '…'` (`'--raw=…'` when it starts with `-`). |
| File body | The standard input: `< /path`. |
| Multipart | `--multipart`, `name=value` and `name@/path;type=…` items; `: = @ ;` in a name and a value's leading `=` or `@` escaped with `\`. A text part's Content-Type and a file part's own file name cannot be set and are left out. |
| Basic, Digest | `--auth=user:password`, with `--auth-type=digest`. |
| TLS off, redirects, timeout, dot segments | `--verify=no`, `--follow --max-redirects=N`, `--timeout=1.5`, `--path-as-is`. |

HTTPie removes a backslash that comes before `: = @ ;` in an item, so
such a value cannot be written exactly.

**wget** (`Wget`, GNU Wget): `wget URL`, one option per continued line,
`-O -` to print the body.

| Request | wget |
|---|---|
| Method | `--method X`, left out for a GET without a body. wget upper-cases it. |
| Header rows, cookies | `--header 'Name: value'` (`Name:` for an empty value); a User-Agent row `--user-agent` (`''`: none sent). |
| Body | `--body-data '…'`, `--body-file /path`, with `--header 'Content-Type: …'` unless it is wget's own `application/x-www-form-urlencoded`. |
| Multipart | `snippet.body_unsupported`: wget cannot send one. |
| Basic | `--user`, `--password` and `--auth-no-challenge` (sent at once, as PostSonde sends it). Digest: `--user`, `--password` (answered to the challenge). |
| TLS off, redirects, timeout | `--no-check-certificate`, `--max-redirect N` (`0` when not followed), `--timeout 1.5`. |

wget percent-encodes `{ } | "` in the URL and removes its dot segments.

### Raw HTTP

`RawHTTP`: the request line in origin form (`POST /items?x=1 HTTP/1.1`)
with `Host` from the URL (unless a Host row is sent), or in absolute form
without `Host` when the URL has no scheme (templated `{{baseUrl}}/items`);
then the header rows, the jar's cookies, Basic credentials
(`Authorization: Basic <base64>`, or `Basic user:password` when they hold
a mask or a `{{reference}}`, which cannot be encoded) and the implied
Content-Type; `Content-Length` for a body in memory; a blank line and the
body as it is. Lines end in LF as shown (the wire uses CRLF). A file body
and each file part are written `< /path/to/file`, as `.http` files write
a file's contents; a multipart body uses the fixed boundary
`PostSondeFormBoundary`. Digest credentials (answered to the server's
challenge), TLS, redirects, timeout and HTTP version are not part of a
message and are left out. It never fails.

### JavaScript fetch

`Fetch`: `const response = await fetch(url, { … })`, then the status and
text printed; an ES module (top-level `await`), for Node 20 or later, or
a browser when no file is sent.

| Request | fetch |
|---|---|
| Method | `method:`, left out for a GET without a body. |
| Header rows, cookies | `headers: { … }`, fields of the same name joined with `, ` (`; ` for Cookie). A value that is not ASCII is its UTF-8 bytes, `String.fromCharCode(...new TextEncoder().encode("…"))`: fetch takes header values as bytes. An empty User-Agent row is left out (Node sends its own). Browsers do not send a Cookie header set here. |
| Basic | `"Authorization": "Basic " + btoa(…)`, of the UTF-8 bytes. |
| Digest, TLS verification off | A comment: fetch can do neither (Node: `NODE_TLS_REJECT_UNAUTHORIZED=0`). |
| Text body | A string literal, or a template literal (`` \ ` ${ `` escaped) when it has several lines. |
| File body | `await openAsBlob(path)` (`node:fs`). |
| Multipart | A `FormData`: text parts appended as strings (their Content-Type cannot be set, and FormData sends a line feed in them as CRLF), files as `openAsBlob(path, { type })` with the file name. |
| Redirects not followed, timeout | `redirect: "manual"`, `signal: AbortSignal.timeout(ms)`. fetch follows up to 20 redirects. |

String literals escape `\` and `"`, control characters and characters
that are not printable (U+2028 and U+2029 included). Text that is not
UTF-8 fails with `snippet.unsupported_char`. fetch refuses a GET or HEAD
with a body, and its URL parser removes `.` and `..` segments and encodes
`"`.

### Python requests

`Python`: `import requests`, one `requests.request(method, url, …)` call,
then the status and text printed.

| Request | requests |
|---|---|
| Header rows, cookies | `headers={…}`, fields of the same name joined; a value that is not ASCII as UTF-8 bytes (`"…".encode()`: http.client would encode a `str` as Latin-1). An empty User-Agent row is `None`, which leaves requests' own out. |
| Basic, Digest | `auth=(user, password)` (non-ASCII ones encoded), `auth=HTTPDigestAuth(…)`. |
| Text body | `data="…"`, several lines as adjacent literals in parentheses; `.encode()` when not ASCII. |
| File body | `data=open(path, "rb")`. |
| Multipart | `files=[(name, (None, value[, type])), (name, (filename, open(path, "rb"), type))]`. |
| Timeout, TLS off, redirects not followed | `timeout=1.5`, `verify=False`, `allow_redirects=False`. requests follows up to 30 redirects. |

Literals are in double quotes (single when the text has `"` and no `'`),
with `\n \r \t \xHH \uHHHH \UHHHHHHHH` for control characters and
characters that are not printable. Text that is not UTF-8 fails with
`snippet.unsupported_char`. requests upper-cases the method, and
percent-encodes `{ } [ ] | "` in the URL and removes its dot segments.

### Go net/http

`Go`: a `package main` program, formatted with `go/format` as gofmt
formats it, that sends the request with `http.Client.Do` and prints the
status and body.

| Request | Go |
|---|---|
| Header rows, cookies | `req.Header.Add(name, value)` in order (net/http writes the name canonicalized); a Host row `req.Host = …`; an empty User-Agent row sends none (net/http's rule). |
| Basic | `req.SetBasicAuth(user, password)`. Digest: a comment (net/http has none). |
| Text body | `strings.NewReader(…)`. |
| File body | The `os.Open`ed file, with `req.ContentLength` from `Stat`. |
| Multipart | `mime/multipart` into a buffer: a `part` helper that writes each part with its Content-Disposition as PostSonde writes it and its Content-Type; files read with `os.ReadFile`; `form.FormDataContentType()`. |
| Redirects | `CheckRedirect`: `http.ErrUseLastResponse` when not followed, a limit when it is not net/http's 10. |
| Timeout, TLS off, HTTP version | `Timeout: 1500 * time.Millisecond`; a cloned `http.DefaultTransport` with `InsecureSkipVerify` and `Protocols` (`SetHTTP1`, or `SetHTTP2` and, on http, `SetUnencryptedHTTP2`). |

Strings are raw (`` `…` ``) when they have a line break, `"` or `\` and
only printable characters, else `strconv.Quote`: any bytes can be
written, including a NUL and text that is not UTF-8.

### Java HttpClient

`Java`: `public class Main` with `main` (run with `java Main.java`, Java
17 or later), `java.net.http.HttpClient`, the status and body printed.

| Request | Java |
|---|---|
| Method | `.GET()`, `.DELETE()`, `.POST(…)`, `.PUT(…)`, else `.method("X", …)`. |
| Header rows, cookies | `.header(name, value)` in order. A header HttpClient does not let a request set (Host, Connection, Content-Length, Expect, Upgrade) is a comment; an empty User-Agent row is left out (HttpClient sends its own). HttpClient writes header values as ASCII: another character is sent as `?`. |
| Basic | `.header("Authorization", "Basic " + Base64…(…UTF_8))`. Digest: a comment (HttpClient has none). |
| Text body | `BodyPublishers.ofString(body)`: a string literal, a text block when it has several printable lines (the closing `"""` on its own line, the last line ended with `\`, trailing blanks as `\s`, `"""` broken with `\"`), else one literal per line. |
| File body | `BodyPublishers.ofFile(Path.of(…))`. |
| Multipart | Written by hand: `ofByteArrays` of the parts (Content-Disposition as PostSonde writes it), `Files.readAllBytes` for files, a random boundary. |
| Redirects, HTTP version, timeout, TLS off | `.followRedirects(Redirect.NORMAL)` (HttpClient follows up to 5), `.version(HTTP_1_1 / HTTP_2)`, `.timeout(Duration.ofMillis(…))`, an `SSLContext` trusting every certificate and `jdk.internal.httpclient.disableHostnameVerification`. |

String literals escape `\` and `"`, control characters as `\n \r \t` or
octal (`\001`: never a `\u` escape below U+0080, which Java would read
before the literal), other characters that are not printable as `\uHHHH`.
Text that is not UTF-8 fails with `snippet.unsupported_char`.
`URI.create` refuses `{ } | "` in a URL.

### C# HttpClient

`CSharp`: top-level statements (.NET 6 or later), a `SocketsHttpHandler`
and `HttpClient`, an `HttpRequestMessage`, the status and body printed.

| Request | C# |
|---|---|
| Header rows, cookies | `request.Headers.TryAddWithoutValidation(name, value)`, on `request.Content.Headers` for content headers (Content-Type, Content-Disposition…); Content-Length and an empty User-Agent row left out (.NET writes the first and sends none of the second). A Cookie header sets `UseCookies = false`; a value that is not ASCII sets `RequestHeaderEncodingSelector` to UTF-8. |
| Basic | `request.Headers.Authorization = new AuthenticationHeaderValue("Basic", …)`. Digest: the handler's `Credentials` (answered to the challenge). |
| Text body | `new ByteArrayContent(Encoding.UTF8.GetBytes(…))` (no Content-Type of its own), several lines as concatenated literals. |
| File body | `new StreamContent(File.OpenRead(…))`. |
| Multipart | `MultipartFormDataContent`: text parts as `ByteArrayContent`, files as `StreamContent` with their file name, each part's Content-Type set. |
| Redirects, TLS off, timeout, HTTP version | `MaxAutomaticRedirections` or `AllowAutoRedirect = false`; `SslOptions` accepting every certificate; `client.Timeout` (.NET's default is 100 s); `request.Version` with `RequestVersionExact`. |

Strings are verbatim (`@"…"`, `"` doubled) when they have a backslash and
only printable characters (a Windows path), else regular with `\0 \a \b
\f \n \r \t \v \uHHHH \UHHHHHHHH` (U+0085, U+2028 and U+2029, which end a
line in C#, escaped). Text that is not UTF-8 fails with
`snippet.unsupported_char`. The tests run the C# code only where
`dotnet` is installed (below).

### What every language leaves to curl

Only the curl commands write `forwardAuthorization` (`--location-trusted`)
and `followOriginalMethod` (`--post301…`); the other clients use their
own rules for both. The HTTP version is written where the client has the
choice.

## Adding a language

A language is a plug-in: one entry in `languages` in
`internal/core/snippet/language.go`.

```go
type Language struct {
	ID       string                         // "curl": the API's language ID, the UI's message key
	Name     string                         // shown when the UI has no text for ID
	Syntax   string                         // "shell" (POSIX), or "text"
	Generate func(r Request) (string, error) // pure: no file read, nothing sent
}
```

- `Generate` takes the neutral `snippet.Request` (method, URL as sent,
  headers, Basic or Digest credentials, body as data, a file path or
  multipart parts, cookies, TLS, redirects, timeout, HTTP version) and
  returns the code. Masking and the templated view happen on the model
  before it runs, so a generator never sees an unmasked secret it should
  not write, and needs no code for either mode. It fails with an
  `*errs.Error` for what the language cannot hold (like
  `snippet.nul_byte`).
- The service lists it (`SnippetLanguages`) and `Snippet` finds it by
  ID; no service, binding or panel change is needed. Add
  `editor.snippet.language.<id>` to `frontend/src/i18n/en/editor.ts` and
  to `LANGUAGE_NAMES` in `SnippetPanel.tsx` for a translatable name.
  A new `syntax` needs a CodeMirror language only if the panel starts
  highlighting (it shows plain text now).
- Tests: the goldens run over every listed language, so a new one needs
  only `-update` and a read of what it wrote: `TestLanguagesGolden`
  (`testdata/<id>/`) and the mode goldens (`TestModesGolden`,
  `TestModesReveal`, `testdata/modes/<id>/`), with the checks that no
  masked or templated code holds a secret. Add a runner to
  `languages_exec_test.go` when the language has a tool to run or check
  it.
- gRPC and WebSocket requests are not `snippet.Request`s: their
  languages are `GRPCLanguage` and `WebSocketLanguage` entries over
  `snippet.GRPCRequest` and `snippet.WebSocketRequest`
  ([below](#grpc-and-websocket-commands)), listed by `GRPCLanguages` and
  `WebSocketLanguages` and served by those services' own `Snippet`
  calls; the panel is the same. Their goldens and round trips are in
  `realtime_test.go`.

## gRPC and WebSocket commands

The Code sub-tab of the gRPC editor (`GrpcEditor`, GEN-10) and of the
WebSocket editor (`WebSocketEditor`, GEN-11), last after Notes, is the
same `SnippetPanel` with a `source` (`commands/snippets.ts`:
`grpcSnippetSource`, `wsSnippetSource`; `useSourceSnippet`). The editors
have no saved-request build in Go: their calls take the input the editor
builds (`invokeInput`, `connectInput`), so the panel sends that input,
asks again `SNIPPET_DELAY_MS` after it changes, and lists the languages
of that kind. The gRPC tab waits for an inherited `.proto` setup to load.

| Call | Result |
|---|---|
| `GRPCService.SnippetLanguages()` | `grpcurl`. |
| `GRPCService.Snippet({call, language, revealSecrets})` | `call` is an `InvokeInputDTO`. `mode` is always `resolved`: a call has no variables (Invoke refuses them), so the panel offers no Resolved / Templated choice and says "The call as it is set up now." The credentials of `authorization` and `proxy-authorization` metadata are masked unless `revealSecrets`. Fails with `snippet.unknown_language`, `grpc.invalid_settings` (a transport or compression that is not known) and `snippet.nul_byte`. |
| `WebSocketService.SnippetLanguages()` | `websocat`, `wscat`. |
| `WebSocketService.Snippet({connect, language, mode, revealSecrets})` | `connect` is a `ConnectInputDTO`. **Resolved** renders the URL, header rows and subprotocols with the request's scopes as Connect does, leaves undefined variables as written and lists them in `unresolved`, adds the jar's cookies (unless the request leaves the jar out) as the handshake does, and masks the values of the secret variables used and every `Authorization` credential unless `revealSecrets`. **Templated** is the input as written, without jar cookies; a typed credential is masked, one made of references kept. Fails with `ws.snippet_mode`, `snippet.unknown_language`, a template problem other than an undefined name, and `snippet.nul_byte`. |

Nothing is dialed, read or linked. Both models are written in the
handshake's or call's own terms: `snippet.WebSocketURL` splits the URL's
userinfo into the `Authorization: Basic …` row the WebSocket client sends
first and percent-encodes what the request line cannot hold;
`snippet.WithCookies` adds jar cookies after the first `Cookie` row.

All three commands are for a POSIX shell, every word quoted with
`snippet.Quote` (the `shell` syntax); there are no cmd or PowerShell
variants.

### grpcurl

`snippet.Grpcurl`: the options one per continued line, then the address
and `pkg.Service/Method` on the last line (Go's flag parsing stops at the
first argument that is not an option).

| PostSonde | grpcurl |
|---|---|
| No TLS (h2c) | `-plaintext` |
| TLS, verification off | `-insecure` |
| Address without a port | The port the transport dials: `:443` with TLS, else `:80` (grpcurl needs one). |
| `.proto` files and import paths | `-import-path` for each import path, then `-proto` with each file relative to the first import path holding it; a file no import path holds is named by its base name and its folder added as an import path (grpcurl refuses absolute file names without import paths). Stored paths are made absolute against the project. |
| No `.proto` files | Nothing: grpcurl asks the server's reflection service. |
| No method chosen | `list` instead of the method. |
| Metadata rows (enabled, named) | `-H 'key: value'`, keys in lower case; `-bin` values as standard Base64 (grpcurl decodes them). |
| Message | `-d '<json>'` as typed; none when blank (an empty message). For a stream from the client grpcurl sends each JSON value in `-d` as one message (use `-d @` to type them on standard input). |
| Connect timeout, deadline | `-connect-timeout`, `-max-time` (seconds). |
| gRPC-Web, gzip | grpcurl speaks native gRPC only and does not compress: a `#` comment line before the command says so. |

**Not carried:** custom CA bundles and client certificates (add
`-cacert`, `-cert`, `-key`), and leading or trailing spaces of a metadata
value (grpcurl trims them; Invoke sends them). Values with a NUL byte
fail.

### websocat and wscat

`snippet.Websocat` (websocat 1.x) and `snippet.Wscat`: the URL on the
first line (`wscat -c URL`), then one option per line. Both send each
line typed as a text message and print what arrives.

| PostSonde | websocat | wscat |
|---|---|---|
| Header rows (and the userinfo's `Authorization`, the jar's `Cookie`) | `-H 'Name: value'` | `-H 'Name: value'`; wscat keeps one value per name, so rows of one name are joined (`Cookie` with `; `, others with `, `). |
| Subprotocols | One `--protocol 'a, b'` (the `Sec-WebSocket-Protocol` value) | `-s a -s b` |
| Verify TLS off | `-k` | `-n` |
| Keepalive ping, pong timeout | `--ping-interval`, `--ping-timeout` (whole seconds, rounded up) | Not available. |

**Not carried:** connect timeout, auto-reconnect, custom CA bundles and
client certificates, binary messages (websocat `--binary`, wscat has
none).

### Checks

Nothing runs the tools (they are not installed in CI). Instead:

- `internal/core/snippet/realtime_test.go`: `TestRealtimeLanguagesGolden`
  (`testdata/grpcurl/`, `testdata/websocat/`, `testdata/wscat/`; plain
  and TLS calls, a setup inside and outside the import paths, reflection
  and `list`, gRPC-Web with gzip, `-bin` metadata, shell quoting, a NUL
  byte; headers, subprotocols, keepalive, duplicate rows, templated
  values), masked ones holding no secret; `TestGrpcurlRoundTrip` and
  `TestWebSocketRoundTrip` read every command back with the cURL
  importer's POSIX tokenizer and each tool's own argument rules and
  check it makes the same call or handshake; `TestRealtimeMasked`,
  `TestWebSocketURL`, `TestRealtimeLanguageByID`.
- `internal/app/services/grpc/snippet_test.go`:
  `TestSnippetReproducesCall` reads the grpcurl command back the same way
  into an `InvokeInputDTO`, calls the test gRPC server with it (plaintext
  and TLS) and checks the server receives the same metadata (binary ones
  too), message and deadline as from the editor's own call;
  `TestSnippetCall`.
- `internal/app/services/websocket/snippet_test.go`:
  `TestSnippetReproducesHandshake` does the same for websocat and wscat
  against the test WebSocket server (userinfo, secret variables, the
  jar's cookies, subprotocols, quoting); `TestSnippetModes`.

Running the commands against real servers is left to a manual check.

## Tests

- `internal/core/snippet/template_test.go`: `TestModesGolden`, both modes
  of the same requests (variables of every kind, a variable host, URL
  userinfo, typed and referenced Basic, bearer and API key secrets,
  Digest, typed `Authorization` rows) in
  `testdata/modes/<language>/<name>.{resolved,templated}.golden`, for
  every language of `Languages()` (regenerate with
  `go test ./internal/core/snippet -run TestModesGolden -update`), none
  holding a secret; `TestModesReveal`; `TestTemplateAuth` for every auth
  type; `TestLanguageByID`; `TestRestoreReferences`.
- `internal/core/snippet/languages_test.go`: `TestLanguagesGolden`, every
  language's code for the curl cases and the quoting cases (shell and
  template syntax, every kind of quote, backslash runs, Unicode, a long
  header, a header name with every token character, JSON, forms,
  multipart, files) built as Send builds them, and for model cases
  (Windows paths, a NUL byte, text that is not UTF-8, duplicate headers,
  a templated URL, Unicode Basic credentials), in
  `testdata/<language>/<name>.golden` (an error as `error <code>: …`;
  regenerate with `-run TestLanguagesGolden -update`), masked ones
  holding no secret; `TestLanguagesWriteEveryCase`; `TestQuoteWindows`;
  `TestWindowsCurlRoundTrip`.
- `internal/core/snippet/languages_exec_test.go`, skipped with `-short`
  and for each tool that is not installed: `TestSnippetsCompile` checks
  the code of every golden request with the language's tool
  (`python3 -m py_compile`, `node --check`, `go vet` over one module of
  them all, `javac -Xlint:all -Werror` over them all, renamed);
  `TestSnippetsSend` runs the code of every curl and quoting case (curl,
  Python with requests, Node, wget, Go and Java built once, and HTTPie and
  `dotnet run` where installed) against a test server and checks it
  receives what PostSonde sends, apart
  from the headers the client adds of its own, the multipart boundary and
  `Content-Length`. The cases a client cannot send the same way are
  skipped with the reason (above). The shell commands (curl, wget,
  HTTPie) run in `sh`, or in bash for one with `$'…'` words where `sh`
  cannot read them (dash before 0.5.13, [curl.md](curl.md#copy-as-curl)),
  and not on Windows, where a POSIX shell hands native programs their
  arguments through the C runtime's quoting and the ANSI code page.
- `internal/app/services/http/snippet_test.go`:
  `TestSnippetModesAndSecretVariables` (secret variables of every level
  masked, local values shown, reveal equal to what the server receives,
  not kept, templated exact and without secrets or local values),
  `TestSnippetAuthSecrets` (every auth type: masked by default, revealed
  on request, templated without the typed secret; the netrc file never
  read), `TestSnippetOAuth2` (the session's token masked or revealed,
  never asked for, never templated), `TestSnippetReadsAndAsksNothing`
  (`{{$file}}`, `{{$prompt}}`), `TestSnippetTemplatedURL`,
  `TestSnippetLanguages`.
- `frontend/src/components/editor/SnippetPanel.test.tsx`: masked by
  default, languages, reveal and hide (never the revealed code after
  Hide), reveal not kept or stored, templated, copy, the edit delay,
  errors and the WebSocket note, with the accessibility audit;
  `RequestEditor.test.tsx` opens the Code tab.
- `frontend/src/components/editor/SnippetPanel.source.test.tsx`: the
  gRPC source (no Resolved / Templated, reveal) and the WebSocket source
  (both languages, templated), asked again when the input changes;
  `GrpcEditor.test.tsx` and `WebSocketEditor.test.tsx` open the Code tab.
- gRPC and WebSocket: [Checks](#checks).
