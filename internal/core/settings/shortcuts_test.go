package settings

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestValidChord(t *testing.T) {
	t.Parallel()
	for _, c := range []string{"Mod+Enter", "Mod+Shift+F", "?", "Mod+/", "F6", "Ctrl+Space", "Alt+Shift+ArrowLeft", "Mod+,", "Delete"} {
		if !ValidChord(c) {
			t.Errorf("ValidChord(%q) = false", c)
		}
	}
	for _, c := range []string{"", "Mod+", "Mod+Mod+K", "Meta+K", "mod+K", "Mod+Shift", "Mod++", "Mod+é", "Mod+enter", "Mod+ ", "Mod+ArrowLeftArrowLeftArrow"} {
		if ValidChord(c) {
			t.Errorf("ValidChord(%q) = true", c)
		}
	}
}

func TestApplyShortcuts(t *testing.T) {
	t.Parallel()
	overrides := map[string][]string{"request.send": {"Mod+Shift+Enter"}, "palette.open": {"Mod+K", "F1"}}
	s, err := Defaults().Apply(Patch{Shortcuts: &overrides})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Shortcuts, overrides) {
		t.Fatalf("Shortcuts = %v, want %v", s.Shortcuts, overrides)
	}
	// The settings keep their own copy.
	overrides["request.send"][0] = "Mod+J"
	if s.Shortcuts["request.send"][0] != "Mod+Shift+Enter" {
		t.Error("Apply kept the caller's slice")
	}

	empty := map[string][]string{}
	cleared, err := s.Apply(Patch{Shortcuts: &empty})
	if err != nil || cleared.Shortcuts != nil || !cleared.Equal(Defaults()) {
		t.Fatalf("clearing = %v, %v; want the defaults", cleared.Shortcuts, err)
	}

	for name, bad := range map[string]map[string][]string{
		"bad id":         {"Request Send": {"Mod+K"}},
		"no chords":      {"request.send": {}},
		"bad chord":      {"request.send": {"Cmd+K"}},
		"duplicate":      {"request.send": {"Mod+K", "Mod+K"}},
		"too many keys":  {"request.send": {"F1", "F2", "F3", "F4", "F5"}},
		"id without dot": {"send": {"Mod+K"}},
	} {
		got, err := s.Apply(Patch{Shortcuts: &bad})
		if err == nil || !strings.Contains(err.Error(), "shortcuts") {
			t.Errorf("%s: err = %v, want an invalid shortcuts error", name, err)
		}
		if !got.Equal(s) {
			t.Errorf("%s: the settings changed", name)
		}
	}
}

func TestLoadShortcuts(t *testing.T) {
	t.Parallel()
	s, r, err := Load(writeSettings(t, `{"shortcuts": {
		"request.send": ["Mod+Shift+Enter"],
		"view.focusMode": ["Mod+Alt+Z", "F11"],
		"tab.close": "Mod+W",
		"sidebar.toggle": ["Hyper+B"]
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"request.send": {"Mod+Shift+Enter"}, "view.focusMode": {"Mod+Alt+Z", "F11"}}
	if !reflect.DeepEqual(s.Shortcuts, want) {
		t.Errorf("Shortcuts = %v, want %v", s.Shortcuts, want)
	}
	got := problemsOf(t, r.Problems)
	if len(got) != 2 || got[0].field != "shortcuts.sidebar.toggle" || got[1].field != "shortcuts.tab.close" {
		t.Errorf("problems = %+v, want one per invalid entry", got)
	}

	s, r, err = Load(writeSettings(t, `{"shortcuts": ["Mod+K"]}`))
	if err != nil || s.Shortcuts != nil || len(problemsOf(t, r.Problems)) != 1 {
		t.Errorf("a list = %v, %+v, %v; want no overrides and one problem", s.Shortcuts, r.Problems, err)
	}
}

func TestShortcutsRoundTrip(t *testing.T) {
	t.Parallel()
	overrides := map[string][]string{"sidebar.toggle": {"Mod+\\"}, "request.send": {"Mod+Shift+Enter"}}
	s, err := Defaults().Apply(Patch{Shortcuts: &overrides})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, s, nil); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, path)
	if !strings.Contains(text, `"shortcuts": {`) || strings.Index(text, `"request.send"`) > strings.Index(text, `"sidebar.toggle"`) {
		t.Errorf("file does not list the shortcuts sorted by id:\n%s", text)
	}
	back, r, err := Load(path)
	if err != nil || len(r.Problems) != 0 || !back.Equal(s) {
		t.Fatalf("reload = %v, %v, %v; want %v", back.Shortcuts, r.Problems, err, s.Shortcuts)
	}

	// Without overrides the section is left out.
	if err := Save(path, Defaults(), nil); err != nil {
		t.Fatal(err)
	}
	if text := readFile(t, path); strings.Contains(text, "shortcuts") {
		t.Errorf("defaults wrote a shortcuts section:\n%s", text)
	}
}

func TestStoreUpdateShortcuts(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), FileName)
	store := Open(path, nil)
	overrides := map[string][]string{"palette.open": {"Mod+K"}}
	if _, changed, err := store.Update(Patch{Shortcuts: &overrides}); err != nil || !changed {
		t.Fatalf("Update = %v, %v", changed, err)
	}
	if _, changed, err := store.Update(Patch{Shortcuts: &overrides}); err != nil || changed {
		t.Errorf("the same overrides again: changed = %v, err = %v", changed, err)
	}
	reopened := Open(path, nil)
	if got := reopened.Settings().Shortcuts; !reflect.DeepEqual(got, overrides) {
		t.Errorf("after reopening: %v, want %v", got, overrides)
	}
}
