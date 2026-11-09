package xpath

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/xmldom"
)

func TestOperatorsAndFunctions(t *testing.T) {
	doc := mustDoc(t, `<r xml:lang="en-GB"><a>1</a><b>x</b><b>y</b><n>5</n><n>7</n><m>6</m><e/><s>  a
 b  </s></r>`)
	runCases(t, doc, Options{}, []queryCase{
		// Arithmetic.
		{`1 + 2 * 3`, val("number:7")},
		{`(1 + 2) * 3`, val("number:9")},
		{`7 mod 3`, val("number:1")},
		{`-7 mod 3`, val("number:-1")},
		{`7 mod -3`, val("number:1")},
		{`7 div 2`, val("number:3.5")},
		{`1 div 0`, val("number:Infinity")},
		{`-1 div 0`, val("number:-Infinity")},
		{`0 div 0`, val("number:NaN")},
		{`--3`, val("number:3")},
		{`3 - -2`, val("number:5")},
		{`2*-1`, val("number:-2")},
		{`r/n*2`, val("number:10")},
		{`0.1 + 0.2`, val("number:0.30000000000000004")},
		{`1000000 * 1000000 * 1000000 * 1000`, val("number:1000000000000000000000")},
		{`-0`, val("number:0")},
		{`.5 + 5.`, val("number:5.5")},
		// Comparisons of scalars (§3.4).
		{`1 = 1.0`, val("boolean:true")},
		{`'1' = 1`, val("boolean:true")},
		{`'1.0' = '1'`, val("boolean:false")},
		{`true() = 'x'`, val("boolean:true")},
		{`false() = ''`, val("boolean:true")},
		{`1 < '2'`, val("boolean:true")},
		{`'10' < '9'`, val("boolean:false")},
		{`true() > false()`, val("boolean:true")},
		{`0 div 0 = 0 div 0`, val("boolean:false")},
		{`0 div 0 != 0 div 0`, val("boolean:true")},
		{`1 < 2 < 3`, val("boolean:true")},
		{`3 > 2 > 1`, val("boolean:false")},
		// Node-sets against scalars and node-sets.
		{`/r/b = 'x'`, val("boolean:true")},
		{`/r/b != 'x'`, val("boolean:true")},
		{`/r/a != '1'`, val("boolean:false")},
		{`/r/e = ''`, val("boolean:true")},
		{`/r/missing = ''`, val("boolean:false")},
		{`/r/missing != ''`, val("boolean:false")},
		{`/r/missing = false()`, val("boolean:true")},
		{`/r/a = true()`, val("boolean:true")},
		{`/r/n = 7`, val("boolean:true")},
		{`7 = /r/n`, val("boolean:true")},
		{`/r/n < /r/m`, val("boolean:true")},
		{`/r/n > /r/m`, val("boolean:true")},
		{`/r/n >= 8`, val("boolean:false")},
		{`8 > /r/n`, val("boolean:true")},
		{`/r/n = /r/m`, val("boolean:false")},
		{`/r/n != /r/n`, val("boolean:true")},
		{`/r/a != /r/a`, val("boolean:false")},
		{`/r/n <= '5'`, val("boolean:true")},
		// Boolean operators.
		{`true() and false() or true()`, val("boolean:true")},
		{`true() or 1 div 0`, val("boolean:true")},
		{`not(0)`, val("boolean:true")},
		{`not(/r/missing)`, val("boolean:true")},
		{`boolean('false')`, val("boolean:true")},
		{`boolean(0 div 0)`, val("boolean:false")},
		{`boolean(-0)`, val("boolean:false")},
		// Conversions.
		{`number('  -12.5 ')`, val("number:-12.5")},
		{`number('1e3')`, val("number:NaN")},
		{`number('+1')`, val("number:NaN")},
		{`number('.5')`, val("number:0.5")},
		{`number('5.')`, val("number:5")},
		{`number('.')`, val("number:NaN")},
		{`number('')`, val("number:NaN")},
		{`number(true())`, val("number:1")},
		{`number(/r/a)`, val("number:1")},
		{`string(1 div 0)`, val("string:Infinity")},
		{`string(-0.0)`, val("string:0")},
		{`string(12.50)`, val("string:12.5")},
		{`string(true())`, val("string:true")},
		{`string(/r/b)`, val("string:x")},
		{`string(/r/missing)`, val("string:")},
		// String functions.
		{`concat('a', 1, true(), /r/b)`, val("string:a1truex")},
		{`starts-with('abc', '')`, val("boolean:true")},
		{`contains('abc', 'bc')`, val("boolean:true")},
		{`contains('abc', 'x')`, val("boolean:false")},
		{`substring('12345', 2, 3)`, val("string:234")},
		{`substring('12345', 2)`, val("string:2345")},
		{`substring('12345', 1.5, 2.6)`, val("string:234")},
		{`substring('12345', 0, 3)`, val("string:12")},
		{`substring('12345', 0 div 0, 3)`, val("string:")},
		{`substring('12345', 1, 0 div 0)`, val("string:")},
		{`substring('12345', -42, 1 div 0)`, val("string:12345")},
		{`substring('12345', -1 div 0, 1 div 0)`, val("string:")},
		{`substring('héllo', 2, 2)`, val("string:él")},
		{`substring-before('1999/04/01', '/')`, val("string:1999")},
		{`substring-after('1999/04/01', '/')`, val("string:04/01")},
		{`substring-after('abc', 'x')`, val("string:")},
		{`substring-before('abc', '')`, val("string:")},
		{`string-length('héllo')`, val("number:5")},
		{`translate('bar', 'abc', 'ABC')`, val("string:BAr")},
		{`translate('--aaa--', 'abc-', 'ABC')`, val("string:AAA")},
		{`translate('aa', 'aa', 'xy')`, val("string:xx")},
		{`normalize-space('  a` + "\t\n" + ` b  ')`, val("string:a b")},
		{`normalize-space(/r/s)`, val("string:a b")},
		{`/r/s[normalize-space() = 'a b']`, ns("/r[1]/s[1]")},
		{`/r/b[string-length() = 1][2]`, ns("/r[1]/b[2]")},
		// Number functions.
		{`round(2.5)`, val("number:3")},
		{`round(-2.5)`, val("number:-2")},
		{`round(-0.4)`, val("number:0")},
		{`1 div round(-0.4)`, val("number:-Infinity")},
		{`round(0.49999999999999994)`, val("number:0")},
		{`round(1 div 0)`, val("number:Infinity")},
		{`floor(-1.5)`, val("number:-2")},
		{`ceiling(1.1)`, val("number:2")},
		{`sum(/r/n)`, val("number:12")},
		{`sum(/r/missing)`, val("number:0")},
		// Node-set functions.
		{`last()`, val("number:1")},
		{`position()`, val("number:1")},
		{`count(/r/*)`, val("number:8")},
		{`count(/)`, val("number:1")},
		{`name(/r/n[2])`, val("string:n")},
		{`name()`, val("string:")},
		{`local-name(/r/missing)`, val("string:")},
		{`name(/r/@xml:lang)`, val("string:xml:lang")},
		{`namespace-uri(/r/@xml:lang)`, val("string:http://www.w3.org/XML/1998/namespace")},
		{`local-name(/r/b/text())`, val("string:")},
		{`/r/*[lang('en')][1]`, ns("/r[1]/a[1]")},
		{`/r[lang('fr')]`, nil},
		{`/r/*[position() = last()]`, ns("/r[1]/s[1]")},
		{`/r/*[last()][1]`, ns("/r[1]/s[1]")},
		{`/r/n[position() mod 2 = 0]`, ns("/r[1]/n[2]")},
		{`/r/*[1.0]`, ns("/r[1]/a[1]")},
		{`/r/*[1.5]`, nil},
		{`/r/*['1']`, ns("/r[1]/a[1]", "/r[1]/b[1]", "/r[1]/b[2]", "/r[1]/n[1]", "/r[1]/n[2]", "/r[1]/m[1]", "/r[1]/e[1]", "/r[1]/s[1]")},
		// Abbreviations and wildcards.
		{`/r/./a/../b`, ns("/r[1]/b[1]", "/r[1]/b[2]")},
		{`//b/..`, ns("/r[1]")},
		{`/r/e/text()`, nil},
		{`/r/*[not(node())]`, ns("/r[1]/e[1]")},
		{`/..`, nil},
		{`/`, ns("/")},
		{`/r/@*`, ns("/r[1]/@xml:lang")},
		{`/*/*[self::n or self::m]`, ns("/r[1]/n[1]", "/r[1]/n[2]", "/r[1]/m[1]")},
		{`(/r/n | /r/a | /r/n)`, ns("/r[1]/a[1]", "/r[1]/n[1]", "/r[1]/n[2]")},
		{`(/r/n)[last()]/preceding-sibling::*[2]`, ns("/r[1]/b[2]")},
		{`/r/n/following-sibling::*`, ns("/r[1]/n[2]", "/r[1]/m[1]", "/r[1]/e[1]", "/r[1]/s[1]")},
	})

	// A relative expression evaluated on an element.
	x := MustParse(`b[2]`)
	if got := format(x.Query(doc.Element())); got != "/r[1]/b[2]" {
		t.Errorf("relative from element: %s", got)
	}
	if got := format(MustParse(`/r/a`).Query(doc.Element().Children[1])); got != "/r[1]/a[1]" {
		t.Errorf("absolute from a nested node: %s", got)
	}
}

func TestResult(t *testing.T) {
	doc := mustDoc(t, `<r><a>3</a><a>4</a></r>`)
	nodes := MustParse(`//a`).Query(doc.Root)
	if nodes.String() != "3" || nodes.Number() != 3 || !nodes.Boolean() ||
		strings.Join(nodes.Values(), ",") != "3,4" || strings.Join(nodes.Paths(), ",") != "/r[1]/a[1],/r[1]/a[2]" {
		t.Errorf("node-set result: %q %v %v %v %v", nodes.String(), nodes.Number(), nodes.Boolean(), nodes.Values(), nodes.Paths())
	}
	empty := MustParse(`//x`).Query(doc.Root)
	if empty.String() != "" || !math.IsNaN(empty.Number()) || empty.Boolean() || len(empty.Values()) != 0 {
		t.Errorf("empty result: %+v", empty)
	}
	num := MustParse(`count(//a) div 4`).Query(doc.Root)
	if num.String() != "0.5" || num.Number() != 0.5 || !num.Boolean() || num.Paths() != nil || strings.Join(num.Values(), "") != "0.5" {
		t.Errorf("number result: %+v", num)
	}
	b := MustParse(`//a = 4`).Query(doc.Root)
	if b.String() != "true" || b.Number() != 1 || !b.Boolean() {
		t.Errorf("boolean result: %+v", b)
	}
	s := MustParse(`string(//a[2])`).Query(doc.Root)
	if s.String() != "4" || s.Number() != 4 || !s.Boolean() || s.Type != String {
		t.Errorf("string result: %+v", s)
	}
	for expr, typ := range map[string]ResultType{
		`//a`: NodeSet, `(//a)[1]`: NodeSet, `//a | //b`: NodeSet, `count(//a)`: Number, `-//a`: Number,
		`//a = 1`: Boolean, `name()`: String, `'x'`: String, `1`: Number, `not(1)`: Boolean,
	} {
		if got := MustParse(expr).ResultType(); got != typ {
			t.Errorf("ResultType(%s) = %v, want %v", expr, got, typ)
		}
	}
	if MustParse(` //a `).String() != ` //a ` {
		t.Error("String() does not return the source")
	}
}

func TestParseErrors(t *testing.T) {
	for _, c := range []struct {
		expr     string
		offset   int
		offset16 int
		msg      string
	}{
		{``, 0, 0, "the expression is empty"},
		{`   `, 0, 0, "the expression is empty"},
		{`/r/`, 3, 3, "unexpected end of expression: expected a node test"},
		{`//`, 2, 2, "unexpected end of expression: expected a node test"},
		{`/r[`, 3, 3, "unexpected end of expression: expected a location path"},
		{`/r[1`, 4, 4, "unexpected end of expression: expected ']'"},
		{`/r]`, 2, 2, "unexpected ']': expected an operator or the end of the expression"},
		{`/r[1]]`, 5, 5, "unexpected ']'"},
		{`//a[@id='x]`, 8, 8, "unterminated string literal"},
		{`//a[@id="x']`, 8, 8, "unterminated string literal"},
		{`//a#b`, 3, 3, "unexpected character '#'"},
		{`//a foo`, 4, 4, `unexpected name "foo": expected an operator`},
		{`1 +`, 3, 3, "unexpected end of expression: expected a location path"},
		{`count()`, 0, 0, "count() takes 1 argument"},
		{`concat('a')`, 0, 0, "concat() takes at least 2 arguments"},
		{`substring('a', 1, 2, 3)`, 0, 0, "substring() takes 2 to 3 arguments"},
		{`true(1)`, 0, 0, "true() takes 0 arguments"},
		{`count('a')`, 6, 6, "count() needs a node-set here; this is a string"},
		{`sum(1 + 1)`, 4, 4, "sum() needs a node-set here; this is a number"},
		{`name(1)`, 5, 5, "name() needs a node-set here"},
		{`foo(1)`, 0, 0, "unknown function foo()"},
		{`x:foo(1)`, 0, 0, "unknown function x:foo()"},
		{`id('a')`, 0, 0, "id() is not supported"},
		{`$var`, 0, 0, "variables are not supported"},
		{`//a[$x]`, 4, 4, "variables are not supported"},
		{`namespace::*`, 0, 0, "the namespace axis is not supported"},
		{`sideways::a`, 0, 0, `unknown axis "sideways"`},
		{`child::`, 7, 7, "expected a node test"},
		{`@`, 1, 1, "expected a node test"},
		{`./[1]`, 2, 2, "expected a node test"},
		{`.[1]`, 1, 1, "a predicate cannot follow '.'"},
		{`..[1]`, 2, 2, "a predicate cannot follow '..'"},
		{`'a' | //b`, 0, 0, "'|' combines node-sets only; this is a string"},
		{`//b | 1`, 6, 6, "'|' combines node-sets only; this is a number"},
		{`'a'[1]`, 0, 0, "a predicate filters node-sets only"},
		{`count(//a)/b`, 0, 0, "a path step applies to node-sets only"},
		{`(1)//b`, 0, 0, "a path step applies to node-sets only"},
		{`text(1)`, 5, 5, "unexpected number 1: expected ')'"},
		{`processing-instruction(1)`, 23, 23, "unexpected number 1: expected ')'"},
		{`//a[1] and`, 10, 10, "unexpected end of expression"},
		{`é/ü[`, 6, 4, "unexpected end of expression"},
		{`"😀" = //a[`, 13, 11, "unexpected end of expression"},
		{`!a`, 0, 0, "unexpected character '!'"},
		{strings.Repeat("(", MaxNesting+1) + "1" + strings.Repeat(")", MaxNesting+1), MaxNesting, MaxNesting, "nested too deeply"},
		{"//a" + strings.Repeat("[b", MaxNesting+1), 3 + 2*MaxNesting, 3 + 2*MaxNesting, "nested too deeply"},
		{strings.Repeat("a", MaxLength+1), MaxLength, MaxLength, "longer than"},
	} {
		_, err := Parse(c.expr)
		var se *SyntaxError
		if !errors.As(err, &se) {
			t.Errorf("Parse(%q) = %v, want a *SyntaxError", c.expr, err)
			continue
		}
		if se.Offset != c.offset || se.Offset16 != c.offset16 || !strings.Contains(se.Message, c.msg) {
			t.Errorf("Parse(%.40q) = %q at %d/%d, want %q at %d/%d", c.expr, se.Message, se.Offset, se.Offset16, c.msg, c.offset, c.offset16)
		}
		var ee *errs.Error
		if !errors.As(se.Err(), &ee) || ee.Kind != errs.InvalidArgument || ee.Code != CodeInvalidQuery || ee.Details["offset16"] == "" {
			t.Errorf("Err() = %#v", se.Err())
		}
	}

	// Tokens that look alike but parse.
	for _, expr := range []string{
		`div div div`, `and/or`, `mod mod mod`, `*/*`, `* * *`, `text`, `text/node`, `a-b`, `a.b`, `a -b`,
		`child::child`, `child:a`, `processing-instruction('x')`, `comment ()`, `@*`, `p:*`, `//p:a/@q:b`, `1.`, `.5`,
		`-a`, `a|b|c`, `(a)[1][2]/b`, `//a[b][c]`, `a  [ 1 ] `, `following-sibling :: a`, `a[.=.]`, `a[..]`,
	} {
		if _, err := Parse(expr); err != nil {
			t.Errorf("Parse(%q): %v", expr, err)
		}
	}
}

func TestStrictPrefixError(t *testing.T) {
	doc := mustDoc(t, `<a/>`)
	x := MustParse(`//p:a[q:b = 'é' or ç:c]`)
	_, err := x.Eval(context.Background(), doc.Root, Options{StrictNames: true, Namespaces: map[string]string{"p": "urn:p"}})
	var ee *errs.Error
	if !errors.As(err, &ee) || ee.Kind != errs.InvalidArgument || ee.Code != CodeUnknownPrefix ||
		ee.Details["prefix"] != "q" || ee.Details["offset"] != "6" {
		t.Fatalf("err = %#v", err)
	}
	_, err = x.Eval(context.Background(), doc.Root, Options{StrictNames: true, Namespaces: map[string]string{"p": "", "q": ""}})
	if !errors.As(err, &ee) || ee.Details["prefix"] != "ç" || ee.Details["offset"] != "20" || ee.Details["offset16"] != "19" {
		t.Fatalf("err = %#v", err)
	}
	// xml is always bound; lenient mode never fails.
	if _, err := MustParse(`//@xml:lang`).Eval(context.Background(), doc.Root, Options{StrictNames: true}); err != nil {
		t.Error(err)
	}
	if _, err := x.Eval(context.Background(), doc.Root, Options{}); err != nil {
		t.Error(err)
	}
}

// TestPathsSelectTheirNode checks the canonical path of every node of
// every sample: evaluated with default options it selects exactly that
// node.
func TestPathsSelectTheirNode(t *testing.T) {
	files, _ := filepath.Glob("testdata/*")
	extra := `<r xmlns:p="urn:1" xmlns:q="urn:1"><p:a/><a/><q:a/><a xmlns="urn:2"/><p:a/>t<![CDATA[c]]>t<!--x--><?pi?><?pi2?><?pi?><b p:x="1" x="2" q:y="3"/></r>`
	docs := map[string]*xmldom.Document{"extra": mustDoc(t, extra)}
	for _, f := range files {
		docs[f] = sample(t, filepath.Base(f))
	}
	for name, doc := range docs {
		count := 0
		var walk func(n *xmldom.Node)
		walk = func(n *xmldom.Node) {
			if n.Type != xmldom.DoctypeNode {
				count++
				path := n.Path()
				r, err := MustParse(path).Eval(context.Background(), doc.Root, Options{Limits: DefaultLimits})
				if err != nil || len(r.Nodes) != 1 || r.Nodes[0] != n {
					t.Errorf("%s: %s selects %v (%v)", name, path, r.Paths(), err)
				}
			}
			for _, a := range n.Attrs {
				walk(a)
			}
			for _, c := range n.Children {
				walk(c)
			}
		}
		walk(doc.Root)
		if count < 10 {
			t.Errorf("%s: only %d nodes", name, count)
		}
	}
}

func TestLimits(t *testing.T) {
	var b strings.Builder
	b.WriteString("<r>")
	for range 2000 {
		b.WriteString("<a><b><c>1</c></b></a>")
	}
	b.WriteString("</r>")
	doc := mustDoc(t, b.String())

	x := MustParse(`count(//*[count(//*[. = 2]) > 0])`)
	start := time.Now()
	_, err := x.Eval(context.Background(), doc.Root, Options{Limits: Limits{MaxSteps: 1_000_000}})
	var ee *errs.Error
	if !errors.As(err, &ee) || ee.Kind != errs.Unsupported || ee.Code != CodeTooComplex {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("stopping took %v", d)
	}

	// The default limit stops quadratic work on a larger document quickly.
	start = time.Now()
	_, err = x.Eval(context.Background(), doc.Root, Options{Limits: DefaultLimits})
	if !errors.As(err, &ee) || ee.Code != CodeTooComplex {
		t.Errorf("default limits: err = %v", err)
	}
	t.Logf("DefaultLimits stopped a quadratic query after %v", time.Since(start))

	// Ordinary queries on the same document fit easily.
	r, err := MustParse(`count(//a[b/c = 1]) + count(//c/ancestor::*) + count(//b//node()//node())`).Eval(context.Background(), doc.Root, Options{Limits: DefaultLimits})
	if err != nil || r.Num != 2000+6001+0 {
		t.Errorf("ordinary query: %v %v", r.Num, err)
	}

	// Many context nodes selecting the same descendants stay deduplicated.
	deep := mustDoc(t, strings.Repeat("<a>", 800)+strings.Repeat("</a>", 800))
	r, err = MustParse(`//a//a`).Eval(context.Background(), deep.Root, Options{Limits: DefaultLimits})
	if err != nil || len(r.Nodes) != 799 {
		t.Errorf("//a//a: %d nodes, %v", len(r.Nodes), err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = x.Eval(ctx, doc.Root, Options{})
	if !errors.Is(err, context.Canceled) || errs.KindOf(err) != errs.Cancelled {
		t.Errorf("cancelled: %v", err)
	}
}

func TestQueryXML(t *testing.T) {
	data, err := os.ReadFile("testdata/soap11-fault.xml")
	if err != nil {
		t.Fatal(err)
	}
	r, err := QueryXML(`string(//faultstring)`, data)
	if err != nil || r.Str != "Order 1042 could not be processed" {
		t.Errorf("QueryXML = %+v, %v", r, err)
	}
	for _, c := range []struct {
		expr, data string
		kind       errs.Kind
		code       string
	}{
		{`//a[`, `<a/>`, errs.InvalidArgument, CodeInvalidQuery},
		{`//a`, `<a>`, errs.Parse, xmldom.CodeInvalidXML},
		{`//a`, `<?xml version="1.0" encoding="Big5"?><a/>`, errs.Unsupported, xmldom.CodeUnsupportedEncoding},
	} {
		_, err := QueryXML(c.expr, []byte(c.data))
		var ee *errs.Error
		if !errors.As(err, &ee) || ee.Kind != c.kind || ee.Code != c.code {
			t.Errorf("QueryXML(%q, %q) = %v, want %s %s", c.expr, c.data, err, c.kind, c.code)
		}
	}
}

func TestHTMLDocument(t *testing.T) {
	doc, err := xmldom.ParseOptions(context.Background(), []byte(`<html><body><ul class=nav><li><a href="/a">A</a><li><a href="/b">B&nbsp;</a></ul><img src="x.png"><p>after</body></html>`), xmldom.Options{HTML: true})
	if err != nil {
		t.Fatal(err)
	}
	runCases(t, doc, Options{}, []queryCase{
		{`//ul[@class='nav']//a/@href`, ns("/html[1]/body[1]/ul[1]/li[1]/a[1]/@href", "/html[1]/body[1]/ul[1]/li[1]/li[1]/a[1]/@href")},
		{`string(//li//li/a)`, val("string:B ")},
		{`//img/following-sibling::p`, ns("/html[1]/body[1]/p[1]")},
	})
}
