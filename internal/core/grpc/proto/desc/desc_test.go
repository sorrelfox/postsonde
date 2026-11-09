package desc

import "testing"

func TestKinds(t *testing.T) {
	// Numbers must match google.protobuf.FieldDescriptorProto.Type.
	for name, want := range map[string]Kind{
		"double": 1, "float": 2, "int64": 3, "uint64": 4, "int32": 5, "fixed64": 6, "fixed32": 7,
		"bool": 8, "string": 9, "bytes": 12, "uint32": 13, "sfixed32": 15, "sfixed64": 16, "sint32": 17, "sint64": 18,
	} {
		k, ok := ScalarKind(name)
		if !ok || k != want || k.String() != name || !k.IsScalar() {
			t.Errorf("ScalarKind(%q) = %v, %v", name, k, ok)
		}
		if k.IsPackable() == (name == "string" || name == "bytes") {
			t.Errorf("%s: IsPackable = %v", name, k.IsPackable())
		}
	}
	for _, k := range []Kind{KindUnknown, KindGroup, KindMessage, KindEnum} {
		if k.IsScalar() {
			t.Errorf("%v is scalar", k)
		}
	}
	if KindGroup != 10 || KindMessage != 11 || KindEnum != 14 || !KindEnum.IsPackable() || KindMessage.IsPackable() {
		t.Errorf("message kinds")
	}
	if _, ok := ScalarKind("Int32"); ok {
		t.Errorf("scalar names are case-sensitive")
	}
	if Repeated.String() != "repeated" || Cardinality(9).String() != "Cardinality(9)" || Kind(99).String() != "Kind(99)" {
		t.Errorf("String")
	}
}

func TestPool(t *testing.T) {
	f := &File{Path: "a.proto", Package: "p"}
	m := &Message{Base: Base{Name: "M", FullName: "p.M", File: f}}
	fld := &Field{Base: Base{Name: "id", FullName: "p.M.id", File: f}, Number: 1, Kind: KindInt32, JSONName: "id", Parent: m}
	m.Fields = []*Field{fld}
	m.ExtensionRanges = []Range{{Start: 100, End: 199}}
	e := &Enum{Base: Base{Name: "E", FullName: "p.E", File: f}}
	v := &EnumValue{Base: Base{Name: "E_A", FullName: "p.E_A", File: f}, Enum: e, Number: 3}
	e.Values = []*EnumValue{v}
	s := &Service{Base: Base{Name: "S", FullName: "p.S", File: f}}
	meth := &Method{Base: Base{Name: "Get", FullName: "p.S.Get", File: f}, Service: s, Input: m, Output: m}
	s.Methods = []*Method{meth}
	x2 := &Field{Base: Base{Name: "x2", FullName: "p.x2", File: f}, Number: 150, IsExtension: true, Extendee: m}
	x1 := &Field{Base: Base{Name: "x1", FullName: "p.x1", File: f}, Number: 120, IsExtension: true, Extendee: m}
	f.Messages, f.Enums, f.Services, f.Extensions = []*Message{m}, []*Enum{e}, []*Service{s}, []*Field{x2, x1}
	dup := &File{Path: "a.proto"}

	p := NewPool([]*File{f, dup})
	if len(p.Files()) != 2 || p.FindFile("a.proto") != f {
		t.Errorf("files")
	}
	if p.FindMessage(".p.M") != m || p.FindEnum("p.E") != e || p.FindService("p.S") != s || p.Find("p.S.Get") != meth ||
		p.Find("p.E_A") != v || p.FindMessage("p.E") != nil || p.FindExtension("p.M.id") != nil || p.FindExtension("p.x1") != x1 {
		t.Errorf("Find")
	}
	if xs := p.Extensions(m); len(xs) != 2 || xs[0] != x1 || p.FindExtensionByNumber(m, 150) != x2 || p.FindExtensionByNumber(m, 1) != nil {
		t.Errorf("Extensions = %v", xs)
	}
	if len(p.Services()) != 1 || s.MethodByName("Get") != meth || m.FieldByNumber(1) != fld || m.FieldByJSONName("id") != fld ||
		e.ValueByNumber(3) != v || e.ValueByName("E_A") != v || !m.InExtensionRange(199) || m.InExtensionRange(200) {
		t.Errorf("lookups")
	}
	var visited []string
	Walk(f, func(d Descriptor) bool {
		visited = append(visited, d.Desc().FullName)
		return d.Desc().FullName != "p.E"
	})
	want := []string{"p.M", "p.M.id", "p.E", "p.S", "p.S.Get", "p.x2", "p.x1"}
	if len(visited) != len(want) {
		t.Fatalf("Walk = %v", visited)
	}
	for i := range want {
		if visited[i] != want[i] {
			t.Fatalf("Walk = %v", visited)
		}
	}
	if fld.IsMap() || fld.MapKey() != nil {
		t.Errorf("IsMap")
	}
}
