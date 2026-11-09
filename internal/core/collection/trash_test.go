package collection

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func TestRestoreRenewsTakenIDs(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)
	trash := NewTrash(filepath.Join(tempDir(t), "trash"))

	e := must(tree.Delete(idUsers, trash))(t)
	// Meanwhile files with the IDs of a request and a folder inside the
	// deleted folder appear (copied by hand, checked out from git).
	writeFile(t, dir, "aaa.json", `{"kind": "http", "id": "req_c93e1a7b40f2d856", "name": "Hand copy", "method": "GET"}`)
	writeFile(t, dir, "aaa-folder/_folder.json", `{"kind": "folder", "id": "fld_a14d8f02e6c95b37", "name": "Hand folder"}`)
	tree.reload()
	checkNode(t, tree, idCreateUser, "aaa.json", "Hand copy")

	r := must(tree.Restore(trash, e.ID))(t)
	if r.NodeID != idUsers || r.Path != "users" {
		t.Errorf("Restored = %+v", r)
	}
	// The existing items keep their IDs; the restored ones got new ones,
	// written to their files, and the others kept theirs.
	checkNode(t, tree, idCreateUser, "aaa.json", "Hand copy")
	checkNode(t, tree, idOnCall, "aaa-folder", "Hand folder")
	checkNode(t, tree, idListAdmins, "users/admins/list-admins.json", "List admins")
	for _, p := range []string{"users/create-user.json", "users/admins/on-call"} {
		n := tree.NodeAt(p)
		if n == nil || n.ID == idCreateUser || n.ID == idOnCall || n.IDChanged {
			t.Errorf("%s = %+v", p, n)
		}
	}
	checkTree(t, tree, "")
}

func TestTrashListAndPurge(t *testing.T) {
	t.Parallel()
	tree, _ := loadSample(t)
	trashDir := filepath.Join(tempDir(t), "trash")
	trash := NewTrash(trashDir)

	// A missing trash is empty.
	if list := must(trash.List())(t); len(list) != 0 {
		t.Errorf("List = %+v", list)
	}
	if n := must(trash.Purge(time.Now()))(t); n != 0 {
		t.Errorf("Purge = %d", n)
	}

	day := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	trash.now = func() time.Time { return day }
	old := must(tree.Delete(idHealth, trash))(t)
	trash.now = func() time.Time { return day.Add(48 * time.Hour) }
	recent := must(tree.Delete(idOrders, trash))(t)

	// Leftovers of interrupted operations are not listed: an entry
	// without data, one without a manifest, and unrelated files.
	noErr(t, os.MkdirAll(filepath.Join(trashDir, TrashEntryPrefix+"0000000000000001", TrashDataDir), 0o700))
	writeFile(t, trashDir, TrashEntryPrefix+"0000000000000002/"+TrashManifestName, `{"version": 1, "entry": "gone.json", "deletedAt": "2026-01-01T00:00:00Z"}`)
	writeFile(t, trashDir, "notes.txt", "keep")

	list := must(trash.List())(t)
	if len(list) != 2 || list[0] != recent || list[1] != old {
		t.Errorf("List = %+v", list)
	}
	if !list[0].DeletedAt.Equal(day.Add(48 * time.Hour)) {
		t.Errorf("DeletedAt = %v", list[0].DeletedAt)
	}

	// Purging everything deleted before a day after old removes old and
	// the old leftover without data (manifest from January) but not the
	// leftover without a manifest, whose directory is new.
	n := must(trash.Purge(day.Add(24 * time.Hour)))(t)
	if n != 2 {
		t.Errorf("Purge = %d, want 2", n)
	}
	list = must(trash.List())(t)
	if len(list) != 1 || list[0] != recent {
		t.Errorf("List after purge = %+v", list)
	}
	_, err := tree.Restore(trash, old.ID)
	checkErr(t, err, errs.NotFound, CodeTrashNotFound)

	n = must(trash.Purge(time.Now().Add(time.Hour)))(t)
	if n != 2 {
		t.Errorf("Purge = %d, want 2", n)
	}
	if got := dirNames(t, trashDir); !slices.Equal(got, []string{"notes.txt"}) {
		t.Errorf("trash = %q", got)
	}
	checkTree(t, tree, "")
}

func TestTrashInsideProject(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)
	_, err := tree.Delete(idHealth, NewTrash(filepath.Join(dir, "fixtures", "trash")))
	checkErr(t, err, errs.InvalidArgument, CodeTrashFailed)
	_, err = tree.Delete(idHealth, NewTrash(dir))
	checkErr(t, err, errs.InvalidArgument, CodeTrashFailed)
	if tree.Node(idHealth) == nil {
		t.Error("the item was deleted")
	}
}

// The trash may be on another disk: renaming fails with EXDEV and the item
// is copied and removed instead. Not parallel: it replaces renameFunc.
func TestDeleteRestoreAcrossDevices(t *testing.T) {
	tree, dir := loadSample(t)
	trashDir := filepath.Join(tempDir(t), "trash")
	trash := NewTrash(trashDir)
	crossings := 0
	renameFunc = func(from, to string) error {
		if strings.HasPrefix(from, trashDir) != strings.HasPrefix(to, trashDir) {
			crossings++
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EXDEV}
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { renameFunc = os.Rename })

	filesBefore := contents(t, filepath.Join(dir, "users"))
	treeBefore := dump(tree, nil)
	e := must(tree.Delete(idUsers, trash))(t)
	if _, err := os.Stat(filepath.Join(dir, "users")); !os.IsNotExist(err) {
		t.Errorf("users still on disk: %v", err)
	}
	if d := diffContents(filesBefore, contents(t, filepath.Join(trashDir, e.ID, TrashDataDir, "users"))); len(d) != 0 {
		t.Errorf("trash copy differs: %q", d)
	}
	must(tree.Restore(trash, e.ID))(t)
	if crossings != 2 {
		t.Errorf("crossings = %d", crossings)
	}
	if d := diffContents(filesBefore, contents(t, filepath.Join(dir, "users"))); len(d) != 0 {
		t.Errorf("restored files differ: %q", d)
	}
	if got := dump(tree, nil); got != treeBefore {
		t.Errorf("tree after restoring:\n%s", got)
	}

	// Another rename error is reported, and nothing is left in the trash.
	renameFunc = func(from, to string) error {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EACCES}
	}
	_, err := tree.Delete(idHealth, trash)
	checkErr(t, err, errs.KindOf(&os.LinkError{Err: syscall.EACCES}), CodeFileOpFailed)
	if list := must(trash.List())(t); len(list) != 0 {
		t.Errorf("List = %+v", list)
	}
	if names, _ := os.ReadDir(trashDir); len(names) != 0 {
		t.Errorf("trash not cleaned up: %v", names)
	}
	checkNode(t, tree, idHealth, "health.json", "Health check")
}

// A derived ID depends on the path, so it is written into the file before
// the item's path changes.
func TestOperationsKeepDerivedIDs(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)
	trash := NewTrash(filepath.Join(tempDir(t), "trash"))
	writeFile(t, dir, "users/no-id.json", `{"kind": "http", "name": "No id", "method": "GET"}`)
	tree.reload()
	n := tree.NodeAt("users/no-id.json")
	if n == nil || !n.IDChanged {
		t.Fatalf("node = %+v", n)
	}
	id := n.ID
	noErr(t, tree.Move(id, idOrders, End))
	checkNode(t, tree, id, "orders/no-id.json", "No id")
	e := must(tree.Delete(idOrders, trash))(t)
	must(tree.CreateFolder("", "Orders", End))(t)
	must(tree.Restore(trash, e.ID))(t)
	checkNode(t, tree, id, "orders-2/no-id.json", "No id")
	if f, err := projectfile.ReadFile(filepath.Join(dir, "orders-2", "no-id.json")); err != nil || f.(*projectfile.HTTPRequest).ID != id {
		t.Errorf("file = %+v, %v", f, err)
	}
	checkTree(t, tree, "")
}
