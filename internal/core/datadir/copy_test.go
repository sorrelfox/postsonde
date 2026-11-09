package datadir

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// seed fills a data directory with PostSonde data and some files that are
// not PostSonde data, and returns the data files by relative path.
func seed(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{
		"workspaces/recent.json":                          `{"version":1}`,
		"workspaces/ws_1/local.json":                      `{"path":"/p"}`,
		"workspaces/ws_1/cookies.json":                    `[{"name":"sid"}]`,
		"workspaces/ws_1/secrets/environments/env_1.json": `{"token":"s3cret"}`,
		"workspaces/ws_1/ui-state.json":                   `{"entries":{}}`,
		"workspaces/ws_1/graphql-schemas/a.json":          strings.Repeat("x", 300_000),
	}
	for rel, content := range files {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(rel)), content)
	}
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "ws_1", "scratch"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Not PostSonde data: stays behind.
	writeFile(t, filepath.Join(dir, "settings.json"), "{}")
	writeFile(t, filepath.Join(dir, "tmp", "response-bodies", "x.part"), "spool")
	return files
}

// tree lists the files under dir (relative, slash-separated) with their
// content, and folders with a trailing slash.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		b, err := os.ReadFile(path)
		out[rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCopyData(t *testing.T) {
	base := t.TempDir()
	from, to := filepath.Join(base, "old"), filepath.Join(base, "new", "PostSonde")
	files := seed(t, from)
	before := tree(t, from)

	entries, err := CopyData(from, to)
	if err != nil {
		t.Fatalf("CopyData: %v", err)
	}
	if !slices.Equal(entries, []string{"workspaces"}) {
		t.Errorf("entries = %v", entries)
	}
	got := tree(t, to)
	for rel, content := range files {
		if got[rel] != content {
			t.Errorf("%s: copied %d bytes, want %d", rel, len(got[rel]), len(content))
		}
	}
	if _, ok := got["workspaces/ws_1/scratch/"]; !ok {
		t.Error("empty folder not copied")
	}
	for _, rel := range []string{"settings.json", "tmp/", moveMarker} {
		if _, ok := got[rel]; ok {
			t.Errorf("%s should not be in the new folder", rel)
		}
	}
	if after := tree(t, from); len(after) != len(before) {
		t.Error("the old folder changed")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(to, "workspaces", "ws_1", "secrets", "environments", "env_1.json"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("secret copied with %v, want 0600", fi.Mode().Perm())
		}
	}
}

func TestCopyDataRefusesExistingData(t *testing.T) {
	base := t.TempDir()
	from, to := filepath.Join(base, "old"), filepath.Join(base, "new")
	seed(t, from)
	writeFile(t, filepath.Join(to, "workspaces", "recent.json"), "mine")
	if _, err := CopyData(from, to); err == nil {
		t.Fatal("want error for a folder holding PostSonde data")
	}
	if b, _ := os.ReadFile(filepath.Join(to, "workspaces", "recent.json")); string(b) != "mine" {
		t.Error("existing data was touched")
	}
}

func TestCopyDataRefusesOverlap(t *testing.T) {
	from := t.TempDir()
	seed(t, from)
	if _, err := CopyData(from, filepath.Join(from, "inner")); err == nil {
		t.Error("want error for a target inside the source")
	}
}

func TestCopyDataResumesInterruptedAttempt(t *testing.T) {
	base := t.TempDir()
	from, to := filepath.Join(base, "old"), filepath.Join(base, "new")
	files := seed(t, from)
	// An earlier attempt died half-way: its marker and a partial copy.
	writeFile(t, filepath.Join(to, moveMarker), "")
	writeFile(t, filepath.Join(to, "workspaces", "recent.json"), "partial")

	if _, err := CopyData(from, to); err != nil {
		t.Fatalf("CopyData: %v", err)
	}
	if got := tree(t, to); got["workspaces/recent.json"] != files["workspaces/recent.json"] {
		t.Errorf("recent.json = %q", got["workspaces/recent.json"])
	}
}

func TestCopyDataCleansUpOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not make files unreadable on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	base := t.TempDir()
	from, to := filepath.Join(base, "old"), filepath.Join(base, "new")
	seed(t, from)
	unreadable := filepath.Join(from, "workspaces", "ws_1", "cookies.json")
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })

	if _, err := CopyData(from, to); err == nil {
		t.Fatal("want error")
	}
	if _, err := os.Stat(to); !os.IsNotExist(err) {
		t.Errorf("the new folder should be removed again, stat: %v (contents %v)", err, tree(t, to))
	}
	if _, err := os.Stat(filepath.Join(from, "workspaces", "recent.json")); err != nil {
		t.Errorf("the old data must stay: %v", err)
	}
}

func TestVerifyTreeFindsDifferences(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	writeFile(t, filepath.Join(a, "f"), "hello")
	writeFile(t, filepath.Join(b, "f"), "hellp")
	if err := verifyTree(a, b); err == nil {
		t.Error("same size, different content: want error")
	}
	writeFile(t, filepath.Join(b, "f"), "hello")
	writeFile(t, filepath.Join(a, "g"), "missing in b")
	if err := verifyTree(a, b); err == nil {
		t.Error("missing file: want error")
	}
}

func TestRemoveEntriesRefusesPaths(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "keep", "f"), "x")
	for _, name := range []string{"..", ".", "", "keep/f", filepath.Join("..", "x")} {
		if err := removeEntries(dir, []string{name}); err == nil {
			t.Errorf("removeEntries(%q): want error", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "keep", "f")); err != nil {
		t.Error("file removed")
	}
}
