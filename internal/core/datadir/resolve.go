package datadir

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/appdirs"
	"github.com/sorrelfox/postsonde/internal/core/errs"
)

const (
	// MarkerName is the file that turns on portable mode when it is in the
	// app folder. Its content is ignored.
	MarkerName = "postsonde.portable"
	// PortableDirName is the folder beside the marker that holds
	// everything in portable mode, in the appdirs.Under layout.
	PortableDirName = "postsonde-data"
	// LocationFileName records the chosen data directory and a scheduled
	// change, in the config directory.
	LocationFileName = "data-location.json"
	// LocationVersion is the version of the location file.
	LocationVersion = 1
)

// DataEntries are the top-level entries of a data directory that hold
// PostSonde data. A move copies these and nothing else; other files in a
// data directory (on Linux, the web view's storage; on macOS and Windows,
// the settings, cache and logs sharing the default folder) stay.
var DataEntries = []string{"workspaces"}

// Mode says where the directories come from.
type Mode string

const (
	// ModeEnv: POSTSONDE_HOME is set.
	ModeEnv Mode = "env"
	// ModePortable: the portable marker is in the app folder.
	ModePortable Mode = "portable"
	// ModeConfigured: a data directory is chosen in the settings.
	ModeConfigured Mode = "configured"
	// ModeDefault: the appdirs locations.
	ModeDefault Mode = "default"
)

// Error codes.
const (
	// CodeUnwritable: a directory cannot be created or written.
	CodeUnwritable = "datadir.unwritable"
	// CodeNotFound: the chosen data directory does not exist (a disk that
	// is not mounted).
	CodeNotFound = "datadir.not_found"
	// CodeLocationInvalid: data-location.json cannot be read or parsed.
	CodeLocationInvalid = "datadir.location_invalid"
	// CodeMoveFailed: a scheduled change could not be carried out.
	CodeMoveFailed = "datadir.move_failed"
	// CodeChangeUnavailable: the data directory is fixed by
	// POSTSONDE_HOME or portable mode.
	CodeChangeUnavailable = "datadir.change_unavailable"
	// CodeInvalidPath: the chosen folder cannot be the data directory
	// (not absolute, a file, the current one, nested, in a project).
	CodeInvalidPath = "datadir.invalid_path"
	// CodeNotEmpty: the chosen folder has other files.
	CodeNotEmpty = "datadir.not_empty"
	// CodeInvalidAction: the action does not fit the chosen folder.
	CodeInvalidAction = "datadir.invalid_action"
	// CodeDeleteFailed: the old copy could not be deleted.
	CodeDeleteFailed = "datadir.delete_failed"
)

// Env is what Resolve reads from the system.
type Env struct {
	// GOOS is a runtime.GOOS value.
	GOOS string
	// Getenv looks up an environment variable.
	Getenv func(string) string
	// Home is the user's home directory ("" if unknown).
	Home string
	// Executable is the absolute path of the running binary, symlinks
	// resolved ("" if unknown: no portable mode).
	Executable string
}

// HostEnv returns the Env of the running process.
func HostEnv() Env {
	home, _ := os.UserHomeDir()
	exe, err := os.Executable()
	if err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
	} else {
		exe = ""
	}
	return Env{GOOS: runtime.GOOS, Getenv: os.Getenv, Home: home, Executable: exe}
}

// Resolution is where the directories are for this run, before anything
// is created.
type Resolution struct {
	// Dirs are the directories in effect.
	Dirs appdirs.Dirs
	// Mode says which rule gave Dirs.
	Mode Mode
	// Default is the data directory the OS default gives ("" when the
	// home directory is unknown).
	Default string
	// AppFolder is where the portable marker is looked for ("" when the
	// executable is unknown).
	AppFolder string
	// Root is POSTSONDE_HOME (ModeEnv) or the portable folder
	// (ModePortable), else "".
	Root string
	// LocationPath is data-location.json in the config directory.
	LocationPath string
	// Location is its content (zero when missing or ignored).
	Location Location
	// Problems found while resolving (a broken location file).
	Problems []*errs.Error
}

// Location is the content of data-location.json.
type Location struct {
	Version int `json:"version"`
	// DataDir is the chosen data directory; "" is the default.
	DataDir string `json:"dataDir,omitempty"`
	// Pending is a change to carry out at the next start.
	Pending *Pending `json:"pending,omitempty"`
	// Previous is the old folder a move copied from, kept until the user
	// deletes or forgets it.
	Previous *Previous `json:"previous,omitempty"`
}

// Action is what a scheduled change does with the data.
type Action string

const (
	// ActionMove copies the data to the new folder, checks the copy and
	// switches; the old folder is kept.
	ActionMove Action = "move"
	// ActionFresh switches to an empty folder and leaves the data where it
	// is.
	ActionFresh Action = "fresh"
	// ActionUse switches to a folder that already holds PostSonde data.
	ActionUse Action = "use"
)

// Pending is a scheduled change.
type Pending struct {
	Action Action `json:"action"`
	// DataDir is the new data directory.
	DataDir string `json:"dataDir"`
	// From is the data directory when the change was scheduled; the change
	// is dropped if it is no longer the one in effect.
	From string `json:"from"`
	// RequestedAt is an RFC 3339 time.
	RequestedAt string `json:"requestedAt,omitempty"`
}

// Previous is the folder a move copied from.
type Previous struct {
	DataDir string `json:"dataDir"`
	// Entries are the names copied (see DataEntries), which DeletePrevious
	// removes.
	Entries []string `json:"entries"`
	// MovedAt is an RFC 3339 time.
	MovedAt string `json:"movedAt,omitempty"`
}

// AppFolder returns the folder where portable mode looks for its marker,
// for the executable exe on goos. getenv reads $APPIMAGE and $APPDIR.
func AppFolder(goos string, getenv func(string) string, exe string) string {
	if exe == "" {
		return ""
	}
	dir := filepath.Dir(exe)
	// An AppImage runs from a temporary mount; the folder that matters is
	// the one holding the .AppImage file. Only trust $APPIMAGE when the
	// binary is inside $APPDIR, so a stray variable changes nothing.
	if goos == "linux" {
		if img, appDir := getenv("APPIMAGE"), getenv("APPDIR"); img != "" && appDir != "" && filepath.IsAbs(img) && within(appDir, exe) {
			return filepath.Dir(img)
		}
	}
	// …/PostSonde.app/Contents/MacOS/postsonde → the folder of PostSonde.app.
	if goos == "darwin" {
		macOS := dir
		contents := filepath.Dir(macOS)
		bundle := filepath.Dir(contents)
		if filepath.Base(macOS) == "MacOS" && filepath.Base(contents) == "Contents" && strings.HasSuffix(bundle, ".app") {
			return filepath.Dir(bundle)
		}
	}
	return dir
}

// Resolve applies the precedence rules (see the package documentation). It
// reads the marker and the location file but creates nothing.
func Resolve(env Env) (Resolution, error) {
	getenv := env.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	noHome := func(k string) string {
		if k == appdirs.EnvHome {
			return ""
		}
		return getenv(k)
	}
	var r Resolution
	r.AppFolder = AppFolder(env.GOOS, getenv, env.Executable)
	defaults, defErr := appdirs.ResolveFor(env.GOOS, noHome, env.Home)
	if defErr == nil {
		r.Default = defaults.Data
	}

	if h := getenv(appdirs.EnvHome); h != "" {
		root, err := filepath.Abs(h)
		if err != nil {
			return Resolution{}, err
		}
		r.Mode, r.Root, r.Dirs = ModeEnv, root, appdirs.Under(root)
		r.LocationPath = filepath.Join(r.Dirs.Config, LocationFileName)
		return r, nil
	}
	if r.AppFolder != "" {
		if fi, err := os.Stat(filepath.Join(r.AppFolder, MarkerName)); err == nil && !fi.IsDir() {
			root := filepath.Join(r.AppFolder, PortableDirName)
			r.Mode, r.Root, r.Dirs = ModePortable, root, appdirs.Under(root)
			r.LocationPath = filepath.Join(r.Dirs.Config, LocationFileName)
			return r, nil
		}
	}
	if defErr != nil {
		return Resolution{}, defErr
	}
	r.Mode, r.Dirs = ModeDefault, defaults
	r.LocationPath = filepath.Join(defaults.Config, LocationFileName)
	loc, err := ReadLocation(r.LocationPath)
	if err != nil {
		r.Problems = append(r.Problems, err)
		// The chosen folder is unknown: keep no data rather than write to
		// a folder the user moved away from.
		r.Dirs.Data = ""
		r.Mode = ModeConfigured
		return r, nil
	}
	r.Location = loc
	if loc.DataDir != "" {
		r.Mode, r.Dirs.Data = ModeConfigured, filepath.Clean(loc.DataDir)
	}
	return r, nil
}

// ReadLocation reads a location file. A missing or empty file is the zero
// Location. The error is a CodeLocationInvalid *errs.Error.
func ReadLocation(path string) (Location, *errs.Error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && len(strings.TrimSpace(string(data))) == 0) {
		return Location{}, nil
	}
	invalid := func(err error, kind errs.Kind, msg string) *errs.Error {
		return errs.Wrap(err, kind, msg).
			WithCode(CodeLocationInvalid).
			WithHint("Fix or delete "+path+", then restart PostSonde. Until then nothing is kept in a data directory.").
			WithDetail("path", path)
	}
	if err != nil {
		return Location{}, invalid(err, errs.KindOf(err), "The data directory setting could not be read.")
	}
	var loc Location
	if err := json.Unmarshal(data, &loc); err != nil {
		return Location{}, invalid(err, errs.Parse, "The data directory setting is not valid JSON.")
	}
	if loc.Version > LocationVersion {
		return Location{}, invalid(nil, errs.Unsupported, "The data directory setting is from a newer PostSonde.")
	}
	if loc.DataDir != "" && !filepath.IsAbs(loc.DataDir) {
		return Location{}, invalid(nil, errs.InvalidArgument, "The chosen data directory is not an absolute path.")
	}
	if p := loc.Pending; p != nil && (!filepath.IsAbs(p.DataDir) || !validAction(p.Action)) {
		loc.Pending = nil // a broken schedule is dropped, the rest stays usable
	}
	if p := loc.Previous; p != nil && !filepath.IsAbs(p.DataDir) {
		loc.Previous = nil
	}
	return loc, nil
}

func validAction(a Action) bool {
	return a == ActionMove || a == ActionFresh || a == ActionUse
}

// within reports whether path is dir or inside it (lexically).
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// samePath reports whether a and b name the same folder, comparing the
// resolved paths when they exist.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = realPath(a), realPath(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		// Case-insensitive by default on both.
		return strings.EqualFold(a, b)
	}
	return a == b
}

// realPath is path cleaned, with symlinks resolved in the longest part of
// it that exists.
func realPath(path string) string {
	path = filepath.Clean(path)
	var rest []string
	for p := path; ; {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(append([]string{r}, rest...)...)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return path
		}
		rest = append([]string{filepath.Base(p)}, rest...)
		p = parent
	}
}
