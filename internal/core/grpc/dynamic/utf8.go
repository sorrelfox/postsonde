package dynamic

import (
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/ast"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

// validatesUTF8 reports whether a string field must hold valid UTF-8, as
// protobuf-go decides it: never in proto2, always in proto3, and in
// editions by the utf8_validation feature (VERIFY by default), resolved
// from the field outwards through its messages to the file. The linker
// keeps this feature as an option, so it is looked up here.
func validatesUTF8(fd *desc.Field) bool {
	if fd.Kind != desc.KindString || fd.File == nil {
		return false
	}
	switch fd.File.Syntax {
	case ast.Proto2:
		return false
	case ast.Proto3:
		return true
	case ast.Editions:
	default:
		return true
	}
	// Fields of a map entry take the features of the map field.
	owner := fd
	if p := fd.Parent; p != nil && p.MapEntry && p.Parent != nil {
		for _, x := range p.Parent.Fields {
			if x.Message == p {
				owner = x
				break
			}
		}
	}
	if v, ok := utf8Feature(owner.Options); ok {
		return v
	}
	for m := owner.Parent; m != nil; m = m.Parent {
		if v, ok := utf8Feature(m.Options); ok {
			return v
		}
	}
	if v, ok := utf8Feature(fd.File.Options); ok {
		return v
	}
	return true
}

// utf8Feature finds features.utf8_validation in opts, written either as
// `features.utf8_validation = NONE` or `features = { utf8_validation: NONE }`.
func utf8Feature(opts []*desc.Option) (verify, found bool) {
	for i := len(opts) - 1; i >= 0; i-- {
		o := opts[i]
		if len(o.Path) == 0 || o.Path[0].IsExtension || o.Path[0].Name != "features" {
			continue
		}
		switch {
		case len(o.Path) == 2 && o.Path[1].Name == "utf8_validation" && o.Scalar != nil && o.Scalar.Enum != nil:
			return o.Scalar.Enum.Name != "NONE", true
		case len(o.Path) == 1:
			lit, ok := o.Value.(*ast.MessageLit)
			if !ok {
				continue
			}
			for j := len(lit.Fields) - 1; j >= 0; j-- {
				mf := lit.Fields[j]
				if mf.Name == nil || mf.Name.Kind != ast.FieldRefName || mf.Name.Name == nil ||
					mf.Name.Name.String() != "utf8_validation" {
					continue
				}
				if v, ok := mf.Value.(*ast.IdentLit); ok {
					return v.Value != "NONE", true
				}
			}
		}
	}
	return false, false
}
