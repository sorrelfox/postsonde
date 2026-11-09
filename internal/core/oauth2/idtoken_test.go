package oauth2

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// makeIDToken returns an ID token with header alg and the claims: a JWS
// whose signature is not a real one (nothing verifies it).
func makeIDToken(alg string, claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": alg, "typ": "JWT", "kid": "k1"})
	p, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString
	return enc(h) + "." + enc(p) + "." + enc([]byte("not-a-real-signature"))
}

// idClaims are valid claims for client "app" at now, with nonce.
func idClaims(now time.Time, nonce string) map[string]any {
	c := map[string]any{
		"iss": "https://idp.test", "sub": "user-42", "aud": "app",
		"exp": now.Add(10 * time.Minute).Unix(), "iat": now.Unix(),
		"name": "Ada Lovelace", "email": "ada@example.test",
	}
	if nonce != "" {
		c["nonce"] = nonce
	}
	return c
}

func TestDecodeIDToken(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	c := idClaims(now, "n-1")
	c["aud"] = []string{"app", "api"}
	c["azp"] = "app"
	c["nbf"] = 1790000000.5
	id, err := DecodeIDToken(makeIDToken("RS256", c))
	if err != nil {
		t.Fatal(err)
	}
	if id.Algorithm != "RS256" || id.Issuer != "https://idp.test" || id.Subject != "user-42" || strings.Join(id.Audience, ",") != "app,api" ||
		id.AuthorizedParty != "app" || !id.Expiry.Equal(now.Add(10*time.Minute)) || !id.IssuedAt.Equal(now) ||
		id.NotBefore.UnixMilli() != 1790000000500 || id.Nonce != "n-1" || id.Name != "Ada Lovelace" || id.Email != "ada@example.test" {
		t.Errorf("decoded = %+v", id)
	}

	enc := base64.RawURLEncoding.EncodeToString
	header := enc([]byte(`{"alg":"RS256"}`))
	tests := []struct {
		name, raw, claim string
	}{
		{name: "empty", raw: ""},
		{name: "two parts", raw: "a.b"},
		{name: "JWE", raw: "a.b.c.d.e"},
		{name: "header not base64", raw: "*." + enc([]byte(`{}`)) + ".x"},
		{name: "payload not JSON", raw: header + "." + enc([]byte("hello")) + ".c2ln"},
		{name: "payload an array", raw: header + "." + enc([]byte("[1]")) + ".c2ln"},
		{name: "no sub", raw: makeIDToken("RS256", map[string]any{"iss": "i", "aud": "a", "exp": 1}), claim: "sub"},
		{name: "empty iss", raw: makeIDToken("RS256", map[string]any{"iss": "", "sub": "s", "aud": "a", "exp": 1}), claim: "iss"},
		{name: "no aud", raw: makeIDToken("RS256", map[string]any{"iss": "i", "sub": "s", "exp": 1}), claim: "aud"},
		{name: "aud a number", raw: makeIDToken("RS256", map[string]any{"iss": "i", "sub": "s", "aud": 5, "exp": 1}), claim: "aud"},
		{name: "no exp", raw: makeIDToken("RS256", map[string]any{"iss": "i", "sub": "s", "aud": "a"}), claim: "exp"},
		{name: "exp a string", raw: makeIDToken("RS256", map[string]any{"iss": "i", "sub": "s", "aud": "a", "exp": "soon"}), claim: "exp"},
		{name: "nonce a number", raw: makeIDToken("RS256", map[string]any{"iss": "i", "sub": "s", "aud": "a", "exp": 1, "nonce": 7}), claim: "nonce"},
	}
	for _, tt := range tests {
		_, err := DecodeIDToken(tt.raw)
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != CodeIDTokenMalformed || e.Kind != errs.Parse || e.Details["claim"] != tt.claim {
			t.Errorf("%s: err = %#v", tt.name, err)
		}
	}
}

// ID token claims and the validation failures: expired, not yet valid,
// another audience or authorized party, another issuer, another or no
// nonce, an at_hash of another access token.
func TestCheckIDToken(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cfg := Config{ClientID: "app", Nonce: "n-1"}
	atHash, _ := AccessTokenHash("RS256", "access-1")
	with := func(edit func(map[string]any)) string {
		c := idClaims(now, "n-1")
		c["at_hash"] = atHash
		if edit != nil {
			edit(c)
		}
		return makeIDToken("RS256", c)
	}
	tests := []struct {
		name  string
		cfg   func(*Config)
		token string
		at    time.Time
		code  string
		claim string
	}{
		{name: "valid", token: with(nil)},
		{name: "valid within the leeway", token: with(nil), at: now.Add(10*time.Minute + 30*time.Second)},
		{name: "valid, issuer expected", token: with(nil), cfg: func(c *Config) { c.Issuer = "https://idp.test" }},
		{name: "valid, no nonce expected", token: with(func(c map[string]any) { delete(c, "nonce") }), cfg: func(c *Config) { c.Nonce = "" }},
		{name: "valid, no client ID to check", token: with(func(c map[string]any) { c["aud"] = "other" }), cfg: func(c *Config) { c.ClientID = "" }},
		{name: "valid, aud list", token: with(func(c map[string]any) { c["aud"] = []string{"api", "app"}; c["azp"] = "app" })},
		{name: "expired", token: with(nil), at: now.Add(12 * time.Minute), code: CodeIDTokenExpired, claim: "exp"},
		{name: "not yet valid", token: with(func(c map[string]any) { c["nbf"] = now.Add(5 * time.Minute).Unix() }), code: CodeIDTokenExpired, claim: "nbf"},
		{name: "wrong audience", token: with(func(c map[string]any) { c["aud"] = "someone-else" }), code: CodeIDTokenAudience, claim: "aud"},
		{name: "wrong authorized party", token: with(func(c map[string]any) { c["aud"] = []string{"app", "x"}; c["azp"] = "x" }), code: CodeIDTokenAudience, claim: "azp"},
		{name: "wrong nonce", token: with(func(c map[string]any) { c["nonce"] = "replayed" }), code: CodeIDTokenNonce, claim: "nonce"},
		{name: "no nonce", token: with(func(c map[string]any) { delete(c, "nonce") }), code: CodeIDTokenNonce, claim: "nonce"},
		{name: "wrong issuer", token: with(nil), cfg: func(c *Config) { c.Issuer = "https://other.test" }, code: CodeIDTokenIssuer, claim: "iss"},
		{name: "at_hash of another token", token: with(func(c map[string]any) { c["at_hash"] = "AAAAAAAAAAAAAAAAAAAAAA" }), code: CodeIDTokenAtHash, claim: "at_hash"},
		{name: "malformed", token: "x.y", code: CodeIDTokenMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := cfg
			if tt.cfg != nil {
				tt.cfg(&c)
			}
			at := tt.at
			if at.IsZero() {
				at = now
			}
			id, err := CheckIDToken(c, Token{AccessToken: "access-1", IDToken: tt.token}, at)
			if tt.code == "" {
				if err != nil || id.Subject != "user-42" || id.Email != "ada@example.test" {
					t.Fatalf("CheckIDToken = %+v, %v", id, err)
				}
				return
			}
			var e *errs.Error
			if !errors.As(err, &e) || e.Code != tt.code || e.Details["claim"] != tt.claim {
				t.Fatalf("err = %#v", err)
			}
			if tt.code != CodeIDTokenMalformed && e.Kind != errs.PermissionDenied {
				t.Errorf("kind = %s", e.Kind)
			}
			// No value of the token in the message.
			if strings.Contains(e.Message, "replayed") || strings.Contains(e.Message, "someone-else") || strings.Contains(e.Message, tt.token) {
				t.Errorf("message = %s", e.Message)
			}
		})
	}
	if _, err := CheckIDToken(cfg, Token{AccessToken: "a"}, now); err != nil {
		t.Errorf("no ID token: %v", err)
	}
}

// OpenID Connect Core A.3/A.4: the at_hash of the example access token.
func TestAccessTokenHash(t *testing.T) {
	t.Parallel()
	if got, ok := AccessTokenHash("RS256", "jHkWEdUXMU1BwAsC4vtUsZwnNvTIxEl0z9K3vx5KF0Y"); !ok || got != "77QmUPtjPfzWtF2AnpK9RQ" {
		t.Errorf("at_hash = %s, %v", got, ok)
	}
	for alg, n := range map[string]int{"ES384": 32, "PS512": 43, "EdDSA": 43, "HS256": 22} {
		if got, ok := AccessTokenHash(alg, "x"); !ok || len(got) != n {
			t.Errorf("%s: %s, %v", alg, got, ok)
		}
	}
	if _, ok := AccessTokenHash("none", "x"); ok {
		t.Error("none has a hash")
	}
}

// The ID token decides whether a token can be sent when the auth sends the
// ID token (AUTH-14).
func TestExpiredFor(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	c := idClaims(now, "")
	c["exp"] = now.Add(time.Minute).Unix()
	tok := Token{AccessToken: "a", Expiry: now.Add(time.Hour), IDToken: makeIDToken("RS256", c)}
	if tok.ExpiredFor(now, false) || tok.ExpiredFor(now, true) {
		t.Error("expired now")
	}
	later := now.Add(45 * time.Second)
	if tok.ExpiredFor(later, false) || !tok.ExpiredFor(later, true) {
		t.Error("the ID token's expiry is not used")
	}
	if !(Token{AccessToken: "a"}).ExpiredFor(now, true) {
		t.Error("no ID token can be sent")
	}
}
