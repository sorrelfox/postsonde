package mockhttp

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func testRequest(t *testing.T) *requestData {
	t.Helper()
	r := httptest.NewRequest("POST", "http://api.test/users/42/files/a/b.txt?page=2&page=3&q=x+y", strings.NewReader(""))
	r.Header.Set("X-Trace", "t-1")
	r.Header.Set("Authorization", "Bearer abc")
	body := []byte(`{"user":{"name":"Ada","tags":["a","b"],"age":36},"note":"{{request.method}}"}`)
	return newRequestData(r, body, []Param{{Name: "id", Value: "42"}, {Name: "rest", Value: "a/b.txt"}}, "a/b.txt")
}

func TestRequestLookup(t *testing.T) {
	t.Parallel()
	d := testRequest(t)
	tests := []struct {
		name  string
		want  string
		found bool
	}{
		{"request.method", "POST", true},
		{"request.path", "/users/42/files/a/b.txt", true},
		{"request.url", "http://api.test/users/42/files/a/b.txt?page=2&page=3&q=x+y", true},
		{"request.host", "api.test", true},
		{"request.wildcard", "a/b.txt", true},
		{"request.body", string(d.body), true},
		{"request.params.id", "42", true},
		{"request.params.nope", "", true},
		{"request.query.page", "2", true},
		{"request.query.q", "x y", true},
		{"request.query.nope", "", true},
		{"request.headers.x-trace", "t-1", true},
		{"request.headers.X-TRACE", "t-1", true},
		{"request.headers.Host", "api.test", true},
		{"request.headers.X-Nope", "", true},
		{"request.body.user.name", "Ada", true},
		{"request.body.user.age", "36", true},
		{"request.body.user.tags", `["a","b"]`, true},
		{"request.body.user.tags.1", "b", true},
		{"request.body.user.email", "", true},
		{"request.body.user.tags.9", "", true},
		{"request.body.a..b", "", false},
		{"request.query.q | base64", "", false},
		{"request.query.q|base64", "", false},
		{"request.params", "", false},
		{"request.query.", "", false},
		{"request.nope", "", false},
		{"request.nope.x", "", false},
		{"method", "", false},
	}
	for _, tt := range tests {
		got, found := d.lookup(tt.name)
		if got != tt.want || found != tt.found {
			t.Errorf("lookup(%q) = %q, %v; want %q, %v", tt.name, got, found, tt.want, tt.found)
		}
	}

	notJSON := testRequest(t)
	notJSON.body = []byte("plain")
	if got, found := notJSON.lookup("request.body.user"); got != "" || !found {
		t.Errorf("lookup in a body that is not JSON = %q, %v", got, found)
	}
}

func TestRenderTemplated(t *testing.T) {
	t.Parallel()
	d := testRequest(t)
	p := prepareReply("default", 200, []Header{
		{Enabled: true, Name: "X-Echo", Value: "{{request.headers.X-Trace}}-{{request.params.id}}"},
		{Enabled: true, Name: "X-Plain", Value: "no template"},
	}, Body{Mode: BodyRaw, Type: "json", Text: `{"id": {{request.params.id}}, "name": "{{request.body.user.name}}", "note": "{{request.body.note}}", "raw": "\{{request.method}}", "uuid": "{{$uuid}}", "b64": "{{request.query.q | base64}}", "missing": "{{request.query.nope}}"}`})
	p.templated = true
	body, header, err := p.render(d)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^\{"id": 42, "name": "Ada", "note": "\{\{request.method\}\}", "raw": "\{\{request.method\}\}", "uuid": "[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}", "b64": "eCB5", "missing": ""\}$`)
	if !re.Match(body) {
		t.Errorf("body = %s", body)
	}
	if got := header.Get("X-Echo"); got != "t-1-42" {
		t.Errorf("X-Echo = %q", got)
	}
	if got := header.Get("X-Plain"); got != "no template" {
		t.Errorf("X-Plain = %q", got)
	}
	if got := p.header.Get("X-Echo"); !strings.Contains(got, "{{") {
		t.Errorf("the prepared header changed: %q", got)
	}

	// Not templated: as written, the same header map.
	p.templated = false
	body, header, err = p.render(d)
	if err != nil || !strings.Contains(string(body), "{{request.params.id}}") || header.Get("X-Echo") != p.header.Get("X-Echo") {
		t.Errorf("untemplated: %s, %v, %v", body, header, err)
	}
}

func TestRenderErrors(t *testing.T) {
	t.Parallel()
	d := testRequest(t)
	tests := []struct {
		name    string
		body    string
		headers []Header
		want    string
	}{
		{"unknown name", "{{request.nope}}", nil, "The template in the body cannot be rendered: The variable {{request.nope}} is not defined."},
		{"environment variable", "{{baseUrl}}", nil, "{{baseUrl}} is not defined"},
		{"invalid dynamic", "{{$randomInt 5 1}}", nil, "the minimum is greater than the maximum"},
		{"invalid filter", "{{request.body | b64}}", nil, `"b64" is not a filter`},
		{"header", "", []Header{{Enabled: true, Name: "X-A", Value: "{{nope}}"}}, "The template in the header X-A cannot be rendered"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := Body{Mode: BodyNone}
			if tt.body != "" {
				body = Body{Mode: BodyRaw, Type: "text", Text: tt.body}
			}
			p := prepareReply("default", 200, tt.headers, body)
			p.templated = true
			_, _, err := p.render(d)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}

	// A header whose rendered value has a line break.
	d2 := testRequest(t)
	d2.body = []byte(`{"v":"a\nb"}`)
	p := prepareReply("default", 200, []Header{{Enabled: true, Name: "X-A", Value: "{{request.body.v}}"}}, Body{})
	p.templated = true
	if _, _, err := p.render(d2); err == nil || !strings.Contains(err.Error(), "line break") {
		t.Errorf("line break: err = %v", err)
	}

	// The rendered size is bounded.
	big := testRequest(t)
	big.body = []byte(strings.Repeat("x", MaxMatchBodyBytes))
	p = prepareReply("default", 200, nil, Body{Mode: BodyRaw, Type: "text", Text: strings.Repeat("{{request.body}}", 20)})
	p.templated = true
	if _, _, err := p.render(big); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("limit: err = %v", err)
	}
}

func TestRequestNames(t *testing.T) {
	t.Parallel()
	d := testRequest(t)
	for _, n := range RequestNames() {
		name := n.Name
		if strings.HasSuffix(name, ".") {
			name += "x"
		}
		if _, ok := d.lookup(name); !ok {
			t.Errorf("RequestNames lists %q, which does not resolve", n.Name)
		}
		if n.Description == "" {
			t.Errorf("%q has no description", n.Name)
		}
	}
}
