// Package awssigv4 signs HTTP requests with AWS Signature Version 4
// (AUTH-17): the canonical request (method, canonical URI, canonical query
// string, canonical headers, signed headers, hashed payload), the string
// to sign over its SHA-256 and the credential scope, the signing key
// derived from the secret access key, and the Authorization header or the
// query parameters of a presigned URL.
//
// The package is pure: the caller says which headers are signed (Host and
// X-Amz-Date included) and gives the path and query exactly as they are
// sent. Standard library only. See docs/auth.md#aws-signature-v4-auth-17.
package awssigv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Algorithm is the signing algorithm this package implements.
const Algorithm = "AWS4-HMAC-SHA256"

// UnsignedPayload is the payload hash of a request whose body is not
// signed (S3, presigned URLs).
const UnsignedPayload = "UNSIGNED-PAYLOAD"

// EmptyPayloadHash is the hex SHA-256 of no bytes.
const EmptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// TimeFormat is the format of X-Amz-Date (ISO 8601 basic, UTC);
// DateFormat that of the credential scope's date.
const (
	TimeFormat = "20060102T150405Z"
	DateFormat = "20060102"
)

// ServiceS3 is the service whose paths are encoded once and not
// normalized, and whose requests carry X-Amz-Content-Sha256.
const ServiceS3 = "s3"

// Header names this package reads or writes.
const (
	HeaderDate          = "X-Amz-Date"
	HeaderSecurityToken = "X-Amz-Security-Token"
	HeaderContentSHA256 = "X-Amz-Content-Sha256"
)

// Query parameters of a presigned URL.
const (
	QueryAlgorithm     = "X-Amz-Algorithm"
	QueryCredential    = "X-Amz-Credential"
	QueryDate          = "X-Amz-Date"
	QueryExpires       = "X-Amz-Expires"
	QuerySignedHeaders = "X-Amz-SignedHeaders"
	QuerySecurityToken = "X-Amz-Security-Token"
	QuerySignature     = "X-Amz-Signature"
)

// Header is a header to sign, as sent.
type Header struct {
	Name  string
	Value string
}

// Request is what the signature covers.
type Request struct {
	Method string
	// Path is the request target's path as sent, percent-encoded as on
	// the wire; "" is "/".
	Path string
	// Query is the query as sent, without "?".
	Query string
	// Headers are the headers to sign, Host and X-Amz-Date included. A
	// name given more than once has its values joined with ",".
	Headers []Header
	// PayloadHash is the lower-case hex SHA-256 of the body (PayloadHash
	// computes it), or UnsignedPayload.
	PayloadHash string
}

// Scope is the credential scope: the date, region and service the
// signing key is derived for.
type Scope struct {
	Time    time.Time
	Region  string
	Service string
}

// String is "<yyyymmdd>/<region>/<service>/aws4_request".
func (s Scope) String() string {
	return s.Time.UTC().Format(DateFormat) + "/" + s.Region + "/" + s.Service + "/aws4_request"
}

// Result is a signature with the texts it was computed from.
type Result struct {
	CanonicalRequest string
	StringToSign     string
	// SignedHeaders is the lower-case, sorted, ";"-joined header names.
	SignedHeaders string
	// Scope is Scope.String().
	Scope string
	// Signature is lower-case hex.
	Signature string
}

// AuthorizationHeader is the Authorization value for accessKey:
// "AWS4-HMAC-SHA256 Credential=<key>/<scope>, SignedHeaders=…,
// Signature=…".
func (r Result) AuthorizationHeader(accessKey string) string {
	return Algorithm + " Credential=" + accessKey + "/" + r.Scope + ", SignedHeaders=" + r.SignedHeaders + ", Signature=" + r.Signature
}

// Sign signs r with secretKey for scope.
func Sign(r Request, secretKey string, scope Scope) Result {
	creq, signed := CanonicalRequest(r, scope.Service == ServiceS3)
	sts := StringToSign(creq, scope)
	mac := hmacSHA256(SigningKey(secretKey, scope), []byte(sts))
	return Result{
		CanonicalRequest: creq,
		StringToSign:     sts,
		SignedHeaders:    signed,
		Scope:            scope.String(),
		Signature:        hex.EncodeToString(mac),
	}
}

// CanonicalRequest returns r's canonical request and its signed headers.
// s3 encodes the path once and does not normalize it.
func CanonicalRequest(r Request, s3 bool) (creq, signedHeaders string) {
	headers, signed := CanonicalHeaders(r.Headers)
	payload := r.PayloadHash
	if payload == "" {
		payload = EmptyPayloadHash
	}
	creq = strings.Join([]string{
		r.Method,
		CanonicalURI(r.Path, s3),
		CanonicalQuery(r.Query),
		headers,
		signed,
		payload,
	}, "\n")
	return creq, signed
}

// StringToSign is "AWS4-HMAC-SHA256\n<time>\n<scope>\n<hex sha256 of
// creq>".
func StringToSign(creq string, scope Scope) string {
	sum := sha256.Sum256([]byte(creq))
	return Algorithm + "\n" + scope.Time.UTC().Format(TimeFormat) + "\n" + scope.String() + "\n" + hex.EncodeToString(sum[:])
}

// SigningKey derives the key of scope from secretKey: HMAC-SHA256 chained
// over the date, region, service and "aws4_request", starting from
// "AWS4" + secretKey.
func SigningKey(secretKey string, scope Scope) []byte {
	k := hmacSHA256([]byte("AWS4"+secretKey), []byte(scope.Time.UTC().Format(DateFormat)))
	k = hmacSHA256(k, []byte(scope.Region))
	k = hmacSHA256(k, []byte(scope.Service))
	return hmacSHA256(k, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// CanonicalURI is path as the canonical request has it. Every path
// segment is URI-encoded (Encode): for most services the path as sent,
// with its escapes, so they are encoded a second time ("%20" becomes
// "%2520"), after removing "." and ".." segments and empty segments; for
// S3 (s3 true) each segment is decoded first and encoded once, and the
// path is kept as it is. "" is "/".
func CanonicalURI(path string, s3 bool) string {
	if path == "" {
		return "/"
	}
	if !s3 {
		path = normalizePath(path)
	}
	segments := strings.Split(path, "/")
	for i, s := range segments {
		if s3 {
			if d, err := url.PathUnescape(s); err == nil {
				s = d
			}
		}
		segments[i] = Encode(s)
	}
	return strings.Join(segments, "/")
}

// normalizePath removes "." and ".." segments and empty segments (RFC
// 3986 section 5.2.4, and "//" collapsed), keeping a trailing "/".
func normalizePath(path string) string {
	var out []string
	parts := strings.Split(path, "/")
	for _, p := range parts {
		switch p {
		case "", ".":
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, p)
		}
	}
	s := "/" + strings.Join(out, "/")
	last := parts[len(parts)-1]
	if len(out) > 0 && (last == "" || last == "." || last == "..") {
		s += "/"
	}
	return s
}

// CanonicalQuery is the query as the canonical request has it: each
// name=value pair decoded ("+" as a space) and encoded again (Encode), a
// pair without "=" given an empty value, empty pairs dropped, sorted by
// name, then value.
func CanonicalQuery(query string) string {
	if query == "" {
		return ""
	}
	type pair struct{ name, value string }
	var pairs []pair
	for entry := range strings.SplitSeq(query, "&") {
		if entry == "" {
			continue
		}
		name, value, _ := strings.Cut(entry, "=")
		pairs = append(pairs, pair{Encode(queryUnescape(name)), Encode(queryUnescape(value))})
	}
	slices.SortFunc(pairs, func(a, b pair) int {
		if c := strings.Compare(a.name, b.name); c != 0 {
			return c
		}
		return strings.Compare(a.value, b.value)
	})
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.name)
		b.WriteByte('=')
		b.WriteString(p.value)
	}
	return b.String()
}

func queryUnescape(s string) string {
	if d, err := url.QueryUnescape(s); err == nil {
		return d
	}
	return s
}

// CanonicalHeaders returns the canonical headers (one "name:value\n" line
// each, names lower case and sorted, values trimmed with runs of spaces
// collapsed, the values of a repeated name joined with ",") and the signed
// headers ("name;name").
func CanonicalHeaders(headers []Header) (canonical, signed string) {
	values := map[string][]string{}
	var names []string
	for _, h := range headers {
		n := strings.ToLower(strings.TrimSpace(h.Name))
		if _, ok := values[n]; !ok {
			names = append(names, n)
		}
		values[n] = append(values[n], trimAll(h.Value))
	}
	slices.Sort(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(':')
		b.WriteString(strings.Join(values[n], ","))
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(names, ";")
}

// SignedHeaders is the signed headers list of headers ("name;name").
func SignedHeaders(headers []Header) string {
	_, s := CanonicalHeaders(headers)
	return s
}

// trimAll trims s and collapses each run of spaces and tabs inside it to
// one space.
func trimAll(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\t' }), " ")
}

// Encode URI-encodes s as SigV4 wants it: every byte except the
// unreserved characters (letters, digits, "-", ".", "_", "~") becomes %XX
// with upper-case hex digits; a space is %20.
func Encode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}

// PayloadHash is the lower-case hex SHA-256 of what body reads; of no
// bytes when body is nil.
func PayloadHash(body io.Reader) (string, error) {
	h := sha256.New()
	if body != nil {
		if _, err := io.Copy(h, body); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Param is a query parameter, decoded.
type Param struct {
	Name  string
	Value string
}

// PresignParams are the query parameters of a presigned URL, without
// X-Amz-Signature: they are part of the query that is signed. expires is
// in seconds; sessionToken is left out when "".
func PresignParams(accessKey string, scope Scope, expires int64, signedHeaders, sessionToken string) []Param {
	p := []Param{
		{QueryAlgorithm, Algorithm},
		{QueryCredential, accessKey + "/" + scope.String()},
		{QueryDate, scope.Time.UTC().Format(TimeFormat)},
		{QueryExpires, strconv.FormatInt(expires, 10)},
		{QuerySignedHeaders, signedHeaders},
	}
	if sessionToken != "" {
		p = append(p, Param{QuerySecurityToken, sessionToken})
	}
	return p
}
