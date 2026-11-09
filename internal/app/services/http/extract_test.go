package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/extract"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/runner"
	"github.com/sorrelfox/postsonde/internal/core/runtimevars"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// loginToken is the secret the login server hands out.
const loginToken = "tok-S3cret-e2e-4711"

// loginServer is a login endpoint and an API that needs its token:
// POST /login answers {"access_token": …, "user": {"id": 7}} with a
// session cookie and an X-Request-Id; /me says what it got.
type loginServer struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newLoginServer(t *testing.T) *loginServer {
	t.Helper()
	s := &loginServer{}
	s.Server = httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		switch r.URL.Path {
		case "/login":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-Id", "rid-1")
			nethttp.SetCookie(w, &nethttp.Cookie{Name: "session", Value: "sess-9", Path: "/"})
			fmt.Fprintf(w, `{"access_token": %q, "user": {"id": 7, "roles": ["admin"]}}`, loginToken)
		default:
			line := fmt.Sprintf("%s|%s|%s", r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("X-Rid"))
			s.mu.Lock()
			s.seen = append(s.seen, line)
			s.mu.Unlock()
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, line)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// loginProject writes a login request with extraction rules and a
// request that uses what they set.
func loginProject(t *testing.T, f *projectFixture, base string) {
	t.Helper()
	if _, err := collection.SaveGlobalVariables(f.coll, t.Context(), []projectfile.Variable{{Name: "base", Value: base}}); err != nil {
		t.Fatal(err)
	}
	f.write(t, "auth/_folder.json", `{"kind": "folder", "id": "fld_auth", "name": "Auth", "order": ["login.json", "me.json"]}`)
	f.write(t, "auth/login.json", `{"kind": "http", "id": "req_login", "name": "Log in", "method": "POST", "url": "{{base}}/login", "auth": {"type": "none"},
		"assertions": [{"target": "jsonpath", "expr": "$.access_token", "op": "exists"}],
		"extractions": [
			{"source": "jsonpath", "expr": "$.access_token", "variable": "token", "secret": true},
			{"source": "jsonpath", "expr": "$.user.id", "variable": "userId", "persist": true},
			{"source": "cookie", "expr": "session", "variable": "session"},
			{"source": "header", "expr": "X-Request-Id", "variable": "rid", "scope": "run"},
			{"source": "header", "expr": "X-Missing", "variable": "missing"},
			{"source": "jsonpath", "expr": "$.{{nope}}", "variable": "templated"},
			{"source": "status", "variable": "never", "disabled": true},
			{"source": "regex", "expr": "\"roles\":\\s*(\\[[^\\]]*\\])", "variable": "roles"}
		]}`)
	f.write(t, "auth/me.json", `{"kind": "http", "id": "req_me", "name": "Me", "method": "GET", "url": "{{base}}/me?user={{userId}}&s={{session}}", "auth": {"type": "none"},
		"headers": [{"name": "Authorization", "value": "Bearer {{token}}"}, {"name": "X-Token", "value": "{{token}}"}, {"name": "X-Rid", "value": "{{rid}}"}]}`)
	f.reload(t)
}

// TestExtractLoginTokenSingleSends (TEST-05): a
// login response's token is extracted into a secret runtime variable, and
// the next send carries it. Every rule's result is listed: set, failed
// (a header that is not there), invalid (an undefined variable in the
// expression), skipped (a run variable outside a run); a disabled rule is
// not listed. Nothing is written to a project file.
func TestExtractLoginTokenSingleSends(t *testing.T) {
	t.Parallel()
	srv := newLoginServer(t)
	f := newProjectFixture(t)
	loginProject(t, f, srv.URL)

	started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_login"})
	if err != nil {
		t.Fatal(err)
	}
	f.rec.wait(t, started.OpID)
	login := f.rec.response(t, started.OpID)
	type row struct {
		index                          int
		variable, outcome, value, code string
	}
	var got []row
	for _, e := range login.Extractions {
		got = append(got, row{e.Index, e.Variable, e.Outcome, e.Value, e.Code})
	}
	want := []row{
		{0, "token", ExtractionSet, httpclient.Mask, ""},
		{1, "userId", ExtractionSet, "7", ""},
		{2, "session", ExtractionSet, "sess-9", ""},
		{3, "rid", ExtractionSkipped, "", ""},
		{4, "missing", ExtractionFailed, "", extract.CodeNotFound},
		{5, "templated", ExtractionInvalid, "", "template.unresolved"},
		{7, "roles", ExtractionSet, `["admin"]`, ""},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("extractions =\n%v\nwant\n%v", got, want)
	}
	msgs := map[string]string{}
	for _, e := range login.Extractions {
		msgs[e.Variable] = e.Message
	}
	for name, msg := range map[string]string{
		"token":   "Set the runtime variable token from JSONPath $.access_token.",
		"rid":     "Not set: rid is a run variable, set during a folder run only.",
		"missing": "Header X-Missing is not in the response.",
	} {
		if msgs[name] != msg {
			t.Errorf("%s: message %q, want %q", name, msgs[name], msg)
		}
	}
	if login.Extractions[0].Scope != "runtime" || !login.Extractions[0].Secret || login.Extractions[3].Scope != "run" {
		t.Errorf("rules = %+v", login.Extractions)
	}
	// The token's assertion shows it masked.
	if len(login.Tests) != 1 || login.Tests[0].Outcome != "passed" || strings.Contains(login.Tests[0].Actual+login.Tests[0].Message, loginToken) {
		t.Errorf("tests = %+v", login.Tests)
	}

	rt, err := f.svc.runtimeVariables(f.ws)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := rt.Get("token"); !ok || v.Value != loginToken || !v.Secret || v.Persist {
		t.Errorf("token = %+v", v)
	}
	if v, ok := rt.Get("userId"); !ok || v.Value != "7" || !v.Persist {
		t.Errorf("userId = %+v", v)
	}
	for _, name := range []string{"rid", "missing", "templated", "never"} {
		if _, ok := rt.Get(name); ok {
			t.Errorf("%s was set", name)
		}
	}

	// The next request carries the token: two single sends. rid is not
	// set outside a run and is sent as written.
	started, err = f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_me", AllowUnresolved: true})
	if err != nil {
		t.Fatal(err)
	}
	f.rec.wait(t, started.OpID)
	me := f.rec.response(t, started.OpID)
	if want := "/me?user=7&s=sess-9|Bearer " + loginToken + "|{{rid}}"; me.Body != want {
		t.Errorf("the next request sent %q, want %q", me.Body, want)
	}
	if len(me.Extractions) != 0 || me.Extractions == nil {
		t.Errorf("me extractions = %#v", me.Extractions)
	}

	// A later failure keeps the value: the variable is not cleared.
	f.write(t, "auth/login.json", `{"kind": "http", "id": "req_login", "name": "Log in", "method": "POST", "url": "{{base}}/nope", "auth": {"type": "none"},
		"extractions": [{"source": "jsonpath", "expr": "$.access_token", "variable": "token", "secret": true}]}`)
	f.reload(t)
	started, err = f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_login"})
	if err != nil {
		t.Fatal(err)
	}
	f.rec.wait(t, started.OpID)
	if again := f.rec.response(t, started.OpID); len(again.Extractions) != 1 || again.Extractions[0].Outcome != ExtractionFailed ||
		again.Extractions[0].Code != extract.CodeUnreadableBody {
		t.Errorf("failed extraction = %+v", again.Extractions)
	}
	if v, _ := rt.Get("token"); v.Value != loginToken {
		t.Errorf("token after a failed extraction = %q", v.Value)
	}

	// Only the rules are in the project files, never the values.
	assertNoValueInProject(t, f.dir, loginToken, "sess-9")
}

// TestExtractLoginTokenInRun (TEST-05): inside a folder run the token a
// login extracts reaches the next request of the same run, a run-scope
// rule sets the run's variable only, and a runtime rule also sets the
// run's variable of the name, which would hide it otherwise.
func TestExtractLoginTokenInRun(t *testing.T) {
	t.Parallel()
	srv := newLoginServer(t)
	f := newProjectFixture(t)
	loginProject(t, f, srv.URL)

	vars := runner.NewVars()
	// A stale run value of token: the runtime rule must replace it here too.
	vars.Set("token", template.Variable{Value: "stale"})
	ctx := netx.WithUserOp(t.Context(), netx.UserOp{Kind: "runner.run", ID: "1"})
	run := NewRunSession(f.svc)
	o := RunOptions{Vars: vars, AllowUnresolved: true}
	login := run.Send(ctx, "req_login", o, nil)
	me := run.Send(ctx, "req_me", o, nil)
	run.Close()
	if login.Response == nil || me.Response == nil {
		t.Fatalf("login = %+v, me = %+v", login, me)
	}
	if want := "/me?user=7&s=sess-9|Bearer " + loginToken + "|rid-1"; me.Response.Body != want {
		t.Errorf("the next request of the run sent %q, want %q", me.Response.Body, want)
	}
	byName := map[string]ExtractionResultDTO{}
	for _, e := range login.Response.Extractions {
		byName[e.Variable] = e
	}
	if e := byName["rid"]; e.Outcome != ExtractionSet || e.Value != "rid-1" || e.Message != "Set the run variable rid from header X-Request-Id." {
		t.Errorf("rid = %+v", e)
	}
	if e := byName["token"]; e.Outcome != ExtractionSet || e.Message != "Set the runtime variable token, and the run's, from JSONPath $.access_token." {
		t.Errorf("token = %+v", e)
	}
	if v, ok := vars.Lookup("token"); !ok || v.Value != loginToken || !v.Secret {
		t.Errorf("run token = %+v", v)
	}
	rt, err := f.svc.runtimeVariables(f.ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rt.Get("rid"); ok {
		t.Error("a run-scope rule set a runtime variable")
	}
	if v, _ := rt.Get("token"); v.Value != loginToken {
		t.Errorf("runtime token = %q", v.Value)
	}
	// The run's timeline and error messages never show the token.
	data, _ := json.Marshal([]any{login.Response.Timeline, me.Response.Timeline, login.Response.Extractions})
	if strings.Contains(string(data), loginToken) {
		t.Errorf("the run's responses show the token:\n%s", data)
	}
}

// TestExtractedSecretMasked (TEST-05): a secret value extracted from a
// login is masked in the timeline, the history (the login's own entry,
// whose body held it, and the next request's), a copied cURL command and
// a HAR export of the next request.
func TestExtractedSecretMasked(t *testing.T) {
	t.Parallel()
	srv := newLoginServer(t)
	var hist historyEntries
	harPath := filepath.Join(t.TempDir(), "out.har")
	f := newProjectFixture(t, hist.option(),
		WithSaveBody("", func(context.Context, SaveDialog) (string, error) { return harPath, nil }))
	loginProject(t, f, srv.URL)

	for _, id := range []string{"req_login", "req_me"} {
		started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: id, AllowUnresolved: true})
		if err != nil {
			t.Fatal(err)
		}
		f.rec.wait(t, started.OpID)
		resp := f.rec.response(t, started.OpID)
		for i, hop := range resp.Timeline.Hops {
			if strings.Contains(hop.Request.Head+hop.Request.Target+hop.ResponseHead, loginToken) {
				t.Errorf("%s: timeline hop %d shows the token", id, i)
			}
		}
		// Masked as a secret variable's value, not only as an
		// Authorization field.
		if id == "req_me" && !strings.Contains(resp.Timeline.Hops[0].Request.Head, "X-Token: "+httpclient.Mask) {
			t.Errorf("me timeline head:\n%s", resp.Timeline.Hops[0].Request.Head)
		}
	}

	entries := hist.list()
	if len(entries) != 2 {
		t.Fatalf("history = %d entries", len(entries))
	}
	for i, e := range entries {
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), loginToken) || !e.Masked {
			t.Errorf("history entry %d (%s) holds the token or is not marked masked:\n%s", i, e.RequestName, data)
		}
	}

	curl, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_me"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(curl.Command, loginToken) || !curl.Masked {
		t.Errorf("cURL command:\n%s", curl.Command)
	}
	res, err := f.svc.ExportHAR(t.Context(), ExportHARInputDTO{Name: "Auth", RequestIDs: []string{"req_me"}})
	if err != nil || !res.Masked || res.Entries != 1 {
		t.Fatalf("ExportHAR = %+v, %v", res, err)
	}
	if har := readFile(t, harPath); strings.Contains(har, loginToken) {
		t.Errorf("the HAR file holds the token:\n%s", har)
	}
}

// assertNoValueInProject fails when a file of the project holds one of
// values.
func assertNoValueInProject(t *testing.T, dir string, values ...string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, v := range values {
			if strings.Contains(string(data), v) {
				t.Errorf("%s holds %q", path, v)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestExtractNoRuntimeVariables: without runtime variables (a service
// built without them) a runtime rule fails with its code; a run rule in a
// run still works.
func TestExtractNoRuntimeVariables(t *testing.T) {
	t.Parallel()
	x := &send{
		extractions: []extractItem{
			{index: 0, rule: extract.Rule{Source: extract.SourceStatus, Variable: "code", Scope: extract.ScopeRuntime}},
			{index: 1, rule: extract.Rule{Source: extract.SourceStatus, Variable: "code", Scope: extract.ScopeRun}},
		},
		runVars: runner.NewVars(),
	}
	out, secrets := x.runExtractions(t.Context(), &httpclient.Response{StatusCode: 204}, nil)
	if len(out) != 2 || out[0].Outcome != ExtractionFailed || out[0].Code != CodeNoRuntimeVariables || out[1].Outcome != ExtractionSet || out[1].Value != "204" || secrets != nil {
		t.Errorf("results = %+v, %v", out, secrets)
	}
	if v, ok := x.runVars.(*runner.Vars).Lookup("code"); !ok || v.Value != "204" {
		t.Errorf("run code = %+v", v)
	}
	// A broken store says why.
	x.runtimeStore = func() (*runtimevars.Store, error) {
		return nil, fmt.Errorf("boom")
	}
	x.runVars = nil
	out, _ = x.runExtractions(t.Context(), &httpclient.Response{StatusCode: 204}, nil)
	if out[0].Outcome != ExtractionFailed || out[0].Message != "The runtime variable code could not be set: boom" || out[1].Outcome != ExtractionSkipped {
		t.Errorf("results = %+v", out)
	}
}

// TestExtractValueClipped: a long value is set whole and shown cut.
func TestExtractValueClipped(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", MaxExtractionShown)
	x := &send{
		extractions: []extractItem{{rule: extract.Rule{Source: extract.SourceHeader, Expr: "X-Long", Variable: "long", Scope: extract.ScopeRun}}},
		runVars:     runner.NewVars(),
	}
	out, _ := x.runExtractions(t.Context(), &httpclient.Response{StatusCode: 200, Headers: []httpclient.Header{{Name: "X-Long", Value: long}}}, nil)
	if !out[0].ValueClipped || len(out[0].Value) > MaxExtractionShown+len("…") || !strings.HasSuffix(out[0].Value, "…") {
		t.Errorf("value = %d bytes, clipped %v", len(out[0].Value), out[0].ValueClipped)
	}
	if v, _ := x.runVars.(*runner.Vars).Lookup("long"); v.Value != long {
		t.Error("the value set was cut")
	}
}
