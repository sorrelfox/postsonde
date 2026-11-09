package collection

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newProject creates a small project and opens it.
func newProject(t *testing.T) (*coreworkspace.Manager, string) {
	t.Helper()
	manager := coreworkspace.New(tempDir(t))
	dir := filepath.Join(tempDir(t), "shop")
	writeFile(t, dir, "postsonde.json", `{"schemaVersion": 1, "kind": "workspace", "id": "ws_0123456789abcdef", "name": "Shop", "order": ["users"]}`)
	writeFile(t, dir, "health.json", `{"kind": "http", "id": "req_health", "name": "Health", "method": "HEAD", "url": "/health", "body": {"mode": "none"}, "auth": {"type": "inherit"}}`)
	writeFile(t, dir, "users/_folder.json", `{"kind": "folder", "id": "fld_users", "name": "Users", "auth": {"type": "inherit"}}`)
	writeFile(t, dir, "users/me.json", `{"kind": "graphql", "id": "req_me", "name": "", "url": "/graphql", "query": "{ me { id } }", "auth": {"type": "inherit"}}`)
	writeFile(t, dir, "users/stream.json", `{"kind": "mqtt", "id": "req_ws", "name": "Stream"}`)
	writeFile(t, dir, "users/broken.json", `{"kind": "http", "id": `)
	if _, err := manager.Open(dir); err != nil {
		t.Fatal(err)
	}
	return manager, dir
}

func checkCode(t *testing.T, err error, kind errs.Kind, code string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || errs.KindOf(err) != kind || e.Code != code {
		t.Fatalf("err = %v, want kind %s code %s", err, kind, code)
	}
}

func TestTree(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	svc := New(manager, nil, nil)
	tree, err := svc.Tree(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(tree, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "dto/tree", []byte(strings.ReplaceAll(string(got), dir, "<root>")+"\n"))
}

func TestTreeFollowsWorkspace(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	svc := New(manager, nil, nil)
	ctx := t.Context()

	first, err := svc.Tree(ctx)
	if err != nil || len(first.Nodes) != 5 {
		t.Fatalf("Tree = %d nodes, %v", len(first.Nodes), err)
	}

	// A file added outside the app shows after Reload, not before.
	writeFile(t, dir, "added.json", `{"kind": "http", "id": "req_added", "method": "GET"}`)
	if cached, _ := svc.Tree(ctx); len(cached.Nodes) != 5 {
		t.Errorf("Tree reloaded without a reason: %d nodes", len(cached.Nodes))
	}
	if reloaded, err := svc.Reload(ctx); err != nil || len(reloaded.Nodes) != 6 {
		t.Errorf("Reload = %d nodes, %v", len(reloaded.Nodes), err)
	}

	// Opening another project loads its tree.
	other := filepath.Join(tempDir(t), "other")
	writeFile(t, other, "postsonde.json", `{"kind": "workspace", "id": "ws_other"}`)
	if _, err := manager.Open(other); err != nil {
		t.Fatal(err)
	}
	if tree, err := svc.Tree(ctx); err != nil || tree.WorkspaceID != "ws_other" || len(tree.Nodes) != 0 || tree.Path != other {
		t.Errorf("Tree after opening another project = %+v, %v", tree, err)
	}

	// Without a project there is no tree.
	if _, err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Tree(ctx)
	checkCode(t, err, errs.NotFound, coreworkspace.CodeNoWorkspace)
}

func TestTreeCancelled(t *testing.T) {
	t.Parallel()
	manager, _ := newProject(t)
	svc := New(manager, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := svc.Tree(ctx)
	if !errors.Is(err, context.Canceled) || errs.KindOf(err) != errs.Cancelled {
		t.Errorf("err = %v", err)
	}
}
