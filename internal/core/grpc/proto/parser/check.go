package parser

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/ast"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/lexer"
)

// Field number limits.
const (
	MaxFieldNumber        = 536870911 // 2^29 - 1
	FirstReservedNumber   = 19000     // reserved for the protobuf implementation
	LastReservedNumber    = 19999
	maxMessageSetExtRange = math.MaxInt32
)

// Severity says whether a diagnostic makes the file invalid.
type Severity uint8

// Severities.
const (
	SeverityError Severity = iota
	SeverityWarning
)

func (s Severity) String() string {
	if s == SeverityWarning {
		return "warning"
	}
	return "error"
}

// DiagCode classifies a diagnostic from Check.
type DiagCode uint8

// Diagnostic codes.
const (
	// DiagSyntax is a missing syntax statement (a warning; the file is
	// proto2), an unknown syntax or an unsupported edition.
	DiagSyntax DiagCode = iota + 1
	// DiagDuplicateDecl is a second package statement or a repeated import.
	DiagDuplicateDecl
	// DiagFieldNumber is a field number outside 1 to 536,870,911 or in the
	// implementation-reserved 19000 to 19999.
	DiagFieldNumber
	// DiagDuplicateNumber is a field number used twice in one message.
	DiagDuplicateNumber
	// DiagDuplicateName is a name defined twice in one scope. Enum values
	// are siblings of their enum (C++ scoping), map fields define a
	// <Name>Entry type and groups define a type and a lower-case field.
	DiagDuplicateName
	// DiagReserved is a field or enum value using a reserved number or name.
	DiagReserved
	// DiagRange is an invalid reserved or extension range: out of bounds,
	// end before start, overlapping another range, or an extension range
	// containing a field.
	DiagRange
	// DiagLabel is a label where it is not allowed (required in proto3,
	// optional or required in editions, any label in a oneof or on a map, a
	// required extension) or a missing label in proto2.
	DiagLabel
	// DiagGroup is a group outside proto2, or a group name that does not
	// start with a capital letter.
	DiagGroup
	// DiagDefault is an explicit default value in proto3.
	DiagDefault
	// DiagExtensions is an extension range in proto3.
	DiagExtensions
	// DiagMapField is a map key type other than an integral type, bool or
	// string, or a map field in a oneof or an extend block.
	DiagMapField
	// DiagEmptyOneof is a oneof without fields.
	DiagEmptyOneof
	// DiagEnumValue is an empty enum, a non-zero first value where zero is
	// required, a number outside int32, a duplicate number without
	// allow_alias, or allow_alias without any alias.
	DiagEnumValue
	// DiagReservedName is a reserved name in the wrong form for the syntax
	// (quoted in editions, unquoted otherwise) or not an identifier.
	DiagReservedName
	// DiagVisibility is export or local before edition 2024.
	DiagVisibility
)

var diagCodeNames = [...]string{
	DiagSyntax:          "syntax",
	DiagDuplicateDecl:   "duplicate declaration",
	DiagFieldNumber:     "field number",
	DiagDuplicateNumber: "duplicate number",
	DiagDuplicateName:   "duplicate name",
	DiagReserved:        "reserved",
	DiagRange:           "range",
	DiagLabel:           "label",
	DiagGroup:           "group",
	DiagDefault:         "default value",
	DiagExtensions:      "extensions",
	DiagMapField:        "map field",
	DiagEmptyOneof:      "empty oneof",
	DiagEnumValue:       "enum value",
	DiagReservedName:    "reserved name",
	DiagVisibility:      "visibility",
}

func (c DiagCode) String() string {
	if int(c) < len(diagCodeNames) && diagCodeNames[c] != "" {
		return diagCodeNames[c]
	}
	return "DiagCode(" + strconv.Itoa(int(c)) + ")"
}

// Diagnostic is a problem Check finds in a syntactically valid tree, with
// the byte range and 1-based line and column it applies to.
type Diagnostic struct {
	Code         DiagCode
	Severity     Severity
	Start, End   int
	Line, Column int
	File         string
	Message      string
}

// Error returns "file:line:column: message", with "warning: " before the
// message for warnings.
func (d Diagnostic) Error() string {
	if d.Severity == SeverityWarning {
		return location(d.File, d.Line, d.Column) + ": warning: " + d.Message
	}
	return location(d.File, d.Line, d.Column) + ": " + d.Message
}

// Check runs the checks that need only this file: field number ranges,
// duplicate numbers and names per scope, reserved numbers and names,
// ranges, labels and groups per syntax, map key types, oneofs, enum values
// and aliases. It skips what the parser left missing, so it can run on a
// partial tree; its diagnostics are separate from syntax errors. Checks
// that need imported files (type resolution, option interpretation, JSON
// name conflicts, extension number clashes) belong to the linker.
// Diagnostics are sorted by position.
func Check(f *ast.File) []Diagnostic {
	c := &checker{file: f, syntax: f.Syntax()}
	if s := f.SyntaxDecl(); s != nil && s.Edition && s.Value != nil {
		c.edition = s.Value.Value
	}
	c.checkFile()
	slices.SortStableFunc(c.diags, func(a, b Diagnostic) int { return cmp.Compare(a.Start, b.Start) })
	for i := range c.diags {
		pos := f.Position(c.diags[i].Start)
		c.diags[i].Line, c.diags[i].Column, c.diags[i].File = pos.Line, pos.Column, f.Name
	}
	return c.diags
}

type checker struct {
	file    *ast.File
	syntax  ast.SyntaxKind
	edition string
	diags   []Diagnostic
}

func (c *checker) report(code DiagCode, n ast.Node, format string, args ...any) {
	c.reportSev(code, SeverityError, n.Pos(), format, args...)
}

func (c *checker) reportSev(code DiagCode, sev Severity, l ast.Loc, format string, args ...any) {
	c.diags = append(c.diags, Diagnostic{Code: code, Severity: sev, Start: l.Start, End: l.End,
		Message: fmt.Sprintf(format, args...)})
}

func (c *checker) syntaxName() string {
	if c.syntax == ast.Editions {
		return "editions"
	}
	return c.syntax.String()
}

// scope detects duplicate names among the symbols of one message or file.
type scope struct {
	name string // "message \"pkg.Msg\"" or "package \"pkg\""
	defs map[string]string
}

func newScope(name string) *scope { return &scope{name: name, defs: map[string]string{}} }

// define adds name, described by what, or reports a duplicate at n.
func (c *checker) define(s *scope, name string, n ast.Node, what string) {
	if name == "" {
		return
	}
	prev, dup := s.defs[name]
	if !dup {
		s.defs[name] = what
		return
	}
	c.report(DiagDuplicateName, n, "%q is already defined in %s (as %s)", name, s.name, prev)
}

func (c *checker) defineIdent(s *scope, id *ast.Ident, what string) {
	if id != nil && !id.Missing() {
		c.define(s, id.Value, id, what)
	}
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// ---- file ----

func (c *checker) checkFile() {
	f := c.file
	switch s := f.SyntaxDecl(); {
	case s == nil:
		c.reportSev(DiagSyntax, SeverityWarning, ast.Loc{}, `no syntax specified; the file is treated as proto2 (add syntax = "proto2"; or syntax = "proto3";)`)
	case s.Value == nil:
	case s.Edition && s.Value.Value != "2023" && s.Value.Value != "2024":
		c.report(DiagSyntax, s.Value, "edition %q is not supported; supported editions are 2023 and 2024", s.Value.Value)
	case !s.Edition && c.syntax == ast.UnknownSyntax:
		c.report(DiagSyntax, s.Value, `unrecognized syntax %q; use "proto2" or "proto3"`, s.Value.Value)
	}

	pkg := f.Package()
	fileScope := newScope(fmt.Sprintf("package %q", pkg))
	if pkg == "" {
		fileScope.name = "the file"
	}
	var sawPackage bool
	imports := map[string]bool{}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.Package:
			if sawPackage {
				c.report(DiagDuplicateDecl, d, "multiple package declarations")
			}
			sawPackage = true
		case *ast.Import:
			if d.Path == nil {
				continue
			}
			if imports[d.Path.Value] {
				c.report(DiagDuplicateDecl, d.Path, "%q is imported more than once", d.Path.Value)
			}
			imports[d.Path.Value] = true
		case *ast.Message:
			c.defineIdent(fileScope, d.Name, "a message")
			c.checkMessage(d, pkg)
		case *ast.Enum:
			c.defineIdent(fileScope, d.Name, "an enum")
			c.checkEnum(d, fileScope)
		case *ast.Service:
			c.defineIdent(fileScope, d.Name, "a service")
			c.checkService(d)
		case *ast.Extend:
			c.checkExtend(d, fileScope, pkg)
		}
	}
}

func (c *checker) checkVisibility(v ast.Visibility, l ast.Loc) {
	if v != ast.VisibilityDefault && (c.syntax != ast.Editions || c.edition < "2024") {
		c.reportSev(DiagVisibility, SeverityError, l, "%q requires edition 2024 or later", v.String())
	}
}

// ---- messages ----

// numbered is a field number in use in a message.
type numbered struct {
	num  uint64
	node ast.Node
	name string
}

// span is a checked range, inclusive.
type span struct {
	lo, hi int64
	r      *ast.Range
}

func (s span) String() string {
	if s.lo == s.hi {
		return strconv.FormatInt(s.lo, 10)
	}
	return strconv.FormatInt(s.lo, 10) + " to " + strconv.FormatInt(s.hi, 10)
}

// messageInfo collects what a message body declares, for the cross checks.
type messageInfo struct {
	scope     *scope
	fullName  string
	numbers   []numbered
	names     []*ast.Ident // field names
	reserved  []span
	resNames  map[string]bool
	extRanges []span
}

func (c *checker) checkMessage(m *ast.Message, parent string) {
	c.checkVisibility(m.Visibility, m.VisibilityLoc)
	name := ""
	if m.Name != nil {
		name = m.Name.Value
	}
	c.checkMessageBody(m.Body, join(parent, name))
}

func (c *checker) checkMessageBody(body []ast.Decl, fullName string) {
	mi := &messageInfo{
		scope:    newScope(fmt.Sprintf("message %q", fullName)),
		fullName: fullName,
		resNames: map[string]bool{},
	}

	messageSet := false
	for _, d := range body {
		if o, ok := d.(*ast.Option); ok && optionIs(o, "message_set_wire_format", "true") {
			messageSet = true
		}
	}
	extMax := int64(MaxFieldNumber)
	if messageSet {
		extMax = maxMessageSetExtRange
	}

	for _, d := range body {
		switch d := d.(type) {
		case *ast.Reserved:
			mi.reserved = append(mi.reserved, c.checkRanges(d.Ranges, 1, MaxFieldNumber, "reserved field numbers")...)
			c.checkReservedNames(d, mi.resNames)
		case *ast.Extensions:
			if c.syntax == ast.Proto3 {
				c.report(DiagExtensions, d, "extension ranges are not allowed in proto3")
			}
			mi.extRanges = append(mi.extRanges, c.checkRanges(d.Ranges, 1, extMax, "extension numbers")...)
		}
	}
	c.checkOverlaps(mi.reserved, "reserved range")
	c.checkOverlaps(mi.extRanges, "extension range")
	for _, e := range mi.extRanges {
		for _, r := range mi.reserved {
			if e.lo <= r.hi && r.lo <= e.hi {
				c.report(DiagRange, e.r, "extension range %s overlaps with reserved range %s", e, r)
			}
		}
	}

	for _, d := range body {
		switch d := d.(type) {
		case *ast.Field:
			c.checkField(d, mi, fieldInMessage)
		case *ast.MapField:
			c.checkMapField(d, mi, fieldInMessage)
		case *ast.Group:
			c.checkGroup(d, mi, fieldInMessage, mi.scope)
		case *ast.Oneof:
			c.checkOneof(d, mi)
		case *ast.Message:
			c.defineIdent(mi.scope, d.Name, "a message")
			c.checkMessage(d, fullName)
		case *ast.Enum:
			c.defineIdent(mi.scope, d.Name, "an enum")
			c.checkEnum(d, mi.scope)
		case *ast.Extend:
			c.checkExtend(d, mi.scope, fullName)
		}
	}

	slices.SortStableFunc(mi.numbers, func(a, b numbered) int { return cmp.Compare(a.node.Pos().Start, b.node.Pos().Start) })
	seen := map[uint64]numbered{}
	for _, n := range mi.numbers {
		if prev, dup := seen[n.num]; dup {
			c.report(DiagDuplicateNumber, n.node, "field number %d is already used by %q", n.num, prev.name)
		} else {
			seen[n.num] = n
		}
		for _, r := range mi.reserved {
			if int64(n.num) >= r.lo && int64(n.num) <= r.hi {
				c.report(DiagReserved, n.node, "field %q uses reserved number %d", n.name, n.num)
				break
			}
		}
		for _, r := range mi.extRanges {
			if int64(n.num) >= r.lo && int64(n.num) <= r.hi {
				c.report(DiagRange, n.node, "field %q uses number %d, which is in extension range %s", n.name, n.num, r)
				break
			}
		}
	}
	for _, id := range mi.names {
		if mi.resNames[id.Value] {
			c.report(DiagReserved, id, "field name %q is reserved", id.Value)
		}
	}
}

// optionIs reports whether o is `option name = value;` with a plain name.
func optionIs(o *ast.Option, name, value string) bool {
	if o.Name == nil || o.Name.String() != name {
		return false
	}
	v, ok := o.Value.(*ast.IdentLit)
	return ok && !v.Negative && v.Value == value
}

type fieldContext uint8

const (
	fieldInMessage fieldContext = iota
	fieldInOneof
	fieldInExtend
)

// checkNumber checks a field number and records it in mi (nil for
// extensions). Extensions may go up to the int32 maximum here: extensions of
// a message with message_set_wire_format can, and the extendee is only known
// to the linker.
func (c *checker) checkNumber(n *ast.IntLit, name string, mi *messageInfo) {
	if n == nil {
		return
	}
	limit := uint64(MaxFieldNumber)
	if mi == nil {
		limit = maxMessageSetExtRange
	}
	if n.Negative || n.Value < 1 || n.Value > limit {
		c.report(DiagFieldNumber, n, "field numbers must be in the range 1 to %d", limit)
		return
	}
	if n.Value >= FirstReservedNumber && n.Value <= LastReservedNumber {
		c.report(DiagFieldNumber, n, "field numbers %d to %d are reserved for the protobuf implementation",
			FirstReservedNumber, LastReservedNumber)
	}
	if mi != nil {
		mi.numbers = append(mi.numbers, numbered{num: n.Value, node: n, name: name})
	}
}

// checkLabel checks a field's label for the syntax and context.
func (c *checker) checkLabel(label ast.Label, labelLoc ast.Loc, start ast.Loc, ctx fieldContext, isMap bool) {
	switch {
	case isMap && label != ast.LabelNone:
		c.reportSev(DiagLabel, SeverityError, labelLoc, "map fields cannot have labels")
	case ctx == fieldInOneof && label != ast.LabelNone:
		c.reportSev(DiagLabel, SeverityError, labelLoc, "fields in oneofs must not have labels")
	case isMap || ctx == fieldInOneof:
	case c.syntax == ast.Proto2 && label == ast.LabelNone:
		c.reportSev(DiagLabel, SeverityError, ast.Loc{Start: start.Start, End: start.Start},
			`fields in proto2 files need a label: "required", "optional" or "repeated"`)
	case label == ast.LabelRequired && ctx == fieldInExtend:
		c.reportSev(DiagLabel, SeverityError, labelLoc, "extensions cannot be required")
	case label == ast.LabelRequired && c.syntax == ast.Proto3:
		c.reportSev(DiagLabel, SeverityError, labelLoc, "required fields are not allowed in proto3")
	case (label == ast.LabelRequired || label == ast.LabelOptional) && c.syntax == ast.Editions:
		c.reportSev(DiagLabel, SeverityError, labelLoc,
			"the %q label is not allowed in editions; use features.field_presence instead", label.String())
	}
}

func (c *checker) checkDefault(opts *ast.CompactOptions) {
	if opts == nil || c.syntax != ast.Proto3 {
		return
	}
	for _, o := range opts.Options {
		if o.Name != nil && o.Name.String() == "default" {
			c.report(DiagDefault, o.Name, "explicit default values are not allowed in proto3")
		}
	}
}

func identValue(id *ast.Ident) string {
	if id == nil {
		return ""
	}
	return id.Value
}

func (c *checker) checkField(f *ast.Field, mi *messageInfo, ctx fieldContext) {
	c.checkLabel(f.Label, f.LabelLoc, f.Loc, ctx, false)
	c.checkDefault(f.Options)
	name := identValue(f.Name)
	if ctx == fieldInExtend {
		c.checkNumber(f.Number, name, nil)
		return
	}
	c.checkNumber(f.Number, name, mi)
	c.defineIdent(mi.scope, f.Name, "a field")
	if f.Name != nil && !f.Name.Missing() {
		mi.names = append(mi.names, f.Name)
	}
}

// validMapKeys are the scalar types allowed as map keys.
var validMapKeys = map[string]bool{
	"int32": true, "int64": true, "uint32": true, "uint64": true, "sint32": true, "sint64": true,
	"fixed32": true, "fixed64": true, "sfixed32": true, "sfixed64": true, "bool": true, "string": true,
}

// MapEntryName returns the name of the nested type protoc generates for a
// map field: "foo_bar" becomes "FooBarEntry".
func MapEntryName(field string) string {
	var b strings.Builder
	capNext := true
	for i := 0; i < len(field); i++ {
		switch ch := field[i]; {
		case ch == '_':
			capNext = true
		case capNext && 'a' <= ch && ch <= 'z':
			b.WriteByte(ch - 'a' + 'A')
			capNext = false
		default:
			b.WriteByte(ch)
			capNext = false
		}
	}
	return b.String() + "Entry"
}

func (c *checker) checkMapField(m *ast.MapField, mi *messageInfo, ctx fieldContext) {
	c.checkLabel(m.Label, m.LabelLoc, m.Loc, ctx, true)
	switch ctx {
	case fieldInOneof:
		c.report(DiagMapField, m, "map fields are not allowed in oneofs")
	case fieldInExtend:
		c.report(DiagMapField, m, "map fields are not allowed in extend blocks")
	}
	if k := m.KeyType; k != nil && !(len(k.Parts) == 1 && !k.LeadingDot && validMapKeys[k.Parts[0].Value]) {
		c.report(DiagMapField, k, "map key type must be an integral type, bool or string; %q is not allowed", k.String())
	}
	c.checkDefault(m.Options)
	name := identValue(m.Name)
	if ctx == fieldInExtend {
		c.checkNumber(m.Number, name, nil)
		return
	}
	c.checkNumber(m.Number, name, mi)
	c.defineIdent(mi.scope, m.Name, "a field")
	if name != "" {
		mi.names = append(mi.names, m.Name)
		c.define(mi.scope, MapEntryName(name), m.Name, fmt.Sprintf("the entry type of map field %q", name))
	}
}

// checkGroup checks a group, defining its field and type names in s (the
// message scope, or the enclosing scope of an extend block).
func (c *checker) checkGroup(g *ast.Group, mi *messageInfo, ctx fieldContext, s *scope) {
	if c.syntax != ast.Proto2 {
		c.report(DiagGroup, g, "groups are not allowed in %s; use a nested message instead", c.syntaxName())
	}
	c.checkLabel(g.Label, g.LabelLoc, g.Loc, ctx, false)
	name := identValue(g.Name)
	if name != "" && !('A' <= name[0] && name[0] <= 'Z') {
		c.report(DiagGroup, g.Name, "group names must start with a capital letter")
	}
	field := strings.ToLower(name)
	if ctx == fieldInExtend {
		c.checkNumber(g.Number, field, nil)
	} else {
		c.checkNumber(g.Number, field, mi)
		if name != "" {
			mi.names = append(mi.names, &ast.Ident{Loc: g.Name.Loc, Value: field})
		}
	}
	if name != "" {
		c.define(s, field, g.Name, fmt.Sprintf("the field of group %q", name))
		if field != name { // a lower-case name was reported above
			c.defineIdent(s, g.Name, "a group")
		}
	}
	c.checkMessageBody(g.Body, join(mi.fullName, name))
}

func (c *checker) checkOneof(o *ast.Oneof, mi *messageInfo) {
	c.defineIdent(mi.scope, o.Name, "a oneof")
	fields := 0
	for _, d := range o.Body {
		switch d := d.(type) {
		case *ast.Field:
			fields++
			c.checkField(d, mi, fieldInOneof)
		case *ast.MapField:
			fields++
			c.checkMapField(d, mi, fieldInOneof)
		case *ast.Group:
			fields++
			c.checkGroup(d, mi, fieldInOneof, mi.scope)
		}
	}
	if fields == 0 && o.Name != nil && !o.Name.Missing() {
		c.report(DiagEmptyOneof, o.Name, "oneof %q must have at least one field", o.Name.Value)
	}
}

func (c *checker) checkExtend(e *ast.Extend, s *scope, parent string) {
	mi := &messageInfo{scope: newScope(""), fullName: parent, resNames: map[string]bool{}}
	for _, d := range e.Body {
		switch d := d.(type) {
		case *ast.Field:
			c.checkField(d, mi, fieldInExtend)
			c.defineIdent(s, d.Name, "an extension")
		case *ast.MapField:
			c.checkMapField(d, mi, fieldInExtend)
			c.defineIdent(s, d.Name, "an extension")
		case *ast.Group:
			c.checkGroup(d, mi, fieldInExtend, s)
		}
	}
}

// ---- ranges and reserved names ----

// checkRanges validates ranges against [lo, hi] and returns the valid ones.
func (c *checker) checkRanges(rs []*ast.Range, lo, hi int64, what string) []span {
	var spans []span
	for _, r := range rs {
		if r.From == nil || !r.Max && r.To == nil && r.Loc.End > r.From.End {
			continue // left incomplete by the parser
		}
		from, ok := r.From.Int64()
		if !ok || from < lo || from > hi {
			c.report(DiagRange, r.From, "%s must be in the range %d to %d", what, lo, hi)
			continue
		}
		to := from
		switch {
		case r.Max:
			to = hi
		case r.To != nil:
			if to, ok = r.To.Int64(); !ok || to < lo || to > hi {
				c.report(DiagRange, r.To, "%s must be in the range %d to %d", what, lo, hi)
				continue
			}
		}
		if to < from {
			c.report(DiagRange, r, "range end %d is before its start %d", to, from)
			continue
		}
		spans = append(spans, span{lo: from, hi: to, r: r})
	}
	return spans
}

// checkOverlaps reports ranges overlapping an earlier one.
func (c *checker) checkOverlaps(spans []span, what string) {
	for i, s := range spans {
		for _, prev := range spans[:i] {
			if s.lo <= prev.hi && prev.lo <= s.hi {
				c.report(DiagRange, s.r, "%s %s overlaps with %s %s", what, s, what, prev)
				break
			}
		}
	}
}

func (c *checker) checkReservedNames(r *ast.Reserved, names map[string]bool) {
	for _, n := range r.Names {
		switch {
		case c.syntax == ast.Editions && n.Quoted:
			c.report(DiagReservedName, n, "reserved names must be identifiers, not strings, in editions")
		case c.syntax != ast.Editions && !n.Quoted:
			c.report(DiagReservedName, n, "reserved names must be quoted strings in %s", c.syntaxName())
		}
		if !lexer.IsIdent(n.Value) {
			c.report(DiagReservedName, n, "reserved name %q is not a valid identifier", n.Value)
			continue
		}
		names[n.Value] = true
	}
}

// ---- enums ----

func (c *checker) checkEnum(e *ast.Enum, parent *scope) {
	c.checkVisibility(e.Visibility, e.VisibilityLoc)
	var reserved []span
	resNames := map[string]bool{}
	allowAlias, closed := (*ast.Option)(nil), false
	for _, d := range e.Body {
		switch d := d.(type) {
		case *ast.Reserved:
			reserved = append(reserved, c.checkRanges(d.Ranges, math.MinInt32, math.MaxInt32, "reserved enum numbers")...)
			c.checkReservedNames(d, resNames)
		case *ast.Option:
			if optionIs(d, "allow_alias", "true") {
				allowAlias = d
			}
			if optionIs(d, "features.enum_type", "CLOSED") {
				closed = true
			}
		}
	}
	for _, d := range c.file.Decls {
		if o, ok := d.(*ast.Option); ok && optionIs(o, "features.enum_type", "CLOSED") {
			closed = true
		}
	}
	c.checkOverlaps(reserved, "reserved range")

	enumName := identValue(e.Name)
	values := 0
	byNumber := map[int64]string{}
	aliases := false
	for _, d := range e.Body {
		v, ok := d.(*ast.EnumValue)
		if !ok {
			continue
		}
		values++
		name := identValue(v.Name)
		c.define(parent, name, v.Name, fmt.Sprintf("a value of enum %q; enum values are siblings of their enum, not children of it", enumName))
		if resNames[name] {
			c.report(DiagReserved, v.Name, "enum value name %q is reserved", name)
		}
		if v.Number == nil {
			continue
		}
		num, ok := v.Number.Int64()
		if !ok || num < math.MinInt32 || num > math.MaxInt32 {
			c.report(DiagEnumValue, v.Number, "enum value numbers must be in the int32 range")
			continue
		}
		if values == 1 && num != 0 {
			switch {
			case c.syntax == ast.Proto3:
				c.report(DiagEnumValue, v.Number, "the first enum value must be zero in proto3")
			case c.syntax == ast.Editions && !closed:
				c.report(DiagEnumValue, v.Number, "the first enum value must be zero for open enums")
			}
		}
		for _, r := range reserved {
			if num >= r.lo && num <= r.hi {
				c.report(DiagReserved, v.Number, "enum value %q uses reserved number %d", name, num)
				break
			}
		}
		if prev, dup := byNumber[num]; dup {
			aliases = true
			if allowAlias == nil {
				c.report(DiagEnumValue, v.Number, "%q uses the same number as %q; add option allow_alias = true; to the enum to allow aliases", name, prev)
			}
		} else {
			byNumber[num] = name
		}
	}
	if values == 0 && e.RBrace >= 0 && e.Name != nil && !e.Name.Missing() {
		c.report(DiagEnumValue, e.Name, "enum %q must have at least one value", enumName)
	}
	if allowAlias != nil && !aliases && values > 0 {
		c.report(DiagEnumValue, allowAlias, "enum %q sets allow_alias but no two values share a number", enumName)
	}
}

// ---- services ----

func (c *checker) checkService(s *ast.Service) {
	rpcs := newScope(fmt.Sprintf("service %q", identValue(s.Name)))
	for _, d := range s.Body {
		if r, ok := d.(*ast.RPC); ok {
			c.defineIdent(rpcs, r.Name, "an rpc")
		}
	}
}
