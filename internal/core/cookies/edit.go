package cookies

import (
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes of the cookie manager's edits (CK-02).
const (
	// CodeInvalidCookie: a field of an edited cookie is not valid; the
	// "field" detail names it.
	CodeInvalidCookie = "cookies.invalid_cookie"
	// CodeCookieExists: another cookie has the same name, domain, host-only
	// flag and path.
	CodeCookieExists = "cookies.exists"
	// CodeCookieNotFound: the cookie being edited or deleted is no longer
	// in the jar (the server deleted it, or it expired).
	CodeCookieNotFound = "cookies.not_found"
)

// MaxAttributeBytes is the longest domain or path accepted from the cookie
// manager, as for Set-Cookie attributes (rfc6265bis).
const MaxAttributeBytes = 1024

// Set stores c as the user entered it in the cookie manager (CK-02) and
// returns the cookie as stored. With old nil it adds a cookie; otherwise it
// replaces old (found by its identity: Name, Domain, HostOnly, Path), which
// may change the identity.
//
// Unlike SetCookieLines there is no request URL, so the channel rules do
// not apply; the cookie itself must still be one the jar could hold: a
// name or value without control characters, ';' (and '=' in the name), a
// domain that is not a public suffix unless host-only, a path starting
// with '/', the __Secure- and __Host- prefix rules and a future expiry
// (capped at MaxAge). IP addresses are always host-only. Errors are
// invalid_argument with code CodeInvalidCookie, already_exists with
// CodeCookieExists when another cookie has the new identity, and not_found
// with CodeCookieNotFound when old is gone.
//
// Created is kept from the replaced cookie; Created and LastAccess of c
// are otherwise ignored. Expires is ignored unless Persistent.
func (j *Jar) Set(old *Cookie, c Cookie) (Cookie, error) {
	j.mu.Lock()
	now := j.now()
	c, err := normalize(c, now)
	if err != nil {
		j.mu.Unlock()
		return Cookie{}, err
	}
	k := keyOf(&c)
	var prev *entry
	if old != nil {
		o := *old
		o.Domain = strings.TrimPrefix(strings.ToLower(o.Domain), ".")
		oldKey := keyOf(&o)
		if prev = j.entries[oldKey]; prev == nil || prev.Persistent && !prev.Expires.After(now) {
			j.mu.Unlock()
			return Cookie{}, errs.New(errs.NotFound, "The cookie is no longer in the jar.").
				WithCode(CodeCookieNotFound).
				WithHint("The server may have deleted it, or it expired. Reload the cookies.")
		}
		if oldKey != k {
			if _, taken := j.entries[k]; taken {
				j.mu.Unlock()
				return Cookie{}, exists()
			}
			delete(j.entries, oldKey)
		}
	} else if _, taken := j.entries[k]; taken {
		j.mu.Unlock()
		return Cookie{}, exists()
	}

	e := &entry{Cookie: c}
	e.Created, e.LastAccess = now, now
	if prev != nil {
		e.Created, e.seq = prev.Created, prev.seq
	} else {
		j.seq++
		e.seq = j.seq
	}
	j.entries[k] = e
	j.removeExpiredLocked(now)
	j.evictLocked()
	stored := e.Cookie
	onChange := j.onChange
	j.mu.Unlock()
	if onChange != nil {
		onChange()
	}
	return stored, nil
}

// DeleteDomain removes every cookie whose Domain is domain (host-only and
// domain cookies alike: the cookie manager's groups) and returns how many
// there were.
func (j *Jar) DeleteDomain(domain string) int {
	domain = strings.TrimPrefix(strings.ToLower(domain), ".")
	j.mu.Lock()
	n := 0
	for k := range j.entries {
		if k.domain == domain {
			delete(j.entries, k)
			n++
		}
	}
	onChange := j.onChange
	j.mu.Unlock()
	if n > 0 && onChange != nil {
		onChange()
	}
	return n
}

func exists() error {
	return errs.New(errs.AlreadyExists, "A cookie with this name, domain and path already exists.").
		WithCode(CodeCookieExists).
		WithHint("Edit that cookie instead, or change the name, domain or path.")
}

func invalid(field, msg, hint string) error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidCookie).WithHint(hint).WithDetail("field", field)
}

// normalize checks an edited cookie and puts it in stored form.
func normalize(c Cookie, now time.Time) (Cookie, error) {
	switch {
	case c.Name == "" && c.Value == "":
		return c, invalid("name", "The cookie needs a name or a value.", "Enter a name.")
	case hasBadByte(c.Name, ";=") || strings.TrimSpace(c.Name) != c.Name:
		return c, invalid("name", "The cookie name contains a character a Cookie header cannot carry.",
			"Remove control characters, spaces around the name, ';' and '='.")
	case hasBadByte(c.Value, ";"):
		return c, invalid("value", "The cookie value contains a character a Cookie header cannot carry.",
			"Remove control characters and ';'.")
	case len(c.Name)+len(c.Value) > MaxNameValueBytes:
		return c, invalid("value", "The cookie is too large.", "Name and value together can have at most 4096 bytes.")
	}

	c.Domain = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(c.Domain)), ".")
	switch {
	case c.Domain == "":
		return c, invalid("domain", "The cookie needs a domain.", "Enter the host the cookie is sent to, such as api.example.com.")
	case len(c.Domain) > MaxAttributeBytes || hasBadByte(c.Domain, " ;,/\\?#@") || strings.Contains(c.Domain, ".."):
		return c, invalid("domain", "The domain is not a valid host name.", "Enter a host name or an IP address, without a scheme, port or path.")
	}
	if isIP(c.Domain) {
		c.HostOnly = true
	} else if !c.HostOnly && isPublicSuffix(c.Domain) {
		return c, invalid("domain", "The domain is a public suffix, so the cookie would go to unrelated sites.",
			"Make the cookie host-only, or use a longer domain.")
	}

	if c.Path == "" {
		c.Path = "/"
	}
	switch {
	case c.Path[0] != '/':
		return c, invalid("path", "The path must start with '/'.", "Enter a path such as / or /api.")
	case len(c.Path) > MaxAttributeBytes || hasBadByte(c.Path, ";"):
		return c, invalid("path", "The path contains a character a Cookie attribute cannot carry.", "Remove control characters and ';'.")
	}

	switch c.SameSite {
	case SameSiteDefault, SameSiteNone, SameSiteLax, SameSiteStrict:
	default:
		return c, invalid("sameSite", "SameSite must be Strict, Lax, None or empty.", "Pick a SameSite value from the list.")
	}

	if c.Persistent {
		if !c.Expires.After(now) {
			return c, invalid("expires", "The expiry date has passed.", "Pick a future date, or make it a session cookie.")
		}
		if limit := now.Add(MaxAge); c.Expires.After(limit) {
			c.Expires = limit
		}
	} else {
		c.Expires = time.Time{}
	}

	if !prefixOK(c, !c.HostOnly) {
		return c, invalid("name", "Cookies named __Secure- need Secure; __Host- also needs host-only and path /.",
			"Turn on Secure (and for __Host-, host-only with path /), or rename the cookie.")
	}
	return c, nil
}

// hasBadByte reports control characters (tab included) or any of extra.
func hasBadByte(s, extra string) bool {
	for i := 0; i < len(s); i++ {
		if b := s[i]; b < 0x20 || b == 0x7f {
			return true
		}
	}
	return strings.ContainsAny(s, extra)
}
