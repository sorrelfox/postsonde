package webhook

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Limits of a Config.
const (
	// MaxHeaders is the most response header rows.
	MaxHeaders = 100
	// MaxBodyBytes is the largest response body.
	MaxBodyBytes = 1 << 20
)

// CodeInvalidConfig is the error code of NormalizeConfig: the
// configuration is rejected as a whole, details.field says which part
// ("status", "headers", "body").
const CodeInvalidConfig = "webhook.invalid_config"

// Config is the response a webhook receiver answers every request with
// (Spec.Config).
type Config struct {
	// Status is 200–599.
	Status int
	// Headers are sent in order; rows not Enabled are skipped.
	// Content-Length is set from the body.
	Headers []Header
	// Body is sent as is ("" for none).
	Body string
}

// Header is one response header row.
type Header struct {
	Enabled bool
	Name    string
	Value   string
}

// DefaultConfig answers 200 with an empty body.
func DefaultConfig() Config {
	return Config{Status: http.StatusOK, Headers: []Header{}}
}

// Normalize checks c: a status from 200 to 599, valid header names and
// values (and none of the framing headers net/http sets), at most
// MaxHeaders rows and a body of at most MaxBodyBytes. Rows without a name
// and value are dropped; names are trimmed.
func Normalize(c Config) (Config, error) {
	if c.Status < 200 || c.Status > 599 {
		return Config{}, invalidConfig("status", "The status must be a number from 200 to 599.")
	}
	if len(c.Headers) > MaxHeaders {
		return Config{}, invalidConfig("headers", fmt.Sprintf("A webhook response can have at most %d headers.", MaxHeaders))
	}
	headers := make([]Header, 0, len(c.Headers))
	for _, h := range c.Headers {
		h.Name = strings.TrimSpace(h.Name)
		if h.Name == "" && h.Value == "" {
			continue
		}
		if !isToken(h.Name) {
			return Config{}, invalidConfig("headers", fmt.Sprintf("%q is not a valid header name.", h.Name))
		}
		if !validHeaderValue(h.Value) {
			return Config{}, invalidConfig("headers", "The value of "+h.Name+" contains a line break or control character.")
		}
		switch http.CanonicalHeaderKey(h.Name) {
		case "Content-Length", "Transfer-Encoding", "Connection":
			return Config{}, invalidConfig("headers", h.Name+" is set by the server; remove that header row.")
		}
		headers = append(headers, h)
	}
	if len(c.Body) > MaxBodyBytes {
		return Config{}, invalidConfig("body", fmt.Sprintf("The response body can be at most %d bytes.", MaxBodyBytes))
	}
	return Config{Status: c.Status, Headers: headers, Body: c.Body}, nil
}

func invalidConfig(field, msg string) *errs.Error {
	e := errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidConfig)
	if field != "" {
		e = e.WithDetail("field", field)
	}
	return e
}

// isToken reports whether s is an HTTP token (RFC 9110 §5.6.2).
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// validHeaderValue rejects control characters other than tab.
func validHeaderValue(v string) bool {
	for i := 0; i < len(v); i++ {
		if c := v[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}
