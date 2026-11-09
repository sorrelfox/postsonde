package mockhttp_test

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
)

func (f *fixture) send(t *testing.T, method, path string, header map[string]string, body string) reply {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.url+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header, string(b)}
}

func alt(name string, status int, body string, rules ...mockhttp.Rule) mockhttp.Response {
	r := mockhttp.Response{Name: name, Status: status, Rules: rules}
	if body != "" {
		r.Body = mockhttp.Body{Mode: mockhttp.BodyRaw, Type: "text", Text: body}
	}
	return r
}

func rule(source, name, op, value string) mockhttp.Rule {
	return mockhttp.Rule{Source: source, Name: name, Op: op, Value: value}
}

// conditionalRoute has one response per kind of rule, and a response
// without rules that only the header selects.
func conditionalRoute() mockhttp.Route {
	r := route("orders", "ANY", "/orders/:id", 200, "default")
	r.Responses = []mockhttp.Response{
		alt("admin", 200, "admin view", rule(mockhttp.SourceHeader, "X-Role", mockhttp.OpEquals, "admin")),
		alt("mine", 200, "mine", rule(mockhttp.SourceQuery, "owner", mockhttp.OpEquals, "me")),
		alt("search", 200, "search", rule(mockhttp.SourceQuery, "q", mockhttp.OpExists, "")),
		alt("big", 201, "big order",
			rule(mockhttp.SourceJSON, "$.total", mockhttp.OpEquals, "100"),
			rule(mockhttp.SourceJSON, "$.items[*].sku", mockhttp.OpEquals, "B-2")),
		alt("gift", 202, "gift", rule(mockhttp.SourceBody, "", mockhttp.OpContains, "gift")),
		alt("code", 200, "coded", rule(mockhttp.SourceQuery, "code", mockhttp.OpMatches, `^[A-Z]{3}-\d+$`)),
		alt("noauth", 401, "no token", rule(mockhttp.SourceHeader, "Authorization", mockhttp.OpNotExists, "")),
		alt("broken", 500, "boom"),
		alt("gone", 410, "gone"),
	}
	return r
}

func TestConditionalResponses(t *testing.T) {
	t.Parallel()
	f := start(t, mockhttp.Options{}, mockhttp.Config{Routes: []mockhttp.Route{conditionalRoute()}})
	auth := map[string]string{"Authorization": "Bearer x"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{"Authorization": "Bearer x"}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	tests := []struct {
		name         string
		method, path string
		header       map[string]string
		body         string
		status       int
		want         string
	}{
		{"no rule matches: default", "GET", "/orders/1", auth, "", 200, "default"},
		{"header equals", "GET", "/orders/1", with("X-Role", "admin"), "", 200, "admin view"},
		{"header value differs", "GET", "/orders/1", with("X-Role", "user"), "", 200, "default"},
		{"header name case", "GET", "/orders/1", with("x-role", "admin"), "", 200, "admin view"},
		{"first match wins", "GET", "/orders/1?owner=me", with("X-Role", "admin"), "", 200, "admin view"},
		{"query equals", "GET", "/orders/1?owner=me", auth, "", 200, "mine"},
		{"repeated query: any value", "GET", "/orders/1?owner=you&owner=me", auth, "", 200, "mine"},
		{"query exists, empty value", "GET", "/orders/1?q=", auth, "", 200, "search"},
		{"json path equals, all rules", "POST", "/orders/1", auth, `{"total": 100.0, "items": [{"sku": "A-1"}, {"sku": "B-2"}]}`, 201, "big order"},
		{"json one rule fails", "POST", "/orders/1", auth, `{"total": 100, "items": [{"sku": "A-1"}]}`, 200, "default"},
		{"not json", "POST", "/orders/1", auth, `total=100`, 200, "default"},
		{"body contains", "POST", "/orders/1", auth, `wrap it as a gift please`, 202, "gift"},
		{"query regex", "GET", "/orders/1?code=ABC-42", auth, "", 200, "coded"},
		{"query regex no match", "GET", "/orders/1?code=abc-42", auth, "", 200, "default"},
		{"header not exists", "GET", "/orders/1", nil, "", 401, "no token"},
		{"select by name", "GET", "/orders/1", with(mockhttp.SelectHeader, "broken"), "", 500, "boom"},
		{"select name case", "GET", "/orders/1", with(mockhttp.SelectHeader, "BROKEN"), "", 500, "boom"},
		{"select overrides rules", "GET", "/orders/1", with("X-Role", "admin", mockhttp.SelectHeader, "gone"), "", 410, "gone"},
		{"select default", "GET", "/orders/1", map[string]string{mockhttp.SelectHeader: "default"}, "", 200, "default"},
		{"select status of a response", "GET", "/orders/1", with(mockhttp.SelectHeader, "201"), "", 201, "big order"},
		{"select status nobody has", "GET", "/orders/1", with(mockhttp.SelectHeader, "503"), "", 503, ""},
	}
	for _, tt := range tests {
		got := f.send(t, tt.method, tt.path, tt.header, tt.body)
		if got.status != tt.status || got.body != tt.want {
			t.Errorf("%s: %s %s = %d %q, want %d %q", tt.name, tt.method, tt.path, got.status, got.body, tt.status, tt.want)
		}
	}

	resps := f.rec.waitFor(t, servers.KindResponse, len(tests))
	names := []string{}
	for _, ev := range resps {
		names = append(names, ev.HTTP.Route.Response)
	}
	want := []string{"default", "admin", "default", "admin", "admin", "mine", "mine", "search", "big", "default", "default",
		"gift", "code", "default", "noauth", "broken", "broken", "gone", "default", "big", "status 503"}
	if !slices.Equal(names, want) {
		t.Errorf("response names:\n got %q\nwant %q", names, want)
	}
}

func TestUnknownSelectedResponse(t *testing.T) {
	t.Parallel()
	plain := route("plain", "GET", "/plain", 200, "plain")
	f := start(t, mockhttp.Options{}, mockhttp.Config{Routes: []mockhttp.Route{conditionalRoute(), plain}})

	for _, v := range []string{"nope", "700", "99", "0500"} {
		got := f.send(t, "GET", "/orders/1", map[string]string{mockhttp.SelectHeader: v}, "")
		if got.status != http.StatusBadRequest || got.header.Get("Content-Type") != "application/json" {
			t.Errorf("%q: %d %v", v, got.status, got.header)
			continue
		}
		var body struct {
			Error     string   `json:"error"`
			Responses []string `json:"responses"`
		}
		if err := json.Unmarshal([]byte(got.body), &body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body.Error, mockhttp.SelectHeader) || !strings.Contains(body.Error, `"`+v+`"`) {
			t.Errorf("%q: error = %q", v, body.Error)
		}
		if !slices.Equal(body.Responses, []string{"default", "admin", "mine", "search", "big", "gift", "code", "noauth", "broken", "gone"}) {
			t.Errorf("%q: responses = %q", v, body.Responses)
		}
	}

	// A route without other responses takes the header too, and an empty
	// value is no selection.
	if got := f.send(t, "GET", "/plain", map[string]string{mockhttp.SelectHeader: "418"}, ""); got.status != 418 || got.body != "" {
		t.Errorf("forced 418 = %d %q", got.status, got.body)
	}
	if got := f.send(t, "GET", "/plain", map[string]string{mockhttp.SelectHeader: " "}, ""); got.status != 200 || got.body != "plain" {
		t.Errorf("blank selection = %d %q", got.status, got.body)
	}
	if got := f.send(t, "GET", "/plain", map[string]string{mockhttp.SelectHeader: "x"}, ""); got.status != 400 {
		t.Errorf("unknown on plain route = %d", got.status)
	}
	resps := f.rec.waitFor(t, servers.KindResponse, 7)
	if !strings.Contains(resps[0].Summary, "unknown "+mockhttp.SelectHeader) {
		t.Errorf("summary = %q", resps[0].Summary)
	}
	if r := resps[4].HTTP.Route; r == nil || r.Response != "status 418" {
		t.Errorf("forced 418: route = %+v", r)
	}
	if r := resps[5].HTTP.Route; r == nil || r.Response != "" {
		t.Errorf("no selection on a route without responses: route = %+v", r)
	}
}

func TestRequestBodyForRulesIsNotCappedByLog(t *testing.T) {
	t.Parallel()
	r := route("big", "POST", "/big", 200, "small")
	r.Responses = []mockhttp.Response{alt("tail", 200, "tail found", rule(mockhttp.SourceBody, "", mockhttp.OpContains, "TAIL"))}
	f := start(t, mockhttp.Options{}, mockhttp.Config{Routes: []mockhttp.Route{r}})
	body := strings.Repeat("x", 200<<10) + "TAIL"
	if got := f.send(t, "POST", "/big", nil, body); got.body != "tail found" {
		t.Errorf("body = %q", got.body)
	}
	req := f.rec.waitFor(t, servers.KindRequest, 1)[0]
	if !req.Truncated || req.Size != len(body) || len(req.Content) >= len(body) {
		t.Errorf("request event: truncated %v, size %d, content %d", req.Truncated, req.Size, len(req.Content))
	}
}

func TestCheckResponses(t *testing.T) {
	t.Parallel()
	r := route("r", "GET", "/r", 200, "")
	r.Responses = []mockhttp.Response{
		alt("ok", 200, "", rule(mockhttp.SourceQuery, "a", mockhttp.OpEquals, "1")), // 0
		alt("Default", 200, ""), // 1: reserved
		alt("OK", 200, ""),      // 2: duplicate of 0
		alt("", 700, "", // 3
			rule(mockhttp.SourceQuery, "", mockhttp.OpEquals, "x"),
			rule(mockhttp.SourceHeader, "Bad Name", mockhttp.OpExists, ""),
			rule(mockhttp.SourceJSON, "$[", mockhttp.OpExists, ""),
			rule(mockhttp.SourceBody, "", mockhttp.OpMatches, "("),
			rule("cookie", "a", mockhttp.OpExists, ""),
			rule(mockhttp.SourceBody, "", "gt", "1"),
			rule(mockhttp.SourceBody, "", mockhttp.OpNotContains, "fine"),
		),
		{Name: "hdr", Status: 200, Headers: []mockhttp.Header{{Enabled: true, Name: "X Y", Value: "v"}}}, // 4
		alt("", 200, ""), // 5: two unnamed responses are fine
		alt("", 200, ""),
	}
	diags := mockhttp.Check(mockhttp.Config{Routes: []mockhttp.Route{r}})
	type key struct {
		code, field          string
		resp, header, ruleIx int
	}
	var got []key
	for _, d := range diags {
		got = append(got, key{d.Code, d.Field, d.ResponseIndex, d.HeaderIndex, d.RuleIndex})
		if d.Message == "" || d.RouteIndex != 0 || d.RouteID != "r" {
			t.Errorf("diagnostic = %+v", d)
		}
	}
	want := []key{
		{mockhttp.CodeInvalidResponseName, mockhttp.FieldName, 1, -1, -1},
		{mockhttp.CodeDuplicateResponseName, mockhttp.FieldName, 2, -1, -1},
		{mockhttp.CodeInvalidRule, mockhttp.FieldRule, 3, -1, 0},
		{mockhttp.CodeInvalidRule, mockhttp.FieldRule, 3, -1, 1},
		{mockhttp.CodeInvalidRule, mockhttp.FieldRule, 3, -1, 2},
		{mockhttp.CodeInvalidRule, mockhttp.FieldRule, 3, -1, 3},
		{mockhttp.CodeInvalidRule, mockhttp.FieldRule, 3, -1, 4},
		{mockhttp.CodeInvalidRule, mockhttp.FieldRule, 3, -1, 5},
		{mockhttp.CodeInvalidStatus, mockhttp.FieldStatus, 3, -1, -1},
		{mockhttp.CodeInvalidHeaderName, mockhttp.FieldHeader, 4, 0, -1},
	}
	if !slices.Equal(got, want) {
		t.Errorf("diagnostics:\n got %v\nwant %v", got, want)
	}

	// A route with a problem in a response is not served.
	f := start(t, mockhttp.Options{}, mockhttp.Config{Routes: []mockhttp.Route{r}})
	if got := f.do(t, "GET", "/r"); got.status != 404 {
		t.Errorf("route with a bad response answered %d", got.status)
	}
}

func TestNormalizeResponses(t *testing.T) {
	t.Parallel()
	in := route("r", "GET", "/r", 200, "")
	in.Responses = []mockhttp.Response{{Name: "  err ", Status: 500, Rules: []mockhttp.Rule{rule(mockhttp.SourceBody, "", mockhttp.OpExists, "")}}}
	out, err := mockhttp.Normalize(mockhttp.Config{Routes: []mockhttp.Route{in}})
	if err != nil {
		t.Fatal(err)
	}
	got := out.Routes[0].Responses[0]
	if got.Name != "err" || got.Body.Mode != mockhttp.BodyNone {
		t.Errorf("normalized = %+v", got)
	}
	got.Rules[0].Value = "changed"
	if in.Responses[0].Rules[0].Value != "" {
		t.Error("the normalized config shares rules with the input")
	}
	old, err := mockhttp.Normalize(mockhttp.Config{Routes: []mockhttp.Route{route("o", "GET", "/o", 200, "")}})
	if err != nil || old.Routes[0].Responses != nil {
		t.Errorf("a route without responses got %+v, %v", old.Routes[0].Responses, err)
	}

	many := in
	many.Responses = make([]mockhttp.Response, mockhttp.MaxResponses+1)
	if _, err := mockhttp.Normalize(mockhttp.Config{Routes: []mockhttp.Route{many}}); err == nil {
		t.Error("too many responses were accepted")
	}
	rules := in
	rules.Responses = []mockhttp.Response{{Rules: make([]mockhttp.Rule, mockhttp.MaxRules+1)}}
	if _, err := mockhttp.Normalize(mockhttp.Config{Routes: []mockhttp.Route{rules}}); err == nil {
		t.Error("too many rules were accepted")
	}
	big := in
	big.Responses = []mockhttp.Response{{Body: mockhttp.Body{Mode: "raw", Text: strings.Repeat("x", mockhttp.MaxBodyBytes+1)}}}
	if _, err := mockhttp.Normalize(mockhttp.Config{Routes: []mockhttp.Route{big}}); err == nil {
		t.Error("a too large response body was accepted")
	}
}
