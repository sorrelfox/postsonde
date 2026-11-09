package jwt

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

// RFC 7515 appendix A.1: HMAC SHA-256.
func TestSignRFC7515HS256(t *testing.T) {
	key := b64(t, "AyM1SysPpbyDfgZld3umj1qzKObwVMkoqQ-EstJQLr_T-1qS0gZH75aKtMN3Yj0iPS4hcgUuTwjAzZr1Z9CAow")
	input := "eyJ0eXAiOiJKV1QiLA0KICJhbGciOiJIUzI1NiJ9" +
		"." +
		"eyJpc3MiOiJqb2UiLA0KICJleHAiOjEzMDA4MTkzODAsDQogImh0dHA6Ly9leGFtcGxlLmNvbS9pc19yb290Ijp0cnVlfQ"
	sig, err := Sign(HS256, key, []byte(input), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := base64.RawURLEncoding.EncodeToString(sig), "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"; got != want {
		t.Errorf("signature = %s, want %s", got, want)
	}
	// The same key as a base64url secret, as the Auth tab takes it.
	k, err := ParseKey(HS256, []byte("AyM1SysPpbyDfgZld3umj1qzKObwVMkoqQ-EstJQLr_T-1qS0gZH75aKtMN3Yj0iPS4hcgUuTwjAzZr1Z9CAow"), true)
	if err != nil || !bytes.Equal(k.([]byte), key) {
		t.Errorf("ParseKey base64url = %x, %v", k, err)
	}
}

// RFC 7515 appendix A.3: ECDSA P-256 SHA-256. The signature is
// randomized, so the example's signature is verified with the example's
// key the way ours are laid out (R || S), and ours verifies too.
func TestSignRFC7515ES256(t *testing.T) {
	d := new(big.Int).SetBytes(b64(t, "jpsQnnGQmL-YBIffH1136cspYG6-0iY7X1fCE9-E9LI"))
	x := new(big.Int).SetBytes(b64(t, "f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU"))
	y := new(big.Int).SetBytes(b64(t, "x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0"))
	der := sec1(t, elliptic.P256(), d)
	key, err := x509.ParseECPrivateKey(der)
	if err != nil {
		t.Fatal(err)
	}
	if key.X.Cmp(x) != 0 || key.Y.Cmp(y) != 0 {
		t.Fatal("the RFC's d does not give its x and y")
	}
	input := "eyJhbGciOiJFUzI1NiJ9.eyJpc3MiOiJqb2UiLA0KICJleHAiOjEzMDA4MTkzODAsDQogImh0dHA6Ly9leGFtcGxlLmNvbS9pc19yb290Ijp0cnVlfQ"
	rfcSig := b64(t, "DtEhU3ljbEg8L38VWAfUAqOyKAM6-Xx-F4GawxaepmXFCgfTjDxw5djxLa8ISlSApmWQxfKTUJqPP3-Kg6NU1Q")
	if !verify(t, ES256, &key.PublicKey, []byte(input), rfcSig) {
		t.Error("the RFC's signature does not verify as R || S")
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	k, err := ParseKey(ES256, pemKey, false)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := Sign(ES256, k, []byte(input), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 64 || !verify(t, ES256, &key.PublicKey, []byte(input), sig) {
		t.Errorf("our ES256 signature (%d bytes) does not verify", len(sig))
	}
}

// RFC 8037 appendix A.4: Ed25519.
func TestSignRFC8037EdDSA(t *testing.T) {
	seed := b64(t, "nWGxne_9WmC6hEr0kuwsxERJxWl7MmkZcDusAxyuf2A")
	priv := ed25519.NewKeyFromSeed(seed)
	if got := base64.RawURLEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)); got != "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo" {
		t.Fatalf("public key = %s", got)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ParseKey(EdDSA, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), false)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := Sign(EdDSA, k, []byte("eyJhbGciOiJFZERTQSJ9.RXhhbXBsZSBvZiBFZDI1NTE5IHNpZ25pbmc"), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "hgyY0il_MGCjP0JzlnLWG1PPOt7-09PGcvMg3AIbQR6dWbhijcNR4ki4iylGjg5BhVsPt9g7sVvpAr_MuM0KAg"
	if got := base64.RawURLEncoding.EncodeToString(sig); got != want {
		t.Errorf("signature = %s, want %s", got, want)
	}
}

// sec1 returns the SEC 1 DER of the private key d on c.
func sec1(t *testing.T, c elliptic.Curve, d *big.Int) []byte {
	t.Helper()
	k, err := ecdsa.ParseRawPrivateKey(c, d.FillBytes(make([]byte, (c.Params().BitSize+7)/8)))
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// verify checks sig over input with the public key pub, as a JWT library
// would (RFC 7518), using only the standard library.
func verify(t *testing.T, alg string, pub crypto.PublicKey, input, sig []byte) bool {
	t.Helper()
	if IsHMAC(alg) {
		m := hmac.New(newHash(hashOf(alg)), pub.([]byte))
		m.Write(input)
		return hmac.Equal(m.Sum(nil), sig)
	}
	if alg == EdDSA {
		return ed25519.Verify(pub.(ed25519.PublicKey), input, sig)
	}
	h := hashOf(alg)
	d := h.New()
	d.Write(input)
	digest := d.Sum(nil)
	switch alg[:2] {
	case "RS":
		return rsa.VerifyPKCS1v15(pub.(*rsa.PublicKey), h, digest, sig) == nil
	case "PS":
		return rsa.VerifyPSS(pub.(*rsa.PublicKey), h, digest, sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: h}) == nil
	}
	k := pub.(*ecdsa.PublicKey)
	size := (k.Curve.Params().BitSize + 7) / 8
	if len(sig) != 2*size {
		return false
	}
	r, s := new(big.Int).SetBytes(sig[:size]), new(big.Int).SetBytes(sig[size:])
	return ecdsa.Verify(k, digest, r, s)
}

type testKeys struct {
	rsaPKCS1, rsaPKCS8, p256, p384, p521, ed []byte
	rsa                                      *rsa.PrivateKey
	ec                                       map[string]*ecdsa.PrivateKey
	edKey                                    ed25519.PrivateKey
}

func keys(t *testing.T) testKeys {
	t.Helper()
	var k testKeys
	var err error
	if k.rsa, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	k.rsaPKCS1 = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k.rsa)})
	der, _ := x509.MarshalPKCS8PrivateKey(k.rsa)
	k.rsaPKCS8 = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	k.ec = map[string]*ecdsa.PrivateKey{}
	for alg, c := range map[string]elliptic.Curve{ES256: elliptic.P256(), ES384: elliptic.P384(), ES512: elliptic.P521()} {
		ek, err := ecdsa.GenerateKey(c, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		k.ec[alg] = ek
		var block *pem.Block
		if alg == ES384 {
			der, _ := x509.MarshalPKCS8PrivateKey(ek)
			block = &pem.Block{Type: "PRIVATE KEY", Bytes: der}
		} else {
			der, _ := x509.MarshalECPrivateKey(ek)
			block = &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}
		}
		p := pem.EncodeToMemory(block)
		switch alg {
		case ES256:
			// openssl ecparam -genkey writes the parameters first.
			k.p256 = append([]byte("-----BEGIN EC PARAMETERS-----\nBggqhkjOPQMBBw==\n-----END EC PARAMETERS-----\n"), p...)
		case ES384:
			k.p384 = p
		default:
			k.p521 = p
		}
	}
	_, k.edKey, _ = ed25519.GenerateKey(rand.Reader)
	der, _ = x509.MarshalPKCS8PrivateKey(k.edKey)
	k.ed = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return k
}

// Every algorithm: Build's token has three parts, the header names the
// algorithm, and the signature verifies with the public key.
func TestBuildEveryAlgorithmVerifies(t *testing.T) {
	k := keys(t)
	secret := []byte("a-string-secret-at-least-256-bits-long")
	cases := []struct {
		alg string
		key []byte
		pub crypto.PublicKey
	}{
		{HS256, secret, secret}, {HS384, secret, secret}, {HS512, secret, secret},
		{RS256, k.rsaPKCS1, &k.rsa.PublicKey}, {RS384, k.rsaPKCS8, &k.rsa.PublicKey}, {RS512, k.rsaPKCS1, &k.rsa.PublicKey},
		{PS256, k.rsaPKCS8, &k.rsa.PublicKey}, {PS384, k.rsaPKCS1, &k.rsa.PublicKey}, {PS512, k.rsaPKCS8, &k.rsa.PublicKey},
		{ES256, k.p256, &k.ec[ES256].PublicKey}, {ES384, k.p384, &k.ec[ES384].PublicKey}, {ES512, k.p521, &k.ec[ES512].PublicKey},
		{EdDSA, k.ed, k.edKey.Public()},
	}
	for _, c := range cases {
		t.Run(c.alg, func(t *testing.T) {
			key, err := ParseKey(c.alg, c.key, false)
			if err != nil {
				t.Fatal(err)
			}
			tok, err := Build(Input{Algorithm: c.alg, Payload: `{"sub":"1234567890"}`, Key: key})
			if err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(tok.Compact, ".")
			if len(parts) != 3 {
				t.Fatalf("token has %d parts", len(parts))
			}
			var h map[string]any
			if err := json.Unmarshal(b64(t, parts[0]), &h); err != nil || h["alg"] != c.alg || h["typ"] != "JWT" {
				t.Errorf("header = %s (%v)", b64(t, parts[0]), err)
			}
			if !verify(t, c.alg, c.pub, []byte(parts[0]+"."+parts[1]), b64(t, parts[2])) {
				t.Error("the signature does not verify")
			}
		})
	}
}

// RS256 is deterministic: the same input signs the same.
func TestRS256Deterministic(t *testing.T) {
	k := keys(t)
	key, err := ParseKey(RS256, k.rsaPKCS1, false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	a, _ := Build(Input{Algorithm: RS256, Payload: `{"a":1}`, Key: key, Now: now, Claims: Claims{IssuedAt: true}})
	b, _ := Build(Input{Algorithm: RS256, Payload: `{"a":1}`, Key: key, Now: now, Claims: Claims{IssuedAt: true}})
	if a.Compact == "" || a.Compact != b.Compact {
		t.Errorf("tokens differ:\n%s\n%s", a.Compact, b.Compact)
	}
}

func TestBuildClaimsAndOrder(t *testing.T) {
	now := time.Unix(1700000000, 0)
	tok, err := Build(Input{
		Algorithm: HS256,
		Header:    ` { "kid" : "k1", "alg": "none", "typ": "at+jwt" } `,
		Payload:   "{\n  \"sub\": \"u1\",\n  \"iat\": 5,\n  \"n\": 1.50,\n  \"o\": {\"b\": [1, 2]}\n}",
		Claims:    Claims{IssuedAt: true, NotBefore: true, ExpiresIn: 3600, ID: true},
		Key:       []byte("k"),
		Now:       now,
		Rand:      bytes.NewReader(make([]byte, 64)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"alg":"HS256","kid":"k1","typ":"at+jwt"}`; tok.Header != want {
		t.Errorf("header = %s, want %s", tok.Header, want)
	}
	want := `{"sub":"u1","iat":5,"n":1.50,"o":{"b":[1,2]},"nbf":1700000000,"exp":1700003600,"jti":"00000000-0000-4000-8000-000000000000"}`
	if tok.Payload != want {
		t.Errorf("payload = %s\nwant      %s", tok.Payload, want)
	}
	parts := strings.Split(tok.Compact, ".")
	if string(b64(t, parts[1])) != want || string(b64(t, parts[0])) != tok.Header {
		t.Error("the encoded parts differ from Header and Payload")
	}
	// Empty payload: {} and the claims.
	tok, err = Build(Input{Key: []byte("k"), Now: now, Claims: Claims{IssuedAt: true}})
	if err != nil || tok.Payload != `{"iat":1700000000}` || tok.Header != `{"alg":"HS256","typ":"JWT"}` {
		t.Errorf("empty payload: %s %s %v", tok.Header, tok.Payload, err)
	}
}

func TestBuildErrors(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		code string
		part string
	}{
		{"unknown alg", Input{Algorithm: "none", Key: []byte("k")}, CodeUnsupportedAlgorithm, PartAlgorithm},
		{"lower-case alg", Input{Algorithm: "hs256", Key: []byte("k")}, CodeUnsupportedAlgorithm, PartAlgorithm},
		{"payload array", Input{Payload: `[1]`, Key: []byte("k")}, CodeInvalidJSON, PartPayload},
		{"payload syntax", Input{Payload: `{"a": secret}`, Key: []byte("k")}, CodeInvalidJSON, PartPayload},
		{"payload trailing", Input{Payload: `{} {}`, Key: []byte("k")}, CodeInvalidJSON, PartPayload},
		{"payload duplicate", Input{Payload: `{"a":1,"a":2}`, Key: []byte("k")}, CodeInvalidJSON, PartPayload},
		{"payload open", Input{Payload: `{"a":1`, Key: []byte("k")}, CodeInvalidJSON, PartPayload},
		{"header string", Input{Header: `"x"`, Key: []byte("k")}, CodeInvalidJSON, PartHeader},
		{"key type", Input{Algorithm: RS256, Key: []byte("k")}, CodeInvalidKey, PartKey},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Build(c.in)
			e, ok := err.(*errs.Error)
			if !ok || e.Code != c.code || e.Details[DetailPart] != c.part {
				t.Fatalf("err = %#v, want code %s part %s", err, c.code, c.part)
			}
			if strings.Contains(e.Error(), "secret") && c.name == "payload syntax" {
				t.Error("the message quotes the payload")
			}
		})
	}
}

func TestParseKeyErrors(t *testing.T) {
	k := keys(t)
	pubDER, _ := x509.MarshalPKIXPublicKey(&k.rsa.PublicKey)
	pub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	cases := []struct {
		name string
		alg  string
		data []byte
		b64  bool
		want string
	}{
		{"not pem", RS256, []byte("hello"), false, "not a PEM"},
		{"public key", RS256, pub, false, "public key"},
		{"encrypted", RS256, []byte("-----BEGIN ENCRYPTED PRIVATE KEY-----\nAAAA\n-----END ENCRYPTED PRIVATE KEY-----\n"), false, "encrypted"},
		{"openssh", EdDSA, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n"), false, "OpenSSH"},
		{"rsa for es", ES256, k.rsaPKCS1, false, "needs an EC private key"},
		{"wrong curve", ES256, k.p384, false, "P-256"},
		{"ec for rsa", PS256, k.p256, false, "needs an RSA private key"},
		{"ec for eddsa", EdDSA, k.p521, false, "Ed25519"},
		{"garbage der", RS256, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte{1, 2}}), false, "could not be read"},
		{"bad base64", HS256, []byte("a*b"), true, "base64"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseKey(c.alg, c.data, c.b64)
			e, ok := err.(*errs.Error)
			if !ok || e.Code != CodeInvalidKey || !strings.Contains(e.Message, c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	// Standard base64 with padding and line breaks.
	got, err := ParseKey(HS512, []byte("c2Vj\ncmV0Pw=="), true)
	if err != nil || string(got.([]byte)) != "secret?" {
		t.Errorf("base64 = %q, %v", got, err)
	}
}
