package dynamic

import (
	"math"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/protowire"
)

// MarshalOptions configure encoding.
type MarshalOptions struct {
	// Deterministic writes map entries sorted by key (false before true,
	// integers ascending, strings by bytes). Otherwise they are written in
	// insertion order, which after decoding is the wire order.
	Deterministic bool
}

// Marshal encodes m with default options.
func Marshal(m *Message) ([]byte, error) {
	return MarshalOptions{}.MarshalAppend(nil, m)
}

// Marshal encodes m.
func (o MarshalOptions) Marshal(m *Message) ([]byte, error) {
	return o.MarshalAppend(nil, m)
}

// MarshalAppend appends the encoding of m to b.
//
// Fields are written in field number order, extensions included, followed
// by the unknown fields as they were decoded or set. Fields with implicit
// presence are written only when non-zero; repeated scalars use the packed
// encoding when the descriptor says so; map entries always contain both key
// and value. The only error is invalid UTF-8 in a string field that
// requires valid UTF-8.
func (o MarshalOptions) MarshalAppend(b []byte, m *Message) ([]byte, error) {
	e := encoder{o: o}
	p := &path{name: m.md.FullName, index: -1}
	if err := e.check(m, p); err != nil {
		return b, err
	}
	return e.message(b, m), nil
}

// Size returns the encoded size of m.
func Size(m *Message) int { return messageSize(m) }

type encoder struct {
	o MarshalOptions
}

// check validates strings before anything is written, so that encoding
// itself cannot fail half way.
func (e *encoder) check(m *Message, p *path) error {
	for _, f := range m.fields {
		fd := f.fd
		switch v := f.v.(type) {
		case string:
			if validatesUTF8(fd) && !utf8.ValidString(v) {
				return &Error{Offset: -1, Path: child(p, fd, -1).String(), Err: ErrInvalidUTF8}
			}
		case *Message:
			if err := e.check(v, child(p, fd, -1)); err != nil {
				return err
			}
		case *List:
			for i, x := range v.items {
				switch x := x.(type) {
				case string:
					if validatesUTF8(fd) && !utf8.ValidString(x) {
						return &Error{Offset: -1, Path: child(p, fd, i).String(), Err: ErrInvalidUTF8}
					}
				case *Message:
					if err := e.check(x, child(p, fd, i)); err != nil {
						return err
					}
				}
			}
		case *Map:
			kf, vf := fd.MapKey(), fd.MapValue()
			for _, k := range v.keys {
				cp := &path{parent: p, name: fd.Name, isMap: true, key: k}
				if s, ok := k.(string); ok && validatesUTF8(kf) && !utf8.ValidString(s) {
					return &Error{Offset: -1, Path: cp.String(), Err: ErrInvalidUTF8}
				}
				switch x := v.vals[k].(type) {
				case string:
					if validatesUTF8(vf) && !utf8.ValidString(x) {
						return &Error{Offset: -1, Path: cp.String(), Err: ErrInvalidUTF8}
					}
				case *Message:
					if err := e.check(x, cp); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (e *encoder) message(b []byte, m *Message) []byte {
	for _, f := range m.fields {
		b = e.field(b, f.fd, f.v)
	}
	return append(b, m.unknown...)
}

func messageSize(m *Message) int {
	n := len(m.unknown)
	for _, f := range m.fields {
		n += fieldSize(f.fd, f.v)
	}
	return n
}

func (e *encoder) field(b []byte, fd *desc.Field, v any) []byte {
	num := protowire.Number(fd.Number)
	switch v := v.(type) {
	case *List:
		if len(v.items) == 0 {
			return b
		}
		if fd.Packed && fd.Kind.IsPackable() {
			n := 0
			for _, x := range v.items {
				n += scalarSize(fd.Kind, x)
			}
			b = protowire.AppendTag(b, num, protowire.BytesType)
			b = protowire.AppendVarint(b, uint64(n))
			for _, x := range v.items {
				b = appendScalar(b, fd.Kind, x)
			}
			return b
		}
		for _, x := range v.items {
			b = e.singular(b, num, fd.Kind, x)
		}
		return b
	case *Map:
		kf, vf := fd.MapKey(), fd.MapValue()
		for _, k := range v.Keys(e.o.Deterministic) {
			x := v.vals[k]
			b = protowire.AppendTag(b, num, protowire.BytesType)
			b = protowire.AppendVarint(b, uint64(singularSize(1, kf.Kind, k)+singularSize(2, vf.Kind, x)))
			b = e.singular(b, 1, kf.Kind, k)
			b = e.singular(b, 2, vf.Kind, x)
		}
		return b
	}
	return e.singular(b, num, fd.Kind, v)
}

func fieldSize(fd *desc.Field, v any) int {
	num := protowire.Number(fd.Number)
	switch v := v.(type) {
	case *List:
		if len(v.items) == 0 {
			return 0
		}
		n := 0
		if fd.Packed && fd.Kind.IsPackable() {
			for _, x := range v.items {
				n += scalarSize(fd.Kind, x)
			}
			return protowire.SizeTag(num) + protowire.SizeBytes(n)
		}
		for _, x := range v.items {
			n += singularSize(num, fd.Kind, x)
		}
		return n
	case *Map:
		kf, vf := fd.MapKey(), fd.MapValue()
		n := 0
		for k, x := range v.vals {
			n += protowire.SizeTag(num) + protowire.SizeBytes(singularSize(1, kf.Kind, k)+singularSize(2, vf.Kind, x))
		}
		return n
	}
	return singularSize(num, fd.Kind, v)
}

// singular appends one tagged value.
func (e *encoder) singular(b []byte, num protowire.Number, kind desc.Kind, v any) []byte {
	switch kind {
	case desc.KindMessage:
		m := v.(*Message)
		b = protowire.AppendTag(b, num, protowire.BytesType)
		b = protowire.AppendVarint(b, uint64(messageSize(m)))
		return e.message(b, m)
	case desc.KindGroup:
		b = protowire.AppendTag(b, num, protowire.StartGroupType)
		b = e.message(b, v.(*Message))
		return protowire.AppendTag(b, num, protowire.EndGroupType)
	}
	b = protowire.AppendTag(b, num, wireType(kind))
	return appendScalar(b, kind, v)
}

func singularSize(num protowire.Number, kind desc.Kind, v any) int {
	switch kind {
	case desc.KindMessage:
		return protowire.SizeTag(num) + protowire.SizeBytes(messageSize(v.(*Message)))
	case desc.KindGroup:
		return 2*protowire.SizeTag(num) + messageSize(v.(*Message))
	}
	return protowire.SizeTag(num) + scalarSize(kind, v)
}

// appendScalar appends an untagged non-message value.
func appendScalar(b []byte, kind desc.Kind, v any) []byte {
	switch kind {
	case desc.KindDouble:
		return protowire.AppendFixed64(b, math.Float64bits(v.(float64)))
	case desc.KindFloat:
		return protowire.AppendFixed32(b, math.Float32bits(v.(float32)))
	case desc.KindInt32:
		return protowire.AppendVarint(b, uint64(v.(int32)))
	case desc.KindInt64:
		return protowire.AppendVarint(b, uint64(v.(int64)))
	case desc.KindUint32:
		return protowire.AppendVarint(b, uint64(v.(uint32)))
	case desc.KindUint64:
		return protowire.AppendVarint(b, v.(uint64))
	case desc.KindSint32:
		return protowire.AppendVarint(b, protowire.EncodeZigZag(int64(v.(int32)))&math.MaxUint32)
	case desc.KindSint64:
		return protowire.AppendVarint(b, protowire.EncodeZigZag(v.(int64)))
	case desc.KindFixed32:
		return protowire.AppendFixed32(b, v.(uint32))
	case desc.KindSfixed32:
		return protowire.AppendFixed32(b, uint32(v.(int32)))
	case desc.KindFixed64:
		return protowire.AppendFixed64(b, v.(uint64))
	case desc.KindSfixed64:
		return protowire.AppendFixed64(b, uint64(v.(int64)))
	case desc.KindBool:
		return protowire.AppendVarint(b, protowire.EncodeBool(v.(bool)))
	case desc.KindEnum:
		return protowire.AppendVarint(b, uint64(int64(v.(EnumNumber))))
	case desc.KindString:
		return protowire.AppendString(b, v.(string))
	case desc.KindBytes:
		return protowire.AppendBytes(b, v.([]byte))
	}
	panic("dynamic: cannot encode kind " + kind.String())
}

func scalarSize(kind desc.Kind, v any) int {
	switch kind {
	case desc.KindDouble, desc.KindFixed64, desc.KindSfixed64:
		return 8
	case desc.KindFloat, desc.KindFixed32, desc.KindSfixed32:
		return 4
	case desc.KindInt32:
		return protowire.SizeVarint(uint64(v.(int32)))
	case desc.KindInt64:
		return protowire.SizeVarint(uint64(v.(int64)))
	case desc.KindUint32:
		return protowire.SizeVarint(uint64(v.(uint32)))
	case desc.KindUint64:
		return protowire.SizeVarint(v.(uint64))
	case desc.KindSint32:
		return protowire.SizeVarint(protowire.EncodeZigZag(int64(v.(int32))) & math.MaxUint32)
	case desc.KindSint64:
		return protowire.SizeVarint(protowire.EncodeZigZag(v.(int64)))
	case desc.KindBool:
		return 1
	case desc.KindEnum:
		return protowire.SizeVarint(uint64(int64(v.(EnumNumber))))
	case desc.KindString:
		return protowire.SizeBytes(len(v.(string)))
	case desc.KindBytes:
		return protowire.SizeBytes(len(v.([]byte)))
	}
	panic("dynamic: cannot size kind " + kind.String())
}
