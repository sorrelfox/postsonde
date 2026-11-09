package jsonpath

import (
	"context"
	"os"
	"slices"
	"testing"
)

// ctsCase is one test of the JSONPath Compliance Test Suite.
type ctsCase struct {
	name         string
	selector     string
	document     any
	invalid      bool
	results      [][]any // one entry, or several valid orderings
	resultsPaths [][]string
}

// loadCTS reads testdata/cts/cts.json with this package's own decoder,
// so documents keep member order and number literals.
func loadCTS(t testing.TB) []ctsCase {
	t.Helper()
	data, err := os.ReadFile("testdata/cts/cts.json")
	if err != nil {
		t.Fatal(err)
	}
	v, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	top := v.(*Object)
	testsV, _ := top.Get("tests")
	var cases []ctsCase
	for _, tv := range testsV.([]any) {
		o := tv.(*Object)
		var c ctsCase
		name, _ := o.Get("name")
		c.name = name.(string)
		sel, _ := o.Get("selector")
		c.selector = sel.(string)
		if inv, ok := o.Get("invalid_selector"); ok {
			c.invalid = inv.(bool)
		}
		c.document, _ = o.Get("document")
		if r, ok := o.Get("result"); ok {
			c.results = [][]any{r.([]any)}
			if rp, ok := o.Get("result_paths"); ok {
				c.resultsPaths = [][]string{toStrings(rp.([]any))}
			}
		}
		if rs, ok := o.Get("results"); ok {
			for _, r := range rs.([]any) {
				c.results = append(c.results, r.([]any))
			}
			if rps, ok := o.Get("results_paths"); ok {
				for _, rp := range rps.([]any) {
					c.resultsPaths = append(c.resultsPaths, toStrings(rp.([]any)))
				}
			}
		}
		cases = append(cases, c)
	}
	return cases
}

func toStrings(vs []any) []string {
	s := make([]string, len(vs))
	for i, v := range vs {
		s[i] = v.(string)
	}
	return s
}

// TestComplianceSuite runs every case of the JSONPath Compliance Test
// Suite: invalid selectors must fail to parse; valid ones must select one
// of the expected results, with the expected normalized paths.
func TestComplianceSuite(t *testing.T) {
	cases := loadCTS(t)
	passed := 0
	for _, c := range cases {
		ok := t.Run(c.name, func(t *testing.T) {
			p, err := Parse(c.selector)
			if c.invalid {
				if err == nil {
					t.Fatalf("Parse(%q) succeeded, want invalid selector", c.selector)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.selector, err)
			}
			got, err := p.Eval(context.Background(), c.document, DefaultLimits)
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			e := &evaluator{}
			for i, want := range c.results {
				if len(want) != len(got) {
					continue
				}
				same := true
				for j := range want {
					if !e.equal(want[j], got[j].Value) {
						same = false
						break
					}
				}
				if !same {
					continue
				}
				if c.resultsPaths != nil && !slices.Equal(c.resultsPaths[i], got.Paths()) {
					t.Fatalf("paths = %q, want %q", got.Paths(), c.resultsPaths[i])
				}
				return
			}
			t.Fatalf("%q: got %s, want one of %s", c.selector, dump(got.Values()), dump(c.results))
		})
		if ok {
			passed++
		}
	}
	t.Logf("compliance suite: %d/%d passed", passed, len(cases))
}
