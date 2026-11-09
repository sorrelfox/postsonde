package mockroute

import (
	"cmp"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Route is a mock route as the user defined it.
type Route struct {
	ID string
	// Method is matched exactly (HTTP methods are case-sensitive). "", "*"
	// and "ANY" accept every method.
	Method  string
	Pattern string
}

// Options change how paths are matched. The zero value is the default.
type Options struct {
	// StrictTrailingSlash makes /users and /users/ different paths. By
	// default one trailing slash is ignored, in patterns and requests.
	StrictTrailingSlash bool
	// CaseInsensitive compares literal segments ignoring case. Parameter
	// values keep their case; constraints are not affected (use (?i)).
	CaseInsensitive bool
}

// Param is a path parameter of a match.
type Param struct {
	Name string
	// Value is the percent-decoded segment; Raw is the segment as it was in
	// the request path. For a named catch-all they are Wildcard and
	// WildcardRaw.
	Value string
	Raw   string
}

// Result is the outcome of Match.
type Result struct {
	// Matched is true when a route accepts the path and the method.
	Matched bool
	// Route and RouteIndex (the index given to Compile, -1 when nothing
	// matched) identify the route; RouteID is Route.ID.
	Route      Route
	RouteIndex int
	RouteID    string
	// Params are the named parameters in pattern order.
	Params []Param
	// Wildcard is the remainder matched by a catch-all: the decoded
	// segments joined with "/" (no leading slash, "" when empty).
	// WildcardRaw is the same remainder as written in the request, which
	// keeps an encoded %2F apart from a separator.
	Wildcard    string
	WildcardRaw string
	// HeadFallback is true when a HEAD request matched a GET route.
	HeadFallback bool
	// MethodNotAllowed is true when no route matched but some route
	// accepts the path with another method; Allowed then lists those
	// methods, sorted, for an Allow header (HEAD is added when GET is
	// there).
	MethodNotAllowed bool
	Allowed          []string
}

// Param returns the value of the named parameter.
func (r Result) Param(name string) (string, bool) {
	for _, p := range r.Params {
		if p.Name == name {
			return p.Value, true
		}
	}
	return "", false
}

// Matcher matches requests against compiled routes. It is safe for
// concurrent use.
type Matcher struct {
	opts   Options
	routes []*route // in specificity order
	// byFirst indexes routes whose first segment is a literal by that
	// literal; rest holds every other route.
	byFirst map[string][]*route
	rest    []*route
}

type route struct {
	src    Route
	index  int
	method string // "" accepts every method
	pat    parsedPattern
	rank   int // equal for routes of the same specificity
}

// Compile checks routes and builds a Matcher from the valid ones. Every
// route with a diagnostic is left out; the Matcher is never nil.
func Compile(routes []Route, opts Options) (*Matcher, []Diagnostic) {
	m := &Matcher{opts: opts, byFirst: map[string][]*route{}}
	var diags []Diagnostic
	ids := map[string]bool{}
	keys := map[string]int{}

	for i, rt := range routes {
		var rd []Diagnostic
		method, ok := normalizeMethod(rt.Method)
		if !ok {
			rd = append(rd, Diagnostic{Code: CodeInvalidMethod, Field: FieldMethod, End: len(rt.Method),
				Message: "\"" + rt.Method + "\" is not a valid HTTP method."})
		}
		pat, pd := parsePattern(rt.Pattern, opts)
		rd = append(rd, pd...)
		if len(rd) == 0 && rt.ID != "" && ids[rt.ID] {
			rd = append(rd, Diagnostic{Code: CodeDuplicateID, Field: FieldID, End: len(rt.ID),
				Message: "Another route has the ID \"" + rt.ID + "\"."})
		}
		if len(rd) == 0 {
			key := method + " " + canonicalKey(pat.segs)
			if first, dup := keys[key]; dup {
				rd = append(rd, Diagnostic{Code: CodeDuplicateRoute, Field: FieldPattern, End: len(rt.Pattern),
					Message: "This route matches the same method and paths as route " + strconv.Itoa(first+1) +
						", which is defined first, so it is never used."})
			} else {
				keys[key] = i
			}
		}
		if len(rd) > 0 {
			for j := range rd {
				rd[j].RouteIndex, rd[j].RouteID = i, rt.ID
			}
			diags = append(diags, rd...)
			continue
		}
		if rt.ID != "" {
			ids[rt.ID] = true
		}
		m.routes = append(m.routes, &route{src: rt, index: i, method: method, pat: pat})
	}

	slices.SortStableFunc(m.routes, func(a, b *route) int {
		return cmp.Or(compareSegs(a.pat.segs, b.pat.segs), cmp.Compare(a.index, b.index))
	})
	for i, r := range m.routes {
		if i > 0 {
			prev := m.routes[i-1]
			r.rank = prev.rank
			if compareSegs(prev.pat.segs, r.pat.segs) != 0 {
				r.rank++
			}
		}
		if segs := r.pat.segs; len(segs) > 0 && segs[0].kind == kindLiteral {
			m.byFirst[segs[0].lit] = append(m.byFirst[segs[0].lit], r)
		} else {
			m.rest = append(m.rest, r)
		}
	}
	return m, diags
}

// Len returns the number of routes in the Matcher.
func (m *Matcher) Len() int { return len(m.routes) }

// Match finds the route for a request. path is the escaped request path
// (for net/http, r.URL.EscapedPath() or the RequestURI), so that %2F stays
// inside its segment; anything from the first ? or # on is ignored. A path
// that does not start with / (an empty path counts as /) matches nothing.
//
// Among the routes whose pattern matches the path, the winner is the most
// specific pattern, then the best method (exact, then GET for HEAD, then
// any), then the route defined first.
func (m *Matcher) Match(method, path string) Result {
	res := Result{RouteIndex: -1}
	req, ok := splitPath(path, m.opts)
	if !ok {
		return res
	}

	var best *route
	bestRank, pathHit := 0, false
	visit := func(rs []*route) {
		for _, r := range rs {
			if !r.matches(&req) {
				continue
			}
			mRank, ok := methodRank(r.method, method)
			if !ok {
				pathHit = true
				continue
			}
			if best == nil || r.rank < best.rank ||
				(r.rank == best.rank && (mRank < bestRank || (mRank == bestRank && r.index < best.index))) {
				best, bestRank = r, mRank
			}
		}
	}
	if len(req.dec) > 0 {
		visit(m.byFirst[req.fold(0)])
	}
	visit(m.rest)

	if best == nil {
		if pathHit {
			res.MethodNotAllowed = true
			res.Allowed = m.allowed(&req)
		}
		return res
	}

	res.Matched = true
	res.Route, res.RouteIndex, res.RouteID = best.src, best.index, best.src.ID
	res.HeadFallback = bestRank == rankHeadFallback
	for i, s := range best.pat.segs {
		switch s.kind {
		case kindParam, kindConstraint:
			res.Params = append(res.Params, Param{Name: s.name, Value: req.dec[i], Raw: req.raw[i]})
		case kindCatchAll:
			res.Wildcard = strings.Join(req.dec[i:], "/")
			res.WildcardRaw = strings.Join(req.raw[i:], "/")
			if s.name != "" {
				res.Params = append(res.Params, Param{Name: s.name, Value: res.Wildcard, Raw: res.WildcardRaw})
			}
		}
	}
	return res
}

// allowed lists the methods of the routes that match the path.
func (m *Matcher) allowed(req *reqPath) []string {
	var out []string
	add := func(rs []*route) {
		for _, r := range rs {
			if r.matches(req) {
				out = append(out, r.method)
				if r.method == "GET" {
					out = append(out, "HEAD")
				}
			}
		}
	}
	if len(req.dec) > 0 {
		add(m.byFirst[req.fold(0)])
	}
	add(m.rest)
	slices.Sort(out)
	return slices.Compact(out)
}

func (r *route) matches(req *reqPath) bool {
	segs := r.pat.segs
	n := len(segs)
	if r.pat.catchAll {
		n--
		if len(req.dec) < n {
			return false
		}
	} else if len(req.dec) != n {
		return false
	}
	for i := 0; i < n; i++ {
		s := &segs[i]
		switch s.kind {
		case kindLiteral:
			if s.lit != req.fold(i) {
				return false
			}
		case kindConstraint:
			if req.dec[i] == "" || !s.re.MatchString(req.dec[i]) {
				return false
			}
		default: // kindParam, kindWildcard
			if req.dec[i] == "" {
				return false
			}
		}
	}
	return true
}

const (
	rankExact = iota
	rankHeadFallback
	rankAny
)

// methodRank reports whether a route with method rm accepts method m, and
// how well.
func methodRank(rm, m string) (int, bool) {
	switch {
	case rm == "":
		return rankAny, true
	case rm == m:
		return rankExact, true
	case rm == "GET" && m == "HEAD":
		return rankHeadFallback, true
	}
	return 0, false
}

// normalizeMethod returns "" for the any-method spellings and checks that
// other methods are HTTP tokens.
func normalizeMethod(m string) (string, bool) {
	if m == "" || m == "*" || m == "ANY" {
		return "", true
	}
	for i := 0; i < len(m); i++ {
		if !isTokenChar(m[i]) {
			return m, false
		}
	}
	return m, true
}

func isTokenChar(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// reqPath is a request path split into segments.
type reqPath struct {
	raw, dec []string
	lower    []string // lower-cased dec with CaseInsensitive, else nil
}

func (p *reqPath) fold(i int) string {
	if p.lower != nil {
		return p.lower[i]
	}
	return p.dec[i]
}

func splitPath(path string, opts Options) (reqPath, bool) {
	var p reqPath
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if path == "" {
		path = "/"
	}
	if path[0] != '/' {
		return p, false
	}
	if path == "/" {
		return p, true
	}
	p.raw = strings.Split(path[1:], "/")
	if !opts.StrictTrailingSlash && p.raw[len(p.raw)-1] == "" {
		p.raw = p.raw[:len(p.raw)-1]
	}
	p.dec = make([]string, len(p.raw))
	for i, s := range p.raw {
		p.dec[i] = s
		if strings.IndexByte(s, '%') >= 0 {
			if d, err := url.PathUnescape(s); err == nil {
				p.dec[i] = d
			}
		}
	}
	if opts.CaseInsensitive {
		p.lower = make([]string, len(p.dec))
		for i, s := range p.dec {
			p.lower[i] = strings.ToLower(s)
		}
	}
	return p, true
}
