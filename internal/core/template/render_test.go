package template

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// chain is global < environment < collection < folder < runtime.
func testChain() Chain {
	return Chain{
		Vars("Globals", map[string]string{"host": "global.example", "scheme": "https", "only_global": "g"}),
		Vars("Environment: dev", map[string]string{"host": "dev.example", "base": "{{scheme}}://{{host}}"}),
		MapScope{Label: "Collection", Variables: map[string]Variable{
			"token": {Value: "s3cr3t", Secret: true},
			"auth":  {Value: "Bearer {{token}}"},
		}},
		Vars("Folder", map[string]string{"path": "/users"}),
		Vars("Runtime", map[string]string{"host": "runtime.example"}),
	}
}

func TestChainLookup(t *testing.T) {
	t.Parallel()
	c := testChain()
	tests := []struct {
		name      string
		lookup    string
		wantOK    bool
		wantScope string
		wantValue string
	}{
		{"highest scope wins", "host", true, "Runtime", "runtime.example"},
		{"lowest scope only", "only_global", true, "Globals", "g"},
		{"middle scope", "path", true, "Folder", "/users"},
		{"undefined", "nope", false, "", ""},
		{"case sensitive", "HOST", false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, ok := c.Lookup(tt.lookup)
			if ok != tt.wantOK || b.Scope != tt.wantScope || b.Value != tt.wantValue {
				t.Errorf("Lookup(%q) = %+v, %v; want scope %q value %q ok %v", tt.lookup, b, ok, tt.wantScope, tt.wantValue, tt.wantOK)
			}
		})
	}

	all := c.All("host")
	var scopes []string
	for _, b := range all {
		scopes = append(scopes, b.Scope)
	}
	if want := []string{"Runtime", "Environment: dev", "Globals"}; !reflect.DeepEqual(scopes, want) {
		t.Errorf("All(host) scopes = %v, want %v", scopes, want)
	}
}

func TestRender(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		src        string
		want       string
		wantSecret bool
		problems   []Problem
	}{
		{"no variables", "plain", "plain", false, nil},
		{"precedence", "{{host}}", "runtime.example", false, nil},
		{"nested value uses whole chain", "{{base}}{{path}}", "https://runtime.example/users", false, nil},
		{"secret carries over", "Authorization: {{ auth }}", "Authorization: Bearer s3cr3t", true, nil},
		{"escape", `\{{host}} is {{host}}`, "{{host}} is runtime.example", false, nil},
		{"unresolved stays as written", "a{{ missing }}b", "a{{ missing }}b", false, []Problem{
			{Kind: ProblemUnresolved, Name: "missing", Path: []string{"missing"}, Start: 1, End: 14},
		}},
		{"empty name", "{{ }}", "{{ }}", false, []Problem{
			{Kind: ProblemEmptyName, Path: []string{}, Start: 0, End: 5},
		}},
		{"unterminated is a warning", "{{host", "{{host", false, []Problem{
			{Kind: ProblemUnterminated, Path: []string{}, Start: 0, End: 2},
		}},
	}
	r := Resolver{Scopes: testChain()}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := r.Render(tt.src)
			if res.Text != tt.want || res.Secret != tt.wantSecret {
				t.Errorf("Render(%q) = %q secret %v, want %q secret %v", tt.src, res.Text, res.Secret, tt.want, tt.wantSecret)
			}
			if !reflect.DeepEqual(res.Problems, tt.problems) {
				t.Errorf("Render(%q) problems\n got %+v\nwant %+v", tt.src, res.Problems, tt.problems)
			}
		})
	}
}

func TestRenderRefs(t *testing.T) {
	t.Parallel()
	res := Resolver{Scopes: testChain()}.Render("{{base}}/{{nope}}")
	want := []Ref{
		{
			Name: "base", Start: 0, End: 8, NameStart: 2, NameEnd: 6,
			Resolved: true, Scope: "Environment: dev", Value: "https://runtime.example",
			Nested: []Ref{
				{Name: "scheme", Start: 0, End: 10, NameStart: 2, NameEnd: 8, Resolved: true, Scope: "Globals", Value: "https"},
				{Name: "host", Start: 13, End: 21, NameStart: 15, NameEnd: 19, Resolved: true, Scope: "Runtime", Value: "runtime.example"},
			},
		},
		{Name: "nope", Start: 9, End: 17, NameStart: 11, NameEnd: 15},
	}
	if !reflect.DeepEqual(res.Refs, want) {
		t.Errorf("Refs\n got %+v\nwant %+v", res.Refs, want)
	}
}

func TestRenderNestedProblems(t *testing.T) {
	t.Parallel()
	chain := Chain{Vars("Env", map[string]string{
		"url":   "https://{{host}}/{{path}}",
		"path":  "{{missing}}",
		"host":  "example.com",
		"a":     "x{{b}}",
		"b":     "{{c}}",
		"c":     "{{a}}",
		"self":  "{{ self }}",
		"loop2": "{{self}}",
	})}
	tests := []struct {
		name     string
		src      string
		want     string
		problems []Problem
	}{
		{"unresolved deep inside", "go {{url}}", "go https://example.com/{{missing}}", []Problem{
			{Kind: ProblemUnresolved, Name: "missing", Path: []string{"url", "path", "missing"}, Start: 3, End: 10},
		}},
		{"cycle", "{{a}}", "x{{a}}", []Problem{
			{Kind: ProblemCycle, Name: "a", Path: []string{"a", "b", "c", "a"}, Start: 0, End: 5},
		}},
		{"self reference", "{{self}}", "{{ self }}", []Problem{
			{Kind: ProblemCycle, Name: "self", Path: []string{"self", "self"}, Start: 0, End: 8},
		}},
		{"cycle reached through another name", "{{loop2}}", "{{ self }}", []Problem{
			{Kind: ProblemCycle, Name: "self", Path: []string{"loop2", "self", "self"}, Start: 0, End: 9},
		}},
		{"same name twice is not a cycle", "{{host}}{{host}}", "example.comexample.com", nil},
	}
	r := Resolver{Scopes: chain}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := r.Render(tt.src)
			if res.Text != tt.want {
				t.Errorf("Render(%q) = %q, want %q", tt.src, res.Text, tt.want)
			}
			if !reflect.DeepEqual(res.Problems, tt.problems) {
				t.Errorf("Render(%q) problems\n got %+v\nwant %+v", tt.src, res.Problems, tt.problems)
			}
		})
	}
}

func TestRenderLimits(t *testing.T) {
	t.Parallel()
	deep := map[string]string{}
	for i := range 20 {
		deep["v"+string(rune('a'+i))] = "{{v" + string(rune('a'+i+1)) + "}}"
	}
	deep["vu"] = "end"
	fanout := map[string]string{"leaf": strings.Repeat("x", 100)}
	prev := "leaf"
	for i := range 8 { // 10^8 lookups without a limit
		name := "f" + string(rune('a'+i))
		fanout[name] = strings.Repeat("{{"+prev+"}}", 10)
		prev = name
	}

	tests := []struct {
		name string
		r    Resolver
		src  string
		kind ProblemKind
		text string
	}{
		{"depth", Resolver{Scopes: Chain{Vars("d", deep)}, MaxDepth: 5}, "{{va}}", ProblemTooDeep, "{{vf}}"},
		{"expansions", Resolver{Scopes: Chain{Vars("f", fanout)}}, "{{" + prev + "}}", ProblemLimit, ""},
		{"length", Resolver{Scopes: Chain{Vars("f", fanout)}, MaxLength: 500}, "{{fa}}", ProblemLimit, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := tt.r.Render(tt.src)
			if len(res.Problems) == 0 || res.Problems[0].Kind != tt.kind {
				t.Fatalf("problems = %+v, want first %s", res.Problems, tt.kind)
			}
			if tt.text != "" && res.Text != tt.text {
				t.Errorf("Text = %q, want %q", res.Text, tt.text)
			}
			if res.Err() == nil {
				t.Error("Err() = nil")
			}
		})
	}

	// The default depth is enough for the 21-level chain minus the limit.
	res := Resolver{Scopes: Chain{Vars("d", deep)}, MaxDepth: 25}.Render("{{va}}")
	if res.Text != "end" || len(res.Problems) != 0 {
		t.Errorf("deep chain = %q, %+v", res.Text, res.Problems)
	}
}

func TestResultErr(t *testing.T) {
	t.Parallel()
	chain := Chain{MapScope{Label: "Env", Variables: map[string]Variable{
		"a":   {Value: "{{b}}"},
		"b":   {Value: "{{a}}"},
		"tok": {Value: "hunter2", Secret: true},
		"d":   {Value: "{{e}}"},
		"e":   {Value: "{{f}}"},
		"f":   {Value: "{{g}}"},
	}}}
	tests := []struct {
		name     string
		src      string
		r        Resolver
		code     string
		message  string
		details  map[string]string
		wantNone bool
	}{
		{name: "clean", src: "{{tok}}", wantNone: true},
		{name: "warning only", src: "{{", wantNone: true},
		{name: "one unresolved", src: "{{x}} {{x}}", code: CodeUnresolved,
			message: "The variable {{x}} is not defined.",
			details: map[string]string{"names": "x", "problems": "2"}},
		{name: "several unresolved", src: "{{a1}}{{a2}}{{a3}}{{a4}}{{a5}}{{a6}}{{a7}}{{a1}}", code: CodeUnresolved,
			message: "7 variables are not defined: {{a1}}, {{a2}}, {{a3}}, {{a4}}, {{a5}}, and 2 more.",
			details: map[string]string{"names": "a1,a2,a3,a4,a5,a6,a7", "problems": "8"}},
		{name: "unresolved wins over cycle", src: "{{a}}{{x}}", code: CodeUnresolved,
			message: "The variable {{x}} is not defined.",
			details: map[string]string{"names": "x", "problems": "2"}},
		{name: "cycle", src: "{{tok}}{{a}}", code: CodeCycle,
			message: "The variable {{a}} refers to itself.",
			details: map[string]string{"path": "a,b,a", "problems": "1"}},
		{name: "too deep", src: "{{d}}", r: Resolver{MaxDepth: 2}, code: CodeTooDeep,
			message: "The variable {{d}} nests other variables too deeply.",
			details: map[string]string{"path": "d,e,f", "problems": "1"}},
		{name: "empty", src: "{{}}", code: CodeEmptyName,
			message: "A variable has no name.",
			details: map[string]string{"problems": "1"}},
		{name: "limit", src: "{{tok}}{{tok}}", r: Resolver{MaxExpansions: 1}, code: CodeLimit,
			message: "The text is too large after its variables are replaced.",
			details: map[string]string{"path": "tok", "problems": "1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := tt.r
			r.Scopes = chain
			err := r.Render(tt.src).Err()
			if tt.wantNone {
				if err != nil {
					t.Fatalf("Err() = %v, want nil", err)
				}
				return
			}
			e, ok := errors.AsType[*errs.Error](err)
			if !ok {
				t.Fatalf("Err() = %v, want *errs.Error", err)
			}
			if errs.KindOf(err) != errs.InvalidArgument || e.Code != tt.code || e.Message != tt.message || e.Hint == "" {
				t.Errorf("Err() = kind %s code %q message %q hint %q", errs.KindOf(err), e.Code, e.Message, e.Hint)
			}
			if !reflect.DeepEqual(e.Details, tt.details) {
				t.Errorf("details = %v, want %v", e.Details, tt.details)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("Err() leaks a secret value: %v", err)
			}
		})
	}
}
