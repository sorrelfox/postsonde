package template

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()
	text := func(start, end int, s string) Segment {
		return Segment{Kind: Text, Start: start, End: end, Text: s}
	}
	v := func(start, end, ns, ne int, name string) Segment {
		return Segment{Kind: Var, Start: start, End: end, Name: name, NameStart: ns, NameEnd: ne}
	}
	unterm := func(start int) Segment {
		return Segment{Kind: Unterminated, Start: start, End: start + 2, Text: "{{"}
	}
	tests := []struct {
		name string
		src  string
		want []Segment
	}{
		{"empty", "", nil},
		{"plain text", "hello", []Segment{text(0, 5, "hello")}},
		{"only a variable", "{{a}}", []Segment{v(0, 5, 2, 3, "a")}},
		{"variable in text", "x{{host}}/y", []Segment{text(0, 1, "x"), v(1, 9, 3, 7, "host"), text(9, 11, "/y")}},
		{"whitespace in braces", "{{  id\t}}", []Segment{v(0, 9, 4, 6, "id")}},
		{"newline in braces", "{{\nid\n}}", []Segment{v(0, 8, 3, 5, "id")}},
		{"empty name", "{{}}", []Segment{v(0, 4, 2, 2, "")}},
		{"blank name", "{{   }}", []Segment{v(0, 7, 5, 5, "")}},
		{"adjacent variables", "{{a}}{{b}}", []Segment{v(0, 5, 2, 3, "a"), v(5, 10, 7, 8, "b")}},
		{"dotted and dynamic names", "{{auth.token}}{{$uuid}}", []Segment{v(0, 14, 2, 12, "auth.token"), v(14, 23, 16, 21, "$uuid")}},
		{"inner space kept", "{{a b}}", []Segment{v(0, 7, 2, 5, "a b")}},
		{"unterminated at end", "a{{b", []Segment{text(0, 1, "a"), unterm(1), text(3, 4, "b")}},
		{"lone braces", "{{", []Segment{unterm(0)}},
		{"opener before close", "{{a {{b}}", []Segment{unterm(0), text(2, 4, "a "), v(4, 9, 6, 7, "b")}},
		{"single braces are text", "{a} }}", []Segment{text(0, 6, "{a} }}")}},
		{"json object around variable", `{"a":{{{x}}}}`, []Segment{text(0, 6, `{"a":{`), v(6, 11, 8, 9, "x"), text(11, 13, "}}")}},
		{"four open braces", "{{{{x}}", []Segment{text(0, 2, "{{"), v(2, 7, 4, 5, "x")}},
		{"close takes first }}", "{{a}}}", []Segment{v(0, 5, 2, 3, "a"), text(5, 6, "}")}},
		{"escaped braces", `\{{a}}`, []Segment{text(0, 6, "{{a}}")}},
		{"escaped then variable", `\{{a}}{{b}}`, []Segment{text(0, 6, "{{a}}"), v(6, 11, 8, 9, "b")}},
		{"escaped backslash", `\\{{a}}`, []Segment{text(0, 2, `\`), v(2, 7, 4, 5, "a")}},
		{"three backslashes", `\\\{{a}}`, []Segment{text(0, 8, `\{{a}}`)}},
		{"backslash elsewhere", `C:\\dir\n{`, []Segment{text(0, 10, `C:\\dir\n{`)}},
		{"escaped unterminated", `\{{a`, []Segment{text(0, 4, "{{a")}},
		{"multibyte", "ё{{имя}}ж", []Segment{text(0, 2, "ё"), v(2, 12, 4, 10, "имя"), text(12, 14, "ж")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Parse(tt.src)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse(%q)\n got %+v\nwant %+v", tt.src, got, tt.want)
			}
			checkSegments(t, tt.src, got)
		})
	}
}

func TestEscape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{"no braces", `a\b{c}`, `a\b{c}`},
		{"braces", "{{a}}", `\{{a}}`},
		{"backslash before braces", `\{{a}}`, `\\\{{a}}`},
		{"two backslashes", `\\{{`, `\\\\\{{`},
		{"three open braces", "{{{", `\{{{`},
		{"four open braces", "{{{{", `\{{\{{`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Escape(tt.in)
			if got != tt.want {
				t.Errorf("Escape(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if lit := literalText(t, got); lit != tt.in {
				t.Errorf("Parse(Escape(%q)) = %q", tt.in, lit)
			}
		})
	}
}

func TestSegmentKindString(t *testing.T) {
	t.Parallel()
	for k, want := range map[SegmentKind]string{Text: "text", Var: "var", Unterminated: "unterminated", 0: "invalid"} {
		if got := k.String(); got != want {
			t.Errorf("SegmentKind(%d).String() = %q, want %q", k, got, want)
		}
	}
}

// literalText returns the text of src, failing if it has anything but text.
func literalText(t *testing.T, src string) string {
	t.Helper()
	var out string
	for _, s := range Parse(src) {
		if s.Kind != Text {
			t.Fatalf("Parse(%q) has a %s segment at %d", src, s.Kind, s.Start)
		}
		out += s.Text
	}
	return out
}
