package cookies

import (
	"cmp"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Default limits of a Jar, as in Chromium (docs/cookies.md).
const (
	// DefaultMaxPerSite is the most cookies kept for one site (the domain
	// below its public suffix, see siteKey).
	DefaultMaxPerSite = 180
	// DefaultMaxTotal is the most cookies kept in a jar.
	DefaultMaxTotal = 3000
	// MaxAge is the longest lifetime a cookie can get from Expires or
	// Max-Age (rfc6265bis: 400 days).
	MaxAge = 400 * 24 * time.Hour
)

// SameSite is the SameSite attribute of a cookie.
type SameSite string

// SameSite values. SameSiteDefault means the attribute was missing or had
// an unknown value.
const (
	SameSiteDefault SameSite = ""
	SameSiteNone    SameSite = "None"
	SameSiteLax     SameSite = "Lax"
	SameSiteStrict  SameSite = "Strict"
)

// Cookie is a stored cookie (RFC 6265 §5.3).
type Cookie struct {
	Name  string
	Value string
	// Domain is the host (HostOnly) or the domain the cookie is sent to,
	// lower case, without a leading dot.
	Domain string
	// HostOnly cookies are sent to Domain only; others to its subdomains
	// too.
	HostOnly bool
	Path     string
	// Secure cookies are sent over secure channels only (https, and http
	// to localhost or a loopback address).
	Secure bool
	// HTTPOnly cookies are hidden from scripts once scripts can read the
	// jar; HTTP requests always send them.
	HTTPOnly bool
	// SameSite is stored and shown; it does not restrict sending (see
	// docs/cookies.md).
	SameSite SameSite
	// Persistent is true when the cookie had Expires or Max-Age. Session
	// cookies (false) have no Expires and are kept until the jar is
	// cleared, restarts included.
	Persistent bool
	Expires    time.Time
	// Created is when the cookie was first stored; replacing it keeps the
	// time. LastAccess is when it was last stored or sent.
	Created    time.Time
	LastAccess time.Time
}

// Reason says why a Set-Cookie value was not stored.
type Reason string

// Reasons a cookie is ignored.
const (
	// ReasonInvalid: control characters, or empty name and value.
	ReasonInvalid Reason = "invalid"
	// ReasonTooLarge: name and value longer than MaxNameValueBytes.
	ReasonTooLarge Reason = "too_large"
	// ReasonDomainMismatch: the Domain attribute does not cover the host.
	ReasonDomainMismatch Reason = "domain_mismatch"
	// ReasonPublicSuffix: the Domain attribute is a public suffix.
	ReasonPublicSuffix Reason = "public_suffix"
	// ReasonInsecure: a Secure cookie from an insecure channel.
	ReasonInsecure Reason = "insecure"
	// ReasonOverwriteSecure: an insecure channel tried to replace a Secure
	// cookie.
	ReasonOverwriteSecure Reason = "overwrite_secure"
	// ReasonPrefix: the __Secure- or __Host- name prefix requirements are
	// not met.
	ReasonPrefix Reason = "prefix"
	// ReasonURL: the URL is not http(s) or ws(s), or has no host.
	ReasonURL Reason = "url"
)

// Options configure a Jar. The zero value gives the defaults.
type Options struct {
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// MaxPerSite and MaxTotal are the cookie limits; zero means
	// DefaultMaxPerSite and DefaultMaxTotal.
	MaxPerSite int
	MaxTotal   int
	// SaveDelay is how long Open's Store waits after a change before
	// saving, so a burst of responses is written once; zero means
	// DefaultSaveDelay.
	SaveDelay time.Duration
	// OnSaveError receives errors of background saves; nil ignores them
	// (Flush and Close still return them).
	OnSaveError func(error)
}

// key identifies a cookie: storing one with the same key replaces it.
type key struct {
	domain, path, name string
	hostOnly           bool
}

func keyOf(c *Cookie) key { return key{c.Domain, c.Path, c.Name, c.HostOnly} }

type entry struct {
	Cookie
	// seq orders cookies created at the same time.
	seq uint64
}

// Jar is a cookie store. It is safe for concurrent use.
type Jar struct {
	now        func() time.Time
	maxPerSite int
	maxTotal   int

	mu      sync.Mutex
	entries map[key]*entry
	seq     uint64
	// onChange is called, without mu held, after the cookies changed.
	// touched is set when only access times changed.
	onChange func()
	touched  bool
}

var _ http.CookieJar = (*Jar)(nil)

// New returns an empty Jar.
func New(o Options) *Jar {
	j := &Jar{now: o.Now, maxPerSite: o.MaxPerSite, maxTotal: o.MaxTotal, entries: map[key]*entry{}}
	if j.now == nil {
		j.now = time.Now
	}
	if j.maxPerSite <= 0 {
		j.maxPerSite = DefaultMaxPerSite
	}
	if j.maxTotal <= 0 {
		j.maxTotal = DefaultMaxTotal
	}
	return j
}

// Outcome is what the jar did with one Set-Cookie value: Reason is "" when
// it was stored (or deleted an existing cookie), else why it was ignored.
type Outcome struct {
	Name   string
	Reason Reason
}

// SetCookieLines stores the cookies of a response to u, given the raw
// Set-Cookie field values in the order received. It returns the names of
// the cookies stored (deletions included) and, for those ignored, the name
// followed by ": " and the Reason, for the timeline. Values are never in
// the result.
func (j *Jar) SetCookieLines(u *url.URL, lines []string) (stored, rejected []string) {
	for _, o := range j.StoreLines(u, lines) {
		if o.Reason != "" {
			rejected = append(rejected, o.Name+": "+string(o.Reason))
			continue
		}
		stored = append(stored, o.Name)
	}
	return stored, rejected
}

// StoreLines is SetCookieLines with the outcome of each value, in the
// order of lines, for showing what happened to each (HTTP-32).
func (j *Jar) StoreLines(u *url.URL, lines []string) []Outcome {
	if len(lines) == 0 {
		return nil
	}
	out := make([]Outcome, 0, len(lines))
	changed := false
	j.mu.Lock()
	now := j.now()
	for _, line := range lines {
		name, reason := j.storeLocked(u, line, now)
		out = append(out, Outcome{Name: name, Reason: reason})
		if reason == "" {
			changed = true
		}
	}
	if changed {
		j.removeExpiredLocked(now)
		j.evictLocked()
	}
	onChange := j.onChange
	j.mu.Unlock()
	if changed && onChange != nil {
		onChange()
	}
	return out
}

// SetCookies implements http.CookieJar for net/http clients. Cookies parsed
// by net/http carry their Set-Cookie value in Raw, which is parsed again
// here; others are serialized with http.Cookie.String first.
func (j *Jar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	lines := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if c.Raw != "" {
			lines = append(lines, c.Raw)
		} else if s := c.String(); s != "" {
			lines = append(lines, s)
		}
	}
	j.SetCookieLines(u, lines)
}

// storeLocked applies the storage model of RFC 6265 §5.3, with the
// rfc6265bis additions, to one Set-Cookie value.
func (j *Jar) storeLocked(u *url.URL, line string, now time.Time) (string, Reason) {
	sc, reason, ok := parseSetCookie(line)
	if !ok {
		return sc.name, reason
	}
	host := canonicalHost(u)
	if !isHTTPScheme(u) || host == "" {
		return sc.name, ReasonURL
	}
	secureContext := isSecureContext(u)

	c := Cookie{
		Name:     sc.name,
		Value:    sc.value,
		Secure:   sc.secure,
		HTTPOnly: sc.httpOnly,
		SameSite: sc.sameSite,
		Created:  now,
	}
	// Expiry: Max-Age wins over Expires, both capped at MaxAge.
	switch {
	case sc.hasMaxAge:
		c.Persistent = true
		if sc.maxAge <= 0 {
			c.Expires = time.Unix(0, 0).UTC()
		} else if d := time.Duration(sc.maxAge); d > MaxAge/time.Second {
			c.Expires = now.Add(MaxAge)
		} else {
			c.Expires = now.Add(d * time.Second)
		}
	case sc.hasExpires:
		c.Persistent = true
		c.Expires = sc.expires
		if limit := now.Add(MaxAge); c.Expires.After(limit) {
			c.Expires = limit
		}
	}

	// Domain.
	domain := sc.domain
	if sc.hasDomain && isPublicSuffix(domain) {
		if domain != host {
			return sc.name, ReasonPublicSuffix
		}
		domain = ""
	}
	switch {
	case domain == "":
		c.HostOnly, c.Domain = true, host
	case !domainMatch(host, domain):
		return sc.name, ReasonDomainMismatch
	case isIP(host):
		// An IP address only matches itself: the cookie is host-only.
		c.HostOnly, c.Domain = true, host
	default:
		c.Domain = domain
	}

	// Path.
	c.Path = sc.path
	if c.Path == "" {
		c.Path = defaultPath(requestPath(u))
	}

	if c.Secure && !secureContext {
		return sc.name, ReasonInsecure
	}
	if !prefixOK(c, sc.hasDomain) {
		return sc.name, ReasonPrefix
	}

	// An insecure channel cannot replace or shadow a Secure cookie
	// (rfc6265bis "Leave Secure Cookies Alone").
	if !secureContext {
		for _, e := range j.entries {
			if e.Secure && e.Name == c.Name &&
				(domainMatch(e.Domain, c.Domain) || domainMatch(c.Domain, e.Domain)) &&
				pathMatch(c.Path, e.Path) {
				return sc.name, ReasonOverwriteSecure
			}
		}
	}

	// Same name, domain, host-only flag and path: the new cookie replaces
	// the old one and keeps its creation time.
	k := keyOf(&c)
	if old, ok := j.entries[k]; ok {
		c.Created = old.Created
	}
	c.LastAccess = now
	if !c.Expires.IsZero() && !c.Expires.After(now) {
		delete(j.entries, k)
		return sc.name, ""
	}
	e := &entry{Cookie: c}
	if old, ok := j.entries[k]; ok {
		e.seq = old.seq
	} else {
		j.seq++
		e.seq = j.seq
	}
	j.entries[k] = e
	return sc.name, ""
}

// prefixOK checks the cookie name prefixes of rfc6265bis §4.1.3, matched
// case-insensitively: __Secure- needs Secure; __Host- also needs no Domain
// attribute and Path "/". A cookie with an empty name cannot have a value
// that looks like a prefixed name.
func prefixOK(c Cookie, hadDomain bool) bool {
	name := strings.ToLower(c.Name)
	if c.Name == "" {
		name = strings.ToLower(c.Value)
		if strings.HasPrefix(name, "__secure-") || strings.HasPrefix(name, "__host-") {
			return false
		}
	}
	if strings.HasPrefix(name, "__secure-") && !c.Secure {
		return false
	}
	if strings.HasPrefix(name, "__host-") && (!c.Secure || hadDomain || c.Path != "/") {
		return false
	}
	return true
}

// removeExpiredLocked drops cookies whose expiry has passed.
func (j *Jar) removeExpiredLocked(now time.Time) bool {
	removed := false
	for k, e := range j.entries {
		if e.Persistent && !e.Expires.After(now) {
			delete(j.entries, k)
			removed = true
		}
	}
	return removed
}

// evictLocked enforces the limits (RFC 6265 §5.3 step 12): first per site,
// then in total, removing the least recently accessed cookies.
func (j *Jar) evictLocked() {
	if len(j.entries) <= j.maxPerSite && len(j.entries) <= j.maxTotal {
		return
	}
	bySite := map[string][]*entry{}
	for _, e := range j.entries {
		s := siteKey(e.Domain)
		bySite[s] = append(bySite[s], e)
	}
	for _, list := range bySite {
		if len(list) <= j.maxPerSite {
			continue
		}
		sortLRU(list)
		for _, e := range list[:len(list)-j.maxPerSite] {
			delete(j.entries, keyOf(&e.Cookie))
		}
	}
	if len(j.entries) <= j.maxTotal {
		return
	}
	all := make([]*entry, 0, len(j.entries))
	for _, e := range j.entries {
		all = append(all, e)
	}
	sortLRU(all)
	for _, e := range all[:len(all)-j.maxTotal] {
		delete(j.entries, keyOf(&e.Cookie))
	}
}

// sortLRU sorts least recently accessed first, older cookies first on ties.
func sortLRU(list []*entry) {
	slices.SortFunc(list, func(a, b *entry) int {
		if c := a.LastAccess.Compare(b.LastAccess); c != 0 {
			return c
		}
		return cmp.Compare(a.seq, b.seq)
	})
}

// Cookies returns the cookies to send to u (RFC 6265 §5.4), in Cookie
// header order: longer paths first, then earlier creation. Only Name and
// Value are set. Sending updates the cookies' access times.
//
// Secure cookies go only to secure channels; HttpOnly and SameSite do not
// restrict HTTP requests (docs/cookies.md).
func (j *Jar) Cookies(u *url.URL) []*http.Cookie {
	if !isHTTPScheme(u) {
		return nil
	}
	host := canonicalHost(u)
	path := requestPath(u)
	if path == "" {
		path = "/"
	}
	secure := isSecureContext(u)

	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	j.removeExpiredLocked(now)
	var match []*entry
	for _, e := range j.entries {
		if e.HostOnly && host != e.Domain || !e.HostOnly && !domainMatch(host, e.Domain) {
			continue
		}
		if !pathMatch(path, e.Path) || e.Secure && !secure {
			continue
		}
		match = append(match, e)
	}
	slices.SortFunc(match, func(a, b *entry) int {
		if c := cmp.Compare(len(b.Path), len(a.Path)); c != 0 {
			return c
		}
		if c := a.Created.Compare(b.Created); c != 0 {
			return c
		}
		return cmp.Compare(a.seq, b.seq)
	})
	out := make([]*http.Cookie, len(match))
	for i, e := range match {
		e.LastAccess = now
		out[i] = &http.Cookie{Name: e.Name, Value: e.Value}
	}
	if len(match) > 0 {
		j.touched = true
	}
	return out
}

// Header returns the Cookie header value for the cookies of u: "name=value"
// pairs joined by "; ", a cookie with an empty name as its value alone.
// It is "" when no cookie matches.
func (j *Jar) Header(u *url.URL) string { return CookieHeader(j.Cookies(u)) }

// CookieHeader serializes cookies as a Cookie header value, byte for byte
// as stored (net/http's Cookie.String would quote or drop characters).
func CookieHeader(cookies []*http.Cookie) string {
	var b strings.Builder
	for i, c := range cookies {
		if i > 0 {
			b.WriteString("; ")
		}
		if c.Name != "" {
			b.WriteString(c.Name)
			b.WriteByte('=')
		}
		b.WriteString(c.Value)
	}
	return b.String()
}

// All returns every unexpired cookie, sorted by domain, path and name.
func (j *Jar) All() []Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.removeExpiredLocked(j.now())
	out := make([]Cookie, 0, len(j.entries))
	for _, e := range j.entries {
		out = append(out, e.Cookie)
	}
	slices.SortFunc(out, func(a, b Cookie) int {
		return cmp.Or(cmp.Compare(a.Domain, b.Domain), cmp.Compare(a.Path, b.Path), cmp.Compare(a.Name, b.Name))
	})
	return out
}

// Len returns the number of stored cookies, expired ones not yet removed
// included.
func (j *Jar) Len() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.entries)
}

// Delete removes the cookie identified by c's Domain, Path, Name and
// HostOnly and reports whether there was one.
func (j *Jar) Delete(c Cookie) bool {
	j.mu.Lock()
	c.Domain = strings.ToLower(c.Domain)
	k := keyOf(&c)
	_, ok := j.entries[k]
	delete(j.entries, k)
	onChange := j.onChange
	j.mu.Unlock()
	if ok && onChange != nil {
		onChange()
	}
	return ok
}

// Clear removes every cookie.
func (j *Jar) Clear() {
	j.mu.Lock()
	n := len(j.entries)
	j.entries = map[key]*entry{}
	onChange := j.onChange
	j.mu.Unlock()
	if n > 0 && onChange != nil {
		onChange()
	}
}

// snapshot returns the cookies in creation order, for saving, and clears
// touched.
func (j *Jar) snapshot() []Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.touched = false
	list := make([]*entry, 0, len(j.entries))
	for _, e := range j.entries {
		list = append(list, e)
	}
	slices.SortFunc(list, func(a, b *entry) int {
		return cmp.Or(a.Created.Compare(b.Created), cmp.Compare(a.seq, b.seq))
	})
	out := make([]Cookie, len(list))
	for i, e := range list {
		out[i] = e.Cookie
	}
	return out
}

// load adds cookies read from a file, in creation order; expired ones and
// ones that break the jar's invariants are skipped. It reports whether any
// were skipped.
func (j *Jar) load(cookies []Cookie) (skipped int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	for _, c := range cookies {
		if c.Domain == "" || c.Path == "" || c.Path[0] != '/' || c.Name == "" && c.Value == "" ||
			c.Persistent && !c.Expires.After(now) {
			skipped++
			continue
		}
		c.Domain = strings.ToLower(c.Domain)
		if !c.Persistent {
			c.Expires = time.Time{}
		}
		j.seq++
		j.entries[keyOf(&c)] = &entry{Cookie: c, seq: j.seq}
	}
	j.evictLocked()
	return skipped
}

// isTouched reports whether access times changed since the last snapshot.
func (j *Jar) isTouched() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.touched
}
