// Package hawk signs HTTP requests with the Hawk authentication scheme
// (AUTH-18): the header normalized string ("hawk.1.header") over the
// timestamp, nonce, method, resource, host, port, payload hash and ext
// (and app and dlg), its HMAC-SHA256 or HMAC-SHA1 MAC with the key, the
// optional payload hash ("hawk.1.payload" over the content type and body)
// and the Authorization header.
//
// Standard library only. See docs/auth.md#hawk-auth-18.
package hawk

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"hash"
	"io"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Algorithms.
const (
	SHA256 = "sha256"
	SHA1   = "sha1"
)

// DefaultAlgorithm is the algorithm of an auth that names none.
const DefaultAlgorithm = SHA256

// CodeUnsupportedAlgorithm: an algorithm this package does not know.
// Details: algorithm.
const CodeUnsupportedAlgorithm = "hawk.unsupported_algorithm"

// Supported reports whether algorithm is one MAC knows.
func Supported(algorithm string) bool {
	return algorithm == SHA256 || algorithm == SHA1
}

func newHash(algorithm string) (func() hash.Hash, error) {
	switch algorithm {
	case SHA256:
		return sha256.New, nil
	case SHA1:
		return sha1.New, nil
	}
	return nil, errs.New(errs.Unsupported, "Hawk cannot use this algorithm.").
		WithCode(CodeUnsupportedAlgorithm).
		WithHint("Choose sha256 or sha1.").
		WithDetail("algorithm", algorithm)
}

// Artifacts are what the MAC covers.
type Artifacts struct {
	// Timestamp is Unix seconds; Nonce a random string.
	Timestamp int64
	Nonce     string
	// Method is the request method, Resource the request target as sent
	// (path and query), Host the host name in lower case and Port the
	// port (the scheme's default when the URL names none).
	Method   string
	Resource string
	Host     string
	Port     int
	// Hash is the payload hash (PayloadHash); "" when the payload is not
	// covered.
	Hash string
	// Ext is application data, sent and covered as it is. App and Dlg
	// are Oz's application and delegated-by IDs; Dlg only with App.
	Ext string
	App string
	Dlg string
}

// NormalizedString is the text a header MAC is computed over
// ("hawk.1.header" and one line per artifact).
func NormalizedString(a Artifacts) string {
	var b strings.Builder
	b.WriteString("hawk.1.header\n")
	b.WriteString(strconv.FormatInt(a.Timestamp, 10) + "\n")
	b.WriteString(a.Nonce + "\n")
	b.WriteString(strings.ToUpper(a.Method) + "\n")
	b.WriteString(a.Resource + "\n")
	b.WriteString(strings.ToLower(a.Host) + "\n")
	b.WriteString(strconv.Itoa(a.Port) + "\n")
	b.WriteString(a.Hash + "\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(a.Ext, `\`, `\\`), "\n", `\n`) + "\n")
	if a.App != "" {
		b.WriteString(a.App + "\n")
		b.WriteString(a.Dlg + "\n")
	}
	return b.String()
}

// MAC is the base64 HMAC of the normalized string with key.
func MAC(algorithm, key string, a Artifacts) (string, error) {
	h, err := newHash(algorithm)
	if err != nil {
		return "", err
	}
	m := hmac.New(h, []byte(key))
	m.Write([]byte(NormalizedString(a)))
	return base64.StdEncoding.EncodeToString(m.Sum(nil)), nil
}

// PayloadHash is the base64 hash of "hawk.1.payload\n<content type>\n
// <payload>\n", the content type being contentType's media type in lower
// case, without parameters. A nil payload is empty.
func PayloadHash(algorithm, contentType string, payload io.Reader) (string, error) {
	newH, err := newHash(algorithm)
	if err != nil {
		return "", err
	}
	h := newH()
	mt, _, _ := strings.Cut(contentType, ";")
	io.WriteString(h, "hawk.1.payload\n"+strings.ToLower(strings.TrimSpace(mt))+"\n")
	if payload != nil {
		if _, err := io.Copy(h, payload); err != nil {
			return "", err
		}
	}
	io.WriteString(h, "\n")
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

// Header is the Authorization value: Hawk id="…", ts="…", nonce="…",
// [hash="…", ][ext="…", ]mac="…"[, app="…"[, dlg="…"]], in the order of
// the reference implementation. In ext, "\" and `"` are escaped with "\".
func Header(id string, a Artifacts, mac string) string {
	var b strings.Builder
	b.WriteString(`Hawk id="` + id + `", ts="` + strconv.FormatInt(a.Timestamp, 10) + `", nonce="` + a.Nonce + `"`)
	if a.Hash != "" {
		b.WriteString(`, hash="` + a.Hash + `"`)
	}
	if a.Ext != "" {
		b.WriteString(`, ext="` + strings.ReplaceAll(strings.ReplaceAll(a.Ext, `\`, `\\`), `"`, `\"`) + `"`)
	}
	b.WriteString(`, mac="` + mac + `"`)
	if a.App != "" {
		b.WriteString(`, app="` + a.App + `"`)
		if a.Dlg != "" {
			b.WriteString(`, dlg="` + a.Dlg + `"`)
		}
	}
	return b.String()
}

// ValidAttribute reports whether s may be sent in a header attribute as it
// is (id, app, dlg): printable ASCII without `"` and "\".
func ValidAttribute(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < ' ' || c > '~' || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

// ValidExt reports whether s may be sent as ext: printable ASCII (`"` and
// "\" are escaped).
func ValidExt(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < ' ' || c > '~' {
			return false
		}
	}
	return true
}
