package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/appdirs"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// tempDir returns a new temporary directory with symlinks resolved (on
// macOS t.TempDir is under the /var symlink), so paths compare equal to
// Workspace.Root.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// listTree returns every file and directory below root, as slash paths
// relative to it, sorted.
func listTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			rel, _ := filepath.Rel(root, path)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// snapshot maps each entry below root to its size, mode and modification
// time, to detect any write.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, rel := range listTree(t, root) {
		fi, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		out[rel] = fmt.Sprintf("%d %v %d", fi.Size(), fi.Mode(), fi.ModTime().UnixNano())
	}
	return out
}

func checkErr(t *testing.T, err error, kind errs.Kind, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want kind %s code %s", kind, code)
	}
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v (%T), want an *errs.Error", err, err)
	}
	if got := errs.KindOf(err); got != kind {
		t.Errorf("kind = %s, want %s (err: %v)", got, kind, err)
	}
	if e.Code != code {
		t.Errorf("code = %q, want %q (err: %v)", e.Code, code, err)
	}
	if e.Message == "" {
		t.Error("empty message")
	}
}

// TestLocalOnlyDataStaysOutOfProject (STO-01, STO-04): with the app
// directories under POSTSONDE_HOME, creating and opening a project and
// writing local side files leaves nothing but postsonde.json in the project
// folder, and opening an existing project writes nothing into it.
func TestLocalOnlyDataStaysOutOfProject(t *testing.T) {
	home := tempDir(t)
	t.Setenv(appdirs.EnvHome, filepath.Join(home, "postsonde-home"))
	dirs, err := appdirs.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if err := dirs.Ensure(); err != nil {
		t.Fatal(err)
	}

	project := filepath.Join(tempDir(t), "shop-api")
	m := New(dirs.Data)

	w, err := m.Create(project, "Shop API")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(w.LocalDir, dirs.Data+string(filepath.Separator)) {
		t.Errorf("LocalDir = %s, want it under the data directory %s", w.LocalDir, dirs.Data)
	}
	if insideProject(project, w.LocalDir) {
		t.Errorf("LocalDir %s is inside the project %s", w.LocalDir, project)
	}

	// What later features keep there: secrets, the active environment,
	// history, caches, scratch requests.
	local, err := m.LocalDir()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(local, "secrets.json"), `{"token":"s3cr3t"}`)
	writeFile(t, filepath.Join(local, "state.json"), `{"activeEnvironment":"env_1"}`)
	writeFile(t, filepath.Join(local, "history", "2026-09-16.jsonl"), "{}\n")
	writeFile(t, filepath.Join(local, "scratch", "req_1.json"), "{}")

	if got, want := listTree(t, project), []string{projectfile.WorkspaceFileName}; !slices.Equal(got, want) {
		t.Fatalf("project folder after Create = %v, want only %v", got, want)
	}

	// Close and reopen, as on the next app start, then open it again.
	before := snapshot(t, project)
	if _, err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Open(project); err != nil {
		t.Fatal(err)
	}
	m2 := New(dirs.Data)
	w2, err := m2.ReopenLast()
	if err != nil || w2 == nil {
		t.Fatalf("ReopenLast = %v, %v", w2, err)
	}
	if w2.LocalDir != w.LocalDir || w2.LocalStatus != LocalExisting {
		t.Errorf("reopened LocalDir = %s (%s), want %s (existing)", w2.LocalDir, w2.LocalStatus, w.LocalDir)
	}
	if _, err := m2.Recent(); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(t, project); !mapsEqual(got, before) {
		t.Errorf("opening changed the project folder:\nbefore %v\nafter  %v", before, got)
	}
	if got, want := listTree(t, project), []string{projectfile.WorkspaceFileName}; !slices.Equal(got, want) {
		t.Errorf("project folder = %v, want only %v", got, want)
	}

	// The secrets are still in the local directory.
	if data, err := os.ReadFile(filepath.Join(w2.LocalDir, "secrets.json")); err != nil || !strings.Contains(string(data), "s3cr3t") {
		t.Errorf("secrets.json = %q, %v", data, err)
	}
	// Nothing of the local data leaked into the marker.
	marker, err := os.ReadFile(filepath.Join(project, projectfile.WorkspaceFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"s3cr3t", dirs.Data, "history"} {
		if strings.Contains(string(marker), s) {
			t.Errorf("postsonde.json contains %q:\n%s", s, marker)
		}
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestOpenExistingProjectWritesNothing(t *testing.T) {
	t.Parallel()
	project := tempDir(t)
	writeFile(t, filepath.Join(project, projectfile.WorkspaceFileName), `{"schemaVersion":1,"kind":"workspace","id":"ws_00000000000000aa","name":"Acme"}`)
	writeFile(t, filepath.Join(project, "users", "_folder.json"), `{"schemaVersion":1,"kind":"folder","id":"fld_1","name":"Users"}`)
	before := snapshot(t, project)

	m := New(tempDir(t))
	w, err := m.Open(project)
	if err != nil {
		t.Fatal(err)
	}
	if w.ID() != "ws_00000000000000aa" || w.Name() != "Acme" || w.Root != project || w.LocalStatus != LocalNew || w.Newer() {
		t.Errorf("workspace = %+v", w)
	}
	if m.Current() != w {
		t.Error("Current is not the opened workspace")
	}
	if got := snapshot(t, project); !mapsEqual(got, before) {
		t.Errorf("Open changed the project folder:\nbefore %v\nafter  %v", before, got)
	}
}

func TestCreate(t *testing.T) {
	t.Parallel()
	parent := tempDir(t)
	m := New(tempDir(t))

	tests := []struct {
		name     string
		dir      string
		setup    func(dir string)
		input    string
		wantName string
	}{
		{"missing folder is created", filepath.Join(parent, "new", "api"), nil, "  My API ", "My API"},
		{"empty folder", filepath.Join(parent, "empty"), func(d string) { _ = os.Mkdir(d, 0o755) }, "", "empty"},
		{"folder with files", filepath.Join(parent, "files"), func(d string) { writeFile(t, filepath.Join(d, "README.md"), "hi") }, "Files", "Files"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup(tt.dir)
			}
			w, err := m.Create(tt.dir, tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if w.Name() != tt.wantName || w.File.Name != tt.wantName {
				t.Errorf("name = %q, want %q", w.File.Name, tt.wantName)
			}
			if !strings.HasPrefix(w.ID(), projectfile.WorkspaceIDPrefix) || LocalKey(w.ID()) != w.ID() {
				t.Errorf("ID = %q, want a generated ws_ ID", w.ID())
			}
			if w.LocalStatus != LocalNew || !isDir(w.LocalDir) {
				t.Errorf("local = %s %s", w.LocalDir, w.LocalStatus)
			}
			data, err := os.ReadFile(filepath.Join(tt.dir, projectfile.WorkspaceFileName))
			if err != nil {
				t.Fatal(err)
			}
			f, err := projectfile.DecodeWorkspace(data)
			if err != nil || f.ID != w.ID() || f.Name != tt.wantName || f.SchemaVersion != projectfile.CurrentVersion {
				t.Errorf("marker = %+v, %v", f, err)
			}
		})
	}
}

func TestCreateErrors(t *testing.T) {
	t.Parallel()
	dir := tempDir(t)
	existing := filepath.Join(dir, "existing")
	marker := filepath.Join(existing, projectfile.WorkspaceFileName)
	const original = `{"schemaVersion":1,"kind":"workspace","id":"ws_1","name":"Keep me"}`
	writeFile(t, marker, original)
	file := filepath.Join(dir, "file.txt")
	writeFile(t, file, "x")

	tests := []struct {
		name string
		path string
		kind errs.Kind
		code string
	}{
		{"existing marker", existing, errs.AlreadyExists, CodeAlreadyAProject},
		{"a file", file, errs.NotFound, CodeFolderNotFound},
		{"relative", "relative/dir", errs.InvalidArgument, CodeInvalidPath},
		{"empty", "", errs.InvalidArgument, CodeInvalidPath},
	}
	m := New(tempDir(t))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, err := m.Create(tt.path, "New")
			if w != nil {
				t.Errorf("workspace = %+v, want nil", w)
			}
			checkErr(t, err, tt.kind, tt.code)
		})
	}
	if data, _ := os.ReadFile(marker); string(data) != original {
		t.Errorf("existing marker was overwritten: %s", data)
	}
	if m.Current() != nil {
		t.Error("a failed Create opened a workspace")
	}
}

func TestOpenErrors(t *testing.T) {
	t.Parallel()
	dir := tempDir(t)
	project := func(name, marker string) string {
		p := filepath.Join(dir, name)
		writeFile(t, filepath.Join(p, projectfile.WorkspaceFileName), marker)
		return p
	}
	noMarker := filepath.Join(dir, "plain")
	writeFile(t, filepath.Join(noMarker, "notes.txt"), "x")
	markerDir := filepath.Join(dir, "marker-dir", projectfile.WorkspaceFileName)
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		kind errs.Kind
		code string
	}{
		{"missing folder", filepath.Join(dir, "missing"), errs.NotFound, CodeFolderNotFound},
		{"relative path", "some/dir", errs.InvalidArgument, CodeInvalidPath},
		{"no marker", noMarker, errs.NotFound, CodeNotAProject},
		{"marker is a directory", filepath.Dir(markerDir), errs.NotFound, CodeNotAProject},
		{"broken json", project("broken", `{"schemaVersion":1,`), errs.Parse, projectfile.CodeParseFailed},
		{"wrong kind", project("folder", `{"schemaVersion":1,"kind":"folder","id":"fld_1","name":"x"}`), errs.Parse, projectfile.CodeWrongKind},
		{"unknown kind", project("unknown", `{"schemaVersion":7,"kind":"galaxy","id":"x"}`), errs.Parse, projectfile.CodeWrongKind},
		{"missing id", project("noid", `{"schemaVersion":1,"kind":"workspace","name":"x"}`), errs.Parse, CodeMissingID},
	}
	m := New(tempDir(t))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, err := m.Open(tt.path)
			if w != nil {
				t.Errorf("workspace = %+v, want nil", w)
			}
			checkErr(t, err, tt.kind, tt.code)
		})
	}
	if m.Current() != nil {
		t.Error("a failed Open opened a workspace")
	}
}

func TestOpenAcceptsMarkerPath(t *testing.T) {
	t.Parallel()
	project := tempDir(t)
	marker := filepath.Join(project, projectfile.WorkspaceFileName)
	writeFile(t, marker, `{"schemaVersion":1,"kind":"workspace","id":"ws_1","name":"A"}`)
	w, err := New(tempDir(t)).Open(marker)
	if err != nil {
		t.Fatal(err)
	}
	if w.Root != project {
		t.Errorf("Root = %s, want %s", w.Root, project)
	}
}

func TestOpenNewerVersion(t *testing.T) {
	t.Parallel()
	project := tempDir(t)
	const content = `{"schemaVersion":99,"kind":"workspace","id":"ws_1","name":"Future","hologram":true}`
	writeFile(t, filepath.Join(project, projectfile.WorkspaceFileName), content)
	w, err := New(tempDir(t)).Open(project)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Newer() || w.Name() != "Future" {
		t.Errorf("Newer = %v, Name = %q", w.Newer(), w.Name())
	}
	if data, _ := os.ReadFile(w.MarkerPath()); string(data) != content {
		t.Errorf("marker changed: %s", data)
	}
}

func TestNameFallsBackToFolder(t *testing.T) {
	t.Parallel()
	project := filepath.Join(tempDir(t), "billing")
	writeFile(t, filepath.Join(project, projectfile.WorkspaceFileName), `{"schemaVersion":1,"kind":"workspace","id":"ws_1","name":"  "}`)
	w, err := New(tempDir(t)).Open(project)
	if err != nil {
		t.Fatal(err)
	}
	if w.Name() != "billing" {
		t.Errorf("Name = %q, want the folder name", w.Name())
	}
}

// TestCopiesAndMoves covers two folders with one workspace ID: a copy gets
// its own local directory, a moved project keeps its local data.
func TestCopiesAndMoves(t *testing.T) {
	t.Parallel()
	dir := tempDir(t)
	m := New(tempDir(t))

	original := filepath.Join(dir, "api")
	w, err := m.Create(original, "API")
	if err != nil {
		t.Fatal(err)
	}
	primary := w.LocalDir
	if filepath.Base(primary) != w.ID() {
		t.Errorf("local directory %s is not named after the ID %s", primary, w.ID())
	}
	writeFile(t, filepath.Join(primary, "secrets.json"), "original")
	markerData, err := os.ReadFile(w.MarkerPath())
	if err != nil {
		t.Fatal(err)
	}

	step := func(name, path string, wantStatus LocalStatus, wantDir, wantPrevious string) *Workspace {
		t.Helper()
		got, err := m.Open(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.LocalStatus != wantStatus || got.LocalDir != wantDir || got.PreviousPath != wantPrevious {
			t.Errorf("%s: local = %s %s (previous %q), want %s %s (previous %q)",
				name, got.LocalStatus, got.LocalDir, got.PreviousPath, wantStatus, wantDir, wantPrevious)
		}
		return got
	}

	step("reopen", original, LocalExisting, primary, "")

	// Copy the project: same marker, same ID, the original still there.
	copied := filepath.Join(dir, "api-copy")
	writeFile(t, filepath.Join(copied, projectfile.WorkspaceFileName), string(markerData))
	c := step("copy", copied, LocalCopy, filepath.Join(filepath.Dir(primary), copyKey(w.ID(), copied)), original)
	if c.LocalDir == primary || isFile(filepath.Join(c.LocalDir, "secrets.json")) {
		t.Error("the copy shares the original's local data")
	}
	step("copy again", copied, LocalCopy, c.LocalDir, original)
	step("original again", original, LocalExisting, primary, "")

	// Move the original: its local data follows it.
	moved := filepath.Join(dir, "api-moved")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	step("moved", moved, LocalMoved, primary, original)
	if data, _ := os.ReadFile(filepath.Join(primary, "secrets.json")); string(data) != "original" {
		t.Errorf("secrets after move = %q", data)
	}
	step("moved again", moved, LocalExisting, primary, "")

	// Neither folder was changed by any of this.
	for _, p := range []string{moved, copied} {
		if got := listTree(t, p); !slices.Equal(got, []string{projectfile.WorkspaceFileName}) {
			t.Errorf("%s = %v", p, got)
		}
		if data, _ := os.ReadFile(filepath.Join(p, projectfile.WorkspaceFileName)); string(data) != string(markerData) {
			t.Errorf("%s: marker changed", p)
		}
	}
}

func TestLocalKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		id    string
		plain bool
	}{
		{"ws_7c1e4b9a2f3d5e60", true},
		{"ws_my-project_2", true},
		{"ws_", false},
		{"WS_ABC", false},
		{"ws_ABC", false},
		{"acme", false},
		{"con", false},
		{"../../etc", false},
		{"ws_../x", false},
		{`ws_a\b`, false},
		{"ws_" + strings.Repeat("a", 62), false},
		{"ws_" + strings.Repeat("a", 61), true},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()
			key := LocalKey(tt.id)
			if (key == tt.id) != tt.plain {
				t.Errorf("LocalKey(%q) = %q, plain %v", tt.id, key, tt.plain)
			}
			if !tt.plain && (!strings.HasPrefix(key, "x.") || len(key) != 34) {
				t.Errorf("LocalKey(%q) = %q, want x. and 32 hex digits", tt.id, key)
			}
			if strings.ContainsAny(key, `/\:`) || key != LocalKey(tt.id) {
				t.Errorf("LocalKey(%q) = %q is not a stable directory name", tt.id, key)
			}
		})
	}
	if LocalKey("ws_ABC") == LocalKey("ws_abc") {
		t.Error("IDs that differ in case share a key")
	}
}

func TestHandEditedIDStaysInsideDataDir(t *testing.T) {
	t.Parallel()
	project := tempDir(t)
	writeFile(t, filepath.Join(project, projectfile.WorkspaceFileName), `{"schemaVersion":1,"kind":"workspace","id":"../../escape","name":"x"}`)
	data := tempDir(t)
	w, err := New(data).Open(project)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(w.LocalDir) != filepath.Join(data, DirName) {
		t.Errorf("LocalDir = %s, want a direct child of %s", w.LocalDir, filepath.Join(data, DirName))
	}
}

func TestRecent(t *testing.T) {
	t.Parallel()
	dir := tempDir(t)
	data := tempDir(t)
	m := New(data)
	clock := time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
	m.now = func() time.Time { clock = clock.Add(time.Minute); return clock }

	paths := make([]string, 0, RecentLimit+2)
	for i := range RecentLimit + 2 {
		p := filepath.Join(dir, fmt.Sprintf("p%02d", i))
		if _, err := m.Create(p, fmt.Sprintf("Project %d", i)); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	// Reopening moves p00 back to the top instead of adding it twice.
	if _, err := m.Open(paths[0]); err != nil {
		t.Fatal(err)
	}

	recent, err := m.Recent()
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != RecentLimit {
		t.Fatalf("len = %d, want %d", len(recent), RecentLimit)
	}
	if recent[0].Path != paths[0] || recent[0].Name != "Project 0" || !recent[0].Available || !recent[0].OpenedAt.Equal(clock) {
		t.Errorf("recent[0] = %+v", recent[0])
	}
	if recent[1].Path != paths[len(paths)-1] {
		t.Errorf("recent[1] = %s, want the last created", recent[1].Path)
	}
	if slices.ContainsFunc(recent, func(r Recent) bool { return r.Path == paths[1] }) {
		t.Error("the oldest entry was not dropped")
	}

	// A deleted marker makes the entry unavailable; RemoveRecent drops it.
	if err := os.Remove(filepath.Join(paths[len(paths)-1], projectfile.WorkspaceFileName)); err != nil {
		t.Fatal(err)
	}
	recent, _ = m.Recent()
	if recent[1].Available {
		t.Error("entry without a marker is available")
	}
	if err := m.RemoveRecent(paths[len(paths)-1]); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveRecent("/not/listed"); err != nil {
		t.Fatal(err)
	}
	recent, _ = m.Recent()
	if len(recent) != RecentLimit-1 || recent[1].Path == paths[len(paths)-1] {
		t.Errorf("after RemoveRecent: %d entries, recent[1] = %s", len(recent), recent[1].Path)
	}

	// A broken recent.json is treated as empty and replaced.
	writeFile(t, filepath.Join(data, DirName, RecentFileName), "{not json")
	if recent, err := m.Recent(); err != nil || len(recent) != 0 {
		t.Errorf("broken file: %v, %v", recent, err)
	}
	if _, err := m.Open(paths[2]); err != nil {
		t.Fatal(err)
	}
	if recent, _ := m.Recent(); len(recent) != 1 {
		t.Errorf("after reopening = %v", recent)
	}
}

func TestReopenLast(t *testing.T) {
	t.Parallel()
	dir := tempDir(t)
	data := tempDir(t)

	if w, err := New(data).ReopenLast(); w != nil || err != nil {
		t.Fatalf("nothing recorded: %v, %v", w, err)
	}

	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	m := New(data)
	if _, err := m.Create(a, "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(b, "B"); err != nil {
		t.Fatal(err)
	}
	if w, err := New(data).ReopenLast(); err != nil || w == nil || w.Root != b {
		t.Fatalf("ReopenLast = %v, %v; want b", w, err)
	}

	// Closed before quitting: nothing is reopened.
	if open, err := m.Close(); !open || err != nil {
		t.Fatalf("Close = %v, %v", open, err)
	}
	if open, err := m.Close(); open || err != nil {
		t.Fatalf("second Close = %v, %v", open, err)
	}
	if w, err := New(data).ReopenLast(); w != nil || err != nil {
		t.Fatalf("after Close: %v, %v", w, err)
	}

	// A folder that is gone is an error, and the entry stays for later.
	if _, err := m.Open(a); err != nil {
		t.Fatal(err)
	}
	hidden := a + ".unmounted"
	if err := os.Rename(a, hidden); err != nil {
		t.Fatal(err)
	}
	m2 := New(data)
	w, err := m2.ReopenLast()
	if w != nil || m2.Current() != nil {
		t.Errorf("reopened a missing folder: %v", w)
	}
	checkErr(t, err, errs.NotFound, CodeFolderNotFound)
	if err := os.Rename(hidden, a); err != nil {
		t.Fatal(err)
	}
	if w, err := New(data).ReopenLast(); err != nil || w == nil || w.Root != a {
		t.Errorf("after the folder is back: %v, %v", w, err)
	}
}

func TestLocalDirWithoutWorkspace(t *testing.T) {
	t.Parallel()
	_, err := New(tempDir(t)).LocalDir()
	checkErr(t, err, errs.NotFound, CodeNoWorkspace)
}

func TestNoDataDir(t *testing.T) {
	t.Parallel()
	project := tempDir(t)
	m := New("")
	_, err := m.Create(project, "x")
	checkErr(t, err, errs.Internal, CodeNoDataDir)
	_, err = m.Open(project)
	checkErr(t, err, errs.Internal, CodeNoDataDir)
	_, err = m.Recent()
	checkErr(t, err, errs.Internal, CodeNoDataDir)
	if w, err := m.ReopenLast(); w != nil || err != nil {
		t.Errorf("ReopenLast = %v, %v", w, err)
	}
	if got := listTree(t, project); len(got) != 0 {
		t.Errorf("project = %v, want empty", got)
	}
}

func TestDataDirInsideProjectIsRefused(t *testing.T) {
	t.Parallel()
	project := tempDir(t)
	writeFile(t, filepath.Join(project, projectfile.WorkspaceFileName), `{"schemaVersion":1,"kind":"workspace","id":"ws_1","name":"x"}`)
	data := filepath.Join(project, ".postsonde-home", "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := New(data).Open(project)
	checkErr(t, err, errs.InvalidArgument, CodeDataDirInProject)
	if isDir(filepath.Join(data, DirName)) {
		t.Error("local data was written inside the project")
	}
}

func TestLocalPermissions(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	w, err := New(tempDir(t)).Create(filepath.Join(tempDir(t), "p"), "P")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		want fs.FileMode
	}{
		{w.LocalDir, 0o700},
		{filepath.Join(w.LocalDir, LocalFileName), 0o600},
		{filepath.Join(filepath.Dir(w.LocalDir), RecentFileName), 0o600},
		{w.MarkerPath(), projectfile.FilePerm},
	}
	for _, tt := range tests {
		fi, err := os.Stat(tt.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got&^tt.want != 0 {
			t.Errorf("%s: mode %v, want at most %v", tt.path, got, tt.want)
		}
	}
}

// TestIsCurrent: the open project's folder, its postsonde.json and a
// symlink to it are the current workspace; another folder, a missing one
// and anything while none is open are not (APP-06, docs/windows.md).
func TestIsCurrent(t *testing.T) {
	t.Parallel()
	project := tempDir(t)
	writeFile(t, filepath.Join(project, projectfile.WorkspaceFileName), `{"schemaVersion":1,"kind":"workspace","id":"ws_00000000000000ab","name":"Acme"}`)
	other := tempDir(t)
	m := New(tempDir(t))
	if m.IsCurrent(project) {
		t.Error("IsCurrent with nothing open = true")
	}
	if _, err := m.Open(project); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tempDir(t), "link")
	symlinked := os.Symlink(project, link) == nil
	for _, c := range []struct {
		path string
		want bool
	}{
		{project, true},
		{filepath.Join(project, projectfile.WorkspaceFileName), true},
		{project + string(filepath.Separator), true},
		{other, false},
		{filepath.Join(other, "missing"), false},
		{"relative", false},
		{"", false},
	} {
		if got := m.IsCurrent(c.path); got != c.want {
			t.Errorf("IsCurrent(%q) = %v, want %v", c.path, got, c.want)
		}
	}
	if symlinked && !m.IsCurrent(link) {
		t.Errorf("IsCurrent(symlink %q) = false", link)
	}
	if _, err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if m.IsCurrent(project) {
		t.Error("IsCurrent after Close = true")
	}
}
