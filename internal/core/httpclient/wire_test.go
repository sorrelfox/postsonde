package httpclient

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// TestWire sends requests to a raw TCP server and compares the bytes that
// arrive with testdata/wire/<name>.golden. Regenerate with -update and
// review the diff: these files are the "no hidden changes" contract.
func TestWire(t *testing.T) {
	t.Parallel()

	binary := make([]byte, 256)
	for i := range binary {
		binary[i] = byte(i)
	}
	post := func(url string, body Body, headers ...Header) Request {
		return Request{Method: http.MethodPost, URL: url, Headers: headers, Body: body, Settings: DefaultSettings()}
	}
	encoded := func(enc URLEncoding, url string) Request {
		r := get(url)
		r.Settings.URLEncoding = enc
		return r
	}
	// Every character the encoding modes treat differently (HTTP-24).
	const mixed = "http://ADDR/p a/{x}/\"q\"/<t>/`^|[]\\/%zz/%41/é?a=\"1\"&b=<2>&c='3'&d={4}&e[]=5&f=|^`&g=%zz&h=?/:@ é"
	tests := []struct {
		name string
		req  Request
	}{
		// Request line.
		{"raw_path_and_query", encoded(URLEncodingAsTyped, "http://ADDR/a%2Fb/c%7e/%zz/{x}/;p=1?q=1&q=2&e=%20&plus=a+b&u=%2f#fragment")},
		{"url_encoding_auto", encoded(URLEncodingWHATWG, mixed)},
		{"url_encoding_rfc3986", encoded(URLEncodingRFC3986, mixed)},
		{"url_encoding_none", encoded(URLEncodingAsTyped, mixed)},
		{"query_method", Request{Method: MethodQuery, URL: "http://ADDR/contacts", Body: RawBody{Data: []byte(`select=name&limit=10`), Type: "application/x-www-form-urlencoded"}, Settings: DefaultSettings()}},
		{"minimal_encoding", get("http://ADDR/a b/é?q=ü v")},
		{"empty_path", get("http://ADDR")},
		{"empty_query", get("http://ADDR/p?")},
		{"double_slash_path", get("http://ADDR//x//y?z")},
		{"upper_case_scheme", get("HTTP://ADDR/")},
		{"custom_method", Request{Method: "PROPFIND", URL: "http://ADDR/dav/"}},
		{"lower_case_method", Request{Method: "patch", URL: "http://ADDR/"}},

		// Headers.
		{"no_default_headers", get("http://ADDR/")},
		{"header_casing_duplicates_disabled", get("http://ADDR/",
			on("x-lower", "1"),
			on("X-MiXeD-CaSe", "2"),
			on("X-Dup", "a"),
			Header{Name: "X-Off", Value: "not sent"},
			on("X-Dup", "b"),
			on("x-dup", "c"),
			on("ETag", `"v1"`),
			on("X-Spaces", "  padded  "),
		)},
		{"user_agent_as_typed", get("http://ADDR/", on("user-agent", "PostSonde-Test/1.0"))},
		{"user_agent_canonical", get("http://ADDR/", on("User-Agent", "curl/8.0"))},
		{"user_agent_empty", get("http://ADDR/", on("User-Agent", ""))},
		{"accept_encoding_as_typed", get("http://ADDR/", on("Accept-Encoding", "gzip, br"))},
		{"host_override", get("http://ADDR/", on("host", "api.example.test:8443"))},
		{"userinfo_to_basic_auth", get("http://us%40er:p%3Ass@ADDR/")},
		{"userinfo_with_authorization_row", get("http://us%40er:p%3Ass@ADDR/", on("authorization", "Bearer t"))},

		// Bodies.
		{"post_raw_body", post("http://ADDR/items", RawBody{Data: []byte(`{"a":1}`), Type: "application/json"})},
		{"content_type_row_wins", post("http://ADDR/", RawBody{Data: []byte("x"), Type: "text/plain"}, on("content-type", "application/x-custom"))},
		{"post_without_body", post("http://ADDR/", nil)},
		{"delete_without_body", Request{Method: http.MethodDelete, URL: "http://ADDR/items/1"}},
		{"get_with_body", Request{Method: http.MethodGet, URL: "http://ADDR/", Body: RawBody{Data: []byte("q")}}},
		{"content_length_row", post("http://ADDR/", RawBody{Data: []byte("hello")}, on("content-length", "5"))},
		{"chunked", post("http://ADDR/", RawBody{Data: []byte("hello")}, on("transfer-encoding", "chunked"))},
		{"chunked_empty", post("http://ADDR/", nil, on("Transfer-Encoding", "chunked"))},
		{"binary_body", post("http://ADDR/upload", RawBody{Data: binary, Type: "application/octet-stream"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := startRawServer(t, okResponse)
			c := newTestClient(t, TransportOptions{})
			req := tt.req
			req.URL = strings.ReplaceAll(req.URL, "ADDR", srv.addr)

			resp, err := c.Send(t.Context(), req)
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if resp.StatusCode != http.StatusOK || string(resp.Body) != "ok" {
				t.Fatalf("response = %d %q, want 200 \"ok\"", resp.StatusCode, resp.Body)
			}
			raw := srv.next(t)
			head, _, ok := bytes.Cut(raw, []byte("\r\n\r\n"))
			if !ok {
				t.Fatalf("no end of head in %q", raw)
			}
			if n := bytes.Count(head, []byte("\n")); n != bytes.Count(head, []byte("\r\n")) {
				t.Errorf("head has bare line feeds: %q", head)
			}
			golden.Assert(t, "wire/"+tt.name, renderWire(raw, srv.addr))
			checkSent(t, resp.Request, raw)
		})
	}
}

// TestWireBody checks that body bytes arrive exactly, with their length.
func TestWireBody(t *testing.T) {
	t.Parallel()

	binary := make([]byte, 64<<10)
	for i := range binary {
		binary[i] = byte(i * 7)
	}
	tests := []struct {
		name string
		body []byte
	}{
		{"binary", binary},
		{"crlf_and_nul", []byte("a\r\n\r\nb\x00c\n")},
		{"utf8", []byte("héllo 世界")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := startRawServer(t, okResponse)
			c := newTestClient(t, TransportOptions{})
			req := Request{Method: http.MethodPut, URL: "http://" + srv.addr + "/", Body: RawBody{Data: tt.body}}
			resp, err := c.Send(t.Context(), req)
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			raw := srv.next(t)
			checkSent(t, resp.Request, raw)
			_, body, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
			if !bytes.Equal(body, tt.body) {
				t.Errorf("body differs: got %d bytes, want %d", len(body), len(tt.body))
			}
			want := "\r\nContent-Length: " + strconv.Itoa(len(tt.body)) + "\r\n"
			if !bytes.Contains(raw, []byte(want)) {
				t.Errorf("no %q in the head", want)
			}
		})
	}
}

// checkSent checks that the recorded request is what the server received
// (DBG-01): byte for byte for a body with a length, and the head and the
// decoded body for a chunked one.
func checkSent(t *testing.T, sent *SentRequest, raw []byte) {
	t.Helper()
	if sent == nil {
		t.Fatal("Response.Request is nil")
	}
	if sent.Proto != "HTTP/1.1" {
		t.Errorf("Proto = %q, want HTTP/1.1", sent.Proto)
	}
	if sent.BodyTruncated || int64(len(sent.Body)) != sent.BodySize {
		t.Fatalf("body recorded %d of %d bytes, truncated %v", len(sent.Body), sent.BodySize, sent.BodyTruncated)
	}
	head := sent.Head()
	chunked := false
	for _, h := range sent.Headers {
		chunked = chunked || strings.EqualFold(h.Name, "Transfer-Encoding")
	}
	if !chunked {
		if got := append(head, sent.Body...); !bytes.Equal(got, raw) {
			t.Errorf("recorded request differs from the bytes received:\n got %q\nwant %q", got, raw)
		}
		return
	}
	if !bytes.HasPrefix(raw, head) {
		t.Errorf("recorded head differs from the bytes received:\n got %q\nwant %q", head, raw)
	}
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatalf("parse the received request: %v", err)
	}
	body, _ := io.ReadAll(req.Body)
	if !bytes.Equal(body, sent.Body) {
		t.Errorf("recorded body %q, received %q", sent.Body, body)
	}
}
