package mockroute

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// compile builds a Matcher from "METHOD pattern" lines; the route ID is the
// line itself.
func compile(t testing.TB, opts Options, lines ...string) *Matcher {
	t.Helper()
	m, diags := Compile(routesOf(lines...), opts)
	if len(diags) > 0 {
		t.Fatalf("Compile: unexpected diagnostics: %v", diags)
	}
	return m
}

func routesOf(lines ...string) []Route {
	rs := make([]Route, len(lines))
	for i, l := range lines {
		method, pattern, _ := strings.Cut(l, " ")
		rs[i] = Route{ID: l, Method: method, Pattern: pattern}
	}
	return rs
}

// want describes an expected match. route "" means no match.
type want struct {
	route    string
	params   string // "name=value,name=value"
	wildcard string
	head     bool
	allowed  string // "GET,HEAD" when 405 is expected
}

func check(t *testing.T, m *Matcher, method, path string, w want) {
	t.Helper()
	got := m.Match(method, path)
	if got.RouteID != w.route || got.Matched != (w.route != "") {
		t.Fatalf("Match(%s %q) = route %q (matched %v), want %q", method, path, got.RouteID, got.Matched, w.route)
	}
	var ps []string
	for _, p := range got.Params {
		ps = append(ps, p.Name+"="+p.Value)
	}
	if s := strings.Join(ps, ","); s != w.params {
		t.Errorf("Match(%s %q) params = %q, want %q", method, path, s, w.params)
	}
	if got.Wildcard != w.wildcard {
		t.Errorf("Match(%s %q) wildcard = %q, want %q", method, path, got.Wildcard, w.wildcard)
	}
	if got.HeadFallback != w.head {
		t.Errorf("Match(%s %q) HeadFallback = %v, want %v", method, path, got.HeadFallback, w.head)
	}
	if got.MethodNotAllowed != (w.allowed != "") || strings.Join(got.Allowed, ",") != w.allowed {
		t.Errorf("Match(%s %q) MethodNotAllowed = %v, Allowed = %v, want %q",
			method, path, got.MethodNotAllowed, got.Allowed, w.allowed)
	}
	if !got.Matched && got.RouteIndex != -1 {
		t.Errorf("Match(%s %q) RouteIndex = %d without a match", method, path, got.RouteIndex)
	}
}

type matchCase struct {
	method, path string
	want         want
}

func runCases(t *testing.T, m *Matcher, cases []matchCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			check(t, m, c.method, c.path, c.want)
		})
	}
}

func TestParams(t *testing.T) {
	m := compile(t, Options{},
		"GET /users/:id",
		"GET /users/{id}/posts/:postId",
		"GET /orgs/{org}/repos/{repo}",
		"GET /v1/items:batchGet",
	)
	runCases(t, m, []matchCase{
		{"GET", "/users/42", want{route: "GET /users/:id", params: "id=42"}},
		{"GET", "/users/42/posts/7", want{route: "GET /users/{id}/posts/:postId", params: "id=42,postId=7"}},
		{"GET", "/orgs/acme/repos/api", want{route: "GET /orgs/{org}/repos/{repo}", params: "org=acme,repo=api"}},
		{"GET", "/users/caf%C3%A9", want{route: "GET /users/:id", params: "id=café"}},
		{"GET", "/users", want{}},
		{"GET", "/users/42/posts", want{}},
		{"GET", "/users//posts/7", want{}}, // params never match an empty segment
		{"GET", "/v1/items:batchGet", want{route: "GET /v1/items:batchGet"}},
		{"GET", "/users/42?x=1#frag", want{route: "GET /users/:id", params: "id=42"}},
		{"GET", "", want{}},
		{"GET", "*", want{}},
		{"GET", "http://example.com/users/1", want{}},
	})
}

func TestConstraints(t *testing.T) {
	m := compile(t, Options{},
		"GET /items/{id:[0-9]+}",
		"GET /items/{slug:[a-z-]+}",
		"GET /items/:other",
		"GET /files/{name:.*\\.json}",
	)
	runCases(t, m, []matchCase{
		{"GET", "/items/123", want{route: "GET /items/{id:[0-9]+}", params: "id=123"}},
		{"GET", "/items/red-shoes", want{route: "GET /items/{slug:[a-z-]+}", params: "slug=red-shoes"}},
		{"GET", "/items/Red", want{route: "GET /items/:other", params: "other=Red"}},
		// Anchored: a partial match does not count.
		{"GET", "/items/12a", want{route: "GET /items/:other", params: "other=12a"}},
		{"GET", "/files/a.json", want{route: "GET /files/{name:.*\\.json}", params: "name=a.json"}},
		{"GET", "/files/a.jsonx", want{}},
		// The constraint sees the decoded value.
		{"GET", "/items/%31%32", want{route: "GET /items/{id:[0-9]+}", params: "id=12"}},
	})
}

func TestWildcards(t *testing.T) {
	m := compile(t, Options{},
		"GET /a/*/c",
		"GET /static/**",
		"GET /files/*path",
		"GET /deep/:x/**",
	)
	runCases(t, m, []matchCase{
		{"GET", "/a/b/c", want{route: "GET /a/*/c"}},
		{"GET", "/a//c", want{}},
		{"GET", "/a/b/d", want{}},
		{"GET", "/static/css/site.css", want{route: "GET /static/**", wildcard: "css/site.css"}},
		{"GET", "/static/x", want{route: "GET /static/**", wildcard: "x"}},
		{"GET", "/static", want{route: "GET /static/**"}},
		{"GET", "/static/", want{route: "GET /static/**"}},
		{"GET", "/static/a//b", want{route: "GET /static/**", wildcard: "a//b"}},
		{"GET", "/files/a/b.txt", want{route: "GET /files/*path", params: "path=a/b.txt", wildcard: "a/b.txt"}},
		{"GET", "/files", want{route: "GET /files/*path", params: "path="}},
		{"GET", "/deep/1/2/3", want{route: "GET /deep/:x/**", params: "x=1", wildcard: "2/3"}},
		{"GET", "/deep", want{}},
	})
}

func TestOverlappingRoutes(t *testing.T) {
	lines := []string{
		"GET /**",
		"GET /users/*",
		"GET /users/:id",
		"GET /users/{id:[0-9]+}",
		"GET /users/me",
		"GET /users/:id/**",
		"GET /users/:id/posts",
		"GET /users",
		"GET /",
	}
	cases := []matchCase{
		{"GET", "/users/me", want{route: "GET /users/me"}},
		{"GET", "/users/42", want{route: "GET /users/{id:[0-9]+}", params: "id=42"}},
		{"GET", "/users/bob", want{route: "GET /users/:id", params: "id=bob"}},
		{"GET", "/users/42/posts", want{route: "GET /users/:id/posts", params: "id=42"}},
		{"GET", "/users/42/likes", want{route: "GET /users/:id/**", params: "id=42", wildcard: "likes"}},
		// A pattern that ends beats a catch-all that could match nothing.
		{"GET", "/users", want{route: "GET /users"}},
		{"GET", "/", want{route: "GET /"}},
		{"GET", "/other/thing", want{route: "GET /**", wildcard: "other/thing"}},
	}
	runCases(t, compile(t, Options{}, lines...), cases)

	// Without the more specific routes, the next one down wins.
	m := compile(t, Options{}, "GET /**", "GET /users/*")
	runCases(t, m, []matchCase{
		{"GET", "/users/me", want{route: "GET /users/*"}},
		{"GET", "/users", want{route: "GET /**", wildcard: "users"}},
		{"GET", "/", want{route: "GET /**"}},
	})
}

func TestSpecificityLeftToRight(t *testing.T) {
	// The first differing segment decides, even if later ones are less
	// specific.
	m := compile(t, Options{}, "GET /:a/b/c", "GET /a/:b/:c", "GET /a/*/c")
	runCases(t, m, []matchCase{
		{"GET", "/a/b/c", want{route: "GET /a/:b/:c", params: "b=b,c=c"}},
		{"GET", "/x/b/c", want{route: "GET /:a/b/c", params: "a=x"}},
	})

	// The path decides before the method.
	m = compile(t, Options{}, "GET /users/:id", "ANY /users/me")
	check(t, m, "GET", "/users/me", want{route: "ANY /users/me"})

	// Two constraints of the same shape: the route defined first wins.
	m = compile(t, Options{}, "GET /n/{a:[0-9]+}", "GET /n/{b:[0-9]{2}}")
	check(t, m, "GET", "/n/12", want{route: "GET /n/{a:[0-9]+}", params: "a=12"})
	m = compile(t, Options{}, "GET /n/{b:[0-9]{2}}", "GET /n/{a:[0-9]+}")
	check(t, m, "GET", "/n/12", want{route: "GET /n/{b:[0-9]{2}}", params: "b=12"})
}

func TestMethods(t *testing.T) {
	m := compile(t, Options{},
		"ANY /things/:id",
		"GET /things/:id",
		"DELETE /things/:id",
		"* /any/**",
		" /blank",
		"GET /users/me",
		"DELETE /users/:id",
		"POST /only-post",
		"PUT /only-post",
		"GET /get-only",
		"HEAD /head",
		"GET /head",
		"ANY /mixed/*",
		"GET /mixed/x",
		"PURGE /cache",
	)
	runCases(t, m, []matchCase{
		{"GET", "/things/1", want{route: "GET /things/:id", params: "id=1"}},
		{"DELETE", "/things/1", want{route: "DELETE /things/:id", params: "id=1"}},
		{"PATCH", "/things/1", want{route: "ANY /things/:id", params: "id=1"}},
		{"HEAD", "/things/1", want{route: "GET /things/:id", params: "id=1", head: true}},
		{"OPTIONS", "/any/x", want{route: "* /any/**", wildcard: "x"}},
		{"BREW", "/blank", want{route: " /blank"}},
		// Methods are case-sensitive.
		{"get", "/get-only", want{allowed: "GET,HEAD"}},
		// A more specific path with the wrong method falls through to a less
		// specific path that accepts the method.
		{"DELETE", "/users/me", want{route: "DELETE /users/:id", params: "id=me"}},
		{"POST", "/users/me", want{allowed: "DELETE,GET,HEAD"}},
		{"POST", "/users/bob", want{allowed: "DELETE"}},
		{"GET", "/only-post", want{allowed: "POST,PUT"}},
		{"HEAD", "/get-only", want{route: "GET /get-only", head: true}},
		{"HEAD", "/head", want{route: "HEAD /head"}},
		{"GET", "/head", want{route: "GET /head"}},
		// The path decides first: ANY /mixed/* loses to GET /mixed/x only for GET.
		{"GET", "/mixed/x", want{route: "GET /mixed/x"}},
		{"POST", "/mixed/x", want{route: "ANY /mixed/*"}},
		{"PURGE", "/cache", want{route: "PURGE /cache"}},
		{"GET", "/nothing", want{}},
	})
}

func TestTrailingSlash(t *testing.T) {
	lines := []string{"GET /users", "GET /teams/", "GET /files/**", "GET /"}

	runCases(t, compile(t, Options{}, lines...), []matchCase{
		{"GET", "/users", want{route: "GET /users"}},
		{"GET", "/users/", want{route: "GET /users"}},
		{"GET", "/teams", want{route: "GET /teams/"}},
		{"GET", "/teams/", want{route: "GET /teams/"}},
		{"GET", "/users//", want{}},
		{"GET", "/files/a/", want{route: "GET /files/**", wildcard: "a"}},
		{"GET", "/", want{route: "GET /"}},
	})

	runCases(t, compile(t, Options{StrictTrailingSlash: true}, lines...), []matchCase{
		{"GET", "/users", want{route: "GET /users"}},
		{"GET", "/users/", want{}},
		{"GET", "/teams", want{}},
		{"GET", "/teams/", want{route: "GET /teams/"}},
		{"GET", "/files", want{route: "GET /files/**"}},
		{"GET", "/files/", want{route: "GET /files/**"}},
		{"GET", "/files/a/", want{route: "GET /files/**", wildcard: "a/"}},
		{"GET", "/", want{route: "GET /"}},
	})

	// Strict: both spellings can be separate routes.
	m := compile(t, Options{StrictTrailingSlash: true}, "GET /x", "GET /x/", "GET /x/:id")
	runCases(t, m, []matchCase{
		{"GET", "/x", want{route: "GET /x"}},
		{"GET", "/x/", want{route: "GET /x/"}},
		{"GET", "/x/1", want{route: "GET /x/:id", params: "id=1"}},
	})
	// Lenient: they are duplicates.
	_, diags := Compile(routesOf("GET /x", "GET /x/"), Options{})
	if len(diags) != 1 || diags[0].Code != CodeDuplicateRoute || diags[0].RouteIndex != 1 {
		t.Errorf("lenient /x and /x/: diagnostics = %v, want one duplicate_route for route 1", diags)
	}
}

func TestCaseInsensitive(t *testing.T) {
	lines := []string{"GET /Users/:ID", "GET /users/Me", "GET /caf%C3%89", "GET /n/{v:[a-z]+}"}

	runCases(t, compile(t, Options{}, lines...), []matchCase{
		{"GET", "/Users/Bob", want{route: "GET /Users/:ID", params: "ID=Bob"}},
		{"GET", "/users/bob", want{}},
		{"GET", "/users/Me", want{route: "GET /users/Me"}},
		{"GET", "/users/me", want{}},
	})

	runCases(t, compile(t, Options{CaseInsensitive: true}, lines...), []matchCase{
		{"GET", "/users/Bob", want{route: "GET /Users/:ID", params: "ID=Bob"}},
		{"GET", "/USERS/ME", want{route: "GET /users/Me"}},
		{"GET", "/CAF%C3%A9", want{route: "GET /caf%C3%89"}},
		{"GET", "/cafÉ", want{route: "GET /caf%C3%89"}},
		{"GET", "/N/abc", want{route: "GET /n/{v:[a-z]+}", params: "v=abc"}},
		// Constraints are not affected by the option.
		{"GET", "/n/ABC", want{}},
	})

	_, diags := Compile(routesOf("GET /a/B", "GET /A/b"), Options{CaseInsensitive: true})
	if len(diags) != 1 || diags[0].Code != CodeDuplicateRoute {
		t.Errorf("case-insensitive duplicates: diagnostics = %v, want one duplicate_route", diags)
	}
}

func TestPercentEncoding(t *testing.T) {
	m := compile(t, Options{},
		"GET /files/:name",
		"GET /files/:dir/:name",
		"GET /raw/**",
		"GET /a%2Fb",
		"GET /hello world",
	)
	runCases(t, m, []matchCase{
		// %2F stays inside its segment.
		{"GET", "/files/a%2Fb", want{route: "GET /files/:name", params: "name=a/b"}},
		{"GET", "/files/a/b", want{route: "GET /files/:dir/:name", params: "dir=a,name=b"}},
		{"GET", "/a%2Fb", want{route: "GET /a%2Fb"}},
		{"GET", "/a/b", want{}},
		{"GET", "/hello%20world", want{route: "GET /hello world"}},
		{"GET", "/hello world", want{route: "GET /hello world"}},
		// An invalid escape is matched as written.
		{"GET", "/files/100%", want{route: "GET /files/:name", params: "name=100%"}},
		{"GET", "/files/a+b", want{route: "GET /files/:name", params: "name=a+b"}},
	})

	got := m.Match("GET", "/files/a%2Fb")
	if got.Params[0].Raw != "a%2Fb" {
		t.Errorf("Raw = %q, want a%%2Fb", got.Params[0].Raw)
	}
	got = m.Match("GET", "/raw/x%2Fy/z")
	if got.Wildcard != "x/y/z" || got.WildcardRaw != "x%2Fy/z" {
		t.Errorf("Wildcard = %q, WildcardRaw = %q, want x/y/z and x%%2Fy/z", got.Wildcard, got.WildcardRaw)
	}
}

func TestResultFields(t *testing.T) {
	routes := []Route{
		{ID: "r1", Method: "GET", Pattern: "/a/:x"},
		{ID: "r2", Method: "POST", Pattern: "/a/:x"},
	}
	m, diags := Compile(routes, Options{})
	if diags != nil {
		t.Fatal(diags)
	}
	got := m.Match("POST", "/a/1")
	if !got.Matched || got.RouteIndex != 1 || got.Route != routes[1] || got.RouteID != "r2" {
		t.Errorf("Match = %+v, want route r2 at index 1", got)
	}
	if v, ok := got.Param("x"); !ok || v != "1" {
		t.Errorf("Param(x) = %q, %v", v, ok)
	}
	if _, ok := got.Param("y"); ok {
		t.Error("Param(y) found")
	}
	if m.Len() != 2 {
		t.Errorf("Len = %d, want 2", m.Len())
	}
}

func TestDiagnostics(t *testing.T) {
	type d struct {
		code       string
		field      string
		start, end int
	}
	tests := []struct {
		name  string
		route Route
		want  []d
	}{
		{"empty", Route{Method: "GET", Pattern: ""}, []d{{CodeEmptyPattern, FieldPattern, 0, 0}}},
		{"no slash", Route{Method: "GET", Pattern: "users"}, []d{{CodeMissingLeadingSlash, FieldPattern, 0, 0}}},
		{"query", Route{Method: "GET", Pattern: "/users?x=1"}, []d{{CodeQueryInPattern, FieldPattern, 6, 10}}},
		{"fragment", Route{Method: "GET", Pattern: "/users#x"}, []d{{CodeQueryInPattern, FieldPattern, 6, 8}}},
		{"empty segment", Route{Method: "GET", Pattern: "/a//b"}, []d{{CodeEmptySegment, FieldPattern, 2, 4}}},
		{"leading empty segment", Route{Method: "GET", Pattern: "//a"}, []d{{CodeEmptySegment, FieldPattern, 0, 2}}},
		{"colon without name", Route{Method: "GET", Pattern: "/a/:"}, []d{{CodeInvalidParamName, FieldPattern, 4, 4}}},
		{"bad colon name", Route{Method: "GET", Pattern: "/a/:1d"}, []d{{CodeInvalidParamName, FieldPattern, 4, 6}}},
		{"bad colon name dash", Route{Method: "GET", Pattern: "/a/:user-id"}, []d{{CodeInvalidParamName, FieldPattern, 4, 11}}},
		{"empty braces", Route{Method: "GET", Pattern: "/a/{}"}, []d{{CodeInvalidParamName, FieldPattern, 4, 4}}},
		{"bad brace name", Route{Method: "GET", Pattern: "/a/{my id}"}, []d{{CodeInvalidParamName, FieldPattern, 4, 9}}},
		{"unclosed brace", Route{Method: "GET", Pattern: "/a/{id"}, []d{{CodeInvalidSegment, FieldPattern, 3, 6}}},
		{"brace in literal", Route{Method: "GET", Pattern: "/file.{ext}"}, []d{{CodeInvalidSegment, FieldPattern, 6, 7}}},
		{"star in literal", Route{Method: "GET", Pattern: "/a/b*"}, []d{{CodeInvalidSegment, FieldPattern, 4, 5}}},
		{"bad catch-all name", Route{Method: "GET", Pattern: "/a/*1"}, []d{{CodeInvalidParamName, FieldPattern, 4, 5}}},
		{"bad escape", Route{Method: "GET", Pattern: "/a/b%zz"}, []d{{CodeInvalidEscape, FieldPattern, 4, 7}}},
		{"duplicate param", Route{Method: "GET", Pattern: "/:id/{id}"}, []d{{CodeDuplicateParam, FieldPattern, 5, 9}}},
		{"duplicate catch-all name", Route{Method: "GET", Pattern: "/:p/*p"}, []d{{CodeDuplicateParam, FieldPattern, 4, 6}}},
		{"catch-all not last", Route{Method: "GET", Pattern: "/a/**/b"}, []d{{CodeCatchAllNotLast, FieldPattern, 5, 7}}},
		{"named catch-all not last", Route{Method: "GET", Pattern: "/*rest/b/c"}, []d{{CodeCatchAllNotLast, FieldPattern, 6, 10}}},
		{"bad regex", Route{Method: "GET", Pattern: "/a/{id:[0-9}"}, []d{{CodeInvalidRegex, FieldPattern, 7, 11}}},
		{"empty regex", Route{Method: "GET", Pattern: "/a/{id:}"}, []d{{CodeInvalidRegex, FieldPattern, 7, 7}}},
		{"bad method", Route{Method: "GE T", Pattern: "/a"}, []d{{CodeInvalidMethod, FieldMethod, 0, 4}}},
		{"several", Route{Method: "G()", Pattern: "/a//:/{x"}, []d{
			{CodeInvalidMethod, FieldMethod, 0, 3},
			{CodeEmptySegment, FieldPattern, 2, 4},
			{CodeInvalidParamName, FieldPattern, 5, 5},
			{CodeInvalidSegment, FieldPattern, 6, 8},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.route.ID = "r"
			m, diags := Compile([]Route{tt.route}, Options{})
			var got []d
			for _, dg := range diags {
				got = append(got, d{dg.Code, dg.Field, dg.Start, dg.End})
				if dg.RouteIndex != 0 || dg.RouteID != "r" || dg.Message == "" {
					t.Errorf("diagnostic %+v: want route 0 \"r\" and a message", dg)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("diagnostics = %v, want %v", got, tt.want)
			}
			if m == nil || m.Len() != 0 {
				t.Errorf("invalid route was compiled")
			}
		})
	}
}

func TestValidPatterns(t *testing.T) {
	for _, p := range []string{
		"/", "/a", "/a/", "/**", "/*rest", "/a/**/", "/:_x/{Y9}/{z:(a|b)}/*", "/v1/items:batchGet",
		"/a%20b", "/~user/.well-known", "/{re:[0-9]{2,3}}",
	} {
		if _, diags := Compile([]Route{{Method: "GET", Pattern: p}}, Options{}); diags != nil {
			t.Errorf("Compile(%q): %v", p, diags)
		}
		if _, diags := Compile([]Route{{Method: "GET", Pattern: p}}, Options{StrictTrailingSlash: true}); diags != nil {
			t.Errorf("Compile(%q) strict: %v", p, diags)
		}
	}
}

func TestDuplicates(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		opts  Options
		want  []string // "index:code"
	}{
		{"same route", []string{"GET /a/:id", "GET /a/:id"}, Options{}, []string{"1:" + CodeDuplicateRoute}},
		{"param names differ", []string{"GET /a/:id", "GET /a/{key}"}, Options{}, []string{"1:" + CodeDuplicateRoute}},
		{"catch-all spellings", []string{"GET /a/**", "GET /a/*rest"}, Options{}, []string{"1:" + CodeDuplicateRoute}},
		{"any spellings", []string{"ANY /a", "* /a", " /a"}, Options{}, []string{"1:" + CodeDuplicateRoute, "2:" + CodeDuplicateRoute}},
		{"different methods", []string{"GET /a/:id", "POST /a/:id", "ANY /a/:id"}, Options{}, nil},
		{"different constraints", []string{"GET /a/{id:[0-9]+}", "GET /a/{id:[a-z]+}"}, Options{}, nil},
		{"same constraint", []string{"GET /a/{id:[0-9]+}", "GET /a/{n:[0-9]+}"}, Options{}, []string{"1:" + CodeDuplicateRoute}},
		{"literal vs escaped", []string{"GET /a b", "GET /a%20b"}, Options{}, []string{"1:" + CodeDuplicateRoute}},
		{"case differs", []string{"GET /A", "GET /a"}, Options{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := routesOf(tt.lines...)
			for i := range rs {
				rs[i].ID = fmt.Sprint("r", i)
			}
			_, diags := Compile(rs, tt.opts)
			var got []string
			for _, d := range diags {
				got = append(got, fmt.Sprint(d.RouteIndex, ":", d.Code))
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("diagnostics = %v, want %v", got, tt.want)
			}
		})
	}

	_, diags := Compile([]Route{
		{ID: "x", Method: "GET", Pattern: "/a"},
		{ID: "x", Method: "GET", Pattern: "/b"},
	}, Options{})
	if len(diags) != 1 || diags[0].Code != CodeDuplicateID || diags[0].Field != FieldID || diags[0].RouteIndex != 1 {
		t.Errorf("duplicate ID: diagnostics = %v", diags)
	}
}

func TestDiagnosticErr(t *testing.T) {
	_, diags := Compile([]Route{{ID: "r7", Method: "GET", Pattern: "/a/:"}}, Options{})
	if len(diags) != 1 {
		t.Fatal(diags)
	}
	err := diags[0].Err()
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.InvalidArgument || e.Code != CodeInvalidRoute {
		t.Fatalf("Err() = %#v", err)
	}
	if e.Details["diagnostic"] != CodeInvalidParamName || e.Details["routeId"] != "r7" || e.Details["start"] != "4" {
		t.Errorf("details = %v", e.Details)
	}
	if s := diags[0].Error(); !strings.Contains(s, "route 0 pattern 4-4: ") {
		t.Errorf("Error() = %q", s)
	}
}

// overlapRoutes is a route set with many overlaps and no ties between
// routes that can match the same request (so the definition order never
// decides).
var overlapRoutes = []string{
	"GET /",
	"GET /**",
	"ANY /**",
	"GET /users",
	"POST /users",
	"GET /users/me",
	"ANY /users/me",
	"GET /users/{id:[0-9]+}",
	"GET /users/:id",
	"DELETE /users/:id",
	"GET /users/*",
	"PUT /users/*",
	"GET /users/:id/posts",
	"GET /users/:id/posts/{post:[0-9]+}",
	"GET /users/:id/**",
	"HEAD /users/:id/avatar",
	"GET /static/*path",
	"GET /static/css/*",
	"GET /:tenant/users",
	"GET /:tenant/:resource/:id",
	"PATCH /*/users/:id",
	"GET /a%2Fb/:x",
}

var overlapRequests = []string{
	"/", "/users", "/users/", "/users/me", "/users/42", "/users/bob", "/users/42/posts",
	"/users/42/posts/7", "/users/42/posts/x", "/users/42/avatar", "/users/42/a/b/c",
	"/static", "/static/css/site.css", "/static/css", "/static/js/app.js",
	"/acme/users", "/acme/users/1", "/acme/things/1", "/a%2Fb/1", "/a/b/1", "/nothing/here",
	"/USERS/ME", "/users//posts", "/users/%34%32",
}

func TestInsertionOrderIndependence(t *testing.T) {
	methods := []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
	for _, opts := range []Options{{}, {StrictTrailingSlash: true}, {CaseInsensitive: true}} {
		base := compile(t, opts, overlapRoutes...)
		r := rand.New(rand.NewPCG(145, uint64(len(overlapRoutes))))
		for range 50 {
			shuffled := slices.Clone(overlapRoutes)
			r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
			m := compile(t, opts, shuffled...)
			for _, method := range methods {
				for _, path := range overlapRequests {
					a, b := base.Match(method, path), m.Match(method, path)
					// Indexes differ with the order; the rest must not.
					a.RouteIndex, b.RouteIndex = 0, 0
					if !reflect.DeepEqual(a, b) {
						t.Fatalf("%+v: %s %s: order %v gives %+v, original gives %+v", opts, method, path, shuffled, b, a)
					}
				}
			}
		}
	}
}

func TestOverlapTable(t *testing.T) {
	m := compile(t, Options{}, overlapRoutes...)
	runCases(t, m, []matchCase{
		{"GET", "/", want{route: "GET /"}},
		{"POST", "/", want{route: "ANY /**"}},
		{"GET", "/users/me", want{route: "GET /users/me"}},
		{"POST", "/users/me", want{route: "ANY /users/me"}},
		{"HEAD", "/users/me", want{route: "GET /users/me", head: true}},
		{"GET", "/users/42", want{route: "GET /users/{id:[0-9]+}", params: "id=42"}},
		{"DELETE", "/users/42", want{route: "DELETE /users/:id", params: "id=42"}},
		{"PUT", "/users/42", want{route: "PUT /users/*"}},
		{"HEAD", "/users/42/avatar", want{route: "HEAD /users/:id/avatar", params: "id=42"}},
		{"GET", "/users/42/avatar", want{route: "GET /users/:id/**", params: "id=42", wildcard: "avatar"}},
		{"GET", "/users/42/posts/7", want{route: "GET /users/:id/posts/{post:[0-9]+}", params: "id=42,post=7"}},
		{"GET", "/users/42/posts/x", want{route: "GET /users/:id/**", params: "id=42", wildcard: "posts/x"}},
		{"GET", "/static/css/site.css", want{route: "GET /static/css/*"}},
		{"GET", "/static/js/app.js", want{route: "GET /static/*path", params: "path=js/app.js", wildcard: "js/app.js"}},
		{"GET", "/acme/users", want{route: "GET /:tenant/users", params: "tenant=acme"}},
		{"PATCH", "/acme/users/1", want{route: "PATCH /*/users/:id", params: "id=1"}},
		{"GET", "/acme/users/1", want{route: "GET /:tenant/:resource/:id", params: "tenant=acme,resource=users,id=1"}},
		{"GET", "/a%2Fb/1", want{route: "GET /a%2Fb/:x", params: "x=1"}},
		{"GET", "/a/b/1", want{route: "GET /:tenant/:resource/:id", params: "tenant=a,resource=b,id=1"}},
	})
}

func BenchmarkMatch1000(b *testing.B) {
	var lines []string
	for i := range 250 {
		lines = append(lines,
			fmt.Sprintf("GET /api/v%d/users/:id", i),
			fmt.Sprintf("POST /api/v%d/users/{id:[0-9]+}/posts", i),
			fmt.Sprintf("GET /svc%d/items/*/detail", i),
			fmt.Sprintf("ANY /:tenant/r%d/**", i),
		)
	}
	m := compile(b, Options{}, lines...)
	if m.Len() != 1000 {
		b.Fatalf("Len = %d", m.Len())
	}
	paths := []string{"/api/v200/users/42", "/svc3/items/x/detail", "/acme/r249/a/b/c", "/nothing/at/all"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Match("GET", paths[i%len(paths)])
	}
}
