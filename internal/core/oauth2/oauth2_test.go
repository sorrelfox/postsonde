package oauth2

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// testIDP is a token endpoint for tests: it checks the client
// credentials (in the Authorization header or the body, as RFC 6749
// section 2.3.1 says), the grant and the scopes, and answers with a token
// or an error response. Canned answers replace it with reply.
type testIDP struct {
	*httptest.Server
	clientID, secret string
	scopes           map[string]bool
	// reply, when set, answers instead: status, content type and body.
	reply func() (int, string, string)

	mu    sync.Mutex
	forms []url.Values
	auths []string
	count int
}

func newIDP(t *testing.T) *testIDP {
	idp := &testIDP{clientID: "app", secret: "s3cret+/ key", scopes: map[string]bool{"read": true, "write": true}}
	idp.Server = httptest.NewServer(http.HandlerFunc(idp.serve))
	t.Cleanup(idp.Close)
	return idp
}

func (idp *testIDP) serve(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	idp.mu.Lock()
	idp.forms = append(idp.forms, r.PostForm)
	idp.auths = append(idp.auths, r.Header.Get("Authorization"))
	idp.count++
	n := idp.count
	idp.mu.Unlock()
	if idp.reply != nil {
		status, ct, body := idp.reply()
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(status)
		fmt.Fprint(w, body)
		return
	}
	fail := func(status int, code, desc string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc})
	}
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != FormType {
		fail(400, "invalid_request", "POST a form")
		return
	}
	id, secret, ok := r.BasicAuth()
	if ok {
		// Basic credentials are form-encoded first (section 2.3.1).
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != idp.clientID || secret != idp.secret {
		w.Header().Set("WWW-Authenticate", `Basic realm="idp"`)
		fail(401, "invalid_client", "Client authentication failed")
		return
	}
	if r.PostForm.Get("grant_type") != GrantClientCredentials {
		fail(400, "unsupported_grant_type", "")
		return
	}
	for s := range strings.FieldsSeq(r.PostForm.Get("scope")) {
		if !idp.scopes[s] {
			fail(400, "invalid_scope", "Unknown scope "+s)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `{"access_token":"tok-%d","token_type":"Bearer","expires_in":3600,"scope":%q}`, n, r.PostForm.Get("scope"))
}

func client() *httpclient.Client {
	return httpclient.New(netx.NewDirect(netx.Options{}), httpclient.TransportOptions{})
}

func codeOf(err error) string {
	if e, ok := err.(*errs.Error); ok {
		return e.Code
	}
	return ""
}

func TestExchangeClientCredentials(t *testing.T) {
	t.Parallel()
	idp := newIDP(t)
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	tests := []struct {
		name       string
		cfg        Config
		wantCode   string
		wantKind   errs.Kind
		wantDetail string
	}{
		{name: "header", cfg: Config{ClientID: "app", ClientSecret: idp.secret, Scope: "read write"}},
		{name: "body", cfg: Config{ClientID: "app", ClientSecret: idp.secret, ClientAuth: ClientAuthBody, Scope: "read"}},
		{name: "invalid_client", cfg: Config{ClientID: "app", ClientSecret: "wrong"}, wantCode: CodeTokenError, wantKind: errs.PermissionDenied, wantDetail: "invalid_client"},
		{name: "invalid_scope", cfg: Config{ClientID: "app", ClientSecret: idp.secret, Scope: "read admin"}, wantCode: CodeTokenError, wantKind: errs.InvalidArgument, wantDetail: "invalid_scope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			cfg.TokenURL = idp.URL + "/token"
			req, err := NewTokenRequest(cfg)
			if err != nil {
				t.Fatal(err)
			}
			res, err := Exchange(t.Context(), client(), req, httpclient.DefaultSettings(), clock)
			if res == nil || res.Response == nil {
				t.Fatalf("no response recorded: %v", err)
			}
			if tt.wantCode != "" {
				e, ok := err.(*errs.Error)
				if !ok || e.Code != tt.wantCode || e.Kind != tt.wantKind || e.Details["error"] != tt.wantDetail {
					t.Fatalf("err = %#v", err)
				}
				if strings.Contains(e.Error(), idp.secret) || e.Hint != ErrorHints[tt.wantDetail] {
					t.Errorf("message %q, hint %q", e.Error(), e.Hint)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tok := res.Token
			if !strings.HasPrefix(tok.AccessToken, "tok-") || tok.TokenType != "Bearer" || tok.ExpiresIn != 3600 || tok.Scope != cfg.Scope ||
				!tok.Expiry.Equal(now.Add(time.Hour)) || !tok.Obtained.Equal(now) {
				t.Errorf("token = %+v", tok)
			}
		})
	}
}

func TestTokenRequestForm(t *testing.T) {
	t.Parallel()
	cfg := Config{TokenURL: "https://idp.test/t", ClientID: "my app", ClientSecret: "a:b+c", Scope: "read write",
		Audience: "https://api.test", Resource: "urn:r", State: "not sent", Params: []Param{{"tenant", "t 1"}}}
	req, err := NewTokenRequest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := req.HTTP(httpclient.DefaultSettings())
	body, _ := h.Body.(httpclient.RawBody)
	if got, want := string(body.Data), "grant_type=client_credentials&scope=read+write&audience=https%3A%2F%2Fapi.test&resource=urn%3Ar&tenant=t+1"; got != want {
		t.Errorf("body = %s\nwant %s", got, want)
	}
	if h.Method != "POST" || h.Settings.FollowRedirects || body.Type != FormType {
		t.Errorf("request = %+v", h)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("my+app:a%3Ab%2Bc"))
	if h.Headers[1].Value != wantAuth {
		t.Errorf("Authorization = %s, want %s", h.Headers[1].Value, wantAuth)
	}
	if s := req.Secrets(); len(s) != 2 || s[0] != "a:b+c" || s[1] != strings.TrimPrefix(wantAuth, "Basic ") {
		t.Errorf("secrets = %q", s)
	}

	cfg.ClientAuth = ClientAuthBody
	req, _ = NewTokenRequest(cfg)
	h = req.HTTP(httpclient.Settings{})
	body, _ = h.Body.(httpclient.RawBody)
	if !strings.Contains(string(body.Data), "&client_id=my+app&client_secret=a%3Ab%2Bc&tenant=") || len(h.Headers) != 1 {
		t.Errorf("body auth: %s %+v", body.Data, h.Headers)
	}
	if h.Settings.MaxBodyBytes != MaxResponseBytes {
		t.Errorf("max body = %d", h.Settings.MaxBodyBytes)
	}

	// The key changes with every field that makes the token.
	base, _ := NewTokenRequest(Config{TokenURL: "https://idp.test/t", ClientID: "a", ClientSecret: "s", Scope: "x"})
	for _, c := range []Config{
		{TokenURL: "https://idp.test/u", ClientID: "a", ClientSecret: "s", Scope: "x"},
		{TokenURL: "https://idp.test/t", ClientID: "b", ClientSecret: "s", Scope: "x"},
		{TokenURL: "https://idp.test/t", ClientID: "a", ClientSecret: "t", Scope: "x"},
		{TokenURL: "https://idp.test/t", ClientID: "a", ClientSecret: "s", Scope: "y"},
		{TokenURL: "https://idp.test/t", ClientID: "a", ClientSecret: "s", Scope: "x", Audience: "z"},
	} {
		other, _ := NewTokenRequest(c)
		if other.Key() == base.Key() {
			t.Errorf("same key for %+v", c)
		}
	}
	if strings.Contains(base.Key(), "s") && len(base.Key()) != 32 {
		t.Errorf("key = %s", base.Key())
	}
}

func TestUnsupportedGrant(t *testing.T) {
	t.Parallel()
	for _, g := range []string{GrantRefreshToken, "urn:custom"} {
		if _, err := NewTokenRequest(Config{Grant: g}); codeOf(err) != CodeUnsupportedGrant || errs.KindOf(err) != errs.Unsupported {
			t.Errorf("%s: err = %v", g, err)
		}
	}
}

func TestParseResponse(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		status   int
		ct, body string
		want     Token
		code     string
		field    string
	}{
		{name: "minimal", status: 200, ct: "application/json", body: `{"access_token":"a"}`, want: Token{AccessToken: "a"}},
		{name: "full", status: 200, ct: "application/json", body: `{"access_token":"a","token_type":"bearer","expires_in":60,"refresh_token":"r","scope":"s","id_token":"i","extra":{"x":1}}`,
			want: Token{AccessToken: "a", TokenType: "bearer", ExpiresIn: 60, Expiry: now.Add(time.Minute), RefreshToken: "r", Scope: "s", IDToken: "i"}},
		{name: "expires_in as a string", status: 200, ct: "application/json", body: `{"access_token":"a","expires_in":"120"}`, want: Token{AccessToken: "a", ExpiresIn: 120, Expiry: now.Add(2 * time.Minute)}},
		{name: "expires_in as a float", status: 200, ct: "", body: `{"access_token":"a","expires_in":30.0}`, want: Token{AccessToken: "a", ExpiresIn: 30, Expiry: now.Add(30 * time.Second)}},
		{name: "form", status: 200, ct: "application/x-www-form-urlencoded", body: "access_token=a%2Bb&token_type=bearer&scope=repo", want: Token{AccessToken: "a+b", TokenType: "bearer", Scope: "repo"}},
		{name: "malformed JSON", status: 200, ct: "application/json", body: `{"access_token": "a",`, code: CodeMalformedResponse},
		{name: "HTML", status: 200, ct: "text/html", body: `<html>login</html>`, code: CodeMalformedResponse},
		{name: "array", status: 200, ct: "application/json", body: `["a"]`, code: CodeMalformedResponse},
		{name: "no access_token", status: 200, ct: "application/json", body: `{"token_type":"Bearer"}`, code: CodeMalformedResponse, field: "access_token"},
		{name: "access_token not a string", status: 200, ct: "application/json", body: `{"access_token":42}`, code: CodeMalformedResponse, field: "access_token"},
		{name: "access_token with a line break", status: 200, ct: "application/json", body: `{"access_token":"a\r\nX: y"}`, code: CodeMalformedResponse, field: "access_token"},
		{name: "expires_in not a number", status: 200, ct: "application/json", body: `{"access_token":"a","expires_in":"soon"}`, code: CodeMalformedResponse, field: "expires_in"},
		{name: "negative expires_in", status: 200, ct: "application/json", body: `{"access_token":"a","expires_in":-5}`, code: CodeMalformedResponse, field: "expires_in"},
		{name: "error response", status: 400, ct: "application/json", body: `{"error":"invalid_grant","error_description":"bad","error_uri":"https://idp.test/e"}`, code: CodeTokenError},
		{name: "error with 200", status: 200, ct: "application/json", body: `{"error":"invalid_scope"}`, code: CodeTokenError},
		{name: "500 without error", status: 500, ct: "text/plain", body: `oops`, code: CodeTokenHTTPStatus},
		{name: "401 with malformed JSON", status: 401, ct: "application/json", body: `{`, code: CodeTokenHTTPStatus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseResponse(tt.status, tt.ct, []byte(tt.body), now)
			if tt.code != "" {
				e, ok := err.(*errs.Error)
				if !ok || e.Code != tt.code || e.Details["field"] != tt.field {
					t.Fatalf("err = %#v", err)
				}
				if tt.code == CodeTokenError && tt.status == 400 && (e.Details["errorDescription"] != "bad" || e.Details["errorUri"] != "https://idp.test/e" || !strings.Contains(e.Message, "invalid_grant (bad)")) {
					t.Errorf("error = %#v", e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.want.Obtained = now
			if got != tt.want {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestTokenExpiry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tok := Token{AccessToken: "a", Expiry: now.Add(time.Hour)}
	for _, tt := range []struct {
		at   time.Time
		want bool
	}{
		{now, false},
		{now.Add(time.Hour - ExpiryLeeway - time.Second), false},
		{now.Add(time.Hour - ExpiryLeeway), true},
		{now.Add(2 * time.Hour), true},
	} {
		if got := tok.Expired(tt.at); got != tt.want {
			t.Errorf("Expired(%v) = %v", tt.at.Sub(now), got)
		}
	}
	if (Token{AccessToken: "a"}).Expired(now.Add(1000 * time.Hour)) {
		t.Error("a token without expiry expired")
	}
}

func TestExchangeFailures(t *testing.T) {
	t.Parallel()
	idp := newIDP(t)
	idp.reply = func() (int, string, string) { return 200, "application/json", `{"access_token":` }
	req, _ := NewTokenRequest(Config{TokenURL: idp.URL + "/token?x=1", ClientID: "app", ClientSecret: "s"})
	res, err := Exchange(t.Context(), client(), req, httpclient.DefaultSettings(), nil)
	if codeOf(err) != CodeMalformedResponse || res.Response == nil || res.Response.StatusCode != 200 {
		t.Fatalf("malformed: %v", err)
	}

	// Nothing listening: the cause's kind and code are kept.
	idp.Close()
	res, err = Exchange(t.Context(), client(), req, httpclient.DefaultSettings(), nil)
	e, ok := err.(*errs.Error)
	if !ok || res.Response != nil || !strings.HasPrefix(e.Message, "The OAuth 2.0 token request failed") || e.Details["tokenUrl"] != idp.URL+"/token" {
		t.Fatalf("closed: %#v", err)
	}
}

func TestMemoryStore(t *testing.T) {
	t.Parallel()
	var s MemoryStore
	if _, ok := s.Get("k"); ok {
		t.Fatal("empty store has a token")
	}
	s.Put("k", Token{AccessToken: "a"})
	if got, ok := s.Get("k"); !ok || got.AccessToken != "a" {
		t.Fatalf("Get = %+v %v", got, ok)
	}
	s.Delete("k")
	if _, ok := s.Get("k"); ok {
		t.Fatal("deleted token still there")
	}
	s.Put("k", Token{})
	s.Clear()
	if _, ok := s.Get("k"); ok {
		t.Fatal("cleared token still there")
	}
}
