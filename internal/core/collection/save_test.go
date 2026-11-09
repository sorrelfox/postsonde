package collection

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Loading the sample project and saving every file leaves every file
// byte-identical, and since nothing changed nothing is written at all
// (modification times, no temp files).
func TestSaveAllLeavesSampleUnchanged(t *testing.T) {
	t.Parallel()
	dir := copySample(t)
	before := snapshot(t, dir)

	tree, problems := Load(dir)
	if len(problems) != 0 {
		t.Fatalf("problems = %+v", problems)
	}
	written, err := tree.SaveAll()
	if err != nil || written != 0 {
		t.Fatalf("SaveAll = %d, %v; want 0, nil", written, err)
	}
	if d := diffSnapshots(before, snapshot(t, dir)); len(d) != 0 {
		t.Errorf("SaveAll changed files:\n%s", strings.Join(d, "\n"))
	}

	// Every writable file really is in the canonical encoding, so the
	// comparison above is not vacuous: encoding it gives the same bytes.
	n := 0
	tree.Walk(func(node *Node) bool {
		if node.File == nil || node.ReadOnly != Writable {
			return true
		}
		data, err := projectfile.Marshal(node.File)
		if err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, dir, node.FilePath()); got != string(data) {
			t.Errorf("%s re-encodes differently:\n%s", node.FilePath(), data)
		}
		n++
		return true
	})
	if n != 14 {
		t.Errorf("checked %d files, want 14", n)
	}
}

func TestSaveRequest(t *testing.T) {
	t.Parallel()
	dir := copySample(t)
	tree, _ := Load(dir)
	const id = "req_c93e1a7b40f2d856"
	before := snapshot(t, dir)

	// The same content: nothing is written.
	orig := tree.Node(id).File.(*projectfile.HTTPRequest)
	same := *orig
	if written, err := tree.SaveRequest(id, &same); err != nil || written {
		t.Fatalf("SaveRequest(unchanged) = %v, %v", written, err)
	}
	if d := diffSnapshots(before, snapshot(t, dir)); len(d) != 0 {
		t.Errorf("unchanged save wrote:\n%s", strings.Join(d, "\n"))
	}

	// A change is written, keeps unknown members, and only that file changes.
	changed := *orig
	changed.Name = "Create a user"
	changed.Headers = append([]projectfile.KeyValue(nil), orig.Headers...)
	changed.Headers[0].Value = "application/problem+json"
	if written, err := tree.SaveRequest(id, &changed); err != nil || !written {
		t.Fatalf("SaveRequest(changed) = %v, %v", written, err)
	}
	if d := diffSnapshots(before, snapshot(t, dir)); len(d) != 2 || !strings.HasPrefix(d[0], "users/create-user.json:") || !strings.HasPrefix(d[1], "users:") {
		t.Errorf("changed files = %q, want users/create-user.json (and its directory)", d)
	}
	if n := tree.Node(id); n.Name != "Create a user" || n.File != &changed {
		t.Errorf("node after save = %+v", n)
	}
	again, _ := Load(dir)
	if r := again.Node(id).File.(*projectfile.HTTPRequest); r.Name != "Create a user" || r.Headers[0].Value != "application/problem+json" {
		t.Errorf("reloaded = %+v", r)
	}

	// Switching the kind is allowed.
	gql := &projectfile.GraphQLRequest{ID: id, Name: "Now GraphQL", URL: "/graphql", Query: "{ a }", Auth: projectfile.Auth{Type: projectfile.AuthInherit}}
	if written, err := tree.SaveRequest(id, gql); err != nil || !written || tree.Node(id).FileKind != projectfile.KindGraphQL {
		t.Fatalf("SaveRequest(graphql) = %v, %v", written, err)
	}
}

func TestSaveUnknownMembersKept(t *testing.T) {
	t.Parallel()
	dir := copySample(t)
	tree, _ := Load(dir)
	n := tree.NodeAt("users/hand-edited.json")
	r := *n.File.(*projectfile.HTTPRequest)
	r.URL = "https://example.com/changed"
	if _, err := tree.SaveRequest(n.ID, &r); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, dir, "users/hand-edited.json")
	for _, want := range []string{`"aNote": "kept"`, `"zFuture": {`, `"origin": "import"`, `"retries": 3`, `"prefix": "Token"`, `"mode": "graphql-multipart"`} {
		if !strings.Contains(got, want) {
			t.Errorf("saved file lost %s:\n%s", want, got)
		}
	}

	folder := tree.NodeAt("users")
	f := *folder.File.(*projectfile.Folder)
	f.Description = "Changed"
	if _, err := tree.SaveFolder(folder.ID, &f); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dir, "users/_folder.json"); !strings.Contains(got, `"color": "teal"`) || !strings.Contains(got, `"disabled": true`) {
		t.Errorf("saved folder lost members:\n%s", got)
	}
}

func TestSaveFolderOrder(t *testing.T) {
	t.Parallel()
	dir := copySample(t)
	tree, _ := Load(dir)
	folder := tree.NodeAt("users")

	// A new order is written and applied; stale names are dropped.
	f := *folder.File.(*projectfile.Folder)
	f.Order = []string{"upload-avatar.json", "gone.json", "admins"}
	if written, err := tree.SaveFolder(folder.ID, &f); err != nil || !written {
		t.Fatalf("SaveFolder = %v, %v", written, err)
	}
	var entries []string
	for _, c := range folder.Children {
		entries = append(entries, c.Entry)
	}
	if got := strings.Join(entries, " "); got != "upload-avatar.json admins create-user.json hand-edited.json user-by-id.json" {
		t.Errorf("children = %s", got)
	}
	if got := folder.Order(); strings.Join(got, " ") != "upload-avatar.json admins" {
		t.Errorf("order = %q", got)
	}
	if f.Order[1] != "gone.json" {
		t.Error("SaveFolder changed the caller's order")
	}
	again, _ := Load(dir)
	if got := again.NodeAt("users").Order(); strings.Join(got, " ") != "upload-avatar.json admins" {
		t.Errorf("reloaded order = %q", got)
	}

	// The root's order.
	root := tree.Root()
	w := *root.File.(*projectfile.Workspace)
	w.Order = []string{"orders", "users"}
	if written, err := tree.SaveWorkspace(&w); err != nil || !written {
		t.Fatalf("SaveWorkspace = %v, %v", written, err)
	}
	if root.Children[0].Entry != "orders" || root.Children[2].Entry != "gemini-echo.json" {
		t.Errorf("root children = %v", root.Children)
	}
}

func TestSaveStaleOrderIsDroppedByTheNextSave(t *testing.T) {
	t.Parallel()
	dir := tempDir(t)
	writeFile(t, dir, "postsonde.json", "{\n  \"schemaVersion\": 1,\n  \"kind\": \"workspace\",\n  \"id\": \"ws_1\",\n  \"name\": \"\",\n  \"order\": [\n    \"gone.json\"\n  ]\n}\n")
	tree, _ := Load(dir)
	if written, err := tree.SaveAll(); err != nil || written != 1 {
		t.Fatalf("SaveAll = %d, %v", written, err)
	}
	if got := readFile(t, dir, "postsonde.json"); strings.Contains(got, "order") {
		t.Errorf("postsonde.json kept a stale order:\n%s", got)
	}
}

func TestSaveErrors(t *testing.T) {
	t.Parallel()
	dir := copySample(t)
	writeFile(t, dir, "broken.json", `{"kind": "http",`)
	writeFile(t, dir, "newer.json", `{"schemaVersion": 2, "kind": "http", "id": "req_newer", "method": "GET"}`)
	tree, _ := Load(dir)
	req := tree.Node("req_5a0f93c7e2b14d68")
	health := *req.File.(*projectfile.HTTPRequest)
	folder := tree.NodeAt("users")

	tests := []struct {
		name string
		save func() (bool, error)
		kind errs.Kind
		code string
	}{
		{"unknown id", func() (bool, error) { return tree.SaveRequest("req_nope", &health) }, errs.NotFound, CodeNotFound},
		{"folder as request", func() (bool, error) { return tree.SaveRequest(folder.ID, &health) }, errs.InvalidArgument, CodeWrongNode},
		{"request as folder", func() (bool, error) { return tree.SaveFolder(req.ID, &projectfile.Folder{ID: req.ID}) }, errs.InvalidArgument, CodeWrongNode},
		{"not a request", func() (bool, error) { return tree.SaveRequest(req.ID, &projectfile.Folder{ID: req.ID}) }, errs.InvalidArgument, CodeWrongNode},
		{"nil request", func() (bool, error) { return tree.SaveRequest(req.ID, (*projectfile.HTTPRequest)(nil)) }, errs.InvalidArgument, CodeWrongNode},
		{"other id", func() (bool, error) {
			c := health
			c.ID = "req_other"
			return tree.SaveRequest(req.ID, &c)
		}, errs.InvalidArgument, CodeWrongNode},
		{"unknown kind", func() (bool, error) {
			return tree.SaveRequest("req_e0b1c2d3a4f56789", &projectfile.HTTPRequest{ID: "req_e0b1c2d3a4f56789", Method: "GET"})
		}, errs.Unsupported, CodeReadOnly},
		{"invalid file", func() (bool, error) {
			n := tree.NodeAt("broken.json")
			return tree.SaveRequest(n.ID, &projectfile.HTTPRequest{ID: n.ID, Method: "GET"})
		}, errs.Unsupported, CodeReadOnly},
		{"newer version", func() (bool, error) {
			return tree.SaveRequest("req_newer", &projectfile.HTTPRequest{ID: "req_newer", Method: "GET"})
		}, errs.Unsupported, CodeReadOnly},
		{"marshal refuses", func() (bool, error) {
			c := health
			c.Method = ""
			return tree.SaveRequest(req.ID, &c)
		}, errs.InvalidArgument, projectfile.CodeInvalidValue},
		{"workspace with another id", func() (bool, error) { return tree.SaveWorkspace(&projectfile.Workspace{ID: "ws_other"}) }, errs.InvalidArgument, CodeWrongNode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := snapshot(t, dir)
			written, err := tt.save()
			if written {
				t.Error("written = true on error")
			}
			checkErr(t, err, tt.kind, tt.code)
			if d := diffSnapshots(before, snapshot(t, dir)); len(d) != 0 {
				t.Errorf("a failed save changed files:\n%s", strings.Join(d, "\n"))
			}
		})
	}
	if tree.Node(req.ID).File.(*projectfile.HTTPRequest).Method != "GET" {
		t.Error("a failed save changed the node")
	}

	// SaveAll skips the read-only files and writes nothing else.
	before := snapshot(t, dir)
	if written, err := tree.SaveAll(); err != nil || written != 0 {
		t.Errorf("SaveAll = %d, %v", written, err)
	}
	if d := diffSnapshots(before, snapshot(t, dir)); len(d) != 0 {
		t.Errorf("SaveAll changed files:\n%s", strings.Join(d, "\n"))
	}
}

func TestSaveInNewerProjectIsRefused(t *testing.T) {
	t.Parallel()
	dir := tempDir(t)
	writeFile(t, dir, "postsonde.json", `{"schemaVersion": 5, "kind": "workspace", "id": "ws_1"}`)
	writeFile(t, dir, "a.json", `{"kind": "http", "id": "req_a", "method": "GET"}`)
	tree, _ := Load(dir)
	if tree.ReadOnly() != ReadOnlyNewerVersion {
		t.Fatalf("ReadOnly = %q", tree.ReadOnly())
	}
	_, err := tree.SaveRequest("req_a", tree.Node("req_a").File)
	checkErr(t, err, errs.Unsupported, CodeReadOnly)
	_, err = tree.SaveAll()
	checkErr(t, err, errs.Unsupported, CodeReadOnly)
	if got := readFile(t, dir, "a.json"); got != `{"kind": "http", "id": "req_a", "method": "GET"}` {
		t.Errorf("a.json = %s", got)
	}
}

func TestSaveWriteFails(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix permissions and a non-root user")
	}
	dir := tempDir(t)
	writeFile(t, dir, "postsonde.json", `{"kind": "workspace", "id": "ws_1"}`)
	writeFile(t, dir, "sub/_folder.json", `{"kind": "folder", "id": "fld_1"}`)
	writeFile(t, dir, "sub/a.json", `{"kind": "http", "id": "req_a", "method": "GET"}`)
	tree, _ := Load(dir)
	sub := filepath.Join(dir, "sub")
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	written, err := tree.SaveAll()
	// The root's file is written; the two in sub fail, both reported.
	if written != 1 || err == nil || strings.Count(err.Error(), "could not be saved") != 2 {
		t.Fatalf("SaveAll = %d, %v", written, err)
	}
	checkErr(t, err, errs.PermissionDenied, projectfile.CodeSaveFailed)
}
