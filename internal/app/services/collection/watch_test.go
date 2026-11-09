package collection

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	envservice "github.com/sorrelfox/postsonde/internal/app/services/environments"
	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/testutil/fakeclock"
)

// watchEvents records every event the service emits.
type watchEvents struct {
	mu       sync.Mutex
	external []ExternalChangeDTO
	envs     int
	saved    []SavedEventDTO
	failed   []SaveFailedEventDTO
}

func (e *watchEvents) emit(name string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch name {
	case EventExternalChange:
		e.external = append(e.external, data.(ExternalChangeDTO))
	case envservice.EventChanged:
		e.envs++
	case EventSaved:
		e.saved = append(e.saved, data.(SavedEventDTO))
	case EventSaveFailed:
		e.failed = append(e.failed, data.(SaveFailedEventDTO))
	}
}

func (e *watchEvents) takeExternal() []ExternalChangeDTO {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.external
	e.external = nil
	return out
}

// newWatchedService opens the small project with a manual clock that
// drives both the auto-saver and the watcher (1 s interval), and loads
// the tree, which starts watching.
func newWatchedService(t *testing.T) (*CollectionService, string, *fakeclock.Clock, *watchEvents) {
	t.Helper()
	manager, dir := newProject(t)
	clock := fakeclock.New(clockStart)
	ev := &watchEvents{}
	svc := New(manager, ev.emit, nil, withClock(clock), WithWatch(time.Second))
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	if _, err := svc.Tree(t.Context()); err != nil {
		t.Fatal(err)
	}
	return svc, dir, clock, ev
}

// writeOutside writes a project file as another program would, with a
// modification time the watcher cannot confuse with the previous one.
var outsideWrites sync.Mutex

func writeOutside(t *testing.T, dir, rel, content string) {
	t.Helper()
	outsideWrites.Lock()
	defer outsideWrites.Unlock()
	writeFile(t, dir, rel, content)
	at := time.Now().Add(time.Duration(len(content)) * time.Millisecond)
	if err := os.Chtimes(filepath.Join(dir, filepath.FromSlash(rel)), at, at); err != nil {
		t.Fatal(err)
	}
}

const healthOutside = `{"kind": "http", "id": "req_health", "name": "Health (edited outside)", "method": "GET", "url": "/health", "body": {"mode": "none"}, "auth": {"type": "inherit"}}`

func TestExternalEditIsReportedWithinTwoIntervals(t *testing.T) {
	t.Parallel()
	svc, dir, clock, ev := newWatchedService(t)
	writeOutside(t, dir, "health.json", healthOutside)
	clock.Advance(time.Second)
	if got := ev.takeExternal(); len(got) != 0 {
		t.Fatalf("reported before the file settled: %+v", got)
	}
	clock.Advance(time.Second)
	got := ev.takeExternal()
	if len(got) != 1 {
		t.Fatalf("got %d external-change events, want 1", len(got))
	}
	e := got[0]
	if e.WorkspaceID != "ws_0123456789abcdef" || !slices.Equal(e.Modified, []string{"req_health"}) ||
		len(e.Added)+len(e.Removed)+len(e.Conflicts) != 0 || e.Globals || e.Environments {
		t.Fatalf("event = %+v", e)
	}
	if name := getRequest(t, svc, "req_health").Name; name != "Health (edited outside)" {
		t.Errorf("Request().Name = %q after the refresh", name)
	}
}

func TestOwnWritesAreNotReported(t *testing.T) {
	t.Parallel()
	svc, _, clock, ev := newWatchedService(t)
	req := getRequest(t, svc, "req_health")
	req.URL = "/healthz"
	update(t, svc, req, 1)
	if _, err := svc.CreateFolder(t.Context(), CreateFolderInputDTO{Name: "Orders", Index: -1}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Second)
	if len(ev.saved) != 1 {
		t.Fatalf("saved events = %d, want 1", len(ev.saved))
	}
	if got := ev.takeExternal(); len(got) != 0 {
		t.Fatalf("the app's own writes were reported: %+v", got)
	}
}

func TestAddsAndRemovesAreReported(t *testing.T) {
	t.Parallel()
	svc, dir, clock, ev := newWatchedService(t)
	req := getRequest(t, svc, "req_me")
	req.Name = "Pending"
	update(t, svc, req, 1)
	if err := os.Remove(filepath.Join(dir, "users", "me.json")); err != nil {
		t.Fatal(err)
	}
	writeOutside(t, dir, "users/added.json", `{"kind": "http", "id": "req_added", "name": "Added", "method": "GET", "url": ""}`)
	clock.Advance(3 * time.Second)
	got := ev.takeExternal()
	if len(got) != 1 || !slices.Equal(got[0].Removed, []string{"req_me"}) || !slices.Equal(got[0].Added, []string{"req_added"}) {
		t.Fatalf("events = %+v", got)
	}
	// The pending edit of the removed request was dropped: nothing brings
	// the file back, and no failure is reported for it.
	clock.Advance(5 * time.Second)
	if _, err := os.Stat(filepath.Join(dir, "users", "me.json")); !os.IsNotExist(err) {
		t.Fatalf("the removed file was written again: %v", err)
	}
	if len(ev.failed) != 0 {
		t.Errorf("save-failed events = %+v", ev.failed)
	}
	_, err := svc.Request(t.Context(), "req_me")
	checkCode(t, err, errs.NotFound, core.CodeNotFound)
}

// TestConflictSetsTheEditAside: a file changed on disk while its request
// had an unsaved edit. The write is refused, the edit is set aside as a
// conflict instead of overwriting the other version, further edits are
// refused until ResolveConflict, and after it the kept version is written.
func TestConflictSetsTheEditAside(t *testing.T) {
	t.Parallel()
	svc, dir, clock, ev := newWatchedService(t)
	req := getRequest(t, svc, "req_health")
	req.URL = "/mine"
	update(t, svc, req, 1)
	writeOutside(t, dir, "health.json", healthOutside)

	clock.Advance(3 * time.Second)
	got := ev.takeExternal()
	if len(got) != 1 || !slices.Equal(got[0].Conflicts, []string{"req_health"}) || !slices.Equal(got[0].Modified, []string{"req_health"}) {
		t.Fatalf("events = %+v", got)
	}
	if len(ev.failed) != 0 || len(ev.saved) != 0 {
		t.Errorf("saved %v, failed %v: a conflict is neither", ev.saved, ev.failed)
	}
	if body := readFile(t, dir, "health.json"); !strings.Contains(body, "edited outside") {
		t.Fatalf("the outside version was overwritten: %s", body)
	}
	if got := getRequest(t, svc, "req_health"); got.URL != "/health" {
		t.Errorf("Request() = %q, want the version on disk", got.URL)
	}

	_, err := svc.UpdateRequest(t.Context(), UpdateRequestInputDTO{Request: req, Revision: 2})
	checkCode(t, err, errs.IO, core.CodeChangedOnDisk)

	// Keep mine: resolve, then send it again.
	svc.ResolveConflict(t.Context(), "req_health")
	update(t, svc, req, 3)
	clock.Advance(time.Second)
	if body := readFile(t, dir, "health.json"); !strings.Contains(body, `"/mine"`) {
		t.Fatalf("the kept version was not written: %s", body)
	}
	clock.Advance(3 * time.Second)
	if got := ev.takeExternal(); len(got) != 0 {
		t.Fatalf("the kept version's write was reported: %+v", got)
	}
}

func TestFolderOrderChangeKeepsTheFolderEdit(t *testing.T) {
	t.Parallel()
	svc, dir, clock, ev := newWatchedService(t)
	f := getFolder(t, svc, "fld_users")
	f.Headers = []KeyValueDTO{{Name: "X-Mine", Value: "1", Enabled: true}}
	if _, err := svc.UpdateFolder(t.Context(), UpdateFolderInputDTO{Folder: f, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	writeOutside(t, dir, "users/_folder.json", `{"kind": "folder", "id": "fld_users", "name": "Users", "order": ["me.json"], "auth": {"type": "inherit"}}`)
	// Detected before the save runs: the refresh sees only the order change.
	svc.mu.Lock()
	svc.refreshLocked(svc.ws, svc.tree)
	svc.mu.Unlock()
	got := ev.takeExternal()
	if len(got) != 1 || len(got[0].Conflicts) != 0 || !slices.Equal(got[0].Moved, []string{"fld_users"}) {
		t.Fatalf("events = %+v", got)
	}
	clock.Advance(time.Second)
	body := readFile(t, dir, "users/_folder.json")
	if !strings.Contains(body, "X-Mine") || !strings.Contains(body, `"me.json"`) {
		t.Fatalf("the folder edit and the outside order were not merged: %s", body)
	}
}

func TestEnvironmentAndGlobalChanges(t *testing.T) {
	t.Parallel()
	_, dir, clock, ev := newWatchedService(t)
	writeOutside(t, dir, "environments/dev.json", `{"kind": "environment", "id": "env_dev", "name": "Dev", "variables": []}`)
	clock.Advance(3 * time.Second)
	got := ev.takeExternal()
	if len(got) != 1 || !got[0].Environments || got[0].Globals {
		t.Fatalf("events = %+v", got)
	}
	writeOutside(t, dir, "postsonde.json", `{"schemaVersion": 1, "kind": "workspace", "id": "ws_0123456789abcdef", "name": "Shop", "order": ["users"], "variables": [{"name": "host", "value": "example.test"}]}`)
	clock.Advance(3 * time.Second)
	got = ev.takeExternal()
	if len(got) != 1 || !got[0].Globals || got[0].Environments {
		t.Fatalf("events = %+v", got)
	}
	if ev.envs != 2 {
		t.Errorf("environments:changed sent %d times, want 2", ev.envs)
	}
}

func TestEnvironmentsWrittenByTheAppAreNotReported(t *testing.T) {
	t.Parallel()
	svc, dir, clock, ev := newWatchedService(t)
	writeOutside(t, dir, "environments/dev.json", `{"kind": "environment", "id": "env_dev", "name": "Dev", "variables": []}`)
	EnvironmentsWritten(svc)
	clock.Advance(3 * time.Second)
	if got := ev.takeExternal(); len(got) != 0 {
		t.Fatalf("events = %+v", got)
	}
}

func TestSetWatchingPausesPolling(t *testing.T) {
	t.Parallel()
	svc, dir, clock, ev := newWatchedService(t)
	svc.SetWatching(t.Context(), false)
	writeOutside(t, dir, "health.json", healthOutside)
	clock.Advance(10 * time.Second)
	if got := ev.takeExternal(); len(got) != 0 {
		t.Fatalf("reported while hidden: %+v", got)
	}
	svc.SetWatching(t.Context(), true)
	clock.Advance(time.Second)
	if got := ev.takeExternal(); len(got) != 1 {
		t.Fatalf("got %d events after showing the window again", len(got))
	}
}

func TestReloadReportsExternalChangesWithoutAWatcher(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	ev := &watchEvents{}
	svc := New(manager, ev.emit, nil, withClock(fakeclock.New(clockStart)))
	if _, err := svc.Tree(t.Context()); err != nil {
		t.Fatal(err)
	}
	writeOutside(t, dir, "health.json", healthOutside)
	tree, err := svc.Reload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := ev.takeExternal(); len(got) != 1 || !slices.Equal(got[0].Modified, []string{"req_health"}) {
		t.Fatalf("events = %+v", got)
	}
	for _, n := range tree.Nodes {
		if n.ID == "req_health" && n.Name != "Health (edited outside)" {
			t.Errorf("reloaded name = %q", n.Name)
		}
	}
}

func TestRecreateRequestWritesARemovedFileBack(t *testing.T) {
	t.Parallel()
	svc, dir, clock, ev := newWatchedService(t)
	req := getRequest(t, svc, "req_me")
	if err := os.Remove(filepath.Join(dir, "users", "me.json")); err != nil {
		t.Fatal(err)
	}
	clock.Advance(3 * time.Second)
	if got := ev.takeExternal(); len(got) != 1 || !slices.Equal(got[0].Removed, []string{"req_me"}) {
		t.Fatalf("events = %+v", got)
	}
	req.Query = "{ me { id name } }"
	res, err := svc.RecreateRequest(t.Context(), RecreateRequestInputDTO{ParentID: "fld_users", Index: -1, Request: req})
	if err != nil {
		t.Fatal(err)
	}
	if res.NodeID != "req_me" {
		t.Errorf("NodeID = %q, want the request's own ID", res.NodeID)
	}
	if got := getRequest(t, svc, "req_me"); got.Query != "{ me { id name } }" {
		t.Errorf("query = %q", got.Query)
	}
	clock.Advance(3 * time.Second)
	if got := ev.takeExternal(); len(got) != 0 {
		t.Fatalf("the restore was reported as an outside change: %+v", got)
	}

	// Into the top level when its folder is gone as well.
	if err := os.RemoveAll(filepath.Join(dir, "users")); err != nil {
		t.Fatal(err)
	}
	clock.Advance(3 * time.Second)
	ev.takeExternal()
	res, err = svc.RecreateRequest(t.Context(), RecreateRequestInputDTO{ParentID: "fld_users", Index: -1, Request: req})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range res.Tree.Nodes {
		if n.ID == "req_me" && n.ParentID != nil {
			t.Errorf("recreated into %v, want the top level", *n.ParentID)
		}
	}
}

func TestSwitchingProjectsStopsWatchingTheOldOne(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	clock := fakeclock.New(clockStart)
	ev := &watchEvents{}
	svc := New(manager, ev.emit, nil, withClock(clock), WithWatch(time.Second))
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	if _, err := svc.Tree(t.Context()); err != nil {
		t.Fatal(err)
	}
	manager.Close()
	writeOutside(t, dir, "health.json", healthOutside)
	clock.Advance(5 * time.Second)
	if got := ev.takeExternal(); len(got) != 0 {
		t.Fatalf("a closed project was reported: %+v", got)
	}
	if clock.Pending() != 0 {
		t.Errorf("%d timers left after the project closed", clock.Pending())
	}
}
