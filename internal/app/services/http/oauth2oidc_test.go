package http

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
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

const (
	oidcClient   = "desk-app"
	oidcUser     = "ada"
	oidcPassword = "pa55 w0rd&more"
)

// oidcIDP is a test OpenID Connect provider for the password grant
// (/token) and the implicit grant (/authorize, the token in the redirect's
// fragment), issuing access tokens "oidc-access-<n>-xyz" and ID tokens for
// oidcClient that the API accepts too. Its clock is the test's. claims
// edits the claims of the ID tokens it issues; answer the fragment.
type oidcIDP struct {
	*httptest.Server
	clock *testClock

	mu      sync.Mutex
	n       int
	issued  map[string]bool
	forms   []url.Values
	claims  func(c map[string]any)
	answer  func(q, f url.Values) url.Values
	noIDTok bool
}

func newOIDCIDP(t *testing.T, clock *testClock) *oidcIDP {
	idp := &oidcIDP{clock: clock, issued: map[string]bool{}}
	mux := nethttp.NewServeMux()
	mux.HandleFunc("/token", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		_ = r.ParseForm()
		f := r.PostForm
		idp.mu.Lock()
		idp.forms = append(idp.forms, f)
		idp.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if f.Get("grant_type") != "password" || f.Get("client_id") != oidcClient {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"unsupported_grant_type"}`)
			return
		}
		if f.Get("username") != oidcUser || f.Get("password") != oidcPassword {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Invalid user credentials"}`)
			return
		}
		access, id := idp.issue("", oauth2.HasOpenIDScope(f.Get("scope")))
		out := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600}
		if id != "" {
			out["id_token"] = id
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/authorize", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		q := r.URL.Query()
		access, id := idp.issue(q.Get("nonce"), q.Get("response_type") == "id_token token")
		f := url.Values{"access_token": {access}, "token_type": {"Bearer"}, "expires_in": {"3600"}, "state": {q.Get("state")}}
		if id != "" {
			f.Set("id_token", id)
		}
		idp.mu.Lock()
		answer := idp.answer
		idp.mu.Unlock()
		if answer != nil {
			f = answer(q, f)
		}
		nethttp.Redirect(w, r, q.Get("redirect_uri")+"#"+f.Encode(), nethttp.StatusFound)
	})
	idp.Server = httptest.NewServer(mux)
	t.Cleanup(idp.Close)
	return idp
}

// issue makes an access token and, when withID, an ID token (with nonce
// when not "").
func (idp *oidcIDP) issue(nonce string, withID bool) (string, string) {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	idp.n++
	access := fmt.Sprintf("oidc-access-%d-xyz", idp.n)
	idp.issued[access] = true
	if !withID || idp.noIDTok {
		return access, ""
	}
	now := idp.clock.Now()
	sum := sha256.Sum256([]byte(access))
	c := map[string]any{"iss": idp.URL, "sub": "user-42", "aud": oidcClient, "exp": now.Add(10 * time.Minute).Unix(), "iat": now.Unix(),
		"name": "Ada Lovelace", "email": "ada@example.test", "at_hash": base64.RawURLEncoding.EncodeToString(sum[:16]), "jti": fmt.Sprint(idp.n)}
	if nonce != "" {
		c["nonce"] = nonce
	}
	if idp.claims != nil {
		idp.claims(c)
	}
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	p, _ := json.Marshal(c)
	enc := base64.RawURLEncoding.EncodeToString
	id := enc(h) + "." + enc(p) + "." + enc([]byte("sig"))
	idp.issued[id] = true
	return access, id
}

func (idp *oidcIDP) valid(tok string) bool {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	return idp.issued[tok]
}

// oidcAPI answers "ok|<token>" to a token the IdP issued, 401 otherwise.
func oidcAPI(t *testing.T, idp *oidcIDP) *httptest.Server {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !idp.valid(tok) {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, "ok|"+tok)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func passwordAuth(idp string) projectfile.Auth {
	return projectfile.Auth{Type: projectfile.AuthOAuth2, GrantType: projectfile.OAuth2Password, TokenURL: idp + "/token",
		ClientID: oidcClient, Username: oidcUser, Password: oidcPassword, Scope: "openid profile", Issuer: idp}
}

func implicitAuth(idp string) projectfile.Auth {
	return projectfile.Auth{Type: projectfile.AuthOAuth2, GrantType: projectfile.OAuth2Implicit, AuthURL: idp + "/authorize",
		ClientID: oidcClient, Scope: "openid profile"}
}

// idTokens returns the ID tokens the IdP issued.
func (idp *oidcIDP) idTokens() []string {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	var out []string
	for tok := range idp.issued {
		if strings.Count(tok, ".") == 2 {
			out = append(out, tok)
		}
	}
	return out
}

// A send with the password grant gets its token from a
// test identity provider and uses it; the password, the access token and
// the ID token are masked in the timeline, the history, copy as cURL and
// the HAR export. A wrong password fails the send with invalid_grant.
func TestSendOAuth2Password(t *testing.T) {
	t.Parallel()
	clock := newClock()
	idp := newOIDCIDP(t, clock)
	api := oidcAPI(t, idp)
	st, record := historyStore(t)
	src := httpSource(api.URL + "/me")
	src.Auth = passwordAuth(idp.URL)
	f := newFixture(t, src, nil, record)
	f.svc.now = clock.Now

	done, id := sendOp(t, f)
	resp := f.rec.response(t, id)
	if done.Status != operation.StatusOK || resp.Body != "ok|oidc-access-1-xyz" {
		t.Fatalf("send: %+v %s", done, resp.Body)
	}
	tl := resp.Timeline
	if len(tl.Hops) != 2 || tl.Hops[0].Kind != HopToken {
		t.Fatalf("hops = %+v", tl.Hops)
	}
	if body := tl.Hops[0].Request.Body; body != "grant_type=password&username=ada&password="+httpclient.Mask+"&scope=openid+profile&client_id="+oidcClient {
		t.Errorf("token request body = %s", body)
	}
	if !strings.Contains(tl.Hops[0].ResponseBody, `"id_token":"`+httpclient.Mask+`"`) {
		t.Errorf("token response = %s", tl.Hops[0].ResponseBody)
	}
	if !hasEvent(tl, EventOAuth2Token, "password from "+idp.URL+"/token: 200, expires in 3600 s") {
		t.Errorf("events = %+v", tl.Events)
	}
	ids := idp.idTokens()
	secrets := append([]string{oidcPassword, url.QueryEscape(oidcPassword), "oidc-access-1-xyz"}, ids...)
	noSecrets(t, "timeline", tl, secrets...)

	// The Auth tab: the ID token's claims, the token masked.
	st2, err := f.svc.OAuth2Token(t.Context(), OAuth2TokenInputDTO{RequestID: "req_1"})
	if err != nil || st2.Status != OAuth2TokenValid || st2.Grant != "password" || st2.IDToken == nil || st2.SendsIDToken {
		t.Fatalf("status = %+v, %v", st2, err)
	}
	it := st2.IDToken
	if it.Issuer != idp.URL || it.Subject != "user-42" || !slices.Equal(it.Audience, []string{oidcClient}) || it.Name != "Ada Lovelace" ||
		it.Email != "ada@example.test" || it.ExpiresAt != clock.Now().Add(10*time.Minute).UnixMilli() || it.Expired || it.Error != "" ||
		!strings.HasPrefix(it.Token, httpclient.Mask) || len(it.Token) > len(httpclient.Mask)+4 {
		t.Errorf("ID token = %+v", it)
	}
	noSecrets(t, "status", st2, secrets...)

	// Copies: masked unless asked.
	c, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1"})
	if err != nil || !c.Masked || !strings.Contains(c.Command, "Bearer "+httpclient.Mask) {
		t.Errorf("curl = %+v, %v", c, err)
	}
	noSecrets(t, "curl", c, secrets...)
	path := filepath.Join(t.TempDir(), "out.har")
	f.svc.pickSave = func(context.Context, SaveDialog) (string, error) { return path, nil }
	if res, err := f.svc.ExportHAR(t.Context(), ExportHARInputDTO{Name: "x", RequestIDs: []string{"req_1"}}); err != nil || !res.Masked {
		t.Fatalf("HAR = %+v, %v", res, err)
	}
	har := readFile(t, path)
	for _, s := range secrets {
		if strings.Contains(har, s) {
			t.Errorf("HAR holds %q", s)
		}
	}

	waitEntries(t, st, 1)
	data := historyFiles(t, st)
	for _, s := range secrets {
		if strings.Contains(data, s) {
			t.Errorf("history holds %q", s)
		}
	}

	// A wrong password: the server's invalid_grant, nothing sent.
	src.Auth.Password = "wrong-pass"
	f2 := newFixture(t, src, nil)
	done, _ = sendOp(t, f2)
	if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != oauth2.CodeTokenError || done.Error.Details["error"] != "invalid_grant" {
		t.Fatalf("wrong password: %+v, %+v", done, done.Error)
	}
	noSecrets(t, "error", done.Error, "wrong-pass")
}

// AUTH-14: the auth sends the ID token instead of the access token; once
// the ID token expires the token is got again even though the access
// token has not; a server that sends no ID token fails the send.
func TestSendOAuth2IDToken(t *testing.T) {
	t.Parallel()
	clock := newClock()
	idp := newOIDCIDP(t, clock)
	api := oidcAPI(t, idp)
	src := httpSource(api.URL + "/me")
	src.Auth = passwordAuth(idp.URL)
	src.Auth.IDToken = true
	f := newFixture(t, src, nil)
	f.svc.now = clock.Now

	done, id := sendOp(t, f)
	resp := f.rec.response(t, id)
	ids := idp.idTokens()
	if done.Status != operation.StatusOK || len(ids) != 1 || resp.Body != "ok|"+ids[0] {
		t.Fatalf("send: %+v %s", done, resp.Body)
	}
	if !strings.Contains(resp.Timeline.Hops[1].Request.Head, "Authorization: Bearer "+httpclient.Mask) {
		t.Errorf("request head = %s", resp.Timeline.Hops[1].Request.Head)
	}
	noSecrets(t, "timeline", resp.Timeline, ids[0], "oidc-access-1-xyz")
	c, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1", IncludeSecrets: true})
	if err != nil || !strings.Contains(c.Command, "Bearer "+ids[0]) {
		t.Errorf("curl with secrets = %+v, %v", c, err)
	}
	st, _ := f.svc.OAuth2Token(t.Context(), OAuth2TokenInputDTO{RequestID: "req_1"})
	if !st.SendsIDToken || st.Status != OAuth2TokenValid {
		t.Errorf("status = %+v", st)
	}

	// The ID token lives 10 minutes, the access token an hour.
	clock.add(11 * time.Minute)
	if st, _ = f.svc.OAuth2Token(t.Context(), OAuth2TokenInputDTO{RequestID: "req_1"}); st.Status != OAuth2TokenExpired || !st.IDToken.Expired {
		t.Errorf("status after the ID token's expiry = %+v", st)
	}
	done, id = sendOp(t, f)
	if resp = f.rec.response(t, id); done.Status != operation.StatusOK || !strings.HasPrefix(resp.Body, "ok|ey") || resp.Body == "ok|"+ids[0] {
		t.Fatalf("after expiry: %+v %s", done, resp.Body)
	}

	// No ID token from the server.
	idp.mu.Lock()
	idp.noIDTok = true
	idp.mu.Unlock()
	src.Auth.Scope = "profile"
	f2 := newFixture(t, src, nil)
	f2.svc.now = clock.Now
	done, _ = sendOp(t, f2)
	if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != oauth2.CodeNoIDToken {
		t.Fatalf("no ID token: %+v, %+v", done, done.Error)
	}
}

// An ID token that fails its checks fails the send; nothing is stored or
// sent to the API, and the refused tokens are masked in the error.
func TestSendOAuth2BadIDToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		edit   func(c map[string]any)
		issuer string
		code   string
	}{
		{name: "expired", edit: func(c map[string]any) { c["exp"] = time.Date(2026, 9, 23, 11, 0, 0, 0, time.UTC).Unix() }, code: oauth2.CodeIDTokenExpired},
		{name: "wrong audience", edit: func(c map[string]any) { c["aud"] = "someone-else" }, code: oauth2.CodeIDTokenAudience},
		{name: "wrong issuer", issuer: "https://other.test", code: oauth2.CodeIDTokenIssuer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clock := newClock()
			idp := newOIDCIDP(t, clock)
			idp.claims = tt.edit
			api := oidcAPI(t, idp)
			src := httpSource(api.URL + "/me")
			src.Auth = passwordAuth(idp.URL)
			if tt.issuer != "" {
				src.Auth.Issuer = tt.issuer
			}
			f := newFixture(t, src, nil)
			f.svc.now = clock.Now
			done, _ := sendOp(t, f)
			if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != tt.code || done.Error.Kind != string(errs.PermissionDenied) {
				t.Fatalf("done = %+v, %+v", done, done.Error)
			}
			noSecrets(t, "error", done.Error, idp.idTokens()...)
			if st, _ := f.svc.OAuth2Token(t.Context(), OAuth2TokenInputDTO{RequestID: "req_1"}); st.Status != OAuth2TokenNone {
				t.Errorf("stored = %+v", st)
			}
		})
	}
}

// fragmentTestBrowser stands in for the system browser on the implicit
// grant: it follows the authorization URL to the loopback listener's
// page, checks that its CSP allows exactly its inline script, and posts
// the fragment back from the page's origin as the script does.
type fragmentTestBrowser struct {
	mu     sync.Mutex
	opened []string
	pages  []string
	wg     sync.WaitGroup
}

var pageScript = regexp.MustCompile(`<script>(.*)</script>`)

func (b *fragmentTestBrowser) open(u string) error {
	b.mu.Lock()
	b.opened = append(b.opened, u)
	b.mu.Unlock()
	b.wg.Go(func() {
		add := func(s string) {
			b.mu.Lock()
			b.pages = append(b.pages, s)
			b.mu.Unlock()
		}
		resp, err := nethttp.Get(u)
		if err != nil {
			add("error " + err.Error())
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		m := pageScript.FindSubmatch(body)
		if m == nil {
			add("no script")
			return
		}
		sum := sha256.Sum256(m[1])
		if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") {
			add("bad page")
			return
		}
		final := resp.Request.URL
		req, _ := nethttp.NewRequest(nethttp.MethodPost, "http://"+final.Host+final.Path, strings.NewReader(final.EscapedFragment()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://"+final.Host)
		pr, err := nethttp.DefaultClient.Do(req)
		if err != nil {
			add("error " + err.Error())
			return
		}
		text, _ := io.ReadAll(pr.Body)
		pr.Body.Close()
		add(fmt.Sprintf("%d %s", pr.StatusCode, text))
	})
	return nil
}

func (b *fragmentTestBrowser) redirectHost(t *testing.T) string {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	u, _ := url.Parse(b.opened[0])
	r, _ := url.Parse(u.Query().Get("redirect_uri"))
	return r.Host
}

// A send with the implicit grant signs in in the (test)
// browser, gets the token from the redirect's fragment and uses it; there
// is no token request, the sign-in is in the timeline's events, and the
// tokens are masked; the next send uses the stored token.
func TestSendOAuth2Implicit(t *testing.T) {
	t.Parallel()
	clock := newClock()
	idp := newOIDCIDP(t, clock)
	api := oidcAPI(t, idp)
	b := &fragmentTestBrowser{}
	src := httpSource(api.URL + "/me")
	src.Auth = implicitAuth(idp.URL)
	f := newFixture(t, src, nil, WithOAuth2Browser(b.open))
	f.svc.now = clock.Now

	done, id := sendOp(t, f)
	resp := f.rec.response(t, id)
	if done.Status != operation.StatusOK || resp.Body != "ok|oidc-access-1-xyz" {
		t.Fatalf("send: %+v %s", done, resp.Body)
	}
	b.wg.Wait()
	if len(b.pages) != 1 || !strings.HasPrefix(b.pages[0], "200 PostSonde got the token.") {
		t.Errorf("pages = %q", b.pages)
	}
	host := b.redirectHost(t)
	assertListenerClosed(t, host)
	tl := resp.Timeline
	if len(tl.Hops) != 1 || tl.Hops[0].Kind == HopToken {
		t.Errorf("hops = %+v", tl.Hops)
	}
	if !hasEvent(tl, EventOAuth2Authorized, "signed in in the browser, redirected to http://"+host+"/callback") ||
		!hasEvent(tl, EventOAuth2Token, "implicit from the redirect, expires in 3600 s") {
		t.Errorf("events = %+v", tl.Events)
	}
	noSecrets(t, "timeline", tl, append(idp.idTokens(), "oidc-access-1-xyz")...)
	b.mu.Lock()
	opened, _ := url.Parse(b.opened[0])
	b.mu.Unlock()
	if q := opened.Query(); q.Get("response_type") != "id_token token" || q.Get("nonce") == "" || q.Has("code_challenge") {
		t.Errorf("authorization request = %v", q)
	}
	if st, _ := f.svc.OAuth2Token(t.Context(), OAuth2TokenInputDTO{RequestID: "req_1"}); st.Grant != "implicit" || st.IDToken == nil || st.Refreshable {
		t.Errorf("status = %+v", st)
	}

	if done, id = sendOp(t, f); f.rec.response(t, id).Body != "ok|oidc-access-1-xyz" || len(b.opened) != 1 {
		t.Errorf("second send: %+v", done)
	}
}

// A fragment with another state, or an ID token with
// another nonce, fails the send; nothing is stored or sent to the API.
func TestSendOAuth2ImplicitRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		answer func(q, f url.Values) url.Values
		code   string
	}{
		{name: "state mismatch", answer: func(_, f url.Values) url.Values { f.Set("state", "forged"); return f }, code: oauth2.CodeStateMismatch},
		{name: "nonce mismatch", code: oauth2.CodeIDTokenNonce},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clock := newClock()
			idp := newOIDCIDP(t, clock)
			idp.answer = tt.answer
			if tt.answer == nil {
				idp.claims = func(c map[string]any) { c["nonce"] = "from-another-sign-in" }
			}
			api := oidcAPI(t, idp)
			b := &fragmentTestBrowser{}
			src := httpSource(api.URL + "/me")
			src.Auth = implicitAuth(idp.URL)
			f := newFixture(t, src, nil, WithOAuth2Browser(b.open))
			f.svc.now = clock.Now
			done, _ := sendOp(t, f)
			if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != tt.code {
				t.Fatalf("done = %+v, %+v", done, done.Error)
			}
			b.wg.Wait()
			if len(b.pages) != 1 || !strings.HasPrefix(b.pages[0], "400 ") {
				t.Errorf("pages = %q", b.pages)
			}
			assertListenerClosed(t, b.redirectHost(t))
			if st, _ := f.svc.OAuth2Token(t.Context(), OAuth2TokenInputDTO{RequestID: "req_1"}); st.Status != OAuth2TokenNone {
				t.Errorf("stored = %+v", st)
			}
		})
	}
}
