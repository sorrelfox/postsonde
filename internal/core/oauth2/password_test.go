package oauth2

import (
	"encoding/json"
	"errors"
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
)

// passwordIDP is a test identity provider for the resource owner password
// credentials grant (RFC 6749 section 4.3): a confidential client "app"
// with its secret in the Authorization header, one user, and an ID token
// for the openid scope.
type passwordIDP struct {
	*httptest.Server
	mu    sync.Mutex
	forms []url.Values
	now   time.Time
}

const (
	pwUser     = "ada@example.test"
	pwPassword = "c0rrect horse+battery&staple"
)

func newPasswordIDP(t *testing.T, now time.Time) *passwordIDP {
	idp := &passwordIDP{now: now}
	idp.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f := r.PostForm
		idp.mu.Lock()
		idp.forms = append(idp.forms, f)
		n := len(idp.forms)
		idp.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fail := func(code string) {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": "refused by the test IdP"})
		}
		if id, secret, ok := r.BasicAuth(); !ok || id != "app" || secret != "app-secret" {
			fail("invalid_client")
			return
		}
		if f.Get("grant_type") != GrantPassword {
			fail("unsupported_grant_type")
			return
		}
		if f.Get("username") != pwUser || f.Get("password") != pwPassword {
			fail("invalid_grant")
			return
		}
		access := fmt.Sprintf("pw-access-%d", n)
		out := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 600, "refresh_token": "pw-refresh", "scope": f.Get("scope")}
		if HasOpenIDScope(f.Get("scope")) {
			c := idClaims(idp.now, "")
			c["aud"] = "app"
			at, _ := AccessTokenHash("ES256", access)
			c["at_hash"] = at
			out["id_token"] = makeIDToken("ES256", c)
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(idp.Close)
	return idp
}

// The password grant gets a token from a test identity
// provider; a wrong password is the server's invalid_grant. The password
// is one of the request's secrets, masked in its body.
func TestPasswordGrant(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	idp := newPasswordIDP(t, now)
	cfg := Config{Grant: GrantPassword, TokenURL: idp.URL + "/token", ClientID: "app", ClientSecret: "app-secret",
		Username: pwUser, Password: pwPassword, Scope: "openid orders"}
	req, err := NewTokenRequest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Exchange(t.Context(), client(), req, httpclient.Settings{}, func() time.Time { return now })
	if err != nil || res.Token.AccessToken != "pw-access-1" || res.Token.RefreshToken != "pw-refresh" || res.Token.IDToken == "" {
		t.Fatalf("Exchange = %+v, %v", res, err)
	}
	body := string(res.Request.Body.(httpclient.RawBody).Data)
	if body != "grant_type=password&username=ada%40example.test&password=c0rrect+horse%2Bbattery%26staple&scope=openid+orders" {
		t.Errorf("form = %s", body)
	}
	masked := httpclient.NewSecretMasker(req.Secrets()).MaskText(body)
	if strings.Contains(masked, "horse") || !strings.Contains(masked, "password="+httpclient.Mask) {
		t.Errorf("masked form = %s", masked)
	}
	id, err := CheckIDToken(req.Config, res.Token, now)
	if err != nil || id.Subject != "user-42" {
		t.Errorf("ID token = %+v, %v", id, err)
	}

	// A changed username or password is another token.
	other := cfg
	other.Password = "other"
	if must(NewTokenRequest(other)).Key() == req.Key() {
		t.Error("another password has the same key")
	}
	other = cfg
	other.Username = "bob"
	if must(NewTokenRequest(other)).Key() == req.Key() {
		t.Error("another username has the same key")
	}

	wrong := cfg
	wrong.Password = "wrong"
	_, err = Exchange(t.Context(), client(), must(NewTokenRequest(wrong)), httpclient.Settings{}, nil)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeTokenError || e.Details["error"] != "invalid_grant" || strings.Contains(e.Error(), "wrong") {
		t.Fatalf("wrong password: %#v", err)
	}
}

// The password grant's ID token is checked like any other: one issued for
// another client fails the exchange.
func TestPasswordGrantBadIDToken(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	idp := newPasswordIDP(t, now.Add(-time.Hour))
	cfg := Config{Grant: GrantPassword, TokenURL: idp.URL, ClientID: "app", ClientSecret: "app-secret", Username: pwUser, Password: pwPassword, Scope: "openid"}
	res, err := Exchange(t.Context(), client(), must(NewTokenRequest(cfg)), httpclient.Settings{}, func() time.Time { return now })
	if codeOf(err) != CodeIDTokenExpired || res == nil || res.Token.AccessToken == "" {
		t.Fatalf("Exchange = %+v, %v", res, err)
	}
}
