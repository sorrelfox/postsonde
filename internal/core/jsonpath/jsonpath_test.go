package jsonpath

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func TestParseErrors(t *testing.T) {
	for _, c := range []struct {
		query    string
		offset   int
		offset16 int
		msg      string
	}{
		{``, 0, 0, "a query must start with '$'"},
		{` $`, 0, 0, "a query must start with '$'"},
		{`$ `, 1, 1, "unexpected whitespace: expected '.', '..' or '['"},
		{`$.store[`, 8, 8, "unexpected end of query: expected a selector"},
		{`$.store[]`, 8, 8, "unexpected ']': expected a selector"},
		{`$[1 2]`, 4, 4, "unexpected '2': expected ',' or ']'"},
		{`$.1`, 2, 2, "unexpected '1': expected a member name or '*' after '.'"},
		{`$[01]`, 2, 2, "an integer must not have leading zeros"},
		{`$[-0]`, 2, 2, "-0 is not a valid index"},
		{`$[9007199254740992]`, 2, 2, "the integer 9007199254740992 is out of range"},
		{`$['abc`, 2, 2, "unterminated string literal"},
		{`$['a\"']`, 4, 4, `invalid escape \" (only the enclosing quote can be escaped)`},
		{`$["\q"]`, 3, 3, "invalid escape sequence"},
		{"$.éé[", 7, 5, "unexpected end of query"},
		{`$[?@.a]]`, 7, 7, "unexpected ']'"},
		{`$[?1]`, 3, 3, "a literal cannot be used as a test"},
		{`$[?@.* == 1]`, 3, 3, "only a singular query"},
		{`$[?length(@) ]`, 3, 3, "the result of length() is a value and must be compared"},
		{`$[?match(@, 'a') == true]`, 3, 3, "the result of match() is LogicalType and cannot be compared"},
		{`$[?count(@) == 1 && 1]`, 20, 20, "a literal cannot be used as a test"},
		{`$[?foo(@)]`, 3, 3, "unknown function foo()"},
		{`$[?length(@, 1) == 1]`, 13, 13, "length() takes 1 argument"},
		{`$[?match(@) == 1]`, 3, 3, "match() takes 2 arguments"},
		{`$[?count(@.a == 1) == 1]`, 9, 9, "count() needs a query here"},
		{`$[?length(@.*) == 1]`, 10, 10, "length() needs a value here; the query is not singular"},
		{`$[?@.a == True]`, 10, 10, "unexpected 'T': expected a literal"},
		{`$[?@.a == nul]`, 10, 10, `unknown name "nul"`},
		{`$[?@.a == 1.]`, 12, 12, "unexpected ']': expected a digit after '.'"},
		{`$[?!1]`, 4, 4, "'!' must be followed by a query, a function or '('"},
		{`$[?(@.a]`, 7, 7, "unexpected ']': expected ')'"},
		{`$[?` + strings.Repeat("(", MaxNesting+1) + `@` + strings.Repeat(")", MaxNesting+1) + `]`, 2 + MaxNesting, 2 + MaxNesting, "nested too deeply"},
	} {
		_, err := Parse(c.query)
		var se *SyntaxError
		if !errors.As(err, &se) {
			t.Errorf("Parse(%q) = %v, want a *SyntaxError", c.query, err)
			continue
		}
		if se.Offset != c.offset || se.Offset16 != c.offset16 || !strings.Contains(se.Message, c.msg) {
			t.Errorf("Parse(%q) = %q at %d/%d, want %q at %d/%d", c.query, se.Message, se.Offset, se.Offset16, c.msg, c.offset, c.offset16)
		}
	}
}

func TestSyntaxErrorErr(t *testing.T) {
	_, err := Parse(`$[`)
	se := err.(*SyntaxError)
	if got := se.Error(); got != "jsonpath: unexpected end of query: expected a selector at 2" {
		t.Errorf("Error() = %q", got)
	}
	var e *errs.Error
	if !errors.As(se.Err(), &e) || e.Kind != errs.InvalidArgument || e.Code != CodeInvalidQuery || e.Details["offset"] != "2" {
		t.Errorf("Err() = %#v", se.Err())
	}
}

func TestValidQueries(t *testing.T) {
	for _, q := range []string{
		`$`,
		`$ .a`,
		"$\t[ 'a' , \"b\" ]\n.c",
		`$..*`,
		`$..[0]`,
		`$['\b\f\n\r\t\/\\\'']`,
		"$[\"\\ud83d\\ude00\"]",
		`$[::]`,
		`$[ 1 : 2 : 3 ]`,
		`$[-9007199254740991:9007199254740991:-9007199254740991]`,
		`$[?@.a==-0]`,
		`$[?@.a==1.5e-3 && @.b!=1E+3]`,
		`$[? ! ( @.a ) ]`,
		`$[?(@.a || @.b) && !@.c]`,
		`$[?@[ 'a' ] == 1]`, // blank space inside a singular query's brackets: accepted
		`$[?value(@..x) == length(value($.y))]`,
		`$[?count(@..*) > 2 && match(@.name, "[a-z]+") || search($.q, @.p)]`,
		`$.ünïcödé_1`,
	} {
		if _, err := Parse(q); err != nil {
			t.Errorf("Parse(%q): %v", q, err)
		}
	}
}

func TestSingular(t *testing.T) {
	for q, want := range map[string]bool{
		`$`: true, `$.a[0]['b'][-1]`: true, `$.a.*`: false, `$..a`: false, `$[0,1]`: false, `$[0:1]`: false, `$[?@]`: false,
	} {
		if got := MustParse(q).Singular(); got != want {
			t.Errorf("%s Singular() = %v", q, got)
		}
	}
}

func TestDecimalCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1", "1.0", 0},
		{"1", "10e-1", 0},
		{"0.1e1", "1", 0},
		{"-0", "0", 0},
		{"0.000", "-0e5", 0},
		{"12345678901234567890", "12345678901234567891", -1},
		{"9007199254740993", "9007199254740992", 1},
		{"0.30000000000000001", "0.3", 1},
		{"1e400", "1e399", 1},
		{"-1e400", "-1e399", -1},
		{"1e-400", "0", 1},
		{"-2", "1", -1},
		{"100", "99.999", 1},
		{"1e99999999999999999999", "1e99999999999999999998", 0}, // saturated
		{"1.5", "15e-1", 0},
	} {
		da, ok1 := parseDecimal(c.a)
		db, ok2 := parseDecimal(c.b)
		if !ok1 || !ok2 {
			t.Fatalf("parse %s / %s failed", c.a, c.b)
		}
		if got := da.cmp(db); got != c.want {
			t.Errorf("cmp(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	for _, bad := range []string{"", "-", "01", "1.", ".5", "1e", "+1", "1e+", "0x1", "1 "} {
		if _, ok := parseDecimal(bad); ok {
			t.Errorf("parseDecimal(%q) ok", bad)
		}
	}
}

func TestExactNumbers(t *testing.T) {
	d := mustDecode(t, `[12345678901234567890, 12345678901234567891, 1.0, 0.1]`)
	got := MustParse(`$[?@ > 12345678901234567890]`).Query(d)
	if len(got) != 1 || got[0].Value != json.Number("12345678901234567891") {
		t.Errorf("got %s", dump(got.Values()))
	}
	got = MustParse(`$[?@ == 1]`).Query(d)
	if len(got) != 1 || got[0].Value != json.Number("1.0") {
		t.Errorf("got %s", dump(got.Values()))
	}
}

func TestDecode(t *testing.T) {
	v := mustDecode(t, `{"z": 1, "a": [1.50e+3, true, null, "x"], "m": {}, "z": 2}`)
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"z":2,"a":[1.50e+3,true,null,"x"],"m":{}}`; string(b) != want {
		t.Errorf("Marshal = %s, want %s", b, want)
	}

	big := NewObject(0)
	for i := range 20 {
		big.Set(string(rune('a'+i)), i)
	}
	big.Set("c", "again")
	if v, _ := big.Get("c"); v != "again" || big.Len() != 20 || big.Names()[2] != "c" {
		t.Errorf("indexed object: %v %d %v", v, big.Len(), big.Names())
	}

	for _, bad := range []string{``, `{`, `[1,]`, `{"a" 1}`, `1 2`, `[] x`, `nul`} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Errorf("Decode(%q) succeeded", bad)
		}
	}
	if _, err := Decode([]byte(strings.Repeat("[", MaxDecodeDepth+1) + strings.Repeat("]", MaxDecodeDepth+1))); err == nil {
		t.Error("deep document decoded")
	}
}

// Plain Go values work too; map members are visited in name order.
func TestGoValues(t *testing.T) {
	doc := map[string]any{
		"b": []any{1, 2.5, int64(3), uint8(4), float32(0.5)},
		"a": map[string]any{"x": "y"},
	}
	if got := MustParse(`$.*`).Query(doc).Paths(); strings.Join(got, " ") != `$['a'] $['b']` {
		t.Errorf("paths = %q", got)
	}
	if got := MustParse(`$.b[?@ >= 2.5]`).Query(doc); len(got) != 3 {
		t.Errorf("got %v", got.Values())
	}
	if got := MustParse(`$[?@.x == 'y']`).Query(doc); len(got) != 1 {
		t.Errorf("got %v", got.Values())
	}
	if got := MustParse(`$[?length(@) == 5]`).Query(doc); len(got) != 1 {
		t.Errorf("got %v", got.Values())
	}
}

func TestFunctions(t *testing.T) {
	runExamples(t, `{"s": ["", "ab", "é😀", 1, true, null, [1, 2], {"a": 1}],
		"n": [{"k": [1, 2, 3]}, {"k": []}, {"k": {"x": 1}}],
		"re": "a.c"}`, []example{
		{`$.s[?length(@) == 2]`, `["ab", "é😀", [1, 2]]`, nil},
		{`$.s[?length(@) == 1]`, `[{"a": 1}]`, nil},
		{`$.s[?length(@) == 0]`, `[""]`, nil},
		{`$.n[?count(@.k[*]) == 3]`, `[{"k": [1, 2, 3]}]`, nil},
		{`$.n[?count(@..*) == 2]`, `[{"k": {"x": 1}}]`, nil},
		{`$.n[?value(@.k.x) == 1]`, `[{"k": {"x": 1}}]`, nil},
		{`$.n[?value(@.k[*]) == 1]`, `[{"k": {"x": 1}}]`, nil},
		{`$.n[?value(@.k[*]) == $.absent]`, `[{"k": [1, 2, 3]}, {"k": []}]`, nil}, // 3 or 0 nodes: Nothing
		{`$.s[?match(@, $.re)]`, `[]`, nil},
		{`$.s[?search(@, 'b$')]`, `["ab"]`, nil},
		{`$.s[?match(@, 'a(')]`, `[]`, nil}, // invalid pattern: false
		{`$.s[?!match(@, 'a(')]`, `["", "ab", "é😀", 1, true, null, [1, 2], {"a": 1}]`, nil},
	})
}

func TestIRegexp(t *testing.T) {
	for _, c := range []struct {
		pat     string
		valid   bool
		matches []string
		rejects []string
	}{
		{`a.c`, true, []string{"abc", "a c"}, []string{"a\nc", "a\rc", "abcd"}},
		{`[a-c]+`, true, []string{"abcba"}, []string{"abd", ""}},
		{`[^a-c]`, true, []string{"d", "\n"}, []string{"a"}},
		{`[-a]`, true, []string{"-", "a"}, []string{"b"}},
		{`[a-]`, true, []string{"-", "a"}, []string{"b"}},
		{`[\-\[\]]`, true, []string{"-", "[", "]"}, []string{"a"}},
		{`\p{Lu}\P{Lu}`, true, []string{"Ab"}, []string{"AB"}},
		{`[\p{Nd}x]{2,3}`, true, []string{"1x", "x12"}, []string{"1", "1234"}},
		{`\p{Cn}`, true, []string{"\U000E0080"}, []string{"a", "͸a"}},
		{`\P{Cn}`, true, []string{"a"}, []string{"\U000E0080"}},
		{`\p{C}`, true, []string{"\x01", "\U000E0080"}, []string{"a"}},
		{`\P{C}`, true, []string{"a"}, []string{"\x01", "\U000E0080"}},
		{`(ab|cd){2}`, true, []string{"abcd"}, []string{"ab"}},
		{`a{0}b{1,}c{,}`, false, nil, nil},
		{`a{3,2}`, false, nil, nil},
		{`a{2,}`, true, []string{"aaa"}, []string{"a"}},
		{`\n\r\t\.\\\(\)\*\+\?\{\}\|\^`, true, []string{"\n\r\t.\\()*+?{}|^"}, nil},
		{`^ab$`, true, []string{"ab"}, []string{"xab"}},
		{`\d`, false, nil, nil},
		{`\w`, false, nil, nil},
		{`\s`, false, nil, nil},
		{`\1`, false, nil, nil},
		{`(?:a)`, false, nil, nil},
		{`a*?`, false, nil, nil},
		{`a**`, false, nil, nil},
		{`*a`, false, nil, nil},
		{`[]`, false, nil, nil},
		{`[^]`, false, nil, nil},
		{`[z-a]`, false, nil, nil},
		{`[a-c-e]`, false, nil, nil},
		{`[[]`, false, nil, nil},
		{`(a`, false, nil, nil},
		{`a)`, false, nil, nil},
		{`a]`, false, nil, nil},
		{`\p{IsBasicLatin}`, false, nil, nil},
		{`\p{Cs}`, false, nil, nil},
		{`a{1001}`, false, nil, nil}, // over RE2's repeat limit
		{strings.Repeat("a", MaxRegexpLength+1), false, nil, nil},
	} {
		re, err := compileIRegexp(c.pat, true)
		if (err == nil) != c.valid {
			t.Errorf("compileIRegexp(%q) error = %v, want valid=%v", c.pat, err, c.valid)
			continue
		}
		for _, s := range c.matches {
			if !re.MatchString(s) {
				t.Errorf("%q does not match %q", c.pat, s)
			}
		}
		for _, s := range c.rejects {
			if re.MatchString(s) {
				t.Errorf("%q matches %q", c.pat, s)
			}
		}
	}
}

func TestLimits(t *testing.T) {
	// A wide, deep document and a query whose work grows with every '..'.
	var b strings.Builder
	for range 30 {
		b.WriteString(`{"a": [1, 2, 3, `)
	}
	b.WriteString("0")
	for range 30 {
		b.WriteString("]}")
	}
	doc := mustDecode(t, b.String())
	p := MustParse(`$..*..*..*..*..*`)
	_, err := p.Eval(context.Background(), doc, Limits{MaxSteps: 10_000})
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.Unsupported || e.Code != CodeTooComplex {
		t.Fatalf("Eval = %v, want too_complex", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.Eval(ctx, doc, Limits{})
	if errs.KindOf(err) != errs.Cancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("Eval with a cancelled context = %v", err)
	}

	if got, err := MustParse(`$..a`).Eval(context.Background(), doc, DefaultLimits); err != nil || len(got) != 30 {
		t.Fatalf("Eval = %d nodes, %v", len(got), err)
	}
}

func TestQueryJSON(t *testing.T) {
	got, err := QueryJSON(`$.a[1]`, []byte(`{"a": [1, 2.50]}`))
	if err != nil || len(got) != 1 || got[0].Value != json.Number("2.50") || got[0].Path.String() != `$['a'][1]` {
		t.Errorf("QueryJSON = %v, %v", got, err)
	}
	for _, c := range []struct {
		expr, doc string
		kind      errs.Kind
		code      string
	}{
		{`$[`, `{}`, errs.InvalidArgument, CodeInvalidQuery},
		{`$`, `{`, errs.Parse, CodeInvalidJSON},
	} {
		_, err := QueryJSON(c.expr, []byte(c.doc))
		var e *errs.Error
		if !errors.As(err, &e) || e.Kind != c.kind || e.Code != c.code {
			t.Errorf("QueryJSON(%q, %q) = %v", c.expr, c.doc, err)
		}
	}
}
