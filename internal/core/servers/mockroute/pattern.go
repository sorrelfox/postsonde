package mockroute

import (
	"net/url"
	"regexp"
	"strings"
)

// kind is the kind of a pattern segment. The order of the constants is the
// specificity order: a lower kind is more specific.
type kind uint8

const (
	kindLiteral    kind = iota // users
	kindConstraint             // {id:[0-9]+}
	kindParam                  // :id or {id}
	kindWildcard               // *
	kindEnd                    // the pattern has no segment here (comparison only)
	kindCatchAll               // ** or *rest
)

// segment is one compiled pattern segment.
type segment struct {
	kind  kind
	lit   string // kindLiteral: decoded text, lower-cased with CaseInsensitive
	name  string // kindConstraint, kindParam, named kindCatchAll
	reSrc string // kindConstraint: the constraint as written
	re    *regexp.Regexp
}

// parsedPattern is a compiled path pattern.
type parsedPattern struct {
	segs     []segment
	catchAll bool // the last segment is kindCatchAll
}

// parsePattern compiles pattern. Diagnostics get their Code, Field, Start,
// End and Message set; the caller fills in the route.
func parsePattern(pattern string, opts Options) (parsedPattern, []Diagnostic) {
	var p parsedPattern
	var diags []Diagnostic
	add := func(code string, start, end int, msg string) {
		diags = append(diags, Diagnostic{Code: code, Field: FieldPattern, Start: start, End: end, Message: msg})
	}

	if pattern == "" {
		add(CodeEmptyPattern, 0, 0, "The route path is empty. Start it with /, for example /users.")
		return p, diags
	}
	if pattern[0] != '/' {
		add(CodeMissingLeadingSlash, 0, 0, "The route path must start with /.")
		return p, diags
	}
	if i := strings.IndexAny(pattern, "?#"); i >= 0 {
		add(CodeQueryInPattern, i, len(pattern),
			"The route path cannot contain a query string or fragment; routes match the path only.")
		return p, diags
	}
	if pattern == "/" {
		return p, nil
	}

	names := map[string]bool{}
	body := pattern[1:]
	pos := 1
	parts := strings.Split(body, "/")
	for i, s := range parts {
		start, end := pos, pos+len(s)
		pos = end + 1
		last := i == len(parts)-1

		if p.catchAll {
			// Only a trailing slash may follow a catch-all; it absorbs it.
			if last && s == "" {
				break
			}
			add(CodeCatchAllNotLast, start-1, len(pattern),
				"A catch-all (** or *name) must be the last segment of the route path.")
			return p, diags
		}

		if s == "" {
			if last {
				if opts.StrictTrailingSlash {
					p.segs = append(p.segs, segment{kind: kindLiteral})
				}
				break
			}
			add(CodeEmptySegment, start-1, end+1, "The route path has an empty segment (//).")
			continue
		}

		seg, d := parseSegment(s, start, opts)
		if d != nil {
			diags = append(diags, *d)
			continue
		}
		if seg.name != "" {
			if names[seg.name] {
				add(CodeDuplicateParam, start, end,
					"The parameter name \""+seg.name+"\" is used more than once in the route path.")
				continue
			}
			names[seg.name] = true
		}
		p.segs = append(p.segs, seg)
		if seg.kind == kindCatchAll {
			p.catchAll = true
		}
	}
	return p, diags
}

// parseSegment compiles one non-empty segment s that starts at byte start
// of the pattern.
func parseSegment(s string, start int, opts Options) (segment, *Diagnostic) {
	end := start + len(s)
	diag := func(code string, st, en int, msg string) (segment, *Diagnostic) {
		return segment{}, &Diagnostic{Code: code, Field: FieldPattern, Start: st, End: en, Message: msg}
	}

	switch {
	case s == "**":
		return segment{kind: kindCatchAll}, nil
	case s == "*":
		return segment{kind: kindWildcard}, nil
	case s[0] == '*':
		name := s[1:]
		if !validName(name) {
			return diag(CodeInvalidParamName, start+1, end, invalidNameMsg(name))
		}
		return segment{kind: kindCatchAll, name: name}, nil
	case s[0] == ':':
		name := s[1:]
		if !validName(name) {
			return diag(CodeInvalidParamName, start+1, end, invalidNameMsg(name))
		}
		return segment{kind: kindParam, name: name}, nil
	case s[0] == '{':
		if s[len(s)-1] != '}' || len(s) < 2 {
			return diag(CodeInvalidSegment, start, end,
				"A parameter in braces must fill the whole segment and end with }, for example {id}.")
		}
		inner := s[1 : len(s)-1]
		name, reSrc, hasRe := strings.Cut(inner, ":")
		if !validName(name) {
			return diag(CodeInvalidParamName, start+1, start+1+len(name), invalidNameMsg(name))
		}
		if !hasRe {
			return segment{kind: kindParam, name: name}, nil
		}
		reStart := start + 2 + len(name)
		if reSrc == "" {
			return diag(CodeInvalidRegex, reStart, end-1,
				"The constraint of parameter \""+name+"\" is empty. Write a regular expression after the colon, for example {id:[0-9]+}.")
		}
		re, err := regexp.Compile("^(?:" + reSrc + ")$")
		if err != nil {
			return diag(CodeInvalidRegex, reStart, end-1,
				"The constraint of parameter \""+name+"\" is not a valid regular expression: "+err.Error()+".")
		}
		return segment{kind: kindConstraint, name: name, reSrc: reSrc, re: re}, nil
	}

	if i := strings.IndexAny(s, "{}*"); i >= 0 {
		return diag(CodeInvalidSegment, start+i, start+i+1,
			"Parameters and wildcards must fill a whole segment; \""+s[i:i+1]+"\" cannot appear inside literal text.")
	}
	dec, err := url.PathUnescape(s)
	if err != nil {
		i := strings.IndexByte(s, '%')
		return diag(CodeInvalidEscape, start+i, end, "The route path has an invalid percent-escape.")
	}
	if opts.CaseInsensitive {
		dec = strings.ToLower(dec)
	}
	return segment{kind: kindLiteral, lit: dec}, nil
}

// validName reports whether name is a parameter name: a letter or _
// followed by letters, digits or _ (ASCII).
func validName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_', 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z':
		case '0' <= c && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func invalidNameMsg(name string) string {
	if name == "" {
		return "A parameter needs a name, for example :id or {id}."
	}
	return "\"" + name + "\" is not a valid parameter name. Use letters, digits and _, starting with a letter or _."
}

// compareSegs orders two patterns by specificity: negative when a is more
// specific than b. Segments are compared left to right by kind; a pattern
// that has ended ranks below any segment except a catch-all.
func compareSegs(a, b []segment) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		ka, kb := kindEnd, kindEnd
		if i < len(a) {
			ka = a[i].kind
		}
		if i < len(b) {
			kb = b[i].kind
		}
		if ka != kb {
			return int(ka) - int(kb)
		}
	}
	return 0
}

// canonicalKey identifies the set of paths a pattern matches: parameter
// names are left out, ** and *name are the same.
func canonicalKey(segs []segment) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteByte('/')
		b.WriteByte('0' + byte(s.kind))
		switch s.kind {
		case kindLiteral:
			b.WriteString(url.PathEscape(s.lit))
		case kindConstraint:
			b.WriteString(url.PathEscape(s.reSrc))
		}
	}
	return b.String()
}
