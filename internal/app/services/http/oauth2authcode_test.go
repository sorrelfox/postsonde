package http

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/oauth2"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// signInIDP is a test identity provider for the authorization code grant
// with PKCE, for a public client "desk": /authorize sends the browser back
// to the redirect_uri with a code (or what answer says), /token redeems a
// code once, checking the redirect_uri and the S256 verifier, and
// refreshes its refresh tokens. Access tokens are "user-token-<n>".
type signInIDP struct {
	*httptest.Server
	// answer, when set, is the query of the redirect instead of a code.
	answer func(state string) string

	mu       sync.Mutex
	n        int
	codes    map[string]signInCode
	refresh  map[string]bool
	access   map[string]bool
	grants   []string
	requests []url.Values
}

type signInCode struct{ redirectURI, challenge string }

func newSignInIDP(t *testing.T) *signInIDP {
	idp := &signInIDP{codes: map[string]signInCode{}, refresh: map[string]bool{}, access: map[string]bool{}}
	mux := nethttp.NewServeMux()
	mux.HandleFunc("/authorize", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		q := r.URL.Query()
		idp.mu.Lock()
		idp.requests = append(idp.requests, q)
		idp.n++
		code := fmt.Sprintf("auth-code-%d", idp.n)
		idp.codes[code] = signInCode{redirectURI: q.Get("redirect_uri"), challenge: q.Get("code_challenge")}
		answer := idp.answer
		idp.mu.Unlock()
		query := "code=" + code + "&state=" + url.QueryEscape(q.Get("state"))
		if answer != nil {
			query = answer(q.Get("state"))
		}
		nethttp.Redirect(w, r, q.Get("redirect_uri")+"?"+query, nethttp.StatusFound)
	})
	mux.HandleFunc("/token", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		_ = r.ParseForm()
		f := r.PostForm
		idp.mu.Lock()
		defer idp.mu.Unlock()
		idp.grants = append(idp.grants, f.Get("grant_type"))
		w.Header().Set("Content-Type", "application/json")
		fail := func(code string) {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
		}
		if f.Get("client_id") != "desk" || r.Header.Get("Authorization") != "" {
			fail("invalid_client")
			return
		}
		switch f.Get("grant_type") {
		case "authorization_code":
			c, ok := idp.codes[f.Get("code")]
			delete(idp.codes, f.Get("code"))
			sum := sha256.Sum256([]byte(f.Get("code_verifier")))
			if !ok || c.redirectURI != f.Get("redirect_uri") || base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
				fail("invalid_grant")
				return
			}
		case "refresh_token":
			if !idp.refresh[f.Get("refresh_token")] {
				fail("invalid_grant")
				return
			}
		default:
			fail("unsupported_grant_type")
			return
		}
		idp.n++
		access, refresh := fmt.Sprintf("user-token-%d-abcdef", idp.n), fmt.Sprintf("user-refresh-%d", idp.n)
		idp.access[access], idp.refresh[refresh] = true, true
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 600, "refresh_token": refresh})
	})
	idp.Server = httptest.NewServer(mux)
	t.Cleanup(idp.Close)
	return idp
}

func (idp *signInIDP) grantsSent() []string {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	return slices.Clone(idp.grants)
}

// testBrowser stands in for the system browser: it follows the
// authorization URL and the redirect back to PostSonde's listener, as a
// user who signs in at once would. It never opens a real browser.
type testBrowser struct {
	mu     sync.Mutex
	opened []string
	pages  []string
	// idle, when set, opens nothing: nobody signs in.
	idle bool
	wg   sync.WaitGroup
}

func (b *testBrowser) open(u string) error {
	b.mu.Lock()
	b.opened = append(b.opened, u)
	idle := b.idle
	b.mu.Unlock()
	if idle {
		return nil
	}
	b.wg.Go(func() {
		resp, err := nethttp.Get(u)
		if err != nil {
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		b.mu.Lock()
		b.pages = append(b.pages, fmt.Sprintf("%d %s", resp.StatusCode, body))
		b.mu.Unlock()
	})
	return nil
}

func (b *testBrowser) openedURLs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.opened)
}

// redirectHost returns the host:port of the redirect_uri of the i-th
// authorization URL opened.
func (b *testBrowser) redirectHost(t *testing.T, i int) string {
	t.Helper()
	opened := b.openedURLs()
	if len(opened) <= i {
		t.Fatalf("opened %d URLs", len(opened))
	}
	u, _ := url.Parse(opened[i])
	r, _ := url.Parse(u.Query().Get("redirect_uri"))
	return r.Host
}

func assertListenerClosed(t *testing.T, host string) {
	t.Helper()
	if c, err := net.DialTimeout("tcp", host, time.Second); err == nil {
		c.Close()
		t.Errorf("the redirect listener %s is still open", host)
	}
}

func signInSource(api, idp string) projectfile.Auth {
	return projectfile.Auth{Type: projectfile.AuthOAuth2, GrantType: projectfile.OAuth2AuthorizationCode,
		AuthURL: idp + "/authorize", TokenURL: idp + "/token", ClientID: "desk", Scope: "openid orders"}
}

// signInAPI answers "ok|<token>" to a token the IdP issued, 401 otherwise.
func signInAPI(t *testing.T, idp *signInIDP) *httptest.Server {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		idp.mu.Lock()
		ok := idp.access[tok]
		idp.mu.Unlock()
		if !ok {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, "ok|"+tok)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func signInFixture(t *testing.T, idp *signInIDP, api string, b *testBrowser, opts ...Option) *fixture {
	src := httpSource(api + "/me")
	src.Auth = signInSource(api, idp.URL)
	return newFixture(t, src, nil, append([]Option{WithOAuth2Browser(b.open)}, opts...)...)
}

// A send with the authorization code grant signs in in
// the (test) browser, gets the code on the loopback listener, redeems it
// with the PKCE verifier and sends the token; the listener is closed
// after, the code and verifier are masked, the next send uses the stored
// token and, once it expires, refreshes it without a browser.
func TestSendOAuth2AuthorizationCode(t *testing.T) {
	t.Parallel()
	idp := newSignInIDP(t)
	api := signInAPI(t, idp)
	b := &testBrowser{}
	clock := newClock()
	f := signInFixture(t, idp, api.URL, b)
	f.svc.now = clock.Now

	done, id := sendOp(t, f)
	resp := f.rec.response(t, id)
	if done.Status != operation.StatusOK || !strings.HasPrefix(resp.Body, "ok|user-token-2-") {
		t.Fatalf("send: %+v %s", done, resp.Body)
	}
	b.wg.Wait()
	if len(b.pages) != 1 || !strings.HasPrefix(b.pages[0], "200 ") || !strings.Contains(b.pages[0], "Signed in") {
		t.Errorf("browser pages = %q", b.pages)
	}
	host := b.redirectHost(t, 0)
	if !strings.HasPrefix(host, "127.0.0.1:") {
		t.Errorf("redirect host = %s", host)
	}
	assertListenerClosed(t, host)
	if p := f.rec.progressOf(id); !slices.Contains(p, StageSignIn) || !slices.Contains(p, StageToken) {
		t.Errorf("progress = %v", p)
	}
	tl := resp.Timeline
	if len(tl.Hops) != 2 || tl.Hops[0].Kind != HopToken {
		t.Fatalf("hops = %+v", tl.Hops)
	}
	body := tl.Hops[0].Request.Body
	if !strings.HasPrefix(body, "grant_type=authorization_code&code="+httpclient.Mask+"&redirect_uri=http%3A%2F%2F127.0.0.1%3A") ||
		!strings.HasSuffix(body, "&code_verifier="+httpclient.Mask+"&client_id=desk") {
		t.Errorf("token request body = %s", body)
	}
	if !hasEvent(tl, EventOAuth2Authorized, "signed in in the browser, redirected to http://"+host+"/callback") ||
		!hasEvent(tl, EventOAuth2Token, "authorization_code from "+idp.URL+"/token: 200, expires in 600 s") {
		t.Errorf("events = %+v", tl.Events)
	}
	noSecrets(t, "timeline", tl, "auth-code-1", "user-refresh-2")

	// Stored: no browser.
	if done, id = sendOp(t, f); !strings.HasPrefix(f.rec.response(t, id).Body, "ok|user-token-2-") || len(b.openedURLs()) != 1 {
		t.Fatalf("second send: %+v, opened %d", done, len(b.openedURLs()))
	}
	// Expired: refreshed, still no browser.
	clock.add(10 * time.Minute)
	if done, id = sendOp(t, f); !strings.HasPrefix(f.rec.response(t, id).Body, "ok|user-token-3-") || len(b.openedURLs()) != 1 {
		t.Fatalf("refreshing send: %+v %s, opened %d", done, f.rec.response(t, id).Body, len(b.openedURLs()))
	}
	if got := idp.grantsSent(); !slices.Equal(got, []string{"authorization_code", "refresh_token"}) {
		t.Errorf("grants = %v", got)
	}

	// The authorization request: the challenge of S256, the state, the
	// scope, the client.
	idp.mu.Lock()
	q := idp.requests[0]
	idp.mu.Unlock()
	if q.Get("response_type") != "code" || q.Get("client_id") != "desk" || q.Get("code_challenge_method") != "S256" ||
		len(q.Get("code_challenge")) != 43 || len(q.Get("state")) < 20 || q.Get("scope") != "openid orders" {
		t.Errorf("authorization request = %v", q)
	}
}

// A redirect with a wrong state is rejected; so is a
// sign-in the user declined. Nothing is sent to the API, nothing stored,
// and the listener is closed.
func TestSendOAuth2AuthorizationCodeRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		answer func(state string) string
		code   string
		kind   errs.Kind
	}{
		{name: "wrong state", answer: func(string) string { return "code=injected&state=not-the-one" }, code: oauth2.CodeStateMismatch, kind: errs.PermissionDenied},
		{name: "access denied", answer: func(s string) string { return "error=access_denied&state=" + url.QueryEscape(s) }, code: oauth2.CodeAccessDenied, kind: errs.PermissionDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			idp := newSignInIDP(t)
			idp.answer = tt.answer
			api := signInAPI(t, idp)
			b := &testBrowser{}
			f := signInFixture(t, idp, api.URL, b)
			done, _ := sendOp(t, f)
			if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != tt.code || done.Error.Kind != string(tt.kind) {
				t.Fatalf("done = %+v, error %+v", done, done.Error)
			}
			if got := idp.grantsSent(); len(got) != 0 {
				t.Errorf("the token endpoint was asked: %v", got)
			}
			b.wg.Wait()
			if len(b.pages) != 1 || !strings.HasPrefix(b.pages[0], "400 ") {
				t.Errorf("browser pages = %q", b.pages)
			}
			assertListenerClosed(t, b.redirectHost(t, 0))
			if st, _ := f.svc.OAuth2Token(t.Context(), OAuth2TokenInputDTO{RequestID: "req_1"}); st.Status != OAuth2TokenNone {
				t.Errorf("stored = %+v", st)
			}
		})
	}
}

// Nobody signs in: the sign-in times out, or is cancelled from the Auth
// tab; the listener is closed either way.
func TestOAuth2AuthorizationCodeTimeoutAndCancel(t *testing.T) {
	t.Parallel()
	idp := newSignInIDP(t)
	api := signInAPI(t, idp)

	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		b := &testBrowser{idle: true}
		f := signInFixture(t, idp, api.URL, b, WithOAuth2AuthorizeTimeout(100*time.Millisecond))
		done, _ := sendOp(t, f)
		if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != oauth2.CodeAuthorizationTimeout {
			t.Fatalf("done = %+v, error %+v", done, done.Error)
		}
		assertListenerClosed(t, b.redirectHost(t, 0))
	})
	t.Run("cancel", func(t *testing.T) {
		t.Parallel()
		b := &testBrowser{idle: true}
		f := signInFixture(t, idp, api.URL, b)
		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			for len(b.openedURLs()) == 0 {
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
		}()
		start := time.Now()
		_, err := f.svc.OAuth2GetToken(ctx, OAuth2GetTokenInputDTO{RequestID: "req_1"})
		if errs.KindOf(err) != errs.Cancelled || time.Since(start) > 10*time.Second {
			t.Fatalf("err = %v", err)
		}
		assertListenerClosed(t, b.redirectHost(t, 0))
	})
}

// Get new token from the Auth tab signs in the same way; sends that need
// the token meanwhile wait for it rather than opening a second browser
// (run with -race).
func TestOAuth2AuthorizationCodeOneSignIn(t *testing.T) {
	t.Parallel()
	idp := newSignInIDP(t)
	api := signInAPI(t, idp)
	b := &testBrowser{}
	f := signInFixture(t, idp, api.URL, b)
	ids := make([]string, 3)
	for i := range ids {
		ids[i] = f.send(t)
	}
	for _, id := range ids {
		if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
			t.Fatalf("done = %+v", done)
		}
	}
	if n := len(b.openedURLs()); n != 1 {
		t.Errorf("the browser was opened %d times", n)
	}
	st, err := f.svc.OAuth2GetToken(t.Context(), OAuth2GetTokenInputDTO{RequestID: "req_1"})
	if err != nil || st.Status != OAuth2TokenValid || st.Grant != "authorization_code" || !st.Refreshable || len(b.openedURLs()) != 2 {
		t.Fatalf("get new token = %+v, %v", st, err)
	}
	b.wg.Wait()
}

// Without a browser (a service built without WithOAuth2Browser) the grant
// fails and says why.
func TestOAuth2AuthorizationCodeWithoutBrowser(t *testing.T) {
	t.Parallel()
	idp := newSignInIDP(t)
	src := httpSource("https://api.test/me")
	src.Auth = signInSource("", idp.URL)
	f := newFixture(t, src, nil)
	_, err := f.svc.OAuth2GetToken(t.Context(), OAuth2GetTokenInputDTO{RequestID: "req_1"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != oauth2.CodeBrowserFailed {
		t.Fatalf("err = %v", err)
	}
}
