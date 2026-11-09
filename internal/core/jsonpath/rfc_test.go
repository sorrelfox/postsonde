package jsonpath

import (
	"slices"
	"testing"
)

// Examples from RFC 9535, with the results and normalized paths the RFC
// gives. Where the RFC allows several orders (object members), the order
// here is the document's, which is what this package produces.

const bookstore = `{ "store": {
    "book": [
      { "category": "reference",
        "author": "Nigel Rees",
        "title": "Sayings of the Century",
        "price": 8.95
      },
      { "category": "fiction",
        "author": "Evelyn Waugh",
        "title": "Sword of Honour",
        "price": 12.99
      },
      { "category": "fiction",
        "author": "Herman Melville",
        "title": "Moby Dick",
        "isbn": "0-553-21311-3",
        "price": 8.99
      },
      { "category": "fiction",
        "author": "J. R. R. Tolkien",
        "title": "The Lord of the Rings",
        "isbn": "0-395-19395-8",
        "price": 22.99
      }
    ],
    "bicycle": {
      "color": "red",
      "price": 399
    }
  }
}`

type example struct {
	query string
	want  string   // JSON array of the selected values
	paths []string // nil: not checked
}

func runExamples(t *testing.T, doc string, examples []example) {
	t.Helper()
	d := mustDecode(t, doc)
	for _, ex := range examples {
		t.Run(ex.query, func(t *testing.T) {
			p, err := Parse(ex.query)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := p.Query(d)
			want := mustDecode(t, ex.want).([]any)
			if !(&evaluator{}).equal(want, got.Values()) {
				t.Errorf("values = %s, want %s", dump(got.Values()), ex.want)
			}
			if ex.paths != nil && !slices.Equal(got.Paths(), ex.paths) {
				t.Errorf("paths = %q, want %q", got.Paths(), ex.paths)
			}
		})
	}
}

func mustDecode(t testing.TB, s string) any {
	t.Helper()
	v, err := Decode([]byte(s))
	if err != nil {
		t.Fatalf("Decode(%s): %v", s, err)
	}
	return v
}

// RFC 9535 §1.5, Table 2.
func TestRFCOverviewExamples(t *testing.T) {
	book := func(i int) string { return `$['store']['book'][` + string(rune('0'+i)) + `]` }
	runExamples(t, bookstore, []example{
		{`$.store.book[*].author`, `["Nigel Rees","Evelyn Waugh","Herman Melville","J. R. R. Tolkien"]`,
			[]string{book(0) + `['author']`, book(1) + `['author']`, book(2) + `['author']`, book(3) + `['author']`}},
		{`$..author`, `["Nigel Rees","Evelyn Waugh","Herman Melville","J. R. R. Tolkien"]`, nil},
		{`$.store.*`, `[[{"category":"reference","author":"Nigel Rees","title":"Sayings of the Century","price":8.95},{"category":"fiction","author":"Evelyn Waugh","title":"Sword of Honour","price":12.99},{"category":"fiction","author":"Herman Melville","title":"Moby Dick","isbn":"0-553-21311-3","price":8.99},{"category":"fiction","author":"J. R. R. Tolkien","title":"The Lord of the Rings","isbn":"0-395-19395-8","price":22.99}],{"color":"red","price":399}]`,
			[]string{`$['store']['book']`, `$['store']['bicycle']`}},
		{`$.store..price`, `[8.95, 12.99, 8.99, 22.99, 399]`,
			[]string{book(0) + `['price']`, book(1) + `['price']`, book(2) + `['price']`, book(3) + `['price']`, `$['store']['bicycle']['price']`}},
		{`$..book[2]`, `[{"category":"fiction","author":"Herman Melville","title":"Moby Dick","isbn":"0-553-21311-3","price":8.99}]`, []string{book(2)}},
		{`$..book[2].author`, `["Herman Melville"]`, []string{book(2) + `['author']`}},
		{`$..book[2].publisher`, `[]`, []string{}},
		{`$..book[-1].title`, `["The Lord of the Rings"]`, []string{book(3) + `['title']`}},
		{`$..book[0,1].title`, `["Sayings of the Century","Sword of Honour"]`, nil},
		{`$..book[:2].title`, `["Sayings of the Century","Sword of Honour"]`, nil},
		{`$..book[?@.isbn].title`, `["Moby Dick","The Lord of the Rings"]`, nil},
		{`$..book[?@.price<10].title`, `["Sayings of the Century","Moby Dick"]`, []string{book(0) + `['title']`, book(2) + `['title']`}},
	})

	d := mustDecode(t, bookstore)
	if n := len(MustParse(`$..*`).Query(d)); n != 27 {
		t.Errorf("$..* selected %d nodes, want 27", n)
	}
}

// RFC 9535 §2.2.3, §2.3.1.3, §2.3.2.3, §2.3.3.3, §2.3.4.3.
func TestRFCSelectorExamples(t *testing.T) {
	runExamples(t, `{"k": "v"}`, []example{
		{`$`, `[{"k": "v"}]`, []string{`$`}},
	})
	runExamples(t, `{"o": {"j j": {"k.k": 3}}, "'": {"@": 2}}`, []example{
		{`$.o['j j']`, `[{"k.k": 3}]`, []string{`$['o']['j j']`}},
		{`$.o['j j']['k.k']`, `[3]`, []string{`$['o']['j j']['k.k']`}},
		{`$.o["j j"]["k.k"]`, `[3]`, []string{`$['o']['j j']['k.k']`}},
		{`$["'"]["@"]`, `[2]`, []string{`$['\'']['@']`}},
	})
	runExamples(t, `{"o": {"j": 1, "k": 2}, "a": [5, 3]}`, []example{
		{`$[*]`, `[{"j": 1, "k": 2}, [5, 3]]`, []string{`$['o']`, `$['a']`}},
		{`$.o[*]`, `[1, 2]`, []string{`$['o']['j']`, `$['o']['k']`}},
		{`$.o[*, *]`, `[1, 2, 1, 2]`, []string{`$['o']['j']`, `$['o']['k']`, `$['o']['j']`, `$['o']['k']`}},
		{`$.a[*]`, `[5, 3]`, []string{`$['a'][0]`, `$['a'][1]`}},
	})
	runExamples(t, `["a","b"]`, []example{
		{`$[1]`, `["b"]`, []string{`$[1]`}},
		{`$[-2]`, `["a"]`, []string{`$[0]`}},
	})
	runExamples(t, `["a", "b", "c", "d", "e", "f", "g"]`, []example{
		{`$[1:3]`, `["b", "c"]`, []string{`$[1]`, `$[2]`}},
		{`$[5:]`, `["f", "g"]`, []string{`$[5]`, `$[6]`}},
		{`$[1:5:2]`, `["b", "d"]`, []string{`$[1]`, `$[3]`}},
		{`$[5:1:-2]`, `["f", "d"]`, []string{`$[5]`, `$[3]`}},
		{`$[::-1]`, `["g", "f", "e", "d", "c", "b", "a"]`, nil},
		{`$[1:5:0]`, `[]`, nil},
		{`$[-100:100]`, `["a", "b", "c", "d", "e", "f", "g"]`, nil},
		{`$[100:-100:-3]`, `["g", "d", "a"]`, nil},
	})
}

// RFC 9535 §2.3.5.3, Table 12.
func TestRFCFilterExamples(t *testing.T) {
	doc := `{
  "a": [3, 5, 1, 2, 4, 6,
        {"b": "j"},
        {"b": "k"},
        {"b": {}},
        {"b": "kilo"}
       ],
  "o": {"p": 1, "q": 2, "r": 3, "s": 5, "t": {"u": 6}},
  "e": "f"
}`
	runExamples(t, doc, []example{
		{`$.a[?@.b == 'kilo']`, `[{"b": "kilo"}]`, []string{`$['a'][9]`}},
		{`$.a[?(@.b == 'kilo')]`, `[{"b": "kilo"}]`, []string{`$['a'][9]`}},
		{`$.a[?@>3.5]`, `[5, 4, 6]`, []string{`$['a'][1]`, `$['a'][4]`, `$['a'][5]`}},
		{`$.a[?@.b]`, `[{"b": "j"}, {"b": "k"}, {"b": {}}, {"b": "kilo"}]`, []string{`$['a'][6]`, `$['a'][7]`, `$['a'][8]`, `$['a'][9]`}},
		{`$[?@.*]`, `[[3, 5, 1, 2, 4, 6, {"b": "j"}, {"b": "k"}, {"b": {}}, {"b": "kilo"}], {"p": 1, "q": 2, "r": 3, "s": 5, "t": {"u": 6}}]`, []string{`$['a']`, `$['o']`}},
		{`$[?@[?@.b]]`, `[[3, 5, 1, 2, 4, 6, {"b": "j"}, {"b": "k"}, {"b": {}}, {"b": "kilo"}]]`, []string{`$['a']`}},
		{`$.o[?@<3, ?@<3]`, `[1, 2, 1, 2]`, []string{`$['o']['p']`, `$['o']['q']`, `$['o']['p']`, `$['o']['q']`}},
		{`$.a[?@<2 || @.b == "k"]`, `[1, {"b": "k"}]`, []string{`$['a'][2]`, `$['a'][7]`}},
		{`$.a[?match(@.b, "[jk]")]`, `[{"b": "j"}, {"b": "k"}]`, []string{`$['a'][6]`, `$['a'][7]`}},
		{`$.a[?search(@.b, "[jk]")]`, `[{"b": "j"}, {"b": "k"}, {"b": "kilo"}]`, []string{`$['a'][6]`, `$['a'][7]`, `$['a'][9]`}},
		{`$.o[?@>1 && @<4]`, `[2, 3]`, []string{`$['o']['q']`, `$['o']['r']`}},
		{`$.o[?@.u || @.x]`, `[{"u": 6}]`, []string{`$['o']['t']`}},
		{`$.a[?@.b == $.x]`, `[3, 5, 1, 2, 4, 6]`, nil},
		{`$.a[?@ == @]`, `[3, 5, 1, 2, 4, 6, {"b": "j"}, {"b": "k"}, {"b": {}}, {"b": "kilo"}]`, nil},
	})
}

// RFC 9535 §2.3.5.2.2, Table 11: each comparison is evaluated as a filter
// on an object with two members, so true selects both and false none.
func TestRFCComparisonExamples(t *testing.T) {
	d := mustDecode(t, `{"obj": {"x": "y"}, "arr": [2, 3]}`)
	for _, c := range []struct {
		expr string
		want bool
	}{
		{`$.absent1 == $.absent2`, true},
		{`$.absent1 <= $.absent2`, true},
		{`$.absent == 'g'`, false},
		{`$.absent1 != $.absent2`, false},
		{`$.absent != 'g'`, true},
		{`1 <= 2`, true},
		{`1 > 2`, false},
		{`13 == '13'`, false},
		{`'a' <= 'b'`, true},
		{`'a' > 'b'`, false},
		{`$.obj == $.arr`, false},
		{`$.obj != $.arr`, true},
		{`$.obj == $.obj`, true},
		{`$.obj != $.obj`, false},
		{`$.arr == $.arr`, true},
		{`$.arr != $.arr`, false},
		{`$.obj == 17`, false},
		{`$.obj != 17`, true},
		{`$.obj <= $.arr`, false},
		{`$.obj < $.arr`, false},
		{`$.obj <= $.obj`, true},
		{`$.arr <= $.arr`, true},
		{`1 <= $.arr`, false},
		{`1 >= $.arr`, false},
		{`1 > $.arr`, false},
		{`1 < $.arr`, false},
		{`true <= true`, true},
		{`true > true`, false},
	} {
		got := len(MustParse(`$[?`+c.expr+`]`).Query(d)) == 2
		if got != c.want {
			t.Errorf("%s = %v, want %v", c.expr, got, c.want)
		}
	}
}

// RFC 9535 §2.4.9, Table 14 (the examples with the standard functions).
func TestRFCWellTypedness(t *testing.T) {
	for _, c := range []struct {
		query string
		ok    bool
	}{
		{`$[?length(@) < 3]`, true},
		{`$[?length(@.*) < 3]`, false},
		{`$[?count(@.*) == 1]`, true},
		{`$[?count(1) == 1]`, false},
		{`$[?match(@.timezone, 'Europe/.*')]`, true},
		{`$[?match(@.timezone, 'Europe/.*') == true]`, false},
		{`$[?value(@..color) == "red"]`, true},
		{`$[?value(@..color)]`, false},
		{`$[?count(foo(@.*)) == 1]`, false}, // no function foo
	} {
		_, err := Parse(c.query)
		if (err == nil) != c.ok {
			t.Errorf("Parse(%s) error = %v, want ok=%v", c.query, err, c.ok)
		}
	}
}

// RFC 9535 §2.5.2.3.
func TestRFCDescendantExamples(t *testing.T) {
	runExamples(t, `{"o": {"j": 1, "k": 2}, "a": [5, 3, [{"j": 4}, {"k": 6}]]}`, []example{
		{`$..j`, `[1, 4]`, []string{`$['o']['j']`, `$['a'][2][0]['j']`}},
		{`$..[0]`, `[5, {"j": 4}]`, []string{`$['a'][0]`, `$['a'][2][0]`}},
		{`$..[*]`, `[{"j": 1, "k": 2}, [5, 3, [{"j": 4}, {"k": 6}]], 1, 2, 5, 3, [{"j": 4}, {"k": 6}], {"j": 4}, {"k": 6}, 4, 6]`,
			[]string{`$['o']`, `$['a']`, `$['o']['j']`, `$['o']['k']`, `$['a'][0]`, `$['a'][1]`, `$['a'][2]`, `$['a'][2][0]`, `$['a'][2][1]`, `$['a'][2][0]['j']`, `$['a'][2][1]['k']`}},
		{`$..*`, `[{"j": 1, "k": 2}, [5, 3, [{"j": 4}, {"k": 6}]], 1, 2, 5, 3, [{"j": 4}, {"k": 6}], {"j": 4}, {"k": 6}, 4, 6]`, nil},
		{`$..o`, `[{"j": 1, "k": 2}]`, []string{`$['o']`}},
		{`$.o..[*, *]`, `[1, 2, 1, 2]`, []string{`$['o']['j']`, `$['o']['k']`, `$['o']['j']`, `$['o']['k']`}},
		{`$.a..[0, 1]`, `[5, 3, {"j": 4}, {"k": 6}]`, []string{`$['a'][0]`, `$['a'][1]`, `$['a'][2][0]`, `$['a'][2][1]`}},
	})
}

// RFC 9535 §2.6.1.
func TestRFCNullExamples(t *testing.T) {
	runExamples(t, `{"a": null, "b": [null], "c": [{}], "null": 1}`, []example{
		{`$.a`, `[null]`, []string{`$['a']`}},
		{`$.a[0]`, `[]`, nil},
		{`$.a.d`, `[]`, nil},
		{`$.b[0]`, `[null]`, []string{`$['b'][0]`}},
		{`$.b[*]`, `[null]`, []string{`$['b'][0]`}},
		{`$.b[?@]`, `[null]`, []string{`$['b'][0]`}},
		{`$.b[?@==null]`, `[null]`, []string{`$['b'][0]`}},
		{`$.c[?@.d==null]`, `[]`, nil},
		{`$.null`, `[1]`, []string{`$['null']`}},
	})
}

// RFC 9535 §2.7.1, Table 18.
func TestNormalizedPaths(t *testing.T) {
	for _, c := range []struct {
		path NormalizedPath
		want string
	}{
		{NormalizedPath{}, `$`},
		{NormalizedPath{{Name: "a"}}, `$['a']`},
		{NormalizedPath{{Index: 1, IsIndex: true}}, `$[1]`},
		{NormalizedPath{{Name: "a"}, {Index: 0, IsIndex: true}, {Name: "b"}}, `$['a'][0]['b']`},
		{NormalizedPath{{Name: "\u000b"}}, "$['\\u000b']"},
		{NormalizedPath{{Name: "\u0000\u001f"}}, "$['\\u0000\\u001f']"},
		{NormalizedPath{{Name: "\b\f\n\r\t"}}, `$['\b\f\n\r\t']`},
		{NormalizedPath{{Name: "'"}}, `$['\'']`},
		{NormalizedPath{{Name: `\`}}, `$['\\']`},
		{NormalizedPath{{Name: `"/` + "\u007f\u00e9\U0001F600"}}, `$['"/` + "\u007f\u00e9\U0001F600" + `']`},
	} {
		if got := c.path.String(); got != c.want {
			t.Errorf("String() = %s, want %s", got, c.want)
		}
	}

	// A normalized path is itself a query selecting that node.
	d := mustDecode(t, `{"a\u000bb": [{"'\\": 1}]}`)
	got := MustParse(`$..*`).Query(d)
	for _, n := range got {
		again := MustParse(n.Path.String()).Query(d)
		if len(again) != 1 || again[0].Path.String() != n.Path.String() {
			t.Errorf("%s selects %q", n.Path, again.Paths())
		}
	}
}
