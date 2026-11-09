# Workspaces

A PostSonde project is a folder on disk with a `postsonde.json` file at its
root (STO-01). There is no account and no server. Everything that must stay
on your computer (secrets, local variable values, the active environment,
history, tokens, caches, scratch requests) lives in a separate local
directory that is never part of the project folder (STO-04).

| Piece | Where |
|---|---|
| Open, create, local directory, copies, recent list | `internal/core/workspace` (`Manager`) |
| Marker file format | `internal/core/storage/projectfile`, [file-format.md](file-format.md) |
| Bound service, folder dialog, `workspace:changed` | `internal/app/services/workspace` (`WorkspaceService`), `internal/app/dialogs.go` |
| Binding wrappers | `frontend/src/lib/workspace.ts` |
| View state per workspace (STO-06) | `internal/core/workspace/uistate.go`, `frontend/src/lib/workspaceLocal.ts` |
| Store and commands | `frontend/src/stores/workspace.ts`, `frontend/src/commands/workspace.ts`, `frontend/src/commands/workspaceLeave.ts` |
| Welcome screen, gate, title-bar project switcher | `frontend/src/components/workspace/` |
| Recent projects on the settings screen | `frontend/src/components/settings/SettingsScreen.tsx` (General) |

## Opening and creating

- **Open folder** shows the native folder dialog and opens the chosen
  folder. It must contain `postsonde.json` (choosing the file itself works
  too); parent folders are not searched. A folder without one is an error
  with code `workspace.not_a_project`, and the welcome screen offers to
  create a project there. Opening never writes into the project folder.
- **Create project** shows the folder dialog (it can make a new folder),
  then an inline form for the name, which defaults to the folder name. It
  writes only `postsonde.json`, with a new workspace ID (`ws_` and 16 hex
  digits). The folder may already hold other files. A folder that already
  has `postsonde.json` is refused (`workspace.already_a_project`); the
  welcome screen offers to open it instead. A missing folder is created.
- A marker that is not valid JSON, is of another kind or has no `id` is not
  opened and not changed; the error names the file.
- A marker from a **newer PostSonde** opens (`newerFormat: true`). Features
  that write project files must refuse to write in such a workspace.
- Paths must be absolute. Symlinks are resolved, so the project path is the
  real folder.

## Local side files

```
<data>/workspaces/recent.json                 recent projects and the last open one
<data>/workspaces/<id>/local.json             project path, first and last opened
<data>/workspaces/<id>/environments.json      the active environment (docs/environments.md)
<data>/workspaces/<id>/cookies.json           the shared cookie jar (docs/cookies.md)
<data>/workspaces/<id>/cookies/               one cookie jar per environment, with separate cookies per environment on (CK-03)
<data>/workspaces/<id>/cookie-settings.json   the cookie settings: separate cookies per environment
<data>/workspaces/<id>/secrets/environments/  secret variable values (docs/environments.md)
<data>/workspaces/<id>/secrets/requests/      typed auth passwords, tokens and API keys, and the values of secret folder and global variables, by item id (docs/auth.md)
<data>/workspaces/<id>/secrets/oauth2/        tokens.json: the OAuth 2.0 tokens oauth2 auths obtained, by a hash of the auth configuration (docs/auth.md#token-store)
<data>/workspaces/<id>/secrets/runtime/       values of the persisted secret runtime variables (docs/environments.md#runtime-variables)
<data>/workspaces/<id>/runtime-variables.json the persisted runtime variables (ENV-15); session ones are never written
<data>/workspaces/<id>/local-values/          local values of environment and global variables, which replace their shared values on this computer (SEC-02, docs/environments.md#local-values)
<data>/workspaces/<id>/trash/                 deleted folders and requests (docs/collection.md)
<data>/workspaces/<id>/scratch/               scratch requests, one <request id>.json each (docs/collection.md)
<data>/workspaces/<id>/ui-state.json          open tabs, expanded and sorted folders (see View state)
<data>/workspaces/<id>/graphql-schemas/        GraphQL schemas fetched by introspection, one file per endpoint (docs/graphql.md)
<data>/workspaces/<id>/grpc/reflection/       gRPC services fetched by server reflection, one snapshot per address (docs/grpc-reflection.md#cache)
<data>/workspaces/<id>/history/               the request history, JSON Lines segments with rotation (docs/history.md)
<data>/workspaces/<id>/proxy-scopes.json     the project's and its environments' proxies on this computer (SOCKS-08, docs/network.md#proxy-scopes); their passwords are in secrets/requests/proxy_scopes.json
<data>/workspaces/<id>/runs/                  the run history: past folder runs and their results, no responses (RUN-09, docs/runner.md#run-history)
<data>/workspaces/<id>/...                    side files of later features
<data>/workspaces/<id>@<path hash>/           a copy of the project (see below)
```

Settings, Privacy, Local data clears the history, run history, cookies,
OAuth 2.0 tokens, cached schemas and runtime variables of the open
project's local directory or of all of them, and nothing else
([privacy.md](privacy.md#clearing-local-data)); a feature that adds a
kind of such data adds it to `internal/core/localdata`.

Every file under a `secrets/` directory is read and written through
`internal/core/secretstore`: plain JSON by default, encrypted with
AES-256-GCM once the user sets a master password, with the key file in
`<data>/secrets/vault.json` next to the app-wide `<data>/secrets/app.json`
([privacy.md](privacy.md#encrypted-secrets)). A copy of a project's local
directory keeps its encrypted files readable: a file is bound to its name
below `secrets/`, not to the workspace directory. The other side files,
local values included, are not secrets and stay plain.

`<data>` is the app data directory: `$POSTSONDE_HOME/data` when
`POSTSONDE_HOME` is set, `<app folder>/postsonde-data/data` in portable
mode, the folder chosen on Settings, General, or the OS default
(`~/Library/Application Support/PostSonde` on macOS). See
[settings.md](settings.md#data-directory-set-03), which also covers moving
it: a move copies `workspaces/` as a whole, so every side file of a later
feature must live under it. Directories are created `0700` and files
`0600`.

`<id>` is the workspace ID when it looks like a generated one (`ws_`, then
lower-case letters, digits, `_` or `-`, at most 64 characters). Any other ID,
which only a hand edit produces, is replaced by `x.` and a hash of it, so it
cannot escape the directory, clash on a case-insensitive disk or be a
reserved Windows name.

If the data directory is inside the project folder (for example
`POSTSONDE_HOME` pointing into it, or a portable copy of PostSonde kept in
the project), opening is refused (`workspace.data_dir_in_project`), so
local data cannot end up in project files. **Change…** refuses a folder
inside a project in the first place. Without a usable data directory
(see [settings.md](settings.md#data-directory-set-03)) nothing can be
opened (`workspace.no_data_dir`).

**For later features:** the Go side gets the directory from
`Manager.LocalDir()` (or `Manager.Current().LocalDir`); it fails with
`workspace.none_open` when no project is open. Keep your files in a
subdirectory or a clearly named file of it. The manager is created once in
`services()` (`internal/app/services.go`); pass it to your service's `New`.

## Moved and copied projects

Two folders can have the same workspace ID, for example after copying a
project or checking out a repository twice. `local.json` records which
folder a local directory belongs to, and opening compares it with the
opened folder (`localStatus` in `WorkspaceDTO`):

| Situation | `localStatus` | Local data |
|---|---|---|
| First time this ID is opened | `new` | a new `<id>/` |
| The recorded folder is the opened one | `existing` | `<id>/` |
| The recorded folder no longer has a `postsonde.json` with this ID: the project was moved or renamed | `moved` (`previousPath` is the old folder) | `<id>/` follows it |
| The recorded folder still has this ID: the opened folder is a copy | `copy` (`previousPath` is the other folder) | its own `<id>@<hash of its path>/`, kept for later opens |

Project files are never changed to give a copy a new ID: in a repository
checked out twice that would show up as a change to commit. The two copies
simply never share secrets, history or tokens.

Limit: if the original folder is temporarily unreachable (a disk that is not
mounted) when a copy is opened, the copy is taken for a move and gets the
original's local data. Opening the original later then gives it a copy
directory.

## Recent projects, switching and startup

- Opening or creating a project moves it to the top of the recent list (at
  most 20 entries, one per folder) and makes it the last open project.
- **Startup:** `WorkspaceService.ServiceStartup` reopens the last open
  project. If its folder cannot be opened (deleted, disk not mounted) the
  error is logged, the welcome screen is shown and the entry is kept, so the
  project reopens once the folder is back.
- **Close project** (the × next to the name in the title bar, or Close
  project in the project menu) closes it and clears the last open project,
  so the next start shows the welcome screen.
- **Switching (STO-06).** The project name in the title bar opens the
  project menu: the other recent projects (up to 10), Open folder…, New
  project…, Reveal in Finder (Show in file manager on Windows and Linux),
  Manage recent projects… and Close project. Choosing a recent project
  switches to it. Before Go opens the other project, the UI writes the
  pending edits of the open one and saves its view state (open tabs,
  expanded folders); then the new project's tabs come back as they were:
  order, active tab, pinned tabs, preview tab and recently closed tabs,
  with its expanded folders. Its active environment and cookies are in its
  local directory already. Running requests of the old project are
  cancelled with their tabs, and its WebSocket and TCP connections are
  closed. Local servers keep running: they are not part of a project
  (docs/servers.md). Servers saved in the old project's folders (SRV-04)
  are dropped from the Servers view when stopped, and those of the new
  project are defined, stopped, once its tree loads; none is started
  (docs/servers.md, Saved server configs). See docs/frontend.md
  (Workspace switching).
- **Settings screen.** General lists every recent project with Open,
  Reveal and Remove, and Clear, which empties the list except for the open
  project (so it still reopens on the next start). The open project cannot
  be removed. The list is kept in `recent.json`, not in `settings.json`.
- The welcome screen lists the recent projects. Entries whose folder or
  marker is gone are marked "Not found"; × removes an entry from the list
  without touching the folder or its local data.
- `recent.json` is a cache: if it cannot be read it is treated as empty and
  rewritten.

## View state

Small per-workspace UI state (STO-06) is kept in `ui-state.json` in the
workspace's local directory: `{ "version": 1, "entries": { "<key>": <JSON> } }`.
The UI uses the keys `tabs` (the open tabs, docs/frontend.md),
`tree.expanded` (the expanded folders) and `tree.sort` (the folders shown
sorted by name, `{"topLevel": "name", "folders": {"<id>": "name"}}`,
COL-05) and `response.filters` (each request's response body filter:
syntax, expression and recent expressions, HTTP-36, docs/frontend.md).
It is local data, never written into
the project folder, so switching projects does not dirty a repository; and
since each copy of a project has its own local directory, two checkouts of
one project keep their own tabs.

- Go reads it into `WorkspaceDTO.uiState` (key to JSON text) whenever it
  makes the DTO (Open, Create, Current, `workspace:changed`).
- `SaveUIState` writes one key. The workspace is named by ID and folder and
  must be the open one or one opened earlier since the app started, so the
  UI can save the project it is leaving. Keys are lower-case letters,
  digits, `.` and `-` (at most 64); a value is at most 1 MiB of JSON and a
  workspace keeps at most 32 keys (`workspace.invalid_ui_state`). A `null`
  value removes the key.
- Like `recent.json` it is a cache: a file that cannot be read counts as
  empty and is replaced on the next save.
- Builds before STO-06 kept the same values in the web view's localStorage
  (`postsonde.tabs.<id>`, `postsonde.tree.expanded.<id>`). The UI still
  reads them while `ui-state.json` has no value for the key, and saves the
  next change to the file.

## Service

`WorkspaceService` (TypeScript: `WorkspaceService.*`, wrapped by
`lib/workspace.ts`):

| Method | Returns |
|---|---|
| `Current()` | `CurrentDTO` (`workspace`: `WorkspaceDTO` or null) |
| `Open(path)` | `WorkspaceDTO` |
| `Create({path, name})` | `WorkspaceDTO` |
| `Close()` | nothing; closing with nothing open does nothing |
| `Recent()` | `RecentWorkspaceDTO[]` (`id`, `name`, `path`, `openedAt`, `available`) |
| `RemoveRecent(path)` | nothing |
| `ClearRecent()` | nothing; empties the recent list except for the open project |
| `Reveal(path)` | nothing; shows the folder in Finder or the file manager. Only the open project or a recent one (`workspace.invalid_path` otherwise) |
| `SaveUIState({workspaceId, path, key, value})` | nothing; saves one key of the view state (see View state) |
| `PickFolder(purpose)` | the chosen folder, `""` when cancelled; `purpose` is `"open"` or `"create"` |
| `PickBodyFile()` | the native file dialog for a body or multipart file (HTTP-14, HTTP-15): a `BodyFileDTO` with the path to store (relative to the open project when the file is inside it, else absolute), name, size and `exists`; `path` is `""` when cancelled |
| `BodyFile(path)` | describes a stored body file path (name, size, `exists`, `absolutePath`); a missing file is `exists: false`, not an error |

`WorkspaceDTO` has `id`, `name`, `path`, `localDir`, `localStatus`,
`previousPath`, `newerFormat`, `openedAt` and `uiState`. After `Open`, `Create`,
`Close` and the startup reopen, Go emits **`workspace:changed`** with a
`CurrentDTO`. The UI applies both the call result and the event; applying
the same workspace twice changes nothing. When the open project changes,
the tree and the tabs (with their drafts) of the previous one are dropped,
after their edits and view state were saved. While a project is open, its
files (`postsonde.json`, folders, requests, environments) are polled for
changes made outside PostSonde; polling follows the open project and stops
when it closes ([collection.md](collection.md#external-changes)).

Error codes: `workspace.invalid_path`, `workspace.folder_not_found`,
`workspace.not_a_project`, `workspace.already_a_project`,
`workspace.missing_id`, `workspace.create_failed`,
`workspace.local_data_failed`, `workspace.data_dir_in_project`,
`workspace.none_open`, `workspace.no_data_dir`, `workspace.invalid_purpose`,
`workspace.invalid_ui_state`, `workspace.reveal_failed`,
plus the `projectfile.*` codes for a broken marker.

## Several windows

Every window shows the open project (APP-06, [windows.md](windows.md)).
While other windows are open, `WorkspaceService` refuses `Open` of another
folder, `Create` and `Close` with `workspace.shared_by_windows` (kind
`invalid_argument`, details `windows` and, for `Open`, `path`): switching
the project under a window that may still be sending edits could lose
them. Opening the open project again is allowed (`Manager.IsCurrent`
compares resolved folders), and nothing is refused while no project is
open. The app passes the count of the other windows (`WithWindows`); the
UI shows the error as a toast.

The view state (open tabs, expanded folders) is saved by every window;
the last one to change it wins.

## Not handled yet

- Two PostSonde windows or instances with the same project open: there is
  no lock.
- The native folder dialog cannot be closed from the UI; cancelling the
  call's promise does not close it.
