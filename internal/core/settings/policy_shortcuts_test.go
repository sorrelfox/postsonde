package settings

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The shortcuts section is a top-level map: it can be locked as a
// whole ("shortcuts": {} keeps the default shortcuts) or one entry at a
// time ("shortcuts.request.send").
func TestStorePolicyLocksShortcuts(t *testing.T) {
	t.Parallel()
	whole := LoadPolicy(writePolicy(t, `{"locked": {"shortcuts": {}, "shortcuts.bad id": ["Mod+K"], "version": 2}}`))
	if got := whole.LockedKeys(); !slices.Equal(got, []string{"shortcuts"}) {
		t.Fatalf("locked = %v, problems %v", got, whole.Problems)
	}
	if got := problemFields(whole); !slices.Equal(got, []string{"shortcuts.bad id", "version"}) {
		t.Errorf("problems = %v", got)
	}
	if !whole.IsLocked("shortcuts.request.send") {
		t.Error("an entry of a locked map is not locked")
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(`{"shortcuts": {"request.send": ["Mod+Shift+Enter"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(path, nil, WithPolicy(whole))
	if got := s.Settings().Shortcuts; len(got) != 0 {
		t.Errorf("shortcuts = %v, want none", got)
	}
	mine := map[string][]string{"tab.close": {"Mod+W"}}
	if _, _, err := s.Update(Patch{Shortcuts: &mine}); err == nil {
		t.Error("a custom shortcut was saved under a locked map")
	}

	one := LoadPolicy(writePolicy(t, `{"locked": {"shortcuts.request.send": ["Mod+Enter"]}}`))
	if len(one.Problems) != 0 || !one.IsLocked("shortcuts.request.send") || one.IsLocked("shortcuts") {
		t.Fatalf("policy = %+v", one)
	}
	s = Open(path, nil, WithPolicy(one))
	if got := s.Settings().Shortcuts["request.send"]; !slices.Equal(got, []string{"Mod+Enter"}) {
		t.Errorf("request.send = %v, want the locked chord", got)
	}
	next := map[string][]string{"request.send": {"Mod+Enter"}, "tab.close": {"Mod+W"}}
	got, changed, err := s.Update(Patch{Shortcuts: &next})
	if err != nil || !changed || !slices.Equal(got.Shortcuts["tab.close"], []string{"Mod+W"}) {
		t.Fatalf("Update = %v, %v, %v", got.Shortcuts, changed, err)
	}
	loaded, _, _ := Load(path)
	if !slices.Equal(loaded.Shortcuts["request.send"], []string{"Mod+Shift+Enter"}) {
		t.Errorf("file shortcuts = %v, want the user's own chord kept", loaded.Shortcuts)
	}
	reset := map[string][]string{}
	if _, _, err := s.Update(Patch{Shortcuts: &reset}); err == nil {
		t.Error("resetting all shortcuts dropped the locked one")
	}
}
