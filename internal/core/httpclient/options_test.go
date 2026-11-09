package httpclient

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// echoTarget answers with the request target as it arrived.
var echoTarget = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.RequestURI) })

// TestURLEncodingModes sends one URL in each mode (HTTP-24) and checks the
// request target a test server receives; TestWire has the full character
// set.
func TestURLEncodingModes(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(echoTarget)
	defer srv.Close()
	c := newTestClient(t, TransportOptions{})

	const target = "/a b/{id}/[x]|%41?q={v}&arr[]=1&s='t'&p=%41"
	tests := []struct {
		enc  URLEncoding
		want string
	}{
		{URLEncodingWHATWG, "/a%20b/%7Bid%7D/[x]|%41?q={v}&arr[]=1&s=%27t%27&p=%41"},
		{URLEncodingRFC3986, "/a%20b/%7Bid%7D/%5Bx%5D%7C%41?q=%7Bv%7D&arr%5B%5D=1&s='t'&p=%41"},
		{URLEncodingAsTyped, "/a%20b/{id}/[x]|%41?q={v}&arr[]=1&s='t'&p=%41"},
	}
	for _, tt := range tests {
		r := get(srv.URL + target)
		r.Settings.URLEncoding = tt.enc
		resp, err := c.Send(t.Context(), r)
		if err != nil {
			t.Fatalf("%q: %v", tt.enc, err)
		}
		if got := string(resp.Body); got != tt.want {
			t.Errorf("%q: server got %q, want %q", tt.enc, got, tt.want)
		}
		if resp.Request.Target != tt.want {
			t.Errorf("%q: SentRequest.Target = %q, want %q", tt.enc, resp.Request.Target, tt.want)
		}
	}

	r := get(srv.URL + "/")
	r.Settings.URLEncoding = "percent"
	_, err := c.Send(t.Context(), r)
	if e := wantErr(t, err, errs.InvalidArgument, CodeInvalidSetting); e.Details["field"] != "urlEncoding" {
		t.Errorf("details = %v", e.Details)
	}
}

// TestHTTPVersion sends each HTTP version choice (HTTP-25) to servers that
// speak HTTP/1.1 only, HTTP/2 through ALPN, and h2c.
func TestHTTPVersion(t *testing.T) {
	t.Parallel()

	proto := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.Proto) })
	tlsH1 := httptest.NewUnstartedServer(proto)
	tlsH1.StartTLS()
	defer tlsH1.Close()
	tlsH2 := httptest.NewUnstartedServer(proto)
	tlsH2.EnableHTTP2 = true
	tlsH2.StartTLS()
	defer tlsH2.Close()
	plainH1 := httptest.NewServer(proto)
	defer plainH1.Close()
	h2c := httptest.NewUnstartedServer(proto)
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	h2c.Config.Protocols = &p
	h2c.Start()
	defer h2c.Close()

	c := newTestClient(t, TransportOptions{InsecureSkipVerify: true})
	const unsupported = "http2_unsupported"
	tests := []struct {
		name    string
		url     string
		version HTTPVersion
		want    string // the proto the server saw, or unsupported
	}{
		{"auto tls h1", tlsH1.URL, HTTPVersionAuto, "HTTP/1.1"},
		{"auto tls h2", tlsH2.URL, HTTPVersionAuto, "HTTP/2.0"},
		{"auto plain", plainH1.URL, HTTPVersionAuto, "HTTP/1.1"},
		{"auto h2c server", h2c.URL, HTTPVersionAuto, "HTTP/1.1"},
		{"http1 tls h2", tlsH2.URL, HTTPVersion1, "HTTP/1.1"},
		{"http1 h2c server", h2c.URL, HTTPVersion1, "HTTP/1.1"},
		{"http2 tls h2", tlsH2.URL, HTTPVersion2, "HTTP/2.0"},
		{"http2 h2c", h2c.URL, HTTPVersion2, "HTTP/2.0"},
		{"http2 tls h1", tlsH1.URL, HTTPVersion2, unsupported},
		{"http2 plain h1", plainH1.URL, HTTPVersion2, unsupported},
	}
	for _, tt := range tests {
		r := get(tt.url + "/")
		r.Settings.HTTPVersion = tt.version
		resp, err := c.Send(t.Context(), r)
		if tt.want == unsupported {
			e := wantErr(t, err, errs.Protocol, CodeHTTP2Unsupported)
			if !strings.Contains(e.Hint, "HTTP version") {
				t.Errorf("%s: hint = %q", tt.name, e.Hint)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if got := string(resp.Body); got != tt.want {
			t.Errorf("%s: server saw %s, want %s", tt.name, got, tt.want)
		}
		wantConn := "HTTP/1.1"
		if tt.want == "HTTP/2.0" {
			wantConn = "h2"
		}
		if resp.Conn.Protocol != wantConn {
			t.Errorf("%s: Conn.Protocol = %q, want %q", tt.name, resp.Conn.Protocol, wantConn)
		}
		options := eventDetails(resp.Events, EventRequestOptions)
		switch tt.version {
		case HTTPVersionAuto:
			if len(options) != 0 {
				t.Errorf("%s: request_options = %q, want none", tt.name, options)
			}
		case HTTPVersion1:
			if len(options) != 1 || options[0] != "HTTP/1.1 only" {
				t.Errorf("%s: request_options = %q", tt.name, options)
			}
		case HTTPVersion2:
			if len(options) != 1 || options[0] != "HTTP/2 only" {
				t.Errorf("%s: request_options = %q", tt.name, options)
			}
		}
	}

	// A connection negotiated for one choice is not reused for another.
	for _, v := range []HTTPVersion{HTTPVersion2, HTTPVersion1, HTTPVersionAuto, HTTPVersion1} {
		r := get(tlsH2.URL + "/")
		r.Settings.HTTPVersion = v
		resp, err := c.Send(t.Context(), r)
		if err != nil {
			t.Fatal(err)
		}
		want := "HTTP/2.0"
		if v == HTTPVersion1 {
			want = "HTTP/1.1"
		}
		if string(resp.Body) != want {
			t.Errorf("%q after other versions: server saw %s, want %s", v, resp.Body, want)
		}
	}

	r := get(plainH1.URL + "/")
	r.Settings.HTTPVersion = "http3"
	_, err := c.Send(t.Context(), r)
	if e := wantErr(t, err, errs.InvalidArgument, CodeInvalidSetting); e.Details["field"] != "httpVersion" {
		t.Errorf("details = %v", e.Details)
	}
}

// TestCookieSwitches turns sending and storing the jar's cookies off per
// request (HTTP-26).
func TestCookieSwitches(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/set", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "fresh=new; Path=/")
		w.Header().Add("Set-Cookie", "other=2; Path=/")
		io.WriteString(w, r.Header.Get("Cookie"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	u, _ := url.Parse(srv.URL + "/")

	send := func(t *testing.T, jar *cookies.Jar, skipSend, skipStore bool, headers ...Header) *Response {
		t.Helper()
		r := get(srv.URL+"/set", headers...)
		r.Settings.SkipSendingCookies = skipSend
		r.Settings.SkipStoringCookies = skipStore
		resp, err := newTestClient(t, TransportOptions{}).WithCookieJar(jar).Send(t.Context(), r)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	jarWith := func(t *testing.T) *cookies.Jar {
		jar := cookies.New(cookies.Options{})
		jar.SetCookieLines(u, []string{"old=1; Path=/"})
		return jar
	}
	names := func(jar *cookies.Jar) string {
		var out []string
		for _, c := range jar.Cookies(u) {
			out = append(out, c.Name)
		}
		return strings.Join(out, ",")
	}

	t.Run("both on", func(t *testing.T) {
		jar := jarWith(t)
		resp := send(t, jar, false, false)
		if string(resp.Body) != "old=1" || names(jar) != "old,fresh,other" {
			t.Errorf("sent %q, jar %s", resp.Body, names(jar))
		}
		if o := eventDetails(resp.Events, EventRequestOptions); len(o) != 0 {
			t.Errorf("request_options = %q", o)
		}
	})
	t.Run("not sent", func(t *testing.T) {
		jar := jarWith(t)
		resp := send(t, jar, true, false, on("Cookie", "row=r"))
		if string(resp.Body) != "row=r" || resp.RequestCookie != "row=r" {
			t.Errorf("sent %q (RequestCookie %q), want only the row", resp.Body, resp.RequestCookie)
		}
		if names(jar) != "old,fresh,other" {
			t.Errorf("jar = %s, want the response's cookies stored", names(jar))
		}
		if s := eventDetails(resp.Events, EventCookiesSent); len(s) != 0 {
			t.Errorf("cookies_sent = %q", s)
		}
		if o := eventDetails(resp.Events, EventRequestOptions); len(o) != 1 || o[0] != "jar cookies not sent" {
			t.Errorf("request_options = %q", o)
		}
	})
	t.Run("not stored", func(t *testing.T) {
		jar := jarWith(t)
		resp := send(t, jar, false, true)
		if string(resp.Body) != "old=1" {
			t.Errorf("sent %q, want the jar's cookie", resp.Body)
		}
		if names(jar) != "old" {
			t.Errorf("jar = %s, want nothing stored", names(jar))
		}
		if s := eventDetails(resp.Events, EventCookiesStored); len(s) != 1 || s[0] != "none stored, off for this request: fresh, other" {
			t.Errorf("cookies_stored = %q", s)
		}
		if len(resp.Headers) == 0 || !strings.Contains(fmt.Sprint(resp.Headers), "fresh=new") {
			t.Errorf("Set-Cookie missing from the response headers: %v", resp.Headers)
		}
	})
	t.Run("neither", func(t *testing.T) {
		jar := jarWith(t)
		resp := send(t, jar, true, true)
		if string(resp.Body) != "" || names(jar) != "old" {
			t.Errorf("sent %q, jar %s", resp.Body, names(jar))
		}
		if o := eventDetails(resp.Events, EventRequestOptions); len(o) != 1 || o[0] != "jar cookies not sent, response cookies not stored" {
			t.Errorf("request_options = %q", o)
		}
	})
}

// TestQueryMethod sends QUERY with a body (HTTP-03) and follows each kind
// of redirect: 301, 302, 307 and 308 keep the method and body, 303 turns
// it into a GET without body.
func TestQueryMethod(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	for _, code := range []int{301, 302, 303, 307, 308} {
		mux.HandleFunc(fmt.Sprintf("/r%d", code), func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			http.Redirect(w, r, "/echo", code)
		})
	}
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s %s [%s] idem=%v", r.Method, r.Header.Get("Content-Type"), body, r.Header.Values("Idempotency-Key"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestClient(t, TransportOptions{})

	query := func(path string) Request {
		return Request{
			Method:   MethodQuery,
			URL:      srv.URL + path,
			Body:     RawBody{Data: []byte(`{"q":"x"}`), Type: "application/json"},
			Settings: DefaultSettings(),
		}
	}
	tests := []struct {
		path, want string
	}{
		{"/echo", `QUERY application/json [{"q":"x"}] idem=[]`},
		{"/r301", `QUERY application/json [{"q":"x"}] idem=[]`},
		{"/r302", `QUERY application/json [{"q":"x"}] idem=[]`},
		{"/r303", `GET  [] idem=[]`},
		{"/r307", `QUERY application/json [{"q":"x"}] idem=[]`},
		{"/r308", `QUERY application/json [{"q":"x"}] idem=[]`},
	}
	for _, tt := range tests {
		resp, err := c.Send(t.Context(), query(tt.path))
		if err != nil {
			t.Fatalf("%s: %v", tt.path, err)
		}
		if got := string(resp.Body); got != tt.want {
			t.Errorf("%s: echo = %s, want %s", tt.path, got, tt.want)
		}
	}

	// KeepMethod keeps QUERY on a 303 too.
	r := query("/r303")
	r.Settings.KeepMethod = true
	resp, err := c.Send(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(resp.Body), `QUERY application/json [{"q":"x"}] idem=[]`; got != want {
		t.Errorf("303 with KeepMethod: echo = %s, want %s", got, want)
	}

	// A POST still turns into a GET on 301 and 302.
	post := query("/r301")
	post.Method = http.MethodPost
	if resp, err = c.Send(t.Context(), post); err != nil {
		t.Fatal(err)
	}
	if got := string(resp.Body); !strings.HasPrefix(got, "GET ") {
		t.Errorf("POST after 301: echo = %s, want a GET", got)
	}
}

// TestQueryIdempotent: net/http treats a QUERY request as replayable, so a
// kept-alive connection the server closed is retried like a GET's, and
// the marker is not sent.
func TestQueryIdempotent(t *testing.T) {
	t.Parallel()

	h := http.Header{}
	markIdempotent(h)
	if v, ok := h["Idempotency-Key"]; !ok || v != nil {
		t.Errorf("marker = %v, %v; want a nil entry", v, ok)
	}
	h = http.Header{"X-Idempotency-Key": {"k"}}
	markIdempotent(h)
	if _, ok := h["Idempotency-Key"]; ok {
		t.Error("marker added although X-Idempotency-Key is set")
	}

	p, err := prepare(Request{Method: MethodQuery, URL: "http://h/", Body: RawBody{Data: []byte("q")}, Settings: DefaultSettings()})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := p.req.Header["Idempotency-Key"]; !ok || v != nil {
		t.Errorf("QUERY request marker = %v, %v", v, ok)
	}
	p, err = prepare(Request{Method: http.MethodPost, URL: "http://h/", Settings: DefaultSettings()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.req.Header["Idempotency-Key"]; ok {
		t.Error("POST request marked idempotent")
	}
}

func TestOptionsDetail(t *testing.T) {
	t.Parallel()

	if d := optionsDetail(DefaultSettings(), true); d != "" {
		t.Errorf("defaults: %q", d)
	}
	s := DefaultSettings()
	s.URLEncoding = URLEncodingAsTyped
	s.HTTPVersion = HTTPVersion2
	s.SkipSendingCookies = true
	if d := optionsDetail(s, false); d != "HTTP/2 only, URL not encoded" {
		t.Errorf("without jar: %q", d)
	}
	s.URLEncoding = URLEncodingRFC3986
	s.SkipStoringCookies = true
	if d := optionsDetail(s, true); d != "HTTP/2 only, URL encoding RFC 3986, jar cookies not sent, response cookies not stored" {
		t.Errorf("with jar: %q", d)
	}
}
