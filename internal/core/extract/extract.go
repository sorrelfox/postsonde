// Package extract reads the values that extraction rules take from a
// response (TEST-05): a rule names a source (the status code, a
// header, a cookie, a JSONPath or XPath of the body, a regular expression
// on the body), its expression, and the variable the value goes to, in the
// runtime scope (the project's runtime variables, ENV-15) or the run's own
// variables (a folder run only). It reads the values with
// internal/core/assert, the same way assertions read their targets, and
// says what it found; the caller sets the variables. Standard library and
// this module only. See docs/extraction.md.
//
//	results := extract.Evaluate(ctx, assert.Response{Status: 200, Header: h, Body: body}, []extract.Rule{
//		{Source: extract.SourceJSONPath, Expr: "$.token", Variable: "token", Secret: true},
//	})
//	// results[0].Outcome == extract.Found, results[0].Value == "eyJ…"
package extract

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/assert"
	"github.com/sorrelfox/postsonde/internal/core/runtimevars"
)

// Source is where a rule takes its value from.
type Source string

// Sources.
const (
	// SourceStatus: the status code ("200"); no expression.
	SourceStatus Source = "status"
	// SourceHeader: the header named by the expression (any case), its
	// values joined with ", ".
	SourceHeader Source = "header"
	// SourceCookie: the value of the cookie named by the expression in
	// the response's Set-Cookie headers; the last one wins.
	SourceCookie Source = "cookie"
	// SourceJSONPath: an RFC 9535 JSONPath of the body. A singular query
	// gives its value; any other the array of the values it selects.
	SourceJSONPath Source = "jsonpath"
	// SourceXPath: an XPath 1.0 expression of the body.
	SourceXPath Source = "xpath"
	// SourceRegex: an RE2 regular expression on the body: its first
	// capture group, or the whole match without one.
	SourceRegex Source = "regex"
)

// Sources lists the sources in the order the editor offers them.
var Sources = []Source{SourceJSONPath, SourceHeader, SourceCookie, SourceRegex, SourceXPath, SourceStatus}

// Scope is where a rule's variable is set.
type Scope string

// Scopes.
const (
	// ScopeRuntime: the project's runtime variables (ENV-15), which every
	// later send of the project renders with; inside a folder run the
	// run's own variable of the name too, so the run sees the value
	// whatever its variables hold. The default: a stored rule without a
	// scope has this one.
	ScopeRuntime Scope = "runtime"
	// ScopeRun: the variables of the folder run only; outside a run the
	// rule is skipped.
	ScopeRun Scope = "run"
)

// ScopeOf is the scope a stored scope names: "" is ScopeRuntime.
func ScopeOf(stored string) Scope {
	if stored == "" {
		return ScopeRuntime
	}
	return Scope(stored)
}

// Rule is one extraction rule, with {{variables}} in Expr already
// rendered.
type Rule struct {
	Source Source
	// Expr is the header or cookie name, the JSONPath, the XPath or the
	// regular expression; unused by SourceStatus.
	Expr string
	// Variable is the name of the variable set: a name a {{reference}} can
	// spell (runtimevars.CheckName).
	Variable string
	Scope    Scope
	// Secret masks the value wherever it is shown or recorded.
	Secret bool
	// Persist keeps a runtime variable after the project is closed; a run
	// variable is never kept.
	Persist bool
}

// Outcome of a rule.
type Outcome string

const (
	// Found: the value is in Result.Value.
	Found Outcome = "found"
	// Failed: the response has no such value (a header that is not there,
	// a JSONPath that selects nothing, a body that is not JSON), or it is
	// too large for a variable. The variable is left as it was.
	Failed Outcome = "failed"
	// Invalid: the rule itself is wrong (an unknown source or scope, no
	// expression, an expression that does not parse, a variable name that
	// cannot be used). The variable is left as it was.
	Invalid Outcome = "invalid"
)

// Error codes of Failed and Invalid results, besides the codes of an
// invalid assertion's target (assert.missing_expr, assert.invalid_regex,
// jsonpath.invalid_query, xpath.invalid_query, *.too_complex) and
// runtimevars.invalid_name.
const (
	CodeUnknownSource  = "extract.unknown_source"
	CodeUnknownScope   = "extract.unknown_scope"
	CodeNotFound       = "extract.not_found"
	CodeUnreadableBody = "extract.unreadable_body"
	CodeValueTooLarge  = "extract.value_too_large"
)

// MaxValueBytes bounds an extracted value: a variable is sent in requests
// and a persisted one written to a file, so a whole large body is refused.
const MaxValueBytes = 1 << 20

// Result is the outcome of one rule.
type Result struct {
	Rule    Rule
	Outcome Outcome
	// Value is the value found, as text: a string as it is, a number as
	// written, true, false and null as words, an array or object as
	// compact JSON. "" unless Found.
	Value string
	// Label names the source in messages: "JSONPath $.token", "header
	// X-Token", "status code".
	Label string
	// Message says why a rule failed or is invalid; "" when Found.
	Message string
	// Code is the error code of a Failed or Invalid result.
	Code string
}

// Check returns why rule r cannot be evaluated (an Invalid result), or
// nil. Evaluate checks every rule with it first.
func Check(r Rule) *Result {
	res := &Result{Rule: r, Outcome: Invalid, Label: labelOf(r)}
	if !knownSource(r.Source) {
		res.Code, res.Message = CodeUnknownSource, fmt.Sprintf("The source %q is not known.", string(r.Source))
		return res
	}
	if r.Scope != ScopeRuntime && r.Scope != ScopeRun {
		res.Code, res.Message = CodeUnknownScope, fmt.Sprintf("The scope %q is not known: use runtime or run.", string(r.Scope))
		return res
	}
	if runtimevars.CheckName(r.Variable) != nil {
		res.Code, res.Message = runtimevars.CodeInvalidName, variableProblem(r.Variable)
		return res
	}
	return nil
}

// variableProblem says what is wrong with a variable name.
func variableProblem(name string) string {
	switch {
	case name == "":
		return "The rule has no variable name."
	case len(name) > runtimevars.MaxNameLength:
		return "The variable name is too long."
	case strings.TrimSpace(name) != name:
		return "The variable name cannot start or end with a space."
	}
	return "The variable name " + strconv.Quote(name) + " cannot be used in a {{reference}}."
}

func knownSource(s Source) bool {
	for _, k := range Sources {
		if s == k {
			return true
		}
	}
	return false
}

// Evaluate reads every rule's value from resp, in order. The body is
// parsed at most once per format; ctx bounds JSONPath and XPath
// evaluation. Nothing is set: the caller sets the Found values.
func Evaluate(ctx context.Context, resp assert.Response, rules []Rule) []Result {
	out := make([]Result, len(rules))
	var queries []assert.Query
	var at []int
	for i, r := range rules {
		if bad := Check(r); bad != nil {
			out[i] = *bad
			continue
		}
		queries = append(queries, assert.Query{Target: assert.TargetKind(r.Source), Expr: r.Expr})
		at = append(at, i)
	}
	readings := assert.Read(ctx, resp, queries)
	for k, rd := range readings {
		i := at[k]
		out[i] = result(rules[i], rd)
	}
	return out
}

func result(r Rule, rd assert.Reading) Result {
	res := Result{Rule: r, Label: labelOf(r)}
	switch {
	case rd.Invalid():
		res.Outcome, res.Code, res.Message = Invalid, rd.Code, rd.Message
	case rd.Problem != "":
		res.Outcome, res.Code = Failed, CodeUnreadableBody
		res.Message = fmt.Sprintf("%s cannot be read: %s.", upperFirst(res.Label), rd.Problem)
	case !rd.Exists:
		res.Outcome, res.Code = Failed, CodeNotFound
		res.Message = notFound(r, res.Label)
	case len(rd.Text) > MaxValueBytes:
		res.Outcome, res.Code = Failed, CodeValueTooLarge
		res.Message = fmt.Sprintf("%s is %d bytes, more than a variable can hold (%d bytes).", upperFirst(res.Label), len(rd.Text), MaxValueBytes)
	default:
		res.Outcome, res.Value = Found, rd.Text
	}
	return res
}

func notFound(r Rule, label string) string {
	switch r.Source {
	case SourceRegex:
		return "The regular expression /" + r.Expr + "/ does not match the body."
	case SourceJSONPath, SourceXPath:
		return upperFirst(label) + " selects nothing in the body."
	}
	return upperFirst(label) + " is not in the response."
}

// labelOf names a rule's source: "JSONPath $.token", "header X-Token",
// "cookie session", "body match of /id=(\d+)/", "status code".
func labelOf(r Rule) string {
	switch r.Source {
	case SourceStatus:
		return "status code"
	case SourceHeader:
		return "header " + strings.TrimSpace(r.Expr)
	case SourceCookie:
		return "cookie " + strings.TrimSpace(r.Expr)
	case SourceJSONPath:
		return "JSONPath " + r.Expr
	case SourceXPath:
		return "XPath " + r.Expr
	case SourceRegex:
		return "body match of /" + r.Expr + "/"
	}
	return string(r.Source)
}

func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
