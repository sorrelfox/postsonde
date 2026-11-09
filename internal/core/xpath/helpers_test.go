package xpath

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/xmldom"
)

var (
	samplesMu sync.Mutex
	samples   = map[string]*xmldom.Document{}
)

// sample parses testdata/<name> once.
func sample(t testing.TB, name string) *xmldom.Document {
	t.Helper()
	samplesMu.Lock()
	defer samplesMu.Unlock()
	if doc, ok := samples[name]; ok {
		return doc
	}
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := xmldom.Parse(data)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	samples[name] = doc
	return doc
}

func mustDoc(t testing.TB, src string) *xmldom.Document {
	t.Helper()
	doc, err := xmldom.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// format renders a result for comparison: the node paths one per line for
// a node-set, "<type>:<string value>" for a scalar.
func format(r Result) string {
	if r.Type == NodeSet {
		return strings.Join(r.Paths(), "\n")
	}
	return r.Type.String() + ":" + r.String()
}

// queryCase is an expression and its formatted result (see format); a
// node-set is given as its paths.
type queryCase struct {
	expr string
	want []string
}

func runCases(t *testing.T, doc *xmldom.Document, opts Options, cases []queryCase) {
	t.Helper()
	for _, c := range cases {
		x, err := Parse(c.expr)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.expr, err)
			continue
		}
		r, err := x.Eval(context.Background(), doc.Root, opts)
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		if got, want := format(r), strings.Join(c.want, "\n"); got != want {
			t.Errorf("%s:\n got: %s\nwant: %s", c.expr, strings.ReplaceAll(got, "\n", "\n      "), strings.ReplaceAll(want, "\n", "\n      "))
		}
		if r.Type != x.ResultType() {
			t.Errorf("%s: result type %v, static type %v", c.expr, r.Type, x.ResultType())
		}
	}
}

// ns is a shorthand for a node-set of paths.
func ns(paths ...string) []string { return paths }

func val(s string) []string { return []string{s} }
