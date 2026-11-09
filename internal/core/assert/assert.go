// Package assert evaluates declarative assertions against a response
// (TEST-02, TEST-03): a target (status code, header, cookie, body through
// JSONPath, XPath, a regular expression or as a whole, response time, body
// size), an operator and an expected value; matches_schema validates the
// target against a JSON Schema (TEST-07, internal/core/jsonschema). There is no scripting: every
// assertion is data, and every result says what was expected and what the
// response had. See docs/assertions.md.
//
//	results := assert.Evaluate(ctx, assert.Response{Status: 200, Body: body}, []assert.Assertion{
//		{Target: assert.TargetStatus, Op: assert.OpEquals, Expected: "200"},
//		{Target: assert.TargetJSONPath, Expr: "$.items", Op: assert.OpIsType, Expected: "array"},
//	})
//	for _, r := range results {
//		fmt.Println(r.Outcome, r.Message) // failed Expected status code to equal 200, but it was 404.
//	}
package assert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/jsonpath"
	"github.com/sorrelfox/postsonde/internal/core/jsonschema"
	"github.com/sorrelfox/postsonde/internal/core/xmldom"
	"github.com/sorrelfox/postsonde/internal/core/xpath"
)

// TargetKind is what part of the response an assertion checks.
type TargetKind string

// Targets (TEST-02).
const (
	// TargetStatus is the status code, a number.
	TargetStatus TargetKind = "status"
	// TargetHeader is the header named by Expr (case-insensitive); several
	// values are joined with ", ". It does not exist when absent.
	TargetHeader TargetKind = "header"
	// TargetCookie is the value of the cookie named by Expr (exact name)
	// set by the response's Set-Cookie headers; the last one wins.
	TargetCookie TargetKind = "cookie"
	// TargetJSONPath is the body parsed as JSON, queried with the RFC 9535
	// JSONPath in Expr. A singular query ($.a.b, $[0]) gives the value
	// or nothing; any other query gives the array of selected values, and
	// does not exist when it selects nothing.
	TargetJSONPath TargetKind = "jsonpath"
	// TargetXPath is the body parsed as XML, evaluated with the XPath 1.0
	// expression in Expr. A node-set gives the string-value of its only
	// node, the array of string-values of several, and nothing when
	// empty; a number, string or boolean result is that value.
	TargetXPath TargetKind = "xpath"
	// TargetRegex is the first match in the body of the regular
	// expression (RE2 syntax) in Expr: its first capture group when it has
	// one, the whole match otherwise. It does not exist without a match.
	TargetRegex TargetKind = "regex"
	// TargetBody is the whole body as text.
	TargetBody TargetKind = "body"
	// TargetTime is the response time in milliseconds, a number. An
	// expected value may carry a unit: "500ms", "1.5s".
	TargetTime TargetKind = "time"
	// TargetSize is the body size in bytes, a number.
	TargetSize TargetKind = "size"
	// TargetMessageCount is the number of messages a stream received
	// (TEST-08), a number. Only EvaluateStream has it.
	TargetMessageCount TargetKind = "message_count"
)

// Operator compares the target with the expected value.
type Operator string

// Operators (TEST-03).
const (
	OpEquals         Operator = "equals"
	OpNotEquals      Operator = "not_equals"
	OpContains       Operator = "contains"
	OpNotContains    Operator = "not_contains"
	OpExists         Operator = "exists"
	OpNotExists      Operator = "not_exists"
	OpIsEmpty        Operator = "is_empty"
	OpGreater        Operator = "gt"
	OpLess           Operator = "lt"
	OpGreaterOrEqual Operator = "gte"
	OpLessOrEqual    Operator = "lte"
	OpMatches        Operator = "matches"
	OpIsType         Operator = "is_type"
	// OpMatchesSchema validates the target against the JSON Schema in
	// Expected (TEST-07): the schema itself when Expected starts with "{"
	// (or is true or false), else the path of a schema file, read with
	// Options.LoadSchema. On the body target it validates the body parsed
	// as JSON; on the others, the target's value.
	OpMatchesSchema Operator = "matches_schema"
)

// Assertion is one check: Target (with Expr) · Op · Expected.
type Assertion struct {
	Target TargetKind `json:"target"`
	// Expr is the header or cookie name, the JSONPath, the XPath or the
	// regular expression; unused by the other targets.
	Expr string   `json:"expr,omitempty"`
	Op   Operator `json:"op"`
	// Expected is the value to compare with, as the user typed it, with
	// {{variables}} already rendered. exists and not_exists ignore it.
	Expected string `json:"expected,omitempty"`
	// Messages makes it a check on the messages of a stream (TEST-08,
	// EvaluateStream): the target is read from each message, and Messages
	// says which must satisfy it. "" checks the response as a whole.
	Messages MessageScope `json:"messages,omitempty"`
	// Index is the message MessagesNth checks, counted from 1.
	Index int `json:"index,omitempty"`
}

// Response is what assertions are evaluated against.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	// Time is the response time.
	Time time.Duration
	// Size is the body size in bytes; 0 means len(Body). Set it when Body
	// is truncated.
	Size int64
}

// Outcome of an assertion.
type Outcome string

const (
	// Passed: the response satisfies the assertion.
	Passed Outcome = "passed"
	// Failed: it does not; Message says what was expected and what the
	// response had.
	Failed Outcome = "failed"
	// Invalid: the assertion itself cannot be evaluated (an unknown
	// target or operator, a JSONPath, XPath or regular expression that
	// does not parse, a number expected where it is not one). Code says
	// which.
	Invalid Outcome = "invalid"
)

// Error codes of Invalid results.
const (
	CodeUnknownTarget   = "assert.unknown_target"
	CodeUnknownOperator = "assert.unknown_operator"
	CodeMissingExpr     = "assert.missing_expr"
	CodeInvalidRegex    = "assert.invalid_regex"
	CodeInvalidNumber   = "assert.invalid_number"
	CodeInvalidType     = "assert.invalid_type"
	// CodeMissingSchema: matches_schema without a schema or a file path.
	CodeMissingSchema = "assert.missing_schema"
	// CodeSchemaFileUnavailable: matches_schema names a schema file, but
	// the evaluation has no Options.LoadSchema to read it.
	CodeSchemaFileUnavailable = "assert.schema_file_unavailable"
	// Plus the jsonschema.* codes of a schema that does not compile or a
	// validation over its limits, and the codes of Options.LoadSchema's
	// errors.
	// Plus jsonpath.invalid_query, xpath.invalid_query and the
	// *.too_complex codes of those packages.
)

// Result is the outcome of one assertion.
type Result struct {
	Assertion Assertion `json:"assertion"`
	Outcome   Outcome   `json:"outcome"`
	// Actual is the target's value as shown to the user ("404",
	// "\"text/html\"", "[1,2]", "120 ms"); "" when it does not exist or
	// the assertion is invalid.
	Actual string `json:"actual"`
	// Exists is false when the target is not in the response.
	Exists bool `json:"exists"`
	// Message is a sentence with the expected and the actual value:
	// "Expected status code to equal 200, but it was 404."
	Message string `json:"message"`
	// Code is the error code of an Invalid result.
	Code string `json:"code,omitempty"`
	// SchemaErrors are the ways the value does not match the schema of a
	// failed matches_schema assertion, each with its location in the
	// value (at most jsonschema.DefaultLimits.MaxErrors).
	SchemaErrors []jsonschema.Error `json:"schemaErrors,omitempty"`
}

// Options are the evaluation's access to the outside world.
type Options struct {
	// LoadSchema reads the JSON Schema file a matches_schema assertion
	// names, as the user typed its path. Its error makes the assertion
	// invalid with the error's code and message (*errs.Error). Without
	// it, schema files cannot be used (CodeSchemaFileUnavailable).
	LoadSchema func(path string) ([]byte, error)
}

// Evaluate checks every assertion against resp, in order. The body is
// parsed at most once per format. ctx bounds JSONPath and XPath
// evaluation, which also stops over the default work limits of those
// packages.
func Evaluate(ctx context.Context, resp Response, list []Assertion) []Result {
	return EvaluateWith(ctx, resp, list, Options{})
}

// EvaluateWith is Evaluate with options: the loader of schema files. Each
// schema is read and compiled once per call.
func EvaluateWith(ctx context.Context, resp Response, list []Assertion, opts Options) []Result {
	ev := &evaluator{ctx: ctx, resp: resp, opts: opts}
	out := make([]Result, len(list))
	for i, a := range list {
		out[i] = ev.eval(a)
	}
	return out
}

// EvaluateOne checks a single assertion.
func EvaluateOne(ctx context.Context, resp Response, a Assertion) Result {
	return (&evaluator{ctx: ctx, resp: resp}).eval(a)
}

type evaluator struct {
	ctx  context.Context
	resp Response
	opts Options
	// stream is set by EvaluateStream.
	stream *Stream

	schemas map[string]compiledSchema

	jsonDone bool
	jsonDoc  any
	jsonErr  error
	xmlDone  bool
	xmlDoc   *xmldom.Document
	xmlErr   error
}

// value is a target's value in the JSON value model of jsonpath.Decode:
// string, json.Number, bool, nil, []any or *jsonpath.Object.
type value struct {
	exists bool
	v      any
	unit   string // " ms", " bytes": appended when shown
}

// invalidErr is an assertion that cannot be evaluated.
type invalidErr struct {
	code, msg string
}

func (e *invalidErr) Error() string { return e.msg }

func invalid(code, format string, args ...any) error {
	return &invalidErr{code: code, msg: fmt.Sprintf(format, args...)}
}

// failErr is a response that cannot provide the target (a body that is
// not JSON): the assertion fails with this message.
type failErr struct{ msg string }

func (e *failErr) Error() string { return e.msg }

func (ev *evaluator) eval(a Assertion) Result {
	if !knownOp(a.Op) {
		return invalidResult(Result{Assertion: a}, invalid(CodeUnknownOperator, "The operator %q is not known.", string(a.Op)))
	}
	if a.Messages != "" {
		return ev.evalMessages(a)
	}
	return ev.evalPlain(a)
}

// evalPlain checks a against the evaluator's response (for a message
// scope's single message, against that message).
func (ev *evaluator) evalPlain(a Assertion) Result {
	r := Result{Assertion: a}
	if a.Op == OpMatchesSchema {
		return ev.evalSchema(a)
	}
	val, err := ev.target(a)
	if err != nil {
		var fe *failErr
		if errors.As(err, &fe) {
			r.Outcome, r.Message = Failed, fmt.Sprintf("Expected %s to %s, but %s.", label(a), phrase(a), fe.msg)
			return r
		}
		return invalidResult(r, err)
	}
	r.Exists = val.exists
	if val.exists {
		r.Actual = show(val)
	}
	pass, err := check(a, val)
	if err != nil {
		return invalidResult(r, err)
	}
	if pass {
		r.Outcome = Passed
		r.Message = passMessage(a, r)
		return r
	}
	r.Outcome = Failed
	switch {
	case !val.exists && a.Op != OpNotExists:
		r.Message = fmt.Sprintf("Expected %s to %s, but it does not exist.", label(a), phrase(a))
	default:
		r.Message = fmt.Sprintf("Expected %s to %s, but it was %s.", label(a), phrase(a), r.Actual)
		if a.Op == OpIsType {
			r.Message = fmt.Sprintf("Expected %s to %s, but it was %s (%s).", label(a), phrase(a), typeName(val.v), r.Actual)
		}
	}
	return r
}

func invalidResult(r Result, err error) Result {
	r.Outcome, r.Actual, r.Exists = Invalid, "", false
	var ie *invalidErr
	var ee *errs.Error
	switch {
	case errors.As(err, &ie):
		r.Code, r.Message = ie.code, ie.msg
	case errors.As(err, &ee):
		r.Code, r.Message = ee.Code, ee.Message
	default:
		r.Message = err.Error()
	}
	return r
}

// passMessage: "Status code equals 200: it was 200."
func passMessage(a Assertion, r Result) string {
	if a.Op == OpNotExists {
		return upperFirst(label(a)) + " does not exist."
	}
	return fmt.Sprintf("%s %s: it was %s.", upperFirst(label(a)), phrase3(a), r.Actual)
}

func knownOp(op Operator) bool {
	switch op {
	case OpEquals, OpNotEquals, OpContains, OpNotContains, OpExists, OpNotExists, OpIsEmpty,
		OpGreater, OpLess, OpGreaterOrEqual, OpLessOrEqual, OpMatches, OpIsType, OpMatchesSchema:
		return true
	}
	return false
}

// ---- targets ----

func (ev *evaluator) target(a Assertion) (value, error) {
	switch a.Target {
	case TargetStatus:
		return value{exists: true, v: json.Number(strconv.Itoa(ev.resp.Status))}, nil
	case TargetTime:
		ms := float64(ev.resp.Time) / float64(time.Millisecond)
		return value{exists: true, v: json.Number(strconv.FormatFloat(ms, 'f', -1, 64)), unit: " ms"}, nil
	case TargetSize:
		n := ev.resp.Size
		if n == 0 {
			n = int64(len(ev.resp.Body))
		}
		return value{exists: true, v: json.Number(strconv.FormatInt(n, 10)), unit: " bytes"}, nil
	case TargetBody:
		return value{exists: true, v: string(ev.resp.Body)}, nil
	case TargetHeader:
		if strings.TrimSpace(a.Expr) == "" {
			return value{}, invalid(CodeMissingExpr, "The header name is missing.")
		}
		vs := ev.resp.Header.Values(strings.TrimSpace(a.Expr))
		if len(vs) == 0 {
			return value{}, nil
		}
		return value{exists: true, v: strings.Join(vs, ", ")}, nil
	case TargetCookie:
		if strings.TrimSpace(a.Expr) == "" {
			return value{}, invalid(CodeMissingExpr, "The cookie name is missing.")
		}
		return ev.cookie(strings.TrimSpace(a.Expr)), nil
	case TargetJSONPath:
		return ev.jsonPath(a.Expr)
	case TargetXPath:
		return ev.xPath(a.Expr)
	case TargetRegex:
		return ev.regex(a.Expr)
	case TargetMessageCount:
		if ev.stream == nil {
			return value{}, notStream()
		}
		return value{exists: true, v: json.Number(strconv.Itoa(ev.stream.total()))}, nil
	}
	return value{}, invalid(CodeUnknownTarget, "The target %q is not known.", string(a.Target))
}

func (ev *evaluator) cookie(name string) value {
	var out value
	for _, line := range ev.resp.Header.Values("Set-Cookie") {
		c, err := http.ParseSetCookie(line)
		if err != nil || c.Name != name {
			continue
		}
		out = value{exists: true, v: c.Value}
	}
	return out
}

func (ev *evaluator) jsonPath(expr string) (value, error) {
	if strings.TrimSpace(expr) == "" {
		return value{}, invalid(CodeMissingExpr, "The JSONPath is missing.")
	}
	p, err := jsonpath.Parse(expr)
	if err != nil {
		var se *jsonpath.SyntaxError
		if errors.As(err, &se) {
			return value{}, se.Err()
		}
		return value{}, err
	}
	doc, err := ev.bodyJSON()
	if err != nil {
		return value{}, err
	}
	nodes, err := p.Eval(ev.ctx, doc, jsonpath.DefaultLimits)
	if err != nil {
		return value{}, err
	}
	if p.Singular() {
		if len(nodes) == 0 {
			return value{}, nil
		}
		return value{exists: true, v: nodes[0].Value}, nil
	}
	if len(nodes) == 0 {
		return value{}, nil
	}
	return value{exists: true, v: nodes.Values()}, nil
}

// bodyJSON is the body parsed as JSON, once.
func (ev *evaluator) bodyJSON() (any, error) {
	if !ev.jsonDone {
		ev.jsonDone = true
		ev.jsonDoc, ev.jsonErr = jsonpath.Decode(ev.resp.Body)
	}
	if ev.jsonErr != nil {
		return nil, &failErr{"the body is not valid JSON"}
	}
	return ev.jsonDoc, nil
}

func (ev *evaluator) xPath(expr string) (value, error) {
	if strings.TrimSpace(expr) == "" {
		return value{}, invalid(CodeMissingExpr, "The XPath is missing.")
	}
	x, err := xpath.Parse(expr)
	if err != nil {
		var se *xpath.SyntaxError
		if errors.As(err, &se) {
			return value{}, se.Err()
		}
		return value{}, err
	}
	if !ev.xmlDone {
		ev.xmlDone = true
		ev.xmlDoc, ev.xmlErr = xmldom.ParseOptions(ev.ctx, ev.resp.Body, xmldom.Options{})
	}
	if ev.xmlErr != nil {
		return value{}, &failErr{"the body is not valid XML"}
	}
	res, err := x.Eval(ev.ctx, ev.xmlDoc.Root, xpath.Options{Limits: xpath.DefaultLimits})
	if err != nil {
		return value{}, err
	}
	switch res.Type {
	case xpath.NodeSet:
		switch len(res.Nodes) {
		case 0:
			return value{}, nil
		case 1:
			return value{exists: true, v: res.Nodes[0].StringValue()}, nil
		}
		vs := res.Values()
		list := make([]any, len(vs))
		for i, s := range vs {
			list[i] = s
		}
		return value{exists: true, v: list}, nil
	case xpath.Number:
		s := xpath.FormatNumber(res.Num)
		if _, ok := parseNumber(s); !ok {
			return value{exists: true, v: s}, nil // NaN, Infinity
		}
		return value{exists: true, v: json.Number(s)}, nil
	case xpath.Boolean:
		return value{exists: true, v: res.Bool}, nil
	}
	return value{exists: true, v: res.Str}, nil
}

func (ev *evaluator) regex(expr string) (value, error) {
	if expr == "" {
		return value{}, invalid(CodeMissingExpr, "The regular expression is missing.")
	}
	re, err := compile(expr)
	if err != nil {
		return value{}, err
	}
	m := re.FindSubmatch(ev.resp.Body)
	if m == nil {
		return value{}, nil
	}
	if len(m) > 1 {
		return value{exists: true, v: string(m[1])}, nil
	}
	return value{exists: true, v: string(m[0])}, nil
}

func compile(expr string) (*regexp.Regexp, error) {
	re, err := regexp.Compile(expr)
	if err != nil {
		msg := strings.TrimPrefix(err.Error(), "error parsing regexp: ")
		return nil, invalid(CodeInvalidRegex, "The regular expression %s is not valid: %s.", strconv.Quote(expr), msg)
	}
	return re, nil
}

// ---- operators ----

// Compare checks one value with op and expected the way an assertion
// does, for callers that find the value themselves (mock response rules,
// docs/mock-routes.md). v is in the value model of jsonpath.Decode: a
// string, json.Number, bool, nil, []any or *jsonpath.Object; exists false
// means there is no value. matches_schema is not supported. The error is
// an invalid operator, regular expression, number or type.
func Compare(op Operator, expected string, exists bool, v any) (bool, error) {
	if !knownOp(op) || op == OpMatchesSchema {
		return false, invalid(CodeUnknownOperator, "The operator %q is not known.", string(op))
	}
	return check(Assertion{Op: op, Expected: expected}, value{exists: exists, v: v})
}

func check(a Assertion, val value) (bool, error) {
	switch a.Op {
	case OpExists:
		return val.exists, nil
	case OpNotExists:
		return !val.exists, nil
	case OpMatches:
		re, err := compile(a.Expected)
		if err != nil {
			return false, err
		}
		return val.exists && re.MatchString(text(val.v)), nil
	case OpIsType:
		want := strings.ToLower(strings.TrimSpace(a.Expected))
		switch want {
		case "string", "number", "boolean", "null", "array", "object":
		case "bool":
			want = "boolean"
		default:
			return false, invalid(CodeInvalidType, "The type %q is not one of string, number, boolean, null, array or object.", a.Expected)
		}
		return val.exists && typeName(val.v) == want, nil
	case OpGreater, OpLess, OpGreaterOrEqual, OpLessOrEqual:
		want, ok := expectedNumber(a)
		if !ok {
			return false, invalid(CodeInvalidNumber, "The expected value %q is not a number.", a.Expected)
		}
		if !val.exists {
			return false, nil
		}
		got, ok := numberOf(val.v)
		if !ok {
			return false, nil
		}
		c := got.Cmp(want)
		switch a.Op {
		case OpGreater:
			return c > 0, nil
		case OpLess:
			return c < 0, nil
		case OpGreaterOrEqual:
			return c >= 0, nil
		}
		return c <= 0, nil
	}
	if !val.exists {
		return false, nil
	}
	switch a.Op {
	case OpEquals:
		return equals(a, val.v), nil
	case OpNotEquals:
		return !equals(a, val.v), nil
	case OpContains:
		return contains(val.v, a.Expected), nil
	case OpNotContains:
		return !contains(val.v, a.Expected), nil
	case OpIsEmpty:
		return isEmpty(val.v), nil
	}
	return false, invalid(CodeUnknownOperator, "The operator %q is not known.", string(a.Op))
}

// expectedNumber parses the expected value of a comparison; for the
// response time it may have a unit ("1.5s").
func expectedNumber(a Assertion) (*big.Float, bool) {
	s := strings.TrimSpace(a.Expected)
	if a.Target == TargetTime {
		if n, ok := parseNumber(strings.TrimSpace(strings.TrimSuffix(s, "ms"))); ok && strings.HasSuffix(s, "ms") {
			return n, true
		}
		if d, err := time.ParseDuration(s); err == nil && strings.IndexFunc(s, isUnitLetter) >= 0 {
			return big.NewFloat(float64(d) / float64(time.Millisecond)), true
		}
	}
	return parseNumber(s)
}

func isUnitLetter(r rune) bool { return r >= 'a' && r <= 'z' || r == 'µ' }

// equals compares a value with the expected text. Strings compare
// exactly (a JSON-quoted expected string is unquoted); numbers
// numerically; booleans and null by their JSON literal; arrays and
// objects as JSON (member order ignored).
func equals(a Assertion, v any) bool {
	exp := a.Expected
	switch v := v.(type) {
	case string:
		if v == exp {
			return true
		}
		var s string
		return strings.HasPrefix(strings.TrimSpace(exp), `"`) && json.Unmarshal([]byte(exp), &s) == nil && s == v
	case json.Number:
		want, ok := expectedNumber(a)
		got, ok2 := numberOf(v)
		return ok && ok2 && got.Cmp(want) == 0
	case bool:
		return strings.TrimSpace(exp) == strconv.FormatBool(v)
	case nil:
		return strings.TrimSpace(exp) == "null"
	}
	want, err := jsonpath.Decode([]byte(exp))
	return err == nil && jsonEqual(v, want)
}

// contains: a substring of a string (or of a number's or boolean's
// text), an element of an array (compared as equals does), a member name
// of an object.
func contains(v any, exp string) bool {
	switch v := v.(type) {
	case []any:
		for _, e := range v {
			if equals(Assertion{Expected: exp}, e) {
				return true
			}
		}
		return false
	case *jsonpath.Object:
		_, ok := v.Get(exp)
		return ok
	}
	return strings.Contains(text(v), exp)
}

func isEmpty(v any) bool {
	switch v := v.(type) {
	case string:
		return v == ""
	case nil:
		return true
	case []any:
		return len(v) == 0
	case *jsonpath.Object:
		return v.Len() == 0
	}
	return false
}

func typeName(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	case []any:
		return "array"
	case *jsonpath.Object:
		return "object"
	}
	return "unknown"
}

// numberOf reads a number, or a string holding one (XML text, headers).
func numberOf(v any) (*big.Float, bool) {
	switch v := v.(type) {
	case json.Number:
		return parseNumber(string(v))
	case string:
		return parseNumber(strings.TrimSpace(v))
	}
	return nil, false
}

var numberRe = regexp.MustCompile(`^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d{1,6})?$`)

// parseNumber reads a decimal number exactly enough for comparisons:
// 256 bits of mantissa, so integers up to 2^256 and the decimals people
// type compare as written.
func parseNumber(s string) (*big.Float, bool) {
	if !numberRe.MatchString(s) {
		return nil, false
	}
	f, _, err := big.ParseFloat(s, 10, 256, big.ToNearestEven)
	if err != nil {
		return nil, false
	}
	return f, true
}

func jsonEqual(a, b any) bool {
	switch a := a.(type) {
	case json.Number:
		b, ok := b.(json.Number)
		if !ok {
			return false
		}
		x, ok1 := parseNumber(string(a))
		y, ok2 := parseNumber(string(b))
		return ok1 && ok2 && x.Cmp(y) == 0
	case []any:
		b, ok := b.([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !jsonEqual(a[i], b[i]) {
				return false
			}
		}
		return true
	case *jsonpath.Object:
		b, ok := b.(*jsonpath.Object)
		if !ok || a.Len() != b.Len() {
			return false
		}
		for i := range a.Len() {
			name, av := a.Member(i)
			bv, ok := b.Get(name)
			if !ok || !jsonEqual(av, bv) {
				return false
			}
		}
		return true
	case string, bool, nil:
		return a == b
	}
	return false
}

// ---- messages ----

// maxShown bounds a value in a message.
const maxShown = 200

// text is a value as plain text: a string itself, anything else as JSON.
func text(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case json.Number:
		return string(v)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// show is a value for a message: strings quoted, the rest as JSON, cut
// after maxShown characters.
func show(val value) string {
	var s string
	if str, ok := val.v.(string); ok {
		s = strconv.Quote(truncate(str))
	} else {
		s = truncate(text(val.v)) + val.unit
	}
	return s
}

func truncate(s string) string {
	if len(s) <= maxShown {
		return s
	}
	cut := maxShown
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

func label(a Assertion) string {
	base := targetLabel(a)
	switch a.Messages {
	case MessagesFirst:
		return "the first message's " + base
	case MessagesLast:
		return "the last message's " + base
	case MessagesNth:
		return "message " + strconv.Itoa(a.Index) + "'s " + base
	}
	return base
}

// targetLabel names the target: "status code", "JSONPath $.id".
func targetLabel(a Assertion) string {
	switch a.Target {
	case TargetStatus:
		return "status code"
	case TargetHeader:
		return "header " + strings.TrimSpace(a.Expr)
	case TargetCookie:
		return "cookie " + strings.TrimSpace(a.Expr)
	case TargetJSONPath:
		return "JSONPath " + a.Expr
	case TargetXPath:
		return "XPath " + a.Expr
	case TargetRegex:
		return "body match of /" + a.Expr + "/"
	case TargetBody:
		return "body"
	case TargetTime:
		return "response time"
	case TargetSize:
		return "body size"
	case TargetMessageCount:
		return "message count"
	}
	return string(a.Target)
}

// expectedShown is the expected value in a message, with the target's
// unit where it is a plain number.
func expectedShown(a Assertion) string {
	exp := truncate(a.Expected)
	switch a.Op {
	case OpMatches:
		return "/" + exp + "/"
	case OpIsType:
		return exp
	case OpMatchesSchema:
		return schemaName(a.Expected)
	}
	switch strings.TrimSpace(a.Expected) {
	case "true", "false", "null":
		return strings.TrimSpace(exp)
	}
	if _, ok := parseNumber(strings.TrimSpace(a.Expected)); ok {
		switch a.Target {
		case TargetTime:
			return strings.TrimSpace(exp) + " ms"
		case TargetSize:
			return strings.TrimSpace(exp) + " bytes"
		}
		return strings.TrimSpace(exp)
	}
	return strconv.Quote(exp)
}

// phrase is the operator and expected value after "to": "equal 200".
func phrase(a Assertion) string {
	e := expectedShown(a)
	switch a.Op {
	case OpEquals:
		return "equal " + e
	case OpNotEquals:
		return "not equal " + e
	case OpContains:
		return "contain " + e
	case OpNotContains:
		return "not contain " + e
	case OpExists:
		return "exist"
	case OpNotExists:
		return "not exist"
	case OpIsEmpty:
		return "be empty"
	case OpGreater:
		return "be greater than " + e
	case OpLess:
		return "be less than " + e
	case OpGreaterOrEqual:
		return "be at least " + e
	case OpLessOrEqual:
		return "be at most " + e
	case OpMatches:
		return "match " + e
	case OpIsType:
		return "be of type " + e
	case OpMatchesSchema:
		return "match " + e
	}
	return string(a.Op) + " " + e
}

// phrase3 is the operator in the third person: "equals 200".
func phrase3(a Assertion) string {
	e := expectedShown(a)
	switch a.Op {
	case OpEquals:
		return "equals " + e
	case OpNotEquals:
		return "does not equal " + e
	case OpContains:
		return "contains " + e
	case OpNotContains:
		return "does not contain " + e
	case OpExists:
		return "exists"
	case OpNotExists:
		return "does not exist"
	case OpIsEmpty:
		return "is empty"
	case OpGreater:
		return "is greater than " + e
	case OpLess:
		return "is less than " + e
	case OpGreaterOrEqual:
		return "is at least " + e
	case OpLessOrEqual:
		return "is at most " + e
	case OpMatches:
		return "matches " + e
	case OpIsType:
		return "is of type " + e
	case OpMatchesSchema:
		return "matches " + e
	}
	return string(a.Op) + " " + e
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
