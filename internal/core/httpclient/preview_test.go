package httpclient

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/cookies"
)

// unsizedBody is a body of unknown length, sent chunked.
type unsizedBody struct{ data []byte }

func (b unsizedBody) Open() (io.ReadCloser, int64, error) {
	return io.NopCloser(bytes.NewReader(b.data)), -1, nil
}
func (unsizedBody) ContentType() string { return "text/plain" }

// HTTP-09: Preview lists exactly the header fields a send writes, in the
// same order, for the cases where the client adds fields of its own.
func TestPreviewMatchesSend(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	jar := cookies.New(cookies.Options{})
	if _, rejected := jar.SetCookieLines(&url.URL{Scheme: "http", Host: host, Path: "/"}, []string{"sid=abc; Path=/", "theme=dark; Path=/"}); len(rejected) > 0 {
		t.Fatalf("rejected %v", rejected)
	}
	big := bytes.Repeat([]byte("x"), 20000)

	cases := []struct {
		name string
		req  Request
		jar  CookieJar
	}{
		{"get", Request{Method: "GET", URL: srv.URL + "/a"}, nil},
		{"post empty", Request{Method: "POST", URL: srv.URL + "/a"}, nil},
		{"raw body", Request{Method: "POST", URL: srv.URL + "/a", Headers: []Header{on("X-B", "2"), on("accept", "*/*")}, Body: RawBody{Data: []byte(`{"a":1}`), Type: "application/json"}}, nil},
		{"large body", Request{Method: "PUT", URL: srv.URL + "/a", Body: RawBody{Data: big, Type: "application/octet-stream"}}, nil},
		{"chunked", Request{Method: "POST", URL: srv.URL + "/a", Body: unsizedBody{[]byte("abc")}}, nil},
		{"host and user agent rows", Request{Method: "GET", URL: srv.URL + "/a", Headers: []Header{on("Host", "example.test"), on("user-agent", "probe/1")}}, nil},
		{"content type row", Request{Method: "POST", URL: srv.URL + "/a", Headers: []Header{on("Content-Type", "text/x")}, Body: RawBody{Data: []byte("x"), Type: "application/json"}}, nil},
		{"userinfo", Request{Method: "GET", URL: "http://u:p@" + host + "/a"}, nil},
		{"jar", Request{Method: "GET", URL: srv.URL + "/a"}, jar},
		{"jar and cookie row", Request{Method: "GET", URL: srv.URL + "/a", Headers: []Header{on("Cookie", "sid=mine")}}, jar},
		{"jar off", Request{Method: "GET", URL: srv.URL + "/a", Settings: Settings{SkipSendingCookies: true}}, jar},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := tc.req
			if r.Settings.URLEncoding == "" {
				skip := r.Settings.SkipSendingCookies
				r.Settings = DefaultSettings()
				r.Settings.SkipSendingCookies = skip
			}
			got, err := Preview(r, tc.jar)
			if err != nil {
				t.Fatalf("Preview: %v", err)
			}
			c := newTestClient(t, TransportOptions{})
			if tc.jar != nil {
				c = c.WithCookieJar(tc.jar)
			}
			r.Settings.HTTPVersion = HTTPVersion1
			resp, err := c.Send(t.Context(), r)
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			hasCookie := slices.ContainsFunc(got, func(h Header) bool { return h.Name == "Cookie" && strings.Contains(h.Value, "theme=dark") })
			if wantCookie := tc.jar != nil && !r.Settings.SkipSendingCookies; hasCookie != wantCookie {
				t.Errorf("jar cookie previewed = %v, want %v: %v", hasCookie, wantCookie, got)
			}
			if !slices.Equal(got, resp.Request.Headers) {
				t.Errorf("Preview =\n%v\nsent =\n%v", got, resp.Request.Headers)
			}
		})
	}
}

func TestPreviewErrors(t *testing.T) {
	t.Parallel()
	if _, err := Preview(Request{Method: "GET", URL: "not a url", Settings: DefaultSettings()}, nil); err == nil {
		t.Error("an invalid URL previews")
	}
	r := Request{Method: "GET", URL: "http://example.test/", Headers: []Header{on("Bad Name", "x")}, Settings: DefaultSettings()}
	if _, err := Preview(r, nil); err == nil {
		t.Error("an invalid header previews")
	}
}
