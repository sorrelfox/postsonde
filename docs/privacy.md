# Privacy: no remote UI resources, user-initiated traffic, safe defaults

PostSonde's UI never loads anything from the network (PRV-03): no web fonts,
CDNs, remote images or iframes. Everything is bundled into `frontend/dist`
and embedded in the binary. Two things enforce that:

1. a strict **Content Security Policy** at run time, which the webview
   enforces;
2. a **build check** (`frontend/scripts/check-dist.mjs`) that fails CI when a
   remote URL shows up in the built frontend.

Requests the user sends go through the Go core, not the webview, so the CSP
doesn't affect them.

## Content Security Policy

The policy lives in one file, [`frontend/csp.json`](../frontend/csp.json),
one directive per entry:

```
default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline';
img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self';
media-src 'self' data: blob:; worker-src 'self'; object-src 'none';
frame-src about:; base-uri 'none'; form-action 'none'
```

It is applied twice, from that file:

| Where | How | Why |
|---|---|---|
| Response header | `internal/app/csp.go`: a Wails asset server middleware sets `Content-Security-Policy` on every response (index.html, assets, runtime endpoints). `cmd/postsonde` passes `frontend.ContentSecurityPolicy` (embedded `csp.json`) in `app.Config`; `app.Run` refuses an empty policy. | Covers the document before it is parsed. |
| `<meta http-equiv>` in `dist/index.html` | `vite.config.ts` (`postsonde:csp-meta`, build only) puts it first in `<head>`. | Works wherever `dist/` is served, even if a webview ignored custom-scheme response headers. |

Both carry the same policy, so the effective policy is the same.

### The directives

- **`default-src 'self'`**: anything not listed below loads only from the
  app's own origin. `'self'` is `wails://localhost` on macOS and Linux and
  `http://wails.localhost` on Windows (Wails v3.0.0 asset server).
- **`script-src 'self'`**: no inline scripts, no `eval`. The theme bootstrap
  that sets `data-theme` before first paint is therefore a file,
  `frontend/public/theme-init.js`, not an inline `<script>`.
- **`style-src 'self' 'unsafe-inline'`**: needed. CodeMirror (via
  `style-mod`) writes its theme into `<style>` elements, and it and React set
  `style` attributes. A nonce isn't an option: the `<meta>` tag is static and
  attribute styles can't carry one. The risk is CSS injection only (no script),
  and with every other directive limited to the app, injected CSS can't load
  or send anything remote. Custom themes (UI-05) need nothing more: their
  colours are set one custom property at a time through CSSOM
  (`style.setProperty`), only for known tokens and `#rrggbb` values, never as
  CSS text ([themes.md](themes.md#how-a-theme-is-applied)).
- **`img-src` / `media-src 'self' data: blob:`**: bundled images, inline SVG
  data URLs (CodeMirror uses them) and in-memory previews: the image
  response preview is an `<img>` of a `blob:` URL of the body's bytes
  (HTTP-35, below). Unchanged by the preview.
- **`font-src 'self' data:`**: the bundled fonts; `data:` in case Vite
  inlines a small one.
- **`connect-src 'self'`**: the Wails runtime IPC is `fetch` to
  `<origin>/wails/runtime` (and `/wails/stream/...`), same origin.
- **`worker-src 'self'`**: none today; bundled workers only.
- **`object-src 'none'`**: no plugins (so no PDF preview either).
- **`frame-src about:`**: the only frame the UI has is the sandboxed HTML
  response preview, an `about:srcdoc` document (below). `about:` matches
  only `about:srcdoc` and `about:blank`, documents that are built in memory
  and fetch nothing. Every other frame load or navigation, `http(s):`, the
  app's own origin, `blob:` and `data:` included, is refused, and
  `frame-src` is checked against this (the parent's) policy for any
  navigation of the frame, whoever starts it: a link clicked in a preview
  stays blank. Chromium (WebView2) does not apply `frame-src` to
  `about:srcdoc` at all, so `'none'` would do there; `about:` is listed so a
  webview that does check it (WebKit) still renders the preview. It used to
  be `'none'`; nothing else changed. `check-dist.test.mjs` fails if any
  other frame or `child-src` source appears.
- **`base-uri 'none'`, `form-action 'none'`**: no `<base>` hijacking, no form
  posts anywhere.
- No `unsafe-eval`, no remote hosts, no wildcards. `frontend/assets_test.go`
  and `check-dist.test.mjs` fail if any of those appear.

Scripts Wails itself runs in the page through the native webview APIs
(`evaluateJavaScript`, `ExecuteScript`) are not subject to the page's CSP in
WebKit or WebView2, and `@wailsio/runtime` uses no `eval`.

### `wails3 dev`

The Vite dev server injects an inline module script (the React Refresh
preamble) and opens its HMR WebSocket straight to the dev server, which the
strict policy blocks. So:

- the `<meta>` tag is added only by `vite build`, never by the dev server;
- when the Go app is **not** built with `-tags production` **and**
  `FRONTEND_DEVSERVER_URL` is set (what `wails3 dev` does), the header adds
  `'unsafe-inline'` to `script-src` and
  `ws://localhost:* ws://127.0.0.1:* wss://localhost:* wss://127.0.0.1:*` to
  `connect-src` (`devPolicy` in `internal/app/csp.go`).

Production builds (`-tags production`) always send the strict policy, whatever
the environment. Dev mode still blocks every non-loopback host.

### Checked in the app window

The policy is enforced inside the real web view, not only in a browser
against `dist/`. Checked so far:

| Date | OS | Web view | Wails | Commit | Result |
|---|---|---|---|---|---|
| 2026-11-08 | Fedora 44, KDE Plasma 6.8.2 (Wayland) | WebKitGTK 2.54.2 (`webkitgtk6.0`), GTK 4.22.5 | v3.0.0 | 72bc3b99 (+ this check's Taskfile fix) | Enforced, header and `<meta>` each on their own. `wails3 dev` works after a fix (below). |
| | macOS | WKWebView | | | not checked yet |
| | Windows | WebView2 | | | not checked yet |

How it was checked on Linux (the release build has no inspector, so with
throwaway builds that are not committed):

1. `make build`, then three probe binaries built from the same tree with
   `go build -tags production`, each with a `csp-probe.js` (a same-origin
   file, so `script-src 'self'` lets it run) added to `dist/`:
   - **P1**: the release `index.html` and header, unchanged apart from the
     probe;
   - **P2**: the `<meta>` tag removed, so only the response header applies;
   - **P3** (control): `<meta>` removed and the header replaced by
     `default-src * data: blob: 'unsafe-inline' 'unsafe-eval'`.
2. The probe tries one load per directive against `http://127.0.0.1:18948`,
   a loopback listener standing in for a remote host that logs every
   request: `fetch`, `WebSocket`, `sendBeacon`, `<img>`, `<script src>`, an
   inline `<script>`, `eval`, `<link rel=stylesheet>`, an `@font-face` in an
   inline `<style>`, `<iframe>`, `<object>` and `<audio>`, plus a
   same-origin `fetch("/theme-init.js")` that must work. It records each
   outcome and every `securitypolicyviolation` event in an `<h1>`, read
   through the accessibility tree (AT-SPI), since there are no devtools.
3. Each binary ran with `POSTSONDE_HOME` and the `XDG_*` directories in a
   scratch folder.

| Build | Probe result | Violations | Listener |
|---|---|---|---|
| P1 (release: header + `<meta>`) | everything blocked; same-origin fetch ok | 26: P2's 13, once per policy | no request |
| P2 (header only) | everything blocked; same-origin fetch ok | 13: `connect-src` ×3 (fetch, WebSocket, beacon), `img-src`, `script-src-elem` ×2 (remote, inline), `script-src` (eval), `style-src-elem`, `font-src`, `frame-src`, `media-src`, `object-src` ×2 | no request |
| P3 (control, permissive) | every fetch, script, stylesheet and image loaded; inline script and eval ran | 0 | a request for each of the 11 probe URLs, `Origin: wails://localhost` |

So WebKitGTK enforces the policy from the custom-scheme response header
(Wails passes the headers with `webkit_uri_scheme_response_set_http_headers`)
and from the `<meta>` tag, each on its own. `'self'` matches
`wails://localhost`: the bundle loads under `script-src 'self'` and the
same-origin fetch works. P3 shows that every probe really would reach the
network without the policy. The UI of the plain release build renders
(read through AT-SPI) and the strace runs in
[Manual checks](#manual-checks) show it loads nothing remote.

`wails3 dev` (`make dev`) on the same machine: at first it **did not
start**. The `build:frontend` task meant to skip `npm run check:dist` for
`DEV=true` builds with a command-level `if:`, but the Task library inside
wails3 (`github.com/wailsapp/task/v3` v3.40.1) has no `if:` on commands and
ignores the key silently. The check ran on the unminified dev bundle,
failed on the libraries' doc links, and dev mode stopped before the app
launched. The condition is now in the shell command
(`[ "{{.DEV}}" = "true" ] || npm run check:dist -s`), and
`frontend/taskfile_test.go` fails on any `if:` in the Taskfiles and checks
that the dist check stays behind that test. After the fix `make build`
still runs the check, and `wails3 dev`:

- renders the UI (the dev header's `'unsafe-inline'` lets Vite's React
  Refresh preamble run);
- has an established WebSocket from `WebKitNetworkProcess` to the Vite dev
  server on `127.0.0.1:9245` (`ss -tnp`), the HMR connection that the dev
  `connect-src` allows;
- hot reloads: an edit to `src/i18n/en/workspace.ts` showed up in the open
  window (`[vite] page reload`) without restarting the app.

## The build check

[`frontend/scripts/check-dist.mjs`](../frontend/scripts/check-dist.mjs)
(`npm run check:dist`, plain Node, no dependencies) walks `frontend/dist` and
fails when:

- a text asset (`.html`, `.js`, `.css`, `.svg`, `.json`, ...; fonts and images
  are skipped) contains `http://`, `https://`, `ws://`, `wss://` or `ftp://`
  URLs, or a protocol-relative `//host.tld` right after a quote, backtick or
  `url(`, unless the exact URL is allowlisted;
- `dist/index.html` lacks the CSP `<meta>`, or it differs from `csp.json`.

It prints `file:line:column: remote URL ...` for each hit.

### Allowlist

`ALLOWED` in the script, exact matches only, each with a reason. Today:

| URL | Why it's harmless |
|---|---|
| `http://www.w3.org/2000/svg`, `.../1999/xhtml`, `.../1999/xlink`, `.../1998/Math/MathML`, `.../XML/1998/namespace` | XML namespace identifiers (react-dom, CodeMirror); compared as strings, never fetched |
| `https://reactjs.org/docs/error-decoder.html?invariant=` | text in react-dom's minified error messages |
| `https://v3.wails.io/learn/build/#using-a-browser-for-development` | text in a `@wailsio/runtime` `console.warn` |

A new hit is a bug to fix by bundling the resource. Add an entry only for a
string that is provably never requested, with its reason.

### Where it runs

- `scripts/build-frontend.sh` (CI `frontend:build`, `make linux`): the `dist/`
  the Linux binaries embed is checked before it becomes an artifact.
- `scripts/check-frontend.sh` (CI `frontend:typecheck-test`,
  `make check-frontend`), after `npm run build`.
- `wails3 build` (`build:frontend` in `build/Taskfile.yml`) for production
  builds. `DEV=true` builds are unminified and keep library doc comments full
  of links, so they aren't checked. The skip is a shell test in the command,
  because wails3's Task ignores `if:` ([above](#checked-in-the-app-window)).

Its tests (`frontend/scripts/check-dist.test.mjs`, run by `npm test`) inject
remote URLs and a missing or altered `<meta>` into a temporary dist and
expect a failure.

### Limits

It's a text scan: it catches URLs written out in the bundle, not ones built
at run time (`"ht" + "tp://..."`) or fetched by user input. The CSP is the
runtime guard for those.

## Response preview (HTTP-35, PRV-04)

The response viewer previews image and HTML bodies
([frontend.md](frontend.md#request-editor), `components/response/ResponsePreview.tsx`,
`lib/preview.ts`). A response is data from any server, so the preview may
neither run its scripts nor let it reach the network (a tracking pixel, a
remote stylesheet or font would tell the server the response was viewed,
from where):

- **Images** are `<img>` elements whose `src` is a `blob:` URL of the
  body's bytes (`HTTPService.PreviewBody`, at most 16 MiB), revoked when
  the view goes. An image decoder fetches nothing. **SVG** is only ever an
  `<img>` source, never inserted into the page: SVG as an image runs no
  script and loads no external resource in every engine.
- **HTML** is an `<iframe sandbox="" srcdoc="…">`, never `src`:
  - `sandbox` with no token: scripts off (no `allow-scripts`), an opaque
    origin (no `allow-same-origin`: no access to the app's DOM, storage or
    Wails IPC), no forms, popups, modals, pointer lock or top-level
    navigation. The child-frame limits in the Wails v3.0.0 release notes
    (a frame can call bound methods over `/wails/runtime`; on Linux a
    same-origin frame can post `wails:` messages) need scripts or the
    app's origin, and this frame has neither.
  - The `srcdoc` starts with a `<meta http-equiv="Content-Security-Policy">`
    before any of the body: `default-src 'none'; img-src data: blob:;
    style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'`. No
    fetch of any kind (images, styles, `@import`, fonts, media, frames,
    `fetch`, pings, prefetch), only images and styles inline in the body.
    A srcdoc document also inherits the app's policy; both apply, so the
    stricter wins. A `<meta>` CSP in the body can only add restrictions.
  - Then `x-dns-prefetch-control: off` and `referrer: no-referrer`, and a
    light default page (`Canvas`/`CanvasText`) the body's own styles
    override.
  - Navigation of the frame is limited by the app's `frame-src about:`
    (above): links and `<meta http-equiv="refresh">` go nowhere.
- Nothing else loads: no PDF preview (it would need a plugin or a PDF
  viewer frame; `object-src 'none'`).

Verified in Chromium (the engine of WebView2) with the app policy and the
exact frame markup: a page with a remote stylesheet, `@import`, a font,
a background image, `<script src>`, an inline `fetch`, `<base>`, `<img>`,
`srcset`, `<picture>`, `<iframe>`, `<object>`, `<embed>`, `<video>`,
`<audio>`, an SVG `<image>`, `<a ping>`, a form, `onerror` and a meta
refresh rendered its text with **zero** requests to the stand-in server;
clicking its link and submitting its form were refused too. The same
check in WKWebView (macOS) and WebKitGTK (Linux) is manual: open an HTML
response that references a remote host and confirm the host sees no
request. `ResponsePreview.test.tsx` checks the attributes, that the CSP
comes first and that SVG is never inlined; `check-dist.test.mjs` the app
policy's frame sources.

## Markdown notes

Notes (COL-07) are text from project files, which may come from someone
else, rendered in the UI. The renderer (`components/common/Markdown.tsx`)
never creates an element that loads something (`<img>`, `<iframe>`,
`<link>`, `<script>`, `<style>`, `<object>`) or any URL attribute
(`href`, `src`): images show as placeholders with their alt text, raw
HTML as text. So a note cannot make the app fetch a tracking pixel, even
where the CSP would allow it (`img-src` allows `data:` and `blob:`). An
http(s) or mailto link opens in the system browser only after the user
confirms the URL; other schemes are inert. Details:
[collection.md](collection.md#notes).

## Go core: user-initiated traffic only (PRV-05)

The UI cannot reach the network; the Go core can, and it connects only for
something the user did: sending a request, connecting a TCP or WebSocket
client, a gRPC call, the OAuth 2.0 token request of a send (AUTH-08,
part of the send's operation) or of **Get new token** in the Auth tab,
schema introspection (Fetch schema / Refresh, or a request's schema
auto-refresh, which is off until the user turns it on in the request's
Settings tab, GQL-19), runners and the proxy connection test of the
settings screen (SOCKS-11). The authorization code and implicit
grants' sign-in (AUTH-07, AUTH-10) opens the system browser only when a
send or Get new token needs a new token, and waits for the redirect on a
loopback listener (`listen.Listen`, below) that is closed as soon as the
redirect came, the sign-in was cancelled or it timed out
([auth.md](auth.md#authorization-code-with-pkce)). For the implicit
grant the listener serves one page whose only script (allowed by its
hash in the page's CSP) posts the redirect's fragment back to the same
origin; it loads nothing, and a post from another origin is refused
([auth.md](auth.md#implicit-grant)). An OpenID Connect ID token is
checked without any network access: PostSonde never fetches the
issuer's keys or discovery document ([auth.md](auth.md#oidc-id-token)). No prefetch, update check or telemetry dials out.

This is enforced in the one dialer every protocol client uses: user
actions mark their context (`netx.WithUserOp`, set by `operation.Start` for
every operation and by the TCP, WebSocket and gRPC services for their
connections), and the dialer's `RequireUserOp` guard refuses any dial
without the mark with `permission_denied` / `netx.not_user_initiated`, and
logs it at warn level. Details, the table of marked paths and the rules for
new code are in [network.md](network.md#user-initiated-dials-only).
`TestDialerRefusesNonUserDials` in `internal/app` proves a dial without a
user operation never reaches the server, and `TestNoTrafficWithoutUserAction`
that the whole app, started headless, dials nothing until the user sends
([Audit](#audit-what-could-phone-home)). Local servers listen, they don't
dial, and are covered by the loopback rule (PRV-07,
[network.md](network.md#listening)).

## Connection log

PRV-06: so the guarantee above can be checked, the status bar shows how
many outgoing connections are open ("Idle" when none), and the console's
Connections view lists every connection the app opened, and every dial the
guard refused, with the user action that caused it
([frontend.md](frontend.md#network-activity),
[network.md](network.md#connection-log)). An idle app has none; a
request's HTTP keep-alive connection shows as open until the transport
closes it (at most 90 seconds idle).

The log names each target by `host:port` as the protocol client dialed it,
plus the local and remote IP addresses: never the URL, its path or query,
headers, credentials or payloads, so it holds no secret a URL could carry.
It keeps at most 500 connections in memory only (never on disk), and Clear
drops the ended ones. Copy puts the shown lines on the clipboard, and Save
writes them to a file the user picks, only when the user asks (masked as
below, [Copied and saved logs](#copied-and-saved-logs)).

## Audit: what could phone home

PRV-01 (no telemetry), PRV-02 (no phone-home), APP-02 (no account) and
APP-03 (offline) are stated for users in [PRIVACY.md](../PRIVACY.md). This
section records how they were checked against PostSonde's code, its
dependencies and the web views, and how to check again.

### Findings

Audited at Wails v3.0.0-beta.22, `@wailsio/runtime` 3.0.0-beta.22, and the
frontend packages in `frontend/package-lock.json` at the time of the audit.
Re-checked for Wails v3.0.0 and `@wailsio/runtime` 3.0.0 with the
greps below: the same hits as at beta.22, the npm package's code is
unchanged, and the scripts 3.0.0 adds to the web views (a guard on Linux
that drops `wails:` messages from child frames, a shim on Windows that
starts same-origin workers from workers through a blob URL) make no
requests of their own.

| Where | Found | Done |
|---|---|---|
| PostSonde's Go code | Only `netx.Direct` dials (`TestNoDirectDials`). DNS lookups happen only inside `internal/core/netx` (`httpproxy.go` looks up a port name, which reads the services database, not DNS). Processes started: `scutil`/`gsettings` (system proxy), `security`/`secret-tool` (keychain), PostSonde itself (restart after a data directory change). No updater, analytics, crash reporting or account code. | `TestNoDirectDials` now also bans `net.Lookup*` and `net.DefaultResolver`. |
| Wails `pkg/updater` | Linked into every Wails v3 app (`app.Updater`), with a periodic check loop and GitHub/HTTP providers, **inert until `Updater.Init`** is called. Its helper mode runs only when its own environment variables are set. Its providers aren't linked. | Never initialized. `TestNoWailsPhoneHome` fails if any non-test code imports `pkg/updater` or uses `.Updater`. |
| Wails default macOS menu | With no menu set, Wails installs `DefaultApplicationMenu`, whose **Help → Learn More loads `https://wails.io` into the app's window**. That's a top-level navigation, so the CSP doesn't stop it. | **Disabled:** on macOS `app.New` sets a menu bar of the same roles without Help (`applicationMenu`, `internal/app/privacy.go`). Windows and Linux windows get no menu bar unless one is set. `TestWebviewPrivacyOptions` checks that the roles leave out Help, and logs when Wails' Help menu changes. |
| Wails `Browser.OpenURL` | Hands a URL to the OS. | Used by `openURL` (OAuth 2.0 sign-in) and, in the frontend, by `openInBrowser` after the user confirms a note link. `TestNoWailsPhoneHome` allows `.OpenURL` only in `internal/app/dialogs.go`, and `.SetURL` nowhere. |
| Wails dev-only code | `application_dev.go` polls `FRONTEND_DEVSERVER_URL`, `assetserver/build_dev.go` proxies to the Vite dev server, and `roles_dev.go`/`webview_window_*_dev.go` enable the inspector. | All are behind `//go:build !production`. Release builds use `-tags production` (`build/*/Taskfile.yml`, `scripts/ci-build-linux.sh`). |
| Wails MCP server, server mode | `mcp_enabled.go` listens on a TCP port; `application_server.go` serves over HTTP. | Behind the `mcp` and `server` build tags, which PostSonde never sets. |
| Wails single instance, Linux IPC | D-Bus session bus (Linux), distributed notifications (macOS), window messages (Windows); `environment_linux.go` talks to Hyprland/Sway over Unix sockets for window placement. | Local only. Nothing to do. |
| WebView2 (Windows) | Wails passes `--disable-features=msSmartScreenProtection` (no SmartScreen lookups) and sets `AreDevToolsEnabled(false)` in production. The Edge runtime can still fetch component updates, Safe Browsing lists and variations, send Domain Reliability reports, and upload metrics. | **Added** `--disable-background-networking --disable-component-update --disable-domain-reliability --no-pings --metrics-recording-only` (`webview2BrowserArgs`, `WindowsOptions.AdditionalBrowserArgs`). Checked by `TestWebviewPrivacyOptions`. Whether the runtime honours every switch is left for the [manual checks](#manual-checks). |
| WKWebView (macOS) | Safe Browsing ("fraudulent website warnings") is off unless `EnableFraudulentWebsiteWarnings` is set. The inspector is off in production. | Set to `false` explicitly in `mainWindowOptions`. Checked by `TestWebviewPrivacyOptions`. |
| WebKitGTK (Linux) | No telemetry known. The inspector is enabled only with `DevToolsEnabled` or in debug builds. | Left as is; see the manual checks. |
| Frontend bundle | `check-dist` finds no remote URLs. Every `fetch` in `dist` is the Wails runtime IPC (`/wails/runtime`, same origin) or Vite's module preload of bundled assets. `EventSource`/`XMLHttpRequest` occur only as text (a code snippet, a header description). | The CSP's `connect-src 'self'` blocks anything else at run time. |
| npm packages | Runtime: React, CodeMirror/Lezer, `@wailsio/runtime`. None has telemetry. The only install script in the lock file is `fsevents` (a native file watcher used by the dev server, macOS only, not in the bundle). | Nothing to do. Build tools (npm, Vite, Vitest, the Go toolchain) may talk to their registries **on the build machine**; that isn't the app. |

### Automated checks

| Test | What it proves |
|---|---|
| `TestNoTrafficWithoutUserAction` (`internal/app/privacy_test.go`) | Starts every bound service headless as Wails would (`ServiceStartup`, then `ServiceShutdown` at the end), idles, loads the settings, creates, closes and reopens a project, creates requests of every kind, an environment, and reads every list the views show. Throughout, there are no entries in the connection log (refused dials included), no "dial refused" log line, no DNS query through the process's default resolver, and no connection to `HTTP_PROXY`/`HTTPS_PROXY` (pointed at a recording listener, to catch a stray default transport). Then the user sends one request, which becomes the only connection, marked `http.send`. |
| `TestStartupOffline` | The same first run with no network: every DNS query fails and the proxy variables point nowhere. Startup and every local call work. The user's send fails with an error in its history entry, and the app keeps working. |
| `TestNoAccount` | No bound service has a method for signing in, registering, licensing, upgrading, update checks, telemetry or crash reporting. `TestStartupOffline` shows the first run needs none. |
| `TestNoWailsPhoneHome` | No import of the Wails updater, no `.Updater`, `.OpenURL` only in `dialogs.go`, no `.SetURL`. |
| `TestWebviewPrivacyOptions` | The WebView2 switches, WKWebView Safe Browsing off, no inspector, the window loading the embedded UI, the macOS menu without Help. |
| `TestNoDirectDials` (`internal/core/netx`) | No dial, DNS lookup or default HTTP client outside `netx`. |
| `TestDialerRefusesNonUserDials` (`internal/app`) | The guard refuses an unmarked dial before it reaches the network. |
| `check-dist` | No remote URL in the built frontend, CSP `<meta>` present. |

What they can't prove: a dial to an IP literal by code that bypasses
both the dialer and the static rule, such as a dependency that dials
directly. The Go core has no third-party dependencies besides Wails
(PRV-12), and Wails was audited above. The web view runtimes are native
code, so only the manual checks cover them.

### Re-running the audit

After a Wails or frontend dependency update:

```sh
# Wails packages actually linked into the binary
go list -deps ./cmd/postsonde | grep wailsapp
# Network calls, update checks and telemetry in Wails' non-test sources
cd "$(go env GOMODCACHE)/github.com/wailsapp/wails/v3@<version>"
grep -rnE --include='*.go' --exclude='*_test.go' \
  'http\.(Get|Post|Head|NewRequest|DefaultClient|Client\{)|net\.Dial|DialContext|tls\.Dial|net\.Listen|sentry|analytics|telemetry|posthog|mixpanel' \
  pkg/application pkg/updater/*.go internal/assetserver internal/runtime
grep -rnoE --include='*.go' --exclude='*_test.go' '"https?://[^"]+' pkg/application
# What a build tag hides: check the //go:build line of every hit
# The default menus (Help role) and the WebView2 browser arguments
grep -n 'HelpMenu\|DefaultApplicationMenu' pkg/application/menu_*.go
grep -n 'disable-features\|AdditionalBrowserArgs' pkg/application/webview_window_windows.go
```

Then, in the repository: `go test ./internal/app/ ./internal/core/netx/`
(the tests above), and `npm run build && npm run check:dist` in
`frontend/`. For the bundle, `grep -ohE 'fetch\(|sendBeacon|new WebSocket|new EventSource|XMLHttpRequest' frontend/dist/assets/*.js | sort | uniq -c`
and read every new hit. For npm, look at new runtime dependencies and at
`"hasInstallScript": true` in `package-lock.json`.

### Manual checks

The web view runtimes are native code. These checks need a release build
(`scripts/wails.sh build`, which builds with `-tags production`) and an
outbound monitor. On each OS:

1. Start PostSonde with an empty data directory (portable mode, or a new
   user). Leave it idle for 5 minutes on the start screen.
2. Open a project, browse requests, environments, history and settings,
   open and close a second window, and leave it idle for 5 minutes.
3. Send one request to a host of your own and confirm that it's the only
   new connection. The console's **Connections** view should show it too.
4. Turn the network off (or block PostSonde entirely) and repeat 1–2.
   Everything should work. A send should fail with a connection error.

Expected: no connection from PostSonde, or from its web view processes,
in steps 1, 2 and 4, and only the request's in step 3.

| OS | Monitor | What to watch |
|---|---|---|
| macOS | Little Snitch or LuLu (alert mode), or `sudo tcpdump -i any -n 'not host 127.0.0.1'` | `PostSonde` and `com.apple.WebKit.Networking` (WKWebView's network process, shared by name with other apps: filter by its parent PID or run it with other WebKit apps closed). Also check that the menu bar has no Help menu. |
| Windows | Windows Firewall with outbound blocked by default plus a log, Wireshark, or Resource Monitor → Network | `postsonde.exe` and the `msedgewebview2.exe` processes it starts (Task Manager → Details, parent `postsonde.exe`). Watch for `*.msedge.net`, `*.microsoft.com`, `edge.microsoft.com`, `config.edge.skype.com`, `smartscreen*.microsoft.com`, and `*.events.data.microsoft.com` (diagnostic data). Also check that the browser process's command line (Process Explorer → `msedgewebview2.exe` → Properties) carries `--disable-background-networking` and the other switches. WebView2 runtime updates come from the system-wide Edge updater (`MicrosoftEdgeUpdate.exe`), not from PostSonde. That's expected and outside its control. |
| Linux | `scripts/check-no-network.sh` (below), OpenSnitch, or `sudo tcpdump -i any -n 'not host 127.0.0.1'` | `postsonde` and the `WebKitNetworkProcess`/`WebKitWebProcess` children. Local D-Bus (session bus, single instance, Secret Service) and the Wayland/X11 sockets are expected. |

Record the date, OS version, web view runtime version and result in
[Results](#results).

### `scripts/check-no-network.sh` (Linux)

Runs a build under `strace -f` and reports every network connection it or
a process it starts attempts. No root needed:

```sh
make build
scripts/check-no-network.sh                     # steps 1-2: 5 min, use the window meanwhile
scripts/check-no-network.sh --allow 192.0.2.10:443 -- ~/my-project   # step 3
scripts/check-no-network.sh --offline           # step 4: no network at all
```

- The app gets `POSTSONDE_HOME` and `XDG_{CONFIG,DATA,CACHE,STATE}_HOME`
  in a new scratch directory, so your settings, projects and WebKit data
  are untouched. A project folder after `--` opens in the first window.
- `strace -f -yy` follows WebKit's processes, the sandboxed
  `WebKitWebProcess` (bwrap) included, and logs `connect`, `sendto`,
  `sendmsg`, `sendmmsg`, `socket` and `execve` with the sockets decoded.
- `--offline` runs it in its own network namespace with no interface up
  (`unshare --user --map-current-user --net`): every IP connection fails
  with `ENETUNREACH`, while Wayland, D-Bus and AT-SPI, which are Unix
  sockets on the file system, keep working.
- After `--idle` seconds (default 300) the app gets SIGTERM and closes.
- It fails (exit 1) on any IPv4/IPv6 connect or send, loopback included,
  that `--allow` doesn't list, on a connection to a resolver socket
  (systemd-resolved, nscd), and on D-Bus messages to `resolve1`, the
  portal's proxy resolver or reachability calls, or PacRunner. It lists the
  Unix sockets used and the programs started. The log stays in the scratch
  directory.

What it can't see: traffic of other processes on PostSonde's behalf. D-Bus
calls are only matched by name, and the desktop portal, NetworkManager or a
system-wide proxy daemon would make their own connections. The runs below
found no D-Bus call that leads to one.

### Results

| Date | OS | Web view | Wails | Commit | Result |
|---|---|---|---|---|---|
| 2026-11-08 | Fedora 44, KDE Plasma 6.8.2 (Wayland), kernel 7.2.15 | WebKitGTK 2.54.2 (`webkitgtk6.0`), GTK 4.22.5 | v3.0.0 | 72bc3b99 | **No connection on its own.** Steps 1-4 pass. |
| | macOS | WKWebView | | | not checked yet |
| | Windows | WebView2 | | | not checked yet |

Linux, with `scripts/check-no-network.sh` on the `make build` binary
(`-tags production`), strace 7.3. Steps 2 and 4 were driven through the
accessibility tree (AT-SPI), since a script can't click on Wayland:
open requests of each kind (HTTP, GraphQL, Gemini) and their Body, Code,
Notes, Auth and Tests tabs, History, Environments (open Production),
Servers, every Settings page and the console, then **Open in new window**
(the second window closed with the app at the end: AT-SPI has no close
action for it).
The project was a copy of `internal/core/collection/testdata/sample` with
every URL pointed at a loopback listener.

| Run | Duration | IPv4/IPv6 connections | Resolver / DNS |
|---|---|---|---|
| 1. Empty data directory, start screen, idle | 5 min | none; no IP socket created | none |
| 2. Project open, browse, second window, idle | 4.5 min after browsing; again 5 min in a second run | none | none |
| 3. Then one send of `GET http://127.0.0.1:18947/health` | | exactly one: `connect` to `127.0.0.1:18947` from the Go process, at the send | none |
| 4a. Offline, start screen, idle | 5 min | none | none |
| 4b. Offline, project, browse, second window, idle | 6 min | none | none |
| 4c. Offline, one send | | one `connect`, `ENETUNREACH`; the request fails with "127.0.0.1:18947 cannot be reached from this computer", its history entry says Failed, and the app keeps working | none |

Apart from the sends in 3 and 4c, the only sockets in every run were Unix
ones: the Wayland display, the session bus (`/run/user/1000/bus`), the
system bus, the AT-SPI bus and WebKit's own `webkitgtk/bus` and
`at-spi-bus` proxies (`xdg-dbus-proxy` for the sandbox). The D-Bus traffic went to the desktop portal (Settings,
Inhibit, Realtime, PowerProfileMonitor, Documents, Session),
NetworkManager (`GetAll` of its properties and signal matches: GLib's
network monitor reading the connection state), UPower, timedate1,
RealtimeKit, gvfs and AT-SPI, the same set in every run, with no call to
`resolve1`, a proxy resolver or `CanReach`. The programs
started were `WebKitNetworkProcess`, `WebKitWebProcess` (in `bwrap`),
`xdg-dbus-proxy`, and `gsettings` (the system proxy lookup, which reads
local settings). WebKit's caches and storage went to the scratch
`XDG_CACHE_HOME`/`XDG_DATA_HOME`.

## Where PostSonde writes (APP-04, SET-03)

PostSonde writes only to its config, data, cache and log directories
([settings.md](settings.md#data-directory-set-03)) and to paths the user
picks in a native dialog (saved bodies, exports). In portable mode all
four directories are in `postsonde-data/` next to the app, so nothing of
PostSonde's own is written outside the app folder; if that folder cannot
be written PostSonde keeps nothing on disk for the session rather than
falling back to the home directory. The web view and the OS keep some
files of their own that PostSonde cannot redirect on macOS and Linux
(caches, web storage, window state); on Windows the WebView2 profile is
moved into the app folder too. The list per OS is in
[packaging.md](packaging.md#portable-mode-app-04).

PostSonde also reads, and never writes, one file outside those
directories: the administrator policy file (SET-02), at a fixed
system-wide location in every mode, portable included, so an
administrator can lock settings such as TLS verification and allowed
directories (`privacy.allowedDirs` locked to `[]` keeps `{{$file}}` to the
project folder) and a user cannot get around them
([settings.md](settings.md#settings-locked-by-your-administrator-set-02)).

A data directory move copies the data to the folder the user chose and
checks it; the old copy, which holds secrets, stays until the user
deletes it from Settings, General (after a confirmation), so it is not
removed behind their back, and it is not left unmentioned either.

## Response bodies on disk

Response data is written outside the data directory only where the user
chooses: "Save body to file" (HTTP-34) writes to the path picked in the
native save dialog. To keep large bodies out of memory, a body is
written to `<data>/tmp/response-bodies/` (`0700`, files `0600`) while it
arrives once it passes 1 MiB (HTTP-37), never to the system temp
directory. A gzip or deflate body is stored decompressed. Each file is
deleted when its body is dropped (after 8 newer responses or an hour),
when its send fails or is cancelled part way, all of them at shutdown,
and any left by a crash at the next start. How much one response may
take is `network.maxResponseBodyMb` (512 MiB by default), so with the 8
kept bodies the directory holds at most eight times that
([http-client.md](http-client.md#large-responses-http-37)).

## Local file access (PRV-10)

A project can come from anyone, so a path written in it must not be a way
to read, and then send, a file the user never meant to share
(`{{$file ~/.ssh/id_rsa}}` in a header, a JWT "key file" that is someone's
SSH key, a `.proto` import that is a password file). Every feature that
reads a local file whose path comes from **project data** (request files,
`_folder.json`, environment files) is therefore limited to allowed
directories:

- **The allowlist** is `privacy.allowedDirs` in `settings.json`, edited on
  the settings screen under Privacy, Local files, "Allowed directories for
  file access" (**Add folder…** opens the native folder dialog,
  **Remove** takes one out). It is an app setting, never stored in a
  project, so opening a project cannot widen it. It starts empty. An
  administrator can lock it (SET-02, [settings.md](settings.md)).
- **The open project's folder is always allowed**, as its files (fixtures,
  keys and schemas next to the requests) are the project's own, shared
  with it. A project whose files are all inside its folder works without
  any setting.
- **Paths** may be absolute, start with `~/` (the home folder), or be
  relative to the project folder. Links and `..` are followed on disk
  before the check (`filepath.EvalSymlinks`), so neither a link inside an
  allowed directory that points out of it nor `..` can reach a file
  elsewhere; a link from elsewhere pointing into an allowed directory
  reads the allowed file. The file is then opened through `os.Root` on
  the allowed directory, which refuses to leave it even if a link changes
  between the check and the open. On macOS and Windows the check ignores
  case, as their file systems usually do.
- **Refusals** are clear and happen before anything is read or sent:
  `fileaccess.not_allowed` (kind `permission_denied`), naming the feature
  and the path as written (*The JWT key file "…" is outside the
  directories allowed for file access.*), with a hint naming the setting
  (`fileaccess.HintAllow`). A missing file is `fileaccess.not_found`
  (or the feature's own "missing" code) only when it would be allowed, so
  a project cannot probe which files exist elsewhere.
- **File pickers** of these fields (the JWT key file, a schema file, the
  GraphQL schema file, `.proto` files and import paths of a request or a
  gRPC mock server, a GraphQL mock server's schema file, a server's
  certificate files) check the chosen file
  (`SettingsService.CheckFileAccess`). One outside the allowed directories
  gets a notice under the picker saying it will be refused, with
  **Allow "folder"**, which adds the folder the file really is in (links
  followed) to the list (`SettingsService.AllowDirectory`). When an
  administrator locks the list, the notice says so and offers nothing.
  One shared component does this: `components/common/FileAccessNotice`.
- **Implementation:** `internal/core/fileaccess` (`Policy`: `Resolve`,
  `Open`, `Stat`, `ReadFile`; `Subject` names the feature in messages).
  The services read the setting whenever they read a file, so a change
  applies at once, and a gRPC setup linked while a folder was allowed is
  not reused from the cache after it is removed.

Where PostSonde reads a local path, and whether the allowlist applies:

| Feature | Path from | Allowlist |
|---|---|---|
| `{{$file PATH}}` (DYN-08) | project data (any templated field) | **Yes** (`template.file` problem with the reader's error) |
| JWT auth key file (AUTH-16) | project data (`auth.keyFile`) | **Yes**: the send, the header preview and the Auth tab's JWT preview refuse it (details `type` `jwt`, `field` `keyFile`) |
| netrc file named by the auth (AUTH-19) | project data (`auth.netrcFile`) | **Yes**: the send and NetrcCheck refuse it (field `netrcFile`) |
| JSON Schema file of a `matches_schema` assertion (TEST-07) | project data | **Yes**: the assertion is `invalid` with code `fileaccess.not_allowed`; the response is not affected |
| GraphQL `schemaFile` (GQL-11) | project data | **Yes**: the schema strip shows the refusal, and a schema read before is no longer served |
| gRPC `.proto` files and import paths of a request (GRPC-*) | project data (`files`, `importPaths`) | **Yes**: a `fileaccess.not_allowed` diagnostic per path in Describe, and a call is refused; an import that leads out through a link is not found (`linker.Options.Access`) |
| `.proto` files and import paths of a gRPC mock server (SSRV-07) | project data (`grpcMock.files`, `importPaths`) | **Yes**: the start (and a reconfigure) fails with `fileaccess.not_allowed` |
| Schema file of a GraphQL mock server (SSRV-08) | project data (`graphqlMock.schemaFile`) | **Yes**: the start fails with `fileaccess.not_allowed`, a reconfigure keeps the schema loaded before, and the editor's description shows the refusal |
| Certificate files of a local TLS server (SRV-05) | the server definition (`tls.certFile`, `keyFile`, `pfxFile`), saved in a project folder | **Yes**: saving the server and starting it refuse a file outside (detail `field`); a server kept only for the session is checked the same way |
| netrc file of `NETRC`, or `~/.netrc` (`_netrc`) by default | the user's environment | No: chosen by the user, not the project |
| File body, multipart file parts, GraphQL upload files (GQL-07) | project data, chosen with the native file dialog | No: exempt, the user picks the file, and it is sent as the request's content, which the user sees (PRV-10) |
| Data file of a data-driven run (RUN-05) | the native open dialog; the path is kept for the session in the window and in Go, never saved | No: exempt, the user picks the file, and its rows are shown before the run. Go reads only a path picked in the dialog in this session (`runner.data_not_picked` otherwise), so a path from anywhere else is never read ([runner.md](runner.md#data-driven-runs)) |
| Import, OpenAPI file for mock routes, "Save body to file", HAR and log export, saved exchanges, environment exports, run reports (RUN-08) | a native dialog, never stored | No: chosen by the user |
| Custom CA bundles, per-host client certificates, the themes folder | app settings, chosen in a dialog | No: app settings, not project data |
| Per-request TLS files (CA, client certificate) | none: requests store only `tls` and `verifyTls` switches; client certificates are app settings (AUTH-30) | Not needed |
| The project's own files (requests, `_folder.json`, environments, `postsonde.json`) | the project folder | Not needed: that is the project |
| OpenAPI `$ref` to other files | never followed (`openapi.external_ref` warning) | Not needed |

## Secret-free project files (PRV-09, SEC-04)

Project files are meant to be committed and shared, so no credential is
ever written into one. What is secret, and where it is kept instead:

| Secret | Kept in | Details |
|---|---|---|
| Typed Basic or Digest password, bearer token, API key, JWT signing secret or private key (AUTH-16), OAuth 1.0 consumer or token secret (AUTH-15), AWS secret key or session token (AUTH-17), Hawk key (AUTH-18), OAuth 2.0 client secret (AUTH-08) or password-grant password (AUTH-09) of an HTTP, GraphQL or SSE request, or of a folder | `<local>/secrets/requests/<item id>.json`, `auth.<member>` | [auth.md](auth.md#secrets) |
| OAuth 2.0 access, refresh and ID tokens (AUTH-08, AUTH-13) | `<local>/secrets/oauth2/tokens.json`, by a hash of the auth configuration (`internal/core/oauth2/tokenstore`, `0600` in a `0700` directory); the UI gets only the last four characters | [auth.md](auth.md#token-store) |
| Value of a secret folder variable, or of a secret global variable (`postsonde.json`) | the same side file of the folder or workspace ID, `var.<name>` | [environments.md](environments.md#folder-and-global-secret-variables) |
| Value of a secret environment variable | `<local>/secrets/environments/<environment id>.json` | [environments.md](environments.md#where-things-are-kept) |
| Value of a secret runtime variable (ENV-15) | memory only for a session one; `<local>/secrets/runtime/variables.json` for a persisted one (the list, `<local>/runtime-variables.json`, has `""`) | [environments.md](environments.md#runtime-variables) |
| Local value of an environment or global variable (SEC-02; not a secret, but this computer's own: the project file keeps the shared value) | `<local>/local-values/environments/<environment id>.json`, `<local>/local-values/global/<workspace id>.json` (`internal/core/localvalues`) | [environments.md](environments.md#local-values) |
| Cookies (every jar, per environment too, and the cookie settings), the active environment, scratch requests, history | the workspace's local directory | [workspace.md](workspace.md#local-side-files) |
| Passphrase of a per-host client certificate (a PKCS#12 file, an encrypted PKCS#8 key) | `<data>/secrets/app.json`, `clientCert.<id>.passphrase` (`internal/core/appsecrets`, `0600` in a `0700` directory) | [network.md](network.md#client-certificates) |
| Password of the manual proxy (SOCKS-04, NET-02) | the same file, `proxy.password`; `settings.json` keeps the proxy URL with the user name only, and the password never goes back to the UI (`hasPassword` only) or into a message. It is sent only to the proxy (RFC 1929, or HTTP `Proxy-Authorization: Basic`), and is in no error, timeline, request as sent, history entry, cURL command, HAR file or log line | [network.md](network.md#http-proxy) |
| Password of a project's or environment's proxy (SOCKS-08, SOCKS-10) | `<local>/secrets/requests/proxy_scopes.json`, `project.password` and `environment.<id>.password` (`internal/core/proxyscope` through `requestsecrets`); the scope itself (URL with the user name only, bypass list) is in `<local>/proxy-scopes.json`, never in a project file. Never back to the UI (`hasPassword` only), masked in copied logs | [network.md](network.md#proxy-scopes) |
| Passphrase of a local server's certificate file (SRV-05) | the same file, `serverCert.<hash of the path>.passphrase` | [servers.md](servers.md#tls) |
| Private key of the local CA that signs the local servers' generated certificates (SRV-05) | `<data>/servers/tls/ca-key.pem` (`0600` in a `0700` directory); never in a project | [servers.md](servers.md#tls) |

A **netrc auth** (AUTH-19) keeps its credentials out of PostSonde
altogether: the project file stores only the type and, when set, the
file's path. The file (`~/.netrc`, or the one `NETRC` or the auth names)
is read only when a request with that auth is sent, copied as cURL or
exported, or when **Check** is clicked in the Auth tab; never at startup,
never for another auth type, and not for the header preview computed
after each edit. Its password is masked like the other secrets and is
never sent to the UI; the check returns only the entry's name, line and
login ([auth.md](auth.md#netrc-auth-19)).

Client certificates are app settings, not project data: `settings.json`
keeps only their host patterns and file paths (`network.clientCerts`), and
the passphrase goes to the app-wide side file above, which is plain JSON
readable by your account like the other side files, or encrypted with them
once a master password is set ([Encrypted secrets](#encrypted-secrets)). A
passphrase is never sent back to the UI
(`hasPassphrase` only), logged, or shown in the timeline, which names only
the certificate's common name and the host pattern (event `client_cert`).
Removing the certificate on the settings screen removes its passphrase.

A local server that serves TLS (SRV-05) is defined with the paths of its
certificate files only, in the Servers view or in a folder's
`_folder.json` ([file-format.md](file-format.md#saved-server-config)):
the passphrase goes to `<data>/secrets/app.json`, keyed by a hash of the
file's path (so the key does not show the path), and never returns to the
UI (`hasPassphrase` only); certificate files are read, never copied. The
generated certificates come from a local CA whose private key stays in the
data directory; PostSonde never installs that CA in the system or a
browser: trusting it is the user's step ([servers.md](servers.md#tls)).
A passphrase stays stored when its server is removed, so another server
using the same file finds it.

`<local>` is never inside the project. Every path that writes a project
file enforces it: the editors' auto-save (`requestsecrets.SplitFile`
before the pending save), every write of the collection tree (saves,
renames, reorders, creates, duplicates, restores:
`Tree.KeepSecretsLocal` → `requestsecrets.Store.MoveOut`), the import
writer and cURL import, and the environments store. A secret typed into a
project file by hand is used as it is and moves to local storage the next
time PostSonde writes that file; loading never writes, and if the local
storage cannot be written the project file is left as it was, so a value is
never lost. The UI masks secret values (password fields with an eye button)
and says under each secret field, and in the variables tabs, that a typed
value is stored only on this computer.

`TestProjectFilesNeverHoldSecrets` (`internal/app/services/collection`)
enters credentials through every save path (each auth type of HTTP
requests, GraphQL and SSE requests, folder auth and secret variables, cURL paste and file
import, a collection import with folder settings, environment variables)
and hand-types secrets into the project files, runs the tree operations
over them, then reads every file of the project folder and fails on any
secret byte. It sets local values (SEC-02) of an environment variable and
of a global variable too, and fails on those bytes as well: local values
are kept apart from the project the same way, though they are not secrets
(they are shown and sent like any value, and copied logs are not masked
with them).

Header, query, metadata and body values are not secret fields: they are
written as typed. Credentials belong there as a `{{variable}}` of a secret
variable. Exports and code snippets include a secret only when the user
explicitly chooses to (PRV-09); see below for Copy as cURL, the first of
them.

## Encrypted secrets

Every file in the table above that holds a secret can be encrypted at rest
with a master password (SEC-03): the request, folder and global
secrets (`requestsecrets`, which also keeps the proxy passwords of the
project and environment scopes), the environment secrets (`environments`), the
persisted secret runtime variables (`runtimevars`), the OAuth 2.0 token
store (`oauth2/tokenstore`) and the app-wide secrets (`appsecrets`: the
proxy password and the certificate passphrases). Local values
(`localvalues`, SEC-02) are not secrets and stay plain.

**Opt-in.** Encryption is off until the user sets a master password in
Settings, Privacy, Stored secrets, and PostSonde works as before without
one. Always-on was ruled out: it would need a password (or a keychain)
before a first request could be sent, and a forgotten password
would lose data the user never chose to protect. An administrator can
require it instead (below).

**How.** Package `internal/core/secretstore`:

- Each store reads and writes its secret file through
  `secretstore.ReadFile` and `secretstore.WriteFile` (atomic, via
  `atomicfile`) instead of `os.ReadFile` and `atomicfile`; nothing else
  in the stores changed, and each keeps its own format. `logexport`
  reads the side files the same way. A file is handled by the vault
  registered for the data directory that holds it (`secretstore.Register`,
  done by `internal/app` before anything reads a secret); a file outside
  it (store tests, no data directory) stays plain.
- A random 256-bit **data key** encrypts the files. The master password
  never encrypts a file: **PBKDF2-HMAC-SHA256** (`crypto/pbkdf2`, **600 000
  iterations**, a random **32-byte salt** per installation, 32-byte output)
  derives a key-encryption key that wraps the data key with AES-256-GCM.
  `<data>/secrets/vault.json` (`0600`) holds only the KDF name, hash,
  iteration count and salt, the wrapped key, a random key ID and a key
  check (a known text sealed with the data key), so the parameters can
  change later. The wrapped key's associated data includes the KDF
  parameters, so none of them can be changed in the file.
- A **wrong password** fails the GCM authentication of the wrapped key and
  is refused with `secretstore.wrong_password`; it never yields a wrong
  key or garbage.
- An **encrypted file** is one line of JSON:
  `{"postsondeSecret":1,"cipher":"AES-256-GCM","keyId":"…","nonce":"…","data":"…"}`,
  AES-256-GCM with a fresh random 96-bit nonce on every write. Its
  associated data is the key ID and the file's name below its `secrets`
  directory (`requests/req_1.json`, `app.json`), so a file copied over
  another, a tampered or a truncated file is refused with
  `secretstore.corrupt`, one encrypted with another key with
  `secretstore.other_key`, and an encrypted file without `vault.json`
  with `secretstore.not_set_up`. A plain file where every file should be
  encrypted is refused too (`secretstore.corrupt`). Binding the name
  below `secrets/` rather than the whole path keeps a moved data
  directory (SET-03) and a copied workspace directory readable.

**Unlocking.** With encryption on, the vault starts locked every time
PostSonde starts, and the unwrapped key is only ever in memory (and, with
the key store "keychain", in the OS keychain: [Keychain](#keychain)).

- At startup the secrets unlock from the OS keychain when the key is kept
  there; otherwise, or when that fails, the window asks for the master
  password (`SecretsHost`,
  `UnlockDialog`). **Not now** leaves it locked. The status bar shows
  **Locked** or **Unlocked** while encryption is on: clicking Locked opens
  the dialog again, clicking Unlocked locks now.
- **While locked**, anything that must read or write a secret file fails
  with `secretstore.locked` (kind `permission_denied`) and the hint to
  unlock; the stores pass the error through unchanged, and the UI opens
  the unlock dialog when it sees that code (`setLockedErrorListener` in
  `lib/errors.ts`, unless Not now was chosen since the state last
  changed). So a send whose scopes need a secret file (a request with a
  typed auth secret, secret global or folder variables, an active
  environment with secret variables, a proxy password, a certificate
  passphrase) is refused before anything is sent, and a send that needs
  none goes out. Nothing is written while locked, so no secret is ever
  overwritten or left plain; a missing file is still just missing. Views
  that load secrets (an environment with secret variables, a request with
  a typed secret, Copy as cURL, copied logs) show that error instead of
  values: a secret is never shown, masked or not, from a locked store. The
  environments reload once the secrets are unlocked. The OAuth 2.0 token
  store drops the tokens it holds in memory on every lock and does not
  fail a send: a locked token file has no tokens.
- **Lock now**: the status bar, Settings, or the command palette's **Lock
  stored secrets now** (⌥⌘L, Ctrl+Alt+L; `secrets.lock`).
- **Idle lock**: `privacy.secretsLockMinutes` (Settings, Privacy, Lock
  after: never, 5, 15, 30 minutes, 1 or 4 hours; 0 to 1440 in the file)
  locks the secrets once none has been read or written for that long.
  Every secret read or write counts as use; `SecretsService` checks every
  15 seconds and every use checks too, so a use after the time finds it
  locked.
- Session runtime variables live in memory only and are not affected by a
  lock; persisted secret runtime variables already loaded in the session
  stay usable too (they are in memory like session ones).

**Changing the password** asks for the current one, wraps the same data
key with a key from the new password (a new salt, the current iteration
count) and replaces `vault.json` in one atomic write; the secret files are
not touched, so a failure or a crash leaves the old password working.
**Turning it off** asks for the password, then writes every secret file
back plain and removes `vault.json` (and the keychain's key). An
administrator's requirement refuses it (`secretstore.required`).

**Migration and recovery.** Turning encryption on writes `vault.json`
first, marked `"migration": "encrypting"`, then encrypts the secret files
one by one: each encrypted copy is written to a temporary file, synced and
renamed over the plain one (`atomicfile`), so every file is at all times
either its complete old or its complete new form. Then the mark is
cleared. `ReadFile` reads plain and encrypted files alike while a
migration is marked, so a crash at any point loses nothing: the next
start finds the mark (the vault is locked, the Privacy page says a change
was interrupted), and the next unlock encrypts what is left. Leftover
`.<name>.tmp-*` files of interrupted writes, which may hold a secret in
the clear, are deleted by the migration. Turning encryption off works the
same way with `"decrypting"`, removing `vault.json` last. Migrations look
for secret files in `<data>/secrets/` and `<data>/*/*/secrets/` (the
workspaces' local directories) only, so a project folder named `secrets`
in the local trash is never touched. A file that cannot be decrypted stops
turning encryption off, with the mark kept: delete that file (its secret
is lost) and unlock again. Replacing a file does not wipe the blocks the
plain copy used on disk; full-disk encryption covers that.

**Forgotten password.** It cannot be recovered. **Forgot password?** in
the unlock dialog, or **Delete encrypted secrets…** in Settings, deletes
every encrypted secret file and `vault.json` after the user types the
confirmation word (DELETE); encryption is then off, and a new master
password can be set. Plain files left by an interrupted migration stay.

**Administrator policy (SET-02).** `privacy.requireSecretEncryption`
(default `false`) is meant to be locked to `true` in the policy file: the
window then asks for a new master password at startup while there is
none, turning encryption off is refused, and storing any secret while it
is off fails with `secretstore.required`, so nothing new is written plain.
`privacy.secretsLockMinutes` can be locked the same way.

**Limits.** PBKDF2 is the only password KDF in the standard library;
scrypt and Argon2 would resist GPU guessing better. There is no delay
between wrong passwords beyond the KDF's cost. The local CA's private key
of the local servers (`<data>/servers/tls/ca-key.pem`) is generated, not a
user secret, and is not encrypted. Response bodies, history and run
records are not secrets and are masked as described below, not encrypted.

Tests: `internal/core/secretstore` (round trip of every kind through its
store, wrong password, tampered, truncated and swapped files, no plaintext
left in the data directory after turning it on, password change, turning
it off, a crash at every step of both migrations, the idle lock, the
administrator's requirement, the keychain seam, concurrent writes during
a migration), `internal/core/secretstore/keychain` (the `security` and
`secret-tool` commands through a fake runner, with the key never in a
command line, their exit statuses and errors, the DPAPI file with a fake
seal; the real DPAPI round trip runs on Windows; a real macOS keychain
round trip under a throwaway service name runs only with
`POSTSONDE_KEYCHAIN_TEST=1`), `internal/app/services/secrets` (startup
unlock, fallback, switching both ways, the policy), and
`TestSendRefusedWhileSecretsLocked` (`internal/app/services/http`); in the
UI `UnlockDialog.test.tsx` and `SecretEncryption.test.tsx`.

### Keychain

With encryption on, the data key can also be kept in the OS keychain, so
the secrets unlock at startup without typing the master password (SEC-03).
The master password always stays: it is set when encryption is
turned on, it unlocks whenever the keychain cannot, and changing it,
turning encryption off and the reset work as above.

**Setting.** `privacy.secretsKeyStore` (Settings, Privacy, Stored secrets,
**Unlock with**): `"keychain"` (the default, left out of the file: the
key is also in the keychain) or `"password"` (only the master password).
The keychain is the default because unlocking without retyping the
password is the point of SEC-03's "key in the OS keychain"; the master
password remains the fallback. An administrator can lock
it to `"password"` (SET-02).

**Where.** Package `internal/core/secretstore/keychain`; each backend is a
`secretstore.KeyStore` (`LoadKey`, `StoreKey`, `DeleteKey` by the
vault's key ID), with no library and no cgo. The key is stored
hex-encoded and never passed on a command line.

| System | Backend | Item |
|---|---|---|
| macOS | `/usr/bin/security` | A generic password of the default (login) keychain: service `io.github.sorrelfox.PostSonde.secrets`, account `vault-<keyID>`, label `PostSonde secrets key`. |
| Linux, BSD | `secret-tool` (libsecret) over the Secret Service: GNOME Keyring, KWallet, KeePassXC | Attributes `application=io.github.sorrelfox.PostSonde.secrets`, `account=vault-<keyID>`, label `PostSonde secrets key`. |
| Windows | DPAPI (`CryptProtectData` of `crypt32.dll` through `syscall`, current user, `CRYPTPROTECT_UI_FORBIDDEN`, entropy bound to the key ID) | `<data>/keychain/vault-<keyID>.dpapi` (`0600`). |

- **No secret in a command line.** `security add-generic-password -w KEY`
  would show the key to every process (`ps`), so PostSonde writes that
  command to the standard input of `security -i` (its interactive mode,
  which reads commands from stdin and exits with the last command's
  status); `security find-generic-password -w` prints the key on standard
  output. `secret-tool store` reads the secret from standard input and
  `secret-tool lookup` prints it. The trade-off on macOS: the item is
  created by `/usr/bin/security`, which its access list then trusts, so
  PostSonde reads it without a prompt, and so can any program of the same
  user that runs `security` (which can also read the unlocked app's
  memory or ask the same keychain). Restricting the item to PostSonde's
  own binary (`-T`) would need the Security framework through cgo.
- **Windows.** DPAPI ties the file to the Windows account (and computer,
  unless the profile roams); a data directory copied elsewhere cannot be
  unsealed and falls back to the master password. The `keychain`
  directory is not a secrets directory, so the vault's migrations never
  touch it.
- **The item name follows the app ID** (`<app ID>.secrets`).
  Builds from before the app ID became
  `io.github.sorrelfox.PostSonde` stored the key as
  `app.postsonde.secrets`, which newer builds don't look for: they fall back
  to the master password once, then store the key under the new name. The
  old item stays in the keychain until it is deleted by hand (Keychain
  Access, Seahorse, KWallet). DPAPI files are named by key ID only and are
  not affected.

**When.**

- **Turning encryption on** with the keychain mode stores the key at once.
- **At startup** `SecretsService` unlocks from the keychain in the
  background (the system may first ask to unlock the keychain);
  `StatusDTO.keychain.pending` is true meanwhile and the window waits for
  the next `secrets:changed` before showing the unlock dialog. The key is
  checked against `vault.json`'s key check, so a stale or foreign key is
  refused (`secretstore.wrong_password`).
- **The fallback.** No key in the keychain, no `secret-tool`, no Secret
  Service on the session bus, a denied access or a locked keychain: the
  secrets stay locked, the unlock dialog says why (`keychain.unavailable`,
  `keychain.failed`, with a hint, e.g. which package provides
  `secret-tool`), and the master password unlocks. Every password unlock
  in the keychain mode stores the key again, so the next start unlocks
  from the keychain once it works; a failure to store it does not fail
  the unlock and is shown under Unlock with.
- **After Lock now or the idle lock**, the unlock dialog offers **Unlock
  with \<keychain\>** (`SecretsService.UnlockWithKeychain`) besides the
  password.
- **Switching** (`SecretsService.SetKeyStore`) only moves the key: the
  data key, the secret files and the master password do not change, so no
  secret is re-encrypted or lost. To `"keychain"` while unlocked, the key
  is stored first and the setting changes only if that worked (else the
  keychain's error); while locked, at the next unlock. To `"password"`,
  the setting changes and the key is deleted from the keychain; when
  that fails the status says so, and the item can be deleted by hand
  (names above).
- **Turning encryption off and the reset** delete the keychain's key
  too, whatever the mode.

Codes: `secrets.no_keychain` (no keychain here, or the mode is
`"password"`), `secrets.keychain_no_key` (nothing stored yet),
`keychain.unavailable`, `keychain.failed` and `keychain.invalid` (a stored
value that is not a key).

## Secrets in copied commands

Copy as cURL (GEN-01, [curl.md](curl.md#copy-as-curl)) puts a request on
the clipboard as the command that sends it, so it is built from the
request with its secrets merged in, as a send is. **Masked by default**:
before the command is written, `snippet.Request.Masked` replaces with
`••••••` the same values the timeline masks (below): the values of the
secret variables used, wherever they appear (URL, header values, body,
multipart names and values, also URL-escaped), the value an API key auth
adds, a JWT auth's token and its typed signing secret, an OAuth 1.0 auth's secrets and signature, an AWS auth's secret key, session token and signature, a Hawk auth's key and MAC, the password a netrc auth read from its file, an OAuth 2.0 auth's client secret and the session's token (a copy never asks the token endpoint; without a token it shows `Bearer …`), and the credentials of every `Authorization` and
`Proxy-Authorization` row after the scheme. The Basic or Digest password
of `-u` is always masked; the username only when it is a secret. A masked
command shows the request but does not authenticate as it is; the toast
says the secrets were masked. **Copy with secrets**, an explicit choice in
that toast, copies the command again with the real values
(`CurlCommand` with `includeSecrets`). Nothing is written to a file or
the log either way: the command goes only to the clipboard. The
timeline's limits apply (masking by value; transformed secrets are not
recognised). Cookies from the jar are copied as they are: cookies are not
secrets (below), and a session cookie in a copied command is visible in
it.

The **snippet panel** (the Code sub-tab, GEN-12, GEN-13,
[snippets.md](snippets.md)) shows the same code in the editor. In
resolved mode it masks the same values by default; **Show secrets** is an
explicit choice for that view only: it lives in the panel's state, is
never saved (not in the drafts, the settings, the project, browser
storage or Go) and is dropped when the panel closes or the mode changes.
The values it shows are those of a build made as a send makes it. The
templated mode never holds a secret: variables are not resolved (so
neither secret variables nor local values appear), values typed in an
auth's secret members and what an auth makes for one send (a JWT, an
OAuth 2.0 token, a signature) are `••••••`, and an `Authorization`
credential is kept only when it is made of `{{references}}`. Neither
mode fetches, reads or asks anything (no token request, no `{{$file}}`
or netrc read, no `{{$prompt}}`).

## Postman export

Exporting a folder as a Postman collection (EXP-03,
[import.md](import.md#postman-export)) writes the project's files as they
are shared: the export never reads the local side files, so a secret
variable is written without a value and an auth secret only as a
`{{variable}}` reference, else empty, with a note in the report. Report
messages name items, variables and places, never values.

## Environment export

Exporting an environment (EXP-05,
[environments.md](environments.md#export)), as a PostSonde environment
file or for Postman, reads the environment's file in the project only,
never the local side files: secret variables are written as secrets
without a value (a value typed into the file by hand too), and local
values are not in it.

## Folder zips

Exporting a folder as a zip (EXP-02, [import.md](import.md#folder-zips))
packs its project files as they are on disk. The local side files, where
secrets, local values, history and tokens live ([workspace.md](workspace.md#local-side-files)),
are never read, so they cannot be in the zip; a secret typed into a
project file by hand is left out as the file's next save would move it
(`requestsecrets.SplitFile`), counted in the result and named in the
toast. Files whose secrets cannot be checked (unknown kinds, newer
versions, files that do not load) are left out. Importing a zip extracts
nothing: entries are checked (no absolute or parent paths, links, special
or encrypted files; limits on entries and sizes) and the import writer
chooses every name and keeps any secret in the local side files. Report
messages never contain values.

## HAR files

HAR import and export ([har.md](har.md)) follow the rules above:

- **Import.** A browser recording holds the browser's session. Its
  cookies (`Cookie` header, `cookies` list) are dropped with a warning
  that names them, never their values; `Authorization` (Bearer, other
  token schemes, Basic) and API key headers become typed auth, whose
  secret the import writer keeps in the local side files, so the project
  files hold no credential (`TestImportHAR` reads every project file for
  them). Credential headers that cannot become typed auth (Digest,
  `Proxy-Authorization`, a second key, a WebSocket handshake's) are
  dropped with a warning. Bodies and URLs are imported as recorded.
- **Export of history entries** starts from the history, which is masked
  when it is recorded: nothing unmasked exists to export, so there is no
  opt-in. Cookies are exported as recorded (they are not secrets).
- **Export of a folder's requests** builds them as Copy as cURL does and
  masks the same values by default; **Export with secrets**, an explicit
  choice in the toast, writes the real values. A file body's path is
  reduced to its file name.
- **A response's exchange** (EXP-06, [har.md](har.md#a-responses-exchange))
  is masked with the send's secrets, as the timeline and the history are,
  then again with every secret the app keeps, as copied logs are, bodies
  included; it has no "with secrets" choice either. What it needs is kept
  in memory with the response's body, never on disk.
- All of them write only to the path picked in the native save dialog.

## Secrets in the timeline

The response's Timeline tab (DBG-01) shows each request as it was sent,
so it could show secrets. Go masks them before the response reaches the
UI ([http-client.md](http-client.md#request-as-sent-dbg-01)): the values of
secret variables wherever they appear (URL, header values, body, also
URL-escaped), the value an API key auth added, the token a JWT auth signed
for that send and its typed signing secret (AUTH-16: the values of that
very build, since every build signs a new token; in a custom header or the
query string too), an OAuth 1.0 auth's consumer and token secrets and the
signature of that build (AUTH-15; also in the query string or form body),
an AWS auth's secret key, session token and signature (AUTH-17; also in a
presigned URL), a Hawk auth's key and MAC (AUTH-18),
the password a netrc auth read from its file for that build (AUTH-19),
an OAuth 2.0 auth's client secret, the password grant's password, and
the access, refresh and ID tokens of the send, also when the ID token is
the one sent (AUTH-08, AUTH-09, AUTH-14; also in the token request's form body and
Authorization header and the token response's body, which the timeline
shows as the first hops, AUTH-33),
and the credentials of every
`Authorization` and `Proxy-Authorization` header after the scheme (Basic,
Bearer, or one typed in the header table), all shown as `••••••`. The
response's `url` and `finalUrl` (the redirects badge's title) are masked
the same way, also in the partial responses of an incremental GraphQL
response. A Digest
`Authorization` keeps its parameters (username, realm, nonce, uri, cnonce)
and shows its `response` hash as `••••••`, since the hash could be used to
guess the password offline; the Digest password itself is never sent. Only the
masked text crosses the bridge; the client's record of the request is
dropped once the response DTO is built. Masking is by value, so a secret
that is very short can also hide the same characters elsewhere, and a
secret sent transformed in a way other than URL escaping or Basic auth
(hashed, Base64 in a body) is not recognised. Cookies are not treated as
secrets (the Cookies tab and the cookie manager show them too).

## Request history

Every request sent (NAV-02) is recorded in the project's local directory
(`<local>/history/`, `0700`, files `0600`), never in the project folder
and never anywhere else ([history.md](history.md)). Secrets are masked
before a line is written, with the timeline's masker and secrets (above),
plus the credentials of every Authorization field sent, the decoded Basic
password and URL userinfo as values, so a response that echoes them is
masked too: URL, headers, both bodies (masked whole, then clipped to 64
KiB), final URL and error message. The same limits apply (masking by
value; transformed secrets are not recognised; cookies are not secrets).
Values typed for `{{$prompt}}` (DYN-07) are never written to project,
environment or local files; a `secret` prompt's value is masked in the
timeline and the history, another is recorded as sent.
The history is bounded (the newest 1000 entries, at most 64 MiB of them),
old segment files are deleted as entries age out, and `HistoryService.Clear`
removes every file; Settings, Privacy, Local data clears it with the rest
([Clearing local data](#clearing-local-data)).

Each entry also keeps the request as its editor had it (NAV-03), with
`{{variables}}` unresolved, masked by value with the same secrets (an
`Authorization` row without a reference also by name); the auth and its
secrets are not kept. The history panel's **search** runs in Go over what
the history keeps, masked again with the secrets PostSonde knows now, so a
secret is never found by its value, nor shown in the list, even in an entry
recorded before it became one. **Restoring** an entry masks it again the
same way and takes every `••••••` out before the UI sees it; a value that
was masked is kept from the request it is restored into, or left out of a
new scratch request, never written back as the mask
([history.md](history.md#the-history-panel)).

## Run history and reports

The folder runner keeps each run that ended (RUN-09) in the project's
local directory (`<local>/runs/`, `0700`, files `0600`), never in the
project folder and never anywhere else ([runner.md](runner.md#run-history)).
A record holds the run's summary and results (names, statuses, times,
assertion values and messages, errors), **never a response body or
header**, and the data file's name but not its path. Before it is
written, every secret the app keeps is masked in it, as in copied logs
(below): the open project's side files, the session's secret runtime
variables and the app-wide secrets. The history keeps the newest 50 runs,
at most 256 MiB, and **Delete** removes one. A report (RUN-08: JUnit XML,
JSON or HTML) is written only where the user picks in the native save
dialog, masked again when it is saved; the HTML page has no scripts,
links or remote resources. The same limits as for copied logs apply:
masking is by value, and a secret sent transformed is not recognised.

## The console

The in-app console (DBG-06, [frontend.md](frontend.md#console)) shows the
app log, so it gets the same redaction as the log file. Its records come
from a `logging.Ring` that `logging.New` feeds below the `Redactor`: values
under a sensitive key (`authorization`, `cookie`, `password`, `token`,
`client_secret` and the rest of the list in `internal/core/logging`) and
every registered secret value, also URL-escaped, are `[REDACTED]` before a
record reaches the ring. The ring applies the redactor again whenever
records are read or streamed, so a secret registered after it was logged
is masked there too. Only redacted, flattened text crosses the bridge; the
ring keeps at most 2000 records, each value cut to 2 KiB, in memory only,
and Clear drops them (the log file is not touched). The ring also keeps
debug records, which the log file does not, so debug logging follows the
same rule as the rest: never log a secret under a key that is not
sensitive without registering it. Copy puts the shown, redacted lines on
the clipboard, and Save writes them to a file the user picks, only when
the user asks, masked once more ([Copied and saved logs](#copied-and-saved-logs)).

## Clearing local data

PRV-13: Settings, Privacy, **Local data** clears what PostSonde
keeps about the projects on this computer with one action
(`LocalDataService`, `internal/app/services/localdata`; the files:
`internal/core/localdata`; the page: `components/settings/LocalData.tsx`).

| Kind (tick) | Files removed from each project's local directory | Also dropped in memory |
|---|---|---|
| Request history | `history/` | the open project's through `HistoryService.Clear` (`history:changed`); another project's store, if the session opened it; the responses kept in memory for `{{$response}}` references (DYN-06) |
| Run history | `runs/` | nothing: the runner reads the files on each call |
| Cookies | `cookies.json`, `cookies/` (every jar, per environment too); `cookie-settings.json` stays | every jar of the project, emptied and never saved again (`cookies.Registry.Forget`), so a send still holding one does not write it back |
| OAuth 2.0 tokens | `secrets/oauth2/` | the tokens held in memory (`tokenstore.Store.Clear`, `Forget`); the Auth tabs are told (`http:oauth2-token-changed`) |
| Cached schemas | `graphql-schemas/` (GraphQL introspection) and `grpc/reflection/` (gRPC server reflection snapshots); schema and `.proto` files of the project are not cached and stay | the GraphQL schemas held in memory and their search indexes (`graphql.ForgetSchemas`); the gRPC reflection cache keeps nothing in memory |
| Runtime variables | `runtime-variables.json`, `secrets/runtime/` | the open project's, those of the session included (`runtimevars.Registry.Clear`, `runtimevars:changed`) |

What an interrupted write left next to these files (`.<name>.tmp-*`) goes
with them.

- **Scope.** *This project* (the open one) or *All projects*: every
  local directory under `<data>/workspaces/`, copies included, whether
  the project was opened in this session or not. Without an open project
  only *All projects* is offered.
- **What each holds** is shown next to its checkbox, measured by Go
  (`Usage`): the number of files and their size, for the scope chosen.
  Every kind starts ticked.
- **Confirmation.** *Clear selected data…* opens a danger dialog that
  lists what will be removed, with its size, for which project or how
  many, says it cannot be undone and that the rest stays, and is enabled
  only once the user types the confirmation word (CLEAR). Afterwards a
  line says how many files were removed and how much, and names any kind
  that could not be removed everywhere, with Go's reason; the sizes are
  measured again.
- **Locked secrets.** Nothing is read to be cleared: the files are
  deleted, not rewritten, so the OAuth 2.0 tokens and the secret runtime
  variables go while the secrets are encrypted and locked (SEC-03), and no
  master password is asked for. Unlocking was ruled out: there is nothing
  to decrypt, and asking for a password to delete one's own data would
  only get in the way.
- **What stays**: the project folders, `local.json`, the active
  environment, the secrets of requests, folders, environments and the
  project (`secrets/requests/`, `secrets/environments/`), local values,
  scratch requests, the trash, the view state, the cookie settings,
  `vault.json` and the app-wide secrets, settings, recent projects, logs.
  Kinds not ticked stay as they are.
- **Order.** For each project and kind, the service that holds it in
  memory is told first (`WithHook`), then the files are removed. A hook
  that fails keeps that project's files of that kind (`failed` in the
  result); the other projects and kinds go on.

Tests: `TestClearLocalData` (internal/app) fills every kind in two
projects through their stores, the open one in memory too, encrypts and
locks the secrets, clears some kinds of the open project and checks that
only those are gone, then clears everything everywhere and scans the data
directory: no file of those kinds and no byte of their values is left,
nothing is written back after the cookie save delay, and every other file
is still there. Also `internal/core/localdata` (paths, sizes, leftovers,
failures), `internal/app/services/localdata` (scopes, errors, hooks),
`TestRegistryForget` (cookies) and `LocalData.test.tsx`.

**Limits.** The app log and the console are not cleared: they are
redacted, app-wide, and the log file is open while PostSonde runs. The
bodies kept for Save body (`<data>/tmp/response-bodies/`) are not listed:
they are dropped after an hour and at every start and shutdown. A send
still running while its project's data is cleared may record its entry
or store its token afterwards. Views already open (the cookie manager, a
GraphQL request's schema) show what they loaded until they reload.
Deleting a file does not wipe the blocks it used on disk; full-disk
encryption covers that.

## Copied and saved logs

DBG-07: an entry of the response timeline, the console, the connection
log or a message log (WebSocket, SSE, Socket.IO, TCP, gRPC, GraphQL
subscriptions, the local servers' traffic) can be copied, and each of them
saved as plain text or JSON to a file picked in the native save dialog
([frontend.md](frontend.md#copying-and-saving-logs)). Some of these views
show what was sent or received as it was, secrets included: a WebSocket
URL or message built from a secret variable, a server's captured request.
So nothing is copied or saved as the UI has it: `LogExportService`
(`internal/app/services/logexport`, `internal/core/logexport`) masks the
text first, every time.

- **What is masked.** Every value PostSonde keeps as a secret, read when
  the copy or save happens (so a secret typed a moment ago counts): the
  open project's local side files (typed passwords, tokens, API keys and
  signing secrets of requests, folders and the project; secret folder,
  global and environment variables:
  `<local>/secrets/requests/` and `<local>/secrets/environments/`), the
  stored OAuth 2.0 tokens (`<local>/secrets/oauth2/tokens.json`), the
  secret runtime variables (`<local>/secrets/runtime/`, and those of the
  session, from memory) and the
  app-wide `<data>/secrets/app.json` (certificate passphrases). Each
  occurrence, also URL-escaped, becomes `••••••`, as in the timeline. On
  top of that, the credentials after the scheme of every line that is an
  `Authorization` or `Proxy-Authorization` header field (a Digest
  `response` hash only), and in JSON the string value of an
  `authorization` or `proxy-authorization` member.
- **JSON stays JSON.** In a JSON copy or file each string is masked on
  its own and the rest is kept as it was, so the result still parses.
- **The timeline and the console were masked already** (above); this is a
  second pass, not a reveal. Nothing in these views shows a secret on
  demand, and copy and save have no "with secrets" choice.
- **Where it goes.** The clipboard, or the path picked in the save dialog
  (written atomically, `0644` like other exported files). Nothing else is
  written; the app log records only that a log was saved, its format,
  size and whether something was masked, never its content.
- **Limits.** Masking is by value, like the timeline's: a short secret can
  hide the same characters elsewhere, and a secret sent transformed
  (hashed, Base64 in a body) is not recognised. Values made for one send
  and never stored (a signed JWT, an OAuth 1.0 signature, a netrc
  password, a `{{$prompt}}` answer) are masked in the HTTP timeline and
  the history when they are built, but a message log of another protocol
  that shows one (a token in a WebSocket URL) is masked only where it is
  an Authorization credential. A side file that cannot be read is skipped
  (and logged): its secrets cannot be sent either. A secret typed but not
  yet saved (the auto-save is pending) is not known yet. **Copy hex** and
  **Copy Base64** of a binary message copy its bytes as received.

## Secrets in errors

Error messages written by PostSonde never contain values (`errs.Error`:
"It must not contain secret values"; header and URL checks say which
field, not what is in it). Causes from libraries can: net/http's
`*url.Error` repeats the URL, query and all (`Get "https://…?key=…":
EOF`), and a message names the host it could not reach. Such an error
reaches the UI (the `causes` of the ErrorDTO), the app log and the
console (`operation finished … error=…`), which are not masked with the
secret variables' values. So the HTTP service masks the error of a send
that fails (HTTP and GraphQL requests, a repeat run) and of an event
stream (SSE requests, GraphQL subscriptions over SSE) with that send's
secrets before returning it: `errs.Redact` passes the message, hint,
details and every cause through the timeline's masker, keeping the kind
and code. The history's failure entry is masked on top
([Request history](#request-history)).

The Socket.IO service logs a connection's scheme and host only (a token
in the query is common there), like the WebSocket and GraphQL
subscription services.

**Limits.** Errors of the other protocols' connections (WebSocket,
Socket.IO, gRPC, TCP) are not masked by value; their own messages and
details name hosts and never values, but a library cause could quote one.
Their message logs show what was sent and received as it was
([Copied and saved logs](#copied-and-saved-logs)), masked when copied or
saved.

## Checks: secrets never unmasked (SEC-01)

The values of secret variables, of every level (global, environment,
folder), and typed auth secrets are checked on every path that shows or
writes something about a request:

| Path | Test |
|---|---|
| The chips, their hover and completion (`HTTPService.Variables`), response and timeline, the history files, Copy as cURL (the snippet generator), the HAR export, the error of a failed send and of an event stream | `TestSecretsNeverUnmasked`, `TestSecretHostMaskedInErrors` (internal/app/services/http); also `TestSendSecretGlobalAndFolderVariables`, `TestSendRecordsHistory`, `TestCurlCommand`, `TestExportHAR` |
| The snippet panel: resolved masked by default for secret variables of every level and every auth type, reveal per call and never kept, templated without any secret or local value | `TestSnippetModesAndSecretVariables`, `TestSnippetAuthSecrets`, `TestSnippetOAuth2` (internal/app/services/http), `TestModesGolden` (internal/core/snippet), `SnippetPanel.test.tsx` |
| The hover, a title's description and completion in the UI, even for a value that reached it | `templateVariables.test.ts`, `editor/templateVariables.test.ts`, `templateCompletion.test.ts` |
| The Environments view (password fields, the eye) | `EnvironmentsPanel.test.tsx` |
| Copied and saved logs: secrets of requests, folders, the project, environments and the app | `TestKnownSecrets` (internal/app), `TestMask*` (logexport) |
| The console and the log file: redaction of sensitive keys and registered values | `internal/core/logging` tests |
| Postman export: no secret value, no local value | `TestExportPostmanLeavesOutSecretAndLocalValues`, `TestExportPostmanRoundTrip` |
| Environment export: no secret value, no local value, in both formats | `TestExportLeavesOutSecretAndLocalValues` (internal/app/services/environments), `TestSharedLeavesOutSecretAndLocalValues`, `TestExportEnvironmentRoundTrip` |
| Saved exchanges: the send's and the stored secrets, bodies included | `TestSaveExchange`, `TestSaveExchangeBinary` (internal/app/services/http) |
| Clearing local data: nothing of the cleared kinds left on disk, locked secrets included | `TestClearLocalData` (internal/app) |
| Folder zip: no secret kept locally or typed by hand; crafted zips refused | `TestExportHoldsNoSecrets`, `TestParseRefusesUnsafeZips` (internal/core/importer/projectzip), `TestExportFolderZipRoundTrip`, `TestImportUnsafeZip` |
| Import: secrets to the local side files, never shown in the preview or report | `TestProjectFilesNeverHoldSecrets`, `TestImportPostman`, `TestImportPreviewAndReportHoldNoSecret` |
| Project files: no secret and no local value | `TestProjectFilesNeverHoldSecrets`, `TestLocalValuesStayOutOfTheProject`, `TestLocalValues` |
| Errors: masked by value | `TestRedact*` (internal/core/errs), `TestLogURL` (socketio) |

## Safe network defaults (PRV-11)

| Default | Where | Locked in by |
|---|---|---|
| **TLS verification on.** A client built with zero `TransportOptions` verifies certificates against the system roots, TLS 1.2 minimum; a request without `verifyTls` verifies. Turning it off is an explicit choice: per request (`verifyTls: false`, HTTP-23, also for WebSocket and gRPC) or for HTTP and GraphQL requests without their own setting (`network.verifyTls: false` in settings.json, default `true`). Off is always marked: a "TLS unverified" badge in the URL bar and on the response's status line. | `httpclient.NewTransport`, `request.BuildHTTP`, `settings.Defaults` | `TestSafeDefaultTLSVerification`, `TestBuildHTTPSafeDefaults`, `TestBuildHTTPRedirectAndTLSSettings`, `TestSendTLSVerification` |
| **Custom CAs only add trust, and only from files the user chose.** The CA bundles of `network.caBundles` (NET-04) are added to the system roots, never replace them, and only the paths the user picked in the file dialog (or wrote into settings.json) are read; nothing is fetched or installed in the OS trust store. The settings keep paths only, not certificate contents; a private key found in a bundle is ignored and flagged. A bundle that can no longer be read fails TLS connections with an error rather than being skipped silently. Verification off skips the bundles too. | `tlsconf.Trust`, `tlsconf.InspectBundle`, `settings.validCABundles` | `TestTrustRoots`, `TestTrustAddsToSystemRoots`, `TestInspectBundleWarnings`, `TestSendCustomCA`, `TestConnectCustomCA`, `TestInvokeCustomCA` |
| **Redirects capped.** Followed by default, at most `DefaultMaxRedirects` (10); a loop fails with `httpclient.too_many_redirects`. `maxRedirects: 0` follows none (HTTP-22); no value means "unlimited". | `httpclient.Settings`, `checkRedirect` | `TestSafeDefaultRedirectCap`, `TestRedirects`, `TestRedirectFollowAndLimit`, `TestBuildHTTPSafeDefaults` |
| **Authorization never forwarded to another host.** On a redirect to another origin (scheme, host or port; subdomains included) `Authorization`, `Proxy-Authorization` and `WWW-Authenticate` are dropped, in any spelling, and stay dropped for the rest of the redirects. net/http alone would keep them for a subdomain or another port, so `checkRedirect` drops them itself. Forwarding `Authorization` is an explicit per-request option (`forwardAuthorization: true`, HTTP-22) that the Settings tab warns about. | `httpclient.checkRedirect` | `TestSafeDefaultAuthorizationNotForwarded`, `TestRedirectSensitiveHeaders`, `TestSameOrigin`, `TestRedirectForwardAuthorization` |

Changing any of these is a product decision: update the tests and this
table with it.
