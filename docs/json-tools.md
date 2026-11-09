# JSON tools and request bodies

Validate, beautify and minify JSON bodies (HTTP-12), and what a saved body
sends (HTTP-10, HTTP-11). The body editor UI is in
[frontend.md](frontend.md#body-editor).

| Piece | Where |
|---|---|
| Validate, beautify, minify | `internal/core/jsonfmt` |
| Bound service | `internal/app/services/format` (`FormatService`) |
| Frontend wrapper | `frontend/src/lib/format.ts` |
| Saved body → bytes to send | `internal/core/request/body.go` |

## `internal/core/jsonfmt`

- `Validate(src) *SyntaxError`, `Beautify(src, indent) (string, *SyntaxError)`,
  `Minify(src) (string, *SyntaxError)`. A nil error means valid.
- **Bytes, not values.** The work is done by `json.Indent` and `json.Compact`
  on the text, never by decoding and encoding again, so number literals
  (`1.50e+3`, `12345678901234567890`), string escapes (`é`), member
  order and duplicate keys are kept exactly. Only whitespace outside strings
  changes; `Minify` does not HTML-escape.
- **Beautify** indents by `indent` spaces (0 is a tab, at most
  `MaxIndent` = 8), keeps `{}` and `[]` on one line and drops leading and
  trailing whitespace. The UI uses 2 spaces.
- **Invalid JSON is refused.** Beautify and Minify return the error and no
  text; nothing is half-formatted. Blank text is invalid
  ("Unexpected end of JSON input").
- `TopLevelKeys(src) ([]Key, TopKind, *SyntaxError)` lists the member names
  of a top-level object in source order, each with its byte range (quotes
  included) and whether it holds a `{{variable}}`, and says whether the
  text is blank, `null`, an object or something else. The GraphQL
  variables check uses it ([graphql.md](graphql.md#ui)).

### `{{variables}}`

Bodies like `{"id": {{userId}}, "name": "Hi {{name}}"}` are common, so
variables count as JSON values:

1. `template.Parse` finds the variables (the same syntax as everywhere,
   [templates.md](templates.md); `\{{` stays text).
2. Each one is replaced by a run of `1` digits of the same byte length. A
   digit run is a valid number and valid string content, and has no
   whitespace, so every byte offset stays where it was.
3. The masked text is scanned and formatted.
4. The formatters copy every byte except whitespace outside strings in
   order, so each variable is found in the output by walking the input and
   output together, and its original text (`{{ user id }}`, spaces
   included) is put back untouched.

Consequences: a variable works wherever a value, part of a number
(`-{{n}}`, `{{n}}.5`) or part of a string can be. A variable where no value
can be (`{{{key}}: 1}`, or two in a row) is an error that names it:
"Unexpected variable {{key}} looking for beginning of object key string".
Whether the rendered text is valid JSON depends on the variable's value
and is not checked.

### Error positions

`SyntaxError` has:

| Field | Meaning |
|---|---|
| `Offset` | Byte offset of the offending character; the text's length when it ends too early. |
| `Offset16` | The same position in UTF-16 code units: a CodeMirror / JavaScript string position. |
| `Line`, `Column` | 1-based. `Column` counts UTF-16 units. `\n`, `\r\n` and a lone `\r` end a line, as in CodeMirror. |
| `Message` | `encoding/json`'s message, capitalised. |

`json.SyntaxError.Offset` is the number of bytes read, i.e. just after the
rejected character (a whole UTF-8 sequence), or the length at the end of
input; `jsonfmt` steps back over that character. A fuzz test
(`FuzzFormat`) checks that the three functions agree on validity, that
positions are in range, that `Minify(Beautify(x)) == Minify(x)` and that
variables survive.

## `FormatService`

Stateless; registered in `internal/app/services.go`.

| Method | Returns |
|---|---|
| `ValidateJSON(text)` | `JSONCheckDTO { valid, error }` |
| `BeautifyJSON(text, indent)` | `JSONFormatDTO { valid, text, error }` |
| `MinifyJSON(text)` | `JSONFormatDTO { valid, text, error }` |
| `IndentXML(text)` | `XMLFormatDTO { valid, text, error }`: `xmldom.Indent` with two spaces, for response bodies too large to be pretty-printed on arrival (HTTP-31); `error` is `XMLErrorDTO { line, column, message }` |

- **Invalid JSON is a result, not a rejection.** `error` is a
  `JSONErrorDTO { offset, byteOffset, line, column, message }` (`offset` in
  UTF-16 units for the editor) or `null`. For an invalid input `text` is the
  input unchanged.
- Errors (rejections) are only for a bad argument (`indent` outside 0..8:
  `invalid_argument`, code `format.indent_out_of_range`) and cancelled calls.
- The shapes are pinned by golden files in
  `internal/app/services/format/testdata/dto/`.

Go is the only validator: the editor has no TypeScript JSON check, so the
marker, Beautify and Minify always agree. Validation runs 300ms after typing
pauses; a check still running when the text changes is cancelled.

## Sending bodies

`request.Body(projectfile.Body, expand, root) (httpclient.Body, error)` maps a
saved body to what is sent. Sending itself is `request.BuildHTTP` and the
HTTP service ([http-client.md](http-client.md#sending-from-the-app)).

| Mode | Sends |
|---|---|
| `none`, `""` | No body (`nil`), whatever other members the file has. |
| `raw` | `text` as UTF-8 (`httpclient.RawBody`), for every `type`, including an empty text (a zero-length body) and invalid JSON. The JSON tools mark errors; they never block a send. |
| `urlencoded` | The enabled `fields`, encoded as `application/x-www-form-urlencoded` ([http-client.md](http-client.md#form-bodies)). |
| `multipart`, `file` | Streamed from disk; relative file paths are resolved against `root` ([http-client.md](http-client.md#multipart-and-file-bodies)). |
| unknown | Error `unsupported`, code `request.body_mode_unsupported`. |

- `expand` (optional, the `request.Expand` of `request.Headers`) renders
  `{{variables}}` in the raw text, or in each field's name and value before
  it is encoded; with `nil` the text is sent as typed. An `expand` error is
  returned as it is ([templates.md](templates.md)).
- The returned body's `ContentType()` is the auto Content-Type of its mode,
  `request.ContentType(body)` (HTTP-16, [http-client.md](http-client.md#auto-content-type)),
  which httpclient sends unless an enabled `Content-Type` row overrides it.
  For raw bodies it is `request.RawMediaType(type)`:

  | `type` | Media type |
  |---|---|
  | `json` | `application/json` |
  | `xml` | `application/xml` |
  | `html` | `text/html` |
  | `text`, `""` | `text/plain` |
  | `yaml` | `application/yaml` (RFC 9512) |
  | unknown | `""` |
