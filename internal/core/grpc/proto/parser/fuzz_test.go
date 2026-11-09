package parser

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/ast"
)

// FuzzParse checks the parser's invariants on arbitrary input: Parse and
// Check never panic and terminate; every node range lies within the source
// and within its parent; siblings are in source order without overlap;
// identifiers match the source; errors and diagnostics are in range, carry
// a message and a matching line:column, and errors are sorted and capped;
// and a file without errors has no placeholder identifiers.
func FuzzParse(f *testing.F) {
	paths, _ := filepath.Glob(filepath.Join("testdata", "files", "*.proto"))
	for _, p := range paths {
		if data, err := os.ReadFile(p); err == nil {
			f.Add(string(data))
		}
	}
	for _, c := range errorCases {
		f.Add(c.src)
	}
	for _, c := range partialCases {
		f.Add(c.src)
	}
	for _, c := range checkCases {
		f.Add(c.src)
	}
	f.Fuzz(func(t *testing.T, src string) {
		cfg := Config{MaxDepth: 20, MaxErrors: 30}
		file, errs := cfg.Parse("f.proto", src)
		if file == nil || file.Source != src || file.Start != 0 || file.End != len(src) {
			t.Fatalf("bad file %+v", file)
		}
		checkNode(t, src, file, ast.Loc{Start: 0, End: len(src)})

		if len(errs) > cfg.MaxErrors {
			t.Fatalf("%d errors, cap %d", len(errs), cfg.MaxErrors)
		}
		for i, e := range errs {
			pos := file.Position(e.Start)
			if e.Start < 0 || e.Start > e.End || e.End > len(src) || e.Message == "" ||
				e.Line != pos.Line || e.Column != pos.Column {
				t.Fatalf("bad error %d: %+v", i, e)
			}
			if i > 0 && e.Code != ErrTooManyErrors && errs[i-1].Start > e.Start {
				t.Fatalf("errors not sorted: %+v before %+v", errs[i-1], e)
			}
		}
		for i, d := range Check(file) {
			pos := file.Position(d.Start)
			if d.Start < 0 || d.Start > d.End || d.End > len(src) || d.Message == "" ||
				d.Line != pos.Line || d.Column != pos.Column || d.Code.String() == "" {
				t.Fatalf("bad diagnostic %d: %+v", i, d)
			}
		}
		if len(errs) == 0 {
			ast.Inspect(file, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Missing() {
					t.Fatalf("missing identifier at %d without errors", id.Start)
				}
				return true
			})
		}
	})
}

// checkNode checks that n lies within parent, that its children lie within
// it in order without overlap, and that identifiers match the source.
func checkNode(t *testing.T, src string, n ast.Node, parent ast.Loc) {
	t.Helper()
	l := n.Pos()
	if l.Start < parent.Start || l.End > parent.End || l.Start > l.End {
		t.Fatalf("%T %v outside parent %v", n, l, parent)
	}
	if id, ok := n.(*ast.Ident); ok && !id.Missing() && src[l.Start:l.End] != id.Value {
		t.Fatalf("identifier %q at %v reads %q", id.Value, l, src[l.Start:l.End])
	}
	prevEnd := l.Start
	first := true
	ast.Inspect(n, func(c ast.Node) bool {
		if first {
			first = false
			return true // n itself
		}
		cl := c.Pos()
		if cl.Start < prevEnd {
			t.Fatalf("%T %v starts before its previous sibling ends (%d) in %T %v", c, cl, prevEnd, n, l)
		}
		checkNode(t, src, c, l)
		prevEnd = cl.End
		return false // children are checked by the recursive call
	})
	if v := reflect.ValueOf(n); v.Kind() != reflect.Pointer || v.IsNil() {
		t.Fatalf("node %T is not a non-nil pointer", n)
	}
}
