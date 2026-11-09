package generate

import (
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
	"github.com/sorrelfox/postsonde/internal/core/graphql/validate"
)

// TargetKind is what a name in a document refers to.
type TargetKind string

// Target kinds.
const (
	TargetType       TargetKind = "type"
	TargetField      TargetKind = "field"
	TargetArgument   TargetKind = "argument"
	TargetInputField TargetKind = "input-field"
	TargetEnumValue  TargetKind = "enum-value"
	TargetDirective  TargetKind = "directive"
)

// Target is the schema item a name refers to, as the docs explorer shows
// it: the page of Type, scrolled to Member; or the directive Directive.
type Target struct {
	Kind TargetKind
	// Type is the type whose page shows the item: the type itself, the
	// type a field belongs to, the input object or enum. Empty for a
	// directive.
	Type string
	// Member is the field, input field or enum value on that page ("" for
	// a type). An argument's member is its field.
	Member string
	// Directive names the directive of a directive or directive argument.
	Directive string
	// Name is the name at the offset (an argument's own name, say).
	Name string
	// Start and End are the name's byte range.
	Start, End int
}

// TargetAt returns what the name at byte offset off of src refers to in s:
// a field (or its alias), an argument of a field or directive, a directive,
// a type in a type condition or variable definition, a fragment spread or
// definition (its type condition), an input object field or an enum value.
// It reports false for anything else and for names s does not know.
func TargetAt(s *schema.Schema, src string, off int) (Target, bool) {
	if s == nil || off < 0 || off > len(src) {
		return Target{}, false
	}
	doc, _ := parser.Parse(src)
	path := ast.NodePathAt(doc, off)
	if len(path) < 2 {
		return Target{}, false
	}
	ti := validate.NewTypeInfo(s)
	switch n := path[len(path)-1].(type) {
	case *ast.EnumValue:
		ti.EnterPath(path)
		if t := s.TypeOf(ti.InputType()); t != nil && t.Kind == schema.KindEnum && t.EnumValue(n.Value) != nil {
			return Target{Kind: TargetEnumValue, Type: t.Name, Member: n.Value, Name: n.Value, Start: n.Start, End: n.End}, true
		}
		return Target{}, false
	case *ast.Name:
		if n.Missing() {
			return Target{}, false
		}
		tg := Target{Name: n.Value, Start: n.Start, End: n.End}
		switch p := path[len(path)-2].(type) {
		case *ast.Field:
			ti.EnterPath(path[:len(path)-1])
			parent, def := ti.ParentType(), ti.FieldDef()
			if parent == nil || def == nil {
				return Target{}, false
			}
			if strings.HasPrefix(def.Name, "__") {
				// Meta fields are not listed: show their type (__Schema, …).
				t := s.TypeOf(def.Type)
				if t == nil {
					return Target{}, false
				}
				tg.Kind, tg.Type = TargetType, t.Name
				return tg, true
			}
			tg.Kind, tg.Type, tg.Member = TargetField, parent.Name, def.Name
			return tg, true
		case *ast.Argument:
			ti.EnterPath(path[:len(path)-1])
			if ti.Argument() == nil {
				return Target{}, false
			}
			tg.Kind = TargetArgument
			if d := ti.Directive(); d != nil {
				tg.Directive = d.Name
				return tg, true
			}
			parent, def := ti.ParentType(), ti.FieldDef()
			if parent == nil || def == nil {
				return Target{}, false
			}
			tg.Type, tg.Member = parent.Name, def.Name
			return tg, true
		case *ast.Directive:
			if s.Directive(n.Value) == nil {
				return Target{}, false
			}
			tg.Kind, tg.Directive = TargetDirective, n.Value
			return tg, true
		case *ast.NamedType:
			if s.Type(n.Value) == nil {
				return Target{}, false
			}
			tg.Kind, tg.Type = TargetType, n.Value
			return tg, true
		case *ast.FragmentSpread:
			if name := fragmentType(doc, n.Value); name != "" && s.Type(name) != nil {
				tg.Kind, tg.Type = TargetType, name
				return tg, true
			}
		case *ast.FragmentDefinition:
			if p.Name == n {
				if name := fragmentType(doc, n.Value); name != "" && s.Type(name) != nil {
					tg.Kind, tg.Type = TargetType, name
					return tg, true
				}
			}
		case *ast.ObjectField:
			ti.EnterPath(path[:len(path)-1])
			if t := s.TypeOf(ti.ParentInputType()); t != nil && t.Kind == schema.KindInputObject && t.InputField(n.Value) != nil {
				tg.Kind, tg.Type, tg.Member = TargetInputField, t.Name, n.Value
				return tg, true
			}
		}
	}
	return Target{}, false
}

// fragmentType returns the type condition of the first fragment named
// name in doc, or "".
func fragmentType(doc *ast.Document, name string) string {
	for _, d := range doc.Definitions {
		f, ok := d.(*ast.FragmentDefinition)
		if !ok || f.Name == nil || f.Name.Value != name {
			continue
		}
		if f.TypeCondition != nil && f.TypeCondition.Type != nil && f.TypeCondition.Type.Name != nil {
			return f.TypeCondition.Type.Name.Value
		}
		return ""
	}
	return ""
}
