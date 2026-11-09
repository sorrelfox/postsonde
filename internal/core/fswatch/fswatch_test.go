package fswatch_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/fswatch"
	"github.com/sorrelfox/postsonde/internal/testutil/fakeclock"
)

var start = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// files is a fake file system: Scan returns a copy of its map.
type files struct {
	mu   sync.Mutex
	m    fswatch.Snapshot
	fail bool
}

func (f *files) set(p string, size int64, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[p] = fswatch.Stat{Size: size, ModTime: at}
}

func (f *files) remove(p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, p)
}

func (f *files) scan() (fswatch.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("scan failed")
	}
	out := fswatch.Snapshot{}
	for p, st := range f.m {
		out[p] = st
	}
	return out, nil
}

type reports struct {
	mu  sync.Mutex
	got []fswatch.Changes
}

func (r *reports) add(c fswatch.Changes) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, c)
}

func (r *reports) take() []fswatch.Changes {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.got
	r.got = nil
	return out
}

func setup(t *testing.T) (*files, *reports, *fakeclock.Clock, *fswatch.Watcher) {
	t.Helper()
	fs := &files{m: fswatch.Snapshot{"a.json": {Size: 1, ModTime: start}}}
	rep := &reports{}
	clock := fakeclock.New(start)
	w := fswatch.New(fswatch.Options{Interval: time.Second, Clock: clock, Scan: fs.scan, OnChange: rep.add})
	w.Start()
	t.Cleanup(w.Stop)
	return fs, rep, clock, w
}

func TestReportsEditsAddsAndRemovesAfterAQuietInterval(t *testing.T) {
	fs, rep, clock, _ := setup(t)
	clock.Advance(3 * time.Second)
	if got := rep.take(); len(got) != 0 {
		t.Fatalf("no change reported %v", got)
	}

	fs.set("a.json", 2, start.Add(time.Minute))
	fs.set("b.json", 1, start.Add(time.Minute))
	clock.Advance(time.Second) // noticed
	if got := rep.take(); len(got) != 0 {
		t.Fatalf("reported before the files settled: %v", got)
	}
	clock.Advance(time.Second) // quiet for one interval
	want := []fswatch.Changes{{Added: []string{"b.json"}, Modified: []string{"a.json"}}}
	if got := rep.take(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	fs.remove("a.json")
	clock.Advance(2 * time.Second)
	want = []fswatch.Changes{{Removed: []string{"a.json"}}}
	if got := rep.take(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestRenameIsARemoveAndAnAdd(t *testing.T) {
	fs, rep, clock, _ := setup(t)
	fs.remove("a.json")
	fs.set("renamed.json", 1, start)
	clock.Advance(2 * time.Second)
	want := []fswatch.Changes{{Added: []string{"renamed.json"}, Removed: []string{"a.json"}}}
	if got := rep.take(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestBurstIsCoalescedIntoOneReport(t *testing.T) {
	fs, rep, clock, _ := setup(t)
	// A checkout writing files over three intervals.
	for i := range 3 {
		fs.set("a.json", int64(10+i), start.Add(time.Duration(i+1)*time.Minute))
		fs.set(filepath.ToSlash(filepath.Join("dir", string(rune('a'+i))+".json")), 1, start)
		clock.Advance(time.Second)
	}
	if got := rep.take(); len(got) != 0 {
		t.Fatalf("reported during the burst: %v", got)
	}
	clock.Advance(time.Second)
	want := []fswatch.Changes{{Added: []string{"dir/a.json", "dir/b.json", "dir/c.json"}, Modified: []string{"a.json"}}}
	if got := rep.take(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestChangesThatKeepComingAreReportedAfterMaxWait(t *testing.T) {
	fs, rep, clock, _ := setup(t)
	for i := range 10 {
		fs.set("a.json", int64(i+2), start.Add(time.Duration(i)*time.Minute))
		clock.Advance(time.Second)
		if len(rep.got) > 0 {
			if i < 5 {
				t.Fatalf("reported after %d intervals, before MaxWait", i+1)
			}
			return
		}
	}
	t.Fatal("never reported while the file kept changing")
}

func TestChangeUndoneBeforeReportIsNotReported(t *testing.T) {
	fs, rep, clock, _ := setup(t)
	fs.set("a.json", 5, start.Add(time.Minute))
	clock.Advance(time.Second)
	fs.set("a.json", 1, start) // back as it was
	clock.Advance(3 * time.Second)
	if got := rep.take(); len(got) != 0 {
		t.Fatalf("reported %v", got)
	}
}

func TestPauseResumeAndPoke(t *testing.T) {
	fs, rep, clock, w := setup(t)
	w.Pause()
	fs.set("b.json", 1, start)
	clock.Advance(10 * time.Second)
	if got := rep.take(); len(got) != 0 {
		t.Fatalf("reported while paused: %v", got)
	}
	w.Resume() // polls at once: noticed, then reported after a quiet interval
	clock.Advance(0)
	clock.Advance(time.Second)
	if got := rep.take(); len(got) != 1 || !reflect.DeepEqual(got[0].Added, []string{"b.json"}) {
		t.Fatalf("got %+v after resume", got)
	}

	fs.set("c.json", 1, start)
	w.Poke()
	clock.Advance(0) // the poked poll notices it
	w.Poll()         // and a second scan finds it settled
	if got := rep.take(); len(got) != 1 || !reflect.DeepEqual(got[0].Added, []string{"c.json"}) {
		t.Fatalf("got %+v after poke", got)
	}
}

func TestRebaseDropsUnreportedChanges(t *testing.T) {
	fs, rep, clock, w := setup(t)
	fs.set("b.json", 1, start)
	clock.Advance(time.Second)
	w.Rebase()
	clock.Advance(3 * time.Second)
	if got := rep.take(); len(got) != 0 {
		t.Fatalf("reported %v after Rebase", got)
	}
}

func TestScanErrorsSkipThePoll(t *testing.T) {
	fs, rep, clock, _ := setup(t)
	fs.mu.Lock()
	fs.fail = true
	fs.mu.Unlock()
	fs.m["b.json"] = fswatch.Stat{Size: 1}
	clock.Advance(5 * time.Second)
	if got := rep.take(); len(got) != 0 {
		t.Fatalf("reported %v while scans failed", got)
	}
	fs.mu.Lock()
	fs.fail = false
	fs.mu.Unlock()
	clock.Advance(2 * time.Second)
	if got := rep.take(); len(got) != 1 {
		t.Fatalf("got %+v once scans work again", got)
	}
}

func TestStopStopsPolling(t *testing.T) {
	fs, rep, clock, w := setup(t)
	w.Stop()
	fs.set("b.json", 1, start)
	clock.Advance(10 * time.Second)
	if got := rep.take(); len(got) != 0 {
		t.Fatalf("reported %v after Stop", got)
	}
	if clock.Pending() != 0 {
		t.Fatalf("%d timers left after Stop", clock.Pending())
	}
}

func TestStatOfAndCompareOnRealFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.json")
	if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	before := fswatch.Snapshot{"a.json": fswatch.StatOf(fi)}
	if err := os.Chtimes(p, start, start); err != nil {
		t.Fatal(err)
	}
	fi, _ = os.Stat(p)
	after := fswatch.Snapshot{"a.json": fswatch.StatOf(fi)}
	if before.Equal(after) {
		t.Fatal("a changed modification time compares equal")
	}
	if c := fswatch.Compare(before, after); !reflect.DeepEqual(c.Modified, []string{"a.json"}) {
		t.Fatalf("Compare = %+v", c)
	}
}
