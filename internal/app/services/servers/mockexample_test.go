package servers

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// recorded sends a GET with the request headers hs to url through the
// HTTP service, as the app does (decompressing gzip, recording the
// history), and returns the history entry.
func recorded(t *testing.T, url string, hs []projectfile.KeyValue) history.Entry {
	t.Helper()
	st, err := history.Open(filepath.Join(t.TempDir(), history.DirName), history.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	src := collection.HTTPSource{
		Request: projectfile.HTTPRequest{ID: "req_1", Method: "GET", URL: url, Headers: hs},
		Auth:    projectfile.Auth{Type: projectfile.AuthNone},
	}
	ops := operation.NewRegistry(func(string, any) {}, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ops.Shutdown(ctx)
	})
	load := func(context.Context, string) (collection.HTTPSource, error) { return src, nil }
	svc := httpservice.New(load, func() time.Duration { return 0 }, ops,
		netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(nil))), nil, slog.New(slog.DiscardHandler),
		httpservice.WithHistory(func() func(history.Entry) {
			return func(e history.Entry) {
				if _, err := st.Append(e); err != nil {
					t.Errorf("Append = %v", err)
				}
			}
		}))
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	if _, err := svc.Send(t.Context(), httpservice.SendInputDTO{RequestID: "req_1"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if p := st.List(history.Query{}); p.Total == 1 {
			e, err := st.Get(p.Entries[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if e.Response == nil {
				t.Fatalf("no response: %+v", e)
			}
			return e
		}
		if time.Now().After(deadline) {
			t.Fatal("the send was not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// fields is a response's header fields, name canonical, in order, without
// those the mock server makes its own (Date, Content-Length).
func fields(h http.Header) map[string][]string {
	out := map[string][]string{}
	for k, v := range h {
		if k == "Date" || k == "Content-Length" {
			continue
		}
		out[k] = v
	}
	return out
}

// MOCK-03 end to end: real responses (a gzip-compressed JSON body with
// cookies, and a binary body) are sent through the HTTP service and
// recorded, made into response examples (as HistoryService.Example does)
// and then into mock routes; the running mock server answers with the
// same status, the same header fields (but the documented ones: Date,
// Content-Length and the Content-Encoding PostSonde removed) and the same
// body bytes, with the secret the response echoed masked.
func TestMockRouteFromExampleEndToEnd(t *testing.T) {
	t.Parallel()
	payload := `{"id":42,"name":"Ada","token":"s3cr3t-value","tags":["a","b"]}`
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0xff, 0xfe, 1, 2, 3}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		switch r.URL.Path {
		case "/v1/users/42":
			h.Set("Content-Type", "application/json; charset=utf-8")
			h.Set("X-Request-Id", "req-123")
			h.Set("Cache-Control", "max-age=60")
			h.Set("ETag", `"v1"`)
			h.Add("Set-Cookie", "a=1; Path=/")
			h.Add("Set-Cookie", "b=2; Path=/")
			h.Set("X-Token-Echo", "s3cr3t-value")
			h.Set("Vary", "Accept-Encoding")
			h.Set("Content-Encoding", "gzip")
			w.WriteHeader(http.StatusCreated)
			gz := gzip.NewWriter(w)
			_, _ = gz.Write([]byte(payload))
			_ = gz.Close()
		case "/v1/files/logo.png":
			h.Set("Content-Type", "image/png")
			h.Set("Cache-Control", "no-cache")
			_, _ = w.Write(png)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	svc, _ := newTestService(t)
	svc.knownSecrets = func(context.Context) ([]string, error) { return []string{"s3cr3t-value"}, nil }

	type want struct {
		status int
		header map[string][]string
		body   []byte
	}
	var wants []want
	cfg := MockHTTPConfigDTO{Routes: []MockRouteDTO{}}
	for _, tc := range []struct {
		path, typed, pattern string
		hs                   []projectfile.KeyValue
	}{
		{"/v1/users/42", "{{baseUrl}}/users/:id", "/v1/users/:id", []projectfile.KeyValue{{Name: "Accept-Encoding", Value: "gzip"}}},
		{"/v1/files/logo.png", "{{baseUrl}}/files/{{file}}", "/v1/files/:file", nil},
	} {
		e := recorded(t, upstream.URL+tc.path, tc.hs)
		ex, _, err := history.Example(e, "", time.Now(), nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := svc.MockRouteFromExample(t.Context(), MockExampleInputDTO{
			Example: collection.ToResponseExampleDTO(ex), Method: "GET", URL: tc.typed, Config: cfg,
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "added" || res.Path != tc.pattern || len(res.Diagnostics) != 0 {
			t.Fatalf("%s: status %s, path %s, diagnostics %+v", tc.path, res.Status, res.Path, res.Diagnostics)
		}
		data, _ := json.Marshal(res)
		if strings.Contains(string(data), "s3cr3t-value") {
			t.Errorf("the route holds the secret: %s", data)
		}
		cfg = res.Config

		// What the client got from the upstream server, less what the
		// route leaves out by design.
		w := want{status: e.Response.StatusCode, header: map[string][]string{}}
		for _, h := range e.Response.Headers {
			switch h.Name {
			case "Date", "Content-Length", "Content-Encoding":
				continue
			}
			v := strings.ReplaceAll(h.Value, "s3cr3t-value", httpclient.Mask)
			w.header[http.CanonicalHeaderKey(h.Name)] = append(w.header[http.CanonicalHeaderKey(h.Name)], v)
		}
		if tc.path == "/v1/users/42" {
			w.body = []byte(strings.ReplaceAll(payload, "s3cr3t-value", httpclient.Mask))
		} else {
			w.body = png
		}
		wants = append(wants, w)
	}

	created, err := svc.Create(t.Context(), CreateInputDTO{Type: mockhttp.TypeName, Config: ConfigDTO{MockHTTP: &cfg}})
	if err != nil {
		t.Fatal(err)
	}
	running, err := svc.Start(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(running.BoundPort))
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	for i, path := range []string{"/v1/users/7", "/v1/files/other.png"} {
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		w := wants[i]
		if resp.StatusCode != w.status {
			t.Errorf("%s: status %d, want %d", path, resp.StatusCode, w.status)
		}
		if !slices.Equal(body, w.body) {
			t.Errorf("%s: body %q, want %q", path, body, w.body)
		}
		if got := fields(resp.Header); !equalFields(got, w.header) {
			t.Errorf("%s: headers\n got %v\nwant %v", path, got, w.header)
		}
		if resp.Header.Get("Content-Length") != strconv.Itoa(len(w.body)) || resp.Header.Get("Date") == "" {
			t.Errorf("%s: Content-Length %q, Date %q", path, resp.Header.Get("Content-Length"), resp.Header.Get("Date"))
		}
	}
}

func equalFields(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

// The merge modes through the service: a second example for the same
// route is refused as a route, added as a named response or put in place
// of the route's response; an unknown mode is an error.
func TestMockRouteFromExampleModes(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	ex := collection.ResponseExampleDTO{Name: "200 OK", StatusCode: 200, StatusText: "OK",
		Headers: []collection.KeyValueDTO{{Name: "Content-Type", Value: "application/json"}, {Name: "Transfer-Encoding", Value: "chunked"}},
		Body:    `{"ok":true}`, Request: &collection.ExampleRequestDTO{Method: "GET", URL: "https://api.example/items/9"}}
	in := MockExampleInputDTO{Example: ex, Method: "GET", URL: "https://api.example/items/:id", Config: MockHTTPConfigDTO{Routes: []MockRouteDTO{}}}
	first, err := svc.MockRouteFromExample(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "added" || first.Existing != nil || len(first.Dropped) != 1 || first.Dropped[0].Reason != "hop_by_hop" ||
		first.Response.Body.Type != "json" || len(first.Config.Routes) != 1 {
		t.Fatalf("first = %+v", first)
	}
	in.Config = first.Config
	for mode, status := range map[string]string{"": "existing", "route": "existing", "response": "response_added", "replace": "replaced"} {
		in.Mode = mode
		res, err := svc.MockRouteFromExample(t.Context(), in)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != status || res.Existing == nil || res.Existing.ID != first.RouteID {
			t.Errorf("mode %q: %s, existing %+v", mode, res.Status, res.Existing)
		}
		if mode == "response" && (res.ResponseName != "200 OK" || len(res.Config.Routes[0].Responses) != 1) {
			t.Errorf("response: %q %+v", res.ResponseName, res.Config.Routes[0].Responses)
		}
	}
	in.Mode = "append"
	if _, err := svc.MockRouteFromExample(t.Context(), in); codeOf(err) != CodeInvalidMode {
		t.Errorf("unknown mode = %v", err)
	}
}
