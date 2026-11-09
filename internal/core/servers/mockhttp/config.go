package mockhttp

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockroute"
)

// Limits of a configuration.
const (
	// MaxRoutes is the most routes one server has.
	MaxRoutes = 1000
	// MaxBodyBytes is the largest response body of one route.
	MaxBodyBytes = 10 << 20
	// MaxDelay is the longest response delay.
	MaxDelay = 5 * time.Minute
)

// Body modes.
const (
	BodyNone = "none"
	BodyRaw  = "raw"
)

// BodyTypeBase64 is the raw body type of a binary body: Text is its bytes
// in standard Base64 (line breaks and spaces are ignored), sent decoded
// with the implicit Content-Type application/octet-stream. Such a body
// cannot be templated. Mock routes made from a response example that is
// not UTF-8 have it (docs/mock-routes.md#routes-from-a-response).
const BodyTypeBase64 = "base64"

// Delay modes.
const (
	DelayNone   = "none"
	DelayFixed  = "fixed"
	DelayRandom = "random"
)

// MethodAny accepts every method (mockroute also takes "*" and "").
const MethodAny = "ANY"

// Config is the configuration of a mock server (Spec.Config).
type Config struct {
	Routes []Route
	// StrictTrailingSlash and CaseInsensitive are the mockroute.Options of
	// every route.
	StrictTrailingSlash bool
	CaseInsensitive     bool
}

// Route is one hand-made route (MOCK-01): a method and a path pattern
// (docs/mock-routes.md) and the response it gets.
type Route struct {
	// ID identifies the route in diagnostics and traffic; NormalizeConfig
	// fills in an empty one.
	ID      string
	Enabled bool
	// Method is an HTTP method, or ANY.
	Method string
	// Path is a mockroute pattern ("/users/:id", "/files/**").
	Path string
	// Status is 200–599.
	Status  int
	Headers []Header
	Body    Body
	// Delay holds back every response of the route.
	Delay Delay
	// Responses are the route's other responses (MOCK-05, MOCK-07), tried
	// in order before Status, Headers and Body, its default response.
	Responses []Response
	// Templated renders the default response's body and header values
	// with the request's values (MOCK-06); each of Responses has its own.
	Templated bool
}

// Header is one response header row. Disabled rows are not sent.
type Header struct {
	Enabled bool
	Name    string
	Value   string
}

// Body is the response body: the "none" and "raw" modes of a request body
// (projectfile.Body), so a request's body can become a route's later.
type Body struct {
	// Mode is "none" or "raw"; "" is none.
	Mode string
	// Type is the raw body's language: "json", "xml", "html", "text" or
	// "yaml"; it gives the implicit Content-Type. "base64"
	// (BodyTypeBase64) is a binary body written in Base64.
	Type string
	Text string
}

// Delay holds the response back (MOCK-04).
type Delay struct {
	// Mode is "none", "fixed" (Ms) or "random" (between MinMs and MaxMs,
	// both included); "" is none.
	Mode  string
	Ms    int
	MinMs int
	MaxMs int
}

// DefaultConfig is the configuration of a new mock server: one example
// route.
func DefaultConfig() Config {
	return Config{Routes: []Route{{
		ID:      "rt_hello",
		Enabled: true,
		Method:  "GET",
		Path:    "/hello",
		Status:  200,
		Body:    Body{Mode: BodyRaw, Type: "json", Text: "{\n  \"message\": \"Hello from PostSonde\"\n}"},
		Delay:   Delay{Mode: DelayNone},
	}}}
}

// Error codes of NormalizeConfig.
const (
	CodeInvalidConfig = "mockhttp.invalid_config"
	CodeTooManyRoutes = "mockhttp.too_many_routes"
	CodeBodyTooLarge  = "mockhttp.body_too_large"
	// CodeTooManyResponses: a route has more than MaxResponses responses,
	// or a response more than MaxRules rules.
	CodeTooManyResponses = "mockhttp.too_many_responses"
)

// Normalize checks the limits of c and returns a copy that shares nothing
// with it: empty IDs filled in, empty body and delay modes set to none,
// the method, path and response names trimmed of spaces.
func Normalize(c Config) (Config, error) {
	if len(c.Routes) > MaxRoutes {
		return Config{}, errs.New(errs.InvalidArgument, fmt.Sprintf("A mock server can have at most %d routes.", MaxRoutes)).
			WithCode(CodeTooManyRoutes)
	}
	out := Config{StrictTrailingSlash: c.StrictTrailingSlash, CaseInsensitive: c.CaseInsensitive, Routes: make([]Route, len(c.Routes))}
	for i, r := range c.Routes {
		if len(r.Body.Text) > MaxBodyBytes {
			return Config{}, errs.New(errs.InvalidArgument, fmt.Sprintf("The body of route %d is larger than %d MiB.", i+1, MaxBodyBytes>>20)).
				WithCode(CodeBodyTooLarge).WithDetail("routeId", r.ID)
		}
		if len(r.Responses) > MaxResponses {
			return Config{}, errs.New(errs.InvalidArgument, fmt.Sprintf("Route %d has more than %d responses.", i+1, MaxResponses)).
				WithCode(CodeTooManyResponses).WithDetail("routeId", r.ID)
		}
		r.Headers = slices.Clone(r.Headers)
		if r.Responses != nil {
			alts := make([]Response, len(r.Responses))
			for j, alt := range r.Responses {
				if len(alt.Body.Text) > MaxBodyBytes {
					return Config{}, errs.New(errs.InvalidArgument, fmt.Sprintf("The body of response %d of route %d is larger than %d MiB.", j+1, i+1, MaxBodyBytes>>20)).
						WithCode(CodeBodyTooLarge).WithDetail("routeId", r.ID)
				}
				if len(alt.Rules) > MaxRules {
					return Config{}, errs.New(errs.InvalidArgument, fmt.Sprintf("Response %d of route %d has more than %d rules.", j+1, i+1, MaxRules)).
						WithCode(CodeTooManyResponses).WithDetail("routeId", r.ID)
				}
				alt.Name = strings.TrimSpace(alt.Name)
				alt.Headers = slices.Clone(alt.Headers)
				alt.Rules = slices.Clone(alt.Rules)
				if alt.Body.Mode == "" {
					alt.Body.Mode = BodyNone
				}
				alts[j] = alt
			}
			r.Responses = alts
		}
		if r.ID == "" {
			r.ID = newRouteID()
		}
		r.Method = strings.TrimSpace(r.Method)
		r.Path = strings.TrimSpace(r.Path)
		if r.Body.Mode == "" {
			r.Body.Mode = BodyNone
		}
		if r.Delay.Mode == "" {
			r.Delay.Mode = DelayNone
		}
		out.Routes[i] = r
	}
	return out, nil
}

func newRouteID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "rt_" + hex.EncodeToString(b[:])
}

// Diagnostic codes of mockhttp. The mockroute codes (docs/mock-routes.md)
// come too. Every diagnostic means the route is not served.
const (
	CodeInvalidStatus      = "mockhttp.invalid_status"
	CodeInvalidHeaderName  = "mockhttp.invalid_header_name"
	CodeInvalidHeaderValue = "mockhttp.invalid_header_value"
	CodeInvalidBody        = "mockhttp.invalid_body"
	CodeInvalidDelay       = "mockhttp.invalid_delay"
	// CodeInvalidResponseName: a response name with a control character,
	// or "default".
	CodeInvalidResponseName = "mockhttp.invalid_response_name"
	// CodeDuplicateResponseName: two responses of a route with the same
	// name (letter case ignored).
	CodeDuplicateResponseName = "mockhttp.duplicate_response_name"
	// CodeInvalidRule: a rule with an unknown source or operator, a
	// missing name, an invalid header name, JSONPath or regular
	// expression.
	CodeInvalidRule = "mockhttp.invalid_rule"
)

// Fields a Diagnostic points to, besides the mockroute ones (the pattern
// field is the route's Path).
const (
	FieldStatus = "status"
	FieldHeader = "header"
	FieldBody   = "body"
	FieldDelay  = "delay"
	// FieldName is a response's name.
	FieldName = "name"
	// FieldRule is a response's rule (Diagnostic.RuleIndex).
	FieldRule = "rule"
)

// Diagnostic is one problem with a route.
type Diagnostic struct {
	Code string
	// RouteIndex is the index in Config.Routes; RouteID the route's ID.
	RouteIndex int
	RouteID    string
	// Field is mockroute.FieldPattern (the Path), mockroute.FieldMethod,
	// mockroute.FieldID or a Field constant.
	Field string
	// Start and End are the byte range [Start, End) in the pattern or
	// method; 0 for the other fields.
	Start, End int
	// ResponseIndex is the index in Route.Responses when the problem is in
	// one of them (FieldName, FieldRule, FieldStatus, FieldHeader,
	// FieldBody), else -1 (the route and its default response).
	ResponseIndex int
	// HeaderIndex is the index in the response's Headers for FieldHeader,
	// else -1.
	HeaderIndex int
	// RuleIndex is the index in Response.Rules for FieldRule, else -1.
	RuleIndex int
	Message   string
}

// Check returns the diagnostics of every route of c, disabled ones
// included, in route order.
func Check(c Config) []Diagnostic {
	_, diags := compile(c)
	return diags
}

var dupRouteRef = regexp.MustCompile(`route (\d+),`)

// compile checks c and builds what serving needs from the enabled routes
// without diagnostics.
func compile(c Config) (*compiled, []Diagnostic) {
	opts := mockroute.Options{StrictTrailingSlash: c.StrictTrailingSlash, CaseInsensitive: c.CaseInsensitive}
	// Pattern and method problems first, then the response's, per route.
	byRoute := make([][]Diagnostic, len(c.Routes))

	// Patterns and methods: the enabled routes together (duplicates count
	// only among routes that are served), each disabled route alone.
	var enabled []mockroute.Route
	var enabledIdx []int
	for i, r := range c.Routes {
		route := mockroute.Route{ID: r.ID, Method: r.Method, Pattern: r.Path}
		if !r.Enabled {
			_, ds := mockroute.Compile([]mockroute.Route{route}, opts)
			byRoute[i] = fromRoute(ds, func(int) int { return i })
			continue
		}
		enabled = append(enabled, route)
		enabledIdx = append(enabledIdx, i)
	}
	_, ds := mockroute.Compile(enabled, opts)
	for _, d := range fromRoute(ds, func(j int) int { return enabledIdx[j] }) {
		byRoute[d.RouteIndex] = append(byRoute[d.RouteIndex], d)
	}
	for i, r := range c.Routes {
		byRoute[i] = append(byRoute[i], checkResponse(i, r)...)
	}

	var diags []Diagnostic
	out := &compiled{}
	var served []mockroute.Route
	for _, i := range enabledIdx {
		if len(byRoute[i]) > 0 {
			continue
		}
		r := c.Routes[i]
		served = append(served, mockroute.Route{ID: r.ID, Method: r.Method, Pattern: r.Path})
		out.routes = append(out.routes, prepare(r))
	}
	for _, d := range byRoute {
		diags = append(diags, d...)
	}
	out.matcher, _ = mockroute.Compile(served, opts)
	return out, diags
}

// fromRoute converts mockroute diagnostics; index maps their RouteIndex to
// the index in Config.Routes.
func fromRoute(ds []mockroute.Diagnostic, index func(int) int) []Diagnostic {
	out := make([]Diagnostic, 0, len(ds))
	for _, d := range ds {
		msg := d.Message
		if d.Code == mockroute.CodeDuplicateRoute {
			msg = dupRouteRef.ReplaceAllStringFunc(msg, func(m string) string {
				n, err := strconv.Atoi(dupRouteRef.FindStringSubmatch(m)[1])
				if err != nil {
					return m
				}
				return "route " + strconv.Itoa(index(n-1)+1) + ","
			})
		}
		out = append(out, Diagnostic{
			Code: d.Code, RouteIndex: index(d.RouteIndex), RouteID: d.RouteID, Field: d.Field,
			Start: d.Start, End: d.End, ResponseIndex: -1, HeaderIndex: -1, RuleIndex: -1, Message: msg,
		})
	}
	return out
}

// checkResponse checks the responses and delay of route i.
func checkResponse(i int, r Route) []Diagnostic {
	var out []Diagnostic
	resp := -1
	add := func(code, field string, header int, msg string) {
		out = append(out, Diagnostic{Code: code, RouteIndex: i, RouteID: r.ID, Field: field, ResponseIndex: resp, HeaderIndex: header, RuleIndex: -1, Message: msg})
	}
	checkReply(add, r.Status, r.Headers, r.Body, r.Templated)
	seen := map[string]int{}
	for j, alt := range r.Responses {
		resp = j
		name := strings.TrimSpace(alt.Name)
		key := strings.ToLower(name)
		switch {
		case name == "":
		case strings.EqualFold(name, DefaultResponseName):
			add(CodeInvalidResponseName, FieldName, -1, `"default" is the route's own response; give this one another name.`)
		case !validHeaderValue(name):
			add(CodeInvalidResponseName, FieldName, -1, "The name contains a line break or control character.")
		default:
			if first, dup := seen[key]; dup {
				add(CodeDuplicateResponseName, FieldName, -1, fmt.Sprintf("Response %d is also called %q.", first+1, name))
			} else {
				seen[key] = j
			}
		}
		for k, ru := range alt.Rules {
			if msg := checkRule(ru); msg != "" {
				out = append(out, Diagnostic{Code: CodeInvalidRule, RouteIndex: i, RouteID: r.ID, Field: FieldRule, ResponseIndex: j, HeaderIndex: -1, RuleIndex: k, Message: msg})
			}
		}
		checkReply(add, alt.Status, alt.Headers, alt.Body, alt.Templated)
	}
	resp = -1
	maxMs := int(MaxDelay / time.Millisecond)
	inRange := func(ms int) bool { return ms >= 0 && ms <= maxMs }
	switch r.Delay.Mode {
	case DelayNone, "":
	case DelayFixed:
		if !inRange(r.Delay.Ms) {
			add(CodeInvalidDelay, FieldDelay, -1, fmt.Sprintf("The delay must be from 0 to %d ms.", maxMs))
		}
	case DelayRandom:
		switch {
		case !inRange(r.Delay.MinMs) || !inRange(r.Delay.MaxMs):
			add(CodeInvalidDelay, FieldDelay, -1, fmt.Sprintf("The delays must be from 0 to %d ms.", maxMs))
		case r.Delay.MinMs > r.Delay.MaxMs:
			add(CodeInvalidDelay, FieldDelay, -1, "The shortest delay is longer than the longest.")
		}
	default:
		add(CodeInvalidDelay, FieldDelay, -1, fmt.Sprintf("%q is not a delay mode.", r.Delay.Mode))
	}
	return out
}

// checkReply checks the status, headers and body of a response.
func checkReply(add func(code, field string, header int, msg string), status int, headers []Header, body Body, templated bool) {
	if status < 200 || status > 599 {
		add(CodeInvalidStatus, FieldStatus, -1, "The status must be a number from 200 to 599.")
	}
	for h, row := range headers {
		if !row.Enabled || (row.Name == "" && row.Value == "") {
			continue
		}
		if !isToken(row.Name) {
			add(CodeInvalidHeaderName, FieldHeader, h, fmt.Sprintf("%q is not a valid header name.", row.Name))
		} else if !validHeaderValue(row.Value) {
			add(CodeInvalidHeaderValue, FieldHeader, h, "The value of "+row.Name+" contains a line break or control character.")
		}
	}
	switch body.Mode {
	case BodyNone, "":
	case BodyRaw:
		switch {
		case body.Type == BodyTypeBase64:
			if _, err := decodeBase64(body.Text); err != nil {
				add(CodeInvalidBody, FieldBody, -1, "The body is not valid Base64.")
			} else if templated {
				add(CodeInvalidBody, FieldBody, -1, "A Base64 body cannot be templated; turn Templated off.")
			}
		case rawMediaType(body.Type) == "":
			add(CodeInvalidBody, FieldBody, -1, fmt.Sprintf("%q is not a body language PostSonde knows.", body.Type))
		}
	default:
		add(CodeInvalidBody, FieldBody, -1, fmt.Sprintf("A mock response body cannot be %q.", body.Mode))
	}
}

// decodeBase64 decodes a Base64 body, ignoring ASCII white space (a body
// wrapped over lines).
func decodeBase64(text string) ([]byte, error) {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, text)
	return base64.StdEncoding.DecodeString(clean)
}

func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// validHeaderValue rejects control characters other than tab.
func validHeaderValue(v string) bool {
	for i := 0; i < len(v); i++ {
		if c := v[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}
