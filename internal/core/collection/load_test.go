package collection

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// testdata/sample is a project with:
//
//   - nested folders (users/admins/on-call), manual order with unlisted
//     items after the listed ones (root, users) and no order (orders),
//     sorted ignoring case (orders: all-orders before Cancel-order);
//   - http and graphql requests, disabled rows, every body mode, unknown
//     members at every level (users/_folder.json, users/hand-edited.json);
//   - an unknown kind from a newer schema written in a non-canonical
//     format (gemini-echo.json);
//   - things that are not in the tree: a .json file without kind
//     (notes.json), a directory without _folder.json (fixtures), a reserved
//     directory (_drafts), environments, a README and a .tmp- leftover of an
//     interrupted save.
func TestLoadSample(t *testing.T) {
	t.Parallel()
	dir := copySample(t)
	tree, problems := Load(dir)
	if len(problems) != 0 {
		t.Errorf("problems = %+v, want none", problems)
	}
	golden.Assert(t, "sample_tree", []byte(dump(tree, problems)))

	if n := tree.Node("req_c2e9b4170f8a6d53"); n == nil || n.Path != "users/admins/on-call/page-on-call.json" {
		t.Errorf("Node(page-on-call) = %+v", n)
	}
	n := tree.NodeAt("users/admins")
	if n == nil || n.ID != "fld_5e2c9a7d31b0f486" || n.Parent.ID != "fld_0b8d2e61c4a9f375" {
		t.Fatalf("NodeAt(users/admins) = %+v", n)
	}
	if n.FilePath() != "users/admins/_folder.json" || tree.Abs(n.FilePath()) != filepath.Join(dir, "users", "admins", "_folder.json") {
		t.Errorf("FilePath = %q", n.FilePath())
	}
	if tree.Root().ID != "ws_7c1e4b9a2f3d5e60" || tree.NodeAt("") != tree.Root() || tree.Dir() != dir {
		t.Errorf("root = %+v", tree.Root())
	}
	if tree.Len() != 15 {
		t.Errorf("Len = %d, want 15", tree.Len())
	}
	if r, ok := tree.Node("req_c93e1a7b40f2d856").File.(*projectfile.HTTPRequest); !ok || !r.Params[1].Disabled {
		t.Errorf("create-user file = %#v", tree.Node("req_c93e1a7b40f2d856").File)
	}
}

func TestLoadDoesNotWrite(t *testing.T) {
	t.Parallel()
	dir := copySample(t)
	// Broken files must not be touched either.
	writeFile(t, dir, "broken.json", `{"kind": "http", "id": `)
	writeFile(t, dir, "users/no-id.json", `{"kind": "http", "name": "No id", "method": "GET", "url": "", "body": {"mode": "none"}, "auth": {"type": "inherit"}}`)
	before := snapshot(t, dir)
	Load(dir)
	if d := diffSnapshots(before, snapshot(t, dir)); len(d) != 0 {
		t.Errorf("Load changed files:\n%s", strings.Join(d, "\n"))
	}
}

func TestLoadTolerant(t *testing.T) {
	t.Parallel()
	dir := tempDir(t)
	writeFile(t, dir, "postsonde.json", `{"kind": "workspace", "id": "ws_1", "name": "Tolerant", "order": ["b-folder", "stale.json", "z.json"]}`)
	writeFile(t, dir, "z.json", `{"kind": "http", "id": "req_z", "name": "Z", "method": "GET", "url": "", "body": {"mode": "none"}, "auth": {"type": "inherit"}}`)
	// Not valid JSON: an invalid node with the line and column.
	writeFile(t, dir, "syntax.json", "{\n  \"kind\": \"http\",\n  \"id\": \"req_syntax\"\n  \"name\": \"x\"\n}\n")
	// A member of the wrong type: an invalid node that keeps its id and name.
	writeFile(t, dir, "wrong-type.json", `{"kind": "http", "id": "req_wrong", "name": "Wrong type", "headers": "Accept: */*"}`)
	// An environment among requests.
	writeFile(t, dir, "env.json", `{"kind": "environment", "id": "env_1", "name": "Misplaced"}`)
	// A leftover of an interrupted save, and JSON that is not a project file.
	writeFile(t, dir, ".z.json.tmp-123", `{"kind": "http", "id": "req_z"`)
	writeFile(t, dir, "package.json", `{"name": "not-a-project-file"}`)
	writeFile(t, dir, "list.json", `[1, 2]`)
	writeFile(t, dir, "tsconfig.json", "{\n  // comments are not JSON\n  \"strict\": true\n}\n")
	// A folder whose _folder.json is broken: its requests still load, in
	// the order read best-effort.
	writeFile(t, dir, "b-folder/_folder.json", `{"kind": "folder", "id": "fld_b", "name": "B", "order": ["second.json"], "headers": 7}`)
	writeFile(t, dir, "b-folder/first.json", `{"kind": "graphql", "id": "req_first", "name": "First", "url": "", "query": ""}`)
	writeFile(t, dir, "b-folder/second.json", `{"kind": "graphql", "id": "req_second", "name": "Second", "url": "", "query": ""}`)
	// A directory without _folder.json and its contents are not in the tree.
	writeFile(t, dir, "plain-dir/inner.json", `{"kind": "http", "id": "req_inner", "method": "GET"}`)
	// No id: a derived one, written on the next save.
	writeFile(t, dir, "no-id.json", `{"kind": "http", "name": "No id", "method": "GET", "url": "", "body": {"mode": "none"}, "auth": {"type": "inherit"}}`)
	// From a newer schema version: read-only.
	writeFile(t, dir, "newer.json", `{"schemaVersion": 99, "kind": "http", "id": "req_newer", "name": "Newer", "method": "GET", "url": "", "body": {"mode": "none"}, "auth": {"type": "inherit"}}`)
	// A folder file that is a directory is no folder file.
	writeFile(t, dir, "odd/_folder.json/x.json", `{}`)

	tree, problems := Load(dir)
	golden.Assert(t, "tolerant_tree", []byte(dump(tree, problems)))

	if got := tree.Problems(); len(got) != len(problems) {
		t.Errorf("Tree.Problems = %d problems, Load returned %d", len(got), len(problems))
	}
	if n := tree.Node("req_wrong"); n == nil || n.Kind != KindInvalid || n.Name != "Wrong type" {
		t.Errorf("wrong-type node = %+v", n)
	}
	if n := tree.NodeAt("b-folder"); n == nil || n.Kind != KindFolder || n.File != nil || len(n.Children) != 2 || n.Children[0].Entry != "second.json" {
		t.Errorf("b-folder = %+v", n)
	}
}

func TestLoadDerivedIDsAreStable(t *testing.T) {
	t.Parallel()
	dir := tempDir(t)
	writeFile(t, dir, "postsonde.json", `{"kind": "workspace", "id": "ws_1"}`)
	writeFile(t, dir, "no-id.json", `{"kind": "http", "method": "GET"}`)
	first, _ := Load(dir)
	second, _ := Load(dir)
	a, b := first.NodeAt("no-id.json"), second.NodeAt("no-id.json")
	if a.ID != b.ID || !strings.HasPrefix(a.ID, projectfile.RequestIDPrefix) || len(a.ID) != len("req_")+16 {
		t.Errorf("derived IDs = %q, %q", a.ID, b.ID)
	}
	if r := a.File.(*projectfile.HTTPRequest); r.ID != a.ID {
		t.Errorf("file id = %q, want the node ID %q", r.ID, a.ID)
	}
}

func TestLoadDuplicateIDs(t *testing.T) {
	t.Parallel()
	dir := copySample(t)
	// Copies by hand: a request into another folder, a folder with its
	// content, and a request under a name that sorts before the original.
	for _, c := range [][2]string{
		{"users/create-user.json", "orders/create-user.json"},
		{"users/create-user.json", "health-copy.json"},
		{"users/admins/_folder.json", "orders/admins-copy/_folder.json"},
		{"users/admins/list-admins.json", "orders/admins-copy/list-admins.json"},
	} {
		writeFile(t, dir, c[1], readFile(t, dir, c[0]))
	}
	tree, problems := Load(dir)
	golden.Assert(t, "duplicate_tree", []byte(dump(tree, problems)))

	// The file with the smallest path keeps the id: health-copy.json sorts
	// before users/create-user.json.
	if n := tree.Node("req_c93e1a7b40f2d856"); n == nil || n.Path != "health-copy.json" {
		t.Errorf("keeper = %+v", n)
	}
	for _, rel := range []string{"users/create-user.json", "orders/create-user.json"} {
		n := tree.NodeAt(rel)
		if n.ID == "req_c93e1a7b40f2d856" || !n.IDChanged || tree.Node(n.ID) != n {
			t.Errorf("%s = %+v", rel, n)
		}
	}
	for _, c := range []struct{ id, keeper string }{
		{"fld_5e2c9a7d31b0f486", "orders/admins-copy"},
		{"req_7b3f0e5a9c2d4168", "orders/admins-copy/list-admins.json"},
	} {
		if n := tree.Node(c.id); n == nil || n.Path != c.keeper {
			t.Errorf("Node(%s) = %+v, want %s", c.id, n, c.keeper)
		}
	}
	for _, p := range problems {
		checkErr(t, p.Err, errs.InvalidArgument, CodeDuplicateID)
	}

	// Saving writes the new ids into the copies only, and then the project
	// loads without problems and with the same IDs.
	written, err := tree.SaveAll()
	if err != nil || written != 4 {
		t.Fatalf("SaveAll = %d, %v; want 4 files written", written, err)
	}
	again, problems := Load(dir)
	if len(problems) != 0 {
		t.Fatalf("problems after saving = %+v", problems)
	}
	tree.Walk(func(n *Node) bool {
		if m := again.NodeAt(n.Path); m == nil || m.ID != n.ID || m.IDChanged {
			t.Errorf("after saving %s = %+v, want ID %s", n.Path, m, n.ID)
		}
		return true
	})
}

func TestLoadReadOnlyAndSymlinks(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := tempDir(t)
	outside := tempDir(t)
	writeFile(t, dir, "postsonde.json", `{"kind": "workspace", "id": "ws_1"}`)
	writeFile(t, outside, "linked.json", `{"kind": "http", "id": "req_linked", "name": "Linked", "method": "GET", "url": "", "body": {"mode": "none"}, "auth": {"type": "inherit"}}`)
	writeFile(t, outside, "folder/_folder.json", `{"kind": "folder", "id": "fld_linked"}`)
	for _, l := range [][2]string{
		{filepath.Join(outside, "linked.json"), "linked.json"},
		{filepath.Join(outside, "folder"), "linked-folder"},
		{filepath.Join(outside, "missing.json"), "dangling.json"},
		{dir, "loop"},
	} {
		if err := os.Symlink(l[0], filepath.Join(dir, l[1])); err != nil {
			t.Fatal(err)
		}
	}
	tree, problems := Load(dir)
	if len(problems) != 0 {
		t.Errorf("problems = %+v", problems)
	}
	if tree.Len() != 2 {
		t.Errorf("Len = %d, want the root and linked.json", tree.Len())
	}
	n := tree.Node("req_linked")
	if n == nil || n.ReadOnly != ReadOnlySymlink {
		t.Fatalf("linked.json = %+v", n)
	}
	_, err := tree.SaveRequest(n.ID, n.File)
	checkErr(t, err, errs.Unsupported, CodeReadOnly)
}

func TestLoadUnreadable(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix permissions and a non-root user")
	}
	dir := tempDir(t)
	writeFile(t, dir, "postsonde.json", `{"kind": "workspace", "id": "ws_1"}`)
	writeFile(t, dir, "locked/_folder.json", `{"kind": "folder", "id": "fld_locked", "name": "Locked"}`)
	writeFile(t, dir, "secret.json", `{"kind": "http", "id": "req_secret"}`)
	writeFile(t, dir, "ok.json", `{"kind": "http", "id": "req_ok", "method": "GET"}`)
	for _, p := range []string{"locked", "secret.json"} {
		path := filepath.Join(dir, p)
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
	}
	tree, problems := Load(dir)
	golden.Assert(t, "unreadable_tree", []byte(dump(tree, problems)))
	if tree.Node("req_ok") == nil {
		t.Error("ok.json did not load")
	}
}

func TestLoadMissingProject(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(tempDir(t), "gone")
	tree, problems := Load(dir)
	if tree == nil || tree.Root() == nil || tree.ReadOnly() != ReadOnlyInvalid || len(tree.Root().Children) != 0 {
		t.Fatalf("tree = %+v", tree)
	}
	if len(problems) != 2 {
		t.Fatalf("problems = %+v, want the marker and the directory", problems)
	}
	checkErr(t, problems[0].Err, errs.NotFound, CodeReadDirFailed)
	checkErr(t, problems[1].Err, errs.NotFound, projectfile.CodeReadFailed)
	if _, err := tree.SaveAll(); err == nil {
		t.Error("SaveAll in a project without a workspace file succeeded")
	}
}

func TestLoadTooDeep(t *testing.T) {
	t.Parallel()
	dir := tempDir(t)
	writeFile(t, dir, "postsonde.json", `{"kind": "workspace", "id": "ws_1"}`)
	rel := ""
	for i := range MaxDepth + 2 {
		rel = filepath.Join(rel, "d")
		writeFile(t, dir, filepath.Join(rel, "_folder.json"), `{"kind": "folder", "id": "fld_`+strings.Repeat("x", i+1)+`"}`)
	}
	tree, problems := Load(dir)
	if len(problems) != 1 {
		t.Fatalf("problems = %+v, want one", problems)
	}
	checkErr(t, problems[0].Err, errs.Unsupported, CodeTooDeep)
	if tree.Len() != MaxDepth+2 {
		t.Errorf("Len = %d, want the root and %d folders", tree.Len(), MaxDepth+1)
	}
}

func TestApplyOrder(t *testing.T) {
	t.Parallel()
	nodes := func(entries ...string) []*Node {
		out := make([]*Node, 0, len(entries))
		for _, e := range entries {
			out = append(out, &Node{Entry: e})
		}
		return out
	}
	entries := func(ns []*Node) string {
		var s []string
		for _, n := range ns {
			s = append(s, n.Entry)
		}
		return strings.Join(s, " ")
	}
	tests := []struct {
		name     string
		order    []string
		children []string
		want     string
		kept     []string
	}{
		{"none", nil, []string{"b.json", "A.json", "c"}, "A.json b.json c", nil},
		{"listed first", []string{"c", "b.json"}, []string{"a.json", "b.json", "c", "d.json"}, "c b.json a.json d.json", []string{"c", "b.json"}},
		{"stale and repeated names dropped", []string{"x.json", "b.json", "b.json"}, []string{"a.json", "b.json"}, "b.json a.json", []string{"b.json"}},
		{"case ties by exact name", nil, []string{"b", "B", "a"}, "a B b", nil},
		{"exact match wins", []string{"b"}, []string{"B", "b"}, "b B", []string{"b"}},
		// A hand-written NFC name matches the NFD name macOS lists.
		{"normalization", []string{"Caf\u00e9.json"}, []string{"a.json", "Cafe\u0301.json"}, "Cafe\u0301.json a.json", []string{"Caf\u00e9.json"}},
		{"ambiguous fold matches nothing", []string{"x"}, []string{"X", "x\u0301"}, "X x\u0301", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := entries(applyOrder(tt.order, nodes(tt.children...))); got != tt.want {
				t.Errorf("applyOrder = %q, want %q", got, tt.want)
			}
			got := keptOrder(tt.order, nodes(tt.children...))
			if strings.Join(got, " ") != strings.Join(tt.kept, " ") {
				t.Errorf("keptOrder = %q, want %q", got, tt.kept)
			}
		})
	}
}
