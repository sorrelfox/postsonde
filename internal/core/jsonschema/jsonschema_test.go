package jsonschema

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/jsonpath"
)

func mustCompile(t *testing.T, schema string, opts Options) *Schema {
	t.Helper()
	s, err := CompileJSON([]byte(schema), opts)
	if err != nil {
		t.Fatalf("compile %s: %v", schema, err)
	}
	return s
}

func validate(t *testing.T, s *Schema, data string, lim Limits) (Result, error) {
	t.Helper()
	doc, err := jsonpath.Decode([]byte(data))
	if err != nil {
		t.Fatalf("data %s: %v", data, err)
	}
	return s.Validate(context.Background(), doc, lim)
}

// TestErrors checks every keyword's error: its locations and message.
func TestErrors(t *testing.T) {
	tests := []struct {
		name, schema, data string
		format             bool
		want               []Error // Keyword is checked through KeywordPath
	}{
		{"type", `{"type":"integer"}`, `"x"`, false, []Error{{"", "$", "/type", "#/type", "type", "must be integer, not string"}}},
		{"type list", `{"type":["string","null","array"]}`, `1.5`, false, []Error{{"", "$", "/type", "#/type", "type", "must be string, null or array, not number"}}},
		{"enum", `{"enum":["a","b"]}`, `"c"`, false, []Error{{"", "$", "/enum", "#/enum", "enum", `must be one of "a", "b"`}}},
		{"enum one", `{"enum":[1]}`, `2`, false, []Error{{"", "$", "/enum", "#/enum", "enum", "must equal 1"}}},
		{"enum long", `{"enum":[1,2,3,4,5,6,7]}`, `8`, false, []Error{{"", "$", "/enum", "#/enum", "enum", "must be one of the 7 values in enum"}}},
		{"const", `{"const":{"a":[1]}}`, `{}`, false, []Error{{"", "$", "/const", "#/const", "const", `must equal {"a":[1]}`}}},
		{"minimum", `{"minimum":5}`, `3`, false, []Error{{"", "$", "/minimum", "#/minimum", "minimum", "must be at least 5, not 3"}}},
		{"maximum", `{"maximum":5}`, `5.5`, false, []Error{{"", "$", "/maximum", "#/maximum", "maximum", "must be at most 5, not 5.5"}}},
		{"exclusiveMinimum", `{"exclusiveMinimum":5}`, `5`, false, []Error{{"", "$", "/exclusiveMinimum", "#/exclusiveMinimum", "exclusiveMinimum", "must be greater than 5, not 5"}}},
		{"exclusiveMaximum", `{"exclusiveMaximum":5}`, `6`, false, []Error{{"", "$", "/exclusiveMaximum", "#/exclusiveMaximum", "exclusiveMaximum", "must be less than 5, not 6"}}},
		{"draft-04 exclusiveMaximum", `{"$schema":"http://json-schema.org/draft-04/schema#","maximum":5,"exclusiveMaximum":true}`, `5`, false, []Error{{"", "$", "/exclusiveMaximum", "#/exclusiveMaximum", "exclusiveMaximum", "must be less than 5, not 5"}}},
		{"multipleOf", `{"multipleOf":0.5}`, `1.2`, false, []Error{{"", "$", "/multipleOf", "#/multipleOf", "multipleOf", "must be a multiple of 0.5, not 1.2"}}},
		{"minLength", `{"minLength":3}`, `"ab"`, false, []Error{{"", "$", "/minLength", "#/minLength", "minLength", "must have at least 3 characters, not 2"}}},
		{"maxLength", `{"maxLength":1}`, `"ab"`, false, []Error{{"", "$", "/maxLength", "#/maxLength", "maxLength", "must have at most 1 character, not 2"}}},
		{"pattern", `{"pattern":"^a"}`, `"b"`, false, []Error{{"", "$", "/pattern", "#/pattern", "pattern", `must match the pattern "^a"`}}},
		{"format", `{"format":"uuid"}`, `"x"`, true, []Error{{"", "$", "/format", "#/format", "format", `must be a valid uuid, not "x"`}}},
		{"minItems", `{"minItems":2}`, `[1]`, false, []Error{{"", "$", "/minItems", "#/minItems", "minItems", "must have at least 2 items, not 1"}}},
		{"maxItems", `{"maxItems":1}`, `[1,2]`, false, []Error{{"", "$", "/maxItems", "#/maxItems", "maxItems", "must have at most 1 item, not 2"}}},
		{"uniqueItems", `{"uniqueItems":true}`, `[1,2,1.0]`, false, []Error{{"", "$", "/uniqueItems", "#/uniqueItems", "uniqueItems", "must have unique items, but items 0 and 2 are equal"}}},
		{"items", `{"items":{"type":"string"}}`, `["a",1]`, false, []Error{{"/1", "$[1]", "/items/type", "#/items/type", "type", "must be string, not number"}}},
		{"items false", `{"prefixItems":[{}],"items":false}`, `[1,2]`, false, []Error{{"/1", "$[1]", "/items", "#/items", "false", "is not allowed (items is false)"}}},
		{"prefixItems", `{"prefixItems":[{"type":"string"}]}`, `[1]`, false, []Error{{"/0", "$[0]", "/prefixItems/0/type", "#/prefixItems/0/type", "type", "must be string, not number"}}},
		{"contains", `{"contains":{"const":1}}`, `[2]`, false, []Error{{"", "$", "/contains", "#/contains", "contains", "must contain at least 1 item matching the contains schema, not 0"}}},
		{"minContains", `{"contains":{"const":1},"minContains":2}`, `[1]`, false, []Error{{"", "$", "/minContains", "#/minContains", "minContains", "must contain at least 2 items matching the contains schema, not 1"}}},
		{"maxContains", `{"contains":{"const":1},"maxContains":1}`, `[1,1]`, false, []Error{{"", "$", "/maxContains", "#/maxContains", "maxContains", "must contain at most 1 item matching the contains schema, not 2"}}},
		{"required", `{"required":["id","name"]}`, `{"id":1}`, false, []Error{{"", "$", "/required", "#/required", "required", `missing required property "name"`}}},
		{"properties", `{"properties":{"a b":{"properties":{"c":{"type":"null"}}}}}`, `{"a b":{"c":1}}`, false, []Error{{"/a b/c", "$['a b'].c", "/properties/a b/properties/c/type", "#/properties/a b/properties/c/type", "type", "must be null, not number"}}},
		{"additionalProperties", `{"properties":{"a":{}},"additionalProperties":false}`, `{"a":1,"b~/":2}`, false, []Error{{"/b~0~1", "$['b~/']", "/additionalProperties", "#/additionalProperties", "false", "is not allowed (additionalProperties is false)"}}},
		{"patternProperties", `{"patternProperties":{"^x-":{"type":"string"}}}`, `{"x-a":1}`, false, []Error{{"/x-a", "$['x-a']", "/patternProperties/^x-/type", "#/patternProperties/^x-/type", "type", "must be string, not number"}}},
		{"propertyNames", `{"propertyNames":{"pattern":"^[a-z]+$"}}`, `{"A":1}`, false, []Error{{"", "$", "/propertyNames", "#/propertyNames", "propertyNames", `property name "A" does not match the propertyNames schema`}}},
		{"minProperties", `{"minProperties":1}`, `{}`, false, []Error{{"", "$", "/minProperties", "#/minProperties", "minProperties", "must have at least 1 property, not 0"}}},
		{"maxProperties", `{"maxProperties":1}`, `{"a":1,"b":2}`, false, []Error{{"", "$", "/maxProperties", "#/maxProperties", "maxProperties", "must have at most 1 property, not 2"}}},
		{"dependentRequired", `{"dependentRequired":{"a":["b"]}}`, `{"a":1}`, false, []Error{{"", "$", "/dependentRequired", "#/dependentRequired", "dependentRequired", `property "a" requires property "b"`}}},
		{"dependentSchemas", `{"dependentSchemas":{"a":{"required":["b"]}}}`, `{"a":1}`, false, []Error{{"", "$", "/dependentSchemas/a/required", "#/dependentSchemas/a/required", "required", `missing required property "b"`}}},
		{"dependencies in 2020-12", `{"dependencies":{"a":["b"],"c":{"required":["d"]}}}`, `{"a":1,"c":2}`, false, []Error{
			{"", "$", "/dependencies", "#/dependencies", "dependencies", `property "a" requires property "b"`},
			{"", "$", "/dependencies/c/required", "#/dependencies/c/required", "required", `missing required property "d"`},
		}},
		{"allOf", `{"allOf":[{"type":"string"},{"minLength":2}]}`, `"a"`, false, []Error{{"", "$", "/allOf/1/minLength", "#/allOf/1/minLength", "minLength", "must have at least 2 characters, not 1"}}},
		{"anyOf", `{"anyOf":[{"type":"string"},{"type":"null"}]}`, `1`, false, []Error{{"", "$", "/anyOf", "#/anyOf", "anyOf", "must match at least one of the 2 schemas in anyOf"}}},
		{"oneOf none", `{"oneOf":[{"type":"string"},{"type":"null"}]}`, `1`, false, []Error{{"", "$", "/oneOf", "#/oneOf", "oneOf", "must match exactly one of the 2 schemas in oneOf, but matches none"}}},
		{"oneOf several", `{"oneOf":[{"type":"number"},{"minimum":0},{"type":"string"}]}`, `1`, false, []Error{{"", "$", "/oneOf", "#/oneOf", "oneOf", "must match exactly one of the schemas in oneOf, but matches 0 and 1"}}},
		{"not", `{"not":{"type":"string"}}`, `"a"`, false, []Error{{"", "$", "/not", "#/not", "not", "must not match the schema in not"}}},
		{"then", `{"if":{"type":"string"},"then":{"minLength":2}}`, `"a"`, false, []Error{{"", "$", "/then/minLength", "#/then/minLength", "minLength", "must have at least 2 characters, not 1"}}},
		{"else", `{"if":{"type":"string"},"else":{"type":"null"}}`, `1`, false, []Error{{"", "$", "/else/type", "#/else/type", "type", "must be null, not number"}}},
		{"false schema", `false`, `1`, false, []Error{{"", "$", "", "#", "false", "is not allowed here (the schema is false)"}}},
		{"property false", `{"properties":{"a":false}}`, `{"a":1}`, false, []Error{{"/a", "$.a", "/properties/a", "#/properties/a", "false", "is not allowed (properties)"}}},
		{"ref", `{"$defs":{"id":{"type":"integer"}},"properties":{"id":{"$ref":"#/$defs/id"}}}`, `{"id":"x"}`, false, []Error{{"/id", "$.id", "/properties/id/$ref/type", "#/$defs/id/type", "type", "must be integer, not string"}}},
		{"unevaluatedProperties", `{"properties":{"a":{}},"unevaluatedProperties":false}`, `{"a":1,"b":2}`, false, []Error{{"/b", "$.b", "/unevaluatedProperties", "#/unevaluatedProperties", "false", "is not allowed (unevaluatedProperties is false)"}}},
		{"unevaluatedItems", `{"prefixItems":[{}],"unevaluatedItems":false}`, `[1,2]`, false, []Error{{"/1", "$[1]", "/unevaluatedItems", "#/unevaluatedItems", "false", "is not allowed (unevaluatedItems is false)"}}},
		{"several errors in keyword order", `{"type":"object","required":["a"],"properties":{"b":{"type":"string"},"c":{"minimum":1}}}`, `{"b":1,"c":0}`, false, []Error{
			{"", "$", "/required", "#/required", "required", `missing required property "a"`},
			{"/b", "$.b", "/properties/b/type", "#/properties/b/type", "type", "must be string, not number"},
			{"/c", "$.c", "/properties/c/minimum", "#/properties/c/minimum", "minimum", "must be at least 1, not 0"},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := mustCompile(t, tc.schema, Options{FormatAssertion: tc.format})
			res, err := validate(t, s, tc.data, DefaultLimits)
			if err != nil {
				t.Fatal(err)
			}
			if res.Valid {
				t.Fatalf("valid, want errors %v", tc.want)
			}
			if len(res.Errors) != len(tc.want) {
				t.Fatalf("errors = %+v\nwant %+v", res.Errors, tc.want)
			}
			for i, got := range res.Errors {
				if got != tc.want[i] {
					t.Errorf("error %d:\n got %+v\nwant %+v", i, got, tc.want[i])
				}
			}
		})
	}
}

func TestErrorString(t *testing.T) {
	e := Error{Path: "$.items[0].id", Message: "must be integer, not string"}
	if got := e.String(); got != "$.items[0].id: must be integer, not string" {
		t.Errorf("String() = %q", got)
	}
}

func TestCompileErrors(t *testing.T) {
	tests := []struct {
		name, schema, code, msg string
	}{
		{"not JSON", `{"type":`, CodeInvalidSchema, "The JSON schema is not valid JSON"},
		{"not a schema", `[1]`, CodeInvalidSchema, "The JSON schema is not valid at #: a schema must be an object or a boolean."},
		{"bad type", `{"type":"int"}`, CodeInvalidSchema, `The JSON schema is not valid at #/type: "int" is not a JSON Schema type.`},
		{"bad minLength", `{"properties":{"a":{"minLength":-1}}}`, CodeInvalidSchema, "The JSON schema is not valid at #/properties/a/minLength: minLength must be a non-negative integer."},
		{"fractional maxItems", `{"maxItems":1.5}`, CodeInvalidSchema, "maxItems must be a non-negative integer"},
		{"zero multipleOf", `{"multipleOf":0}`, CodeInvalidSchema, "multipleOf must be greater than 0"},
		{"required of numbers", `{"required":[1]}`, CodeInvalidSchema, "required must be an array of strings"},
		{"empty anyOf", `{"anyOf":[]}`, CodeInvalidSchema, "anyOf must be a non-empty array of schemas"},
		{"subschema not a schema", `{"items":1}`, CodeInvalidSchema, "#/items: a schema must be an object or a boolean"},
		{"enum not an array", `{"enum":1}`, CodeInvalidSchema, "enum must be an array"},
		{"remote ref", `{"$ref":"https://example.com/other.json"}`, CodeUnresolvedRef, `The reference "https://example.com/other.json" at #/$ref cannot be resolved: it points outside the schema`},
		{"relative remote ref", `{"$ref":"other.json#/a"}`, CodeUnresolvedRef, "it points outside the schema"},
		{"missing pointer", `{"$ref":"#/$defs/nope"}`, CodeUnresolvedRef, "there is nothing at #/$defs/nope"},
		{"missing anchor", `{"$ref":"#nope"}`, CodeUnresolvedRef, `no $anchor is named "nope"`},
		{"ref to a non-schema", `{"$defs":{"a":1},"$ref":"#/$defs/a"}`, CodeInvalidSchema, "#/$defs/a: a schema must be an object or a boolean"},
		{"bad pattern", `{"pattern":"(a"}`, CodeInvalidPattern, `The pattern "(a" at #/pattern is not a valid regular expression: missing closing ): ` + "`(a`."},
		{"lookahead", `{"pattern":"^(?=a)a"}`, CodeInvalidPattern, "uses lookahead, which Go regular expressions (RE2) do not support"},
		{"lookbehind", `{"patternProperties":{"(?<!x)y":{}}}`, CodeInvalidPattern, "uses lookbehind"},
		{"backreference", `{"pattern":"(a)\\1"}`, CodeInvalidPattern, "uses backreferences"},
		{"duplicate id", `{"$defs":{"a":{"$id":"x.json"},"b":{"$id":"x.json"}}}`, CodeInvalidSchema, "is declared twice"},
		{"id in an unknown keyword", `{"$defs":{"a":{"x-group":{"b":{"$id":"b.json"}}}},"$ref":"b.json"}`, CodeUnresolvedRef, "it points outside the schema"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CompileJSON([]byte(tc.schema), Options{})
			var e *errs.Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v, want *errs.Error", err)
			}
			if e.Code != tc.code || !strings.Contains(e.Message, tc.msg) {
				t.Errorf("err = %s %q, want %s containing %q", e.Code, e.Message, tc.code, tc.msg)
			}
		})
	}
}

func TestLimits(t *testing.T) {
	code := func(err error) string {
		var e *errs.Error
		if errors.As(err, &e) {
			return e.Code
		}
		return ""
	}
	t.Run("self reference on the same value", func(t *testing.T) {
		s := mustCompile(t, `{"$ref":"#"}`, Options{})
		_, err := validate(t, s, `1`, DefaultLimits)
		if code(err) != CodeTooDeep {
			t.Errorf("err = %v, want %s", err, CodeTooDeep)
		}
	})
	t.Run("reference cycle through anyOf", func(t *testing.T) {
		s := mustCompile(t, `{"$defs":{"a":{"anyOf":[{"$ref":"#/$defs/b"}]},"b":{"allOf":[{"$ref":"#/$defs/a"}]}},"$ref":"#/$defs/a"}`, Options{})
		_, err := validate(t, s, `{}`, DefaultLimits)
		if code(err) != CodeTooDeep {
			t.Errorf("err = %v, want %s", err, CodeTooDeep)
		}
	})
	t.Run("recursion that goes into the value is fine", func(t *testing.T) {
		s := mustCompile(t, `{"items":{"$ref":"#"}}`, Options{})
		res, err := validate(t, s, `[[[[]]]]`, DefaultLimits)
		if err != nil || !res.Valid {
			t.Errorf("res = %+v, err = %v", res, err)
		}
	})
	t.Run("max depth", func(t *testing.T) {
		s := mustCompile(t, `{"items":{"$ref":"#"}}`, Options{})
		_, err := validate(t, s, strings.Repeat("[", 20)+strings.Repeat("]", 20), Limits{MaxDepth: 10})
		if code(err) != CodeTooDeep {
			t.Errorf("err = %v, want %s", err, CodeTooDeep)
		}
	})
	t.Run("max steps", func(t *testing.T) {
		s := mustCompile(t, `{"items":{"anyOf":[{"type":"string"},{"type":"number"}]}}`, Options{})
		data := "[" + strings.TrimSuffix(strings.Repeat("1,", 1000), ",") + "]"
		_, err := validate(t, s, data, Limits{MaxSteps: 500})
		if code(err) != CodeTooComplex {
			t.Errorf("err = %v, want %s", err, CodeTooComplex)
		}
		res, err := validate(t, s, data, DefaultLimits)
		if err != nil || !res.Valid {
			t.Errorf("default limits: res = %+v, err = %v", res, err)
		}
	})
	t.Run("max errors", func(t *testing.T) {
		s := mustCompile(t, `{"items":{"type":"string"}}`, Options{})
		res, err := validate(t, s, "["+strings.TrimSuffix(strings.Repeat("1,", 10), ",")+"]", Limits{MaxErrors: 3})
		if err != nil {
			t.Fatal(err)
		}
		if res.Valid || len(res.Errors) != 3 || !res.Truncated {
			t.Errorf("res = %+v, want 3 errors, truncated", res)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		s := mustCompile(t, `{"items":{"type":"number"}}`, Options{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		doc, _ := jsonpath.Decode([]byte("[" + strings.TrimSuffix(strings.Repeat("1,", 5000), ",") + "]"))
		_, err := s.Validate(ctx, doc, DefaultLimits)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
	t.Run("huge exponent", func(t *testing.T) {
		s := mustCompile(t, `{"maximum":1e400000000}`, Options{})
		res, err := validate(t, s, `1e400000001`, DefaultLimits)
		if err != nil || res.Valid {
			t.Errorf("res = %+v, err = %v", res, err)
		}
	})
}

func TestGoValueModel(t *testing.T) {
	var schema any
	if err := json.Unmarshal([]byte(`{"type":"object","properties":{"n":{"type":"integer","maximum":3}},"required":["n"]}`), &schema); err != nil {
		t.Fatal(err)
	}
	s, err := Compile(schema, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		v    any
		want bool
	}{
		{map[string]any{"n": 2.0}, true},
		{map[string]any{"n": 2.5}, false},
		{map[string]any{"n": 4}, false},
		{map[string]any{}, false},
		{"x", false},
	} {
		res, err := s.Validate(context.Background(), tc.v, DefaultLimits)
		if err != nil || res.Valid != tc.want {
			t.Errorf("Validate(%v) = %+v, %v; want valid %v", tc.v, res, err, tc.want)
		}
	}
}

func TestTranslatePattern(t *testing.T) {
	tests := []struct{ in, want string }{
		{`abc`, `abc`},
		{`\u00e9`, `\x{00e9}`},
		{`\u{1F600}`, `\x{1F600}`},
		{`\cJ`, `\x{0a}`},
		{`\\u0041`, `\\u0041`},
		{`\d\w\.`, `\d\w\.`},
		{`\s`, "[" + ecmaSpace + "]"},
		{`[\s-]`, "[" + ecmaSpace + "-]"},
		{`\S`, "[^" + ecmaSpace + "]"},
		{`[\S]`, `[\S]`},
		{`a[]b`, `a[^\x00-\x{10FFFF}]b`},
		{`[^]`, `[\x00-\x{10FFFF}]`},
		{`[]a]`, `[^\x00-\x{10FFFF}]a]`},
		{`[a[]`, `[a[]`},
	}
	for _, tc := range tests {
		if got := translatePattern(tc.in); got != tc.want {
			t.Errorf("translatePattern(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResources(t *testing.T) {
	decode := func(s string) any {
		v, err := jsonpath.Decode([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	opts := Options{Resources: map[string]any{
		"https://example.com/defs.json#":  decode(`{"$defs":{"id":{"type":"integer"},"t":{"$anchor":"tag","type":"string"},"o":{"$ref":"other.json"}}}`),
		"https://example.com/other.json":  decode(`{"$id":"https://example.com/other.json","type":"object","required":["x"]}`),
		"https://example.com/broken.json": decode(`{"type":"int"}`), // never referenced, so never compiled
	}}
	s := mustCompile(t, `{"properties":{
		"id":{"$ref":"https://example.com/defs.json#/$defs/id"},
		"tag":{"$ref":"https://example.com/defs.json#tag"},
		"o":{"$ref":"https://example.com/defs.json#/$defs/o"}}}`, opts)
	res, err := validate(t, s, `{"id":"x","tag":1,"o":{}}`, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	want := []Error{
		{"/id", "$.id", "/properties/id/$ref/type", "https://example.com/defs.json#/$defs/id/type", "type", "must be integer, not string"},
		{"/tag", "$.tag", "/properties/tag/$ref/type", "https://example.com/defs.json#/$defs/t/type", "type", "must be string, not number"},
		{"/o", "$.o", "/properties/o/$ref/$ref/required", "https://example.com/other.json#/required", "required", `missing required property "x"`},
	}
	if len(res.Errors) != len(want) {
		t.Fatalf("errors = %+v, want %+v", res.Errors, want)
	}
	for i, e := range res.Errors {
		if e != want[i] {
			t.Errorf("error %d = %+v, want %+v", i, e, want[i])
		}
	}
	if res, _ := validate(t, s, `{"id":1,"tag":"a","o":{"x":1}}`, DefaultLimits); !res.Valid {
		t.Errorf("valid value: %+v", res.Errors)
	}

	_, err = CompileJSON([]byte(`{"$ref":"https://example.com/missing.json"}`), opts)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeUnresolvedRef {
		t.Errorf("a document not given: err = %v, want %s", err, CodeUnresolvedRef)
	}
	_, err = CompileJSON([]byte(`{"$ref":"https://example.com/broken.json"}`), opts)
	if !errors.As(err, &e) || e.Code != CodeInvalidSchema || !strings.Contains(e.Message, "https://example.com/broken.json#/type") {
		t.Errorf("an invalid document: err = %v, want %s at https://example.com/broken.json#/type", err, CodeInvalidSchema)
	}
}

func TestJSONPath(t *testing.T) {
	got := jsonPath([]pathToken{{name: "items"}, {name: "0", index: true}, {name: "a b"}, {name: "it's"}, {name: "_x9"}, {name: "9x"}, {name: "\n"}})
	want := `$.items[0]['a b']['it\'s']._x9['9x']['\n']`
	if got != want {
		t.Errorf("jsonPath = %s, want %s", got, want)
	}
}

func TestDraft(t *testing.T) {
	for schema, want := range map[string]Draft{
		`{}`: Draft2020,
		`{"$schema":"https://json-schema.org/draft/2019-09/schema"}`: Draft2019,
		`{"$schema":"http://json-schema.org/draft-07/schema#"}`:      Draft7,
		`{"$schema":"http://json-schema.org/draft-06/schema#"}`:      Draft6,
		`{"$schema":"http://json-schema.org/draft-04/schema#"}`:      Draft4,
		`{"$schema":"https://example.com/custom"}`:                   Draft2020,
	} {
		if got := mustCompile(t, schema, Options{}).Draft(); got != want {
			t.Errorf("%s: draft %v, want %v", schema, got, want)
		}
	}
	if got := mustCompile(t, `{}`, Options{Draft: Draft7}).Draft(); got != Draft7 {
		t.Errorf("Options.Draft: %v", got)
	}
}

func TestConcurrentValidate(t *testing.T) {
	s := mustCompile(t, `{"$defs":{"n":{"$dynamicAnchor":"n","type":"number"}},"items":{"$dynamicRef":"#n"},"unevaluatedItems":false}`, Options{})
	done := make(chan bool)
	for range 8 {
		go func() {
			for range 50 {
				doc, _ := jsonpath.Decode([]byte(`[1,2,"x"]`))
				res, err := s.Validate(context.Background(), doc, DefaultLimits)
				if err != nil || res.Valid {
					t.Errorf("res = %+v, err = %v", res, err)
				}
			}
			done <- true
		}()
	}
	for range 8 {
		<-done
	}
}
