# Windows and the single instance

APP-06: one PostSonde process at a time per set of app directories,
with as many windows as the user opens. Starting PostSonde again opens a
window in the running process instead of a second process.

This step is **one open project per process**: every window shows the
project that is open, and each window has its own tabs, drafts and view.
A window cannot show another project than the others yet; see
[Not handled yet](#not-handled-yet) for why and what that needs.

| Where | What |
|---|---|
| Lock and launch | `cmd/postsonde/main.go` (`app.New` before the directories are set up), `internal/app/app.go` (`Options.InstanceID`, `appOptions`), `internal/app/windows.go` (`InstanceID`, `singleInstanceAvailable`) |
| Windows | `internal/app/windows.go` (`windowHost`), `internal/app/windowctx` (the calling window's ID) |
| Service | `internal/app/services/window` (`WindowService`: `Current`, `NewWindow`, `OpenInNewWindow`; `Launch`, `ProjectArg`) |
| Edits from several windows | `internal/app/services/collection/windows.go` (versions, `collection:edited`) |
| Refusing to switch the project | `internal/app/services/workspace` (`WithWindows`, `workspace.shared_by_windows`) |
| UI | `frontend/src/commands/windows.ts`, `commands/windowId.ts`, `lib/windows.ts`; the version handling in `commands/autosave.ts` and `commands/externalChanges.ts` |

## What is per window and what is shared

Every window loads the same page, so everything the page holds is per
window. Go has one set of services for the process, so everything they hold
is shared.

| Per window (the page's stores) | Shared (Go, one per process) |
|---|---|
| Open tabs, the active and pinned tabs, the sidebar and layout, focus mode | The open project (`workspace.Manager`), its request tree, pending auto-saves and conflicts (`CollectionService`) |
| Drafts of open requests and folders, their undo history, sub-tabs | Settings (`settings.json`), the admin policy, themes, app secrets |
| Responses shown, message logs, the console panel's view | The project's cookie jars, request history, environments and the active environment, GraphQL schema cache |
| The requests and streams started from the window (operation IDs) | Connections (TCP, WebSocket, Socket.IO), local servers, the connection log, the log ring |
| Whether the window is hidden (`SetWatching`) | The polling for changes on disk (runs while any window is shown) |

The view state saved per project (open tabs, expanded folders;
[workspace.md](workspace.md#view-state)) is written by every window: the
window that changed its tabs last wins, and a new window starts with the
tabs saved last.

The window title is the project's name, `Shop API - PostSonde`, or
`PostSonde` with no project; every window's title follows a switch.

## Events

Wails sends an event to every window. Go keeps doing that, and the events
that belong to one window name it; the page knows its own ID from
`WindowService.Current` (`commands/windowId.ts`) and drops the others'.
Until the page knows its ID it takes every event as its own, as with one
window.

| Event | Goes to | Why |
|---|---|---|
| `workspace:changed`, `collection:changed`, `collection:external-change`, `environments:changed`, `settings:changed`, `history:changed`, `servers:*`, `network:activity`, `console:entries`, `system:themeChanged` | every window, all act on it | the state is shared: every window shows it |
| Operation events (`<domain>:op:*`) | every window | only the window that started the operation knows its ID; the others ignore unknown IDs |
| `collection:saved`, `collection:save-failed` | every window; carry `window` | the `revision` is the editing window's own counter: another window's must not mark a draft saved |
| `collection:edited` (new) | every window; carries `window` | the other windows reload their drafts of the edited items (below); the editing window ignores it |
| `window:open-project` (new) | every window; carries `window` | only the named window opens the project |
| `collection:files-dropped` | **only the window the files were dropped on** (`WebviewWindow.DispatchWailsEvent`) | a drop must not open an Import dialog in every window |

Native dialogs (folder, file, save) open on the window the call came from
(`dialogParent`, from the call's context), not on whichever is focused.

## Edits

Two windows can have the same request open. Go holds one pending version
of it and is the only writer, so two writes never race, and the
external-change machinery ([collection.md](collection.md#external-changes))
still guards against changes made outside PostSonde. What could still go
wrong is a window sending a draft made from content the other window has
changed since: Go would take it as newest, and the other window's edit
would be gone without anyone seeing it.

So every request and folder has a **version** in Go (`windows.go`):

- `Request` and `Folder` return it (`version`, left out while 0);
- `UpdateRequest` and `UpdateFolder` take the version the draft was made
  from (`baseVersion`) and return the new one; the UI keeps it per draft
  and sends it with the next edit (`commands/autosave.ts`);
- an accepted edit, a change of the item's file on disk and a rename of a
  request move the version on; versions are per project folder;
- an edit made from an older version is refused with
  `collection.edited_elsewhere` (kind `io`). The UI shows it like a change
  on disk: the draft keeps its edits, which are held until the user chooses
  Keep mine (sent as an edit of the version Go holds now) or Take theirs,
  and the notice and the comparison say "another PostSonde window"
  (`drafts.otherWindow`);
- after every accepted edit Go sends `collection:edited {workspaceId, ids,
  window}`. The other windows wait until their own queued edits of the
  item have reached Go, then replace their draft with Go's version: it
  already has their accepted edits, since the other window's edit could
  only be taken on top of them. A draft edited while that version is
  fetched keeps its edit, which Go then refuses: a conflict, not a loss.

The in-process event, not the file watcher, keeps the windows in step: the
watcher ignores the app's own writes, and an edit is pending in memory for
400 ms before it is written anyway.

An edit without `baseVersion` (Go callers, older tests) is not checked.
Renames keep working as before: `collection:changed` gives every window's
draft the new name, and they read the new version.

Tests: `internal/app/services/collection/windows_test.go` (a stale draft
is refused; folder settings; changes on disk and renames; windows editing
one request at once with the auto-saver writing, run with `-race`, where
every accepted edit must end up in the file),
`frontend/src/commands/windows.test.ts` (two windows' stores against a fake
of the versioned service: live updates, a refused stale edit shown as a
conflict, Take theirs, Keep mine, another window's save events).

## Shared state from several windows

The shared services were already safe for concurrent calls; with several
windows two of them really can come at once. Checked with `-race` tests:

- **Settings:** patches apply to the settings as they are
  (`settings.Store.Update`); the lists (allowed directories, CA bundles,
  client certificates) were read and written in two steps, so two windows
  adding at once could lose one. They now change under one lock
  (`SettingsService.listMu`). Test: `services/settings/windows_test.go`.
- **Cookies:** every send and the cookie manager use the project's one
  jar, under its lock. Test: `services/cookies/windows_test.go`.
- **History:** recording, listing and deleting from several windows. Test:
  `services/history/windows_test.go`.

## Switching projects

Since every window shows the open project, switching it would switch every
window, and a window could lose an edit still on its way to Go. So while
other windows are open, `WorkspaceService` refuses to open another
project, create one or close the open one: `workspace.shared_by_windows`
(kind `invalid_argument`), shown as a toast with the reason. Opening the
open project again is allowed, and with no project open any window may
open one (the others follow). The refusal is in Go, so every way of
switching (the project menu, the welcome screen, the command line of a
second start) is covered.

## New windows

- **New window** (`window.new`, ⇧⌘N / Ctrl+Shift+N; also in the command
  palette) and **Open in new window** in the project menu open another
  window on the open project (`WindowService.NewWindow`).
- On the welcome screen each recent project has **Open in new window**
  (`WindowService.OpenInNewWindow(path)`): with no project open, the new
  window opens it, and every window then shows it. For another project
  than the open one it is refused (`window.project_differs`).
- Windows are named `main`, `window-2`, …; each gets the same options as
  the first (size, minimum size, file drops, the theme's background) and
  opens where the OS puts it.

## Single instance

Wails' single-instance lock (`application.Options.SingleInstance`,
v3.0.0): a lock file in the user's temp folder and a distributed
notification on macOS, a D-Bus name on the session bus on Linux, a named
mutex and a message window on Windows. The D-Bus name is the lock ID plus
`.SingleInstance` (since v3.0.0-beta.23; beta.22 used
`org.wails_app_<ID with _ for .>.SingleInstance`), so a build from before
the Wails 3.0 upgrade and one from after it don't see each other. The
second process sends its command line and working directory to the first
and exits (`os.Exit` inside `application.New`).

- **One per set of directories.** The lock ID is the app ID
  `io.github.sorrelfox.PostSonde` plus `.i` and 16 hex digits of the
  SHA-256 of the config directory (`app.InstanceID`): a portable copy and a
  `POSTSONDE_HOME` run use other directories and run next to the installed
  app, as before. On Linux the bus name is then, for example,
  `io.github.sorrelfox.PostSonde.i30466b9b10f86597.SingleInstance`, which
  starts with the app ID as a Flatpak would require. Builds from before the
  app ID changed used `app.postsonde.i…`, so an older build and a
  newer one don't see each other either. The GTK application ID is the
  bare app ID for every copy, but Wails creates the `GtkApplication` with
  `G_APPLICATION_NON_UNIQUE`, so it is no second lock.
- **Before anything is written.** `main` only resolves the directories
  (`datadir.Resolve`, which creates nothing) and calls `app.New`, which
  takes the lock. Only then does it run `datadir.Start` (which may move the
  data directory), open the log file and build the services (which read
  the settings and empty the response spool folder). Services are
  registered with `App.RegisterService` in `App.Run`. The WebView2 folder
  of portable mode is chosen before the lock (`webviewDataDir`), creating
  only that folder.
- **Linux without a session bus.** Wails ends the app when it cannot reach
  the session bus. Without `DBUS_SESSION_BUS_ADDRESS` (other than
  `autolaunch:`) or `$XDG_RUNTIME_DIR/bus`, PostSonde runs without the
  lock (`singleInstanceAvailable`).
- **Restart after a data directory change** (SET-03): the old process
  starts the new one while it is still quitting, and Wails does not free
  the Windows mutex or the D-Bus name until the process ends. The new
  process gets `POSTSONDE_RELAUNCHED_FROM=<pid>` and waits (up to 10 s) for
  that process to end before it takes the lock (`waitForExit`).
- **No encryption key.** The command line goes to the first process
  unencrypted; any process of the same user could send one. What it can do
  is open a window, or ask a window to open a project folder, which the
  user sees; nothing is run or sent.

### The command line

`postsonde [project folder]`: the first argument that is not an option
(options start with `-`, like macOS's `-psn_…`; `--` ends them), made
absolute against the working directory (`window.ProjectArg`).

- First process: the first window opens the folder once its page has
  loaded (`WindowDTO.openPath`), as if the user had chosen it, so an error
  (not a project, missing) shows on the welcome screen or as a toast. The
  last project is reopened first, as before.
- Second start (`window.Launch`): no folder, or the open project: a new
  window. A folder while no project is open: a new window that opens it. A
  folder of another project: the most recently focused window comes to the
  front and gets `window:open-project`; it switches as if the user had
  chosen the folder, which works when it is the only window and is refused
  with the reason otherwise.

## Closing windows

Closing a window closes only that window: its page's queued edits went to
Go as they were made, and Go writes them. Closing the **last window quits
the app on every OS, macOS included** (`ApplicationShouldTerminateAfterLastWindowClosed`;
Wails does the same on Windows and Linux). Staying alive without a window
is the macOS convention, but PostSonde's local servers and connections
belong to what the windows show, and a windowless process that keeps mock
servers listening would be surprising; the dock icon starts PostSonde
again. A closed window is forgotten by the services (`collection.WindowClosed`,
`window.Closed`), so polling resumes or pauses for the windows left.

## Not handled yet

- **A different project per window.** Every service reads "the open
  project" from the one `workspace.Manager` without knowing the window
  (21 call sites across collection, environments, cookies, history,
  GraphQL, gRPC, HTTP variables, servers and log masking), the collection
  service caches one tree, and connections close when "the" project
  changes. Per-window projects need: the calling window in every call and
  in every operation's context (operations outlive the call), a
  `workspace.Manager` session per window, per-project state in the
  collection service (tree, watcher, conflicts, versions keyed by project
  folder; the auto-saver already keys edits by tree), connections owned by
  the window that opened them, and `workspace:changed`,
  `environments:changed` and the history and cookie events sent to the
  windows of that project only. The window IDs, `windowctx`, the per-window
  events and the versioned edits of this step are the parts that carries
  over.
- New windows open where the OS puts them, possibly right on top of the
  first; they do not cascade.
- macOS "open with" (a folder dropped on the Dock icon) arrives as an Apple
  event, not a command-line argument, and is not handled.
- Environment variable edits keep their own debounce in the UI
  ([collection.md](collection.md#not-handled-yet)) and are not versioned:
  two windows editing the same environment's variables at once can still
  overwrite each other.
