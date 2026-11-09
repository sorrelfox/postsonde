// Package desc is the linked model of a set of .proto files: files,
// messages, fields, oneofs, enums, services, methods and extensions with
// their types resolved to pointers, in the shape of protoc's descriptors.
// Package linker builds it; the dynamic codec, server reflection and the
// UI tree read it.
//
// The model is immutable by convention: the linker fills every field and
// nothing may modify a descriptor afterwards, so a Pool can be shared
// between goroutines.
//
// Partial input: files with syntax errors or unresolved names still produce
// descriptors. A declaration whose name is missing is left out; a field
// whose type did not resolve has Kind KindUnknown and nil Message and Enum;
// a method whose request or response type did not resolve has a nil Input
// or Output. Callers that need a complete model check the linker's
// diagnostics first.
//
// Names: FullName never has a leading dot ("pkg.Msg.field"). Enum values
// are scoped like C++: the full name of value V of enum pkg.E is pkg.V.
//
// See docs/proto.md.
package desc

import (
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/ast"
)

// Descriptor is any named element: *Message, *Field, *Oneof, *Enum,
// *EnumValue, *Service or *Method.
type Descriptor interface {
	Desc() *Base
}

// Base holds what every named element has.
type Base struct {
	Name     string // simple name
	FullName string // qualified name without a leading dot
	File     *File  // defining file
	// Loc is the declaration's byte range in File.AST.Source and NameLoc
	// its name's. For a map entry message both are the map field's.
	Loc, NameLoc ast.Loc
	Comments     ast.Comments
	// Options are the element's options in source order, with names
	// resolved where possible.
	Options []*Option
	// Deprecated is option deprecated = true.
	Deprecated bool
}

// Desc returns b.
func (b *Base) Desc() *Base { return b }

// Position returns the line and column of the element's name.
func (b *Base) Position() ast.Position {
	if b.File == nil || b.File.AST == nil {
		return ast.Position{}
	}
	return b.File.AST.Position(b.NameLoc.Start)
}

// File is a linked .proto file.
type File struct {
	// Path is the import path: relative to the import root the file was
	// found under, with forward slashes ("google/api/http.proto").
	Path string
	// DiskPath is where the content came from: an OS path, a path in the
	// linker's fs.FS, or "" for a built-in well-known type file.
	DiskPath string
	// Root is the import root Path is relative to ("" for built-in files).
	Root string
	// Builtin marks an embedded well-known type file.
	Builtin bool
	// AST is the parsed file; its Source is the text that was linked.
	AST *ast.File

	Syntax  ast.SyntaxKind
	Edition string // "2023" for edition = "2023"; "" otherwise
	Package string

	Imports    []*Import
	Messages   []*Message // top-level
	Enums      []*Enum    // top-level
	Services   []*Service
	Extensions []*Field // top-level extend blocks
	Options    []*Option
	Deprecated bool
}

// Import is an import statement.
type Import struct {
	Path string
	Kind ast.ImportKind
	// File is the imported file, or nil if it was not found (or would have
	// closed a cycle).
	File *File
	Loc  ast.Loc // the statement
}

// Message is a message type, including the types generated for map fields
// (MapEntry) and groups.
type Message struct {
	Base
	Parent *Message // nil for a top-level message

	// Fields in declaration order, oneof members included; for a map entry,
	// key (1) and value (2).
	Fields []*Field
	// Oneofs in declaration order, followed by the synthetic oneofs of
	// proto3 optional fields.
	Oneofs     []*Oneof
	Messages   []*Message // nested, including map entries and groups
	Enums      []*Enum
	Extensions []*Field // declared in nested extend blocks

	ExtensionRanges []Range
	ReservedRanges  []Range
	ReservedNames   []string

	MapEntry             bool // generated for a map field
	MessageSetWireFormat bool // option message_set_wire_format = true
}

// Range is an inclusive number range.
type Range struct {
	Start, End int32
	Loc        ast.Loc
}

// Contains reports whether n is in the range.
func (r Range) Contains(n int32) bool { return r.Start <= n && n <= r.End }

// FieldByName returns the field named name, or nil.
func (m *Message) FieldByName(name string) *Field {
	for _, f := range m.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// FieldByNumber returns the field with number n, or nil.
func (m *Message) FieldByNumber(n int32) *Field {
	for _, f := range m.Fields {
		if f.Number == n {
			return f
		}
	}
	return nil
}

// FieldByJSONName returns the field whose JSON name is name, or nil.
func (m *Message) FieldByJSONName(name string) *Field {
	for _, f := range m.Fields {
		if f.JSONName == name {
			return f
		}
	}
	return nil
}

// InExtensionRange reports whether n is in one of the extension ranges.
func (m *Message) InExtensionRange(n int32) bool {
	for _, r := range m.ExtensionRanges {
		if r.Contains(n) {
			return true
		}
	}
	return false
}

// Field is a message field or an extension.
type Field struct {
	Base
	Number      int32 // 0 if missing or invalid
	Kind        Kind
	Cardinality Cardinality
	// JSONName is the json_name option or the default lowerCamelCase name;
	// HasJSONName is true when the option is set.
	JSONName    string
	HasJSONName bool

	// Message is the type of a KindMessage or KindGroup field, Enum of a
	// KindEnum field. TypeName is the resolved full name of either, or
	// the name as written if it did not resolve ("" for scalars).
	Message  *Message
	Enum     *Enum
	TypeName string

	// Parent is the message the field belongs to; for an extension, the
	// message whose extend block declares it (nil at the top level).
	Parent *Message
	Oneof  *Oneof // containing oneof, including a synthetic one

	// Extendee is the extended message of an extension (nil if it did not
	// resolve); IsExtension is set for every field of an extend block.
	IsExtension bool
	Extendee    *Message
	// ExtendeeName is the extendee as written.
	ExtendeeName string

	// Proto3Optional is a proto3 field with the optional label.
	Proto3Optional bool
	// HasPresence reports whether the field tracks presence: singular
	// message, oneof member, extension, proto2 or editions EXPLICIT
	// presence, or proto3 optional.
	HasPresence bool
	// Packed is the wire encoding of a repeated scalar field.
	Packed bool

	// Default is the [default = ...] value, nil if not set or invalid.
	Default *Value
}

// IsMap reports whether the field is a map field.
func (f *Field) IsMap() bool {
	return f.Cardinality == Repeated && f.Message != nil && f.Message.MapEntry && f.Kind == KindMessage
}

// MapKey returns the key field of a map field's entry, or nil.
func (f *Field) MapKey() *Field {
	if !f.IsMap() || len(f.Message.Fields) < 2 {
		return nil
	}
	return f.Message.Fields[0]
}

// MapValue returns the value field of a map field's entry, or nil.
func (f *Field) MapValue() *Field {
	if !f.IsMap() || len(f.Message.Fields) < 2 {
		return nil
	}
	return f.Message.Fields[1]
}

// Oneof is a oneof, or the synthetic oneof of a proto3 optional field.
type Oneof struct {
	Base
	Parent    *Message
	Fields    []*Field
	Synthetic bool
}

// Enum is an enum type.
type Enum struct {
	Base
	Parent *Message // nil for a top-level enum
	Values []*EnumValue
	// Closed enums (proto2, or editions enum_type = CLOSED) reject unknown
	// numbers.
	Closed         bool
	ReservedRanges []Range
	ReservedNames  []string
}

// ValueByName returns the value named name, or nil.
func (e *Enum) ValueByName(name string) *EnumValue {
	for _, v := range e.Values {
		if v.Name == name {
			return v
		}
	}
	return nil
}

// ValueByNumber returns the first value with number n, or nil.
func (e *Enum) ValueByNumber(n int32) *EnumValue {
	for _, v := range e.Values {
		if v.Number == n {
			return v
		}
	}
	return nil
}

// EnumValue is an enum value. Its FullName is a sibling of its enum's.
type EnumValue struct {
	Base
	Enum   *Enum
	Number int32
}

// Service is a service.
type Service struct {
	Base
	Methods []*Method
}

// MethodByName returns the method named name, or nil.
func (s *Service) MethodByName(name string) *Method {
	for _, m := range s.Methods {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// Method is an rpc.
type Method struct {
	Base
	Service *Service
	// Input and Output are nil if the type did not resolve to a message.
	Input, Output                    *Message
	InputName, OutputName            string // as resolved, or as written
	ClientStreaming, ServerStreaming bool
}

// Option is one option statement or compact option.
type Option struct {
	// Name is the name as written: "(google.api.http).get".
	Name string
	// Path holds the field or extension each name part resolved to; it is
	// shorter than the name when resolution failed, and empty for
	// json_name and default, which are not options fields.
	Path []*Field
	// Value is the value as written. Scalar is the checked value when the
	// last field is a scalar or enum; nil for messages (aggregate values
	// are kept as written) and for values that failed to check.
	Value  ast.Value
	Scalar *Value
	Loc    ast.Loc
}

// Field returns the last resolved field of the option name, or nil.
func (o *Option) Field() *Field {
	if len(o.Path) == 0 {
		return nil
	}
	return o.Path[len(o.Path)-1]
}

// Value is a checked scalar or enum value.
type Value struct {
	Kind   Kind
	Bool   bool
	Int    int64   // signed integer kinds
	Uint   uint64  // unsigned integer kinds
	Float  float64 // float and double
	String string  // string and bytes (decoded bytes)
	Enum   *EnumValue
}
