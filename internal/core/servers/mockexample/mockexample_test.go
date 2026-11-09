package mockexample_test

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockexample"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
)

func TestRoutePath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, tmpl, sent, want string
		note                   string
	}{
		{name: "full URL", tmpl: "https://api.example/users/42", sent: "https://api.example/users/42", want: "/users/42"},
		{name: "base variable", tmpl: "{{baseUrl}}/users", sent: "https://api.example/users", want: "/users"},
		{name: "variable segment", tmpl: "{{baseUrl}}/users/{{userId}}", sent: "https://api.example/users/42", want: "/users/:userId"},
		{name: "colon param", tmpl: "https://api.example/users/:id/posts", sent: "https://api.example/users/7/posts", want: "/users/:id/posts"},
		{name: "brace param", tmpl: "https://api.example/users/{id}", want: "/users/:id"},
		{name: "dotted variable", tmpl: "{{host}}/u/{{user.id}}", sent: "http://h/u/1", want: "/u/:user_id"},
		{name: "dynamic value with arguments", tmpl: "{{base}}/n/{{$randomInt 1 9}}", sent: "http://h/n/3", want: "/n/:randomInt"},
		{name: "filter", tmpl: "{{base}}/n/{{name | upper}}", sent: "http://h/n/X", want: "/n/:name"},
		{name: "mixed segment", tmpl: "{{base}}/files/{{name}}.json", sent: "http://h/files/a.json", want: `/files/{name:.+\.json}`},
		{name: "query left out", tmpl: "https://api.example/search?q={{q}}", want: "/search", note: "query string"},
		{name: "base URL path from the URL sent", tmpl: "{{baseUrl}}/users/{{id}}", sent: "https://api.example/v1/users/42", want: "/v1/users/:id", note: "base URL"},
		{name: "sent path does not end with the typed path", tmpl: "{{baseUrl}}/users/{{id}}", sent: "https://api.example/v1/accounts/42", want: "/users/:id", note: "does not end"},
		{name: "variable host", tmpl: "https://{{host}}/v2/items", sent: "https://x.example/v2/items", want: "/v2/items"},
		{name: "host without scheme", tmpl: "localhost:8080/items/:id", want: "/items/:id"},
		{name: "no path", tmpl: "{{baseUrl}}", sent: "https://api.example", want: "/"},
		{name: "no template: the URL sent", tmpl: "", sent: "https://api.example/a/b", want: "/a/b"},
		{name: "repeated names", tmpl: "{{b}}/a/{{id}}/b/{{id}}", sent: "http://h/a/1/b/2", want: "/a/:id/b/:id_2"},
		{name: "reserved characters", tmpl: "https://h/glob*", want: "/glob%2A"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, notes := mockexample.RoutePath(tt.tmpl, tt.sent)
			if got != tt.want {
				t.Errorf("RoutePath(%q, %q) = %q, want %q (notes %q)", tt.tmpl, tt.sent, got, tt.want, notes)
			}
			if tt.note != "" && !strings.Contains(strings.Join(notes, "\n"), tt.note) {
				t.Errorf("notes %q, want one containing %q", notes, tt.note)
			}
		})
	}
}

func headerNames(hs []mockhttp.Header) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Name+": "+h.Value)
	}
	return out
}

func TestBuildHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		headers []mockexample.Header
		kept    []string
		dropped map[string]string
	}{
		{
			name: "hop-by-hop and framing",
			headers: []mockexample.Header{
				{"Content-Type", "application/json"}, {"Connection", "keep-alive, X-Hop"}, {"Keep-Alive", "timeout=5"},
				{"Transfer-Encoding", "chunked"}, {"X-Hop", "1"}, {"Content-Length", "12"}, {"Date", "Mon, 01 Jan 2026 00:00:00 GMT"},
				{"X-Request-Id", "abc"}, {"Set-Cookie", "a=1"}, {"Set-Cookie", "b=2"},
			},
			kept: []string{"Content-Type: application/json", "X-Request-Id: abc", "Set-Cookie: a=1", "Set-Cookie: b=2"},
			dropped: map[string]string{
				"Connection": mockexample.DropHopByHop, "Keep-Alive": mockexample.DropHopByHop, "Transfer-Encoding": mockexample.DropHopByHop,
				"X-Hop": mockexample.DropHopByHop, "Content-Length": mockexample.DropContentLength, "Date": mockexample.DropDate,
			},
		},
		{
			name:    "gzip was decoded",
			headers: []mockexample.Header{{"Content-Encoding", "gzip"}, {"Vary", "Accept-Encoding"}},
			kept:    []string{"Vary: Accept-Encoding"},
			dropped: map[string]string{"Content-Encoding": mockexample.DropContentEncoding},
		},
		{
			name:    "brotli is kept with its bytes",
			headers: []mockexample.Header{{"Content-Encoding", "br"}},
			kept:    []string{"Content-Encoding: br"},
		},
		{
			name:    "two codings are not decoded",
			headers: []mockexample.Header{{"Content-Encoding", "gzip"}, {"Content-Encoding", "gzip"}},
			kept:    []string{"Content-Encoding: gzip", "Content-Encoding: gzip"},
		},
		{
			name:    "invalid and pseudo fields",
			headers: []mockexample.Header{{":status", "200"}, {"Bad Name", "x"}, {"X-Ok", "fine"}},
			kept:    []string{"X-Ok: fine"},
			dropped: map[string]string{"Bad Name": mockexample.DropInvalid},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := mockexample.Build(mockexample.Input{Example: mockexample.Example{StatusCode: 200, Headers: tt.headers}, URL: "http://h/x"})
			if got := headerNames(res.Reply.Headers); !reflect.DeepEqual(got, tt.kept) {
				t.Errorf("kept %q, want %q", got, tt.kept)
			}
			got := map[string]string{}
			for _, d := range res.Dropped {
				got[d.Name] = d.Reason
			}
			if len(tt.dropped) == 0 && len(got) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.dropped) {
				t.Errorf("dropped %v, want %v", got, tt.dropped)
			}
		})
	}
}

func TestBuildBody(t *testing.T) {
	t.Parallel()
	bin := []byte{0x89, 'P', 'N', 'G', 0, 0xff, 0xfe}
	tests := []struct {
		name     string
		ex       mockexample.Example
		wantBody mockhttp.Body
		codes    []string
	}{
		{
			name:     "json",
			ex:       mockexample.Example{StatusCode: 200, Headers: []mockexample.Header{{"Content-Type", "application/problem+json"}}, Body: `{"a":1}`},
			wantBody: mockhttp.Body{Mode: "raw", Type: "json", Text: `{"a":1}`},
		},
		{
			name:     "no content type",
			ex:       mockexample.Example{StatusCode: 200, Body: "hello"},
			wantBody: mockhttp.Body{Mode: "raw", Type: "text", Text: "hello"},
			codes:    []string{mockexample.CodeNoContentType},
		},
		{
			name: "binary",
			ex: mockexample.Example{StatusCode: 200, Headers: []mockexample.Header{{"Content-Type", "image/png"}},
				Body: base64.StdEncoding.EncodeToString(bin), Base64: true},
			wantBody: mockhttp.Body{Mode: "raw", Type: mockhttp.BodyTypeBase64, Text: base64.StdEncoding.EncodeToString(bin)},
			codes:    []string{mockexample.CodeBinary},
		},
		{
			name:     "invalid base64",
			ex:       mockexample.Example{StatusCode: 200, Headers: []mockexample.Header{{"Content-Type", "image/png"}}, Body: "!!", Base64: true},
			wantBody: mockhttp.Body{Mode: "none"},
			codes:    []string{mockexample.CodeInvalidBase64},
		},
		{
			name:     "empty",
			ex:       mockexample.Example{StatusCode: 201},
			wantBody: mockhttp.Body{Mode: "none"},
		},
		{
			name:     "204 has no body",
			ex:       mockexample.Example{StatusCode: 204, Headers: []mockexample.Header{{"Content-Type", "text/plain"}}, Body: "x"},
			wantBody: mockhttp.Body{Mode: "none"},
			codes:    []string{mockexample.CodeNoBodyStatus},
		},
		{
			name:     "clipped",
			ex:       mockexample.Example{StatusCode: 200, Headers: []mockexample.Header{{"Content-Type", "text/html"}}, Body: "<p>", Clipped: true, Size: 200000},
			wantBody: mockhttp.Body{Mode: "raw", Type: "html", Text: "<p>"},
			codes:    []string{mockexample.CodeClipped},
		},
		{
			name:     "custom reason phrase",
			ex:       mockexample.Example{StatusCode: 200, StatusText: "Fine Thanks", Headers: []mockexample.Header{{"Content-Type", "text/plain"}}, Body: "x"},
			wantBody: mockhttp.Body{Mode: "raw", Type: "text", Text: "x"},
			codes:    []string{mockexample.CodeReasonPhrase},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := mockexample.Build(mockexample.Input{Example: tt.ex, URL: "http://h/x"})
			if res.Reply.Body != tt.wantBody {
				t.Errorf("body %+v, want %+v", res.Reply.Body, tt.wantBody)
			}
			var codes []string
			for _, n := range res.Notes {
				codes = append(codes, n.Code)
			}
			if !reflect.DeepEqual(codes, tt.codes) {
				t.Errorf("notes %q, want %q", codes, tt.codes)
			}
			// Every route made is one the mock server serves (200–599).
			if diags := mockhttp.Check(mockhttp.Config{Routes: []mockhttp.Route{{ID: "r", Enabled: true, Method: res.Method, Path: res.Path,
				Status: res.Reply.Status, Headers: res.Reply.Headers, Body: res.Reply.Body}}}); len(diags) > 0 {
				t.Errorf("diagnostics %+v", diags)
			}
		})
	}
}

func TestBuildMasksAgain(t *testing.T) {
	t.Parallel()
	secret := "s3cr3t-token"
	m := httpclient.NewSecretMasker([]string{secret})
	bin := append([]byte{0xff, 0xfe}, []byte(secret)...)
	tests := []struct {
		name string
		ex   mockexample.Example
	}{
		{name: "header and text body", ex: mockexample.Example{StatusCode: 200,
			Headers: []mockexample.Header{{"X-Token", secret}, {"Authorization", "Bearer abc"}, {"Content-Type", "application/json"}},
			Body:    `{"token":"` + secret + `"}`}},
		{name: "binary body", ex: mockexample.Example{StatusCode: 200, Headers: []mockexample.Header{{"Content-Type", "application/octet-stream"}},
			Body: base64.StdEncoding.EncodeToString(bin), Base64: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := mockexample.Build(mockexample.Input{Example: tt.ex, URL: "http://h/x", Masker: m})
			if !res.Masked {
				t.Error("Masked is false")
			}
			all := strings.Join(headerNames(res.Reply.Headers), "\n") + "\n" + res.Reply.Body.Text
			if res.Reply.Body.Type == mockhttp.BodyTypeBase64 {
				raw, _ := base64.StdEncoding.DecodeString(res.Reply.Body.Text)
				all += string(raw)
			}
			if strings.Contains(all, secret) || strings.Contains(all, "Bearer abc") {
				t.Errorf("a secret reached the route: %q", all)
			}
		})
	}
}

func TestBuildMethodAndPathOverride(t *testing.T) {
	t.Parallel()
	p := "/custom/:x"
	res := mockexample.Build(mockexample.Input{Example: mockexample.Example{StatusCode: 200, SentMethod: "PATCH", SentURL: "http://h/a/1"}, Path: &p})
	if res.Method != "PATCH" || res.Path != p || res.DerivedPath != "/a/1" {
		t.Errorf("got %s %s (derived %s)", res.Method, res.Path, res.DerivedPath)
	}
	res = mockexample.Build(mockexample.Input{Example: mockexample.Example{StatusCode: 200}})
	if res.Method != "GET" || res.Path != "/" {
		t.Errorf("empty input: %s %s", res.Method, res.Path)
	}
}

func example(status int, body string) mockexample.Result {
	return mockexample.Build(mockexample.Input{
		Method:  "GET",
		URL:     "{{baseUrl}}/users/{{id}}",
		Example: mockexample.Example{Name: "200 OK", StatusCode: status, Headers: []mockexample.Header{{"Content-Type", "application/json"}}, Body: body},
	})
}

func TestMerge(t *testing.T) {
	t.Parallel()
	existing := mockhttp.Config{Routes: []mockhttp.Route{
		{ID: "rt_hello", Enabled: true, Method: "GET", Path: "/hello", Status: 200},
		{ID: "rt_user", Enabled: false, Method: "GET", Path: "/users/:userId", Status: 404, Delay: mockhttp.Delay{Mode: "fixed", Ms: 5},
			Responses: []mockhttp.Response{{Name: "200 OK", Status: 200}}},
	}}
	tests := []struct {
		name     string
		cfg      mockhttp.Config
		mode     mockexample.Mode
		respName string
		status   string
		check    func(t *testing.T, res mockexample.MergeResult)
	}{
		{name: "new route", cfg: mockhttp.Config{}, mode: mockexample.ModeRoute, status: mockexample.StatusAdded,
			check: func(t *testing.T, res mockexample.MergeResult) {
				r := res.Config.Routes[0]
				if r.ID != "rt_get_users_id" || r.Method != "GET" || r.Path != "/users/:id" || r.Status != 201 || r.Body.Text != `{"id":1}` || !r.Enabled {
					t.Errorf("route %+v", r)
				}
			}},
		{name: "same method and path: nothing added", cfg: existing, mode: mockexample.ModeRoute, status: mockexample.StatusExisting,
			check: func(t *testing.T, res mockexample.MergeResult) {
				if res.Existing == nil || res.Existing.ID != "rt_user" || len(res.Config.Routes) != 2 || res.RouteIndex != -1 {
					t.Errorf("%+v", res)
				}
			}},
		{name: "as a named response, name made unique", cfg: existing, mode: mockexample.ModeResponse, status: mockexample.StatusResponseAdded,
			check: func(t *testing.T, res mockexample.MergeResult) {
				r := res.Config.Routes[1]
				if res.ResponseName != "200 OK (2)" || len(r.Responses) != 2 || r.Responses[1].Name != "200 OK (2)" || r.Responses[1].Status != 201 {
					t.Errorf("%+v", r.Responses)
				}
				if len(existing.Routes[1].Responses) != 1 {
					t.Error("the given configuration was changed")
				}
			}},
		{name: "response named by the user", cfg: existing, mode: mockexample.ModeResponse, respName: " created ", status: mockexample.StatusResponseAdded,
			check: func(t *testing.T, res mockexample.MergeResult) {
				if res.ResponseName != "created" {
					t.Errorf("name %q", res.ResponseName)
				}
			}},
		{name: "default is not a response name", cfg: existing, mode: mockexample.ModeResponse, respName: "Default", status: mockexample.StatusResponseAdded,
			check: func(t *testing.T, res mockexample.MergeResult) {
				if res.ResponseName != "example" {
					t.Errorf("name %q", res.ResponseName)
				}
			}},
		{name: "replace the default response", cfg: existing, mode: mockexample.ModeReplace, status: mockexample.StatusReplaced,
			check: func(t *testing.T, res mockexample.MergeResult) {
				r := res.Config.Routes[1]
				if r.ID != "rt_user" || r.Status != 201 || r.Body.Text != `{"id":1}` || r.Delay.Ms != 5 || len(r.Responses) != 1 || r.Enabled {
					t.Errorf("%+v", r)
				}
			}},
		{name: "response mode without a route adds one", cfg: mockhttp.Config{Routes: existing.Routes[:1]}, mode: mockexample.ModeResponse, status: mockexample.StatusAdded},
		{name: "taken id gets a suffix", cfg: mockhttp.Config{Routes: []mockhttp.Route{{ID: "rt_get_users_id", Method: "POST", Path: "/users/:id", Status: 200}}},
			mode: mockexample.ModeRoute, status: mockexample.StatusAdded,
			check: func(t *testing.T, res mockexample.MergeResult) {
				if res.RouteID != "rt_get_users_id_2" {
					t.Errorf("id %q", res.RouteID)
				}
			}},
		{name: "case-insensitive server", cfg: mockhttp.Config{CaseInsensitive: true, Routes: []mockhttp.Route{{ID: "u", Method: "GET", Path: "/USERS/:x", Status: 200}}},
			mode: mockexample.ModeRoute, status: mockexample.StatusExisting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := mockexample.Merge(tt.cfg, example(201, `{"id":1}`), tt.mode, tt.respName)
			if res.Status != tt.status {
				t.Fatalf("status %q, want %q", res.Status, tt.status)
			}
			if tt.check != nil {
				tt.check(t, res)
			}
		})
	}
}

func TestMergeLimits(t *testing.T) {
	t.Parallel()
	full := mockhttp.Config{}
	for i := range mockhttp.MaxRoutes {
		full.Routes = append(full.Routes, mockhttp.Route{ID: "r" + string(rune('a'+i%26)) + strings.Repeat("x", i/26), Method: "GET", Path: "/r/" + strings.Repeat("a", i+1), Status: 200})
	}
	if res := mockexample.Merge(full, example(200, ""), mockexample.ModeRoute, ""); res.Status != mockexample.StatusOverLimit || len(res.Config.Routes) != mockhttp.MaxRoutes {
		t.Errorf("routes: %s, %d", res.Status, len(res.Config.Routes))
	}
	route := mockhttp.Route{ID: "u", Method: "GET", Path: "/users/:id", Status: 200}
	for i := range mockhttp.MaxResponses {
		route.Responses = append(route.Responses, mockhttp.Response{Name: "r" + strings.Repeat("x", i), Status: 200})
	}
	if res := mockexample.Merge(mockhttp.Config{Routes: []mockhttp.Route{route}}, example(200, ""), mockexample.ModeResponse, ""); res.Status != mockexample.StatusOverLimit {
		t.Errorf("responses: %s", res.Status)
	}
}
