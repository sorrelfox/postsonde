package linker

import (
	"math"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/ast"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/parser"
)

// pendingField is a field whose type name needs resolving.
type pendingField struct {
	f     *fileState
	field *desc.Field
	name  *ast.FullName
	scope string
}

// pendingExtend is an extend block whose extendee needs resolving.
type pendingExtend struct {
	f      *fileState
	fields []*desc.Field
	name   *ast.FullName
	scope  string
}

// pendingMethod is an rpc whose request and response types need resolving.
type pendingMethod struct {
	f      *fileState
	method *desc.Method
	rpc    *ast.RPC
	scope  string
}

// Options messages in google/protobuf/descriptor.proto.
const (
	fileOptions      = "FileOptions"
	messageOptions   = "MessageOptions"
	fieldOptions     = "FieldOptions"
	oneofOptions     = "OneofOptions"
	enumOptions      = "EnumOptions"
	enumValueOptions = "EnumValueOptions"
	serviceOptions   = "ServiceOptions"
	methodOptions    = "MethodOptions"
	rangeOptions     = "ExtensionRangeOptions"
)

// pendingOptions are the options of one element.
type pendingOptions struct {
	f      *fileState
	target string // options message name, such as FieldOptions
	scope  string // for extension names
	owner  any    // *desc.File, *desc.Message, *desc.Field, ... or nil
	out    *[]*desc.Option
	opts   []*ast.Option
}

func (l *linker) addOptions(f *fileState, target, scope string, owner any, out *[]*desc.Option, opts []*ast.Option) {
	if len(opts) > 0 {
		l.pendingOptions = append(l.pendingOptions, pendingOptions{f, target, scope, owner, out, opts})
	}
}

func compact(c *ast.CompactOptions) []*ast.Option {
	if c == nil {
		return nil
	}
	return c.Options
}

func bodyOptions(body []ast.Decl) []*ast.Option {
	var opts []*ast.Option
	for _, d := range body {
		if o, ok := d.(*ast.Option); ok {
			opts = append(opts, o)
		}
	}
	return opts
}

func named(id *ast.Ident) bool { return id != nil && !id.Missing() }

func (l *linker) base(f *fileState, name, full string, loc ast.Loc, nameLoc ast.Loc, c *ast.Comments) desc.Base {
	b := desc.Base{Name: name, FullName: full, File: f.d, Loc: loc, NameLoc: nameLoc}
	if c != nil {
		b.Comments = *c
	}
	return b
}

// build creates the descriptors of f and registers their names.
func (l *linker) build(f *fileState) {
	a, d := f.ast, f.d
	d.Syntax = a.Syntax()
	if s := a.SyntaxDecl(); s != nil && s.Edition && s.Value != nil {
		d.Edition = s.Value.Value
	}
	d.Package = a.Package()
	for _, imp := range f.imports {
		di := &desc.Import{Path: imp.decl.Path.Value, Kind: imp.decl.Kind, Loc: imp.decl.Loc}
		if imp.target != nil {
			di.File = imp.target.d
		}
		d.Imports = append(d.Imports, di)
	}
	scope := d.Package
	for _, decl := range a.Decls {
		switch decl := decl.(type) {
		case *ast.Package:
			if decl.Name != nil && nameString(decl.Name) == d.Package {
				l.definePackage(f, d.Package, decl.Name)
			}
		case *ast.Option:
			l.addOptions(f, fileOptions, scope, d, &d.Options, []*ast.Option{decl})
		case *ast.Message:
			if m := l.buildMessage(f, decl, scope, nil); m != nil {
				d.Messages = append(d.Messages, m)
			}
		case *ast.Enum:
			if e := l.buildEnum(f, decl, scope, nil); e != nil {
				d.Enums = append(d.Enums, e)
			}
		case *ast.Service:
			if s := l.buildService(f, decl, scope); s != nil {
				d.Services = append(d.Services, s)
			}
		case *ast.Extend:
			d.Extensions = append(d.Extensions, l.buildExtend(f, decl, scope, nil)...)
		}
	}
}

func (l *linker) buildMessage(f *fileState, m *ast.Message, scope string, parent *desc.Message) *desc.Message {
	if !named(m.Name) {
		return nil
	}
	full := join(scope, m.Name.Value)
	dm := &desc.Message{Base: l.base(f, m.Name.Value, full, m.Loc, m.Name.Loc, &m.Comments), Parent: parent}
	l.define(f, symMessage, dm)
	l.buildMessageBody(f, dm, m.Body)
	return dm
}

func optionIsTrue(opts []*ast.Option, name string) bool {
	for _, o := range opts {
		if o.Name != nil && o.Name.String() == name {
			if v, ok := o.Value.(*ast.IdentLit); ok && !v.Negative && v.Value == "true" {
				return true
			}
		}
	}
	return false
}

func (l *linker) buildMessageBody(f *fileState, dm *desc.Message, body []ast.Decl) {
	dm.MessageSetWireFormat = optionIsTrue(bodyOptions(body), "message_set_wire_format")
	maxExt := int64(parser.MaxFieldNumber)
	if dm.MessageSetWireFormat {
		maxExt = math.MaxInt32
	}
	for _, decl := range body {
		switch decl := decl.(type) {
		case *ast.Field, *ast.MapField, *ast.Group:
			l.buildMember(f, dm, decl, nil)
		case *ast.Oneof:
			var o *desc.Oneof
			if named(decl.Name) {
				o = &desc.Oneof{Base: l.base(f, decl.Name.Value, join(dm.FullName, decl.Name.Value), decl.Loc, decl.Name.Loc, &decl.Comments), Parent: dm}
				l.define(f, symOneof, o)
				dm.Oneofs = append(dm.Oneofs, o)
				l.addOptions(f, oneofOptions, o.FullName, o, &o.Options, bodyOptions(decl.Body))
			}
			for _, od := range decl.Body {
				if x := l.buildMember(f, dm, od, o); x != nil && o != nil {
					o.Fields = append(o.Fields, x)
				}
			}
		case *ast.Message:
			if n := l.buildMessage(f, decl, dm.FullName, dm); n != nil {
				dm.Messages = append(dm.Messages, n)
			}
		case *ast.Enum:
			if e := l.buildEnum(f, decl, dm.FullName, dm); e != nil {
				dm.Enums = append(dm.Enums, e)
			}
		case *ast.Extend:
			dm.Extensions = append(dm.Extensions, l.buildExtend(f, decl, dm.FullName, dm)...)
		case *ast.Extensions:
			for _, r := range decl.Ranges {
				if dr, ok := makeRange(r, maxExt); ok {
					dm.ExtensionRanges = append(dm.ExtensionRanges, dr)
				}
			}
			l.addOptions(f, rangeOptions, dm.FullName, nil, new([]*desc.Option), compact(decl.Options))
		case *ast.Reserved:
			for _, r := range decl.Ranges {
				if dr, ok := makeRange(r, parser.MaxFieldNumber); ok {
					dm.ReservedRanges = append(dm.ReservedRanges, dr)
				}
			}
			for _, n := range decl.Names {
				dm.ReservedNames = append(dm.ReservedNames, n.Value)
			}
		case *ast.Option:
			l.addOptions(f, messageOptions, dm.FullName, dm, &dm.Options, []*ast.Option{decl})
		}
	}
	// Synthetic oneofs for proto3 optional fields, named like protoc's.
	var names map[string]bool
	for _, x := range dm.Fields {
		if !x.Proto3Optional {
			continue
		}
		if names == nil {
			names = map[string]bool{}
			for _, y := range dm.Fields {
				names[y.Name] = true
			}
			for _, o := range dm.Oneofs {
				names[o.Name] = true
			}
			for _, n := range dm.Messages {
				names[n.Name] = true
			}
			for _, e := range dm.Enums {
				names[e.Name] = true
			}
		}
		name := "_" + x.Name
		for names[name] {
			name = "X" + name
		}
		names[name] = true
		o := &desc.Oneof{Base: desc.Base{Name: name, FullName: join(dm.FullName, name), File: f.d, Loc: x.Loc, NameLoc: x.NameLoc},
			Parent: dm, Fields: []*desc.Field{x}, Synthetic: true}
		x.Oneof = o
		dm.Oneofs = append(dm.Oneofs, o)
	}
}

// makeRange converts a reserved or extension range; max stands for "max".
func makeRange(r *ast.Range, max int64) (desc.Range, bool) {
	if r == nil || r.From == nil {
		return desc.Range{}, false
	}
	lo, ok := r.From.Int64()
	hi := lo
	switch {
	case r.Max:
		hi = max
	case r.To != nil:
		var ok2 bool
		hi, ok2 = r.To.Int64()
		ok = ok && ok2
	}
	if !ok || lo < math.MinInt32 || hi > math.MaxInt32 || lo > hi {
		return desc.Range{}, false
	}
	return desc.Range{Start: int32(lo), End: int32(hi), Loc: r.Loc}, true
}

func number(n *ast.IntLit) int32 {
	if n == nil {
		return 0
	}
	v, ok := n.Int64()
	if !ok || v < math.MinInt32 || v > math.MaxInt32 {
		return 0
	}
	return int32(v)
}

func cardinality(label ast.Label) desc.Cardinality {
	switch label {
	case ast.LabelRepeated:
		return desc.Repeated
	case ast.LabelRequired:
		return desc.Required
	}
	return desc.Optional
}

// buildMember builds a field, map field or group of message dm (in oneof o
// if not nil) and appends it to dm.Fields.
func (l *linker) buildMember(f *fileState, dm *desc.Message, decl ast.Decl, o *desc.Oneof) *desc.Field {
	var x *desc.Field
	switch decl := decl.(type) {
	case *ast.Field:
		x = l.buildField(f, decl, dm.FullName, dm, false)
	case *ast.MapField:
		x = l.buildMap(f, decl, dm)
	case *ast.Group:
		x = l.buildGroup(f, decl, dm.FullName, dm, false)
	}
	if x != nil {
		x.Oneof = o
		if o != nil {
			x.Proto3Optional = false
		}
		dm.Fields = append(dm.Fields, x)
	}
	return x
}

// setType sets a scalar kind or queues the type name for resolution.
func (l *linker) setType(f *fileState, x *desc.Field, t *ast.FullName, scope string) {
	name := nameString(t)
	if name == "" {
		return
	}
	x.TypeName = name
	if len(t.Parts) == 1 && !t.LeadingDot {
		if k, ok := desc.ScalarKind(name); ok {
			x.Kind, x.TypeName = k, ""
			return
		}
	}
	l.pendingFields = append(l.pendingFields, pendingField{f, x, t, scope})
}

// buildField builds a normal field or, with ext, an extension declared in
// scope (parent is the enclosing message, nil at the top level).
func (l *linker) buildField(f *fileState, a *ast.Field, scope string, parent *desc.Message, ext bool) *desc.Field {
	if !named(a.Name) {
		return nil
	}
	x := &desc.Field{
		Base:        l.base(f, a.Name.Value, join(scope, a.Name.Value), a.Loc, a.Name.Loc, &a.Comments),
		Number:      number(a.Number),
		Cardinality: cardinality(a.Label),
		JSONName:    jsonName(a.Name.Value),
		Parent:      parent,
		IsExtension: ext,
	}
	x.Proto3Optional = f.d.Syntax == ast.Proto3 && a.Label == ast.LabelOptional && !ext
	l.nodes[x] = a
	l.setType(f, x, a.Type, scope)
	l.define(f, symField, x)
	l.addOptions(f, fieldOptions, x.FullName, x, &x.Options, compact(a.Options))
	return x
}

func (l *linker) buildMap(f *fileState, a *ast.MapField, dm *desc.Message) *desc.Field {
	if !named(a.Name) {
		return nil
	}
	entryName := parser.MapEntryName(a.Name.Value)
	entry := &desc.Message{
		Base:     desc.Base{Name: entryName, FullName: join(dm.FullName, entryName), File: f.d, Loc: a.Loc, NameLoc: a.Name.Loc},
		Parent:   dm,
		MapEntry: true,
	}
	l.define(f, symMessage, entry)
	key := &desc.Field{Base: desc.Base{Name: "key", FullName: entry.FullName + ".key", File: f.d, Loc: a.Loc, NameLoc: a.Name.Loc},
		Number: 1, Cardinality: desc.Optional, JSONName: "key", Parent: entry}
	if a.KeyType != nil {
		key.Loc = a.KeyType.Loc
		// Invalid key types are reported by parser.Check; only scalars are set.
		if k, ok := desc.ScalarKind(nameString(a.KeyType)); ok && len(a.KeyType.Parts) == 1 && !a.KeyType.LeadingDot {
			key.Kind = k
		}
	}
	val := &desc.Field{Base: desc.Base{Name: "value", FullName: entry.FullName + ".value", File: f.d, Loc: a.Loc, NameLoc: a.Name.Loc},
		Number: 2, Cardinality: desc.Optional, JSONName: "value", Parent: entry}
	if a.ValType != nil {
		val.Loc = a.ValType.Loc
	}
	l.setType(f, val, a.ValType, dm.FullName)
	entry.Fields = []*desc.Field{key, val}
	l.define(f, symField, key)
	l.define(f, symField, val)
	dm.Messages = append(dm.Messages, entry)

	x := &desc.Field{
		Base:        l.base(f, a.Name.Value, join(dm.FullName, a.Name.Value), a.Loc, a.Name.Loc, &a.Comments),
		Number:      number(a.Number),
		Kind:        desc.KindMessage,
		Cardinality: desc.Repeated,
		JSONName:    jsonName(a.Name.Value),
		Message:     entry,
		TypeName:    entry.FullName,
		Parent:      dm,
	}
	l.define(f, symField, x)
	l.addOptions(f, fieldOptions, x.FullName, x, &x.Options, compact(a.Options))
	return x
}

// buildGroup builds a group field and its message type, both declared in
// scope. parent is the message holding the field (nil for a top-level
// extension); the type goes into parent, or the file at the top level.
func (l *linker) buildGroup(f *fileState, g *ast.Group, scope string, parent *desc.Message, ext bool) *desc.Field {
	if !named(g.Name) {
		return nil
	}
	msg := &desc.Message{Base: l.base(f, g.Name.Value, join(scope, g.Name.Value), g.Loc, g.Name.Loc, &g.Comments), Parent: parent}
	l.define(f, symMessage, msg)
	l.buildMessageBody(f, msg, g.Body)
	if parent != nil {
		parent.Messages = append(parent.Messages, msg)
	} else {
		f.d.Messages = append(f.d.Messages, msg)
	}
	name := strings.ToLower(g.Name.Value)
	x := &desc.Field{
		Base:        l.base(f, name, join(scope, name), g.Loc, g.Name.Loc, &g.Comments),
		Number:      number(g.Number),
		Kind:        desc.KindGroup,
		Cardinality: cardinality(g.Label),
		JSONName:    jsonName(name),
		Message:     msg,
		TypeName:    msg.FullName,
		Parent:      parent,
		IsExtension: ext,
	}
	l.nodes[x] = g
	l.define(f, symField, x)
	l.addOptions(f, fieldOptions, x.FullName, x, &x.Options, compact(g.Options))
	return x
}

func (l *linker) buildExtend(f *fileState, e *ast.Extend, scope string, parent *desc.Message) []*desc.Field {
	var fields []*desc.Field
	for _, decl := range e.Body {
		var x *desc.Field
		switch decl := decl.(type) {
		case *ast.Field:
			x = l.buildField(f, decl, scope, parent, true)
		case *ast.Group:
			x = l.buildGroup(f, decl, scope, parent, true)
		}
		if x != nil {
			x.ExtendeeName = nameString(e.Extendee)
			fields = append(fields, x)
		}
	}
	if e.Extendee != nil && nameString(e.Extendee) != "" && len(fields) > 0 {
		l.pendingExtends = append(l.pendingExtends, pendingExtend{f, fields, e.Extendee, scope})
	}
	return fields
}

func (l *linker) buildEnum(f *fileState, e *ast.Enum, scope string, parent *desc.Message) *desc.Enum {
	if !named(e.Name) {
		return nil
	}
	de := &desc.Enum{Base: l.base(f, e.Name.Value, join(scope, e.Name.Value), e.Loc, e.Name.Loc, &e.Comments), Parent: parent}
	l.define(f, symEnum, de)
	for _, decl := range e.Body {
		switch decl := decl.(type) {
		case *ast.EnumValue:
			if !named(decl.Name) {
				continue
			}
			// C++ scoping: values are siblings of the enum.
			v := &desc.EnumValue{Base: l.base(f, decl.Name.Value, join(scope, decl.Name.Value), decl.Loc, decl.Name.Loc, &decl.Comments),
				Enum: de, Number: number(decl.Number)}
			de.Values = append(de.Values, v)
			l.define(f, symEnumValue, v)
			l.addOptions(f, enumValueOptions, de.FullName, v, &v.Options, compact(decl.Options))
		case *ast.Reserved:
			for _, r := range decl.Ranges {
				if dr, ok := makeRange(r, math.MaxInt32); ok {
					de.ReservedRanges = append(de.ReservedRanges, dr)
				}
			}
			for _, n := range decl.Names {
				de.ReservedNames = append(de.ReservedNames, n.Value)
			}
		case *ast.Option:
			l.addOptions(f, enumOptions, de.FullName, de, &de.Options, []*ast.Option{decl})
		}
	}
	return de
}

func (l *linker) buildService(f *fileState, s *ast.Service, scope string) *desc.Service {
	if !named(s.Name) {
		return nil
	}
	ds := &desc.Service{Base: l.base(f, s.Name.Value, join(scope, s.Name.Value), s.Loc, s.Name.Loc, &s.Comments)}
	l.define(f, symService, ds)
	for _, decl := range s.Body {
		switch decl := decl.(type) {
		case *ast.RPC:
			if !named(decl.Name) {
				continue
			}
			m := &desc.Method{Base: l.base(f, decl.Name.Value, join(ds.FullName, decl.Name.Value), decl.Loc, decl.Name.Loc, &decl.Comments), Service: ds}
			if decl.Input != nil {
				m.ClientStreaming = decl.Input.Stream
				m.InputName = nameString(decl.Input.Type)
			}
			if decl.Output != nil {
				m.ServerStreaming = decl.Output.Stream
				m.OutputName = nameString(decl.Output.Type)
			}
			ds.Methods = append(ds.Methods, m)
			l.define(f, symMethod, m)
			l.pendingMethods = append(l.pendingMethods, pendingMethod{f, m, decl, ds.FullName})
			l.addOptions(f, methodOptions, m.FullName, m, &m.Options, bodyOptions(decl.Body))
		case *ast.Option:
			l.addOptions(f, serviceOptions, ds.FullName, ds, &ds.Options, []*ast.Option{decl})
		}
	}
	return ds
}

// jsonName is protoc's default JSON name: underscores removed and the
// letter after each one upper-cased.
func jsonName(name string) string {
	var b strings.Builder
	up := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_':
			up = true
		case up && 'a' <= c && c <= 'z':
			b.WriteByte(c - 'a' + 'A')
			up = false
		default:
			b.WriteByte(c)
			up = false
		}
	}
	return b.String()
}
