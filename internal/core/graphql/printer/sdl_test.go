package printer

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
)

func TestPrettifySchema(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, src, want string }{
		{
			"object with everything",
			`"""Obj""" type User implements & Node&Entity @key(fields:"id"){"The ID." id:ID! friends(first:Int=10,after:String):[User!]!@deprecated}`,
			`"""Obj"""
type User implements Node & Entity @key(fields: "id") {
  "The ID."
  id: ID!
  friends(first: Int = 10, after: String): [User!]! @deprecated
}`,
		},
		{
			"described arguments go one per line",
			`type Q { f("a" a: Int, b: Int): Int }`,
			`type Q {
  f(
    "a"
    a: Int
    b: Int
  ): Int
}`,
		},
		{
			"union, enum, input, scalar, directive, schema",
			`schema@a{query:Q mutation:M} union U=|A|B enum E{A B@deprecated(reason:"x")} input I{a:Int=1 b:[S!]=["x"]}
scalar Date@specifiedBy(url:"u") directive@d(a:Int)repeatable on|FIELD|OBJECT`,
			`schema @a {
  query: Q
  mutation: M
}

union U = A | B

enum E {
  A
  B @deprecated(reason: "x")
}

input I {
  a: Int = 1
  b: [S!] = ["x"]
}

scalar Date @specifiedBy(url: "u")

directive @d(a: Int) repeatable on FIELD | OBJECT`,
		},
		{
			"extensions",
			`extend schema@a extend type T implements I extend union U=C extend enum E{Z} extend input I@d extend scalar S@d`,
			`extend schema @a

extend type T implements I

extend union U = C

extend enum E {
  Z
}

extend input I @d

extend scalar S @d`,
		},
		{
			"comments stay",
			"# header\ntype T { # after brace\n  a: Int # trailing\n  # before b\n  b: Int\n  # before brace\n}\n# end",
			"# header\ntype T { # after brace\n  a: Int # trailing\n  # before b\n  b: Int\n  # before brace\n}\n# end",
		},
		{
			"long argument lists break",
			`type Q { search(query: String!, first: Int = 10, after: String, orderBy: SearchOrder = {field: NAME}): SearchResult }`,
			`type Q {
  search(
    query: String!
    first: Int = 10
    after: String
    orderBy: SearchOrder = {field: NAME}
  ): SearchResult
}`,
		},
	}
	for _, tt := range tests {
		got, errs := PrettifySchema(tt.src)
		if len(errs) > 0 {
			t.Errorf("%s: errors %v", tt.name, errs)
			continue
		}
		if got != tt.want {
			t.Errorf("%s: PrettifySchema =\n%s\nwant\n%s", tt.name, got, tt.want)
		}
		checkSchemaRoundTrip(t, tt.src)
	}
	if out, errs := PrettifySchema("type T {"); out != "" || len(errs) == 0 {
		t.Errorf("PrettifySchema(invalid) = %q, %v", out, errs)
	}
}

// schemaCorpus is the parser's valid type system documents (the sources
// of its SDL golden files) plus documents with comments in every position.
func schemaCorpus(t testing.TB) []string {
	files, err := filepath.Glob("../parser/testdata/sdl/*.golden")
	if err != nil || len(files) == 0 {
		t.Fatalf("no SDL corpus: %v", err)
	}
	var docs []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		end := strings.Index(s, "\n# tree\n")
		if !strings.HasPrefix(s, "# source\n") || end < 0 {
			t.Fatalf("%s: no source section", f)
		}
		docs = append(docs, s[len("# source\n"):end])
	}
	return append(docs,
		"#a\n\"d\"#b\ntype#c\nT#d\nimplements#e\nA#f\n&#g\nB#h\n@#i\nx#j\n{#k\n\"f\"#l\nf#m\n(#n\na#o\n:#p\nInt#q\n=#r\n1#s\n)#t\n:#u\n[#v\nT#w\n!#x\n]#y\n}#z",
		"union#a\nU#b\n=#c\n|#d\nA#e\n|#f\nB#g\n\ndirective#h\n@#i\nd#j\nrepeatable#k\non#l\nFIELD#m\n|#n\nOBJECT",
		"extend#a\nschema#b\n{#c\nquery#d\n:#e\nQ#f\n}#g\nenum#h\nE#i\n{#j\nA#k\n}",
	)
}

func TestSchemaCorpusRoundTrip(t *testing.T) {
	t.Parallel()

	for _, src := range schemaCorpus(t) {
		checkSchemaRoundTrip(t, src)
	}
}

// FuzzPrintSchema checks checkSchemaRoundTrip's properties on every input
// that parses as SDL without errors. Run it with
// go test ./internal/core/graphql/printer -run '^$' -fuzz FuzzPrintSchema -fuzztime 60s
func FuzzPrintSchema(f *testing.F) {
	for _, src := range schemaCorpus(f) {
		f.Add(src)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if _, errs := parser.ParseSchema(src); len(errs) > 0 {
			return
		}
		checkSchemaRoundTrip(t, src)
	})
}

// checkSchemaRoundTrip checks, for SDL: the prettified and the minified
// documents parse without errors to the same tree (and the same extension,
// implements and repeatable markers, which are offsets); Prettify is
// idempotent and keeps the comments; Minify is idempotent and agrees with
// Minify of the prettified document.
func checkSchemaRoundTrip(t *testing.T, src string) {
	t.Helper()
	doc, errs := parser.ParseSchema(src)
	if len(errs) > 0 {
		t.Fatalf("corpus document %q has errors: %v", src, errs)
	}
	want := shape(doc) + markers(doc)

	pretty := Print(doc)
	prettyDoc, errs := parser.ParseSchema(pretty)
	if len(errs) > 0 {
		t.Fatalf("Prettify(%q) = %q, which has errors: %v", src, pretty, errs)
	}
	if got := shape(prettyDoc) + markers(prettyDoc); got != want {
		t.Fatalf("Prettify(%q) = %q changes the tree:\n%s\nwant\n%s", src, pretty, got, want)
	}
	if again := Print(prettyDoc); again != pretty {
		t.Fatalf("Prettify is not idempotent on %q:\n%s\nthen\n%s", src, pretty, again)
	}
	if got, want := commentTexts(prettyDoc), commentTexts(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("Prettify(%q) = %q comments %q, want %q", src, pretty, got, want)
	}

	small := PrintMinified(doc)
	smallDoc, errs := parser.ParseSchema(small)
	if len(errs) > 0 {
		t.Fatalf("Minify(%q) = %q, which has errors: %v", src, small, errs)
	}
	if got := shape(smallDoc) + markers(smallDoc); got != want {
		t.Fatalf("Minify(%q) = %q changes the tree:\n%s\nwant\n%s", src, small, got, want)
	}
	if again := PrintMinified(smallDoc); again != small {
		t.Fatalf("Minify is not idempotent on %q: %q then %q", src, small, again)
	}
	if fromPretty := PrintMinified(prettyDoc); fromPretty != small {
		t.Fatalf("Minify(Prettify(%q)) = %q, want %q", src, fromPretty, small)
	}
}

// markers lists the offset fields shape leaves out that carry meaning:
// whether each definition is an extension, has "implements", "=" or a
// block, and whether a directive is repeatable.
func markers(doc *ast.Document) string {
	var b strings.Builder
	for _, def := range doc.Definitions {
		switch d := def.(type) {
		case *ast.SchemaDefinition:
			fmt.Fprintf(&b, "schema extend=%v block=%v;", d.Extend >= 0, d.LBrace >= 0)
		case *ast.TypeDefinition:
			fmt.Fprintf(&b, "%s extend=%v implements=%v equals=%v block=%v;", d.Kind, d.Extend >= 0, d.Implements >= 0, d.Equals >= 0, d.LBrace >= 0)
		case *ast.DirectiveDefinition:
			fmt.Fprintf(&b, "directive repeatable=%v;", d.Repeatable >= 0)
		}
	}
	return b.String()
}
