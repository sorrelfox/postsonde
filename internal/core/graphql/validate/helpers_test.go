package validate

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// Fixture names, mapped to the introspection results stored with package
// schema.
var fixtureFiles = map[string]string{
	"countries":    "countries-full.json",
	"swapi":        "swapi-conservative.json",
	"rickandmorty": "rickandmorty-conservative.json",
	"github":       "github-subset.json",
}

// fixtureBuilders make the fixtures that are variations of stored ones.
var fixtureBuilders = map[string]func(testing.TB) *schema.Schema{
	"github+tag": githubWithTag,
}

var (
	fixtureMu    sync.Mutex
	fixtureCache = map[string]*schema.Schema{}
)

func loadSchema(t testing.TB, name string) *schema.Schema {
	t.Helper()
	fixtureMu.Lock()
	defer fixtureMu.Unlock()
	if s, ok := fixtureCache[name]; ok {
		return s
	}
	if build, ok := fixtureBuilders[name]; ok {
		s := build(t)
		fixtureCache[name] = s
		return s
	}
	file, ok := fixtureFiles[name]
	if !ok {
		t.Fatalf("unknown fixture %q", name)
	}
	data, err := os.ReadFile(filepath.Join("..", "schema", "testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	s, err := schema.ParseIntrospection(data)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	fixtureCache[name] = s
	return s
}

// Range markers: "⟦" and "⟧" enclose an expected diagnostic range in a test
// query. They are not valid GraphQL outside strings, so they cannot be
// confused with the query itself.
const (
	markOpen  = "⟦"
	markClose = "⟧"
)

// mark removes the range markers from src and returns the clean source and
// the marked byte ranges in it, in order of their opening markers. Markers
// do not nest, but a doubled marker "⟦⟦name⟧⟧" stands for two diagnostics on
// the same range (and so on).
func mark(t testing.TB, src string) (string, []ast.Loc) {
	t.Helper()
	var b strings.Builder
	var locs []ast.Loc
	open, count := -1, 0
	for i := 0; i < len(src); {
		switch {
		case strings.HasPrefix(src[i:], markOpen):
			if open >= 0 && b.Len() != open {
				t.Fatalf("nested %s in %q", markOpen, src)
			}
			open = b.Len()
			count++
			i += len(markOpen)
		case strings.HasPrefix(src[i:], markClose):
			if open < 0 {
				t.Fatalf("%s without %s in %q", markClose, markOpen, src)
			}
			for range count {
				if !strings.HasPrefix(src[i:], markClose) {
					t.Fatalf("unbalanced %s in %q", markClose, src)
				}
				locs = append(locs, ast.Loc{Start: open, End: b.Len()})
				i += len(markClose)
			}
			open, count = -1, 0
		default:
			b.WriteByte(src[i])
			i++
		}
	}
	if open >= 0 {
		t.Fatalf("unclosed %s in %q", markOpen, src)
	}
	return b.String(), locs
}

// want is an expected diagnostic; its range comes from the markers. An
// empty msg is not checked; a nil sugg is not checked.
type want struct {
	code string
	msg  string
	sugg []string
}

type ruleCase struct {
	name   string
	schema string
	query  string // with range markers
	want   []want // in the order of the markers
	// syntax allows parse errors (half-typed documents).
	syntax bool
	// ignore lists codes of other rules that the document triggers on
	// purpose, such as unused variables declared for their default values.
	ignore []string
}

func runCases(t *testing.T, cases []ruleCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := loadSchema(t, tc.schema)
			src, locs := mark(t, tc.query)
			if len(locs) != len(tc.want) {
				t.Fatalf("%d markers but %d expected diagnostics", len(locs), len(tc.want))
			}
			doc, errs := parser.Parse(src)
			if len(errs) > 0 && !tc.syntax {
				t.Fatalf("syntax errors: %v", errs)
			}
			got := slices.DeleteFunc(Validate(s, doc), func(d Diagnostic) bool { return slices.Contains(tc.ignore, d.Code) })
			if len(got) != len(tc.want) {
				t.Fatalf("got %d diagnostics, want %d:\n%s", len(got), len(tc.want), describe(src, got))
			}
			for i, w := range tc.want {
				g, l := got[i], locs[i]
				if g.Code != w.code || g.Start != l.Start || g.End != l.End {
					t.Errorf("diagnostic %d = %s [%d:%d] %q, want %s [%d:%d] %q\nall:\n%s",
						i, g.Code, g.Start, g.End, src[g.Start:g.End], w.code, l.Start, l.End, src[l.Start:l.End], describe(src, got))
				}
				if g.Severity != SeverityError {
					t.Errorf("diagnostic %d severity = %q", i, g.Severity)
				}
				if w.msg != "" && g.Message != w.msg {
					t.Errorf("diagnostic %d message\n got: %s\nwant: %s", i, g.Message, w.msg)
				}
				if w.sugg != nil && !slices.Equal(g.Suggestions, w.sugg) {
					t.Errorf("diagnostic %d suggestions = %q, want %q", i, g.Suggestions, w.sugg)
				}
			}
		})
	}
}

func describe(src string, ds []Diagnostic) string {
	var b strings.Builder
	for _, d := range ds {
		fmt.Fprintf(&b, "  %s [%d:%d] %q: %s %q\n", d.Code, d.Start, d.End, src[d.Start:d.End], d.Message, d.Suggestions)
	}
	return b.String()
}
