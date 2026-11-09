package cookies

import (
	"encoding/base64"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	core "github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// DTOs are the only types CookiesService methods take or return. See
// docs/bindings.md and, for the behaviour, docs/cookies.md.

// CodeInvalidKey: a cookie key from the UI could not be decoded.
const CodeInvalidKey = "cookies.invalid_key"

// CookieDTO is a stored cookie.
type CookieDTO struct {
	// Key identifies the cookie (name, domain, host-only flag and path,
	// byte for byte) for Set and Delete. It is opaque to the UI and
	// ignored in SetInputDTO.Cookie.
	Key   string `json:"key"`
	Name  string `json:"name"`
	Value string `json:"value"`
	// Domain is the host (HostOnly) or the domain the cookie is sent to,
	// without a leading dot; the manager groups cookies by it.
	Domain string `json:"domain"`
	// HostOnly cookies go to Domain only; others to its subdomains too.
	HostOnly bool   `json:"hostOnly"`
	Path     string `json:"path"`
	Secure   bool   `json:"secure"`
	HTTPOnly bool   `json:"httpOnly"`
	// SameSite is "Strict", "Lax", "None" or "" (not set).
	SameSite string `json:"sameSite"`
	// Expires is an RFC 3339 time, or "" for a session cookie.
	Expires string `json:"expires"`
	// Created and LastAccess are RFC 3339 times; ignored on input.
	Created    string `json:"created"`
	LastAccess string `json:"lastAccess"`
	// Binary is true when the name, value, domain or path is not valid
	// UTF-8: the strings here are only an approximation, so the cookie can
	// be deleted but not edited.
	Binary bool `json:"binary"`
}

// ProblemDTO says why the project's cookie file could not be loaded; the
// jar then works in memory and is not saved.
type ProblemDTO struct {
	// Code is cookies.file_invalid or cookies.file_newer.
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

// JarDTO says which jar a CookiesDTO is: the one sends use now.
type JarDTO struct {
	// Workspace is the open project's name.
	Workspace string `json:"workspace"`
	// PerEnvironment is the project's "separate cookies per environment"
	// setting (off by default).
	PerEnvironment bool `json:"perEnvironment"`
	// EnvironmentID and Environment (its name) are the active environment
	// whose jar this is; both "" for the project's shared jar (the setting
	// is off, or no environment is active).
	EnvironmentID string `json:"environmentId"`
	Environment   string `json:"environment"`
}

// CookiesDTO is the jar sends use now: the open project's shared jar, or
// its active environment's.
type CookiesDTO struct {
	Cookies []CookieDTO `json:"cookies"`
	// Problem is set when the cookie file could not be loaded.
	Problem *ProblemDTO `json:"problem"`
	// Jar says which jar this is.
	Jar JarDTO `json:"jar"`
}

// SetInputDTO adds or replaces a cookie.
type SetInputDTO struct {
	// Key is the key of the cookie to replace, or "" to add one.
	Key    string    `json:"key"`
	Cookie CookieDTO `json:"cookie"`
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toCookieDTO(c core.Cookie) CookieDTO {
	d := CookieDTO{
		Key:        encodeKey(c),
		Name:       c.Name,
		Value:      c.Value,
		Domain:     c.Domain,
		HostOnly:   c.HostOnly,
		Path:       c.Path,
		Secure:     c.Secure,
		HTTPOnly:   c.HTTPOnly,
		SameSite:   string(c.SameSite),
		Created:    formatTime(c.Created),
		LastAccess: formatTime(c.LastAccess),
		Binary: !utf8.ValidString(c.Name) || !utf8.ValidString(c.Value) ||
			!utf8.ValidString(c.Domain) || !utf8.ValidString(c.Path),
	}
	if c.Persistent {
		d.Expires = formatTime(c.Expires)
	}
	return d
}

func toCookiesDTO(list []core.Cookie, problem error) CookiesDTO {
	out := CookiesDTO{Cookies: make([]CookieDTO, len(list))}
	for i, c := range list {
		out.Cookies[i] = toCookieDTO(c)
	}
	if problem != nil {
		p := &ProblemDTO{Message: problem.Error()}
		var e *errs.Error
		if errors.As(problem, &e) {
			p.Code, p.Message, p.Hint = e.Code, e.Message, e.Hint
		}
		out.Problem = p
	}
	return out
}

func fromCookieDTO(d CookieDTO) (core.Cookie, error) {
	c := core.Cookie{
		Name:     d.Name,
		Value:    d.Value,
		Domain:   d.Domain,
		HostOnly: d.HostOnly,
		Path:     d.Path,
		Secure:   d.Secure,
		HTTPOnly: d.HTTPOnly,
		SameSite: core.SameSite(d.SameSite),
	}
	if d.Expires != "" {
		t, err := time.Parse(time.RFC3339Nano, d.Expires)
		if err != nil {
			return c, errs.Wrap(err, errs.InvalidArgument, "The expiry date is not a valid date.").
				WithCode(core.CodeInvalidCookie).
				WithHint("Pick a date and time, or make it a session cookie.").
				WithDetail("field", "expires")
		}
		c.Persistent, c.Expires = true, t
	}
	return c, nil
}

// keySeparator cannot occur in a stored name, domain or path (control
// characters are rejected).
const keySeparator = "\x00"

// encodeKey returns the opaque key of a cookie's identity.
func encodeKey(c core.Cookie) string {
	hostOnly := "0"
	if c.HostOnly {
		hostOnly = "1"
	}
	raw := strings.Join([]string{c.Name, c.Domain, c.Path, hostOnly}, keySeparator)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeKey returns a Cookie with the identity of key.
func decodeKey(key string) (core.Cookie, error) {
	raw, err := base64.RawURLEncoding.DecodeString(key)
	parts := strings.Split(string(raw), keySeparator)
	if err != nil || len(parts) != 4 || parts[3] != "0" && parts[3] != "1" {
		return core.Cookie{}, errs.New(errs.InvalidArgument, "The cookie could not be identified.").
			WithCode(CodeInvalidKey).
			WithHint("Reload the cookies and try again.")
	}
	return core.Cookie{Name: parts[0], Domain: parts[1], Path: parts[2], HostOnly: parts[3] == "1"}, nil
}
