package cookies

import (
	"math"
	"strings"
	"time"
)

// Parser limits from rfc6265bis §5.6 (as enforced by browsers).
const (
	// MaxNameValueBytes is the longest name plus value accepted; a longer
	// cookie is ignored.
	MaxNameValueBytes = 4096
	// MaxAttributeValueBytes is the longest attribute value accepted; a
	// longer attribute is ignored.
	MaxAttributeValueBytes = 1024
)

// setCookie is a parsed Set-Cookie field value (RFC 6265 §5.2), before the
// storage model is applied.
type setCookie struct {
	name, value string

	expires    time.Time
	hasExpires bool
	// maxAge is the Max-Age delta in seconds; <= 0 expires the cookie.
	maxAge    int64
	hasMaxAge bool
	// domain is the Domain attribute, lower case without a leading dot.
	domain    string
	hasDomain bool
	// path is the Path attribute; "" (with hasPath) means the default path,
	// which an empty or relative Path gives.
	path     string
	hasPath  bool
	secure   bool
	httpOnly bool
	sameSite SameSite
}

// parseSetCookie parses a Set-Cookie field value like a browser: attribute
// names are case-insensitive, unknown attributes and invalid attribute
// values are ignored, and a pair without "=" is a cookie with an empty
// name (rfc6265bis). It fails (ok false) only when the cookie must be
// ignored as a whole, with the reason.
func parseSetCookie(line string) (c setCookie, reason Reason, ok bool) {
	return parseSetCookieNotes(line, nil)
}

// parseSetCookieNotes is parseSetCookie, calling note (when not nil) with
// a sentence for each attribute value it ignores or reads differently
// from how it is written.
func parseSetCookieNotes(line string, note func(string)) (c setCookie, reason Reason, ok bool) {
	if note == nil {
		note = func(string) {}
	}
	for i := 0; i < len(line); i++ {
		if b := line[i]; b < 0x20 && b != '\t' || b == 0x7f {
			return c, ReasonInvalid, false
		}
	}
	pair, attrs, _ := strings.Cut(line, ";")
	if name, value, found := strings.Cut(pair, "="); found {
		c.name, c.value = trimWSP(name), trimWSP(value)
	} else {
		c.value = trimWSP(pair)
	}
	switch {
	case c.name == "" && c.value == "":
		return c, ReasonInvalid, false
	case len(c.name)+len(c.value) > MaxNameValueBytes:
		return c, ReasonTooLarge, false
	}

	for attrs != "" {
		var av string
		av, attrs, _ = strings.Cut(attrs, ";")
		name, value, _ := strings.Cut(av, "=")
		name, value = trimWSP(name), trimWSP(value)
		if len(value) > MaxAttributeValueBytes {
			note(name + " is longer than 1024 bytes and was ignored.")
			continue
		}
		switch strings.ToLower(name) {
		case "expires":
			if t, ok := parseCookieDate(value); ok {
				c.expires, c.hasExpires = t, true
			} else {
				note("Expires=" + value + " is not a date and was ignored.")
			}
		case "max-age":
			if n, ok := parseMaxAge(value); ok {
				c.maxAge, c.hasMaxAge = n, true
			} else {
				note("Max-Age=" + value + " is not a number and was ignored.")
			}
		case "domain":
			if value != "" {
				c.domain, c.hasDomain = strings.ToLower(strings.TrimPrefix(value, ".")), true
			} else {
				note("An empty Domain was ignored.")
			}
		case "path":
			c.hasPath = true
			c.path = ""
			if value != "" && value[0] == '/' {
				c.path = value
			} else {
				note("Path=" + value + " does not start with /, so the default path applies.")
			}
		case "secure":
			c.secure = true
		case "httponly":
			c.httpOnly = true
		case "samesite":
			switch strings.ToLower(value) {
			case "none":
				c.sameSite = SameSiteNone
			case "lax":
				c.sameSite = SameSiteLax
			case "strict":
				c.sameSite = SameSiteStrict
			default:
				c.sameSite = SameSiteDefault
				note("SameSite=" + value + " is not Strict, Lax or None, so the default applies.")
			}
		}
	}
	return c, "", true
}

// SetCookie is a Set-Cookie field value as parsed (RFC 6265 §5.2), before
// the storage model: the attributes as the server sent them, for showing a
// response's cookies (HTTP-32).
type SetCookie struct {
	Name, Value string
	// Expires is the Expires attribute, UTC, when HasExpires.
	Expires    time.Time
	HasExpires bool
	// MaxAge is the Max-Age attribute in seconds, when HasMaxAge; <= 0
	// deletes the cookie.
	MaxAge    int64
	HasMaxAge bool
	// Domain is the Domain attribute, lower case without a leading dot;
	// "" for none (a host-only cookie).
	Domain string
	// Path is the Path attribute; "" for none, or one that is empty or
	// relative (the default path applies).
	Path     string
	Secure   bool
	HTTPOnly bool
	// SameSite is Strict, Lax, None or "" (none or an unknown value).
	SameSite SameSite
	// Notes are the attribute values that were ignored or read
	// differently, one sentence each, in order.
	Notes []string
}

// ParseSetCookie parses a Set-Cookie field value as the jar does. It
// fails (ok false) when the jar ignores the cookie as a whole, with the
// Reason; c then has the name when there is one.
func ParseSetCookie(line string) (c SetCookie, reason Reason, ok bool) {
	sc, reason, ok := parseSetCookieNotes(line, func(n string) { c.Notes = append(c.Notes, n) })
	c.Name, c.Value = sc.name, sc.value
	c.Expires, c.HasExpires = sc.expires, sc.hasExpires
	c.MaxAge, c.HasMaxAge = sc.maxAge, sc.hasMaxAge
	c.Domain, c.Path = sc.domain, sc.path
	c.Secure, c.HTTPOnly, c.SameSite = sc.secure, sc.httpOnly, sc.sameSite
	return c, reason, ok
}

// trimWSP removes leading and trailing spaces and horizontal tabs.
func trimWSP(s string) string { return strings.Trim(s, " \t") }

// parseMaxAge parses a Max-Age value (§5.2.2): an optional "-" and digits.
// Values too large for int64 saturate.
func parseMaxAge(v string) (int64, bool) {
	if v == "" {
		return 0, false
	}
	digits, neg := v, false
	if v[0] == '-' {
		digits, neg = v[1:], true
	}
	if digits == "" {
		return 0, false
	}
	var n int64
	for i := 0; i < len(digits); i++ {
		d := digits[i]
		if d < '0' || d > '9' {
			return 0, false
		}
		if n <= (math.MaxInt64-9)/10 {
			n = n*10 + int64(d-'0')
		} else {
			n = math.MaxInt64
		}
	}
	if neg {
		return -n, true
	}
	return n, true
}

// parseCookieDate implements the cookie date algorithm of RFC 6265 §5.1.1,
// which accepts the many date formats servers send ("Wed, 09 Jun 2021
// 10:18:14 GMT", "Wednesday, 09-Jun-21 10:18:14 GMT", "Wed Jun 9 10:18:14
// 2021"). The time is UTC; zone names are ignored.
func parseCookieDate(s string) (time.Time, bool) {
	var (
		foundTime, foundDay, foundMonth, foundYear bool
		hour, minute, second, day, year            int
		month                                      time.Month
	)
	for tok := range dateTokens(s) {
		if !foundTime {
			if h, m, sec, ok := parseHMS(tok); ok {
				hour, minute, second, foundTime = h, m, sec, true
				continue
			}
		}
		if !foundDay {
			if n, ok := leadingDigits(tok, 1, 2); ok {
				day, foundDay = n, true
				continue
			}
		}
		if !foundMonth {
			if m := parseMonth(tok); m != 0 {
				month, foundMonth = m, true
				continue
			}
		}
		if !foundYear {
			if n, ok := leadingDigits(tok, 2, 4); ok {
				year, foundYear = n, true
				continue
			}
		}
	}
	switch {
	case year >= 70 && year <= 99:
		year += 1900
	case year >= 0 && year <= 69:
		year += 2000
	}
	if !foundTime || !foundDay || !foundMonth || !foundYear ||
		day < 1 || day > 31 || year < 1601 || hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, false
	}
	t := time.Date(year, month, day, hour, minute, second, 0, time.UTC)
	if t.Day() != day {
		return time.Time{}, false // 31 April, 30 February
	}
	return t, true
}

// dateTokens yields the date-tokens of s: runs of non-delimiter bytes.
func dateTokens(s string) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		start := -1
		for i := 0; i <= len(s); i++ {
			if i < len(s) && !isDateDelimiter(s[i]) {
				if start < 0 {
					start = i
				}
				continue
			}
			if start >= 0 {
				if !yield(s[start:i]) {
					return
				}
				start = -1
			}
		}
	}
}

// isDateDelimiter: %x09 / %x20-2F / %x3B-40 / %x5B-60 / %x7B-7E.
func isDateDelimiter(b byte) bool {
	return b == 0x09 || b >= 0x20 && b <= 0x2f || b >= 0x3b && b <= 0x40 ||
		b >= 0x5b && b <= 0x60 || b >= 0x7b && b <= 0x7e
}

// leadingDigits parses tok as min to max digits followed by nothing or a
// non-digit and anything ("1*2DIGIT ( non-digit *OCTET )").
func leadingDigits(tok string, min, max int) (int, bool) {
	n, i := 0, 0
	for i < len(tok) && tok[i] >= '0' && tok[i] <= '9' {
		n = n*10 + int(tok[i]-'0')
		i++
		if i > max {
			return 0, false
		}
	}
	return n, i >= min
}

// parseHMS parses hms-time ( non-digit *OCTET ), each field 1*2DIGIT.
func parseHMS(tok string) (h, m, s int, ok bool) {
	var fields [3]int
	rest := tok
	for i := range fields {
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			fields[i] = fields[i]*10 + int(rest[j]-'0')
			j++
			if j > 2 {
				return 0, 0, 0, false
			}
		}
		if j == 0 {
			return 0, 0, 0, false
		}
		rest = rest[j:]
		if i < 2 {
			if rest == "" || rest[0] != ':' {
				return 0, 0, 0, false
			}
			rest = rest[1:]
		}
	}
	return fields[0], fields[1], fields[2], true
}

var monthNames = [...]string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}

// parseMonth matches the first three characters of tok, case-insensitive.
func parseMonth(tok string) time.Month {
	if len(tok) < 3 {
		return 0
	}
	p := strings.ToLower(tok[:3])
	for i, name := range monthNames {
		if p == name {
			return time.Month(i + 1)
		}
	}
	return 0
}
