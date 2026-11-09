// Package jwt builds and signs JSON Web Tokens (RFC 7519) in JWS compact
// serialization (RFC 7515), for the JWT bearer auth (AUTH-16, docs/auth.md).
//
// Algorithms (RFC 7518 section 3, RFC 8037): HS256/384/512 (HMAC with a
// shared secret), RS256/384/512 (RSASSA-PKCS1-v1_5), PS256/384/512
// (RSASSA-PSS, salt as long as the hash), ES256/384/512 (ECDSA on P-256,
// P-384, P-521, the signature as R || S) and EdDSA (Ed25519). Private keys
// are PEM: PKCS#1 ("RSA PRIVATE KEY"), SEC 1 ("EC PRIVATE KEY") or PKCS#8
// ("PRIVATE KEY"). Encrypted keys are not read.
//
// Nothing here verifies tokens: PostSonde only makes them. Standard
// library only.
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
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"hash"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// The algorithms Sign knows ("alg" header values).
const (
	HS256 = "HS256"
	HS384 = "HS384"
	HS512 = "HS512"
	RS256 = "RS256"
	RS384 = "RS384"
	RS512 = "RS512"
	PS256 = "PS256"
	PS384 = "PS384"
	PS512 = "PS512"
	ES256 = "ES256"
	ES384 = "ES384"
	ES512 = "ES512"
	EdDSA = "EdDSA"
)

// Algorithms lists the algorithms in the order the Auth tab offers them.
var Algorithms = []string{HS256, HS384, HS512, RS256, RS384, RS512, PS256, PS384, PS512, ES256, ES384, ES512, EdDSA}

// DefaultAlgorithm is the algorithm of an auth that names none.
const DefaultAlgorithm = HS256

// Error codes. Every error is an *errs.Error with DetailPart.
const (
	// CodeUnsupportedAlgorithm: an "alg" this package cannot sign with
	// (kind unsupported).
	CodeUnsupportedAlgorithm = "jwt.unsupported_algorithm"
	// CodeInvalidJSON: the header or payload is not a JSON object (kind
	// invalid_argument).
	CodeInvalidJSON = "jwt.invalid_json"
	// CodeInvalidKey: the key cannot sign with the algorithm: not PEM, the
	// wrong key type or curve, encrypted, a public key, bad base64 (kind
	// invalid_argument).
	CodeInvalidKey = "jwt.invalid_key"
)

// DetailPart names what an error is about: PartAlgorithm, PartHeader,
// PartPayload or PartKey.
const DetailPart = "part"

// Parts, for DetailPart.
const (
	PartAlgorithm = "algorithm"
	PartHeader    = "header"
	PartPayload   = "payload"
	PartKey       = "key"
)

// Supported reports whether Sign knows alg (case-sensitive, as in the
// "alg" header).
func Supported(alg string) bool {
	for _, a := range Algorithms {
		if a == alg {
			return true
		}
	}
	return false
}

// IsHMAC reports whether alg signs with a shared secret rather than a
// private key.
func IsHMAC(alg string) bool { return alg == HS256 || alg == HS384 || alg == HS512 }

func unsupported(alg string) error {
	return errs.New(errs.Unsupported, "PostSonde cannot sign a JWT with this algorithm.").
		WithCode(CodeUnsupportedAlgorithm).
		WithHint("Choose one of HS256/384/512, RS256/384/512, PS256/384/512, ES256/384/512 or EdDSA.").
		WithDetail(DetailPart, PartAlgorithm).
		WithDetail("algorithm", clip(alg))
}

func invalidKey(msg, hint string) error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidKey).WithHint(hint).WithDetail(DetailPart, PartKey)
}

func clip(s string) string {
	if len(s) > 16 {
		return s[:16] + "…"
	}
	return s
}

// ParseKey returns the signing key alg needs from data: for HS* the
// secret's bytes (base64-decoded first when isBase64: standard or URL
// alphabet, padded or not, whitespace ignored), else a private key read
// from PEM. The key is a []byte, *rsa.PrivateKey, *ecdsa.PrivateKey (on
// the curve alg names) or ed25519.PrivateKey.
func ParseKey(alg string, data []byte, isBase64 bool) (any, error) {
	if !Supported(alg) {
		return nil, unsupported(alg)
	}
	if IsHMAC(alg) {
		if !isBase64 {
			return bytes.Clone(data), nil
		}
		key, err := decodeBase64(string(data))
		if err != nil {
			return nil, invalidKey("The JWT secret is not valid base64.",
				"Turn off “Secret is base64” to use the text as it is, or fix the encoding.")
		}
		return key, nil
	}
	var block *pem.Block
	rest := data
	for {
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, invalidKey("The JWT signing key is not a PEM private key.",
				"Paste the private key with its -----BEGIN … PRIVATE KEY----- lines, or choose a .pem file.")
		}
		if strings.HasSuffix(block.Type, "PRIVATE KEY") || strings.Contains(block.Type, "PUBLIC KEY") || block.Type == "CERTIFICATE" {
			break
		}
		// Skip other blocks, such as "EC PARAMETERS" before an EC key.
	}
	switch {
	case block.Type == "ENCRYPTED PRIVATE KEY" || block.Headers["Proc-Type"] != "":
		return nil, invalidKey("The JWT signing key is encrypted.",
			"Decrypt it first (for example openssl pkey -in key.pem -out plain.pem).")
	case block.Type == "OPENSSH PRIVATE KEY":
		return nil, invalidKey("The JWT signing key is in OpenSSH format.",
			"Convert it to PKCS#8 PEM (ssh-keygen -p -m PKCS8 -f key).")
	case strings.Contains(block.Type, "PUBLIC KEY") || block.Type == "CERTIFICATE":
		return nil, invalidKey("The JWT key is a public key or a certificate; signing needs the private key.",
			"Use the private key that belongs to it.")
	}
	var key any
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	}
	if err != nil {
		return nil, invalidKey("The JWT signing key could not be read.",
			"Use an unencrypted PKCS#1, SEC 1 or PKCS#8 PEM private key.")
	}
	if k, ok := key.(ed25519.PrivateKey); ok {
		key = k
	} else if k, ok := key.(*ed25519.PrivateKey); ok {
		key = *k
	}
	return checkKey(alg, key)
}

func checkKey(alg string, key any) (any, error) {
	switch alg[:2] {
	case "RS", "PS":
		if _, ok := key.(*rsa.PrivateKey); !ok {
			return nil, invalidKey(alg+" needs an RSA private key; this is "+keyKind(key)+".",
				"Choose the algorithm that matches the key, or use an RSA key.")
		}
	case "ES":
		k, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, invalidKey(alg+" needs an EC private key; this is "+keyKind(key)+".",
				"Choose the algorithm that matches the key, or use an EC key.")
		}
		want := curveOf(alg)
		if k.Curve != want {
			return nil, invalidKey(alg+" needs a key on "+want.Params().Name+"; this key is on "+k.Curve.Params().Name+".",
				"ES256 signs with P-256, ES384 with P-384 and ES512 with P-521.")
		}
	case "Ed":
		if _, ok := key.(ed25519.PrivateKey); !ok {
			return nil, invalidKey("EdDSA needs an Ed25519 private key; this is "+keyKind(key)+".",
				"Choose the algorithm that matches the key, or use an Ed25519 key.")
		}
	}
	return key, nil
}

func keyKind(key any) string {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return "an RSA key"
	case *ecdsa.PrivateKey:
		return "an EC key (" + k.Curve.Params().Name + ")"
	case ed25519.PrivateKey:
		return "an Ed25519 key"
	}
	return "another kind of key"
}

func curveOf(alg string) elliptic.Curve {
	switch alg {
	case ES384:
		return elliptic.P384()
	case ES512:
		return elliptic.P521()
	}
	return elliptic.P256()
}

func hashOf(alg string) crypto.Hash {
	switch alg[2:] {
	case "384":
		return crypto.SHA384
	case "512":
		return crypto.SHA512
	}
	return crypto.SHA256
}

func newHash(h crypto.Hash) func() hash.Hash {
	switch h {
	case crypto.SHA384:
		return sha512.New384
	case crypto.SHA512:
		return sha512.New
	}
	return sha256.New
}

func decodeBase64(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimRight(s, "=")
	if strings.ContainsAny(s, "-_") {
		return base64.RawURLEncoding.DecodeString(s)
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// Sign returns the signature of signingInput with key (from ParseKey) as
// alg defines it. rand is used by PS* and ES*, whose signatures are
// randomized; nil means crypto/rand.
func Sign(alg string, key any, signingInput []byte, random io.Reader) ([]byte, error) {
	if !Supported(alg) {
		return nil, unsupported(alg)
	}
	if random == nil {
		random = rand.Reader
	}
	if IsHMAC(alg) {
		secret, ok := key.([]byte)
		if !ok {
			return nil, invalidKey(alg+" needs a secret, not a private key.", "Choose an RSA, EC or EdDSA algorithm for a private key.")
		}
		m := hmac.New(newHash(hashOf(alg)), secret)
		m.Write(signingInput)
		return m.Sum(nil), nil
	}
	key, err := checkKey(alg, key)
	if err != nil {
		return nil, err
	}
	if alg == EdDSA {
		return ed25519.Sign(key.(ed25519.PrivateKey), signingInput), nil
	}
	h := hashOf(alg)
	d := h.New()
	d.Write(signingInput)
	digest := d.Sum(nil)
	var sig []byte
	switch alg[:2] {
	case "RS":
		sig, err = rsa.SignPKCS1v15(random, key.(*rsa.PrivateKey), h, digest)
	case "PS":
		sig, err = rsa.SignPSS(random, key.(*rsa.PrivateKey), h, digest, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	default: // ES
		k := key.(*ecdsa.PrivateKey)
		r, s, e := ecdsa.Sign(random, k, digest)
		if e != nil {
			err = e
			break
		}
		size := (k.Curve.Params().BitSize + 7) / 8
		sig = make([]byte, 2*size)
		r.FillBytes(sig[:size])
		s.FillBytes(sig[size:])
	}
	if err != nil {
		return nil, errs.Wrap(err, errs.InvalidArgument, "The JWT could not be signed with this key.").
			WithCode(CodeInvalidKey).
			WithHint("Check that the key is large enough for the algorithm (RSA keys of 2048 bits or more).").
			WithDetail(DetailPart, PartKey)
	}
	return sig, nil
}

// Claims are the registered claims Build can add (RFC 7519 section 4.1).
// A claim the payload already has is left as it is.
type Claims struct {
	// IssuedAt adds "iat": the time of Build.
	IssuedAt bool
	// NotBefore adds "nbf": the time of Build.
	NotBefore bool
	// ID adds "jti": a random UUID (version 4).
	ID bool
	// ExpiresIn, when positive, adds "exp": the time of Build plus this
	// many seconds.
	ExpiresIn int64
}

// Input is what Build signs.
type Input struct {
	// Algorithm is the "alg"; "" is DefaultAlgorithm.
	Algorithm string
	// Header is a JSON object of header parameters besides "alg" (which
	// is always Algorithm) and "typ" (which defaults to "JWT"); "" for
	// none.
	Header string
	// Payload is the claims set, a JSON object; "" is {}.
	Payload string
	// Claims are the registered claims to add.
	Claims Claims
	// Key is the signing key, from ParseKey.
	Key any
	// Now is the time of the claims; the zero time is time.Now.
	Now time.Time
	// Rand is the randomness of jti and of PS and ES signatures; nil is
	// crypto/rand.
	Rand io.Reader
}

// Token is a built JWT.
type Token struct {
	// Compact is the token as sent: header.payload.signature.
	Compact string
	// Header and Payload are the JSON of its two parts, compact, members
	// in the order typed, the added ones last.
	Header  string
	Payload string
}

// Build returns the token of in: the header's and payload's JSON made
// compact (members in the order typed; a duplicate member is refused),
// the claims added, both base64url encoded (no padding), then signed.
func Build(in Input) (Token, error) {
	alg := in.Algorithm
	if alg == "" {
		alg = DefaultAlgorithm
	}
	if !Supported(alg) {
		return Token{}, unsupported(alg)
	}
	hdr, err := members(in.Header, PartHeader)
	if err != nil {
		return Token{}, err
	}
	algJSON, _ := json.Marshal(alg)
	head := []member{{"alg", algJSON}}
	if !has(hdr, "typ") {
		head = append(head, member{"typ", json.RawMessage(`"JWT"`)})
	}
	for _, m := range hdr {
		if m.name != "alg" {
			head = append(head, m)
		}
	}

	body, err := members(in.Payload, PartPayload)
	if err != nil {
		return Token{}, err
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	random := in.Rand
	if random == nil {
		random = rand.Reader
	}
	unix := now.Unix()
	add := func(name string, v json.RawMessage) {
		if !has(body, name) {
			body = append(body, member{name, v})
		}
	}
	if in.Claims.IssuedAt {
		add("iat", json.RawMessage(strconv.FormatInt(unix, 10)))
	}
	if in.Claims.NotBefore {
		add("nbf", json.RawMessage(strconv.FormatInt(unix, 10)))
	}
	if in.Claims.ExpiresIn > 0 {
		add("exp", json.RawMessage(strconv.FormatInt(unix+in.Claims.ExpiresIn, 10)))
	}
	if in.Claims.ID && !has(body, "jti") {
		id, err := uuid(random)
		if err != nil {
			return Token{}, errs.Wrap(err, errs.Internal, "No random JWT ID could be made.")
		}
		add("jti", json.RawMessage(`"`+id+`"`))
	}

	h, p := object(head), object(body)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	sig, err := Sign(alg, in.Key, []byte(input), random)
	if err != nil {
		return Token{}, err
	}
	return Token{Compact: input + "." + base64.RawURLEncoding.EncodeToString(sig), Header: string(h), Payload: string(p)}, nil
}

type member struct {
	name  string
	value json.RawMessage
}

func has(ms []member, name string) bool {
	for _, m := range ms {
		if m.name == name {
			return true
		}
	}
	return false
}

// members reads the members of the JSON object text, in order, each value
// compacted. Blank text is no members.
func members(text, part string) ([]member, error) {
	what := "header"
	if part == PartPayload {
		what = "payload"
	}
	bad := func(detail string) error {
		return errs.New(errs.InvalidArgument, "The JWT "+what+" is not a JSON object: "+detail+".").
			WithCode(CodeInvalidJSON).
			WithHint("Write the "+what+" as a JSON object, like {\"sub\": \"{{userId}}\"}; strings need double quotes.").
			WithDetail(DetailPart, part)
	}
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, bad(jsonProblem(err))
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, bad("it does not start with {")
	}
	var out []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, bad(jsonProblem(err))
		}
		name, _ := tok.(string)
		if has(out, name) {
			return nil, bad("the member " + strconv.Quote(clip(name)) + " appears twice")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, bad(jsonProblem(err))
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			return nil, bad(jsonProblem(err))
		}
		out = append(out, member{name, buf.Bytes()})
	}
	if _, err := dec.Token(); err != nil {
		return nil, bad(jsonProblem(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, bad("there is text after the closing }")
	}
	return out, nil
}

// jsonProblem describes a JSON syntax error without quoting the text,
// which may hold secrets.
func jsonProblem(err error) string {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return "invalid syntax at byte " + strconv.FormatInt(se.Offset, 10)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "it ends early"
	}
	return "invalid syntax"
}

func object(ms []member) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		name, _ := json.Marshal(m.name)
		b.Write(name)
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// uuid returns a random version 4 UUID.
func uuid(r io.Reader) (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}
