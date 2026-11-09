package request

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func TestBuildGraphQLPost(t *testing.T) {
	t.Parallel()
	f := projectfile.GraphQLRequest{
		URL:       "{{base}}/graphql",
		Query:     "query User($id: ID!) { user(id: $id) { name } }",
		Variables: "{\n  \"id\": \"{{user}}\",\n  \"html\": \"<b>&</b>\"\n}",
		Headers:   []projectfile.KeyValue{{Name: "X-Trace", Value: "t"}},
	}
	auth := projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{token}}"}
	expand := buildExpand(map[string]string{"base": "https://api.test", "user": "42", "token": "tok"})

	b, err := BuildGraphQL(f, auth, HTTPOptions{Expand: expand})
	if err != nil {
		t.Fatal(err)
	}
	r := b.Request
	if r.Method != "POST" || r.URL != "https://api.test/graphql" {
		t.Errorf("request line = %s %s", r.Method, r.URL)
	}
	want := `{"query":"query User($id: ID!) { user(id: $id) { name } }","variables":{"id":"42","html":"<b>&</b>"},"operationName":"User"}`
	if got := bodyText(t, r.Body); got != want {
		t.Errorf("body = %s\nwant   %s", got, want)
	}
	if r.Body.ContentType() != "application/json" {
		t.Errorf("content type = %q", r.Body.ContentType())
	}
	wantHeaders := []httpclient.Header{on("X-Trace", "t"), on("Authorization", "Bearer tok"), on("Accept", GraphQLAccept)}
	if !reflect.DeepEqual(r.Headers, wantHeaders) {
		t.Errorf("headers = %+v\nwant %+v", r.Headers, wantHeaders)
	}
}

func TestBuildGraphQLPostMinimal(t *testing.T) {
	t.Parallel()
	f := projectfile.GraphQLRequest{
		Method:    "POST",
		URL:       "http://h/g",
		Query:     "{ me { id } }",
		Variables: "  null ",
		Headers:   []projectfile.KeyValue{{Name: "accept", Value: "application/json"}},
	}
	b, err := BuildGraphQL(f, noAuth, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := bodyText(t, b.Request.Body); got != `{"query":"{ me { id } }"}` {
		t.Errorf("body = %s", got)
	}
	if want := []httpclient.Header{on("accept", "application/json")}; !reflect.DeepEqual(b.Request.Headers, want) {
		t.Errorf("headers = %+v, the Accept row should win", b.Request.Headers)
	}
}

func TestBuildGraphQLGet(t *testing.T) {
	t.Parallel()
	f := projectfile.GraphQLRequest{
		Method:        "GET",
		URL:           "http://h/graphql?x=1#top",
		Query:         "query A { a }\nmutation B { b }",
		Variables:     `{"n": 1}`,
		OperationName: "A",
	}
	b, err := BuildGraphQL(f, noAuth, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r := b.Request
	want := "http://h/graphql?x=1&query=query%20A%20%7B%20a%20%7D%0Amutation%20B%20%7B%20b%20%7D&variables=%7B%22n%22%3A1%7D&operationName=A#top"
	if r.Method != "GET" || r.URL != want {
		t.Errorf("request line = %s %s\nwant URL %s", r.Method, r.URL, want)
	}
	if r.Body != nil {
		t.Errorf("GET has a body")
	}
	if len(b.Warnings) != 0 {
		t.Errorf("warnings = %+v", b.Warnings)
	}

	f.OperationName = "B"
	b, err = BuildGraphQL(f, noAuth, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Warnings) != 1 || b.Warnings[0].Code != WarnGraphQLMutationOverGET {
		t.Errorf("warnings = %+v, want the mutation warning", b.Warnings)
	}
}

func TestBuildGraphQLErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		f    projectfile.GraphQLRequest
		code string
	}{
		{"no URL", projectfile.GraphQLRequest{Query: "{a}"}, CodeURLMissing},
		{"no query", projectfile.GraphQLRequest{URL: "http://h", Query: " \n"}, CodeGraphQLQueryMissing},
		{"bad method", projectfile.GraphQLRequest{URL: "http://h", Query: "{a}", Method: "PUT"}, CodeGraphQLInvalidMethod},
		{"bad JSON", projectfile.GraphQLRequest{URL: "http://h", Query: "{a}", Variables: `{"a":`}, CodeGraphQLInvalidVariables},
		{"not an object", projectfile.GraphQLRequest{URL: "http://h", Query: "{a}", Variables: `[1]`}, CodeGraphQLInvalidVariables},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := BuildGraphQL(tt.f, noAuth, HTTPOptions{})
			var e *errs.Error
			if !errors.As(err, &e) || e.Code != tt.code {
				t.Fatalf("err = %v, want code %s", err, tt.code)
			}
		})
	}
}

func TestBuildGraphQLUndefinedVariable(t *testing.T) {
	t.Parallel()
	f := projectfile.GraphQLRequest{URL: "http://h", Query: "{a}", Variables: `{"id": "{{missing}}"}`}
	_, err := BuildGraphQL(f, noAuth, HTTPOptions{Expand: buildExpand(nil)})
	var e *errs.Error
	if !errors.As(err, &e) || e.Details[DetailField] != "variables" {
		t.Fatalf("err = %v, want an error on the variables", err)
	}
}

// ENV-10: {{variables}} in the query are rendered; GraphQL's $variables
// are left alone.
func TestBuildGraphQLRendersQuery(t *testing.T) {
	t.Parallel()
	f := projectfile.GraphQLRequest{
		URL:   "http://h/g",
		Query: `query Q($id: ID!) { user(id: $id, tenant: "{{tenant}}") { name } }`,
	}
	b, err := BuildGraphQL(f, noAuth, HTTPOptions{Expand: buildExpand(map[string]string{"tenant": "acme"})})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"query":"query Q($id: ID!) { user(id: $id, tenant: \"acme\") { name } }","operationName":"Q"}`
	if got := bodyText(t, b.Request.Body); got != want {
		t.Errorf("body = %s\nwant   %s", got, want)
	}

	f.Method = "GET"
	b, err = BuildGraphQL(f, noAuth, HTTPOptions{Expand: buildExpand(map[string]string{"tenant": "acme"})})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.Request.URL, "acme") || strings.Contains(b.Request.URL, "tenant%7D%7D") {
		t.Errorf("GET URL = %s, want the rendered query", b.Request.URL)
	}

	f.Query = `{ user(tenant: "{{missing}}") { name } }`
	_, err = BuildGraphQL(f, noAuth, HTTPOptions{Expand: buildExpand(nil)})
	var e *errs.Error
	if !errors.As(err, &e) || e.Details[DetailField] != "query" {
		t.Fatalf("err = %v, want an error on the query", err)
	}
}

func TestGraphQLOperations(t *testing.T) {
	t.Parallel()
	got := GraphQLOperations("query A { a } fragment F on T { x } mutation { b } subscription S { c }")
	want := []GraphQLOperation{{"A", "query"}, {"", "mutation"}, {"S", "subscription"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ops = %+v, want %+v", got, want)
	}
	if n := GraphQLOperationName([]GraphQLOperation{{"Only", "query"}}, "Stale"); n != "Only" {
		t.Errorf("single operation name = %q", n)
	}
	if n := GraphQLOperationName(want, "S"); n != "S" {
		t.Errorf("saved name = %q", n)
	}
}

var noAuth = projectfile.Auth{Type: projectfile.AuthNone}

// A document with @defer or @stream asks for incremental delivery
// (GQL-08); an Accept row still wins.
func TestBuildGraphQLIncrementalAccept(t *testing.T) {
	t.Parallel()
	for query, want := range map[string]string{
		"{ me { id ... @defer { name } } }":                                     GraphQLIncrementalAccept,
		"{ me { friends @stream(initialCount: 1) { id } } }":                    GraphQLIncrementalAccept,
		"{ me { ...F } } fragment F on User { ... @defer(label: \"x\") { a } }": GraphQLIncrementalAccept,
		"{ me { id } } # @defer in a comment":                                   GraphQLAccept,
		"{ me(q: \"@stream\") { id @skip(if: false) } }":                        GraphQLAccept,
	} {
		b, err := BuildGraphQL(projectfile.GraphQLRequest{URL: "http://h/g", Query: query}, noAuth, HTTPOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := b.Request.Headers[len(b.Request.Headers)-1]; got != on("Accept", want) {
			t.Errorf("%s: %+v", query, got)
		}
	}
	f := projectfile.GraphQLRequest{URL: "http://h/g", Query: "{ a @stream }", Headers: []projectfile.KeyValue{{Name: "Accept", Value: "application/json"}}}
	b, err := BuildGraphQL(f, noAuth, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []httpclient.Header{on("Accept", "application/json")}; !reflect.DeepEqual(b.Request.Headers, want) {
		t.Errorf("headers = %+v", b.Request.Headers)
	}
}
