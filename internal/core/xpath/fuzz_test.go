package xpath

import (
	"context"
	"errors"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/xmldom"
)

// fuzzLimits keep each fuzz input fast however pathological the query.
var fuzzLimits = Limits{MaxSteps: 200_000}

var fuzzSeeds = []string{
	`/rss/channel/item[position() < 3]/title`, `//item[dc:creator='Ada']`, `(//category)[last()]`,
	`//soap:Body//ns1:sku[. = 'XYZ-9']/preceding-sibling::*[1]`, `//@*[starts-with(name(), 'wsu')]`,
	`count(//*) div 0 mod 3`, `substring('12345', -1 div 0, 1 div 0)`, `//node()[self::comment() or self::text()]`,
	`//Quote[Price > //Quote[2]/Price]/@symbol | //processing-instruction('x')`, `/descendant::*[last()]/ancestor::*[1]`,
	`//a/following::node()[1]/preceding::*[2]`, `translate(normalize-space(//title), 'abc', 'ABC')`,
	`//*[lang('en')][not(@id)][count(*) >= 1]`, `-(//n) + --//m`, `//x:*[local-name() = name()]`,
}

// FuzzParse parses random expressions. Parse must not panic; errors must
// point inside the expression; a valid expression must evaluate within
// the limits without panicking, and every result node's path must select
// that node again.
func FuzzParse(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	doc := sample(f, "soap11-fault.xml")

	f.Fuzz(func(t *testing.T, src string) {
		x, err := Parse(src)
		if err != nil {
			var se *SyntaxError
			if !errors.As(err, &se) || se.Offset < 0 || se.Offset > len(src) || se.Message == "" || se.Offset16 > se.Offset {
				t.Fatalf("Parse(%q) error %#v", src, err)
			}
			return
		}
		if x.String() != src {
			t.Fatalf("String() = %q", x.String())
		}
		for _, strict := range []bool{false, true} {
			r, err := x.Eval(context.Background(), doc.Root, Options{Limits: fuzzLimits, StrictNames: strict,
				Namespaces: map[string]string{"soap": "http://schemas.xmlsoap.org/soap/envelope/"}})
			if err != nil {
				continue
			}
			if r.Type != x.ResultType() {
				t.Fatalf("%q: result %v, static type %v", src, r.Type, x.ResultType())
			}
			checkNodeSet(t, src, doc, r)
		}
	})
}

// FuzzQuery evaluates random expressions on random documents, strict and
// HTML-mode.
func FuzzQuery(f *testing.F) {
	f.Add(`//a[@x = ../b][1]`, `<r><a x="1"/><b>1</b><a x="2"><!--c--></a></r>`, false)
	f.Add(`//p:*/text()[2] | //comment()`, `<r xmlns:p="urn:p"><p:a>t<![CDATA[c]]>u<?pi?>v</p:a></r>`, false)
	f.Add(`//li[last()]/following::*`, `<ul><li>a<li>b<br>c</ul><p>x`, true)
	f.Add(`sum(//n) > count(//*[. = 1])`, `<r><n>1</n><n>1.5</n><n>-</n></r>`, false)

	f.Fuzz(func(t *testing.T, src, data string, html bool) {
		x, err := Parse(src)
		if err != nil {
			return
		}
		doc, err := xmldom.ParseOptions(context.Background(), []byte(data), xmldom.Options{HTML: html, Limits: xmldom.Limits{MaxNodes: 10_000}})
		if err != nil {
			return
		}
		r, err := x.Eval(context.Background(), doc.Root, Options{Limits: fuzzLimits})
		if err != nil {
			return
		}
		if html {
			// Paths may repeat an attribute name in HTML mode; only order
			// and uniqueness are checked.
			for i := 1; i < len(r.Nodes); i++ {
				if r.Nodes[i-1].Index >= r.Nodes[i].Index {
					t.Fatalf("%q: nodes out of order", src)
				}
			}
			return
		}
		checkNodeSet(t, src, doc, r)
	})
}

// checkNodeSet checks that a node-set is in document order without
// duplicates and that each node's path selects it.
func checkNodeSet(t *testing.T, src string, doc *xmldom.Document, r Result) {
	t.Helper()
	for i, n := range r.Nodes {
		if i > 0 && r.Nodes[i-1].Index >= n.Index {
			t.Fatalf("%q: nodes out of document order", src)
		}
		path := n.Path()
		if path == "" {
			t.Fatalf("%q: a %v node without a path", src, n.Type)
		}
		px, err := Parse(path)
		if err != nil {
			t.Fatalf("%q: path %q does not parse: %v", src, path, err)
		}
		again, err := px.Eval(context.Background(), doc.Root, Options{Limits: fuzzLimits})
		if err != nil || len(again.Nodes) != 1 || again.Nodes[0] != n {
			t.Fatalf("%q: path %s selects %v, %v", src, path, again.Paths(), err)
		}
	}
}
