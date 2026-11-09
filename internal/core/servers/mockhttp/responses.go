package mockhttp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/assert"
	"github.com/sorrelfox/postsonde/internal/core/jsonpath"
)

// Conditional and header-selected responses (MOCK-05, MOCK-07); their
// bodies may be templates (MOCK-06, template.go). A route's
// own status, headers and body are its default response; Route.Responses
// are tried in order before it, and the first whose rules all match
// answers. The SelectHeader request header picks a response by name or
// status code instead. See docs/mock-routes.md (Responses).

// SelectHeader is the request header that picks a route's response by
// name, or by status code (MOCK-07), overriding the rules.
const SelectHeader = "X-PostSonde-Response"

// DefaultResponseName selects the route's own response with SelectHeader;
// no other response can have it.
const DefaultResponseName = "default"

// Limits of a route's responses.
const (
	// MaxResponses is the most responses one route has besides its default.
	MaxResponses = 100
	// MaxRules is the most rules one response has.
	MaxRules = 50
	// MaxMatchBodyBytes is how much of a request body the rules see.
	MaxMatchBodyBytes = 1 << 20
)

// Rule sources: where a rule finds its value.
const (
	// SourceQuery is a query parameter (Rule.Name).
	SourceQuery = "query"
	// SourceHeader is a request header (Rule.Name).
	SourceHeader = "header"
	// SourceBody is the whole request body as text.
	SourceBody = "body"
	// SourceJSON is the request body read as JSON, at the JSONPath
	// Rule.Name (docs/jsonpath.md).
	SourceJSON = "json"
)

// Rule operators: the assertion operators of the same name
// (docs/assertions.md).
const (
	OpEquals      = "equals"
	OpNotEquals   = "not_equals"
	OpContains    = "contains"
	OpNotContains = "not_contains"
	OpMatches     = "matches"
	OpExists      = "exists"
	OpNotExists   = "not_exists"
)

// Response is one of a route's other responses (MOCK-05): its status,
// headers and body, what it is called, and when it answers.
type Response struct {
	// Name selects the response with SelectHeader (letter case ignored);
	// unique within the route, not "default". "" cannot be selected by
	// name.
	Name string
	// Status is 200–599.
	Status  int
	Headers []Header
	Body    Body
	// Rules must all match for the response to answer. A response without
	// rules answers only when SelectHeader names it.
	Rules []Rule
	// Templated renders the body and header values with the request's
	// values and dynamic values (MOCK-06, template.go).
	Templated bool
}

// Rule is one condition on the request.
type Rule struct {
	// Source is "query", "header", "body" or "json".
	Source string
	// Name is the query parameter, the header name or the JSONPath; unused
	// for "body".
	Name string
	// Op is an operator: equals, not_equals, contains, not_contains,
	// matches (a Go regular expression), exists or not_exists.
	Op string
	// Value is what equals, contains and matches compare with.
	Value string
}

// reply is a prepared response: the route's default or one of its other
// responses.
type reply struct {
	// name is the response's name ("default" for the route's own).
	name   string
	status int
	header http.Header
	body   []byte
	rules  []rule
	// templated: body and header values are templates (render).
	templated bool
}

// rule is a checked Rule, with its JSONPath parsed.
type rule struct {
	Rule
	path *jsonpath.Path
}

// servedRoute is a route ready to answer.
type servedRoute struct {
	route Route
	def   *reply
	// alts are the other responses, in order.
	alts []*reply
	// templated: a response is templated, so it may read the body.
	templated bool
}

// prepare builds the responses of r, which has no diagnostics.
func prepare(r Route) *servedRoute {
	s := &servedRoute{route: r, def: prepareReply(DefaultResponseName, r.Status, r.Headers, r.Body)}
	s.def.templated = r.Templated
	s.templated = r.Templated
	for _, alt := range r.Responses {
		p := prepareReply(strings.TrimSpace(alt.Name), alt.Status, alt.Headers, alt.Body)
		p.templated = alt.Templated
		s.templated = s.templated || alt.Templated
		for _, ru := range alt.Rules {
			c := rule{Rule: ru}
			if ru.Source == SourceJSON {
				c.path, _ = jsonpath.Parse(ru.Name) // checked already
			}
			p.rules = append(p.rules, c)
		}
		s.alts = append(s.alts, p)
	}
	return s
}

// prepareReply builds the headers and body of a response. The body's type
// gives the Content-Type unless an enabled header sets one; Content-Length
// and Transfer-Encoding rows are ignored, since the server sets them.
func prepareReply(name string, status int, headers []Header, body Body) *reply {
	p := &reply{name: name, status: status, header: http.Header{}}
	if body.Mode == BodyRaw {
		p.body = []byte(body.Text)
		if body.Type == BodyTypeBase64 {
			p.body, _ = decodeBase64(body.Text) // checked already
		}
		p.header.Set("Content-Type", contentType(body.Type))
	}
	overridden := false
	for _, h := range headers {
		if !h.Enabled || h.Name == "" {
			continue
		}
		key := http.CanonicalHeaderKey(h.Name)
		switch key {
		case "Content-Length", "Transfer-Encoding":
			continue
		case "Content-Type":
			if !overridden {
				p.header.Del(key)
				overridden = true
			}
		}
		p.header[key] = append(p.header[key], h.Value)
	}
	return p
}

// requestData is what the rules, and the response body, can use of a
// request.
type requestData struct {
	ctx    context.Context
	method string
	host   string
	// path is the decoded path, url the request target as sent.
	path string
	url  string
	// wildcard is what the route's ** or *name matched.
	wildcard string
	query    url.Values
	header   http.Header
	// body is the first MaxMatchBodyBytes of the request body.
	body []byte
	// params are the route's path parameters.
	params []Param

	jsonDone bool
	jsonDoc  any
	jsonOK   bool
}

// Param is a path parameter of the matched route.
type Param struct {
	Name, Value string
}

func newRequestData(r *http.Request, body []byte, params []Param, wildcard string) *requestData {
	return &requestData{
		ctx: r.Context(), method: r.Method, host: r.Host, path: r.URL.Path, url: r.RequestURI, wildcard: wildcard,
		query: r.URL.Query(), header: r.Header, body: body, params: params,
	}
}

// json is the body read as JSON, once; false when it is not JSON.
func (d *requestData) json() (any, bool) {
	if !d.jsonDone {
		d.jsonDone = true
		doc, err := jsonpath.Decode(d.body)
		d.jsonDoc, d.jsonOK = doc, err == nil
	}
	return d.jsonDoc, d.jsonOK
}

// ruleLimits bound one rule's JSONPath evaluation.
var ruleLimits = jsonpath.Limits{MaxSteps: 1_000_000}

// values are the values the rule's source has in the request: every
// value of a repeated parameter or header, every node of a JSONPath.
func (ru rule) values(d *requestData) []any {
	var out []any
	switch ru.Source {
	case SourceQuery:
		for _, v := range d.query[ru.Name] {
			out = append(out, v)
		}
	case SourceHeader:
		if http.CanonicalHeaderKey(ru.Name) == "Host" {
			if d.host != "" {
				out = append(out, d.host)
			}
			break
		}
		for _, v := range d.header.Values(ru.Name) {
			out = append(out, v)
		}
	case SourceBody:
		if len(d.body) > 0 {
			out = append(out, string(d.body))
		}
	case SourceJSON:
		doc, ok := d.json()
		if !ok || ru.path == nil {
			return nil
		}
		nodes, err := ru.path.Eval(d.ctx, doc, ruleLimits)
		if err != nil {
			return nil
		}
		for _, n := range nodes {
			out = append(out, n.Value)
		}
	}
	return out
}

// match reports whether the request meets the rule. equals, contains and
// matches need one value that does; not_equals and not_contains need
// none (so they match when there is no value).
func (ru rule) match(d *requestData) bool {
	vals := ru.values(d)
	var op assert.Operator
	negate := false
	switch ru.Op {
	case OpExists:
		return len(vals) > 0
	case OpNotExists:
		return len(vals) == 0
	case OpNotEquals:
		op, negate = assert.OpEquals, true
	case OpNotContains:
		op, negate = assert.OpContains, true
	default:
		op = assert.Operator(ru.Op)
	}
	found := false
	for _, v := range vals {
		if ok, err := assert.Compare(op, ru.Value, true, v); err == nil && ok {
			found = true
			break
		}
	}
	return found != negate
}

// matches reports whether every rule of p matches; false without rules.
func (p *reply) matches(d *requestData) bool {
	if len(p.rules) == 0 {
		return false
	}
	for _, ru := range p.rules {
		if !ru.match(d) {
			return false
		}
	}
	return true
}

// selectError is a SelectHeader value that names no response.
type selectError struct {
	value string
}

// choose picks the response: the one SelectHeader names, else the first
// whose rules match, else the default. sel is the header's value, "" when
// it is absent.
func (s *servedRoute) choose(d *requestData, sel string) (*reply, *selectError) {
	if sel != "" {
		return s.selected(sel)
	}
	for _, alt := range s.alts {
		if alt.matches(d) {
			return alt, nil
		}
	}
	return s.def, nil
}

// selected is the response sel names: "default", a response's name, or a
// status code, which picks the first response with that status (the
// default last) or, when none has it, an empty response with it.
func (s *servedRoute) selected(sel string) (*reply, *selectError) {
	if strings.EqualFold(sel, DefaultResponseName) {
		return s.def, nil
	}
	for _, alt := range s.alts {
		if alt.name != "" && strings.EqualFold(alt.name, sel) {
			return alt, nil
		}
	}
	code, err := strconv.Atoi(sel)
	if err != nil || len(sel) != 3 || code < 200 || code > 599 {
		return nil, &selectError{value: sel}
	}
	for _, alt := range s.alts {
		if alt.status == code {
			return alt, nil
		}
	}
	if s.def.status == code {
		return s.def, nil
	}
	return &reply{name: "status " + sel, status: code, header: http.Header{}}, nil
}

// names are the names SelectHeader takes for this route.
func (s *servedRoute) names() []string {
	out := []string{DefaultResponseName}
	for _, alt := range s.alts {
		if alt.name != "" {
			out = append(out, alt.name)
		}
	}
	return out
}

// checkRule returns what is wrong with ru, or "".
func checkRule(ru Rule) string {
	switch ru.Source {
	case SourceQuery, SourceHeader:
		if ru.Name == "" {
			if ru.Source == SourceQuery {
				return "The query parameter's name is missing."
			}
			return "The header name is missing."
		}
		if ru.Source == SourceHeader && !isToken(ru.Name) {
			return fmt.Sprintf("%q is not a valid header name.", ru.Name)
		}
	case SourceBody:
	case SourceJSON:
		if strings.TrimSpace(ru.Name) == "" {
			return "The JSONPath is missing."
		}
		if _, err := jsonpath.Parse(ru.Name); err != nil {
			if se, ok := err.(*jsonpath.SyntaxError); ok {
				return "Invalid JSONPath: " + se.Message + "."
			}
			return err.Error()
		}
	default:
		return fmt.Sprintf("%q is not a rule source: use query, header, body or json.", ru.Source)
	}
	switch ru.Op {
	case OpEquals, OpNotEquals, OpContains, OpNotContains, OpExists, OpNotExists:
	case OpMatches:
		if _, err := assert.Compare(assert.OpMatches, ru.Value, true, ""); err != nil {
			return err.Error()
		}
	default:
		return fmt.Sprintf("%q is not a rule operator.", ru.Op)
	}
	return ""
}
