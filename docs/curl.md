# cURL commands

`internal/core/curl` reads a curl command line: the base of pasting a cURL
command as a request (IMP-01).
It only reads text. It runs nothing, opens no files and expands no shell
syntax. `Command.Import` maps a command to an HTTP request file
([file-format.md](file-format.md)); see [Import](#import). The UI (paste
into the URL bar, the Import cURL dialog) is in
[frontend.md](frontend.md#curl-import). Files of curl commands (a shell
script, a `.txt` from API docs) are imported through the import framework
([import.md](import.md#curl-files)). Standard library only.

| Piece | Purpose |
|---|---|
| `Tokenize(src, dialect) Tokens` | Splits the first command into words (`Token`: value and byte range). Never fails. |
| `DetectDialect(src)` | Picks `posix`, `cmd` or `powershell` from the text. |
| `Parse(src)`, `ParseDialect(src, d)` | Tokenizes and reads the words into a `Command`. |
| `Command.Err()` | `nil`, or an `errs.Parse` error, code `curl.invalid_command`, for the first error diagnostic. |
| `Command.Body()` | The body curl would build from the data parts, and whether it is complete (no file parts). |
| `DataPart.Encoded()`, `Escape(s)` | A data part as sent; percent-encoding like `curl_easy_escape`. |
| `options.txt` | curl's options and whether each takes a value, embedded. |
| `Command.Import()`, `Import(src)` | The command as a `projectfile.HTTPRequest` (no ID, no name), a suggested `Name`, and the diagnostics of both steps. Never fails. |
| `StartsCommand(line)` | Whether a line starts a curl command: an optional prompt, then `curl` or `curl.exe` (bare or with a path), then a blank or the end. The cURL file importer splits files with it. |

## Dialects

`DialectAuto` detects the dialect: **cmd** when the text contains `^"` or a
line ends with `^`, **PowerShell** when a line ends with a backtick, **POSIX**
otherwise. A one-line cmd or PowerShell command without those marks is read
as POSIX, which gives the same words unless it uses cmd's `""` or
PowerShell's backtick escapes.

| Dialect | Rules |
|---|---|
| POSIX | `'...'` literal. `"..."` with `\` escaping only `` $ ` " \ `` and newline. `\` outside quotes escapes the next character. `\` + line break continues the line. Bash `$'...'`: `\a \b \e \E \f \n \r \t \v \\ \' \" \?`, octal `\nnn`, `\xHH`, `\uHHHH`, `\UHHHHHHHH`, `\u{...}`, `\cX`; a UTF-16 surrogate pair in two `\u` escapes is combined (lone surrogates and invalid code points become U+FFFD); an unknown escape is kept with its backslash. `$"..."` is read like `"..."`. `#` at the start of a word starts a comment. |
| cmd | Two passes, as on Windows. cmd.exe: outside its own `"` state, `^` escapes the next character, `^` + line break continues the line and escapes the first character of the next line (so `^` + two line breaks is a newline in a value, as Chrome writes it), and `& \| < >` end the command. Then the C runtime's argument rules: blanks separate words outside quotes, 2n backslashes + `"` are n backslashes and a quote that toggles quoting, 2n+1 are n backslashes and a literal `"`, `""` inside quotes is a literal `"`, other backslashes are literal. |
| PowerShell | `'...'` literal with `''` for a quote. `"..."` with backtick escapes and `""`. A backtick outside quotes escapes the next character (`` `n `t `0 `u{...} ``…); backtick + line break continues the line. PowerShell's own argument passing to native programs (quotes stripped by older versions) is not emulated. |

**Chrome's cmd format.** Chrome's "Copy as cURL (cmd)" doubles every
backslash before it escapes quotes, so `C:\tmp` is copied as `C:^\^\tmp`,
which curl.exe would receive as `C:\\tmp`. When the text contains `^"`,
backslash runs that are not followed by a quote are halved, which recovers
the request DevTools copied. Other cmd text follows the C runtime exactly.

In every dialect:

- Leading blank lines, a UTF-8 BOM and a prompt (`$ `, `% `, `> `,
  `PS C:\...> `, `C:\...>`) are skipped. The first word must be `curl` or
  `curl.exe`, with or without a path (`curl.not_curl` otherwise).
- CRLF counts as one line break, and CRLF inside quoted text becomes LF.
- The command ends at an unescaped line break. When the next non-blank line
  starts with `-`, it is taken as a forgotten continuation
  (`curl.missing_continuation`) and the command goes on. Otherwise the rest
  is ignored (`curl.trailing_input`), as it is after `|`, `;`, `&&`, `||`,
  a `&` that ends a word (POSIX), and a here-document `<<`.
- An unquoted `&` inside a POSIX word, typically in a URL, is kept as part of
  the word (`curl.unquoted_ampersand`): the pasted URL was meant whole.
- POSIX redirections (`> file`, `2>&1`, `&> file`, `< file`) are dropped with
  their target (`curl.redirect_ignored`). In cmd and PowerShell, `>` and `<`
  end the command.
- Shell expansions are kept as written and reported
  (`curl.shell_expansion`): `$NAME`, `${...}`, `$(...)`, `$1`, `` `...` ``,
  PowerShell `$env:NAME`, cmd `%NAME%`. Unquoted POSIX glob characters
  `* ? [` are reported as info (`curl.shell_glob`).

## Options

A word starting with `-` is an option until `--`; after `--` every word is a
URL. Short options cluster (`-sSL`); a value-taking short option takes the
rest of the word or the next word (`-XPOST`, `-HX:y`, `-d@file`). Long
options take the next word or `--name=value` (which curl itself does not
accept, but is read here). A boolean option can be negated as `--no-name`.
Options are case-sensitive and must be written in full.

`options.txt` lists curl's options and whether each takes a value. It is
generated from `curl --help all` of curl 8.7.1, with options added in later
versions appended by hand. To update it, regenerate the list from a newer
curl, keep the header, and run the tests (`TestIgnoredOptionsAreKnown`).

**Unknown options.** Whether an unknown option takes a value cannot be
known. The heuristic: it takes the next word when that word does not start
with `-` and does not look like a URL (no `://`, not `localhost…`, not
`host.tld[:port][/…]`); `--name=value` never takes the next word. The
`curl.unknown_option` range covers the word taken as the value, so the UI
can show what was skipped.

### What is imported

| Option | `Command` | Notes |
|---|---|---|
| URL, `--url` | `URL`, `URLs` | The first URL is used; each further one is `curl.multiple_urls`. No URL: `curl.missing_url` (error). The URL is kept as written, without a scheme if it had none. |
| `-X`, `--request` | `Method`, `MethodExplicit` | Kept as written. Without `-X`: `-I` HEAD, `-G` GET, data or `-F` POST, `-T` PUT, else GET. |
| `-H`, `--header` | `Headers` | In order, duplicates and case kept. `Name: value` is `set` (blanks around the value trimmed), `Name:` is `remove` (curl stops sending its own header), `Name;` is `empty` (sent with an empty value). Anything else is `curl.invalid_value`; `@file` is `curl.file_reference`. |
| `-d`, `--data`, `--data-ascii` | `Data` kind `data` | `@file` is a file part (curl strips CR and LF when it reads it). |
| `--data-raw` | kind `raw` | `@` is literal. |
| `--data-binary` | kind `binary` | `@file` is a file part, sent as is (info). |
| `--data-urlencode` | kind `urlencode` | `content`, `=content`, `name=content`, `@file`, `name@file`. The first `=` wins; `@` counts only without `=`. `Encoded()` percent-encodes the content. |
| `--json` | kind `json` | Like `--data-binary`. Adds `Content-Type` and `Accept: application/json` (`Implied`) unless `-H` names them. |
| `--url-query` | `URLQuery` | Like `--data-urlencode`; `+text` is added unencoded (kind `rawquery`). |
| `-G`, `--get` | `Get` | The data goes into the query; `Body()` still joins it. |
| `-F`, `--form` | `Form` | `name=value`, `name=@file` (file upload, info), `name=<file` (text from a file, warning). Parameters `;type=`, `;filename=`, `;headers=` are read, `"..."` quoting (with `\"` and `\\`) protects `;`, and unknown parameters are skipped like curl does (`curl.invalid_value`). |
| `--form-string` | `Form` kind `text` | Everything after the first `=` is literal. |
| `-T`, `--upload-file` | `UploadFile` | Info `curl.file_reference`. |
| `-u`, `--user` | `User`, `AuthScheme` | Split at the first `:`. No `:` is `curl.password_missing` (curl would prompt). `user:` has an empty password. |
| `--basic`, `--digest`, `--ntlm`, `--negotiate`, `--anyauth` | `AuthScheme` | `any` for `--anyauth`. |
| `--oauth2-bearer` | `OAuth2Bearer` | |
| `-A`, `--user-agent` | `UserAgent` | `nil` when not given; `""` removes the header. |
| `-e`, `--referer` | `Referer` | `;auto` is dropped. |
| `-b`, `--cookie` | `Cookies` | Values with `=` are cookie strings; others are cookie files (`curl.file_reference`). |
| `-k`, `--compressed`, `-L` (`--location`, `--location-trusted`, `--follow`) | `Insecure`, `Compressed`, `FollowRedirects` | |
| `--max-redirs` | `MaxRedirects` | Whole number, `-1` for no limit. |
| `--http1.0` … `--http3-only` | `HTTPVersion` | `1.0`, `1.1`, `2`, `2-prior-knowledge`, `3`, `3-only`. |
| `-m`, `--max-time`, `--connect-timeout` | `MaxTime`, `ConnectTimeout` | Seconds, fractions allowed. Invalid values are `curl.invalid_value`. |
| `-x`, `--proxy`, `-U`, `--proxy-user`, `--noproxy` | `Proxy`, `ProxyUser`, `NoProxy` | |

Data parts are joined with `&` in order, except that no `&` goes before a
`--json` part (curl concatenates those). Using data (without `-G`), `-F`,
`-T` and `-I` together is `curl.conflicting_body`: curl refuses it.

Options that do not change the request are **ignored** with an info
diagnostic (`curl.ignored_option`): `-s`, `-S`, `-v`, `-i`, `-o`, `-O`,
`-w`, `-D`, `--trace*`, `-f`, `--retry*`, rate and size limits, `-g`,
`-c` and similar. Every other known option is **not imported** and warned
about (`curl.unsupported_option`): client certificates, `--cacert`,
`--resolve`, `--connect-to`, `--aws-sigv4`, `-K` (config files are not
read), and `-:`/`--next`, after which the rest of the command is ignored.
curl's URL globbing (`{a,b}`, `[1-3]`) is not expanded.

## Diagnostics

Every `Diagnostic` has a stable `code`, a `severity` (`error`, `warning`,
`info`), a byte range `[start, end)` into the pasted text and a message.
Messages name options, header names and files, never header, data or
credential values. `Command.Diagnostics` is sorted by `start`.

| Code | Severity | Range |
|---|---|---|
| `curl.empty` | error | The whole text. |
| `curl.not_curl` | error | The first word. |
| `curl.unterminated_quote` | error | From the opening quote to the end. The words are still returned, the quote running to the end. |
| `curl.missing_value` | error | The option. |
| `curl.missing_url` | error | The whole command. |
| `curl.unknown_option` | warning | The option, and the word taken as its value. |
| `curl.unsupported_option` | warning | The option and its value; for `--next`, to the end of the command. |
| `curl.unexpected_value` | warning | `--flag=value` for a flag without a value. |
| `curl.invalid_value` | warning | The option and its value. |
| `curl.file_reference` | warning, or info where the path is kept (`-F name=@file`, `--data-binary @file`, `--json @file`, `-T`) | The option and its value. |
| `curl.password_missing` | warning | The option and its value. |
| `curl.multiple_urls` | warning | Each extra URL. |
| `curl.conflicting_body` | warning | The whole command. |
| `curl.shell_expansion` | warning | The expansion. |
| `curl.trailing_input` | warning | From the operator or next command to the end. |
| `curl.redirect_ignored` | warning | The operator and its target. |
| `curl.missing_continuation` | warning | The line break. |
| `curl.unquoted_ampersand` | warning | The `&`. |
| `curl.ignored_option` | info | The option and its value. |
| `curl.shell_glob` | info | The word up to the glob character. |

## Import

`Command.Import` (`import.go`) maps a command to an HTTP request file
(IMP-01). It never fails: what the request has no place for is reported
with the codes below, added to the parser's diagnostics and sorted with
them. A command with an error diagnostic is not imported by the service
(`ImportCurl` fails with `curl.invalid_command`); its mapping is only a best
effort for the preview.

| From | To | Notes |
|---|---|---|
| `Method` | `method` | As written; `GET` when empty. |
| First URL | `url` | `http://` is added when it has no scheme (`curl.scheme_added`, info). `--url-query` parts, then with `-G` the data parts (encoded as curl sends them), are appended to the query, before a fragment. |
| The URL's query | `params` | Split at `&` and the first `=`, nothing decoded: the editor's rule (`lib/urlParams.ts`). |
| Path placeholders | `pathParams` | One empty row per name `request.PathParamNames` finds (`:id`, `{id}`). |
| `-H` | `headers` | In order, names trimmed. `Name;` is a row with an empty value. `Name:` (curl's own header removed) is not imported (info): PostSonde adds no headers of its own. `Content-Length` is dropped (info): the length of the body sent is used. `-A` adds `User-Agent`, `-e` `Referer`, and the `-b` cookie strings one `Cookie` row joined with `; `. `--json`'s implied `Accept` is kept; its `Content-Type` comes from the body. |
| `-F`, `--form-string` | `multipart` body | Text parts with their `;type=`. `name=@file` is a file part with the path, not read; a relative path gets `curl.file_not_read` (warning: PostSonde reads it relative to the project folder). `name=<file` becomes a file part too (warning: curl sends the content as a text field). `;filename=` and `;headers=` are not imported (warning). An `-H Content-Type: multipart/form-data` without `boundary=` is dropped (info): the body's own boundary is sent. |
| Data without `-G` | body | One part from a file (`-d @file`, `--data-binary @file`, `--json @file`): a `file` body with the path (curl's default Content-Type as its `contentType` when no header sets one; for `-d`/`--data-urlencode` a warning that curl strips line breaks or encodes the file). Otherwise the literal parts joined as curl joins them; file parts are left out with `curl.file_not_read`. Then: `--json` without a Content-Type header, or JSON text without one (`curl.body_type`, info: curl would send it as a form), is a raw JSON body; without a Content-Type header or with `application/x-www-form-urlencoded`, text that is `name=value` pairs with valid percent-encoding (no blanks, no `{{`) becomes `urlencoded` fields, decoded; other text without a header is raw text with an explicit `Content-Type: application/x-www-form-urlencoded` row (what curl sends); with a header, raw of the header's type (`json`, `xml`, `html`, `yaml`, else `text`). |
| `-T file` | `file` body | The path; `-T -` is nothing. |
| `-I` | no body | `HEAD`. |
| `-u user:password` | `basic` or `digest` auth | Basic with `--basic` or no scheme; `--anyauth` also imports as Basic (info). `--digest` imports as `digest` auth with the same username and password (AUTH-06: sent without credentials first, then answering the server's Digest challenge, as curl does). `--ntlm`, `--negotiate`: not imported (warning), auth stays `inherit`. A typed password is moved to the local side files when the request is saved ([auth.md](auth.md)). |
| `--oauth2-bearer` | `bearer` auth | Not imported when `-u` is imported as Basic or Digest (warning); with `--ntlm` or `--negotiate` the bearer token is imported. |
| `-k` | `settings.verifyTls: false` | |
| `-L` | `settings.followRedirects: true` | Without `-L`, the app's setting applies. `--location-trusted` also sets `forwardAuthorization: true`. `--max-redirs n` sets `maxRedirects` with `-L`; `-1` (no limit) and `--max-redirs` without `-L` are not imported (info). |
| `-m` | `settings.timeoutMs` | Rounded up to whole milliseconds. |
| `--compressed` | nothing | Info: PostSonde does not decompress responses, so it asks for none; an `Accept-Encoding` header is dropped with it. |
| `--connect-timeout`, `--http1.0` … `--http3-only`, `-x`, `-U`, `--noproxy` | nothing | Warning (`curl.not_imported`): no per-request setting. |

`Name` is the URL's last path segment (percent-decoded), else its host
without user info, else "Imported request".

| Code | Severity | Range |
|---|---|---|
| `curl.not_imported` | warning, or info where the request behaves the same | The option and its value (for an auth scheme implied by `-u`, the range of that option, or empty at 0). |
| `curl.file_not_read` | warning | The option and its value. |
| `curl.scheme_added` | info | The URL. |
| `curl.body_type` | info | The first data option. |

`CollectionService.ParseCurl` returns the mapped request as a
`RequestDTO` with the diagnostics in UTF-16 offsets, and `ImportCurl`
writes it as a new request ([collection.md](collection.md#operations-and-trash)).
Both read at most 1 MiB of text. `ImportCurl` goes through the import
framework ([import.md](import.md#writing)): `curlimport.Command` makes a
model with the one request and `importer.Write` puts it straight into the
chosen folder (`IntoParent`) at the index, with a typed password or token
in the local side files.

## Copy as cURL

The other direction (GEN-01, EXP-01): any HTTP, GraphQL or SSE request as
the curl command that sends the same request, for a POSIX shell (bash,
zsh, dash, ksh). `internal/core/snippet` holds the generators, pure
"request → string" functions over one model that the other snippet
targets share (curl for cmd and PowerShell, HTTPie, wget, raw HTTP, fetch, Python, Go, Java, C#),
listed as the languages of the snippet panel (`snippet.Languages`; the
Code sub-tab, resolved or templated,
[snippets.md](snippets.md#languages)). Standard library only.

| Piece | Purpose |
|---|---|
| `snippet.FromBuild(b, cookies)` | The `snippet.Request` a `request.HTTPBuild` sends: method, the URL as sent, enabled header rows in order, Basic or Digest credentials, the body (text in memory, a file path, or the multipart parts with their paths), the jar's cookies, and the settings (TLS verification, redirects, timeout, HTTP version). Files are referenced, never read. |
| `Request.Masked(secrets)` | The request with its secrets replaced by `••••••`, and whether any was ([privacy.md](privacy.md#secrets-in-copied-commands)). |
| `snippet.Curl(r)` | The command. |
| `snippet.Quote(s)` | One POSIX shell word for `s`. |
| `snippet.Languages()`, `LanguageByID(id)` | The generators of the snippet panel (`Language`: ID, name, syntax, `Generate`); cURL is `LanguageCurl` ([snippets.md](snippets.md#adding-a-language)). |
| `snippet.TemplateAuth(a)`, `FromTemplateBuild(b)`, `Request.MaskedTemplate(secrets)` | The templated view: references kept as written, typed secrets and per-send values as `••••••` ([snippets.md](snippets.md#go-httpservicesnippet)). |
| `HTTPService.CurlCommand` | Builds request `requestId` as `Send` does and returns `CurlDTO` (`command`, `masked`, `unresolved`). |

**What is copied.** The request as PostSonde would send it now:
`request.BuildHTTP` or `BuildGraphQL` with the active environment's
variables, dynamic values (`{{$uuid}}` gets a fresh value), path
parameters, folder headers, the resolved auth with its local secret, the
request's settings over the app's (default timeout, TLS verification) and
the cookie jar's cookies for the URL. Undefined variables do not stop it:
they stay as written (`{{name}}`) and `unresolved` lists them. Other build
errors fail it as they fail a send. Secrets are masked unless the user
asks for them (`includeSecrets`).

**The command.** The URL on the first line after `curl` (and `-X` or
`-I`), then one option per line, continued with ` \`:

| Request | curl |
|---|---|
| Method | None for GET without a body and POST with one; `-I` for HEAD; else `-X METHOD` as typed (`-X GET` for a GET with a body, `-X QUERY`, `-X purge`). |
| URL | As sent: encoded in the request's URL encoding mode (`httpclient.SentURL`, HTTP-24), without the fragment. `-g` when it has `[ ] { }` (curl would read them as globs), `--path-as-is` when its path has `.` or `..` segments (curl would remove them). |
| Header rows | `-H 'Name: value'` in order, duplicates kept; an empty value is `-H 'Name;'` (sent empty); an empty User-Agent row is `-H 'User-Agent:'` (none sent, as PostSonde sends none). |
| Body Content-Type | For a text or file body without a Content-Type row, the automatic one as `-H 'Content-Type: …'`, except `application/x-www-form-urlencoded`, which is curl's own. |
| Raw text, URL-encoded form, GraphQL JSON | `--data-raw '…'`: sent byte for byte, `@` not special. Bodies over 1 MiB fail (`snippet.body_too_large`). |
| Binary file | `--data-binary @/absolute/path`. |
| Multipart | A text part without a type: `--form-string 'name=value'` (literal). With a type: `-F 'name="value";type=…'`. A file: `-F 'name=@/absolute/path;type=…'`, `;filename=` only when the name sent differs from the path's. A type with parameters (`text/plain; charset=utf-8`) goes as `;headers="Content-Type: …"`, since curl's `type=` stops at `;`. Paths and values with `; , " \` or blanks use `-F`'s `"…"` quoting. |
| Basic auth | `-u 'user:password'`: from the auth, from an `Authorization: Basic` row that decodes to `user:password` exactly, or from URL userinfo. |
| Digest auth | `--digest -u 'user:password'`. |
| Bearer, API key | As the header row or query parameter they add. |
| JWT (AUTH-16) | The token signed for this copy, as the header it is sent in (`-H 'Authorization: Bearer eyJ…'`) or the query parameter; masked like a bearer token unless copied with secrets. Each copy signs a new token, with its own time claims. |
| Netrc (AUTH-19) | The file is read for the copy, and the entry's credentials go in as a Basic header does: `-u 'login:••••••'`, the password masked unless copied with secrets. Not `--netrc`: the command sends what PostSonde sends. |
| OAuth 1.0 (AUTH-15) | The request signed for this copy, with its own nonce and timestamp: `-H 'Authorization: OAuth …'`, or the `oauth_*` parameters in the URL or the form body. The header is masked by name, the signature and secrets by value, unless copied with secrets. |
| AWS Signature v4 (AUTH-17) | The request signed for this copy, at its own time: `-H 'X-Amz-Date: …' -H 'Authorization: AWS4-HMAC-SHA256 …'` (with `X-Amz-Security-Token` and `X-Amz-Content-Sha256` when they are sent), or the presigned `X-Amz-*` parameters in the URL. The header is masked by name, the secret key, session token and signature by value, unless copied with secrets. |
| Hawk (AUTH-18) | The request signed for this copy, with its own timestamp and nonce: `-H 'Authorization: Hawk id="…", …, mac="…"'`, masked by name unless copied with secrets. |
| OAuth 2.0 (AUTH-08) | The token of this session, when a send got one and it has not expired: `-H 'Authorization: Bearer ••••••'` (or the query parameter), masked unless copied with secrets. A copy never asks the token endpoint: without a token it is `Bearer …`. |

| Jar cookies | `-b 'a=1; b=2'`, or appended to the request's own Cookie row as a send appends them. None when the request's `sendCookies` is off (HTTP-26). |
| `verifyTls` off | `-k`. |
| Redirects followed | `-L --max-redirs N` (PostSonde's default is 10, curl's is not following); `--location-trusted` with `forwardAuthorization`; `--post301 --post302 --post303` for a POST with `followOriginalMethod`. Nothing when redirects are not followed. |
| Timeout | `-m seconds` (`1.5` for 1500 ms). |
| HTTP version | `--http1.1` for HTTP/1.1 only; `--http2-prior-knowledge` for HTTP/2 only (h2c with prior knowledge on http, only h2 offered on https, no fallback), as the request sends. |

**Quoting** (`Quote`): a word of only letters, digits and `-_./:=@%+,`
(not starting with `-`) is written bare; anything else in single quotes,
`'` as `'\''`, line breaks and tabs kept; a value with other control
characters (a CR, an escape) or bytes that are not UTF-8 in `$'…'` with
`\r \n \t \\ \' \xHH` escapes (POSIX.1-2024; bash, zsh, ksh), so a paste
into a terminal cannot change it. A NUL byte cannot be in an argument:
the command fails with `snippet.nul_byte`.

**Differences that remain.** curl adds `User-Agent: curl/…` and
`Accept: */*` when the request has none (PostSonde sends neither). With
`-X`, curl keeps the method on every redirect, where PostSonde turns
POST, PUT and the like into GET on 301, 302 and 303 unless
`followOriginalMethod` is set. The multipart boundary differs. PostSonde's
connect timeout, proxy and client certificate settings are not written.

## Tests

- `internal/core/snippet/curl_test.go`: golden commands for many request
  shapes (`testdata/curl/`, regenerate with
  `go test ./internal/core/snippet -run TestCurlGolden -update`): methods
  (GET with a body, HEAD, QUERY, lower-case), headers with quotes and
  Unicode, JSON, text, URL-encoded, multipart and file bodies, every auth
  type and URL userinfo, settings (timeout, TLS, redirects, HTTP version),
  the three URL encoding modes, control characters, jar cookies with and
  without a Cookie row and with `sendCookies` off, masking, and GraphQL POST
  and GET. `TestCurlRoundTrip` reads each command back with this package's
  importer, builds the imported request and checks it sends the same
  request. `TestQuote` checks that the tokenizer reads every quoted word
  back.
- `curl_exec_test.go`: runs the commands with the machine's curl (skipped
  without it, or with `-short`) against a test server and checks it
  receives what PostSonde's own client sends for the same request.
- `import_test.go`: the mapping of each option and body shape, and every
  real-world command mapped to a request that `request.BuildHTTP` accepts
  and `projectfile.Marshal` writes, with a golden dump in
  `testdata/import/` (`go test ./internal/core/curl -run TestImportRealWorld -update`).
- `realworld_test.go`: commands in the shapes of Chrome "Copy as cURL"
  (bash with `$'...'` bodies, cmd), Firefox, Safari, Postman and Insomnia
  exports, and API documentation examples (form parameters with `-u key:`,
  `-G`, `--data-urlencode`, `-F` uploads, multi-line JSON, shell variables,
  pipes and prompts), each with explicit expectations and a golden dump in
  `testdata/realworld/` (`go test ./internal/core/curl -run TestRealWorld -update`).
- `tokenize_test.go`, `parse_test.go`: quoting and escaping per dialect,
  ranges, option forms, data, form and header semantics, diagnostics.
- `FuzzParse`: no panics, words in order, every range inside the text,
  diagnostics sorted (Import's too), in every dialect:
  `go test ./internal/core/curl -run '^$' -fuzz FuzzParse -fuzztime 60s`.
