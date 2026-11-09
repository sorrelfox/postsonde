package xmldom

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// dump renders the tree one node per line: indentation, type, name or
// value, flags.
func dump(n *Node) string {
	var b strings.Builder
	var walk func(n *Node, depth int)
	walk = func(n *Node, depth int) {
		b.WriteString(strings.Repeat("  ", depth))
		b.WriteString(n.Type.String())
		switch n.Type {
		case ElementNode:
			b.WriteString(" " + n.Name.Qualified() + " {" + n.Name.Space + "}")
			for _, d := range n.NSDecls {
				b.WriteString(" xmlns:" + d.Prefix + "=" + d.URI)
			}
		case ProcInstNode:
			b.WriteString(" " + n.Name.Local + " " + strconv.Quote(n.Value))
		default:
			if n.Type != DocumentNode {
				b.WriteString(" " + strconv.Quote(n.Value))
			}
		}
		if n.CDATA {
			b.WriteString(" [cdata]")
		}
		b.WriteByte('\n')
		for _, a := range n.Attrs {
			b.WriteString(strings.Repeat("  ", depth+1) + "@" + a.Name.Qualified() + " {" + a.Name.Space + "} = " + a.Value + "\n")
		}
		for _, c := range n.Children {
			walk(c, depth+1)
		}
	}
	walk(n, 0)
	return b.String()
}

func mustParse(t testing.TB, src string) *Document {
	t.Helper()
	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}

func TestTree(t *testing.T) {
	doc := mustParse(t, `<?xml version="1.0"?>
<!DOCTYPE note SYSTEM "note.dtd">
<?app mode="x"?>
<a:root xmlns:a="urn:a" xmlns="urn:default" id="1" a:kind="k" xml:lang="en">
  <!-- c -->
  <child>text &amp; <![CDATA[<raw>]]></child>
  <x:other xmlns:x="urn:x" xmlns=""><plain attr='v'/></x:other>
</a:root>
<!-- after -->`)
	want := `document
  doctype "DOCTYPE note SYSTEM \"note.dtd\""
  processing-instruction app "mode=\"x\""
  element a:root {urn:a} xmlns:a=urn:a xmlns:=urn:default
    @id {} = 1
    @a:kind {urn:a} = k
    @xml:lang {http://www.w3.org/XML/1998/namespace} = en
    text "\n  "
    comment " c "
    text "\n  "
    element child {urn:default}
      text "text & "
      text "<raw>" [cdata]
    text "\n  "
    element x:other {urn:x} xmlns:x=urn:x xmlns:=
      element plain {}
        @attr {} = v
    text "\n"
  comment " after "
`
	if got := dump(doc.Root); got != want {
		t.Errorf("tree:\n%s\nwant:\n%s", got, want)
	}
	if doc.Encoding != EncodingUTF8 || doc.Element().Name.Local != "root" {
		t.Errorf("encoding %q, element %v", doc.Encoding, doc.Element())
	}

	// Document order.
	var order []int
	var walk func(n *Node)
	walk = func(n *Node) {
		order = append(order, n.Index)
		for _, a := range n.Attrs {
			order = append(order, a.Index)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(doc.Root)
	for i, idx := range order {
		if idx != i {
			t.Fatalf("document order %v", order)
		}
	}
	if doc.NodeCount != len(order) {
		t.Errorf("NodeCount %d, want %d", doc.NodeCount, len(order))
	}

	ns := doc.Namespaces()
	if len(ns) != 3 || ns["a"] != "urn:a" || ns[""] != "urn:default" || ns["x"] != "urn:x" {
		t.Errorf("Namespaces() = %v", ns)
	}
	plain := doc.Element().Children[5].Children[0]
	if u, ok := plain.LookupNamespace(""); !ok || u != "" {
		t.Errorf("LookupNamespace default = %q %v", u, ok)
	}
	if u, ok := plain.LookupNamespace("a"); !ok || u != "urn:a" {
		t.Errorf("LookupNamespace a = %q %v", u, ok)
	}
	if _, ok := plain.LookupNamespace("zz"); ok {
		t.Error("LookupNamespace zz found")
	}
	if got := doc.Element().StringValue(); got != "\n  \n  text & <raw>\n  \n" {
		t.Errorf("StringValue = %q", got)
	}
}

func TestPaths(t *testing.T) {
	doc := mustParse(t, `<r xmlns:p="urn:p" xmlns:q="urn:p"><a/><p:a/><a k="1" p:k="2"/>text<!--c--><?pi x?><q:a/><!--d-->more<?pi y?><?other?><b/></r>`)
	var got []string
	var walk func(n *Node)
	walk = func(n *Node) {
		got = append(got, n.Path())
		for _, a := range n.Attrs {
			got = append(got, a.Path())
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(doc.Root)
	want := []string{
		"/", "/r[1]",
		"/r[1]/a[1]", "/r[1]/p:a[1]", "/r[1]/a[3]", "/r[1]/a[3]/@k", "/r[1]/a[3]/@p:k",
		"/r[1]/text()[1]", "/r[1]/comment()[1]", "/r[1]/processing-instruction('pi')[1]",
		"/r[1]/q:a[1]", "/r[1]/comment()[2]", "/r[1]/text()[2]", "/r[1]/processing-instruction('pi')[2]",
		"/r[1]/processing-instruction('other')[1]", "/r[1]/b[1]",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("paths:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	doctype := mustParse(t, `<!DOCTYPE r><r/>`).Root.Children[0]
	if doctype.Type != DoctypeNode || doctype.Path() != "" {
		t.Errorf("doctype %v path %q", doctype.Type, doctype.Path())
	}
}

func TestOffsets(t *testing.T) {
	src := "<?xml version=\"1.0\"?>\n<root a=\"1\"\n  b = 'two'>\n  <é>ü<![CDATA[x]]></é><!--c-->\U0001F600<self/>\n</root>"
	doc := mustParse(t, src)
	root := doc.Element()
	check := func(name string, n *Node, text string, line, col int) {
		t.Helper()
		if got := src[n.Offset:n.End]; got != text {
			t.Errorf("%s: source %q, want %q", name, got, text)
		}
		p := doc.Position(n.Offset)
		if p.Line != line || p.Column != col {
			t.Errorf("%s: at %d:%d, want %d:%d", name, p.Line, p.Column, line, col)
		}
	}
	check("root", root, src[22:], 2, 1)
	check("@a", root.Attrs[0], `a="1"`, 2, 7)
	check("@b", root.Attrs[1], `b = 'two'`, 3, 3)
	e := root.Children[1]
	check("é", e, "<é>ü<![CDATA[x]]></é>", 4, 3)
	check("text", e.Children[0], "ü", 4, 7)
	check("cdata", e.Children[1], "<![CDATA[x]]>", 4, 9)
	check("comment", root.Children[2], "<!--c-->", 4, 27)
	check("emoji", root.Children[3], "\U0001F600", 4, 35)
	check("self", root.Children[4], "<self/>", 4, 39)

	// UTF-16 offsets: é and ü are one unit each, the emoji two.
	if p := doc.Position(root.Children[4].Offset); p.Offset16 != len([]rune(src[:root.Children[4].Offset]))+1 {
		t.Errorf("Offset16 = %d", p.Offset16)
	}
	if p := doc.Position(len(src) + 10); p.Offset != len(src) {
		t.Errorf("clamped offset %d", p.Offset)
	}

	// Offset16 across 4 KiB blocks.
	long := "<r>" + strings.Repeat("é\U0001F600", 3000) + "<x/></r>"
	d2 := mustParse(t, long)
	x := d2.Element().Children[1]
	if p := d2.Position(x.Offset); p.Offset16 != 3+3000*3 {
		t.Errorf("long Offset16 = %d, want %d", p.Offset16, 3+3000*3)
	}
}

func TestSyntaxErrors(t *testing.T) {
	for _, c := range []struct {
		src          string
		line, column int
		msg          string
	}{
		{``, 1, 1, "the document has no root element"},
		{`<!-- only -->`, 1, 14, "the document has no root element"},
		{`<a>`, 1, 4, "unexpected end of document: <a> is not closed"},
		{"<a>\n  <b></c>", 2, 6, "element <b> closed by </c>"},
		{`<a/></a>`, 1, 5, "unexpected end tag </a>"},
		{`<a/><b/>`, 1, 5, "only one root element is allowed"},
		{`<a/>text`, 1, 5, "text is not allowed outside the root element"},
		{`text<a/>`, 1, 1, "text is not allowed outside the root element"},
		{` <?xml version="1.0"?><a/>`, 1, 2, "the XML declaration is allowed only at the start"},
		{`<a><!DOCTYPE a></a>`, 1, 4, "a <!DOCTYPE> declaration is not allowed here"},
		{`<!ENTITY x "y"><a/>`, 1, 1, "a <!ENTITY> declaration is not allowed here"},
		{`<a x="1" x="2"/>`, 1, 10, "attribute x is repeated"},
		{`<a xmlns:p="urn:1" xmlns:q="urn:1" p:x="1" q:x="2"/>`, 1, 44, "attribute q:x is repeated"},
		{`<p:a/>`, 1, 1, "the namespace prefix p is not declared"},
		{`<a p:x="1"/>`, 1, 4, "the namespace prefix p is not declared"},
		{`<a xmlns:p=""/>`, 1, 4, "cannot be bound to an empty URI"},
		{`<a>&nbsp;</a>`, 1, 10, "invalid character entity &nbsp;"},
		{`<a b=c/>`, 1, 7, "unquoted or missing attribute value"},
		{`<a>]]></a>`, 1, 7, "unescaped ]]> not in CDATA section"},
		{`<a><!-- x -- y --></a>`, 1, 14, `invalid sequence "--" not allowed in comments`},
		{`<?xml version="1.1"?><a/>`, 1, 22, "unsupported version"},
		{"<a>\xff</a>", 1, 4, "invalid UTF-8"},
		{"<a><!-- \xdb --></a>", 1, 9, "invalid UTF-8"},
	} {
		_, err := Parse([]byte(c.src))
		var se *SyntaxError
		if !errors.As(err, &se) {
			t.Errorf("Parse(%q) = %v, want a *SyntaxError", c.src, err)
			continue
		}
		if se.Line != c.line || se.Column != c.column || !strings.Contains(se.Message, c.msg) {
			t.Errorf("Parse(%q) = %q at %d:%d, want %q at %d:%d", c.src, se.Message, se.Line, se.Column, c.msg, c.line, c.column)
		}
		if !strings.HasPrefix(se.Error(), "xml: ") {
			t.Errorf("Error() = %q", se.Error())
		}
		var ee *errs.Error
		if !errors.As(AsErr(err), &ee) || ee.Kind != errs.Parse || ee.Code != CodeInvalidXML || ee.Details["line"] == "" {
			t.Errorf("AsErr(%q) = %#v", c.src, AsErr(err))
		}
	}
}

// TestEntityExpansion checks that DTD entities are never expanded or
// fetched: XXE and billion-laughs documents fail in strict mode and keep
// the references as text in HTML mode.
func TestEntityExpansion(t *testing.T) {
	xxe := `<?xml version="1.0"?>
<!DOCTYPE foo [ <!ELEMENT foo ANY> <!ENTITY xxe SYSTEM "file:///etc/passwd"> ]>
<foo>&xxe;</foo>`
	ssrf := `<!DOCTYPE foo [ <!ENTITY % remote SYSTEM "http://127.0.0.1:1/evil.dtd"> %remote; ]><foo>&exfil;</foo>`
	laughs := `<?xml version="1.0"?>
<!DOCTYPE lolz [
 <!ENTITY lol "lol">
 <!ENTITY lol1 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">
 <!ENTITY lol2 "&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;">
 <!ENTITY lol9 "&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;">
]>
<lolz>&lol9;</lolz>`
	for name, src := range map[string]string{"xxe": xxe, "ssrf": ssrf, "laughs": laughs} {
		_, err := Parse([]byte(src))
		var se *SyntaxError
		if !errors.As(err, &se) || !strings.Contains(se.Message, "invalid character entity") {
			t.Errorf("%s: strict Parse = %v, want an invalid entity error", name, err)
		}
		doc, err := ParseOptions(context.Background(), []byte(src), Options{HTML: true})
		if err != nil {
			t.Errorf("%s: HTML Parse: %v", name, err)
			continue
		}
		el := doc.Element()
		if got := el.StringValue(); !strings.HasPrefix(got, "&") || len(got) > 10 {
			t.Errorf("%s: HTML text %q, want the literal reference", name, got)
		}
		if doc.Root.Children[0].Type != DoctypeNode {
			t.Errorf("%s: DOCTYPE not kept", name)
		}
	}
}

func TestHTMLMode(t *testing.T) {
	src := `<!DOCTYPE html>
<html>
<head><meta charset=utf-8><title>Tom &amp; Jerry&nbsp;&copy;</title>
<body>
<p class=intro>One<br>Two<BR/>Three
<p>Para <b>bold <i>both</b> tail</P>
<input disabled type="checkbox">
<ul><li>a<li>b</ul>
</div>
<svg:rect/>
</html>
trailing text<extra/>`
	doc, err := ParseOptions(context.Background(), []byte(src), Options{HTML: true})
	if err != nil {
		t.Fatal(err)
	}
	want := `document
  doctype "DOCTYPE html"
  element html {}
    text "\n"
    element head {}
      element meta {}
        @charset {} = utf-8
      element title {}
        text "Tom & Jerry\u00a0©"
      text "\n"
      element body {}
        text "\n"
        element p {}
          @class {} = intro
          text "One"
          element br {}
          text "Two"
          element BR {}
          text "Three\n"
          element p {}
            text "Para "
            element b {}
              text "bold "
              element i {}
                text "both"
            text " tail"
          text "\n"
          element input {}
            @disabled {} = disabled
            @type {} = checkbox
          text "\n"
          element ul {}
            element li {}
              text "a"
              element li {}
                text "b"
          text "\n\n"
          element svg:rect {}
          text "\n"
  text "\ntrailing text"
  element extra {}
`
	if got := dump(doc.Root); got != want {
		t.Errorf("tree:\n%s\nwant:\n%s", got, want)
	}
	// HTML mode never fails on the leniencies above; strict mode does.
	if _, err := Parse([]byte(src)); err == nil {
		t.Error("strict Parse accepted HTML")
	}
}

func TestLimits(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		src   string
		lim   Limits
		limit string
	}{
		{"bytes", `<a>0123456789</a>`, Limits{MaxBytes: 10}, LimitBytes},
		{"depth", strings.Repeat("<a>", 11) + strings.Repeat("</a>", 11), Limits{MaxDepth: 10}, LimitDepth},
		{"nodes", `<a><b/><b/><b/></a>`, Limits{MaxNodes: 3}, LimitNodes},
		{"attributes", `<a x="1" y="2" z="3"/>`, Limits{MaxNodes: 3}, LimitNodes},
	} {
		_, err := ParseOptions(ctx, []byte(c.src), Options{Limits: c.lim})
		var le *LimitError
		if !errors.As(err, &le) || le.Limit != c.limit {
			t.Errorf("%s: err = %v, want a %s LimitError", c.name, err, c.limit)
			continue
		}
		var ee *errs.Error
		if !errors.As(le.Err(), &ee) || ee.Kind != errs.Unsupported || ee.Code != CodeTooLarge {
			t.Errorf("%s: Err() = %v", c.name, le.Err())
		}
	}
	// At the limits exactly: accepted.
	if _, err := ParseOptions(ctx, []byte(strings.Repeat("<a>", 10)+strings.Repeat("</a>", 10)), Options{Limits: Limits{MaxDepth: 10}}); err != nil {
		t.Errorf("depth 10: %v", err)
	}
	if _, err := ParseOptions(ctx, []byte(`<a><b/><b/></a>`), Options{Limits: Limits{MaxNodes: 3}}); err != nil {
		t.Errorf("3 nodes: %v", err)
	}
	// Negative: unlimited.
	deep := strings.Repeat("<a>", 5000) + strings.Repeat("</a>", 5000)
	if _, err := ParseOptions(ctx, []byte(deep), Options{Limits: Limits{MaxDepth: -1}}); err != nil {
		t.Errorf("unlimited depth: %v", err)
	}
	if _, err := Parse([]byte(deep)); err == nil {
		t.Error("default MaxDepth accepted 5000 levels")
	}
}

func TestContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := "<a>" + strings.Repeat("<b/>", 5000) + "</a>"
	_, err := ParseOptions(ctx, []byte(src), Options{})
	if !errors.Is(err, context.Canceled) || errs.KindOf(err) != errs.Cancelled {
		t.Errorf("err = %v", err)
	}
}

func TestLargeDocument(t *testing.T) {
	var b strings.Builder
	b.WriteString("<items>")
	for range 200_000 {
		b.WriteString(`<item id="x"><name>n</name></item>`)
	}
	b.WriteString("</items>")
	start := time.Now()
	doc, err := Parse([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if doc.NodeCount != 1+1+200_000*4 {
		t.Errorf("NodeCount = %d", doc.NodeCount)
	}
	t.Logf("%d bytes, %d nodes in %v", b.Len(), doc.NodeCount, time.Since(start))
}

// TestSamples parses the realistic documents the XPath tests use.
func TestSamples(t *testing.T) {
	files, err := filepath.Glob("../xpath/testdata/*")
	if err != nil || len(files) == 0 {
		t.Fatalf("no samples: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := Parse(data)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if doc.Element() == nil || doc.NodeCount < 10 {
			t.Errorf("%s: %d nodes", f, doc.NodeCount)
		}
	}
}
