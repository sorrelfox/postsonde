# Cookie jar

`internal/core/cookies` is PostSonde's cookie store (CK-01): cookies from
responses are stored and sent back automatically, following RFC 6265 and
the rfc6265bis updates browsers implement. Unlike `net/http/cookiejar`, the
jar can be listed (for the cookie manager, CK-02) and saved in the
workspace's local directory.

```
httpclient.Send
   │ http.Client (no Jar) ── CheckRedirect
   ▼
cookieTransport.RoundTrip, once per hop (first request and each redirect)
   │ Cookie rows + jar.Cookies(url)      → request copy, Response.RequestCookie
   │ base transport
   │ jar.SetCookieLines(url, Set-Cookie) → stored before the next hop
   ▼
cookies.Jar ── onChange ──▶ cookies.Store ── debounced ──▶ <local>/cookies.json
```

## API

| Item | Purpose |
|---|---|
| `New(Options) *Jar` | An empty in-memory jar. `Options{Now, MaxPerSite, MaxTotal, SaveDelay, OnSaveError}`. |
| `(*Jar).SetCookieLines(u, lines) (stored, rejected []string)` | Store the raw Set-Cookie values of a response to `u`. Returns cookie names, and `name: reason` for ignored ones (never values), for the timeline. |
| `(*Jar).StoreLines(u, lines) []Outcome` | The same, returning `Outcome{Name, Reason}` per value in order (`Reason` "" when stored), for the response's Cookies tab (HTTP-32). |
| `ParseSetCookie(line) (SetCookie, Reason, bool)` | Parse one Set-Cookie value as the jar does, without storing it: `SetCookie{Name, Value, Expires, HasExpires, MaxAge, HasMaxAge, Domain, Path, Secure, HTTPOnly, SameSite, Notes}`. `Notes` are sentences for the attribute values that were ignored (a bad date or number, an empty Domain, a relative Path, an unknown SameSite, one over 1024 bytes). Fails with the `Reason` when the jar ignores the whole cookie. |
| `(*Jar).Cookies(u) []*http.Cookie`, `Header(u) string` | The cookies to send to `u`, in header order; `Header` serializes them byte for byte. |
| `(*Jar).SetCookies(u, []*http.Cookie)` | `http.CookieJar`, for other `net/http` clients. Uses `Cookie.Raw` when set. |
| `(*Jar).All() []Cookie`, `Len`, `Delete(Cookie)`, `Clear` | Listing and deleting. |
| `(*Jar).Set(old *Cookie, c Cookie) (Cookie, error)`, `DeleteDomain(domain) int` | The cookie manager's add/edit (replacing `old` by identity) and "clear domain" ([below](#cookie-manager)). |
| `Cookie{Name, Value, Domain, HostOnly, Path, Secure, HTTPOnly, SameSite, Persistent, Expires, Created, LastAccess}` | A stored cookie (§5.3). |
| `Reason*` constants | Why a cookie was ignored: `invalid`, `too_large`, `domain_mismatch`, `public_suffix`, `insecure`, `overwrite_secure`, `prefix`, `url`. |
| `Open(localDir, Options) (*Store, error)`, `Store.Jar/Flush/Close/Problem/Path` | A jar loaded from and saved to `<localDir>/cookies.json` (the shared jar). |
| `OpenJar(localDir, jar, Options)`, `JarPath(localDir, jar)` | A named jar, `<localDir>/cookies/<jar>.json` (`JarsDir`); `jar` "" is the shared jar. A name that is not a plain file name (letters, digits, `.`, `_`, `-`, not starting with `.`, at most 128 bytes) fails with `cookies.invalid_jar`. |
| `NewRegistry(Options)`, `Registry.Open(localDir)`, `OpenJar(localDir, jar)`, `Close(localDir)`, `CloseAll()` | One `Store` per jar of a workspace local directory, shared by every send. `Close(localDir)` saves and forgets all of that directory's jars. |
| `ReadSettings(localDir) Settings`, `WriteSettings(localDir, Settings)` | The workspace's cookie settings, `<localDir>/cookie-settings.json`: `Settings{PerEnvironment}` ([below](#jars-per-workspace-and-environment)). |
| `httpclient.CookieJar`, `(*httpclient.Client).WithCookieJar(jar)` | Attach a jar to a client (below). |

## Using it from a send

The HTTP client has no jar by default: the request on the wire is still
exactly the configured one ([http-client.md](http-client.md)). The jar is an
explicit component.

In the app, the jars belong to `internal/app/services/cookies.Jars`,
created once in `internal/app/services.go` (`cookieJars`) over the one
`cookies.Registry` and the `workspace.Manager`. Every kind of send takes
that `*Jars` (HTTP, GraphQL and SSE sends, GraphQL subscriptions over
WebSocket and SSE, copy as cURL; WebSocket and Socket.IO through
`websocket.WorkspaceJars(cookieJars)`) and never opens a registry of its
own, so what the manager shows and edits is exactly what the next request
sends, and switching project or environment switches the jar for all of
them:

| `*appcookies.Jars` method | Use |
|---|---|
| `Jar() (*cookies.Jar, error)` | The jar sends use now ([below](#jars-per-workspace-and-environment)), for `httpclient.Client.WithCookieJar`: the client then adds the Cookie header and stores Set-Cookie on every hop, redirects included (preferred). |
| `Header(u *url.URL) (string, error)` | The Cookie header value for `u` ("" when nothing matches), for a path that builds the request itself. |
| `SetCookieLines(u, lines) (stored, rejected []string, err error)` | Store the raw `Set-Cookie` values of a response to `u`; names and `name: reason` for the timeline, never values. |
| `Locate() (Location, error)` | Which jar that is: `LocalDir`, `Workspace` (name), `PerEnvironment`, `EnvironmentID` and `Environment` (name; "" for the shared jar), `Jar` (the registry name). |
| `Settings(localDir)`, `SetPerEnvironment(on) error` | The project's cookie settings (cached per local directory) and the switch. |
| `Store() (*cookies.Store, error)`, `CloseAll() error` | The store (for `Problem`); save and forget every jar and the cached settings. |

All of them fail with `workspace.none_open` when no project is open.

## Jars per workspace and environment

CK-03. Every project has its own jars, in its own local directory, so a
cookie set while one project is open is never sent from another. Within a
project, the setting **Separate cookies per environment** (off by
default; the checkbox in the cookie manager) picks the jar:

| Setting | Active environment | Jar | File |
|---|---|---|---|
| off | any, or none | the project's shared jar | `<local>/cookies.json` |
| on | none | the shared jar | `<local>/cookies.json` |
| on | environment E | E's jar | `<local>/cookies/<key of E's ID>.json` |

- **Migration.** `cookies.json` is the jar every version before CK-03
  used; it stays the shared jar, so existing cookies keep working with the
  setting off, and with it on they are the "No environment" jar. Turning
  the setting on or off moves no cookie: each jar keeps its cookies for
  when it is used again.
- **The key** of an environment is `environments.SecretsKey(id)`: the ID
  itself when it is a plain generated `env_…` ID, else `x.` and a hash, so
  a hand-edited ID cannot escape the directory.
- **The setting** is kept in `<local>/cookie-settings.json`
  (`{"version": 1, "perEnvironment": true}`, `0600`), not in the project:
  like the active environment and the cookies, it is the user's own. A
  missing or unreadable file is "off".
- **When it applies.** The jar is looked up at the start of every send,
  handshake and connect, from the active environment then. A long-lived
  connection opened earlier (a WebSocket, Socket.IO or GraphQL
  subscription connection, an SSE stream with its reconnects) **keeps the
  jar it started with**; connections opened after a switch use the new
  one. Switching the project closes the realtime connections anyway.
- **Deleting an environment** leaves its jar file; an environment created
  later has a new ID and so a new, empty jar. Clearing a project's local
  data removes them all.
- An active environment that cannot be read (its secrets file is broken)
  fails the send with the environment's error, as its variables would.

Per request (HTTP-26), `settings.sendCookies: false` and
`settings.storeCookies: false` of an http, graphql or sse request file
([file-format.md](file-format.md#settings)) turn one direction off; the
client keeps its jar and `httpclient.Settings.SkipSendingCookies` /
`SkipStoringCookies` decide per send:

| `sendCookies` | `storeCookies` | On every hop |
|---|---|---|
| missing / `true` | missing / `true` | the jar's cookies are added, `Set-Cookie` values stored |
| `false` | missing / `true` | only the `Cookie` rows are sent; `Set-Cookie` values are stored |
| missing / `true` | `false` | the jar's cookies are added; nothing is stored, the timeline says `none stored, off for this request: a, b` and the Cookies tab "Not stored" |
| `false` | `false` | the jar is neither read nor written |

The `request_options` timeline event names the switches that are off. A
WebSocket request's `sendCookies: false` leaves the jar out of the
handshake entirely, and so does a GraphQL request's for its subscription
handshake.

```go
jar, err := cookieJars.Jar()                   // the open project's jar
client := base.WithCookieJar(jar)              // shares base's connections
resp, err := client.Send(ctx, req)
```

The core API underneath, for code outside the app layer:

```go
store, err := registry.Open(localDir)          // workspace.Manager.LocalDir()
client := base.WithCookieJar(store.Jar())
// on workspace close: registry.Close(localDir); on shutdown: registry.CloseAll()
```

`WithCookieJar(nil)` gives a client without a jar. Per-environment jars
are more `Store`s in the registry (`registry.OpenJar(localDir, key)`).

What the client does with a jar, on every hop:

- **Sending:** the jar's matching cookies are appended to the Cookie header.
  With `Cookie` rows, the jar's cookies go after the first row's value (in
  the row's spelling), and a jar cookie whose **name a row already sends is
  left out**: the user's explicit value wins.
- **Storing:** the response's `Set-Cookie` values are stored when the
  response headers arrive, before `net/http` follows a redirect, so a cookie
  set by a 302 is sent to the redirect target.
- **Redirects:** each hop asks the jar again for its own URL, so cookies
  follow domain and path rules across hosts. The `Cookie` rows are still
  dropped by `net/http` when the host changes to one that is not a
  subdomain.
- **What was sent:** `Response.RequestCookie` and `Hop.RequestCookie` hold
  the Cookie header each hop sent (rows and jar). The timeline gets
  `cookies_sent` (names of jar cookies added) and `cookies_stored`
  (`stored a, b; ignored c: domain_mismatch`). Values never appear in
  events.

`internal/app/services.go` creates one `Registry` and one `Jars` over it.
The WebSocket and Socket.IO services reach it through
`websocket.WorkspaceJars(cookieJars)`, which asks `cookieJars` for the
jar on each connect; the WebSocket service saves and forgets every store
(`cookieJars.CloseAll`) when the workspace changes and on shutdown
([websocket.md](websocket.md#service-websocketservice)). The HTTP send
service and the cookie manager take `cookieJars` directly.

## Cookie manager

CK-02: the user views, adds, edits and deletes the cookies of the jar
sends use now (the open project's, or its active environment's, CK-03),
grouped by domain. The UI is a dialog opened from the status bar's
"Cookies" button ([frontend.md](frontend.md#cookie-manager)); the service
is `CookiesService` in `internal/app/services/cookies`, over the same
`Jars` as sends. Every change goes to the live jar and is saved like a
cookie from a response (debounced, to that jar's file), so it applies to
the next request, restarts included. The dialog names the jar and has the
"Separate cookies per environment" checkbox; it reloads when the active
environment or the project changes. A change made in the manager goes to
the jar current when it is saved.

| Method | Does |
|---|---|
| `List() CookiesDTO` | Every unexpired cookie (sorted by domain, path, name), `jar` (`workspace`, `perEnvironment`, `environmentId`, `environment`: which jar this is; the environment members are "" for the shared jar) and `problem` (`code`, `message`, `hint`) when the cookie file could not be loaded. |
| `SetPerEnvironment(on) CookiesDTO` | Turns "Separate cookies per environment" on or off for the open project; returns the jar sends use from then on, as `List`. Fails with `cookies.local_data_failed` when the setting cannot be saved. |
| `Set({key, cookie}) CookieDTO` | Adds a cookie (`key` "") or replaces the cookie with `key`; returns it as stored. |
| `Delete(key)` | Deletes one cookie; one already gone is not an error. |
| `DeleteDomain(domain) int` | Deletes the domain's group; returns the count. |
| `Clear()` | Deletes every cookie of the jar shown; other environments' jars keep theirs. |

`CookieDTO` has `name`, `value`, `domain`, `hostOnly`, `path`, `secure`,
`httpOnly`, `sameSite` (`Strict`, `Lax`, `None` or ""), `expires` (RFC
3339, "" for a session cookie), `created`, `lastAccess`, and:

- **`key`**: the cookie's identity (name, domain, host-only flag, path),
  base64url of the raw bytes. Opaque to the UI; it is how an edit that
  renames a cookie, or a cookie with bytes that are not UTF-8, is found.
- **`binary`**: the name, value, domain or path is not UTF-8, so the
  strings are approximate; the UI only offers Delete for it.

**Groups.** A group is all cookies with the same `Domain`, host-only and
domain cookies together (the table marks domain cookies "+ subdomains").
"Clear domain" deletes exactly that group, not subdomains' groups.

**Edits** (`(*Jar).Set`). There is no request URL, so the channel rules
(Secure from http, "Leave Secure Cookies Alone") do not apply; the cookie
must still be one the jar could hold. Errors are `invalid_argument` with
code `cookies.invalid_cookie` and a `field` detail (`name`, `value`,
`domain`, `path`, `sameSite`, `expires`), shown under that field:

- name or value empty together; control characters (tab included) or `;`
  in either, `=` or surrounding spaces in the name; name + value over
  4096 bytes;
- domain empty, over 1024 bytes, or containing spaces, `/`, `\`, `?`, `#`,
  `@`, `,`, `;` or `..` (a leading dot is dropped, it is lower-cased); a
  public suffix unless host-only; an IP address is always host-only;
- path not starting with `/` (empty means `/`), over 1024 bytes, or with
  `;`;
- an expiry in the past (Expires over 400 days is capped, as from a
  response); a session cookie has no expiry;
- the `__Secure-` / `__Host-` prefix rules.

A new identity that another cookie has is `already_exists`
(`cookies.exists`); editing a cookie that is gone (the server deleted it,
or it expired) is `not_found` (`cookies.not_found`), and the UI reloads.
An edit keeps the replaced cookie's creation time, so its place in the
Cookie header does not change. A key that cannot be decoded is
`cookies.invalid_key`.

The service has no events: the manager loads the list when it opens,
after each change and on Refresh. Logs name the cookie and domain, never
the value.

## Rules

### Parsing (§5.2)

Lenient, like browsers:

- Attribute names are case-insensitive; unknown attributes and invalid
  values are ignored; the last valid attribute of a name wins.
- A name-value pair without `=` is a cookie with an **empty name**
  (rfc6265bis), sent as its value alone. Empty name and value: ignored.
- Control characters (other than tab) anywhere: the cookie is ignored.
- `Expires` uses the §5.1.1 date algorithm (RFC 1123, RFC 850, asctime and
  most variants; two-digit years 70–99 are 19xx, 00–69 are 20xx; dates
  before 1601 or that do not exist are invalid).
- `Max-Age` wins over `Expires`. `Max-Age` ≤ 0 or a past `Expires` deletes
  the cookie.
- `Domain`: a leading dot is dropped, lower-cased; an empty value is
  ignored. `Path`: empty or not starting with `/` means the default path.
- `SameSite`: `Strict`, `Lax`, `None` (any case); other values are "default".

### Storage (§5.3)

- **Host-only vs domain:** without `Domain`, the cookie is host-only (sent
  to that exact host). With `Domain`, the host must domain-match it
  (identical, or a subdomain), and the cookie goes to the domain and all
  subdomains.
- **IP addresses** are always host-only; a `Domain` attribute on an IP host
  must be the IP itself.
- **Default path:** the request path up to its last `/` (`/docs/page` →
  `/docs`), or `/`.
- **Identity:** name, domain, host-only flag and path (rfc6265bis). A new
  cookie with the same identity replaces the old one and keeps its creation
  time (so header order is stable).
- **Secure** cookies can only be set from a secure channel, and an insecure
  channel cannot overwrite or shadow a Secure cookie of the same name
  ("Leave Secure Cookies Alone").
- **Prefixes:** `__Secure-` needs `Secure`; `__Host-` needs `Secure`, `Path=/`
  and no `Domain` (case-insensitive, as in current browsers).
- **Scheme:** only `http`, `https`, `ws` and `wss` URLs with a host.

### Sending (§5.4)

A cookie is sent when the host matches (exactly for host-only, domain-match
otherwise), the request path path-matches the cookie path (`/docs` matches
`/docs`, `/docs/`, `/docs/x`, not `/docsets`), it has not expired, and, if
Secure, the channel is secure. Order: longer paths first, then earlier
creation. Sending updates the access time used for eviction.

### Secure channels

`https` and `wss`, plus — as browsers treat them as "potentially
trustworthy" — `http` to `localhost`, `*.localhost` and loopback addresses
(`127.0.0.0/8`, `::1`). So a local dev server can set and receive Secure
cookies over plain http. Everything else over http neither sets nor gets
Secure cookies.

### SameSite and HttpOnly

PostSonde is not a browser: every request is a top-level request the user
started, and there is no "site" it comes from. So:

- **SameSite is stored and shown, but never restricts sending or storing.**
  `SameSite=None` without `Secure` is also accepted (browsers reject it;
  rejecting it here would only hide a server bug the user may be testing).
- **HttpOnly** is stored. HTTP requests always send HttpOnly cookies. It
  will matter once scripts (pre-request/test scripts) can read the jar:
  scripts must not see HttpOnly cookies.

## Public suffixes

A `Domain` attribute that names a public suffix (`com`, `co.uk`,
`github.io`) would set a "supercookie" for unrelated sites, so it is
rejected (or, when it equals the request host, the cookie becomes
host-only). Browsers use the Public Suffix List; `golang.org/x/net/publicsuffix`
is not allowed in `internal/core` (standard library only). PostSonde uses a
heuristic instead:

- every **single-label** domain (`com`, `dev`, `internal`), except
  `localhost` (so `app.localhost` and `api.localhost` can share cookies);
- **`<generic>.<cc>`**: a generic second-level label (`ac`, `co`, `com`,
  `edu`, `gob`, `go`, `gov`, `govt`, `ltd`, `me`, `mil`, `ne`, `net`, `nic`,
  `nom`, `or`, `org`, `plc`, `sch`) under a two-letter country code
  (`co.uk`, `com.au`, `ne.jp`);
- a short list of **shared-hosting suffixes** (`github.io`,
  `vercel.app`, `netlify.app`, `pages.dev`, `workers.dev`, `herokuapp.com`,
  `appspot.com`, `web.app`, `firebaseapp.com`, `ngrok.io`, `ngrok-free.app`,
  `azurewebsites.net`, `cloudfront.net`, `s3.amazonaws.com`, …; see
  `privateSuffixes` in `domain.go`).

**Limitation:** suffixes not covered (e.g. `kyoto.jp`, `pvt.k12.ma.us`,
wildcard rules, most private suffixes) are treated as registrable, so a
server there could set a cookie for sibling sites. The risk is small in an
API tool, where the user picks the hosts. The same heuristic groups cookies
per site for the limits. Follow-up: embed the full list, generated into a
Go table by a stdlib-only script, and refresh it with releases.

Internationalized host names are compared as given (no punycode
conversion, which also needs `x/net/idna`); a server that answers a
Unicode URL with an ASCII `Domain` gets `domain_mismatch`.

## Limits

| Limit | Value | Source |
|---|---|---|
| Name + value | 4096 bytes, longer cookies ignored | rfc6265bis, all browsers |
| Attribute value | 1024 bytes, longer attributes ignored | rfc6265bis |
| Lifetime | Expires / Max-Age capped at 400 days | rfc6265bis, Chrome |
| Per site | 180 cookies (`MaxPerSite`) | Chrome |
| Total | 3000 cookies (`MaxTotal`) | Chrome; RFC 6265 asks for at least 3000 |

When a store goes over a limit, expired cookies go first, then the least
recently accessed cookies of the site over its limit, then the least
recently accessed overall (§5.3 step 12). Unlike Chrome, Secure cookies get
no priority and the jar evicts down to the limit, not below it.

## Persistence

- File: `<data>/workspaces/<id>/cookies.json` (the workspace's local
  directory, [workspace.md](workspace.md)) for the shared jar, and
  `<data>/workspaces/<id>/cookies/<environment key>.json` (directory 0700)
  for each environment's jar ([above](#jars-per-workspace-and-environment)),
  all in the same format; mode 0600, written with `atomicfile` in the
  stablejson format. Never in the project folder.
- Written one second (`SaveDelay`) after a change, so a burst of responses
  is written once, and on `Flush`/`Close`. Access-time updates alone do not
  schedule a write; they are saved with the next write or on close.
- **Session cookies** (no `Expires`/`Max-Age`) are saved and survive a
  restart, as in Postman and Insomnia: an API session should not end when
  the app restarts. They stay until the server deletes them or the user
  clears the jar. `Persistent: false` marks them for the manager UI.
- Expired cookies and invalid entries are dropped on load.
- A file that is not valid JSON, or has a version other than 1, is **left
  untouched**: the jar works in memory and is not saved, and
  `Store.Problem()` returns `cookies.file_invalid` or `cookies.file_newer`
  for the UI. A file that exists but cannot be read makes `Open` fail with
  `cookies.local_data_failed`, as do failed writes.

```json
{
  "version": 1,
  "cookies": [
    {
      "name": "session",
      "value": "abc",
      "domain": "api.example.com",
      "hostOnly": true,
      "path": "/v1",
      "secure": true,
      "httpOnly": true,
      "sameSite": "Lax",
      "persistent": false,
      "created": "2026-09-16T10:00:00Z",
      "lastAccess": "2026-09-16T10:05:00Z"
    }
  ]
}
```

Cookies are in creation order. `expires` is present for persistent cookies.
Header bytes need not be UTF-8: a cookie whose name, value, domain or path
is not valid UTF-8 has `"encoding": "base64"` and those four fields
base64-encoded.

Cookie values are secrets as far as logs go: they are never logged, and
error messages and timeline events carry names only.

### Clearing

Settings, Privacy, Local data (PRV-13,
[privacy.md](privacy.md#clearing-local-data)) removes every jar of a
project, or of every project: `Registry.Forget(localDir)` first empties
each open jar of the directory in memory and marks its store so it never
writes again (a send still holding the jar sends and stores nothing that
lands on disk), then the files go. The next send opens the jar afresh.
`cookie-settings.json` is a setting and stays.

## Tests

`go test -race ./internal/core/cookies ./internal/core/httpclient ./internal/app/services/cookies`:

- `parse_test.go`: Set-Cookie parsing (attributes, case, empty names, limits,
  control characters), the §5.1.1 date algorithm, and
  `FuzzParseSetCookie` (`go test -fuzz FuzzParseSetCookie ./internal/core/cookies`).
- `jar_test.go`: §5.1.3 domain matching, §5.1.4 default path and path
  matching, the §3.1 examples, host-only vs domain cookies, public
  suffixes, IP hosts, Secure over http and localhost, prefixes, overwrite
  and deletion (Max-Age=0, past Expires, precedence), expiry on a manual
  clock and the 400-day cap, header order, SameSite, limits and eviction,
  `http.CookieJar`.
- `store_test.go`: restart round trip (session, persistent, binary values,
  access times, 0600), file format, debounced saves, bad files never
  overwritten, unwritable directories, the registry; named jars apart from
  the shared one and from each other, invalid names, `Close(localDir)`
  closing them all (`TestNamedJars`); the settings file (`TestSettings`).
- `edit_test.go`: the manager's `Set` (add, replace, rename, duplicates,
  gone cookies, header order kept), validation, expiry and the cap,
  `DeleteDomain`.
- `internal/app/services/cookies`: CK-02's "done when" — cookies stored by
  a response, then edited, added, renamed, deleted and cleared through the
  service, are what the next `httpclient` send over `httptest` carries
  (and what `Jars.Header` returns); errors, persistence across a reopen,
  a bad cookie file, keys. `jars_test.go` is CK-03's "done when" for HTTP:
  a cookie set over `httptest` in project A / environment X is not sent
  from project B nor, with the setting on, from environment Y; with it off
  environments share; "no environment" uses the shared jar, Clear clears
  only the jar shown, `List` names the jar, and a `cookies.json` from before
  CK-03 is the shared jar (`TestSharedJarMigration`).
- `internal/app/services/websocket/jars_test.go`: the same for the
  WebSocket handshake, through `WorkspaceJars(*appcookies.Jars)`.
- Frontend: `lib/cookies.test.ts` (`jarLabel`) and
  `components/cookies/CookieManager.test.tsx` (the jar line, reloading on
  `environments:changed`, the setting's checkbox and its error, Clear all
  naming the jar).
- `httpclient/cookies_test.go`: end to end over `httptest` — cookies set on
  redirects sent on the next hop, `RequestCookie` per hop, timeline events
  without values, rows winning over the jar, no jar, cross-host redirects,
  deletion, and a restart between two sends.
- `httpclient/options_test.go` (`TestCookieSwitches`) and
  `internal/app/services/http` (`TestSendRequestOptions`): the per-request
  switches (HTTP-26) — what the server receives, what the jar holds
  afterwards, the timeline events and the Cookies tab's jar state.
