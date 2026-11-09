package collection

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/projectzip"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// zipTree renders folder node id and everything inside as JSON as a user
// sees it, without what an import changes by design: IDs, order members
// (the items' order is kept), and secrets (the values and their local
// marks). Every request kind is included.
func zipTree(t *testing.T, svc *CollectionService, id string) string {
	t.Helper()
	svc.mu.Lock()
	defer svc.mu.Unlock()
	clean := func(f projectfile.File, name string) map[string]any {
		f, _ = requestsecrets.SplitFile(f)
		data, err := projectfile.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		delete(m, "id")
		delete(m, "order")
		m["name"] = name
		if auth, ok := m["auth"].(map[string]any); ok {
			delete(auth, "localSecrets")
		}
		return m
	}
	var walk func(id string) any
	walk = func(id string) any {
		n := svc.tree.Node(id)
		if n.File == nil {
			return string(n.Kind) + ":" + n.DisplayName()
		}
		m := clean(n.File, n.DisplayName())
		if n.IsContainer() {
			var items []any
			for _, ch := range n.Children {
				items = append(items, walk(ch.ID))
			}
			m["items"] = items
		}
		return m
	}
	data, err := json.MarshalIndent(walk(id), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// unzipAll returns the decompressed content of every file of the zip at
// path.
func unzipAll(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = string(b)
	}
	return out
}

// A folder exported as a zip and imported again gives the same tree, with
// every request kind and folder setting, minus the secrets: those kept on
// this computer and those typed into the files by hand are in no file of
// the zip.
func TestExportFolderZipRoundTrip(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	save := &fakeSave{path: filepath.Join(tempDir(t), "users.zip")}
	svc := New(manager, nil, nil, WithSaveDialog(save.pick))
	ctx := t.Context()
	for _, name := range []string{"broken.json", "stream.json"} {
		if err := os.Remove(filepath.Join(dir, "users", name)); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, dir, "users/_folder.json", `{"kind": "folder", "id": "fld_users", "name": "Users", "description": "Notes.",
  "order": ["tcp.json", "admins", "me.json"],
  "variables": [{"name": "vault", "value": "hand-folder-var", "secret": true}, {"name": "plain", "value": "not-a-secret"}],
  "headers": [{"name": "Accept", "value": "application/json"}],
  "auth": {"type": "apikey", "key": "X-Api-Key", "value": "hand-folder-apikey"},
  "grpc": {"proto": {"files": ["proto/shop.proto"], "importPaths": ["proto"]}},
  "server": {"type": "echo_http", "port": 8080}, "x-note": "kept"}`)
	writeFile(t, dir, "users/get.json", `{"kind": "http", "id": "req_get", "name": "Get", "method": "GET", "url": "{{base}}/u",
  "headers": [{"name": "X-A", "value": "1", "disabled": true}], "auth": {"type": "bearer", "token": "hand-bearer-token"}, "x-custom": 1}`)
	writeFile(t, dir, "users/tcp.json", `{"kind": "tcp", "id": "req_tcp", "name": "TCP", "address": "localhost:7"}`)
	writeFile(t, dir, "users/ws.json", `{"kind": "websocket", "id": "req_ws", "name": "Socket", "url": "wss://h/ws"}`)
	writeFile(t, dir, "users/io.json", `{"kind": "socketio", "id": "req_io", "name": "IO", "url": "https://h"}`)
	writeFile(t, dir, "users/rpc.json", `{"kind": "grpc", "id": "req_rpc", "name": "RPC", "address": "localhost:50051", "proto": {"inherit": true}}`)
	writeFile(t, dir, "users/admins/_folder.json", `{"kind": "folder", "id": "fld_admins", "name": "Admins", "auth": {"type": "inherit"}}`)
	writeFile(t, dir, "users/admins/events.json", `{"kind": "sse", "id": "req_sse", "name": "Events", "url": "https://h/events", "auth": {"type": "inherit"}}`)
	if _, err := svc.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	// Secrets entered in the editors live in the local side files.
	sse := getRequest(t, svc, "req_sse")
	sse.Auth = AuthDTO{Type: "basic", Username: "ada", Password: "typed-sse-password"}
	update(t, svc, sse, 1)
	gql := getRequest(t, svc, "req_me")
	gql.Auth = AuthDTO{Type: "bearer", Token: "typed-graphql-token"}
	update(t, svc, gql, 1)
	if err := svc.FlushAll(ctx); err != nil {
		t.Fatal(err)
	}
	secrets := []string{"hand-folder-var", "hand-folder-apikey", "hand-bearer-token", "typed-sse-password", "typed-graphql-token"}

	res, err := svc.ExportFolderZip(ctx, ExportZipInputDTO{ID: "fld_users"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Saved || res.Path != save.path || res.Name != "Users" || res.Folders != 2 || res.Requests != 7 || res.Skipped != 0 || res.SecretsRemoved != 3 {
		t.Errorf("result = %+v", res)
	}
	if len(save.dialogs) != 1 || save.dialogs[0].Filename != "Users.zip" {
		t.Errorf("dialogs = %+v", save.dialogs)
	}
	files := unzipAll(t, save.path)
	if len(files) < 9 {
		t.Errorf("zip = %v", files)
	}
	for name, content := range files {
		for _, s := range secrets {
			if strings.Contains(content, s) {
				t.Errorf("%s holds the secret %q", name, s)
			}
		}
	}
	if !strings.Contains(files["users/_folder.json"], "not-a-secret") {
		t.Errorf("users/_folder.json =\n%s", files["users/_folder.json"])
	}

	// Dropping the zip on the window detects it.
	det, err := svc.DetectImport(ctx, DetectImportInputDTO{Paths: []string{save.path}})
	if err != nil || len(det) != 1 || det[0].Format.ID != projectzip.FormatID || det[0].Confidence != importer.Sure {
		t.Fatalf("detect = %+v, %v", det, err)
	}
	preview, err := svc.PreviewImport(ctx, PreviewImportInputDTO{Path: save.path})
	if err != nil {
		t.Fatal(err)
	}
	back, err := svc.Import(ctx, ImportInputDTO{Path: save.path, Digest: preview.Digest, Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	if c := back.Report.Counts; c.Folders != 2 || c.Requests != 7 || c.Failed != 0 || c.Skipped != 0 {
		t.Errorf("import counts = %+v", c)
	}
	if back.NodeID == "" || back.NodeID == "fld_users" {
		t.Errorf("import = %+v", back.NodeID)
	}
	want, got := zipTree(t, svc, "fld_users"), zipTree(t, svc, back.NodeID)
	// The import is named after the folder, with a free name.
	got = strings.Replace(got, `"name": "Users 2"`, `"name": "Users"`, 1)
	if want != got {
		t.Errorf("round trip differs:\nwant %s\ngot  %s", want, got)
	}
	// The imported folder's files hold no secret either (the originals
	// keep the hand-typed ones until their next save).
	svc.mu.Lock()
	imported := filepath.Join(dir, filepath.FromSlash(svc.tree.Node(back.NodeID).Path))
	svc.mu.Unlock()
	if n := assertProjectFree(t, imported, secrets); n != 9 {
		t.Errorf("scanned %d files of the import", n)
	}
}

func TestExportFolderZipErrors(t *testing.T) {
	t.Parallel()
	manager, _ := newProject(t)
	ctx := t.Context()
	if _, err := New(manager, nil, nil).ExportFolderZip(ctx, ExportZipInputDTO{ID: "fld_users"}); err == nil {
		t.Error("export without a save dialog did not fail")
	}
	save := &fakeSave{}
	svc := New(manager, nil, nil, WithSaveDialog(save.pick))
	for _, id := range []string{"", "req_health", "nope"} {
		_, err := svc.ExportFolderZip(ctx, ExportZipInputDTO{ID: id})
		checkCode(t, err, errs.NotFound, "collection.not_found")
	}
	// A cancelled dialog writes nothing but still reports.
	res, err := svc.ExportFolderZip(ctx, ExportZipInputDTO{ID: "fld_users"})
	if err != nil || res.Saved || res.Skipped != 2 || res.Warnings != 2 {
		t.Errorf("cancelled = %+v, %v", res, err)
	}
}

// A crafted zip is refused as a whole: nothing is written.
func TestImportUnsafeZip(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	svc := New(manager, nil, nil)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"users/_export.json":    `{"format": "postsonde-folder", "formatVersion": 1, "schemaVersion": 1}`,
		"users/_folder.json":    `{"kind": "folder", "id": "fld_x", "name": "X", "auth": {"type": "inherit"}}`,
		"users/../../evil.json": `{"kind": "http", "id": "req_evil", "name": "Evil", "method": "GET", "url": "/"}`,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tempDir(t), "evil.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Import(t.Context(), ImportInputDTO{Path: path, Index: -1})
	checkCode(t, err, errs.InvalidArgument, projectzip.CodeUnsafeEntry)
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("the project changed: %v -> %v", before, after)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.json")); err == nil {
		t.Error("evil.json was written outside the project")
	}
}
