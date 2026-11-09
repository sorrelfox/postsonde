package collection

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fswatch"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/fakeclock"
)

func ids(cs []NodeChange) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func mustLoad(t *testing.T, dir string) *Tree {
	t.Helper()
	tree, _ := Load(dir)
	return tree
}

func nodeAt(t *testing.T, tree *Tree, rel string) *Node {
	t.Helper()
	n := tree.NodeAt(rel)
	if n == nil {
		t.Fatalf("no node at %s", rel)
	}
	return n
}

// editRequestName rewrites the name member of the http request file rel,
// as an editor outside PostSonde would.
func editRequestName(t *testing.T, dir, rel, name string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	f, err := projectfile.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	r := f.(*projectfile.HTTPRequest)
	r.Name = name
	if err := projectfile.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), r); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshWithoutChangesReportsNothing(t *testing.T) {
	dir := copySample(t)
	tree := mustLoad(t, dir)
	if c := tree.Refresh(); !c.Empty() {
		t.Fatalf("Refresh of an unchanged project = %+v", c)
	}
	// Touching a file without changing it is not a change either.
	now := time.Now()
	if err := os.Chtimes(filepath.Join(dir, "health.json"), now, now); err != nil {
		t.Fatal(err)
	}
	if c := tree.Refresh(); !c.Empty() {
		t.Fatalf("Refresh after a touch = %+v", c)
	}
}

func TestRefreshIgnoresTheTreesOwnWrites(t *testing.T) {
	dir := copySample(t)
	tree := mustLoad(t, dir)
	n := nodeAt(t, tree, "health.json")
	f := *(n.File.(*projectfile.HTTPRequest))
	f.Name = "Health (edited in the app)"
	if _, err := tree.SaveRequest(n.ID, &f); err != nil {
		t.Fatal(err)
	}
	if _, err := tree.CreateRequest("", "New one", projectfile.KindHTTP, End); err != nil {
		t.Fatal(err)
	}
	if c := tree.Refresh(); !c.Empty() {
		t.Fatalf("Refresh after the tree's own writes = %+v", c)
	}
}

func TestRefreshReportsEditsAddsRemovesAndMoves(t *testing.T) {
	dir := copySample(t)
	tree := mustLoad(t, dir)
	health := nodeAt(t, tree, "health.json").ID
	gone := nodeAt(t, tree, "users/user-by-id.json").ID
	users := nodeAt(t, tree, "users").ID
	createUser := nodeAt(t, tree, "users/create-user.json").ID
	allOrders := nodeAt(t, tree, "orders/all-orders.json").ID

	editRequestName(t, dir, "health.json", "Health (edited outside)")
	if err := os.Remove(filepath.Join(dir, "users", "user-by-id.json")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "new-request.json", `{"version":1,"kind":"http","id":"req_00000000000000aa","name":"New","method":"GET","url":""}`)
	// A rename, and a move into another folder, by hand.
	if err := os.Rename(filepath.Join(dir, "users", "create-user.json"), filepath.Join(dir, "users", "add-user.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "orders", "all-orders.json"), filepath.Join(dir, "all-orders.json")); err != nil {
		t.Fatal(err)
	}

	c := tree.Refresh()
	if got := ids(c.Modified); !slices.Equal(got, []string{health}) {
		t.Errorf("Modified = %v, want [%s]", got, health)
	}
	if got := ids(c.Removed); !slices.Equal(got, []string{gone}) {
		t.Errorf("Removed = %v, want [%s]", got, gone)
	}
	if len(c.Removed) == 1 && (c.Removed[0].Path != "users/user-by-id.json" || c.Removed[0].ParentID != users || c.Removed[0].Old == nil) {
		t.Errorf("Removed[0] = %+v", c.Removed[0])
	}
	if got := ids(c.Added); !slices.Equal(got, []string{"req_00000000000000aa"}) {
		t.Errorf("Added = %v", got)
	}
	moved := ids(c.Moved)
	slices.Sort(moved)
	want := []string{allOrders, createUser}
	slices.Sort(want)
	if !slices.Equal(moved, want) {
		t.Errorf("Moved = %v, want %v", moved, want)
	}
	for _, m := range c.Moved {
		if m.ID == createUser && (m.Path != "users/add-user.json" || m.ParentID != users) {
			t.Errorf("moved create-user = %+v", m)
		}
	}
	if c.Root {
		t.Error("Root set, but postsonde.json did not change")
	}
	if tree.Node(health).Name != "Health (edited outside)" {
		t.Errorf("the tree was not reloaded: name %q", tree.Node(health).Name)
	}
	if !tree.Refresh().Empty() {
		t.Error("a second Refresh reports the same changes again")
	}
}

func TestRefreshReportsTheWorkspaceFile(t *testing.T) {
	dir := copySample(t)
	tree := mustLoad(t, dir)
	w := *(tree.Root().File.(*projectfile.Workspace))
	w.Variables = append(w.Variables, projectfile.Variable{Name: "added", Value: "1"})
	if err := projectfile.WriteFile(filepath.Join(dir, projectfile.WorkspaceFileName), &w); err != nil {
		t.Fatal(err)
	}
	c := tree.Refresh()
	if !c.Root || c.OldRoot == nil {
		t.Fatalf("Root = %v, OldRoot = %v", c.Root, c.OldRoot)
	}
}

func TestWriteRefusesAFileChangedOnDisk(t *testing.T) {
	dir := copySample(t)
	tree := mustLoad(t, dir)
	n := nodeAt(t, tree, "health.json")
	editRequestName(t, dir, "health.json", "Changed outside")
	f := *(n.File.(*projectfile.HTTPRequest))
	f.Name = "Changed in the app"
	_, err := tree.SaveRequest(n.ID, &f)
	checkErr(t, err, errs.IO, CodeChangedOnDisk)
	if got := readFile(t, dir, "health.json"); !strings.Contains(got, "Changed outside") {
		t.Fatalf("the outside edit was overwritten: %s", got)
	}

	// After a refresh the tree has the new version and saving works again.
	tree.Refresh()
	n = tree.Node(n.ID)
	f = *(n.File.(*projectfile.HTTPRequest))
	f.Name = "Changed in the app"
	if _, err := tree.SaveRequest(n.ID, &f); err != nil {
		t.Fatalf("save after Refresh: %v", err)
	}

	// A file removed outside is not brought back by a late save.
	if err := os.Remove(filepath.Join(dir, "health.json")); err != nil {
		t.Fatal(err)
	}
	_, err = tree.SaveRequest(n.ID, &f)
	checkErr(t, err, errs.IO, CodeChangedOnDisk)
	if _, err := os.Stat(filepath.Join(dir, "health.json")); !os.IsNotExist(err) {
		t.Fatalf("the removed file was written again: %v", err)
	}
}

func TestScanListsTheFilesLoadReads(t *testing.T) {
	dir := copySample(t)
	snap, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"postsonde.json",
		"environments/production.json",
		"health.json",
		"users/_folder.json",
		"users/create-user.json",
		"users/admins/on-call/page-on-call.json",
	} {
		if _, ok := snap[p]; !ok {
			t.Errorf("Scan misses %s", p)
		}
	}
	for _, p := range []string{
		".health.json.tmp-4242",
		"_drafts/_folder.json",
		"fixtures/avatar.json", // no _folder.json: not part of the tree
		"README.md",
	} {
		if _, ok := snap[p]; ok {
			t.Errorf("Scan lists %s", p)
		}
	}
	if _, err := Scan(filepath.Join(dir, "missing")); err == nil {
		t.Error("Scan of a missing folder succeeded")
	}
}

// TestWatchingAProject runs a Watcher over Scan with a manual clock, the
// way CollectionService does: an outside edit, add and delete are
// reported once the files settle, and Refresh names the items.
func TestWatchingAProject(t *testing.T) {
	dir := copySample(t)
	tree := mustLoad(t, dir)
	clock := fakeclock.New(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	var got []Changes
	w := fswatch.New(fswatch.Options{
		Interval: time.Second,
		Clock:    clock,
		Scan:     func() (fswatch.Snapshot, error) { return Scan(dir) },
		OnChange: func(fswatch.Changes) { got = append(got, tree.Refresh()) },
	})
	w.Start()
	defer w.Stop()

	// The app's own write: the watcher sees the file change, Refresh finds
	// the hash it wrote, and nothing is reported.
	n := nodeAt(t, tree, "health.json")
	f := *(n.File.(*projectfile.HTTPRequest))
	f.URL = "https://example.test/own-write"
	if _, err := tree.SaveRequest(n.ID, &f); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "health.json"), future, future)
	clock.Advance(3 * time.Second)
	if len(got) != 1 || !got[0].Empty() {
		t.Fatalf("own write: got %+v, want one empty refresh", got)
	}
	got = nil

	editRequestName(t, dir, "users/create-user.json", "Edited outside")
	writeFile(t, dir, "orders/added.json", `{"version":1,"kind":"http","id":"req_00000000000000bb","name":"Added","method":"GET","url":""}`)
	if err := os.Remove(filepath.Join(dir, "users", "user-by-id.json")); err != nil {
		t.Fatal(err)
	}
	later := future.Add(time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "users", "create-user.json"), later, later)
	clock.Advance(time.Second)
	if len(got) != 0 {
		t.Fatalf("reported before the files settled: %+v", got)
	}
	clock.Advance(time.Second)
	if len(got) != 1 {
		t.Fatalf("got %d refreshes, want 1", len(got))
	}
	c := got[0]
	if len(c.Modified) == 0 || len(c.Added) != 1 || len(c.Removed) != 1 {
		t.Fatalf("changes = added %v removed %v modified %v", ids(c.Added), ids(c.Removed), ids(c.Modified))
	}
}
