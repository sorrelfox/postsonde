package mockhttp_test

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
)

// TestTemplatedResponses runs a mock server whose responses read the
// request (MOCK-06) and sends real requests to it.
func TestTemplatedResponses(t *testing.T) {
	t.Parallel()
	users := route("users", "ANY", "/users/:id", 200, "")
	users.Templated = true
	users.Body = mockhttp.Body{Mode: mockhttp.BodyRaw, Type: "json", Text: `{
  "id": "{{request.params.id}}",
  "method": "{{request.method}}",
  "path": "{{request.path}}",
  "page": "{{request.query.page}}",
  "trace": "{{request.headers.x-trace}}",
  "name": "{{request.body.user.name}}",
  "firstTag": "{{request.body.user.tags.0}}",
  "missing": "{{request.query.nope}}",
  "requestId": "{{$uuid}}",
  "hash": "{{request.params.id | sha256}}",
  "literal": "\{{request.method}}"
}`}
	users.Headers = []mockhttp.Header{{Enabled: true, Name: "Location", Value: "/users/{{request.params.id}}"}}
	echo := alt("echo", 201, "", rule(mockhttp.SourceQuery, "echo", mockhttp.OpExists, ""))
	echo.Body = mockhttp.Body{Mode: mockhttp.BodyRaw, Type: "text", Text: "you sent: {{request.body}}"}
	echo.Templated = true
	users.Responses = []mockhttp.Response{echo}

	plain := route("plain", "GET", "/plain", 200, `{"template": "{{request.method}}"}`)

	broken := route("broken", "GET", "/broken", 200, "Hi {{name}}")
	broken.Templated = true

	files := route("files", "GET", "/files/*rest", 200, "{{request.wildcard}} {{request.params.rest}}")
	files.Templated = true

	f := start(t, mockhttp.Options{}, mockhttp.Config{Routes: []mockhttp.Route{users, plain, broken, files}})

	t.Run("request values", func(t *testing.T) {
		got := f.send(t, "POST", "/users/42?page=3&page=4", map[string]string{"X-Trace": "abc"},
			`{"user": {"name": "Ada {{request.method}}", "tags": ["x", "y"]}}`)
		if got.status != 200 {
			t.Fatalf("status = %d: %s", got.status, got.body)
		}
		var doc map[string]string
		if err := json.Unmarshal([]byte(got.body), &doc); err != nil {
			t.Fatalf("body is not JSON: %v\n%s", err, got.body)
		}
		want := map[string]string{
			"id": "42", "method": "POST", "path": "/users/42", "page": "3", "trace": "abc",
			// Text from the request is never read as a template.
			"name":     "Ada {{request.method}}",
			"firstTag": "x", "missing": "",
			"hash":    "73475cb40a568e8da8a045ced110137e159f890ac4da883b6b17dc651b3a8049",
			"literal": "{{request.method}}",
		}
		for k, v := range want {
			if doc[k] != v {
				t.Errorf("%s = %q, want %q", k, doc[k], v)
			}
		}
		if !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(doc["requestId"]) {
			t.Errorf("requestId = %q", doc["requestId"])
		}
		if loc := got.header.Get("Location"); loc != "/users/42" {
			t.Errorf("Location = %q", loc)
		}
		if got.header.Get("Content-Length") != "" && got.header.Get("Content-Length") != strconv.Itoa(len(got.body)) {
			t.Errorf("Content-Length = %s for %d bytes", got.header.Get("Content-Length"), len(got.body))
		}

		again := f.send(t, "GET", "/users/42", nil, "")
		var doc2 map[string]string
		_ = json.Unmarshal([]byte(again.body), &doc2)
		if doc2["requestId"] == doc["requestId"] || doc2["method"] != "GET" || doc2["name"] != "" {
			t.Errorf("second request: %s", again.body)
		}
	})

	t.Run("templated other response", func(t *testing.T) {
		got := f.send(t, "PUT", "/users/7?echo", nil, "hello")
		if got.status != 201 || got.body != "you sent: hello" {
			t.Errorf("got %d %q", got.status, got.body)
		}
	})

	t.Run("wildcard", func(t *testing.T) {
		got := f.do(t, "GET", "/files/a/b.txt")
		if got.body != "a/b.txt a/b.txt" {
			t.Errorf("body = %q", got.body)
		}
	})

	t.Run("not templated", func(t *testing.T) {
		got := f.do(t, "GET", "/plain")
		if got.body != `{"template": "{{request.method}}"}` {
			t.Errorf("body = %q", got.body)
		}
	})

	t.Run("render error", func(t *testing.T) {
		got := f.do(t, "GET", "/broken")
		if got.status != http.StatusInternalServerError {
			t.Fatalf("status = %d", got.status)
		}
		var doc struct{ Error string }
		if err := json.Unmarshal([]byte(got.body), &doc); err != nil {
			t.Fatal(err)
		}
		const want = `Route GET /broken, response "default": The template in the body cannot be rendered: The variable {{name}} is not defined.`
		if doc.Error != want {
			t.Errorf("error = %q\nwant    %q", doc.Error, want)
		}
		f.rec.waitFor(t, servers.KindError, 1)
		var logged bool
		for _, ev := range f.rec.all() {
			if ev.Kind == servers.KindError && ev.Summary == want {
				logged = true
			}
			if ev.Kind == servers.KindResponse && ev.HTTP != nil && ev.HTTP.Status == 500 && !strings.Contains(ev.Summary, "template error") {
				t.Errorf("response summary = %q", ev.Summary)
			}
		}
		if !logged {
			t.Errorf("no error event in the traffic log: %+v", f.rec.all())
		}
	})
}
