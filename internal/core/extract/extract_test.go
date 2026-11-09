package extract

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/assert"
	"github.com/sorrelfox/postsonde/internal/core/runtimevars"
)

const loginBody = `{"token":"eyJhbGciOi.payload.sig","user":{"id":42,"admin":true,"email":null,"tags":["a","<b>"]},"items":[{"id":1},{"id":2}]}`

const xmlBody = `<login><token>xml-t0ken</token><user id="7"/><user id="8"/></login>`

func response(body string) assert.Response {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("X-Auth-Token", "hdr-t0ken")
	h.Add("X-Multi", "a")
	h.Add("X-Multi", "b")
	h.Add("Set-Cookie", "session=first; Path=/")
	h.Add("Set-Cookie", "csrf=c5rf; Path=/")
	h.Add("Set-Cookie", "session=s3ss; Path=/; HttpOnly")
	return assert.Response{Status: 201, Header: h, Body: []byte(body)}
}

func rule(source Source, expr string) Rule {
	return Rule{Source: source, Expr: expr, Variable: "v", Scope: ScopeRuntime}
}

// TestEvaluateSources: every source finds its value, as text, or says why
// not with its code and message.
func TestEvaluateSources(t *testing.T) {
	tests := []struct {
		name    string
		xml     bool
		body    string // overrides the body
		r       Rule
		outcome Outcome
		value   string
		code    string
		msg     string
	}{
		// status
		{name: "status", r: rule(SourceStatus, ""), outcome: Found, value: "201"},
		{name: "status ignores expr", r: rule(SourceStatus, "anything"), outcome: Found, value: "201"},

		// header
		{name: "header", r: rule(SourceHeader, "X-Auth-Token"), outcome: Found, value: "hdr-t0ken"},
		{name: "header any case, trimmed", r: rule(SourceHeader, " x-auth-token "), outcome: Found, value: "hdr-t0ken"},
		{name: "header joined", r: rule(SourceHeader, "X-Multi"), outcome: Found, value: "a, b"},
		{name: "header missing", r: rule(SourceHeader, "X-Nope"), outcome: Failed, code: CodeNotFound, msg: "Header X-Nope is not in the response."},
		{name: "header no name", r: rule(SourceHeader, ""), outcome: Invalid, code: assert.CodeMissingExpr, msg: "The header name is missing."},

		// cookie
		{name: "cookie last wins", r: rule(SourceCookie, "session"), outcome: Found, value: "s3ss"},
		{name: "cookie", r: rule(SourceCookie, "csrf"), outcome: Found, value: "c5rf"},
		{name: "cookie missing", r: rule(SourceCookie, "nope"), outcome: Failed, code: CodeNotFound, msg: "Cookie nope is not in the response."},
		{name: "cookie no name", r: rule(SourceCookie, " "), outcome: Invalid, code: assert.CodeMissingExpr, msg: "The cookie name is missing."},

		// jsonpath
		{name: "jsonpath string", r: rule(SourceJSONPath, "$.token"), outcome: Found, value: "eyJhbGciOi.payload.sig"},
		{name: "jsonpath number", r: rule(SourceJSONPath, "$.user.id"), outcome: Found, value: "42"},
		{name: "jsonpath boolean", r: rule(SourceJSONPath, "$.user.admin"), outcome: Found, value: "true"},
		{name: "jsonpath null", r: rule(SourceJSONPath, "$.user.email"), outcome: Found, value: "null"},
		{name: "jsonpath array, no HTML escaping", r: rule(SourceJSONPath, "$.user.tags"), outcome: Found, value: `["a","<b>"]`},
		{name: "jsonpath object", r: rule(SourceJSONPath, "$.items[0]"), outcome: Found, value: `{"id":1}`},
		{name: "jsonpath non-singular", r: rule(SourceJSONPath, "$.items[*].id"), outcome: Found, value: "[1,2]"},
		{name: "jsonpath missing", r: rule(SourceJSONPath, "$.nope"), outcome: Failed, code: CodeNotFound, msg: "JSONPath $.nope selects nothing in the body."},
		{name: "jsonpath body not JSON", body: "<html/>", r: rule(SourceJSONPath, "$.token"), outcome: Failed, code: CodeUnreadableBody,
			msg: "JSONPath $.token cannot be read: the body is not valid JSON."},
		{name: "jsonpath invalid", r: rule(SourceJSONPath, "$.["), outcome: Invalid, code: "jsonpath.invalid_query"},
		{name: "jsonpath missing expr", r: rule(SourceJSONPath, ""), outcome: Invalid, code: assert.CodeMissingExpr, msg: "The JSONPath is missing."},

		// xpath
		{name: "xpath text", xml: true, r: rule(SourceXPath, "/login/token"), outcome: Found, value: "xml-t0ken"},
		{name: "xpath attribute list", xml: true, r: rule(SourceXPath, "//user/@id"), outcome: Found, value: `["7","8"]`},
		{name: "xpath number", xml: true, r: rule(SourceXPath, "count(//user)"), outcome: Found, value: "2"},
		{name: "xpath missing", xml: true, r: rule(SourceXPath, "//nope"), outcome: Failed, code: CodeNotFound, msg: "XPath //nope selects nothing in the body."},
		{name: "xpath body not XML", r: rule(SourceXPath, "//token"), outcome: Failed, code: CodeUnreadableBody,
			msg: "XPath //token cannot be read: the body is not valid XML."},
		{name: "xpath invalid", xml: true, r: rule(SourceXPath, "//["), outcome: Invalid, code: "xpath.invalid_query"},

		// regex
		{name: "regex group", r: rule(SourceRegex, `"token":"([^"]+)"`), outcome: Found, value: "eyJhbGciOi.payload.sig"},
		{name: "regex whole match", r: rule(SourceRegex, `eyJ[a-zA-Z]+`), outcome: Found, value: "eyJhbGciOi"},
		{name: "regex no match", r: rule(SourceRegex, `nope=(\d+)`), outcome: Failed, code: CodeNotFound, msg: `The regular expression /nope=(\d+)/ does not match the body.`},
		{name: "regex invalid", r: rule(SourceRegex, `(`), outcome: Invalid, code: assert.CodeInvalidRegex},

		// the rule itself
		{name: "unknown source", r: rule("body", ""), outcome: Invalid, code: CodeUnknownSource, msg: `The source "body" is not known.`},
		{name: "unknown scope", r: Rule{Source: SourceStatus, Variable: "v", Scope: "global"}, outcome: Invalid, code: CodeUnknownScope,
			msg: `The scope "global" is not known: use runtime or run.`},
		{name: "no variable", r: Rule{Source: SourceStatus, Scope: ScopeRuntime}, outcome: Invalid, code: runtimevars.CodeInvalidName, msg: "The rule has no variable name."},
		{name: "variable with braces", r: Rule{Source: SourceStatus, Variable: "a}}b", Scope: ScopeRun}, outcome: Invalid, code: runtimevars.CodeInvalidName,
			msg: `The variable name "a}}b" cannot be used in a {{reference}}.`},
		{name: "variable with spaces", r: Rule{Source: SourceStatus, Variable: " v", Scope: ScopeRun}, outcome: Invalid, code: runtimevars.CodeInvalidName,
			msg: "The variable name cannot start or end with a space."},
		{name: "value too large", body: `{"big":"` + strings.Repeat("x", MaxValueBytes+1) + `"}`, r: rule(SourceJSONPath, "$.big"), outcome: Failed, code: CodeValueTooLarge,
			msg: "JSONPath $.big is 1048577 bytes, more than a variable can hold (1048576 bytes)."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := loginBody
			if tt.xml {
				body = xmlBody
			}
			if tt.body != "" {
				body = tt.body
			}
			got := Evaluate(t.Context(), response(body), []Rule{tt.r})
			if len(got) != 1 {
				t.Fatalf("got %d results", len(got))
			}
			r := got[0]
			if r.Outcome != tt.outcome || r.Value != tt.value || r.Code != tt.code {
				t.Errorf("got %s %q %s (%s), want %s %q %s", r.Outcome, r.Value, r.Code, r.Message, tt.outcome, tt.value, tt.code)
			}
			if tt.msg != "" && r.Message != tt.msg {
				t.Errorf("message = %q, want %q", r.Message, tt.msg)
			}
			if r.Outcome != Found && r.Message == "" {
				t.Error("no message")
			}
			if r.Rule != tt.r {
				t.Errorf("rule = %+v", r.Rule)
			}
		})
	}
}

// TestEvaluateOrder: results keep the rules' order, invalid rules among
// them, and one body is read by several rules.
func TestEvaluateOrder(t *testing.T) {
	rules := []Rule{
		rule(SourceJSONPath, "$.token"),
		rule("nope", ""),
		rule(SourceHeader, "X-Auth-Token"),
		rule(SourceJSONPath, "$.user.id"),
	}
	got := Evaluate(t.Context(), response(loginBody), rules)
	want := []Outcome{Found, Invalid, Found, Found}
	if len(got) != len(want) {
		t.Fatalf("got %d results", len(got))
	}
	for i := range want {
		if got[i].Outcome != want[i] || got[i].Rule != rules[i] {
			t.Errorf("result %d = %+v", i, got[i])
		}
	}
	if got[3].Value != "42" || got[3].Label != "JSONPath $.user.id" {
		t.Errorf("result 3 = %+v", got[3])
	}
	if len(Evaluate(t.Context(), response(""), nil)) != 0 {
		t.Error("no rules gave results")
	}
}

func TestScopeOf(t *testing.T) {
	if ScopeOf("") != ScopeRuntime || ScopeOf("run") != ScopeRun || ScopeOf("x") != "x" {
		t.Error("ScopeOf")
	}
}
