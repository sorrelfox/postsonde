package protojson

import (
	"encoding/base64"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

// MarshalOptions configure JSON output.
type MarshalOptions struct {
	// Indent, if not empty, writes one element per line indented by Indent,
	// which may contain only spaces and tabs.
	Indent string
	// UseProtoNames writes the proto field names (or group type names)
	// instead of the lowerCamelCase JSON names.
	UseProtoNames bool
	// UseEnumNumbers writes enum values as numbers.
	UseEnumNumbers bool
	// EmitUnpopulated writes fields that are not set, except oneof members
	// and extensions: null for fields with presence, and the zero value,
	// [] or {} for the others.
	EmitUnpopulated bool
	// EmitDefaultValues writes unset fields without presence (zero values,
	// [] and {}), but not the null of unset fields with presence.
	// EmitUnpopulated takes precedence.
	EmitDefaultValues bool
	// AllowPartial skips the check for missing required fields.
	AllowPartial bool
	// Resolver finds the types of google.protobuf.Any values and the
	// extensions inside them. Without it, a non-empty Any is an error.
	Resolver Resolver
}

// Marshal converts m to JSON with default options.
func Marshal(m *dynamic.Message) ([]byte, error) {
	return MarshalOptions{}.Marshal(m)
}

// Marshal converts m to JSON. A nil message is written as {}.
func (o MarshalOptions) Marshal(m *dynamic.Message) ([]byte, error) {
	w, err := newWriter(o.Indent)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return []byte("{}"), nil
	}
	e := encoder{w: w, o: o}
	if err := e.message(m, ""); err != nil {
		return nil, err
	}
	if !o.AllowPartial {
		if err := checkInitialized(m); err != nil {
			return nil, err
		}
	}
	return w.out, nil
}

type encoder struct {
	w *writer
	o MarshalOptions
}

type fieldEntry struct {
	fd *desc.Field
	v  any // nil writes null
}

// message writes m as an object, with an "@type" member first if typeURL is
// not empty.
func (e *encoder) message(m *dynamic.Message, typeURL string) error {
	md := m.Descriptor()
	if md.MessageSetWireFormat {
		return outputError("%s: message sets are not supported", md.FullName)
	}
	if fn := wellKnownMarshaler(md); fn != nil {
		return fn(e, m)
	}
	var entries []fieldEntry
	emit := e.o.EmitUnpopulated || e.o.EmitDefaultValues
	for _, fd := range md.Fields {
		if m.Has(fd) {
			entries = append(entries, fieldEntry{fd, m.Get(fd)})
			continue
		}
		if !emit || fd.Oneof != nil {
			continue
		}
		if err := checkField(fd); err != nil {
			return err
		}
		if fd.HasPresence {
			if e.o.EmitUnpopulated {
				entries = append(entries, fieldEntry{fd, nil})
			}
			continue
		}
		entries = append(entries, fieldEntry{fd, m.Get(fd)})
	}
	var exts []fieldEntry
	m.Range(func(fd *desc.Field, v any) bool {
		if fd.IsExtension {
			exts = append(exts, fieldEntry{fd, v})
		}
		return true
	})
	slices.SortStableFunc(exts, func(a, b fieldEntry) int { return strings.Compare(a.fd.FullName, b.fd.FullName) })
	entries = append(entries, exts...)

	e.w.startObject()
	if typeURL != "" {
		_ = e.w.name("@type")
		if err := e.w.string(typeURL); err != nil {
			return outputError("%s: type URL contains invalid UTF-8", md.FullName)
		}
	}
	for _, en := range entries {
		name := jsonFieldName(en.fd, e.o.UseProtoNames)
		_ = e.w.name(name)
		if err := e.value(en.fd, en.v); err != nil {
			return err
		}
	}
	e.w.endObject()
	return nil
}

// jsonFieldName returns the name a field is written with.
func jsonFieldName(fd *desc.Field, protoNames bool) string {
	switch {
	case fd.IsExtension:
		return "[" + fd.FullName + "]"
	case protoNames:
		return textName(fd)
	}
	return fd.JSONName
}

// textName is the proto name of a field, or the type name of a group-like
// field (a group whose field name is its type name in lower case, declared
// in the same scope), as in the text format.
func textName(fd *desc.Field) string {
	if isGroupLike(fd) {
		return fd.Message.Name
	}
	return fd.Name
}

func isGroupLike(fd *desc.Field) bool {
	if fd.Kind != desc.KindGroup || fd.Message == nil {
		return false
	}
	if strings.ToLower(fd.Message.Name) != fd.Name || fd.Message.File != fd.File {
		return false
	}
	return fd.Parent == fd.Message.Parent
}

// value writes the value of fd: a list, a map or a singular value.
func (e *encoder) value(fd *desc.Field, v any) error {
	switch {
	case v == nil:
		e.w.null()
		return nil
	case fd.IsMap():
		mp, _ := v.(*dynamic.Map)
		return e.mapValue(fd, mp)
	case fd.Cardinality == desc.Repeated:
		l, _ := v.(*dynamic.List)
		return e.list(fd, l)
	}
	return e.singular(fd, v)
}

func (e *encoder) list(fd *desc.Field, l *dynamic.List) error {
	e.w.startArray()
	for i := range l.Len() {
		if err := e.singular(fd, l.Get(i)); err != nil {
			return err
		}
	}
	e.w.endArray()
	return nil
}

func (e *encoder) mapValue(fd *desc.Field, mp *dynamic.Map) error {
	vfd := fd.MapValue()
	e.w.startObject()
	for _, k := range mp.Keys(true) {
		if err := e.w.name(mapKeyString(k)); err != nil {
			return outputError("%s: map key contains invalid UTF-8", fd.FullName)
		}
		v, _ := mp.Get(k)
		if err := e.singular(vfd, v); err != nil {
			return err
		}
	}
	e.w.endObject()
	return nil
}

func mapKeyString(k any) string {
	switch k := k.(type) {
	case string:
		return k
	case bool:
		return strconv.FormatBool(k)
	case int32:
		return strconv.FormatInt(int64(k), 10)
	case int64:
		return strconv.FormatInt(k, 10)
	case uint32:
		return strconv.FormatUint(uint64(k), 10)
	case uint64:
		return strconv.FormatUint(k, 10)
	}
	return ""
}

// singular writes a scalar, enum or message value of fd.
func (e *encoder) singular(fd *desc.Field, v any) error {
	switch x := v.(type) {
	case bool:
		e.w.bool(x)
	case string:
		if e.w.string(x) != nil {
			return outputError("field %s contains invalid UTF-8", fd.FullName)
		}
	case int32:
		e.w.int(int64(x))
	case uint32:
		e.w.uint(uint64(x))
	case int64:
		_ = e.w.string(strconv.FormatInt(x, 10))
	case uint64:
		_ = e.w.string(strconv.FormatUint(x, 10))
	case float32:
		e.w.float(float64(x), 32)
	case float64:
		e.w.float(x, 64)
	case []byte:
		_ = e.w.string(base64.StdEncoding.EncodeToString(x))
	case dynamic.EnumNumber:
		switch ed := fd.Enum; {
		case ed != nil && ed.FullName == nullValueName:
			e.w.null()
		case e.o.UseEnumNumbers || ed == nil || ed.ValueByNumber(int32(x)) == nil:
			e.w.int(int64(x))
		default:
			_ = e.w.string(ed.ValueByNumber(int32(x)).Name)
		}
	case *dynamic.Message:
		if x == nil {
			e.w.null()
			return nil
		}
		return e.message(x, "")
	default:
		return outputError("field %s holds an unsupported value %T", fd.FullName, v)
	}
	return nil
}

// checkField reports a field whose type or number did not resolve, which
// cannot hold a value.
func checkField(fd *desc.Field) error {
	if fd.Number <= 0 || fd.Kind == desc.KindUnknown ||
		(fd.Kind == desc.KindMessage || fd.Kind == desc.KindGroup) && fd.Message == nil ||
		fd.Kind == desc.KindEnum && fd.Enum == nil ||
		fd.IsMap() && (fd.MapKey() == nil || fd.MapValue() == nil) {
		return &Error{Offset: -1, Message: "field " + fd.FullName + " has an unresolved type or number"}
	}
	return nil
}

// checkInitialized reports the first required field that is not set, in m
// or in the messages it contains (not inside google.protobuf.Any values).
func checkInitialized(m *dynamic.Message) error {
	for _, fd := range m.Descriptor().Fields {
		if fd.Cardinality == desc.Required && !m.Has(fd) {
			return outputError("required field %s not set", fd.FullName)
		}
	}
	var err error
	m.Range(func(fd *desc.Field, v any) bool {
		switch x := v.(type) {
		case *dynamic.Message:
			err = checkInitialized(x)
		case *dynamic.List:
			for i := 0; i < x.Len() && err == nil; i++ {
				if sub, ok := x.Get(i).(*dynamic.Message); ok {
					err = checkInitialized(sub)
				}
			}
		case *dynamic.Map:
			for _, k := range x.Keys(false) {
				if sub, ok := mapGet(x, k).(*dynamic.Message); ok {
					if err = checkInitialized(sub); err != nil {
						break
					}
				}
			}
		}
		return err == nil
	})
	return err
}

func mapGet(mp *dynamic.Map, k any) any {
	v, _ := mp.Get(k)
	return v
}
