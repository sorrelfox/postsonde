package template

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func pathChain() Chain {
	return Chain{
		Vars("Globals", map[string]string{
			"host": "example.com",
			"auth": `{"token": "t0k", "user": {"id": 7, "name": "Ann", "tags": ["a", "b"]}, "ratio": 1.50, "on": true, "none": null}`,
			// A flat dotted name wins over selecting from a base.
			"auth.token": "flat",
			"list":       ` [ {"id": 1}, {"id": 2} ] `,
			"text":       "not json",
			"api":        `{"url": "https://{{host}}/v1", "key": "{{secret}}"}`,
			"api.v2":     `{"url": "https://v2.{{host}}"}`,
			"loop":       `{"self": "{{loop.self}}"}`,
			"b1":         `{"x": "{{b2.x}}"}`,
			"b2":         `{"x": "{{b1.x}}"}`,
			"escaped":    `{"a": "\{{host}}"}`,
			"odd.":       "trailing dot",
		}),
		MapScope{Label: "Environment: dev", Variables: map[string]Variable{
			"secret": {Value: "s3", Secret: true},
			"creds":  {Value: `{"password": "pw"}`, Secret: true},
		}},
	}
}

func TestRenderDotNotation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		src        string
		want       string
		wantSecret bool
		wantKinds  []ProblemKind
	}{
		{"flat dotted name wins", "{{auth.token}}", "flat", false, nil},
		{"field of an object", "{{auth.user.name}}", "Ann", false, nil},
		{"number as written", "{{auth.user.id}}/{{auth.ratio}}", "7/1.50", false, nil},
		{"bool and null", "{{auth.on}} {{auth.none}}", "true null", false, nil},
		{"array index", "{{auth.user.tags.1}} {{list.0.id}}", "b 1", false, nil},
		{"object as compact JSON, keys in order", "{{list.1}}", `{"id":2}`, false, nil},
		{"array as compact JSON", "{{auth.user.tags}}", `["a","b"]`, false, nil},
		// The base's value includes a secret, so everything read from it is
		// masked, even a field without it.
		{"variables inside the base are expanded", "{{api.url}}", "https://example.com/v1", true, nil},
		{"longest defined base wins", "{{api.v2.url}}", "https://v2.example.com", false, nil},
		{"secret base makes the value secret", "{{creds.password}} {{api.key}}", "pw s3", true, nil},
		{"whitespace around the name", "{{ auth.user.name }}", "Ann", false, nil},
		{"escape inside the base value", "{{escaped.a}}", "{{host}}", false, nil},
		{"undefined base stays unresolved", "{{nope.x}}", "{{nope.x}}", false, []ProblemKind{ProblemUnresolved}},
		{"missing field", "{{auth.user.email}}", "{{auth.user.email}}", false, []ProblemKind{ProblemUnresolved}},
		{"index out of range", "{{list.5}}", "{{list.5}}", false, []ProblemKind{ProblemUnresolved}},
		{"not an index", "{{list.first}}", "{{list.first}}", false, []ProblemKind{ProblemUnresolved}},
		{"leading zero is not an index", "{{list.01}}", "{{list.01}}", false, []ProblemKind{ProblemUnresolved}},
		{"base is not JSON", "{{text.x}}", "{{text.x}}", false, []ProblemKind{ProblemUnresolved}},
		{"into a scalar", "{{auth.token.x}}", "{{auth.token.x}}", false, []ProblemKind{ProblemUnresolved}},
		{"empty part is a plain name", "{{auth..token}}", "{{auth..token}}", false, []ProblemKind{ProblemUnresolved}},
		{"trailing dot name defined flat", "{{odd.}}", "trailing dot", false, nil},
		{"self cycle through a path", "{{loop.self}}", "{{loop.self}}", false, []ProblemKind{ProblemCycle}},
		{"two-variable cycle through paths", "{{b1.x}}", "{{b1.x}}", false, []ProblemKind{ProblemCycle}},
		{"escaped reference is literal", `\{{auth.user.name}}`, "{{auth.user.name}}", false, nil},
	}
	r := Resolver{Scopes: pathChain()}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := r.Render(tt.src)
			if res.Text != tt.want || res.Secret != tt.wantSecret {
				t.Errorf("Render(%q) = %q secret %v, want %q secret %v", tt.src, res.Text, res.Secret, tt.want, tt.wantSecret)
			}
			var kinds []ProblemKind
			for _, p := range res.Problems {
				kinds = append(kinds, p.Kind)
			}
			if !reflect.DeepEqual(kinds, tt.wantKinds) {
				t.Errorf("Render(%q) problems = %+v, want kinds %v", tt.src, res.Problems, tt.wantKinds)
			}
		})
	}
}

func TestRenderDotNotationRefs(t *testing.T) {
	t.Parallel()
	res := Resolver{Scopes: pathChain()}.Render("{{api.url}}")
	if len(res.Refs) != 1 {
		t.Fatalf("refs = %+v", res.Refs)
	}
	ref := res.Refs[0]
	if ref.Name != "api.url" || ref.Base != "api" || !ref.Resolved || ref.Scope != "Globals" || !ref.Secret ||
		ref.Value != "https://example.com/v1" || len(ref.Nested) != 2 || ref.Nested[0].Name != "host" {
		t.Errorf("ref = %+v", ref)
	}
	if res.Refs[0].Start != 0 || res.Refs[0].End != 11 {
		t.Errorf("ref span = %d..%d", ref.Start, ref.End)
	}
}

func TestRenderDotNotationErrors(t *testing.T) {
	t.Parallel()
	r := Resolver{Scopes: pathChain()}
	tests := []struct {
		src, code, msg string
	}{
		{"{{auth.user.email}}", CodeUnresolved, `The variable {{auth.user.email}} cannot be resolved: {{auth.user}} has no field "email".`},
		{"{{text.x}}", CodeUnresolved, "The variable {{text.x}} cannot be resolved: the value of {{text}} is not JSON."},
		{"{{list.9}}", CodeUnresolved, "The variable {{list.9}} cannot be resolved: {{list}} has no element 9."},
		{"{{list.x}}", CodeUnresolved, `The variable {{list.x}} cannot be resolved: {{list}} is a list: "x" is not an index.`},
		{"{{auth.on.x}}", CodeUnresolved, "The variable {{auth.on.x}} cannot be resolved: {{auth.on}} is not an object or a list."},
		{"{{nope.x}}", CodeUnresolved, "The variable {{nope.x}} is not defined."},
		{"{{loop.self}}", CodeCycle, "The variable {{loop}} refers to itself."},
	}
	for _, tt := range tests {
		err := r.Render(tt.src).Err()
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != tt.code || e.Message != tt.msg {
			t.Errorf("Render(%q).Err() = %v, want code %s message %q", tt.src, err, tt.code, tt.msg)
		}
	}
	// A secret value never appears in the message.
	err := r.Render("{{creds.nope}}").Err()
	if err == nil || strings.Contains(err.Error(), "pw") {
		t.Errorf("Err() = %v", err)
	}
}

func TestSplitPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want []PathSplit
	}{
		{"a", nil},
		{"a.b", []PathSplit{{"a", []string{"b"}}}},
		{"a.b.c", []PathSplit{{"a.b", []string{"c"}}, {"a", []string{"b", "c"}}}},
		{"a..b", nil},
		{".a", nil},
		{"a.", nil},
		{"$datetime YYYY.MM", nil},
	}
	for _, tt := range tests {
		if got := SplitPath(tt.name); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("SplitPath(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestDotNotationDoesNotShadowDynamicValues(t *testing.T) {
	t.Parallel()
	// A scope defining "$datetime" must not turn a dynamic value with a
	// dotted argument into a path lookup.
	r := Resolver{Scopes: Chain{Vars("s", map[string]string{"x": `{"y": 1}`})}}
	res := r.Render("{{$datetime YYYY.MM}}")
	if res.Err() != nil || len(res.Refs) != 1 || res.Refs[0].Scope != DynamicScope {
		t.Errorf("Render = %+v", res)
	}
}

// FuzzSelect checks that selecting never panics and returns a string or an
// error for any value and path.
func FuzzSelect(f *testing.F) {
	f.Add(`{"a":{"b":[1,"x",{"c":null}]}}`, "a.b.2.c")
	f.Add(`[1,2`, "0")
	f.Add(`"s"`, "x")
	f.Fuzz(func(t *testing.T, value, name string) {
		for _, s := range SplitPath("v." + name) {
			if s.Base != "v" {
				continue
			}
			if _, err := Select("v", value, s.Path); err != nil && err.Error() == "" {
				t.Fatalf("empty error for %q %q", value, name)
			}
		}
	})
}
