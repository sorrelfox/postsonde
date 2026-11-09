package http

import (
	"errors"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// namedServer answers with its name and the request target it got.
func namedServer(t *testing.T, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		fmt.Fprintf(w, "%s %s", name, r.URL.RequestURI())
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRelativeURLFollowsTheEnvironment (COL-08): a request whose URL
// is a path ("/users/{{id}}") is sent to the {{baseUrl}} of the scopes, so
// switching environments sends it to another server. The global baseUrl
// (the base environment) is the default; an environment overrides it; a
// base with a path keeps it. The copies (curl, HAR, the snippet panel) and
// the GraphQL endpoint use the same URL; the templated snippet keeps
// {{baseUrl}}. An undefined baseUrl is an undefined variable; a base that
// is not an absolute URL is refused.
func TestRelativeURLFollowsTheEnvironment(t *testing.T) {
	t.Parallel()
	f := newProjectFixture(t)
	prod, staging := namedServer(t, "prod"), namedServer(t, "staging")

	if _, err := collection.SaveGlobalVariables(f.coll, t.Context(), []projectfile.Variable{
		{Name: "baseUrl", Value: prod.URL},
		{Name: "id", Value: "7"},
	}); err != nil {
		t.Fatal(err)
	}
	envWith := func(name string, vars ...environments.Variable) string {
		t.Helper()
		env, err := f.envs.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.envs.SaveVariables(env.ID, vars); err != nil {
			t.Fatal(err)
		}
		return env.ID
	}
	stagingID := envWith("Staging", environments.Variable{Name: "baseUrl", Value: staging.URL})
	inheritsID := envWith("Inherits")
	prefixedID := envWith("Prefixed", environments.Variable{Name: "baseUrl", Value: staging.URL + "/api/v2/"})
	secretID := envWith("Secret", environments.Variable{Name: "baseUrl", Value: staging.URL, Secret: true})
	localID := envWith("Local", environments.Variable{Name: "baseUrl", Value: prod.URL, Local: true, LocalValue: staging.URL + "/mine"})
	badID := envWith("Bad", environments.Variable{Name: "baseUrl", Value: "api.example.com"})

	f.write(t, "users.json", `{"kind": "http", "id": "req_users", "name": "Users", "method": "GET", "url": "/users/{{id}}?q=1"}`)
	f.write(t, "gql.json", `{"kind": "graphql", "id": "req_gql", "name": "Gql", "url": "/graphql", "query": "{ a }"}`)
	f.reload(t)

	for _, c := range []struct{ env, want string }{
		{stagingID, "staging /users/7?q=1"},
		{inheritsID, "prod /users/7?q=1"},
		{prefixedID, "staging /api/v2/users/7?q=1"},
		{secretID, "staging /users/7?q=1"},
		{localID, "staging /mine/users/7?q=1"},
		{"", "prod /users/7?q=1"},
		{stagingID, "staging /users/7?q=1"},
	} {
		if err := f.envs.SetActive(c.env); err != nil {
			t.Fatal(err)
		}
		if got := f.sendBody(t, "req_users"); got != c.want {
			t.Errorf("env %q: sent to %q, want %q", c.env, got, c.want)
		}
	}

	// The copies send it to the same server.
	if err := f.envs.SetActive(stagingID); err != nil {
		t.Fatal(err)
	}
	curl, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_users"})
	if err != nil || !strings.Contains(curl.Command, staging.URL+"/users/7?q=1") {
		t.Errorf("curl = %+v, %v", curl, err)
	}
	h, _, err := f.svc.buildHAR(t.Context(), ExportHARInputDTO{RequestIDs: []string{"req_users"}}, time.Now())
	if err != nil || len(h.Log.Entries) != 1 || h.Log.Entries[0].Request.URL != staging.URL+"/users/7?q=1" {
		t.Errorf("HAR = %+v, %v", h, err)
	}
	resolved, err := f.svc.Snippet(t.Context(), SnippetInputDTO{RequestID: "req_users", Mode: SnippetResolved})
	if err != nil || !strings.Contains(resolved.Code, staging.URL+"/users/7?q=1") {
		t.Errorf("resolved snippet = %+v, %v", resolved, err)
	}
	templated, err := f.svc.Snippet(t.Context(), SnippetInputDTO{RequestID: "req_users", Mode: SnippetTemplated})
	if err != nil || !strings.Contains(templated.Code, "{{baseUrl}}/users/{{id}}?q=1") {
		t.Errorf("templated snippet = %+v, %v", templated, err)
	}
	if endpoint, err := GraphQLEndpoint(t.Context(), f.svc, "req_gql"); err != nil || endpoint != staging.URL+"/graphql" {
		t.Errorf("GraphQL endpoint = %q, %v", endpoint, err)
	}

	// A secret base URL is masked in the copies.
	if err := f.envs.SetActive(secretID); err != nil {
		t.Fatal(err)
	}
	curl, err = f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_users"})
	if err != nil || !curl.Masked || strings.Contains(curl.Command, staging.URL) || !strings.Contains(curl.Command, "••••••/users/7") {
		t.Errorf("masked curl = %+v, %v", curl, err)
	}

	// A base that is not an absolute URL is refused.
	if err := f.envs.SetActive(badID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_users"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != request.CodeInvalidBaseURL {
		t.Errorf("bad base: err = %v", err)
	}

	// No baseUrl anywhere: an undefined variable, as for any reference.
	if _, err := collection.SaveGlobalVariables(f.coll, t.Context(), []projectfile.Variable{{Name: "id", Value: "7"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.envs.SetActive(inheritsID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_users"})
	if !errors.As(err, &e) || e.Code != template.CodeUnresolved || !strings.Contains(e.Details["names"], "baseUrl") {
		t.Errorf("no base: err = %v", err)
	}
	// The templated snippet still shows where the request goes.
	templated, err = f.svc.Snippet(t.Context(), SnippetInputDTO{RequestID: "req_users", Mode: SnippetTemplated})
	if err != nil || !strings.Contains(templated.Code, "{{baseUrl}}/users/{{id}}?q=1") {
		t.Errorf("templated snippet without a base = %+v, %v", templated, err)
	}
}
