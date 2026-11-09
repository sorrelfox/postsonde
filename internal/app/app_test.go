package app

import (
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sorrelfox/postsonde/internal/app/services/settings"
	"github.com/sorrelfox/postsonde/internal/app/services/system"
	"github.com/sorrelfox/postsonde/internal/app/services/workspace"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
)

func TestMainWindowOptions(t *testing.T) {
	t.Parallel()

	w := mainWindowOptions(darkBackground)

	if w.Title != "PostSonde" {
		t.Errorf("Title = %q, want %q", w.Title, "PostSonde")
	}
	if w.URL != "/" {
		t.Errorf("URL = %q, want %q", w.URL, "/")
	}

	tests := []struct {
		name          string
		size, minimum int
	}{
		{"width", w.Width, w.MinWidth},
		{"height", w.Height, w.MinHeight},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.minimum <= 0 {
				t.Errorf("minimum %s = %d, want > 0", tt.name, tt.minimum)
			}
			if tt.size < tt.minimum {
				t.Errorf("default %s %d is below the minimum %d", tt.name, tt.size, tt.minimum)
			}
		})
	}
}

func TestAppOptions(t *testing.T) {
	t.Parallel()

	assets := fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	o := appOptions(Options{Assets: assets}, nil)

	if o.Name != "PostSonde" {
		t.Errorf("Name = %q, want %q", o.Name, "PostSonde")
	}
	if o.Assets.Handler == nil {
		t.Error("Assets.Handler is nil; the embedded frontend would not be served")
	}
	if !o.Mac.ApplicationShouldTerminateAfterLastWindowClosed {
		t.Error("closing the last window should quit the app on macOS")
	}
	// Run registers the services, after the single-instance lock.
	if len(o.Services) != 0 {
		t.Error("services are built before the single-instance lock")
	}
	if o.SingleInstance != nil {
		t.Error("the single-instance lock is on without an InstanceID")
	}
	if len(services(Config{})) == 0 {
		t.Error("no services are bound")
	}
}

// TestServicesMarshalErrors guards the error model: a service registered
// without serviceOptions would reject its calls with Wails' default error
// JSON instead of an ErrorDTO.
func TestServicesMarshalErrors(t *testing.T) {
	t.Parallel()

	for _, s := range services(Config{}) {
		// application.Service keeps its options unexported; reflection can
		// still tell whether MarshalError is set.
		opts := reflect.ValueOf(s).FieldByName("options")
		if !opts.IsValid() || !opts.FieldByName("MarshalError").IsValid() {
			t.Fatal("application.Service no longer has options.MarshalError; Wails changed, recheck docs/errors.md")
		}
		if opts.FieldByName("MarshalError").IsNil() {
			t.Errorf("%T is registered without serviceOptions; its errors would not reach the UI as ErrorDTOs", s.Instance())
		}
	}
}

func TestServices(t *testing.T) {
	t.Parallel()

	svcs := services(Config{Version: "v1.2.3"})

	var sys *system.SystemService
	for _, s := range svcs {
		if v, ok := s.Instance().(*system.SystemService); ok {
			sys = v
		}
	}
	if sys == nil {
		t.Fatal("SystemService is not bound")
	}
	info, err := sys.Info(t.Context())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "PostSonde" || info.Version != "v1.2.3" {
		t.Errorf("Info = %+v, want name PostSonde and version v1.2.3 passed down from Config", info)
	}
}

// TestServicesSettings checks that Config.SettingsPath reaches the bound
// SettingsService: an update writes the file there.
func TestServicesSettings(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), coresettings.FileName)
	var svc *settings.SettingsService
	for _, s := range services(Config{SettingsPath: path}) {
		if v, ok := s.Instance().(*settings.SettingsService); ok {
			svc = v
		}
	}
	if svc == nil {
		t.Fatal("SettingsService is not bound")
	}
	ratio := 0.3
	if _, err := svc.Update(t.Context(), settings.SettingsPatchDTO{UI: &settings.UISettingsPatchDTO{SplitRatio: &ratio}}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := coresettings.Load(path); err != nil || got.UI.SplitRatio != 0.3 {
		t.Errorf("settings file = %+v, %v; want splitRatio 0.3", got, err)
	}
}

// TestServicesWorkspace checks that Config.DataDir reaches the bound
// WorkspaceService: a created project's local directory is under it.
func TestServicesWorkspace(t *testing.T) {
	t.Parallel()

	data := t.TempDir()
	var svc *workspace.WorkspaceService
	for _, s := range services(Config{DataDir: data}) {
		if v, ok := s.Instance().(*workspace.WorkspaceService); ok {
			svc = v
		}
	}
	if svc == nil {
		t.Fatal("WorkspaceService is not bound")
	}
	w, err := svc.Create(t.Context(), workspace.CreateInputDTO{Path: filepath.Join(t.TempDir(), "api")})
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(data, w.LocalDir); err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("LocalDir = %s, want it under %s", w.LocalDir, data)
	}
}

func TestModuleVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{"no build info", nil, ""},
		{"not linked", &debug.BuildInfo{Deps: []*debug.Module{{Path: "example.com/other", Version: "v1.0.0"}}}, ""},
		{"linked", &debug.BuildInfo{Deps: []*debug.Module{
			{Path: "example.com/other", Version: "v1.0.0"},
			{Path: wailsModule, Version: "v3.0.0"},
		}}, "v3.0.0"},
		{"replaced", &debug.BuildInfo{Deps: []*debug.Module{
			{Path: wailsModule, Version: "v3.0.0", Replace: &debug.Module{Path: "example.com/fork", Version: "v3.0.0-fork.1"}},
		}}, "v3.0.0-fork.1"},
		{"replaced by a directory", &debug.BuildInfo{Deps: []*debug.Module{
			{Path: wailsModule, Version: "v3.0.0", Replace: &debug.Module{Path: "../wails/v3"}},
		}}, "v3.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := moduleVersion(tt.info, wailsModule); got != tt.want {
				t.Errorf("moduleVersion = %q, want %q", got, tt.want)
			}
		})
	}
}
