package linker

import (
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/ast"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/parser"
)

// link runs the linking passes over every loaded file.
func (l *linker) link() error {
	for _, f := range l.order {
		if err := l.ctx.Err(); err != nil {
			return cancelled(err)
		}
		l.build(f)
	}
	for _, p := range l.pendingFields {
		l.resolveField(p)
	}
	for _, p := range l.pendingExtends {
		l.resolveExtend(p)
	}
	for _, p := range l.pendingMethods {
		l.resolveMethod(p)
	}
	if err := l.ctx.Err(); err != nil {
		return cancelled(err)
	}
	l.features = map[any]*featureSet{}
	l.packed = map[*desc.Field]bool{}
	for _, p := range l.pendingOptions {
		l.interpretOptions(p)
	}
	for _, f := range l.order {
		l.applyFeatures(f)
	}
	if err := l.ctx.Err(); err != nil {
		return cancelled(err)
	}
	l.checkExtensions()
	for _, f := range l.order {
		desc.Walk(f.d, func(d desc.Descriptor) bool {
			switch d := d.(type) {
			case *desc.Message:
				l.checkJSONNames(f, d)
			case *desc.Field:
				l.checkClosedEnum(f, d)
			}
			return true
		})
	}
	return nil
}

func (l *linker) resolveField(p pendingField) {
	name := nameString(p.name)
	res := l.lookup(p.f, p.scope, name, true, false)
	if res.sym == nil {
		// A name that only resolves to something other than a type.
		if all := l.lookup(p.f, p.scope, name, false, false); all.sym != nil && !all.sym.isType() {
			res = all
		} else {
			l.unresolved(p.f, p.name, name, "type", res, true)
			return
		}
	}
	switch d := res.sym.d.(type) {
	case *desc.Message:
		p.field.Message, p.field.TypeName = d, d.FullName
		if p.field.Kind != desc.KindGroup {
			p.field.Kind = desc.KindMessage
		}
	case *desc.Enum:
		p.field.Enum, p.field.TypeName, p.field.Kind = d, d.FullName, desc.KindEnum
	default:
		l.errorAt(p.f, CodeWrongKind, p.name, strconv.Quote(name)+" is "+res.sym.describe()+
			", not a message or enum type")
	}
}

// resolveMessage resolves a name that must be a message (extendees and rpc
// types), with protoc's LOOKUP_ALL mode.
func (l *linker) resolveMessage(f *fileState, scope string, n *ast.FullName) *desc.Message {
	name := nameString(n)
	if name == "" {
		return nil
	}
	res := l.lookup(f, scope, name, false, false)
	if res.sym == nil {
		l.unresolved(f, n, name, "message type", res, true)
		return nil
	}
	m, ok := res.sym.d.(*desc.Message)
	if !ok {
		l.errorAt(f, CodeWrongKind, n, strconv.Quote(name)+" is "+res.sym.describe()+", not a message type")
		return nil
	}
	return m
}

func (l *linker) resolveExtend(p pendingExtend) {
	m := l.resolveMessage(p.f, p.scope, p.name)
	if m == nil {
		return
	}
	for _, x := range p.fields {
		x.Extendee, x.ExtendeeName = m, m.FullName
	}
}

func (l *linker) resolveMethod(p pendingMethod) {
	if p.rpc.Input != nil && p.rpc.Input.Type != nil {
		if m := l.resolveMessage(p.f, p.scope, p.rpc.Input.Type); m != nil {
			p.method.Input, p.method.InputName = m, m.FullName
		}
	}
	if p.rpc.Output != nil && p.rpc.Output.Type != nil {
		if m := l.resolveMessage(p.f, p.scope, p.rpc.Output.Type); m != nil {
			p.method.Output, p.method.OutputName = m, m.FullName
		}
	}
}

// numberLoc returns the location of a field's number, or its name.
func (l *linker) numberLoc(x *desc.Field) ast.Loc {
	switch n := l.nodes[x].(type) {
	case *ast.Field:
		if n.Number != nil {
			return n.Number.Loc
		}
	case *ast.Group:
		if n.Number != nil {
			return n.Number.Loc
		}
	}
	return x.NameLoc
}

func isOptionsMessage(m *desc.Message) bool {
	return strings.HasPrefix(m.FullName, "google.protobuf.") && strings.HasSuffix(m.FullName, "Options") &&
		m.Parent == nil
}

// checkExtensions checks extension numbers against the extendees' ranges
// and each other, in load order.
func (l *linker) checkExtensions() {
	used := map[*desc.Message]map[int32]*desc.Field{}
	for _, p := range l.pendingExtends {
		f := p.f
		for _, x := range p.fields {
			m := x.Extendee
			if m == nil {
				continue
			}
			if f.d.Syntax == ast.Proto3 && !isOptionsMessage(m) {
				l.errorAt(f, CodeInvalidExtension, p.name, "extensions in proto3 are only allowed for custom options; "+
					strconv.Quote(m.FullName)+" is not an options message")
			}
			if x.Number == 0 {
				continue
			}
			loc := l.numberLoc(x)
			switch {
			case !m.InExtensionRange(x.Number):
				l.report(f, CodeExtensionNumber, parser.SeverityError, loc, strconv.Quote(m.FullName)+
					" does not declare "+strconv.Itoa(int(x.Number))+" as an extension number")
			case m.MessageSetWireFormat:
				if x.Kind != desc.KindMessage || x.Cardinality != desc.Optional {
					l.report(f, CodeInvalidExtension, parser.SeverityError, x.NameLoc, "extensions of message set "+
						strconv.Quote(m.FullName)+" must be optional messages")
				}
			case x.Number > parser.MaxFieldNumber:
				l.report(f, CodeExtensionNumber, parser.SeverityError, loc, "extension number "+strconv.Itoa(int(x.Number))+
					" is above the maximum field number 536870911; only message sets allow larger numbers")
			}
			byNum := used[m]
			if byNum == nil {
				byNum = map[int32]*desc.Field{}
				used[m] = byNum
			}
			if prev := byNum[x.Number]; prev != nil {
				l.report(f, CodeDuplicateExtension, parser.SeverityError, loc, "extension number "+
					strconv.Itoa(int(x.Number))+" of "+strconv.Quote(m.FullName)+" is already used by "+
					strconv.Quote(prev.FullName)+" in "+strconv.Quote(prev.File.Path))
				continue
			}
			byNum[x.Number] = x
		}
	}
}

// checkJSONNames reports fields of m with the same JSON name. In proto2
// files only conflicts involving a json_name option are errors.
func (l *linker) checkJSONNames(f *fileState, m *desc.Message) {
	if m.MapEntry {
		return
	}
	strict := f.d.Syntax != ast.Proto2
	seen := map[string]*desc.Field{}
	for _, x := range m.Fields {
		prev := seen[x.JSONName]
		if prev == nil {
			seen[x.JSONName] = x
			continue
		}
		if !strict && !prev.HasJSONName && !x.HasJSONName {
			continue
		}
		l.report(f, CodeJSONNameConflict, parser.SeverityError, x.NameLoc, "the "+jsonKind(x)+" JSON name of field "+
			strconv.Quote(x.Name)+" ("+strconv.Quote(x.JSONName)+") conflicts with the "+jsonKind(prev)+
			" JSON name of field "+strconv.Quote(prev.Name))
	}
}

func jsonKind(x *desc.Field) string {
	if x.HasJSONName {
		return "custom"
	}
	return "default"
}

// checkClosedEnum reports closed enums used by proto3 message fields.
func (l *linker) checkClosedEnum(f *fileState, x *desc.Field) {
	if f.d.Syntax != ast.Proto3 || x.IsExtension || x.Enum == nil || !x.Enum.Closed {
		return
	}
	loc := x.NameLoc
	if n, ok := l.findTypeLoc(x); ok {
		loc = n
	}
	l.report(f, CodeClosedEnum, parser.SeverityError, loc, "enum "+strconv.Quote(x.Enum.FullName)+
		" is closed (proto2), so it cannot be used in the proto3 message "+strconv.Quote(x.Parent.FullName))
}

// findTypeLoc returns the location of a field's type name.
func (l *linker) findTypeLoc(x *desc.Field) (ast.Loc, bool) {
	if x.Parent != nil && x.Parent.MapEntry {
		return x.Loc, true
	}
	if n, ok := l.nodes[x].(*ast.Field); ok && n.Type != nil {
		return n.Type.Loc, true
	}
	return ast.Loc{}, false
}
