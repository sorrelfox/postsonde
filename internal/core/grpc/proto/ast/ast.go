// Package ast defines the syntax tree of a .proto file as produced by
// package parser, for proto2, proto3 and editions files alike.
//
// The tree keeps what a later linker needs to resolve names and build
// descriptors: every name as written (with its leading dot and parts),
// every number with its sign and literal text, options as uninterpreted
// name/value pairs, and declarations in source order. It does no name
// resolution and no option interpretation.
//
// Positions: every node embeds Loc, a half-open byte range [Start, End) into
// File.Source; File.Position converts an offset to line and column.
//
// Comments: declarations embed Comments with the leading, trailing and
// detached comments protoc would record in SourceCodeInfo (see
// docs/proto.md). File.Comments lists every comment token.
//
// Partial trees: the parser builds a tree for invalid input too.
//   - A missing identifier is an *Ident with Value "" whose Loc is empty at
//     the offset where it was expected.
//   - Other missing required children (a field's type or number, an option
//     value) are nil.
//   - A declaration without its closing "}" has RBrace -1 and ends where
//     parsing stopped.
package ast

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Loc is a byte range [Start, End) into the file source.
type Loc struct {
	Start, End int
}

// Pos returns the node's byte range.
func (l Loc) Pos() Loc { return l }

// Node is any syntax tree node.
type Node interface {
	Pos() Loc
}

// Decl is a statement: a top-level declaration or an element of a message,
// enum, service, oneof, extend or rpc body.
//
//   - File: *Syntax, *Package, *Import, *Option, *Message, *Enum, *Service,
//     *Extend, *Empty.
//   - Message: *Field, *MapField, *Group, *Oneof, *Message, *Enum, *Extend,
//     *Extensions, *Reserved, *Option, *Empty.
//   - Oneof: *Field, *MapField, *Group, *Option, *Empty.
//   - Enum: *EnumValue, *Reserved, *Option, *Empty.
//   - Service: *RPC, *Option, *Empty. RPC: *Option, *Empty.
//   - Extend: *Field, *MapField, *Group, *Empty.
//
// Maps in oneofs and extend blocks, and labels in oneofs, parse but are
// reported by parser.Check.
type Decl interface {
	Node
	declNode()
}

// Value is an option value: *IdentLit (an identifier such as true, inf or
// an enum value name, possibly negated), *IntLit, *FloatLit, *StringLit,
// or, in aggregate values only, *MessageLit and *ListLit.
type Value interface {
	Node
	valueNode()
}

// Comments are the comments attached to a declaration, with protoc's
// normalization: "//" and "/*", "*/" removed, a leading "*" removed from
// each continuation line of a block comment, consecutive line comments
// joined, each line comment ending in "\n".
type Comments struct {
	Leading  string
	Trailing string
	Detached []string // comment blocks before Leading, separated by blank lines
}

// Doc returns the comments.
func (c *Comments) Doc() *Comments { return c }

// Comment is one comment token in the source.
type Comment struct {
	Loc
	Block bool // "/* */" rather than "//"
}

// File is a parsed .proto file.
type File struct {
	Loc             // always [0, len(Source))
	Name     string // file name as given to the parser, for messages
	Source   string
	Decls    []Decl
	Comments []Comment

	lines []int // byte offsets of line starts
}

// NewFile returns an empty file over src with its line index built.
func NewFile(name, src string) *File {
	return &File{Loc: Loc{0, len(src)}, Name: name, Source: src, lines: lineStarts(src)}
}

func lineStarts(src string) []int {
	lines := []int{0}
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '\r':
			if i+1 < len(src) && src[i+1] == '\n' {
				i++
			}
			lines = append(lines, i+1)
		case '\n':
			lines = append(lines, i+1)
		}
	}
	return lines
}

// Position is a 1-based line and column. Column counts Unicode code points,
// with an invalid UTF-8 byte counting as one, like lexer.Token.
type Position struct {
	Line, Column int
}

func (p Position) String() string { return strconv.Itoa(p.Line) + ":" + strconv.Itoa(p.Column) }

// Position returns the line and column of byte offset off, clamped to
// [0, len(Source)]. It is O(log lines) for files from NewFile.
func (f *File) Position(off int) Position {
	off = max(0, min(off, len(f.Source)))
	lines := f.lines
	if lines == nil { // built as a literal; do not mutate a shared file
		lines = lineStarts(f.Source)
	}
	line := sort.Search(len(lines), func(i int) bool { return lines[i] > off }) - 1
	return Position{Line: line + 1, Column: utf8.RuneCountInString(f.Source[lines[line]:off]) + 1}
}

// SyntaxKind is the dialect a file is written in.
type SyntaxKind uint8

// Syntax kinds.
const (
	Proto2   SyntaxKind = iota // no syntax statement, or syntax = "proto2"
	Proto3                     // syntax = "proto3"
	Editions                   // edition = "..."
	// UnknownSyntax is a syntax statement with an unsupported value.
	UnknownSyntax
)

func (k SyntaxKind) String() string {
	switch k {
	case Proto2:
		return "proto2"
	case Proto3:
		return "proto3"
	case Editions:
		return "editions"
	case UnknownSyntax:
		return "unknown"
	}
	return "SyntaxKind(" + strconv.Itoa(int(k)) + ")"
}

// SyntaxDecl returns the file's syntax or edition statement, or nil.
func (f *File) SyntaxDecl() *Syntax {
	for _, d := range f.Decls {
		if s, ok := d.(*Syntax); ok {
			return s
		}
	}
	return nil
}

// Syntax returns the file's dialect. A file without a syntax statement is
// proto2, as in protoc.
func (f *File) Syntax() SyntaxKind {
	s := f.SyntaxDecl()
	switch {
	case s == nil:
		return Proto2
	case s.Edition:
		return Editions
	case s.Value == nil:
		return UnknownSyntax
	case s.Value.Value == "proto2":
		return Proto2
	case s.Value.Value == "proto3":
		return Proto3
	}
	return UnknownSyntax
}

// Package returns the file's package name ("" if none; the first one if
// there are several).
func (f *File) Package() string {
	for _, d := range f.Decls {
		if p, ok := d.(*Package); ok && p.Name != nil {
			return p.Name.String()
		}
	}
	return ""
}

// ---- names ----

// Ident is a single identifier. Value is "" for a missing identifier.
type Ident struct {
	Loc
	Value string
}

// Missing reports whether the identifier is a placeholder for an absent one.
func (n *Ident) Missing() bool { return n.Value == "" }

// FullName is a dotted name such as foo.bar.Baz, optionally fully qualified
// with a leading dot. Comments and whitespace may separate the parts.
type FullName struct {
	Loc
	LeadingDot bool
	Parts      []*Ident // at least one
}

// String returns the name as written without whitespace: ".foo.Bar".
func (n *FullName) String() string {
	var b strings.Builder
	for i, p := range n.Parts {
		if i > 0 || n.LeadingDot {
			b.WriteByte('.')
		}
		b.WriteString(p.Value)
	}
	return b.String()
}

// ---- declarations ----

// Syntax is `syntax = "proto3";` or, with Edition set, `edition = "2023";`.
type Syntax struct {
	Loc
	Comments
	Edition bool
	Value   *StringLit
}

// Package is `package foo.bar;`.
type Package struct {
	Loc
	Comments
	Name *FullName
}

// ImportKind is the modifier of an import statement.
type ImportKind uint8

// Import kinds.
const (
	ImportDefault ImportKind = iota
	ImportPublic             // import public
	ImportWeak               // import weak
	ImportOption             // import option (edition 2024)
)

func (k ImportKind) String() string {
	switch k {
	case ImportDefault:
		return "default"
	case ImportPublic:
		return "public"
	case ImportWeak:
		return "weak"
	case ImportOption:
		return "option"
	}
	return "ImportKind(" + strconv.Itoa(int(k)) + ")"
}

// Import is `import "path.proto";` with an optional modifier.
type Import struct {
	Loc
	Comments
	Kind ImportKind
	Path *StringLit
}

// Option is `option name = value;` as a statement, or `name = value` inside
// compact options (then Comments are empty).
type Option struct {
	Loc
	Comments
	Name  *OptionName
	Value Value
}

// OptionName is an option name such as deprecated, (my.ext) or
// (foo.bar).baz.qux.
type OptionName struct {
	Loc
	Parts []*OptionNamePart
}

// String returns the name as written without whitespace: "(foo.bar).baz".
func (n *OptionName) String() string {
	var b strings.Builder
	for i, p := range n.Parts {
		if i > 0 {
			b.WriteByte('.')
		}
		if p.Extension {
			b.WriteByte('(')
			b.WriteString(p.Name.String())
			b.WriteByte(')')
		} else {
			b.WriteString(p.Name.String())
		}
	}
	return b.String()
}

// OptionNamePart is one part of an option name. An extension part is a
// parenthesized, possibly qualified name; a plain part is a single
// identifier (Name has one part and no leading dot).
type OptionNamePart struct {
	Loc
	Extension bool
	Name      *FullName
}

// CompactOptions is a bracketed option list: `[deprecated = true, (a) = 1]`.
type CompactOptions struct {
	Loc
	Options []*Option
}

// Label is a field's cardinality keyword.
type Label uint8

// Labels.
const (
	LabelNone     Label = iota
	LabelOptional       // optional
	LabelRequired       // required
	LabelRepeated       // repeated
)

func (l Label) String() string {
	switch l {
	case LabelNone:
		return ""
	case LabelOptional:
		return "optional"
	case LabelRequired:
		return "required"
	case LabelRepeated:
		return "repeated"
	}
	return "Label(" + strconv.Itoa(int(l)) + ")"
}

// Message is `message Name { ... }`.
type Message struct {
	Loc
	Comments
	Visibility    Visibility
	VisibilityLoc Loc // zero for VisibilityDefault
	Name          *Ident
	RBrace        int // offset of "}", -1 if missing
	Body          []Decl
}

// Visibility is the edition 2024 symbol visibility keyword of a message or
// enum.
type Visibility uint8

// Visibilities.
const (
	VisibilityDefault Visibility = iota
	VisibilityExport             // export
	VisibilityLocal              // local
)

func (v Visibility) String() string {
	switch v {
	case VisibilityDefault:
		return "default"
	case VisibilityExport:
		return "export"
	case VisibilityLocal:
		return "local"
	}
	return "Visibility(" + strconv.Itoa(int(v)) + ")"
}

// Field is a normal field: `[label] Type name = number [options];`. It
// appears in messages, oneofs and extend blocks.
type Field struct {
	Loc
	Comments
	Label    Label
	LabelLoc Loc // zero when Label is LabelNone
	Type     *FullName
	Name     *Ident
	Number   *IntLit
	Options  *CompactOptions // nil without brackets
}

// MapField is `map<Key, Value> name = number [options];`.
type MapField struct {
	Loc
	Comments
	Label    Label // always LabelNone in valid files
	LabelLoc Loc
	KeyType  *FullName
	ValType  *FullName
	Name     *Ident
	Number   *IntLit
	Options  *CompactOptions
}

// Group is a proto2 group: `[label] group Name = number [options] { ... }`.
type Group struct {
	Loc
	Comments
	Label    Label
	LabelLoc Loc
	Name     *Ident
	Number   *IntLit
	Options  *CompactOptions
	RBrace   int
	Body     []Decl
}

// Oneof is `oneof name { ... }`.
type Oneof struct {
	Loc
	Comments
	Name   *Ident
	RBrace int
	Body   []Decl
}

// Enum is `enum Name { ... }`.
type Enum struct {
	Loc
	Comments
	Visibility    Visibility
	VisibilityLoc Loc
	Name          *Ident
	RBrace        int
	Body          []Decl
}

// EnumValue is `NAME = number [options];`.
type EnumValue struct {
	Loc
	Comments
	Name    *Ident
	Number  *IntLit
	Options *CompactOptions
}

// Service is `service Name { ... }`.
type Service struct {
	Loc
	Comments
	Name   *Ident
	RBrace int
	Body   []Decl
}

// RPC is `rpc Name(Req) returns (Resp);` or with a `{ option ...; }` body.
type RPC struct {
	Loc
	Comments
	Name   *Ident
	Input  *RPCType
	Output *RPCType
	LBrace int // offset of the body's "{", -1 for the ";" form
	RBrace int
	Body   []Decl
}

// RPCType is the parenthesized request or response type of an RPC.
type RPCType struct {
	Loc
	Stream    bool
	StreamLoc Loc
	Type      *FullName
}

// Extend is `extend Type { ... }`.
type Extend struct {
	Loc
	Comments
	Extendee *FullName
	RBrace   int
	Body     []Decl
}

// Extensions is `extensions 100 to 199, 500 to max [options];`.
type Extensions struct {
	Loc
	Comments
	Ranges  []*Range
	Options *CompactOptions
}

// Reserved is `reserved 2, 15, 9 to 11;` or `reserved "foo", "bar";` or,
// in editions, `reserved foo, bar;`. A statement mixing numbers and names is
// a syntax error.
type Reserved struct {
	Loc
	Comments
	Ranges []*Range
	Names  []*ReservedName
}

// Range is `n`, `n to m` or `n to max`. To is nil for a single number and
// for max.
type Range struct {
	Loc
	From *IntLit
	To   *IntLit
	Max  bool
}

// ReservedName is a reserved field or value name. Quoted is false for the
// editions identifier form.
type ReservedName struct {
	Loc
	Quoted bool
	Value  string
}

// Empty is a lone ";".
type Empty struct {
	Loc
}

// ---- values ----

// IdentLit is an identifier used as a value, such as true, inf or an enum
// value name, with an optional "-" sign ("-inf").
type IdentLit struct {
	Loc
	Negative bool
	Value    string
}

// IntLit is an integer with an optional sign. Value is the magnitude.
type IntLit struct {
	Loc
	Negative bool
	Raw      string // literal text without the sign
	Value    uint64
}

// Int64 returns the signed value and whether it fits in an int64.
func (n *IntLit) Int64() (int64, bool) {
	if n.Negative {
		if n.Value > 1<<63 {
			return 0, false
		}
		return -int64(n.Value-1) - 1, true
	}
	if n.Value > 1<<63-1 {
		return 0, false
	}
	return int64(n.Value), true
}

// FloatLit is a floating-point number with an optional sign, or an integer
// literal too large for 64 bits. Value includes the sign.
type FloatLit struct {
	Loc
	Negative bool
	Raw      string
	Value    float64
}

// StringLit is one or more adjacent string literals ("a" "b"), concatenated.
// Value holds the decoded bytes.
type StringLit struct {
	Loc
	Value string
}

// MessageLit is a text-format message value: `{ a: 1 b { c: "x" } }` or
// `< ... >`.
type MessageLit struct {
	Loc
	Fields []*MessageLitField
}

// MessageLitField is one `name: value` entry of a message literal. The ":"
// is optional before a message or a list of messages.
type MessageLitField struct {
	Loc
	Name  *FieldRef
	Value Value
}

// FieldRefKind says how a message literal field is named.
type FieldRefKind uint8

// Field reference kinds.
const (
	FieldRefName      FieldRefKind = iota // name
	FieldRefExtension                     // [pkg.ext]
	FieldRefAny                           // [type.googleapis.com/pkg.Type]
)

func (k FieldRefKind) String() string {
	switch k {
	case FieldRefName:
		return "name"
	case FieldRefExtension:
		return "extension"
	case FieldRefAny:
		return "any"
	}
	return "FieldRefKind(" + strconv.Itoa(int(k)) + ")"
}

// FieldRef names a field in a message literal. For FieldRefAny, URLPrefix
// is the part before the last "/" ("type.googleapis.com").
type FieldRef struct {
	Loc
	Kind      FieldRefKind
	URLPrefix string
	Name      *FullName
}

// ListLit is `[v1, v2]` in a message literal.
type ListLit struct {
	Loc
	Elements []Value
}

func (*Syntax) declNode()     {}
func (*Package) declNode()    {}
func (*Import) declNode()     {}
func (*Option) declNode()     {}
func (*Message) declNode()    {}
func (*Field) declNode()      {}
func (*MapField) declNode()   {}
func (*Group) declNode()      {}
func (*Oneof) declNode()      {}
func (*Enum) declNode()       {}
func (*EnumValue) declNode()  {}
func (*Service) declNode()    {}
func (*RPC) declNode()        {}
func (*Extend) declNode()     {}
func (*Extensions) declNode() {}
func (*Reserved) declNode()   {}
func (*Empty) declNode()      {}

func (*IdentLit) valueNode()   {}
func (*IntLit) valueNode()     {}
func (*FloatLit) valueNode()   {}
func (*StringLit) valueNode()  {}
func (*MessageLit) valueNode() {}
func (*ListLit) valueNode()    {}
