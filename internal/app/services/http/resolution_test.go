package http

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/localvalues"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/runtimevars"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

func resolved(t *testing.T, res ResolutionDTO, name string) ResolvedVariableDTO {
	t.Helper()
	for _, v := range res.Variables {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("%s is not in the resolution: %+v", name, res.Variables)
	return ResolvedVariableDTO{}
}

func scopeNames(v ResolvedVariableDTO) string {
	parts := make([]string, 0, len(v.Scopes))
	for _, s := range v.Scopes {
		p := s.Scope
		if s.Wins {
			p += "*"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " < ")
}

// TestResolutionMatchesTheSend (DBG-08, ENV-12): the resolution view
// lists every variable a request uses with the value it is sent with and
// the scope that wins, across every scope (global with a local value and a
// secret, environment with a local value, collection, folder, runtime and
// a folder run's own variables), nested values, dot notation, filters and
// dynamic values; the value it shows is the one the server gets, and the
// hover (Variables) shows the same. It is the send's own build: a run's
// resolution has the Run scope, as the run's send.
func TestResolutionMatchesTheSend(t *testing.T) {
	t.Parallel()
	f := newProjectFixture(t)
	srv := echoHeaders(t)

	if _, err := collection.SaveGlobalVariables(f.coll, t.Context(), []projectfile.Variable{
		{Name: "baseUrl", Value: srv.URL},
		{Name: "g", Value: "global-g"},
		{Name: "gl", Value: "global-gl"},
		{Name: "gs", Value: "global-s3cret", Secret: true},
		{Name: "e", Value: "global-e"},
		{Name: "greeting", Value: "hi {{g}}"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := localvalues.New(f.ws.LocalDir).Write(localvalues.Global(f.ws.ID()), map[string]string{"gl": "global-gl-local"}); err != nil {
		t.Fatal(err)
	}
	env, err := f.envs.Create("Dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.envs.SaveVariables(env.ID, []environments.Variable{
		{Name: "e", Value: "env-e"},
		{Name: "el", Value: "env-el", Local: true, LocalValue: "env-el-local"},
		{Name: "c", Value: "env-c"},
		{Name: "auth", Value: `{"token": "t0k"}`},
		{Name: "inner", Value: "env-inner"},
		{Name: "outer", Value: "[{{inner}}]"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.envs.SetActive(env.ID); err != nil {
		t.Fatal(err)
	}
	folder := func(id, name string, vars ...string) string {
		var parts []string
		for i := 0; i < len(vars); i += 2 {
			parts = append(parts, fmt.Sprintf(`{"name": %q, "value": %q}`, vars[i], vars[i+1]))
		}
		return fmt.Sprintf(`{"kind": "folder", "id": %q, "name": %q, "variables": [%s]}`, id, name, strings.Join(parts, ", "))
	}
	f.write(t, "api/_folder.json", folder("fld_api", "API", "c", "coll-c", "f", "coll-f"))
	f.write(t, "api/users/_folder.json", folder("fld_users", "Users", "f", "folder-f", "r", "folder-r"))
	names := []string{"g", "gl", "gs", "e", "el", "c", "f", "r", "rs", "n", "greeting", "outer", "auth.token", "$uuid", "g | base64"}
	headers := make([]string, 0, len(names))
	for i, n := range names {
		headers = append(headers, fmt.Sprintf(`{"name": "X-V-%d", "value": "{{%s}}"}`, i, n))
	}
	f.write(t, "api/users/list.json", fmt.Sprintf(`{"kind": "http", "id": "req_list", "name": "List", "method": "GET", "url": "/list?g={{g}}", "headers": [%s]}`,
		strings.Join(headers, ", ")))
	f.reload(t)

	rt, err := f.svc.runtimeVariables(f.ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []runtimevars.Variable{
		{Name: "r", Value: "runtime-r"},
		{Name: "rs", Value: "runtime-s3cret", Secret: true},
		{Name: "n", Value: "runtime-n"},
	} {
		if err := rt.Set(v); err != nil {
			t.Fatal(err)
		}
	}

	type want struct{ value, scope, scopes string }
	wants := map[string]want{
		"g":          {"global-g", "Global", "Global*"},
		"gl":         {"global-gl-local", "Global", "Global*"},
		"gs":         {"global-s3cret", "Global", "Global*"},
		"e":          {"env-e", "Environment: Dev", "Global < Environment: Dev*"},
		"el":         {"env-el-local", "Environment: Dev", "Environment: Dev*"},
		"c":          {"coll-c", "Collection: API", "Environment: Dev < Collection: API*"},
		"f":          {"folder-f", "Folder: Users", "Collection: API < Folder: Users*"},
		"r":          {"runtime-r", "Runtime", "Folder: Users < Runtime*"},
		"rs":         {"runtime-s3cret", "Runtime", "Runtime*"},
		"n":          {"runtime-n", "Runtime", "Runtime*"},
		"greeting":   {"hi global-g", "Global", "Global*"},
		"outer":      {"[env-inner]", "Environment: Dev", "Environment: Dev*"},
		"auth.token": {"t0k", "Environment: Dev", "Environment: Dev*"},
		"g | base64": {base64.StdEncoding.EncodeToString([]byte("global-g")), "Global", ""},
	}
	check := func(label string, res ResolutionDTO, sent map[string]string, wants map[string]want) {
		t.Helper()
		for i, name := range names {
			v := resolved(t, res, name)
			got := sent[fmt.Sprint(i)]
			if name == "$uuid" {
				if v.Kind != ResolutionDynamic || !v.Generated || v.Value != "" || v.Description == "" || len(got) != 36 {
					t.Errorf("%s: $uuid = %+v, sent %q", label, v, got)
				}
				continue
			}
			w := wants[name]
			if got != w.value {
				t.Errorf("%s: sent %s = %q, want %q", label, name, got, w.value)
			}
			count := 1
			if name == "g" {
				count = 2 // the URL's query uses it too
			}
			if !v.Resolved || v.Scope != w.scope || v.Count != count || scopeNames(v) != w.scopes {
				t.Errorf("%s: %s = %+v (scopes %s), want %+v", label, name, v, scopeNames(v), w)
			}
			// The view shows what is sent, a secret masked.
			if v.Secret {
				if v.Value != "" || strings.Contains(fmt.Sprint(v.Scopes), w.value) {
					t.Errorf("%s: %s shows its secret: %+v", label, name, v)
				}
			} else if v.Value != got {
				t.Errorf("%s: %s shows %q, sent %q", label, name, v.Value, got)
			}
		}
	}

	// A single send.
	sent := sentHeaders(t, f.sendBody(t, "req_list"))
	res, err := f.svc.Resolution(t.Context(), "req_list")
	if err != nil {
		t.Fatal(err)
	}
	check("send", res, sent, wants)
	if res.Environment != "Dev" || res.URL != srv.URL+"/list?g=global-g" {
		t.Errorf("environment %q, URL %q", res.Environment, res.URL)
	}
	// The relative URL's base is listed, as the build used it (COL-08).
	if v := resolved(t, res, "baseUrl"); v.Value != srv.URL || v.Scope != "Global" || v.Count != 1 {
		t.Errorf("baseUrl = %+v", v)
	}
	// A variable used only inside another's value says through which.
	if v := resolved(t, res, "inner"); v.Count != 0 || strings.Join(v.Via, ">") != "outer" || v.Value != "env-inner" {
		t.Errorf("inner = %+v", v)
	}
	if v := resolved(t, res, "auth.token"); v.Kind != ResolutionPath || v.Base != "auth" {
		t.Errorf("auth.token = %+v", v)
	}
	if v := resolved(t, res, "g | base64"); v.Kind != ResolutionFilter || len(v.Scopes) != 0 {
		t.Errorf("pipe = %+v", v)
	}
	if v := resolved(t, res, "gl"); !v.Local || !v.Scopes[0].Local || v.Scopes[0].Value != "global-gl-local" {
		t.Errorf("gl = %+v", v)
	}
	// The hover and completion (Variables) agree.
	vars, err := f.svc.Variables(t.Context(), "req_list")
	if err != nil {
		t.Fatal(err)
	}
	for _, hv := range vars.Variables {
		w, ok := wants[hv.Name]
		if !ok {
			continue
		}
		v := resolved(t, res, hv.Name)
		if hv.Scope != v.Scope || hv.Secret != v.Secret || hv.Value != v.Value || hv.Local != v.Local {
			t.Errorf("hover %s = %+v, resolution %+v (want %s)", hv.Name, hv, v, w.value)
		}
	}

	// A folder run: its own variables are the Run scope, above the runtime.
	ctx := netx.WithUserOp(t.Context(), netx.UserOp{Kind: "runner.run", ID: "1"})
	run := NewRunSession(f.svc)
	defer run.Close()
	o := RunOptions{Vars: template.Vars(runScopeName, map[string]string{"n": "run-n", "r": "run-r"})}
	out := run.Send(ctx, "req_list", o, nil)
	if out.Response == nil {
		t.Fatalf("run sent %+v", out.Error)
	}
	runRes, err := run.Resolution(ctx, "req_list", o)
	if err != nil {
		t.Fatal(err)
	}
	runWants := map[string]want{}
	for k, v := range wants {
		runWants[k] = v
	}
	runWants["n"] = want{"run-n", "Run", "Runtime < Run*"}
	runWants["r"] = want{"run-r", "Run", "Folder: Users < Runtime < Run*"}
	check("run", runRes, sentHeaders(t, out.Response.Body), runWants)
	if v := resolved(t, runRes, "n"); v.Source != SourceRun || v.Scopes[0].Source != SourceRuntime {
		t.Errorf("run n = %+v", v)
	}
}

// TestResolutionOfWhatCannotBeSentYet: undefined names, prompts and files
// are listed with why, not refused: the view is for finding them.
func TestResolutionOfWhatCannotBeSentYet(t *testing.T) {
	t.Parallel()
	f := newProjectFixture(t)
	f.write(t, "r.json", `{"kind": "http", "id": "req_r", "name": "R", "method": "POST", "url": "https://h.test/{{nope}}",
		"headers": [{"name": "A", "value": "{{$prompt \"Token\" secret}}"}],
		"body": {"mode": "raw", "type": "text", "text": "{{$file data.txt}}"}}`)
	f.reload(t)
	if _, err := collection.SaveGlobalVariables(f.coll, t.Context(), []projectfile.Variable{{Name: "loop", Value: "{{loop}}"}}); err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.Resolution(t.Context(), "req_r")
	if err != nil {
		t.Fatal(err)
	}
	for name, w := range map[string]struct{ kind, problem string }{
		"nope":                   {ResolutionVariable, "unresolved"},
		`$prompt "Token" secret`: {ResolutionPrompt, "prompt"},
		"$file data.txt":         {ResolutionFile, "file"},
	} {
		v := resolved(t, res, name)
		if v.Kind != w.kind || v.Resolved || v.Problem != w.problem || v.Value != "" {
			t.Errorf("%s = %+v, want kind %s problem %s", name, v, w.kind, w.problem)
		}
	}
	if res.URL != "https://h.test/{{nope}}" {
		t.Errorf("URL = %q", res.URL)
	}

	// A problem that stops every build (a loop) is the error, as for Send.
	f.write(t, "loop.json", `{"kind": "http", "id": "req_loop", "name": "Loop", "method": "GET", "url": "https://h.test/{{loop}}"}`)
	f.reload(t)
	_, err = f.svc.Resolution(t.Context(), "req_loop")
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != template.CodeCycle {
		t.Errorf("loop: err = %v", err)
	}
}
