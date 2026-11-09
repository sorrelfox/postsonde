# Mock routes

The HTTP mock server (MOCK-01, MOCK-02)
picks the route for each request with `internal/core/servers/mockroute`.
It is a pure matcher: no sockets and no `net/http` server. The mock server
type ([servers.md](servers.md)) compiles its routes with it and
calls `Match` for every request.

```go
m, diags := mockroute.Compile([]mockroute.Route{
    {ID: "me", Method: "GET", Pattern: "/users/me"},
    {ID: "user", Method: "GET", Pattern: "/users/{id:[0-9]+}"},
    {ID: "files", Method: "ANY", Pattern: "/files/*path"},
}, mockroute.Options{})
res := m.Match(r.Method, r.URL.EscapedPath())
switch {
case res.Matched:            // res.RouteID, res.Params, res.Wildcard, res.HeadFallback
case res.MethodNotAllowed:   // 405 with Allow: strings.Join(res.Allowed, ", ")
default:                     // 404
}
```

## Patterns

A pattern starts with `/` and is split on `/` into segments. Each segment
is one of:

| Segment | Matches | Captures |
|---|---|---|
| `users` | exactly that text, after percent-decoding both sides (`/a%20b` and `/a b` are the same) | nothing |
| `:id` or `{id}` | any one non-empty segment | `id` |
| `{id:[0-9]+}` | one non-empty segment whose decoded value matches the Go regular expression, anchored to the whole segment (`^(?:[0-9]+)$`) | `id` |
| `*` | any one non-empty segment | nothing |
| `**` | zero or more trailing segments; only as the last segment | `Wildcard` |
| `*path` | the same as `**` | `Wildcard`, and the parameter `path` |

- `/` alone is the root and has no segments.
- `:name` and `{name}` are the same syntax as request path parameters.
  Names are ASCII letters, digits and `_`, not starting with a digit,
  and unique within a pattern.
- Parameters and wildcards fill a whole segment. A `:` inside a segment is
  literal text (`/v1/items:batchGet`); `{`, `}` and `*` inside literal
  text are errors.
- A constraint cannot contain `/` (the pattern is split on `/` first).
  Braces inside it are fine: `{code:[A-Z]{3}}`.
- A trailing slash after a catch-all is ignored: `/files/**/` is
  `/files/**`.
- A pattern has no query string or fragment.

## Requests

`Match(method, path)` takes the **escaped** request path
(`r.URL.EscapedPath()` or the request target, not `r.URL.Path`, which has
already decoded `%2F`). Anything from the first `?` or `#` is ignored; an
empty path is `/`; a path that does not start with `/` (`*`,
absolute-form) matches nothing.

- The path is split on `/` first, then every segment is percent-decoded,
  so `%2F` stays inside its segment: `/files/a%2Fb` matches `/files/:name`
  with `name = "a/b"`, not `/files/:dir/:name`. A segment with an invalid
  escape is used as written.
- Literals, constraints and parameter values use the decoded text.
  `Param.Raw` keeps the segment as written. `Wildcard` is the decoded
  segments joined with `/`, and `WildcardRaw` is the remainder as written,
  which is the only way to tell `%2F` from a separator.
- Empty segments in the request (`/a//b`) match only a catch-all.

## Trailing slashes

By default (**lenient**) one trailing slash is dropped from patterns and
requests: `/users` and `/users/` match both requests, and the two patterns
are duplicates.

With `Options.StrictTrailingSlash`, `/users` matches only `/users`, and
`/users/` matches only `/users/`. The root `/` is the same in both modes. A
catch-all also takes the trailing slash: `/files/**` matches `/files`,
`/files/` and `/files/a/`, and in strict mode the wildcard of the last one
is `a/` (lenient: `a`).

## Case

Paths are case-sensitive by default. With `Options.CaseInsensitive`,
literal segments are compared lower-cased (`strings.ToLower`) on both
sides. Parameter values keep their case, and constraints are not changed
(write `(?i)` in the constraint).

## Methods

- A route's method is matched exactly; HTTP methods are case-sensitive, so
  `get` is not `GET`. Any token is allowed (`PURGE`, `PROPFIND`).
- `ANY`, `*` and an empty method accept every method.
- A `HEAD` request also matches `GET` routes (`Result.HeadFallback`, so
  the server sends no body). A `HEAD` route for the same path wins over the
  `GET` route.
- **405:** when no route accepts both path and method but some route
  matches the path, `MethodNotAllowed` is true. `Allowed` lists the
  methods of those routes, sorted, with `HEAD` added when `GET` is there.
  It is meant for the `Allow` header.

## Which route wins

Among the routes whose pattern matches the path and whose method accepts
the request:

1. **The most specific pattern.** Patterns are compared segment by
   segment, left to right. At the first position where the kinds differ,
   the kind higher in this list wins:
   literal > constrained parameter > parameter > `*` > *pattern ended* >
   catch-all.
   "Pattern ended" means that `/users` beats `/users/**` for `/users`. A
   longer pattern beats a shorter one only through that rule, because two
   patterns without a catch-all match paths of different lengths.
2. **The best method:** exact method, then `GET` for a `HEAD` request,
   then `ANY`.
3. **The route defined first.** This only matters for patterns of the
   same shape that can match the same path with different constraints,
   such as `{a:[0-9]+}` and `{b:[0-9]{2}}`.

Parameter names, literal text and constraint text do not change the
order, so the result does not depend on the order of the routes, except
for rule 3.

The path decides before the method, and a route with the wrong method is
skipped rather than ending the search. With `GET /users/me` and
`DELETE /users/:id`, `DELETE /users/me` matches the second route, and
`POST /users/me` is a 405 with `Allow: DELETE, GET, HEAD`.

| Routes | Request | Winner |
|---|---|---|
| `/users/me`, `/users/{id:[0-9]+}`, `/users/:id`, `/users/*`, `/**` | `/users/me` | `/users/me` |
| same | `/users/42` | `/users/{id:[0-9]+}` |
| same | `/users/bob` | `/users/:id` |
| same | `/users/bob/likes` | `/**` |
| `/:a/b/c`, `/a/:b/:c` | `/a/b/c` | `/a/:b/:c` (the first segment decides) |
| `/users`, `/users/**` | `/users` | `/users` |
| `ANY /mixed/*`, `GET /mixed/x` | `POST /mixed/x` | `ANY /mixed/*` |
| `ANY /users/me`, `GET /users/:id` | `GET /users/me` | `ANY /users/me` (path first) |

## Validation

`Compile(routes, opts)` returns a `Matcher` built from the valid routes
and a `Diagnostic` for each problem. A route with any diagnostic is left
out. A diagnostic has `Code`, `RouteIndex`, `RouteID`, `Field` (`pattern`,
`method` or `id`), the byte range `Start`–`End` in that field, and a
`Message`. `Diagnostic.Err()` turns it into an `errs.InvalidArgument` error
with code `mockroute.invalid_route` ([errors.md](errors.md)).

| Code | When |
|---|---|
| `mockroute.empty_pattern` | the pattern is empty |
| `mockroute.missing_leading_slash` | it does not start with `/` |
| `mockroute.query_in_pattern` | it has `?` or `#` |
| `mockroute.empty_segment` | `//` |
| `mockroute.invalid_segment` | `{`, `}` or `*` inside literal text, or a `{` without its `}` |
| `mockroute.invalid_escape` | a bad `%` escape in literal text |
| `mockroute.invalid_param_name` | `:`, `{}`, `:1d`, `{my id}`, `*1` |
| `mockroute.duplicate_param` | the same name twice (including a named catch-all) |
| `mockroute.catch_all_not_last` | `**` or `*name` followed by more segments |
| `mockroute.invalid_regex` | an empty or invalid constraint |
| `mockroute.invalid_method` | the method is not an HTTP token |
| `mockroute.duplicate_id` | a valid route reuses an earlier route's ID |
| `mockroute.duplicate_route` | a route matches the same method and paths as an earlier one: same shape, same literals (lower-cased with `CaseInsensitive`) and same constraint text; parameter names, `**` vs `*name`, `ANY` vs `*`, and `/x` vs `/x/` when lenient do not count. The later route could never win |

## Performance

`Compile` sorts the routes by specificity once and indexes them by their
first literal segment. `Match` checks only the routes in that bucket and the
routes that start with a parameter or wildcard, without allocating for
routes that do not match. `BenchmarkMatch1000` (1,000 routes, 250 of them
starting with a parameter) takes about 3.5 µs per match on an M2 Max.

## Responses

A route answers with its own status, headers and body, its **default
response**, unless one of its other responses (`Route.Responses`,
MOCK-05 and MOCK-07) is picked. This is in `mockhttp`
(`responses.go`), after the matcher has chosen the route.

```go
route.Responses = []mockhttp.Response{{
    Name:   "admin",
    Status: 200,
    Body:   mockhttp.Body{Mode: "raw", Type: "json", Text: `{"role": "admin"}`},
    Rules:  []mockhttp.Rule{{Source: "header", Name: "X-Role", Op: "equals", Value: "admin"}},
}, {
    Name: "outage", Status: 503, // no rules: only the header picks it
}}
```

**Which response answers.**

1. The **`X-PostSonde-Response`** request header (`mockhttp.SelectHeader`,
   MOCK-07), when it is there and not blank, decides alone; the rules are
   not looked at. Its value (spaces trimmed, letter case ignored) is
   - `default`: the route's own response;
   - the name of a response;
   - else a status code 200–599 (three digits): the first response with that
     status, the default last, or when none has it, that status with no
     headers and no body (`X-PostSonde-Response: 500` forces an error on any
     route);
   - anything else: **400** with the JSON body `{"error", "method", "path",
     "responses"}`, where `responses` lists the names the route takes
     (`default` first), and `Cache-Control: no-store`. The route's delay is
     not applied.
2. Else the first response, in order, whose **rules all match**. A response
   without rules never matches by rules: it is for the header.
3. Else the default response.

The route's delay (MOCK-04) holds back every response of the route. The
traffic log's response event names the response in `RouteMatch.Response`
(`default`, the name, or `status 500` for a forced status) when the route
has other responses or the request used the header.

**Rules.** A rule has a source, a name, an operator and a value:

| Source | Name | Values |
|---|---|---|
| `query` | the parameter (case-sensitive) | every value of the parameter; `?q` and `?q=` have the value "" |
| `header` | the header (any letter case) | every value of the header; `Host` is the request's host |
| `body` | unused | the request body as text; none when it is empty |
| `json` | a JSONPath ([jsonpath.md](jsonpath.md)) | every node the path selects in the body read as JSON; none when the body is not JSON |

| Operator | Matches when |
|---|---|
| `equals` | a value equals `value` |
| `not_equals` | no value equals `value` (also when there is none) |
| `contains` | a value contains `value` |
| `not_contains` | no value contains `value` (also when there is none) |
| `matches` | a value matches the Go regular expression `value` (unanchored) |
| `exists` | there is a value |
| `not_exists` | there is none |

Values are compared as assertions compare them
([assertions.md](assertions.md), `assert.Compare`): text exactly (a
JSON-quoted `value` is unquoted); a JSON number numerically (`100` equals
`100.0`); booleans and `null` by their literal; arrays and objects as JSON,
members in any order. `contains` on a JSON array looks for an element, on
an object for a member name. The rules see the first 1 MiB of the body
(`MaxMatchBodyBytes`), however much the traffic log keeps.

A response's name is optional (without one only the rules pick it), unique
within the route (letter case ignored) and not `default`. Problems are
diagnostics with the response's index ([servers.md](servers.md#mock-http));
a route with one is not served.

**Bodies** are produced in one place, `reply.render(requestData)`, which
gets the request's method, path, query, headers, body and path parameters:
as written, or rendered when the response is templated
([Templates](#templates)).

**Base64 bodies.** A raw body of type `base64` (`mockhttp.BodyTypeBase64`)
is a binary body written in Base64 (spaces and line breaks ignored): the
server sends the decoded bytes, with the implicit `Content-Type:
application/octet-stream` unless a row sets one. Such a body cannot be
templated, and one that does not decode is a `mockhttp.invalid_body`
diagnostic. Routes made from a binary response have it
([below](#routes-from-a-response)).

**OpenAPI.** Routes generated from OpenAPI (below) have only their default
response, the first 2xx one; the header still forces any status on them,
and responses can be added to them by hand. A second merge keeps a route
that is already there with its responses.

## Templates

MOCK-06. A response with **Templated** on (`Route.Templated` for the
default response, `Response.Templated` for each other one; `templated` in
the project file) has its body and its enabled header values rendered for
every request, by the same template engine as requests
([templates.md](templates.md)), in `mockhttp/template.go`. It is opt-in
per response and off by default, so bodies that contain `{{` (saved before
templates, or JSON with literal braces) are sent as written.

```json
{
  "id": {{request.params.id}},
  "name": "{{request.body.user.name}}",
  "page": "{{request.query.page}}",
  "trace": "{{request.headers.X-Trace}}",
  "requestId": "{{$uuid}}",
  "at": "{{$isoTimestamp}}",
  "etag": "{{request.body | sha256}}"
}
```

The request's values are one scope, `Request`, under the `request.`
namespace:

| Name | Value |
|---|---|
| `request.method` | the method: `GET` |
| `request.path` | the path, decoded, without the query: `/users/42` |
| `request.url` | the request target as sent: `/users/42?full=1` |
| `request.host` | the `Host` the request was sent to |
| `request.params.NAME` | a path parameter of the route (`:id`, `{id}`, `*rest`), decoded |
| `request.wildcard` | what `**` or `*name` matched: `a/b.txt` |
| `request.query.NAME` | the first value of a query parameter (case-sensitive) |
| `request.headers.NAME` | the first value of a request header, any letter case; `Host` is the host |
| `request.body` | the request body as text |
| `request.body.PATH` | a field of the body read as JSON, with [dot notation](templates.md#dot-notation): `request.body.user.name`, `request.body.items.0.id`; a string is its text, anything else compact JSON |

- **Dynamic values and filters** work as in a request: `{{$uuid}}`,
  `{{$randomInt 1 6}}`, the fake data (`{{$randomFullName}}`), the
  timestamps with offsets, and the filter pipes
  (`{{request.headers.Authorization | base64}}`). Each reference is
  generated afresh, for every request.
- **Missing values are empty.** A parameter, query parameter, header or
  JSON field the request does not have, and any `request.body.PATH` of a
  body that is not JSON, render as `""`; the response is sent.
- **Request data is never a template.** A value from the request is
  inserted as it is: a body containing `{{request.method}}` echoes those
  characters. `\{{` in the response writes literal braces, as in requests.
- **Environment and project variables are not used.** A mock server runs
  apart from the requests and the active environment (it keeps running
  when the environment changes or the project closes), so its responses do
  not depend on them: `{{baseUrl}}` is undefined.
- **Errors are 500.** A name outside the `request.` names and the dynamic
  values (`{{request.nope}}`, `{{baseUrl}}`), invalid dynamic arguments or
  filters, a rendered header value with a line break, or a result larger
  than 16 MiB (`MaxRenderedBytes`) answer **500** with the JSON body
  `{"error", "method", "path"}` and `Cache-Control: no-store`; `error`
  names the route, the response and the problem (*Route GET /users/:id,
  response "default": The template in the body cannot be rendered: The
  variable {{request.nope}} is not defined.*). The traffic log gets an
  `error` event with the same message, and the response event's summary
  ends in "(template error)". The route's delay is not applied.
- **Limits.** Like the rules, templates see the first 1 MiB of the request
  body (`MaxMatchBodyBytes`). Names that contain whitespace or `|` are
  pipes and dynamic values, not request names, so a query parameter or
  JSON key with a space cannot be read.
- Header values are rendered only when they contain `{{`; `Content-Type`
  can be templated too. Header names, status and rules are never
  templated.

**Editor.** The body editor has a Templated checkbox. On, the body and
header values draw `{{references}}` as chips, red when Go would refuse
them, and typing `{{` completes the `request.` names (with the route's own
path parameters, `request.params.id` for `/users/:id`) and the dynamic
values; `ServersService.MockTemplateNames()` supplies the names Go knows
(`mockhttp.RequestNames` and `template.DynamicNames`). Off, a body with
`{{` shows "Sent as written".

## Routes from OpenAPI

`internal/core/servers/mockopenapi` (MOCK-09) makes the routes of a
mock server from an OpenAPI 3.0/3.1 or Swagger 2.0 document read by the
OpenAPI reader ([openapi.md](openapi.md)). Standard library only.

```go
res, err := mockopenapi.Parse(data, mockopenapi.Options{})  // Prefix nil: the first server's path
// err: not an OpenAPI document at all (the reader's error).
res.Routes      // []mockhttp.Route, one per operation, all valid (mockhttp.Check)
res.Operations  // what each route answers: method, path, operationId, response, media type
res.Diagnostics // the reader's, then one per note, with the operation and route index

m := mockopenapi.Merge(current, res.Routes, mockopenapi.ModeMerge) // or ModeReplace
m.Config        // the server's routes with the generated ones
m.Statuses      // per generated route: added, existing, over_limit
```

**One route per operation**, in document order, enabled, no delay:

| Route | From |
|---|---|
| ID | `rt_` and the `operationId` in lower case (`rt_listpets`), else the method and path (`rt_get_pets_petid`); a suffix `_2` keeps IDs unique |
| Method | the operation's method |
| Path | the prefix and the path, converted (below) |
| Status | the first 2xx code in document order; else a `2XX` range (200); else `default` (200); else the first other code or range (`404`, `5XX` = 500), with a `mockopenapi.status` warning; no responses at all: 200 with no body |
| Headers | the response headers with an example: the header's `example` or `examples`, its content's example, or its schema's `example`, `examples`, `default`, `enum` or `const`; a required header without one gets a generated value. Values are written in the simple style (a string as it is, an array `a,b`, an object `k,v,k2,v2`). `Content-Type` (the specification says to ignore it), `Content-Length` and `Transfer-Encoding` are left out, and so is a value with a line break (`mockopenapi.header`) |
| Body | the response's preferred media type, below; none for 204 and 304 |

**Body.** The media types are ranked JSON (`application/json`, `+json`),
XML, YAML, `text/html`, other `text/*`, wildcards (`*/*`), others, files.
The best one gives the body (the others are listed in a
`mockopenapi.media_type` note) and its value comes from
`openapiimport.MediaTypeExample` in response mode (`writeOnly` properties
left out): the media type's `example`, its first `examples` entry with a
value, or one generated from the schema (cycle-safe and bounded, the same
generator as the importer's, [import.md](import.md#openapi-documents)).

| Media type | Body |
|---|---|
| JSON | raw `json`, the example as indented JSON |
| XML | raw `xml`: a string example as it is, anything else rendered with the schema's `xml` hints (`XMLExample`) |
| YAML, `text/html`, other text and others | raw `yaml`, `html` or `text`: a string example as it is, anything else as indented JSON |
| `*/*` and other wildcards | a string example as text, anything else as JSON; no `Content-Type` row |
| files (`application/octet-stream`, `image/*`, `application/pdf`, a `format: binary` schema…) | none, with a note |

A `Content-Type` header row with the document's media type is added when
the body's language would send another one (`application/problem+json`,
`text/csv`, `application/x-yaml`, `application/json; charset=utf-16`); not
for `text/plain; charset=utf-8`, which is what the server sends anyway.
Notes say which named example was used when there are several, that
external examples (`externalValue`) are not read, that a generated body
stops at a recursive schema or the depth limit
(`mockopenapi.example_truncated`), or that there is nothing to make a body
from (`mockopenapi.no_example`). An example over 10 MiB leaves the body
empty (`mockopenapi.body_too_large`).

**Paths.** `ConvertPath` turns `{param}` into `:param`; everything else
is kept, except what a pattern cannot say, each with a `mockopenapi.path`
note:

| OpenAPI path | Route path |
|---|---|
| `/pets/{petId}` | `/pets/:petId` |
| `/users/{user-id}/repos/{repo.name}` | `/users/:user_id/repos/:repo_name` (names are letters, digits and `_`; one starting with a digit gets `p`) |
| `/a/{id}/b/{id}` | `/a/:id/b/:id_2` |
| `/files/{name}.{ext}` | `/files/{name_ext:.+\..+}`: a segment with text and parameters is one constrained parameter, the text quoted in the regular expression |
| `/items/{id}:activate` | `/items/{id:.+:activate}` |
| `/glob*`, `/:x`, `/100%` | `/glob%2A`, `/%3Ax`, `/100%25`: characters a pattern reserves are percent-encoded, and the matcher decodes both sides |
| `/search?type=x`, `pets`, `/a//b` | `/search`, `/pets`, `/a/b` |

`FuzzConvertPath` checks that every conversion is a valid pattern.

**Prefix.** Every path starts with a prefix, by default the path of the
document's first server with its variables at their defaults
(`https://petstore.example/v1` → `/v1`; Swagger 2.0's `basePath`, which the
reader turns into the server URL, likewise; `/` and a server without a
path give none), so a client pointed at the mock uses the same paths as
with the real API. `Options.Prefix` sets another one: a path, or a URL
whose path is taken (`PrefixOf`). A server variable without a default
stays in the path as a parameter, with a `mockopenapi.prefix` warning.
Operation- and path-level `servers` are not used.

**Left out.** Webhooks and callbacks get no routes (the API calls them;
one `mockopenapi.not_mocked` note). Two operations that give the same
route (`/dup/{a}` and `/dup/{b}`) keep the first
(`mockopenapi.duplicate_route`); a route mockhttp still rejects would be
`mockopenapi.invalid_route`. More than 1,000 operations keep the first
1,000 (`mockopenapi.too_many_routes`).

**Merge or replace.** `Merge(config, routes, mode)`:

- `merge` keeps the server's routes and adds the generated ones after
  them. A generated route with the same method and paths as a route
  already there (the matcher's `duplicate_route` rule, with the server's
  own trailing-slash and case options, disabled routes included) is not
  added, so routes edited by hand survive a second import
  (`mockopenapi.existing_route`, status `existing`). A generated ID that
  is taken gets a `_2` suffix.
- `replace` drops the server's routes; the matcher options are kept.
- Routes beyond 1,000 are not added (`over_limit`).

To take a changed document's responses for routes that are already there,
replace, or delete those routes first.

The service method and the preview dialog are in
[servers.md](servers.md#routes-from-openapi) and
[frontend.md](frontend.md#routes-editor).

## Routes from a response

`internal/core/servers/mockexample` (MOCK-03) turns a response a
request got into a route, so a mock server answers as the real server did.
Standard library only. The source is a **response example** (HTTP-38,
[history.md](history.md#response-examples)): a request's saved example as
it is, or the latest or an earlier response, which the UI first has Go
make into one from its history entry (`HistoryService.Example`).

```go
res := mockexample.Build(mockexample.Input{
    Example: ex,                        // status, headers, body, the request as sent
    Method:  "GET",
    URL:     "{{baseUrl}}/users/:id",   // the request's URL as typed
    Masker:  httpclient.NewSecretMasker(secrets),
})
m := mockexample.Merge(config, res, mockexample.ModeRoute, "")
```

**Method and path** (`RoutePath`). The method is the request's; the path
comes from its URL **as typed**, so the route matches every value the
request could send:

| Request URL | Sent as | Route |
|---|---|---|
| `{{baseUrl}}/users/{{userId}}` | `https://api.example/users/42` | `/users/:userId` |
| `https://api.example/users/:id/posts` | | `/users/:id/posts` |
| `{{baseUrl}}/users/:id` | `https://api.example/v1/users/42` | `/v1/users/:id` |
| `{{host}}/u/{{user.id}}`, `{{b}}/n/{{$randomInt 1 9}}` | | `/u/:user_id`, `/n/:randomInt` |
| `{{b}}/files/{{name}}.json` | | `/files/{name:.+\.json}` |
| `https://api.example/search?q={{q}}` | | `/search` |

- The scheme and authority are left out, and so is a `{{variable}}` at the
  very start of the URL: it stands for the base URL.
- A segment that is one `{{variable}}`, `:param` or `{param}` (HTTP-05)
  is a parameter named after it (filters, arguments and the `$` of a
  dynamic value dropped); names and mixed segments are converted as in
  [routes from OpenAPI](#routes-from-openapi) (`mockopenapi.ConvertPath`).
- **Base URL path.** When the URL sent has more segments than the typed
  path and ends with segments that match it, the extra leading ones are the
  base URL's path (`{{baseUrl}}` = `https://api.example/v1`) and go in
  front, so a client whose base URL points at the mock uses the same
  paths. When the ends do not match (a variable holding several
  segments), the route has the typed path only, with a note.
- The query and fragment are left out, with a note: routes match paths.
- The path can be edited in the dialog before the route is added.

**Status, headers and body.** The route's default response is the
response's status, header fields (in order, repeated names kept: two
`Set-Cookie` fields stay two rows) and body, not templated (a body with
`{{` is sent as written). What differs, and why:

| Left out | Why |
|---|---|
| hop-by-hop fields: `Connection`, `Keep-Alive`, `Proxy-Connection`, `TE`, `Trailer`, `Transfer-Encoding`, `Upgrade`, and the fields `Connection` names | they describe one connection, not the response (RFC 9110 section 7.6.1); the mock server manages its own |
| `Content-Length` | the mock server sets it from the body it sends (a route's `Content-Length` row is ignored anyway) |
| `Content-Encoding`, when it is one `gzip`, `x-gzip` or `deflate` (or `identity`) | PostSonde decompressed that body when it arrived (httpclient `SendTo`), so the example keeps the decoded bytes; sending them with the header would be a lie. Any other coding (`br`, `zstd`, two codings) was not decoded: the body keeps the encoded bytes and the header is kept, so the bytes and header match again |
| `Date` | the mock server sends the current date; a date frozen in the route would mislead caches |
| a field with an invalid name or a control character in its value | the route would not be served with it |
| HTTP/2 pseudo-header fields (`:status`) | not header fields |

- **Content-Type.** The response's own `Content-Type` row is kept, so it
  replaces the implicit one of the body type. A response without one gets
  the body type's (`text/plain; charset=utf-8`, or
  `application/octet-stream` for a binary body), with a note: a route
  always has a body type.
- **Body.** Text is kept as text, typed from the `Content-Type` (`json`,
  `xml`, `html`, `yaml`, else `text`). A body that is not UTF-8 (Base64 in
  the example) becomes the raw type **`base64`** (`mockhttp.BodyTypeBase64`):
  the route keeps it in Base64 and the server sends the decoded bytes. An
  example keeps at most 64 KiB of a body; a clipped one gives a route that
  sends that part, with a warning. 204 and 304 get no body.
- **Reason phrase.** The mock server sends Go's standard reason phrase;
  a response with another one (`299 Fine Thanks`) gets a note.
- **Secrets.** The example was masked when it was saved (or made from the
  history). `ServersService` masks it **again** with every secret
  PostSonde keeps now (`WithSecrets`, the same values copied logs and
  examples are masked with, [privacy.md](privacy.md#copied-and-saved-logs)):
  header values (`Authorization` and `Proxy-Authorization` by name) and the
  body, a Base64 body in its decoded bytes. A secret therefore reaches the
  route only as `••••••`, and a note says secrets were hidden. The same
  limits apply as everywhere: a secret sent transformed is not recognised.

**Merge** (`Merge(config, result, mode, name)`). The server's routes are
compared with the new route by the matcher's `duplicate_route` rule (the
server's trailing-slash and case options, disabled routes included):

| Mode | No route answers the method and path | A route does (`Existing`) |
|---|---|---|
| `route` (default) | a new route, enabled, no delay, ID `rt_` + method and path (`rt_get_v1_users_id`, `_2` when taken): `added` | nothing changes: `existing` |
| `response` | a new route: `added` | the response is added to that route as a **named response** without rules (MOCK-07: `X-PostSonde-Response: <name>` picks it), named after the example or the name given, made unique (`200 OK (2)`), never `default`: `response_added` |
| `replace` | a new route: `added` | the route's own response (status, headers, body) is replaced and its Templated flag cleared; its other responses, delay, ID and Enabled stay: `replaced` |

At 1,000 routes, or 100 responses on the route, nothing is added
(`over_limit`). The service method and the dialog are in
[servers.md](servers.md#routes-from-a-response) and
[frontend.md](frontend.md#routes-editor).

## Tests

`mockexample_test.go` has table tests for the path (base variables,
`:params`, `{params}`, dotted and dynamic variables, filters, mixed
segments, the base URL's path, the query, a host without a scheme), the
headers left out (hop-by-hop and `Connection`-named, framing, the decoded
and the kept `Content-Encoding`, `Date`, invalid fields), every body kind
(JSON, no `Content-Type`, binary, bad Base64, empty, 204, clipped, a
custom reason phrase), masking again (headers, a text and a binary body),
and the merge modes and limits. `mockhttp`'s `TestBase64Body` serves a
Base64 body wrapped over lines and checks the diagnostics. The servers
service's `TestMockRouteFromExampleEndToEnd` sends real requests through
the HTTP service to an upstream server (a gzip-compressed JSON body with
cookies and an echoed secret, and a binary body), makes response examples
of the recorded entries and routes of those, starts a mock server with
them and checks that it answers with the same status, header fields and
body bytes, but for the documented differences and the masked secret;
`TestMockRouteFromExampleModes` covers the modes through the service.

`matcher_test.go` has table tests for parameters, constraints, wildcards,
catch-alls, overlaps, methods, 405, trailing slashes, case,
percent-encoding and diagnostics. `TestInsertionOrderIndependence` shuffles
a route set with many overlaps and checks every result is the same.
`FuzzMatch` matches random paths and checks that `Match` does not panic,
and that the winning route, compiled alone, matches the same path with
the same result.

`mockhttp`'s `template_internal_test.go` covers every `request.` name,
missing values, escaping, the rendered headers and every kind of render
error; `template_test.go` sends real requests to a running server with
templated default and other responses, a wildcard, an untemplated body with
`{{`, and a render error (500 and the traffic log's error event). The
servers service's `TestMockTemplated` checks the flag through the DTOs and
`MockTemplateNames`.

`mockhttp`'s `responses_test.go` covers the responses against a running
server: every source and operator, first match wins, repeated values,
numbers compared as numbers, a body that is not JSON, the header by name,
letter case, `default` and status (one a response has, one none has),
unknown values (400 with the list of names), a body larger than the log
keeps, and the diagnostics and limits. The servers service test
`TestMockConditionalResponsesEndToEnd` does the same through
`ServersService` and checks the response names in the traffic.

`mockopenapi` has golden dumps of the routes and diagnostics of the
reader's six petstore fixtures (2.0, 3.0 and 3.1, YAML and JSON),
`recursive.yaml` (self-, mutually and `allOf`-recursive schemas, an XML
expression tree, a chain deeper than the generator goes) and `edge.yaml`
(mixed segments, renamed and repeated parameters, header examples, every
body kind, duplicate routes, missing responses) in
`internal/core/servers/mockopenapi/testdata/`; every generated route is
checked with `mockhttp.Check` and every JSON body with `json.Valid`.
`TestConvertPath` and `FuzzConvertPath` cover the path conversion,
`TestMerge` the two modes, clashing IDs and the route limit. The servers
service test `TestGenerateMockRoutesEndToEnd` starts a mock server from
each petstore and calls every generated route with `net/http`, checking
status, body and `Content-Type`.
