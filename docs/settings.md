# Settings

PostSonde keeps its app settings in one human-readable JSON file,
`settings.json` (SET-01). You can change them in the app or edit the file by
hand, even while PostSonde is running.

| Piece | Where |
|---|---|
| File format, validation, unknown keys, backups | `internal/core/settings` (`Load`, `Save`, `Store`) |
| Bound service and `settings:changed` event | `internal/app/services/settings` (`SettingsService`) |
| Binding wrappers and the persistence adapter | `frontend/src/lib/settings.ts` |
| Startup, saving and live updates in the UI | `frontend/src/commands/settings.ts`, `components/shell/useSettingsPersistence.ts` |
| The settings screen | `frontend/src/components/settings/SettingsScreen.tsx` |
| Data directory and portable mode (SET-03, APP-04) | `internal/core/datadir`, `internal/app/services/datadir` (`DataDirService`), `frontend/src/components/settings/DataDirectory.tsx`; see [Data directory](#data-directory-set-03) |
| Settings locked by an administrator (SET-02) | `internal/core/settings/policy.go` (`LoadPolicy`, `SystemPolicyPath`, `WithPolicy`), `SettingsService.Policy`, `frontend/src/components/settings/Locked.tsx`, `components/shell/PolicyStatus.tsx`; see [Settings locked by your administrator](#settings-locked-by-your-administrator-set-02) |

## Where the file is

`settings.json` lives in the config directory (package `appdirs`):

| OS | Path |
|---|---|
| macOS | `~/Library/Application Support/PostSonde/settings.json` |
| Windows | `%APPDATA%\PostSonde\settings.json` |
| Linux and other Unix | `$XDG_CONFIG_HOME/postsonde/settings.json`, by default `~/.config/postsonde/settings.json` |
| Any, with `POSTSONDE_HOME` set | `$POSTSONDE_HOME/config/settings.json` |
| Any, in portable mode | `<app folder>/postsonde-data/config/settings.json` ([packaging.md](packaging.md#portable-mode-app-04)) |

Choosing another data directory (below) does not move `settings.json`: it
stays in the config directory, next to `data-location.json`, which
records the choice.

The file does not exist until you first change a setting (or use **Open
settings file**, which writes it with the settings in effect). Until then
every setting has its default.

## The settings screen

The gear at the bottom of the icon rail (or ⌘, / Ctrl+,) shows the settings
screen in place of the sidebar and the work area; the gear again, a sidebar
view or the sidebar toggle go back. Open tabs and editors stay as they were.

- **Pages:** General (theme, custom theme ([themes.md](themes.md)), language and response position; the recent projects;
  the data directory, with **Show** and **Change…**
  ([Data directory](#data-directory-set-03)), and the settings file, with
  **Reveal** to show it in Finder or Explorer), Editor (font, font size,
  tab size, word wrap, line numbers, bracket matching, with a live
  preview), Network
  (default request timeout; the proxy, see
  [network.md](network.md#proxy-settings); TLS: certificate verification, the custom
  CA certificates and the per-host client certificates), Shortcuts (every
  keyboard shortcut, to remap or reset; see [Shortcuts](#shortcuts)) and
  Privacy (the master password of the stored secrets and its idle lock,
  see [privacy.md](privacy.md#encrypted-secrets); the allowed directories for file access, see
  [privacy.md](privacy.md#local-file-access-prv-10); Local data: the request and run
  histories, cookies, OAuth 2.0 tokens, cached GraphQL and gRPC schemas and runtime variables of
  this project or of all projects, with their sizes, and one action that clears the kinds
  ticked after a typed confirmation, see
  [privacy.md](privacy.md#clearing-local-data); then what PostSonde never does, as
  facts rather than switches).
- **Changes apply at once** and are written to `settings.json`; there is no
  Save button. A number field saves 400 ms after you stop typing, and at
  once on Enter or when it loses focus.
- **An invalid value** shows its error under the field and is not saved: the
  file and the app keep the last valid value. The screen checks what it can
  as you type; anything Go refuses (`settings.invalid_value`) is shown the
  same way.
- **Open settings file** opens `settings.json` in your default app for JSON
  files, to edit by hand. Hand edits show up on the screen within about
  2 seconds (see [Editing by hand](#editing-by-hand)).
- **Problems** with the file (see [Problems](#problems)) are listed at the
  top of every page until the file is fixed.
- **Recent projects** (General, STO-06) are not settings and are not in
  `settings.json`: they are the recent-projects list in the app data
  directory (`workspaces/recent.json`, [workspace.md](workspace.md)). Each
  project has **Open** (switches to it, restoring its tabs), **Reveal** and
  **Remove** (the folder and its local data are not touched); the open
  project is marked "open now" and cannot be removed. **Clear** empties the
  list except for the open project. The title bar's project menu has
  **Manage recent projects…**, which opens this page.
- **Locked settings** (SET-02): a setting an administrator locks shows a
  lock after its name and its control is disabled, with the tooltip "Set
  by your administrator". A notice at the top of every page says that
  settings are managed, how many are locked and where the policy file is.
  See [below](#settings-locked-by-your-administrator-set-02).

## Settings locked by your administrator (SET-02)

An administrator can lock settings for everyone on a computer, for example
"TLS verification always on", with a policy file in a system-wide folder
that normal users cannot write:

| OS | Policy file |
|---|---|
| macOS | `/Library/Application Support/PostSonde/policy.json` |
| Windows | `%ProgramData%\PostSonde\policy.json` (usually `C:\ProgramData\PostSonde\policy.json`; the folder is asked of Windows, not read from the `ProgramData` environment variable) |
| Linux and other Unix | `/etc/postsonde/policy.json` |

The location is fixed. `POSTSONDE_HOME`, portable mode and a chosen data
directory do not change it, and there is deliberately no environment
variable or command-line option to point elsewhere: a user could set one to
start PostSonde without the policy. The file must be readable by every
user and writable only by administrators (root, or the Administrators
group). PostSonde reads it once at startup; a change applies at the next
start. Tests pass a path under `t.TempDir()` to `LoadPolicy`, never the
real location.

```json
{
  "version": 1,
  "locked": {
    "network.verifyTls": true,
    "network.caBundles": ["/etc/ssl/certs/corp-root.pem"],
    "privacy.allowedDirs": []
  },
  "requireTlsVerification": true
}
```

| Member | Meaning |
|---|---|
| `version` | Policy schema version, `1`. Missing counts as 1. A file from a newer PostSonde still applies the entries this version understands, with a problem. |
| `locked` | Setting keys as in `settings.json` and the value each always has, with the same types and ranges: `"<section>.<field>"` (the fields in [The file](#the-file)), a top-level member such as `"shortcuts"` (`{}` keeps every default shortcut), or one entry of it, `"shortcuts.request.send": ["Mod+Enter"]`. Any setting can be locked, also those added later (such as `ui.customTheme`), since the value is checked by the same decoder as `settings.json`. A key locks everything under it. |
| `requireTlsVerification` | `true`: no request or connection may turn certificate verification off (below). Also locks `network.verifyTls` to `true`; a `locked` entry setting it to `false` is overridden, with a problem. |

What a lock does:

- **The value comes from the policy**, everywhere: the settings screen,
  every send (the default timeout, verification, trusted CAs, client
  certificates, allowed directories, …) and the window background (the
  theme is read before the window opens).
- **Changing it in the app is refused** with `settings.locked` (kind
  `permission_denied`, detail `field`); a patch that also changes other
  settings changes nothing. Setting it to its locked value is not an error.
  The settings screen disables the control (on the Shortcuts page, Change
  and Reset of each locked shortcut, and Reset all when the whole map is
  locked; other saves keep the locked entries); the title bar's theme button
  is disabled while the theme or the custom theme is locked; a locked
  layout value (sidebar, split) goes back to its locked value after a drag
  or ⌘B and is not saved.
- **A hand edit of `settings.json` is ignored for that key.** The file is
  not rewritten because of it: it keeps your own value, which is written
  back unchanged when another setting is saved and applies again if the
  policy stops locking it. **Open settings file** creates a missing file
  with your values, not the locked ones.

### Requiring TLS verification

With `"requireTlsVerification": true`, a request or connection that turns
certificate verification off (its own `verifyTls: false`, which is project
data, not a setting) is refused before it connects with
`settings.tls_verification_required` (kind `permission_denied`, detail
`protocol`). The hint says to turn verification back on for the request or
to trust a private CA under Custom CA certificates:

| Protocol | Where it is checked |
|---|---|
| HTTP sends, GraphQL queries and schema fetches, SSE streams, GraphQL over SSE, webhook replays | `HTTPService.client` (every client the HTTP service builds) |
| GraphQL subscriptions over WebSocket | `NewGraphQLSubscription` |
| WebSocket | `WebSocketService` connect, and every reconnect |
| Socket.IO | `SocketIOService.Connect` |
| gRPC and gRPC-Web over TLS | `GRPCService.Invoke` (plaintext h2c calls are not affected) |
| TCP over TLS (TCP-02) | `TCPService.Connect` (plain TCP is not affected) |

Local servers (they present certificates rather than verify them) and
exports such as Copy as cURL (`-k`) are not affected.

### Requiring encrypted secrets

`"privacy.requireSecretEncryption": true` in `locked` requires a master
password for the stored secrets: at startup the window asks for one
while there is none, turning encryption off is refused and storing a
secret while it is off fails (`secretstore.required`). Lock
`"privacy.secretsLockMinutes"` too to force an idle lock
([privacy.md](privacy.md#encrypted-secrets)), and
`"privacy.secretsKeyStore": "password"` to keep the key out of the OS
keychain, so the master password is needed at every start
([privacy.md](privacy.md#keychain)).

### A policy file that cannot be used

PostSonde **still starts and applies nothing** from a policy file that
cannot be read or is not a JSON object, and says so in the status bar
("Policy not applied"), at the top of the settings screen and in the log
at error level (`settings.policy_invalid`, detail `path`). An entry that
cannot be used (an unknown setting key, a value of the wrong type or out of
range, an unknown top-level member) is skipped with a problem naming it
(detail `field`); the other entries apply. Refusing to start was
considered, but only an administrator can write the file, so a broken one
is an administrator's mistake rather than a way for a user to get around
it, and a tool that does not start helps nobody fix it.

### Seeing that a policy is in effect

The status bar shows **Managed** with a lock while a policy locks
anything; its tooltip lists the locked keys, whether TLS verification is
required and the policy file. The settings screen's notice says the same,
and every locked row has its lock.

## Shortcuts

The **Shortcuts** page (UI-07) lists every keyboard shortcut with its keys.
**Change** records a new key combination: press it (Escape cancels).
PostSonde refuses one that another shortcut of the same kind already uses
(it names it), one the system or every text field keeps (⌘Q, ⌘H, ⌘M, ⌘R
and ⌘Space on macOS, Alt+F4 elsewhere, copy, paste, cut and select all
everywhere) and a plain character without ⌘, Ctrl or ⌥. It warns, and
saves, when the keys do something else in one place, such as Find inside a
code editor. **Reset** goes back to the default keys, **Reset all** for
every shortcut. Changes apply at once. The code editor's own shortcuts
(Find, Select next occurrence, Toggle comment) cannot be changed. How it
works: [frontend.md](frontend.md#custom-shortcuts).

## Client certificates

Network, TLS, **Client certificates** (AUTH-30): certificates sent to
servers that require mTLS. **Add…** opens a dialog with the host pattern,
the format (PEM certificate and key, or PKCS#12), the files (typed, or
**Choose…** for the native file dialog) and the passphrase. Go reads the
files before saving: a file that does not decode, a wrong passphrase or a
key that is not the certificate's is shown in the dialog and nothing is
saved. Each certificate is listed with its files, its common name, format
and expiry, warnings (expired, not for client authentication) and, if it
can no longer be used (a moved file), its error. **Edit** opens the same
dialog; the passphrase field starts empty and leaving it so keeps the
stored passphrase (**Remove passphrase** clears it). **Remove** stops
sending it and forgets its passphrase; the files are not touched.

The settings file keeps the host pattern and the paths
(`network.clientCerts`); passphrases are kept in PostSonde's local data
(`<data>/secrets/app.json`, [privacy.md](privacy.md)), never in
`settings.json`, and never shown again. A hand-written entry without a
passphrase works for unencrypted files; an encrypted one then reports
`pkcs12.wrong_password` until it is edited on the screen. How hosts match
and what each protocol does: [network.md](network.md#client-certificates).

## Data directory (SET-03)

The data directory holds everything PostSonde keeps on this computer for
your projects: history, cookies, tokens, local secrets, scratch requests,
view state and fetched GraphQL schemas, all under `workspaces/`
([workspace.md](workspace.md#local-side-files)). Settings, logs and the
cache are not in it.

### Where it is

The first rule that applies wins:

| # | Rule | Data directory | Change… |
|---|---|---|---|
| 1 | `POSTSONDE_HOME` is set | `$POSTSONDE_HOME/data` (config, cache and logs beside it) | disabled |
| 2 | Portable mode: a `postsonde.portable` file in the app folder | `<app folder>/postsonde-data/data` (everything under `postsonde-data/`) | disabled |
| 3 | A folder chosen with **Change…** | that folder | yes |
| 4 | Otherwise, the OS default | `~/Library/Application Support/PostSonde` (macOS), `%LOCALAPPDATA%\PostSonde` (Windows), `$XDG_DATA_HOME/postsonde` or `~/.local/share/postsonde` (Linux) | yes |

Rules 1 and 2 put every directory under one root and ignore a choice made
with rule 3. Portable mode, the app folder on each OS and read-only
locations: [packaging.md](packaging.md#portable-mode-app-04).

### Changing it

**Change…** on Settings, General shows the native folder dialog (it can
make a new folder). PostSonde checks the folder at once and says why it
cannot be used:

- it must be a folder you can write to, given by its full path;
- it must not be the current data directory, inside it, or a parent of it;
- it must not be in a project folder (a folder at or above it with a
  `postsonde.json`), so secrets never end up in project files;
- it must not be inside PostSonde's cache or log folder;
- it must be empty (`.DS_Store`, `Thumbs.db`, `desktop.ini` and
  `.localized` do not count) or already hold PostSonde data (a
  `workspaces/` folder). The OS default folder may hold other files (on
  macOS the settings share it; on Linux the web view keeps its storage
  there), as long as it has no PostSonde data for a move.

Then it asks what to do with your data:

| Choice | When offered | At the next start |
|---|---|---|
| **Move my data there** | empty folder (the default) | copies `workspaces/` to the new folder, checks every file (size and SHA-256), then switches. The old folder keeps its copy. |
| **Start fresh** | empty folder | switches to the empty folder; your data stays in the old folder, unused |
| **Use the data in this folder** | folder with PostSonde data | switches to it; your current data stays where it is, unused. There is no merge. |

Nothing changes while PostSonde runs: open projects, cookie jars and the
log keep files open. The choice is saved as a pending change and
PostSonde offers **Restart now** (or **Later**). Restarting saves your
edits and view state first, then quits and starts PostSonde again, which
carries out the change before anything opens the data. Until then
General shows the change under **After restart** with **Cancel change**.

**Safety of a move.**

- The copy goes into the new folder only; the old folder is never changed
  by a move. If copying or the check fails, whatever was copied is
  removed again, the data directory stays as it was, and a problem says
  so. The same happens if the new folder gained PostSonde data in the
  meantime.
- An interrupted copy (a crash, a power cut) leaves a `.postsonde-move`
  file in the new folder; the next start replaces the partial copy.
- After a move, General shows **Old data folder** with **Show**, **Keep**
  (stop mentioning it) and **Delete…**. Delete asks first and then removes
  only the PostSonde data it copied (`workspaces/`) from the old folder,
  then the folder if nothing else is left in it. It never deletes the
  data directory in use or one a pending change goes to. PostSonde never
  deletes the old copy without this confirmation.
- A toast at startup says the move is done, or what went wrong.

**When the data directory is not available** (a chosen folder on a disk
that is not connected, a portable folder that cannot be written, a broken
`data-location.json`), PostSonde still starts but keeps nothing in a data
directory: projects cannot be opened (`workspace.no_data_dir`). It never
falls back to another folder, so your data is never split. The welcome
screen and Settings, General show the problem, and **Choose another data
directory…** lets you pick another one (not in portable mode or with
`POSTSONDE_HOME`). A missing chosen folder is never created, since it may
be a disk that is not mounted.

### The location file

The choice is kept in `data-location.json` in the config directory
(written `0600`, atomically), not in `settings.json`, because it is read
before anything else and a hand edit takes effect only at the next start:

```json
{
  "version": 1,
  "dataDir": "/Volumes/Work/PostSonde",
  "pending": {"action": "move", "dataDir": "/Volumes/Work/PostSonde2", "from": "/Volumes/Work/PostSonde", "requestedAt": "2026-09-19T12:00:00Z"},
  "previous": {"dataDir": "/Users/ann/Library/Application Support/PostSonde", "entries": ["workspaces"], "movedAt": "2026-09-19T12:00:00Z"}
}
```

`dataDir` is left out for the OS default. A pending change whose `from`
is no longer the data directory in effect is dropped. A file that is not
valid JSON, from a newer version or with a relative `dataDir` is not used
(`datadir.location_invalid`); fix or delete it and restart.

### Problems and errors

| Code | When |
|---|---|
| `datadir.unwritable` | The portable folder or `POSTSONDE_HOME` cannot be written; the default data directory cannot be created; the location file cannot be saved; a picked folder cannot be written. |
| `datadir.not_found` | The chosen data directory does not exist at start. |
| `datadir.location_invalid` | `data-location.json` cannot be read or used. |
| `datadir.move_failed` | A pending change could not be carried out (copy or check failed, the folder changed); the old data directory stays in use. |
| `datadir.change_unavailable` | **Change…** in portable mode, with `POSTSONDE_HOME`, or with a broken location file. |
| `datadir.invalid_path` | The picked folder breaks one of the rules above. |
| `datadir.not_empty` | The picked folder has other files. |
| `datadir.invalid_action` | The action does not fit the folder (move into a folder with data, use an empty one). |
| `datadir.delete_failed` | The old copy could not be deleted (or is in use). |
| `datadir.unavailable` | No dialog, file manager or restart in this run (tests). |

## The file

The complete file with every default:

```json
{
  "version": 1,
  "ui": {
    "theme": "system",
    "sidebarCollapsed": false,
    "splitRatio": 0.5,
    "responsePosition": "beside",
    "splitRatioBelow": 0.5,
    "locale": "system"
  },
  "network": {
    "defaultTimeoutMs": 0,
    "verifyTls": true,
    "maxResponseBodyMb": 512,
    "proxyMode": "system"
  },
  "editor": {
    "fontFamily": "JetBrains Mono",
    "fontSize": 12,
    "tabSize": 2,
    "wordWrap": false,
    "lineNumbers": true,
    "matchBrackets": true
  }
}
```

With custom shortcuts, a `shortcuts` section follows `editor` (and `privacy`):

```json
  "shortcuts": {
    "request.send": ["Mod+Shift+K"],
    "view.focusMode": ["F11"]
  }
```

Settings are grouped in sections, one JSON object per area. The `privacy`
section is written only when it holds something (`{"privacy":
{"allowedDirs": [...]}}`). Every field is optional: a missing field, or one set to
`null`, has its default.

| Field | Type | Default | Meaning |
|---|---|---|---|
| `version` | whole number | `1` | Schema version of the file. See [Versions](#versions). |
| `ui.theme` | `"system"`, `"dark"` or `"light"` | `"system"` | Colour theme (UI-04). `"system"` follows the OS setting, also when it changes while the app runs. Also set by the title bar's theme button. |
| `ui.sidebarCollapsed` | `true` or `false` | `false` | Hides the sidebar (⌘B, or Ctrl+B on Windows and Linux). |
| `ui.splitRatio` | number from `0.2` to `0.8` | `0.5` | The request pane's share of the width of the editor area, with the response beside it. |
| `ui.responsePosition` | `"beside"` or `"below"` | `"beside"` | Where the response pane sits (UI-02): right of the request, or under it. On the settings screen: General, Appearance, Response position. Switches live; open editors keep their state. |
| `ui.splitRatioBelow` | number from `0.2` to `0.8` | `0.5` | The request pane's share of the height of the editor area, with the response below it. Each layout keeps its own split. |
| `ui.customTheme` | `""` or a `.json` file name (no directory, not starting with `.` or `_`, at most 128 bytes) | `""` (left out of the file) | The custom theme (UI-05): a theme file in `<data>/themes/`. While it is set and the file has no errors, its colours and its base (`dark` or `light`) replace `ui.theme`'s. On the settings screen: General, Appearance, Custom theme. The title bar's theme button sets it back to `""`. See [themes.md](themes.md). |
| `ui.locale` | `"system"` or a language tag: 2 or 3 letters, then subtags of 1 to 8 letters or digits joined by `-` (`"en"`, `"en-US"`, `"zh-Hant-TW"`), at most 35 characters | `"system"` | The language of the UI (UI-10). `"system"` uses the OS language when PostSonde has it, else English, and follows it when it changes. A tag PostSonde has no messages for is kept and English is shown, so a file from a newer version stays valid. English is the only language so far; `"en-XA"` is the pseudo-locale, a generated test language (accented, longer text in brackets) for finding text that is not translatable yet. On the settings screen: General, Appearance, Language (the pseudo-locale is listed there only in development builds, or while it is set). Applies at once. See [frontend.md](frontend.md#localization). |
| `network.defaultTimeoutMs` | `0`, or a whole number from `1000` to `86400000` | `0` | Timeout in milliseconds for a request that has none of its own (HTTP-21); `0` means no timeout. The settings screen shows it in seconds. Read on every HTTP send; a request's own `settings.timeoutMs` overrides it ([http-client.md](http-client.md#sending-from-the-app)). |
| `network.verifyTls` | `true` or `false` | `true` | Verify TLS certificates for HTTP and GraphQL requests without their own `settings.verifyTls` (HTTP-23). `false` accepts any certificate; every such request then shows a "TLS unverified" badge in its URL bar and on its response. On the settings screen: Network, TLS, Verify certificates. Read on every send ([http-client.md](http-client.md#tls-verification-http-23)). |
| `network.maxResponseBodyMb` | a whole number from `1` to `16384` | `512` | The largest HTTP or GraphQL response body kept, in MiB (HTTP-37). A longer body is cut there: what was received is kept and shown, marked truncated, with a warning naming the limit. Bodies are written to disk while they arrive (`<data>/tmp/response-bodies/`), so this bounds disk use, not memory; it counts decompressed bytes. On the settings screen: Network, Requests, Response body limit. Read on every send ([http-client.md](http-client.md#large-responses-http-37)). |
| `network.proxyMode` | `"system"`, `"env"`, `"manual"` or `"off"` | `"system"` | Where the proxy of every connection comes from (SOCKS-04, NET-01): `"system"` the operating system's proxy settings (macOS network settings, the Windows proxy settings, GNOME's or the `http_proxy`/`https_proxy`/`all_proxy`/`no_proxy` environment variables on Linux), one proxy for http:// requests and one for the rest, with the system's bypass list; `"env"` the `ALL_PROXY` (or `all_proxy`) environment variable, and directly when it is not set; `"manual"` `network.proxyUrl`; `"off"` no proxy, whatever the system or the environment says. A proxy that is set but cannot be used (a system PAC file included) fails the connections instead of going direct. The default was `"env"` before NET-01; a file that has `"env"` keeps it. On the settings screen: Network, Proxy. Read on every connection, the system's settings at most every 30 s ([network.md](network.md#system-proxy)). |
| `network.proxyUrl` | `""` or a proxy URL: `socks5h://`, `socks5://`, `socks4://`, `socks4a://`, `http://` or `https://`, then `[user[:password]@]host[:port]` | `""` (left out of the file) | The manual proxy. `socks5h` lets the proxy resolve host names, so nothing is looked up on this computer (SOCKS-03); `socks5` resolves them here. `http` and `https` are HTTP proxies (NET-02; `https` is TLS to the proxy, verified with the custom CA bundles too): CONNECT tunnels, and absolute-form requests for http:// URLs ([network.md](network.md#http-proxy)). `socks4a` and `socks4` are legacy SOCKS4 proxies (SOCKS-06): a user ID in the URL and no password (a stored one is dropped), `socks4a` letting the proxy resolve host names, `socks4` resolving them here to IPv4 addresses ([network.md](network.md#socks4-proxy)). The app writes it without a password, which goes to the local secret storage (`<data>/secrets/app.json`) with the user name taken from the URL; a password written here by hand is used too, and is never shown in problems or messages. Kept when the mode changes. |
| `network.proxyBypass` | list of at most 500 entries: `example.com`, `.example.com` or `*.example.com`, `<local>`, an IP address, a CIDR range, each but a range optionally with `:port` | none (left out of the file) | The bypass list (SOCKS-09): targets that connect directly whatever proxy is in effect, the app's or a project's or environment's ([network.md](network.md#bypass-list)). localhost is always bypassed. With `proxyMode` `"env"`, `NO_PROXY` adds to it. An invalid entry is a problem naming it, and the list falls back to none. On the settings screen: Network, Proxy, Bypass list. |
| `network.caBundles` | list of absolute paths, at most 100, no duplicates | none (left out of the file) | PEM files whose certificates every TLS connection trusts in addition to the system's (NET-04): HTTP, GraphQL, WebSocket and gRPC. On the settings screen: Network, TLS, Custom CA certificates, where **Add file…** shows the native file dialog and checks the file first, and each file is listed with its certificates, warnings (an expired CA) and, if it can no longer be read, its error. The files are read when a connection needs them and re-read when they change ([network.md](network.md#custom-ca-certificates)). |
| `network.clientCerts` | list of at most 100 objects `{id, host, certFile, keyFile, pfxFile}`, IDs different | none (left out of the file) | Per-host client certificates for mTLS (AUTH-30, NET-05): `host` is a pattern (`api.example.com`, `*.example.com`, `localhost:8443`), with either `certFile` (PEM, optionally with `keyFile`) or `pfxFile` (PKCS#12), absolute paths. Every TLS connection to a matching host (HTTP, GraphQL, SSE, WebSocket, gRPC) sends it when the server asks. Passphrases are never here: they are in the local secret storage under the entry's `id`. See [Client certificates](#client-certificates) and [network.md](network.md#client-certificates). |
| `privacy.allowedDirs` | list of absolute paths, at most 100, no duplicates | none (the section is left out of the file) | Directories whose files a project may read besides the open project's folder, which is always allowed (PRV-10): `{{$file PATH}}` (DYN-08), JWT key and netrc files named by an auth, schema files of assertions and GraphQL requests, `.proto` files and import paths of gRPC requests and mock servers, and servers' certificate files. On the settings screen: Privacy, Local files, Allowed directories for file access, where **Add folder…** shows the native folder dialog and **Remove** takes one out; a folder that no longer exists is listed with an error and allows nothing. The editors' file pickers offer to add the folder of a chosen file that is outside (`AllowDirectory`). Read whenever such a file is read. App-wide and never in a project, so a project cannot widen it ([privacy.md](privacy.md#local-file-access-prv-10)). |
| `privacy.secretsLockMinutes` | whole number of minutes, 0 to 1440 | `0` (never; left out of the file) | Locks the encrypted secrets once none has been read or written for that long. On the settings screen: Privacy, Stored secrets, Lock after (never, 5, 15, 30 minutes, 1 or 4 hours; another value set by hand is listed too). Saved through `SecretsService.SetIdleLock`, not the settings DTO; read on every use of a secret ([privacy.md](privacy.md#encrypted-secrets)). |
| `privacy.requireSecretEncryption` | `true` or `false` | `false` (left out of the file) | Meant to be locked to `true` by the admin policy (SET-02): the window asks for a master password while there is none, turning encryption off is refused and no secret is stored while it is off (`secretstore.required`). Not shown on the settings screen; the Privacy page says when it is on ([privacy.md](privacy.md#encrypted-secrets)). |
| `privacy.secretsKeyStore` | `"keychain"` or `"password"` | `"keychain"` (left out of the file) | Where the key of the encrypted secrets is kept besides behind the master password: `"keychain"` also keeps it in the OS keychain (macOS Keychain, the Secret Service through `secret-tool`, Windows DPAPI), so the secrets unlock at startup; `"password"` keeps it behind the password only. On the settings screen: Privacy, Stored secrets, Unlock with. Saved through `SecretsService.SetKeyStore`, which moves the key into or out of the keychain; set by hand in the file, the key is stored at the next unlock but not removed ([privacy.md](privacy.md#keychain)). |
| `editor.fontFamily` | the name of one font, up to 64 characters | `"JetBrains Mono"` | The font of every code editor and response viewer, and of one-line fields such as the URL (UI-08). `"ui-monospace"` or `"monospace"` is the system's monospace font. No quotes, commas, semicolons, backslashes, braces, angle brackets or control characters, and no spaces around it. A font that is not installed falls back to the built-in JetBrains Mono. The settings screen offers common coding fonts; one set by hand is shown there too. |
| `editor.fontSize` | whole number from `8` to `32` | `12` | Font size of code editors and response viewers, in pixels; the line height follows it. |
| `editor.tabSize` | whole number from `1` to `8` | `2` | Width of a tab and of one indent level (Tab, auto-indent), in spaces. The settings screen offers 2, 4 and 8. |
| `editor.wordWrap` | `true` or `false` | `false` | Wrap long lines instead of scrolling sideways. |
| `editor.lineNumbers` | `true` or `false` | `true` | Show the line-number gutter. |
| `editor.matchBrackets` | `true` or `false` | `true` | Highlight the bracket matching the one at the cursor. |
| `shortcuts` | object of at most 200 shortcut ids, each with a list of 1 to 4 different chords | none (left out of the file) | Custom keyboard shortcuts (UI-07): the keys that replace a shortcut's default ones. An id is the shortcut's registry id (`request.send`, `palette.open`, `view.focusMode`; the full list is `frontend/src/lib/shortcuts/registry.ts`). A chord is written the same on every platform: modifiers `Mod` (⌘ on macOS, Ctrl elsewhere), `Ctrl`, `Alt` (⌥), `Shift`, then one key: a letter, digit or symbol (`K`, `1`, `/`), or a key name (`Enter`, `ArrowUp`, `F6`, `Space`), joined by `+`: `"Mod+Shift+K"`. An invalid entry is reported and dropped, and that shortcut keeps its default keys; the other entries still apply. Ids the app does not know are kept in the file and ignored (a newer version's shortcuts survive). Set on the settings screen's Shortcuts page; a hand edit applies at once. |

The editor settings apply at once to every open editor, also after a hand
edit.

PostSonde writes the file in a fixed format (two-space indentation, known
keys in the order above, unknown keys after them in sorted order), with
permission `0600` when it creates it. Writes are atomic: a temporary file
in the same folder is renamed over `settings.json`, so a crash never leaves
half a file. Leftover `.settings.json.tmp-*` files from a crash can be
deleted.

## Editing by hand

Edit the file with any text editor. A running PostSonde checks it every
2 seconds and applies your changes; you don't need to restart. Changes made
in the app are written to the same file, so edit either way.

What PostSonde does with what it finds:

- **Unknown keys are kept.** Keys it does not know, at the top level or
  inside a section, are left alone and written back when the app saves.
  So are keys written by a newer PostSonde. Only their order may change:
  they are sorted and placed after the known keys.
- **An invalid value falls back to its default.** A value of the wrong type
  (`"splitRatio": "0.5"`), out of range (`"splitRatio": 2`) or not one of the
  allowed strings (`"theme": "blue"`) is ignored. That setting uses its
  default and the rest of the file still loads. PostSonde reports each one
  (see [Problems](#problems)). The next time the app saves, the invalid value
  is replaced by the value in effect.
- **A file that is not JSON** (a syntax error, or not an object) is not used
  at all: PostSonde runs on the defaults and reports where the error is
  (line and column). It never overwrites such a file silently: before it
  first saves a change, it copies the broken file to
  `settings.json.bak-<UTC time>` (e.g. `settings.json.bak-20260915T143000Z`,
  with `-2`, `-3`… if that name exists) in the same folder. Fix the file by
  hand to keep your settings. If PostSonde sees the broken file while it is
  running, it keeps the settings it has, since an editor may be half-way
  through saving.
- **An empty file** counts as a missing one at startup: all defaults, no
  problem. While PostSonde runs, a file that turns empty is taken as a save
  in progress and ignored until it has content again.
- **Deleting the file** while PostSonde runs resets every setting to its
  default.
- **A file PostSonde cannot read** (permissions) is not written either:
  changes are kept until the app quits, and a problem says so.

## Versions

`version` is the schema version of the file, not the app version.

- A file without `version` counts as version 1.
- The version goes up only when a change needs old files to be converted,
  such as a renamed field or one whose meaning changed. Adding a field with
  a default does not change it.
- **Older files are migrated.** Each version bump comes with a step in
  `migrations` (`internal/core/settings/load.go`) that takes the file's
  members from version *n* to *n+1*. Loading runs the steps in order, in
  memory. The file is rewritten in the new version the next time the app
  saves.
- **Newer files are read, not written.** A file from a newer PostSonde loads
  the settings this version knows, and a problem
  (`settings.newer_version`) says so. Changes made in the app then apply
  for the session only and the file stays as it is, so the newer version's
  settings are not lost or downgraded.

## Problems

Everything wrong with the file is reported through the shared error model
([errors.md](errors.md)) and never stops the app from starting:

| Code | Kind | When | Details |
|---|---|---|---|
| `settings.invalid_value` | `invalid_argument` | A value was replaced by its default; also an invalid `Update` from the UI (then nothing is changed or written). | `field` (`ui.splitRatio`), `value` (the JSON as written, up to 64 characters), `expected` |
| `settings.parse_failed` | `parse` | The file is not a JSON object. | `path`, and `line` and `column` for a syntax error |
| `settings.newer_version` | `unsupported` | The file is from a newer PostSonde. | `version`, `supported` |
| `settings.read_failed` | from the cause (`permission_denied`, `io`) | The file exists but cannot be read. | `path` |
| `settings.save_failed` | from the cause | Writing the file or the backup failed. | `path` |
| `settings.open_failed` | `unsupported` (no file, e.g. in tests), else from the cause | **Open settings file** or **Reveal** could not hand the file to the OS. | `path` |
| `settings.locked` | `permission_denied` | An update changes a setting the admin policy locks (SET-02); nothing is changed or written. | `field` |
| `settings.policy_invalid` | `parse`, `invalid_argument`, `unsupported` or the read error's | The admin policy file, or one of its entries, cannot be used. Reported by `SettingsService.Policy`, not `Problems`. | `path`, `field` for an entry, `version` for a newer file |
| `settings.tls_verification_required` | `permission_denied` | A request or connection turns TLS verification off while the admin policy requires it. | `protocol` |

Each has a hint saying what to do. The settings screen lists the load
problems at the top of every page and shows an invalid update under its
field. They are also written to the log and, in the UI, to the console.
Settings that can hold secrets do not put them in `value` or in messages:
an invalid `network.proxyUrl` is quoted without its user info
(`"socks5h://…@proxy:0"`), and the reason goes into the hint.

## For developers

### Go

`internal/core/settings` (standard library only):

- `Settings{Version, UI, Network, Editor}`, `UI{Theme, SidebarCollapsed,
  SplitRatio, ResponsePosition, SplitRatioBelow, CustomTheme, Locale}`
  (`LocaleSystem`, `MaxLocaleLen`), `Network{DefaultTimeoutMs,
  VerifyTLS, CABundles, ClientCerts, MaxResponseBodyMb, ProxyMode, ProxyURL}`
  (`ProxyModeEnv`, `ProxyModeManual`, `ProxyModeOff`;
  `Network.EffectiveProxy(password, getenv)` returns the proxy in effect,
  see [network.md](network.md#where-the-proxy-comes-from)) (bounds `MinDefaultTimeoutMs`, `MaxDefaultTimeoutMs`,
  `MaxCABundles`), `Editor{FontFamily, FontSize, TabSize, WordWrap,
  LineNumbers, MatchBrackets}` (bounds `MinEditorFontSize`…`MaxEditorTabSize`,
  `MaxEditorFontFamilyLen`), `Shortcuts map[string][]string` (custom
  shortcuts, `shortcuts.go`: `ValidChord`, bounds `MaxShortcutOverrides`,
  `MaxShortcutChords`; loaded entry by entry, so one bad entry does not
  drop the others), `Defaults()`, `CurrentVersion`, `FileName`.
  `Settings` holds slices and a map, so compare with `Settings.Equal`, not `==`.
- `Load(path) (Settings, Report, error)`: `Report` has `Exists`,
  `FileVersion`, `Newer`, `Problems []*errs.Error` and `Unknown` (unknown
  members by section, `""` for the top level). The error is the parse or
  read failure; the settings are then the defaults.
- `Save(path, Settings, Unknown)` validates and writes atomically;
  `Marshal` gives the bytes; `Backup(path, now)` copies a file to
  `<path>.bak-<time>`.
- `Settings.Apply(Patch)` validates a partial update (`Patch{UI: UIPatch{…},
  Network: NetworkPatch{…}, Editor: EditorPatch{…}, Shortcuts: *map}` with
  pointer fields; `Shortcuts` replaces the whole map, an empty one removes
  them all).
- `Store`: `Open(path, logger, opts...)` (never fails; an empty path keeps settings
  in memory), `State()`, `Settings()`, `Update(Patch) (Settings, changed,
  error)`, `Reload() changed`, `Create()` (writes a missing file with the
  settings in effect, so it can be opened; leaves an existing one alone).
  It serialises updates, backs up broken files, refuses to write newer or
  unreadable files, and recognises its own writes by content.
- The admin policy (SET-02, `policy.go`): `LoadPolicy(path) Policy` (never
  fails; problems in `Policy.Problems`), `SystemPolicyPath()` (per OS,
  `policypath_*.go`), `Policy{Path, Exists, Locked, RequireTLSVerification,
  Problems}` with `Active`, `IsLocked`, `LockedKeys`, `CheckLocked(key)`,
  `Enforce(Settings)` and `TLSVerificationRequired(insecure, protocol)`;
  `TLSVerificationRequiredError(protocol)`. `Open(path, logger,
  WithPolicy(p))` keeps two sets of settings: the user's (the file's, what
  `Save` writes) and those in effect (`Settings()`: the user's with
  `Enforce`). `Update` applies the patch to the settings in effect to find
  a changed lock (`CodeLocked`), then to the user's, restoring their own
  values of the locked keys. The policy is generic: a lock is applied by
  re-encoding the settings as the file, setting the member and decoding it
  with `parse`, so a new setting needs nothing here as long as `parse`,
  `Marshal` and `Settings.Equal` know it.

`internal/core/datadir` (standard library only): `Resolve(Env)` applies
the precedence above without writing anything; `Start(Env, now)` also
creates the directories, carries out a pending change (`CopyData`) and
returns `Startup` with unusable directories empty and `Problems`;
`Store` (`Info`, `Check`, `Schedule`, `Cancel`, `DeletePrevious`,
`ForgetPrevious`) is the setting at run time. `DataEntries` lists the
top-level entries of a data directory that hold data (`workspaces`); a
feature adding another top-level entry must add it there, or a move leaves
it behind. `cmd/postsonde` calls `Start` and passes the directories,
`app.Config.DataLocation` (the `Store`) and `Relaunch` to `internal/app`.

`internal/app/services/datadir`, `DataDirService`:

| Method | Returns |
|---|---|
| `Info()` | `InfoDTO` `{mode, dataDir, wanted, default, root, marker, pending: {action, dataDir} \| null, previous: {dataDir, movedAt} \| null, moved, problems: ErrorDTO[], changeBlocked: ErrorDTO \| null, canRestart}` |
| `Pick()` | Shows the folder dialog, then `Check`; `CandidateDTO` `{path, exists, hasData, isDefault, canMove}`, `path: ""` when cancelled |
| `Check(path)` | `CandidateDTO`, or the reason the folder cannot be used |
| `Schedule({path, action})` | `InfoDTO` with `pending` set; `action` is `"move"`, `"fresh"` or `"use"` |
| `Cancel()`, `DeletePrevious()`, `ForgetPrevious()` | `InfoDTO` |
| `Reveal(which)` | Shows `"data"` or `"previous"` in the file manager |
| `Restart()` | Quits; after every service has shut down, `app` calls `Config.Relaunch`, which starts the same executable (the `.AppImage` file in an AppImage) with the same arguments |

`internal/app/services/settings`, `SettingsService`:

| Method | Returns |
|---|---|
| `Get()` | `SettingsDTO` `{version, ui: {theme, sidebarCollapsed, splitRatio, responsePosition, splitRatioBelow, customTheme, locale}, network: {defaultTimeoutMs, verifyTls, maxResponseBodyMb}, editor: {fontFamily, fontSize, tabSize, wordWrap, lineNumbers, matchBrackets}, shortcuts: {[id]: string[]}}` (`shortcuts` is `{}`, never `null`, without custom shortcuts) |
| `Update(patch)` | `SettingsDTO`. `patch` is `SettingsPatchDTO` `{ui: {…} \| null, network: {…} \| null, editor: {…} \| null, shortcuts: {…} \| null}`, the same fields, with `null` (or a missing member) for sections and fields left as they are; `shortcuts` replaces the whole map, `{}` removes them all. |
| `Problems()` | `ErrorDTO[]`, the load problems above; `[]` when the file is fine |
| `Location()` | `LocationDTO` `{path, exists, dataDir}`: the settings file (`""` in memory) and the data directory |
| `OpenFile()` | Writes a missing file (`Store.Create`), then opens it in the default app. Error `settings.open_failed`. |
| `RevealFile()` | The same, but shows the file selected in the file manager. |
| `CABundles()` | `CABundleDTO[]` `{path, certificates: [{subject, issuer, notBefore, notAfter, expired, isCa, sha256}], warnings: [{code, message, hint}], error: ErrorDTO \| null}`: `network.caBundles`, each file read now (NET-04) |
| `AddCABundle()` | Shows the file dialog, checks the chosen file with `tlsconf.InspectBundle` (errors `tlsconf.ca_no_certificates`, `tlsconf.ca_invalid`, `tlsconf.file_unreadable`, …; nothing saved) and appends it; then `CABundles()`. Cancelling or a file already listed changes nothing. `settings.pick_unavailable` without a dialog. |
| `RemoveCABundle(path)` | Removes `path` from the list (an unknown path changes nothing); then `CABundles()`. |
| `AllowedDirs()` | `AllowedDirDTO[]` `{path, error: ErrorDTO \| null}`: `privacy.allowedDirs`, each checked now (`settings.allowed_dir_missing` when it is not a directory any more) (PRV-10) |
| `AddAllowedDir()` | Shows the native folder dialog and appends the chosen folder; then `AllowedDirs()`. Cancelling or a folder already listed changes nothing; a path that is not a folder is `settings.allowed_dir_missing`. `settings.pick_unavailable` without a dialog. |
| `RemoveAllowedDir(path)` | Removes `path` from the list (an unknown path changes nothing); then `AllowedDirs()`. |
| `CheckFileAccess(path)` | `FileAccessDTO` `{allowed, folder, locked}`: whether a path as stored in a project (absolute, `~/…`, or relative to the open project) is in the project folder or an allowed directory, links followed; when not, `folder` is the folder that would allow it (the one the file really is in, or the path itself for a folder), and `locked` whether the admin policy locks the list. Reads nothing. `fileaccess.invalid_path` for an empty path or a relative one without a project. The file pickers of the editors call it (PRV-10). |
| `AllowDirectory(path)` | Appends the folder at `path` (the offer after `CheckFileAccess`); then `AllowedDirs()`. A folder already listed changes nothing; a path that is not an existing absolute folder is `settings.allowed_dir_missing`; `settings.locked` when the admin policy locks the list. |
| `ClientCerts()` | `ClientCertDTO[]` `{id, host, kind: "pem" \| "pkcs12", certFile, keyFile, pfxFile, hasPassphrase, subject, issuer, notBefore, notAfter, expired, sha256, chainLength, warnings, error: ErrorDTO \| null}`: `network.clientCerts`, each loaded now with its stored passphrase (AUTH-30). The passphrase itself never leaves Go. |
| `PickClientCertFile(kind)` | The native file dialog for `"cert"`, `"key"` or `"pfx"`; the chosen absolute path, `""` when cancelled. Errors `settings.client_cert_kind`, `settings.pick_unavailable`. |
| `AddClientCert(input)` | `input` is `ClientCertInputDTO` `{host, certFile, keyFile, pfxFile, passphrase: string \| null}`. Loads the files with `tlsconf.LoadClientCert` (errors `tlsconf.host_pattern_invalid`, `tlsconf.client_cert_source`, `tlsconf.client_key_mismatch`, `tlsconf.client_key_encrypted`, `pkcs12.wrong_password`, `tlsconf.file_unreadable`, …; `settings.client_cert_duplicate` for a pattern already listed; nothing saved), stores the passphrase in the local secret storage, appends the entry with a new `id`; then `ClientCerts()`. |
| `UpdateClientCert(id, input)` | The same check, replacing entry `id`; a `null` passphrase keeps the stored one, `""` removes it. `settings.client_cert_not_found` for an unknown ID. |
| `RemoveClientCert(id)` | Removes the entry and its passphrase (an unknown ID changes nothing); then `ClientCerts()`. |
| `Proxy()` | `ProxyDTO` `{mode, url, hasPassword, envVar, envUrl, effective, effectiveHttp, system: SystemProxyDTO \| null, remoteDns, error: ErrorDTO \| null}`: `network.proxyMode` and `network.proxyUrl` (without its password), whether a password is stored, the proxy in `ALL_PROXY`/`all_proxy` (without its password), the proxy connections go through now and whether it resolves host names, or why connections fail now (`netx.proxy_invalid_url`, `netx.proxy_unsupported`). The password never leaves Go. |
| `SetProxy(input)` | `input` is `ProxyInputDTO` `{mode, url, password: string \| null}`. Checks the URL (`netx.proxy_invalid_url`, detail `field` `url`; `"manual"` needs one), moves a password in the URL or in `password` to the local secret storage (`null` keeps the stored one, `""` removes it; it needs a user name in the URL, field `password`), saves the URL without it; then `Proxy()`. A URL without a user name forgets the stored password. `settings.locked` for a locked mode or URL, before the password is stored. |
| `SetProxyBypass(entries)` | Replaces `network.proxyBypass` (SOCKS-09): spaces around entries and empty ones are dropped, each entry is checked (`netx.proxy_invalid_bypass`, details `entry` and `field` `bypass`; nothing saved), `settings.locked` for a locked list; then `Proxy()`. `ProxyDTO` also has `bypass` (the list as written), `noProxyVar` and `noProxy` (`NO_PROXY` or `no_proxy` and its value). |
| `TestProxy(input)` | `input` is `ProxyTestInputDTO` `{target}`, a `host:port`. Connects to it through the proxy in effect and closes the connection (SOCKS-11), as the user's action (`settings.proxy_test`), within 20 s. Resolves with `ProxyTestDTO` `{ok, proxy, target, durationMs, step, hop, error}`: `proxy` without its password, `step` where a failed test stopped (`setup`, `dns`, `connect`, `tls`, `handshake`, `auth`, `target`, `timeout`, `cancelled`), `bypassed` whether other connections to the target skip the proxy (loopback or bypass list), `hop` the `ProxyHopDTO` of the dial as far as it got (address, version, authentication, DNS mode, reply, timings), `error` the `ErrorDTO` of the failure (`netx.proxy_none` with no proxy, `netx.proxy_test_target` for a target that is not `host:port`, the proxy dialer's codes). Rejects only when it cannot run (`unsupported` without `WithProxyTester`). See [network.md](network.md#testing-the-proxy). |
| `Policy()` | `PolicyDTO` `{active, path, exists, locked: string[], requireTlsVerification, problems: ErrorDTO[]}`: the admin policy (SET-02), fixed for the run. `locked` are setting keys (`network.verifyTls`); their values are already in `Get()`. |

Every method that changes a setting fails with `settings.locked` when the
policy locks it; `AddCABundle`, `AddAllowedDir` and `AddClientCert` /
`UpdateClientCert` check first, so no dialog is shown and no passphrase is
stored for a change that could not be saved.

`New(store, emit, logger, opts...)` takes `WithDesktop(Desktop{Open,
Reveal})`, the functions that hand a path to the OS (`internal/app` passes
Wails' `Browser.OpenFile` and `Env.OpenFileManager`; without them the two
methods fail as `unsupported`), `WithDataDir(dir)` for `Location` and
`WithFilePicker(pick)`, the native file dialog of `AddCABundle` and
`PickClientCertFile` (`internal/app/dialogs.go` `pickFile`), and
`WithSecrets(store)`, the `appsecrets.Store` that keeps the client
certificate passphrases and the proxy password (`<data>/secrets/app.json`;
without it they are kept in memory for the session), and
`WithProxyEnv(getenv)`, how `Proxy()` reads `ALL_PROXY` (`internal/app`
passes `Config.Getenv`, `os.Getenv`), `WithSystemProxy(read)`, how the
"system" mode reads the OS proxy settings (the app's cached
`sysproxy` reader; `ProxyDTO.system` is `{source, http, https, socks,
pac, autoDiscovery, bypass, skipped}` and `effectiveHttp` the proxy of
http:// requests), and `WithProxyTester(test)`, how
`TestProxy` connects (`internal/app` passes `proxyTester`, the app's
`netx.ProxyRouter.Test` with the connection log and the guard). The CA bundle and client
certificate lists and the proxy are not in `SettingsDTO` or `Update`: the
screen uses the methods above and calls `CABundles()`, `ClientCerts()`
and `Proxy()` again on every `settings:changed` event.

Event `settings:changed`, payload `ChangedEventDTO`
`{settings, problems, source}`: after every `Update` that changed something
(`source: "app"`) and whenever the file changed on disk (`"file"`).

**Detecting hand edits.** The service polls: every 2 s (`PollInterval`) it
calls `Store.Reload`, which reads the file and compares it with the bytes it
last read or wrote. Reading a file this small costs nothing, works on every
OS and file system (network drives, editors that save by rename), and needs
no file-watching library. Comparing content rather than modification times
means the app's own writes are never mistaken for edits and coarse
timestamps never hide one. The poller starts in `ServiceStartup` and stops in
`ServiceShutdown`.

`cmd/postsonde` resolves the directories with `datadir.Start` (honouring
`POSTSONDE_HOME`, portable mode and the chosen data directory), opens the
rotating log in the logs directory (stderr when there is none) and passes
`Logger` and `SettingsPath` (empty, so in memory, when the config
directory cannot be used) in `app.Config`. Tests pass
no `SettingsPath` (in memory) or one under `t.TempDir()`, never the real
config directory. It also loads the admin policy from
`SystemPolicyPath()`, logs it (problems at error level) and passes it as
`app.Config.Policy`; `internal/app` opens the store `WithPolicy`, applies
it to the theme read before the window opens (`savedTheme`) and hands
`RequireTLSVerification` to the HTTP, WebSocket, Socket.IO, gRPC and TCP
services (`WithRequireTLSVerification`). Tests leave `Policy` zero (no
policy) or load one from a temporary file.

### Frontend

- `lib/settings.ts` wraps the bindings (`getSettings`, `updateSettings`,
  `getSettingsProblems`, `onSettingsChanged`) and provides
  `goSettingsPersistence()`, the `SettingsPersistence` over the service:
  `load()` is `Get` (plus `Problems`), `save(patch)` is `Update`, and
  `subscribe` follows `settings:changed` events from the file. It maps the
  store's flat keys (`splitRatio`, `editorTabSize`) to the file's sections
  (`ui.splitRatio`, `editor.tabSize`).
- `main.tsx` calls `installGoSettings()` (`commands/settings.ts`) before
  rendering. It wraps the adapter in `withLocalStorageMigration`, which
  moves settings saved in `localStorage` (`postsonde.ui`) by earlier
  versions into the file once, then removes them.
- `useSettingsPersistence` (called by `App`) loads the settings at
  startup, saves `theme`, `customTheme` and `sidebarCollapsed` whenever they change
  (`trackSettings`), and applies hand edits live (`followSettingsChanges`,
  which dispatches `settings/load`). The split ratio of the layout in
  effect (`splitRatio` or `splitRatioBelow`) is saved by `commitSplitRatio`
  at the end of a drag. `useEditorPreferencesSync` applies the editor
  settings to every editor ([frontend.md](frontend.md#editor-settings)) and
  `useShortcutOverridesSync` the custom shortcuts to the shortcut registry
  ([frontend.md](frontend.md#custom-shortcuts)). Saves are debounced by 400 ms.
- The settings screen saves through `applySetting(stores, patch)`: it writes
  queued saves first, saves the patch at once and applies it to the store
  only when the save succeeded, so a value Go refuses changes nothing and
  the rejected AppError is shown under the field. `installGoSettings` also
  keeps the file's problems (`getSettingsProblems`,
  `subscribeSettingsProblems`) for the screen; `goSettingsPersistence`
  reports them whenever they change, `[]` once fixed.
- Events with `source: "app"` are the UI's own saves and are not applied
  again, so a slow save cannot move the splitter back.
- The theme is also cached in `localStorage` (`postsonde.theme`) by
  `applyTheme`, only so `index.html` can set it before the first paint.
  The file is the source of truth.
- `ui.theme` can be changed in three places, all saved to the file: the
  title bar's theme button (cycles System → Light → Dark, saved like the
  sidebar through `trackSettings`), the settings screen (General →
  Appearance) and a hand edit. Go also reads it once at startup, before
  the window opens, to paint the window background in that theme
  (`internal/app/theme.go`). How themes are applied and how "System"
  follows the OS on each platform: [frontend.md](frontend.md#themes).
- `ui.locale` (UI-10) is `settings.locale` in the store (`isValidLocale`
  checks the shape as Go does), saved by the settings screen's Language
  row with `applySetting`. `useLocaleSync` (called by `App`) applies it
  with `setLocale` (`src/i18n`) whenever it changes, also after a hand
  edit, and caches it in `localStorage` (`postsonde.locale`) for the first
  frames of the next start ([frontend.md](frontend.md#localization)).
- The admin policy (SET-02): `installGoSettings` loads it once
  (`getSettingsPolicy`) into `commands/settings.ts` (`currentSettingsPolicy`,
  `subscribeSettingsPolicy`, `isSettingLocked(key)`, which takes a file key
  or a store key through `SETTING_FILE_KEYS` in `lib/settings.ts`).
  `trackSettings` and `commitSplitRatio` do not save a locked key and
  reload the settings, so the store goes back to the locked value.
  `components/settings/Locked.tsx` has `useSettingsPolicy`,
  `useSettingLocked`, `LockBadge` and `RowControl`, a disabled `<fieldset>`
  as the control column of a locked row, which disables every control in
  it; a settings row takes the setting's file key (`setting="network.verifyTls"`).
  `components/shell/PolicyStatus.tsx` is the status bar's **Managed**
  indicator.

### Adding a setting

1. Add the field to the section struct in `internal/core/settings`
   (`settings.go`) with a JSON tag and a comment giving its default. Set the
   default in `Defaults()`, the patch field in the section's `…Patch` and its
   check in `Apply`.
2. Decode it in `parse` (`load.go`) with `decodeField`, which falls back to
   the default and reports the problem.
3. Add it to the DTOs and `toSettingsDTO`/`toCore` in
   `internal/app/services/settings/dto.go`, and run `wails3 task bindings`.
4. Map it in `lib/settings.ts` (`toPersisted`, `toPatchDTO`,
   `SETTING_FILE_KEYS`) and the settings store (`settings/load` validates
   it). Compare it in `Settings.Equal`: the admin policy (SET-02) finds a
   changed lock with it.
5. Add its control to the settings screen (`components/settings`), saved
   with `applySetting`, with `setting="<section>.<field>"` on its row (or
   `useSettingLocked` and `RowControl` in a row of its own) so it shows
   locked under an admin policy. The policy itself needs no change.
6. Document it in the table above. Bump `CurrentVersion` and add a migration
   only if old files need converting.

A new section (`"network": {…}`) is a new struct field on `Settings`, its
own `raw…` struct in `load.go` split with `stablejson.SplitUnknown` so its
unknown keys are kept (as `ui` does), and a `MarshalWithExtra` call in
`Marshal`.
