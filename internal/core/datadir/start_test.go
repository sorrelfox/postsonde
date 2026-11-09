package datadir

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/appdirs"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

var t0 = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func start(t *testing.T, f *fixture) Startup {
	t.Helper()
	st, err := Start(f.Env(), t0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return st
}

func asErr(err error) *errs.Error {
	e, _ := errors.AsType[*errs.Error](err)
	return e
}

func codes(st Startup) []string {
	var out []string
	for _, p := range st.Problems {
		out = append(out, p.Code)
	}
	return out
}

func TestStartDefault(t *testing.T) {
	f := newFixture(t)
	st := start(t, f)
	if st.Mode != ModeDefault || len(st.Problems) != 0 {
		t.Fatalf("mode %q, problems %v", st.Mode, codes(st))
	}
	for _, d := range []string{st.Dirs.Config, st.Dirs.Data, st.Dirs.Cache, st.Dirs.Logs} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("%s not created: %v", d, err)
		}
	}
}

func TestStartPortableWritesOnlyInTheAppFolder(t *testing.T) {
	f := newFixture(t)
	f.portable(t)
	st := start(t, f)
	if st.Mode != ModePortable || len(st.Problems) != 0 {
		t.Fatalf("mode %q, problems %v", st.Mode, codes(st))
	}
	root := filepath.Join(f.app, PortableDirName)
	if st.Dirs != appdirs.Under(root) {
		t.Errorf("Dirs = %+v", st.Dirs)
	}
	for _, d := range []string{st.Dirs.Config, st.Dirs.Data, st.Dirs.Cache, st.Dirs.Logs} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("%s not created: %v", d, err)
		}
	}
	// Nothing in the home directory.
	if got := tree(t, f.home); len(got) != 0 {
		t.Errorf("portable mode wrote to the home directory: %v", got)
	}
}

func TestStartPortableReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only folders work differently on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	f := newFixture(t)
	f.portable(t)
	// Like a mounted disk image: the folder exists and cannot be written.
	if err := os.Chmod(f.app, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.app, 0o755) })

	st := start(t, f)
	if st.Mode != ModePortable || !slices.Equal(codes(st), []string{CodeUnwritable}) {
		t.Fatalf("mode %q, problems %v", st.Mode, codes(st))
	}
	if st.Dirs != (appdirs.Dirs{}) {
		t.Errorf("Dirs = %+v, want none usable", st.Dirs)
	}
	if got := tree(t, f.home); len(got) != 0 {
		t.Errorf("fell back to the home directory: %v", got)
	}
}

func TestStartConfiguredMissing(t *testing.T) {
	f := newFixture(t)
	gone := filepath.Join(f.home, "usb-not-mounted", "PostSonde")
	f.configure(t, Location{DataDir: gone})
	st := start(t, f)
	if st.Mode != ModeConfigured || st.Dirs.Data != "" || !slices.Equal(codes(st), []string{CodeNotFound}) {
		t.Fatalf("mode %q, data %q, problems %v", st.Mode, st.Dirs.Data, codes(st))
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Error("a missing chosen folder must not be created")
	}
	if st.Dirs.Config == "" || st.Dirs.Logs == "" {
		t.Error("settings and logs should still work")
	}
}

// TestMoveAndDeletePrevious runs the whole move: schedule, restart (Start
// again), check the data arrived and the old copy is kept, then delete it.
func TestMoveAndDeletePrevious(t *testing.T) {
	f := newFixture(t)
	st := start(t, f)
	old := st.Dirs.Data
	files := seed(t, old)
	// On macOS the settings share the default folder; they must stay.
	writeFile(t, filepath.Join(old, "settings.json"), `{"version":1}`)

	target := filepath.Join(f.home, "Sync", "PostSonde")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(target, ".DS_Store"), "junk") // still counts as empty

	s := NewStore(st)
	c, err := s.Check(target)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if c.HasData || !c.CanMove || !c.Exists {
		t.Errorf("candidate = %+v", c)
	}
	info, err := s.Schedule(target, ActionMove)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if info.Pending == nil || info.Pending.Action != ActionMove || info.DataDir != old {
		t.Fatalf("info = %+v; the change must wait for a restart", info)
	}

	// Restart.
	st2 := start(t, f)
	if len(st2.Problems) != 0 {
		t.Fatalf("problems: %v", st2.Problems)
	}
	wantTarget := realPath(target)
	if st2.Mode != ModeConfigured || st2.Dirs.Data != wantTarget {
		t.Fatalf("mode %q, data %q; want configured %q", st2.Mode, st2.Dirs.Data, wantTarget)
	}
	if st2.Moved == nil || st2.Location.Pending != nil || st2.Location.Previous == nil {
		t.Fatalf("location = %+v", st2.Location)
	}
	got := tree(t, st2.Dirs.Data)
	for rel, content := range files {
		if got[rel] != content {
			t.Errorf("%s not moved intact", rel)
		}
	}
	// The old data is kept until the user deletes it.
	if tree(t, old)["workspaces/recent.json"] == "" {
		t.Fatal("old data removed without confirmation")
	}

	// A third start is an ordinary configured start.
	st3 := start(t, f)
	if st3.Dirs.Data != wantTarget || st3.Moved != nil || st3.Location.Previous == nil {
		t.Fatalf("third start: data %q moved %v", st3.Dirs.Data, st3.Moved)
	}

	s3 := NewStore(st3)
	info, err = s3.DeletePrevious()
	if err != nil {
		t.Fatalf("DeletePrevious: %v", err)
	}
	if info.Previous != nil {
		t.Error("Previous still set")
	}
	left := tree(t, old)
	if _, ok := left["workspaces/"]; ok {
		t.Error("old workspaces not deleted")
	}
	if left["settings.json"] == "" {
		t.Error("settings.json in the old folder must stay")
	}
	if tree(t, wantTarget)["workspaces/recent.json"] == "" {
		t.Error("new data touched")
	}
	loc, _ := ReadLocation(st3.LocationPath)
	if loc.Previous != nil || loc.DataDir != wantTarget {
		t.Errorf("saved location = %+v", loc)
	}
}

func TestMoveBackToDefault(t *testing.T) {
	f := newFixture(t)
	chosen := filepath.Join(f.home, "chosen")
	f.configure(t, Location{DataDir: chosen})
	seed(t, chosen)
	st := start(t, f)
	if st.Dirs.Data != chosen {
		t.Fatalf("data %q", st.Dirs.Data)
	}
	// The default folder exists and has the settings in it (macOS) but no data.
	def := f.defaults(t).Data
	writeFile(t, filepath.Join(def, "settings.json"), "{}")

	if _, err := NewStore(st).Schedule(def, ActionMove); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	st2 := start(t, f)
	if st2.Mode != ModeDefault || st2.Dirs.Data != realPath(def) || len(st2.Problems) != 0 {
		t.Fatalf("mode %q data %q problems %v", st2.Mode, st2.Dirs.Data, codes(st2))
	}
	if st2.Location.DataDir != "" {
		t.Errorf("DataDir = %q, want default recorded as empty", st2.Location.DataDir)
	}
}

func TestFreshAndUse(t *testing.T) {
	f := newFixture(t)
	st := start(t, f)
	seed(t, st.Dirs.Data)
	empty := filepath.Join(f.home, "empty")
	if _, err := NewStore(st).Schedule(empty, ActionFresh); err != nil {
		t.Fatalf("Schedule fresh: %v", err)
	}
	st2 := start(t, f)
	if st2.Dirs.Data != realPath(empty) || st2.Moved != nil || len(tree(t, empty)) != 0 {
		t.Fatalf("fresh: data %q, contents %v", st2.Dirs.Data, tree(t, empty))
	}
	// Back to the old data, which is still in the default folder.
	s := NewStore(st2)
	c, err := s.Check(st.Dirs.Data)
	if err != nil || !c.HasData {
		t.Fatalf("Check default: %+v, %v", c, err)
	}
	if _, err := s.Schedule(st.Dirs.Data, ActionMove); err == nil {
		t.Error("move into a folder holding data: want error")
	}
	if _, err := s.Schedule(st.Dirs.Data, ActionUse); err != nil {
		t.Fatalf("Schedule use: %v", err)
	}
	st3 := start(t, f)
	if st3.Mode != ModeDefault || st3.Dirs.Data != realPath(st.Dirs.Data) {
		t.Fatalf("use: mode %q data %q", st3.Mode, st3.Dirs.Data)
	}
}

func TestFailedMoveKeepsTheOldDirectory(t *testing.T) {
	f := newFixture(t)
	st := start(t, f)
	seed(t, st.Dirs.Data)
	target := filepath.Join(f.home, "target")
	if _, err := NewStore(st).Schedule(target, ActionMove); err != nil {
		t.Fatal(err)
	}
	// Someone puts PostSonde data there before the restart.
	writeFile(t, filepath.Join(target, "workspaces", "recent.json"), "other")

	st2 := start(t, f)
	if st2.Dirs.Data != st.Dirs.Data || !slices.Equal(codes(st2), []string{CodeMoveFailed}) {
		t.Fatalf("data %q problems %v", st2.Dirs.Data, codes(st2))
	}
	if st2.Location.Pending != nil {
		t.Error("the failed change should be dropped")
	}
	if b, _ := os.ReadFile(filepath.Join(target, "workspaces", "recent.json")); string(b) != "other" {
		t.Error("the target's files were touched")
	}
	// The next start is clean.
	if st3 := start(t, f); len(st3.Problems) != 0 || st3.Dirs.Data != st.Dirs.Data {
		t.Errorf("third start: %v %q", codes(st3), st3.Dirs.Data)
	}
}

func TestCancel(t *testing.T) {
	f := newFixture(t)
	st := start(t, f)
	s := NewStore(st)
	if _, err := s.Schedule(filepath.Join(f.home, "x"), ActionFresh); err != nil {
		t.Fatal(err)
	}
	info, err := s.Cancel()
	if err != nil || info.Pending != nil {
		t.Fatalf("Cancel: %+v %v", info.Pending, err)
	}
	if st2 := start(t, f); st2.Dirs.Data != st.Dirs.Data {
		t.Errorf("data %q after cancel", st2.Dirs.Data)
	}
}

func TestCheck(t *testing.T) {
	f := newFixture(t)
	st := start(t, f)
	seed(t, st.Dirs.Data)
	s := NewStore(st)

	project := filepath.Join(f.home, "shop")
	writeFile(t, filepath.Join(project, projectfile.WorkspaceFileName), `{"kind":"workspace","id":"ws_1"}`)
	full := filepath.Join(f.home, "Documents")
	writeFile(t, filepath.Join(full, "notes.txt"), "mine")
	file := filepath.Join(f.home, "a-file")
	writeFile(t, file, "x")

	tests := []struct {
		name, path, code string
	}{
		{"relative", "rel/dir", CodeInvalidPath},
		{"current", st.Dirs.Data, CodeInvalidPath},
		{"inside current", filepath.Join(st.Dirs.Data, "sub"), CodeInvalidPath},
		{"parent of current", filepath.Dir(st.Dirs.Data), CodeInvalidPath},
		{"project root", project, CodeInvalidPath},
		{"inside a project", filepath.Join(project, "local"), CodeInvalidPath},
		{"not empty", full, CodeNotEmpty},
		{"a file", file, CodeInvalidPath},
		{"missing parent", filepath.Join(f.home, "no", "such", "dir"), CodeInvalidPath},
		{"inside the logs", filepath.Join(st.Dirs.Logs, "x"), CodeInvalidPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.Check(tt.path)
			if err == nil {
				t.Fatal("want error")
			}
			if e := asErr(err); e == nil || e.Code != tt.code {
				t.Errorf("err = %v, want code %s", err, tt.code)
			}
		})
	}

	t.Run("new folder in an existing parent", func(t *testing.T) {
		c, err := s.Check(filepath.Join(f.home, "new"))
		if err != nil || c.Exists || c.HasData {
			t.Errorf("Check = %+v, %v", c, err)
		}
	})
}

func TestChangeBlockedInEnvAndPortableModes(t *testing.T) {
	for _, mode := range []Mode{ModeEnv, ModePortable} {
		t.Run(string(mode), func(t *testing.T) {
			f := newFixture(t)
			if mode == ModeEnv {
				f.env[appdirs.EnvHome] = filepath.Join(f.home, "ps")
			} else {
				f.portable(t)
			}
			s := NewStore(start(t, f))
			info := s.Info()
			if info.Mode != mode || info.ChangeBlocked == nil || info.ChangeBlocked.Code != CodeChangeUnavailable {
				t.Fatalf("info = %+v", info)
			}
			if mode == ModePortable && info.Marker != filepath.Join(f.app, MarkerName) {
				t.Errorf("Marker = %q", info.Marker)
			}
			if _, err := s.Schedule(filepath.Join(f.home, "x"), ActionFresh); err == nil {
				t.Error("Schedule: want error")
			}
		})
	}
}

func TestDeletePreviousRefusesTheFolderInUse(t *testing.T) {
	f := newFixture(t)
	st := start(t, f)
	seed(t, st.Dirs.Data)
	st.Location.Previous = &Previous{DataDir: filepath.Dir(st.Dirs.Data), Entries: []string{filepath.Base(st.Dirs.Data)}}
	if _, err := NewStore(st).DeletePrevious(); err == nil {
		t.Fatal("want error")
	}
	if _, err := os.Stat(filepath.Join(st.Dirs.Data, "workspaces")); err != nil {
		t.Error("data in use was deleted")
	}
}
