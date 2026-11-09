// Package descproto converts between serialized
// google.protobuf.FileDescriptorProto messages, the form in which gRPC
// server reflection and descriptor sets carry schemas, and the linked
// model of package desc.
//
// Link turns FileDescriptorProtos into a linked pool by rendering each one
// as .proto source and linking the sources with package linker, so a schema
// obtained by reflection has exactly the model, checks and diagnostics of
// the same schema loaded from files. Marshal goes the other way, from a
// linked file to FileDescriptorProto bytes.
//
// Messages of descriptor.proto are decoded and encoded with package dynamic
// against the embedded google/protobuf/descriptor.proto.
//
// See docs/grpc-reflection.md.
package descproto

import (
	"context"
	"sync"
	"testing/fstest"

	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
)

const descriptorFile = "google/protobuf/descriptor.proto"

var (
	schemaOnce sync.Once
	schemaPool *desc.Pool
)

// schema returns a pool holding the embedded descriptor.proto, linked once.
func schema() *desc.Pool {
	schemaOnce.Do(func() {
		res, err := linker.Load(context.Background(), linker.Options{
			Files: []string{descriptorFile},
			FS:    fstest.MapFS{},
		})
		if err != nil || res.HasErrors() || res.Pool.FindMessage("google.protobuf.FileDescriptorProto") == nil {
			panic("descproto: the embedded descriptor.proto does not link")
		}
		schemaPool = res.Pool
	})
	return schemaPool
}

// Descriptor types in descriptor.proto.
const (
	typeFile      = "google.protobuf.FileDescriptorProto"
	typeFileSet   = "google.protobuf.FileDescriptorSet"
	optFile       = "google.protobuf.FileOptions"
	optMessage    = "google.protobuf.MessageOptions"
	optField      = "google.protobuf.FieldOptions"
	optOneof      = "google.protobuf.OneofOptions"
	optEnum       = "google.protobuf.EnumOptions"
	optEnumValue  = "google.protobuf.EnumValueOptions"
	optService    = "google.protobuf.ServiceOptions"
	optMethod     = "google.protobuf.MethodOptions"
	optExtRange   = "google.protobuf.ExtensionRangeOptions"
	editionProto2 = 998
	edition2023   = 1000
	edition2024   = 1001
)

// Decoded FileDescriptorProto, as plain structs. Options stay dynamic
// messages of the schema pool's options types.
type fileProto struct {
	name, pkg  string
	deps       []string
	public     map[int]bool
	weak       map[int]bool
	optionDeps []string
	messages   []*messageProto
	enums      []*enumProto
	services   []*serviceProto
	extensions []*fieldProto
	options    *dynamic.Message
	syntax     string
	edition    int32
}

type messageProto struct {
	name           string
	fields         []*fieldProto
	extensions     []*fieldProto
	nested         []*messageProto
	enums          []*enumProto
	extRanges      []extRangeProto
	oneofs         []oneofProto
	options        *dynamic.Message
	reserved       [][2]int32 // end exclusive
	reservedNames  []string
	visibility     int32
	mapEntry       bool
	messageSetWire bool
}

type extRangeProto struct {
	start, end int32 // end exclusive
	options    *dynamic.Message
}

type oneofProto struct {
	name    string
	options *dynamic.Message
}

type fieldProto struct {
	name           string
	number         int32
	label, typ     int32
	typeName       string
	extendee       string
	def            string
	hasDefault     bool
	oneof          int32
	hasOneof       bool
	jsonName       string
	hasJSONName    bool
	options        *dynamic.Message
	proto3Optional bool
}

type enumProto struct {
	name          string
	values        []enumValueProto
	options       *dynamic.Message
	reserved      [][2]int32 // end inclusive
	reservedNames []string
	visibility    int32
}

type enumValueProto struct {
	name    string
	number  int32
	options *dynamic.Message
}

type serviceProto struct {
	name    string
	methods []methodProto
	options *dynamic.Message
}

type methodProto struct {
	name, input, output              string
	clientStreaming, serverStreaming bool
	options                          *dynamic.Message
}

// Field labels and types of FieldDescriptorProto.
const (
	labelOptional = 1
	labelRequired = 2
	labelRepeated = 3
)

// Accessors for dynamic messages of descriptor.proto types. A field name
// that the type does not declare reads as unset.

func field(m *dynamic.Message, name string) *desc.Field {
	if m == nil {
		return nil
	}
	return m.Descriptor().FieldByName(name)
}

func has(m *dynamic.Message, name string) bool {
	fd := field(m, name)
	return fd != nil && m.Has(fd)
}

func getString(m *dynamic.Message, name string) string {
	if fd := field(m, name); fd != nil {
		s, _ := m.Get(fd).(string)
		return s
	}
	return ""
}

func getInt32(m *dynamic.Message, name string) int32 {
	fd := field(m, name)
	if fd == nil {
		return 0
	}
	switch v := m.Get(fd).(type) {
	case int32:
		return v
	case dynamic.EnumNumber:
		return int32(v)
	}
	return 0
}

func getBool(m *dynamic.Message, name string) bool {
	if fd := field(m, name); fd != nil {
		b, _ := m.Get(fd).(bool)
		return b
	}
	return false
}

func getMessage(m *dynamic.Message, name string) *dynamic.Message {
	if fd := field(m, name); fd != nil && m.Has(fd) {
		x, _ := m.Get(fd).(*dynamic.Message)
		return x
	}
	return nil
}

func getList(m *dynamic.Message, name string) []any {
	fd := field(m, name)
	if fd == nil {
		return nil
	}
	l, _ := m.Get(fd).(*dynamic.List)
	out := make([]any, l.Len())
	for i := range out {
		out[i] = l.Get(i)
	}
	return out
}

func getMessages(m *dynamic.Message, name string) []*dynamic.Message {
	items := getList(m, name)
	out := make([]*dynamic.Message, 0, len(items))
	for _, it := range items {
		if x, ok := it.(*dynamic.Message); ok {
			out = append(out, x)
		}
	}
	return out
}

// decodeFile decodes a serialized FileDescriptorProto.
func decodeFile(b []byte) (*fileProto, error) {
	m := dynamic.New(schema().FindMessage(typeFile))
	if err := dynamic.Unmarshal(b, m); err != nil {
		return nil, err
	}
	f := &fileProto{
		name:    getString(m, "name"),
		pkg:     getString(m, "package"),
		options: getMessage(m, "options"),
		syntax:  getString(m, "syntax"),
		edition: getInt32(m, "edition"),
		public:  map[int]bool{},
		weak:    map[int]bool{},
	}
	for _, d := range getList(m, "dependency") {
		f.deps = append(f.deps, d.(string))
	}
	for _, d := range getList(m, "public_dependency") {
		f.public[int(d.(int32))] = true
	}
	for _, d := range getList(m, "weak_dependency") {
		f.weak[int(d.(int32))] = true
	}
	for _, d := range getList(m, "option_dependency") {
		f.optionDeps = append(f.optionDeps, d.(string))
	}
	for _, x := range getMessages(m, "message_type") {
		f.messages = append(f.messages, decodeMessage(x))
	}
	for _, x := range getMessages(m, "enum_type") {
		f.enums = append(f.enums, decodeEnum(x))
	}
	for _, x := range getMessages(m, "service") {
		s := &serviceProto{name: getString(x, "name"), options: getMessage(x, "options")}
		for _, y := range getMessages(x, "method") {
			s.methods = append(s.methods, methodProto{
				name:            getString(y, "name"),
				input:           getString(y, "input_type"),
				output:          getString(y, "output_type"),
				clientStreaming: getBool(y, "client_streaming"),
				serverStreaming: getBool(y, "server_streaming"),
				options:         getMessage(y, "options"),
			})
		}
		f.services = append(f.services, s)
	}
	for _, x := range getMessages(m, "extension") {
		f.extensions = append(f.extensions, decodeField(x))
	}
	return f, nil
}

func decodeMessage(m *dynamic.Message) *messageProto {
	d := &messageProto{
		name:       getString(m, "name"),
		options:    getMessage(m, "options"),
		visibility: getInt32(m, "visibility"),
	}
	d.mapEntry = getBool(d.options, "map_entry")
	d.messageSetWire = getBool(d.options, "message_set_wire_format")
	for _, x := range getMessages(m, "field") {
		d.fields = append(d.fields, decodeField(x))
	}
	for _, x := range getMessages(m, "extension") {
		d.extensions = append(d.extensions, decodeField(x))
	}
	for _, x := range getMessages(m, "nested_type") {
		d.nested = append(d.nested, decodeMessage(x))
	}
	for _, x := range getMessages(m, "enum_type") {
		d.enums = append(d.enums, decodeEnum(x))
	}
	for _, x := range getMessages(m, "extension_range") {
		d.extRanges = append(d.extRanges, extRangeProto{getInt32(x, "start"), getInt32(x, "end"), getMessage(x, "options")})
	}
	for _, x := range getMessages(m, "oneof_decl") {
		d.oneofs = append(d.oneofs, oneofProto{getString(x, "name"), getMessage(x, "options")})
	}
	for _, x := range getMessages(m, "reserved_range") {
		d.reserved = append(d.reserved, [2]int32{getInt32(x, "start"), getInt32(x, "end")})
	}
	for _, x := range getList(m, "reserved_name") {
		d.reservedNames = append(d.reservedNames, x.(string))
	}
	return d
}

func decodeField(m *dynamic.Message) *fieldProto {
	return &fieldProto{
		name:           getString(m, "name"),
		number:         getInt32(m, "number"),
		label:          getInt32(m, "label"),
		typ:            getInt32(m, "type"),
		typeName:       getString(m, "type_name"),
		extendee:       getString(m, "extendee"),
		def:            getString(m, "default_value"),
		hasDefault:     has(m, "default_value"),
		oneof:          getInt32(m, "oneof_index"),
		hasOneof:       has(m, "oneof_index"),
		jsonName:       getString(m, "json_name"),
		hasJSONName:    has(m, "json_name"),
		options:        getMessage(m, "options"),
		proto3Optional: getBool(m, "proto3_optional"),
	}
}

func decodeEnum(m *dynamic.Message) *enumProto {
	e := &enumProto{name: getString(m, "name"), options: getMessage(m, "options"), visibility: getInt32(m, "visibility")}
	for _, x := range getMessages(m, "value") {
		e.values = append(e.values, enumValueProto{getString(x, "name"), getInt32(x, "number"), getMessage(x, "options")})
	}
	for _, x := range getMessages(m, "reserved_range") {
		e.reserved = append(e.reserved, [2]int32{getInt32(x, "start"), getInt32(x, "end")})
	}
	for _, x := range getList(m, "reserved_name") {
		e.reservedNames = append(e.reservedNames, x.(string))
	}
	return e
}

// walkOptions calls fn with every options message of f.
func (f *fileProto) walkOptions(fn func(*dynamic.Message)) {
	visit := func(m *dynamic.Message) {
		if m != nil {
			fn(m)
		}
	}
	visit(f.options)
	var fields func([]*fieldProto)
	fields = func(xs []*fieldProto) {
		for _, x := range xs {
			visit(x.options)
		}
	}
	enums := func(es []*enumProto) {
		for _, e := range es {
			visit(e.options)
			for _, v := range e.values {
				visit(v.options)
			}
		}
	}
	var messages func([]*messageProto)
	messages = func(ms []*messageProto) {
		for _, m := range ms {
			visit(m.options)
			fields(m.fields)
			fields(m.extensions)
			for _, r := range m.extRanges {
				visit(r.options)
			}
			for _, o := range m.oneofs {
				visit(o.options)
			}
			messages(m.nested)
			enums(m.enums)
		}
	}
	messages(f.messages)
	enums(f.enums)
	fields(f.extensions)
	for _, s := range f.services {
		visit(s.options)
		for _, m := range s.methods {
			visit(m.options)
		}
	}
}
