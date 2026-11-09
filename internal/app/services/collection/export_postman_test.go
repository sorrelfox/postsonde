package collection

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer/postman"
	"github.com/sorrelfox/postsonde/internal/core/localvalues"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// fakeSave answers the save dialog with path, and remembers the dialogs.
type fakeSave struct {
	mu      sync.Mutex
	path    string
	dialogs []SaveDialog
}

func (f *fakeSave) pick(_ context.Context, d SaveDialog) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dialogs = append(f.dialogs, d)
	return f.path, nil
}

// canonicalTree renders the folder node id and everything inside as JSON,
// without IDs, order members and local secret marks, for comparisons.
func canonicalTree(t *testing.T, svc *CollectionService, id string) string {
	t.Helper()
	svc.mu.Lock()
	defer svc.mu.Unlock()
	var walk func(id string) any
	walk = func(id string) any {
		n := svc.tree.Node(id)
		switch f := n.File.(type) {
		case *projectfile.Folder:
			c := *f
			c.ID, c.Order, c.Auth.LocalSecrets = "", nil, nil
			var items []any
			for _, ch := range n.Children {
				items = append(items, walk(ch.ID))
			}
			return map[string]any{"folder": c, "items": items}
		case *projectfile.HTTPRequest:
			c := *f
			c.ID, c.Auth.LocalSecrets = "", nil
			return &c
		case *projectfile.GraphQLRequest:
			c := *f
			c.ID, c.Auth.LocalSecrets = "", nil
			return &c
		}
		return n.Kind
	}
	data, err := json.MarshalIndent(walk(id), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A Postman collection imported into a project, exported and imported
// again gives the same folders and requests, and the export holds none of
// the secrets the first import kept on this computer.
func TestExportPostmanRoundTrip(t *testing.T) {
	t.Parallel()
	manager, _ := newProject(t)
	save := &fakeSave{path: filepath.Join(tempDir(t), "shop.postman_collection.json")}
	svc := New(manager, nil, nil, WithSaveDialog(save.pick))
	ctx := t.Context()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "..", "core", "importer", "postman", "testdata", "shop.postman_collection.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Import(ctx, ImportInputDTO{Path: fixture, Index: -1})
	if err != nil {
		t.Fatal(err)
	}

	res, err := svc.ExportPostman(ctx, ExportPostmanInputDTO{ID: first.NodeID})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Saved || res.Path != save.path || res.Name != "Shop API" || res.Requests != 20 || res.Folders != 3 {
		t.Errorf("result = %+v", res)
	}
	if len(save.dialogs) != 1 || save.dialogs[0].Filename != "Shop API.postman_collection.json" {
		t.Errorf("dialogs = %+v", save.dialogs)
	}
	data, err := os.ReadFile(save.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"hunter2", "s3cr3t-avatar", "coll-api-key-5", "oauth1-consumer-secret-5", "rsa-key-6"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("the export holds the secret %s", secret)
		}
	}
	if !slices.ContainsFunc(res.Notes, func(n ExportNoteDTO) bool { return n.Code == postman.CodeExportSecret }) {
		t.Errorf("no note on the secrets left out: %+v", res.Notes)
	}

	second, err := svc.Import(ctx, ImportInputDTO{Path: save.path, Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	if c := second.Report.Counts; c.Requests != 20 || c.Folders != 4 || c.Failed != 0 {
		t.Errorf("second import = %+v", c)
	}
	want, got := canonicalTree(t, svc, first.NodeID), canonicalTree(t, svc, second.NodeID)
	// The secrets typed in Postman are in neither folder's files (the
	// first import keeps them in the local side files, marked local; the
	// marks are left out of the comparison).
	// The second folder is named apart from the first.
	got = strings.Replace(got, `"name": "Shop API 2"`, `"name": "Shop API"`, 1)
	if got != want {
		w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
		for i := range min(len(w), len(g)) {
			if w[i] != g[i] {
				t.Errorf("the round trip changed the collection at line %d:\nfirst import:  %s\nsecond import: %s\n%s", i+1, w[i], g[i], strings.Join(w[max(0, i-8):i], "\n"))
				break
			}
		}
	}
}

// Exporting the whole project: its name and variables, the requests,
// unknown and broken files left out and reported.
func TestExportPostmanProject(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	writeFile(t, dir, "postsonde.json", `{"schemaVersion": 1, "kind": "workspace", "id": "ws_0123456789abcdef", "name": "Shop", "order": ["users"],
		"variables": [{"name": "baseUrl", "value": "http://localhost"}]}`)
	if _, err := manager.Open(dir); err != nil {
		t.Fatal(err)
	}
	save := &fakeSave{path: filepath.Join(tempDir(t), "out.json")}
	svc := New(manager, nil, nil, WithSaveDialog(save.pick))
	res, err := svc.ExportPostman(t.Context(), ExportPostmanInputDTO{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Saved || res.Name != "Shop" || res.Folders != 1 || res.Requests != 2 || res.Skipped != 2 || res.Warnings != 2 {
		t.Errorf("result = %+v", res)
	}
	var skipped []string
	for _, n := range res.Notes {
		if n.Skipped {
			skipped = append(skipped, n.Path)
		}
	}
	if !slices.Equal(skipped, []string{"Users / broken.json", "Users / Stream"}) {
		t.Errorf("skipped = %v (%+v)", skipped, res.Notes)
	}
	data, err := os.ReadFile(save.path)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Info struct {
			Name   string `json:"name"`
			Schema string `json:"schema"`
		} `json:"info"`
		Item     []map[string]any `json:"item"`
		Variable []map[string]any `json:"variable"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	if c.Info.Name != "Shop" || c.Info.Schema != postman.SchemaV21 || len(c.Item) != 2 || len(c.Variable) != 1 || c.Variable[0]["key"] != "baseUrl" {
		t.Errorf("collection = %s", data)
	}
}

// The export (EXP-03) is what the project shares (SEC-04): a
// secret global or folder variable goes out without its value, a secret
// environment variable not at all, and a local value never; the shared
// value is exported in its place.
func TestExportPostmanLeavesOutSecretAndLocalValues(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	writeFile(t, dir, "postsonde.json", `{"schemaVersion": 1, "kind": "workspace", "id": "ws_0123456789abcdef", "name": "Shop", "order": ["users"],
		"variables": [{"name": "baseUrl", "value": "https://shared.example"}, {"name": "globalKey", "value": "", "secret": true}]}`)
	writeFile(t, dir, "users/_folder.json", `{"kind": "folder", "id": "fld_users", "name": "Users", "auth": {"type": "inherit"},
		"variables": [{"name": "folderKey", "value": "", "secret": true}]}`)
	writeFile(t, dir, "environments/dev.json", `{"schemaVersion": 1, "kind": "environment", "id": "env_dev", "name": "Dev",
		"variables": [{"name": "envKey", "value": "", "secret": true}, {"name": "host", "value": "dev.shared"}]}`)
	ws, err := manager.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	secrets := requestsecrets.New(ws.LocalDir)
	if err := secrets.Write(ws.ID(), map[string]string{"var.globalKey": "gl0bal-s3cret"}); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Write("fld_users", map[string]string{"var.folderKey": "f0lder-s3cret"}); err != nil {
		t.Fatal(err)
	}
	envs := environments.New(ws.Root, ws.LocalDir, false)
	if _, err := envs.SaveVariables("env_dev", []environments.Variable{
		{Name: "envKey", Value: "env-s3cret", Secret: true},
		{Name: "host", Value: "dev.shared", Local: true, LocalValue: "local-env-host"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := envs.SetActive("env_dev"); err != nil {
		t.Fatal(err)
	}
	if err := localvalues.New(ws.LocalDir).Write(localvalues.Global(ws.ID()), map[string]string{"baseUrl": "http://local-global"}); err != nil {
		t.Fatal(err)
	}

	save := &fakeSave{path: filepath.Join(tempDir(t), "out.json")}
	svc := New(manager, nil, nil, WithSaveDialog(save.pick))
	for _, in := range []ExportPostmanInputDTO{{}, {ID: "fld_users"}} {
		if _, err := svc.ExportPostman(t.Context(), in); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(save.path)
		if err != nil {
			t.Fatal(err)
		}
		for _, leak := range []string{"gl0bal-s3cret", "f0lder-s3cret", "env-s3cret", "local-env-host", "http://local-global"} {
			if strings.Contains(string(data), leak) {
				t.Errorf("export %+v holds %q:\n%s", in, leak, data)
			}
		}
		if !strings.Contains(string(data), "https://shared.example") {
			t.Errorf("export %+v lacks the shared value:\n%s", in, data)
		}
	}
}

// A folder's export carries what it inherits from above, and pending
// edits.
func TestExportPostmanFolderInherits(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	writeFile(t, dir, "postsonde.json", `{"schemaVersion": 1, "kind": "workspace", "id": "ws_0123456789abcdef", "name": "Shop", "order": ["users"],
		"variables": [{"name": "baseUrl", "value": "http://root"}, {"name": "v", "value": "root"}]}`)
	writeFile(t, dir, "users/_folder.json", `{"kind": "folder", "id": "fld_users", "name": "Users",
		"variables": [{"name": "v", "value": "users"}],
		"headers": [{"name": "X-Tenant", "value": "acme"}, {"name": "X-Off", "value": "1", "disabled": true}],
		"auth": {"type": "bearer", "token": "{{token}}"}}`)
	writeFile(t, dir, "users/admin/_folder.json", `{"kind": "folder", "id": "fld_admin", "name": "Admin",
		"headers": [{"name": "x-tenant", "value": "admin"}], "auth": {"type": "inherit"}}`)
	writeFile(t, dir, "users/admin/list.json", `{"kind": "http", "id": "req_list", "name": "List", "method": "GET", "url": "{{baseUrl}}/admins",
		"body": {"mode": "none"}, "auth": {"type": "inherit"}}`)
	if _, err := manager.Open(dir); err != nil {
		t.Fatal(err)
	}
	save := &fakeSave{path: filepath.Join(tempDir(t), "admin.json")}
	svc := New(manager, nil, nil, WithSaveDialog(save.pick))
	ctx := t.Context()
	req := getRequest(t, svc, "req_list")
	req.URL = "{{baseUrl}}/admins?edited=1"
	update(t, svc, req, 1)

	res, err := svc.ExportPostman(ctx, ExportPostmanInputDTO{ID: "fld_admin"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Saved || res.Name != "Admin" || res.Requests != 1 {
		t.Errorf("result = %+v", res)
	}
	data, err := os.ReadFile(save.path)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Auth struct {
			Type string `json:"type"`
		} `json:"auth"`
		Variable []struct{ Key, Value string } `json:"variable"`
		Item     []struct {
			Request struct {
				Header []struct{ Key, Value string } `json:"header"`
				URL    struct {
					Raw string `json:"raw"`
				} `json:"url"`
			} `json:"request"`
		} `json:"item"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	vars, _ := json.Marshal(c.Variable)
	if c.Auth.Type != "bearer" || string(vars) != `[{"Key":"baseUrl","Value":"http://root"},{"Key":"v","Value":"users"}]` {
		t.Errorf("collection auth %q, variables %s", c.Auth.Type, vars)
	}
	if len(c.Item) != 1 {
		t.Fatalf("items = %s", data)
	}
	r := c.Item[0].Request
	if h, _ := json.Marshal(r.Header); string(h) != `[{"Key":"x-tenant","Value":"admin"}]` || r.URL.Raw != "{{baseUrl}}/admins?edited=1" {
		t.Errorf("request = %+v", r)
	}
}

func TestExportPostmanFailures(t *testing.T) {
	t.Parallel()
	manager, _ := newProject(t)
	ctx := t.Context()
	code := func(err error) string {
		var e *errs.Error
		if errors.As(err, &e) {
			return e.Code
		}
		return ""
	}

	if _, err := New(manager, nil, nil).ExportPostman(ctx, ExportPostmanInputDTO{}); code(err) != CodeExportUnavailable {
		t.Errorf("without a dialog: %v", err)
	}

	save := &fakeSave{}
	svc := New(manager, nil, nil, WithSaveDialog(save.pick))
	if _, err := svc.ExportPostman(ctx, ExportPostmanInputDTO{ID: "req_health"}); code(err) != "collection.not_found" {
		t.Errorf("a request: %v", err)
	}
	res, err := svc.ExportPostman(ctx, ExportPostmanInputDTO{ID: "fld_users"})
	if err != nil || res.Saved || res.Requests != 1 || len(res.Notes) == 0 {
		t.Errorf("cancelled = %+v, %v", res, err)
	}

	save.path = filepath.Join(tempDir(t), "missing", "out.json")
	if _, err := svc.ExportPostman(ctx, ExportPostmanInputDTO{ID: "fld_users"}); code(err) != CodeExportFailed {
		t.Errorf("unwritable: %v", err)
	}
	if _, err := os.Stat(save.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file was written: %v", err)
	}
}
