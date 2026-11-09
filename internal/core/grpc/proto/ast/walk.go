package ast

import "reflect"

// Inspect traverses the tree rooted at n in depth-first source order. It
// calls f(n) for each non-nil node; if f returns true, Inspect visits the
// node's children. Comments are not nodes and are not visited.
func Inspect(n Node, f func(Node) bool) {
	if isNil(n) || !f(n) {
		return
	}
	visit := func(c Node) {
		if !isNil(c) {
			Inspect(c, f)
		}
	}
	decls := func(ds []Decl) {
		for _, d := range ds {
			visit(d)
		}
	}
	switch n := n.(type) {
	case *File:
		decls(n.Decls)
	case *FullName:
		for _, p := range n.Parts {
			visit(p)
		}
	case *Syntax:
		visit(n.Value)
	case *Package:
		visit(n.Name)
	case *Import:
		visit(n.Path)
	case *Option:
		visit(n.Name)
		visit(n.Value)
	case *OptionName:
		for _, p := range n.Parts {
			visit(p)
		}
	case *OptionNamePart:
		visit(n.Name)
	case *CompactOptions:
		for _, o := range n.Options {
			visit(o)
		}
	case *Message:
		visit(n.Name)
		decls(n.Body)
	case *Field:
		visit(n.Type)
		visit(n.Name)
		visit(n.Number)
		visit(n.Options)
	case *MapField:
		visit(n.KeyType)
		visit(n.ValType)
		visit(n.Name)
		visit(n.Number)
		visit(n.Options)
	case *Group:
		visit(n.Name)
		visit(n.Number)
		visit(n.Options)
		decls(n.Body)
	case *Oneof:
		visit(n.Name)
		decls(n.Body)
	case *Enum:
		visit(n.Name)
		decls(n.Body)
	case *EnumValue:
		visit(n.Name)
		visit(n.Number)
		visit(n.Options)
	case *Service:
		visit(n.Name)
		decls(n.Body)
	case *RPC:
		visit(n.Name)
		visit(n.Input)
		visit(n.Output)
		decls(n.Body)
	case *RPCType:
		visit(n.Type)
	case *Extend:
		visit(n.Extendee)
		decls(n.Body)
	case *Extensions:
		for _, r := range n.Ranges {
			visit(r)
		}
		visit(n.Options)
	case *Reserved:
		for _, r := range n.Ranges {
			visit(r)
		}
		for _, r := range n.Names {
			visit(r)
		}
	case *Range:
		visit(n.From)
		visit(n.To)
	case *MessageLit:
		for _, fl := range n.Fields {
			visit(fl)
		}
	case *MessageLitField:
		visit(n.Name)
		visit(n.Value)
	case *FieldRef:
		visit(n.Name)
	case *ListLit:
		for _, e := range n.Elements {
			visit(e)
		}
	}
}

// isNil reports whether n is nil or a typed nil pointer.
func isNil(n Node) bool {
	if n == nil {
		return true
	}
	v := reflect.ValueOf(n)
	return v.Kind() == reflect.Pointer && v.IsNil()
}
