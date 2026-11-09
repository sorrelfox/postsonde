package app

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/app/services/window"
	"github.com/sorrelfox/postsonde/internal/app/services/workspace"
	"github.com/sorrelfox/postsonde/internal/app/windowctx"
)

func TestInstanceID(t *testing.T) {
	t.Parallel()
	a := InstanceID("/home/u/.config/PostSonde")
	b := InstanceID("/media/stick/PostSonde/postsonde-data/config")
	if a == b {
		t.Errorf("two config directories share the ID %q", a)
	}
	if again := InstanceID("/home/u/.config/PostSonde/"); again != a {
		t.Errorf("the same directory spelled differently: %q and %q", again, a)
	}
	if got := InstanceID(""); got != "io.github.sorrelfox.PostSonde" {
		t.Errorf("InstanceID(\"\") = %q", got)
	}
	// A lock file name, a D-Bus bus name (Wails v3.0.0 uses the ID as is
	// and refuses an element that starts with a digit) and a mutex name:
	// letters, digits and dots, no leading digit in an element.
	valid := regexp.MustCompile(`^io\.github\.sorrelfox\.PostSonde\.i[0-9a-f]{16}$`)
	if !valid.MatchString(a) {
		t.Errorf("InstanceID = %q", a)
	}
}

func TestSingleInstanceAvailable(t *testing.T) {
	t.Parallel()
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	exists := func(want string) func(string) bool { return func(p string) bool { return p == want } }
	none := func(string) bool { return false }
	for _, c := range []struct {
		name   string
		goos   string
		env    map[string]string
		exists func(string) bool
		want   bool
	}{
		{"macOS", "darwin", nil, none, true},
		{"Windows", "windows", nil, none, true},
		{"Linux with a bus address", "linux", map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus"}, none, true},
		{"Linux with the runtime bus", "linux", map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, exists(filepath.Join("/run/user/1000", "bus")), true},
		{"Linux autolaunch only", "linux", map[string]string{"DBUS_SESSION_BUS_ADDRESS": "autolaunch:"}, none, false},
		{"Linux without a bus", "linux", map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, none, false},
	} {
		if got := singleInstanceAvailable(c.goos, env(c.env), c.exists); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAppOptionsSingleInstance(t *testing.T) {
	t.Parallel()
	assets := fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	o := appOptions(Options{Assets: assets, InstanceID: "io.github.sorrelfox.PostSonde.itest"}, &App{windows: newWindowHost(nil)})
	if !singleInstanceAvailable(runtime.GOOS, os.Getenv, fileExists) {
		if o.SingleInstance != nil {
			t.Error("the lock is on where it cannot work")
		}
		return
	}
	if o.SingleInstance == nil || o.SingleInstance.UniqueID != "io.github.sorrelfox.PostSonde.itest" || o.SingleInstance.OnSecondInstanceLaunch == nil {
		t.Fatalf("SingleInstance = %+v", o.SingleInstance)
	}
}

func TestWindowTitle(t *testing.T) {
	t.Parallel()
	if got := windowTitle(""); got != "PostSonde" {
		t.Errorf("no project: %q", got)
	}
	if got := windowTitle("Shop API"); got != "Shop API - PostSonde" {
		t.Errorf("project: %q", got)
	}
}

// Without an application (tests of the services) the host opens nothing
// and counts one window; launches wait until the window service is set.
func TestWindowHostWithoutApp(t *testing.T) {
	t.Parallel()
	h := newWindowHost(nil)
	if _, err := h.Open(); !errors.Is(err, errNoApp) {
		t.Errorf("Open = %v", err)
	}
	if h.Count() != 1 || h.others(windowctx.With(t.Context(), 1)) != 0 || h.Focus(0) != 0 {
		t.Errorf("Count %d, others %d", h.Count(), h.others(t.Context()))
	}
	h.retitle() // no windows: nothing to do

	var mu sync.Mutex
	var got [][]string
	h.secondInstance(application.SecondInstanceData{Args: []string{"/Applications/PostSonde.app/Contents/MacOS/postsonde", "/p"}, WorkingDir: "/w"})
	h.secondInstance(application.SecondInstanceData{})
	h.setLaunch(func(args []string, wd string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, append(args, "@"+wd))
	})
	h.secondInstance(application.SecondInstanceData{Args: []string{"postsonde"}, WorkingDir: "/x"})
	want := [][]string{{"/p", "@/w"}, {"@"}, {"@/x"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("launches = %q, want %q", got, want)
	}
}

// TestServicesWindows: the window service is bound, the workspace service
// asks the host about other windows, and the title follows the project.
func TestServicesWindows(t *testing.T) {
	t.Parallel()
	host := newWindowHost(nil)
	var ws *workspace.WorkspaceService
	var win *window.WindowService
	for _, s := range services(Config{DataDir: t.TempDir(), windows: host}) {
		switch v := s.Instance().(type) {
		case *workspace.WorkspaceService:
			ws = v
		case *window.WindowService:
			win = v
		}
	}
	if ws == nil || win == nil {
		t.Fatal("the workspace or window service is not bound")
	}
	if got := host.title(); got != "PostSonde" {
		t.Errorf("title with no project = %q", got)
	}
	if _, err := ws.Create(t.Context(), workspace.CreateInputDTO{Path: filepath.Join(t.TempDir(), "api"), Name: "Shop API"}); err != nil {
		t.Fatal(err)
	}
	if got := host.title(); got != "Shop API - PostSonde" {
		t.Errorf("title = %q", got)
	}
	// No application: NewWindow fails, cleanly.
	if err := win.NewWindow(t.Context()); err == nil {
		t.Error("NewWindow without an application succeeded")
	}
	// With no other window the project can be closed.
	if err := ws.Close(t.Context()); err != nil {
		t.Errorf("Close with one window: %v", err)
	}
}
