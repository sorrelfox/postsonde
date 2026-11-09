// Package dynamic holds Protocol Buffers messages whose type is only known
// at run time, as a linked descriptor from package desc, and encodes and
// decodes them in the binary wire format without generated code.
//
// Values are plain Go values chosen by the field's kind:
//
//	double                       float64
//	float                        float32
//	int32, sint32, sfixed32      int32
//	int64, sint64, sfixed64      int64
//	uint32, fixed32              uint32
//	uint64, fixed64              uint64
//	bool                         bool
//	string                       string
//	bytes                        []byte
//	enum                         EnumNumber
//	message, group               *Message
//	repeated fields              *List of the above
//	map fields                   *Map
//
// A Message is not safe for concurrent modification. Descriptors are shared
// and never modified.
//
// See docs/protobuf-codec.md.
package dynamic

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

// EnumNumber is the value of an enum field. Numbers that the enum does not
// declare are kept for open enums.
type EnumNumber int32

// Message is a message of a type described by a *desc.Message.
type Message struct {
	md *desc.Message
	// fields holds the set fields sorted by number, extensions included.
	fields  []fieldValue
	unknown []byte
}

type fieldValue struct {
	fd *desc.Field
	v  any
}

// New returns an empty message of type md.
func New(md *desc.Message) *Message {
	if md == nil {
		panic("dynamic.New: nil descriptor")
	}
	return &Message{md: md}
}

// Descriptor returns the message type.
func (m *Message) Descriptor() *desc.Message { return m.md }

// Reset clears every field and the unknown fields.
func (m *Message) Reset() {
	m.fields = nil
	m.unknown = nil
}

// Unknown returns the unknown fields as raw wire bytes in the order they
// were decoded. The slice must not be modified.
func (m *Message) Unknown() []byte { return m.unknown }

// SetUnknown replaces the unknown fields. b must be a sequence of complete
// fields; Marshal writes it as it is after the known fields.
func (m *Message) SetUnknown(b []byte) { m.unknown = slices.Clone(b) }

// FieldByName returns the field named name: a field of the message type,
// or a set extension whose full name is name. It returns nil otherwise.
func (m *Message) FieldByName(name string) *desc.Field {
	if fd := m.md.FieldByName(name); fd != nil {
		return fd
	}
	for _, f := range m.fields {
		if f.fd.IsExtension && f.fd.FullName == name {
			return f.fd
		}
	}
	return nil
}

// FieldByNumber returns the field with number n: a field of the message
// type, or a set extension. It returns nil otherwise.
func (m *Message) FieldByNumber(n int32) *desc.Field {
	if fd := m.md.FieldByNumber(n); fd != nil {
		return fd
	}
	if i, ok := m.find(n); ok {
		return m.fields[i].fd
	}
	return nil
}

func (m *Message) find(n int32) (int, bool) {
	return slices.BinarySearchFunc(m.fields, n, func(f fieldValue, n int32) int {
		switch {
		case f.fd.Number < n:
			return -1
		case f.fd.Number > n:
			return 1
		}
		return 0
	})
}

// checkField reports whether fd can be used with m.
func (m *Message) checkField(fd *desc.Field) error {
	switch {
	case fd == nil:
		return errors.New("nil field descriptor")
	case fd.IsExtension:
		if fd.Extendee != m.md {
			return fmt.Errorf("%s does not extend %s", fd.FullName, m.md.FullName)
		}
	case fd.Parent != m.md:
		return fmt.Errorf("%s is not a field of %s", fd.FullName, m.md.FullName)
	}
	if fd.Number <= 0 || fd.Kind == desc.KindUnknown ||
		(fd.Kind == desc.KindMessage || fd.Kind == desc.KindGroup) && fd.Message == nil ||
		fd.Kind == desc.KindEnum && fd.Enum == nil {
		return fmt.Errorf("%s has an unresolved type or number", fd.FullName)
	}
	if fd.IsMap() && (fd.MapKey() == nil || fd.MapValue() == nil) {
		return fmt.Errorf("%s has an invalid map entry", fd.FullName)
	}
	return nil
}

// Has reports whether fd is set: a non-empty list or map, a message or a
// field with explicit presence that was set, or a field with implicit
// presence holding a non-zero value.
func (m *Message) Has(fd *desc.Field) bool {
	if fd == nil {
		return false
	}
	i, ok := m.find(fd.Number)
	return ok && m.fields[i].fd == fd && !isEmptyComposite(m.fields[i].v)
}

// Get returns the value of fd. For an unset scalar or enum it returns the
// default (the [default = ...] value, or zero; the first value for an
// enum); for an unset message, list or map it returns a nil *Message, *List
// or *Map. Use Mutable to create those. Get panics if fd does not belong to
// the message type.
func (m *Message) Get(fd *desc.Field) any {
	if err := m.checkField(fd); err != nil {
		panic("dynamic.Message.Get: " + err.Error())
	}
	if i, ok := m.find(fd.Number); ok && m.fields[i].fd == fd {
		return m.fields[i].v
	}
	switch {
	case fd.IsMap():
		return (*Map)(nil)
	case fd.Cardinality == desc.Repeated:
		return (*List)(nil)
	}
	return defaultValue(fd)
}

// Mutable returns the message, list or map stored in fd, creating and
// setting an empty one if fd is unset. It panics for scalar fields and
// fields of another message type.
func (m *Message) Mutable(fd *desc.Field) any {
	if err := m.checkField(fd); err != nil {
		panic("dynamic.Message.Mutable: " + err.Error())
	}
	if i, ok := m.find(fd.Number); ok && m.fields[i].fd == fd {
		return m.fields[i].v
	}
	var v any
	switch {
	case fd.IsMap():
		v = &Map{fd: fd}
	case fd.Cardinality == desc.Repeated:
		v = &List{fd: fd}
	case fd.Kind == desc.KindMessage || fd.Kind == desc.KindGroup:
		v = New(fd.Message)
	default:
		panic("dynamic.Message.Mutable: " + fd.FullName + " is a scalar field")
	}
	m.store(fd, v)
	return v
}

// Set sets fd to v, which must have the Go type of the field's kind (see
// the package documentation), or be a *List or *Map created for fd. Setting
// a member of a oneof clears the other members. Setting a field with
// implicit presence to its zero value clears it.
func (m *Message) Set(fd *desc.Field, v any) error {
	if err := m.checkField(fd); err != nil {
		return err
	}
	switch {
	case fd.IsMap():
		mv, ok := v.(*Map)
		if !ok || mv == nil || mv.fd != fd {
			return fmt.Errorf("%s: want a *dynamic.Map for this field, got %T", fd.FullName, v)
		}
	case fd.Cardinality == desc.Repeated:
		lv, ok := v.(*List)
		if !ok || lv == nil || lv.fd != fd {
			return fmt.Errorf("%s: want a *dynamic.List for this field, got %T", fd.FullName, v)
		}
	default:
		if err := checkValue(fd, v); err != nil {
			return err
		}
		if !fd.HasPresence && isZero(v) {
			m.Clear(fd)
			return nil
		}
	}
	m.store(fd, v)
	return nil
}

// store sets fd without checks, clearing other oneof members.
func (m *Message) store(fd *desc.Field, v any) {
	if o := fd.Oneof; o != nil && !o.Synthetic {
		for _, other := range o.Fields {
			if other != fd {
				m.Clear(other)
			}
		}
	}
	i, ok := m.find(fd.Number)
	if ok {
		m.fields[i] = fieldValue{fd, v}
		return
	}
	m.fields = slices.Insert(m.fields, i, fieldValue{fd, v})
}

// Clear unsets fd.
func (m *Message) Clear(fd *desc.Field) {
	if fd == nil {
		return
	}
	if i, ok := m.find(fd.Number); ok && m.fields[i].fd == fd {
		m.fields = slices.Delete(m.fields, i, i+1)
	}
}

// WhichOneof returns the set member of o, or nil.
func (m *Message) WhichOneof(o *desc.Oneof) *desc.Field {
	if o == nil {
		return nil
	}
	for _, fd := range o.Fields {
		if m.Has(fd) {
			return fd
		}
	}
	return nil
}

// Range calls fn for every set field in declaration order, followed by set
// extensions by number, until fn returns false. Empty lists and maps are
// skipped. fn must not modify m.
func (m *Message) Range(fn func(fd *desc.Field, v any) bool) {
	for _, fd := range m.md.Fields {
		if fd.Number <= 0 {
			continue
		}
		i, ok := m.find(fd.Number)
		if !ok || m.fields[i].fd != fd || isEmptyComposite(m.fields[i].v) {
			continue
		}
		if !fn(fd, m.fields[i].v) {
			return
		}
	}
	for _, f := range m.fields {
		if f.fd.IsExtension && !isEmptyComposite(f.v) {
			if !fn(f.fd, f.v) {
				return
			}
		}
	}
}

func isEmptyComposite(v any) bool {
	switch v := v.(type) {
	case *List:
		return v.Len() == 0
	case *Map:
		return v.Len() == 0
	}
	return false
}

// Clone returns a deep copy of m.
func (m *Message) Clone() *Message {
	if m == nil {
		return nil
	}
	c := &Message{md: m.md, unknown: slices.Clone(m.unknown), fields: make([]fieldValue, len(m.fields))}
	for i, f := range m.fields {
		c.fields[i] = fieldValue{f.fd, cloneValue(f.v)}
	}
	return c
}

func cloneValue(v any) any {
	switch v := v.(type) {
	case []byte:
		return slices.Clone(v)
	case *Message:
		return v.Clone()
	case *List:
		l := &List{fd: v.fd, items: make([]any, len(v.items))}
		for i, x := range v.items {
			l.items[i] = cloneValue(x)
		}
		return l
	case *Map:
		c := &Map{fd: v.fd, keys: slices.Clone(v.keys), vals: make(map[any]any, len(v.vals))}
		for k, x := range v.vals {
			c.vals[k] = cloneValue(x)
		}
		return c
	}
	return v
}

// List is the value of a repeated field.
type List struct {
	fd    *desc.Field
	items []any
}

// Field returns the repeated field the list belongs to.
func (l *List) Field() *desc.Field { return l.fd }

// Len returns the number of elements; 0 for a nil list.
func (l *List) Len() int {
	if l == nil {
		return 0
	}
	return len(l.items)
}

// Get returns element i.
func (l *List) Get(i int) any { return l.items[i] }

// Set replaces element i.
func (l *List) Set(i int, v any) error {
	if err := checkValue(l.fd, v); err != nil {
		return err
	}
	l.items[i] = v
	return nil
}

// Append adds v at the end.
func (l *List) Append(v any) error {
	if err := checkValue(l.fd, v); err != nil {
		return err
	}
	l.items = append(l.items, v)
	return nil
}

// AppendMessage appends a new empty message to a list of messages and
// returns it.
func (l *List) AppendMessage() *Message {
	if l.fd.Message == nil {
		panic("dynamic.List.AppendMessage: " + l.fd.FullName + " is not a message field")
	}
	x := New(l.fd.Message)
	l.items = append(l.items, x)
	return x
}

// Truncate keeps the first n elements.
func (l *List) Truncate(n int) {
	clear(l.items[n:])
	l.items = l.items[:n]
}

// Map is the value of a map field. It remembers insertion order, which is
// the wire order after decoding.
type Map struct {
	fd   *desc.Field
	keys []any
	vals map[any]any
}

// Field returns the map field the map belongs to.
func (mp *Map) Field() *desc.Field { return mp.fd }

// Len returns the number of entries; 0 for a nil map.
func (mp *Map) Len() int {
	if mp == nil {
		return 0
	}
	return len(mp.keys)
}

// Get returns the value for key.
func (mp *Map) Get(key any) (any, bool) {
	if mp == nil {
		return nil, false
	}
	v, ok := mp.vals[key]
	return v, ok
}

// Set sets the value for key. An existing key keeps its position.
func (mp *Map) Set(key, v any) error {
	if err := checkValue(mp.fd.MapKey(), key); err != nil {
		return err
	}
	if err := checkValue(mp.fd.MapValue(), v); err != nil {
		return err
	}
	mp.set(key, v)
	return nil
}

func (mp *Map) set(key, v any) {
	if mp.vals == nil {
		mp.vals = map[any]any{}
	}
	if _, ok := mp.vals[key]; !ok {
		mp.keys = append(mp.keys, key)
	}
	mp.vals[key] = v
}

// Delete removes key.
func (mp *Map) Delete(key any) {
	if _, ok := mp.vals[key]; !ok {
		return
	}
	delete(mp.vals, key)
	mp.keys = slices.DeleteFunc(mp.keys, func(k any) bool { return k == key })
}

// Keys returns the keys in insertion order, or sorted when sorted is true:
// false before true, integers in ascending order, strings by bytes.
func (mp *Map) Keys(sorted bool) []any {
	if mp == nil {
		return nil
	}
	keys := slices.Clone(mp.keys)
	if sorted {
		slices.SortFunc(keys, compareKeys)
	}
	return keys
}

func compareKeys(a, b any) int {
	switch a := a.(type) {
	case bool:
		switch {
		case a == b.(bool):
			return 0
		case !a:
			return -1
		}
		return 1
	case int32:
		return cmpOrdered(a, b.(int32))
	case int64:
		return cmpOrdered(a, b.(int64))
	case uint32:
		return cmpOrdered(a, b.(uint32))
	case uint64:
		return cmpOrdered(a, b.(uint64))
	case string:
		return cmpOrdered(a, b.(string))
	}
	return 0
}

func cmpOrdered[T int32 | int64 | uint32 | uint64 | string](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// checkValue checks that v is a valid singular value (or list element, or
// map key or value) for fd.
func checkValue(fd *desc.Field, v any) error {
	ok := false
	switch fd.Kind {
	case desc.KindDouble:
		_, ok = v.(float64)
	case desc.KindFloat:
		_, ok = v.(float32)
	case desc.KindInt32, desc.KindSint32, desc.KindSfixed32:
		_, ok = v.(int32)
	case desc.KindInt64, desc.KindSint64, desc.KindSfixed64:
		_, ok = v.(int64)
	case desc.KindUint32, desc.KindFixed32:
		_, ok = v.(uint32)
	case desc.KindUint64, desc.KindFixed64:
		_, ok = v.(uint64)
	case desc.KindBool:
		_, ok = v.(bool)
	case desc.KindString:
		_, ok = v.(string)
	case desc.KindBytes:
		_, ok = v.([]byte)
	case desc.KindEnum:
		_, ok = v.(EnumNumber)
	case desc.KindMessage, desc.KindGroup:
		x, isMsg := v.(*Message)
		if isMsg && x != nil && x.md != fd.Message {
			return fmt.Errorf("%s: want a message of type %s, got %s", fd.FullName, fd.TypeName, x.md.FullName)
		}
		ok = isMsg && x != nil
	}
	if !ok {
		return fmt.Errorf("%s: a %s field cannot hold a %T", fd.FullName, fd.Kind, v)
	}
	return nil
}

func isZero(v any) bool {
	switch v := v.(type) {
	case float64:
		return math.Float64bits(v) == 0
	case float32:
		return math.Float32bits(v) == 0
	case int32:
		return v == 0
	case int64:
		return v == 0
	case uint32:
		return v == 0
	case uint64:
		return v == 0
	case bool:
		return !v
	case string:
		return v == ""
	case []byte:
		return len(v) == 0
	case EnumNumber:
		return v == 0
	}
	return false
}

// defaultValue returns the default of a singular non-message field, or a
// nil *Message.
func defaultValue(fd *desc.Field) any {
	d := fd.Default
	switch fd.Kind {
	case desc.KindDouble:
		if d != nil {
			return d.Float
		}
		return float64(0)
	case desc.KindFloat:
		if d != nil {
			return float32(d.Float)
		}
		return float32(0)
	case desc.KindInt32, desc.KindSint32, desc.KindSfixed32:
		if d != nil {
			return int32(d.Int)
		}
		return int32(0)
	case desc.KindInt64, desc.KindSint64, desc.KindSfixed64:
		if d != nil {
			return d.Int
		}
		return int64(0)
	case desc.KindUint32, desc.KindFixed32:
		if d != nil {
			return uint32(d.Uint)
		}
		return uint32(0)
	case desc.KindUint64, desc.KindFixed64:
		if d != nil {
			return d.Uint
		}
		return uint64(0)
	case desc.KindBool:
		return d != nil && d.Bool
	case desc.KindString:
		if d != nil {
			return d.String
		}
		return ""
	case desc.KindBytes:
		if d != nil {
			return []byte(d.String)
		}
		return []byte(nil)
	case desc.KindEnum:
		if d != nil && d.Enum != nil {
			return EnumNumber(d.Enum.Number)
		}
		if fd.Enum != nil && len(fd.Enum.Values) > 0 {
			return EnumNumber(fd.Enum.Values[0].Number)
		}
		return EnumNumber(0)
	case desc.KindMessage, desc.KindGroup:
		return (*Message)(nil)
	}
	return nil
}

// formatKey formats a map key for error paths.
func formatKey(k any) string {
	switch k := k.(type) {
	case string:
		return strconv.Quote(k)
	case nil:
		return "?"
	}
	return fmt.Sprint(k)
}
