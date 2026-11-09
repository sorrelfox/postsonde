package collection

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/autosave"
	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Tree operations and pending auto-save edits (docs/collection.md#auto-save):
// rename, move and duplicate write the edits first; delete and restore
// drop them.

func missing(t *testing.T, dir, rel string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
	return errors.Is(err, fs.ErrNotExist)
}

func TestRenameFlushesPendingEdits(t *testing.T) {
	t.Parallel()
	svc, _, dir, clock, ev := newAutosaveService(t)
	ctx := t.Context()

	req := getRequest(t, svc, "req_health")
	req.URL = "/health/live"
	update(t, svc, req, 1)
	// Renamed before the save delay.
	if _, err := svc.Rename(ctx, RenameInputDTO{ID: "req_health", Name: "Liveness"}); err != nil {
		t.Fatal(err)
	}
	if !missing(t, dir, "health.json") {
		t.Error("health.json is still there")
	}
	got := readFile(t, dir, "liveness.json")
	if !strings.Contains(got, `"url": "/health/live"`) || !strings.Contains(got, `"name": "Liveness"`) {
		t.Fatalf("liveness.json after the rename:\n%s", got)
	}
	if saved, failed := ev.counts(); saved != 1 || failed != 0 {
		t.Errorf("events: %d saved, %d failed; want the flush's save", saved, failed)
	}
	// Nothing is left to write: the name stays, no file comes back.
	clock.Advance(autosave.DefaultMaxDelay)
	if readFile(t, dir, "liveness.json") != got || !missing(t, dir, "health.json") {
		t.Error("a write after the rename changed the files")
	}
	if saved, _ := ev.counts(); saved != 1 {
		t.Errorf("saved events after the delay = %d", saved)
	}
	if name := getRequest(t, svc, "req_health").Name; name != "Liveness" {
		t.Errorf("Request().Name = %q", name)
	}

	// Renaming a folder flushes the requests in it.
	me := getRequest(t, svc, "req_me")
	me.Query = "{ me { id name } }"
	update(t, svc, me, 1)
	if _, err := svc.Rename(ctx, RenameInputDTO{ID: "fld_users", Name: "People"}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dir, "people/me.json"); !strings.Contains(got, `{ me { id name } }`) {
		t.Fatalf("people/me.json:\n%s", got)
	}
	if !missing(t, dir, "users") {
		t.Error("users is still there")
	}
	clock.Advance(autosave.DefaultMaxDelay)
	if !missing(t, dir, "users") {
		t.Error("a late write recreated users/")
	}
}

func TestMoveFlushesPendingEdits(t *testing.T) {
	t.Parallel()
	svc, _, dir, clock, _ := newAutosaveService(t)
	ctx := t.Context()

	me := getRequest(t, svc, "req_me")
	me.Query = "{ me { email } }"
	update(t, svc, me, 1)
	if _, err := svc.Move(ctx, MoveInputDTO{ID: "req_me", ParentID: "", Index: -1}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dir, "me.json"); !strings.Contains(got, `{ me { email } }`) {
		t.Fatalf("me.json after the move:\n%s", got)
	}
	clock.Advance(autosave.DefaultMaxDelay)
	if !missing(t, dir, "users/me.json") {
		t.Error("users/me.json is there after the move")
	}
}

func TestDuplicateCopiesPendingEdits(t *testing.T) {
	t.Parallel()
	svc, _, dir, _, _ := newAutosaveService(t)
	ctx := t.Context()

	req := getRequest(t, svc, "req_health")
	req.URL = "/health/ready"
	update(t, svc, req, 1)
	res, err := svc.Duplicate(ctx, "req_health")
	if err != nil {
		t.Fatal(err)
	}
	n := nodeByID(res.Tree, res.NodeID)
	if n == nil {
		t.Fatalf("no copy %s in the tree", res.NodeID)
	}
	if got := readFile(t, dir, n.Path); !strings.Contains(got, `"url": "/health/ready"`) {
		t.Fatalf("%s:\n%s", n.Path, got)
	}
	if got := readFile(t, dir, "health.json"); !strings.Contains(got, `"url": "/health/ready"`) {
		t.Fatalf("health.json:\n%s", got)
	}
}

func TestDeleteDropsPendingEdits(t *testing.T) {
	t.Parallel()
	svc, _, dir, clock, ev := newAutosaveService(t)
	ctx := t.Context()
	before := readFile(t, dir, "health.json")

	req := getRequest(t, svc, "req_health")
	req.URL = "/health/deleted"
	update(t, svc, req, 1)
	me := getRequest(t, svc, "req_me")
	me.Query = "{ gone }"
	update(t, svc, me, 1)

	del, err := svc.Delete(ctx, "req_health")
	if err != nil {
		t.Fatal(err)
	}
	// The folder's requests go with it.
	delUsers, err := svc.Delete(ctx, "fld_users")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(autosave.DefaultMaxDelay)
	if clock.Pending() != 0 {
		t.Errorf("%d save timers left", clock.Pending())
	}
	if !missing(t, dir, "health.json") || !missing(t, dir, "users") {
		t.Fatal("a write after the delete recreated a file")
	}
	if saved, failed := ev.counts(); saved != 0 || failed != 0 {
		t.Errorf("events: %d saved, %d failed; want none", saved, failed)
	}
	if err := svc.FlushAll(ctx); err != nil {
		t.Errorf("FlushAll = %v", err)
	}
	_, err = svc.Request(ctx, "req_health")
	checkCode(t, err, errs.NotFound, core.CodeNotFound)

	// Restoring brings back the file as it was deleted, not the edit.
	if _, err := svc.Restore(ctx, del.Entry.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Restore(ctx, delUsers.Entry.ID); err != nil {
		t.Fatal(err)
	}
	clock.Advance(autosave.DefaultMaxDelay)
	if got := readFile(t, dir, "health.json"); got != before {
		t.Errorf("health.json after the restore:\n%s\nwant:\n%s", got, before)
	}
	if got := getRequest(t, svc, "req_me").Query; got != "{ me { id } }" {
		t.Errorf("req_me query after the restore = %q", got)
	}
}

func TestRestoreDropsStalePendingEdits(t *testing.T) {
	t.Parallel()
	svc, _, dir, clock, _ := newAutosaveService(t)
	ctx := t.Context()
	before := readFile(t, dir, "health.json")

	del, err := svc.Delete(ctx, "req_health")
	if err != nil {
		t.Fatal(err)
	}
	// An edit left for the deleted ID, which the operations never keep:
	// restoring must not write it over the restored file.
	stale := getRequestFile(t, before)
	svc.mu.Lock()
	svc.saver.Put(saveKey{svc.tree, "req_health"}, pendingSave{file: stale, revision: 9, workspaceID: "ws_0123456789abcdef"})
	svc.mu.Unlock()

	if _, err := svc.Restore(ctx, del.Entry.ID); err != nil {
		t.Fatal(err)
	}
	clock.Advance(autosave.DefaultMaxDelay)
	if got := readFile(t, dir, "health.json"); got != before {
		t.Errorf("health.json after the restore:\n%s\nwant:\n%s", got, before)
	}
	if len(svc.saver.Keys()) != 0 {
		t.Errorf("pending after the restore: %v", svc.saver.Keys())
	}
}

// getRequestFile decodes data and changes its url, to tell it apart.
func getRequestFile(t *testing.T, data string) projectfile.File {
	t.Helper()
	f, err := projectfile.Decode([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	r, ok := f.(*projectfile.HTTPRequest)
	if !ok {
		t.Fatalf("decoded %T", f)
	}
	r.URL = "/stale"
	return r
}

func TestRenameStopsWhenTheFlushFails(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not stop writes on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	svc, _, dir, _, ev := newAutosaveService(t)
	ctx := t.Context()
	users := filepath.Join(dir, "users")
	before := readFile(t, dir, "users/me.json")

	me := getRequest(t, svc, "req_me")
	me.Name = "Me"
	update(t, svc, me, 1)
	if err := os.Chmod(users, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(users, 0o755) })

	_, err := svc.Rename(ctx, RenameInputDTO{ID: "req_me", Name: "Profile"})
	checkCode(t, err, errs.PermissionDenied, projectfile.CodeSaveFailed)
	if _, failed := ev.counts(); failed != 1 {
		t.Errorf("save-failed events = %d, want 1", failed)
	}
	if readFile(t, dir, "users/me.json") != before {
		t.Error("users/me.json changed")
	}
	if got := getRequest(t, svc, "req_me").Name; got != "Me" {
		t.Errorf("Request().Name = %q, want the pending edit", got)
	}

	if err := os.Chmod(users, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Rename(ctx, RenameInputDTO{ID: "req_me", Name: "Profile"}); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, dir, "users/profile.json")
	if !strings.Contains(got, `"name": "Profile"`) {
		t.Errorf("users/profile.json:\n%s", got)
	}
}
