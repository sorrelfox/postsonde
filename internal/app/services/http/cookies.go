package http

import (
	nethttp "net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// Jar states of a response cookie (CookieDTO.Jar).
const (
	// JarStored: the jar stored the cookie.
	JarStored = "stored"
	// JarRemoved: the cookie expired on arrival (Max-Age <= 0 or a past
	// Expires), so the jar deleted the cookie of that name, if it had one.
	JarRemoved = "removed"
	// JarRejected: the jar ignored the cookie; JarReason says why.
	JarRejected = "rejected"
	// JarNone: no cookie jar was used (no project open) or what it did is
	// not known.
	JarNone = ""
	// JarSkipped: the request's settings turn storing cookies off
	// (storeCookies false, HTTP-26), so the jar was not given it.
	JarSkipped = "skipped"
)

// skippedMessage is the JarMessage of a JarSkipped cookie.
const skippedMessage = "Storing cookies is off in the request's Settings tab."

// CookieDTO is one Set-Cookie header of the response, parsed (HTTP-32).
type CookieDTO struct {
	// Raw is the Set-Cookie value as received.
	Raw string `json:"raw"`
	// Name and Value; Name is "" for a cookie without "=" (sent as its
	// value alone).
	Name  string `json:"name"`
	Value string `json:"value"`
	// Domain is the Domain attribute without its leading dot, "" for a
	// host-only cookie. Path is the Path attribute, "" for none (the
	// default path: the request path up to its last "/").
	Domain string `json:"domain"`
	Path   string `json:"path"`
	// Expires is the Expires attribute (RFC 3339, UTC), "" for none.
	// MaxAge is the Max-Age attribute in seconds when HasMaxAge; it wins
	// over Expires. Neither: a session cookie.
	Expires   string `json:"expires"`
	MaxAge    int64  `json:"maxAge"`
	HasMaxAge bool   `json:"hasMaxAge"`
	Secure    bool   `json:"secure"`
	HTTPOnly  bool   `json:"httpOnly"`
	// SameSite is "Strict", "Lax", "None" or "" (not set, or an unknown
	// value).
	SameSite string `json:"sameSite"`
	// Error is why the value cannot be a cookie at all (control
	// characters, empty, too large), "" when it parsed. The jar ignores
	// such a cookie.
	Error string `json:"error"`
	// Notes are attributes that were ignored or read differently from how
	// they are written, one sentence each.
	Notes []string `json:"notes"`
	// Jar is what the project's cookie jar did with it: JarStored,
	// JarRemoved, JarRejected, JarSkipped or JarNone. JarReason is the
	// cookies.Reason of a rejection ("domain_mismatch", …) and JarMessage
	// says it in a sentence.
	Jar        string `json:"jar"`
	JarReason  string `json:"jarReason"`
	JarMessage string `json:"jarMessage"`
}

// reasonMessages says why the jar ignores a cookie, per cookies.Reason.
var reasonMessages = map[cookies.Reason]string{
	cookies.ReasonInvalid:         "Not a valid cookie: it is empty or has control characters.",
	cookies.ReasonTooLarge:        "The name and value together are longer than 4096 bytes.",
	cookies.ReasonDomainMismatch:  "The Domain attribute does not match the host that sent it.",
	cookies.ReasonPublicSuffix:    "The Domain attribute is a public suffix, which cannot hold cookies.",
	cookies.ReasonInsecure:        "A Secure cookie cannot be set over plain http.",
	cookies.ReasonOverwriteSecure: "Plain http cannot replace a Secure cookie of the same name.",
	cookies.ReasonPrefix:          "The __Secure- or __Host- name prefix rules are not met.",
	cookies.ReasonURL:             "Cookies are only stored for http and https URLs with a host.",
}

func reasonMessage(r cookies.Reason) string {
	if m, ok := reasonMessages[r]; ok {
		return m
	}
	return "The cookie jar ignored it (" + string(r) + ")."
}

// jarRecorder is the cookie jar of one send: the project's jar, keeping
// the outcome of each Set-Cookie value of the latest hop, which after the
// send is the final response's when it had any (the jar is called for
// every hop that has Set-Cookie values, in order).
type jarRecorder struct {
	jar *cookies.Jar

	mu       sync.Mutex
	lines    []string
	outcomes []cookies.Outcome
}

var _ httpclient.CookieJar = (*jarRecorder)(nil)

func (r *jarRecorder) Cookies(u *url.URL) []*nethttp.Cookie { return r.jar.Cookies(u) }

func (r *jarRecorder) SetCookieLines(u *url.URL, lines []string) (stored, rejected []string) {
	out := r.jar.StoreLines(u, lines)
	r.mu.Lock()
	r.lines, r.outcomes = slices.Clone(lines), out
	r.mu.Unlock()
	for _, o := range out {
		if o.Reason != "" {
			rejected = append(rejected, o.Name+": "+string(o.Reason))
		} else {
			stored = append(stored, o.Name)
		}
	}
	return stored, rejected
}

// outcomesFor returns the outcomes of lines when the latest hop's values
// were exactly lines, else nil.
func (r *jarRecorder) outcomesFor(lines []string) []cookies.Outcome {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Equal(r.lines, lines) || len(r.outcomes) != len(lines) {
		return nil
	}
	return r.outcomes
}

// responseCookies parses the Set-Cookie headers of a response received at
// now, with what the jar did with each (jar nil: no jar).
func responseCookies(headers []httpclient.Header, jar *jarRecorder, now time.Time) []CookieDTO {
	var lines []string
	for _, h := range headers {
		if strings.EqualFold(h.Name, "Set-Cookie") {
			lines = append(lines, h.Value)
		}
	}
	out := make([]CookieDTO, 0, len(lines))
	outcomes := jar.outcomesFor(lines)
	for i, line := range lines {
		c, reason, ok := cookies.ParseSetCookie(line)
		d := CookieDTO{
			Raw:       line,
			Name:      c.Name,
			Value:     c.Value,
			Domain:    c.Domain,
			Path:      c.Path,
			MaxAge:    c.MaxAge,
			HasMaxAge: c.HasMaxAge,
			Secure:    c.Secure,
			HTTPOnly:  c.HTTPOnly,
			SameSite:  string(c.SameSite),
			Notes:     c.Notes,
		}
		if d.Notes == nil {
			d.Notes = []string{}
		}
		if c.HasExpires {
			d.Expires = c.Expires.UTC().Format(time.RFC3339)
		}
		if !ok {
			d.Error = reasonMessage(reason)
		}
		if outcomes != nil {
			o := outcomes[i]
			switch {
			case o.Reason != "":
				d.Jar, d.JarReason, d.JarMessage = JarRejected, string(o.Reason), reasonMessage(o.Reason)
			case c.HasMaxAge && c.MaxAge <= 0, !c.HasMaxAge && c.HasExpires && !c.Expires.After(now):
				d.Jar = JarRemoved
			default:
				d.Jar = JarStored
			}
		}
		out = append(out, d)
	}
	return out
}
