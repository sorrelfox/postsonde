package descproto

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/lexer"
)

// printer renders one decoded file as .proto source.
type printer struct {
	b      strings.Builder
	depth  int
	f      *fileProto
	pool   *desc.Pool // resolves custom options; nil for standard options only
	err    error
	syntax string // "proto2", "proto3" or "editions"
	// dropped counts option fields that could not be rendered (unknown
	// extensions).
	dropped int
}

// render returns the source of f. Options are rendered from their
// decoded fields; with pool, options messages are decoded again against
// the pool so that custom options (extensions) it defines are included.
func render(f *fileProto, pool *desc.Pool) (string, int, error) {
	p := &printer{f: f, pool: pool}
	p.file()
	if p.err != nil {
		return "", 0, p.err
	}
	return p.b.String(), p.dropped, nil
}

func (p *printer) fail(format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
}

func (p *printer) line(parts ...string) {
	for range p.depth {
		p.b.WriteString("  ")
	}
	for _, s := range parts {
		p.b.WriteString(s)
	}
	p.b.WriteByte('\n')
}

func (p *printer) open(parts ...string) {
	p.line(append(parts, " {")...)
	p.depth++
}

func (p *printer) close() {
	p.depth--
	p.line("}")
}

// ident checks that s is a simple identifier.
func (p *printer) ident(s, what string) string {
	if !lexer.IsIdent(s) {
		p.fail("invalid %s name %q", what, s)
	}
	return s
}

// typeName checks a (possibly fully qualified) dotted name.
func (p *printer) typeName(s, what string) string {
	if !validDotted(strings.TrimPrefix(s, ".")) {
		p.fail("invalid %s %q", what, s)
	}
	return s
}

func validDotted(s string) bool {
	for part := range strings.SplitSeq(s, ".") {
		if !lexer.IsIdent(part) {
			return false
		}
	}
	return true
}

func (p *printer) file() {
	f := p.f
	switch {
	case f.syntax == "" || f.syntax == "proto2":
		p.syntax = "proto2"
		p.line(`syntax = "proto2";`)
	case f.syntax == "proto3":
		p.syntax = "proto3"
		p.line(`syntax = "proto3";`)
	case f.syntax == "editions":
		p.syntax = "editions"
		switch f.edition {
		case edition2023:
			p.line(`edition = "2023";`)
		case edition2024:
			p.line(`edition = "2024";`)
		default:
			p.fail("unsupported edition %d", f.edition)
		}
	default:
		p.fail("unsupported syntax %q", f.syntax)
	}
	if f.pkg != "" {
		if !validDotted(f.pkg) {
			p.fail("invalid package name %q", f.pkg)
		}
		p.line("package ", f.pkg, ";")
	}
	for i, d := range f.deps {
		kind := ""
		switch {
		case f.public[i]:
			kind = "public "
		case f.weak[i]:
			kind = "weak "
		}
		p.line("import ", kind, quote([]byte(d)), ";")
	}
	for _, d := range f.optionDeps {
		p.line("import option ", quote([]byte(d)), ";")
	}
	p.options(f.options)
	scope := f.pkg
	for _, m := range f.messages {
		if !p.ownedTopLevel(m) {
			p.message(m, scope)
		}
	}
	for _, e := range f.enums {
		p.enum(e)
	}
	for _, s := range f.services {
		p.service(s)
	}
	p.extends(f.extensions, scope, f.messages)
}

// ownedTopLevel reports whether m is the type of a top-level group
// extension, printed inside its extend block.
func (p *printer) ownedTopLevel(m *messageProto) bool {
	for _, x := range p.f.extensions {
		if p.groupType(x, p.f.pkg, p.f.messages) == m {
			return true
		}
	}
	return false
}

// options renders an options message as option statements.
func (p *printer) options(m *dynamic.Message) {
	for _, s := range p.optionList(m) {
		p.line("option ", s, ";")
	}
}

// compact renders extra (json_name, default) and options as a bracketed
// list, or "" when empty.
func (p *printer) compact(m *dynamic.Message, extra ...string) string {
	list := append(extra, p.optionList(m)...)
	if len(list) == 0 {
		return ""
	}
	return " [" + strings.Join(list, ", ") + "]"
}

// optionList returns "name = value" for every set option field. Message
// fields are flattened into dotted names; repeated messages and maps use
// aggregate values.
func (p *printer) optionList(m *dynamic.Message) []string {
	if m == nil {
		return nil
	}
	m = p.resolve(m)
	var out []string
	var walk func(m *dynamic.Message, prefix string)
	walk = func(m *dynamic.Message, prefix string) {
		if len(m.Unknown()) > 0 {
			p.dropped++
		}
		m.Range(func(fd *desc.Field, v any) bool {
			name := fd.Name
			if fd.IsExtension {
				name = "(." + fd.FullName + ")"
			} else if prefix == "" {
				switch fd.Name {
				case "uninterpreted_option", "map_entry":
					return true
				case "features":
					if p.syntax != "editions" {
						return true
					}
				}
			}
			if prefix != "" {
				name = prefix + "." + name
			}
			switch {
			case fd.IsMap():
				mp := v.(*dynamic.Map)
				for _, k := range mp.Keys(false) {
					val, _ := mp.Get(k)
					out = append(out, name+" = { key: "+p.scalar(fd.MapKey(), k)+" "+p.fieldText(fd.MapValue(), "value", val)+" }")
				}
			case fd.Cardinality == desc.Repeated:
				l := v.(*dynamic.List)
				for i := range l.Len() {
					out = append(out, name+" = "+p.value(fd, l.Get(i)))
				}
			case fd.Kind == desc.KindMessage || fd.Kind == desc.KindGroup:
				sub := v.(*dynamic.Message)
				if empty(sub) {
					out = append(out, name+" = {}")
				} else {
					walk(sub, name)
				}
			default:
				out = append(out, name+" = "+p.scalar(fd, v))
			}
			return true
		})
	}
	walk(m, "")
	return out
}

func empty(m *dynamic.Message) bool {
	n := 0
	m.Range(func(*desc.Field, any) bool { n++; return false })
	return n == 0
}

// resolve decodes an options message again against p.pool, so that
// extensions the pool defines become known fields.
func (p *printer) resolve(m *dynamic.Message) *dynamic.Message {
	if p.pool == nil || len(m.Unknown()) == 0 && !hasUnknownDeep(m) {
		return m
	}
	t := p.pool.FindMessage(m.Descriptor().FullName)
	if t == nil {
		return m
	}
	b, err := dynamic.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return m
	}
	n := dynamic.New(t)
	if err := (dynamic.UnmarshalOptions{Resolver: p.pool}).Unmarshal(b, n); err != nil {
		return m
	}
	return n
}

// hasUnknownDeep reports whether m or a message inside it has unknown
// fields.
func hasUnknownDeep(m *dynamic.Message) bool {
	if len(m.Unknown()) > 0 {
		return true
	}
	found := false
	m.Range(func(fd *desc.Field, v any) bool {
		switch v := v.(type) {
		case *dynamic.Message:
			found = hasUnknownDeep(v)
		case *dynamic.List:
			for i := range v.Len() {
				if x, ok := v.Get(i).(*dynamic.Message); ok && hasUnknownDeep(x) {
					found = true
				}
			}
		case *dynamic.Map:
			for _, k := range v.Keys(false) {
				if x, ok := v.Get(k); ok {
					if xm, ok := x.(*dynamic.Message); ok && hasUnknownDeep(xm) {
						found = true
					}
				}
			}
		}
		return !found
	})
	return found
}

// value renders a singular value of fd: a scalar or an aggregate.
func (p *printer) value(fd *desc.Field, v any) string {
	if m, ok := v.(*dynamic.Message); ok {
		return p.aggregate(m)
	}
	return p.scalar(fd, v)
}

// aggregate renders m in text format: { a: 1 b { c: "x" } [pkg.ext]: 2 }.
func (p *printer) aggregate(m *dynamic.Message) string {
	if len(m.Unknown()) > 0 {
		p.dropped++
	}
	var parts []string
	m.Range(func(fd *desc.Field, v any) bool {
		name := fd.Name
		switch {
		case fd.IsExtension:
			name = "[" + fd.FullName + "]"
		case fd.Kind == desc.KindGroup && fd.Message != nil:
			name = fd.Message.Name
		}
		switch v := v.(type) {
		case *dynamic.List:
			for i := range v.Len() {
				parts = append(parts, p.fieldText(fd, name, v.Get(i)))
			}
		case *dynamic.Map:
			for _, k := range v.Keys(false) {
				val, _ := v.Get(k)
				parts = append(parts, name+" { key: "+p.scalar(fd.MapKey(), k)+" "+p.fieldText(fd.MapValue(), "value", val)+" }")
			}
		default:
			parts = append(parts, p.fieldText(fd, name, v))
		}
		return true
	})
	if len(parts) == 0 {
		return "{}"
	}
	return "{ " + strings.Join(parts, " ") + " }"
}

func (p *printer) fieldText(fd *desc.Field, name string, v any) string {
	if m, ok := v.(*dynamic.Message); ok {
		return name + " " + p.aggregate(m)
	}
	return name + ": " + p.scalar(fd, v)
}

// scalar renders a scalar or enum value as a literal.
func (p *printer) scalar(fd *desc.Field, v any) string {
	switch v := v.(type) {
	case bool:
		return strconv.FormatBool(v)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case float32:
		return formatFloat(float64(v), 32)
	case float64:
		return formatFloat(v, 64)
	case string:
		return quote([]byte(v))
	case []byte:
		return quote(v)
	case dynamic.EnumNumber:
		if fd != nil && fd.Enum != nil {
			if ev := fd.Enum.ValueByNumber(int32(v)); ev != nil {
				return ev.Name
			}
		}
		return strconv.FormatInt(int64(v), 10)
	}
	p.fail("unexpected option value %T", v)
	return ""
}

func formatFloat(f float64, bits int) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	s := strconv.FormatFloat(f, 'g', -1, bits)
	if !strings.ContainsAny(s, ".eEn") {
		s += ".0"
	}
	return s
}

// quote returns a string literal for b. Printable UTF-8 is kept; other
// bytes are octal escapes.
func quote(b []byte) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		switch {
		case r == '"' || r == '\\':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		case r == '\n':
			sb.WriteString(`\n`)
		case r == '\t':
			sb.WriteString(`\t`)
		case r == utf8.RuneError && n <= 1, r < 0x20, r == 0x7f, r >= 0x80 && !strconv.IsPrint(r):
			for _, c := range b[:max(n, 1)] {
				fmt.Fprintf(&sb, `\%03o`, c)
			}
		default:
			sb.Write(b[:n])
		}
		b = b[max(n, 1):]
	}
	sb.WriteByte('"')
	return sb.String()
}

// Scalar type keywords by FieldDescriptorProto.Type.
func scalarKeyword(t int32) string {
	k := desc.Kind(t)
	if t > 0 && t < 256 && k.IsScalar() {
		return k.String()
	}
	return ""
}

// entryType returns the nested map entry type of x in m, or nil.
func (p *printer) entryType(x *fieldProto, m *messageProto, full string) *messageProto {
	if x.label != labelRepeated || x.typ != int32(desc.KindMessage) && x.typ != 0 {
		return nil
	}
	for _, n := range m.nested {
		if n.mapEntry && "."+full+"."+n.name == x.typeName && len(n.fields) == 2 &&
			n.fields[0].number == 1 && n.fields[0].name == "key" && n.fields[1].number == 2 && n.fields[1].name == "value" {
			return n
		}
	}
	return nil
}

// groupType returns the message type of a proto2 group field x declared
// in scope, found among candidates, or nil.
func (p *printer) groupType(x *fieldProto, scope string, candidates []*messageProto) *messageProto {
	if p.syntax != "proto2" || x.typ != int32(desc.KindGroup) {
		return nil
	}
	for _, n := range candidates {
		full := n.name
		if scope != "" {
			full = scope + "." + n.name
		}
		if "."+full == x.typeName && strings.ToLower(n.name) == x.name {
			return n
		}
	}
	return nil
}

func (p *printer) message(m *messageProto, scope string) {
	full := joinName(scope, m.name)
	p.open(p.visibility(m.visibility), "message ", p.ident(m.name, "message"))
	p.messageBody(m, full)
	p.close()
}

func (p *printer) visibility(v int32) string {
	if p.syntax != "editions" || p.f.edition < edition2024 {
		return ""
	}
	switch v {
	case 1:
		return "local "
	case 2:
		return "export "
	}
	return ""
}

func joinName(scope, name string) string {
	if scope == "" {
		return name
	}
	return scope + "." + name
}

func (p *printer) messageBody(m *messageProto, full string) {
	if p.err != nil {
		return
	}
	p.options(m.options)

	// Types owned by fields (map entries, groups) are printed with their
	// field; other nested types keep their order relative to them.
	owned := map[*messageProto]bool{}
	ownerOf := map[*fieldProto]*messageProto{}
	for _, x := range m.fields {
		if t := p.entryType(x, m, full); t != nil {
			owned[t], ownerOf[x] = true, t
		} else if t := p.groupType(x, full, m.nested); t != nil {
			owned[t], ownerOf[x] = true, t
		}
	}
	for _, x := range m.extensions {
		if t := p.groupType(x, full, m.nested); t != nil {
			owned[t] = true
		}
	}
	printed := map[*messageProto]bool{}
	flushNested := func(upTo *messageProto) {
		for _, n := range m.nested {
			if n == upTo {
				return
			}
			if !owned[n] && !printed[n] {
				printed[n] = true
				p.message(n, full)
			}
		}
	}

	oneofDone := map[int32]bool{}
	for _, x := range m.fields {
		if p.err != nil {
			return
		}
		if t := ownerOf[x]; t != nil {
			flushNested(t)
		}
		if x.hasOneof && !x.proto3Optional {
			if oneofDone[x.oneof] {
				continue
			}
			oneofDone[x.oneof] = true
			if x.oneof < 0 || int(x.oneof) >= len(m.oneofs) {
				p.fail("field %q has an invalid oneof index %d", x.name, x.oneof)
				return
			}
			o := m.oneofs[x.oneof]
			// Nested types cannot be declared inside the oneof: print those
			// that come before its last group type first.
			var lastOwned *messageProto
			for _, y := range m.fields {
				if y.hasOneof && !y.proto3Optional && y.oneof == x.oneof && ownerOf[y] != nil {
					lastOwned = ownerOf[y]
				}
			}
			if lastOwned != nil {
				flushNested(lastOwned)
			}
			p.open("oneof ", p.ident(o.name, "oneof"))
			p.options(o.options)
			for _, y := range m.fields {
				if y.hasOneof && !y.proto3Optional && y.oneof == x.oneof {
					p.field(y, m, full, ownerOf[y], true)
				}
			}
			p.close()
			continue
		}
		p.field(x, m, full, ownerOf[x], false)
	}
	flushNested(nil)
	for _, e := range m.enums {
		p.enum(e)
	}
	for _, r := range m.extRanges {
		p.line("extensions ", rangeText(r.start, exclusive(r.end)), p.compact(r.options), ";")
	}
	p.reserved(m.reserved, m.reservedNames, true)
	p.extends(m.extensions, full, m.nested)
}

// exclusive converts an exclusive range end to the inclusive end as
// written; math.MaxInt32, which message sets use for "max", stays.
func exclusive(end int32) int32 {
	if end == math.MaxInt32 {
		return end
	}
	return end - 1
}

func rangeText(start, end int32) string {
	if end == math.MaxInt32 {
		return strconv.FormatInt(int64(start), 10) + " to max"
	}
	if start == end {
		return strconv.FormatInt(int64(start), 10)
	}
	return strconv.FormatInt(int64(start), 10) + " to " + strconv.FormatInt(int64(end), 10)
}

func (p *printer) reserved(ranges [][2]int32, names []string, exclusiveEnd bool) {
	if len(ranges) > 0 {
		parts := make([]string, len(ranges))
		for i, r := range ranges {
			end := r[1]
			if exclusiveEnd {
				end = exclusive(end)
			}
			parts[i] = rangeText(r[0], end)
		}
		p.line("reserved ", strings.Join(parts, ", "), ";")
	}
	if len(names) > 0 {
		parts := make([]string, len(names))
		for i, n := range names {
			p.ident(n, "reserved")
			if p.syntax == "editions" {
				parts[i] = n
			} else {
				parts[i] = strconv.Quote(n)
			}
		}
		p.line("reserved ", strings.Join(parts, ", "), ";")
	}
}

// extends prints extensions in extend blocks, one per run of the same
// extendee.
func (p *printer) extends(xs []*fieldProto, scope string, candidates []*messageProto) {
	for i := 0; i < len(xs); {
		j := i
		for j < len(xs) && xs[j].extendee == xs[i].extendee {
			j++
		}
		p.open("extend ", p.typeName(xs[i].extendee, "extendee"))
		for _, x := range xs[i:j] {
			p.field(x, nil, scope, p.groupType(x, scope, candidates), false)
		}
		p.close()
		i = j
	}
}

// field prints a field, map field or group. owner is the field's map entry
// or group type; m is the containing message (nil for extensions).
func (p *printer) field(x *fieldProto, m *messageProto, scope string, owner *messageProto, inOneof bool) {
	if p.err != nil {
		return
	}
	name := p.ident(x.name, "field")
	num := strconv.FormatInt(int64(x.number), 10)
	var extra []string
	if x.hasJSONName && m != nil && x.jsonName != defaultJSONName(x.name) {
		extra = append(extra, "json_name = "+quote([]byte(x.jsonName)))
	}
	if x.hasDefault {
		extra = append(extra, "default = "+p.defaultValue(x))
	}
	opts := p.compact(x.options, extra...)

	label := ""
	switch {
	case inOneof:
	case x.label == labelRepeated:
		label = "repeated "
	case p.syntax == "proto2" && x.label == labelRequired:
		label = "required "
	case p.syntax == "proto2":
		label = "optional "
	case p.syntax == "proto3" && x.proto3Optional:
		label = "optional "
	}

	switch {
	case owner != nil && owner.mapEntry:
		k, v := owner.fields[0], owner.fields[1]
		p.line("map<", p.fieldType(k), ", ", p.fieldType(v), "> ", name, " = ", num, opts, ";")
	case owner != nil:
		p.open(label, "group ", p.ident(owner.name, "group"), " = ", num, opts)
		p.messageBody(owner, joinName(scope, owner.name))
		p.close()
	default:
		p.line(label, p.fieldType(x), " ", name, " = ", num, opts, ";")
	}
}

func (p *printer) fieldType(x *fieldProto) string {
	if kw := scalarKeyword(x.typ); kw != "" {
		return kw
	}
	switch desc.Kind(x.typ) {
	case desc.KindMessage, desc.KindEnum, desc.KindGroup, desc.KindUnknown:
		if x.typeName == "" {
			p.fail("field %q has no type", x.name)
			return ""
		}
		return p.typeName(x.typeName, "type name")
	}
	p.fail("field %q has an invalid type %d", x.name, x.typ)
	return ""
}

// defaultValue renders default_value, which descriptors hold as text:
// C-escaped for bytes, raw for strings, the value name for enums.
func (p *printer) defaultValue(x *fieldProto) string {
	switch desc.Kind(x.typ) {
	case desc.KindString:
		return quote([]byte(x.def))
	case desc.KindBytes:
		b, err := unescapeC(x.def)
		if err != nil {
			p.fail("field %q: invalid bytes default: %v", x.name, err)
		}
		return quote(b)
	case desc.KindEnum, desc.KindUnknown:
		return p.ident(x.def, "enum value")
	case desc.KindBool:
		if x.def != "true" && x.def != "false" {
			p.fail("field %q: invalid bool default %q", x.name, x.def)
		}
		return x.def
	case desc.KindFloat, desc.KindDouble:
		switch x.def {
		case "inf", "-inf", "nan":
			return x.def
		}
		if _, err := strconv.ParseFloat(x.def, 64); err != nil {
			p.fail("field %q: invalid float default %q", x.name, x.def)
		}
		return x.def
	default:
		if _, err := strconv.ParseInt(x.def, 10, 64); err != nil {
			if _, err := strconv.ParseUint(x.def, 10, 64); err != nil {
				p.fail("field %q: invalid integer default %q", x.name, x.def)
			}
		}
		return x.def
	}
}

// unescapeC decodes protoc's CEscape form of a bytes default.
func unescapeC(s string) ([]byte, error) {
	var out []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		i++
		if i >= len(s) {
			return nil, errors.New("trailing backslash")
		}
		switch c := s[i]; c {
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'a':
			out = append(out, '\a')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'v':
			out = append(out, '\v')
		case '\\', '\'', '"', '?':
			out = append(out, c)
		case 'x', 'X':
			j := i + 1
			for j < len(s) && j < i+3 && isHex(s[j]) {
				j++
			}
			if j == i+1 {
				return nil, errors.New("invalid hex escape")
			}
			v, _ := strconv.ParseUint(s[i+1:j], 16, 8)
			out = append(out, byte(v))
			i = j - 1
		default:
			if c < '0' || c > '7' {
				return nil, fmt.Errorf("invalid escape \\%c", c)
			}
			j := i
			for j < len(s) && j < i+3 && '0' <= s[j] && s[j] <= '7' {
				j++
			}
			v, _ := strconv.ParseUint(s[i:j], 8, 16)
			if v > 255 {
				return nil, errors.New("octal escape out of range")
			}
			out = append(out, byte(v))
			i = j - 1
		}
	}
	return out, nil
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// defaultJSONName is protoc's JSON name for a field without json_name.
func defaultJSONName(name string) string {
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

func (p *printer) enum(e *enumProto) {
	if p.err != nil {
		return
	}
	p.open(p.visibility(e.visibility), "enum ", p.ident(e.name, "enum"))
	p.options(e.options)
	for _, v := range e.values {
		p.line(p.ident(v.name, "enum value"), " = ", strconv.FormatInt(int64(v.number), 10), p.compact(v.options), ";")
	}
	p.reserved(e.reserved, e.reservedNames, false)
	p.close()
}

func (p *printer) service(s *serviceProto) {
	if p.err != nil {
		return
	}
	p.open("service ", p.ident(s.name, "service"))
	p.options(s.options)
	for _, m := range s.methods {
		in, out := p.typeName(m.input, "request type"), p.typeName(m.output, "response type")
		if m.clientStreaming {
			in = "stream " + in
		}
		if m.serverStreaming {
			out = "stream " + out
		}
		head := []string{"rpc ", p.ident(m.name, "method"), "(", in, ") returns (", out, ")"}
		opts := p.optionList(m.options)
		if len(opts) == 0 {
			p.line(append(head, ";")...)
			continue
		}
		p.open(head...)
		for _, o := range opts {
			p.line("option ", o, ";")
		}
		p.close()
	}
	p.close()
}
