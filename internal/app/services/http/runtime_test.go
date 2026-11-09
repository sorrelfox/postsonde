package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/runtimevars"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// TestRuntimeVariablePrecedence (ENV-15): a runtime variable wins over the
// same name at every level a project file defines (global, environment,
// collection, folder), in a send and in the chips; a folder run's own
// variables still win over it. Values set through the API that extraction
// will use count for the next send. None of it reaches a project file.
func TestRuntimeVariablePrecedence(t *testing.T) {
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
		return fmt.Sprintf(`{"kind": "folder", "id": %q, "name": %q, "variables": [{"name": "x", "value": %q}]}`, id, name, value)
	}
	f.write(t, "api/_folder.json", folder("fld_api", "API", "collection"))
	f.write(t, "api/users/_folder.json", folder("fld_users", "Users", "folder"))
	f.write(t, "api/users/list.json", `{"kind": "http", "id": "req_list", "name": "List", "method": "GET", "url": "{{base}}/{{x}}", "headers": [{"name": "X-Key", "value": "{{key}}"}]}`)
	f.reload(t)

	// Set the way extraction will set it after a response.
	rt, err := f.svc.runtimeVariables(f.ws)
	if err != nil || rt == nil {
		t.Fatalf("runtimeVariables = %v, %v", rt, err)
	}
	for _, v := range []runtimevars.Variable{
		{Name: "x", Value: "runtime"},
		{Name: "key", Value: "rt-persisted-s3cret", Secret: true, Persist: true},
		{Name: "kept", Value: "rt-persisted-plain", Persist: true},
	} {
		if err := rt.Set(v); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.sendBody(t, "req_list"); !strings.HasPrefix(got, "/runtime|") || !strings.HasSuffix(got, "|rt-persisted-s3cret") {
		t.Errorf("sent %q, want the runtime values", got)
	}
	v := f.variable(t, "req_list", "x")
	if want := []string{"Folder: Users", "Collection: API", "Environment: dev", "Global"}; v.Scope != runtimevars.ScopeName ||
		v.Source != SourceRuntime || v.Value != "runtime" || !reflect.DeepEqual(v.Overrides, want) {
		t.Errorf("x = %+v", v)
	}
	if v := f.variable(t, "req_list", "key"); !v.Secret || v.Value != "" || v.Scope != "Runtime" || !v.Resolved {
		t.Errorf("key = %+v", v)
	}

	// A folder run's own variables win over the runtime ones.
	ctx := netx.WithUserOp(t.Context(), netx.UserOp{Kind: "runner.run", ID: "1"})
	run := NewRunSession(f.svc)
	out := run.Send(ctx, "req_list", RunOptions{Vars: template.Vars("Run", map[string]string{"x": "run"})}, nil)
	run.Close()
	if out.Response == nil || !strings.HasPrefix(out.Response.Body, "/run|") {
		t.Errorf("run sent %+v", out)
	}
	// Without a run variable of the name, a run sends the runtime value;
	// a value set during the run counts for its next request.
	run = NewRunSession(f.svc)
	if err := rt.Set(runtimevars.Variable{Name: "x", Value: "extracted"}); err != nil {
		t.Fatal(err)
	}
	out = run.Send(ctx, "req_list", RunOptions{Vars: template.Vars("Run", nil)}, nil)
	run.Close()
	if out.Response == nil || !strings.HasPrefix(out.Response.Body, "/extracted|") {
		t.Errorf("run sent %+v", out)
	}

	// Deleting it hands the name back to the folder.
	if err := rt.Delete("x"); err != nil {
		t.Fatal(err)
	}
	if got := f.sendBody(t, "req_list"); !strings.HasPrefix(got, "/folder|") {
		t.Errorf("after delete sent %q", got)
	}

	// No project file holds a runtime value.
	err = filepath.WalkDir(f.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, value := range []string{"rt-persisted", "extracted", "runtime"} {
			if strings.Contains(string(data), value) {
				t.Errorf("%s holds %q:\n%s", path, value, data)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.ws.LocalDir, runtimevars.FileName)); err != nil {
		t.Errorf("the persisted runtime variables are not in the local directory: %v", err)
	}
}

// TestSecretRuntimeVariableMasked: a secret runtime variable (a token
// extracted from a login) is sent as it is and masked in the timeline, the
// history, a copied cURL command and a HAR export.
func TestSecretRuntimeVariableMasked(t *testing.T) {
	t.Parallel()
	const secret = "rt-t0ken-s3cret"
	srv := echoServer(t)
	ws, _, _, _ := envWorkspace(t, srv.URL)
	reg := runtimevars.NewRegistry(nil, nil)
	rt, err := reg.For(ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Set(runtimevars.Variable{Name: "session", Value: secret, Secret: true}); err != nil {
		t.Fatal(err)
	}
	src := httpSource("{{base}}/x?k={{session}}")
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Env", Value: "{{session}}"}}
	src.Workspace = ws
	harPath := filepath.Join(t.TempDir(), "out.har")
	var hist historyEntries
	f := newFixture(t, src, nil, WithRuntimeVariables(reg), hist.option(),
		WithSaveBody("", func(context.Context, SaveDialog) (string, error) { return harPath, nil }))

	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	resp := f.rec.response(t, id)
	if !strings.Contains(resp.Body, `"uri":"/x?k=`+secret+`"`) || !strings.Contains(resp.Body, `"env":"`+secret+`"`) {
		t.Fatalf("server received %s", resp.Body)
	}
	for i, hop := range resp.Timeline.Hops {
		if strings.Contains(hop.Request.Head, secret) || strings.Contains(hop.Request.Target, secret) {
			t.Errorf("timeline hop %d shows the secret:\n%s", i, hop.Request.Head)
		}
		if !strings.Contains(hop.Request.Head, "X-Env: "+httpclient.Mask) {
			t.Errorf("timeline hop %d head:\n%s", i, hop.Request.Head)
		}
	}

	entries := hist.list()
	if len(entries) != 1 {
		t.Fatalf("history = %d entries", len(entries))
	}
	data, err := json.Marshal(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || !entries[0].Masked {
		t.Errorf("history entry holds the secret or is not marked masked:\n%s", data)
	}

	curl, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(curl.Command, secret) || !curl.Masked {
		t.Errorf("cURL command:\n%s", curl.Command)
	}

	res, err := f.svc.ExportHAR(t.Context(), ExportHARInputDTO{Name: "Runtime", RequestIDs: []string{"req_1"}})
	if err != nil || !res.Masked || res.Entries != 1 {
		t.Fatalf("ExportHAR = %+v, %v", res, err)
	}
	if har := readFile(t, harPath); strings.Contains(har, secret) {
		t.Errorf("the HAR file holds the secret:\n%s", har)
	}

	// The chips know it is secret and show no value.
	vars, err := f.svc.Variables(t.Context(), "req_1")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vars.Variables {
		if v.Name == "session" && (!v.Secret || v.Value != "" || v.Source != SourceRuntime) {
			t.Errorf("session = %+v", v)
		}
	}
}
