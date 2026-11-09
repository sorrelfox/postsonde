package request

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"hash"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

var jwtNow = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return jwtNow }

// jwtExpand renders with fixed variables and the fixed clock, so
// {{$timestamp}} is jwtNow too.
func jwtExpand() Expand {
	r := template.Resolver{
		Scopes: template.Chain{template.MapScope{Label: "Environment: dev", Variables: map[string]template.Variable{
			"user":   {Value: "zoë"},
			"secret": {Value: "top-{{n}}", Secret: true},
			"n":      {Value: "7"},
			"kid":    {Value: "key-1"},
			"alg":    {Value: "HS512"},
			"keyDir": {Value: "keys"},
			"hdr":    {Value: "X-Token"},
		}}},
		Now: fixedNow,
	}
	return func(s string) (string, error) {
		res := r.Render(s)
		return res.Text, res.Err()
	}
}

// decodeJWT splits a compact JWT into its header and payload JSON and the
// signing input and signature.
func decodeJWT(t *testing.T, tok string) (header, payload map[string]any, input string, sig []byte) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q has %d parts", tok, len(parts))
	}
	dec := func(s string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatalf("part %q: %v", s, err)
		}
		return b
	}
	if err := json.Unmarshal(dec(parts[0]), &header); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(dec(parts[1]), &payload); err != nil {
		t.Fatal(err)
	}
	return header, payload, parts[0] + "." + parts[1], dec(parts[2])
}

// verifyJWT verifies tok's signature with the public key (the secret for
// HS*) using only the standard library, as a JWT library does
// (RFC 7515, RFC 7518, RFC 8037).
func verifyJWT(t *testing.T, tok string, pub any) (header, payload map[string]any) {
	t.Helper()
	header, payload, input, sig := decodeJWT(t, tok)
	alg, _ := header["alg"].(string)
	hashes := map[string]crypto.Hash{"256": crypto.SHA256, "384": crypto.SHA384, "512": crypto.SHA512}
	ok := false
	switch {
	case alg == "EdDSA":
		ok = ed25519.Verify(pub.(ed25519.PublicKey), []byte(input), sig)
	case strings.HasPrefix(alg, "HS"):
		news := map[string]func() hash.Hash{"256": sha256.New, "384": sha512.New384, "512": sha512.New}
		m := hmac.New(news[alg[2:]], pub.([]byte))
		m.Write([]byte(input))
		ok = hmac.Equal(m.Sum(nil), sig)
	default:
		h := hashes[alg[2:]]
		d := h.New()
		d.Write([]byte(input))
		digest := d.Sum(nil)
		switch alg[:2] {
		case "RS":
			ok = rsa.VerifyPKCS1v15(pub.(*rsa.PublicKey), h, digest, sig) == nil
		case "PS":
			ok = rsa.VerifyPSS(pub.(*rsa.PublicKey), h, digest, sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) == nil
		case "ES":
			k := pub.(*ecdsa.PublicKey)
			n := len(sig) / 2
			ok = ecdsa.Verify(k, digest, new(big.Int).SetBytes(sig[:n]), new(big.Int).SetBytes(sig[n:]))
		}
	}
	if !ok {
		t.Fatalf("%s token does not verify: %s", alg, tok)
	}
	return header, payload
}

func pemKey(t *testing.T, key any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// TestAuthJWTHS256: the header and payload have their {{variables}} and
// dynamic values rendered, the claims come from the clock, and the token
// is sent as Authorization: Bearer and verifies.
func TestAuthJWTHS256(t *testing.T) {
	a := projectfile.Auth{
		Type:      projectfile.AuthJWT,
		JWTSecret: "{{secret}}",
		JWTHeader: `{"kid": "{{kid}}"}`,
		Payload:   `{"sub": "{{user}}", "at": {{$timestamp}}, "iat": 1}`,
		IssuedAt:  true, NotBefore: true, ExpiresIn: 600,
	}
	got, err := AuthWith(a, nil, AuthOptions{Expand: jwtExpand(), Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if got.JWT == nil || len(got.Headers) != 1 || got.Headers[0].Name != "Authorization" || got.Headers[0].Value != "Bearer "+got.JWT.Compact {
		t.Fatalf("Auth = %+v", got)
	}
	unix := jwtNow.Unix()
	if want := `{"alg":"HS256","typ":"JWT","kid":"key-1"}`; got.JWT.Header != want {
		t.Errorf("header = %s, want %s", got.JWT.Header, want)
	}
	// The typed iat wins over the added one.
	if want := `{"sub":"zoë","at":` + itoa(unix) + `,"iat":1,"nbf":` + itoa(unix) + `,"exp":` + itoa(unix+600) + `}`; got.JWT.Payload != want {
		t.Errorf("payload = %s, want %s", got.JWT.Payload, want)
	}
	verifyJWT(t, got.JWT.Compact, []byte("top-7"))
	if !slices.Contains(got.Secrets, got.JWT.Compact) || !slices.Contains(got.Secrets, "top-7") {
		t.Errorf("Secrets = %q, want the token and the secret", got.Secrets)
	}
	// Built again at the same time, the same token (HMAC is deterministic).
	again, _ := AuthWith(a, nil, AuthOptions{Expand: jwtExpand(), Now: fixedNow})
	if again.JWT.Compact != got.JWT.Compact {
		t.Error("the same input signed differently")
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// TestAuthJWTEveryAlgorithm (AUTH-16): a token made with every
// algorithm, the key typed as text or read from a file (relative to the
// project root), verifies with the standard library.
func TestAuthJWTEveryAlgorithm(t *testing.T) {
	root := t.TempDir()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	ec := map[string]*ecdsa.PrivateKey{}
	for alg, c := range map[string]elliptic.Curve{"ES256": elliptic.P256(), "ES384": elliptic.P384(), "ES512": elliptic.P521()} {
		if ec[alg], err = ecdsa.GenerateKey(c, rand.Reader); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "keys"), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, data string) string {
		if err := os.WriteFile(filepath.Join(root, "keys", name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return "{{keyDir}}/" + name
	}
	rsaPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}))

	type tc struct {
		alg       string
		secret    string
		file      string
		b64       bool
		verifyKey any
	}
	cases := []tc{
		{alg: "HS256", secret: "s3cr3t", verifyKey: []byte("s3cr3t")},
		{alg: "HS384", secret: base64.StdEncoding.EncodeToString([]byte("raw\x00bytes")), b64: true, verifyKey: []byte("raw\x00bytes")},
		{alg: "HS512", file: write("hs.txt", "from-file"), verifyKey: []byte("from-file")},
		{alg: "RS256", secret: rsaPEM, verifyKey: &rsaKey.PublicKey},
		{alg: "RS384", file: write("rsa.pem", pemKey(t, rsaKey)), verifyKey: &rsaKey.PublicKey},
		{alg: "RS512", secret: pemKey(t, rsaKey), verifyKey: &rsaKey.PublicKey},
		{alg: "PS256", secret: rsaPEM, verifyKey: &rsaKey.PublicKey},
		{alg: "PS384", file: "{{keyDir}}/rsa.pem", verifyKey: &rsaKey.PublicKey},
		{alg: "PS512", secret: rsaPEM, verifyKey: &rsaKey.PublicKey},
		{alg: "ES256", secret: pemKey(t, ec["ES256"]), verifyKey: &ec["ES256"].PublicKey},
		{alg: "ES384", file: write("ec384.pem", pemKey(t, ec["ES384"])), verifyKey: &ec["ES384"].PublicKey},
		{alg: "ES512", secret: pemKey(t, ec["ES512"]), verifyKey: &ec["ES512"].PublicKey},
		{alg: "EdDSA", file: filepath.Join(root, "keys", "ed.pem"), verifyKey: edKey.Public()},
	}
	if err := os.WriteFile(filepath.Join(root, "keys", "ed.pem"), []byte(pemKey(t, edKey)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.alg, func(t *testing.T) {
			a := projectfile.Auth{Type: projectfile.AuthJWT, Algorithm: c.alg, JWTSecret: c.secret, KeyFile: c.file, SecretBase64: c.b64,
				Payload: `{"sub":"1"}`, JWTID: true}
			got, err := AuthWith(a, nil, AuthOptions{Expand: jwtExpand(), Root: root, Now: fixedNow})
			if err != nil {
				t.Fatal(err)
			}
			h, p := verifyJWT(t, got.JWT.Compact, c.verifyKey)
			if h["alg"] != c.alg || p["sub"] != "1" || len(p["jti"].(string)) != 36 {
				t.Errorf("header %v payload %v", h, p)
			}
		})
	}
}

// TestAuthJWTPlacement: a custom header, no prefix, the query string.
func TestAuthJWTPlacement(t *testing.T) {
	base := projectfile.Auth{Type: projectfile.AuthJWT, JWTSecret: "k"}
	empty := ""
	token := strPtr("Token")

	a := base
	a.Key, a.Prefix = "{{hdr}}", &empty
	got, err := AuthWith(a, nil, AuthOptions{Expand: jwtExpand()})
	if err != nil || len(got.Headers) != 1 || got.Headers[0].Name != "X-Token" || got.Headers[0].Value != got.JWT.Compact {
		t.Errorf("custom header = %+v, %v", got, err)
	}
	a = base
	a.Prefix = token
	got, _ = AuthWith(a, nil, AuthOptions{})
	if got.Headers[0].Value != "Token "+got.JWT.Compact {
		t.Errorf("prefix = %+v", got.Headers)
	}
	a = base
	a.In = projectfile.APIKeyInQuery
	got, _ = AuthWith(a, nil, AuthOptions{})
	if len(got.Headers) != 0 || len(got.Query) != 1 || got.Query[0] != (QueryParam{Name: "access_token", Value: got.JWT.Compact}) {
		t.Errorf("query = %+v", got)
	}
	a.Key = "jwt"
	got, _ = AuthWith(a, nil, AuthOptions{})
	if got.Query[0].Name != "jwt" {
		t.Errorf("query name = %+v", got.Query)
	}
	// No key: nothing is sent. Disabled: nothing either.
	if got, err := AuthWith(projectfile.Auth{Type: projectfile.AuthJWT, Payload: `{}`}, nil, AuthOptions{}); err != nil || got.JWT != nil || len(got.Headers) != 0 {
		t.Errorf("no key = %+v, %v", got, err)
	}
	if got, err := AuthWith(projectfile.Auth{Type: projectfile.AuthJWT, JWTSecret: "k", Disabled: true}, nil, AuthOptions{}); err != nil || got.JWT != nil {
		t.Errorf("disabled = %+v, %v", got, err)
	}
	// A secret from local storage is used.
	got, err = AuthWith(projectfile.Auth{Type: projectfile.AuthJWT, LocalSecrets: []string{"secret"}}, map[string]string{"auth.secret": "local"}, AuthOptions{})
	if err != nil || got.JWT == nil {
		t.Fatalf("local secret = %+v, %v", got, err)
	}
	verifyJWT(t, got.JWT.Compact, []byte("local"))
}

func TestAuthJWTErrors(t *testing.T) {
	root := t.TempDir()
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	edPEM := pemKey(t, edKey)
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		auth  projectfile.Auth
		root  string
		kind  errs.Kind
		code  string
		field string
	}{
		{"unknown algorithm", projectfile.Auth{Algorithm: "none", JWTSecret: "k"}, "", errs.Unsupported, CodeAuthUnsupported, "algorithm"},
		{"algorithm from a variable", projectfile.Auth{Algorithm: "{{user}}", JWTSecret: "k"}, "", errs.Unsupported, CodeAuthUnsupported, "algorithm"},
		{"payload not JSON", projectfile.Auth{JWTSecret: "k", Payload: `{"sub": {{user}}}`}, "", errs.InvalidArgument, CodeAuthInvalid, "payload"},
		{"header not an object", projectfile.Auth{JWTSecret: "k", JWTHeader: `[]`}, "", errs.InvalidArgument, CodeAuthInvalid, "jwtHeader"},
		{"wrong key type", projectfile.Auth{Algorithm: "RS256", JWTSecret: edPEM}, "", errs.InvalidArgument, CodeAuthInvalid, "secret"},
		{"secret for RS256", projectfile.Auth{Algorithm: "RS256", JWTSecret: "k"}, "", errs.InvalidArgument, CodeAuthInvalid, "secret"},
		{"bad base64", projectfile.Auth{JWTSecret: "***", SecretBase64: true}, "", errs.InvalidArgument, CodeAuthInvalid, "secret"},
		{"key file missing", projectfile.Auth{Algorithm: "EdDSA", KeyFile: "nope.pem"}, root, errs.NotFound, CodeAuthKeyFileFailed, "keyFile"},
		{"key file relative without a project", projectfile.Auth{Algorithm: "EdDSA", KeyFile: "k.pem"}, "", errs.NotFound, CodeAuthKeyFileFailed, "keyFile"},
		{"key file is a directory", projectfile.Auth{Algorithm: "EdDSA", KeyFile: "dir"}, root, errs.IO, CodeAuthKeyFileFailed, "keyFile"},
		{"key file wrong key", projectfile.Auth{Algorithm: "ES256", KeyFile: "ed.pem"}, root, errs.InvalidArgument, CodeAuthInvalid, "keyFile"},
		{"header name", projectfile.Auth{JWTSecret: "k", Key: "X Token"}, "", errs.InvalidArgument, CodeAuthInvalid, "key"},
		{"prefix break", projectfile.Auth{JWTSecret: "k", Prefix: strPtr("a\nb")}, "", errs.InvalidArgument, CodeAuthInvalid, "prefix"},
		{"location", projectfile.Auth{JWTSecret: "k", In: "cookie"}, "", errs.Unsupported, CodeAuthUnsupported, "in"},
		{"secret missing here", projectfile.Auth{LocalSecrets: []string{"secret"}}, "", errs.InvalidArgument, CodeAuthSecretMissing, "secret"},
	}
	if err := os.WriteFile(filepath.Join(root, "ed.pem"), []byte(edPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.auth.Type = projectfile.AuthJWT
			_, err := AuthWith(c.auth, nil, AuthOptions{Expand: jwtExpand(), Root: c.root})
			e := wantAuthErr(t, err, c.kind, c.code, "jwt", c.field)
			if strings.Contains(e.Error(), "PRIVATE") || strings.Contains(e.Error(), "zoë") {
				t.Errorf("the error shows a value: %v", e)
			}
		})
	}
	// A key file makes a missing typed secret irrelevant.
	a := projectfile.Auth{Type: projectfile.AuthJWT, Algorithm: "EdDSA", KeyFile: "ed.pem", LocalSecrets: []string{"secret"}}
	if got, err := AuthWith(a, nil, AuthOptions{Root: root}); err != nil || got.JWT == nil {
		t.Errorf("key file with a missing secret = %+v, %v", got, err)
	}
}

// TestBuildHTTPSendsJWT: the build carries the token and its secrets, and
// the server gets it as sent.
func TestBuildHTTPSendsJWT(t *testing.T) {
	var gotAuth, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotQuery = r.Header.Get("Authorization"), r.URL.Query().Get("access_token")
	}))
	defer srv.Close()
	client := httpclient.New(netx.NewDirect(netx.Options{}), httpclient.TransportOptions{})
	defer client.CloseIdleConnections()

	f := projectfile.HTTPRequest{Method: "GET", URL: srv.URL + "/x"}
	auth := projectfile.Auth{Type: projectfile.AuthJWT, JWTSecret: "{{secret}}", Payload: `{"sub":"{{user}}"}`, IssuedAt: true}
	b, err := BuildHTTP(f, auth, HTTPOptions{Expand: jwtExpand(), Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if b.JWT == nil || !slices.Contains(b.AuthSecrets, b.JWT.Compact) || !slices.Contains(b.AuthSecrets, "top-7") {
		t.Fatalf("build = %+v", b)
	}
	if _, err := client.Send(t.Context(), b.Request); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+b.JWT.Compact {
		t.Errorf("server got Authorization %q", gotAuth)
	}
	_, p := verifyJWT(t, strings.TrimPrefix(gotAuth, "Bearer "), []byte("top-7"))
	if p["sub"] != "zoë" || p["iat"] != float64(jwtNow.Unix()) {
		t.Errorf("payload = %v", p)
	}

	auth.In = projectfile.APIKeyInQuery
	b, err = BuildHTTP(f, auth, HTTPOptions{Expand: jwtExpand(), Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Send(t.Context(), b.Request); err != nil {
		t.Fatal(err)
	}
	if gotQuery != b.JWT.Compact || gotAuth != "" {
		t.Errorf("query: server got %q, Authorization %q", gotQuery, gotAuth)
	}
}
