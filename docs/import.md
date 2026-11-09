# Import

One pipeline for every import format (IMP-10): a format's
**importer** reads a file into a **model**, the **preview** says what would
be created, the **writer** puts it into the open project, and the
**report** says what was imported, skipped or converted with a loss, item
by item, with the place in the file. Everything runs locally on the file;
nothing is fetched.

```
file ──ReadFile──▶ Source ──Registry.Parse──▶ Model ──Preview──▶ Report (status "new")
                    (bytes, name)   (detect + Importer.Parse)  └──Write──▶ Report (imported / skipped / failed)
```

Go: `internal/core/importer` (the framework, standard library only),
`internal/core/importer/curlimport` (the cURL format),
`internal/core/importer/insomnia` (Insomnia exports),
`internal/core/importer/postman` (Postman collections, environments and
globals), `internal/core/importer/openapiimport` (OpenAPI 3.0/3.1
documents), `internal/core/importer/harimport` (HAR files, [har.md](har.md)),
`internal/core/importer/altair` (Altair GraphQL Client exports),
`internal/core/importer/postsondeenv` (PostSonde environment files),
`internal/core/importer/projectzip` (PostSonde's own folder zips),
`CollectionService.ImportFormats`, `PickImportFile`, `DetectImport`,
`PreviewImport` and `Import` (`internal/app/services/collection/import.go`),
and the window's file drops (`internal/app/filedrop.go`, [below](#dropped-files)).
UI: the Import dialog, [frontend.md](frontend.md#import). The reverse
direction for Postman, exporting a folder as a Postman collection, is
[below](#postman-export); a folder saved as a zip of project files and
imported back is [Folder zips](#folder-zips).

## Importers

```go
type Importer interface {
	Format() Format                                     // ID "curl", Name "cURL commands", Extensions
	Detect(src Source) int                              // importer.No … importer.Sure
	Parse(ctx context.Context, src Source) (*Model, error)
}
```

- **Detect** looks at the name and the start of the data and returns a
  confidence: `No` (0), `Maybe` (25: the name or a loose look fits),
  `Likely` (60: the content has the format's shape), `Sure` (100: the
  content declares it, e.g. a Postman schema URL, `openapi: 3.1.0`,
  `_type: export`). It must be cheap.
- **Parse** never writes anything and never reads other files or the
  network. Problems of single items are **diagnostics** on the item; Parse
  fails only when the file cannot be read in the format at all
  (`importer.InvalidFile(format, msg, at)`: kind `parse`, code
  `import.invalid_file`, with `line`/`path` details) or when `ctx` is done.
  YAML formats read their text with `internal/core/yaml`
  ([yaml.md](yaml.md)).
- A **Registry** (`importer.NewRegistry(...)`) holds the formats in order
  of preference. `Detect` picks the most confident (the first registered
  wins a tie; none: `import.unknown_format`, kind `unsupported`).
  `Parse(ctx, src, id)` uses format `id`, or detects it when `id` is `""`,
  and normalises the model: a blank `Name` becomes the file's base name
  (else "Imported"), and a model with no items and no environments gets an
  `import.empty` warning.

The formats PostSonde knows are registered in `defaultImporters()`
(`internal/app/services/collection/import.go`). A new format is a package
under `internal/core/importer/` implementing `Importer`, added there.

| Format | Package | Status |
|---|---|---|
| cURL commands (`.sh`, `.curl`, `.txt`, `.bat`, `.cmd`, `.ps1`) | `curlimport` | [below](#curl-files) |
| Postman Collection v2.0/v2.1, environments, globals (IMP-02, IMP-03) | `postman` | [below](#postman-files) |
| Insomnia v4 JSON (or YAML), v5 YAML (IMP-04) | `insomnia` | [below](#insomnia-exports) |
| OpenAPI 3.0 / 3.1, JSON or YAML (IMP-05) | `openapiimport` | [below](#openapi-documents); read by `internal/core/openapi` ([openapi.md](openapi.md)) |
| Swagger 2.0, JSON or YAML (IMP-06) | `openapiimport` | [below](#openapi-documents); converted to the OpenAPI 3.0 model by `internal/core/openapi` ([openapi.md](openapi.md#swagger-20)) |
| HAR 1.1/1.2 from browser DevTools (IMP-07) | `harimport` | [har.md](har.md#import) |
| Altair GraphQL Client windows (`.agq`), collections (`.agc`), environments (IMP-08) | `altair` | [below](#altair-exports) |
| PostSonde environment files (EXP-05) | `postsondeenv` | [below](#postsonde-environments) |
| PostSonde folder zip (`.zip`, EXP-02) | `projectzip` | [below](#folder-zips) |

## The model

`importer.Model` is format-neutral and maps one to one onto project files:

| Type | Holds | Written as |
|---|---|---|
| `Model` | `Name` (the suggested folder name), `Root Folder`, `Environments`, file-level `Diagnostics` | — |
| `Folder` | `Name`, `Description`, `Variables`, `Headers`, `Auth`, `GRPC`, `Server`, `Unknown` (the other `_folder.json` members, set by the folder zip only), `Items`, `Source`, `Diagnostics` | a directory with `_folder.json` ([file-format.md](file-format.md)) |
| `Item` | exactly one of `Folder`, `Request`, `Skipped` | — |
| `Request` | `Name`, `File` (a `*projectfile.HTTPRequest`, `GraphQLRequest`, `WebSocketRequest`, `GRPCRequest`, `TCPRequest`, `SSERequest` or `SocketIORequest`; its ID and name are ignored), `Source`, `Diagnostics` | a request file |
| `Skipped` | `Kind` in the source's terms ("curl command", "script", "socket.io request"), `Name`, `Code` and `Reason`, `Source` | nothing; a warning in the report |
| `Environment` | `Name`, `Variables` (`environments.Variable`: secret ones keep their value), `Source`, `Diagnostics` | `environments/<slug>.json`, secret values in the local side files ([environments.md](environments.md)) |

The root folder's settings become the new folder's settings, so a Postman
collection's variables, auth and headers land in the folder that holds its
requests. **Lossy conversions** are warnings on the item (a script kept as
a note, an auth type PostSonde lacks); **skipped items** are `Skipped`
entries in their folder, so the preview shows them in place.

### Diagnostics and locations

`Diagnostic{Code, Severity, Message, Source}`: `Code` is stable
(`import.*` for the pipeline, `<format>.*` for a format), `Severity` is
`error`, `warning` or `info`, and messages never contain header, body or
credential values. `Location{Line, Column, Path}` says where the thing is
in the file: 1-based line and column (in characters) when the format knows
them, and `Path` in the format's own terms (`command 2`, `item[0].item[3]`,
`paths./users.get`). Every member is optional.

## Preview and report

`importer.Preview(m)` and `importer.Write(...)` return the same `Report`:

- `Items`: the import's own folder first (`Items[0]`, named `m.Name`), then
  its content depth first, then the environments. Each `ReportItem` has
  `Kind` (`folder`, `request`, `environment`, `skipped`), `Name`,
  `Parent` (the index of its folder; -1 for `Items[0]` and the
  environments), `FileKind`, `Method` and `Detail` (a request's method and
  URL or address), `SkippedKind`, `Status`, `NodeID`, `Source` and
  `Diagnostics` (the importer's, then the writer's).
- `Status`: `new` in a preview; `imported`, `skipped` or `failed` after
  `Write`.
- `Counts`: `Folders`, `Requests`, `Environments` created (or to be),
  `Skipped`, `Failed`, `Lossy` (imported items with a warning or error),
  and `Errors`, `Warnings`, `Infos` over every diagnostic.
- `IntoParent`: the items went straight into an existing folder; `Items[0]`
  is that folder (its `NodeID` is `""` for the top level) and is not
  counted.

## Writing

```go
importer.Write(importer.Target{Tree, Secrets, Environments}, model, importer.Options{
	ParentID, Index, FolderName, IntoParent, SkipEnvironments,
})
```

- **Where.** By default a **new folder** in `ParentID` at `Index`
  (`collection.End` for last), named `FolderName` or `m.Name`. When the
  folder already has an item of that name (ignoring case), " 2", " 3"… is
  added (`import.renamed`, info). With `IntoParent` the items go straight
  into `ParentID`; the collection's own settings then have nowhere to go
  (`import.settings_skipped`, warning). A model without a folder or request
  (only environments, or only skipped items) makes no folder.
- **Never overwriting.** Directory and file names come from
  `collection.Slug` and `collection.UniqueName`, so they never replace an
  entry; request and folder IDs are new; environments get free names
  (`import.renamed`). The new folder is created with the tree's
  `CreateFolder`; its content is written directly (each folder's
  `_folder.json` first, then its items, then its `order`), and the tree is
  reloaded once at the end (`Tree.Reload`), so large imports do not reload
  the tree per item.
- **Secrets.** A typed auth secret (Basic password, bearer token, API key,
  JWT signing secret or private key, OAuth 1.0 consumer secret or RSA
  private key and token secret, each on its own, AWS secret key and
  session token, Hawk key, OAuth 2.0 client secret)
  of a request or folder goes to the local side file of its ID
  (`requestsecrets.Split`, [auth.md](auth.md)), never into the project; a
  `{{variable}}` reference stays in the file. Environment secrets go to the
  environments' local secrets file. A folder's **secret variable** keeps
  its value in the folder's side file too (`var.<name>`,
  `requestsecrets.SplitFile`), and every file is written through
  `requestsecrets.Store.MoveOut`, so an import never puts a secret into the
  project ([auth.md](auth.md#how-it-flows)).
- **Failures.** `Write` fails, writing nothing, when the import cannot start
  (the parent is gone or read-only, the new folder cannot be created). After
  that an item that fails is `failed` with `import.write_failed` (or
  `import.invalid_item` for a request of an unknown kind) and `Err` set;
  the items inside a failed folder fail with it; the rest is still written.
- **Environments** are created unless `SkipEnvironments`
  (`import.environments_skipped`, info).

## The service

| Method | Returns |
|---|---|
| `ImportFormats()` | `ImportFormatDTO[]` (`id`, `name`, `extensions`) |
| `PickImportFile()` | the path chosen in the native open dialog, or `""` |
| `DetectImport({paths})` | `DetectedImportDTO[]`, one per path in order: `path`, `fileName`, `format` (`id` `""` when none), `confidence`, `error` (an `ErrorDTO`, or null). The detection of [dropped files](#dropped-files); fails only for no paths, a relative path or more than `MaxDetectFiles` (20, `import.too_many_files`). Writes nothing and needs no open project. |
| `PreviewImport({path, format})` | `ImportReportDTO`: `format`, `path`, `fileName`, `digest` (SHA-256 of the content), `intoParent`, `items`, `diagnostics`, `counts`. Writes nothing and needs no open project. `format` `""` detects it. |
| `Import({path, format, digest, parentId, folderName, intoParent, skipEnvironments, index})` | `ImportResultDTO` (`tree`, `nodeId`: the new folder or the folder the items went into, `report`). Emits `collection:changed` (op `import`), and `environments:changed` when environments were created. |

`ImportItemDTO` and `ImportDiagnosticDTO` carry the report's members;
`source` is `{line, column, path}` (0 and `""` when unknown). Files are
read with `importer.ReadFile`: regular files only, at most 64 MiB
(`import.too_large`); a missing or unreadable file is
`import.read_failed` (kind `not_found`, `permission_denied` or `io`).
`Import` reads the file again and fails with `import.source_changed` when
its digest is not the preview's; the UI previews it again.

| Code | Kind or severity | When |
|---|---|---|
| `import.unknown_format` | unsupported | no importer recognises the file, or an unknown format ID |
| `import.invalid_file` | parse | the file is not valid in its format at all |
| `import.read_failed`, `import.too_large` | not_found / permission_denied / io, invalid_argument | the file cannot be read |
| `import.source_changed` | invalid_argument | the file changed after its preview |
| `import.too_many_files` | invalid_argument | more than 20 files dropped at once (`DetectImport`) |
| `import.empty` | warning | nothing to import |
| `import.renamed` | info | the folder or an environment got a free name |
| `import.settings_skipped` | warning | the collection's settings were left out (`intoParent`) |
| `import.environments_skipped` | info | environments left out on request |
| `import.invalid_item`, `import.write_failed` | error | an item could not be written |

## Dropped files

Files dragged from the OS onto the window are imported (IMP-11) through
the same Import dialog, never silently:

```
OS drop ──Wails (EnableFileDrop)──▶ WindowFilesDropped ──internal/app──▶ collection:files-dropped {paths, target}
   ──UI──▶ DetectImport(paths) ──▶ Import dialog per recognised file (previewed in its detected format)
                                └─▶ toast naming the files that cannot be imported
```

- **Native paths.** The main window has `EnableFileDrop` (Wails
  v3.0.0). Wails takes the drop natively on macOS and Linux, and through
  WebView2's `postMessageWithAdditionalObjects` on Windows, and raises
  `WindowFilesDropped` with the files' real paths when they land on an
  element with a `data-file-drop-target` attribute; drops elsewhere are
  dropped by Wails. `internal/app/filedrop.go` forwards them as
  `collection:files-dropped` (`FilesDroppedDTO`: `paths`, and `target`,
  the attribute's value). The UI never reads file contents in JS.
- **Targets.** The app shell is the target `import`. A drop target inside
  it (a future file field, say) gets its own `data-file-drop-target`
  value; the runtime picks the innermost target under the cursor, so it
  takes precedence and the import ignores drops whose `target` is not
  `import`. Modal dialogs are portals outside the shell: a drop over an
  open dialog does nothing. Drags that carry no files (text, the tree's
  own drags) are not file drops and keep working as before.
- **Detection.** `DetectImport` reads each file with `importer.ReadFile`
  and asks `Registry.Detect`, exactly as `PreviewImport` with format `""`
  would, so the dialog shows the format the drop detected. Nothing is
  fetched: the import then runs as a normal one (an OpenAPI document's
  external `$ref`s are not followed but reported, `openapi.external_ref`, as in [openapi.md](openapi.md#references)).
- **Several files** are imported one after another: one Import dialog per
  recognised file, in the drop's order, titled "Import (2 of 3)"; closing
  one (Cancel or Done) opens the next. Files that cannot be read or whose
  format nobody recognises are listed in one toast ("photo.png: PostSonde
  does not recognise the format of this file."). A drop of more than 20
  files is refused as a whole with `import.too_many_files`.
- A drop needs a writable open project; otherwise a toast says why and
  nothing is read.

## cURL files

`curlimport.Importer` (format `curl`) reads a text file of curl commands:
a shell script, a `.txt` of commands copied from API docs, a batch or
PowerShell file, or one "Copy as cURL" saved to a file. Each command
becomes an http request through `internal/core/curl`
([curl.md](curl.md#import)).

- **Detect**: `Likely` when the first line that is not blank or a comment
  starts a curl command (`curl.StartsCommand`: an optional prompt, then
  `curl` or `curl.exe`, bare or with a path), `Maybe` when one of the next
  20 such lines does (a script that sets variables first); a usual
  extension adds 10. JSON or XML text is `No`.
- **Splitting**: a command starts at a line that starts a curl command,
  outside quotes and not after a line continuation (`\`, `^`, `` ` ``),
  and runs to the next one. Quoting (`'…'`, `"…"`, `$'…'`, backslash
  escapes, `#` comments) is followed across lines, so a line of a quoted
  multi-line body is never a new command. Blank and comment lines at the
  end of a command belong to no command. Other lines before the first
  command are ignored (`curl.not_command`, info, at the first).
- **Names**: a comment line right above a command (`# List users`,
  `// …`, `:: …`, `REM …`; not a shebang) names its request; otherwise
  curl's suggestion (the URL's last path segment, or its host).
- **Diagnostics**: every diagnostic of the parser and the mapping, with
  its line and column in the file and `Path` "command N".
- A command with an **error** (no URL, an unterminated quote…) is a
  skipped item with its first error as the reason.

The Import cURL dialog ([curl.md](curl.md#import)) uses the framework too:
`curlimport.Command(text, name)` makes a one-request model and
`CollectionService.ImportCurl` writes it with `IntoParent` at the chosen
index.

## Insomnia exports

`insomnia.Importer` (format `insomnia`) reads the files Insomnia's
"Export" writes:

- **v4** (`_type: export`, `__export_format: 4`; 3 reads the same): a flat
  `resources` list linked by `parentId`, as JSON, or as YAML with the same
  shape (older Insomnia versions offered both).
- **v5** (Insomnia 11+): YAML files of `type`
  `collection.insomnia.rest/5.0` (the folder tree nested under
  `collection`, then `cookieJar` and `environments`),
  `spec.insomnia.rest/5.0` (a design document: the same plus `spec` and
  `testSuites`), `environment.insomnia.rest/5.0` (environments only) and
  `mock.insomnia.rest/5.0` (skipped). Another minor or major version is
  read as 5 (`insomnia.version`, info).

Both are read into one node tree (JSON through a reader that records the
line and column of every value, YAML through `internal/core/yaml`), so
every item's `Source` has its line, column and `Path`: `resources[12]` for
v4, `collection[0].children[2]` or `environments.subEnvironments[1]` for
v5.

**Detect**: `Sure` for JSON with `"_type": "export"` or
`__export_format`, and for YAML with `_type: export` or a `type:` line of
one of the v5 types; `Likely` for another `*.insomnia.rest/…` type; `No`
otherwise (a Postman or OpenAPI file, a curl script).

### What maps

| Insomnia | PostSonde |
|---|---|
| workspace | the import's folder (`Model.Name`, description). An export with several workspaces makes a folder per workspace. |
| `request_group` / v5 item with `children` | folder, in `metaSortKey` order (v5: file order); its `environment` becomes the folder's variables, `headers` and `authentication` its settings |
| `request` | `http` request; with a body of `mimeType: application/graphql` a `graphql` request (`query`, `variables` pretty-printed, `operationName`, the method when not POST) |
| `grpc_request` / v5 `greq_…` | `grpc` request: `grpcs://`/`https://` in the URL becomes TLS, `protoMethodName` `/pkg.Service/Method` the service and method, `body.text` the message, `metadata` the metadata |
| `websocket_request` / v5 `ws-req_…` | `websocket` request; its first `websocket_payload` is the message being composed (JSON when its mode is), others saved messages |
| `parameters` | the query table; enabled rows are also added to the URL's query (`&`, `#` and `=` in a key encoded, as the editor writes them), after the URL's own entries |
| `pathParameters` | path parameter rows, plus an empty row for every other `:name` in the URL |
| `headers`, `metadata`, form `params` | rows; `disabled` kept; rows without name and value left out |
| body `application/x-www-form-urlencoded`, `multipart/form-data` | URL-encoded fields, multipart parts (a `type: file` part keeps its `fileName`: `insomnia.file_path`, info) |
| body with `fileName` (binary) | file body, the MIME type as its content type |
| body with text | raw body; the type from the MIME type (json, xml, html, yaml, else text). A MIME type PostSonde would not send (e.g. `application/edn`) is added as a Content-Type header when the request has none. |
| `settingFollowRedirects` / `settings.followRedirects` | `on`/`off` become the request's follow-redirects setting, `global` none |
| send/store cookies, encode URL turned off (`settingSendCookies`… / `settings.cookies`, `settings.encodeUrl`) | the request's `sendCookies`/`storeCookies` false and URL encoding `none` (a WebSocket request: `sendCookies`). Turned-off settings PostSonde lacks (rebuild path; store cookies and URL encoding for WebSocket) are an `insomnia.setting_skipped` info. |
| `description` / `meta.description` | description |
| pre-request / after-response scripts (`preRequestScript`, `afterResponseScript`, v5 `scripts`) | kept at the end of the description in a `js` code block, not run (`insomnia.script`, warning) |

**Auth** (requests and folders): `{}` (Insomnia's "Inherit from parent")
is `inherit`; `none`; `basic` and `digest` (username, password;
`useISO88591` is a warning, PostSonde sends UTF-8); `bearer` (an empty
`prefix` is the default `Bearer`, another prefix is kept); `apikey`
(`addTo` header or `queryParams`; `cookie` becomes a `Cookie` header
holding `key=value`, `insomnia.auth_changed`); `oauth1` as an `oauth1`
auth (AUTH-15, [auth.md](auth.md#oauth-10-auth-15)), sent in the
Authorization header as Insomnia does: `consumerKey`, `consumerSecret`,
`tokenKey` (the access token), `tokenSecret`, `signatureMethod`,
`callback`, `verifier`, `realm`, `nonce` and `timestamp` (fixed values;
empty ones are generated on each send) and `includeBodyHash`; with
`RSA-SHA1` the `privateKey` goes in the consumer secret member and the
token secret is dropped (RSA does not use it). Insomnia sends
`oauth_version` 1.0 also when `version` is empty; another version, and a
signature method PostSonde lacks (kept as written, so a send names the
methods to choose), are an `insomnia.auth_changed` warning; `netrc` as a
`netrc` auth (AUTH-19, [auth.md](auth.md#netrc-auth-19)) without
`netrcFile`: Insomnia's has no settings and its curl reads `~/.netrc`,
which PostSonde reads too unless `NETRC` names another file (PostSonde
also sends a user and password written in the URL instead of the file's,
where Insomnia's curl ignores them). `disabled` is
kept. `oauth2` becomes an `oauth2` auth (AUTH-07…10, AUTH-14, [auth.md](auth.md#oauth-20-auth-08-auth-12-auth-33)):
`grantType`, `accessTokenUrl`, `clientId`, `clientSecret`, `scope`,
`state`, `audience`, `resource`, `credentialsInBody` (client credentials
in the body, else the Basic header) and `tokenPrefix` (Insomnia always
sends the token in the Authorization header); `disabled` is kept. The
client credentials grant is imported as it is; the authorization code
grant (Insomnia's default, AUTH-07) with `authorizationUrl`, and
`redirectUrl` when it is on this computer (a loopback `http://` URL;
another is left out with an `insomnia.auth_changed` warning, since
PostSonde receives the code on `http://127.0.0.1:<port>/callback`; a
missing authorization URL is reported too; PostSonde always uses PKCE
S256). The implicit grant (AUTH-10) takes the same two URLs; its
`responseType` `id_token` (Insomnia then sends the ID token) sets
`idToken` (AUTH-14), with a warning when the scope does not ask for
`openid`. The password grant (AUTH-09) takes `username` and `password`
(to the local side file when typed). Tokens Insomnia obtained (`oauth2_token` resources) are
never imported. AWS IAM, Hawk, NTLM, ASAP and other types become
`none` with `insomnia.auth_unsupported` (warning; info when the auth was
turned off).
WebSocket requests have no auth in PostSonde: a WebSocket request's auth
is a warning. Typed secrets go to the local side files as for every
import ([Writing](#writing)).

**Environments**: Insomnia's base environment applies under every
sub-environment, so each sub-environment becomes an environment with the
base's variables first and its own values over them
(`insomnia.base_environment`, info); a base without sub-environments
becomes one environment (named "Base Environment" when unnamed). Several
workspaces prefix the names ("Alpha / Prod"). Values nested in objects or
lists become variables with dotted names (`api.version`, `regions.0`,
`insomnia.nested_value`), as `{{ _.api.version }}` and `{{ _.regions[0] }}`
are converted. Numbers and booleans become their text, null an empty value.
Table environments (`environmentType: kv`, `kvPairData`) keep their rows'
enabled state; a vault `secret` row becomes a secret variable without a
value (the export holds it encrypted: `insomnia.secret_value`, warning). A
**private** environment's variables become secrets, kept on this machine
only (`insomnia.private_environment`, info).

### Templates

Insomnia texts are Nunjucks templates. In every text that is imported
(URL, rows, bodies, auth members, messages, variable values):

- `{{ _.name }}`, `{{ name }}`, `{{ _['my-key'] }}`, `{{ _.a.b }}` and
  `{{ _.list[0] }}` become `{{name}}`, `{{my-key}}`, `{{a.b}}` and
  `{{list.0}}`.
- `{% uuid %}` becomes `{{$uuid}}`; `{% now 'iso-8601' %}` (the default),
  `'millis'` and `'unix'` become `{{$isoTimestamp}}`, `{{$timestampMs}}`
  and `{{$timestamp}}` (`insomnia.template_converted`, info).
- Other tags (`response`, `prompt`, `request`, `cookieJar`, `base64`,
  `hash`, `file`, `faker`, a plugin's tag…) and expressions with filters or
  operators are left as written, with `insomnia.template_tag` naming the
  tag and where it is, or `insomnia.template_expression` (warnings). Their
  arguments are never quoted in messages.

### Skipped

API specs with contents (import the spec with the OpenAPI format), unit
test suites, mock servers, socket.io requests, client and CA certificates
and resource types this version does not know are `Skipped` items in their
folder (`insomnia.unsupported`). Cookie jars are a file-level
`insomnia.cookies_skipped` info (PostSonde keeps the cookies servers set).
A gRPC request's `.proto` file lives inside the export and is not written
out (`insomnia.proto_skipped`, warning, naming the file; also for the Buf
reflection API). Responses, tokens, request versions and other app state
are ignored. In v4, items whose folder is not in the file go to the top
level (`insomnia.orphan`, warning); items in a loop of parent IDs are
skipped with the same code.

| Code | Severity | When |
|---|---|---|
| `insomnia.template_tag`, `insomnia.template_expression` | warning | a template tag or expression left as written |
| `insomnia.template_converted` | info | a `uuid`/`now` tag became a dynamic value |
| `insomnia.auth_unsupported` | warning (info when off) | an auth type PostSonde lacks; WebSocket auth |
| `insomnia.auth_changed` | warning | ISO-8859-1 Basic auth; an API key sent as a cookie; OAuth 1.0 with another version or signature method; OAuth 2.0 with an unknown grant, an authorization code or implicit grant without its authorization URL or with a redirect URL not on this computer, or the implicit grant sending the ID token without `openid` in the scope |
| `insomnia.script` | warning | a script kept in the description |
| `insomnia.setting_skipped` | info | turned-off settings PostSonde lacks (rebuild path) |
| `insomnia.proto_skipped` | warning | a gRPC request's embedded `.proto` file or reflection API |
| `insomnia.file_path` | info | a body sends a file by its path |
| `insomnia.graphql_body` | warning | a GraphQL body that is not Insomnia's JSON, imported as the query |
| `insomnia.unsupported` | warning (skipped) | an item PostSonde does not import |
| `insomnia.cookies_skipped` | info | the cookie jar |
| `insomnia.base_environment`, `insomnia.nested_value`, `insomnia.private_environment` | info | environment mapping notes |
| `insomnia.secret_value` | warning | vault secrets without values |
| `insomnia.orphan` | warning | items without their folder, or in a parent loop |
| `insomnia.version` | info | an export version read as the nearest known one |

## Postman files

`postman.Importer` (format `postman`) reads the JSON files Postman's
"Export" writes:

- **Collections** v2.1 and v2.0 (`info.schema`
  `https://schema.getpostman.com/json/collection/v2.1.0/collection.json`,
  or `schema.postman.com`). A collection without a schema, or with another
  v2 version, is read as v2.1 (`postman.version`, info). v1 collections and
  Postman data dumps (`collections`, `environments` lists) are refused with
  `import.invalid_file` and a message saying to export again as v2.1.
- **Environments** and **globals** (`_postman_variable_scope`
  `environment` / `globals`, a `values` list).

The file is read through the Insomnia importer's JSON reader (a copy in
the package), so every item's `Source` has its line, column and `Path`:
`item[2].item[0]` for a collection's items.

**Detect**: `Sure` for a Postman collection schema URL (v1 included, so its
error is the helpful one) and for `_postman_variable_scope`; `Likely` for
JSON with Postman's export members (`_postman_id`, `_postman_exported_at`…)
and an `info`, `item` or `values` member; `Maybe` for other JSON named
`*.postman_collection.json`, `*.postman_environment.json` or
`*.postman_globals.json`; `No` otherwise (Insomnia and OpenAPI JSON).

### What maps

| Postman | PostSonde |
|---|---|
| the collection (`info.name`, `info.description`, `variable`, `auth`, `event`) | the import's folder: name, description, variables, auth, scripts as notes |
| item group (`item` list) | folder, in file order: `description`, `variable` (the folder's variables), `auth`, `event` |
| item with `request` | `http` request; a body in `graphql` mode makes a `graphql` request (`query`, `variables` as typed, `GET` kept; another method than GET or POST is sent with POST, `postman.graphql_method`, warning) |
| `request` as a string (v2.0/v2.1 shorthand) | a GET request of that URL |
| `url.raw` (else `protocol`, `host`, `port`, `path`, `query`, `hash` put together; v2.0 `{type, value}` path segments too) | the URL. When `url.query` is present it is the query table, disabled rows included, and the URL's query is its enabled rows (`key` alone for a null value) in place of raw's own; else the table is read from the URL |
| `url.variable` | path parameter rows for `:name`, plus an empty row for every other `:name` in the URL |
| `header` (list, or v2.0's text of `Name: value` lines, `//` for a disabled one) | headers; `disabled` and `description` kept |
| body `raw` | raw body, the type from `options.raw.language` (`json`, `xml`, `html`, `text`; `javascript` is text with a `Content-Type: application/javascript` header when the request has none); without a language, from the request's Content-Type header, else text |
| body `urlencoded`, `formdata` | URL-encoded fields, multipart parts (text or file; `contentType` kept; a file part with several `src` becomes a part per file; `postman.file_path`, info) |
| body `file` | file body of `file.src` (`postman.file_path`); `file.content` becomes a raw text body |
| body `disabled` | no body (`postman.body_skipped`, info; an unknown mode is a warning) |
| `description` (text or `{content}`) | description |
| `event` (`prerequest`, `test` scripts) | kept at the end of the description in a `js` code block, not run (`postman.script`, warning; a script turned off says so; empty scripts left out) |
| `response` (saved examples) | the request's response examples (HTTP-38, [file-format.md](file-format.md#response-examples)): `name` (blank: "Example N"), `code` as `statusCode`, `status` as `statusText`, `header` as `headers`, `body` as it is (not converted: `{{…}}` in it is text), `originalRequest` as `request` (method, URL as `raw` or put together, headers, the body mapped as a request's). The credentials of `Authorization` and `Proxy-Authorization` headers are masked. Bodies over 64 KiB are shortened, with `bodyClipped` and `bodySize` (`postman.examples`, info) |
| item `variable` on a request | not imported, PostSonde requests have no variables (`postman.variables_skipped`, warning) |
| `request.proxy`, `request.certificate` | not imported (`postman.setting_skipped`, info) |
| item without `request` or `item`, or not an object | `Skipped` (`postman.unsupported`) |

**Auth** (collection, folders, requests), v2.1's `[{key, value}]` lists and
v2.0's objects alike: no `auth` (or `null`, or `inherit`) is `inherit`, as
Postman's "Inherit auth from parent"; `noauth` is `none`; `basic` and
`digest` (username, password; Digest's realm, nonce and algorithm are
answered from the server's challenge); `bearer` (token); `apikey` (`key`,
`value`, `in` header or query); `jwt` ("JWT Bearer", AUTH-16) as a `jwt`
auth: `algorithm`, `secret` (HS\*) or `privateKey` (the others),
`isSecretBase64Encoded`, `payload`, `header` (an empty `{}` is left out),
and `addTokenTo`: `header` with `headerPrefix` (Postman's default `Bearer`)
or `queryParam` with `queryParamKey` (Postman's default `token`). Postman
has no claim helpers, so none are turned on; a payload's
`{{$timestamp}}` is rendered at send time as in Postman
([auth.md](auth.md#jwt-bearer-auth-16)). `{{variables}}` in auth members stay
references; typed secrets go to the local side files ([Writing](#writing)).

`oauth1` becomes an `oauth1` auth (AUTH-15,
[auth.md](auth.md#oauth-10-auth-15)): `consumerKey`, `consumerSecret`,
`token` (the access token), `tokenSecret`, `signatureMethod` (in upper
case), `callback`, `verifier`, `realm`, `nonce` and `timestamp` (fixed
values; empty ones are generated on each send) and `includeBodyHash`.
With an `RSA-…` method the `privateKey` goes in the consumer secret member
and the token secret is dropped (RSA does not use it). An empty or
missing `version` leaves `oauth_version` out (`omitVersion`), as Postman
does. `addParamsToHeader` sends the parameters in the Authorization
header; without it Postman adds them to the URL, or to the body of a POST
or PUT whose body is URL-encoded: a request's own auth gets `in` `body`
for such a request and `query` otherwise, a folder's or the collection's
auth `query` (a URL-encoded POST or PUT inheriting it then sends them in
the URL, which the server must accept). What PostSonde does differently
is one `postman.auth_changed` warning: another `version` (PostSonde sends
1.0), `addEmptyParamsToSign` (PostSonde leaves an empty token, callback or
verifier out of the request and the signature), `disableHeaderEncoding`
(PostSonde encodes the header's values), and a signature method
PostSonde lacks (`HMAC-SHA512`, `RSA-SHA256`, `RSA-SHA512`), kept as
written so a send names the methods to choose.

`awsv4` becomes an `awsv4` auth (AUTH-17): `accessKey`, `secretKey`,
`sessionToken`, `region`, `service`, and a presigned URL with
`addAuthDataToQuery`. `hawk` becomes a `hawk` auth (AUTH-18): `authId`,
`authKey`, `algorithm`, `extraData` (ext), `app`, `delegation` (dlg),
`includePayloadHash`; Postman's `user` and its fixed `nonce` and
`timestamp` have no PostSonde equivalent (PostSonde makes a new nonce and
timestamp for every send) and are reported (`postman.auth_changed`). The
secret key, session token and Hawk key go to the local side files like
the other typed secrets.

`oauth2` becomes an `oauth2` auth (AUTH-07…10, AUTH-12,
[auth.md](auth.md#oauth-20-auth-08-auth-12-auth-33)): `grant_type`
(`client_credentials`; `authorization_code` and
`authorization_code_with_pkce` as `authorization_code`,
`password_credentials` as `password`, `implicit`; left out, Postman's
default, the authorization code grant), `accessTokenUrl`, `clientId`,
`clientSecret` (to the local side file when typed), `scope`, `state`,
`client_authentication` (`body`, else the Basic header), `addTokenTo`
(`queryParams` sends the token as `access_token` in the URL, else the
Authorization header with `headerPrefix`), and `tokenRequestParams` sent
in the body: `audience` and `resource` become their own fields, the
others extra token parameters (disabled rows kept disabled). The
authorization code and implicit grants (AUTH-07, AUTH-10) also take
`authUrl`, and `redirect_uri` when it is on this computer (a loopback
`http://` URL, or one with `{{variables}}`); the password grant
(AUTH-09) takes `username` and `password` (to the local side file when
typed). What changes is one `postman.auth_changed` warning: an
authorization code or implicit grant without `authUrl`, or whose
`redirect_uri` is a web callback such as Postman's own (left out: PostSonde
receives the redirect on `http://127.0.0.1:<port>/callback`, which the
server must allow); and token parameters sent in a header or the URL (not
imported). PostSonde always sends an S256 challenge, whatever
`challengeAlgorithm` says. The
token Postman obtained (`accessToken`) is never imported.

NTLM, Akamai EdgeGrid, ASAP and other types become `none` with
`postman.auth_unsupported` (warning); their parameters (client secrets,
keys) are dropped, never written.

**Settings**: `protocolProfileBehavior` of the collection, each folder and
the request are merged (the nearer wins) and become the request's settings:
`followRedirects`, `maxRedirects`, `followOriginalHttpMethod`
(follow original method), `followAuthorizationHeader` (forward
Authorization), `strictSSL` (verify TLS), `disableUrlEncoding` (URL
encoding `none`), `disableCookies` (send and store cookies off),
`protocolVersion` `http1`/`http2`. `disableBodyPruning` needs nothing
(PostSonde sends the body of GET, DELETE and custom methods). Other keys
that are set (`insecureHTTPParser`, `tlsDisabledProtocols`,
`disabledSystemHeaders`…) are listed in `postman.setting_skipped` (info).

**Variables**: collection and folder `variable` entries (`key`, v2.0 `id`)
become the folder's variables with `description` and `disabled`; numbers
and booleans become their text. A `secret` variable is a secret one, its
value kept in the local side file. **Environments**: each `values` entry
becomes a variable; `type: secret` a secret variable (its value goes to the
environments' local secrets file), `enabled: false` a disabled one; rows
without a key are left out. **Globals** become an environment named after
the file ("Globals" when unnamed), with `postman.globals` (info): an import
creates environments, not the project's global variables; copy them there
to use them as globals.

### Dynamic variables

Postman's `{{variable}}` syntax is PostSonde's, so texts are imported as
they are, except the dynamic variables:

- `{{$guid}}`, `{{$randomUUID}}`, `{{$timestamp}}`, `{{$isoTimestamp}}`,
  `{{$randomInt}}` (0–1000 in both) and `{{$randomBoolean}}` are PostSonde
  dynamic values of the same name and meaning: unchanged, not reported.
- So are the faker values PostSonde generates from its built-in word lists
  ([templates.md](templates.md#fake-data)): names (`{{$randomFirstName}}`,
  `{{$randomFullName}}`, `{{$randomUserName}}`, `{{$randomJobTitle}}`…),
  internet (`{{$randomEmail}}`, `{{$randomUrl}}`, `{{$randomIP}}`,
  `{{$randomPassword}}`…), places and phone numbers (`{{$randomCity}}`,
  `{{$randomCountryCode}}`, `{{$randomPhoneNumber}}`…), business
  (`{{$randomCompanyName}}`, `{{$randomPrice}}`, `{{$randomCurrencyCode}}`…),
  colours and dates (`{{$randomColor}}`, `{{$randomDatePast}}`…) and words
  and lorem ipsum (`{{$randomWord}}`, `{{$randomLoremSentence}}`,
  `{{$randomLoremParagraphs}}`…). The values differ from Postman's (a
  smaller vocabulary; emails and URLs use reserved `example.*` and `.test`
  domains) but have the same shape.
- `{{$randomAlphaNumeric}}` becomes `{{$randomString 1}}` and
  `{{$randomHexadecimal}}` `{{$randomHex 1}}` (`postman.dynamic_converted`,
  info).
- Every other one (`{{$randomAvatarImage}}`, `{{$randomBankAccountIban}}`,
  `{{$randomCatchPhrase}}`…) is left as written, with
  `postman.dynamic_unsupported` naming it and where it is (warning).
- `{{vault:name}}` references to Postman Vault are left as written
  (`postman.vault`, warning).

| Code | Severity | When |
|---|---|---|
| `postman.script` | warning | a script kept in the description |
| `postman.examples` | info | the body of a saved example was over 64 KiB and was shortened |
| `postman.dynamic_converted` | info | a dynamic variable spelled another way |
| `postman.dynamic_unsupported`, `postman.vault` | warning | a dynamic variable or vault reference left as written |
| `postman.auth_unsupported` | warning | an auth type PostSonde lacks |
| `postman.auth_changed` | warning | OAuth 1.0 options PostSonde does not have (another version, signing empty parameters, header values not encoded, a signature method PostSonde lacks); Hawk's user, fixed nonce or timestamp; OAuth 2.0 with an unknown grant, an authorization code or implicit grant without `authUrl` or with a web `redirect_uri`, or token parameters sent in a header or the URL |
| `postman.setting_skipped` | info | settings PostSonde lacks (protocolProfileBehavior keys, proxy, certificate) |
| `postman.file_path` | info | a body sends a file by its path |
| `postman.graphql_method` | warning | a GraphQL request with a method other than GET or POST |
| `postman.body_skipped` | info (warning for an unknown mode) | a body not imported |
| `postman.variables_skipped` | warning | variables on a request |
| `postman.unsupported` | warning (skipped) | an item that is not a request or folder |
| `postman.globals` | info | globals imported as an environment |
| `postman.version` | info | a collection without a v2.0/v2.1 schema, read as v2.1 |

## Postman export

EXP-03: a folder, or the whole project, is written as a **Postman
Collection v2.1** file (`info.schema`
`https://schema.getpostman.com/json/collection/v2.1.0/collection.json`)
that Postman imports, with a report of what was left out or changed.

| Piece | Where |
|---|---|
| The converter: the import model (`importer.Folder`) → collection JSON and notes | `postman.Export` (`internal/core/importer/postman/exporter.go`) |
| A folder or the project → the model, save dialog, file | `CollectionService.ExportPostman({id})` (`internal/app/services/collection/export_postman.go`) |
| UI | "Export as Postman collection…" on a folder, "Export project as Postman collection…" on the tree's empty area ([frontend.md](frontend.md#export-as-postman-collection)) |

The export works on the **import model**, so the converter is the
importer's mirror and a round trip can be compared model to model.

**The service** writes pending edits first, then reads the files as the
project has them: a typed auth secret or a secret variable's value lives
in the local side files, which the export never reads. The exported
folder's name, description, variables and auth are the collection's; what
its requests inherit from the folders above and the project is added
(their variables, a nearer one winning; their headers; their auth when
the folder's own is inherit), with an info note. Exporting the project
uses its name, description and variables. The native save dialog suggests
`<name>.postman_collection.json`; the file is written atomically; a cancel
writes nothing (`saved: false`, the notes still returned). `_postman_id`
is derived from the folder's ID (a UUID in the version 5 layout, from
SHA-256), so exporting the same folder again gives the same collection
ID. Environments are not part of it (EXP-05).

### What maps on export

| PostSonde | Postman |
|---|---|
| folder: name, description, variables, auth | item group: `name`, `description`, `variable`, `auth` |
| folder headers | Postman folders have none: added to each HTTP and GraphQL request below (a nearer level's header of the same name wins, as a send does; disabled rows left out), info |
| secret variable | `"type": "secret"` with an empty value (a `{{reference}}` is kept), info |
| HTTP request: method, URL | `request.method`; `url` with `raw` as typed and its parts: `protocol`, `host` and `path` split outside `{{…}}`, `port`, `hash` |
| query table (disabled rows too) | `url.query`; a parameter written without `=` has a `null` value |
| path parameters | `url.variable`. A `{name}` placeholder is kept, with a warning: Postman fills `:name` only |
| headers (disabled, descriptions) | `header` (`disabled: true`, `description`) |
| raw JSON, XML, HTML, text | `mode: raw` with `options.raw.language` |
| raw YAML | `mode: raw` without a language, and a `Content-Type: application/yaml` header (info) |
| URL-encoded, multipart (text and file parts, content type, disabled) | `urlencoded`, `formdata` (`type: file` with `src`: the path as written, info) |
| file body | `mode: file`, `file.src`; its content type as a `Content-Type` header |
| named bodies (HTTP-17) | the active one only, warning |
| GraphQL request | `body.mode: graphql` with `query` and `variables`; GET stays GET, the default is POST. The operation name, extensions, uploaded files, connection params and schema file are not exported (warning) |
| auth inherit / none | no `auth` (Postman inherits) / `noauth` |
| basic, digest, bearer, API key (header or query) | the same types. A bearer prefix other than Bearer is lost (warning) |
| JWT | `jwt`: algorithm, `secret` (HS*) or `privateKey`, `isSecretBase64Encoded`, `payload`, `header`, `addTokenTo` header (with `headerPrefix`) or `queryParam` (with `queryParamKey`). A header other than Authorization, a key file and the `iat`/`nbf`/`jti`/`exp` claim helpers are lost (warning) |
| OAuth 1.0 | `oauth1`: consumer key and secret (`privateKey` for RSA), token and token secret, signature method, timestamp, nonce, version (empty for "leave out oauth_version"), realm, callback, verifier, `includeBodyHash`, `addParamsToHeader`. Parameters sent in the body or the URL where Postman would put them elsewhere (it uses the body only for a POST or PUT with a URL-encoded body): warning |
| AWS Signature v4 | `awsv4`: `accessKey`, `secretKey`, `region`, `service`, `sessionToken`, `addAuthDataToQuery` (a presigned URL). UNSIGNED-PAYLOAD and a presigned URL's validity other than the default are lost (warning) |
| Hawk | `hawk`: `authId`, `authKey`, `algorithm` (sha256 when empty), `extraData` (ext), `app`, `delegation` (dlg), `includePayloadHash` |
| OAuth 2.0 | `oauth2`: `grant_type` (`client_credentials`, `authorization_code_with_pkce` with `authUrl`, `challengeAlgorithm` S256 and `redirect_uri` when set, `password_credentials` with `username` and `password` (a secret: a `{{variable}}` or empty), `implicit` with `authUrl` and `redirect_uri`), `accessTokenUrl`, `clientId`, `clientSecret`, `scope`, `state`, `client_authentication` (`header` or `body`), `addTokenTo` `header` (with `headerPrefix`) or `queryParams`, and `tokenRequestParams` sent as `request_body` (audience, resource, then the extra parameters, disabled ones with `enabled: false`). A header other than Authorization or a query parameter other than access_token is lost (warning), and so are sending the ID token and the expected issuer, which Postman's collection has no member for. No token is exported |
| netrc (Postman has no equivalent), an auth turned off, a type of a newer PostSonde | `noauth`, warning |
| settings: follow redirects, max redirects, keep the method, forward Authorization, verify TLS, no URL encoding, HTTP version, neither send nor store cookies | `protocolProfileBehavior`: `followRedirects`, `maxRedirects`, `followOriginalHttpMethod`, `followAuthorizationHeader`, `strictSSL`, `disableUrlEncoding`, `protocolVersion`, `disableCookies` |
| timeout, RFC 3986 encoding, only one of the cookie switches, headers left out, GraphQL transports, the GraphQL schema auto-refresh | not exported, warning |
| assertions | not exported (Postman uses test scripts), warning |
| response examples (HTTP-38) | `response`: `name`, `originalRequest` (method, `header`, `body` as a request's, `url` object), `status` (reason phrase), `code`, `_postman_previewlanguage` (from the Content-Type), `header`, `cookie` (empty), `body`. Credentials of `Authorization` headers masked. A Base64 (binary) body is left out (warning: Postman keeps text); a clipped one is exported as kept (info); `savedAt` has no Postman member |
| descriptions | `description` as written (Markdown) |
| gRPC, WebSocket, Socket.IO, TCP and SSE requests, files of a newer PostSonde or that did not load | left out, listed as skipped (warning) |

A request without response examples has an empty `response` list.
A type PostSonde does not know is exported as `noauth` with a warning.

**Secrets.** An auth secret member (password, token, API key, JWT key,
consumer and token secrets, AWS secret key and session token, Hawk key, OAuth 2.0 client secret) is written only when it is a `{{variable}}`
reference (`requestsecrets.IsReference`); a secret kept on this computer,
or any other text, is written empty with an info note. Nothing reads the
side files, so no value can leak through the export.

**Templates** are written as they are: PostSonde's `{{name}}` is
Postman's. What Postman lacks is reported per item, naming the places
(never values): PostSonde's own dynamic values (`{{$uuid}}`,
`{{$randomString 8}}`, `{{$timestampMs}}`, `{{$datetime …}}`…), filter
pipes (`{{token | base64}}`) and `{{$prompt …}}`. Dynamic values Postman
has under the same name (`{{$guid}}`, `{{$timestamp}}`, the fake data) are
not reported. Dot notation (`{{auth.token}}`) is written as it is; Postman
reads it as a variable named `auth.token`.

### Export report

Each note has the item's path (names from the exported folder down), its
kind, a code, a severity and a message; skipped items are marked. The UI
shows them grouped by item.

| Code | Severity | When |
|---|---|---|
| `postman.export_skipped` | warning (skipped) | an item Postman collections cannot hold |
| `postman.export_dynamic` | warning | a dynamic value, filter pipe or prompt Postman lacks, written as it is |
| `postman.export_assertions` | warning | a request's assertions |
| `postman.export_extractions` | warning | a request's extraction rules ([extraction.md](extraction.md)) |
| `postman.export_auth` | warning | an auth exported with a change, or as no auth |
| `postman.export_secret` | info | a secret exported empty |
| `postman.export_setting` | warning | a setting Postman lacks |
| `postman.export_changed` | info or warning | folder headers moved into the requests, a Content-Type header added, file paths, named bodies, GraphQL options, `{name}` path parameters, a folder's saved server or unreadable settings, the settings inherited from above, response examples with a binary or clipped body |

| Error code | Kind | When |
|---|---|---|
| `collection.not_found` | not_found | the ID is not a folder of the open project |
| `collection.export_failed` | io, not_found, permission_denied | the file could not be written (details: `path`) |
| `collection.export_unavailable` | unsupported | no save dialog (tests, headless) |

### Round trip

A collection exported and imported again with the Postman importer gives
the same folders and requests for everything in the table above that is
not marked as lost. Differences by design: folder headers come back as
request headers, a YAML body gets an explicit Content-Type header, an
empty raw body comes back as no body, an empty JWT algorithm comes back as
HS256, an empty OAuth 1.0 signature method as HMAC-SHA1 and an empty
Hawk algorithm as sha256 (the same defaults), a request without a query table gets one from its URL, and the
secrets kept on this computer come back empty. Response examples come
back with their names, status, headers, text bodies and requests; their
`savedAt`, a binary body and the size of a clipped one do not.

**Schema check.** Postman's importer cannot run here, so the tests
validate every exported file with PostSonde's JSON Schema validator
([jsonschema.md](jsonschema.md)) against
`internal/core/importer/postman/testdata/collection-v2.1.0.schema.json`, a
copy of the published v2.1.0 schema's structure written for the tests
(not downloaded). Two enums are wider than the published file, as
Postman's app writes them: the auth type `jwt` and the variable type
`secret`.

## Folder zips

EXP-02: a folder, with its subfolders and requests, is saved as a **zip
of native project files** ([file-format.md](file-format.md)) to share or
back up, and imported back as a new folder. Secrets are never in it.

| Piece | Where |
|---|---|
| Export (a loaded folder node → zip and notes) and import (format `postsonde-folder`: zip → `importer.Model`) | `internal/core/importer/projectzip` (`Export`, `Importer`) |
| A folder → save dialog, file | `CollectionService.ExportFolderZip({id})` (`internal/app/services/collection/export_zip.go`) |
| Import | the normal pipeline: Import dialog, `PreviewImport`/`Import`, and [dropped files](#dropped-files) (detected as `postsonde-folder`) |
| UI | "Export as zip…" on a folder ([frontend.md](frontend.md#export-as-zip)) |

**The zip** holds one top-level directory, the folder as it is on disk
(same entry names, `_folder.json` with its `order`, request files,
subfolders), and a manifest `_export.json` in it:

```
users/
  _export.json        {"format": "postsonde-folder", "formatVersion": 1, "schemaVersion": 1, "name": "Users"}
  _folder.json
  get-user.json
  admins/
    _folder.json
    list-admins.json
```

The manifest starts with `_`, so a zip unpacked by hand into a project
is a plain folder (give its files new IDs, or let PostSonde do it,
[collection.md](collection.md#ids)). Files are written with
`projectfile.Marshal` (current schema version, stable member order);
entry names that a zip cannot hold safely are replaced by the item's
slug.

**What is in it.** Everything the folder's files hold: every request kind
(HTTP, GraphQL, gRPC, WebSocket, Socket.IO, SSE, TCP), the folder's and
subfolders' description, variables, headers, auth, gRPC proto setup,
saved server and unknown members, request notes, assertions, extraction
rules and response examples (masked when they were saved). **Not in it:**
what the folder inherits from the folders above and the project (global
variables included), environments (EXP-05), the files that file paths
point at (fixtures, `.proto` files; the paths are kept as written),
directories and files that are not part of the tree, and everything in
the local side files.

**Secrets.** The export reads the files as the project has them, after
pending edits are written, and never reads the local side files, so
secrets kept on this computer (typed auth secrets, secret variables'
values) cannot be in the zip. A secret typed into a project file by hand,
which PostSonde moves out only on the file's next save, is left out with
`requestsecrets.SplitFile`, exactly as a save would; the secret member
keeps its "stored locally" mark, so whoever imports it sees that a value
is missing. The result counts them (`secretsRemoved`) and the toast says
so. Files whose secrets cannot be checked are left out with a warning: a
request of a kind this version does not know, a file of a newer
PostSonde, a file that did not load. A folder whose `_folder.json` did
not load or is newer is written with its name only (warning).

**Import** runs on the zip in memory: nothing is extracted, and
`importer.Write` chooses every name on disk, gives every item a new ID and
moves any secret value a hand-made zip holds to the local side files.
Before anything is read the whole zip is checked; one bad entry refuses
it, importing nothing:

| Check | Error |
|---|---|
| A name that is absolute (`/…`, `C:…`), has `..`, `.` or empty elements, a backslash, a control character, is not UTF-8 or is longer than 1024 bytes; folders nested deeper than `collection.MaxDepth` | `projectzip.unsafe_entry` (invalid_argument), details `entry`, `reason` |
| A symbolic link, device, pipe or socket; a directory not marked as one; an encrypted entry; a name twice, or both a file and a directory | `projectzip.unsafe_entry` |
| More than 20,000 entries; a file larger than 32 MiB or all files larger than 256 MiB uncompressed. The declared sizes are checked first, and reading stops at the limits whatever the zip declares (a size that lies is reported as damage by `archive/zip`) | `projectzip.too_large` (invalid_argument), details `limit` (`entry`) |
| A manifest `formatVersion` newer than 1, or a `schemaVersion` newer than this build's | `projectzip.newer_format` (unsupported), details `version`, `supported` |
| Not a zip, damaged, an unknown compression method, no manifest or another `format`, no `_folder.json`, more than one top-level entry, a file outside the top-level directory | `import.invalid_file` (parse) |

The zip file itself is at most 64 MiB, as every import file. An export
that would break these limits fails with `projectzip.too_large`, so every
zip PostSonde writes can be imported again.

Inside an accepted zip, the tree is read as `collection.Load` reads a
project: directories with `_folder.json` are folders, `.json` files with a
request `kind` are requests, in the folder's order (then by name).
Reported, not fatal: a request of an unknown kind, of a newer schema
version, that does not load, or a file of another kind (an environment)
is a skipped item (`projectzip.skipped`, warning); a folder whose
`_folder.json` does not load or is newer comes without its settings
(`projectzip.settings_skipped`, warning); other files (`.json` without a
`kind`, fixtures, directories without `_folder.json`) are counted in one
`projectzip.ignored` info.

**Round trip.** A folder exported and imported again has the same
folders, requests, settings and order; what differs by design: IDs and
file names are new (names are the items' slugs), the import gets a free
name ("Users 2") and the secrets are not there.

| Export code | Severity | When |
|---|---|---|
| `projectzip.export_secret` | info | a secret typed into a file was left out |
| `projectzip.export_skipped` | warning (skipped) | a request of an unknown kind or a newer PostSonde, a file that did not load |
| `projectzip.export_settings` | warning | a folder's `_folder.json` did not load or is newer |

Errors: `collection.not_found` (not a folder of the open project),
`projectzip.too_large`, `collection.export_failed`,
`collection.export_unavailable`, as for the [Postman export](#export-report).

## OpenAPI documents

`openapiimport.Importer` (format `openapi`, "OpenAPI 3.0 / 3.1, Swagger
2.0") turns an OpenAPI 3.0 or 3.1 document, or a Swagger 2.0 one (JSON or
YAML, one file; only local `$ref`s are followed), into a folder of
ready-to-send requests. The document is read by `internal/core/openapi`
([openapi.md](openapi.md)); the mapping is
`openapiimport.FromDocument(doc, diags)`, and the example generator
(`SchemaExample`, `MediaTypeExample`, `XMLExample`, `SchemaNames`) is
exported for mock responses.

**Swagger 2.0** (IMP-06) goes through the same mapping: the reader
converts it to the OpenAPI 3.0 model ([openapi.md](openapi.md#swagger-20)):
`host` + `basePath` + `schemes` become the servers (so `baseUrl` is
`https://host/basePath`, the other schemes in its description), `consumes`
and `produces` the body and `Accept` media types, `body` parameters the
request body, `formData` parameters a URL-encoded form or, with a
`type: file` parameter, a multipart body with a file part,
`collectionFormat` the query serialisation (`multi` one row per value,
`csv`/`ssv`/`pipes` one joined value), `basic` security Basic auth,
`apiKey` API key auth and `oauth2` Bearer auth (`openapi.auth_changed`,
as for 3.x). Diagnostics and request sources point into the Swagger file
(lines, columns and pointers such as `#/definitions/Pet`).

**Detect**: `Sure` for `openapi: 3.x` or `swagger: "2.0"` at the top
level of a YAML file (a nested `openapi:`, as in an Insomnia design
document, does not count) or `"openapi": "3.…"` / `"swagger": "2.0"` in
the start of a JSON file; `Maybe` for another `openapi` or `swagger`
version, so the reader's error says why it cannot be imported
(`openapi.unsupported_version`); `No` otherwise. A file that is not an
OpenAPI 3.x or Swagger 2.0 document fails with `import.invalid_file`
carrying the reader's message, line and column.

### What maps

| OpenAPI | PostSonde |
|---|---|
| `info.title` | the import's folder name (`Model.Name`); `info.summary`, `description` and `externalDocs` its description |
| `servers[0]` | the collection variable `baseUrl` (trailing `/` removed); its `{variables}` become `{{variables}}` in it and collection variables of their own with their default (the description lists an `enum`). Further servers are listed in `baseUrl`'s description (`openapi.servers`, info). No server (or only `/`) leaves `baseUrl` empty (`openapi.no_server`, warning); a relative URL (`/v1`) is kept (`openapi.relative_server`, warning). Collection variables win over environments, so set `baseUrl` in the collection. |
| `tags` | one folder per tag used, in the order of the document's `tags`, then of first use, with the tag's description; unused tags make no folder |
| operation | an `http` request in its first tag's folder (other tags: `openapi.tags`, info); untagged operations after the folders. Name: `summary`, else `operationId`, else `GET /path` (whitespace collapsed, at most 120 bytes). Description: `**Deprecated.**`, `description`, `externalDocs`, the request body's description. |
| path | URL `{{baseUrl}}/pets/{petId}`: the `{name}` placeholders are PostSonde path parameters ([http-client.md](http-client.md)). An operation or path item with its own `servers` uses that server's URL instead of `{{baseUrl}}` (`openapi.servers`, info). A path parameter name PostSonde does not see as a placeholder is `openapi.path_placeholder` (warning). |
| path parameters | path parameter rows, the value serialised by `style` (`simple`, `label` `.a.b`, `matrix` `;id=1`) |
| query parameters | query rows serialised by `style`/`explode` (`form` one row per item or member, `spaceDelimited`, `pipeDelimited`, `deepObject` `filter[color]`); **required** parameters are enabled (and in the URL), optional ones disabled rows, so the request sends only what it must |
| header parameters | header rows, enabled when required. `Accept`, `Content-Type` and `Authorization` are ignored, as the specification says, and a header or query parameter the API key auth already sends is left out (`openapi.parameter_ignored`, info). |
| cookie parameters | one `Cookie` header `a=1; b=2`, enabled when one is required (`openapi.cookie_parameter`, info) |
| parameter values | `example`, the first `examples` value, a `content` parameter's example (as compact JSON), the schema's `example`/`examples`/`const`/`default`/first `enum` (or its items'); a `uuid` or `date-time` without one gets `{{$uuid}}` or `{{$isoTimestamp}}`; else empty (a path parameter then asks for its value when sent) |
| `requestBody` | one body, of the preferred media type: JSON (`application/json`, `*+json`), then form, multipart, XML, `text/plain`, YAML, binary, others; the rest are listed (`openapi.media_type`, info). A media type PostSonde would not send by itself (`application/merge-patch+json`, a `charset`) is added as a `Content-Type` header. |
| JSON, YAML, text bodies | raw body with the example, indented; a string example is used as the text |
| XML bodies | raw XML: a string example as it is, else the example rendered as XML following the schema's `xml` hints (`name`, `attribute`, `wrapped`, `namespace`, `prefix`; the root is `xml.name`, else the component's name) |
| form bodies | URL-encoded fields from the example object; arrays one field per item unless the property's `encoding` says `explode: false`; objects as JSON |
| multipart bodies | parts: a file part (no path yet, `openapi.file`, info) for `format: binary` properties (3.1: `contentMediaType`/`contentEncoding`), text parts for the rest, objects and arrays as JSON with `application/json`; `encoding.contentType` becomes the part's content type |
| binary bodies (`application/octet-stream`, images, PDF…) | a file body with the media type as its content type; choose the file (`openapi.file`, info) |
| responses | an `Accept` header with the first success (else `default`) response's first media type, unless it is a wildcard |
| `callbacks`, `webhooks` | not imported: requests the API sends (`openapi.callback` on the request, `openapi.webhooks` on the file, info) |

**Examples.** A body uses the media type's `example`, else its first
`examples` entry with a `value` (which one, when there are several:
`openapi.example`, info; examples that are only `externalValue` are not
fetched and the body is generated, same code), else an example generated
from the schema:

- The schema's own `example`, first `examples` (3.1), `const`, `default`
  or first non-null `enum` value wins.
- `allOf` members are merged (properties in order, a later member
  overriding one of the same name; a `false` property schema removes it);
  `oneOf`/`anyOf` take the first alternative that is not `null`; 3.1 type
  arrays use their first non-null type; a schema that only wraps one
  other (a 3.1 `$ref` with siblings) is that schema.
- Objects hold every property (request bodies leave out `readOnly` ones,
  response examples `writeOnly` ones); `additionalProperties` alone gives
  `additionalProp1`. A discriminator property gets the mapping key of the
  schema being generated (else its component name). Arrays hold one item
  (or their `prefixItems`).
- Strings follow `format` (`date-time`, `date`, `email`, `uuid`, `uri`,
  `ipv4`…) and `minLength`/`maxLength`; numbers are 0 or the nearest value
  the bounds (`minimum`, `exclusiveMinimum`, `maximum`, `multipleOf`)
  allow; booleans are `true`.
- **Recursive schemas** are walked with the set of schemas on the current
  path: a property that leads back into a schema being generated is left
  out and its array left empty, so a tree node gives
  `{"name": "string", "children": []}`. Nesting stops at 8 levels and 2000
  values in all (so shared, non-recursive schemas cannot blow up either).
  Both are reported (`openapi.example_truncated`, info).

**Auth.** The document's `security` becomes the collection folder's auth
(requests and tag folders inherit it); an operation with its own
`security` gets its own auth (`security: []` is `none`). Of the
alternatives, the first whose schemes all map directly is used, else the
first whose first scheme maps at all; an empty alternative `{}` chosen
that way is `none`. Credentials are **variables of the collection
folder**, named after the scheme: `<scheme>.username` and the secret
`<scheme>.password` (Basic, Digest), `<scheme>.token` (Bearer, OAuth 2.0,
OpenID Connect) and `<scheme>.key` (API keys). The secret ones have no
value, so what the user types stays on this computer, and an environment
can supply them (a secret without a value falls through to lower scopes,
[templates.md](templates.md)).

| Scheme | Auth |
|---|---|
| `http` `basic`, `digest` | Basic / Digest with `{{<scheme>.username}}`, `{{<scheme>.password}}` |
| `http` `bearer` | Bearer with `{{<scheme>.token}}` (`bearerFormat` in the variable's description) |
| `apiKey` in `header` / `query` | API key auth with the scheme's `name` |
| `apiKey` in `cookie` | an API key header `Cookie: <name>={{<scheme>.key}}` (`openapi.auth_changed`, warning) |
| `oauth2`, `openIdConnect` | Bearer with `{{<scheme>.token}}`; PostSonde runs no OAuth flow, so the warning names the scopes and the token URL or discovery document to get a token from (`openapi.auth_changed`) |
| `mutualTLS`, other `http` schemes, unknown schemes | `none` (`openapi.auth_unsupported`, warning; for mutual TLS: set up a client certificate) |
| several schemes in one requirement | the first is the auth; further API key headers become header rows; others are `openapi.auth_extra` (warning) |

**Reader diagnostics** (`openapi.bad_ref`, `openapi.external_ref`,
`openapi.path_parameter`…, [openapi.md](openapi.md#diagnostics)) keep
their codes and positions (`Path` is the JSON pointer): they go to the
request of the operation they are in, else to the file.

Every request's `Source` is the operation's line and column and
`paths./pets/{petId}.get`; a tag folder's is its entry in `tags`.

| Code | Severity | When |
|---|---|---|
| `openapi.no_server`, `openapi.relative_server` | warning | no usable server URL for `baseUrl` |
| `openapi.servers` | info | more servers than the first; an operation's own server |
| `openapi.auth_unsupported` | warning | a scheme without PostSonde auth; no auth is sent |
| `openapi.auth_changed` | warning | OAuth 2.0 / OpenID Connect as Bearer; a cookie API key as a `Cookie` header |
| `openapi.auth_extra` | warning | a requirement's further schemes not set up |
| `openapi.parameter_ignored` | info | `Accept`/`Content-Type`/`Authorization` parameters; parameters the auth sends |
| `openapi.cookie_parameter` | info | cookie parameters became a `Cookie` header |
| `openapi.path_placeholder` | warning | a path parameter name PostSonde does not fill in |
| `openapi.media_type` | info | other media types of the body; a multipart type other than `form-data` |
| `openapi.example` | info | which named example the body uses; external examples |
| `openapi.example_truncated` | info | the generated example stops at recursion or a limit |
| `openapi.file` | info | a file body or file parts to choose |
| `openapi.callback`, `openapi.webhooks` | info | requests the API sends, not imported |
| `openapi.tags` | info | an operation with several tags |

## Altair exports

`altair.Importer` (format `altair`) reads the JSON files the Altair
GraphQL Client exports. Altair has written `"version": 1` in every export
since its first; the members changed between Altair versions (4 to 8), so
every member is optional and read when present. Another `version` is read
as 1 (`altair.version`, info).

- **Window** (`"type": "window"`, `.agq`): one request. The import's
  folder is named after the window and holds it.
- **Collection** (`"type": "collection"`, `.agc`): `title`,
  `description`, `queries` (windows) and sub-collections. Newer Altair
  versions nest sub-collections in `collections`, each with a
  `parentPath` of its ancestors' IDs (`/1/4`); a sub-collection listed
  flat, whose `parentPath` names another collection of the file, goes into
  that one. A `parentPath` naming a collection that is not in the file, or
  making a loop, leaves the sub-collection where it is listed
  (`altair.orphan`, warning).
- **Environment** (`"type": "environment"`, or Altair's own state with
  `title` and `variablesJson`): one environment named after `title`. Text
  values are kept, numbers and booleans become their text, `null` an empty
  value, and objects and lists their compact JSON, so `{{auth.token}}`
  still reaches into them ([dot notation](templates.md#dot-notation)).

The file is read through the Insomnia importer's JSON reader (a copy in
the package), so every item's `Source` has its line, column and `Path`:
`collections[0].queries[1]`.

**Detect**: `Sure` for JSON with `"type": "window"` and a window's members
(`windowName`, `apiUrl`, `subscriptionUrl`…), `"type": "collection"` and
`queries`, or `"type": "environment"` and variables; `Likely` for a
collection without `type` (queries and a window's members) and for
Altair's environment state (`title`, `variablesJson`); `Maybe` for other
JSON named `*.agq` or `*.agc`; `No` otherwise (Postman, Insomnia, HAR,
OpenAPI).

### What maps

Altair's `{{variable}}` syntax is PostSonde's (whitespace inside the braces
included), so every text is imported as it is.

| Altair | PostSonde |
|---|---|
| collection | the import's folder (`title`, `description`); sub-collections become folders, before the collection's queries |
| window / query | `graphql` request, named `windowName` (else the query's first operation name, else "Untitled") |
| `apiUrl` | the endpoint |
| `query`, `selectedOperation` | the query, the operation name |
| `variables`, `requestExtensions` (GQL-06), `subscriptionConnectionParams` (GQL-33) | the Variables, Extensions and Connection texts as typed; Altair's default `{}` is left out, and an object in the file is written as indented JSON |
| `headers` (`key`, `value`, `enabled`) | headers; `enabled: false` is a disabled row; the editor's empty row is left out |
| `httpVerb` / `selectedHttpVerb` | `GET` is kept; another method than GET or POST is sent with POST (`altair.method`, warning) |
| `subscriptionUrl` | nothing when it is the endpoint, or the endpoint with `ws://`/`wss://`, which PostSonde opens for subscriptions ([graphql-subscriptions.md](graphql-subscriptions.md)); another URL is kept in the description (`altair.subscription_url`, warning) |
| `requestHandlerId`, `subscriptionRequestHandlerId`, older `subscriptionProvider` | queries go over HTTP and subscriptions over WebSocket with `graphql-transport-ws`: another query handler is `altair.transport` (warning), the legacy `websocket` (subscriptions-transport-ws) subscription handler an info, AppSync, Action Cable, Socket.IO and SSE warnings |
| `fileVariables` (`name`, `isMultiple`, `data`) | rows of the request's Files tab ([file uploads](graphql.md#file-uploads-gql-07)) bound where Altair binds them: `variables.<name>`, or `variables.<name>.0`, `.1`… for `isMultiple`, one per entry of `data` (at least one). Their files are left to choose, since `data` is a browser file, exported as `{}` (`altair.files`, info); a row without a name is left out |
| `preRequestScript`, `postRequestScript` (and their `…Enabled`), a collection's `preRequest`/`postRequest` | kept at the end of the description in a `js` code block, not run (`altair.script`, warning; a script turned off says so; empty scripts left out) |
| `gqlSchema`, `requestHandlerAdditionalParams`, IDs and timestamps | ignored |

**Auth** (`authorizationType`, `authorizationData`): none and `none`
(Altair's default for every window) are `inherit`, since Altair has no auth
above a window; `basic` (`username`, `password`); `bearer` (`token`);
`api-key` (`headerName`, `headerValue`) as an API key header. OAuth 2.0
and other types become `none` with `altair.auth_unsupported` (warning);
their data (tokens, client secrets) is dropped, never written. Typed
secrets go to the local side files ([Writing](#writing)).

Entries of `queries` or `collections` that are not objects, and
sub-collections nested more than 64 levels deep, are `Skipped`
(`altair.unsupported`).

| Code | Severity | When |
|---|---|---|
| `altair.script` | warning | a script kept in the description |
| `altair.auth_unsupported` | warning | an auth type PostSonde lacks |
| `altair.subscription_url` | warning | a subscription URL PostSonde does not derive from the endpoint |
| `altair.transport` | warning (info for subscriptions-transport-ws) | a request or subscription handler PostSonde does not have |
| `altair.files` | info | file variables imported as upload rows whose files are to choose |
| `altair.method` | warning | a method other than GET or POST |
| `altair.orphan` | warning | a sub-collection whose `parentPath` is not in the file or loops |
| `altair.unsupported` | warning (skipped) | an entry that is not an object; environment variables that are not an object |
| `altair.version` | info | an export version other than 1 |

## PostSonde environments

`postsondeenv` (format `postsonde-environment`) reads a PostSonde
environment file: what **Export…** writes for an environment
([environments.md](environments.md#export)), or a file of a project's
`environments/` folder. Detection is `Sure` for a JSON object with
`"kind": "environment"` and a `schemaVersion` (files over 4 MiB are not
looked at); anything else is `No`. The environment's name (else the
file's) and its variables become a new environment of the open project;
its ID and colour are not imported, so the import never clashes with an
environment the project has. A secret variable stays secret: it arrives
without a value (an export never has one), and a value typed into the
file by hand goes to the local side files, like every imported secret. A
file from a newer PostSonde is imported as far as this version knows it
(`postsondeenv.newer`, info).

## Tests

- `internal/core/importer/postsondeenv/postsondeenv_test.go`: detection,
  an export read back, a newer file, a file of another kind.
- `internal/core/importer/importer_test.go`: a test importer with nested
  folders, folder settings, typed secrets, a secret folder variable, a
  skipped item and environments goes through detection, preview and
  `Write` into a temporary project (new folder with a name collision,
  `IntoParent`, skipped environments, failing items, unknown parents);
  project files are checked for secrets.
- `internal/core/importer/curlimport/curlimport_test.go`: detection,
  splitting (continuations, multi-line quoted bodies, comments as names,
  pipes), locations in the file, skipped commands, `StartsCommand`.
- `internal/core/importer/insomnia/insomnia_test.go`: golden files of the
  parsed model (`testdata/*.golden`, rewrite with `-update`) for a v4 JSON
  export with nested folders, every request kind, every auth type, template
  tags, sub-environments (private, vault) and a cookie jar; a v5 collection;
  a v4 YAML export; several workspaces; a v5 environment file and a design
  document; plus the YAML reader's own Insomnia fixture, detection, invalid
  files with their lines, orphans and parent loops, and the template
  conversion.
- `internal/app/services/collection/import_insomnia_test.go`: the v4
  fixture through the default formats into a project, with the secrets
  only in the local side files.
- `internal/core/importer/postman/postman_test.go`: golden files of the
  parsed model (`testdata/*.golden`, rewrite with `-update`) for a v2.1
  collection (nested folders, every body mode and auth type, variables at
  each level, scripts, examples, settings, dynamic variables, vault), a
  v2.0 collection (object auth, URL objects without raw, header text), an
  environment and a globals file; plus detection among the other formats,
  invalid files with their lines, schema versions and the dynamic variable
  conversion.
- `internal/core/importer/postman/exporter_test.go`: a project using
  everything the export keeps goes through `Export`, is validated against
  the v2.1 schema copy, compared with a golden file
  (`testdata/export/*.golden`, rewrite with `-update`) and imported back
  with the Postman importer into the same model; a project using what
  Postman lacks gives a golden report, no secret in the file, and the
  folder headers on its requests; the schema copy rejects broken
  collections; the collection ID is stable.
- `internal/app/services/collection/export_postman_test.go`: the Postman
  fixture imported, exported through the service (fake save dialog) and
  imported again gives the same folders and requests, with no secret of
  the first import in the file; the project export (unknown and broken
  files skipped); a folder's export with what it inherits and a pending
  edit; cancel, an unwritable path, no dialog, a request ID.
- `frontend/src/commands/exportPostman.test.tsx`: the summary, the toast,
  the report dialog and failures; `TreeActions.test.tsx` the menu items.
- `internal/core/importer/projectzip/projectzip_test.go`: a folder with
  every request kind, settings of every kind, secrets typed by hand and
  files that cannot be exported: no secret in any file of the zip, the
  entries, counts and notes; the zip parsed back (detection, settings,
  order, unknown members, local-secret marks); crafted zips refused (zip
  slip, absolute paths, drive letters, backslashes, `.` and empty
  elements, control characters, symbolic links, pipes, devices,
  encryption, duplicates, a file that is also a directory, nesting, too
  many entries, declared and lying sizes, the total size, newer format and
  schema versions, a missing or foreign manifest, several top-level
  entries, an unknown compression method); what a hand-edited zip holds
  that is skipped or ignored.
- `internal/app/services/collection/export_zip_test.go`: a folder with
  every request kind and setting, secrets typed in the editors and by
  hand, exported through the service, detected as a drop, previewed and
  imported gives the same tree minus the secrets, with no secret in the
  zip or the imported files; the errors and a cancelled dialog; a zip-slip
  zip refused with nothing written. `frontend/src/commands/exportZip.test.tsx`:
  the summary, the toast, the report and failures.
- `internal/app/services/collection/import_postman_test.go`: the
  collection, environment and globals fixtures through the default formats
  into a project; typed auth secrets, secret folder variables and secret
  environment values are readable through the services and never in the
  project's files.
- `internal/core/importer/openapiimport/openapiimport_test.go`: golden
  files of the model (`testdata/*.golden`, rewrite with `-update`) for the
  reader's 3.0 and 3.1 petstores, recursive schemas (trees, mutual
  recursion, recursive `allOf` and `oneOf`, a chain past the depth limit),
  every security scheme and requirement shape, and parameter styles and
  body media types; the JSON and YAML petstores give the same model;
  detection, invalid files, reader diagnostics on their operation, the
  example generator (recursion, value budget, directions, discriminators)
  and XML examples.
- `internal/app/services/collection/import_openapi_test.go`: the 3.0
  petstore through the default formats into a project: tag folders,
  example bodies, path parameters, auth and the collection's variables
  (credentials as secrets without values).
- `internal/core/importer/harimport/harimport_test.go`,
  `internal/app/services/collection/import_har_test.go`: HAR goldens of
  Chrome, Firefox and proxy recordings, and the service-level import with
  no credential or cookie in the project ([har.md](har.md#tests)).
- `internal/core/importer/altair/altair_test.go`: golden files of the
  parsed model (`testdata/*.golden`, rewrite with `-update`) for window
  exports shaped as Altair 4 and 5 write them, a nested collection export
  as Altair 8 writes it (headers, variables, extensions, every auth type,
  handlers, scripts, file variables), a collection with sub-collections
  listed flat by `parentPath` (orphans and loops) and an environment; plus
  detection among the other formats and invalid files. The fixtures are
  reconstructed from Altair's export format, not files saved by Altair.
- `internal/app/services/collection/import_altair_test.go`: the windows
  and the environment through the default formats into a project;
  `internal/app/services/http/import_altair_test.go` imports the collection
  and sends two of its requests (POST with Basic auth from the local side
  file, variables and extensions; GET) to a GraphQL test server.
- `internal/app/services/collection/import_test.go`: `PreviewImport`,
  `Import` into a folder with local secrets, the digest check, an
  environments-only import and its event, and the read errors.
- `frontend/src/components/tree/ImportDialog.test.tsx`,
  `frontend/src/lib/importReport.test.ts`: the dialog's steps and the
  report helpers.
