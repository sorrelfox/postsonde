# Privacy

PostSonde works on your computer and nowhere else. There's no account, no
telemetry and no phone-home, and everything except the requests you send
works without a network.

## What PostSonde never does

- **No telemetry.** No analytics, usage metrics, crash reports or
  "anonymous statistics". Crashes and errors go to the local log file and
  the in-app console only.
- **No phone-home.** No update checks, licence checks, feature flags, news
  feeds or remote config. To update, download a new release yourself
  from the [releases page](https://github.com/sorrelfox/PostSonde/releases).
- **No account.** Nothing to sign up for or sign in to. Every feature
  works without one.
- **No remote UI.** The interface is built into the app: no web fonts,
  CDNs, remote images or iframes. A strict Content Security Policy blocks
  anything else.

## Network traffic PostSonde makes, and when

Every outgoing connection goes through one dialer. That dialer refuses any
dial that doesn't come from something you did, and logs it (see
[docs/privacy.md](docs/privacy.md#go-core-user-initiated-traffic-only-prv-05)).
The status bar and the console's **Connections** view list every
connection the app opens.

| Traffic | When |
|---|---|
| Your HTTP, GraphQL, SSE, WebSocket, Socket.IO, gRPC and raw TCP requests and connections | When you send or connect, run a folder runner, or replay a request a local webhook server received. The proxy you chose (manual, the OS's, a project's or an environment's) carries them. |
| DNS lookups of the hosts in those requests | When the request is sent. Through a SOCKS5h or HTTP proxy, the proxy resolves the name instead. |
| OAuth 2.0 token requests | When a send needs a token, or when you click **Get new token** or **Refresh** in the Auth tab. |
| OAuth 2.0 sign-in in your browser | The authorization code and implicit grants open your system browser only when a new token is needed. PostSonde listens on a loopback port for the redirect and closes it once the redirect arrives, you cancel, or it times out. ID tokens are checked offline: no keys or discovery documents are fetched. |
| GraphQL schema introspection | When you click **Fetch schema** or **Refresh**. A request's schema auto-refresh is off until you turn it on in its Settings tab. |
| Proxy connection test | When you click **Test** in the proxy settings. |
| Links in notes | They open in your system browser, never in the app, and only after you confirm the URL. |

Nothing else goes out. Local servers (mock, webhook, WebSocket, TCP, UDP,
gRPC) **listen** on 127.0.0.1 unless you choose a LAN address. They
accept connections and never open their own.

## Things that stay on your computer

These might look like network activity, but they never leave the machine:

- **Reading the OS proxy settings** (the default "system" proxy mode).
  On macOS this runs `/usr/sbin/scutil --proxy`, on Windows it reads the
  registry, and on Linux it runs `gsettings` or reads the environment.
  In that mode this happens when a dial or the settings screen needs the
  settings, and again every 30 seconds while the app runs. PAC files are
  never downloaded.
- **The OS keychain**, if you keep the secrets key there: the macOS
  `security` tool, `secret-tool` (Secret Service over the local D-Bus
  session) on Linux, DPAPI on Windows.
- **Single instance**: a second start of PostSonde passes its command
  line to the running app through a local OS channel (a distributed
  notification on macOS, the D-Bus session bus on Linux, a window message
  on Windows).
- Files you pick (CA bundles, client certificates, `.netrc`, schema and
  `.proto` files, data files) are read from disk. Nothing is fetched.

## The web view

The UI runs in the OS web view: WKWebView on macOS, WebView2 (Edge) on
Windows, WebKitGTK on Linux. PostSonde turns off what it can. That means no
Safe Browsing or SmartScreen URL lookups, no developer tools in release
builds, Chromium's background networking off in WebView2, and no Wails
"Learn More" menu item that would load wails.io. The OS can still update
the web view runtime itself (Edge's WebView2 runtime updates through
Windows' Edge updater), and that is outside PostSonde's control.

## Checking for yourself

- Watch **Connections** in the console: an idle app has none.
- Use an outbound firewall (Little Snitch, LuLu, Windows Firewall,
  OpenSnitch) or Wireshark, and see that PostSonde connects only when you
  send something. The steps for each OS are in
  [docs/privacy.md](docs/privacy.md#manual-checks), with the results so
  far. On Linux, `scripts/check-no-network.sh` runs a build under `strace`
  (no root needed), online or with the network cut off, and lists every
  connection it attempts.
- The automated checks, and how to re-run the dependency audit, are in
  [docs/privacy.md](docs/privacy.md#audit-what-could-phone-home).

Details on secrets, local files and what PostSonde writes where are in
[docs/privacy.md](docs/privacy.md).
