package ast

import (
	"math"
	"testing"
)

func TestPosition(t *testing.T) {
	t.Parallel()

	src := "ab\r\ncé\rd\n\nx"
	f := NewFile("a.proto", src)
	lit := &File{Source: src} // no line index
	tests := []struct {
		off  int
		want Position
	}{
		{-5, Position{1, 1}}, {0, Position{1, 1}}, {2, Position{1, 3}}, {4, Position{2, 1}},
		{7, Position{2, 3}}, {8, Position{3, 1}}, {10, Position{4, 1}}, {11, Position{5, 1}},
		{12, Position{5, 2}}, {99, Position{5, 2}},
	}
	for _, tt := range tests {
		if got := f.Position(tt.off); got != tt.want {
			t.Errorf("Position(%d) = %v, want %v", tt.off, got, tt.want)
		}
		if got := lit.Position(tt.off); got != tt.want {
			t.Errorf("literal Position(%d) = %v, want %v", tt.off, got, tt.want)
		}
	}
	if got := (Position{3, 7}).String(); got != "3:7" {
		t.Errorf("String = %q", got)
	}
}

func TestNames(t *testing.T) {
	t.Parallel()

	id := func(s string) *Ident { return &Ident{Value: s} }
	if got := (&FullName{LeadingDot: true, Parts: []*Ident{id("a"), id("B")}}).String(); got != ".a.B" {
		t.Errorf("FullName = %q", got)
	}
	name := &OptionName{Parts: []*OptionNamePart{
		{Extension: true, Name: &FullName{Parts: []*Ident{id("x"), id("y")}}},
		{Name: &FullName{Parts: []*Ident{id("z")}}},
	}}
	if got := name.String(); got != "(x.y).z" {
		t.Errorf("OptionName = %q", got)
	}
	if !id("").Missing() || id("a").Missing() {
		t.Error("Missing")
	}
}

func TestIntLit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		lit  IntLit
		want int64
		ok   bool
	}{
		{IntLit{Value: 5}, 5, true},
		{IntLit{Negative: true, Value: 5}, -5, true},
		{IntLit{Value: math.MaxInt64}, math.MaxInt64, true},
		{IntLit{Value: math.MaxInt64 + 1}, 0, false},
		{IntLit{Negative: true, Value: 1 << 63}, math.MinInt64, true},
		{IntLit{Negative: true, Value: 1<<63 + 1}, 0, false},
		{IntLit{Negative: true}, 0, true},
	}
	for _, tt := range tests {
		if got, ok := tt.lit.Int64(); got != tt.want || ok != tt.ok {
			t.Errorf("%+v.Int64() = %d, %v; want %d, %v", tt.lit, got, ok, tt.want, tt.ok)
		}
	}
}

func TestSyntaxAndPackage(t *testing.T) {
	t.Parallel()

	f := &File{Decls: []Decl{
		&Package{Name: &FullName{Parts: []*Ident{{Value: "p"}}}},
		&Syntax{Value: &StringLit{Value: "proto3"}},
	}}
	if f.Syntax() != Proto3 || f.Package() != "p" || f.SyntaxDecl() == nil {
		t.Errorf("Syntax %v Package %q", f.Syntax(), f.Package())
	}
	if (&File{Decls: []Decl{&Syntax{}}}).Syntax() != UnknownSyntax {
		t.Error("syntax without value")
	}
}

func TestInspectAndDump(t *testing.T) {
	t.Parallel()

	one := &IntLit{Loc: Loc{24, 25}, Raw: "1", Value: 1}
	f := &File{Loc: Loc{0, 27}, Name: "d.proto", Decls: []Decl{
		&Message{Loc: Loc{0, 27}, Comments: Comments{Leading: " A message.\n"}, Name: &Ident{Loc{8, 11}, "Foo"}, RBrace: 26, Body: []Decl{
			&Field{Loc: Loc{14, 26}, Type: &FullName{Loc: Loc{14, 19}, Parts: []*Ident{{Loc{14, 19}, "int32"}}},
				Name: &Ident{Loc{20, 21}, "a"}, Number: one},
			&Oneof{Name: &Ident{}, RBrace: -1},
		}},
	}}
	var kinds []string
	Inspect(f, func(n Node) bool {
		kinds = append(kinds, typeName(n))
		_, isField := n.(*Field)
		return !isField
	})
	want := "File Message Ident Field Oneof Ident"
	if got := join(kinds); got != want {
		t.Errorf("Inspect order %q, want %q", got, want)
	}
	Inspect(nil, func(Node) bool { t.Error("visited nil"); return true })
	Inspect((*Message)(nil), func(Node) bool { t.Error("visited typed nil"); return true })

	dump := Dump(f)
	wantDump := `File [0:27] Name="d.proto"
  Decls[0]: Message [0:27] Leading=" A message.\n" Name=[8:11]"Foo" RBrace=26
    Body[0]: Field [14:26] Type=[14:19]"int32" Name=[20:21]"a"
      Number: IntLit [24:25] Raw="1" Value=1
    Body[1]: Oneof [0:0] Name=[0:0]"" RBrace=-1
`
	if dump != wantDump {
		t.Errorf("Dump:\n%s\nwant:\n%s", dump, wantDump)
	}
}

func TestEnumStrings(t *testing.T) {
	t.Parallel()
	for got, want := range map[string]string{
		Proto2.String(): "proto2", Editions.String(): "editions", SyntaxKind(9).String(): "SyntaxKind(9)",
		ImportWeak.String(): "weak", ImportKind(9).String(): "ImportKind(9)",
		LabelRepeated.String(): "repeated", LabelNone.String(): "", Label(9).String(): "Label(9)",
		VisibilityLocal.String(): "local", Visibility(9).String(): "Visibility(9)",
		FieldRefAny.String(): "any", FieldRefKind(9).String(): "FieldRefKind(9)",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func typeName(n Node) string {
	switch n.(type) {
	case *File:
		return "File"
	case *Message:
		return "Message"
	case *Ident:
		return "Ident"
	case *Field:
		return "Field"
	case *Oneof:
		return "Oneof"
	}
	return "?"
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}
