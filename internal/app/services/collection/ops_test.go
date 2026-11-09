package collection

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// events records emitted events.
type events struct {
	mu   sync.Mutex
	list []ChangedEventDTO
}

func (e *events) emit(name string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if name != EventChanged {
		panic("unexpected event " + name)
	}
	e.list = append(e.list, data.(ChangedEventDTO))
}

func (e *events) last(t *testing.T) ChangedEventDTO {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.list) == 0 {
		t.Fatal("no event")
	}
	return e.list[len(e.list)-1]
}

func (e *events) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.list)
}

func nodeByID(tree TreeDTO, id string) *NodeDTO {
	for i := range tree.Nodes {
		if tree.Nodes[i].ID == id {
			return &tree.Nodes[i]
		}
	}
	return nil
}

func TestTreeOperations(t *testing.T) {
	t.Parallel()
	manager, _ := newProject(t)
	ev := &events{}
	svc := New(manager, ev.emit, nil)
	ctx := t.Context()
	wsID := manager.Current().ID()

	// Create a folder at the top, a request in it.
	res, err := svc.CreateFolder(ctx, CreateFolderInputDTO{ParentID: "", Name: "Orders", Index: 0})
	if err != nil {
		t.Fatal(err)
	}
	folderID := res.NodeID
	if n := nodeByID(res.Tree, folderID); n == nil || n.Path != "orders" || n.Name != "Orders" || res.Tree.RootIDs[0] != folderID {
		t.Errorf("created folder = %+v, root %q", n, res.Tree.RootIDs)
	}
	if got := ev.last(t); got != (ChangedEventDTO{WorkspaceID: wsID, Op: "create", NodeID: folderID}) {
		t.Errorf("event = %+v", got)
	}
	res, err = svc.CreateRequest(ctx, CreateRequestInputDTO{ParentID: folderID, Name: "List orders", Kind: "graphql", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	reqID := res.NodeID
	if n := nodeByID(res.Tree, reqID); n == nil || n.Path != "orders/list-orders.json" || n.Method != "POST" || *n.ParentID != folderID {
		t.Errorf("created request = %+v", n)
	}

	// Rename, duplicate, move.
	res, err = svc.Rename(ctx, RenameInputDTO{ID: reqID, Name: "All orders"})
	if err != nil || res.NodeID != reqID || nodeByID(res.Tree, reqID).Path != "orders/all-orders.json" {
		t.Errorf("Rename = %+v, %v", res.NodeID, err)
	}
	res, err = svc.Duplicate(ctx, reqID)
	if err != nil || res.NodeID == reqID || nodeByID(res.Tree, res.NodeID).Name != "All orders copy" {
		t.Errorf("Duplicate = %+v, %v", res.NodeID, err)
	}
	if got := ev.last(t); got.Op != "duplicate" || got.NodeID != res.NodeID {
		t.Errorf("event = %+v", got)
	}
	res, err = svc.Move(ctx, MoveInputDTO{ID: reqID, ParentID: "", Index: -1})
	if err != nil || nodeByID(res.Tree, reqID).ParentID != nil || res.Tree.RootIDs[len(res.Tree.RootIDs)-1] != reqID {
		t.Errorf("Move = %+v, %v", res.Tree.RootIDs, err)
	}

	// Delete into the local trash, list it, restore it.
	del, err := svc.Delete(ctx, folderID)
	if err != nil {
		t.Fatal(err)
	}
	if del.Entry.NodeID != folderID || del.Entry.Name != "Orders" || del.Entry.ParentID != nil || del.Entry.DeletedAt == "" || nodeByID(del.Tree, folderID) != nil {
		t.Errorf("Delete = %+v", del.Entry)
	}
	if got := ev.last(t); got != (ChangedEventDTO{WorkspaceID: wsID, Op: "delete", NodeID: folderID, TrashID: del.Entry.ID}) {
		t.Errorf("event = %+v", got)
	}
	trashDir := filepath.Join(manager.Current().LocalDir, TrashDirName, del.Entry.ID)
	if _, err := os.Stat(filepath.Join(trashDir, core.TrashDataDir, "orders", "_folder.json")); err != nil {
		t.Error(err)
	}
	list, err := svc.ListTrash(ctx)
	if err != nil || len(list) != 1 || list[0] != del.Entry {
		t.Errorf("ListTrash = %+v, %v", list, err)
	}
	rest, err := svc.Restore(ctx, del.Entry.ID)
	if err != nil || rest.NodeID != folderID || rest.ToRoot || nodeByID(rest.Tree, folderID) == nil || rest.Tree.RootIDs[0] != folderID {
		t.Errorf("Restore = %+v, %v", rest, err)
	}
	if got := ev.last(t); got.Op != "restore" || got.TrashID != del.Entry.ID {
		t.Errorf("event = %+v", got)
	}

	// The cached tree is what is on disk.
	reloaded, err := svc.Reload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := mustJSON(t, rest.Tree), mustJSON(t, reloaded); a != b {
		t.Errorf("tree after restore:\n%s\nreloaded:\n%s", a, b)
	}

	// Purge: nothing old enough, then everything.
	if _, err := svc.Delete(ctx, reqID); err != nil {
		t.Fatal(err)
	}
	n := ev.count()
	if got, err := svc.PurgeTrash(ctx, int(time.Hour/time.Millisecond)); err != nil || got != 0 || ev.count() != n {
		t.Errorf("PurgeTrash(1h) = %d, %v", got, err)
	}
	if got, err := svc.PurgeTrash(ctx, 0); err != nil || got != 1 {
		t.Errorf("PurgeTrash(0) = %d, %v", got, err)
	}
	if got := ev.last(t); got != (ChangedEventDTO{WorkspaceID: wsID, Op: "purge_trash"}) {
		t.Errorf("event = %+v", got)
	}
	if list, err := svc.ListTrash(ctx); err != nil || list == nil || len(list) != 0 {
		t.Errorf("ListTrash = %+v, %v", list, err)
	}
	_, err = svc.PurgeTrash(ctx, -1)
	checkCode(t, err, errs.InvalidArgument, "")
}

func TestTreeOperationErrors(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	ev := &events{}
	svc := New(manager, ev.emit, nil)
	ctx := t.Context()
	if _, err := svc.Tree(ctx); err != nil {
		t.Fatal(err)
	}

	// A failed operation emits nothing and makes the next call read the
	// files again.
	writeFile(t, dir, "added.json", `{"kind": "http", "id": "req_added", "method": "GET"}`)
	_, err := svc.Rename(ctx, RenameInputDTO{ID: "req_missing", Name: "X"})
	checkCode(t, err, errs.NotFound, core.CodeNotFound)
	_, err = svc.Rename(ctx, RenameInputDTO{ID: "req_ws", Name: "X"})
	checkCode(t, err, errs.Unsupported, core.CodeReadOnly)
	_, err = svc.Move(ctx, MoveInputDTO{ID: "fld_users", ParentID: "fld_users", Index: 0})
	checkCode(t, err, errs.InvalidArgument, core.CodeInvalidTarget)
	_, err = svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "X", Kind: "mqtt", Index: -1})
	checkCode(t, err, errs.InvalidArgument, core.CodeInvalidKind)
	_, err = svc.Restore(ctx, "trash_0000000000000000")
	checkCode(t, err, errs.NotFound, core.CodeTrashNotFound)
	if ev.count() != 0 {
		t.Errorf("events = %d", ev.count())
	}
	if tree, _ := svc.Tree(ctx); nodeByID(tree, "req_added") == nil {
		t.Error("the tree was not read again after a failure")
	}

	// Cancelled before the call.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = svc.CreateFolder(cancelled, CreateFolderInputDTO{Name: "X"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}

	// Without a project.
	if _, err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Delete(ctx, "req_health")
	checkCode(t, err, errs.NotFound, coreworkspace.CodeNoWorkspace)
	_, err = svc.ListTrash(ctx)
	checkCode(t, err, errs.NotFound, coreworkspace.CodeNoWorkspace)
	_, err = svc.PurgeTrash(ctx, 0)
	checkCode(t, err, errs.NotFound, coreworkspace.CodeNoWorkspace)
}

func TestTrashDTOs(t *testing.T) {
	t.Parallel()
	deleted := time.Date(2026, 9, 16, 10, 30, 0, 0, time.FixedZone("CEST", 2*3600))
	entries := []TrashEntryDTO{
		toTrashEntryDTO(core.TrashEntry{
			ID: "trash_0123456789abcdef", NodeID: "req_me", Kind: core.KindRequest, FileKind: "graphql",
			Name: "Me", Entry: "me.json", Path: "users/me.json", ParentID: "fld_users", Index: 2, DeletedAt: deleted,
		}),
		toTrashEntryDTO(core.TrashEntry{ID: "trash_fedcba9876543210", NodeID: "fld_users", Kind: core.KindFolder, FileKind: "folder", Name: "Users", Entry: "users", Path: "users"}),
	}
	event := ChangedEventDTO{WorkspaceID: "ws_0123456789abcdef", Op: "delete", NodeID: "req_me", TrashID: "trash_0123456789abcdef"}
	golden.Assert(t, "dto/trash", []byte(mustJSON(t, map[string]any{"entries": entries, "event": event})+"\n"))
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
