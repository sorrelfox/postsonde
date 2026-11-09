package jsonschema

import (
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Formats lists the format names checked when Options.FormatAssertion is
// set; other names are annotations only.
var Formats = []string{
	"date-time", "date", "time", "duration",
	"email", "idn-email", "hostname", "idn-hostname",
	"ipv4", "ipv6",
	"uri", "uri-reference", "iri", "iri-reference", "uri-template",
	"uuid", "regex", "json-pointer", "relative-json-pointer",
}

// checkFormat returns why s is not valid in format, or "" when it is (or
// the format is not known).
func checkFormat(format, s string) string {
	var ok bool
	switch format {
	case "date-time":
		ok = isDateTime(s)
	case "date":
		ok = isDate(s)
	case "time":
		ok = isTime(s)
	case "duration":
		ok = isDuration(s)
	case "email":
		ok = isEmail(s, false)
	case "idn-email":
		ok = isEmail(s, true)
	case "hostname":
		ok = isHostname(s, false)
	case "idn-hostname":
		ok = isHostname(s, true)
	case "ipv4":
		a, err := netip.ParseAddr(s)
		ok = err == nil && a.Is4()
	case "ipv6":
		a, err := netip.ParseAddr(s)
		ok = err == nil && a.Is6() && a.Zone() == ""
	case "uri":
		ok = isURI(s, true, false)
	case "uri-reference":
		ok = isURI(s, false, false)
	case "iri":
		ok = isURI(s, true, true)
	case "iri-reference":
		ok = isURI(s, false, true)
	case "uri-template":
		ok = isURITemplate(s)
	case "uuid":
		ok = uuidRe.MatchString(s)
	case "regex":
		ok = isECMARegex(s)
	case "json-pointer":
		ok = isJSONPointer(s)
	case "relative-json-pointer":
		ok = isRelativeJSONPointer(s)
	default:
		return ""
	}
	if ok {
		return ""
	}
	return "must be a valid " + format + ", not " + show(s)
}

// durTime is dur-time of RFC 3339 appendix A.
const durTime = `T(?:\d+H(?:\d+M(?:\d+S)?)?|\d+M(?:\d+S)?|\d+S)`

var (
	dateRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)
	timeRe = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(?:[Zz]|([+-])(\d{2}):(\d{2}))$`)
	uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	// The ABNF of RFC 3339 appendix A: the units are in order and
	// contiguous (P1Y2D and PT1H2S are not durations).
	durationRe = regexp.MustCompile(`^P(?:(?:\d+D|\d+M(?:\d+D)?|\d+Y(?:\d+M(?:\d+D)?)?)(?:` + durTime + `)?|` + durTime + `|\d+W)$`)
)

// isDate: RFC 3339 full-date.
func isDate(s string) bool {
	m := dateRe.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	d, _ := strconv.Atoi(m[3])
	return mo >= 1 && mo <= 12 && d >= 1 && d <= daysIn(y, mo)
}

func daysIn(y, m int) int {
	switch m {
	case 2:
		if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

// isTime: RFC 3339 full-time, with a leap second only at 23:59:60 UTC.
func isTime(s string) bool {
	m := timeRe.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	sec, _ := strconv.Atoi(m[3])
	if h > 23 || mi > 59 || sec > 60 {
		return false
	}
	oh, om := 0, 0
	if m[4] != "" {
		oh, _ = strconv.Atoi(m[5])
		om, _ = strconv.Atoi(m[6])
		if oh > 23 || om > 59 {
			return false
		}
	}
	if sec == 60 {
		// The UTC time must be 23:59.
		total := h*60 + mi
		off := oh*60 + om
		if m[4] == "+" {
			total -= off
		} else {
			total += off
		}
		total = ((total % 1440) + 1440) % 1440
		return total == 23*60+59
	}
	return true
}

// isDateTime: RFC 3339 date-time.
func isDateTime(s string) bool {
	i := strings.IndexAny(s, "Tt")
	return i == 10 && isDate(s[:i]) && isTime(s[i+1:])
}

// isDuration: RFC 3339 appendix A (ISO 8601) duration.
func isDuration(s string) bool { return durationRe.MatchString(s) }

// isEmail: an RFC 5321 Mailbox (RFC 6531 with idn: UTF-8 in the local
// part and a U-label domain): a dot-string or quoted-string local part,
// "@", and a host name or an IPv4 or IPv6 address literal.
func isEmail(s string, idn bool) bool {
	if !idn && !isASCII(s) || !utf8.ValidString(s) {
		return false
	}
	at := emailLocalEnd(s, idn)
	if at <= 0 || at >= len(s)-1 || s[at] != '@' {
		return false
	}
	domain := s[at+1:]
	if lit, ok := strings.CutPrefix(domain, "["); ok {
		lit, ok = strings.CutSuffix(lit, "]")
		if !ok {
			return false
		}
		if len(lit) > 5 && strings.EqualFold(lit[:5], "IPv6:") {
			ad, err := netip.ParseAddr(lit[5:])
			return err == nil && ad.Is6() && ad.Zone() == ""
		}
		ad, err := netip.ParseAddr(lit)
		return err == nil && ad.Is4()
	}
	return isHostname(domain, idn)
}

// emailLocalEnd returns the length of the local part at the start of s,
// or -1 when it is not valid.
func emailLocalEnd(s string, idn bool) int {
	if strings.HasPrefix(s, `"`) {
		for i := 1; i < len(s); i++ {
			c := s[i]
			switch {
			case c == '"':
				return i + 1
			case c == '\\':
				// quoted-pairSMTP: a backslash and a printable ASCII character.
				if i+1 >= len(s) || s[i+1] < ' ' || s[i+1] > '~' {
					return -1
				}
				i++
			case c >= ' ' && c <= '~', idn && c >= utf8.RuneSelf:
			default:
				return -1
			}
		}
		return -1
	}
	// dot-string: atoms of atext separated by single dots.
	prevDot := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '.':
			if prevDot {
				return -1
			}
			prevDot = true
			continue
		case c == '@':
			if prevDot {
				return -1
			}
			return i
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte("!#$%&'*+-/=?^_`{|}~", c) >= 0, idn && c >= utf8.RuneSelf:
		default:
			return -1
		}
		prevDot = false
	}
	return -1
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// isURI: RFC 3986 URI (absolute: with a scheme) or URI reference; with
// iri, non-ASCII characters are allowed (RFC 3987).
func isURI(s string, absolute, iri bool) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= utf8.RuneSelf:
			if !iri {
				return false
			}
		case c <= ' ', c == '"', c == '<', c == '>', c == '\\', c == '^', c == '`', c == '{', c == '|', c == '}', c == 0x7f:
			return false
		case c == '%':
			if i+2 >= len(s) || !isHex(s[i+1:i+3]) {
				return false
			}
		}
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	if absolute && u.Scheme == "" {
		return false
	}
	if strings.Count(s, "#") > 1 {
		return false
	}
	// url.Parse is lenient: "[" and "]" are only for an IP literal host,
	// and the authority has at most one "@".
	rest := s
	if u.Scheme != "" {
		rest = s[len(u.Scheme)+1:]
	}
	if auth, ok := strings.CutPrefix(rest, "//"); ok {
		end := strings.IndexAny(auth, "/?#")
		if end < 0 {
			end = len(auth)
		}
		auth, rest = auth[:end], auth[end:]
		if strings.Count(auth, "@") > 1 {
			return false
		}
		host := auth[strings.IndexByte(auth, '@')+1:]
		if strings.ContainsAny(host, "[]") && !strings.HasPrefix(host, "[") {
			return false
		}
	}
	return !strings.ContainsAny(rest, "[]")
}

// isURITemplate: RFC 6570 (level 4) syntax. Literals are any characters
// but controls, space and the characters "<>\^`{|}, with "%" only in a
// percent-encoded triplet (the apostrophe is allowed, as the suite
// expects). An expression is "{", an optional operator, and a
// comma-separated list of variable names (ALPHA, DIGIT, "_" and
// percent-encoded triplets, with single inner dots), each with an
// optional ":" length of 1 to 9999 or "*".
func isURITemplate(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '{':
			end := strings.IndexByte(s[i:], '}')
			if end < 0 || !isURITemplateExpr(s[i+1:i+end]) {
				return false
			}
			i += end
		case c == '%':
			if i+2 >= len(s) || !isHex(s[i+1:i+3]) {
				return false
			}
			i += 2
		case c <= ' ', c == 0x7f, strings.IndexByte("\"<>\\^`|}", c) >= 0:
			return false
		}
	}
	return utf8.ValidString(s)
}

func isURITemplateExpr(e string) bool {
	if e != "" && strings.IndexByte("+#./;?&=,!@|", e[0]) >= 0 {
		e = e[1:]
	}
	for spec := range strings.SplitSeq(e, ",") {
		name := spec
		if i := strings.IndexByte(spec, ':'); i >= 0 {
			name = spec[:i]
			n := spec[i+1:]
			if n == "" || len(n) > 4 || n[0] < '1' || n[0] > '9' || strings.Trim(n, "0123456789") != "" {
				return false
			}
		} else if strings.HasSuffix(spec, "*") {
			name = spec[:len(spec)-1]
		}
		if !isVarName(name) {
			return false
		}
	}
	return true
}

func isVarName(name string) bool {
	if name == "" || name[0] == '.' || name[len(name)-1] == '.' || strings.Contains(name, "..") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '.', c == '_', c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c == '%':
			if i+2 >= len(name) || !isHex(name[i+1:i+3]) {
				return false
			}
			i += 2
		default:
			return false
		}
	}
	return true
}

func isJSONPointer(s string) bool {
	if s == "" {
		return true
	}
	if s[0] != '/' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '~' && (i+1 >= len(s) || s[i+1] != '0' && s[i+1] != '1') {
			return false
		}
	}
	return true
}

func isRelativeJSONPointer(s string) bool {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i > 1 && s[0] == '0' {
		return false
	}
	rest := s[i:]
	return rest == "#" || isJSONPointer(rest)
}
