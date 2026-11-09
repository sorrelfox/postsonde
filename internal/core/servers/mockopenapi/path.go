package mockopenapi

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ConvertPath turns an OpenAPI path template ("/pets/{petId}") into a mock
// route pattern ("/pets/:petId"). notes say what had to change beyond the
// syntax, one sentence each:
//
//   - A query string or fragment is dropped, a missing leading slash added
//     and empty segments (//) removed.
//   - Parameter names that are not letters, digits and _ are rewritten
//     ({pet-id} is :pet_id), and a name used twice gets a suffix (:id_2).
//   - A segment that mixes parameters and text ({name}.{ext},
//     {id}:activate) becomes one constrained parameter whose regular
//     expression keeps the text ({name_ext:.+\..+}).
//   - The characters a pattern reserves in literal text (*, { and } and a
//     leading :) and a % that is not an escape are percent-encoded; the
//     matcher decodes both sides, so they still match themselves.
func ConvertPath(p string) (pattern string, notes []string) {
	c := pathConv{names: map[string]bool{}}
	return c.convert(p), c.notes
}

type pathConv struct {
	names map[string]bool
	notes []string
}

func (c *pathConv) note(s string) {
	for _, n := range c.notes {
		if n == s {
			return
		}
	}
	c.notes = append(c.notes, s)
}

func (c *pathConv) convert(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
		c.note("The query string or fragment in the path was left out; routes match the path only.")
	}
	if p == "" || p == "/" {
		return "/"
	}
	if p[0] != '/' {
		p = "/" + p
		c.note("The path does not start with /; one was added.")
	}
	parts := strings.Split(p[1:], "/")
	trailing := parts[len(parts)-1] == ""
	if trailing {
		parts = parts[:len(parts)-1]
	}
	out := make([]string, 0, len(parts))
	for _, s := range parts {
		if s == "" {
			c.note("An empty segment (//) in the path was left out.")
			continue
		}
		out = append(out, c.segment(s))
	}
	if len(out) == 0 {
		return "/"
	}
	res := "/" + strings.Join(out, "/")
	if trailing {
		res += "/"
	}
	return res
}

// token is a piece of a segment: literal text or a {parameter}.
type token struct {
	param bool
	text  string // the literal text, or the parameter's name as written
}

func splitSegment(s string) []token {
	var toks []token
	for s != "" {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			toks = append(toks, token{text: s})
			break
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			toks = append(toks, token{text: s})
			break
		}
		if i > 0 {
			toks = append(toks, token{text: s[:i]})
		}
		toks = append(toks, token{param: true, text: s[i+1 : i+j]})
		s = s[i+j+1:]
	}
	return toks
}

func (c *pathConv) segment(s string) string {
	toks := splitSegment(s)
	var params []string
	for _, t := range toks {
		if t.param {
			params = append(params, t.text)
		}
	}
	switch {
	case len(params) == 0:
		return escapeLiteral(s)
	case len(toks) == 1:
		return ":" + c.name(params[0])
	}
	// Text and parameters in one segment: one constrained parameter.
	var re strings.Builder
	for _, t := range toks {
		if t.param {
			re.WriteString(".+")
			continue
		}
		lit, err := url.PathUnescape(t.text)
		if err != nil {
			lit = t.text
		}
		// Invalid UTF-8 cannot be written in a regular expression; the
		// matcher reads such a byte as U+FFFD, which . matches.
		for i, r := range lit {
			if r == utf8.RuneError && !strings.HasPrefix(lit[i:], "�") {
				re.WriteByte('.')
				continue
			}
			re.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	name := c.name(strings.Join(params, "_"))
	c.note("The segment " + strconv.Quote(s) + " mixes parameters and text; it is matched by the parameter " + name + " with a regular expression.")
	return "{" + name + ":" + re.String() + "}"
}

// name returns a valid, unused parameter name for raw.
func (c *pathConv) name(raw string) string {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		switch {
		case ch == '_', 'a' <= ch && ch <= 'z', 'A' <= ch && ch <= 'Z', '0' <= ch && ch <= '9':
			b.WriteByte(ch)
		default:
			if s := b.String(); s == "" || s[len(s)-1] != '_' {
				b.WriteByte('_')
			}
		}
	}
	name := b.String()
	if name == "" || strings.Trim(name, "_") == "" {
		name = "param"
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "p" + name
	}
	if name != raw {
		c.note("The parameter name " + strconv.Quote(raw) + " was changed to " + name + ".")
	}
	if c.names[name] {
		base := name
		for n := 2; ; n++ {
			name = base + "_" + strconv.Itoa(n)
			if !c.names[name] {
				break
			}
		}
		c.note("The parameter " + base + " appears more than once in the path; the next one is " + name + ".")
	}
	c.names[name] = true
	return name
}

// escapeLiteral percent-encodes what a pattern reserves in literal text.
func escapeLiteral(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '*' || ch == '{' || ch == '}' || (ch == ':' && i == 0):
			b.WriteString("%" + strings.ToUpper(strconv.FormatInt(int64(ch), 16)))
		case ch == '%' && !(i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2])):
			b.WriteString("%25")
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// PrefixOf returns the path of a server URL as a route prefix: "" for the
// root, else "/v1" (a leading slash, no trailing one, no query). Absolute
// URLs ("https://host/v1") and relative ones ("/v1", "v1") are both taken.
func PrefixOf(serverURL string) string {
	s := strings.TrimSpace(serverURL)
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		j := strings.IndexByte(rest, '/')
		if j < 0 {
			return ""
		}
		s = rest[j:]
	} else if strings.HasPrefix(s, "//") {
		rest := s[2:]
		j := strings.IndexByte(rest, '/')
		if j < 0 {
			return ""
		}
		s = rest[j:]
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimRight(s, "/")
	if s == "" {
		return ""
	}
	if s[0] != '/' {
		s = "/" + s
	}
	return s
}
