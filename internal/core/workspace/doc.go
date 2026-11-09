// Package workspace opens and creates PostSonde projects (STO-01) and gives
// each one a local side-file directory (STO-04). docs/workspace.md describes
// the behaviour for users.
//
// # Projects
//
// A project is a folder with a postsonde.json marker at its root (package
// projectfile). [Manager.Open] reads and validates the marker of an existing
// folder; [Manager.Create] writes a new marker, with a new workspace ID, into
// a folder that has none. Neither ever writes anything else into the
// project folder, and Open does not write into it at all.
//
// # Local side files
//
// Everything that must not be shared with the project (secrets, local
// variable values, the active environment, history, tokens, caches and
// scratch requests) lives in the workspace's local directory in the app data
// directory:
//
//	<data>/workspaces/recent.json            recent workspaces, last open workspace
//	<data>/workspaces/<key>/local.json       last known project path, timestamps
//	<data>/workspaces/<key>/ui-state.json    the UI's view state (open tabs, ...), [Manager.SaveUIState]
//	<data>/workspaces/<key>/...              side files of later features
//
// <key> is the workspace ID when it is a plain lower-case ID, which every
// generated ID is, and otherwise a hash of it ([LocalKey]). Features get the
// directory of the open workspace from [Manager.LocalDir] or
// [Workspace.LocalDir].
//
// # Copies
//
// Two folders can hold the same workspace ID, typically when a project is
// copied or checked out twice. The local directory records the project path
// it belongs to, so Open can tell the cases apart ([LocalStatus]):
//
//   - The recorded path is the opened folder: [LocalExisting].
//   - The recorded folder no longer holds a marker with this ID: the project
//     was moved or renamed. Its local data follows it ([LocalMoved]).
//   - The recorded folder still holds a marker with this ID: the opened
//     folder is a copy. It gets its own local directory,
//     <key>@<hash of its path>, so the two never share secrets or history,
//     and the project files are left unchanged ([LocalCopy]).
//
// All errors are *errs.Error values with a code from this package or from
// projectfile.
package workspace
