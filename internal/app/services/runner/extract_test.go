package runner

import (
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	"github.com/sorrelfox/postsonde/internal/core/runtimevars"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// TestRunExtractsLoginToken (TEST-05, in a folder run): each iteration
// logs in, the token extracted from the login's response reaches the next
// request of the same run, and so does a value a run-scope rule set. A
// failed extraction fails its result and stops a run that stops on
// failure. A run's own variables do not outlive it.
func TestRunExtractsLoginToken(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var seen []string
	logins := 0
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/login":
			logins++
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-Id", fmt.Sprintf("rid-%d", logins))
			fmt.Fprintf(w, `{"token": "tok-%d-s3cret"}`, logins)
		default:
			seen = append(seen, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Rid"))
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
				w.WriteHeader(nethttp.StatusUnauthorized)
			}
		}
	}))
	t.Cleanup(srv.Close)

	files := map[string]string{
		"auth/_folder.json": `{"kind": "folder", "id": "fld_auth", "name": "Auth", "order": ["login.json", "me.json", "broken.json"]}`,
		"auth/login.json": httpFile("req_login", "Log in", "POST", "/login", `, "extractions": [
			{"source": "jsonpath", "expr": "$.token", "variable": "token", "secret": true},
			{"source": "header", "expr": "X-Request-Id", "variable": "rid", "scope": "run"}]`),
		"auth/me.json": httpFile("req_me", "Me", "GET", "/me", `, "headers": [{"name": "Authorization", "value": "Bearer {{token}}"}, {"name": "X-Rid", "value": "{{rid}}"}],
			"assertions": [{"target": "status", "op": "equals", "expected": "200"}]`),
		"auth/broken.json": httpFile("req_broken", "Broken", "POST", "/login", `, "extractions": [{"source": "jsonpath", "expr": "$.nope", "variable": "nope"}]`),
	}
	var reg *runtimevars.Registry
	f := newFixture(t, srv.URL, files, func(m *coreworkspace.Manager) httpservice.Option {
		reg = runtimevars.NewRegistry(m.Current, nil)
		return httpservice.WithRuntimeVariables(reg)
	})

	id := f.start(t, RunInputDTO{FolderID: "fld_auth", Iterations: 2, RequestIDs: []string{"req_login", "req_me"}})
	f.rec.wait(t, id)
	results := f.rec.resultsOf(id)
	var outcomes []string
	for _, r := range results {
		outcomes = append(outcomes, r.Name+" "+r.Outcome)
	}
	if want := []string{"Log in passed", "Me passed", "Log in passed", "Me passed"}; !slices.Equal(outcomes, want) {
		t.Fatalf("outcomes = %v, want %v", outcomes, want)
	}
	mu.Lock()
	got := slices.Clone(seen)
	mu.Unlock()
	// Each iteration's request carries the token of that iteration's login.
	if want := []string{"Bearer tok-1-s3cret|rid-1", "Bearer tok-2-s3cret|rid-2"}; !slices.Equal(got, want) {
		t.Errorf("the next requests got %v, want %v", got, want)
	}
	login := results[0]
	if len(login.Extractions) != 2 || login.Extractions[0].Outcome != httpservice.ExtractionSet || login.Extractions[0].Value != "••••••" ||
		login.Extractions[1].Value != "rid-1" {
		t.Errorf("login extractions = %+v", login.Extractions)
	}
	if strings.Contains(fmt.Sprint(results), "s3cret") {
		t.Errorf("the results show the token: %+v", results)
	}
	// The runtime variable outlives the run; the run's variable does not.
	store, err := reg.Current()
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := store.Get("token"); !ok || v.Value != "tok-2-s3cret" || !v.Secret {
		t.Errorf("runtime token = %+v", v)
	}
	if _, ok := store.Get("rid"); ok {
		t.Error("the run variable became a runtime one")
	}
	id = f.start(t, RunInputDTO{FolderID: "fld_auth", Iterations: 1, RequestIDs: []string{"req_me"}})
	f.rec.wait(t, id)
	if r := f.rec.resultsOf(id); len(r) != 1 || r[0].Outcome != OutcomeError || r[0].Error == nil || !strings.Contains(r[0].Error.Message, "rid") {
		t.Errorf("a later run saw the earlier run's variable: %+v", r)
	}

	// A failed extraction fails the result and stops a run that stops on
	// failure: the request after it is not sent.
	id = f.start(t, RunInputDTO{FolderID: "fld_auth", Iterations: 1, RequestIDs: []string{"req_broken", "req_me"}, StopOnFailure: true, AllowUnresolved: true})
	f.rec.wait(t, id)
	r := f.rec.resultsOf(id)
	if len(r) != 1 || r[0].Outcome != OutcomeFailed || r[0].TestsFailed != 0 || len(r[0].Extractions) != 1 ||
		r[0].Extractions[0].Outcome != httpservice.ExtractionFailed || r[0].Extractions[0].Message != "JSONPath $.nope selects nothing in the body." {
		t.Errorf("stop at a failed extraction: %+v", r)
	}
}
