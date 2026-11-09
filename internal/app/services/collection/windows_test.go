package collection

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/windowctx"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/testutil/fakeclock"
)

// Edits from several windows (APP-06, windows.go): two windows show the
// same project, each with its own drafts; the tests call the service as
// each window would, with windowctx.With.

// windowEvents records what the multi-window tests look at.
type windowEvents struct {
	mu     sync.Mutex
	edited []EditedEventDTO
	saved  []SavedEventDTO
	failed []SaveFailedEventDTO
	ext    []ExternalChangeDTO
}

func (e *windowEvents) emit(name string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch name {
	case EventEdited:
		e.edited = append(e.edited, data.(EditedEventDTO))
	case EventSaved:
		e.saved = append(e.saved, data.(SavedEventDTO))
	case EventSaveFailed:
		e.failed = append(e.failed, data.(SaveFailedEventDTO))
	case EventExternalChange:
		e.ext = append(e.ext, data.(ExternalChangeDTO))
	}
}

func (e *windowEvents) takeEdited() []EditedEventDTO {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.edited
	e.edited = nil
	return out
}

func (e *windowEvents) savedEvents() []SavedEventDTO {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]SavedEventDTO(nil), e.saved...)
}

func newWindowsService(t *testing.T, opts ...Option) (*CollectionService, string, *fakeclock.Clock, *windowEvents) {
	t.Helper()
	manager, dir := newProject(t)
	clock := fakeclock.New(clockStart)
	ev := &windowEvents{}
	svc := New(manager, ev.emit, nil, append([]Option{withClock(clock)}, opts...)...)
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	return svc, dir, clock, ev
}

// win returns the context of a call from window id.
func win(t *testing.T, id uint) context.Context {
	return windowctx.With(t.Context(), id)
}

func requestIn(t *testing.T, svc *CollectionService, window uint, id string) RequestDTO {
	t.Helper()
	req, err := svc.Request(win(t, window), id)
	if err != nil {
		t.Fatalf("Request(%s) from window %d = %v", id, window, err)
	}
	return req
}

// updateFrom sends req from window w as an edit of version base.
func updateFrom(t *testing.T, svc *CollectionService, w uint, req RequestDTO, revision, base int) (int, error) {
	t.Helper()
	res, err := svc.UpdateRequest(win(t, w), UpdateRequestInputDTO{Request: req, Revision: revision, BaseVersion: &base})
	return res.Version, err
}

func checkEditedElsewhere(t *testing.T, err error, id string, version int) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeEditedElsewhere || errs.KindOf(err) != errs.IO || e.Hint == "" {
		t.Fatalf("err = %v, want %s", err, CodeEditedElsewhere)
	}
	if e.Details["id"] != id || e.Details["version"] != fmt.Sprint(version) {
		t.Errorf("details = %v, want id %s version %d", e.Details, id, version)
	}
}

// TestStaleDraftFromAnotherWindowIsRefused is the case the versions are
// for: window 2 still shows the request as it was before window 1 edited
// it. Its edit is refused, not written over window 1's; once it reloads,
// its edit goes through and keeps window 1's change.
func TestStaleDraftFromAnotherWindowIsRefused(t *testing.T) {
	t.Parallel()
	svc, dir, clock, ev := newWindowsService(t)

	one := requestIn(t, svc, 1, "req_health")
	two := requestIn(t, svc, 2, "req_health")
	if one.Version != 0 || two.Version != 0 {
		t.Fatalf("versions before any edit = %d, %d", one.Version, two.Version)
	}

	one.URL = "/health/v1"
	v, err := updateFrom(t, svc, 1, one, 1, one.Version)
	if err != nil || v != 1 {
		t.Fatalf("window 1's edit = %d, %v", v, err)
	}
	if got := ev.takeEdited(); !reflect.DeepEqual(got, []EditedEventDTO{{WorkspaceID: "ws_0123456789abcdef", IDs: []string{"req_health"}, Window: 1}}) {
		t.Errorf("edited events = %+v", got)
	}

	// Window 2's draft is of version 0.
	two.Name = "Health check"
	_, err = updateFrom(t, svc, 2, two, 1, two.Version)
	checkEditedElsewhere(t, err, "req_health", 1)
	if got := ev.takeEdited(); len(got) != 0 {
		t.Errorf("a refused edit sent %+v", got)
	}
	if got := requestIn(t, svc, 1, "req_health"); got.URL != "/health/v1" || got.Name != "Health" {
		t.Errorf("after the refusal the request is %q %q, want window 1's edit", got.Name, got.URL)
	}

	// Window 2 reloads (collection:edited) and edits again.
	two = requestIn(t, svc, 2, "req_health")
	if two.Version != 1 || two.URL != "/health/v1" {
		t.Fatalf("reloaded = version %d url %q", two.Version, two.URL)
	}
	two.Name = "Health check"
	if v, err := updateFrom(t, svc, 2, two, 7, two.Version); err != nil || v != 2 {
		t.Fatalf("window 2's edit after reloading = %d, %v", v, err)
	}
	// Window 1's next edit, from its version 1, is now the stale one.
	one.Method = "GET"
	_, err = updateFrom(t, svc, 1, one, 2, 1)
	checkEditedElsewhere(t, err, "req_health", 2)

	clock.Advance(time.Minute)
	saved := ev.savedEvents()
	if len(saved) != 1 || saved[0].Window != 2 || saved[0].Revision != 7 {
		t.Fatalf("saved = %+v, want window 2's revision 7", saved)
	}
	file := readFile(t, dir, "health.json")
	if !strings.Contains(file, `"/health/v1"`) || !strings.Contains(file, `"Health check"`) || strings.Contains(file, `"GET"`) {
		t.Errorf("file = %s, want both windows' accepted edits and not the refused one", file)
	}
}

// TestEditWithoutAVersionIsNotChecked: callers that do not send a base
// version (Go callers, older tests) are taken as before.
func TestEditWithoutAVersionIsNotChecked(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newWindowsService(t)
	req := requestIn(t, svc, 1, "req_health")
	req.URL = "/a"
	if _, err := updateFrom(t, svc, 1, req, 1, 0); err != nil {
		t.Fatal(err)
	}
	req.URL = "/b"
	res, err := svc.UpdateRequest(win(t, 2), UpdateRequestInputDTO{Request: req, Revision: 1})
	if err != nil || res.Version != 2 {
		t.Fatalf("unchecked edit = %+v, %v", res, err)
	}
}

// TestFolderSettingsFromTwoWindows: folder drafts are versioned the same
// way.
func TestFolderSettingsFromTwoWindows(t *testing.T) {
	t.Parallel()
	svc, _, _, ev := newWindowsService(t)
	one, err := svc.Folder(win(t, 1), "fld_users")
	if err != nil {
		t.Fatal(err)
	}
	two, err := svc.Folder(win(t, 2), "fld_users")
	if err != nil {
		t.Fatal(err)
	}
	one.Headers = []KeyValueDTO{{Name: "X-One", Value: "1", Enabled: true}}
	base := one.Version
	res, err := svc.UpdateFolder(win(t, 1), UpdateFolderInputDTO{Folder: one, Revision: 1, BaseVersion: &base})
	if err != nil || res.Version != 1 {
		t.Fatalf("window 1 = %+v, %v", res, err)
	}
	two.Headers = []KeyValueDTO{{Name: "X-Two", Value: "2", Enabled: true}}
	base = two.Version
	_, err = svc.UpdateFolder(win(t, 2), UpdateFolderInputDTO{Folder: two, Revision: 1, BaseVersion: &base})
	checkEditedElsewhere(t, err, "fld_users", 1)
	if got := ev.takeEdited(); len(got) != 1 || got[0].Window != 1 || !reflect.DeepEqual(got[0].IDs, []string{"fld_users"}) {
		t.Errorf("edited = %+v", got)
	}
	if f, _ := svc.Folder(win(t, 2), "fld_users"); f.Version != 1 || len(f.Headers) != 1 || f.Headers[0].Name != "X-One" {
		t.Errorf("reloaded folder = %+v", f)
	}
}

// TestChangesOnDiskAndRenamesMoveTheVersionOn: a draft made before the
// file changed on disk, or before a rename, is refused too.
func TestChangesOnDiskAndRenamesMoveTheVersionOn(t *testing.T) {
	t.Parallel()
	svc, dir, _, _ := newWindowsService(t)
	stale := requestIn(t, svc, 2, "req_health")

	writeOutside(t, dir, "health.json", healthOutside)
	if _, err := svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	stale.URL = "/stale"
	_, err := updateFrom(t, svc, 2, stale, 1, stale.Version)
	checkEditedElsewhere(t, err, "req_health", 1)

	fresh := requestIn(t, svc, 2, "req_health")
	if fresh.Name != "Health (edited outside)" || fresh.Version != 1 {
		t.Fatalf("after the change on disk = %q version %d", fresh.Name, fresh.Version)
	}
	res, err := svc.Rename(win(t, 1), RenameInputDTO{ID: "req_health", Name: "Renamed"})
	if err != nil || res.Version != 2 {
		t.Fatalf("Rename = version %d, %v", res.Version, err)
	}
	fresh.URL = "/old-name"
	_, err = updateFrom(t, svc, 2, fresh, 2, fresh.Version)
	checkEditedElsewhere(t, err, "req_health", 2)
	if got := requestIn(t, svc, 1, "req_health"); got.Name != "Renamed" {
		t.Errorf("name = %q, want the rename kept", got.Name)
	}
	// Renaming a folder leaves its settings' version alone.
	if res, err := svc.Rename(win(t, 1), RenameInputDTO{ID: "fld_users", Name: "People"}); err != nil || res.Version != 0 {
		t.Errorf("folder Rename = version %d, %v", res.Version, err)
	}
}

// TestVersionsStartAgainForAnotherProject: versions are per project
// folder.
func TestVersionsStartAgainForAnotherProject(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newWindowsService(t)
	req := requestIn(t, svc, 1, "req_health")
	if _, err := updateFrom(t, svc, 1, req, 1, 0); err != nil {
		t.Fatal(err)
	}
	// The same files in another folder: IDs repeat, versions do not.
	_, otherDir := newProject(t)
	if _, err := svc.manager.Open(otherDir); err != nil {
		t.Fatal(err)
	}
	if got := requestIn(t, svc, 1, "req_health"); got.Version != 0 {
		t.Errorf("version in another project = %d, want 0", got.Version)
	}
}

// TestConcurrentEditsFromWindowsLoseNothing runs windows that edit the
// same request at once, each appending its own marks to the description
// of the version it loaded, while the auto-saver writes. An edit made
// from a version another window has moved on must be refused (the window
// reloads and tries again), so every accepted mark ends up in the file:
// nothing is lost without the window being told. Run with -race.
func TestConcurrentEditsFromWindowsLoseNothing(t *testing.T) {
	t.Parallel()
	svc, dir, clock, _ := newWindowsService(t)
	const windows, edits = 4, 25

	// An accepted edit and the version the service gave it. A window
	// records its edit after UpdateRequest returns, so two windows can
	// record in the other order than the service applied them: the
	// versions give the real order.
	type edit struct {
		version int
		mark    string
	}
	var (
		mu       sync.Mutex
		accepted []edit
	)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var writers sync.WaitGroup
	writers.Add(1)
	go func() { // the auto-saver's writes, and flushes, running meanwhile
		defer writers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			clock.Advance(time.Second)
			_ = svc.FlushAll(t.Context())
		}
	}()
	for w := uint(1); w <= windows; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := windowctx.With(t.Context(), w)
			draft, err := svc.Request(ctx, "req_health")
			if err != nil {
				t.Error(err)
				return
			}
			version := draft.Version
			for i := 0; i < edits; {
				mark := fmt.Sprintf("[w%d-%d]", w, i)
				next := draft
				next.Description = draft.Description + mark
				base := version
				res, err := svc.UpdateRequest(ctx, UpdateRequestInputDTO{Request: next, Revision: i + 1, BaseVersion: &base})
				var e *errs.Error
				switch {
				case err == nil:
					mu.Lock()
					accepted = append(accepted, edit{res.Version, mark})
					mu.Unlock()
					draft, version = next, res.Version
					i++
				case errors.As(err, &e) && e.Code == CodeEditedElsewhere:
					// collection:edited: take the other window's version.
					if draft, err = svc.Request(ctx, "req_health"); err != nil {
						t.Error(err)
						return
					}
					version = draft.Version
				default:
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	writers.Wait()
	if err := svc.FlushAll(t.Context()); err != nil {
		t.Fatal(err)
	}

	if len(accepted) != windows*edits {
		t.Fatalf("accepted %d edits, want %d", len(accepted), windows*edits)
	}
	slices.SortFunc(accepted, func(a, b edit) int { return a.version - b.version })
	marks := make([]string, len(accepted))
	for i, e := range accepted {
		if want := i + 1; e.version != want {
			t.Fatalf("edit %s got version %d, want %d: two edits share a version or one is missing", e.mark, e.version, want)
		}
		marks[i] = e.mark
	}
	final := requestIn(t, svc, 1, "req_health")
	if want := strings.Join(marks, ""); final.Description != want {
		t.Errorf("description = %q\nwant every accepted mark in version order %q", final.Description, want)
	}
	if final.Version != windows*edits {
		t.Errorf("version = %d, want %d", final.Version, windows*edits)
	}
	file := readFile(t, dir, "health.json")
	for _, mark := range marks {
		if !strings.Contains(file, mark) {
			t.Fatalf("the file lost %s:\n%s", mark, file)
		}
	}
}

// TestPollingPausesOnlyWhileEveryWindowIsHidden: SetWatching is per
// window.
func TestPollingPausesOnlyWhileEveryWindowIsHidden(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	clock := fakeclock.New(clockStart)
	ev := &windowEvents{}
	var mu sync.Mutex
	open := 2
	svc := New(manager, ev.emit, nil, withClock(clock), WithWatch(time.Second), WithWindowCount(func() int {
		mu.Lock()
		defer mu.Unlock()
		return open
	}))
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	if _, err := svc.Tree(t.Context()); err != nil {
		t.Fatal(err)
	}
	takeExt := func() int {
		ev.mu.Lock()
		defer ev.mu.Unlock()
		n := len(ev.ext)
		ev.ext = nil
		return n
	}

	svc.SetWatching(win(t, 1), false)
	writeOutside(t, dir, "health.json", healthOutside)
	clock.Advance(5 * time.Second)
	if n := takeExt(); n != 1 {
		t.Fatalf("with window 2 shown: %d external-change events, want 1", n)
	}
	svc.SetWatching(win(t, 2), false)
	writeOutside(t, dir, "health.json", strings.Replace(healthOutside, "edited outside", "edited again", 1))
	clock.Advance(10 * time.Second)
	if n := takeExt(); n != 0 {
		t.Fatalf("with both windows hidden: %d events", n)
	}
	// Window 2 closes: window 1, hidden, is the only one, so polling
	// stays paused; showing it resumes polling.
	mu.Lock()
	open = 1
	mu.Unlock()
	WindowClosed(svc, 2)
	clock.Advance(10 * time.Second)
	if n := takeExt(); n != 0 {
		t.Fatalf("after closing the other window: %d events", n)
	}
	svc.SetWatching(win(t, 1), true)
	clock.Advance(time.Second)
	if n := takeExt(); n != 1 {
		t.Fatalf("after showing window 1: %d events, want 1", n)
	}
}
