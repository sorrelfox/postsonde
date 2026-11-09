package oauth2

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// implicitIDP is a test identity provider for the implicit grant: its
// /authorize sends the browser back to the redirect_uri with the token in
// the fragment (and an ID token with the request's nonce for "id_token
// token"), or with what fragment returns.
type implicitIDP struct {
	*httptest.Server
	now time.Time
	// fragment, when set, makes the fragment of the redirect from the
	// authorization request's parameters and the token it would send.
	fragment func(q url.Values, normal url.Values) url.Values

	mu         sync.Mutex
	authorized []url.Values
}

func newImplicitIDP(t *testing.T, now time.Time) *implicitIDP {
	idp := &implicitIDP{now: now}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		idp.mu.Lock()
		idp.authorized = append(idp.authorized, q)
		n := len(idp.authorized)
		edit := idp.fragment
		idp.mu.Unlock()
		access := fmt.Sprintf("implicit-access-%d", n)
		f := url.Values{"access_token": {access}, "token_type": {"Bearer"}, "expires_in": {"900"}, "state": {q.Get("state")}}
		if q.Get("response_type") == "id_token token" {
			c := idClaims(idp.now, q.Get("nonce"))
			c["aud"] = q.Get("client_id")
			at, _ := AccessTokenHash("RS256", access)
			c["at_hash"] = at
			f.Set("id_token", makeIDToken("RS256", c))
		}
		if q.Get("client_id") != "spa" || (q.Get("response_type") != "token" && q.Get("response_type") != "id_token token") {
			f = url.Values{"error": {"unsupported_response_type"}, "state": {q.Get("state")}}
		}
		if edit != nil {
			f = edit(q, f)
		}
		http.Redirect(w, r, q.Get("redirect_uri")+"#"+f.Encode(), http.StatusFound)
	})
	idp.Server = httptest.NewServer(mux)
	t.Cleanup(idp.Close)
	return idp
}

// fragmentBrowser stands in for the system browser on the implicit
// grant: it follows the redirects to the loopback listener, checks that
// the page's CSP allows exactly its one inline script, and does what the
// script does: posts the fragment back from the page's origin.
type fragmentBrowser struct {
	mu     sync.Mutex
	opened []string
	// results are the status and text of the post, or of the page when
	// the listener answered the redirect itself.
	results []string
	errs    []string
	done    chan struct{}
	// origin, when set, is the Origin the post claims.
	origin string
	// before, when set, runs before the post, with the listener's URL.
	before func(callback *url.URL)
}

func newFragmentBrowser() *fragmentBrowser { return &fragmentBrowser{done: make(chan struct{}, 4)} }

var scriptRe = regexp.MustCompile(`<script>(.*)</script>`)

func (b *fragmentBrowser) open(u string) error {
	b.mu.Lock()
	b.opened = append(b.opened, u)
	b.mu.Unlock()
	go func() {
		defer func() { b.done <- struct{}{} }()
		fail := func(msg string) {
			b.mu.Lock()
			b.errs = append(b.errs, msg)
			b.mu.Unlock()
		}
		resp, err := http.Get(u)
		if err != nil {
			fail(err.Error())
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		final := resp.Request.URL
		m := scriptRe.FindSubmatch(body)
		if m == nil {
			b.mu.Lock()
			b.results = append(b.results, fmt.Sprintf("page %d %s", resp.StatusCode, body))
			b.mu.Unlock()
			return
		}
		sum := sha256.Sum256(m[1])
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"';") ||
			!strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "connect-src 'self'") || strings.Contains(csp, "unsafe-eval") {
			fail("CSP " + csp)
			return
		}
		if final.Fragment == "" && final.RawFragment == "" {
			fail("no fragment in " + final.String())
		}
		callback := &url.URL{Scheme: "http", Host: final.Host, Path: final.Path}
		if b.before != nil {
			b.before(callback)
		}
		frag := final.EscapedFragment()
		req, _ := http.NewRequest(http.MethodPost, callback.String(), strings.NewReader(frag))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		origin := b.origin
		if origin == "" {
			origin = "http://" + final.Host
		}
		req.Header.Set("Origin", origin)
		pr, err := http.DefaultClient.Do(req)
		if err != nil {
			fail(err.Error())
			return
		}
		text, _ := io.ReadAll(pr.Body)
		pr.Body.Close()
		b.mu.Lock()
		b.results = append(b.results, fmt.Sprintf("%d %s", pr.StatusCode, text))
		b.mu.Unlock()
	}()
	return nil
}

func (b *fragmentBrowser) last(t *testing.T) string {
	t.Helper()
	select {
	case <-b.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the browser did not finish")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.errs) > 0 {
		t.Fatalf("browser: %v", b.errs)
	}
	if len(b.results) == 0 {
		t.Fatal("the browser got no answer")
	}
	return b.results[len(b.results)-1]
}

func (b *fragmentBrowser) redirectURI(t *testing.T) *url.URL {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	u, _ := url.Parse(b.opened[0])
	r, err := url.Parse(u.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func implicitConfig(idp *implicitIDP) Config {
	return Config{Grant: GrantImplicit, AuthURL: idp.URL + "/authorize", ClientID: "spa", Scope: "openid profile"}
}

// The implicit grant gets its token from a test
// identity provider: the fragment is posted back by the page, the state
// and the ID token's nonce are checked, and the listener is closed.
func TestAuthorizeImplicit(t *testing.T) {
	t.Parallel()
	now := time.Now().Truncate(time.Second)
	idp := newImplicitIDP(t, now)
	b := newFragmentBrowser()
	got, err := AuthorizeImplicit(t.Context(), implicitConfig(idp), AuthorizeOptions{Open: b.open, Timeout: 10 * time.Second, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if res := b.last(t); !strings.HasPrefix(res, "200 PostSonde got the token.") {
		t.Errorf("page = %s", res)
	}
	tok := got.Token
	if tok.AccessToken != "implicit-access-1" || tok.TokenType != "Bearer" || tok.ExpiresIn != 900 || !tok.Expiry.Equal(now.Add(15*time.Minute)) ||
		tok.RefreshToken != "" || tok.IDToken == "" {
		t.Errorf("token = %+v", tok)
	}
	redirect := b.redirectURI(t)
	if got.RedirectURI != redirect.String() || redirect.Hostname() != "127.0.0.1" || redirect.Path != "/callback" {
		t.Errorf("redirect = %s, %+v", redirect, got)
	}
	assertClosed(t, redirect)
	idp.mu.Lock()
	q := idp.authorized[0]
	idp.mu.Unlock()
	if q.Get("response_type") != "id_token token" || q.Get("nonce") == "" || q.Get("nonce") != got.Nonce || q.Get("code_challenge") != "" ||
		len(q.Get("state")) < 20 || q.Get("scope") != "openid profile" {
		t.Errorf("authorization request = %v", q)
	}

	// Without openid: response_type token, still a nonce, no ID token.
	cfg := implicitConfig(idp)
	cfg.Scope = "orders"
	b = newFragmentBrowser()
	got, err = AuthorizeImplicit(t.Context(), cfg, AuthorizeOptions{Open: b.open, Timeout: 10 * time.Second})
	b.last(t)
	idp.mu.Lock()
	q = idp.authorized[1]
	idp.mu.Unlock()
	if err != nil || got.Token.AccessToken != "implicit-access-2" || got.Token.IDToken != "" || q.Get("response_type") != "token" || q.Get("nonce") == "" {
		t.Errorf("token = %+v, %v; request %v", got, err, q)
	}
}

// A fragment with another state is refused, and so is
// an ID token with another nonce; an error in the fragment is the
// server's; a fragment without a token is an error. The token of a
// refused redirect is not returned.
func TestAuthorizeImplicitRefused(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tests := []struct {
		name     string
		fragment func(q, f url.Values) url.Values
		code     string
		kind     errs.Kind
	}{
		{
			name:     "state mismatch",
			fragment: func(_, f url.Values) url.Values { f.Set("state", "forged"); return f },
			code:     CodeStateMismatch, kind: errs.PermissionDenied,
		},
		{
			name:     "no state",
			fragment: func(_, f url.Values) url.Values { f.Del("state"); return f },
			code:     CodeStateMismatch, kind: errs.PermissionDenied,
		},
		{
			name: "nonce mismatch",
			fragment: func(q, f url.Values) url.Values {
				c := idClaims(now, "another-sign-in")
				c["aud"] = "spa"
				f.Set("id_token", makeIDToken("RS256", c))
				return f
			},
			code: CodeIDTokenNonce, kind: errs.PermissionDenied,
		},
		{
			name: "ID token for another client",
			fragment: func(q, f url.Values) url.Values {
				c := idClaims(now, q.Get("nonce"))
				c["aud"] = "other"
				f.Set("id_token", makeIDToken("RS256", c))
				return f
			},
			code: CodeIDTokenAudience, kind: errs.PermissionDenied,
		},
		{
			name: "swapped access token",
			fragment: func(_, f url.Values) url.Values {
				f.Set("access_token", "swapped")
				return f
			},
			code: CodeIDTokenAtHash, kind: errs.PermissionDenied,
		},
		{
			name: "access denied",
			fragment: func(q, _ url.Values) url.Values {
				return url.Values{"error": {"access_denied"}, "state": {q.Get("state")}}
			},
			code: CodeAccessDenied, kind: errs.PermissionDenied,
		},
		{
			name:     "no access token",
			fragment: func(_, f url.Values) url.Values { f.Del("access_token"); return f },
			code:     CodeAuthorizationError, kind: errs.Protocol,
		},
		{
			name:     "bad expires_in",
			fragment: func(_, f url.Values) url.Values { f.Set("expires_in", "soon"); return f },
			code:     CodeMalformedResponse, kind: errs.Parse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			idp := newImplicitIDP(t, now)
			idp.fragment = tt.fragment
			b := newFragmentBrowser()
			got, err := AuthorizeImplicit(t.Context(), implicitConfig(idp), AuthorizeOptions{Open: b.open, Timeout: 10 * time.Second})
			var e *errs.Error
			if !errors.As(err, &e) || e.Code != tt.code || e.Kind != tt.kind || got.Token.AccessToken != "" {
				t.Fatalf("got %+v, err = %#v", got, err)
			}
			if res := b.last(t); !strings.HasPrefix(res, "400 ") || strings.Contains(res, "implicit-access") {
				t.Errorf("page = %s", res)
			}
			assertClosed(t, b.redirectURI(t))
		})
	}
}

// Only the page on the listener's own origin can post the fragment: a
// post from another origin (a web page guessing the port) is refused and
// does not end the wait; a redirect with the answer in the query (an
// error some servers send so) is read too.
func TestAuthorizeImplicitOrigin(t *testing.T) {
	t.Parallel()
	now := time.Now()
	idp := newImplicitIDP(t, now)
	b := newFragmentBrowser()
	var forged []int
	b.before = func(callback *url.URL) {
		for _, h := range []map[string]string{
			{"Origin": "https://evil.example", "Content-Type": FormType},
			{"Content-Type": FormType},
			{"Origin": "http://" + callback.Host, "Content-Type": "text/plain"},
		} {
			req, _ := http.NewRequest(http.MethodPost, callback.String(), strings.NewReader("access_token=evil&state=x"))
			for k, v := range h {
				req.Header.Set(k, v)
			}
			if resp, err := http.DefaultClient.Do(req); err == nil {
				forged = append(forged, resp.StatusCode)
				resp.Body.Close()
			}
		}
	}
	got, err := AuthorizeImplicit(t.Context(), implicitConfig(idp), AuthorizeOptions{Open: b.open, Timeout: 10 * time.Second})
	if err != nil || got.Token.AccessToken != "implicit-access-1" {
		t.Fatalf("got %+v, %v", got, err)
	}
	b.last(t)
	if fmt.Sprint(forged) != "[403 403 415]" {
		t.Errorf("forged posts = %v", forged)
	}

	// The answer in the query.
	idp2 := newImplicitIDP(t, now)
	query := func(u string) error {
		p, _ := url.Parse(u)
		q := p.Query()
		go func() {
			if resp, err := http.Get(q.Get("redirect_uri") + "?error=server_error&state=" + url.QueryEscape(q.Get("state"))); err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	if _, err := AuthorizeImplicit(t.Context(), implicitConfig(idp2), AuthorizeOptions{Open: query, Timeout: 10 * time.Second}); codeOf(err) != CodeAuthorizationError {
		t.Errorf("query answer: %v", err)
	}
}

// The implicit grant has no token request, and runs without a token URL.
func TestImplicitGrantParams(t *testing.T) {
	t.Parallel()
	r, err := NewTokenRequest(Config{Grant: GrantImplicit, AuthURL: "https://idp/a", ClientID: "c", ClientSecret: "s"})
	if err != nil || len(r.Grant) != 1 || HasTokenRequest(GrantImplicit) || !HasTokenRequest(GrantPassword) {
		t.Fatalf("request = %+v, %v", r, err)
	}
	u, err := AuthorizationURL(Config{Grant: GrantImplicit, AuthURL: "https://idp/a", ClientID: "c", Scope: "openid", Nonce: "n"}, "http://127.0.0.1:1/callback", "st", "")
	if err != nil || u != "https://idp/a?response_type=id_token+token&client_id=c&redirect_uri=http%3A%2F%2F127.0.0.1%3A1%2Fcallback&scope=openid&state=st&nonce=n" {
		t.Errorf("URL = %s, %v", u, err)
	}
}
