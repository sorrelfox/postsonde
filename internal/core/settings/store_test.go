package settings

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func ratio(v float64) Patch   { return Patch{UI: UIPatch{SplitRatio: &v}} }
func theme(v string) Patch    { return Patch{UI: UIPatch{Theme: &v}} }
func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestStoreMissingFileIsCreatedByTheFirstChange(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), FileName)
	s := Open(path, nil)

	st := s.State()
	if !st.Settings.Equal(Defaults()) || st.Exists || st.ReadOnly || len(st.Problems) != 0 {
		t.Fatalf("state = %+v, want the defaults, no file, no problems", st)
	}
	if exists(path) {
		t.Fatal("Open created the file")
	}

	// A patch that changes nothing writes nothing.
	if _, changed, err := s.Update(ratio(0.5)); err != nil || changed {
		t.Fatalf("no-op Update: changed %v, err %v", changed, err)
	}
	if exists(path) {
		t.Fatal("a no-op Update created the file")
	}

	got, changed, err := s.Update(ratio(0.3))
	if err != nil || !changed || got.UI.SplitRatio != 0.3 {
		t.Fatalf("Update = %+v, %v, %v", got, changed, err)
	}
	loaded, _, err := Load(path)
	if err != nil || !loaded.Equal(got) {
		t.Errorf("file holds %+v (%v), want %+v", loaded, err, got)
	}
	if !s.State().Exists {
		t.Error("Exists = false after a save")
	}
}

func TestStoreCreate(t *testing.T) {
	t.Parallel()

	t.Run("missing file is written with the settings in effect", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), FileName)
		s := Open(path, nil)
		if err := s.Create(); err != nil {
			t.Fatal(err)
		}
		loaded, r, err := Load(path)
		if err != nil || !loaded.Equal(Defaults()) || len(r.Problems) != 0 {
			t.Fatalf("file = %+v, %+v, %v; want the defaults", loaded, r, err)
		}
		if !s.State().Exists {
			t.Error("Exists = false after Create")
		}
		// Its own write is not a change.
		if s.Reload() {
			t.Error("Reload after Create reported a change")
		}
	})

	t.Run("existing files are left alone", func(t *testing.T) {
		t.Parallel()
		for _, content := range []string{`{"ui":{"theme":"dark"}}`, `{broken`, `{"version":9}`} {
			path := writeSettings(t, content)
			s := Open(path, nil)
			if err := s.Create(); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, path); got != content {
				t.Errorf("Create changed %q to %q", content, got)
			}
		}
	})

	t.Run("in memory", func(t *testing.T) {
		t.Parallel()
		if err := Open("", nil).Create(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStoreInvalidPatchWritesNothing(t *testing.T) {
	t.Parallel()
	const content = `{"version":1,"ui":{"theme":"dark"}}`
	path := writeSettings(t, content)
	s := Open(path, nil)
	before := s.Settings()

	got, changed, err := s.Update(Patch{UI: UIPatch{Theme: new("light"), SplitRatio: new(0.95)}})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeInvalidValue || e.Details["field"] != "ui.splitRatio" {
		t.Fatalf("err = %v, want %s for ui.splitRatio", err, CodeInvalidValue)
	}
	if changed || !got.Equal(before) || !s.Settings().Equal(before) {
		t.Errorf("an invalid patch changed the settings: %+v", s.Settings())
	}
	if readFile(t, path) != content {
		t.Errorf("an invalid patch changed the file: %s", readFile(t, path))
	}
}

func TestStoreKeepsUnknownKeys(t *testing.T) {
	t.Parallel()
	path := writeSettings(t, `{"future":{"x":1},"ui":{"fontSize":14}}`)
	s := Open(path, nil)
	if _, _, err := s.Update(theme("light")); err != nil {
		t.Fatal(err)
	}
	want := `{
  "version": 1,
  "ui": {
    "theme": "light",
    "sidebarCollapsed": false,
    "splitRatio": 0.5,
    "responsePosition": "beside",
    "splitRatioBelow": 0.5,
    "locale": "system",
    "fontSize": 14
  },
  "network": {
    "defaultTimeoutMs": 0,
    "verifyTls": true,
    "maxResponseBodyMb": 512,
    "proxyMode": "system"
  },
  "editor": {
    "fontFamily": "JetBrains Mono",
    "fontSize": 12,
    "tabSize": 2,
    "wordWrap": false,
    "lineNumbers": true,
    "matchBrackets": true
  },
  "future": {
    "x": 1
  }
}
`
	if got := readFile(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestStoreInvalidValuesAreReportedThenReplaced(t *testing.T) {
	t.Parallel()
	path := writeSettings(t, `{"ui":{"theme":"neon","splitRatio":0.3}}`)
	s := Open(path, nil)
	st := s.State()
	if st.Settings.UI.Theme != ThemeSystem || st.Settings.UI.SplitRatio != 0.3 {
		t.Errorf("settings = %+v, want the default theme and ratio 0.3", st.Settings)
	}
	if len(st.Problems) != 1 || st.Problems[0].Details["field"] != "ui.theme" {
		t.Fatalf("problems = %v, want one for ui.theme", st.Problems)
	}
	if _, _, err := s.Update(ratio(0.4)); err != nil {
		t.Fatal(err)
	}
	if p := s.State().Problems; len(p) != 0 {
		t.Errorf("problems after a save = %v, want none", p)
	}
	if !strings.Contains(readFile(t, path), `"theme": "system"`) {
		t.Errorf("file = %s, want the invalid theme replaced by the default", readFile(t, path))
	}
}

func TestStoreBacksUpABrokenFile(t *testing.T) {
	t.Parallel()
	const broken = `{"ui": {"theme": "dark",`
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(path, nil)
	s.now = func() time.Time { return time.Date(2026, 9, 15, 14, 30, 0, 0, time.UTC) }

	st := s.State()
	if !st.Settings.Equal(Defaults()) || len(st.Problems) != 1 || st.Problems[0].Code != CodeParseFailed || st.ReadOnly {
		t.Fatalf("state = %+v, want the defaults and a parse problem", st)
	}
	if readFile(t, path) != broken {
		t.Fatal("Open changed the broken file")
	}

	if _, _, err := s.Update(ratio(0.6)); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "settings.json.bak-20260915T143000Z")
	if got := readFile(t, backup); got != broken {
		t.Errorf("backup holds %q, want the broken file", got)
	}
	if loaded, r, err := Load(path); err != nil || len(r.Problems) != 0 || loaded.UI.SplitRatio != 0.6 {
		t.Errorf("new file: %+v, %v, %v", loaded, r.Problems, err)
	}
	if p := s.State().Problems; len(p) != 0 {
		t.Errorf("problems after replacing the broken file = %v", p)
	}

	// The next save does not back up the (now valid) file again.
	if _, _, err := s.Update(ratio(0.7)); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("files = %v, want settings.json and one backup", entries)
	}
}

func TestStoreNewerVersionIsNotWritten(t *testing.T) {
	t.Parallel()
	const content = `{"version":2,"ui":{"theme":"light"},"sync":true}`
	path := writeSettings(t, content)
	s := Open(path, nil)

	st := s.State()
	if !st.ReadOnly || st.Settings.UI.Theme != ThemeLight || len(st.Problems) != 1 || st.Problems[0].Code != CodeNewerVersion {
		t.Fatalf("state = %+v, want read-only, the known fields and a newer-version warning", st)
	}
	got, changed, err := s.Update(ratio(0.3))
	if err != nil || !changed || got.UI.SplitRatio != 0.3 || s.Settings().UI.SplitRatio != 0.3 {
		t.Errorf("Update = %+v, %v, %v; want the change kept in memory", got, changed, err)
	}
	if readFile(t, path) != content {
		t.Errorf("the newer file was written: %s", readFile(t, path))
	}
	if p := s.State().Problems; len(p) != 1 {
		t.Errorf("the newer-version warning went away: %v", p)
	}
}

func TestStoreUnreadableFileIsNotWritten(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not stop reads on Windows")
	}
	path := writeSettings(t, `{"ui":{"theme":"dark"}}`)
	if err := os.Chmod(path, 0o200); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("running with permission to read anything (root?)")
	}
	s := Open(path, nil)
	st := s.State()
	if !st.ReadOnly || len(st.Problems) != 1 || st.Problems[0].Code != CodeReadFailed {
		t.Fatalf("state = %+v, want read-only with a read problem", st)
	}
	if _, _, err := s.Update(ratio(0.3)); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(path, 0o600)
	if got := readFile(t, path); got != `{"ui":{"theme":"dark"}}` {
		t.Errorf("the unreadable file was written: %s", got)
	}
}

func TestStoreReload(t *testing.T) {
	t.Parallel()
	path := writeSettings(t, `{"ui":{"theme":"dark"}}`)
	s := Open(path, nil)
	edit := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	steps := []struct {
		name        string
		do          func()
		wantChanged bool
		wantTheme   string
		wantRatio   float64
		wantCodes   []string
	}{
		{name: "unchanged file", do: func() {}, wantTheme: "dark", wantRatio: 0.5},
		{
			name: "own write is not a change",
			do: func() {
				if _, _, err := s.Update(ratio(0.3)); err != nil {
					t.Fatal(err)
				}
			},
			wantTheme: "dark", wantRatio: 0.3,
		},
		{name: "hand edit", do: func() { edit(`{"ui":{"theme":"light","splitRatio":0.3}}`) }, wantChanged: true, wantTheme: "light", wantRatio: 0.3},
		{name: "reformatting only", do: func() { edit("{\n\"ui\": {\"theme\": \"light\", \"splitRatio\": 0.3}}\n") }, wantTheme: "light", wantRatio: 0.3},
		{
			name:        "hand edit with an invalid value",
			do:          func() { edit(`{"ui":{"theme":"light","splitRatio":9}}`) },
			wantChanged: true, wantTheme: "light", wantRatio: 0.5, wantCodes: []string{CodeInvalidValue},
		},
		{
			name:        "half-written file keeps the settings",
			do:          func() { edit(`{"ui":{"theme":"da`) },
			wantChanged: true, wantTheme: "light", wantRatio: 0.5, wantCodes: []string{CodeParseFailed},
		},
		{name: "fixed file", do: func() { edit(`{"ui":{"theme":"dark","splitRatio":0.25}}`) }, wantChanged: true, wantTheme: "dark", wantRatio: 0.25},
		{name: "emptied file keeps the settings", do: func() { edit(" \n") }, wantTheme: "dark", wantRatio: 0.25},
		{name: "refilled with the same content", do: func() { edit(`{"ui":{"theme":"dark","splitRatio":0.25}}`) }, wantTheme: "dark", wantRatio: 0.25},
		{name: "deleted file resets to the defaults", do: func() { _ = os.Remove(path) }, wantChanged: true, wantTheme: "system", wantRatio: 0.5},
		{name: "still deleted", do: func() {}, wantTheme: "system", wantRatio: 0.5},
		{name: "created again", do: func() { edit(`{"ui":{"sidebarCollapsed":false}}`) }, wantTheme: "system", wantRatio: 0.5},
	}
	for _, st := range steps {
		st.do()
		changed := s.Reload()
		state := s.State()
		if changed != st.wantChanged {
			t.Errorf("%s: Reload = %v, want %v", st.name, changed, st.wantChanged)
		}
		if state.Settings.UI.Theme != st.wantTheme || state.Settings.UI.SplitRatio != st.wantRatio {
			t.Errorf("%s: settings = %+v, want theme %s, ratio %v", st.name, state.Settings.UI, st.wantTheme, st.wantRatio)
		}
		if codes := codesOf(state.Problems); strings.Join(codes, ",") != strings.Join(st.wantCodes, ",") {
			t.Errorf("%s: problems = %q, want %q", st.name, codes, st.wantCodes)
		}
	}
}

func TestStoreInMemory(t *testing.T) {
	t.Parallel()
	s := Open("", nil)
	if s.Path() != "" || s.Reload() {
		t.Fatal("an in-memory store has a path or reloads")
	}
	if got, changed, err := s.Update(theme("dark")); err != nil || !changed || got.UI.Theme != "dark" {
		t.Errorf("Update = %+v, %v, %v", got, changed, err)
	}
	if st := s.State(); st.Exists || st.ReadOnly || st.Settings.UI.Theme != "dark" {
		t.Errorf("state = %+v", st)
	}
}

func TestStoreSaveFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "gone", FileName)
	s := Open(path, nil)
	_, changed, err := s.Update(ratio(0.3))
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeSaveFailed || changed {
		t.Fatalf("Update = %v, %v; want %s", changed, err, CodeSaveFailed)
	}
	if !s.Settings().Equal(Defaults()) {
		t.Errorf("a failed save changed the settings to %+v", s.Settings())
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat: %v", err)
	}
}

// TestStoreConcurrent is for the race detector.
func TestStoreConcurrent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), FileName)
	s := Open(path, nil)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 20 {
				_, _, _ = s.Update(ratio(0.2 + float64((i+j)%7)/10))
				s.Reload()
				_ = s.State()
			}
		})
	}
	wg.Wait()
	if _, r, err := Load(path); err != nil || len(r.Problems) != 0 {
		t.Errorf("file after concurrent updates: %v, %v", r.Problems, err)
	}
}
