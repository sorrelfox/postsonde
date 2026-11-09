package wsserver

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// headerTokens splits every value of the header name at commas and returns
// the trimmed, non-empty elements in order.
func headerTokens(h http.Header, name string) []string {
	var out []string
	for _, v := range h.Values(name) {
		for part := range strings.SplitSeq(v, ",") {
			if p := textTrim(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// headerHasToken reports whether the header name lists token, in any
// letter case ("Connection: keep-alive, Upgrade").
func headerHasToken(h http.Header, name, token string) bool {
	for _, t := range headerTokens(h, name) {
		if strings.EqualFold(t, token) {
			return true
		}
	}
	return false
}

// validKey reports whether key is the base64 of 16 bytes (RFC 6455 §4.2.1).
func validKey(key string) bool {
	if len(key) != 24 {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(key)
	return err == nil && len(b) == 16
}

// textTrim trims spaces and tabs (optional whitespace in HTTP).
func textTrim(s string) string { return strings.Trim(s, " \t") }

func utf8Valid(b []byte) bool { return utf8.Valid(b) }

// messageOf is the user-facing message of err.
func messageOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok && e.Message != "" {
		return e.Message
	}
	return err.Error()
}
