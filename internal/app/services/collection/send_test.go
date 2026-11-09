package collection

import (
	"context"
	"testing"

	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

func TestLoadHTTPSource(t *testing.T) {
	t.Parallel()
	svc, manager, dir, _, _ := newAutosaveService(t)
	writeFile(t, dir, "users/_folder.json", `{"kind": "folder", "id": "fld_users", "name": "Users", "variables": [{"name": "a", "value": "1"}], "auth": {"type": "bearer", "token": "", "localSecrets": ["token"]}}`)
	writeFile(t, dir, "users/admins/_folder.json", `{"kind": "folder", "id": "fld_admins", "name": "Admins", "variables": [{"name": "b", "value": "2"}], "auth": {"type": "inherit"}}`)
	writeFile(t, dir, "users/admins/list.json", `{"kind": "http", "id": "req_list", "name": "List", "method": "GET", "url": "{{base}}/admins", "auth": {"type": "inherit"}}`)
	ws := manager.Current()
	if err := requestsecrets.New(ws.LocalDir).Write("fld_users", map[string]string{"auth.token": "folder-secret"}); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	src, err := LoadHTTPSource(svc, ctx, "req_list")
	if err != nil {
		t.Fatal(err)
	}
	if src.Request.URL != "{{base}}/admins" || src.Workspace != ws {
		t.Errorf("request = %+v, workspace = %v", src.Request, src.Workspace)
	}
	if src.Auth.Type != "bearer" || src.Auth.Token != "folder-secret" {
		t.Errorf("auth = %+v, want the folder's bearer with its local token", src.Auth)
	}
	if len(src.FolderVariables) != 2 || src.FolderVariables[0][0].Name != "a" || src.FolderVariables[1][0].Name != "b" {
		t.Errorf("folder variables = %+v, want users then admins", src.FolderVariables)
	}

	// A pending edit is what is sent, with its typed secret.
	req := getRequest(t, svc, "req_list")
	req.URL = "https://example.com/edited"
	req.Auth = AuthDTO{Type: "bearer", Token: "typed-token"}
	update(t, svc, req, 1)
	src, err = LoadHTTPSource(svc, ctx, "req_list")
	if err != nil {
		t.Fatal(err)
	}
	if src.Request.URL != "https://example.com/edited" || src.Auth.Token != "typed-token" {
		t.Errorf("pending: url %q, auth %+v", src.Request.URL, src.Auth)
	}

	// A GraphQL request is sent over HTTP too (GQL-02).
	src, err = LoadHTTPSource(svc, ctx, "req_me")
	if err != nil {
		t.Fatal(err)
	}
	if src.GraphQL == nil || src.GraphQL.ID != "req_me" || src.GraphQL.URL != "/graphql" || src.Request.ID != "" {
		t.Errorf("graphql source = %+v", src)
	}

	writeFile(t, dir, "users/raw.json", `{"kind": "tcp", "id": "req_raw", "name": "Raw", "address": "localhost:7000"}`)
	if _, err := svc.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = LoadHTTPSource(svc, ctx, "req_raw")
	checkCode(t, err, errs.InvalidArgument, CodeNotHTTP)
	_, err = LoadHTTPSource(svc, ctx, "fld_users")
	checkCode(t, err, errs.NotFound, core.CodeNotFound)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := LoadHTTPSource(svc, cancelled, "req_list"); errs.KindOf(err) != errs.Cancelled {
		t.Errorf("cancelled: err = %v", err)
	}

	_, err = LoadHTTPSource(New(coreworkspace.New(tempDir(t)), nil, nil), ctx, "req_list")
	checkCode(t, err, errs.NotFound, coreworkspace.CodeNoWorkspace)
}

// TestLoadVariablesSource: the folder variables of a request of any kind
// (WS-11), and of a folder.
func TestLoadVariablesSource(t *testing.T) {
	t.Parallel()
	svc, manager, dir, _, _ := newAutosaveService(t)
	writeFile(t, dir, "live/_folder.json", `{"kind": "folder", "id": "fld_live", "name": "Live", "variables": [{"name": "room", "value": "lobby"}]}`)
	writeFile(t, dir, "live/feed.json", `{"kind": "websocket", "id": "req_feed", "name": "Feed", "url": "ws://{{host}}/feed"}`)
	ctx := t.Context()
	if _, err := svc.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	src, err := LoadVariablesSource(svc, ctx, "req_feed")
	if err != nil {
		t.Fatal(err)
	}
	if src.Workspace != manager.Current() || len(src.FolderVariables) != 1 || src.FolderVariables[0][0].Name != "room" {
		t.Errorf("source = %+v", src)
	}
	// A folder's own variables come last, for its editor's chips (COL-03).
	src, err = LoadVariablesSource(svc, ctx, "fld_live")
	if err != nil || len(src.FolderVariables) != 1 || src.FolderVariables[0][0].Name != "room" {
		t.Errorf("folder source = %+v, %v", src, err)
	}
	_, err = LoadVariablesSource(svc, ctx, "fld_missing")
	checkCode(t, err, errs.NotFound, core.CodeNotFound)
	_, err = LoadVariablesSource(New(coreworkspace.New(tempDir(t)), nil, nil), ctx, "req_feed")
	checkCode(t, err, errs.NotFound, coreworkspace.CodeNoWorkspace)
}
