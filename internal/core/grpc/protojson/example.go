package protojson

import (
	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

// Defaults of ExampleOptions.
const (
	// DefaultExampleRecursion is how often a message type is expanded again
	// inside itself.
	DefaultExampleRecursion = 1
	// DefaultExampleDepth is the deepest message nesting expanded.
	DefaultExampleDepth = 16
)

// ExampleOptions tune Example.
type ExampleOptions struct {
	// Recursion is how many times a message type that is already being
	// expanded further up is expanded again (a recursive type such as a
	// tree node); deeper occurrences are left unset. 0 means
	// DefaultExampleRecursion; negative means never again.
	Recursion int
	// MaxDepth is the deepest message nesting that is expanded, the
	// message itself being depth 1. 0 means DefaultExampleDepth.
	MaxDepth int
}

// Example returns a message of type md with every field set to its
// default value, as a template for writing one by hand (GRPC-16):
//
//   - scalars get their default ([default = ...] in proto2, else zero),
//     enums their default or first value;
//   - singular message fields are expanded the same way; well-known types
//     are left empty (a google.protobuf.Value is null), so they show their
//     special JSON form;
//   - repeated fields get one element and map fields one entry, keyed by
//     the key type's zero value;
//   - of each oneof only the first member is set (setting another would
//     clear it); proto3 optional fields are set;
//   - extensions and fields whose type did not resolve are left out;
//   - a message type met again inside itself is expanded only
//     opts.Recursion more times and nothing deeper than opts.MaxDepth;
//     beyond that message fields are unset and lists and maps empty.
//
// Write it with MarshalOptions{EmitDefaultValues: true} so that fields
// holding zero values are shown too.
func Example(md *desc.Message, opts ExampleOptions) *dynamic.Message {
	if opts.Recursion == 0 {
		opts.Recursion = DefaultExampleRecursion
	}
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = DefaultExampleDepth
	}
	g := &exampler{opts: opts, seen: map[*desc.Message]int{}}
	m := dynamic.New(md)
	g.fill(m, 1)
	return m
}

type exampler struct {
	opts ExampleOptions
	// seen counts the messages of each type being expanded on the current
	// path.
	seen map[*desc.Message]int
}

// expandable reports whether a message of type md may be expanded at
// depth.
func (g *exampler) expandable(md *desc.Message, depth int) bool {
	if md == nil || depth > g.opts.MaxDepth {
		return false
	}
	return g.seen[md] <= max(g.opts.Recursion, 0)
}

// fill sets the fields of m, a message at depth.
func (g *exampler) fill(m *dynamic.Message, depth int) {
	md := m.Descriptor()
	if isWellKnown(md) {
		// Empty is the special form's zero value, except for Value, which
		// needs a kind.
		if md.FullName == valueName && checkWellKnownShape(md) == nil {
			if fd := md.FieldByName("null_value"); fd != nil {
				_ = m.Set(fd, dynamic.EnumNumber(0))
			}
		}
		return
	}
	g.seen[md]++
	defer func() { g.seen[md]-- }()
	for _, fd := range md.Fields {
		if o := fd.Oneof; o != nil && !o.Synthetic && o.Fields[0] != fd {
			continue
		}
		g.field(m, fd, depth)
	}
}

// field sets fd of m, a message at depth.
func (g *exampler) field(m *dynamic.Message, fd *desc.Field, depth int) {
	if !resolved(fd) {
		return
	}
	switch {
	case fd.IsMap():
		key, val := fd.MapKey(), fd.MapValue()
		if !resolved(key) || !resolved(val) {
			return
		}
		v, ok := g.value(val, depth+1)
		mp := m.Mutable(fd).(*dynamic.Map)
		if ok {
			_ = mp.Set(scalarDefault(key), v)
		}
	case fd.Cardinality == desc.Repeated:
		v, ok := g.value(fd, depth+1)
		l := m.Mutable(fd).(*dynamic.List)
		if ok {
			_ = l.Append(v)
		}
	case isMessage(fd):
		if v, ok := g.value(fd, depth+1); ok {
			_ = m.Set(fd, v)
		}
	default:
		_ = m.Set(fd, m.Get(fd))
	}
}

// value returns an example value of fd's type (a list element or map
// value for those): false for a message that is not expanded.
func (g *exampler) value(fd *desc.Field, depth int) (any, bool) {
	if !isMessage(fd) {
		return scalarDefault(fd), true
	}
	if !g.expandable(fd.Message, depth) {
		return nil, false
	}
	sub := dynamic.New(fd.Message)
	g.fill(sub, depth)
	return sub, true
}

func isMessage(fd *desc.Field) bool {
	return fd.Kind == desc.KindMessage || fd.Kind == desc.KindGroup
}

// resolved reports whether fd's type is known.
func resolved(fd *desc.Field) bool {
	switch {
	case fd == nil:
		return false
	case isMessage(fd):
		return fd.Message != nil
	case fd.Kind == desc.KindEnum:
		return fd.Enum != nil
	}
	return fd.Kind != 0
}

// scalarDefault is the default of a non-message field, repeated or not:
// its [default = ...] value, zero, or an enum's first value.
func scalarDefault(fd *desc.Field) any {
	if fd.Cardinality != desc.Repeated {
		// A singular field's default, via the dynamic package's rules.
		return dynamic.New(fd.Parent).Get(fd)
	}
	switch fd.Kind {
	case desc.KindDouble:
		return float64(0)
	case desc.KindFloat:
		return float32(0)
	case desc.KindInt32, desc.KindSint32, desc.KindSfixed32:
		return int32(0)
	case desc.KindInt64, desc.KindSint64, desc.KindSfixed64:
		return int64(0)
	case desc.KindUint32, desc.KindFixed32:
		return uint32(0)
	case desc.KindUint64, desc.KindFixed64:
		return uint64(0)
	case desc.KindBool:
		return false
	case desc.KindString:
		return ""
	case desc.KindBytes:
		return []byte{}
	case desc.KindEnum:
		if fd.Enum != nil && len(fd.Enum.Values) > 0 {
			return dynamic.EnumNumber(fd.Enum.Values[0].Number)
		}
		return dynamic.EnumNumber(0)
	}
	return nil
}
