package descproto

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/ast"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

// Marshal serializes a linked file as a FileDescriptorProto, the way
// protoc describes it: fully qualified type names with a leading dot,
// json_name on every field, map entry types with map_entry = true, synthetic
// oneofs of proto3 optional fields, and options as their binary encoding.
//
// pool is the pool f was linked in; it resolves extension names inside
// aggregate option values. Options whose name did not resolve are left
// out, as are the options of extension ranges, which the model does not
// keep. Source code info (comments, positions) is not written.
func Marshal(f *desc.File, pool *desc.Pool) ([]byte, error) {
	e := &encoder{pool: pool}
	m := e.file(f)
	if e.err != nil {
		return nil, e.err
	}
	return dynamic.MarshalOptions{Deterministic: true}.Marshal(m)
}

type encoder struct {
	pool *desc.Pool
	err  error
}

func (e *encoder) fail(err error) {
	if e.err == nil && err != nil {
		e.err = err
	}
}

func (e *encoder) set(m *dynamic.Message, name string, v any) {
	fd := field(m, name)
	if fd == nil {
		e.fail(fmt.Errorf("descproto: %s has no field %q", m.Descriptor().FullName, name))
		return
	}
	e.fail(m.Set(fd, v))
}

func (e *encoder) add(m *dynamic.Message, name string, v any) {
	fd := field(m, name)
	if fd == nil {
		e.fail(fmt.Errorf("descproto: %s has no field %q", m.Descriptor().FullName, name))
		return
	}
	e.fail(m.Mutable(fd).(*dynamic.List).Append(v))
}

func (e *encoder) addMessage(m *dynamic.Message, name string) *dynamic.Message {
	return m.Mutable(field(m, name)).(*dynamic.List).AppendMessage()
}

func (e *encoder) file(f *desc.File) *dynamic.Message {
	m := dynamic.New(schema().FindMessage(typeFile))
	e.set(m, "name", f.Path)
	if f.Package != "" {
		e.set(m, "package", f.Package)
	}
	n := int32(0)
	for _, imp := range f.Imports {
		if imp.Kind == ast.ImportOption {
			e.add(m, "option_dependency", imp.Path)
			continue
		}
		e.add(m, "dependency", imp.Path)
		switch imp.Kind {
		case ast.ImportPublic:
			e.add(m, "public_dependency", n)
		case ast.ImportWeak:
			e.add(m, "weak_dependency", n)
		}
		n++
	}
	for _, x := range f.Messages {
		e.message(e.addMessage(m, "message_type"), x)
	}
	for _, x := range f.Enums {
		e.enum(e.addMessage(m, "enum_type"), x)
	}
	for _, s := range f.Services {
		sm := e.addMessage(m, "service")
		e.set(sm, "name", s.Name)
		for _, x := range s.Methods {
			mm := e.addMessage(sm, "method")
			e.set(mm, "name", x.Name)
			e.set(mm, "input_type", "."+nameOf(x.Input, x.InputName))
			e.set(mm, "output_type", "."+nameOf(x.Output, x.OutputName))
			if x.ClientStreaming {
				e.set(mm, "client_streaming", true)
			}
			if x.ServerStreaming {
				e.set(mm, "server_streaming", true)
			}
			e.options(mm, optMethod, x.Options, nil)
		}
		e.options(sm, optService, s.Options, nil)
	}
	for _, x := range f.Extensions {
		e.field(e.addMessage(m, "extension"), x)
	}
	e.options(m, optFile, f.Options, nil)
	switch f.Syntax {
	case ast.Proto3:
		e.set(m, "syntax", "proto3")
	case ast.Editions:
		e.set(m, "syntax", "editions")
		switch f.Edition {
		case "2023":
			e.set(m, "edition", dynamic.EnumNumber(edition2023))
		case "2024":
			e.set(m, "edition", dynamic.EnumNumber(edition2024))
		default:
			e.fail(fmt.Errorf("descproto: unsupported edition %q", f.Edition))
		}
	}
	return m
}

func nameOf(m *desc.Message, written string) string {
	if m != nil {
		return m.FullName
	}
	return strings.TrimPrefix(written, ".")
}

func (e *encoder) message(m *dynamic.Message, x *desc.Message) {
	e.set(m, "name", x.Name)
	oneofIndex := map[*desc.Oneof]int32{}
	for i, o := range x.Oneofs {
		oneofIndex[o] = int32(i)
	}
	for _, f := range x.Fields {
		fm := e.addMessage(m, "field")
		e.field(fm, f)
		if f.Oneof != nil {
			e.set(fm, "oneof_index", oneofIndex[f.Oneof])
		}
	}
	for _, f := range x.Extensions {
		e.field(e.addMessage(m, "extension"), f)
	}
	for _, n := range x.Messages {
		e.message(e.addMessage(m, "nested_type"), n)
	}
	for _, n := range x.Enums {
		e.enum(e.addMessage(m, "enum_type"), n)
	}
	for _, r := range x.ExtensionRanges {
		rm := e.addMessage(m, "extension_range")
		e.set(rm, "start", r.Start)
		e.set(rm, "end", exclusiveEnd(r.End))
	}
	for _, o := range x.Oneofs {
		om := e.addMessage(m, "oneof_decl")
		e.set(om, "name", o.Name)
		e.options(om, optOneof, o.Options, nil)
	}
	for _, r := range x.ReservedRanges {
		rm := e.addMessage(m, "reserved_range")
		e.set(rm, "start", r.Start)
		e.set(rm, "end", exclusiveEnd(r.End))
	}
	for _, n := range x.ReservedNames {
		e.add(m, "reserved_name", n)
	}
	var extra func(*dynamic.Message)
	if x.MapEntry {
		extra = func(opts *dynamic.Message) { e.set(opts, "map_entry", true) }
	}
	e.options(m, optMessage, x.Options, extra)
}

func exclusiveEnd(end int32) int32 {
	if end == math.MaxInt32 {
		return end
	}
	return end + 1
}

func (e *encoder) field(m *dynamic.Message, x *desc.Field) {
	e.set(m, "name", x.Name)
	e.set(m, "number", x.Number)
	// Editions descriptors keep the declaration as written: required and
	// delimited come from features, not from the label and type.
	editions := x.File != nil && x.File.Syntax == ast.Editions
	label := labelOptional
	switch {
	case x.Cardinality == desc.Repeated:
		label = labelRepeated
	case x.Cardinality == desc.Required && !editions:
		label = labelRequired
	}
	e.set(m, "label", dynamic.EnumNumber(label))
	switch {
	case x.Kind == desc.KindGroup && editions:
		e.set(m, "type", dynamic.EnumNumber(desc.KindMessage))
	case x.Kind != desc.KindUnknown:
		e.set(m, "type", dynamic.EnumNumber(x.Kind))
	}
	switch {
	case x.Message != nil:
		e.set(m, "type_name", "."+x.Message.FullName)
	case x.Enum != nil:
		e.set(m, "type_name", "."+x.Enum.FullName)
	case x.TypeName != "":
		e.set(m, "type_name", x.TypeName)
	}
	if x.IsExtension {
		e.set(m, "extendee", "."+nameOf(x.Extendee, x.ExtendeeName))
	}
	if x.Default != nil {
		e.set(m, "default_value", defaultText(x.Default))
	}
	e.set(m, "json_name", x.JSONName)
	if x.Proto3Optional {
		e.set(m, "proto3_optional", true)
	}
	e.options(m, optField, x.Options, nil)
}

// defaultText is protoc's text form of a default value.
func defaultText(v *desc.Value) string {
	switch v.Kind {
	case desc.KindBool:
		return strconv.FormatBool(v.Bool)
	case desc.KindFloat, desc.KindDouble:
		switch {
		case math.IsInf(v.Float, 1):
			return "inf"
		case math.IsInf(v.Float, -1):
			return "-inf"
		case math.IsNaN(v.Float):
			return "nan"
		}
		bits := 64
		if v.Kind == desc.KindFloat {
			bits = 32
		}
		return strconv.FormatFloat(v.Float, 'g', -1, bits)
	case desc.KindString:
		return v.String
	case desc.KindBytes:
		return escapeC([]byte(v.String))
	case desc.KindEnum:
		if v.Enum != nil {
			return v.Enum.Name
		}
		return ""
	case desc.KindUint32, desc.KindUint64, desc.KindFixed32, desc.KindFixed64:
		return strconv.FormatUint(v.Uint, 10)
	}
	return strconv.FormatInt(v.Int, 10)
}

// escapeC is protoc's CEscape.
func escapeC(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch c {
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		case '"':
			sb.WriteString(`\"`)
		case '\'':
			sb.WriteString(`\'`)
		case '\\':
			sb.WriteString(`\\`)
		default:
			if c < 0x20 || c >= 0x7f {
				fmt.Fprintf(&sb, `\%03o`, c)
			} else {
				sb.WriteByte(c)
			}
		}
	}
	return sb.String()
}

func (e *encoder) enum(m *dynamic.Message, x *desc.Enum) {
	e.set(m, "name", x.Name)
	for _, v := range x.Values {
		vm := e.addMessage(m, "value")
		e.set(vm, "name", v.Name)
		e.set(vm, "number", v.Number)
		e.options(vm, optEnumValue, v.Options, nil)
	}
	e.options(m, optEnum, x.Options, nil)
	for _, r := range x.ReservedRanges {
		rm := e.addMessage(m, "reserved_range")
		e.set(rm, "start", r.Start)
		e.set(rm, "end", r.End)
	}
	for _, n := range x.ReservedNames {
		e.add(m, "reserved_name", n)
	}
}

// options sets the options field of m from the linked options. extra adds
// fields the model keeps elsewhere (map_entry).
func (e *encoder) options(m *dynamic.Message, typeName string, opts []*desc.Option, extra func(*dynamic.Message)) {
	var om *dynamic.Message
	get := func(t *desc.Message) *dynamic.Message {
		if om == nil {
			om = dynamic.New(t)
		}
		return om
	}
	for _, o := range opts {
		if len(o.Path) == 0 {
			continue // json_name, default, or an unresolved name
		}
		root := o.Path[0].Parent
		if o.Path[0].IsExtension {
			root = o.Path[0].Extendee
		}
		if root == nil || root.FullName != typeName {
			continue
		}
		e.option(get(root), o)
	}
	if extra != nil {
		extra(get(schema().FindMessage(typeName)))
	}
	if om == nil {
		return
	}
	fd := field(m, "options")
	if fd == nil {
		return
	}
	// The options belong to the file's descriptor.proto; re-decode them as
	// the schema's type so the message fits m's field.
	b, err := dynamic.MarshalOptions{Deterministic: true}.Marshal(om)
	if err != nil {
		e.fail(err)
		return
	}
	target := dynamic.New(fd.Message)
	if err := dynamic.Unmarshal(b, target); err != nil {
		e.fail(err)
		return
	}
	e.fail(m.Set(fd, target))
}

// option sets one resolved option in its options message.
func (e *encoder) option(root *dynamic.Message, o *desc.Option) {
	msg := root
	for _, x := range o.Path[:len(o.Path)-1] {
		if x.Message == nil || x.Cardinality == desc.Repeated {
			return
		}
		sub, ok := msg.Mutable(x).(*dynamic.Message)
		if !ok {
			return
		}
		msg = sub
	}
	last := o.Path[len(o.Path)-1]
	switch {
	case o.Scalar != nil:
		v, ok := fromValue(last, o.Scalar)
		if !ok {
			return
		}
		if last.Cardinality == desc.Repeated {
			e.fail(msg.Mutable(last).(*dynamic.List).Append(v))
		} else {
			e.fail(msg.Set(last, v))
		}
	case last.Message != nil:
		lit, ok := o.Value.(*ast.MessageLit)
		if !ok {
			return
		}
		var target *dynamic.Message
		if last.Cardinality == desc.Repeated {
			if last.IsMap() {
				entry := dynamic.New(last.Message)
				e.aggregate(entry, lit)
				e.putMapEntry(msg, last, entry)
				return
			}
			target = msg.Mutable(last).(*dynamic.List).AppendMessage()
		} else {
			target = msg.Mutable(last).(*dynamic.Message)
		}
		e.aggregate(target, lit)
	}
}

func (e *encoder) putMapEntry(msg *dynamic.Message, fd *desc.Field, entry *dynamic.Message) {
	mp := msg.Mutable(fd).(*dynamic.Map)
	k := entry.Get(fd.MapKey())
	v := entry.Get(fd.MapValue())
	if v == (*dynamic.Message)(nil) {
		v = dynamic.New(fd.MapValue().Message)
	}
	e.fail(mp.Set(k, v))
}

// fromValue converts a checked value to the dynamic value of fd.
func fromValue(fd *desc.Field, v *desc.Value) (any, bool) {
	switch fd.Kind {
	case desc.KindBool:
		return v.Bool, true
	case desc.KindInt32, desc.KindSint32, desc.KindSfixed32:
		return int32(v.Int), true
	case desc.KindInt64, desc.KindSint64, desc.KindSfixed64:
		return v.Int, true
	case desc.KindUint32, desc.KindFixed32:
		return uint32(v.Uint), true
	case desc.KindUint64, desc.KindFixed64:
		return v.Uint, true
	case desc.KindFloat:
		return float32(v.Float), true
	case desc.KindDouble:
		return v.Float, true
	case desc.KindString:
		return v.String, true
	case desc.KindBytes:
		return []byte(v.String), true
	case desc.KindEnum:
		if v.Enum != nil {
			return dynamic.EnumNumber(v.Enum.Number), true
		}
	}
	return nil, false
}

// aggregate fills m from a text-format message literal. Fields and values
// that do not fit are skipped (the linker reported them).
func (e *encoder) aggregate(m *dynamic.Message, lit *ast.MessageLit) {
	md := m.Descriptor()
	for _, mf := range lit.Fields {
		if mf.Name == nil || mf.Name.Name == nil {
			continue
		}
		name := mf.Name.Name.String()
		var fd *desc.Field
		switch mf.Name.Kind {
		case ast.FieldRefName:
			if fd = md.FieldByName(name); fd == nil {
				for _, g := range md.Fields {
					if g.Kind == desc.KindGroup && g.Message != nil && g.Message.Name == name {
						fd = g
					}
				}
			}
		case ast.FieldRefExtension:
			if e.pool != nil {
				if x := e.pool.FindExtension(name); x != nil && x.Extendee == md {
					fd = x
				}
			}
		case ast.FieldRefAny:
			e.anyValue(m, mf)
			continue
		}
		if fd == nil {
			continue
		}
		values := []ast.Value{mf.Value}
		if list, ok := mf.Value.(*ast.ListLit); ok {
			values = list.Elements
		}
		for _, v := range values {
			e.aggregateValue(m, fd, v)
		}
	}
}

func (e *encoder) aggregateValue(m *dynamic.Message, fd *desc.Field, v ast.Value) {
	if sub, ok := v.(*ast.MessageLit); ok {
		if fd.Message == nil {
			return
		}
		switch {
		case fd.IsMap():
			entry := dynamic.New(fd.Message)
			e.aggregate(entry, sub)
			e.putMapEntry(m, fd, entry)
		case fd.Cardinality == desc.Repeated:
			e.aggregate(m.Mutable(fd).(*dynamic.List).AppendMessage(), sub)
		default:
			e.aggregate(m.Mutable(fd).(*dynamic.Message), sub)
		}
		return
	}
	x, ok := literal(fd, v)
	if !ok {
		return
	}
	if fd.Cardinality == desc.Repeated {
		e.fail(m.Mutable(fd).(*dynamic.List).Append(x))
		return
	}
	e.fail(m.Set(fd, x))
}

// anyValue sets google.protobuf.Any fields from [type.url/pkg.Type] { ... }.
func (e *encoder) anyValue(m *dynamic.Message, mf *ast.MessageLitField) {
	sub, ok := mf.Value.(*ast.MessageLit)
	if !ok || e.pool == nil {
		return
	}
	name := mf.Name.Name.String()
	t := e.pool.FindMessage(name)
	if t == nil {
		return
	}
	inner := dynamic.New(t)
	e.aggregate(inner, sub)
	b, err := dynamic.MarshalOptions{Deterministic: true}.Marshal(inner)
	if err != nil {
		e.fail(err)
		return
	}
	md := m.Descriptor()
	if fd := md.FieldByName("type_url"); fd != nil {
		e.fail(m.Set(fd, mf.Name.URLPrefix+"/"+name))
	}
	if fd := md.FieldByName("value"); fd != nil {
		e.fail(m.Set(fd, b))
	}
}

// literal converts a scalar literal of an aggregate to fd's value.
func literal(fd *desc.Field, v ast.Value) (any, bool) {
	switch lit := v.(type) {
	case *ast.IdentLit:
		switch fd.Kind {
		case desc.KindBool:
			switch lit.Value {
			case "true", "True", "t":
				return true, !lit.Negative
			case "false", "False", "f":
				return false, !lit.Negative
			}
		case desc.KindEnum:
			if fd.Enum != nil {
				if ev := fd.Enum.ValueByName(lit.Value); ev != nil && !lit.Negative {
					return dynamic.EnumNumber(ev.Number), true
				}
			}
		case desc.KindFloat, desc.KindDouble:
			var f float64
			switch strings.ToLower(lit.Value) {
			case "inf", "infinity":
				f = math.Inf(1)
			case "nan":
				f = math.NaN()
			default:
				return nil, false
			}
			if lit.Negative {
				f = -f
			}
			if fd.Kind == desc.KindFloat {
				return float32(f), true
			}
			return f, true
		}
	case *ast.IntLit:
		switch fd.Kind {
		case desc.KindInt32, desc.KindSint32, desc.KindSfixed32, desc.KindEnum:
			n, ok := lit.Int64()
			if !ok || n < math.MinInt32 || n > math.MaxInt32 {
				return nil, false
			}
			if fd.Kind == desc.KindEnum {
				return dynamic.EnumNumber(n), true
			}
			return int32(n), true
		case desc.KindInt64, desc.KindSint64, desc.KindSfixed64:
			return lit.Int64()
		case desc.KindUint32, desc.KindFixed32:
			if lit.Negative || lit.Value > math.MaxUint32 {
				return nil, false
			}
			return uint32(lit.Value), true
		case desc.KindUint64, desc.KindFixed64:
			return lit.Value, !lit.Negative
		case desc.KindFloat, desc.KindDouble:
			f := float64(lit.Value)
			if lit.Negative {
				f = -f
			}
			if fd.Kind == desc.KindFloat {
				return float32(f), true
			}
			return f, true
		}
	case *ast.FloatLit:
		f := lit.Value
		if lit.Negative {
			f = -f
		}
		switch fd.Kind {
		case desc.KindFloat:
			return float32(f), true
		case desc.KindDouble:
			return f, true
		}
	case *ast.StringLit:
		switch fd.Kind {
		case desc.KindString:
			return lit.Value, true
		case desc.KindBytes:
			return []byte(lit.Value), true
		}
	}
	return nil, false
}
