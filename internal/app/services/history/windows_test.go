package history

import (
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/windowctx"
	"github.com/sorrelfox/postsonde/internal/core/history"
)

// TestWindowsShareTheHistory (APP-06, docs/windows.md): the windows of a
// project record their sends into, list, open and delete from the one
// history of the project at once. Every recorded entry is kept or was
// deleted by a window, none is lost, and every window gets the events.
// Run with -race.
func TestWindowsShareTheHistory(t *testing.T) {
	t.Parallel()
	ws := &workspace{dir: t.TempDir()}
	ev := &events{}
	svc := New(history.NewRegistry(history.Limits{MaxEntries: 1000}), ws.localDir, ev.emit, nil)
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	rec := Recorder(svc)
	const windows, sends = 4, 25

	var (
		mu      sync.Mutex
		deleted int
	)
	var wg sync.WaitGroup
	for w := range windows {
		wg.Go(func() {
			ctx := windowctx.With(t.Context(), uint(w+1))
			for i := range sends {
				rec()(entry(w*1000 + i))
				page, err := svc.List(ctx, ListInputDTO{})
				if err != nil {
					t.Error(err)
					return
				}
				if i%5 != 4 || len(page.Entries) == 0 {
					continue
				}
				// Every fifth round a window deletes the newest entry it
				// sees; another window may have deleted it first.
				id := page.Entries[0].ID
				if _, err := svc.Get(ctx, id); err != nil {
					continue
				}
				if err := svc.Delete(ctx, id); err == nil {
					mu.Lock()
					deleted++
					mu.Unlock()
				}
			}
		})
	}
	wg.Wait()

	page, err := svc.List(t.Context(), ListInputDTO{})
	if err != nil {
		t.Fatal(err)
	}
	if page.All+deleted != windows*sends {
		t.Errorf("%d entries kept + %d deleted, want %d recorded", page.All, deleted, windows*sends)
	}
	if len(ev.list()) == 0 {
		t.Error("no history:changed events")
	}
}
