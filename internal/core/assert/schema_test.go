package assert

import (
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/jsonschema"
)

const userSchema = `{
	"type": "object",
	"required": ["id", "name", "email"],
	"properties": {
		"id": {"type": "integer"},
		"name": {"type": "string", "minLength": 1},
		"tags": {"type": "array", "items": {"type": "string"}}
	}
}`

// schemaCases cover matches_schema on every target, passing and failing.
var schemaCases = []struct {
	xml     bool
	a       Assertion
	outcome Outcome
	actual  string
	msg     string
	errors  []string // SchemaErrors as String()
}{
	{a: a(TargetBody, "", OpMatchesSchema, `{"type":"object","required":["id"]}`), outcome: Passed, msg: "Body matches the inline JSON schema."},
	{a: a(TargetBody, "", OpMatchesSchema, userSchema), outcome: Failed,
		msg:    `Expected body to match the inline JSON schema, but 2 errors: $ missing required property "email"; $.tags[2] must be string, not number.`,
		errors: []string{`$: missing required property "email"`, `$.tags[2]: must be string, not number`}},
	{a: a(TargetBody, "", OpMatchesSchema, `{"properties":{"id":{"type":"string"}}}`), outcome: Failed,
		msg:    `Expected body to match the inline JSON schema, but $.id must be string, not number.`,
		errors: []string{`$.id: must be string, not number`}},
	{a: a(TargetBody, "", OpMatchesSchema, `{"properties":{"a":{"type":"string"},"b":{"type":"string"},"c":{"type":"string"},"d":{"type":"string"}}}`), outcome: Passed, msg: "Body matches the inline JSON schema."},
	{xml: true, a: a(TargetBody, "", OpMatchesSchema, `true`), outcome: Failed, msg: "Expected body to match the inline JSON schema, but the body is not valid JSON."},
	{a: a(TargetJSONPath, "$.user", OpMatchesSchema, `{"properties":{"age":{"minimum":18}}}`), outcome: Passed, msg: "JSONPath $.user matches the inline JSON schema."},
	{a: a(TargetJSONPath, "$.user", OpMatchesSchema, `{"properties":{"role":{"enum":["user","guest"]}}}`), outcome: Failed,
		msg:    `Expected JSONPath $.user to match the inline JSON schema, but $.role must be one of "user", "guest".`,
		errors: []string{`$.role: must be one of "user", "guest"`}},
	{a: a(TargetJSONPath, "$.missing", OpMatchesSchema, `true`), outcome: Failed, msg: "Expected JSONPath $.missing to match the inline JSON schema, but it does not exist."},
	{a: a(TargetStatus, "", OpMatchesSchema, `{"type":"integer","minimum":200,"maximum":299}`), outcome: Passed, msg: "Status code matches the inline JSON schema."},
	{a: a(TargetStatus, "", OpMatchesSchema, `{"enum":[200,204]}`), outcome: Failed, msg: "Expected status code to match the inline JSON schema, but $ must be one of 200, 204.", errors: []string{"$: must be one of 200, 204"}},
	{a: a(TargetHeader, "Content-Type", OpMatchesSchema, `{"type":"string","pattern":"^application/json"}`), outcome: Passed, msg: "Header Content-Type matches the inline JSON schema."},
	{a: a(TargetHeader, "Content-Type", OpMatchesSchema, `{"const":"text/html"}`), outcome: Failed, msg: `Expected header Content-Type to match the inline JSON schema, but $ must equal "text/html".`, errors: []string{`$: must equal "text/html"`}},
	{a: a(TargetHeader, "X-Missing", OpMatchesSchema, `{}`), outcome: Failed, msg: "Expected header X-Missing to match the inline JSON schema, but it does not exist."},
	{a: a(TargetCookie, "sid", OpMatchesSchema, `{"minLength":4}`), outcome: Passed, msg: "Cookie sid matches the inline JSON schema."},
	{a: a(TargetCookie, "sid", OpMatchesSchema, `{"format":"uuid"}`), outcome: Failed, msg: `Expected cookie sid to match the inline JSON schema, but $ must be a valid uuid, not "last".`, errors: []string{`$: must be a valid uuid, not "last"`}},
	{xml: true, a: a(TargetXPath, "count(//item)", OpMatchesSchema, `{"type":"integer"}`), outcome: Passed, msg: "XPath count(//item) matches the inline JSON schema."},
	{xml: true, a: a(TargetXPath, "/order/@id", OpMatchesSchema, `{"type":"integer"}`), outcome: Failed, msg: "Expected XPath /order/@id to match the inline JSON schema, but $ must be integer, not string.", errors: []string{"$: must be integer, not string"}},
	{a: a(TargetRegex, `"name":"(\w+)`, OpMatchesSchema, `{"pattern":"^A"}`), outcome: Passed, msg: `Body match of /"name":"(\w+)/ matches the inline JSON schema.`},
	{a: a(TargetRegex, `"name":"(\w+)`, OpMatchesSchema, `{"maxLength":2}`), outcome: Failed, msg: `Expected body match of /"name":"(\w+)/ to match the inline JSON schema, but $ must have at most 2 characters, not 3.`, errors: []string{"$: must have at most 2 characters, not 3"}},
	{a: a(TargetTime, "", OpMatchesSchema, `{"exclusiveMaximum":500}`), outcome: Passed, msg: "Response time matches the inline JSON schema."},
	{a: a(TargetTime, "", OpMatchesSchema, `{"type":"integer"}`), outcome: Failed, msg: "Expected response time to match the inline JSON schema, but $ must be integer, not number.", errors: []string{"$: must be integer, not number"}},
	{xml: true, a: a(TargetSize, "", OpMatchesSchema, `{"multipleOf":3}`), outcome: Passed, msg: "Body size matches the inline JSON schema."},
	{xml: true, a: a(TargetSize, "", OpMatchesSchema, `{"maximum":10}`), outcome: Failed, msg: "Expected body size to match the inline JSON schema, but $ must be at most 10, not 81.", errors: []string{"$: must be at most 10, not 81"}},
	{a: a(TargetBody, "", OpMatchesSchema, "schemas/user.json"), outcome: Failed,
		msg:    `Expected body to match the JSON schema in schemas/user.json, but $.tags[2] must be string, not number.`,
		errors: []string{`$.tags[2]: must be string, not number`}},
	{a: a(TargetBody, "", OpMatchesSchema, "schemas/ok.json"), outcome: Passed, msg: "Body matches the JSON schema in schemas/ok.json."},
}

func testLoader(path string) ([]byte, error) {
	switch path {
	case "schemas/user.json":
		return []byte(strings.Replace(userSchema, `"email"`, `"id"`, 1)), nil
	case "schemas/ok.json":
		return []byte(`{"type":"object"}`), nil
	case "schemas/bad.json":
		return []byte(`{"type":"thing"}`), nil
	}
	return nil, errs.New(errs.NotFound, "The schema file was not found: "+path+".").WithCode("test.schema_missing")
}

func TestMatchesSchema(t *testing.T) {
	t.Parallel()
	for _, tc := range schemaCases {
		t.Run(string(tc.a.Target)+" "+tc.a.Expr+" "+tc.a.Expected, func(t *testing.T) {
			t.Parallel()
			body := jsonBody
			if tc.xml {
				body = xmlBody
			}
			got := EvaluateWith(t.Context(), testResponse(body), []Assertion{tc.a}, Options{LoadSchema: testLoader})[0]
			if got.Outcome != tc.outcome {
				t.Fatalf("outcome %s, want %s: %+v", got.Outcome, tc.outcome, got)
			}
			if tc.msg != "" && got.Message != tc.msg {
				t.Errorf("message\n got %q\nwant %q", got.Message, tc.msg)
			}
			if tc.errors != nil {
				var errs []string
				for _, e := range got.SchemaErrors {
					errs = append(errs, e.String())
				}
				if strings.Join(errs, "\n") != strings.Join(tc.errors, "\n") {
					t.Errorf("schema errors\n got %q\nwant %q", errs, tc.errors)
				}
			}
			if got.Outcome == Passed && len(got.SchemaErrors) != 0 {
				t.Errorf("passed with schema errors %v", got.SchemaErrors)
			}
		})
	}
}

func TestMatchesSchemaCoversEveryTarget(t *testing.T) {
	t.Parallel()
	seen := map[TargetKind]map[Outcome]bool{}
	for _, tc := range schemaCases {
		if seen[tc.a.Target] == nil {
			seen[tc.a.Target] = map[Outcome]bool{}
		}
		seen[tc.a.Target][tc.outcome] = true
	}
	for _, tg := range []TargetKind{TargetStatus, TargetHeader, TargetCookie, TargetJSONPath, TargetXPath, TargetRegex, TargetBody, TargetTime, TargetSize} {
		if !seen[tg][Passed] || !seen[tg][Failed] {
			t.Errorf("%s: outcomes %v, want passed and failed", tg, seen[tg])
		}
	}
}

func TestMatchesSchemaMessageLimits(t *testing.T) {
	t.Parallel()
	schema := `{"properties":{"a":{"type":"string"},"b":{"type":"string"},"c":{"type":"string"},"d":{"type":"string"}}}`
	body := `{"a":1,"b":2,"c":3,"d":4}`
	r := EvaluateOne(t.Context(), Response{Status: 200, Body: []byte(body)}, a(TargetBody, "", OpMatchesSchema, schema))
	want := "Expected body to match the inline JSON schema, but 4 errors: $.a must be string, not number; $.b must be string, not number; $.c must be string, not number; …."
	if r.Message != want || len(r.SchemaErrors) != 4 {
		t.Errorf("got %q with %d errors", r.Message, len(r.SchemaErrors))
	}

	var items []string
	for range 150 {
		items = append(items, "1")
	}
	r = EvaluateOne(t.Context(), Response{Status: 200, Body: []byte("[" + strings.Join(items, ",") + "]")}, a(TargetBody, "", OpMatchesSchema, `{"items":{"type":"string"}}`))
	if len(r.SchemaErrors) != jsonschema.DefaultLimits.MaxErrors || !strings.Contains(r.Message, "but more than 100 errors: ") {
		t.Errorf("got %q with %d errors", r.Message, len(r.SchemaErrors))
	}
}

func TestMatchesSchemaInvalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		expected string
		loader   bool
		code     string
		msg      string
	}{
		{"", true, CodeMissingSchema, "The JSON schema is missing: enter it, or the path of a schema file."},
		{"  ", true, CodeMissingSchema, ""},
		{`{"type":`, true, jsonschema.CodeInvalidSchema, ""},
		{`{"type":"thing"}`, true, jsonschema.CodeInvalidSchema, `The JSON schema is not valid at #/type: "thing" is not a JSON Schema type.`},
		{`{"$ref":"other.json"}`, true, jsonschema.CodeUnresolvedRef, ""},
		{`{"pattern":"(?=x)"}`, true, jsonschema.CodeInvalidPattern, ""},
		{"schemas/user.json", false, CodeSchemaFileUnavailable, `The schema file "schemas/user.json" cannot be read here.`},
		{"schemas/missing.json", true, "test.schema_missing", "The schema file was not found: schemas/missing.json."},
		{"schemas/bad.json", true, jsonschema.CodeInvalidSchema, ""},
	}
	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			t.Parallel()
			var opts Options
			if tt.loader {
				opts.LoadSchema = testLoader
			}
			r := EvaluateWith(t.Context(), testResponse(jsonBody), []Assertion{a(TargetBody, "", OpMatchesSchema, tt.expected)}, opts)[0]
			if r.Outcome != Invalid || r.Code != tt.code || r.Message == "" {
				t.Errorf("got %+v, want invalid %s", r, tt.code)
			}
			if tt.msg != "" && r.Message != tt.msg {
				t.Errorf("message = %q, want %q", r.Message, tt.msg)
			}
		})
	}
}

func TestMatchesSchemaTooComplex(t *testing.T) {
	t.Parallel()
	r := EvaluateOne(t.Context(), Response{Status: 200, Body: []byte(`1`)}, a(TargetBody, "", OpMatchesSchema, `{"$ref":"#"}`))
	if r.Outcome != Invalid || r.Code != jsonschema.CodeTooDeep {
		t.Errorf("got %+v", r)
	}
}

// A schema is read and compiled once per evaluation, however many
// assertions use it.
func TestMatchesSchemaLoadsOnce(t *testing.T) {
	t.Parallel()
	loads := 0
	opts := Options{LoadSchema: func(path string) ([]byte, error) {
		loads++
		if path != "s.json" {
			return nil, errors.New("unexpected")
		}
		return []byte(`{"type":"object"}`), nil
	}}
	list := []Assertion{
		a(TargetBody, "", OpMatchesSchema, "s.json"),
		a(TargetJSONPath, "$.user", OpMatchesSchema, " s.json "),
		a(TargetJSONPath, "$.id", OpMatchesSchema, "s.json"),
	}
	res := EvaluateWith(t.Context(), testResponse(jsonBody), list, opts)
	if loads != 1 {
		t.Errorf("loaded %d times", loads)
	}
	if res[0].Outcome != Passed || res[1].Outcome != Passed || res[2].Outcome != Failed {
		t.Errorf("results %+v", res)
	}
}

func TestIsInlineSchema(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]bool{
		`{"type":"object"}`: true, "  {\n}": true, "true": true, " false ": true,
		"schema.json": false, "/abs/schema.json": false, "": false, "null": false,
	} {
		if got := IsInlineSchema(in); got != want {
			t.Errorf("IsInlineSchema(%q) = %v", in, got)
		}
	}
}
