package appdirs

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// unix and win build expected paths for the target OS, independent of the
// host running the test.
func unix(elem ...string) string { return path.Join(elem...) }
func win(elem ...string) string  { return strings.Join(elem, `\`) }

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveFor(t *testing.T) {
	const (
		uhome = "/home/mike"
		mhome = "/Users/mike"
		whome = `C:\Users\mike`
	)
	winEnv := map[string]string{
		"APPDATA":      `C:\Users\mike\AppData\Roaming`,
		"LOCALAPPDATA": `C:\Users\mike\AppData\Local`,
	}
	winLocal := win(whome, "AppData", "Local", AppName)

	tests := []struct {
		name string
		goos string
		env  map[string]string
		home string
		want Dirs
	}{
		{
			name: "linux defaults",
			goos: "linux",
			home: uhome,
			want: Dirs{
				Config: unix(uhome, ".config", UnixName),
				Data:   unix(uhome, ".local", "share", UnixName),
				Cache:  unix(uhome, ".cache", UnixName),
				Logs:   unix(uhome, ".local", "state", UnixName),
			},
		},
		{
			name: "linux XDG vars",
			goos: "linux",
			env: map[string]string{
				"XDG_CONFIG_HOME": "/xdg/config",
				"XDG_DATA_HOME":   "/xdg/data",
				"XDG_CACHE_HOME":  "/xdg/cache",
				"XDG_STATE_HOME":  "/xdg/state/",
			},
			home: uhome,
			want: Dirs{
				Config: unix("/xdg/config", UnixName),
				Data:   unix("/xdg/data", UnixName),
				Cache:  unix("/xdg/cache", UnixName),
				Logs:   unix("/xdg/state", UnixName),
			},
		},
		{
			name: "linux relative XDG vars are ignored",
			goos: "linux",
			env: map[string]string{
				"XDG_CONFIG_HOME": "relative/config",
				"XDG_DATA_HOME":   "./data",
				"XDG_CACHE_HOME":  "/abs/cache",
				"XDG_STATE_HOME":  "~/state",
			},
			home: uhome,
			want: Dirs{
				Config: unix(uhome, ".config", UnixName),
				Data:   unix(uhome, ".local", "share", UnixName),
				Cache:  unix("/abs/cache", UnixName),
				Logs:   unix(uhome, ".local", "state", UnixName),
			},
		},
		{
			name: "linux XDG vars need no home",
			goos: "linux",
			env: map[string]string{
				"XDG_CONFIG_HOME": "/c", "XDG_DATA_HOME": "/d",
				"XDG_CACHE_HOME": "/k", "XDG_STATE_HOME": "/s",
			},
			want: Dirs{
				Config: unix("/c", UnixName),
				Data:   unix("/d", UnixName),
				Cache:  unix("/k", UnixName),
				Logs:   unix("/s", UnixName),
			},
		},
		{
			name: "other unix follows XDG",
			goos: "freebsd",
			env:  map[string]string{"XDG_CACHE_HOME": "/var/cache/mike"},
			home: uhome,
			want: Dirs{
				Config: unix(uhome, ".config", UnixName),
				Data:   unix(uhome, ".local", "share", UnixName),
				Cache:  unix("/var/cache/mike", UnixName),
				Logs:   unix(uhome, ".local", "state", UnixName),
			},
		},
		{
			name: "darwin",
			goos: "darwin",
			home: mhome,
			want: Dirs{
				Config: unix(mhome, "Library", "Application Support", AppName),
				Data:   unix(mhome, "Library", "Application Support", AppName),
				Cache:  unix(mhome, "Library", "Caches", AppName),
				Logs:   unix(mhome, "Library", "Logs", AppName),
			},
		},
		{
			name: "darwin ignores XDG vars",
			goos: "darwin",
			env:  map[string]string{"XDG_CONFIG_HOME": "/xdg/config", "XDG_CACHE_HOME": "/xdg/cache"},
			home: mhome,
			want: Dirs{
				Config: unix(mhome, "Library", "Application Support", AppName),
				Data:   unix(mhome, "Library", "Application Support", AppName),
				Cache:  unix(mhome, "Library", "Caches", AppName),
				Logs:   unix(mhome, "Library", "Logs", AppName),
			},
		},
		{
			name: "windows",
			goos: "windows",
			env:  winEnv,
			home: whome,
			want: Dirs{
				Config: win(whome, "AppData", "Roaming", AppName),
				Data:   winLocal,
				Cache:  win(winLocal, "Cache"),
				Logs:   win(winLocal, "Logs"),
			},
		},
		{
			name: "windows custom app data and trailing separator",
			goos: "windows",
			env:  map[string]string{"APPDATA": `D:\Roam\`, "LOCALAPPDATA": `E:\`},
			want: Dirs{
				Config: win(`D:\Roam`, AppName),
				Data:   win(`E:`, AppName),
				Cache:  win(`E:`, AppName, "Cache"),
				Logs:   win(`E:`, AppName, "Logs"),
			},
		},
		{
			name: "windows falls back to home",
			goos: "windows",
			home: whome,
			want: Dirs{
				Config: win(whome, "AppData", "Roaming", AppName),
				Data:   winLocal,
				Cache:  win(winLocal, "Cache"),
				Logs:   win(winLocal, "Logs"),
			},
		},
		{
			name: "windows ignores XDG vars",
			goos: "windows",
			env: map[string]string{
				"APPDATA": winEnv["APPDATA"], "LOCALAPPDATA": winEnv["LOCALAPPDATA"],
				"XDG_CONFIG_HOME": "/xdg/config",
			},
			want: Dirs{
				Config: win(whome, "AppData", "Roaming", AppName),
				Data:   winLocal,
				Cache:  win(winLocal, "Cache"),
				Logs:   win(winLocal, "Logs"),
			},
		},
		{
			name: "override on linux",
			goos: "linux",
			env:  map[string]string{EnvHome: "/portable/ps", "XDG_CONFIG_HOME": "/xdg/config"},
			home: uhome,
			want: Dirs{
				Config: unix("/portable/ps", "config"),
				Data:   unix("/portable/ps", "data"),
				Cache:  unix("/portable/ps", "cache"),
				Logs:   unix("/portable/ps", "logs"),
			},
		},
		{
			name: "override on darwin needs no home",
			goos: "darwin",
			env:  map[string]string{EnvHome: "/Volumes/USB/PostSonde"},
			want: Dirs{
				Config: unix("/Volumes/USB/PostSonde", "config"),
				Data:   unix("/Volumes/USB/PostSonde", "data"),
				Cache:  unix("/Volumes/USB/PostSonde", "cache"),
				Logs:   unix("/Volumes/USB/PostSonde", "logs"),
			},
		},
		{
			name: "override on windows",
			goos: "windows",
			env:  map[string]string{EnvHome: `F:\PostSonde`, "APPDATA": winEnv["APPDATA"]},
			want: Dirs{
				Config: win(`F:\PostSonde`, "config"),
				Data:   win(`F:\PostSonde`, "data"),
				Cache:  win(`F:\PostSonde`, "cache"),
				Logs:   win(`F:\PostSonde`, "logs"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveFor(tt.goos, env(tt.env), tt.home)
			if err != nil {
				t.Fatalf("ResolveFor: %v", err)
			}
			if got != tt.want {
				t.Errorf("ResolveFor(%q) =\n  %+v\nwant\n  %+v", tt.goos, got, tt.want)
			}
		})
	}
}

func TestResolveForErrors(t *testing.T) {
	tests := []struct {
		name string
		goos string
		env  map[string]string
	}{
		{name: "linux without home", goos: "linux"},
		{name: "linux with some XDG vars but no home", goos: "linux", env: map[string]string{"XDG_CONFIG_HOME": "/c"}},
		{name: "linux with relative XDG vars and no home", goos: "linux", env: map[string]string{
			"XDG_CONFIG_HOME": "c", "XDG_DATA_HOME": "d", "XDG_CACHE_HOME": "k", "XDG_STATE_HOME": "s",
		}},
		{name: "darwin without home", goos: "darwin"},
		{name: "windows without env or home", goos: "windows"},
		{name: "windows with only APPDATA", goos: "windows", env: map[string]string{"APPDATA": `C:\R`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got, err := ResolveFor(tt.goos, env(tt.env), ""); err == nil {
				t.Errorf("ResolveFor(%q) = %+v, want error", tt.goos, got)
			}
		})
	}
}

func TestWindowsJoin(t *testing.T) {
	tests := []struct {
		elem []string
		want string
	}{
		{[]string{`C:\Users\m`, "AppData"}, `C:\Users\m\AppData`},
		{[]string{`C:\`, "PostSonde"}, `C:\PostSonde`},
		{[]string{`C:\a\`, `\b`, "c"}, `C:\a\b\c`},
		{[]string{`C:/a/`, "b"}, `C:/a\b`},
		{[]string{"", `C:\a`, "", "b"}, `C:\a\b`},
		{[]string{`\\server\share`, "PostSonde"}, `\\server\share\PostSonde`},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := windowsJoin(tt.elem...); got != tt.want {
				t.Errorf("windowsJoin(%q) = %q, want %q", tt.elem, got, tt.want)
			}
		})
	}
}

func TestUnder(t *testing.T) {
	root := filepath.Join("some", "root")
	want := Dirs{
		Config: filepath.Join(root, "config"),
		Data:   filepath.Join(root, "data"),
		Cache:  filepath.Join(root, "cache"),
		Logs:   filepath.Join(root, "logs"),
	}
	if got := Under(root); got != want {
		t.Errorf("Under(%q) = %+v, want %+v", root, got, want)
	}
}

func TestResolve(t *testing.T) {
	t.Run("override", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv(EnvHome, root)
		got, err := Resolve()
		if err != nil {
			t.Fatal(err)
		}
		if want := Under(root); got != want {
			t.Errorf("Resolve() = %+v, want %+v", got, want)
		}
	})
	t.Run("relative override is made absolute", func(t *testing.T) {
		t.Chdir(t.TempDir())
		t.Setenv(EnvHome, "portable")
		abs, err := filepath.Abs("portable")
		if err != nil {
			t.Fatal(err)
		}
		got, err := Resolve()
		if err != nil {
			t.Fatal(err)
		}
		if want := Under(abs); got != want {
			t.Errorf("Resolve() = %+v, want %+v", got, want)
		}
	})
	t.Run("host defaults", func(t *testing.T) {
		t.Setenv(EnvHome, "")
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no home directory:", err)
		}
		got, err := Resolve()
		if err != nil {
			t.Fatal(err)
		}
		want, err := ResolveFor(runtime.GOOS, os.Getenv, home)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("Resolve() = %+v, want %+v", got, want)
		}
		// The config and cache bases agree with the standard library.
		if cfg, err := os.UserConfigDir(); err == nil && filepath.Dir(got.Config) != cfg {
			t.Errorf("Config %q is not under os.UserConfigDir %q", got.Config, cfg)
		}
		if cache, err := os.UserCacheDir(); err == nil && !strings.HasPrefix(got.Cache, cache+string(filepath.Separator)) {
			t.Errorf("Cache %q is not under os.UserCacheDir %q", got.Cache, cache)
		}
	})
}

func TestEnsure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing", "parent")
	d := Under(root)
	d.Data = d.Config // like macOS, where both are the same directory
	for range 2 {     // idempotent
		if err := d.Ensure(); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
	}
	for _, p := range []string{d.Config, d.Cache, d.Logs} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.IsDir() {
			t.Errorf("%s is not a directory", p)
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm() != DirPerm {
			t.Errorf("%s has permission %v, want %v", p, fi.Mode().Perm(), fs.FileMode(DirPerm))
		}
	}

	if err := (Dirs{}).Ensure(); err == nil {
		t.Error("Ensure on empty Dirs: want error")
	}
}
