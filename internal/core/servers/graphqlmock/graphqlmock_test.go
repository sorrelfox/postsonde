package graphqlmock_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/graphqlmock"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// The server is driven by PostSonde's own GraphQL client: requests are
// built by request.BuildGraphQL (what a GraphQL request sends) and sent
// with httpclient; the schema is read back with the app's introspection
// query and schema.ParseIntrospection. The GraphQL service's own fetch
// runs against it in internal/app/services/graphql.

type events struct {
	mu   sync.Mutex
	list []servers.Event
}

func (e *events) add(ev servers.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, ev)
}

func (e *events) all() []servers.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.list)
}

type fixture struct {
	m      *servers.Manager
	id     string
	url    string
	rec    *events
	client *httpclient.Client
}

func start(t *testing.T, cfg graphqlmock.Config) *fixture {
	t.Helper()
	rec := &events{}
	m := servers.NewManager(servers.Options{
		Types:       []servers.Type{graphqlmock.New(graphqlmock.Options{})},
		OnTraffic:   rec.add,
		StopTimeout: 2 * time.Second,
		Logger:      slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() { m.StopAll(context.Background()) })
	s, err := m.Add(servers.Spec{Type: graphqlmock.TypeName, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	s, err = m.Start(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	client := httpclient.New(netx.NewDirect(netx.Options{}), httpclient.TransportOptions{})
	return &fixture{m: m, id: s.ID, url: "http://127.0.0.1:" + strconv.Itoa(s.Port()), rec: rec, client: client}
}

// send builds the GraphQL request as the app does and sends it.
func (f *fixture) send(t *testing.T, g projectfile.GraphQLRequest) *httpclient.Response {
	t.Helper()
	if g.URL == "" {
		g.URL = f.url + "/graphql"
	}
	b, err := request.BuildGraphQL(g, projectfile.Auth{Type: projectfile.AuthNone}, request.HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f.client.Send(t.Context(), b.Request)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func header(r *httpclient.Response, name string) string {
	for _, h := range r.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

func TestClientIntrospectsTheSchema(t *testing.T) {
	t.Parallel()
	f := start(t, graphqlmock.DefaultConfig())
	want, err := schema.ParseSDL(graphqlmock.ExampleSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{schema.FullIntrospectionQuery, schema.ConservativeIntrospectionQuery} {
		resp := f.send(t, projectfile.GraphQLRequest{Query: q, OperationName: schema.IntrospectionOperationName})
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(header(resp, "Content-Type"), graphqlmock.MediaGraphQLResponse) {
			t.Fatalf("status %d, %s: %s", resp.StatusCode, header(resp, "Content-Type"), resp.Body)
		}
		got, err := schema.ParseIntrospection(resp.Body)
		if err != nil {
			t.Fatalf("ParseIntrospection: %v\n%.500s", err, resp.Body)
		}
		if schema.SDL(got) != schema.SDL(want) {
			t.Errorf("introspected schema:\n%s\nwant:\n%s", schema.SDL(got), schema.SDL(want))
		}
	}
}

// The fixtures of the schema model, served and introspected back, give
// the same schema: every type kind, interfaces of interfaces, @oneOf,
// deprecations, defaults, repeatable directives.
func TestIntrospectionRoundTripsFixtures(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("..", "..", "graphql", "schema", "testdata")
	for _, name := range []string{"github-subset.json", "countries-full.json", "sdl/github-style.graphql", "sdl/starwars.graphql"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		cfg := graphqlmock.DefaultConfig()
		cfg.SchemaText = string(data)
		f := start(t, cfg)
		resp := f.send(t, projectfile.GraphQLRequest{Query: schema.FullIntrospectionQuery})
		got, err := schema.ParseIntrospection(resp.Body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var want *schema.Schema
		if strings.HasSuffix(name, ".json") {
			want, _ = schema.ParseIntrospection(data)
		} else {
			want, _ = schema.ParseSDL(string(data))
		}
		if schema.SDL(got) != schema.SDL(want) {
			t.Errorf("%s: introspected schema differs:\n%s", name, schema.SDL(got))
		}
	}
}

func TestClientQueryWithFragmentsAndVariables(t *testing.T) {
	t.Parallel()
	cfg := graphqlmock.DefaultConfig()
	cfg.Overrides = []graphqlmock.Override{
		{Enabled: true, Target: "Query.user", Args: `{"id": "7"}`, Value: `{"id": "7", "name": "Grace", "role": "ADMIN"}`},
	}
	f := start(t, cfg)
	g := projectfile.GraphQLRequest{
		Query: `query Profile($id: ID!, $withPosts: Boolean = false) {
  me: user(id: $id) { ...UserFields posts @include(if: $withPosts) { title } }
  search(text: "a") { ... on Node { id } ... on Post { __typename } }
}
fragment UserFields on User { __typename name role }`,
		Variables: `{"id": 7}`,
	}
	for _, method := range []string{"POST", "GET"} {
		g.Method = method
		resp := f.send(t, g)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d: %s", method, resp.StatusCode, resp.Body)
		}
		var got struct {
			Errors []any
			Data   struct {
				Me     json.RawMessage
				Search []map[string]any
			}
		}
		if err := json.Unmarshal(resp.Body, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Errors) > 0 || string(got.Data.Me) != `{"__typename":"User","name":"Grace","role":"ADMIN"}` {
			t.Errorf("%s: %s", method, resp.Body)
		}
		for _, r := range got.Data.Search {
			if _, ok := r["id"]; !ok || len(r) > 2 {
				t.Errorf("%s: search result %v", method, r)
			}
		}
	}
	// Events: request with the operation and variables, response with the
	// result.
	var req, res *servers.Event
	for _, e := range f.rec.all() {
		if e.GraphQL == nil || e.GraphQL.Request != 1 {
			continue
		}
		switch e.Kind {
		case servers.KindRequest:
			req = &e
		case servers.KindResponse:
			res = &e
		}
	}
	if req == nil || req.Summary != "query Profile (POST /graphql)" || req.GraphQL.Variables != `{"id":7}` || req.GraphQL.OperationType != "query" {
		t.Errorf("request event = %+v", req)
	}
	if res == nil || res.Summary != "200 OK: data" || !res.GraphQL.HasData || res.HTTP.Status != 200 || !strings.Contains(string(res.Content), "Grace") {
		t.Errorf("response event = %+v", res)
	}
}

func TestErrorsAndStatusCodes(t *testing.T) {
	t.Parallel()
	f := start(t, graphqlmock.DefaultConfig())
	post := func(accept, ct, body string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, f.url+"/graphql", strings.NewReader(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	invalid := `{"query": "{ user(id: 1) { nmae } }"}`
	// application/json: 200 for a request error.
	resp, body := post("application/json", "application/json", invalid)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") || !strings.Contains(body, `"locations":[{"line":1,"column":17}]`) || strings.Contains(body, `"data"`) {
		t.Errorf("json validation error: %d %s", resp.StatusCode, body)
	}
	// application/graphql-response+json: 400 for a request error, 200 with
	// data and a field error.
	resp, _ = post(graphqlmock.MediaGraphQLResponse, "application/json", invalid)
	if resp.StatusCode != 400 {
		t.Errorf("graphql-response validation error: %d", resp.StatusCode)
	}
	resp, body = post("", "application/json", `{"query": "{ users(first: 1) { id }"}`)
	if resp.StatusCode != 200 || !strings.Contains(body, "Syntax Error") {
		t.Errorf("no Accept: %d %s", resp.StatusCode, body)
	}
	cases := []struct {
		name, accept, ct, body string
		status                 int
		has                    string
	}{
		{"not JSON", "", "application/json", "{", 400, "not valid JSON"},
		{"no query", "", "application/json", `{"variables": {}}`, 400, "has no query"},
		{"variables not an object", "", "application/json", `{"query": "{ users { id } }", "variables": [1]}`, 400, "variables must be a JSON object"},
		{"content type", "", "text/plain", `{ users { id } }`, 415, "not supported"},
		{"accept", "text/html", "application/json", `{"query": "{ users { id } }"}`, 406, "Accept header"},
		{"application/graphql", "", "application/graphql", `{ users(first: 1) { __typename } }`, 200, `"data":{"users":[{"__typename":"User"}]}`},
		{"no subscription root", "", "application/json", `{"query": "subscription { x }"}`, 200, "not supported by the schema"},
	}
	for _, tc := range cases {
		resp, body := post(tc.accept, tc.ct, tc.body)
		if resp.StatusCode != tc.status || !strings.Contains(body, tc.has) {
			t.Errorf("%s: %d %s", tc.name, resp.StatusCode, body)
		}
	}
	// A mutation over GET is refused.
	resp2 := f.send(t, projectfile.GraphQLRequest{Method: "GET", Query: `mutation { createUser(input: {name: "x"}) { id } }`})
	if resp2.StatusCode != 405 || header(resp2, "Allow") != "POST" || !strings.Contains(string(resp2.Body), "Can only perform a mutation operation from a POST request.") {
		t.Errorf("mutation over GET: %d %s", resp2.StatusCode, resp2.Body)
	}
	// Other paths and methods.
	r, err := http.Get(f.url + "/other")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 404 {
		t.Errorf("other path: %d", r.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodDelete, f.url+"/graphql", nil)
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 405 || r.Header.Get("Allow") != "GET, POST" {
		t.Errorf("DELETE: %d %v", r.StatusCode, r.Header)
	}
}

func TestSubscriptionsAreRefused(t *testing.T) {
	t.Parallel()
	cfg := graphqlmock.DefaultConfig()
	cfg.SchemaText = "type Query { a: Int } type Subscription { tick: Int }"
	f := start(t, cfg)
	resp := f.send(t, projectfile.GraphQLRequest{Query: "subscription { tick }"})
	if resp.StatusCode != 400 || !strings.Contains(string(resp.Body), "does not serve subscriptions") {
		t.Errorf("subscription: %d %s", resp.StatusCode, resp.Body)
	}
}

func TestCORS(t *testing.T) {
	t.Parallel()
	cfg := graphqlmock.DefaultConfig()
	cfg.AllowOrigin = "*"
	f := start(t, cfg)
	req, _ := http.NewRequest(http.MethodOptions, f.url+"/graphql", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type, x-token")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 204 || r.Header.Get("Access-Control-Allow-Origin") != "*" || r.Header.Get("Access-Control-Allow-Headers") != "content-type, x-token" {
		t.Errorf("preflight: %d %v", r.StatusCode, r.Header)
	}
	resp := f.send(t, projectfile.GraphQLRequest{Query: "{ __typename }"})
	if header(resp, "Access-Control-Allow-Origin") != "*" {
		t.Errorf("response headers = %+v", resp.Headers)
	}
}

func TestSchemaFromFileAndIntrospection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sdl := filepath.Join(dir, "schema.graphql")
	if err := os.WriteFile(sdl, []byte("type Query { hello: String! }"), 0o600); err != nil {
		t.Fatal(err)
	}
	sch, _ := schema.ParseSDL("type Query { count: Int! }")
	intro, _ := sch.MarshalIntrospection()
	introFile := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(introFile, intro, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cfg   graphqlmock.Config
		query string
		want  string
	}{
		{graphqlmock.Config{SchemaFile: sdl, Introspection: true}, "{ hello }", `"hello":"hello `},
		{graphqlmock.Config{SchemaFile: introFile, Introspection: true}, "{ count }", `"count":`},
		{graphqlmock.Config{SchemaText: string(intro), Introspection: true}, "{ count }", `"count":`},
	} {
		f := start(t, tc.cfg)
		resp := f.send(t, projectfile.GraphQLRequest{Query: tc.query})
		if resp.StatusCode != 200 || !strings.Contains(string(resp.Body), tc.want) {
			t.Errorf("%+v: %d %s", tc.cfg.SchemaFile, resp.StatusCode, resp.Body)
		}
	}
}

func TestSchemaErrorsFailStart(t *testing.T) {
	t.Parallel()
	m := servers.NewManager(servers.Options{Types: []servers.Type{graphqlmock.New(graphqlmock.Options{})}, Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() { m.StopAll(context.Background()) })
	for _, cfg := range []graphqlmock.Config{
		{SchemaText: "type Query { a: }"},
		{SchemaFile: filepath.Join(t.TempDir(), "missing.graphql")},
		{SchemaFile: "relative.graphql"},
		{SchemaText: "type Mutation { a: Int }"},
	} {
		s, err := m.Add(servers.Spec{Type: graphqlmock.TypeName, Config: cfg})
		if err != nil {
			t.Fatal(err)
		}
		_, err = m.Start(t.Context(), s.ID)
		code := codeOf(err)
		if code != graphqlmock.CodeSchemaFailed && code != graphqlmock.CodeRelativePath {
			t.Errorf("%+v: start = %v (%s)", cfg, err, code)
		}
	}
}

func TestNoSchemaAnswersAnError(t *testing.T) {
	t.Parallel()
	f := start(t, graphqlmock.Config{Introspection: true})
	resp := f.send(t, projectfile.GraphQLRequest{Query: "{ a }"})
	if resp.StatusCode != 500 || !strings.Contains(string(resp.Body), "no schema yet") {
		t.Errorf("%d %s", resp.StatusCode, resp.Body)
	}
}

func TestReconfigure(t *testing.T) {
	t.Parallel()
	f := start(t, graphqlmock.DefaultConfig())
	cfg := graphqlmock.DefaultConfig()
	cfg.Overrides = []graphqlmock.Override{
		{Enabled: true, Target: "User.name", Value: `"Ada"`},
		{Enabled: true, Target: "User.nope", Value: `1`},
	}
	if _, err := f.m.UpdateConfig(f.id, cfg); err != nil {
		t.Fatal(err)
	}
	resp := f.send(t, projectfile.GraphQLRequest{Query: "{ user(id: 1) { name } }"})
	if string(resp.Body) != `{"data":{"user":{"name":"Ada"}}}` {
		t.Errorf("after reconfigure: %s", resp.Body)
	}
	// A schema that no longer parses: the old one stays.
	cfg.SchemaText = "type Query {"
	if _, err := f.m.UpdateConfig(f.id, cfg); err != nil {
		t.Fatal(err)
	}
	resp = f.send(t, projectfile.GraphQLRequest{Query: "{ user(id: 1) { name } }"})
	if string(resp.Body) != `{"data":{"user":{"name":"Ada"}}}` {
		t.Errorf("after a broken schema: %s", resp.Body)
	}
	var problems []string
	for _, e := range f.rec.all() {
		if e.Kind == servers.KindError {
			problems = append(problems, e.Summary)
		}
	}
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, `Override 2 (User.nope): the type User has no field "nope".`) || !strings.Contains(joined, "keeps serving the schema it loaded before") {
		t.Errorf("problems logged:\n%s", joined)
	}
}

func TestStopDoesNotHang(t *testing.T) {
	t.Parallel()
	f := start(t, graphqlmock.DefaultConfig())
	// A client that sends half a request body and then nothing.
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "POST /graphql HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{\"query\":")
	// And an idle keep-alive connection.
	resp := f.send(t, projectfile.GraphQLRequest{Query: "{ __typename }"})
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	time.Sleep(50 * time.Millisecond)
	began := time.Now()
	if _, err := f.m.Stop(t.Context(), f.id); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(began); d > 3*time.Second {
		t.Errorf("stop took %v", d)
	}
	srv, _ := f.m.Get(f.id)
	if srv.State != servers.StateStopped {
		t.Errorf("state = %s", srv.State)
	}
	// Started again, it answers.
	if _, err := f.m.Start(t.Context(), f.id); err != nil {
		t.Fatal(err)
	}
}

func TestDescribe(t *testing.T) {
	t.Parallel()
	cfg := graphqlmock.DefaultConfig()
	cfg.Overrides = []graphqlmock.Override{{Enabled: true, Target: "Role", Value: `"ADMIN"`}, {Enabled: true, Target: "User", Value: `{}`}, {Enabled: true, Target: "Query.user", Value: `{`}}
	d, err := graphqlmock.Describe(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Format != graphqlmock.FormatSDL || d.Source != graphqlmock.SourceText || d.QueryFields != 3 || d.MutationFields != 1 || d.SDL == "" {
		t.Errorf("description = %+v", d)
	}
	var targets []string
	for _, tg := range d.Targets {
		targets = append(targets, tg.Target)
	}
	if got := strings.Join(targets, " "); !strings.HasPrefix(got, "Query.user Query.users Query.search Mutation.createUser Node.id User.id") || !strings.HasSuffix(got, "Role DateTime") {
		t.Errorf("targets = %s", got)
	}
	if len(d.Problems) != 2 || d.Problems[0].OverrideIndex != 1 || d.Problems[1].Field != "value" {
		t.Errorf("problems = %+v", d.Problems)
	}
}

func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	c, err := graphqlmock.Normalize(graphqlmock.Config{Overrides: []graphqlmock.Override{{Target: " User.name "}}})
	if err != nil || c.Path != "/graphql" || c.Overrides[0].Target != "User.name" || c.Overrides[0].Mode != graphqlmock.ModeValue {
		t.Errorf("Normalize = %+v, %v", c, err)
	}
	for name, bad := range map[string]graphqlmock.Config{
		"path":       {Path: "graphql"},
		"list":       {ListLength: 101},
		"null":       {NullPercent: -1},
		"target":     {Overrides: []graphqlmock.Override{{Target: "User.na me"}}},
		"mode":       {Overrides: []graphqlmock.Override{{Target: "User.name", Mode: "panic"}}},
		"type error": {Overrides: []graphqlmock.Override{{Target: "DateTime", Mode: graphqlmock.ModeError}}},
		"type args":  {Overrides: []graphqlmock.Override{{Target: "DateTime", Args: `{"a": 1}`}}},
	} {
		if _, err := graphqlmock.Normalize(bad); codeOf(err) != graphqlmock.CodeInvalidConfig {
			t.Errorf("%s: %v", name, err)
		}
	}
}
