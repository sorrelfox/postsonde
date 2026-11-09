package settings

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// recorder collects emitted events.
type recorder struct {
	mu     sync.Mutex
	events []ChangedEventDTO
}

func (r *recorder) emit(name string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name != EventChanged {
		panic("unexpected event " + name)
	}
	r.events = append(r.events, data.(ChangedEventDTO))
}

func (r *recorder) all() []ChangedEventDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ChangedEventDTO(nil), r.events...)
}

// waitFor polls until n events were recorded or the deadline passes.
func (r *recorder) waitFor(t *testing.T, n int) []ChangedEventDTO {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ev := r.all(); len(ev) >= n {
			return ev
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("got %d events, want %d", len(r.all()), n)
	return nil
}

func newService(t *testing.T, content string) (*SettingsService, *recorder, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), core.FileName)
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recorder{}
	return New(core.Open(path, nil), rec.emit, nil), rec, path
}

func ptr[T any](v T) *T { return &v }

func TestGetAndProblems(t *testing.T) {
	t.Parallel()
	svc, rec, _ := newService(t, `{"ui":{"theme":"neon","splitRatio":0.3}}`)

	got, err := svc.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := toSettingsDTO(core.Defaults())
	want.UI.SplitRatio = 0.3
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Get = %+v, want %+v", got, want)
	}

	problems, err := svc.Problems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 {
		t.Fatalf("Problems = %+v, want one", problems)
	}
	p := problems[0]
	if p.Kind != "invalid_argument" || p.Code != core.CodeInvalidValue || p.Details["field"] != "ui.theme" || p.Hint == "" {
		t.Errorf("problem = %+v", p)
	}
	if len(rec.all()) != 0 {
		t.Error("reading emitted events")
	}
}

func TestProblemsEmptyIsNotNil(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t, "")
	problems, err := svc.Problems(t.Context())
	if err != nil || problems == nil || len(problems) != 0 {
		t.Errorf("Problems = %#v, %v; want an empty, non-nil slice", problems, err)
	}
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	svc, rec, path := newService(t, "")

	got, err := svc.Update(t.Context(), SettingsPatchDTO{UI: &UISettingsPatchDTO{SplitRatio: ptr(0.3), Theme: ptr("dark")}})
	if err != nil {
		t.Fatal(err)
	}
	want := toSettingsDTO(core.Defaults())
	want.UI.Theme, want.UI.SplitRatio = "dark", 0.3
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Update = %+v, want %+v", got, want)
	}
	if loaded, _, err := core.Load(path); err != nil || loaded.UI.Theme != "dark" || loaded.UI.SplitRatio != 0.3 {
		t.Errorf("file = %+v, %v", loaded, err)
	}
	events := rec.all()
	if len(events) != 1 || events[0].Source != SourceApp || !reflect.DeepEqual(events[0].Settings, want) || len(events[0].Problems) != 0 {
		t.Fatalf("events = %+v, want one app event with the new settings", events)
	}

	// Nothing to change: no event.
	for _, patch := range []SettingsPatchDTO{{}, {UI: &UISettingsPatchDTO{}}, {UI: &UISettingsPatchDTO{Theme: ptr("dark")}}} {
		if _, err := svc.Update(t.Context(), patch); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(rec.all()); n != 1 {
		t.Errorf("%d events after no-op updates, want 1", n)
	}
}

func TestUpdateLayoutAndEditor(t *testing.T) {
	t.Parallel()
	svc, _, path := newService(t, "")

	got, err := svc.Update(t.Context(), SettingsPatchDTO{
		UI: &UISettingsPatchDTO{ResponsePosition: ptr("below"), SplitRatioBelow: ptr(0.4)},
		Editor: &EditorSettingsPatchDTO{
			FontFamily: ptr("Menlo"), FontSize: ptr(15), TabSize: ptr(4),
			WordWrap: ptr(true), LineNumbers: ptr(false), MatchBrackets: ptr(false),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := toSettingsDTO(core.Defaults())
	want.UI.ResponsePosition, want.UI.SplitRatioBelow = "below", 0.4
	want.Editor = EditorSettingsDTO{FontFamily: "Menlo", FontSize: 15, TabSize: 4, WordWrap: true, LineNumbers: false, MatchBrackets: false}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Update = %+v, want %+v", got, want)
	}
	loaded, _, err := core.Load(path)
	if err != nil || loaded.UI.ResponsePosition != "below" || loaded.Editor.FontFamily != "Menlo" || loaded.Editor.TabSize != 4 || loaded.Editor.LineNumbers {
		t.Errorf("file = %+v, %v", loaded, err)
	}
}

func TestUpdateInvalid(t *testing.T) {
	t.Parallel()
	const content = `{"ui":{"theme":"light"}}`
	svc, rec, path := newService(t, content)

	tests := []struct {
		name  string
		patch SettingsPatchDTO
		field string
	}{
		{"unknown theme", SettingsPatchDTO{UI: &UISettingsPatchDTO{Theme: ptr("blue")}}, "ui.theme"},
		{"ratio out of range", SettingsPatchDTO{UI: &UISettingsPatchDTO{SplitRatio: ptr(0.9), SidebarCollapsed: ptr(true)}}, "ui.splitRatio"},
		{"unknown response position", SettingsPatchDTO{UI: &UISettingsPatchDTO{ResponsePosition: ptr("left")}}, "ui.responsePosition"},
		{"font size out of range", SettingsPatchDTO{Editor: &EditorSettingsPatchDTO{FontSize: ptr(40), WordWrap: ptr(true)}}, "editor.fontSize"},
		{"font family list", SettingsPatchDTO{Editor: &EditorSettingsPatchDTO{FontFamily: ptr("Menlo, monospace")}}, "editor.fontFamily"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Update(t.Context(), tt.patch)
			var e *errs.Error
			if !errors.As(err, &e) || errs.KindOf(err) != errs.InvalidArgument || e.Code != core.CodeInvalidValue || e.Hint == "" || e.Details["field"] != tt.field {
				t.Fatalf("err = %#v, want invalid_argument %s for %s with a hint", err, core.CodeInvalidValue, tt.field)
			}
			// What the UI receives.
			dto := apperr.ToDTO(err)
			if dto.Kind != "invalid_argument" || dto.Details["expected"] == "" {
				t.Errorf("ErrorDTO = %+v", dto)
			}
		})
	}
	if b, _ := os.ReadFile(path); string(b) != content {
		t.Errorf("an invalid update wrote the file: %s", b)
	}
	if got, _ := svc.Get(t.Context()); got.UI.SidebarCollapsed || got.UI.Theme != "light" {
		t.Errorf("an invalid update changed the settings: %+v", got)
	}
	if n := len(rec.all()); n != 0 {
		t.Errorf("%d events after invalid updates, want 0", n)
	}
}

func TestCancelled(t *testing.T) {
	t.Parallel()
	svc, _, path := newService(t, "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := svc.Get(ctx); !errors.Is(err, context.Canceled) || errs.KindOf(err) != errs.Cancelled {
		t.Errorf("Get: err = %v", err)
	}
	if _, err := svc.Problems(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Problems: err = %v", err)
	}
	if _, err := svc.Update(ctx, SettingsPatchDTO{UI: &UISettingsPatchDTO{SplitRatio: ptr(0.3)}}); !errors.Is(err, context.Canceled) {
		t.Errorf("Update: err = %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a cancelled Update wrote the file")
	}
}

func TestWatchPicksUpHandEdits(t *testing.T) {
	t.Parallel()
	svc, rec, path := newService(t, `{"ui":{"theme":"dark"}}`)
	svc.interval = 5 * time.Millisecond
	if err := svc.ServiceStartup(t.Context(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.ServiceShutdown() })

	// Our own write: one "app" event, and the watcher stays quiet.
	if _, err := svc.Update(t.Context(), SettingsPatchDTO{UI: &UISettingsPatchDTO{SplitRatio: ptr(0.3)}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if ev := rec.all(); len(ev) != 1 || ev[0].Source != SourceApp {
		t.Fatalf("events after an update = %+v, want one app event", ev)
	}

	// A hand edit, with an invalid value.
	saveAsEditor(t, path, `{"ui":{"theme":"light","splitRatio":7}}`)
	ev := rec.waitFor(t, 2)[1]
	if ev.Source != SourceFile || ev.Settings.UI.Theme != "light" || ev.Settings.UI.SplitRatio != 0.5 {
		t.Errorf("file event = %+v", ev)
	}
	if len(ev.Problems) != 1 || ev.Problems[0].Details["field"] != "ui.splitRatio" {
		t.Errorf("file event problems = %+v", ev.Problems)
	}
	if got, _ := svc.Get(t.Context()); got.UI.Theme != "light" {
		t.Errorf("Get after the hand edit = %+v", got)
	}

	// Shutdown stops the watcher: later edits are not reported.
	if err := svc.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	saveAsEditor(t, path, `{"ui":{"theme":"dark"}}`)
	time.Sleep(50 * time.Millisecond)
	if n := len(rec.all()); n != 2 {
		t.Errorf("%d events in all, want 2 (none after shutdown)", n)
	}
}

// saveAsEditor replaces the file the way most editors save: a new file
// renamed over the old one, so the watcher never sees it half-written.
// (A file caught empty is ignored; one caught half-written is reported until
// the next poll; see core Store.Reload.)
func saveAsEditor(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".editor-tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func TestWatchInMemoryIsANoOp(t *testing.T) {
	t.Parallel()
	svc := New(core.Open("", nil), nil, nil)
	svc.startWatching(time.Millisecond)
	if svc.stop != nil {
		t.Error("watching an in-memory store")
	}
	if _, err := svc.Update(t.Context(), SettingsPatchDTO{UI: &UISettingsPatchDTO{Theme: ptr("dark")}}); err != nil {
		t.Errorf("Update with a nil emitter: %v", err)
	}
	_ = svc.ServiceShutdown()
}

func TestPatchDTOFromJSON(t *testing.T) {
	t.Parallel()
	// What the UI sends: every field present, null where unchanged.
	var p SettingsPatchDTO
	if err := json.Unmarshal([]byte(`{"ui":{"theme":null,"sidebarCollapsed":true,"splitRatio":null}}`), &p); err != nil {
		t.Fatal(err)
	}
	c := p.toCore()
	if c.UI.Theme != nil || c.UI.SplitRatio != nil || c.UI.SidebarCollapsed == nil || !*c.UI.SidebarCollapsed {
		t.Errorf("toCore = %+v", c.UI)
	}
	var empty SettingsPatchDTO
	if err := json.Unmarshal([]byte(`{"ui":null}`), &empty); err != nil || empty.toCore() != (core.Patch{}) {
		t.Errorf("null section: %+v, %v", empty.toCore(), err)
	}
}

func TestDTOJSON(t *testing.T) {
	t.Parallel()
	_, r, err := core.Load(writeTemp(t, `{"ui":{"splitRatio":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		v    any
	}{
		{"settings_defaults", toSettingsDTO(core.Defaults())},
		{"changed_event", ChangedEventDTO{
			Settings: toSettingsDTO(core.Defaults()),
			Problems: toProblemDTOs(r.Problems),
			Source:   SourceFile,
		}},
		{"changed_event_no_problems", ChangedEventDTO{
			Settings: toSettingsDTO(core.Defaults()),
			Problems: toProblemDTOs(nil),
			Source:   SourceApp,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.MarshalIndent(tt.v, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, "dto/"+tt.name, append(got, '\n'))
		})
	}
}

func TestUpdateNetwork(t *testing.T) {
	t.Parallel()
	svc, rec, path := newService(t, "")

	got, err := svc.Update(t.Context(), SettingsPatchDTO{Network: &NetworkSettingsPatchDTO{DefaultTimeoutMs: ptr(30000)}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Network.DefaultTimeoutMs != 30000 {
		t.Errorf("Update = %+v", got)
	}
	if loaded, _, err := core.Load(path); err != nil || loaded.Network.DefaultTimeoutMs != 30000 {
		t.Errorf("file = %+v, %v", loaded, err)
	}

	// Invalid: an error for the field, and the file is not written.
	before, _ := os.ReadFile(path)
	_, err = svc.Update(t.Context(), SettingsPatchDTO{Network: &NetworkSettingsPatchDTO{DefaultTimeoutMs: ptr(-5000)}})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != core.CodeInvalidValue || e.Details["field"] != "network.defaultTimeoutMs" {
		t.Fatalf("err = %#v, want %s for network.defaultTimeoutMs", err, core.CodeInvalidValue)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("an invalid timeout was written: %s", after)
	}
	if n := len(rec.all()); n != 1 {
		t.Errorf("%d events, want 1 (none for the invalid update)", n)
	}
}

func TestLocation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), core.FileName)
	svc := New(core.Open(path, nil), nil, nil, WithDataDir("/data"))
	got, err := svc.Location(t.Context())
	if err != nil || got != (LocationDTO{Path: path, Exists: false, DataDir: "/data"}) {
		t.Errorf("Location = %+v, %v", got, err)
	}
}

func TestOpenFile(t *testing.T) {
	t.Parallel()
	var opened, revealed []string
	desktop := Desktop{
		Open:   func(p string) error { opened = append(opened, p); return nil },
		Reveal: func(p string) error { revealed = append(revealed, p); return nil },
	}
	path := filepath.Join(t.TempDir(), core.FileName)
	svc := New(core.Open(path, nil), nil, nil, WithDesktop(desktop))

	// A missing file is written first, so there is something to open.
	if err := svc.OpenFile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0] != path {
		t.Errorf("opened %q, want %q", opened, path)
	}
	if loaded, r, err := core.Load(path); err != nil || !r.Exists || !loaded.Equal(core.Defaults()) {
		t.Errorf("file = %+v, %+v, %v; want the defaults written", loaded, r, err)
	}
	if loc, _ := svc.Location(t.Context()); !loc.Exists {
		t.Error("Location.Exists = false after OpenFile")
	}

	if err := svc.RevealFile(t.Context()); err != nil || len(revealed) != 1 || revealed[0] != path {
		t.Errorf("RevealFile: %v, revealed %q", err, revealed)
	}
}

func TestOpenFileFailures(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), core.FileName)

	// The OS refuses.
	failing := New(core.Open(path, nil), nil, nil, WithDesktop(Desktop{
		Open: func(string) error { return errors.New("no application for .json") },
	}))
	err := failing.OpenFile(t.Context())
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeOpenFailed || e.Details["path"] != path || e.Hint == "" {
		t.Errorf("err = %#v, want %s with the path", err, CodeOpenFailed)
	}

	// No way to reach the OS, or no file at all.
	for name, svc := range map[string]*SettingsService{
		"no desktop": New(core.Open(path, nil), nil, nil),
		"in memory":  New(core.Open("", nil), nil, nil, WithDesktop(Desktop{Open: func(string) error { return nil }})),
	} {
		err := svc.OpenFile(t.Context())
		if !errors.As(err, &e) || errs.KindOf(err) != errs.Unsupported || e.Code != CodeOpenFailed {
			t.Errorf("%s: err = %#v, want unsupported %s", name, err, CodeOpenFailed)
		}
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), core.FileName)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUpdateShortcuts(t *testing.T) {
	t.Parallel()
	svc, rec, path := newService(t, "")

	overrides := map[string][]string{"request.send": {"Mod+Shift+Enter"}, "view.focusMode": {"F11"}}
	got, err := svc.Update(t.Context(), SettingsPatchDTO{Shortcuts: &overrides})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Shortcuts, overrides) {
		t.Errorf("Update = %v, want %v", got.Shortcuts, overrides)
	}
	// Saved in the file and read back by a new service (the next start).
	again := New(core.Open(path, nil), nil, nil)
	if back, err := again.Get(t.Context()); err != nil || !reflect.DeepEqual(back.Shortcuts, overrides) {
		t.Errorf("after restart: %v, %v; want %v", back.Shortcuts, err, overrides)
	}
	if events := rec.all(); len(events) != 1 || !reflect.DeepEqual(events[0].Settings.Shortcuts, overrides) {
		t.Errorf("events = %+v, want one with the overrides", events)
	}

	// A patch without shortcuts keeps them.
	if got, err := svc.Update(t.Context(), SettingsPatchDTO{UI: &UISettingsPatchDTO{Theme: ptr("light")}}); err != nil || !reflect.DeepEqual(got.Shortcuts, overrides) {
		t.Errorf("other patch: %v, %v", got.Shortcuts, err)
	}

	// An invalid chord changes nothing.
	bad := map[string][]string{"request.send": {"Hyper+K"}}
	var e *errs.Error
	if _, err := svc.Update(t.Context(), SettingsPatchDTO{Shortcuts: &bad}); !errors.As(err, &e) || e.Code != core.CodeInvalidValue {
		t.Errorf("invalid chord: err = %v", err)
	}

	// {} resets them all; the DTO then has an empty map, not null.
	none := map[string][]string{}
	got, err = svc.Update(t.Context(), SettingsPatchDTO{Shortcuts: &none})
	if err != nil || got.Shortcuts == nil || len(got.Shortcuts) != 0 {
		t.Errorf("reset = %#v, %v", got.Shortcuts, err)
	}
}
