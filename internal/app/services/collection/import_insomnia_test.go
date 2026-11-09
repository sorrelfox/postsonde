package collection

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	envservice "github.com/sorrelfox/postsonde/internal/app/services/environments"
)

// An Insomnia v4 export goes through the default formats into a project:
// nested folders, every request kind, environments, and no secret in the
// project's files.
func TestImportInsomnia(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	ev := &namedEvents{}
	svc := New(manager, ev.emit, nil)
	ctx := t.Context()
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "core", "importer", "insomnia", "testdata", "shop-v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(svc.ImportFormats(ctx), func(f ImportFormatDTO) bool { return f.ID == "insomnia" }) {
		t.Fatalf("formats = %+v", svc.ImportFormats(ctx))
	}

	pre, err := svc.PreviewImport(ctx, PreviewImportInputDTO{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if pre.Format.ID != "insomnia" || pre.Items[0].Name != "Shop API" {
		t.Fatalf("preview = %+v", pre.Format)
	}
	if c := pre.Counts; c.Folders != 4 || c.Requests != 13 || c.Environments != 3 || c.Skipped != 1 || c.Warnings == 0 {
		t.Errorf("counts = %+v", c)
	}

	res, err := svc.Import(ctx, ImportInputDTO{Path: path, Digest: pre.Digest, Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	rep := res.Report
	if rep.Counts.Requests != 13 || rep.Counts.Failed != 0 || rep.Counts.Environments != 3 {
		t.Fatalf("report counts = %+v", rep.Counts)
	}
	if !ev.has(EventChanged) || !ev.has(envservice.EventChanged) {
		t.Errorf("events = %v", ev.names())
	}
	byName := map[string]ImportItemDTO{}
	for _, it := range rep.Items {
		byName[it.Name] = it
	}
	kinds := map[string]string{
		"List users": "http", "Search products": "graphql", "Get order": "grpc", "Order feed": "websocket",
	}
	for name, kind := range kinds {
		if it := byName[name]; it.Status != "imported" || it.FileKind != kind || it.NodeID == "" {
			t.Errorf("%s = %+v", name, it)
		}
	}
	var users *NodeDTO
	for i := range res.Tree.Nodes {
		if res.Tree.Nodes[i].ID == byName["Users"].NodeID {
			users = &res.Tree.Nodes[i]
		}
	}
	if users == nil || users.Path != "shop-api/api/users" || len(users.ChildIDs) != 4 {
		t.Fatalf("users folder = %+v", users)
	}

	// Typed secrets are in the local side files, readable through the
	// service, never in the project.
	create := getRequest(t, svc, byName["Create user"].NodeID)
	if create.Auth.Type != "basic" || create.Auth.Password != "hunter2" || create.Auth.Username != "{{user.login}}" {
		t.Errorf("create user auth = %+v", create.Auth)
	}
	list := getRequest(t, svc, byName["List users"].NodeID)
	if list.URL != "{{base_url}}/users?sort=name&limit={{page_size}}&q=a%26b" {
		t.Errorf("list users url = %q", list.URL)
	}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, secret := range []string{"hunter2", "dev-token-123", "oauth1-consumer-s3cr3t", "oauth1-token-s3cr3t"} {
			if strings.Contains(string(data), secret) {
				t.Errorf("%s holds a secret", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
