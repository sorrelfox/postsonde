package linker

import (
	"math"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/ast"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

// optionsMessage returns google.protobuf.<name> from descriptor.proto, or
// nil when no descriptor.proto was loaded.
func (l *linker) optionsMessage(name string) *desc.Message {
	if l.descriptorFile == nil {
		return nil
	}
	for _, m := range l.descriptorFile.d.Messages {
		if m.Name == name {
			return m
		}
	}
	return nil
}

func (l *linker) interpretOptions(p pendingOptions) {
	f := p.f
	root := l.optionsMessage(p.target)
	seen := l.optionSeen[p.out]
	if seen == nil {
		seen = &setOptions{leaves: map[string]bool{}, prefixes: map[string]bool{}}
		l.optionSeen[p.out] = seen
	}
	for _, o := range p.opts {
		if o.Name == nil || len(o.Name.Parts) == 0 || !optionNameComplete(o.Name) {
			continue
		}
		do := &desc.Option{Name: o.Name.String(), Value: o.Value, Loc: o.Loc}
		*p.out = append(*p.out, do)

		field, _ := p.owner.(*desc.Field)
		if len(o.Name.Parts) == 1 && !o.Name.Parts[0].Extension {
			switch name := o.Name.Parts[0].Name.String(); {
			case name == "json_name" && p.target == fieldOptions:
				l.jsonNameOption(f, field, o)
				continue
			case name == "default" && p.target == fieldOptions:
				l.defaultOption(f, field, o)
				continue
			case name == "map_entry" && p.target == messageOptions:
				l.errorAt(f, CodeOptionValue, o.Name, "map_entry must not be set explicitly; use map<KeyType, ValueType> instead")
				continue
			case name == "uninterpreted_option":
				l.errorAt(f, CodeUnknownOption, o.Name, "uninterpreted_option cannot be set explicitly")
				continue
			}
		}
		if root == nil {
			continue
		}
		msg := root
		for i, part := range o.Name.Parts {
			var x *desc.Field
			partName := part.Name.String()
			if part.Extension {
				res := l.lookup(f, p.scope, partName, false, true)
				if res.sym == nil {
					if res.hidden != nil || res.partial != "" {
						l.unresolved(f, part, partName, "option", res, false)
					} else {
						l.errorAt(f, CodeUnknownOption, part, "unknown option "+strconv.Quote("("+partName+")")+
							"; import the file that defines it")
					}
					break
				}
				ext, ok := res.sym.d.(*desc.Field)
				if !ok || !ext.IsExtension {
					l.errorAt(f, CodeUnknownOption, part, strconv.Quote(partName)+" is "+res.sym.describe()+", not an extension")
					break
				}
				if ext.Extendee == nil {
					break // reported at its extend block
				}
				if ext.Extendee != msg {
					l.errorAt(f, CodeUnknownOption, part, strconv.Quote(ext.FullName)+" extends "+
						strconv.Quote(ext.Extendee.FullName)+", not "+strconv.Quote(msg.FullName))
					break
				}
				x = ext
			} else {
				if x = msg.FieldByName(partName); x == nil {
					if i == 0 {
						l.errorAt(f, CodeUnknownOption, part, "unknown option "+strconv.Quote(partName)+
							"; "+strconv.Quote(msg.FullName)+" has no such field (custom options are written in parentheses)")
					} else {
						l.errorAt(f, CodeUnknownOption, part, strconv.Quote(partName)+" is not a field of "+strconv.Quote(msg.FullName))
					}
					break
				}
			}
			do.Path = append(do.Path, x)
			if i == len(o.Name.Parts)-1 {
				break
			}
			if x.Kind != desc.KindMessage && x.Kind != desc.KindGroup {
				if x.Kind != desc.KindUnknown {
					l.errorAt(f, CodeUnknownOption, o.Name.Parts[i+1], "option "+strconv.Quote(do.Name)+": "+
						strconv.Quote(x.Name)+" is "+article(x.Kind.String())+", not a message")
				}
				break
			}
			if x.Cardinality == desc.Repeated {
				l.errorAt(f, CodeOptionValue, o.Name.Parts[i+1], "option "+strconv.Quote(do.Name)+": "+
					strconv.Quote(x.Name)+" is a repeated message; set it with an aggregate value")
				break
			}
			if msg = x.Message; msg == nil {
				break
			}
		}
		if len(do.Path) != len(o.Name.Parts) {
			continue
		}
		last := do.Path[len(do.Path)-1]
		if !seen.add(do.Path, o.Value) {
			l.errorAt(f, CodeDuplicateOption, o.Name, "option "+strconv.Quote(do.Name)+" is already set")
			continue
		}
		do.Scalar = l.checkValue(f, last, o.Value, "option "+strconv.Quote(do.Name))
		l.optionEffects(p, o, do)
	}
}

func optionNameComplete(n *ast.OptionName) bool {
	for _, p := range n.Parts {
		if p.Name == nil || nameString(p.Name) == "" {
			return false
		}
	}
	return true
}

// setOptions tracks the options set on one element, like protoc's check
// that an option is not set twice: leaves are the non-repeated fields set,
// by path, including the fields named in aggregate values; prefixes are the
// message fields a path went through.
type setOptions struct {
	leaves, prefixes map[string]bool
}

// add records path set to v and reports whether it was not set before.
func (s *setOptions) add(path []*desc.Field, v ast.Value) bool {
	keys := make([]string, len(path))
	for i, x := range path {
		keys[i] = x.FullName
	}
	key := strings.Join(keys, "/")
	last := path[len(path)-1]
	if last.Cardinality != desc.Repeated {
		if s.leaves[key] || s.prefixes[key] {
			return false
		}
		s.leaves[key] = true
	}
	for i := 1; i < len(keys); i++ {
		s.prefixes[strings.Join(keys[:i], "/")] = true
	}
	if lit, ok := v.(*ast.MessageLit); ok && last.Message != nil && last.Cardinality != desc.Repeated {
		for _, mf := range lit.Fields {
			if mf.Name == nil || mf.Name.Kind != ast.FieldRefName || mf.Name.Name == nil {
				continue
			}
			if x := last.Message.FieldByName(mf.Name.Name.String()); x != nil && x.Cardinality != desc.Repeated {
				s.leaves[key+"/"+x.FullName] = true
			}
		}
	}
	return true
}

// optionEffects records what the model interprets: deprecated, packed and
// features.
func (l *linker) optionEffects(p pendingOptions, a *ast.Option, o *desc.Option) {
	first := o.Path[0]
	if first.IsExtension {
		return
	}
	switch first.Name {
	case "deprecated":
		if len(o.Path) == 1 && o.Scalar != nil && o.Scalar.Bool {
			switch owner := p.owner.(type) {
			case *desc.File:
				owner.Deprecated = true
			case desc.Descriptor:
				owner.Desc().Deprecated = true
			}
		}
	case "packed":
		x, ok := p.owner.(*desc.Field)
		if !ok || len(o.Path) != 1 || o.Scalar == nil {
			return
		}
		switch {
		case p.f.d.Syntax == ast.Editions:
			l.errorAt(p.f, CodeOptionValue, a.Name, "packed is not allowed in editions; use features.repeated_field_encoding")
		case x.Cardinality != desc.Repeated || x.Kind != desc.KindUnknown && !x.Kind.IsPackable():
			l.errorAt(p.f, CodeOptionValue, a.Name, "packed can only be set on repeated fields of numeric, bool or enum types")
		default:
			l.packed[x] = o.Scalar.Bool
		}
	case "features":
		if p.f.d.Syntax != ast.Editions {
			l.errorAt(p.f, CodeOptionValue, a.Name, "features are only allowed in editions files, not in "+p.f.d.Syntax.String())
			return
		}
		if p.owner == nil {
			return
		}
		fs := l.features[p.owner]
		if fs == nil {
			fs = &featureSet{}
			l.features[p.owner] = fs
		}
		switch {
		case len(o.Path) == 2 && o.Scalar != nil && o.Scalar.Enum != nil:
			fs.set(o.Path[1].Name, o.Scalar.Enum.Name)
		case len(o.Path) == 1:
			if lit, ok := o.Value.(*ast.MessageLit); ok {
				for _, mf := range lit.Fields {
					if mf.Name == nil || mf.Name.Kind != ast.FieldRefName || mf.Name.Name == nil {
						continue
					}
					if v, ok := mf.Value.(*ast.IdentLit); ok {
						fs.set(mf.Name.Name.String(), v.Value)
					}
				}
			}
		}
	}
}

func (l *linker) jsonNameOption(f *fileState, x *desc.Field, o *ast.Option) {
	if x == nil {
		return
	}
	if x.IsExtension {
		l.errorAt(f, CodeJSONNameConflict, o.Name, "json_name is not allowed on extensions")
		return
	}
	s, ok := o.Value.(*ast.StringLit)
	if !ok {
		if o.Value != nil {
			l.errorAt(f, CodeOptionValue, o.Value, "json_name must be a string")
		}
		return
	}
	x.JSONName, x.HasJSONName = s.Value, true
}

func (l *linker) defaultOption(f *fileState, x *desc.Field, o *ast.Option) {
	if x == nil || f.d.Syntax == ast.Proto3 { // proto3 defaults are reported by parser.Check
		return
	}
	switch {
	case x.Cardinality == desc.Repeated:
		l.errorAt(f, CodeOptionValue, o.Name, "repeated fields cannot have default values")
	case x.Kind == desc.KindMessage || x.Kind == desc.KindGroup:
		l.errorAt(f, CodeOptionValue, o.Name, "message fields cannot have default values")
	default:
		x.Default = l.checkValue(f, x, o.Value, "the default value of "+strconv.Quote(x.Name))
	}
}

// checkValue checks v against field x. It returns the value for scalars and
// enums, and nil for messages or invalid values.
func (l *linker) checkValue(f *fileState, x *desc.Field, v ast.Value, what string) *desc.Value {
	if v == nil {
		return nil
	}
	bad := func(want string) *desc.Value {
		l.errorAt(f, CodeOptionValue, v, what+" must be "+want)
		return nil
	}
	switch x.Kind {
	case desc.KindUnknown:
		return nil
	case desc.KindMessage, desc.KindGroup:
		lit, ok := v.(*ast.MessageLit)
		if !ok {
			return bad("a message value in braces, such as { name: value }")
		}
		l.checkAggregate(f, x.Message, lit, what)
		return nil
	case desc.KindEnum:
		id, ok := v.(*ast.IdentLit)
		if !ok || id.Negative || x.Enum == nil {
			if x.Enum == nil {
				return nil
			}
			return bad("a value name of enum " + strconv.Quote(x.Enum.FullName))
		}
		ev := x.Enum.ValueByName(id.Value)
		if ev == nil {
			l.errorAt(f, CodeOptionValue, v, "enum "+strconv.Quote(x.Enum.FullName)+" has no value named "+
				strconv.Quote(id.Value)+" ("+what+")")
			return nil
		}
		return &desc.Value{Kind: desc.KindEnum, Enum: ev, Int: int64(ev.Number)}
	case desc.KindBool:
		if id, ok := v.(*ast.IdentLit); ok && !id.Negative && (id.Value == "true" || id.Value == "false") {
			return &desc.Value{Kind: x.Kind, Bool: id.Value == "true"}
		}
		return bad("true or false")
	case desc.KindString, desc.KindBytes:
		if s, ok := v.(*ast.StringLit); ok {
			return &desc.Value{Kind: x.Kind, String: s.Value}
		}
		return bad("a string")
	case desc.KindFloat, desc.KindDouble:
		switch n := v.(type) {
		case *ast.FloatLit:
			return &desc.Value{Kind: x.Kind, Float: n.Value}
		case *ast.IntLit:
			fv := float64(n.Value)
			if n.Negative {
				fv = -fv
			}
			return &desc.Value{Kind: x.Kind, Float: fv}
		case *ast.IdentLit:
			switch n.Value {
			case "inf":
				sign := 1
				if n.Negative {
					sign = -1
				}
				return &desc.Value{Kind: x.Kind, Float: math.Inf(sign)}
			case "nan":
				return &desc.Value{Kind: x.Kind, Float: math.NaN()}
			}
		}
		return bad("a number")
	case desc.KindInt32, desc.KindSint32, desc.KindSfixed32:
		if n, ok := v.(*ast.IntLit); ok {
			if i, ok := n.Int64(); ok && i >= math.MinInt32 && i <= math.MaxInt32 {
				return &desc.Value{Kind: x.Kind, Int: i}
			}
		}
		return bad("an integer from -2147483648 to 2147483647")
	case desc.KindInt64, desc.KindSint64, desc.KindSfixed64:
		if n, ok := v.(*ast.IntLit); ok {
			if i, ok := n.Int64(); ok {
				return &desc.Value{Kind: x.Kind, Int: i}
			}
		}
		return bad("an integer from -9223372036854775808 to 9223372036854775807")
	case desc.KindUint32, desc.KindFixed32:
		if n, ok := v.(*ast.IntLit); ok && (!n.Negative || n.Value == 0) && n.Value <= math.MaxUint32 {
			return &desc.Value{Kind: x.Kind, Uint: n.Value}
		}
		return bad("an integer from 0 to 4294967295")
	case desc.KindUint64, desc.KindFixed64:
		if n, ok := v.(*ast.IntLit); ok && (!n.Negative || n.Value == 0) {
			return &desc.Value{Kind: x.Kind, Uint: n.Value}
		}
		return bad("an integer from 0 to 18446744073709551615")
	}
	return nil
}

// checkAggregate checks the field names of a message literal for message m
// and whether each value is a message where the field needs one. Scalar
// values inside aggregates are not checked.
func (l *linker) checkAggregate(f *fileState, m *desc.Message, lit *ast.MessageLit, what string) {
	if m == nil || lit == nil {
		return
	}
	for _, mf := range lit.Fields {
		if mf.Name == nil || nameString(mf.Name.Name) == "" {
			continue
		}
		name := nameString(mf.Name.Name)
		var x *desc.Field
		switch mf.Name.Kind {
		case ast.FieldRefName:
			if x = m.FieldByName(name); x == nil {
				for _, g := range m.Fields {
					if g.Kind == desc.KindGroup && g.Message != nil && g.Message.Name == name {
						x = g
					}
				}
			}
			if x == nil {
				l.errorAt(f, CodeOptionValue, mf.Name, strconv.Quote(name)+" is not a field of "+
					strconv.Quote(m.FullName)+" ("+what+")")
				continue
			}
		case ast.FieldRefExtension:
			s := l.syms[strings.TrimPrefix(name, ".")]
			if s != nil {
				if ext, ok := s.d.(*desc.Field); ok && ext.IsExtension && ext.Extendee == m {
					x = ext
				}
			}
			if x == nil {
				l.errorAt(f, CodeOptionValue, mf.Name, strconv.Quote(name)+" is not an extension of "+
					strconv.Quote(m.FullName)+" ("+what+")")
				continue
			}
		case ast.FieldRefAny:
			if m.FullName != "google.protobuf.Any" {
				l.errorAt(f, CodeOptionValue, mf.Name, "a type URL is only allowed in a google.protobuf.Any value ("+what+")")
				continue
			}
			s := l.syms[strings.TrimPrefix(name, ".")]
			var t *desc.Message
			if s != nil {
				t, _ = s.d.(*desc.Message)
			}
			if t == nil {
				l.errorAt(f, CodeOptionValue, mf.Name, "unknown message type "+strconv.Quote(name)+" in type URL ("+what+")")
				continue
			}
			if sub, ok := mf.Value.(*ast.MessageLit); ok {
				l.checkAggregate(f, t, sub, what)
			}
			continue
		}
		values := []ast.Value{mf.Value}
		if list, ok := mf.Value.(*ast.ListLit); ok {
			if x.Cardinality != desc.Repeated {
				l.errorAt(f, CodeOptionValue, mf.Value, strconv.Quote(x.Name)+" is not repeated, so its value cannot be a list ("+what+")")
				continue
			}
			values = list.Elements
		}
		for _, v := range values {
			sub, isMsg := v.(*ast.MessageLit)
			switch {
			case v == nil || x.Kind == desc.KindUnknown:
			case x.Kind == desc.KindMessage || x.Kind == desc.KindGroup:
				if !isMsg {
					l.errorAt(f, CodeOptionValue, v, strconv.Quote(x.Name)+" is a message; its value must be in braces ("+what+")")
				} else {
					l.checkAggregate(f, x.Message, sub, what)
				}
			case isMsg:
				l.errorAt(f, CodeOptionValue, v, strconv.Quote(x.Name)+" is "+article(x.Kind.String())+", not a message ("+what+")")
			}
		}
	}
}

// featureSet holds the edition features set on one element, by value name.
type featureSet struct {
	presence, enumType, repeated, messageEncoding string
}

func (fs *featureSet) set(name, value string) {
	switch name {
	case "field_presence":
		fs.presence = value
	case "enum_type":
		fs.enumType = value
	case "repeated_field_encoding":
		fs.repeated = value
	case "message_encoding":
		fs.messageEncoding = value
	}
}

// merge returns base with the features set in over applied.
func (fs featureSet) merge(over *featureSet) featureSet {
	if over == nil {
		return fs
	}
	if over.presence != "" {
		fs.presence = over.presence
	}
	if over.enumType != "" {
		fs.enumType = over.enumType
	}
	if over.repeated != "" {
		fs.repeated = over.repeated
	}
	if over.messageEncoding != "" {
		fs.messageEncoding = over.messageEncoding
	}
	return fs
}

// applyFeatures sets presence, packing, closed enums, required fields and
// delimited encoding from the file's syntax and its features.
func (l *linker) applyFeatures(f *fileState) {
	var base featureSet
	switch f.d.Syntax {
	case ast.Proto2:
		base = featureSet{"EXPLICIT", "CLOSED", "EXPANDED", "LENGTH_PREFIXED"}
	case ast.Proto3:
		base = featureSet{"IMPLICIT", "OPEN", "PACKED", "LENGTH_PREFIXED"}
	default:
		base = featureSet{"EXPLICIT", "OPEN", "PACKED", "LENGTH_PREFIXED"}
	}
	base = base.merge(l.features[f.d])
	for _, m := range f.d.Messages {
		l.applyMessage(f, m, base)
	}
	for _, e := range f.d.Enums {
		l.applyEnum(e, base)
	}
	for _, x := range f.d.Extensions {
		l.applyField(f, x, base)
	}
}

func (l *linker) applyMessage(f *fileState, m *desc.Message, parent featureSet) {
	fs := parent.merge(l.features[m])
	for _, x := range m.Fields {
		l.applyField(f, x, fs)
	}
	for _, x := range m.Extensions {
		l.applyField(f, x, fs)
	}
	for _, n := range m.Messages {
		l.applyMessage(f, n, fs)
	}
	for _, e := range m.Enums {
		l.applyEnum(e, fs)
	}
}

func (l *linker) applyEnum(e *desc.Enum, parent featureSet) {
	e.Closed = parent.merge(l.features[e]).enumType == "CLOSED"
}

func (l *linker) applyField(f *fileState, x *desc.Field, parent featureSet) {
	fs := parent.merge(l.features[x])
	editions := f.d.Syntax == ast.Editions
	if editions && fs.presence == "LEGACY_REQUIRED" && x.Cardinality == desc.Optional {
		x.Cardinality = desc.Required
	}
	if editions && fs.messageEncoding == "DELIMITED" && x.Kind == desc.KindMessage && x.Message != nil &&
		!x.Message.MapEntry && !x.IsMap() && (x.Parent == nil || !x.Parent.MapEntry) {
		x.Kind = desc.KindGroup
	}
	repeated := x.Cardinality == desc.Repeated
	x.HasPresence = !repeated && (x.Kind == desc.KindMessage || x.Kind == desc.KindGroup || x.Oneof != nil ||
		x.IsExtension || x.Proto3Optional || f.d.Syntax != ast.Proto3 && fs.presence != "IMPLICIT")
	if repeated && x.Kind.IsPackable() {
		packed, set := l.packed[x]
		switch f.d.Syntax {
		case ast.Proto2:
			x.Packed = packed
		case ast.Proto3:
			x.Packed = packed || !set
		default:
			x.Packed = fs.repeated == "PACKED"
		}
	}
}

// article prefixes s with "a" or "an".
func article(s string) string {
	if s != "" && strings.ContainsRune("aeiou", rune(s[0])) && !strings.HasPrefix(s, "uint") {
		return "an " + s
	}
	return "a " + s
}
