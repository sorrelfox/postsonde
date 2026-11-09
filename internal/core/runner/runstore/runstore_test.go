package runstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/runner/report"
)

// run is a record of n results that ended at endedAt.
func run(id string, endedAt int64, n int) report.Run {
	b := report.NewBuilder(report.Summary{ID: id, FolderID: "fld_api", Folder: "API", StartedAt: endedAt - 10, Iterations: 1, Requests: n, Total: n}, 0)
	for i := range n {
		b.Add(report.Result{Iteration: 1, Index: i + 1, Name: fmt.Sprintf("Request %d", i+1), Outcome: report.OutcomePassed, DurationMs: 3, Path: []string{}, Tests: []report.Test{}})
	}
	return b.Finish(report.StatusOK, endedAt)
}

func ids(list []report.Summary) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.ID
	}
	return out
}

func wantCode(t *testing.T, err error, kind errs.Kind, code string) {
	t.Helper()
	e, ok := errors.AsType[*errs.Error](err)
	if !ok || e.Kind != kind || e.Code != code {
		t.Fatalf("err = %v, want %s %s", err, kind, code)
	}
}

func TestSaveListGet(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "local", Dir)
	s := Open(dir, Limits{})
	if list, err := s.List(); err != nil || len(list) != 0 {
		t.Fatalf("empty history = %v, %v", list, err)
	}
	for i, id := range []string{"op_b", "op_a", "op_c"} {
		if err := s.Save(run(id, int64(1000+i*10), 3)); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(list); !slices.Equal(got, []string{"op_c", "op_a", "op_b"}) {
		t.Fatalf("list = %v, want newest first", got)
	}
	if list[0].Counts.Results != 3 || list[0].Folder != "API" || list[0].Schema != report.Schema {
		t.Errorf("summary = %+v", list[0])
	}

	r, err := s.Get("op_a")
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "op_a" || len(r.Results) != 3 || r.Results[2].Name != "Request 3" {
		t.Fatalf("get = %+v", r)
	}

	// Files: 0600 in a 0700 directory, never elsewhere.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %v, %v", info.Mode().Perm(), err)
		}
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			info, _ := f.Info()
			if info.Mode().Perm() != 0o600 {
				t.Errorf("%s mode = %v", f.Name(), info.Mode().Perm())
			}
		}
		if len(files) != 6 {
			t.Errorf("files = %d, want 6", len(files))
		}
	}

	// Saving a run again replaces it.
	if err := s.Save(run("op_a", 2000, 1)); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Get("op_a"); len(r.Results) != 1 {
		t.Errorf("replaced run has %d results", len(r.Results))
	}
	if list, _ := s.List(); !slices.Equal(ids(list), []string{"op_a", "op_c", "op_b"}) {
		t.Errorf("list after replace = %v", ids(list))
	}

	_, err = s.Get("op_gone")
	wantCode(t, err, errs.NotFound, CodeNotFound)
	for _, bad := range []string{"", "../x", "a/b", "op.x", strings.Repeat("a", 65)} {
		_, err = s.Get(bad)
		wantCode(t, err, errs.InvalidArgument, CodeInvalidID)
		wantCode(t, s.Delete(bad), errs.InvalidArgument, CodeInvalidID)
		wantCode(t, s.Save(run(bad, 1, 0)), errs.InvalidArgument, CodeInvalidID)
	}
}

func TestDeleteAndClear(t *testing.T) {
	t.Parallel()
	s := Open(t.TempDir(), Limits{})
	for i := range 3 {
		if err := s.Save(run(fmt.Sprintf("op_%d", i), int64(i+1), 1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Delete("op_1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("op_1"); err != nil {
		t.Errorf("deleting again = %v", err)
	}
	if list, _ := s.List(); !slices.Equal(ids(list), []string{"op_2", "op_0"}) {
		t.Errorf("after delete = %v", ids(list))
	}
	_, err := s.Get("op_1")
	wantCode(t, err, errs.NotFound, CodeNotFound)
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(s.Dir())
	if list, _ := s.List(); len(list) != 0 || len(files) != 0 {
		t.Errorf("after clear: %v, %d files", ids(list), len(files))
	}
}

func TestRetention(t *testing.T) {
	t.Parallel()
	t.Run("by count", func(t *testing.T) {
		t.Parallel()
		s := Open(t.TempDir(), Limits{MaxRuns: 3})
		for i := range 5 {
			if err := s.Save(run(fmt.Sprintf("op_%d", i), int64(100+i), 1)); err != nil {
				t.Fatal(err)
			}
		}
		list, _ := s.List()
		if !slices.Equal(ids(list), []string{"op_4", "op_3", "op_2"}) {
			t.Fatalf("kept %v", ids(list))
		}
		_, err := s.Get("op_0")
		wantCode(t, err, errs.NotFound, CodeNotFound)
		files, _ := os.ReadDir(s.Dir())
		if len(files) != 6 {
			t.Errorf("files = %d, want 6", len(files))
		}
	})
	t.Run("by size, the newest always kept", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		big := run("op_big", 1, 200)
		s := Open(dir, Limits{MaxBytes: 1})
		if err := s.Save(run("op_small", 0, 1)); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(big); err != nil {
			t.Fatal(err)
		}
		if list, _ := s.List(); !slices.Equal(ids(list), []string{"op_big"}) {
			t.Fatalf("kept %v", ids(list))
		}
		info, err := os.Stat(filepath.Join(dir, "op_big.json"))
		if err != nil {
			t.Fatal(err)
		}
		// Room for two of the big one: the three newest do not fit.
		s = Open(dir, Limits{MaxBytes: 2*info.Size() + 10})
		for i := 2; i <= 4; i++ {
			if err := s.Save(run(fmt.Sprintf("op_%d", i), int64(i), 200)); err != nil {
				t.Fatal(err)
			}
		}
		if list, _ := s.List(); !slices.Equal(ids(list), []string{"op_4", "op_3"}) {
			t.Fatalf("kept %v", ids(list))
		}
	})
	if DefaultMaxRuns != 50 || DefaultMaxBytes != 256<<20 {
		t.Error("the default limits changed: update docs/runner.md")
	}
}

func TestTornAndDamaged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := Open(dir, Limits{})
	if err := s.Save(run("op_ok", 5, 1)); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("op_torn.json", `{"id": "op_torn"}`)                       // a record without its summary
	write("op_lost.summary.json", `{"id": "op_lost", "endedAt": 9}`) // a summary without its record
	write("op_bad.summary.json", `{`)                                // unreadable
	write("op_bad.json", `{}`)
	write("op_other.summary.json", `{"id": "op_x"}`) // not its own ID
	write("op_other.json", `{}`)
	write(".op_ok.json.tmp-123", `{`) // an atomic write's leftover
	write("op_dmg.summary.json", `{"id": "op_dmg", "endedAt": 1}`)
	write("op_dmg.json", `not json`)

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(list), []string{"op_ok", "op_dmg"}) {
		t.Fatalf("list = %v", ids(list))
	}
	_, err = s.Get("op_dmg")
	wantCode(t, err, errs.NotFound, CodeNotFound)
	for _, gone := range []string{"op_torn.json", "op_lost.summary.json", "op_bad.json", "op_other.json"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left: %v", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".op_ok.json.tmp-123")); err != nil {
		t.Errorf("a temporary file was touched: %v", err)
	}
}

func TestConcurrent(t *testing.T) {
	t.Parallel()
	s := Open(t.TempDir(), Limits{MaxRuns: 5})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			id := fmt.Sprintf("op_%d", i)
			if err := s.Save(run(id, int64(i), 2)); err != nil {
				t.Error(err)
			}
			if _, err := s.List(); err != nil {
				t.Error(err)
			}
			if _, err := s.Get(id); err != nil {
				if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != CodeNotFound {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if list, _ := s.List(); len(list) != 5 {
		t.Errorf("kept %d, want 5", len(list))
	}
}
