package ast

import (
	"reflect"
	"testing"
)

func TestPosition(t *testing.T) {
	t.Parallel()

	src := "ab\r\ncd\ré\nx\xffy\n"
	tests := []struct {
		off  int
		want Position
	}{
		{-5, Position{1, 1}}, {0, Position{1, 1}}, {2, Position{1, 3}},
		{3, Position{1, 4}}, // between \r and \n: still the \r line
		{4, Position{2, 1}}, {6, Position{2, 3}}, {7, Position{3, 1}},
		{9, Position{3, 2}}, // after the two-byte é
		{10, Position{4, 1}}, {12, Position{4, 3}}, {13, Position{4, 4}},
		{14, Position{5, 1}}, {99, Position{5, 1}},
	}
	built := NewDocument(src)
	literal := &Document{Source: src}
	for _, tt := range tests {
		if got := built.Position(tt.off); got != tt.want {
			t.Errorf("Position(%d) = %v, want %v", tt.off, got, tt.want)
		}
		if got := literal.Position(tt.off); got != tt.want {
			t.Errorf("literal Document: Position(%d) = %v, want %v", tt.off, got, tt.want)
		}
	}
	if s := (Position{3, 7}).String(); s != "3:7" {
		t.Errorf("String() = %q", s)
	}
}

// tree builds "{ a(x: 1) }" by hand:
//
//	0123456789a
//	{ a(x: 1) }
func tree() (*Document, *OperationDefinition, *SelectionSet, *Field, *ArgumentList, *Argument, *IntValue) {
	iv := &IntValue{Loc: Loc{7, 8}, Raw: "1"}
	arg := &Argument{Loc: Loc{4, 8}, Name: &Name{Loc{4, 5}, "x"}, Colon: 5, Value: iv}
	args := &ArgumentList{Loc: Loc{3, 9}, LParen: 3, RParen: 8, Arguments: []*Argument{arg}}
	f := &Field{Loc: Loc{2, 9}, Colon: -1, Name: &Name{Loc{2, 3}, "a"}, Arguments: args}
	set := &SelectionSet{Loc: Loc{0, 11}, LBrace: 0, RBrace: 10, Selections: []Selection{f}}
	op := &OperationDefinition{Loc: Loc{0, 11}, Keyword: -1, SelectionSet: set}
	doc := NewDocument("{ a(x: 1) }")
	doc.Definitions = []Definition{op}
	return doc, op, set, f, args, arg, iv
}

func TestNodePathAt(t *testing.T) {
	t.Parallel()

	doc, op, set, f, args, arg, iv := tree()
	tests := []struct {
		off  int
		want []Node
	}{
		{-1, nil},
		{12, nil},
		{0, []Node{doc, op, set}},
		{2, []Node{doc, op, set, f, f.Name}},
		{3, []Node{doc, op, set, f, f.Name}}, // touching siblings: the earlier wins
		{4, []Node{doc, op, set, f, args, arg, arg.Name}},
		{6, []Node{doc, op, set, f, args, arg}},
		{8, []Node{doc, op, set, f, args, arg, iv}},
		{9, []Node{doc, op, set, f, args}},
		{10, []Node{doc, op, set}},
		{11, []Node{doc, op, set}},
	}
	for _, tt := range tests {
		got := NodePathAt(doc, tt.off)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("NodePathAt(%d) = %v, want %v", tt.off, types(got), types(tt.want))
		}
	}
	if got := NodePathAt(nil, 0); got != nil {
		t.Errorf("NodePathAt(nil) = %v", got)
	}
}

func types(ns []Node) []string {
	var s []string
	for _, n := range ns {
		s = append(s, reflect.TypeOf(n).String())
	}
	return s
}

func TestChildrenSkipsNil(t *testing.T) {
	t.Parallel()

	var nilValue *IntValue
	arg := &Argument{Name: &Name{}, Value: nilValue}
	if got := Children(arg); len(got) != 1 {
		t.Errorf("Children with a typed nil value = %v, want only the name", types(got))
	}
	def := &VariableDefinition{Variable: &Variable{Name: &Name{}}, Type: &NamedType{Name: &Name{}}}
	if got := Children(def); len(got) != 2 {
		t.Errorf("Children(VariableDefinition) = %v, want variable and type", types(got))
	}
	var count int
	Inspect(nilValue, func(Node) bool { count++; return true })
	if count != 0 {
		t.Errorf("Inspect visited a typed nil")
	}
}

func TestInspect(t *testing.T) {
	t.Parallel()

	doc, _, _, _, _, _, _ := tree()
	var visited []string
	Inspect(doc, func(n Node) bool {
		visited = append(visited, reflect.TypeOf(n).Elem().Name())
		_, isArgs := n.(*ArgumentList)
		return !isArgs
	})
	want := []string{"Document", "OperationDefinition", "SelectionSet", "Field", "Name", "ArgumentList"}
	if !reflect.DeepEqual(visited, want) {
		t.Errorf("Inspect visited %v, want %v", visited, want)
	}
}

func TestDump(t *testing.T) {
	t.Parallel()

	doc, _, _, _, _, _, _ := tree()
	want := `Document [0:11]
  Definitions[0]: OperationDefinition [0:11] Operation=query Keyword=-1
    SelectionSet: SelectionSet [0:11] LBrace=0 RBrace=10
      Selections[0]: Field [2:9] Colon=-1
        Name: Name [2:3] Value="a"
        Arguments: ArgumentList [3:9] LParen=3 RParen=8
          Arguments[0]: Argument [4:8] Colon=5
            Name: Name [4:5] Value="x"
            Value: IntValue [7:8] Raw="1"
`
	if got := Dump(doc); got != want {
		t.Errorf("Dump =\n%s\nwant\n%s", got, want)
	}
}

func TestOperationTypeString(t *testing.T) {
	t.Parallel()

	for op, want := range map[OperationType]string{Query: "query", Mutation: "mutation", Subscription: "subscription", 9: "OperationType(9)"} {
		if got := op.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", op, got, want)
		}
	}
}
