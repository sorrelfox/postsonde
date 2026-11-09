package dynamic

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/protowire"
)

// Decoding limits.
const (
	// DefaultRecursionLimit is how deeply messages may nest, as in
	// protobuf-go. The top-level message counts as one level; map entries
	// count as a level of their own.
	DefaultRecursionLimit = 100
	// DefaultMaxSize is the largest input Unmarshal accepts by default.
	DefaultMaxSize = 64 << 20
)

// Decoding errors, wrapped in *Error.
var (
	ErrInvalidUTF8    = errors.New("string field contains invalid UTF-8")
	ErrRecursionLimit = errors.New("messages nested too deeply")
	ErrTooLarge       = errors.New("input exceeds the size limit")
)

// Error is a decoding or encoding error.
type Error struct {
	// Offset is the byte offset in the input where the failing field or
	// value starts; -1 for encoding errors.
	Offset int
	// Path is where the problem is, starting with the full name of the
	// top-level message: "pkg.Order.items[2].name", "pkg.Order.labels[\"k\"]".
	Path string
	// Err is the cause: a protowire.Err* value, ErrInvalidUTF8,
	// ErrRecursionLimit or ErrTooLarge.
	Err error
}

func (e *Error) Error() string {
	var sb strings.Builder
	sb.WriteString("protobuf: ")
	if e.Offset >= 0 {
		sb.WriteString("decoding ")
	} else {
		sb.WriteString("encoding ")
	}
	sb.WriteString(e.Path)
	if e.Offset >= 0 {
		sb.WriteString(" at offset ")
		sb.WriteString(strconv.Itoa(e.Offset))
	}
	sb.WriteString(": ")
	sb.WriteString(e.Err.Error())
	return sb.String()
}

func (e *Error) Unwrap() error { return e.Err }

// path is a lazily formatted error path.
type path struct {
	parent *path
	name   string // field name, or the root message's full name
	index  int    // list index, or -1
	key    any    // map key; nil if none or not decoded yet
	isMap  bool
}

func (p *path) String() string {
	if p == nil {
		return ""
	}
	var parts []string
	for q := p; q != nil; q = q.parent {
		s := q.name
		switch {
		case q.isMap:
			s += "[" + formatKey(q.key) + "]"
		case q.index >= 0:
			s += "[" + strconv.Itoa(q.index) + "]"
		}
		parts = append(parts, s)
	}
	var sb strings.Builder
	for i := len(parts) - 1; i >= 0; i-- {
		sb.WriteString(parts[i])
		if i > 0 {
			sb.WriteByte('.')
		}
	}
	return sb.String()
}

func child(p *path, fd *desc.Field, index int) *path {
	return &path{parent: p, name: fd.Name, index: index}
}

// ExtensionResolver finds extensions by extendee and number. *desc.Pool
// implements it.
type ExtensionResolver interface {
	FindExtensionByNumber(m *desc.Message, n int32) *desc.Field
}

// UnmarshalOptions configure decoding.
type UnmarshalOptions struct {
	// Merge decodes into m without resetting it first.
	Merge bool
	// Resolver, if set, decodes fields in the message's extension ranges
	// as known extensions. Without it, extensions stay unknown fields.
	Resolver ExtensionResolver
	// DiscardUnknown drops unknown fields instead of keeping them.
	DiscardUnknown bool
	// RecursionLimit is the maximum message nesting; 0 means
	// DefaultRecursionLimit.
	RecursionLimit int
	// MaxSize is the maximum input size in bytes; 0 means DefaultMaxSize,
	// a negative value means no limit.
	MaxSize int
}

// Unmarshal decodes b into m, which is reset first.
func Unmarshal(b []byte, m *Message) error {
	return UnmarshalOptions{}.Unmarshal(b, m)
}

// Unmarshal decodes b into m.
//
// Decoding follows protobuf-go: repeated scalars are accepted packed and
// unpacked whatever the declared encoding; a singular message that occurs
// more than once is merged; for other singular fields and oneofs the last
// occurrence wins; a map entry without a key or value uses the default; a
// known field with the wrong wire type, and an unknown value of a closed
// enum, is kept as an unknown field. On error m holds what was decoded
// before the failing field.
func (o UnmarshalOptions) Unmarshal(b []byte, m *Message) error {
	root := &path{name: m.md.FullName, index: -1}
	limit := o.MaxSize
	if limit == 0 {
		limit = DefaultMaxSize
	}
	if !o.Merge {
		m.Reset()
	}
	if limit > 0 && len(b) > limit {
		return &Error{Offset: 0, Path: root.String(), Err: ErrTooLarge}
	}
	if o.RecursionLimit <= 0 {
		o.RecursionLimit = DefaultRecursionLimit
	}
	d := decoder{o: o}
	return d.message(b, 0, m, root, 1)
}

type decoder struct {
	o UnmarshalOptions
}

func (d *decoder) fail(off int, p *path, err error) error {
	return &Error{Offset: off, Path: p.String(), Err: err}
}

// message decodes the fields in b, which starts at offset base of the input.
func (d *decoder) message(b []byte, base int, m *Message, p *path, depth int) error {
	if depth > d.o.RecursionLimit {
		return d.fail(base, p, ErrRecursionLimit)
	}
	md := m.md
	for pos := 0; pos < len(b); {
		start := pos
		num, typ, n, err := protowire.ConsumeTag(b[pos:])
		if err != nil {
			return d.fail(base+pos, p, err)
		}
		if typ == protowire.EndGroupType {
			return d.fail(base+pos, p, protowire.ErrEndGroup)
		}
		pos += n
		fd := md.FieldByNumber(int32(num))
		if fd == nil && d.o.Resolver != nil && md.InExtensionRange(int32(num)) {
			fd = d.o.Resolver.FindExtensionByNumber(md, int32(num))
			if fd != nil && m.checkField(fd) != nil {
				fd = nil
			}
		}
		if fd != nil && (md.MessageSetWireFormat || m.checkField(fd) != nil) {
			fd = nil
		}
		known := false
		if fd != nil {
			n, known, err = d.field(b, pos, base, typ, m, fd, p, depth)
			if err != nil {
				return err
			}
		}
		if !known {
			n, err = protowire.ConsumeFieldValue(num, typ, b[pos:])
			if err != nil {
				return d.fail(base+start, p, err)
			}
			if !d.o.DiscardUnknown {
				m.unknown = append(m.unknown, b[start:pos+n]...)
			}
		}
		pos += n
	}
	return nil
}

// field decodes the value of fd at b[pos:]. known is false when the value
// belongs in the unknown fields instead (wrong wire type, unknown closed
// enum value); n is then 0.
func (d *decoder) field(b []byte, pos, base int, typ protowire.Type, m *Message, fd *desc.Field, p *path, depth int) (n int, known bool, err error) {
	switch {
	case fd.IsMap():
		return d.mapEntry(b, pos, base, typ, m, fd, p, depth)
	case fd.Cardinality == desc.Repeated:
		return d.listValue(b, pos, base, typ, m, fd, p, depth)
	}
	num := protowire.Number(fd.Number)
	switch fd.Kind {
	case desc.KindMessage, desc.KindGroup:
		body, n, bodyOff, ok, err := d.messageBytes(b, pos, base, num, typ, fd, p)
		if err != nil || !ok {
			return 0, ok, err
		}
		var sub *Message
		if i, found := m.find(fd.Number); found && m.fields[i].fd == fd {
			sub = m.fields[i].v.(*Message)
		} else {
			sub = New(fd.Message)
		}
		if err := d.message(body, bodyOff, sub, child(p, fd, -1), depth+1); err != nil {
			return 0, false, err
		}
		m.store(fd, sub)
		return n, true, nil
	}
	v, n, ok, err := d.scalar(b[pos:], base+pos, typ, fd, child(p, fd, -1))
	if err != nil || !ok {
		return 0, ok, err
	}
	if !fd.HasPresence && isZero(v) {
		// A zero value of an implicit-presence field is the same as no
		// value (such fields are never oneof members).
		m.Clear(fd)
	} else {
		m.store(fd, v)
	}
	return n, true, nil
}

// messageBytes reads the body of a message or group field and returns it
// with its offset in the input.
func (d *decoder) messageBytes(b []byte, pos, base int, num protowire.Number, typ protowire.Type, fd *desc.Field, p *path) (body []byte, n, bodyOff int, ok bool, err error) {
	if fd.Kind == desc.KindGroup {
		if typ != protowire.StartGroupType {
			return nil, 0, 0, false, nil
		}
		body, n, err := protowire.ConsumeGroup(num, b[pos:])
		if err != nil {
			return nil, 0, 0, false, d.fail(base+pos, child(p, fd, -1), err)
		}
		return body, n, base + pos, true, nil
	}
	if typ != protowire.BytesType {
		return nil, 0, 0, false, nil
	}
	body, n, err = protowire.ConsumeBytes(b[pos:])
	if err != nil {
		return nil, 0, 0, false, d.fail(base+pos, child(p, fd, -1), err)
	}
	return body, n, base + pos + n - len(body), true, nil
}

// listValue decodes one occurrence of a repeated field: a single element,
// or a packed run of scalars.
func (d *decoder) listValue(b []byte, pos, base int, typ protowire.Type, m *Message, fd *desc.Field, p *path, depth int) (int, bool, error) {
	num := protowire.Number(fd.Number)
	existing := m.Get(fd).(*List)
	index := existing.Len()
	cp := child(p, fd, index)
	if typ == protowire.BytesType && fd.Kind.IsPackable() {
		run, n, err := protowire.ConsumeBytes(b[pos:])
		if err != nil {
			return 0, false, d.fail(base+pos, cp, err)
		}
		runOff := base + pos + n - len(run)
		elemTyp := wireType(fd.Kind)
		var vals []any
		for q := 0; q < len(run); {
			v, k, ok, err := d.scalar(run[q:], runOff+q, elemTyp, fd, child(p, fd, index+len(vals)))
			if err != nil {
				return 0, false, err
			}
			if !ok {
				// An unknown closed enum value is kept as an unpacked
				// unknown field, as protobuf-go and C++ do.
				raw, _, _ := protowire.ConsumeVarint(run[q:])
				if !d.o.DiscardUnknown {
					m.unknown = protowire.AppendTag(m.unknown, num, protowire.VarintType)
					m.unknown = protowire.AppendVarint(m.unknown, raw)
				}
			} else {
				vals = append(vals, v)
			}
			q += k
		}
		if len(vals) > 0 {
			l := m.Mutable(fd).(*List)
			l.items = append(l.items, vals...)
		}
		return n, true, nil
	}
	switch fd.Kind {
	case desc.KindMessage, desc.KindGroup:
		body, n, bodyOff, ok, err := d.messageBytes(b, pos, base, num, typ, fd, p)
		if err != nil || !ok {
			return 0, ok, err
		}
		sub := New(fd.Message)
		if err := d.message(body, bodyOff, sub, cp, depth+1); err != nil {
			return 0, false, err
		}
		l := m.Mutable(fd).(*List)
		l.items = append(l.items, sub)
		return n, true, nil
	}
	v, n, ok, err := d.scalar(b[pos:], base+pos, typ, fd, cp)
	if err != nil || !ok {
		return 0, ok, err
	}
	l := m.Mutable(fd).(*List)
	l.items = append(l.items, v)
	return n, true, nil
}

// mapEntry decodes one map entry.
func (d *decoder) mapEntry(b []byte, pos, base int, typ protowire.Type, m *Message, fd *desc.Field, p *path, depth int) (int, bool, error) {
	if typ != protowire.BytesType {
		return 0, false, nil
	}
	cp := &path{parent: p, name: fd.Name, isMap: true}
	body, n, err := protowire.ConsumeBytes(b[pos:])
	if err != nil {
		return 0, false, d.fail(base+pos, cp, err)
	}
	if depth+1 > d.o.RecursionLimit {
		return 0, false, d.fail(base+pos, cp, ErrRecursionLimit)
	}
	bodyOff := base + pos + n - len(body)
	kf, vf := fd.MapKey(), fd.MapValue()
	var key, val any
	for q := 0; q < len(body); {
		num, etyp, k, err := protowire.ConsumeTag(body[q:])
		if err != nil {
			return 0, false, d.fail(bodyOff+q, cp, err)
		}
		if etyp == protowire.EndGroupType {
			return 0, false, d.fail(bodyOff+q, cp, protowire.ErrEndGroup)
		}
		q += k
		used := false
		switch num {
		case 1:
			v, k, ok, err := d.scalar(body[q:], bodyOff+q, etyp, kf, cp)
			if err != nil {
				return 0, false, err
			}
			if ok {
				key, used = v, true
				cp.key = key
				q += k
			}
		case 2:
			if vf.Kind == desc.KindMessage || vf.Kind == desc.KindGroup {
				vbody, k, vOff, ok, err := d.messageBytes(body, q, bodyOff, 2, etyp, vf, cp)
				if err != nil {
					return 0, false, err
				}
				if ok {
					sub, isMsg := val.(*Message)
					if !isMsg {
						sub = New(vf.Message)
					}
					if err := d.message(vbody, vOff, sub, cp, depth+2); err != nil {
						return 0, false, err
					}
					val, used = sub, true
					q += k
				}
				break
			}
			v, k, ok, err := d.scalar(body[q:], bodyOff+q, etyp, vf, cp)
			if err != nil {
				return 0, false, err
			}
			if !ok && etyp == wireType(vf.Kind) && vf.Kind == desc.KindEnum {
				// An unknown closed enum value: the whole entry becomes an
				// unknown field.
				return 0, false, nil
			}
			if ok {
				val, used = v, true
				q += k
			}
		}
		if !used {
			k, err := protowire.ConsumeFieldValue(num, etyp, body[q:])
			if err != nil {
				return 0, false, d.fail(bodyOff+q, cp, err)
			}
			q += k
		}
	}
	if key == nil {
		key = defaultValue(kf)
	}
	if val == nil {
		if vf.Kind == desc.KindMessage || vf.Kind == desc.KindGroup {
			val = New(vf.Message)
		} else {
			val = defaultValue(vf)
		}
	}
	mp := m.Mutable(fd).(*Map)
	mp.set(key, val)
	return n, true, nil
}

// wireType returns the wire type of a non-message kind.
func wireType(k desc.Kind) protowire.Type {
	switch k {
	case desc.KindDouble, desc.KindFixed64, desc.KindSfixed64:
		return protowire.Fixed64Type
	case desc.KindFloat, desc.KindFixed32, desc.KindSfixed32:
		return protowire.Fixed32Type
	case desc.KindString, desc.KindBytes, desc.KindMessage:
		return protowire.BytesType
	case desc.KindGroup:
		return protowire.StartGroupType
	}
	return protowire.VarintType
}

// scalar decodes a non-message value of fd. ok is false (with no error)
// if the wire type does not match or the value is an unknown number of a
// closed enum.
func (d *decoder) scalar(b []byte, off int, typ protowire.Type, fd *desc.Field, p *path) (v any, n int, ok bool, err error) {
	if typ != wireType(fd.Kind) {
		return nil, 0, false, nil
	}
	switch typ {
	case protowire.VarintType:
		x, n, err := protowire.ConsumeVarint(b)
		if err != nil {
			return nil, 0, false, d.fail(off, p, err)
		}
		switch fd.Kind {
		case desc.KindInt32:
			v = int32(x)
		case desc.KindInt64:
			v = int64(x)
		case desc.KindUint32:
			v = uint32(x)
		case desc.KindUint64:
			v = x
		case desc.KindSint32:
			v = int32(protowire.DecodeZigZag(x & math.MaxUint32))
		case desc.KindSint64:
			v = protowire.DecodeZigZag(x)
		case desc.KindBool:
			v = protowire.DecodeBool(x)
		case desc.KindEnum:
			e := int32(x)
			if fd.Enum != nil && fd.Enum.Closed && fd.Enum.ValueByNumber(e) == nil {
				return nil, n, false, nil
			}
			v = EnumNumber(e)
		default:
			return nil, 0, false, nil
		}
		return v, n, true, nil
	case protowire.Fixed32Type:
		x, n, err := protowire.ConsumeFixed32(b)
		if err != nil {
			return nil, 0, false, d.fail(off, p, err)
		}
		switch fd.Kind {
		case desc.KindFixed32:
			v = x
		case desc.KindSfixed32:
			v = int32(x)
		default:
			v = math.Float32frombits(x)
		}
		return v, n, true, nil
	case protowire.Fixed64Type:
		x, n, err := protowire.ConsumeFixed64(b)
		if err != nil {
			return nil, 0, false, d.fail(off, p, err)
		}
		switch fd.Kind {
		case desc.KindFixed64:
			v = x
		case desc.KindSfixed64:
			v = int64(x)
		default:
			v = math.Float64frombits(x)
		}
		return v, n, true, nil
	case protowire.BytesType:
		s, n, err := protowire.ConsumeBytes(b)
		if err != nil {
			return nil, 0, false, d.fail(off, p, err)
		}
		switch fd.Kind {
		case desc.KindString:
			if validatesUTF8(fd) && !utf8.Valid(s) {
				return nil, 0, false, d.fail(off, p, ErrInvalidUTF8)
			}
			return string(s), n, true, nil
		case desc.KindBytes:
			return append([]byte{}, s...), n, true, nil
		}
	}
	return nil, 0, false, fmt.Errorf("dynamic: unexpected kind %s", fd.Kind)
}
