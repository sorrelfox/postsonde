package complete

import (
	"strings"
	"testing"
)

// FuzzComplete checks that Complete never panics at any offset of arbitrary
// documents, that the replace range is valid and holds the cursor, that the
// prefix is the text before the cursor, and that items are well formed and
// numbered in order.
//
// Run it with
// go test ./internal/core/graphql/complete -run '^$' -fuzz FuzzComplete -fuzztime 60s
func FuzzComplete(f *testing.F) {
	for _, tt := range completionTests {
		f.Add(tt.src)
	}
	for _, s := range []string{
		"", "{{{{[[[[", "query Q($a: [[[Int = {a: [{b: ", "fragment on on on on { ...on }",
		"{ a(x: \"\\u{zz}\" y: 01 z: \"\"\"open", "query A {\n  a(\nquery B { b }\nfragment F on T {",
		"{ a: b: c(d: $) @ @e( ... ... on ... { } ] ) }", "type T { f(a: Int = {x: 1}): [T!]! }\n{ a }",
		"query Q($ids: [ID!]!) @cost(weight: $x) { nodes(ids: [$ids, $]) { ... on User @skip(if: $b) { __typename } } }",
		"# ü🚀\n{ viewer { ...U } }\nfragment U on User { avatarUrl(size: 1) }",
	} {
		f.Add(s)
	}
	s := parseFixture(f, github)
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 4096 {
			return
		}
		step := 1
		if len(src) > 256 {
			step = len(src)/64 + 1
		}
		for off := 0; off <= len(src); off += step {
			r := Complete(s, src, off)
			if r.From < 0 || r.From > off || off > r.To || r.To > len(src) {
				t.Fatalf("offset %d: range [%d, %d) of %q", off, r.From, r.To, src)
			}
			if !strings.HasSuffix(src[r.From:off], r.Prefix) {
				t.Fatalf("offset %d: prefix %q is not before the cursor in %q", off, r.Prefix, src[r.From:off])
			}
			if r.Context == "" {
				t.Fatalf("offset %d: empty context", off)
			}
			for i, it := range r.Items {
				if it.Label == "" || it.Kind == "" || it.InsertText == "" || it.SortText != pad(i) ||
					it.SuffixCursor < 0 || it.SuffixCursor > len(it.Suffix) {
					t.Fatalf("offset %d: bad item %d %+v", off, i, it)
				}
			}
		}
		// Out-of-range offsets are clamped.
		for _, off := range []int{-1, len(src) + 1} {
			r := Complete(s, src, off)
			if r.From < 0 || r.To > len(src) || r.From > r.To {
				t.Fatalf("offset %d: range [%d, %d)", off, r.From, r.To)
			}
		}
	})
}
