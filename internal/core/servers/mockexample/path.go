package mockexample

import (
	"net/url"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/servers/mockopenapi"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// RoutePath returns the route pattern of a request whose URL was typed
// as tmpl ("{{baseUrl}}/users/{{id}}", "https://api.example/users/:id")
// and sent as sent (resolved; "" when not known), with notes about what
// changed.
//
//   - The scheme and authority are left out; so is a {{variable}} at the
//     very start of the URL, which stands for them (a base URL).
//   - A path segment that is one {{variable}} or a :param or {param}
//     placeholder (HTTP-05) becomes a parameter named after it
//     ({{user.id}} is :user_id; a dynamic value {{$uuid}} is :uuid). A
//     segment that mixes text and variables becomes one constrained
//     parameter, as in routes from OpenAPI (mockopenapi.ConvertPath).
//   - The query string and fragment are left out: routes match paths.
//   - When the URL sent has more segments than the typed path and ends
//     with segments that match it, the extra leading segments are the
//     base URL's path ({{baseUrl}} = https://api.example/v1) and are put
//     in front, so a client pointed at the mock uses the same paths.
//   - A blank tmpl uses the URL sent.
func RoutePath(tmpl, sent string) (pattern string, notes []string) {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = sent
	}
	path, typed := typedPath(tmpl)
	pattern, notes = mockopenapi.ConvertPath(path)
	if !typed || sent == "" {
		return pattern, notes
	}
	u, err := url.Parse(strings.TrimSpace(sent))
	if err != nil {
		return pattern, notes
	}
	sentSegs := segments(u.EscapedPath())
	patSegs := segments(pattern)
	extra := len(sentSegs) - len(patSegs)
	if extra <= 0 {
		return pattern, notes
	}
	for i, p := range patSegs {
		if isParamSegment(p) {
			continue
		}
		if unescape(p) != unescape(sentSegs[extra+i]) {
			notes = append(notes, "The URL sent ("+u.EscapedPath()+") does not end with the request's path; the route has the request's path only.")
			return pattern, notes
		}
	}
	prefix, _ := mockopenapi.ConvertPath("/" + strings.Join(sentSegs[:extra], "/"))
	if pattern == "/" {
		pattern = prefix
	} else {
		pattern = prefix + pattern
	}
	notes = append(notes, "The path "+prefix+" of the base URL, from the URL sent, is put in front of the request's path.")
	return pattern, notes
}

// typedPath returns the path (and query) of a URL as typed, with every
// {{variable}} and :param written as a {param} placeholder, and whether
// variables stood for a part of the URL that is not in it (a base URL at
// the start, or a variable in the authority).
func typedPath(tmpl string) (path string, based bool) {
	segs := template.Parse(strings.TrimSpace(tmpl))
	var b strings.Builder
	for i, s := range segs {
		if s.Kind == template.Var {
			if i == 0 && (len(segs) == 1 || startsPath(segs[1])) {
				// A base URL: scheme, host and perhaps a path.
				based = true
				continue
			}
			b.WriteString("{" + varParam(s.Name) + "}")
			continue
		}
		b.WriteString(s.Text)
	}
	text := b.String()
	from := 0
	if i := strings.Index(text, "://"); i >= 0 {
		auth := text[i+3:]
		end := strings.IndexAny(auth, "/?#")
		if end < 0 {
			end = len(auth)
		}
		based = based || strings.Contains(auth[:end], "{")
		from = i + 3 + end
	} else if !strings.HasPrefix(text, "/") && !based {
		// host:port/path without a scheme.
		end := strings.IndexAny(text, "/?#")
		if end < 0 {
			end = len(text)
		}
		from = end
	}
	path = text[from:]
	if path == "" || path[0] != '/' {
		path = "/" + path
	}
	return colonParams(path), based
}

// startsPath reports whether segment s, after a leading variable, starts
// the path, query or fragment (so the variable is a base URL).
func startsPath(s template.Segment) bool {
	if s.Kind != template.Text {
		return false
	}
	return s.Text == "" || strings.ContainsRune("/?#", rune(s.Text[0]))
}

// varParam is the parameter name of a variable: its name without a
// filter pipe, arguments or the $ of a dynamic value.
func varParam(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.IndexAny(name, "| \t"); i >= 0 {
		name = name[:i]
	}
	name = strings.TrimPrefix(name, "$")
	if name == "" {
		name = "param"
	}
	return name
}

// colonParams writes the :name placeholders of a path (request.PathParams'
// rule: right after a /, letters, digits and _) as {name}.
func colonParams(path string) string {
	end := len(path)
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		end = i
	}
	var b strings.Builder
	for i := 0; i < end; i++ {
		c := path[i]
		if c == ':' && i > 0 && path[i-1] == '/' {
			j := i + 1
			for j < end && isIdent(path[j]) {
				j++
			}
			if j > i+1 {
				b.WriteString("{" + path[i+1:j] + "}")
				i = j - 1
				continue
			}
		}
		b.WriteByte(c)
	}
	b.WriteString(path[end:])
	return b.String()
}

func isIdent(c byte) bool {
	return c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}

// segments splits a path into its non-empty segments.
func segments(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// isParamSegment reports whether a pattern segment matches any segment.
func isParamSegment(s string) bool {
	return strings.HasPrefix(s, ":") || strings.HasPrefix(s, "{") || strings.HasPrefix(s, "*")
}

func unescape(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}
