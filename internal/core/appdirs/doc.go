// Package appdirs resolves the directories PostSonde keeps its files in:
// configuration, data, cache and logs.
//
// Resolve returns the directories for the running system. ResolveFor holds
// the logic as a pure function of the OS name, an environment lookup and the
// home directory, so the rules for every OS can be tested on any host.
//
// # Locations
//
//	          macOS                                  Linux and other Unix                  Windows
//	Config    ~/Library/Application Support/PostSonde $XDG_CONFIG_HOME/postsonde            %APPDATA%\PostSonde
//	                                                   (default ~/.config/postsonde)
//	Data      ~/Library/Application Support/PostSonde $XDG_DATA_HOME/postsonde              %LOCALAPPDATA%\PostSonde
//	                                                   (default ~/.local/share/postsonde)
//	Cache     ~/Library/Caches/PostSonde             $XDG_CACHE_HOME/postsonde             %LOCALAPPDATA%\PostSonde\Cache
//	                                                   (default ~/.cache/postsonde)
//	Logs      ~/Library/Logs/PostSonde               $XDG_STATE_HOME/postsonde             %LOCALAPPDATA%\PostSonde\Logs
//	                                                   (default ~/.local/state/postsonde)
//
// The Config and Cache bases match what os.UserConfigDir and os.UserCacheDir
// return on each OS: ~/Library/Application Support and ~/Library/Caches on
// macOS, $XDG_CONFIG_HOME and $XDG_CACHE_HOME (or ~/.config and ~/.cache) on
// Unix, and %AppData% and %LocalAppData% on Windows. The differences are
// deliberate:
//
//   - A relative XDG variable is ignored and the default is used, as the XDG
//     Base Directory specification requires; os.UserConfigDir returns an
//     error instead.
//   - On Windows, an unset %APPDATA% or %LOCALAPPDATA% falls back to
//     AppData\Roaming or AppData\Local under the home directory;
//     os.UserConfigDir returns an error instead.
//   - Data and Logs have no standard library counterpart and follow the
//     platform conventions above (XDG_DATA_HOME and XDG_STATE_HOME on Unix,
//     ~/Library/Logs on macOS, the non-roaming %LOCALAPPDATA% on Windows).
//
// On macOS, Config and Data are the same directory. XDG variables are only
// read on Unix systems other than macOS.
//
// # Override
//
// If the POSTSONDE_HOME environment variable is set, all four directories
// live under it, whatever the OS:
//
//	$POSTSONDE_HOME/config
//	$POSTSONDE_HOME/data
//	$POSTSONDE_HOME/cache
//	$POSTSONDE_HOME/logs
//
// This is meant for tests and scripted setups. Resolve makes a relative
// value absolute against the working directory. Under builds the same
// layout for a directory chosen in code; portable mode (APP-04) uses it for
// the postsonde-data folder next to the app.
//
// The app does not call Resolve itself: package datadir layers portable
// mode and the data directory chosen in the settings (SET-03) on top of
// these rules.
//
// # Creating the directories
//
// Resolving never touches the file system. Call Dirs.Ensure to create the
// directories; missing ones, including missing parents, are created with
// permission 0700 so other users cannot read requests, secrets or logs.
// Existing directories are left as they are.
package appdirs
