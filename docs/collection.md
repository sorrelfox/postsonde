# Collections on disk

The requests of a project are a tree of plain files (STO-02): every folder
is a directory with a `_folder.json`, every request is one JSON file, and
the project folder, marked by `postsonde.json`, is the top of the tree.
What goes inside each file is in [file-format.md](file-format.md); this page
covers how PostSonde reads the tree, names files and keeps them unchanged.

| Piece | Where |
|---|---|
| Loading, saving, IDs, order, file names | `internal/core/collection` (`Load`, `Tree`, `Slug`, `UniqueName`) |
| Tree operations and the trash | `internal/core/collection` (`ops.go`, `trash.go`: `Tree.CreateFolder` … `Tree.Restore`, `Trash`) |
| File contents | `internal/core/storage/projectfile`, [file-format.md](file-format.md) |
| Bound service (`Tree`, `Reload`, operations, `collection:changed`; `Request`, `UpdateRequest`, `Flush`, `FlushAll`, `collection:saved`, `collection:save-failed`) | `internal/app/services/collection` (`CollectionService`) |
| Debounced saver | `internal/core/autosave` (`Saver`) |
| Binding wrappers | `frontend/src/lib/collection.ts` |
| The tree in the UI | `frontend/src/components/tree`, `frontend/src/commands/collection.ts`, [frontend.md](frontend.md#collection-tree) |
| New folder and New request dialogs (`createItem`) | `frontend/src/components/tree/NewItemDialog.tsx`, [frontend.md](frontend.md#new-folder-and-new-request) |
| Rename, duplicate, delete with Undo (`renameNode`, `duplicateNode`, `deleteNode`, `restoreNode`) | `frontend/src/components/tree/useTreeActions.tsx`, `InlineRename.tsx`, `TreeActionDialogs.tsx`, [frontend.md](frontend.md#rename-duplicate-and-delete) |
| Scratch requests (`Scratch`, `Tree.AddRequest`; `CreateScratch`, `SaveScratch`, `DeleteScratch`; `createScratchRequest`, `saveScratchTo`, `deleteScratchRequest`) | `internal/core/collection/scratch.go`, `internal/app/services/collection/scratch.go`, `frontend/src/commands/collection.ts`, [Scratch requests](#scratch-requests), [frontend.md](frontend.md#scratch-requests) |
| Auto-save commands, hook, store, status | `frontend/src/commands/autosave.ts`, `commands/useAutoSave.ts`, `stores/autosave.ts`, `components/shell/AutoSaveStatus.tsx` |
| Notes: the Markdown `description` of requests and folders (COL-07) | `frontend/src/lib/markdown.ts`, `components/common/Markdown.tsx`, `components/editor/NotesPanel.tsx`, `commands/links.ts`, [Notes](#notes) |
| Folder settings (`Folder`, `UpdateFolder`, `InheritedSettings`; the folder view) | `internal/app/services/collection/folder.go`, `frontend/src/components/editor/FolderEditor.tsx`, `commands/folderSettings.ts`, [Folder settings](#folder-settings), [frontend.md](frontend.md#folder-settings) |
| A folder's saved server (`FolderServers`, `LoadFolderServer`, `SaveFolderServer`; for the servers service) | `internal/app/services/collection/servers.go`, [Saved server configs](#saved-server-configs), [servers.md](servers.md#saved-server-configs) |
| External changes: polling, refresh, conflicts (`fswatch.Watcher`, `collection.Scan`, `Tree.Refresh`; `SetWatching`, `ResolveConflict`, `RecreateRequest`, `collection:external-change`; `commands/externalChanges.ts`, `ExternalChangeBanner`) | `internal/core/fswatch`, `internal/core/collection/watch.go`, `internal/app/services/collection/watch.go`, [External changes](#external-changes), [frontend.md](frontend.md#external-changes) |
| Sample project used by the tests | `internal/core/collection/testdata/sample` |

## Loading the tree

`collection.Load(root)` walks the project folder and returns the tree and
a list of problems. It never writes and never fails as a whole: whatever
cannot be read is reported and the rest loads.

What each directory entry becomes:

| Entry | In the tree |
|---|---|
| A directory with `_folder.json` | a **folder**; its entries are loaded the same way |
| A directory without `_folder.json` (fixtures, scripts) | nothing; not looked into |
| A `.json` file with a request kind (`http`, `graphql`, `tcp`, `websocket`, `grpc`, `sse`, `socketio`) | a **request** |
| A `.json` file with a kind this version does not know (`mqtt` from a newer PostSonde) | an **unknown** item, read-only, never written |
| A `.json` file that looks like a project file but does not load: invalid JSON mentioning `"kind"`, a member of the wrong type, a kind that does not belong there (an environment or folder file among requests) | an **invalid** item, read-only, and a problem |
| Valid JSON without a `kind` (`package.json`), or invalid JSON that does not mention `"kind"` (a `tsconfig.json` with comments) | nothing: not a project file |
| Anything starting with `_` or `.` (including `.<name>.tmp-*` leftovers of an interrupted save), `postsonde.json` and `environments` at the root, other files | nothing |
| A symbolic link to a file | loaded, but read-only: an atomic save would replace the link with a copy |
| A symbolic link to a directory | nothing: links are not followed, so the tree cannot loop or leave the project |

A folder whose `_folder.json` does not load is still a folder (read-only)
and its requests load normally, in the order its file lists as far as it
can be read. Folders nested more than 64 levels deep are shown but not
looked into (`collection.too_deep`).

Items that cannot be written carry a read-only reason: `newer_version` (a
file from a newer schema version, see [Versions](file-format.md#versions)),
`unknown_kind`, `invalid` or `symlink`. If `postsonde.json` itself is from a
newer PostSonde, the whole project is read-only.

### Order

Each folder's items, and the top-level items, are shown in this order:

1. the entries its `order` member names, in that order. A name matches the
   entry with exactly that name, or failing that the only entry whose name
   is the same ignoring case and Unicode normalization (a hand-written
   `"Café"` for a directory macOS lists in decomposed form);
2. then the other entries, sorted by name ignoring case (see `FoldName`
   below), ties broken by the exact name.

Names in `order` that match no entry are ignored. Saving the folder drops
them; a folder whose `order` is complete saves unchanged. Unknown and
invalid items are entries like any other and keep their place.

This is the **manual order** (COL-05). The tree can also show a folder,
or the top level, **sorted by name** (folders first, then the other
items, by name ignoring case and accents, numbers in numeric order). That
is a view preference of the UI, kept per folder in the workspace's local
view state (`tree.sort`, [workspace.md](workspace.md#view-state)), not in
the project: Go, `Tree` and `order` never see it, so teammates keep their
own view and switching back to manual order shows the order unchanged.
Items moved into a folder sorted by name go last in its manual order. See
[frontend.md](frontend.md#sorting).

## IDs

Every item is addressed by an ID: the file's `id` member. Local data (open
tabs, secrets, history) is keyed by it, so it must be unique in a project.
Two cases need a decision:

- **Copied files.** When several files have the same `id` (a request or a
  folder copied by hand), the one with the smallest path, compared byte by
  byte (`health-copy.json` before `users/create-user.json`), keeps it. Each
  other one gets a new ID derived from the old ID and its path
  (`collection.duplicate_id`, with `id`, `keptBy` and `newId`). The choice
  depends only on the paths, so every machine that checks out the project
  makes the same one.
- **No id.** A file without `id` gets one derived from its path
  (`collection.missing_id`).

Derived IDs have the usual form (`req_`, `fld_` or `ws_` and 16 hex digits)
and are the same on every load, so local data keyed by them survives a
restart. Loading does not write them. They are written into the file the
next time it is saved (an edit, or `SaveAll`); from then on the file simply
has that id. A read-only file keeps its id on disk and only the tree uses
the derived one.

## Saving

`Tree.SaveRequest(id, file)`, `Tree.SaveFolder(id, folder)` and
`Tree.SaveWorkspace(workspace)` replace an item's file in the tree and
encode it ([file-format.md](file-format.md#reading-and-writing)). The file is
written, atomically, **only when the encoding differs from the bytes on
disk**, so saving something unchanged does not touch the file, not even its
modification time. `Tree.SaveAll()` does this for every writable file.

Loading the sample project and saving everything leaves every file, its
modification time and the directories unchanged; a test checks it.

Saves are refused for a read-only item or project (`collection.read_only`,
with the `reason`), for an ID that is not in the tree
(`collection.not_found`), for a file of the wrong kind or with another
`id` than the item's (`collection.wrong_node`), and for a file that was
changed or removed on disk since the tree read or wrote it
(`collection.changed_on_disk`, [External changes](#external-changes)). A save never renames a file:
changing the name member only changes the display name. Renaming, moving,
creating and deleting are [tree operations](#tree-operations) of their own,
which keep `order` consistent.

`Tree` is not safe for concurrent use; `CollectionService` owns the tree of
the open project and serialises access to it.

## Tree operations

`Tree` has one method per operation (COL-04). Each changes the files, then
**reloads the tree from disk** (`Load`), so afterwards the tree, its paths,
IDs and problems are exactly what the files say. `parentID` is a folder's
ID, or `""` (or the root's ID) for the top level. `index` is a position
among the folder's items; `End` (-1), or any index out of range, puts the
item last.

| Operation | What it does on disk |
|---|---|
| `CreateFolder(parentID, name, index)` | a directory named by `Slug`/`UniqueName` with a new `_folder.json` (fresh `fld_` ID, `auth: inherit`) |
| `CreateRequest(parentID, name, kind, index)` | a new file for `http` (a `GET` without body), `graphql`, `tcp` (no address yet), `websocket` (no URL yet), `grpc` (no address or proto setup yet) `sse` (a GET without URL, auth inherited) or `socketio` (no URL yet); other kinds fail with `collection.invalid_kind` |
| `Rename(id, name)` | writes the `name` member, then renames the file or directory to fit (see below); the item keeps its place |
| `Duplicate(id)` | copies a request or a whole folder right after the original, named `<name> copy` |
| `Move(id, parentID, index)` | reorders within the folder (`index` counts the items without the moved one), or renames the file or directory into another folder |
| `Delete(id, trash)` | moves the file or directory into the [trash](#trash) and returns the `TrashEntry` |
| `Restore(trash, trashID)` | moves it back, see [Restoring](#restoring) |

### Order after an operation

Every operation rewrites the `order` of the folders it changes so that it
lists **exactly their items**, in display order. An item created in a
folder whose `order` listed only some items makes it list them all; what
is shown does not change. Folders that are not touched keep their `order`,
and files whose bytes would not change are not written.

### File names of operations

- **Create** takes `UniqueName(Slug(name))` in the parent.
- **Rename** keeps the file name when it already fits: its base is the new
  slug, or the slug with a collision suffix (`create-user-2.json`) while
  the plain name is still taken by another entry. Otherwise it takes a
  unique name, not counting the item's own file. A new name that differs
  only in case or normalization (`Cancel-order.json` → `cancel-order.json`)
  is renamed through a temporary name, since a case-insensitive disk sees
  both as one file.
- **Move** and **Restore** keep the base of the file name and add a
  collision suffix only when it is taken in the target folder; the display
  name never changes.
- **Duplicate** names the copy `<name> copy`, then `<name> copy 2`, … (the
  copy of `X copy` is `X copy 2`), compared ignoring case with the other
  items of the folder, and takes a file name from that.

### Duplicating

A copied request is re-encoded with a new ID and the new name. A copied
folder gets a new ID for its `_folder.json` and for every folder and
request inside it; the items keep their names, file names and order, so
loading the result reports no `collection.duplicate_id`. Only the items of
the tree are copied, not other files in the directories (fixtures,
scripts). The copy is built under a hidden name (`.<name>.copy-*`, skipped
by `Load`) and renamed into place when complete. A folder that contains an
unknown, invalid or newer-version item, or is nested too deep to be
loaded, is not copied (`collection.read_only`); a symlinked request is
copied as a regular file. `CollectionService.Duplicate` also copies the
locally stored auth secrets of the copied requests to their new IDs
([auth.md](auth.md#secrets)).

### What read-only items allow

| | Rename | Duplicate | Move | Delete |
|---|---|---|---|---|
| Writable folder or request | yes | yes | yes | yes |
| `unknown_kind` or `invalid` item | no | no | yes, the file is moved unchanged | yes |
| `newer_version` folder or request | no | no | yes | yes |
| `symlink` request | no | yes, as a regular file | only within its folder | yes, the link itself |

Nothing can be created in, moved into or out of, or restored into a folder
whose `_folder.json` is read-only (`newer_version`, `invalid`), since its
`order` cannot be written; the folder itself can still be moved and
deleted. In a read-only project every operation fails with
`collection.read_only`. Items only go into folders, and a folder cannot be
moved into itself or one of its folders (`collection.invalid_target`).

### Crash safety

Each file write is atomic, and the steps are ordered so the project loads
at every point (`Load` ignores `order` names that match nothing and sorts
unlisted items after the listed ones):

- create writes the new file (for a folder, the directory, then its
  `_folder.json`), then the parent's `order`;
- rename writes the name, renames the file, then writes the `order`; an
  interrupted case-only rename leaves the item under a visible temporary
  name;
- move renames, then writes the target's and the source's `order`;
- delete writes the trash manifest, moves the item into the trash, then
  writes the `order`; restore moves it back, writes the `order`, then
  removes the trash entry.

When a step fails the operation stops with its error; `CollectionService`
then drops its cached tree, so the next call reads the files again.

### Derived IDs

Derived IDs (of files without an `id` or with a duplicate one) depend on
the path. Before an operation that changes paths (rename, move, delete,
restore), every writable file with a derived ID gets it written, so the
item keeps its ID. A read-only file keeps its `id` on disk, so its derived
ID can change when it, or a file with the same `id`, moves.

## Trash

Deleting never removes anything. The item moves into the project's trash in
its local side files, `<data>/workspaces/<id>/trash/`
([workspace.md](workspace.md#local-side-files)), never inside the project
(`Delete` refuses a trash inside the project folder):

```
trash/trash_<16 hex>/entry.json          the TrashEntry
trash/trash_<16 hex>/data/<entry name>   the file or whole directory, as it was
```

`entry.json` holds `version` (1), `id`, `nodeId`, `kind`, `fileKind`,
`name` (the display name), `entry` (the file or directory name), `path`,
`parentId` (`""` for the top level), `index` (its position in the folder)
and `deletedAt` (UTC). A directory moves as a whole, with any other files
in it. Any item can be deleted, read-only ones included, as long as its
folder is writable. The move is a rename; when the data directory is on
another disk than the project, the item is copied (keeping permissions,
modification times and symbolic links) and the original removed.

- `Trash.List()` returns the complete entries, most recently deleted first.
  Entries without a readable manifest or without their data (left by an
  interrupted delete or restore) are skipped.
- `Trash.Purge(before)` permanently removes the entries deleted before
  `before` (an entry without a readable manifest counts from its
  directory's modification time) and returns how many. PostSonde never
  purges on its own; only an explicit call does.

### Restoring

`Restore(trash, trashID)` puts the item back and removes the entry:

- **Where.** Into the folder with the entry's `parentId`, wherever that
  folder is now (it may have been renamed or moved), at the old `index`
  (last if the folder has fewer items now). If that folder no longer
  exists (deleted, perhaps into the trash as well), the item goes to the
  **end of the top level** and `Restored.ToRoot` is set. Folders are not
  recreated from nothing: restoring the deleted folder first and then the
  item puts the item back inside it.
- **Name.** The file or directory keeps its name unless that is taken by
  now, in which case it gets a collision suffix (`health-2.json`); the
  display name is unchanged.
- **IDs.** Before moving it back, every writable folder or request file of
  the item whose `id` an item of the project has now (or an earlier file
  of the item has) gets a new random ID written into it, so restoring never
  takes an ID away from an existing item. Read-only files are left as they
  are, and `Load`'s duplicate rule applies to them.

Deleting and restoring an item gives the same tree and every file of the
item byte for byte; the parent's `order` then lists all its items.

## File names

The app chooses a request's file name, and a folder's directory name, from
its name. The name shown in the app is the `name` member, so any name
works; the file name only has to be safe and unique.

`Slug(name)` makes the base:

- lowercase ASCII letters, digits and single hyphens, never starting or
  ending with a hyphen: `Create user` → `create-user`,
  `GET /users/{id}` → `get-users-id`;
- combining marks are dropped and common Latin and Cyrillic letters are
  transliterated: `Zoë` → `zoe`, `Straße` → `strasse`,
  `Получить пользователя` → `poluchit-polzovatelya`. Apostrophes and the
  Cyrillic hard and soft signs are dropped; any other character separates
  words (`日本語 API` → `api`);
- at most 64 bytes, cut at a word boundary when there is one;
- a name with nothing left (`""`, `???`, `日本語`) becomes `untitled`.

`UniqueName(dir, slug, ext, root)` makes it unique: `create-user.json`,
then `create-user-2.json`, `create-user-3.json`, … (after 9999 a random hex
suffix). A candidate is taken when:

- any entry of the directory (a file, a folder, a non-project file) has the
  same name **ignoring case and Unicode normalization** (`FoldName`), so the
  name is free on case-insensitive disks (macOS, Windows) and on disks that
  treat composed and decomposed characters as the same (APFS, HFS+). A
  folder `users` and a request `users.json` do not collide;
- it is a **Windows device name**, with any extension: `CON`, `PRN`, `AUX`,
  `NUL`, `COM0`–`COM9`, `LPT0`–`LPT9` (so `con.json` becomes `con-2.json`),
  or ends with a dot or a space;
- at the project root, it is `postsonde.json` or `environments`.

Slugs cannot start with the reserved `_` or `.`.

The New folder and New request dialogs preview the file path with a
TypeScript port of `Slug` (`frontend/src/components/tree/newItem.ts`, tested
with the same cases); change both together.

### Unicode normalization without a normalization library

The core uses only Go's standard library, which has no Unicode
normalization (NFC/NFD). macOS may hand back a name typed as `é` (NFC) as
`e` + combining acute (NFD). PostSonde handles this in three ways:

- **Names it creates are ASCII**, so they are the same in every
  normalization and on every file system.
- **Comparisons are conservative.** `FoldName` lowercases, drops combining
  marks and transliterates the precomposed Latin and Cyrillic letters of
  the slug table, so NFC and NFD spellings of those letters compare equal.
  It can also call two different names equal (`Café` and `cafe`); for
  `UniqueName` that only means skipping a name that was free.
- **Order matching falls back to the fold** only when there is no exact
  match and exactly one candidate, and the `order` names are written back
  as they were, so hand-written files are not rewritten.

Scripts outside that table (for example Korean syllables, which decompose
algorithmically) are compared exactly after lowercasing; a hand-made file
name in such a script with mixed normalization may miss its `order`
position and sort with the unlisted items instead.

## The service

`CollectionService.Tree()` returns the tree of the open project as a
`TreeDTO`: `rootIds`, and `nodes` depth first in display order, each with
`id`, `parentId` (null at the top level), `kind` (`folder`, `request`,
`unknown`, `invalid`), `name`, `path`, `fileKind`, `method`, `childIds` and
`readOnly`, plus the load `problems` as `ErrorDTO`s and the project's
`scratch` requests ([Scratch requests](#scratch-requests)). The tree is loaded the
first time it is asked for after a project is opened; the UI calls `Tree`
again on `workspace:changed`. `Reload()` writes the pending edits, then
reads the files again in place and handles what changed like the watcher
does ([External changes](#external-changes)). Without an open project both
fail with `workspace.none_open`.

### Operations and trash

| Method | Returns |
|---|---|
| `CreateFolder({parentId, name, index})` | `NodeResultDTO` (`tree`, and `nodeId`: the new folder) |
| `CreateRequest({parentId, name, kind, index})` | `NodeResultDTO`; `kind` is `http`, `graphql`, `tcp`, `websocket` or `grpc` |
| `Rename({id, name})` | `NodeResultDTO` |
| `Duplicate(id)` | `NodeResultDTO` with the copy's `nodeId` |
| `Move({id, parentId, index})` | `NodeResultDTO` |
| `Delete(id)` | `DeleteResultDTO` (`tree`, and `entry`: the `TrashEntryDTO` whose `id` undoes it) |
| `Restore(trashId)` | `RestoreResultDTO` (`tree`, `nodeId`, `toRoot`) |
| `ListTrash()` | `TrashEntryDTO[]` (`id`, `nodeId`, `kind`, `fileKind`, `name`, `path`, `parentId` or null, `index`, `deletedAt`) |
| `PurgeTrash(olderThanMs)` | how many entries were removed; `0` removes all |
| `CreateScratch({kind, name})`, `SaveScratch({id, parentId, name, index})`, `DeleteScratch(id)` | scratch requests, see [Scratch requests](#scratch-requests) |
| `ParseCurl(text)` | `CurlImportDTO` (`ok`, `request`: the http `RequestDTO` a curl command maps to, `name`: a suggested name, `diagnostics` with UTF-16 `from`/`to`); writes nothing, see [curl.md](curl.md#import) |
| `ImportCurl({parentId, name, text, index})` | `NodeResultDTO` with the new http request (op `create`); a command with an error fails with `curl.invalid_command`; a typed password or token goes to the local side files as in `UpdateRequest` |
| `ImportFormats()`, `PickImportFile()`, `PreviewImport({path, format})`, `Import({path, format, digest, parentId, folderName, intoParent, skipEnvironments, index})` | imports of files (IMP-10): the formats, the native open dialog, what a file would create, and the import with its report (`ImportResultDTO`: `tree`, `nodeId`, `report`; op `import`); see [import.md](import.md#the-service) |

`parentId` is `""` for the top level and `index` `-1` for last. The trash
is `trash/` in the workspace's local directory. Every operation that
succeeds, and a purge that removed something, emits
**`collection:changed`** with a `ChangedEventDTO` (`workspaceId`; `op`:
`create`, `rename`, `duplicate`, `move`, `delete`, `restore`,
`purge_trash`, `import`, `update_folder` after `UpdateFolderGRPCProto`, see
[grpc.md](grpc.md), or `create_scratch`, `delete_scratch`, `save_scratch`;
`nodeId`; `trashId`). The caller applies the returned tree;
other listeners call `Tree`. A failed operation emits nothing, and the
next call reads the files again, since some of its steps may have
happened. Operations and pending auto-save edits: see
[Tree operations and pending edits](#tree-operations-and-pending-edits).
`lib/collection.ts` wraps every method (`createFolder`,
`createRequest`, `renameItem`, `duplicateItem`, `moveItem`, `deleteItem`,
`restoreItem`, `listTrash`, `purgeTrash`, `parseCurl`, `importCurl`,
`importFormats`, `pickImportFile`, `previewImport`, `runImport`,
`onCollectionChanged`). The tree
moves and reorders items with `Move` (drag and drop, Alt+↑/↓, Move to
folder…; [frontend.md](frontend.md#move-and-reorder)); the new order is
written to the `order` of the folders it changes, as for every operation.

## Auto-save

Edits are saved without a save button (STO-03).

```
UI (request editor)                         Go (CollectionService)
editRequest(stores, id, patch)
  drafts/edit, autosave/queued
  UpdateRequest({request, revision,  ───▶  checks the node, the content and the
                 baseVersion})             version, keeps it in memory (Request
                                           returns it), schedules the write
                                     ◀───  {version}
                                           … 400 ms without another update …
                                           Tree.SaveRequest(id, file)  (atomic, only if changed)
  autosave/saved, drafts/markSaved   ◀───  collection:saved       {workspaceId, id, revision, window, savedAt}
  autosave/failed                    ◀───  collection:save-failed {workspaceId, id, revision, window, error}
  (other windows) reload the draft   ◀───  collection:edited      {workspaceId, ids, window}
```

With several windows (APP-06) the request's **version** keeps a window
from saving a draft made before another window's edit:
[windows.md](windows.md#edits).

### Go

- `Request(id)` returns a request's content as a `RequestDTO`, including
  edits not written yet. One shape covers `http`, `graphql`, `tcp`,
  `websocket` and `grpc`; members of the other kinds are empty (`tcp` uses `address`,
  `sendMode`, `lineEnding`, `message` and `settings.connectTimeoutMs`;
  `websocket` uses `url`, `params`, `headers`, `subprotocols`, `message`,
  `messageFormat` and `settings.connectTimeoutMs`, `verifyTls`,
  `sendCookies`; `grpc` uses `address`, `proto` {`files`, `importPaths`,
  `inherit`, `extra`}, `service`, `method`, `message`, `metadata` and
  `settings.tls`, `verifyTls`, `connectTimeoutMs`, `deadlineMs`;
  `socketio` uses `url`, `params`, `headers`, `path`, `namespace`,
  `authPayload`, `transport`, `event`, `message` (the arguments), `ack`,
  `savedMessages` (with `event` and `ack`), `listen` and
  `settings.connectTimeoutMs`, `ackTimeoutMs`, `verifyTls`, `sendCookies`;
  [socketio.md](socketio.md#request-kind-socketio)). Each object has an `extra` string: the members
  this version does not know, as JSON object text, which the UI sends back
  unchanged so they survive (a test maps every request of the sample
  project to a DTO and back and gets the same bytes).
- `UpdateRequest({request, revision})` replaces the content. It fails at
  once, and keeps nothing, for a request that is not in the open project
  (`collection.not_found`), a folder (`collection.wrong_node`), a read-only
  file or project (`collection.read_only` with the `reason`: invalid,
  unknown kind, newer version, symlink), an unknown kind or `extra` that is
  not an object (`collection.invalid_request`) and content the file format
  refuses (`projectfile.invalid_value`, e.g. no method). Changing `kind`
  changes the request's kind. `revision` is opaque to Go; it comes back in
  the events, with the `window` the edit came from.
- **Versions** (APP-06, `windows.go`): `Request` and `Folder` return the
  item's `version` (left out while 0), and `UpdateRequest` and
  `UpdateFolder` take the version the edit was made from (`baseVersion`)
  and return the new one. An edit of an older version than the one held is
  refused with `collection.edited_elsewhere`: another window changed the
  item since. Accepted edits, changes on disk and renames of requests move
  the version on; a missing `baseVersion` is not checked. Every accepted
  edit is announced as `collection:edited`. See
  [windows.md](windows.md#edits).
- **Debouncing** (`internal/core/autosave`): a request is written 400 ms
  after its last update, and no later than 2 s after the first unsaved
  one, so continuous typing still reaches the disk. Updates in between
  replace each other: one write. `collection.WithSaveDelay` changes the
  delays; tests use a manual clock (`internal/testutil/fakeclock`).
- **Writes** go through `Tree.SaveRequest`: atomic, and only when the bytes
  differ. The node is looked up **by ID when the write runs**, so a rename
  or move in between writes to the new path, and a request deleted in
  between fails with `collection.not_found`. Writes run under the service's
  lock, like every other use of the tree.
- **Events:** `collection:saved` after a write (also when the file was
  already equal), `collection:save-failed` with the `ErrorDTO` when it
  fails (`projectfile.save_failed`, kind from the cause:
  `permission_denied`, `io`, …). A failed edit **stays pending**: `Request`
  still returns it, and the next update, `Flush`, `FlushAll`, reload or
  quit tries again. There is no retry timer.
- **Flushing:** `Flush(id)` writes one request now (a closing tab),
  `FlushAll()` everything. `ServiceShutdown` flushes everything when the
  app quits (Wails calls it after the app's shutdown hooks) and sends no
  events. Before the tree is loaded again (`Reload`, reopening a project)
  the edits made in that project folder are flushed, so the new tree reads
  them; edits whose write failed move to the new tree (or, for `Reload`,
  which refreshes the tree in place, are tried again after the save
  delay). An edit of a file that changed on disk meanwhile is not written:
  see [External changes](#external-changes).
- **Switching projects:** a pending edit belongs to the tree it was made in
  and is written to that project after its delay, even when another
  project is open by then.

#### Tree operations and pending edits

Each operation runs under the service lock, so no auto-save write runs
during it, and reloads the tree in place (the `*core.Tree` a pending edit
is keyed by stays the same). The operations of `CollectionService`
(`ops.go`, helpers in `autosave.go`) treat the pending edits of the item,
and of every request below a folder, in any tree of the same project
folder, like this:

| Operation | Pending edits |
|---|---|
| `Rename`, `Move` | **written first** (`flushPendingLocked`). The pending content still carries the old `name`, so the rename's own write comes last and keeps the new name; the file at the new path has the edits and the old path is gone. |
| `Duplicate` | **written first**, so the copy has the latest content. |
| `Delete` | **dropped without writing** (`discardPendingLocked`) once the item is in the trash (also when the delete failed after moving it), so a late write cannot recreate the file. |
| `Restore` | any pending edit left for the restored items' IDs is **dropped**: they come back exactly as they were deleted. |
| `CreateFolder`, `CreateRequest`, `PurgeTrash` | not affected. |

When writing the edits first fails, the operation stops with that error
(`projectfile.save_failed`, also sent as `collection:save-failed`),
changes nothing, and the edits stay pending. A failed operation drops the
cached tree like any other failure; the next call flushes the project
folder's edits before reading the files again. Tests:
`ops_autosave_test.go`.

In the UI, `applyCollectionChanged` (run by `followCollection` for every
`collection:changed`) reloads the tree and, when `op` is `rename` and the
renamed request has an open draft, calls `applyRenamed(stores, id, name)`
(`commands/autosave.ts`): `drafts/rename` puts the tree's display name into
the draft without marking it dirty or changing its revision, and an edit
queued but not sent yet gets the name too. An edit **already in flight**
cannot be changed, and Go may receive it after the rename, which would
write the old `name` back. The UI command that renames a request,
`renameNode` (`commands/collection.ts`), therefore:

1. `await flushRequests(stores, [id])` (waits for the queued and in-flight
   sends of the request and asks Go to write them), then
2. `await renameItem(id, name)`, applies the returned tree and calls
   `applyRenamed` for an open draft.

Move, duplicate and delete need no such step for correctness: Go flushes or
drops the edits itself. `moveNode`, `duplicateNode` and `deleteNode` still
send the queued edits of every request in the item first
(`flushRequests`), so the file at its new place, the copy, and the item in
the trash, have them; a send for a deleted request would be refused
(`collection.not_found`) and shown as a failed save. A move keeps every
ID, so the open tabs and drafts of moved requests keep working; only
their paths change, which the tree returns.
`deleteNode` then closes the tabs of the item and everything in it (and
drops their drafts), and `restoreNode` opens them again where they were;
see [frontend.md](frontend.md#rename-duplicate-and-delete).

### UI

- `commands/autosave.ts`: `loadRequestDraft(stores, id)` opens a draft from
  `Request` and keeps its version apart from the draft. `editRequest(stores, id, patch)` applies the patch
  (`drafts/edit`) and sends the whole draft with a per-request revision
  that only increases, as an edit of the draft's version (the result is
  the next one). Sends of one request run one at a time, so Go gets
  them in order; edits made meanwhile collapse into the latest. A send Go
  refuses is recorded as a failure (Go sends no event for it), except
  `collection.changed_on_disk` and `collection.edited_elsewhere`, which put
  the draft in conflict (`holdEdits`; [External changes](#external-changes),
  [windows.md](windows.md#edits)).
- `followAutoSave(stores)` (mounted by `App` through `useAutoSaveSync`)
  applies the events: `autosave/saved` and `drafts/markSaved` for the draft
  revision that was sent, or `autosave/failed`. It also flushes everything
  when the page is hidden or unloaded.
- Closing tabs flushes the closed requests: `followAutoSave` registers an
  `onTabClosed` listener (`commands/tabs.ts`) that runs `flushRequests`
  for them (waits for the queued send, then `Flush`); `closeTabs` then
  drops their drafts at once, and the queue keeps its own copy. Closing a
  project closes its tabs, so it flushes too.
- `useAutoSave(id)` is the editor's hook: `{status, dirty, failure,
  edit, flush}`, with `status` `idle`, `saving`, `saved` or `error`.
- The `autosave` store keeps `pending` revisions, `failed` saves (until a
  later save of the request succeeds or they are dismissed), `savedAt` per
  request and `lastSavedAt`.
- **Status bar** (`AutoSaveStatus`): "Ready", "Saving…" while any save is
  pending, "Auto-saved" with the time as tooltip, and "Save failed" (or
  "N saves failed") when one failed: clicking it shows the message and
  hint inline, with Dismiss. A failure stays shown while newer edits of
  that request are pending.

### Not handled yet

- The HTTP request editor ([frontend.md](frontend.md#request-editor))
  the TCP editor ([tcp.md](tcp.md)) and the WebSocket editor
  ([websocket.md](websocket.md)) call `editRequest`; GraphQL
  requests have no editor yet.
- Environment variable edits keep their own 400 ms debounce in the UI
  (docs/environments.md) and are not flushed when the project closes or the
  app quits.
- Workspace settings are not auto-saved yet; they come with their editor.
  Folder settings are ([Folder settings](#folder-settings)).
- Quitting flushes in Go, but an edit still in the webview's send queue
  when the window closes (a few milliseconds) can be lost; `pagehide`
  flushes are best effort.

## External changes

Files edited, added, removed or renamed outside PostSonde (an editor, a
checkout or pull) show in the tree within about two seconds (STO-05).
Go's standard library has no file notification API, so the core polls.

```
fswatch.Watcher (every 1 s)        CollectionService                       UI
  Scan(root): stat of the files ─▶ checkExternal
  settled change                   Tree.Refresh(): reload in place,
                                   compare SHA-256 of every file
                                   set aside conflicting pending edits
                                   collection:external-change ──────────▶ reload the tree, drafts
                                   (environments:changed)          ──────▶ variable chips
```

- **Polling** (`internal/core/fswatch`). A `Watcher` calls a scan function
  every interval (1 s, `collection.WithWatch`; tests use a manual clock)
  and compares the size, modification time and type of each file with the
  last snapshot it reported. A change is reported once a scan finds
  nothing new (the files were quiet for an interval), or after 5 intervals
  while they keep changing, so a burst of writes is one report; a change
  undone in between is none. A scan that fails skips the poll. `Pause`,
  `Resume` (polls at once), `Poke` (polls at once) and `Rebase` (takes a
  new reference without reporting).
- **What is watched** (`collection.Scan`): the files `Load` reads
  (`postsonde.json`, every folder's `_folder.json`, every `.json` in the
  root and in folders, skipping `_` and `.` names, directories without a
  `_folder.json` and links to directories) and `environments/*.json`. A
  change to a file that turns out not to be a project file costs one
  refresh and reports nothing.
- **Own writes.** Every node keeps the SHA-256 of its file as last read
  (`Load`) or written (every tree write). `Tree.Refresh()` reloads the tree
  in place (the `*Tree`, and so the pending auto-save keys, stay the same)
  and returns `Changes`: items `Added`, `Removed`, `Modified` (the file's
  bytes changed, or whether it loads), `Moved` (same bytes, another path;
  a rename by hand keeps the ID when the file has one) and `Root`
  (`postsonde.json`). A file the app wrote, or one touched without being
  changed, hashes the same, so the app's own writes never count. The
  environment files are hashed by the service; the environments service
  tells it after each of its writes (`environments.WithWritten` calls
  `collection.EnvironmentsWritten`), and tree operations (an import) take
  a new reference too.
- **Writes check the disk.** Every tree write first compares the file on
  disk with the hash it has: a file changed or removed since is not
  overwritten (`collection.changed_on_disk`, kind `io`). So an auto-save
  that runs before the watcher noticed a change cannot overwrite it or
  bring a removed file back; the failure is not shown as a failed save,
  the service asks the watcher to poll at once instead.
- **Pending edits.** On a refresh, a request or folder with an edit waiting
  for the auto-saver whose file was modified is a **conflict**: the edit is
  dropped from the saver (not written), its ID is listed in `conflicts`,
  and `UpdateRequest` and `UpdateFolder` refuse edits of it with
  `collection.changed_on_disk` until `ResolveConflict(id)`. A folder
  whose auth, headers and variables did not change (only its order, name,
  proto setup or server did) is no conflict: it is listed in `moved`, and
  its pending settings edit is written over the new file as usual. The
  pending edits of removed items are dropped. Quitting with an open
  conflict keeps the file on disk as it is.
- **Event.** `collection:external-change` (`ExternalChangeDTO`):
  `workspaceId`, and node IDs in `added`, `removed`, `modified` (requests,
  and folders whose settings changed), `moved` and `conflicts`; `globals`
  when the global variables in `postsonde.json` changed and
  `environments` when an environment file was added, changed or removed.
  When either is set, `environments:changed` follows (with the active
  environment), so the `{{variable}}` chips reload.
- **Methods.** `SetWatching(active)` pauses polling while the window is
  hidden and polls at once when it is shown. `ResolveConflict(id)` accepts
  edits of `id` again. `RecreateRequest({parentId, index, request})`
  writes a request whose file was removed back into its folder (the top
  level when that is gone), keeping its ID unless an item has it by now;
  op `create`.
- **Lifecycle.** The watcher starts when a project's tree is loaded,
  follows the open project, takes a new reference when the tree is read
  again, and stops when the project closes and at quit. Without
  `WithWatch` (most tests) nothing is polled; `Reload` still handles
  external changes on demand. Saved servers need nothing more: the servers
  view syncs them on every tree reload and `Start` reads the folder file
  again ([servers.md](servers.md#saved-server-configs)).

The UI side (reloading clean drafts, the conflict notice, tabs of deleted
files): [frontend.md](frontend.md#external-changes). Tests:
`internal/core/fswatch/fswatch_test.go`,
`internal/core/collection/watch_test.go`,
`internal/app/services/collection/watch_test.go`.

Limits: an edit already on its way from the UI when the change is noticed
(a few milliseconds) can still be written over the other version; a
change is noticed only when the size or modification time of a file
changes, which editors and version control tools do; the whole project is
scanned every second (a stat per file, no reads) while the window is
shown.

## Folder settings

A folder's `_folder.json` holds settings for everything inside it (COL-03,
[file-format.md](file-format.md#folder-_folderjson)): `auth`, `headers` and
`variables`, and the folder's Markdown notes, `description` ([Notes](#notes)). They are edited in the folder's `DIR` tab
([frontend.md](frontend.md#folder-settings)) and saved like a request, with
the same auto-saver, events and undo history.

| Call | Does |
|---|---|
| `Folder(id)` | `FolderDTO` {`id`, `kind: "folder"`, `name`, `description`, `auth`, `headers`, `variables`}, pending edits included, with the folder's local auth secret merged in. `collection.not_found` for an ID that is not a folder; `collection.read_only` when its `_folder.json` did not load. |
| `UpdateFolder({folder, revision})` | Keeps the folder's `auth`, `headers`, `variables` and `description` pending and writes them after the save delay, like `UpdateRequest`: `collection:saved` / `collection:save-failed` with the same `id` and `revision`; `Flush(id)` and `FlushAll` write it now. Refused at once like `UpdateRequest` (`collection.not_found`, `collection.wrong_node`, `collection.read_only` also for an unreadable `_folder.json`, `collection.invalid_request`, `projectfile.invalid_value`). |
| `InheritedSettings(id)` | For a request or folder: the `headers` and `variables` of every folder above it, top-level first (`{folders: [{folderId, folderName, headers, variables, unreadable}]}`), pending edits included; empty for top-level items and scratch requests. The editors list them read-only. |
| `InheritedAuth(id)` | Also takes a folder: the auth the folder's `inherit` resolves to, with that folder's fields (`auth`, typed secrets left out) ([auth.md](auth.md#inheritance)). |

- **What is written.** Only `auth`, `headers`, `variables` and `description`
  come from the edit (`name` is ignored: `Rename` renames). The write puts them over the
  folder's file as the tree has it then (`writeFolder`), so its `name`,
  `order`, `grpc` proto setup (which `UpdateFolderGRPCProto` writes at
  once) and unknown members stay, even after a rename or move in between.
- **Secrets.** A typed password, token or API key is split off like a
  request's and kept in the folder's side file,
  `<local>/secrets/requests/<folder id>.json` ([auth.md](auth.md#secrets));
  `Duplicate` copies the side files of the folders it copies too.
- **Variables** are key/value rows (`KeyValueDTO`). A secret folder
  variable carries `"secret": true` in its `extra` members; the lock button
  of its row sets or clears it. Its value goes to the folder's side file
  (`var.<name>`, `requestsecrets.SplitFile`) and `_folder.json` gets
  `"value": ""`; `Folder` and the send-time sources merge it back
  (`requestsecrets.MergeVariables`: the local value, else a value typed
  into the file by hand). The editor masks it (the eye shows it).
  `InheritedSettings` sends secret values as `""`, and the read-only lists
  show them as `••••••`. A secret variable without a value on this computer
  is undefined at send time.
- **Pending state counts everywhere.** Sending (`LoadHTTPSource`), the
  variable scopes (`LoadVariablesSource`), `InheritedAuth` and
  `InheritedSettings` read a folder's pending edit (`folderLocked`), so a
  send right after an edit of a folder uses it. The same key is used as for
  requests (`saveKey{tree, id}`), with `pendingSave.folder` set; tree
  operations flush or drop it with the item's subtree, as for requests.
- **At send time** (HTTP and GraphQL requests): the request's `auth:
  inherit` resolves to the closest folder that sets one; the variables of
  the folders above resolve after the environment's, the nearer folder
  winning ([environments.md](environments.md)); and `request.BuildHTTP`
  adds the folders' headers (`HTTPSource.FolderHeaders` →
  `HTTPOptions.FolderHeaders`, `request.InheritHeaders`). An enabled header
  row of the request overrides a folder header of the same name (ignoring
  case), a nearer folder overrides the folders above, and disabled or
  unnamed rows override nothing. Folder headers are sent before the
  request's own rows, the top-level folder's first, and win over the
  auth's header as typed rows do. A folder whose `_folder.json` did not
  load passes no headers or variables on (its auth stops the search with
  `request.auth_inherit_failed`). WebSocket, gRPC and TCP requests do not
  get folder headers.

Tests: `internal/app/services/collection/folder_test.go`,
`internal/core/request/folderheaders_test.go`,
`internal/app/services/http/folderheaders_test.go`.

### Saved server configs

A folder's `_folder.json` can also hold one local server, its `server`
member (SRV-04, [file-format.md](file-format.md#saved-server-config)). It
is not part of `FolderDTO` and not edited through the auto-saver: the
servers service reads and writes it through three package functions (not
bound to the UI), and its own methods are what the folder's Server tab
calls ([servers.md](servers.md#saved-server-configs)).

| Function | Does |
|---|---|
| `FolderServers(svc, ctx)` | The open project's ID, its folders that define a server (tree order: ID, display name, definition) and the IDs of the folders whose `_folder.json` did not load. |
| `LoadFolderServer(svc, ctx, workspaceID, id)` | One folder and its server (nil when none). `collection.not_found` when `workspaceID` is not the open project or `id` is not one of its folders; `collection.read_only` for an unreadable `_folder.json`. |
| `SaveFolderServer(svc, ctx, workspaceID, id, def)` | Writes `def` (nil removes it) into the folder's file **at once**, over the file as the tree has it; every other member stays. Refused like `LoadFolderServer`, and for a read-only folder or project. |

A settings edit waiting for the auto-saver is not written by
`SaveFolderServer`; when the auto-saver writes it later, it puts only
`auth`, `headers`, `variables` and `description` over the file, so the
server stays.
Rename, move, duplicate and trash keep the member like any other (a
duplicated folder gets a copy of the definition; both servers can be
defined, but they cannot listen on the same fixed port at once). The write
goes through the tree, so it moves typed secrets out like every write;
a server config itself has no secret fields.

Tests: `internal/app/services/collection/servers_test.go`.

## Notes

Every request and folder can carry notes in Markdown (COL-07): its
`description` member ([file-format.md](file-format.md#common-rules)). There
is no second field: `description` already existed in every request and
folder file, importers fill it (a Postman description, and scripts
PostSonde does not run, as fenced `js` code blocks, [import.md](import.md)),
and an export can write it back as the target's description, so notes
travel with the collection. An empty note leaves the member out of the
file.

**Editing.** The request editors have a **Notes** sub-tab (the last one;
TCP, which has no sub-tabs, has a folding **Notes** section under its
options), and so does the folder's `DIR` tab. It switches between **Edit**,
a CodeMirror editor with Markdown highlighting
(`components/editor/markdownLanguage.ts`, a small stream tokenizer: no
`@codemirror/lang-markdown` is installed), and **Preview**. It opens in
Preview when there is a note and in Edit when there is none; a read-only
item shows the editor read-only. Every change is auto-saved and undoable
like any other edit: a request's through `UpdateRequest`, a folder's
through `UpdateFolder` ([Folder settings](#folder-settings)).

**Rendering.** `lib/markdown.ts` parses the note into plain data (no npm
package: a new dependency needs its own decision), and
`components/common/Markdown.tsx` turns that into React elements. It never
uses `dangerouslySetInnerHTML`, so all source text ends up in text nodes
and raw HTML (`<script>`, `<img onerror=…>`) shows exactly as typed. The
subset, CommonMark-ish:

| Supported | Notes |
|---|---|
| ATX (`#` … `######`) and setext headings, paragraphs, hard line breaks (two spaces or a backslash at the end of a line) | A single line break inside a paragraph is a space, as in CommonMark. |
| `*em*`, `_em_`, `**strong**`, `__strong__`, `***both***`, `~~strikethrough~~` | Pairs with the nearest fitting closer instead of the full CommonMark delimiter algorithm; `_` does not emphasise inside words. |
| Code spans, fenced (backticks or tildes) and indented code blocks | No syntax highlighting; the info string is kept as `data-lang`. |
| Bullet and ordered lists, nested, with start numbers; GFM task items `- [ ]`, `- [x]` | Checkboxes are shown disabled: ticking one does not edit the note. |
| Block quotes (nested, with lazy lines), thematic breaks | |
| GFM tables with column alignment | A backslash before a pipe keeps it in the cell. |
| Links: `[text](url "title")`, reference links with `[label]: url` definitions, `<https://…>` and `<a@b.c>` autolinks, bare `http(s)://` URLs | See below. |
| Images `![alt](url)` | Never loaded; see below. |
| Backslash escapes, the common named character references (`&amp;`, `&lt;`, `&nbsp;`, …) and all numeric ones | |

Not supported: HTML (blocks and inline, shown as text), footnotes,
definition lists, math, emoji shortcodes and syntax highlighting. Nesting
deeper than 32 levels is shown as text.

**Nothing is loaded, and links leave the app only when asked.** Notes come
from project files, which may be someone else's, so:

- An image is never an `<img>`: it shows as a small placeholder with its
  alt text. A remote (`http`/`https`) image's placeholder is a link to the
  image's URL, which opens like any link; a local or relative path is
  neither loaded nor opened (showing local images would need the file
  access check, [privacy.md](privacy.md#local-file-access-prv-10), and its
  own decision).
- Only absolute `http`, `https` and `mailto` URLs are links
  (`linkTarget`). They have no `href`, so neither a click nor the webview
  can navigate the app: activating one (click or Enter) asks "Open this
  link in your browser?" with the full URL (`commands/links.ts`, the shared
  confirm dialog), and only then hands it to the system browser through
  the Wails runtime (`Browser.OpenURL`, `lib/browser.ts`). Wails checks
  the URL again and refuses shell metacharacters such as `;` or `$`; that
  failure shows as a toast.
- Everything else, `javascript:`, `data:`, `file:`, `vbscript:`, relative
  paths (another request's file) and `#anchors`, is inert text with the
  destination in its tooltip. Links to other requests by name are not
  supported.
- The renderer outputs no `<iframe>`, `<link>`, `<script>`, `<style>`,
  `<object>` or `<svg>`, and the CSP is unchanged
  ([privacy.md](privacy.md#markdown-notes)).

Tests: `frontend/src/lib/markdown.test.ts` (each construct, nesting,
escaping, hostile input, link classification),
`components/common/Markdown.test.tsx` (no loading elements or URL
attributes, no `fetch`, links), `commands/links.test.ts`, the Notes tests
in `FolderEditor.test.tsx` and `RequestEditor.test.tsx`, and the file
round trip in `internal/app/services/collection/notes_test.go`.

## Scratch requests

Scratch requests (COL-06) are quick requests that are not part of the
project: they live in the workspace's local side files
([workspace.md](workspace.md#local-side-files)), so trying something out
never touches the project folder or a repository, and they survive a
restart. "Save to…" turns one into a normal request file in a folder.

```
<data>/workspaces/<id>/scratch/<request id>.json   one request file each, 0600 in a 0700 directory
```

- **Files.** Each is a request file in the project's format
  ([file-format.md](file-format.md)), of any kind (`http`, `graphql`,
  `tcp`, `websocket`, `grpc`). The file name is its ID, a generated
  request ID (`req_` and 16 hex digits); other names in the directory are
  ignored, and IDs that do not have this form are never looked up as
  files. A file that does not load is logged and left out; one from a
  newer PostSonde is shown read-only. Typed auth secrets go to the local
  secrets side file like a project request's ([auth.md](auth.md#secrets)).
- **Core.** `collection.Scratch` (`internal/core/collection/scratch.go`):
  `List` (sorted by name ignoring case, then ID), `Get`, `Has`, `Create`
  (from `NewRequestFile`, the empty request `CreateRequest` writes; named
  "Scratch N" with the first free N when no name is given), `Save` (only
  when the bytes differ, and only while the file exists, so a late write
  cannot bring a deleted one back), `Delete` and `CheckWritable`.
  `Tree.AddRequest(parentID, file, name, index)` writes a given request
  file into a folder like `CreateRequest`, keeping its ID unless an item of
  the tree has it.
- **One ID space.** A scratch request has an ordinary request ID, and the
  service treats an ID that is not in the tree but is in the scratch
  directory as a scratch request. So `Request`, `UpdateRequest` (auto-save
  writes it to the scratch file after the usual delay; `collection:saved`
  and `collection:save-failed` as usual), `Flush`, `FlushAll`,
  `InheritedAuth`, `InheritedGRPCProto` and `LoadHTTPSource` (the HTTP and
  GraphQL send path) take it unchanged. It has no folders: `inherit` auth
  sends nothing and there are no folder variables; the environment's
  variables apply. A project that is read-only (newer format) still lets
  scratch requests be edited, since they are not project files.
- **Tree DTO.** `TreeDTO.scratch` lists them as request `NodeDTO`s with no
  parent and an empty path, sorted by name, with pending edits applied (a
  new name shows at once). They are not among `rootIds`.
- **`CreateScratch({kind, name})`** writes an empty one and returns the
  tree with its `nodeId`.
- **`SaveScratch({id, parentId, name, index})`** ("Save to…") writes the
  pending edits of the scratch request first, then its content as a new
  request file in the folder (a tree operation: same file names, `order`
  and read-only rules as `CreateRequest`), called `name` (blank keeps its
  name), then removes the scratch file. It keeps its ID, so the tab, the
  draft, the edit history and the local secret stay with it. If the app
  stops between the two steps, the project's request wins and the leftover
  scratch file is hidden. A scratch request that is gone is
  `collection.not_found`.
- **`DeleteScratch(id)`** removes it for good, with its pending edits and
  local secret (there is no trash for scratch requests; the UI asks
  first). Deleting one that is not there is not an error.

The UI: [frontend.md](frontend.md#scratch-requests).

## Problems

Errors use the shared model ([errors.md](errors.md)). Load problems carry
the item's path relative to the project and its node ID; their `path`
detail is absolute.

| Code | Kind | When | Details |
|---|---|---|---|
| `collection.read_dir_failed` | from the cause | A directory could not be listed; the entries read before the error are kept. | `path` |
| `collection.duplicate_id` | `invalid_argument` | The file shares its `id` with a file that has a smaller path. | `id`, `keptBy`, `newId`, `path` |
| `collection.missing_id` | `invalid_argument` | The file has no `id`. | `newId`, `path` |
| `collection.too_deep` | `unsupported` | Folders nested deeper than 64 levels. | `path` |
| `collection.read_only` | `unsupported` | Saving, renaming, copying or moving a read-only item, changing a read-only folder, anything in a read-only project (see [What read-only items allow](#what-read-only-items-allow)). | `id`, `reason` |
| `collection.not_found` | `not_found` | Saving or operating on an ID, or into a parent ID, that is not in the tree. | `id` |
| `collection.invalid_target` | `invalid_argument` | Creating or moving into something that is not a folder, or a folder into itself or one of its folders. | `id`, `parentId` |
| `collection.invalid_kind` | `invalid_argument` | Creating a request of a kind other than `http`, `graphql`, `tcp`, `websocket` or `grpc`. | `kind` |
| `collection.file_op_failed` | from the cause | Creating a directory, or renaming or moving a file or directory, failed. | `path`, `to` |
| `collection.trash_not_found` | `not_found` | Restoring an entry that is not, or no longer completely, in the trash. | `trashId` |
| `collection.trash_failed` | from the cause | The trash could not be read or written, or would be inside the project. | `path` |
| `collection.changed_on_disk` | `io` | A file changed or was removed outside PostSonde since the tree read it, so a write refused to overwrite it; or an edit of an item whose conflict is not resolved yet ([External changes](#external-changes)). | `id`, `path` |
| `collection.scratch_failed` | from the cause | The scratch directory or a scratch request file could not be listed, written or removed ([Scratch requests](#scratch-requests)). | `path` |
| `collection.wrong_node` | `invalid_argument` | Saving a file of the wrong kind or with another `id`. | `id` |
| `collection.invalid_request` | `invalid_argument` | `UpdateRequest` with a kind other than `http`, `graphql`, `tcp`, `websocket` or `grpc`, or an `extra` that is not a JSON object. | `id`, `field` |
| `projectfile.*` | | A file does not load or cannot be encoded or written ([file-format.md](file-format.md#problems)). | `path`, … |
