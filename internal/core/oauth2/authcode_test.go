package oauth2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// RFC 7636 appendix B: the S256 challenge of its example verifier.
func TestPKCE(t *testing.T) {
	t.Parallel()
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("Challenge = %s", got)
	}
	seen := map[string]bool{}
	for range 50 {
		v, err := NewVerifier(nil)
		if err != nil {
			t.Fatal(err)
		}
		// 43 to 128 characters of [A-Za-z0-9-._~] (section 4.1).
		if len(v) != 43 || strings.Trim(v, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~") != "" || seen[v] {
			t.Fatalf("verifier %q", v)
		}
		seen[v] = true
	}
	v, _ := NewVerifier(bytes.NewReader(make([]byte, 32)))
	if v != strings.Repeat("A", 43) {
		t.Errorf("verifier from zeros = %s", v)
	}
	if _, err := NewState(bytes.NewReader(nil)); err == nil {
		t.Error("NewState without randomness did not fail")
	}
}

func TestParseRedirectURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want Redirect
		bad  bool
	}{
		{in: "", want: Redirect{Host: "127.0.0.1", Path: "/callback"}},
		{in: "http://127.0.0.1:8765/callback", want: Redirect{Host: "127.0.0.1", Port: 8765, Path: "/callback"}},
		{in: "http://127.0.0.1", want: Redirect{Host: "127.0.0.1", Path: "/callback"}},
		{in: "http://localhost:9000/oauth/cb", want: Redirect{Host: "localhost", Port: 9000, Path: "/oauth/cb"}},
		{in: "http://LOCALHOST/", want: Redirect{Host: "localhost", Path: "/callback"}},
		{in: "http://[::1]:7000/cb", want: Redirect{Host: "[::1]", Port: 7000, Path: "/cb"}},
		{in: "http://127.1.2.3:0/x", want: Redirect{Host: "127.1.2.3", Path: "/x"}},
		{in: "https://127.0.0.1/callback", bad: true},
		{in: "http://192.168.1.10/callback", bad: true},
		{in: "http://0.0.0.0/callback", bad: true},
		{in: "http://example.com/callback", bad: true},
		{in: "https://oauth.pstmn.io/v1/callback", bad: true},
		{in: "http://127.0.0.1/cb?x=1", bad: true},
		{in: "http://user@127.0.0.1/cb", bad: true},
		{in: "http://127.0.0.1:99999/cb", bad: true},
		{in: "myapp://callback", bad: true},
	}
	for _, tt := range tests {
		got, err := ParseRedirectURL(tt.in)
		if tt.bad {
			if codeOf(err) != CodeInvalidRedirectURL || errs.KindOf(err) != errs.InvalidArgument {
				t.Errorf("%q: err = %v", tt.in, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("%q = %+v, %v; want %+v", tt.in, got, err, tt.want)
		}
	}
}

func TestAuthorizationURL(t *testing.T) {
	t.Parallel()
	c := Config{AuthURL: "https://idp.test/authorize?tenant=t1#frag", ClientID: "my app", Scope: "openid orders", Audience: "https://api"}
	got, err := AuthorizationURL(c, "http://127.0.0.1:5000/callback", "st", "ch")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://idp.test/authorize?tenant=t1&response_type=code&client_id=my+app&redirect_uri=http%3A%2F%2F127.0.0.1%3A5000%2Fcallback" +
		"&scope=openid+orders&state=st&code_challenge=ch&code_challenge_method=S256&audience=https%3A%2F%2Fapi"
	if got != want {
		t.Errorf("URL = %s\nwant  %s", got, want)
	}
	for _, bad := range []string{"", "idp.test/authorize", "ftp://idp/a"} {
		if _, err := AuthorizationURL(Config{AuthURL: bad}, "", "", ""); codeOf(err) != CodeInvalidAuthURL {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

// codeIDP is a test identity provider for the authorization code grant
// with PKCE: /authorize remembers the challenge and redirects the browser
// back with a code (or what redirect says), /token redeems a code once
// for a public client, checking the redirect_uri and the verifier.
type codeIDP struct {
	*httptest.Server
	// redirect, when set, makes /authorize redirect to the URL it returns
	// for the request's redirect_uri and state.
	redirect func(redirectURI, state string) string

	mu         sync.Mutex
	codes      map[string]codeGrant
	authorized []url.Values
	tokenForms []url.Values
}

type codeGrant struct{ redirectURI, challenge string }

func newCodeIDP(t *testing.T) *codeIDP {
	idp := &codeIDP{codes: map[string]codeGrant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		idp.mu.Lock()
		idp.authorized = append(idp.authorized, q)
		redirect := idp.redirect
		code := fmt.Sprintf("code-%d", len(idp.authorized))
		idp.codes[code] = codeGrant{redirectURI: q.Get("redirect_uri"), challenge: q.Get("code_challenge")}
		idp.mu.Unlock()
		target := q.Get("redirect_uri") + "?code=" + code + "&state=" + url.QueryEscape(q.Get("state"))
		if redirect != nil {
			target = redirect(q.Get("redirect_uri"), q.Get("state"))
		}
		if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "cli" {
			target = q.Get("redirect_uri") + "?error=invalid_request&state=" + url.QueryEscape(q.Get("state"))
		}
		http.Redirect(w, r, target, http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		idp.mu.Lock()
		defer idp.mu.Unlock()
		idp.tokenForms = append(idp.tokenForms, r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		g, ok := idp.codes[r.PostForm.Get("code")]
		delete(idp.codes, r.PostForm.Get("code"))
		if !ok || r.PostForm.Get("grant_type") != GrantAuthorizationCode || r.PostForm.Get("client_id") != "cli" ||
			r.PostForm.Get("redirect_uri") != g.redirectURI || Challenge(r.PostForm.Get("code_verifier")) != g.challenge || r.Header.Get("Authorization") != "" {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		fmt.Fprint(w, `{"access_token":"user-token","token_type":"Bearer","expires_in":300,"refresh_token":"user-refresh"}`)
	})
	idp.Server = httptest.NewServer(mux)
	t.Cleanup(idp.Close)
	return idp
}

// browser stands in for the system browser: it opens the URL and
// follows the redirects, and keeps the last page it got.
type browser struct {
	mu     sync.Mutex
	opened []string
	pages  []page
	done   chan struct{}
	// edit, when set, changes the URL of a redirect before it is followed.
	edit func(*url.URL)
}

type page struct {
	status int
	body   string
}

func newBrowser() *browser { return &browser{done: make(chan struct{}, 4)} }

func (b *browser) open(u string) error {
	b.mu.Lock()
	b.opened = append(b.opened, u)
	b.mu.Unlock()
	client := &http.Client{CheckRedirect: func(r *http.Request, _ []*http.Request) error {
		if b.edit != nil {
			b.edit(r.URL)
		}
		return nil
	}}
	go func() {
		defer func() { b.done <- struct{}{} }()
		resp, err := client.Get(u)
		if err != nil {
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		b.mu.Lock()
		b.pages = append(b.pages, page{resp.StatusCode, string(body)})
		b.mu.Unlock()
	}()
	return nil
}

func (b *browser) lastPage(t *testing.T) page {
	t.Helper()
	select {
	case <-b.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the browser did not finish")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pages) == 0 {
		t.Fatal("the browser got no page")
	}
	return b.pages[len(b.pages)-1]
}

// redirectURIOf returns the redirect_uri of the authorization URL the
// browser was asked to open.
func (b *browser) redirectURIOf(t *testing.T) *url.URL {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.opened) == 0 {
		t.Fatal("no URL was opened")
	}
	u, _ := url.Parse(b.opened[0])
	r, err := url.Parse(u.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// assertClosed fails when the redirect listener still accepts.
func assertClosed(t *testing.T, redirect *url.URL) {
	t.Helper()
	if c, err := net.DialTimeout("tcp", redirect.Host, time.Second); err == nil {
		c.Close()
		t.Errorf("the redirect listener %s is still open", redirect.Host)
	}
}

func codeConfig(idp *codeIDP) Config {
	return Config{Grant: GrantAuthorizationCode, AuthURL: idp.URL + "/authorize", TokenURL: idp.URL + "/token", ClientID: "cli", Scope: "openid"}
}

// The flow completes against a test identity provider:
// the browser signs in and is sent back to the loopback listener, the code
// is redeemed with the PKCE verifier, and the listener is closed after.
func TestAuthorizeAndRedeem(t *testing.T) {
	t.Parallel()
	idp := newCodeIDP(t)
	b := newBrowser()
	cfg := codeConfig(idp)
	code, err := Authorize(t.Context(), cfg, AuthorizeOptions{Open: b.open, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if p := b.lastPage(t); p.status != 200 || !strings.Contains(p.body, "Signed in") {
		t.Errorf("page = %+v", p)
	}
	redirect := b.redirectURIOf(t)
	if redirect.Hostname() != "127.0.0.1" || redirect.Port() == "0" || redirect.Path != "/callback" || code.RedirectURI != redirect.String() {
		t.Errorf("redirect_uri = %s, code = %+v", redirect, code)
	}
	assertClosed(t, redirect)
	idp.mu.Lock()
	q := idp.authorized[0]
	idp.mu.Unlock()
	if q.Get("code_challenge") != Challenge(code.Verifier) || q.Get("scope") != "openid" || len(q.Get("state")) < 20 {
		t.Errorf("authorization request = %v", q)
	}

	res, err := Exchange(t.Context(), client(), must(NewTokenRequest(cfg)).WithCode(code), httpclient.Settings{}, nil)
	if err != nil || res.Token.AccessToken != "user-token" || res.Token.RefreshToken != "user-refresh" {
		t.Fatalf("Exchange = %+v, %v", res, err)
	}
	// A public client: client_id in the form, no Authorization header.
	if body := string(res.Request.Body.(httpclient.RawBody).Data); !strings.HasPrefix(body, "grant_type=authorization_code&code=code-1&redirect_uri=") ||
		!strings.Contains(body, "&code_verifier="+code.Verifier+"&client_id=cli") {
		t.Errorf("token request = %s", body)
	}
	// A code is redeemed once.
	if _, err := Exchange(t.Context(), client(), must(NewTokenRequest(cfg)).WithCode(code), httpclient.Settings{}, nil); codeOf(err) != CodeTokenError {
		t.Errorf("second redeem = %v", err)
	}
}

func must(r TokenRequest, err error) TokenRequest {
	if err != nil {
		panic(err)
	}
	return r
}

// A redirect with another state is refused, and its
// code is not used; so is an error the user caused (access_denied).
func TestAuthorizeRefusedRedirects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		redirect func(redirectURI, state string) string
		edit     func(*url.URL)
		code     string
		kind     errs.Kind
		detail   string
	}{
		{
			name: "state mismatch",
			edit: func(u *url.URL) {
				if u.Hostname() == "127.0.0.1" {
					q := u.Query()
					q.Set("state", "forged")
					u.RawQuery = q.Encode()
				}
			},
			code: CodeStateMismatch, kind: errs.PermissionDenied,
		},
		{
			name:     "no state",
			redirect: func(r, _ string) string { return r + "?code=stolen" },
			code:     CodeStateMismatch, kind: errs.PermissionDenied,
		},
		{
			name: "access denied",
			redirect: func(r, s string) string {
				return r + "?error=access_denied&error_description=" + url.QueryEscape("The user said <no>") + "&state=" + url.QueryEscape(s)
			},
			code: CodeAccessDenied, kind: errs.PermissionDenied, detail: "access_denied",
		},
		{
			name:     "server error",
			redirect: func(r, s string) string { return r + "?error=server_error&state=" + url.QueryEscape(s) },
			code:     CodeAuthorizationError, kind: errs.Protocol, detail: "server_error",
		},
		{
			name:     "no code",
			redirect: func(r, s string) string { return r + "?state=" + url.QueryEscape(s) },
			code:     CodeAuthorizationError, kind: errs.Protocol,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			idp := newCodeIDP(t)
			idp.redirect = tt.redirect
			b := newBrowser()
			b.edit = tt.edit
			_, err := Authorize(t.Context(), codeConfig(idp), AuthorizeOptions{Open: b.open, Timeout: 10 * time.Second})
			var e *errs.Error
			if !errors.As(err, &e) || e.Code != tt.code || e.Kind != tt.kind || e.Details["error"] != tt.detail {
				t.Fatalf("err = %#v", err)
			}
			p := b.lastPage(t)
			if p.status != 400 || !strings.Contains(p.body, "Sign-in failed") || strings.Contains(p.body, "<no>") {
				t.Errorf("page = %+v", p)
			}
			assertClosed(t, b.redirectURIOf(t))
		})
	}
}

// A fixed port and path of the redirect URL are used as they are; other
// paths on the listener are 404 and do not end the wait.
func TestAuthorizeFixedRedirect(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	idp := newCodeIDP(t)
	cfg := codeConfig(idp)
	cfg.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/oauth/cb", port)
	cfg.State = "my-state"
	var stray int
	open := func(u string) error {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/favicon.ico", port))
		if err == nil {
			stray = resp.StatusCode
			resp.Body.Close()
		}
		return newBrowser().open(u)
	}
	code, err := Authorize(t.Context(), cfg, AuthorizeOptions{Open: open, Timeout: 10 * time.Second})
	if err != nil || code.RedirectURI != cfg.RedirectURL || stray != 404 {
		t.Fatalf("code = %+v, %v, stray %d", code, err, stray)
	}
	idp.mu.Lock()
	defer idp.mu.Unlock()
	if idp.authorized[0].Get("state") != "my-state" {
		t.Errorf("state = %s", idp.authorized[0].Get("state"))
	}
}

// Nobody signs in: the wait ends after the timeout, or when cancelled, and
// the listener is closed either way.
func TestAuthorizeTimeoutAndCancel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		timeout time.Duration
		cancel  bool
		kind    errs.Kind
		code    string
	}{
		{name: "timeout", timeout: 100 * time.Millisecond, kind: errs.Timeout, code: CodeAuthorizationTimeout},
		{name: "cancel", timeout: time.Minute, cancel: true, kind: errs.Cancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			idp := newCodeIDP(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var opened string
			open := func(u string) error {
				opened = u
				if tt.cancel {
					go func() {
						time.Sleep(50 * time.Millisecond)
						cancel()
					}()
				}
				return nil
			}
			start := time.Now()
			_, err := Authorize(ctx, codeConfig(idp), AuthorizeOptions{Open: open, Timeout: tt.timeout})
			if errs.KindOf(err) != tt.kind || codeOf(err) != tt.code || time.Since(start) > 5*time.Second {
				t.Fatalf("err = %v after %s", err, time.Since(start))
			}
			u, _ := url.Parse(opened)
			r, _ := url.Parse(u.Query().Get("redirect_uri"))
			assertClosed(t, r)
		})
	}
}

// Nothing is listened on or opened for a bad configuration; a browser that
// cannot be opened fails the sign-in and closes the listener.
func TestAuthorizeFailures(t *testing.T) {
	t.Parallel()
	idp := newCodeIDP(t)
	opened := 0
	count := func(string) error { opened++; return nil }
	cfg := codeConfig(idp)
	cfg.RedirectURL = "https://example.com/cb"
	if _, err := Authorize(t.Context(), cfg, AuthorizeOptions{Open: count}); codeOf(err) != CodeInvalidRedirectURL {
		t.Errorf("redirect: %v", err)
	}
	cfg = codeConfig(idp)
	cfg.AuthURL = ""
	if _, err := Authorize(t.Context(), cfg, AuthorizeOptions{Open: count}); codeOf(err) != CodeInvalidAuthURL {
		t.Errorf("auth URL: %v", err)
	}
	if _, err := Authorize(t.Context(), codeConfig(idp), AuthorizeOptions{}); codeOf(err) != CodeBrowserFailed {
		t.Errorf("no opener: %v", err)
	}
	if opened != 0 {
		t.Errorf("opened %d times", opened)
	}
	var redirect string
	fail := func(u string) error {
		p, _ := url.Parse(u)
		redirect = p.Query().Get("redirect_uri")
		return errors.New("no browser")
	}
	if _, err := Authorize(t.Context(), codeConfig(idp), AuthorizeOptions{Open: fail}); codeOf(err) != CodeBrowserFailed {
		t.Errorf("browser: %v", err)
	}
	r, _ := url.Parse(redirect)
	assertClosed(t, r)
}

// The authorization code grant's token request: the code, redirect_uri
// and verifier, and a key that does not depend on them.
func TestAuthorizationCodeGrantParams(t *testing.T) {
	t.Parallel()
	cfg := Config{Grant: GrantAuthorizationCode, TokenURL: "https://idp/t", AuthURL: "https://idp/a", ClientID: "c", ClientSecret: "s", Scope: "x"}
	r, err := NewTokenRequest(cfg)
	if err != nil || len(r.Grant) != 1 {
		t.Fatalf("before the code = %+v, %v", r, err)
	}
	withCode := r.WithCode(AuthorizationCode{Code: "c1", RedirectURI: "http://127.0.0.1:1/callback", Verifier: "v"})
	if got := Encode(withCode.Form()); got != "grant_type=authorization_code&code=c1&redirect_uri=http%3A%2F%2F127.0.0.1%3A1%2Fcallback&code_verifier=v" {
		t.Errorf("form = %s", got)
	}
	if withCode.Key() != r.Key() {
		t.Error("the code changed the key")
	}
	other := cfg
	other.AuthURL = "https://idp/other"
	if must(NewTokenRequest(other)).Key() == r.Key() {
		t.Error("another authorization URL has the same key")
	}
	other = cfg
	other.RedirectURL = "http://127.0.0.1:9/cb"
	if must(NewTokenRequest(other)).Key() == r.Key() {
		t.Error("another redirect URL has the same key")
	}
}

// With openid in the scope the authorization code grant sends a nonce,
// which the token request carries on (WithCode) for the ID token's check;
// without it none is sent.
func TestAuthorizeNonce(t *testing.T) {
	t.Parallel()
	idp := newCodeIDP(t)
	b := newBrowser()
	code, err := Authorize(t.Context(), codeConfig(idp), AuthorizeOptions{Open: b.open, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	b.lastPage(t)
	idp.mu.Lock()
	q := idp.authorized[0]
	idp.mu.Unlock()
	if code.Nonce == "" || q.Get("nonce") != code.Nonce {
		t.Errorf("nonce = %q, sent %q", code.Nonce, q.Get("nonce"))
	}
	req := must(NewTokenRequest(codeConfig(idp))).WithCode(code)
	if req.Config.Nonce != code.Nonce {
		t.Errorf("token request nonce = %q", req.Config.Nonce)
	}
	now := time.Now()
	c := idClaims(now, "other")
	c["aud"] = "cli"
	if _, err := CheckIDToken(req.Config, Token{IDToken: makeIDToken("RS256", c)}, now); codeOf(err) != CodeIDTokenNonce {
		t.Errorf("wrong nonce: %v", err)
	}

	cfg := codeConfig(idp)
	cfg.Scope = "orders"
	b = newBrowser()
	code, err = Authorize(t.Context(), cfg, AuthorizeOptions{Open: b.open, Timeout: 10 * time.Second})
	b.lastPage(t)
	idp.mu.Lock()
	q = idp.authorized[1]
	idp.mu.Unlock()
	if err != nil || code.Nonce != "" || q.Has("nonce") {
		t.Errorf("without openid: %+v, %v, %v", code, err, q)
	}
}
