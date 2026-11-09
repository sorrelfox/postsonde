// Package oauth1 signs HTTP requests with OAuth 1.0 (RFC 5849, AUTH-15):
// the signature base string of section 3.4.1 over the method, the base
// string URI, the query and form body parameters and the protocol
// parameters, signed with HMAC-SHA1, HMAC-SHA256, RSA-SHA1 or PLAINTEXT,
// and the protocol parameters written as an Authorization header or as
// name=value pairs for a query string or form body. The optional body hash
// (oauth_body_hash, the OAuth Request Body Hash extension) covers bodies
// that are not form-encoded.
//
// Standard library only. See docs/auth.md#oauth-10-auth-15.
package oauth1

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"hash"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Signature methods.
const (
	HMACSHA1   = "HMAC-SHA1"
	HMACSHA256 = "HMAC-SHA256"
	RSASHA1    = "RSA-SHA1"
	PLAINTEXT  = "PLAINTEXT"
)

// DefaultMethod is the signature method of an auth that names none.
const DefaultMethod = HMACSHA1

// Version is the oauth_version sent unless it is left out.
const Version = "1.0"

// Error codes.
const (
	// CodeUnsupportedMethod: a signature method this package does not
	// know. Details: method.
	CodeUnsupportedMethod = "oauth1.unsupported_method"
	// CodeInvalidURL: the request URL has no scheme or host.
	CodeInvalidURL = "oauth1.invalid_url"
	// CodeRSAKeyMissing: RSA-SHA1 without a private key.
	CodeRSAKeyMissing = "oauth1.rsa_key_missing"
)

// Supported reports whether method is a signature method Sign knows.
func Supported(method string) bool {
	switch method {
	case HMACSHA1, HMACSHA256, RSASHA1, PLAINTEXT:
		return true
	}
	return false
}

// Param is a parameter, decoded (not percent-encoded).
type Param struct {
	Name  string
	Value string
}

// Encode percent-encodes s as RFC 5849 section 3.6 says: every byte of
// its UTF-8 text except the unreserved characters (letters, digits, "-",
// ".", "_", "~") becomes %XX with upper-case hex digits. A space is %20,
// never "+".
func Encode(s string) string {
	const hex = "0123456789ABCDEF"
	n := 0
	for i := 0; i < len(s); i++ {
		if !unreserved(s[i]) {
			n++
		}
	}
	if n == 0 {
		return s
	}
	b := make([]byte, 0, len(s)+2*n)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if unreserved(c) {
			b = append(b, c)
		} else {
			b = append(b, '%', hex[c>>4], hex[c&15])
		}
	}
	return string(b)
}

func unreserved(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~'
}

// FormParams parses an application/x-www-form-urlencoded text (a query
// string or a form body) into its parameters, in order, duplicates kept
// (RFC 5849 section 3.4.1.3.1): pairs are split on "&", the name from the
// value on the first "=", a pair without "=" has an empty value, "+" is a
// space and %XX escapes are decoded. An invalid escape is kept as typed.
// Empty pairs are skipped.
func FormParams(text string) []Param {
	var out []Param
	for pair := range strings.SplitSeq(text, "&") {
		if pair == "" {
			continue
		}
		name, value, _ := strings.Cut(pair, "=")
		out = append(out, Param{Name: formDecode(name), Value: formDecode(value)})
	}
	return out
}

func formDecode(s string) string {
	if v, err := url.QueryUnescape(s); err == nil {
		return v
	}
	return strings.ReplaceAll(s, "+", " ")
}

// BaseURI returns the base string URI of rawURL (RFC 5849 section
// 3.4.1.2): the scheme and host in lower case, the port only when it is
// not the scheme's default (80 for http, 443 for https), and the path as
// it is sent ("/" when empty), without query and fragment. rawURL is the
// URL as it goes on the wire, its path already percent-encoded.
func BaseURI(rawURL string) (string, error) {
	i := strings.Index(rawURL, "://")
	if i <= 0 {
		return "", invalidURL()
	}
	scheme := strings.ToLower(rawURL[:i])
	rest := rawURL[i+3:]
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	authority, path := rest[:end], rest[end:]
	if j := strings.IndexAny(path, "?#"); j >= 0 {
		path = path[:j]
	}
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		authority = authority[at+1:]
	}
	host, port := authority, ""
	if j := strings.LastIndexByte(authority, ':'); j >= 0 && !strings.Contains(authority[j:], "]") {
		host, port = authority[:j], authority[j+1:]
	}
	if host == "" {
		return "", invalidURL()
	}
	host = strings.ToLower(host)
	switch {
	case port == "":
	case scheme == "http" && port == "80", scheme == "https" && port == "443":
		port = ""
	case scheme == "ws" && port == "80", scheme == "wss" && port == "443":
		port = ""
	}
	if path == "" {
		path = "/"
	}
	out := scheme + "://" + host
	if port != "" {
		out += ":" + port
	}
	return out + path, nil
}

func invalidURL() error {
	return errs.New(errs.InvalidArgument, "The URL cannot be signed: it has no scheme or host.").
		WithCode(CodeInvalidURL).
		WithHint("Use a full URL such as https://api.example.com/items.")
}

// QueryOf returns the query of rawURL, without "?" and fragment.
func QueryOf(rawURL string) string {
	if i := strings.IndexByte(rawURL, '#'); i >= 0 {
		rawURL = rawURL[:i]
	}
	_, q, _ := strings.Cut(rawURL, "?")
	return q
}

// NormalizeParams returns the normalized parameters of RFC 5849 section
// 3.4.1.3.2: names and values encoded (Encode), sorted by encoded name
// and, for equal names, by encoded value (byte order), each written
// name=value and joined with "&".
func NormalizeParams(params []Param) string {
	enc := make([][2]string, len(params))
	for i, p := range params {
		enc[i] = [2]string{Encode(p.Name), Encode(p.Value)}
	}
	slices.SortFunc(enc, func(a, b [2]string) int {
		if c := strings.Compare(a[0], b[0]); c != 0 {
			return c
		}
		return strings.Compare(a[1], b[1])
	})
	var b strings.Builder
	for i, p := range enc {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p[0])
		b.WriteByte('=')
		b.WriteString(p[1])
	}
	return b.String()
}

// BaseString returns the signature base string (RFC 5849 section 3.4.1.1):
// the method in upper case, the encoded base string URI and the encoded
// normalized parameters, joined with "&". params must not hold
// oauth_signature or realm.
func BaseString(method, baseURI string, params []Param) string {
	return Encode(strings.ToUpper(method)) + "&" + Encode(baseURI) + "&" + Encode(NormalizeParams(params))
}

// Keys are what a signature is computed with.
type Keys struct {
	// ConsumerSecret and TokenSecret make the HMAC key and the PLAINTEXT
	// signature: Encode(ConsumerSecret) "&" Encode(TokenSecret).
	ConsumerSecret string
	TokenSecret    string
	// RSA is the client's private key for RSA-SHA1.
	RSA *rsa.PrivateKey
}

// Sign returns the signature of baseString with method (section 3.4.2 to
// 3.4.4): base64 of the HMAC-SHA1, HMAC-SHA256 or RSASSA-PKCS1-v1_5 SHA-1
// signature, or for PLAINTEXT the key itself.
func Sign(method, baseString string, k Keys) (string, error) {
	key := Encode(k.ConsumerSecret) + "&" + Encode(k.TokenSecret)
	switch method {
	case HMACSHA1, HMACSHA256:
		h := hmac.New(hashFor(method), []byte(key))
		h.Write([]byte(baseString))
		return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
	case PLAINTEXT:
		return key, nil
	case RSASHA1:
		if k.RSA == nil {
			return "", errs.New(errs.InvalidArgument, "RSA-SHA1 needs an RSA private key.").
				WithCode(CodeRSAKeyMissing).
				WithHint("Paste the PEM private key in the consumer secret field.")
		}
		sum := sha1.Sum([]byte(baseString))
		sig, err := rsa.SignPKCS1v15(nil, k.RSA, crypto.SHA1, sum[:])
		if err != nil {
			return "", errs.Wrap(err, errs.InvalidArgument, "The request could not be signed with the RSA key.").
				WithHint("Check the private key.")
		}
		return base64.StdEncoding.EncodeToString(sig), nil
	}
	return "", unsupported(method)
}

func unsupported(method string) error {
	return errs.New(errs.Unsupported, "PostSonde cannot sign OAuth 1.0 requests with this signature method.").
		WithCode(CodeUnsupportedMethod).
		WithHint("Choose HMAC-SHA1, HMAC-SHA256, RSA-SHA1 or PLAINTEXT.").
		WithDetail("method", method)
}

func hashFor(method string) func() hash.Hash {
	if method == HMACSHA256 {
		return sha256.New
	}
	return sha1.New
}

// BodyHash returns oauth_body_hash for body: base64 of its SHA-1 digest,
// or SHA-256 with HMAC-SHA256 (OAuth Request Body Hash, section 3.2).
func BodyHash(method string, body io.Reader) (string, error) {
	h := hashFor(method)()
	if body != nil {
		if _, err := io.Copy(h, body); err != nil {
			return "", err
		}
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

// Input is a request to sign and the auth's values, all rendered.
type Input struct {
	// Method is the HTTP method; URL the request URL as sent (path and
	// query percent-encoded), which gives the base string URI and the
	// query parameters.
	Method string
	URL    string
	// Form are the parameters of an application/x-www-form-urlencoded
	// body (FormParams), included in the signature; nil for other bodies.
	Form []Param

	ConsumerKey string
	Token       string
	// SignatureMethod is one of HMACSHA1 (also ""), HMACSHA256, RSASHA1,
	// PLAINTEXT.
	SignatureMethod string
	// Callback and Verifier are sent as oauth_callback and oauth_verifier
	// when not "".
	Callback string
	Verifier string
	// Version is oauth_version, "" to leave it out.
	Version string
	// Nonce and Timestamp, when "", are generated: 32 random letters and
	// digits, and Now in Unix seconds.
	Nonce     string
	Timestamp string
	// BodyHash, when not "", is sent as oauth_body_hash.
	BodyHash string

	Keys Keys

	// Now is the clock of a generated timestamp (nil: time.Now); Rand the
	// randomness of a generated nonce (nil: crypto/rand).
	Now  func() time.Time
	Rand io.Reader
}

// Result is a signed request's protocol parameters.
type Result struct {
	// Params are the oauth_* parameters in the order they are sent,
	// oauth_signature last.
	Params []Param
	// BaseString is the signature base string that was signed.
	BaseString string
	// Signature is oauth_signature, not encoded.
	Signature string
}

// SignRequest computes the protocol parameters of in and signs them.
func SignRequest(in Input) (Result, error) {
	method := in.SignatureMethod
	if method == "" {
		method = DefaultMethod
	}
	if !Supported(method) {
		return Result{}, unsupported(method)
	}
	baseURI, err := BaseURI(in.URL)
	if err != nil {
		return Result{}, err
	}
	nonce, ts := in.Nonce, in.Timestamp
	if nonce == "" {
		if nonce, err = Nonce(in.Rand); err != nil {
			return Result{}, errs.Wrap(err, errs.Internal, "A nonce could not be generated.")
		}
	}
	if ts == "" {
		now := time.Now
		if in.Now != nil {
			now = in.Now
		}
		ts = strconv.FormatInt(now().Unix(), 10)
	}
	oauth := []Param{{"oauth_consumer_key", in.ConsumerKey}}
	if in.Token != "" {
		oauth = append(oauth, Param{"oauth_token", in.Token})
	}
	oauth = append(oauth,
		Param{"oauth_signature_method", method},
		Param{"oauth_timestamp", ts},
		Param{"oauth_nonce", nonce},
	)
	if in.Version != "" {
		oauth = append(oauth, Param{"oauth_version", in.Version})
	}
	if in.Callback != "" {
		oauth = append(oauth, Param{"oauth_callback", in.Callback})
	}
	if in.Verifier != "" {
		oauth = append(oauth, Param{"oauth_verifier", in.Verifier})
	}
	if in.BodyHash != "" {
		oauth = append(oauth, Param{"oauth_body_hash", in.BodyHash})
	}
	all := append(FormParams(QueryOf(in.URL)), in.Form...)
	all = append(all, oauth...)
	base := BaseString(in.Method, baseURI, all)
	sig, err := Sign(method, base, in.Keys)
	if err != nil {
		return Result{}, err
	}
	return Result{Params: append(oauth, Param{"oauth_signature", sig}), BaseString: base, Signature: sig}, nil
}

// Nonce returns 32 random letters and digits from r (nil: crypto/rand).
func Nonce(r io.Reader) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, 32)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	for i, c := range b {
		b[i] = alphabet[int(c)%len(alphabet)]
	}
	return string(b), nil
}

// Header returns the Authorization header value carrying params (section
// 3.5.1): "OAuth", then realm (when not "", as a quoted-string) and each
// parameter as name="value", the value encoded, separated by ", ".
func Header(realm string, params []Param) string {
	var b strings.Builder
	b.WriteString("OAuth ")
	first := true
	write := func(name, value string) {
		if !first {
			b.WriteString(", ")
		}
		first = false
		b.WriteString(name)
		b.WriteString(`="`)
		b.WriteString(Encode(value))
		b.WriteByte('"')
	}
	if realm != "" {
		// A quoted-string (RFC 2617 section 1.2), not percent-encoded.
		first = false
		b.WriteString(`realm="`)
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(realm))
		b.WriteByte('"')
	}
	for _, p := range params {
		write(Encode(p.Name), p.Value)
	}
	return b.String()
}

// Pairs writes params as name=value pairs joined with "&", both encoded
// (section 3.5.2 and 3.5.3), for a form body or a query string.
func Pairs(params []Param) string {
	var b strings.Builder
	for i, p := range params {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(Encode(p.Name))
		b.WriteByte('=')
		b.WriteString(Encode(p.Value))
	}
	return b.String()
}
