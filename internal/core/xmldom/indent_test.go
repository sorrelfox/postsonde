package xmldom

import (
	"errors"
	"testing"
)

func TestIndentNode(t *testing.T) {
	t.Parallel()
	src := `<s:a xmlns:s="urn:s"><s:b k="v &amp; w"><c>1</c><d><e/></d></s:b>t&amp;u<!--n--></s:a>`
	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	a := doc.Element()
	b := a.Children[0]
	tests := []struct {
		name string
		n    *Node
		want string
	}{
		{"element", b, "<s:b k=\"v &amp; w\">\n  <c>1</c>\n  <d>\n    <e/>\n  </d>\n</s:b>"},
		{"attribute", b.Attrs[0], `k="v &amp; w"`},
		{"text", a.Children[1], "t&u"},
		{"comment", a.Children[2], "<!--n-->"},
		{"mixed content as written", doc.Root, src},
	}
	for _, tt := range tests {
		if got := doc.IndentNode(tt.n, "  "); got != tt.want {
			t.Errorf("%s: IndentNode =\n%s\nwant\n%s", tt.name, got, tt.want)
		}
	}
}

func TestIndent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "nested",
			in:   `<?xml version="1.0" encoding="ISO-8859-1"?><!-- top --><a x="1"><b>caf` + "\u00e9" + `</b><c/><d><e  y='2'/></d></a>`,
			want: `<?xml version="1.0" encoding="ISO-8859-1"?>
<!-- top -->
<a x="1">
  <b>caf` + "\u00e9" + `</b>
  <c/>
  <d>
    <e  y='2'/>
  </d>
</a>`,
		},
		{
			name: "reindents",
			in:   "<a>\n\t<b>\n\t\t<c>1</c>\n\t</b>\n</a>\n",
			want: "<a>\n  <b>\n    <c>1</c>\n  </b>\n</a>",
		},
		{
			name: "mixed content kept",
			in:   `<p><q>Hello <b>big</b>  world &amp; more</q><r><![CDATA[<raw>]]></r></p>`,
			want: "<p>\n  <q>Hello <b>big</b>  world &amp; more</q>\n  <r><![CDATA[<raw>]]></r>\n</p>",
		},
		{
			name: "namespaces, instructions and doctype",
			in:   `<!DOCTYPE s:env><?pi data?><s:env xmlns:s="urn:s"><s:body><!--c--><?p x?></s:body></s:env>`,
			want: "<!DOCTYPE s:env>\n<?pi data?>\n<s:env xmlns:s=\"urn:s\">\n  <s:body>\n    <!--c-->\n    <?p x?>\n  </s:body>\n</s:env>",
		},
		{
			name: "byte order mark dropped",
			in:   "\xEF\xBB\xBF<a><b/></a>",
			want: "<a>\n  <b/>\n</a>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Indent(t.Context(), []byte(tt.in), "  ")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("Indent =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestIndentErrors(t *testing.T) {
	t.Parallel()
	_, err := Indent(t.Context(), []byte("<a><b></a>"), "  ")
	var se *SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *SyntaxError", err)
	}
	// A declared encoding is not used: the text is UTF-8 already.
	got, err := Indent(t.Context(), []byte(`<?xml version="1.0" encoding="shift_jis"?><a>`+"\u00e9"+`</a>`), " ")
	if err != nil || got != `<?xml version="1.0" encoding="shift_jis"?>`+"\n<a>\u00e9</a>" {
		t.Errorf("Indent = %q, %v", got, err)
	}
}
