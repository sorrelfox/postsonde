package oauth2

// idtoken.go reads an OpenID Connect ID token (OpenID Connect Core 1.0
// section 2) that came with a token (AUTH-14): it decodes the JWS compact
// serialization and checks the claims a client must check (section
// 3.1.3.7, 3.2.2.11): the issuer, the audience and authorized party, the
// expiry, the nonce, and at_hash against the access token.
//
// The signature is not verified: that needs the issuer's keys (its JWKS),
// a network fetch this package never makes by itself. For the grants
// whose token comes straight from the token endpoint over TLS (the
// authorization code, password and client credentials grants, a refresh),
// section 3.1.3.7 step 6 lets the TLS server validation stand in for the
// signature. The implicit grant's ID token comes through the browser, so
// its claims are checked but its origin is only as good as the state and
// the nonce. See docs/auth.md#oidc-id-token.

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// IDTokenLeeway is the clock skew allowed when an ID token's exp and nbf
// are checked on arrival (CheckIDToken).
const IDTokenLeeway = time.Minute

// Error codes of an ID token. Details: claim, for a claim.
const (
	// CodeIDTokenMalformed: the ID token is not a signed JWT (three
	// base64url parts, a JSON header and payload), a claim has the wrong
	// type, or a required claim (iss, sub, aud, exp) is missing (kind
	// parse). An encrypted ID token (JWE) is refused with it too.
	CodeIDTokenMalformed = "oauth2.id_token_malformed"
	// CodeIDTokenIssuer: iss is not the issuer the auth expects (kind
	// permission_denied). Details: issuer, expected.
	CodeIDTokenIssuer = "oauth2.id_token_issuer"
	// CodeIDTokenAudience: aud does not name the client ID, or azp names
	// another client (kind permission_denied). Details: claim.
	CodeIDTokenAudience = "oauth2.id_token_audience"
	// CodeIDTokenExpired: the ID token has expired, or is not valid yet
	// (nbf), beyond IDTokenLeeway (kind permission_denied). Details:
	// claim.
	CodeIDTokenExpired = "oauth2.id_token_expired"
	// CodeIDTokenNonce: the nonce claim is not the nonce sent with the
	// sign-in, or is missing (kind permission_denied): the ID token may
	// be replayed from another sign-in.
	CodeIDTokenNonce = "oauth2.id_token_nonce"
	// CodeIDTokenAtHash: at_hash is not the hash of the access token that
	// came with the ID token (kind permission_denied).
	CodeIDTokenAtHash = "oauth2.id_token_at_hash"
	// CodeNoIDToken: the auth sends the ID token, but the server sent none
	// (kind protocol).
	CodeNoIDToken = "oauth2.no_id_token"
)

// IDToken is a decoded ID token: its header's algorithm and the claims
// PostSonde reads. Nothing of it is verified by DecodeIDToken.
type IDToken struct {
	// Algorithm is the header's alg ("RS256"), as sent.
	Algorithm string
	// Issuer (iss), Subject (sub) and Audience (aud, one or more).
	Issuer   string
	Subject  string
	Audience []string
	// AuthorizedParty is azp, "" when absent.
	AuthorizedParty string
	// Expiry (exp), IssuedAt (iat) and NotBefore (nbf); zero when absent.
	Expiry    time.Time
	IssuedAt  time.Time
	NotBefore time.Time
	// Nonce is the nonce claim, "" when absent.
	Nonce string
	// Name and Email are the profile claims name and email, when present.
	Name  string
	Email string
	// AccessTokenHash is at_hash, "" when absent.
	AccessTokenHash string
}

// DecodeIDToken decodes raw, an ID token in JWS compact serialization,
// without checking anything but its form: three base64url parts, a JSON
// object header with alg, and a JSON object payload with iss, sub, aud
// and exp of the right types (CodeIDTokenMalformed otherwise).
func DecodeIDToken(raw string) (IDToken, error) {
	parts := strings.Split(raw, ".")
	if len(parts) == 5 {
		return IDToken{}, idMalformed("The ID token is encrypted (JWE), which PostSonde cannot read.", "")
	}
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return IDToken{}, idMalformed("The ID token is not a JWT.", "")
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if err := decodePart(parts[0], &header); err != nil {
		return IDToken{}, idMalformed("The ID token's header is not a JSON object.", "")
	}
	if _, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[2], "=")); err != nil {
		return IDToken{}, idMalformed("The ID token's signature is not base64url.", "")
	}
	var claims map[string]json.RawMessage
	if err := decodePart(parts[1], &claims); err != nil {
		return IDToken{}, idMalformed("The ID token's payload is not a JSON object.", "")
	}
	out := IDToken{Algorithm: header.Alg}
	strs := []struct {
		name     string
		to       *string
		required bool
	}{
		{"iss", &out.Issuer, true}, {"sub", &out.Subject, true}, {"azp", &out.AuthorizedParty, false},
		{"nonce", &out.Nonce, false}, {"name", &out.Name, false}, {"email", &out.Email, false},
		{"at_hash", &out.AccessTokenHash, false},
	}
	for _, s := range strs {
		v, ok, err := claimString(claims, s.name)
		if err != nil || (s.required && (!ok || v == "")) {
			return IDToken{}, badClaim(s.name, err != nil)
		}
		*s.to = v
	}
	aud, err := claimAudience(claims)
	if err != nil || len(aud) == 0 {
		return IDToken{}, badClaim("aud", err != nil)
	}
	out.Audience = aud
	times := []struct {
		name     string
		to       *time.Time
		required bool
	}{{"exp", &out.Expiry, true}, {"iat", &out.IssuedAt, false}, {"nbf", &out.NotBefore, false}}
	for _, c := range times {
		v, ok, err := claimTime(claims, c.name)
		if err != nil || (c.required && !ok) {
			return IDToken{}, badClaim(c.name, err != nil)
		}
		*c.to = v
	}
	return out, nil
}

// CheckIDToken decodes the ID token of t (DecodeIDToken) and checks it
// against c at now, in this order:
//
//   - nonce: when c.Nonce is set (a sign-in sent one) the claim must be
//     it (CodeIDTokenNonce);
//   - aud must name c.ClientID, and azp, when present, must be it
//     (CodeIDTokenAudience); not checked without a client ID;
//   - iss must be c.Issuer when that is set (CodeIDTokenIssuer);
//   - exp must not have passed, nor nbf be ahead, beyond IDTokenLeeway
//     (CodeIDTokenExpired);
//   - at_hash, when present, must be the hash of t.AccessToken for the
//     header's algorithm (CodeIDTokenAtHash; not checked for an
//     algorithm whose hash is unknown).
//
// The signature is not verified (see the file comment). A token without
// an ID token returns the zero IDToken and no error.
func CheckIDToken(c Config, t Token, now time.Time) (IDToken, error) {
	if t.IDToken == "" {
		return IDToken{}, nil
	}
	id, err := DecodeIDToken(t.IDToken)
	if err != nil {
		return id, err
	}
	refused := func(code, msg, hint, claim string) (IDToken, error) {
		e := errs.New(errs.PermissionDenied, msg).WithCode(code).WithHint(hint)
		if claim != "" {
			e = e.WithDetail("claim", claim)
		}
		return id, e
	}
	if c.Nonce != "" && id.Nonce != c.Nonce {
		return refused(CodeIDTokenNonce, "The ID token was refused: its nonce is not the one PostSonde sent with the sign-in.",
			"The ID token may come from another sign-in. Get a new token to sign in again.", "nonce")
	}
	if c.ClientID != "" {
		if !slices.Contains(id.Audience, c.ClientID) {
			return refused(CodeIDTokenAudience, "The ID token was refused: its audience (aud) does not name the client ID.",
				"The ID token was issued for another client. Check the client ID.", "aud")
		}
		if id.AuthorizedParty != "" && id.AuthorizedParty != c.ClientID {
			return refused(CodeIDTokenAudience, "The ID token was refused: its authorized party (azp) is another client.",
				"The ID token was issued for another client. Check the client ID.", "azp")
		}
	}
	if c.Issuer != "" && id.Issuer != c.Issuer {
		e := errs.New(errs.PermissionDenied, "The ID token was refused: its issuer (iss) is not the expected one.").
			WithCode(CodeIDTokenIssuer).
			WithHint("Check the issuer in the auth: it must be exactly the iss the server writes, e.g. https://auth.example.com/.").
			WithDetail("claim", "iss").WithDetail("issuer", clip(id.Issuer, 200)).WithDetail("expected", clip(c.Issuer, 200))
		return id, e
	}
	if !now.Before(id.Expiry.Add(IDTokenLeeway)) {
		return refused(CodeIDTokenExpired, "The ID token was refused: it expired at "+id.Expiry.UTC().Format(time.RFC3339)+".",
			"Check this computer's clock, then get a new token.", "exp")
	}
	if !id.NotBefore.IsZero() && now.Add(IDTokenLeeway).Before(id.NotBefore) {
		return refused(CodeIDTokenExpired, "The ID token was refused: it is not valid before "+id.NotBefore.UTC().Format(time.RFC3339)+".",
			"Check this computer's clock, then get a new token.", "nbf")
	}
	if id.AccessTokenHash != "" && t.AccessToken != "" {
		if want, ok := AccessTokenHash(id.Algorithm, t.AccessToken); ok && want != id.AccessTokenHash {
			return refused(CodeIDTokenAtHash, "The ID token was refused: its at_hash does not match the access token that came with it.",
				"The access token may have been swapped. Get a new token.", "at_hash")
		}
	}
	return id, nil
}

// AccessTokenHash returns the at_hash of accessToken for an ID token
// signed with alg (OpenID Connect Core section 3.1.3.6): base64url of the
// left half of the hash alg uses (SHA-256 for *256, SHA-384 for *384,
// SHA-512 for *512 and EdDSA). ok is false for another algorithm.
func AccessTokenHash(alg, accessToken string) (string, bool) {
	var h crypto.Hash
	switch {
	case alg == "EdDSA" || strings.HasSuffix(alg, "512"):
		h = crypto.SHA512
	case strings.HasSuffix(alg, "384"):
		h = crypto.SHA384
	case strings.HasSuffix(alg, "256"):
		h = crypto.SHA256
	default:
		return "", false
	}
	var sum []byte
	switch h {
	case crypto.SHA256:
		s := sha256.Sum256([]byte(accessToken))
		sum = s[:]
	case crypto.SHA384:
		s := sha512.Sum384([]byte(accessToken))
		sum = s[:]
	default:
		s := sha512.Sum512([]byte(accessToken))
		sum = s[:]
	}
	return base64.RawURLEncoding.EncodeToString(sum[:len(sum)/2]), true
}

// IDTokenExpired reports whether t's ID token cannot be sent at now: it
// has none, it cannot be read, or its exp is past, ExpiryLeeway early
// like the access token.
func (t Token) IDTokenExpired(now time.Time) bool {
	if t.IDToken == "" {
		return true
	}
	id, err := DecodeIDToken(t.IDToken)
	return err != nil || !now.Before(id.Expiry.Add(-ExpiryLeeway))
}

// ExpiredFor reports whether t cannot be sent at now: the access token
// has expired (Expired), or, when idToken says the ID token is sent
// instead (AUTH-14), the ID token has (IDTokenExpired).
func (t Token) ExpiredFor(now time.Time, idToken bool) bool {
	return t.Expired(now) || (idToken && t.IDTokenExpired(now))
}

func decodePart(part string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(part, "="))
	if err != nil {
		return err
	}
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' {
		return errs.New(errs.Parse, "not a JSON object")
	}
	return json.Unmarshal(b, v)
}

func claimString(claims map[string]json.RawMessage, name string) (string, bool, error) {
	raw, ok := claims[name]
	if !ok || string(raw) == "null" {
		return "", false, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", true, err
	}
	return s, true, nil
}

// claimAudience reads aud: a string or an array of strings.
func claimAudience(claims map[string]json.RawMessage) ([]string, error) {
	raw, ok := claims["aud"]
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		if one == "" {
			return nil, nil
		}
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, err
	}
	return slices.DeleteFunc(many, func(s string) bool { return s == "" }), nil
}

// claimTime reads a NumericDate claim (RFC 7519 section 2): seconds since
// the epoch, an integer or not.
func claimTime(claims map[string]json.RawMessage, name string) (time.Time, bool, error) {
	raw, ok := claims[name]
	if !ok || string(raw) == "null" {
		return time.Time{}, false, nil
	}
	f, err := strconv.ParseFloat(string(bytes.TrimSpace(raw)), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1e11 {
		return time.Time{}, true, errs.New(errs.Parse, "not a NumericDate")
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC(), true, nil
}

func idMalformed(msg, claim string) error {
	e := errs.New(errs.Parse, msg).WithCode(CodeIDTokenMalformed).
		WithHint("Check that the scope asks for openid and that the server is an OpenID Connect provider.")
	if claim != "" {
		e = e.WithDetail("claim", claim)
	}
	return e
}

// badClaim is the error of a claim that is missing, or of the wrong type
// (wrongType).
func badClaim(name string, wrongType bool) error {
	if wrongType {
		return idMalformed("The ID token's "+name+" claim has the wrong type.", name)
	}
	return idMalformed("The ID token has no "+name+" claim.", name)
}
