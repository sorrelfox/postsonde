package datadir

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/appdirs"
)

// fixture is a fake machine under a temp dir: a home, an app folder with
// the binary in it, and an environment.
type fixture struct {
	home, app, exe string
	env            map[string]string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	f := &fixture{
		home: filepath.Join(base, "home"),
		app:  filepath.Join(base, "usb", "PostSonde"),
		env:  map[string]string{},
	}
	f.exe = filepath.Join(f.app, "postsonde")
	for _, d := range []string{f.home, f.app} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Keep XDG and Windows variables inside the fixture.
	f.env["XDG_CONFIG_HOME"] = filepath.Join(f.home, ".config")
	f.env["XDG_DATA_HOME"] = filepath.Join(f.home, ".local", "share")
	f.env["XDG_CACHE_HOME"] = filepath.Join(f.home, ".cache")
	f.env["XDG_STATE_HOME"] = filepath.Join(f.home, ".local", "state")
	f.env["APPDATA"] = filepath.Join(f.home, "AppData", "Roaming")
	f.env["LOCALAPPDATA"] = filepath.Join(f.home, "AppData", "Local")
	return f
}

func (f *fixture) Env() Env {
	return Env{GOOS: runtime.GOOS, Getenv: func(k string) string { return f.env[k] }, Home: f.home, Executable: f.exe}
}

func (f *fixture) defaults(t *testing.T) appdirs.Dirs {
	t.Helper()
	d, err := appdirs.ResolveFor(runtime.GOOS, func(k string) string {
		if k == appdirs.EnvHome {
			return ""
		}
		return f.env[k]
	}, f.home)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f *fixture) portable(t *testing.T) {
	t.Helper()
	writeFile(t, filepath.Join(f.app, MarkerName), "")
}

func (f *fixture) configure(t *testing.T, loc Location) {
	t.Helper()
	path := filepath.Join(f.defaults(t).Config, LocationFileName)
	if err := writeLocation(path, loc); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolvePrecedence(t *testing.T) {
	type setup struct{ env, marker, configured bool }
	tests := []struct {
		name string
		setup
		want Mode
	}{
		{"nothing set: default", setup{}, ModeDefault},
		{"configured", setup{configured: true}, ModeConfigured},
		{"portable beats configured", setup{marker: true, configured: true}, ModePortable},
		{"env beats portable and configured", setup{env: true, marker: true, configured: true}, ModeEnv},
		{"env beats configured", setup{env: true, configured: true}, ModeEnv},
		{"portable alone", setup{marker: true}, ModePortable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			envRoot := filepath.Join(f.home, "ps-home")
			chosen := filepath.Join(f.home, "chosen")
			if tt.env {
				f.env[appdirs.EnvHome] = envRoot
			}
			if tt.marker {
				f.portable(t)
			}
			if tt.configured {
				f.configure(t, Location{DataDir: chosen})
			}
			r, err := Resolve(f.Env())
			if err != nil {
				t.Fatal(err)
			}
			if r.Mode != tt.want {
				t.Fatalf("Mode = %q, want %q", r.Mode, tt.want)
			}
			def := f.defaults(t)
			want := def
			switch tt.want {
			case ModeEnv:
				want = appdirs.Under(envRoot)
			case ModePortable:
				want = appdirs.Under(filepath.Join(f.app, PortableDirName))
			case ModeConfigured:
				want.Data = chosen
			}
			if r.Dirs != want {
				t.Errorf("Dirs = %+v\nwant %+v", r.Dirs, want)
			}
			if r.Default != def.Data {
				t.Errorf("Default = %q, want %q", r.Default, def.Data)
			}
		})
	}
}

func TestResolveMarkerMustBeAFile(t *testing.T) {
	f := newFixture(t)
	if err := os.Mkdir(filepath.Join(f.app, MarkerName), 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := Resolve(f.Env())
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != ModeDefault {
		t.Errorf("Mode = %q, want default for a marker folder", r.Mode)
	}
}

func TestResolveBrokenLocation(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.defaults(t).Config, LocationFileName), "{not json")
	r, err := Resolve(f.Env())
	if err != nil {
		t.Fatal(err)
	}
	if r.Dirs.Data != "" || len(r.Problems) != 1 || r.Problems[0].Code != CodeLocationInvalid {
		t.Errorf("Data = %q, problems = %v; want no data dir and %s", r.Dirs.Data, r.Problems, CodeLocationInvalid)
	}
	if r.Dirs.Config == "" {
		t.Error("Config should stay usable")
	}
}

func TestReadLocation(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "d")
	tests := []struct {
		name, content string
		ok            bool
		want          Location
	}{
		{"missing", "", true, Location{}},
		{"empty", "  \n", true, Location{}},
		{"data dir", `{"version":1,"dataDir":` + strconv.Quote(abs) + `}`, true, Location{Version: 1, DataDir: abs}},
		{"relative data dir", `{"version":1,"dataDir":"rel"}`, false, Location{}},
		{"newer", `{"version":2}`, false, Location{}},
		{"broken pending dropped", `{"version":1,"pending":{"action":"zap","dataDir":` + strconv.Quote(abs) + `}}`, true, Location{Version: 1}},
		{"not json", `[`, false, Location{}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, "loc", string(rune('a'+i))+".json")
			if tt.name != "missing" {
				writeFile(t, path, tt.content)
			}
			got, err := ReadLocation(path)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if err == nil && (got.DataDir != tt.want.DataDir || got.Version != tt.want.Version || got.Pending != nil) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAppFolder(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	none := env(nil)
	j := filepath.Join
	// Absolute on every system (on Windows, the current drive): the
	// AppImage case trusts only an absolute APPIMAGE.
	root, err := filepath.Abs(string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		goos   string
		getenv func(string) string
		exe    string
		want   string
	}{
		{"unknown executable", "linux", none, "", ""},
		{"plain binary", "linux", none, j(root, "usb", "ps", "postsonde"), j(root, "usb", "ps")},
		{"windows exe", "windows", none, j(root, "usb", "PostSonde.exe"), j(root, "usb")},
		{"macOS bundle", "darwin", none, j(root, "Volumes", "USB", "PostSonde.app", "Contents", "MacOS", "postsonde"), j(root, "Volumes", "USB")},
		{"macOS bare binary", "darwin", none, j(root, "src", "bin", "postsonde"), j(root, "src", "bin")},
		{"bundle layout ignored off macOS", "linux", none, j(root, "x", "P.app", "Contents", "MacOS", "p"), j(root, "x", "P.app", "Contents", "MacOS")},
		{
			"AppImage", "linux",
			env(map[string]string{"APPIMAGE": j(root, "usb", "PostSonde.AppImage"), "APPDIR": j(root, "tmp", ".mount_x")}),
			j(root, "tmp", ".mount_x", "usr", "bin", "postsonde"), j(root, "usb"),
		},
		{
			"stray APPIMAGE ignored", "linux",
			env(map[string]string{"APPIMAGE": j(root, "usb", "PostSonde.AppImage"), "APPDIR": j(root, "tmp", ".mount_x")}),
			j(root, "opt", "ps", "postsonde"), j(root, "opt", "ps"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AppFolder(tt.goos, tt.getenv, tt.exe); got != tt.want {
				t.Errorf("AppFolder = %q, want %q", got, tt.want)
			}
		})
	}
}
