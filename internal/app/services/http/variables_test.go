package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// echoServer answers with what it got: method, URI, the X-Env header and
// the body, as JSON.
func echoServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"uri": r.URL.RequestURI(), "env": r.Header.Get("X-Env"), "body": string(body),
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// envWorkspace is a workspace with two environments, dev and prod, that
// define the same names with other values. It returns their IDs.
func envWorkspace(t *testing.T, base string) (*coreworkspace.Workspace, *environments.Store, string, string) {
	t.Helper()
	ws, err := coreworkspace.New(t.TempDir()).Create(filepath.Join(t.TempDir(), "p"), "P")
	if err != nil {
		t.Fatal(err)
	}
	ws.File.Variables = []projectfile.Variable{{Name: "base", Value: base}, {Name: "path", Value: "global"}}
	store := environments.New(ws.Root, ws.LocalDir, false)
	ids := make([]string, 0, 2)
	for _, name := range []string{"dev", "prod"} {
		env, err := store.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		vars := []environments.Variable{
			{Name: "path", Value: name + "-path"},
			{Name: "tag", Value: name + "-tag"},
			{Name: "token", Value: name + "-secret", Secret: true},
		}
		if _, err := store.SaveVariables(env.ID, vars); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, env.ID)
	}
	return ws, store, ids[0], ids[1]
}

func (f *fixture) sendEcho(t *testing.T, in SendInputDTO) map[string]string {
	t.Helper()
	started, err := f.svc.Send(t.Context(), in)
	if err != nil {
		t.Fatalf("Send = %v", err)
	}
	if done := f.rec.wait(t, started.OpID); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(f.rec.response(t, started.OpID).Body), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// ENV-10: switching the active environment changes what is sent, in the
// URL, headers and body of an HTTP request and the query and variables of
// a GraphQL request.
func TestSendFollowsActiveEnvironment(t *testing.T) {
	t.Parallel()
	srv := echoServer(t)
	ws, store, dev, prod := envWorkspace(t, srv.URL)

	src := httpSource("{{base}}/{{path}}?t={{tag}}")
	src.Request.Method = "POST"
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Env", Value: "{{tag}}"}}
	src.Request.Body = projectfile.Body{Mode: projectfile.BodyRaw, Text: `{"tag":"{{tag}}"}`}
	src.Workspace = ws
	f := newFixture(t, src, nil)

	want := map[string]map[string]string{
		dev:  {"uri": "/dev-path?t=dev-tag", "env": "dev-tag", "body": `{"tag":"dev-tag"}`},
		prod: {"uri": "/prod-path?t=prod-tag", "env": "prod-tag", "body": `{"tag":"prod-tag"}`},
		"":   {"uri": "/global?t={{tag}}", "env": "{{tag}}", "body": `{"tag":"{{tag}}"}`},
	}
	for _, id := range []string{dev, prod, ""} {
		if err := store.SetActive(id); err != nil {
			t.Fatal(err)
		}
		in := SendInputDTO{RequestID: "req_1", AllowUnresolved: id == ""}
		if got := f.sendEcho(t, in); !reflect.DeepEqual(got, want[id]) {
			t.Errorf("active %q: sent %v, want %v", id, got, want[id])
		}
	}

	gql := &projectfile.GraphQLRequest{
		ID:        "req_1",
		URL:       "{{base}}/graphql",
		Query:     `{ items(tag: "{{tag}}") { id } }`,
		Variables: `{"path": "{{path}}"}`,
	}
	f.src = collection.HTTPSource{GraphQL: gql, Auth: projectfile.Auth{Type: projectfile.AuthNone}, Workspace: ws}
	for _, id := range []string{dev, prod} {
		if err := store.SetActive(id); err != nil {
			t.Fatal(err)
		}
		name := map[string]string{dev: "dev", prod: "prod"}[id]
		got := f.sendEcho(t, SendInputDTO{RequestID: "req_1"})
		wantBody := fmt.Sprintf(`{"query":"{ items(tag: \"%s-tag\") { id } }","variables":{"path":"%s-path"}}`, name, name)
		if got["body"] != wantBody {
			t.Errorf("GraphQL, active %s: body %s, want %s", name, got["body"], wantBody)
		}
	}
}

// ENV-11: the undefined names of every field come back in one error;
// AllowUnresolved sends them as written.
func TestSendUnresolved(t *testing.T) {
	t.Parallel()
	srv := echoServer(t)
	src := httpSource(srv.URL + "/{{a}}")
	src.Request.Method = "POST"
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Env", Value: "{{b}}-{{a}}"}}
	src.Request.Body = projectfile.Body{Mode: projectfile.BodyRaw, Text: "{{c}}"}
	f := newFixture(t, src, nil)

	_, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1"})
	var e *errs.Error
	errors.As(err, &e)
	checkErr(t, e, errs.InvalidArgument, template.CodeUnresolved)
	if e.Details["names"] != "a,b,c" {
		t.Errorf("names = %q, want a,b,c", e.Details["names"])
	}

	got := f.sendEcho(t, SendInputDTO{RequestID: "req_1", AllowUnresolved: true})
	want := map[string]string{"uri": "/%7B%7Ba%7D%7D", "env": "{{b}}-{{a}}", "body": "{{c}}"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sent %v, want %v", got, want)
	}

	// Other problems still refuse, also with AllowUnresolved.
	f.src.FolderVariables = [][]projectfile.Variable{{{Name: "a", Value: "{{a}}"}}}
	_, err = f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1", AllowUnresolved: true})
	errors.As(err, &e)
	checkErr(t, e, errs.InvalidArgument, template.CodeCycle)
}

func TestVariables(t *testing.T) {
	t.Parallel()
	ws, store, dev, _ := envWorkspace(t, "http://h")
	if err := store.SetActive(dev); err != nil {
		t.Fatal(err)
	}
	src := httpSource("{{base}}")
	src.Workspace = ws
	src.FolderVariables = [][]projectfile.Variable{{
		{Name: "tag", Value: "folder-{{path}}"},
		{Name: "broken", Value: "{{nope}}"},
		{Name: "off", Value: "x", Disabled: true},
		{Name: "$timestamp", Value: "fixed"},
	}}
	f := newFixture(t, src, nil)

	got, err := f.svc.Variables(t.Context(), "req_1")
	if err != nil {
		t.Fatal(err)
	}
	got.Variables = withoutDynamic(t, got.Variables, "$timestamp")
	want := VariablesDTO{Environment: "dev", Variables: []VariableDTO{
		{Name: "$timestamp", Value: "fixed", Resolved: true, Scope: "Folder", Source: SourceFolder},
		{Name: "base", Value: "http://h", Resolved: true, Scope: "Global", Source: SourceGlobal},
		{Name: "broken", Value: "{{nope}}", Resolved: false, Scope: "Folder", Source: SourceFolder},
		{Name: "path", Value: "dev-path", Resolved: true, Scope: "Environment: dev", Source: SourceEnvironment, Overrides: []string{"Global"}},
		{Name: "tag", Value: "folder-dev-path", Resolved: true, Scope: "Folder", Source: SourceFolder, Overrides: []string{"Environment: dev"}},
		{Name: "token", Value: "", Secret: true, Resolved: true, Scope: "Environment: dev", Source: SourceEnvironment},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Variables =\n%+v\nwant\n%+v", got, want)
	}

	if err := store.SetActive(""); err != nil {
		t.Fatal(err)
	}
	got, err = f.svc.Variables(t.Context(), "req_1")
	if err != nil {
		t.Fatal(err)
	}
	got.Variables = withoutDynamic(t, got.Variables, "$timestamp")
	if got.Environment != "" || len(got.Variables) != 5 {
		t.Errorf("no environment: %+v", got)
	}

	if _, err := f.svc.Variables(t.Context(), "req_other"); errs.KindOf(err) != errs.NotFound {
		t.Errorf("unknown request: err = %v", err)
	}
}

// withoutDynamic checks that vars list every dynamic value, except the
// shadowed ones a scope defines, as resolved, sorted with the others, and
// returns the other variables.
func withoutDynamic(t *testing.T, vars []VariableDTO, shadowed ...string) []VariableDTO {
	t.Helper()
	if !slices.IsSortedFunc(vars, func(a, b VariableDTO) int { return strings.Compare(a.Name, b.Name) }) {
		t.Errorf("variables are not sorted by name: %+v", vars)
	}
	var rest []VariableDTO
	dynamic := map[string]VariableDTO{}
	for _, v := range vars {
		if v.Source == SourceDynamic {
			dynamic[v.Name] = v
		} else {
			rest = append(rest, v)
		}
	}
	for _, name := range template.DynamicNames() {
		v, ok := dynamic[name]
		if slices.Contains(shadowed, name) {
			if ok {
				t.Errorf("%s is defined by a scope but listed as dynamic too", name)
			}
			continue
		}
		want := VariableDTO{Name: name, Value: template.DynamicDescription(name), Resolved: true, Scope: template.DynamicScope, Source: SourceDynamic}
		if !ok || !reflect.DeepEqual(v, want) {
			t.Errorf("dynamic %s = %+v, want %+v", name, v, want)
		}
	}
	return rest
}

// Dynamic values resolve at send time: they are not undefined names, and
// each reference gets its own value.
func TestSendDynamicValues(t *testing.T) {
	t.Parallel()
	srv := echoServer(t)
	src := httpSource(srv.URL + "/{{$uuid}}?n={{$randomInt 1 6}}&t={{ $timestamp }}")
	src.Request.Method = "POST"
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Env", Value: "{{$uuid}}"}}
	src.Request.Body = projectfile.Body{Mode: projectfile.BodyRaw, Text: `{{$randomHex 8}}`}
	f := newFixture(t, src, nil)

	got := f.sendEcho(t, SendInputDTO{RequestID: "req_1"})
	re := regexp.MustCompile(`^/([0-9a-f-]{36})\?n=[1-6]&t=\d{10}$`)
	m := re.FindStringSubmatch(got["uri"])
	if m == nil {
		t.Fatalf("uri = %q", got["uri"])
	}
	if len(got["env"]) != 36 || got["env"] == m[1] {
		t.Errorf("header $uuid = %q, want another UUID than the URL's %s", got["env"], m[1])
	}
	if ok, _ := regexp.MatchString(`^[0-9a-f]{8}$`, got["body"]); !ok {
		t.Errorf("body = %q", got["body"])
	}

	// Invalid arguments refuse, also with AllowUnresolved.
	f.src.Request.Body.Text = "{{$randomInt 5 1}}"
	for _, allow := range []bool{false, true} {
		_, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1", AllowUnresolved: allow})
		var e *errs.Error
		errors.As(err, &e)
		checkErr(t, e, errs.InvalidArgument, template.CodeInvalidDynamic)
	}
}

// TestVariablesLoader: with WithVariablesLoader, Variables answers for a
// request Send cannot send (a websocket request, WS-11), with the same
// scopes.
func TestVariablesLoader(t *testing.T) {
	t.Parallel()
	ws, store, _, prod := envWorkspace(t, "ws://h")
	if err := store.SetActive(prod); err != nil {
		t.Fatal(err)
	}
	svc := New(nil, nil, nil, nil, nil, nil, WithVariablesLoader(func(_ context.Context, id string) (collection.VariablesSource, error) {
		if id != "req_ws" {
			return collection.VariablesSource{}, errs.New(errs.NotFound, "no")
		}
		return collection.VariablesSource{Workspace: ws, FolderVariables: [][]projectfile.Variable{{{Name: "room", Value: "{{tag}}"}}}}, nil
	}))
	got, err := svc.Variables(t.Context(), "req_ws")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]VariableDTO{}
	for _, v := range got.Variables {
		byName[v.Name] = v
	}
	if got.Environment != "prod" || byName["room"].Value != "prod-tag" || byName["base"].Value != "ws://h" {
		t.Errorf("Variables = %+v", got)
	}
	if _, err := svc.Variables(t.Context(), "req_other"); errs.KindOf(err) != errs.NotFound {
		t.Errorf("unknown request: err = %v", err)
	}
	if _, err := New(nil, nil, nil, nil, nil, nil).Variables(t.Context(), "req_ws"); errs.KindOf(err) != errs.Unsupported {
		t.Errorf("no loader: err = %v", err)
	}
}
