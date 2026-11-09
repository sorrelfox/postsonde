package appdirs

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// AppName is the directory name used on macOS and Windows.
	AppName = "PostSonde"
	// UnixName is the directory name used on Linux and other Unix systems.
	UnixName = "postsonde"
	// EnvHome is the environment variable that overrides all directories.
	EnvHome = "POSTSONDE_HOME"
)

// DirPerm is the permission Ensure creates directories with.
const DirPerm os.FileMode = 0o700

// Dirs holds the absolute paths of PostSonde's directories.
type Dirs struct {
	Config string // settings and preferences
	Data   string // collections, environments, history
	Cache  string // anything that can be rebuilt
	Logs   string // log files
}

// Resolve returns the directories for the running system, reading the
// environment with os.Getenv and the home directory with os.UserHomeDir.
func Resolve() (Dirs, error) {
	home, _ := os.UserHomeDir() // an empty home is reported by ResolveFor if it is needed
	d, err := ResolveFor(runtime.GOOS, os.Getenv, home)
	if err != nil {
		return Dirs{}, err
	}
	// Only an override can be relative; anchor it to the working directory.
	for _, p := range d.all() {
		if !filepath.IsAbs(*p) {
			abs, err := filepath.Abs(*p)
			if err != nil {
				return Dirs{}, fmt.Errorf("appdirs: %s: %w", EnvHome, err)
			}
			*p = abs
		}
	}
	return d, nil
}

// ResolveFor returns the directories for the OS goos (a runtime.GOOS value),
// looking up environment variables with getenv and using home as the user's
// home directory. It does not touch the file system.
//
// Paths are joined with filepath.Join when goos is the host OS, and with the
// target OS's separator otherwise, so the result for any OS can be checked on
// any host.
func ResolveFor(goos string, getenv func(string) string, home string) (Dirs, error) {
	join := joinFor(goos)
	if h := getenv(EnvHome); h != "" {
		return under(h, join), nil
	}
	switch goos {
	case "darwin", "ios":
		return darwin(home, join)
	case "windows":
		return windows(getenv, home, join)
	default:
		return xdg(getenv, home, join)
	}
}

// Under returns the override layout rooted at root: root/config, root/data,
// root/cache and root/logs.
func Under(root string) Dirs {
	return under(root, filepath.Join)
}

func under(root string, join func(...string) string) Dirs {
	return Dirs{
		Config: join(root, "config"),
		Data:   join(root, "data"),
		Cache:  join(root, "cache"),
		Logs:   join(root, "logs"),
	}
}

var errNoHome = errors.New("appdirs: home directory is unknown")

func darwin(home string, join func(...string) string) (Dirs, error) {
	if home == "" {
		return Dirs{}, errNoHome
	}
	lib := join(home, "Library")
	support := join(lib, "Application Support", AppName)
	return Dirs{
		Config: support,
		Data:   support,
		Cache:  join(lib, "Caches", AppName),
		Logs:   join(lib, "Logs", AppName),
	}, nil
}

func windows(getenv func(string) string, home string, join func(...string) string) (Dirs, error) {
	base := func(env string, fallback ...string) (string, error) {
		if v := getenv(env); v != "" {
			return v, nil
		}
		if home == "" {
			return "", fmt.Errorf("appdirs: %%%s%% is not set and the home directory is unknown", env)
		}
		return join(append([]string{home}, fallback...)...), nil
	}
	roaming, err := base("APPDATA", "AppData", "Roaming")
	if err != nil {
		return Dirs{}, err
	}
	local, err := base("LOCALAPPDATA", "AppData", "Local")
	if err != nil {
		return Dirs{}, err
	}
	data := join(local, AppName)
	return Dirs{
		Config: join(roaming, AppName),
		Data:   data,
		Cache:  join(data, "Cache"),
		Logs:   join(data, "Logs"),
	}, nil
}

func xdg(getenv func(string) string, home string, join func(...string) string) (Dirs, error) {
	var err error
	base := func(env string, fallback ...string) string {
		// The XDG spec says relative paths are invalid and must be ignored.
		if v := getenv(env); strings.HasPrefix(v, "/") {
			return join(v, UnixName)
		}
		if home == "" {
			err = fmt.Errorf("appdirs: $%s is not set to an absolute path and the home directory is unknown", env)
			return ""
		}
		return join(append(append([]string{home}, fallback...), UnixName)...)
	}
	d := Dirs{
		Config: base("XDG_CONFIG_HOME", ".config"),
		Data:   base("XDG_DATA_HOME", ".local", "share"),
		Cache:  base("XDG_CACHE_HOME", ".cache"),
		Logs:   base("XDG_STATE_HOME", ".local", "state"),
	}
	if err != nil {
		return Dirs{}, err
	}
	return d, nil
}

// joinFor returns the path join function for goos.
func joinFor(goos string) func(...string) string {
	switch {
	case goos == runtime.GOOS:
		return filepath.Join
	case goos == "windows":
		return windowsJoin
	default:
		return path.Join
	}
}

// windowsJoin joins elements with backslashes. It is used only to compute
// Windows paths on another host; it trims separators between elements but
// does not otherwise clean the result.
func windowsJoin(elem ...string) string {
	var b strings.Builder
	for _, e := range elem {
		if e == "" {
			continue
		}
		if b.Len() > 0 {
			s := strings.TrimRight(b.String(), `\/`)
			b.Reset()
			b.WriteString(s)
			b.WriteByte('\\')
			e = strings.TrimLeft(e, `\/`)
		}
		b.WriteString(e)
	}
	return b.String()
}

// Ensure creates any of the directories that do not exist yet, with
// permission DirPerm. Existing directories are left unchanged.
func (d Dirs) Ensure() error {
	for _, p := range d.all() {
		if *p == "" {
			return errors.New("appdirs: Ensure: empty directory path")
		}
		if err := os.MkdirAll(*p, DirPerm); err != nil {
			return fmt.Errorf("appdirs: %w", err)
		}
	}
	return nil
}

func (d *Dirs) all() []*string {
	return []*string{&d.Config, &d.Data, &d.Cache, &d.Logs}
}
