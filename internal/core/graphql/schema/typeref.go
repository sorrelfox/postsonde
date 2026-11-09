package schema

import "strings"

// TypeRef is a reference to a type as used by fields, arguments and input
// fields: a named type wrapped in any number of LIST and NON_NULL layers.
// A named reference has Kind and Name and no OfType; a wrapper has Kind
// LIST or NON_NULL and OfType.
type TypeRef struct {
	Kind   Kind
	Name   string
	OfType *TypeRef
}

// Named returns a reference to the named type name of kind kind.
func Named(kind Kind, name string) *TypeRef { return &TypeRef{Kind: kind, Name: name} }

// ListOf returns [t].
func ListOf(t *TypeRef) *TypeRef { return &TypeRef{Kind: KindList, OfType: t} }

// NonNullOf returns t!.
func NonNullOf(t *TypeRef) *TypeRef { return &TypeRef{Kind: KindNonNull, OfType: t} }

// String returns the reference in GraphQL notation, such as "[User!]!".
// A malformed wrapper without OfType prints as "?".
func (t *TypeRef) String() string {
	var b strings.Builder
	t.write(&b)
	return b.String()
}

func (t *TypeRef) write(b *strings.Builder) {
	switch {
	case t == nil:
		b.WriteString("?")
	case t.Kind == KindNonNull:
		t.OfType.write(b)
		b.WriteByte('!')
	case t.Kind == KindList:
		b.WriteByte('[')
		t.OfType.write(b)
		b.WriteByte(']')
	default:
		b.WriteString(t.Name)
	}
}

// NamedType returns the innermost named reference, or nil for a malformed
// reference.
func (t *TypeRef) NamedType() *TypeRef {
	for t != nil && t.Kind.IsWrapper() {
		t = t.OfType
	}
	return t
}

// NamedTypeName returns the name of the innermost named type, or "".
func (t *TypeRef) NamedTypeName() string {
	if n := t.NamedType(); n != nil {
		return n.Name
	}
	return ""
}

// IsNonNull reports whether t is NON_NULL at the outermost layer.
func (t *TypeRef) IsNonNull() bool { return t != nil && t.Kind == KindNonNull }

// IsList reports whether t, ignoring an outer NON_NULL, is a list.
func (t *TypeRef) IsList() bool {
	n := t.Nullable()
	return n != nil && n.Kind == KindList
}

// Nullable returns t without its outer NON_NULL layer.
func (t *TypeRef) Nullable() *TypeRef {
	if t != nil && t.Kind == KindNonNull {
		return t.OfType
	}
	return t
}

// Elem returns the item type of a list reference (ignoring an outer
// NON_NULL), or nil when t is not a list.
func (t *TypeRef) Elem() *TypeRef {
	if n := t.Nullable(); n != nil && n.Kind == KindList {
		return n.OfType
	}
	return nil
}

// Equal reports whether t and u are the same reference.
func (t *TypeRef) Equal(u *TypeRef) bool {
	for t != nil && u != nil {
		if t.Kind != u.Kind || t.Name != u.Name {
			return false
		}
		t, u = t.OfType, u.OfType
	}
	return t == nil && u == nil
}
