package http

import (
	"fmt"
	"io"
	"log"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// End-to-end tests of the request's redirect settings (HTTP-22) and of the
// TLS verification switch, per request and global (HTTP-23): request file
// settings in, a real send against httptest servers, the response DTO out.

// TestSendTLSVerification: a server with a certificate the system does
// not trust is refused unless verification is off, by the request or by
// the app setting; the request's own verifyTls wins over the app's. A
// response sent without verification is marked.
func TestSendTLSVerification(t *testing.T) {
	t.Parallel()
	srv := httptest.NewUnstartedServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		io.WriteString(w, "secure-ish")
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)

	yes, no := true, false
	tests := []struct {
		name       string
		own        *bool
		appVerify  *bool // nil: no WithVerifyTLS
		wantOK     bool
		wantSource string
	}{
		{"verified by default", nil, nil, false, ""},
		{"app setting on", nil, &yes, false, ""},
		{"request turns it off", &no, nil, true, request.TLSFromRequest},
		{"app setting turns it off", nil, &no, true, request.TLSFromSettings},
		{"request turns it back on", &yes, &no, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := httpSource(srv.URL)
			src.Request.Settings.VerifyTLS = tt.own
			var opts []Option
			if tt.appVerify != nil {
				v := *tt.appVerify
				opts = append(opts, WithVerifyTLS(func() bool { return v }))
			}
			f := newFixture(t, src, nil, opts...)
			id := f.send(t)
			done := f.rec.wait(t, id)
			if !tt.wantOK {
				if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != httpclient.CodeTLSUnknownAuthority {
					t.Fatalf("done = %+v, want the certificate refused", done)
				}
				return
			}
			if done.Status != operation.StatusOK {
				t.Fatalf("done = %+v", done)
			}
			resp := f.rec.response(t, id)
			if resp.Body != "secure-ish" || !resp.InsecureTLS || resp.InsecureTLSSource != tt.wantSource {
				t.Errorf("response body %q, insecureTls %v from %q; want marked from %q", resp.Body, resp.InsecureTLS, resp.InsecureTLSSource, tt.wantSource)
			}
		})
	}
}

// TestSendVerifiedIsNotMarked: a plain send carries no TLS warning.
func TestSendVerifiedIsNotMarked(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(nethttp.ResponseWriter, *nethttp.Request) {}))
	t.Cleanup(srv.Close)
	f := newFixture(t, httpSource(srv.URL), nil)
	id := f.send(t)
	f.rec.wait(t, id)
	if resp := f.rec.response(t, id); resp.InsecureTLS || resp.InsecureTLSSource != "" {
		t.Errorf("insecureTls %v from %q, want unmarked", resp.InsecureTLS, resp.InsecureTLSSource)
	}
}

// TestSendRequestOptions: the URL encoding (HTTP-24), HTTP version
// (HTTP-25) and cookie (HTTP-26) settings of a request file, and the QUERY
// method (HTTP-03), sent end to end to a TLS server that speaks HTTP/2.
func TestSendRequestOptions(t *testing.T) {
	t.Parallel()
	srv := httptest.NewUnstartedServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		nethttp.SetCookie(w, &nethttp.Cookie{Name: "fresh", Value: "1", Path: "/"})
		b, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s %s %s [%s] cookie=%q", r.Proto, r.Method, r.RequestURI, b, r.Header.Get("Cookie"))
	}))
	srv.EnableHTTP2 = true
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL + "/")

	yes, no := true, false
	tests := []struct {
		name      string
		settings  projectfile.RequestSettings
		body      string
		jar       string // the jar's cookie names afterwards
		cookieJar string // CookieDTO.Jar of the response's cookie
		option    string // the request_options event, "" for none
	}{
		{"defaults", projectfile.RequestSettings{},
			`HTTP/2.0 QUERY /q%20x/%5E/[a]?f={b} [{"a":1}] cookie="old=1"`, "old,fresh", JarStored, ""},
		{"RFC 3986, HTTP/1.1", projectfile.RequestSettings{URLEncoding: "rfc3986", HTTPVersion: "http1"},
			`HTTP/1.1 QUERY /q%20x/%5E/%5Ba%5D?f=%7Bb%7D [{"a":1}] cookie="old=1"`, "old,fresh", JarStored, "HTTP/1.1 only, URL encoding RFC 3986"},
		{"no encoding, HTTP/2 only", projectfile.RequestSettings{URLEncoding: "none", HTTPVersion: "http2"},
			`HTTP/2.0 QUERY /q%20x/^/[a]?f={b} [{"a":1}] cookie="old=1"`, "old,fresh", JarStored, "HTTP/2 only, URL not encoded"},
		{"cookies neither sent nor stored", projectfile.RequestSettings{SendCookies: &no, StoreCookies: &no},
			`HTTP/2.0 QUERY /q%20x/%5E/[a]?f={b} [{"a":1}] cookie=""`, "old", JarSkipped, "jar cookies not sent, response cookies not stored"},
		{"cookies explicitly on", projectfile.RequestSettings{SendCookies: &yes, StoreCookies: &yes},
			`HTTP/2.0 QUERY /q%20x/%5E/[a]?f={b} [{"a":1}] cookie="old=1"`, "old,fresh", JarStored, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jar := cookies.New(cookies.Options{})
			jar.StoreLines(u, []string{"old=1; Path=/"})
			src := httpSource(srv.URL + "/q x/^/[a]?f={b}")
			src.Request.Method = "QUERY"
			src.Request.Body = projectfile.Body{Mode: projectfile.BodyRaw, Type: "json", Text: `{"a":1}`}
			src.Request.Settings = tt.settings
			src.Request.Settings.VerifyTLS = &no
			f := newFixture(t, src, fixedJar{jar})
			id := f.send(t)
			if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
				t.Fatalf("done = %+v", done)
			}
			resp := f.rec.response(t, id)
			if resp.Body != tt.body {
				t.Errorf("server saw %s\nwant           %s", resp.Body, tt.body)
			}
			var names []string
			for _, c := range jar.Cookies(u) {
				names = append(names, c.Name)
			}
			if got := strings.Join(names, ","); got != tt.jar {
				t.Errorf("jar = %s, want %s", got, tt.jar)
			}
			if len(resp.Cookies) != 1 || resp.Cookies[0].Jar != tt.cookieJar {
				t.Errorf("cookies = %+v, want jar state %q", resp.Cookies, tt.cookieJar)
			}
			var option string
			for _, e := range resp.Timeline.Events {
				if e.Name == httpclient.EventRequestOptions {
					option = e.Detail
				}
			}
			if option != tt.option {
				t.Errorf("request_options = %q, want %q", option, tt.option)
			}
		})
	}
}

// TestSendRedirectSettings: each redirect option of the request file,
// sent end to end. The POST with a bearer token is redirected with a 302
// from 127.0.0.1 to localhost (another origin) and then to /echo.
func TestSendRedirectSettings(t *testing.T) {
	t.Parallel()
	mux := nethttp.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	other := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	mux.HandleFunc("/start", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		nethttp.Redirect(w, r, other+"/next", nethttp.StatusFound)
	})
	mux.HandleFunc("/next", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		nethttp.Redirect(w, r, "/echo", nethttp.StatusFound)
	})
	mux.HandleFunc("/echo", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		b, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s %s auth=%q", r.Method, b, r.Header.Get("Authorization"))
	})

	yes, no, zero, one := true, false, 0, 1
	tests := []struct {
		name     string
		settings projectfile.RequestSettings
		status   int
		body     string // checked when not ""
		errCode  string
	}{
		{"defaults: followed, GET, Authorization stripped", projectfile.RequestSettings{}, 200, `GET  auth=""`, ""},
		{"not followed", projectfile.RequestSettings{FollowRedirects: &no}, 302, "", ""},
		{"maxRedirects 0 follows none", projectfile.RequestSettings{MaxRedirects: &zero}, 302, "", ""},
		{"maxRedirects 1 is too few", projectfile.RequestSettings{MaxRedirects: &one}, 0, "", httpclient.CodeTooManyRedirects},
		{"original method kept", projectfile.RequestSettings{FollowOriginalMethod: &yes}, 200, `POST data auth=""`, ""},
		{"Authorization forwarded", projectfile.RequestSettings{ForwardAuthorization: &yes}, 200, `GET  auth="Bearer tok"`, ""},
		{"both", projectfile.RequestSettings{FollowOriginalMethod: &yes, ForwardAuthorization: &yes}, 200, `POST data auth="Bearer tok"`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := httpSource(srv.URL + "/start")
			src.Request.Method = "POST"
			src.Request.Body = projectfile.Body{Mode: projectfile.BodyRaw, Type: "text", Text: "data"}
			src.Request.Settings = tt.settings
			src.Auth = projectfile.Auth{Type: projectfile.AuthBearer, Token: "tok"}
			f := newFixture(t, src, nil)
			id := f.send(t)
			done := f.rec.wait(t, id)
			if tt.errCode != "" {
				if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != tt.errCode {
					t.Fatalf("done = %+v, want %s", done, tt.errCode)
				}
				return
			}
			if done.Status != operation.StatusOK {
				t.Fatalf("done = %+v", done)
			}
			resp := f.rec.response(t, id)
			if resp.StatusCode != tt.status || (tt.body != "" && resp.Body != tt.body) {
				t.Errorf("response %d %q, want %d %q", resp.StatusCode, resp.Body, tt.status, tt.body)
			}
		})
	}
}
