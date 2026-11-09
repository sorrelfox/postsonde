package graphqlmock

import (
	"encoding/json"
	"strings"
	"testing"
)

// run executes query against the example schema with cfg's settings.
func run(t *testing.T, cfg Config, query string, vars string) (result, string) {
	t.Helper()
	if cfg.SchemaText == "" && cfg.SchemaFile == "" {
		cfg.SchemaText = ExampleSchema
	}
	c, err := Normalize(cfg)
	if err != nil {
		t.Fatal(err)
	}
	l, err := LoadSchema(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := compile(c, l)
	if len(st.problems) > 0 {
		t.Fatalf("problems: %+v", st.problems)
	}
	req := gqlRequest{Query: query}
	if vars != "" {
		v, err := decodeJSON(vars)
		if err != nil {
			t.Fatal(err)
		}
		req.Variables = v.(map[string]any)
	}
	res := execute(st, req, nil)
	return res, string(res.body())
}

func cfg(overrides ...Override) Config {
	c := DefaultConfig()
	c.Overrides = overrides
	return c
}

func TestShapeFollowsSelection(t *testing.T) {
	t.Parallel()
	q := `query Q($id: ID!, $skip: Boolean!) {
  first: user(id: $id) { ...F id }
  user(id: "2") { __typename role posts @skip(if: $skip) { title } }
  search(text: "x") { __typename ... on Post { title } ...U }
}
fragment F on User { name email }
fragment U on User { name }`
	res, body := run(t, cfg(), q, `{"id": 1, "skip": true}`)
	if len(res.Errors) > 0 || !res.HasData {
		t.Fatalf("response = %s", body)
	}
	var got struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(res.body(), &got); err != nil {
		t.Fatal(err)
	}
	// Keys in selection order, aliases, fragments merged in place.
	data := res.Data.(object)
	keys := func(o object) string {
		var out []string
		for _, m := range o {
			out = append(out, m.key)
		}
		return strings.Join(out, ",")
	}
	if k := keys(data); k != "first,user,search" {
		t.Errorf("root keys = %s", k)
	}
	if first, ok := data[0].val.(object); !ok || keys(first) != "name,email,id" {
		t.Errorf("first = %#v", data[0].val)
	}
	user := data[1].val.(object)
	if keys(user) != "__typename,role" || user[0].val != "User" {
		t.Errorf("user = %s", got.Data["user"])
	}
	if r := user[1].val.(string); r != "ADMIN" && r != "EDITOR" && r != "VIEWER" {
		t.Errorf("role = %q", r)
	}
	results := data[2].val.([]any)
	if len(results) != DefaultListLength {
		t.Fatalf("search = %s", got.Data["search"])
	}
	for _, r := range results {
		o := r.(object)
		switch o[0].val {
		case "Post":
			if keys(o) != "__typename,title" {
				t.Errorf("post = %#v", o)
			}
		case "User":
			if keys(o) != "__typename,name" {
				t.Errorf("user result = %#v", o)
			}
		default:
			t.Errorf("result type = %v", o[0].val)
		}
	}
}

func TestDeterministic(t *testing.T) {
	t.Parallel()
	q := `{ users(first: 3) { id name email role posts { id title publishedAt author { name } } } search(text: "a") { __typename } }`
	_, a := run(t, cfg(), q, "")
	_, b := run(t, cfg(), q, "")
	if a != b {
		t.Errorf("two runs differ:\n%s\n%s", a, b)
	}
	other := cfg()
	other.Seed = 7
	if _, c := run(t, other, q, ""); c == a {
		t.Error("another seed gave the same data")
	}
	// Different arguments, different data; an alias changes nothing.
	_, x := run(t, cfg(), `{ a: user(id: 1) { name } b: user(id: 2) { name } }`, "")
	_, y := run(t, cfg(), `{ a: user(id: 1) { name } c: user(id: 1) { name } }`, "")
	var xr, yr struct{ Data map[string]map[string]string }
	_ = json.Unmarshal([]byte(x), &xr)
	_ = json.Unmarshal([]byte(y), &yr)
	if xr.Data["a"]["name"] == "" || xr.Data["a"] == nil {
		t.Fatalf("x = %s", x)
	}
	if yr.Data["a"]["name"] != yr.Data["c"]["name"] || yr.Data["a"]["name"] != xr.Data["a"]["name"] {
		t.Errorf("aliases changed the data: %s / %s", x, y)
	}
}

func TestGeneratedTypes(t *testing.T) {
	t.Parallel()
	c := cfg()
	c.ListLength = 4
	c.SchemaText = `type Query { v: V! }
type V { i: Int! f: Float! s: String! b: Boolean! id: ID! e: E! d: DateTime! u: UUID! j: JSON! l: [[Int!]!]! createdAt: String! email: String! avatarUrl: String! }
enum E { A B @deprecated }
scalar DateTime
scalar UUID
scalar JSON`
	res, body := run(t, c, `{ v { i f s b id e d u j l createdAt email avatarUrl } }`, "")
	if len(res.Errors) > 0 {
		t.Fatal(body)
	}
	var got struct {
		Data struct {
			V struct {
				I         int
				F         float64
				S         string
				B         bool
				ID        string
				E         string
				D         string
				U         string
				J         map[string]any
				L         [][]int
				CreatedAt string
				Email     string
				AvatarURL string
			}
		}
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	v := got.Data.V
	if v.E != "A" || v.ID == "" || v.S == "" || len(v.D) != 20 || !strings.HasSuffix(v.D, "Z") || len(v.U) != 36 || v.J == nil {
		t.Errorf("v = %+v", v)
	}
	if len(v.L) != 4 || len(v.L[0]) != 4 {
		t.Errorf("nested lists = %v", v.L)
	}
	if !strings.Contains(v.Email, "@example.com") || !strings.HasPrefix(v.AvatarURL, "https://") || len(v.CreatedAt) != 20 {
		t.Errorf("strings by name = %q %q %q", v.Email, v.AvatarURL, v.CreatedAt)
	}
}

func TestNullPercent(t *testing.T) {
	t.Parallel()
	c := cfg()
	c.NullPercent = 100
	c.ListLength = 3
	res, body := run(t, c, `{ user(id: 1) { id } users { id email } }`, "")
	if len(res.Errors) > 0 {
		t.Fatal(body)
	}
	if body != `{"data":{"user":null,"users":[{"id":"`+idOf(body, 0)+`","email":null},{"id":"`+idOf(body, 1)+`","email":null},{"id":"`+idOf(body, 2)+`","email":null}]}}` {
		t.Errorf("body = %s", body)
	}
}

func idOf(body string, n int) string {
	var got struct {
		Data struct{ Users []struct{ ID string } }
	}
	_ = json.Unmarshal([]byte(body), &got)
	if n < len(got.Data.Users) {
		return got.Data.Users[n].ID
	}
	return ""
}

func TestOverrides(t *testing.T) {
	t.Parallel()
	c := cfg(
		Override{Enabled: true, Target: "Query.user", Args: `{"id": 1}`, Value: `{"id": "1", "name": "Ada", "posts": [{"title": "Notes"}]}`},
		Override{Enabled: true, Target: "Query.user", Args: `{"id": "2"}`, Mode: ModeError, Error: "No such user.", Code: "NOT_FOUND"},
		Override{Enabled: true, Target: "User.email", Value: `"ada@example.org"`},
		Override{Enabled: false, Target: "User.name", Value: `"disabled"`},
		Override{Enabled: true, Target: "DateTime", Value: `"2026-01-02T03:04:05Z"`},
		Override{Enabled: true, Target: "Query.search", Value: `[{"__typename": "Post", "title": "Hello"}]`},
	)
	q := `{ a: user(id: 1) { id name email posts { title publishedAt } } b: user(id: 2) { id } c: user(id: 3) { name } search(text: "x") { __typename ... on Post { title } } }`
	res, body := run(t, c, q, "")
	var got struct {
		Errors []struct {
			Message    string
			Path       []any
			Locations  []struct{ Line, Column int }
			Extensions map[string]string
		}
		Data map[string]json.RawMessage
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Data["a"]) != `{"id":"1","name":"Ada","email":"ada@example.org","posts":[{"title":"Notes","publishedAt":"2026-01-02T03:04:05Z"}]}` {
		t.Errorf("a = %s", got.Data["a"])
	}
	if string(got.Data["b"]) != "null" || len(got.Errors) != 1 || got.Errors[0].Message != "No such user." ||
		len(got.Errors[0].Path) != 1 || got.Errors[0].Path[0] != "b" || got.Errors[0].Extensions["code"] != "NOT_FOUND" ||
		len(got.Errors[0].Locations) != 1 || got.Errors[0].Locations[0].Column != 64 {
		t.Errorf("b = %s, errors %+v", got.Data["b"], got.Errors)
	}
	if strings.Contains(string(got.Data["c"]), "disabled") || string(got.Data["c"]) == "null" {
		t.Errorf("c = %s", got.Data["c"])
	}
	if string(got.Data["search"]) != `[{"__typename":"Post","title":"Hello"}]` {
		t.Errorf("search = %s", got.Data["search"])
	}
	if !res.HasData {
		t.Error("no data")
	}
}

func TestNonNullErrorPropagates(t *testing.T) {
	t.Parallel()
	c := cfg(
		Override{Enabled: true, Target: "User.role", Mode: ModeError, Error: "boom"},
		Override{Enabled: true, Target: "Query.users", Value: `[{"name": null}]`},
	)
	_, body := run(t, c, `{ user(id: 1) { id role } }`, "")
	if body != `{"errors":[{"message":"boom","locations":[{"line":1,"column":20}],"path":["user","role"]}],"data":{"user":null}}` {
		t.Errorf("nullable parent: %s", body)
	}
	// users is [User!]! and name String!: a null name nulls the item,
	// which nulls the list, which nulls data.
	_, body = run(t, c, `{ users { name } }`, "")
	if body != `{"errors":[{"message":"Cannot return null for non-nullable field User.name.","locations":[{"line":1,"column":11}],"path":["users",0,"name"],"extensions":{"code":"MOCK_VALUE_INVALID"}}],"data":null}` {
		t.Errorf("non-null chain: %s", body)
	}
}

func TestBadMockValue(t *testing.T) {
	t.Parallel()
	c := cfg(Override{Enabled: true, Target: "User.role", Value: `"OWNER"`}, Override{Enabled: true, Target: "Query.user", Value: `[1]`})
	_, body := run(t, c, `{ users(first: 1) { role } }`, "")
	if !strings.Contains(body, `Enum \"Role\" cannot represent value: \"OWNER\" (the mock value of User.role)`) || !strings.Contains(body, `"data":null`) {
		t.Errorf("body = %s", body)
	}
	_, body = run(t, c, `{ user(id: 1) { id } }`, "")
	if !strings.Contains(body, `Expected an object for Query.user of type \"User\", but the mock value is [1].`) {
		t.Errorf("body = %s", body)
	}
}

func TestRequestErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, query, vars, want string
	}{
		{"syntax", `{ user(id: 1) { name }`, "",
			`{"errors":[{"message":"Syntax Error: \"{\" has no matching \"}\"","locations":[{"line":1,"column":1}],"extensions":{"code":"GRAPHQL_PARSE_FAILED"}}]}`},
		{"validation", `{ user(id: 1) { nmae } }`, "",
			`{"errors":[{"message":"Cannot query field \"nmae\" on type \"User\". Did you mean \"name\"?","locations":[{"line":1,"column":17}],"extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}`},
		{"missing variable", `query Q($id: ID!) { user(id: $id) { id } }`, "",
			`{"errors":[{"message":"Variable \"$id\" of required type \"ID!\" was not provided.","locations":[{"line":1,"column":9}],"extensions":{"code":"BAD_USER_INPUT"}}]}`},
		{"bad variable", `query Q($n: Int) { users(first: $n) { id } }`, `{"n": "ten"}`,
			`{"errors":[{"message":"Variable \"$n\" got invalid value \"ten\"; Int cannot represent non-integer value: \"ten\"","locations":[{"line":1,"column":9}],"extensions":{"code":"BAD_USER_INPUT"}}]}`},
		{"bad input field", `mutation M($in: CreateUserInput!) { createUser(input: $in) { id } }`, `{"in": {"name": 3}}`,
			`{"errors":[{"message":"Variable \"$in\" got invalid value 3 at \"in.name\"; String cannot represent a non string value: 3","locations":[{"line":1,"column":12}],"extensions":{"code":"BAD_USER_INPUT"}}]}`},
		{"several operations", `query A { users { id } } query B { users { id } }`, "",
			`{"errors":[{"message":"Must provide operation name if query contains multiple operations.","extensions":{"code":"OPERATION_RESOLUTION_FAILURE"}}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, body := run(t, cfg(), tc.query, tc.vars)
			if res.HasData || body != tc.want {
				t.Errorf("body = %s\nwant   %s", body, tc.want)
			}
		})
	}
}

func TestVariablesAndDefaults(t *testing.T) {
	t.Parallel()
	c := cfg(
		Override{Enabled: true, Target: "Mutation.createUser", Args: `{"input": {"name": "Ada", "role": "VIEWER"}}`, Value: `{"name": "matched"}`},
		Override{Enabled: true, Target: "Query.users", Args: `{"first": 10}`, Value: `[{"name": "default first"}]`},
	)
	_, body := run(t, c, `mutation M($n: String!) { createUser(input: {name: $n}) { name } }`, `{"n": "Ada"}`)
	if body != `{"data":{"createUser":{"name":"matched"}}}` {
		t.Errorf("input default filled in: %s", body)
	}
	_, body = run(t, c, `query Q($f: Int = 10) { users(first: $f) { name } }`, "")
	if body != `{"data":{"users":[{"name":"default first"}]}}` {
		t.Errorf("variable default: %s", body)
	}
	_, body = run(t, c, `{ users { name } }`, "")
	if body != `{"data":{"users":[{"name":"default first"}]}}` {
		t.Errorf("argument default: %s", body)
	}
}

func TestIntrospectionShape(t *testing.T) {
	t.Parallel()
	res, body := run(t, cfg(), `{ __type(name: "User") { kind name fields { name type { kind ofType { name } } } interfaces { name } } nope: __type(name: "Nope") { name } __typename }`, "")
	if len(res.Errors) > 0 || !res.Introspection {
		t.Fatal(body)
	}
	want := `{"data":{"__type":{"kind":"OBJECT","name":"User","fields":[` +
		`{"name":"id","type":{"kind":"NON_NULL","ofType":{"name":"ID"}}},` +
		`{"name":"name","type":{"kind":"NON_NULL","ofType":{"name":"String"}}},` +
		`{"name":"email","type":{"kind":"SCALAR","ofType":null}},` +
		`{"name":"role","type":{"kind":"NON_NULL","ofType":{"name":"Role"}}},` +
		`{"name":"posts","type":{"kind":"NON_NULL","ofType":{"name":null}}}],` +
		`"interfaces":[{"name":"Node"}]},"nope":null,"__typename":"Query"}}`
	if body != want {
		t.Errorf("body = %s\nwant   %s", body, want)
	}
	off := cfg()
	off.Introspection = false
	res, _ = run(t, off, `{ __schema { queryType { name } } }`, "")
	if res.HasData || len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "introspection is turned off") {
		t.Errorf("introspection off: %+v", res)
	}
	// __typename is not introspection.
	if res, _ := run(t, off, `{ __typename }`, ""); !res.HasData {
		t.Errorf("__typename with introspection off: %+v", res)
	}
}

func TestPagingArgumentSetsLength(t *testing.T) {
	t.Parallel()
	count := func(body string) int {
		var got struct{ Data struct{ Users []any } }
		_ = json.Unmarshal([]byte(body), &got)
		return len(got.Data.Users)
	}
	if _, body := run(t, cfg(), `{ users(first: 5) { id } }`, ""); count(body) != 5 {
		t.Errorf("first: 5 gave %s", body)
	}
	if _, body := run(t, cfg(), `query Q($n: Int) { users(first: $n) { id } }`, `{"n": 0}`); count(body) != 0 {
		t.Errorf("first: 0 gave %s", body)
	}
	// The default of first (10) does not count.
	if _, body := run(t, cfg(), `{ users { id } }`, ""); count(body) != DefaultListLength {
		t.Errorf("no first gave %s", body)
	}
}

func TestTooLarge(t *testing.T) {
	t.Parallel()
	c := cfg()
	c.ListLength = 100
	c.SchemaText = `type Query { a: [A!] } type A { b: [B!] } type B { c: [Int] }`
	res, _ := run(t, c, `{ a { b { c } } }`, "")
	if len(res.Errors) != 1 || res.Errors[0].Code != ErrCodeTooLarge || !res.HasData {
		t.Errorf("errors = %+v", res.Errors)
	}
}

func TestLooseEqual(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got, want string
		eq        bool
	}{
		{`"1"`, `1`, true},
		{`1`, `1.0`, true},
		{`"a"`, `"a"`, true},
		{`{"a": [1, "x"]}`, `{"a": ["1", "x"]}`, true},
		{`{"a": 1, "b": 2}`, `{"a": 1}`, false},
		{`true`, `"true"`, true},
		{`null`, `null`, true},
		{`"1.0"`, `1`, false},
	}
	for _, tc := range cases {
		g, _ := decodeJSON(tc.got)
		w, _ := decodeJSON(tc.want)
		if looseEqual(g, w) != tc.eq {
			t.Errorf("looseEqual(%s, %s) = %v", tc.got, tc.want, !tc.eq)
		}
	}
}

func TestNameWords(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"createdAt": "created at", "avatar_url": "avatar url", "avatarURL": "avatar url", "URLPath": "url path", "DateTime": "date time", "id": "id"} {
		if got := strings.Join(nameWords(in), " "); got != want {
			t.Errorf("nameWords(%q) = %q, want %q", in, got, want)
		}
	}
}
