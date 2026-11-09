// Package datadir decides where PostSonde keeps its files for this run:
// portable mode (APP-04) and the configurable data directory (SET-03), on
// top of the per-OS locations of package appdirs.
//
// # Precedence
//
// Resolve picks the first that applies:
//
//  1. POSTSONDE_HOME set: every directory lives under it (ModeEnv).
//  2. A marker file named postsonde.portable in the app folder: every
//     directory lives in postsonde-data/ beside it (ModePortable).
//  3. A data directory chosen in the settings, recorded in
//     data-location.json in the config directory (ModeConfigured). Only the
//     data directory moves; settings, cache and logs stay where appdirs puts
//     them.
//  4. The appdirs defaults (ModeDefault).
//
// The app folder (AppFolder) is the folder holding the executable, except
// inside a macOS bundle (…/PostSonde.app/Contents/MacOS/postsonde), where it
// is the folder holding PostSonde.app, and in an AppImage, where it is the
// folder holding the .AppImage file ($APPIMAGE).
//
// # Starting
//
// Start resolves, runs a data directory change scheduled by the previous
// run (a move is a copy, a check of every file and then the switch; the old
// folder is kept until the user deletes it), and creates what is missing.
// Directories that cannot be used (a portable folder on a read-only disk, a
// chosen data directory on a disk that is not mounted) are returned empty
// with a problem, so the app still starts but keeps nothing on disk rather
// than writing somewhere unexpected.
//
// # Changing
//
// Store is the setting at run time: Check validates a folder the user
// picked, Schedule records the change for the next start, Cancel drops it,
// and DeletePrevious removes the copy left behind by a move once the user
// confirms. A change never takes effect while the app runs, because open
// projects, cookie jars and the log keep files open in the old folder.
//
// Only the PostSonde data in a data directory is moved: the entries named
// in DataEntries. Everything a project keeps locally lives under
// workspaces/ (docs/workspace.md); a new top-level entry must be added to
// DataEntries.
//
// See docs/settings.md (Data directory) and docs/packaging.md (Portable
// mode).
package datadir
