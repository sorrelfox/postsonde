package workspace

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// TestUIStatePerWorkspace: each workspace keeps its own UI state in its
// local directory, the one being left can still be saved after another
// opened, and the project folders get nothing (STO-06).
func TestUIStatePerWorkspace(t *testing.T) {
	t.Parallel()
	dir := tempDir(t)
	m := New(tempDir(t))
	a, err := m.Create(filepath.Join(dir, "a"), "A")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SaveUIState(a.ID(), a.Root, "tabs", json.RawMessage(`{"order":["r1"]}`)); err != nil {
		t.Fatal(err)
	}
	b, err := m.Create(filepath.Join(dir, "b"), "B")
	if err != nil {
		t.Fatal(err)
	}
	// A is no longer open but was opened in this session: its state is still saved.
	if err := m.SaveUIState(a.ID(), a.Root, "tree.expanded", json.RawMessage(`["f1"]`)); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveUIState(b.ID(), b.Root, "tabs", json.RawMessage(`{"order":["r2"]}`)); err != nil {
		t.Fatal(err)
	}

	got := ReadUIState(a.LocalDir)
	if string(got["tabs"]) != `{"order":["r1"]}` || string(got["tree.expanded"]) != `["f1"]` || len(got) != 2 {
		t.Errorf("A = %s", got)
	}
	if got := ReadUIState(b.LocalDir); string(got["tabs"]) != `{"order":["r2"]}` || len(got) != 1 {
		t.Errorf("B = %s", got)
	}
	for _, root := range []string{a.Root, b.Root} {
		if files := listTree(t, root); !slices.Equal(files, []string{"postsonde.json"}) {
			t.Errorf("project %s holds %v", root, files)
		}
	}

	// null removes a key.
	if err := m.SaveUIState(a.ID(), a.Root, "tabs", json.RawMessage(`null`)); err != nil {
		t.Fatal(err)
	}
	if got := ReadUIState(a.LocalDir); len(got) != 1 || got["tabs"] != nil {
		t.Errorf("after null = %s", got)
	}

	// A broken file counts as empty and is replaced.
	writeFile(t, filepath.Join(a.LocalDir, UIStateFileName), "{not json")
	if got := ReadUIState(a.LocalDir); len(got) != 0 {
		t.Errorf("broken = %s", got)
	}
	if err := m.SaveUIState(a.ID(), a.Root, "tabs", json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	if got := ReadUIState(a.LocalDir); string(got["tabs"]) != `[]` {
		t.Errorf("after rewrite = %s", got)
	}
	if got := ReadUIState(""); len(got) != 0 {
		t.Errorf("no dir = %s", got)
	}
}

func TestSaveUIStateErrors(t *testing.T) {
	t.Parallel()
	m := New(tempDir(t))
	w, err := m.Create(filepath.Join(tempDir(t), "p"), "")
	if err != nil {
		t.Fatal(err)
	}
	checkErr(t, m.SaveUIState(w.ID(), "/other", "tabs", json.RawMessage(`1`)), errs.NotFound, CodeNoWorkspace)
	checkErr(t, m.SaveUIState("ws_other", w.Root, "tabs", json.RawMessage(`1`)), errs.NotFound, CodeNoWorkspace)
	for _, key := range []string{"", "Tabs", "../x", "a/b", strings.Repeat("a", 65)} {
		checkErr(t, m.SaveUIState(w.ID(), w.Root, key, json.RawMessage(`1`)), errs.InvalidArgument, CodeInvalidUIState)
	}
	checkErr(t, m.SaveUIState(w.ID(), w.Root, "tabs", json.RawMessage(`{`)), errs.InvalidArgument, CodeInvalidUIState)
	big := json.RawMessage(`"` + strings.Repeat("x", MaxUIStateValue) + `"`)
	checkErr(t, m.SaveUIState(w.ID(), w.Root, "tabs", big), errs.InvalidArgument, CodeInvalidUIState)

	for i := range MaxUIStateKeys {
		if err := m.SaveUIState(w.ID(), w.Root, "k"+strings.Repeat("x", i), json.RawMessage(`1`)); err != nil {
			t.Fatal(err)
		}
	}
	checkErr(t, m.SaveUIState(w.ID(), w.Root, "one-more", json.RawMessage(`1`)), errs.InvalidArgument, CodeInvalidUIState)
	// Replacing an existing key is still allowed.
	if err := m.SaveUIState(w.ID(), w.Root, "k", json.RawMessage(`2`)); err != nil {
		t.Fatal(err)
	}
	checkErr(t, New("").SaveUIState(w.ID(), w.Root, "tabs", json.RawMessage(`1`)), errs.Internal, CodeNoDataDir)
}

func TestClearRecentKeepsTheOpenWorkspace(t *testing.T) {
	t.Parallel()
	dir := tempDir(t)
	m := New(tempDir(t))
	a, err := m.Create(filepath.Join(dir, "a"), "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Create(filepath.Join(dir, "b"), "")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Known(a.Root) || !m.Known(b.Root) || m.Known(filepath.Join(dir, "c")) {
		t.Error("Known before clearing")
	}
	if err := m.ClearRecent(); err != nil {
		t.Fatal(err)
	}
	recent, err := m.Recent()
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].Path != b.Root {
		t.Errorf("recent = %+v, want only the open one", recent)
	}
	if m.Known(a.Root) {
		t.Error("a cleared folder is still known")
	}
	if last, err := New(m.dataDir).ReopenLast(); err != nil || last == nil || last.Root != b.Root {
		t.Errorf("ReopenLast = %v, %v", last, err)
	}

	// With nothing open, everything goes and nothing is reopened.
	if _, err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.ClearRecent(); err != nil {
		t.Fatal(err)
	}
	if recent, _ := m.Recent(); len(recent) != 0 {
		t.Errorf("recent = %+v", recent)
	}
}
