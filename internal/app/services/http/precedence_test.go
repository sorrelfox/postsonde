package http

import (
	"context"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/runtimevars"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// projectFixture is an HTTPService wired to a real project, as the app
// wires it: the collection service loads the requests, their folders'
// variables and auth, and the global variables.
type projectFixture struct {
	dir     string
	ws      *coreworkspace.Workspace
	coll    *collection.CollectionService
	svc     *HTTPService
	rec     *recorder
	envs    *environments.Store
	servers *httptest.Server
	// runtime holds the project's runtime variables (ENV-15).
	runtime *runtimevars.Registry
}

func newProjectFixture(t *testing.T, opts ...Option) *projectFixture {
	t.Helper()
	manager := coreworkspace.New(t.TempDir())
	ws, err := manager.Create(filepath.Join(t.TempDir(), "p"), "P")
	if err != nil {
		t.Fatal(err)
	}
	f := &projectFixture{dir: ws.Root, ws: ws, rec: newRecorder(), envs: environments.New(ws.Root, ws.LocalDir, false),
		runtime: runtimevars.NewRegistry(manager.Current, nil)}
	f.coll = collection.New(manager, nil, nil)
	t.Cleanup(func() { _ = f.coll.ServiceShutdown() })
	ops := operation.NewRegistry(f.rec.emit, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ops.Shutdown(ctx)
	})
	load := func(ctx context.Context, id string) (collection.HTTPSource, error) {
		return collection.LoadHTTPSource(f.coll, ctx, id)
	}
	f.svc = New(load, func() time.Duration { return 0 }, ops,
		netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(nil))), nil, nil,
		append([]Option{
			WithVariablesLoader(func(ctx context.Context, id string) (collection.VariablesSource, error) {
				return collection.LoadVariablesSource(f.coll, ctx, id)
			}),
			WithRuntimeVariables(f.runtime),
		}, opts...)...)
	t.Cleanup(func() { _ = f.svc.ServiceShutdown() })
	// The server answers with what it got: the path and the headers that
	// carry auth.
	f.servers = httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		user, pass, _ := r.BasicAuth()
		fmt.Fprintf(w, "%s|%s|%s:%s|%s", r.URL.RequestURI(), r.Header.Get("Authorization"), user, pass, r.Header.Get("X-Key"))
	}))
	t.Cleanup(f.servers.Close)
	return f
}

func (f *projectFixture) write(t *testing.T, rel, content string) {
	t.Helper()
	abs := filepath.Join(f.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *projectFixture) reload(t *testing.T) {
	t.Helper()
	if _, err := f.coll.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// sendBody sends request id and returns the response body.
func (f *projectFixture) sendBody(t *testing.T, id string) string {
	t.Helper()
	started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: id})
	if err != nil {
		t.Fatalf("Send = %v", err)
	}
	if done := f.rec.wait(t, started.OpID); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	return f.rec.response(t, started.OpID).Body
}

func (f *projectFixture) variable(t *testing.T, id, name string) VariableDTO {
	t.Helper()
	got, err := f.svc.Variables(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range got.Variables {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("%s is not listed for %s", name, id)
	return VariableDTO{}
}

// TestPrecedenceAcrossEveryLevel (ENV-06): one name defined globally, in
// the active environment, in the collection (the top-level folder), in a
// folder inside it and in the request's own folder resolves to the
// nearest folder's value; taking the levels away one by one hands it to
// the next one down: global < environment < collection < folder.
func TestPrecedenceAcrossEveryLevel(t *testing.T) {
	t.Parallel()
	f := newProjectFixture(t)
	if _, err := collection.SaveGlobalVariables(f.coll, t.Context(), []projectfile.Variable{
		{Name: "x", Value: "global"}, {Name: "base", Value: f.servers.URL},
	}); err != nil {
		t.Fatal(err)
	}
	env, err := f.envs.Create("dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.envs.SaveVariables(env.ID, []environments.Variable{{Name: "x", Value: "environment"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.envs.SetActive(env.ID); err != nil {
		t.Fatal(err)
	}
	folder := func(id, name, value string) string {
		if value == "" {
			return fmt.Sprintf(`{"kind": "folder", "id": %q, "name": %q}`, id, name)
		}
		return fmt.Sprintf(`{"kind": "folder", "id": %q, "name": %q, "variables": [{"name": "x", "value": %q}]}`, id, name, value)
	}
	f.write(t, "api/_folder.json", folder("fld_api", "API", "collection"))
	f.write(t, "api/users/_folder.json", folder("fld_users", "Users", "folder"))
	f.write(t, "api/users/admins/_folder.json", folder("fld_admins", "Admins", "nearest"))
	f.write(t, "api/users/admins/list.json", `{"kind": "http", "id": "req_list", "name": "List", "method": "GET", "url": "{{base}}/{{x}}"}`)
	f.reload(t)

	steps := []struct {
		name      string
		change    func()
		body      string
		scope     string
		source    string
		overrides []string
	}{
		{"nearest folder", func() {}, "/nearest", "Folder: Admins", SourceFolder,
			[]string{"Folder: Users", "Collection: API", "Environment: dev", "Global"}},
		{"folder", func() { f.write(t, "api/users/admins/_folder.json", folder("fld_admins", "Admins", "")) },
			"/folder", "Folder: Users", SourceFolder, []string{"Collection: API", "Environment: dev", "Global"}},
		{"collection", func() { f.write(t, "api/users/_folder.json", folder("fld_users", "Users", "")) },
			"/collection", "Collection: API", SourceCollection, []string{"Environment: dev", "Global"}},
		{"environment", func() { f.write(t, "api/_folder.json", folder("fld_api", "API", "")) },
			"/environment", "Environment: dev", SourceEnvironment, []string{"Global"}},
		{"global", func() {
			if err := f.envs.SetActive(""); err != nil {
				t.Fatal(err)
			}
		}, "/global", "Global", SourceGlobal, nil},
	}
	for _, st := range steps {
		st.change()
		f.reload(t)
		if got := f.sendBody(t, "req_list"); got[:len(st.body)] != st.body {
			t.Errorf("%s: sent %q, want path %s", st.name, got, st.body)
		}
		v := f.variable(t, "req_list", "x")
		if v.Scope != st.scope || v.Source != st.source || !reflect.DeepEqual(v.Overrides, st.overrides) {
			t.Errorf("%s: x = %+v, want scope %q, source %q, overrides %v", st.name, v, st.scope, st.source, st.overrides)
		}
	}
}

// TestGlobalVariablesEditor (ENV-02): the global variables are saved to
// postsonde.json through the tree, used by the next send without
// reopening the project, and a secret one keeps its value on this
// computer only.
func TestGlobalVariablesEditor(t *testing.T) {
	t.Parallel()
	f := newProjectFixture(t)
	f.write(t, "ping.json", `{"kind": "http", "id": "req_ping", "name": "Ping", "method": "GET", "url": "{{base}}/{{who}}", "auth": {"type": "bearer", "token": "{{token}}"}}`)
	f.reload(t)
	saved, err := collection.SaveGlobalVariables(f.coll, t.Context(), []projectfile.Variable{
		{Name: "base", Value: f.servers.URL},
		{Name: "who", Value: "me"},
		{Name: "token", Value: "gl0bal-s3cret", Secret: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Variables) != 3 || saved.Variables[2].Value != "gl0bal-s3cret" || saved.ReadOnly {
		t.Errorf("saved = %+v", saved)
	}
	data, err := os.ReadFile(filepath.Join(f.dir, projectfile.WorkspaceFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, `"who"`) || strings.Contains(got, "gl0bal-s3cret") {
		t.Errorf("postsonde.json = %s", got)
	}
	if got := f.sendBody(t, "req_ping"); got != "/me|Bearer gl0bal-s3cret|:|" {
		t.Errorf("sent %q", got)
	}
	if v := f.variable(t, "req_ping", "token"); !v.Secret || v.Value != "" || v.Scope != "Global" {
		t.Errorf("token = %+v", v)
	}

	// A reorder of the top level rewrites postsonde.json: the variables
	// stay.
	f.write(t, "zeta.json", `{"kind": "http", "id": "req_zeta", "name": "Zeta", "method": "GET", "url": "/"}`)
	f.reload(t)
	if _, err := f.coll.Move(t.Context(), collection.MoveInputDTO{ID: "req_zeta", ParentID: "", Index: 0}); err != nil {
		t.Fatal(err)
	}
	loaded, err := collection.LoadGlobalVariables(f.coll, t.Context())
	if err != nil || len(loaded.Variables) != 3 || loaded.Variables[1].Value != "me" || loaded.Variables[2].Value != "gl0bal-s3cret" {
		t.Errorf("after a reorder: %+v, %v", loaded, err)
	}

	// Removing the secret drops its local value too.
	if _, err := collection.SaveGlobalVariables(f.coll, t.Context(), saved.Variables[:2]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.ws.LocalDir, "secrets", "requests", f.ws.ID()+".json")); err == nil {
		side, _ := os.ReadFile(filepath.Join(f.ws.LocalDir, "secrets", "requests", f.ws.ID()+".json"))
		if strings.Contains(string(side), "gl0bal-s3cret") {
			t.Errorf("side file still has the removed secret: %s", side)
		}
	}
}
