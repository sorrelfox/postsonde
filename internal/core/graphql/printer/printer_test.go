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

func TestPrettify(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, src, want string }{
		{"shorthand", `{ user(id: 4) { name } }`, "{\n  user(id: 4) {\n    name\n  }\n}"},
		{
			"operations and fragments",
			`query Q($a:Int=1,$b:[String!]!@d){a:f(x:$a,y:{k:[1,2],o:{}})@include(if:true){...F ... on T{b} ...@skip(if:$b){c}}} fragment F on T@d(x:"s"){ d }`,
			`query Q($a: Int = 1, $b: [String!]! @d) {
  a: f(x: $a, y: {k: [1, 2], o: {}}) @include(if: true) {
    ...F
    ... on T {
      b
    }
    ... @skip(if: $b) {
      c
    }
  }
}

fragment F on T @d(x: "s") {
  d
}`,
		},
		{"anonymous with variables", `query($a: Int){a}`, "query ($a: Int) {\n  a\n}"},
		{"keyword without name", `mutation{a}subscription{b}`, "mutation {\n  a\n}\n\nsubscription {\n  b\n}"},
		{
			"long arguments break",
			`{ search(text: "a fairly long search text", first: 100, after: "cursor-value", orderBy: {field: NAME}) { id } }`,
			`{
  search(
    text: "a fairly long search text"
    first: 100
    after: "cursor-value"
    orderBy: {field: NAME}
  ) {
    id
  }
}`,
		},
		{
			"literals as written",
			"{ f(s: \"a\\u00e9\\n\", n: -1.50e+3, b: \"\"\"\n    block\n      text\n  \"\"\") }",
			"{\n  f(s: \"a\\u00e9\\n\", n: -1.50e+3, b: \"\"\"\n    block\n      text\n  \"\"\")\n}",
		},
		{
			"comments",
			`# leading
# second line
query Q( # after paren
  $a: Int # after a
  $b: Int
) { # after brace
  a # after a
  # before b
  b(x: 1)

  # before closing brace
}
# between definitions
fragment F on T { c } # after F
# at the end`,
			`# leading
# second line
query Q( # after paren
  $a: Int # after a
  $b: Int
) { # after brace
  a # after a
  # before b
  b(x: 1)
  # before closing brace
}

# between definitions
fragment F on T {
  c
} # after F
# at the end`,
		},
		{
			"comments inside values",
			"{ f(o: {a: 1 # one\n b: [2, # two\n 3]}) }",
			`{
  f(
    o: {
      a: 1 # one
      b: [
        2 # two
        3
      ]
    }
  )
}`,
		},
		{
			"comment inside a field",
			"query # q\nQ { a # x\n(y: 1) }",
			"query # q\n  Q {\n  a # x\n    (y: 1)\n}",
		},
		{"ignored tokens and BOM", "\uFEFF# c  \t\n{,,a,,b # t\r\n,c\r}\n", "# c\n{\n  a\n  b # t\n  c\n}"},
		{"only comments", "# a\n\n   # b\n", "# a\n# b"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, errs := Prettify(tt.src)
			if errs != nil {
				t.Fatalf("Prettify errors: %v", errs)
			}
			if got != tt.want {
				t.Errorf("Prettify(%q) =\n%s\nwant\n%s", tt.src, got, tt.want)
			}
			checkRoundTrip(t, tt.src)
		})
	}
}

func TestMinify(t *testing.T) {
	t.Parallel()

	tests := []struct{ src, want string }{
		{"query Q($a: Int = -1 @d) {\n  a: f(x: $a) { ...F ... on T { b } }\n}\n\nfragment F on T { c }",
			`query Q($a:Int=-1@d){a:f(x:$a){...F ...on T{b}}}fragment F on T{c}`},
		{`{ f(a: 1, b: -2, c: [1 -2 A], d: ["" "x"], e: 1.5, g: {h: "" i: ""}) }`,
			`{f(a:1,b:-2,c:[1,-2,A],d:["","x"],e:1.5,g:{h:"",i:""})}`},
		{"# gone\n{ a # gone\n b }", `{a b}`},
		{`{ ... @include(if: true) { a } ... { b } }`, `{...@include(if:true){a}...{b}}`},
	}
	for _, tt := range tests {
		got, errs := Minify(tt.src)
		if errs != nil {
			t.Fatalf("Minify(%q) errors: %v", tt.src, errs)
		}
		if got != tt.want {
			t.Errorf("Minify(%q) =\n%s\nwant\n%s", tt.src, got, tt.want)
		}
		checkRoundTrip(t, tt.src)
	}
}

func TestSyntaxErrors(t *testing.T) {
	t.Parallel()

	for _, src := range []string{"{ a(", "type T { a: Int }", "{ a(x: $) }"} {
		for name, f := range map[string]func(string) (string, []parser.Error){"Prettify": Prettify, "Minify": Minify} {
			if out, errs := f(src); out != "" || len(errs) == 0 {
				t.Errorf("%s(%q) = %q, %v; want the syntax errors", name, src, out, errs)
			}
		}
	}
}

// corpus is the parser's valid documents (the sources of its golden files)
// plus documents with comments in every position.
func corpus(t testing.TB) []string {
	files, err := filepath.Glob("../parser/testdata/valid/*.golden")
	if err != nil || len(files) == 0 {
		t.Fatalf("no parser corpus: %v", err)
	}
	var docs []string
	unescape := strings.NewReplacer(`\r`, "\r", `\uFEFF`, "\uFEFF")
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		start, end := strings.Index(s, "# source\n"), strings.Index(s, "\n# tree\n")
		if start != 0 || end < 0 {
			t.Fatalf("%s: no source section", f)
		}
		docs = append(docs, unescape.Replace(s[len("# source\n"):end]))
	}
	return append(docs,
		"#a\n{#b\na#c\n(#d\nx#e\n:#f\n1#g\n)#h\n@#i\nd#j\n{#k\nb#l\n}#m\n}#n\n#o",
		"query#a\nQ#b\n(#c\n$#d\nv#e\n:#f\n[#g\nInt#h\n!#i\n]#j\n=#k\n[#l\n]#m\n)#n\n{#o\n...#p\nF#q\n...#r\non#s\nT#t\n{#u\na#v\n}#w\n}",
		"fragment #a\n  query on #b\n  T { query: query(query: query # c\n) }",
		"query A { a } # 1\n\n\n# 2\n\n# 3\nquery B { b }",
		"{ f(o: {a: {b: [[ # deep\n]]}}) }",
		"{ f(s: \"\"\"\n  # not a comment\n\"\"\" # a comment\n) }",
		"{ a(x: 1, y: 2, z: 3, text: \"a long string that pushes this line past the limit\") @d(x: \"another long string\") }",
	)
}

func TestCorpusRoundTrip(t *testing.T) {
	t.Parallel()

	for _, src := range corpus(t) {
		checkRoundTrip(t, src)
	}
}

// FuzzPrint checks, for every input that parses without errors: Prettify is
// idempotent and keeps the comments in order; the prettified and the
// minified documents parse without errors to the same tree as the input;
// Minify is idempotent; and minifying the prettified document gives the
// minified input. Run it with
// go test ./internal/core/graphql/printer -run '^$' -fuzz FuzzPrint -fuzztime 60s
func FuzzPrint(f *testing.F) {
	for _, src := range corpus(f) {
		f.Add(src)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if _, errs := parser.Parse(src); len(errs) > 0 {
			return
		}
		checkRoundTrip(t, src)
	})
}

func checkRoundTrip(t *testing.T, src string) {
	t.Helper()
	doc, errs := parser.Parse(src)
	if len(errs) > 0 {
		t.Fatalf("corpus document %q has errors: %v", src, errs)
	}
	want := shape(doc)

	pretty := Print(doc)
	prettyDoc, errs := parser.Parse(pretty)
	if len(errs) > 0 {
		t.Fatalf("Prettify(%q) = %q, which has errors: %v", src, pretty, errs)
	}
	if got := shape(prettyDoc); got != want {
		t.Fatalf("Prettify(%q) = %q changes the tree:\n%s\nwant\n%s", src, pretty, got, want)
	}
	if again := Print(prettyDoc); again != pretty {
		t.Fatalf("Prettify is not idempotent on %q:\n%s\nthen\n%s", src, pretty, again)
	}
	if got, want := commentTexts(prettyDoc), commentTexts(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("Prettify(%q) = %q comments %q, want %q", src, pretty, got, want)
	}

	small := PrintMinified(doc)
	smallDoc, errs := parser.Parse(small)
	if len(errs) > 0 {
		t.Fatalf("Minify(%q) = %q, which has errors: %v", src, small, errs)
	}
	if got := shape(smallDoc); got != want {
		t.Fatalf("Minify(%q) = %q changes the tree:\n%s\nwant\n%s", src, small, got, want)
	}
	if len(smallDoc.Comments) > 0 {
		t.Fatalf("Minify(%q) = %q keeps comments", src, small)
	}
	if again := PrintMinified(smallDoc); again != small {
		t.Fatalf("Minify is not idempotent on %q: %q then %q", src, small, again)
	}
	if fromPretty := PrintMinified(prettyDoc); fromPretty != small {
		t.Fatalf("Minify(Prettify(%q)) = %q, want %q", src, fromPretty, small)
	}
}

func commentTexts(doc *ast.Document) []string {
	var texts []string
	for _, c := range doc.Comments {
		texts = append(texts, strings.TrimRight(c.Text, " \t"))
	}
	return texts
}

var locType = reflect.TypeFor[ast.Loc]()

// shape renders a tree without positions: Loc and the delimiter offsets
// (every int field of the ast types) are left out, as are the source and
// the comments.
func shape(n ast.Node) string {
	var b strings.Builder
	writeShape(&b, reflect.ValueOf(n))
	return b.String()
}

func writeShape(b *strings.Builder, v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		writeShape(b, v.Elem())
	case reflect.Struct:
		b.WriteString(v.Type().Name() + "{")
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if !f.IsExported() || f.Type == locType || f.Type.Kind() == reflect.Int ||
				f.Name == "Source" || f.Name == "Comments" {
				continue
			}
			b.WriteString(f.Name + ":")
			writeShape(b, v.Field(i))
			b.WriteString(" ")
		}
		b.WriteString("}")
	case reflect.Slice:
		b.WriteString("[")
		for i := range v.Len() {
			writeShape(b, v.Index(i))
			b.WriteString(" ")
		}
		b.WriteString("]")
	case reflect.String:
		fmt.Fprintf(b, "%q", v.String())
	default:
		fmt.Fprintf(b, "%v", v.Interface())
	}
}
