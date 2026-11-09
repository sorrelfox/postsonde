package jsonpath

import (
	"context"
	"errors"
	"testing"
	"unicode/utf8"
)

// fuzzLimits keep each fuzz input fast however pathological the query.
var fuzzLimits = Limits{MaxSteps: 200_000}

// FuzzParse parses random queries. Parse must not panic; errors must point
// inside the query; a valid query must evaluate within the limits without
// panicking and every result path must select that same node again.
func FuzzParse(f *testing.F) {
	for _, c := range loadCTS(f) {
		f.Add(c.selector)
	}
	doc := mustDecode(f, bookstore)

	f.Fuzz(func(t *testing.T, src string) {
		p, err := Parse(src)
		if err != nil {
			var se *SyntaxError
			if !errors.As(err, &se) || se.Offset < 0 || se.Offset > len(src) || se.Message == "" {
				t.Fatalf("Parse(%q) error %#v", src, err)
			}
			if se.Offset16 > se.Offset {
				t.Fatalf("Offset16 %d > Offset %d", se.Offset16, se.Offset)
			}
			return
		}
		if p.String() != src {
			t.Fatalf("String() = %q", p.String())
		}
		nodes, err := p.Eval(context.Background(), doc, fuzzLimits)
		if err != nil {
			return
		}
		for _, n := range nodes {
			path := n.Path.String()
			again, err := MustParse(path).Eval(context.Background(), doc, fuzzLimits)
			if err != nil || len(again) != 1 || !(&evaluator{}).equal(again[0].Value, n.Value) {
				t.Fatalf("%q: normalized path %s selects %v, %v", src, path, again, err)
			}
		}
	})
}

// FuzzQuery evaluates random queries on random documents.
func FuzzQuery(f *testing.F) {
	f.Add(`$..*`, `{"a": [1, {"b": "c"}], "d": 1e400}`)
	f.Add(`$[?@.a == $.b[0] || match(@.c, "[a-z]+.")]`, `[{"a": 1.0, "c": "xy\n"}, {"a": {}}]`)
	f.Add(`$..[?count(@..*) > length(@)]..[::-1]`, `[[[[[1]]]], {"x": [2, 3]}]`)
	f.Add(`$[?search(@, $.p)]`, `{"p": "(a|b)*c{2,}\\p{Lu}", "q": "abcc"}`)

	f.Fuzz(func(t *testing.T, src, data string) {
		p, err := Parse(src)
		if err != nil {
			return
		}
		doc, err := Decode([]byte(data))
		if err != nil {
			return
		}
		nodes, err := p.Eval(context.Background(), doc, fuzzLimits)
		if err != nil {
			return
		}
		for _, n := range nodes {
			if s := n.Path.String(); !utf8.ValidString(s) && utf8.ValidString(data) {
				t.Fatalf("invalid UTF-8 path %q", s)
			}
		}
	})
}

// FuzzIRegexp translates random patterns: no panic, and a valid pattern
// compiles for both match() and search().
func FuzzIRegexp(f *testing.F) {
	for _, s := range []string{`a.c`, `[^\p{Lu}-]{1,3}`, `(a|b)*\.`, `^x$`, `[a-]`, `\P{Cn}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, pat string) {
		_, errMatch := compileIRegexp(pat, true)
		_, errSearch := compileIRegexp(pat, false)
		if (errMatch == nil) != (errSearch == nil) {
			t.Fatalf("%q: match err %v, search err %v", pat, errMatch, errSearch)
		}
	})
}
