package httpclient

import (
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// failingBody is a Body whose Open fails, like a deleted file.
type failingBody struct{}

func (failingBody) Open() (io.ReadCloser, int64, error) { return nil, 0, fs.ErrNotExist }
func (failingBody) ContentType() string                 { return "" }

func TestPrepareErrors(t *testing.T) {
	t.Parallel()

	body := RawBody{Data: []byte("abc")}
	tests := []struct {
		name string
		req  Request
		kind errs.Kind
		code string
	}{
		{"no method", Request{URL: "http://h/"}, errs.InvalidArgument, CodeInvalidMethod},
		{"method with a space", Request{Method: "GE T", URL: "http://h/"}, errs.InvalidArgument, CodeInvalidMethod},
		{"method with a slash", Request{Method: "GET/1", URL: "http://h/"}, errs.InvalidArgument, CodeInvalidMethod},
		{"CONNECT", Request{Method: "CONNECT", URL: "http://h/"}, errs.Unsupported, CodeMethodUnsupported},
		{"no URL", get(""), errs.InvalidArgument, CodeInvalidURL},
		{"no scheme", get("example.com/items"), errs.InvalidArgument, CodeInvalidURL},
		{"other scheme", get("ftp://example.com/"), errs.Unsupported, CodeSchemeUnsupported},
		{"websocket scheme", get("wss://example.com/"), errs.Unsupported, CodeSchemeUnsupported},
		{"no host", get("http:///path"), errs.InvalidArgument, CodeInvalidURL},
		{"bad port", get("http://h:port/"), errs.InvalidArgument, CodeInvalidURL},
		{"double slash path with a bad escape", get("http://h//%zz"), errs.InvalidArgument, CodeInvalidURL},
		{"header name with a space", get("http://h/", on("X Bad", "1")), errs.InvalidArgument, CodeInvalidHeader},
		{"header name with a colon", get("http://h/", on("X:y", "1")), errs.InvalidArgument, CodeInvalidHeader},
		{"empty header name", get("http://h/", on("", "1")), errs.InvalidArgument, CodeInvalidHeader},
		{"header value with a line break", get("http://h/", on("X", "a\r\nInjected: 1")), errs.InvalidArgument, CodeInvalidHeader},
		{"header value with NUL", get("http://h/", on("X", "a\x00Injected")), errs.InvalidArgument, CodeInvalidHeader},
		{"two Host headers", get("http://h/", on("Host", "a"), on("host", "b")), errs.Unsupported, CodeHeaderUnsupported},
		{"invalid Host", get("http://h/", on("Host", "a b")), errs.InvalidArgument, CodeInvalidHeader},
		{"empty Host", get("http://h/", on("Host", "")), errs.InvalidArgument, CodeInvalidHeader},
		{"two User-Agent headers", get("http://h/", on("User-Agent", "a"), on("user-agent", "b")), errs.Unsupported, CodeHeaderUnsupported},
		{"Transfer-Encoding gzip", get("http://h/", on("Transfer-Encoding", "gzip")), errs.Unsupported, CodeHeaderUnsupported},
		{"Trailer", get("http://h/", on("Trailer", "X-Sum")), errs.Unsupported, CodeHeaderUnsupported},
		{"Content-Length larger than the body", Request{Method: "POST", URL: "http://h/", Body: body, Headers: []Header{on("Content-Length", "4")}}, errs.InvalidArgument, CodeContentLengthMismatch},
		{"Content-Length without a body", get("http://h/", on("content-length", "1")), errs.InvalidArgument, CodeContentLengthMismatch},
		{"Content-Length not a number", Request{Method: "POST", URL: "http://h/", Body: body, Headers: []Header{on("Content-Length", "three")}}, errs.InvalidArgument, CodeContentLengthMismatch},
		{"Content-Length and chunked", Request{Method: "POST", URL: "http://h/", Body: body, Headers: []Header{on("Content-Length", "3"), on("Transfer-Encoding", "chunked")}}, errs.InvalidArgument, CodeInvalidHeader},
		{"negative timeout", Request{Method: "GET", URL: "http://h/", Settings: Settings{Timeout: -time.Second}}, errs.InvalidArgument, CodeInvalidSetting},
		{"negative header timeout", Request{Method: "GET", URL: "http://h/", Settings: Settings{ResponseHeaderTimeout: -1}}, errs.InvalidArgument, CodeInvalidSetting},
		{"negative redirect limit", Request{Method: "GET", URL: "http://h/", Settings: Settings{MaxRedirects: -1}}, errs.InvalidArgument, CodeInvalidSetting},
		{"body that cannot be opened", Request{Method: "POST", URL: "http://h/", Body: failingBody{}}, errs.NotFound, CodeBodyUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := prepare(tt.req)
			e := wantErr(t, err, tt.kind, tt.code)
			if e.Hint == "" && tt.code != CodeBodyUnavailable {
				t.Errorf("no hint: %v", err)
			}
			if strings.Contains(err.Error(), "Injected") {
				t.Errorf("the error repeats the header value: %v", err)
			}
		})
	}
}

func TestPrepareSkipsDisabledHeaders(t *testing.T) {
	t.Parallel()
	p, err := prepare(get("http://h/",
		Header{Name: "X Bad", Value: "\n"},
		Header{Name: "Host", Value: "other"},
		Header{Name: "Content-Length", Value: "99"},
	))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if p.req.Host != "" || p.req.ContentLength != 0 {
		t.Errorf("disabled rows used: Host %q, ContentLength %d", p.req.Host, p.req.ContentLength)
	}
	if want := (http.Header{"User-Agent": {""}}); len(p.req.Header) != 1 || p.req.Header["User-Agent"][0] != "" {
		t.Errorf("header = %v, want %v", p.req.Header, want)
	}
}

func TestParseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw        string
		requestURI string
		host       string
		display    string
	}{
		{"http://h/a%2Fb/%7e?x=%20&x=2", "/a%2Fb/%7e?x=%20&x=2", "h", "http://h/a%2Fb/%7e?x=%20&x=2"},
		{"http://h", "/", "h", "http://h/"},
		{"http://h?", "/?", "h", "http://h/?"},
		{"http://h?a=1", "/?a=1", "h", "http://h/?a=1"},
		{"http://h/a b/é", "/a%20b/%C3%A9", "h", "http://h/a%20b/%C3%A9"},
		{"http://u:p@h:8080/x#frag", "/x", "h:8080", "http://h:8080/x"},
		{"HTTPS://Example.COM/Path", "/Path", "Example.COM", "https://Example.COM/Path"},
		{"http://h//x//y", "//x//y", "h", "http://h//x//y"},
		{"http://[::1]:80/", "/", "[::1]:80", "http://[::1]:80/"},
		{"https://h/%zz/{a}|b", "/%zz/{a}|b", "h", "https://h/%zz/{a}|b"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			t.Parallel()
			u, err := parseURL(tt.raw, URLEncodingAsTyped)
			if err != nil {
				t.Fatalf("parseURL: %v", err)
			}
			if got := u.RequestURI(); got != tt.requestURI {
				t.Errorf("RequestURI = %q, want %q", got, tt.requestURI)
			}
			if u.Host != tt.host {
				t.Errorf("Host = %q, want %q", u.Host, tt.host)
			}
			if got := displayURL(u); got != tt.display {
				t.Errorf("displayURL = %q, want %q", got, tt.display)
			}
		})
	}
}

func TestSettingsDefaults(t *testing.T) {
	t.Parallel()

	want := Settings{FollowRedirects: true, MaxRedirects: 10, MaxBodyBytes: 100 << 20, URLEncoding: URLEncodingWHATWG}
	if got := DefaultSettings(); got != want {
		t.Errorf("DefaultSettings() = %+v, want %+v", got, want)
	}
	got, err := Settings{}.normalize()
	if err != nil {
		t.Fatal(err)
	}
	if want := (Settings{MaxBodyBytes: DefaultMaxBodyBytes}); got != want {
		t.Errorf("zero Settings normalize to %+v, want %+v", got, want)
	}
	got, _ = Settings{MaxBodyBytes: -1, MaxRedirects: 3}.normalize()
	if got.MaxBodyBytes != -1 || got.MaxRedirects != 3 {
		t.Errorf("explicit values changed: %+v", got)
	}
}
