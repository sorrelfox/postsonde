package protojson

import (
	"encoding/base64"
	"math"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

// UnmarshalOptions configure JSON input.
type UnmarshalOptions struct {
	// DiscardUnknown ignores unknown fields, and enum names the enum does
	// not declare, instead of failing.
	DiscardUnknown bool
	// AllowPartial skips the check for missing required fields.
	AllowPartial bool
	// Resolver finds the types named by google.protobuf.Any "@type" members
	// and extensions written as "[full.name]". Without it, a non-empty Any
	// is an error and extension names are unknown fields.
	Resolver Resolver
	// RecursionLimit is the maximum message nesting; 0 means
	// DefaultRecursionLimit.
	RecursionLimit int
}

// Unmarshal parses JSON into m with default options.
func Unmarshal(b []byte, m *dynamic.Message) error {
	return UnmarshalOptions{}.Unmarshal(b, m)
}

// Unmarshal resets m and parses the JSON in b into it. On error, m may be
// partly filled. Errors are *Error values with the input offset.
func (o UnmarshalOptions) Unmarshal(b []byte, m *dynamic.Message) error {
	m.Reset()
	limit := o.RecursionLimit
	if limit == 0 {
		limit = DefaultRecursionLimit
	}
	d := &decoder{s: newScanner(b), o: o}
	if err := d.message(m, false, limit); err != nil {
		return err
	}
	tok, err := d.s.read()
	if err != nil {
		return err
	}
	if tok.kind != kindEOF {
		return d.s.unexpected(tok)
	}
	if !o.AllowPartial {
		if err := checkInitialized(m); err != nil {
			e := err.(*Error)
			return newError(b, len(b), "%s", e.Message)
		}
	}
	return nil
}

type decoder struct {
	s *scanner
	o UnmarshalOptions
}

func (d *decoder) errorf(pos int, format string, args ...any) error {
	return d.s.errorf(pos, format, args...)
}

// message parses an object into m. depth is the nesting still allowed,
// counted like protobuf-go: every message costs one level, except that the
// message inside a well-known type's special form is parsed directly.
func (d *decoder) message(m *dynamic.Message, skipTypeURL bool, depth int) error {
	depth--
	if depth < 0 {
		tok, _ := d.s.peek()
		return d.errorf(tok.pos, "exceeded maximum recursion depth")
	}
	md := m.Descriptor()
	if fn := wellKnownUnmarshaler(md); fn != nil {
		return fn(d, m, depth)
	}
	tok, err := d.s.read()
	if err != nil {
		return err
	}
	if tok.kind != kindObjectOpen {
		return d.s.unexpected(tok)
	}
	if md.MessageSetWireFormat {
		return d.errorf(tok.pos, "%s: message sets are not supported", md.FullName)
	}
	var seen map[int32]bool
	var seenOneofs map[*desc.Oneof]bool
	for {
		tok, err := d.s.read()
		if err != nil {
			return err
		}
		switch tok.kind {
		case kindObjectClose:
			return nil
		case kindName:
		default:
			return d.s.unexpected(tok)
		}
		name := tok.str
		if skipTypeURL && name == "@type" {
			if _, err := d.s.read(); err != nil {
				return err
			}
			continue
		}

		var fd *desc.Field
		if strings.HasPrefix(name, "[") && strings.HasSuffix(name, "]") {
			// Names are exact: *desc.Pool would also accept a leading dot.
			if r := d.o.Resolver; r != nil && !strings.HasPrefix(name, "[.") {
				xname := name[1 : len(name)-1]
				fd = r.FindExtension(xname)
				// A message or enum name is an error even with
				// DiscardUnknown, as in protobuf-go's type registry.
				if fd == nil && isTypeName(r, xname) {
					return d.errorf(tok.pos, "unable to resolve %s: found a type, not an extension", tok.rawString())
				}
			}
			if fd != nil && (fd.Extendee != md || !md.InExtensionRange(fd.Number)) {
				return d.errorf(tok.pos, "message %s cannot be extended by %s", md.FullName, fd.FullName)
			}
		} else {
			fd = md.FieldByJSONName(name)
			if fd == nil {
				for _, f := range md.Fields {
					if textName(f) == name {
						fd = f
						break
					}
				}
			}
		}
		if fd == nil {
			if d.o.DiscardUnknown {
				if err := d.skipValue(depth); err != nil {
					return err
				}
				continue
			}
			return d.errorf(tok.pos, "unknown field %s", tok.rawString())
		}
		if err := checkField(fd); err != nil {
			return d.errorf(tok.pos, "%s", err.(*Error).Message)
		}

		if seen[fd.Number] {
			return d.errorf(tok.pos, "duplicate field %s", tok.rawString())
		}
		if seen == nil {
			seen = map[int32]bool{}
		}
		seen[fd.Number] = true

		// null leaves the field unset, except for Value and NullValue.
		if next, _ := d.s.peek(); next.kind == kindNull && !isValueField(fd) && !isNullValueField(fd) {
			_, _ = d.s.read()
			continue
		}

		switch {
		case fd.IsMap():
			if err := d.mapValue(m.Mutable(fd).(*dynamic.Map), fd, depth); err != nil {
				return err
			}
		case fd.Cardinality == desc.Repeated:
			if err := d.list(m.Mutable(fd).(*dynamic.List), fd, depth); err != nil {
				return err
			}
		default:
			if od := fd.Oneof; od != nil {
				if seenOneofs[od] {
					return d.errorf(tok.pos, "error parsing %s, oneof %s is already set", tok.rawString(), od.FullName)
				}
				if seenOneofs == nil {
					seenOneofs = map[*desc.Oneof]bool{}
				}
				seenOneofs[od] = true
			}
			v, ok, err := d.singular(fd, depth)
			if err != nil {
				return err
			}
			if ok {
				if err := m.Set(fd, v); err != nil {
					return d.errorf(tok.pos, "%v", err)
				}
			}
		}
	}
}

// isTypeName reports whether name is a message, or an enum if the resolver
// can find enums (*desc.Pool can).
func isTypeName(r Resolver, name string) bool {
	if r.FindMessage(name) != nil {
		return true
	}
	er, ok := r.(interface{ FindEnum(string) *desc.Enum })
	return ok && er.FindEnum(name) != nil
}

const (
	valueName     = "google.protobuf.Value"
	nullValueName = "google.protobuf.NullValue"
)

func isValueField(fd *desc.Field) bool {
	return fd.Message != nil && fd.Message.FullName == valueName
}

func isNullValueField(fd *desc.Field) bool {
	return fd.Enum != nil && fd.Enum.FullName == nullValueName
}

// singular parses a message, scalar or enum value for fd. ok is false for
// an unknown enum name skipped with DiscardUnknown.
func (d *decoder) singular(fd *desc.Field, depth int) (v any, ok bool, err error) {
	if fd.Kind == desc.KindMessage || fd.Kind == desc.KindGroup {
		sub := dynamic.New(fd.Message)
		if err := d.message(sub, false, depth); err != nil {
			return nil, false, err
		}
		return sub, true, nil
	}
	return d.scalar(fd)
}

// scalar parses a scalar or enum value for fd.
func (d *decoder) scalar(fd *desc.Field) (any, bool, error) {
	tok, err := d.s.read()
	if err != nil {
		return nil, false, err
	}
	switch fd.Kind {
	case desc.KindBool:
		if tok.kind == kindBool {
			return tok.b, true, nil
		}
	case desc.KindInt32, desc.KindSint32, desc.KindSfixed32:
		if n, ok := parseInt(tok, 32); ok {
			return int32(n), true, nil
		}
	case desc.KindInt64, desc.KindSint64, desc.KindSfixed64:
		if n, ok := parseInt(tok, 64); ok {
			return n, true, nil
		}
	case desc.KindUint32, desc.KindFixed32:
		if n, ok := parseUint(tok, 32); ok {
			return uint32(n), true, nil
		}
	case desc.KindUint64, desc.KindFixed64:
		if n, ok := parseUint(tok, 64); ok {
			return n, true, nil
		}
	case desc.KindFloat:
		if f, ok := parseFloat(tok, 32); ok {
			return float32(f), true, nil
		}
	case desc.KindDouble:
		if f, ok := parseFloat(tok, 64); ok {
			return f, true, nil
		}
	case desc.KindString:
		if tok.kind == kindString {
			return tok.str, true, nil
		}
	case desc.KindBytes:
		if b, ok := parseBytes(tok); ok {
			return b, true, nil
		}
	case desc.KindEnum:
		switch tok.kind {
		case kindString:
			if ev := fd.Enum.ValueByName(tok.str); ev != nil {
				return dynamic.EnumNumber(ev.Number), true, nil
			}
			if d.o.DiscardUnknown {
				return nil, false, nil
			}
		case kindNumber:
			if n, ok := numberInt(tok.raw, 32); ok {
				return dynamic.EnumNumber(n), true, nil
			}
		case kindNull:
			if isNullValueField(fd) {
				return dynamic.EnumNumber(0), true, nil
			}
		}
	}
	name := fd.JSONName
	if fd.IsExtension {
		name = "[" + fd.FullName + "]"
	}
	return nil, false, d.errorf(tok.pos, "invalid value for %s field %s: %s", fd.Kind, name, tok.rawString())
}

// numberInt converts a JSON number token to an integer of bitSize bits.
func numberInt(raw []byte, bitSize int) (int64, bool) {
	p, _, ok := splitNumber(raw)
	if !ok {
		return 0, false
	}
	s, ok := p.intString()
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, bitSize)
	return n, err == nil
}

func numberUint(raw []byte, bitSize int) (uint64, bool) {
	p, _, ok := splitNumber(raw)
	if !ok {
		return 0, false
	}
	s, ok := p.intString()
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, bitSize)
	return n, err == nil
}

// parseInt accepts a number, or a string holding one.
func parseInt(tok token, bitSize int) (int64, bool) {
	switch tok.kind {
	case kindNumber:
		return numberInt(tok.raw, bitSize)
	case kindString:
		if raw, ok := numberInString(tok.str); ok {
			return numberInt(raw, bitSize)
		}
	}
	return 0, false
}

func parseUint(tok token, bitSize int) (uint64, bool) {
	switch tok.kind {
	case kindNumber:
		return numberUint(tok.raw, bitSize)
	case kindString:
		if raw, ok := numberInString(tok.str); ok {
			return numberUint(raw, bitSize)
		}
	}
	return 0, false
}

// parseFloat accepts a number, "NaN", "Infinity", "-Infinity", or a string
// holding a number. Values out of the range of bitSize are rejected.
func parseFloat(tok token, bitSize int) (float64, bool) {
	var raw []byte
	switch tok.kind {
	case kindNumber:
		raw = tok.raw
	case kindString:
		switch tok.str {
		case "NaN":
			return math.NaN(), true
		case "Infinity":
			return math.Inf(1), true
		case "-Infinity":
			return math.Inf(-1), true
		}
		r, ok := numberInString(tok.str)
		if !ok {
			return 0, false
		}
		raw = r
	default:
		return 0, false
	}
	f, err := strconv.ParseFloat(string(raw), bitSize)
	if err != nil {
		return 0, false
	}
	return f, true
}

// parseBytes accepts standard or URL-safe Base64, padded or not.
func parseBytes(tok token) ([]byte, bool) {
	if tok.kind != kindString {
		return nil, false
	}
	s := tok.str
	enc := base64.StdEncoding
	if strings.ContainsAny(s, "-_") {
		enc = base64.URLEncoding
	}
	if len(s)%4 != 0 {
		enc = enc.WithPadding(base64.NoPadding)
	}
	b, err := enc.DecodeString(s)
	if err != nil {
		return nil, false
	}
	if b == nil {
		b = []byte{}
	}
	return b, true
}

func (d *decoder) list(l *dynamic.List, fd *desc.Field, depth int) error {
	tok, err := d.s.read()
	if err != nil {
		return err
	}
	if tok.kind != kindArrayOpen {
		return d.s.unexpected(tok)
	}
	for {
		tok, err := d.s.peek()
		if err != nil {
			return err
		}
		if tok.kind == kindArrayClose {
			_, _ = d.s.read()
			return nil
		}
		v, ok, err := d.singular(fd, depth)
		if err != nil {
			return err
		}
		if ok {
			if err := l.Append(v); err != nil {
				return d.errorf(tok.pos, "%v", err)
			}
		}
	}
}

func (d *decoder) mapValue(mp *dynamic.Map, fd *desc.Field, depth int) error {
	tok, err := d.s.read()
	if err != nil {
		return err
	}
	if tok.kind != kindObjectOpen {
		return d.s.unexpected(tok)
	}
	kfd, vfd := fd.MapKey(), fd.MapValue()
	for {
		tok, err := d.s.read()
		if err != nil {
			return err
		}
		switch tok.kind {
		case kindObjectClose:
			return nil
		case kindName:
		default:
			return d.s.unexpected(tok)
		}
		key, ok := parseMapKey(tok.str, kfd.Kind)
		if !ok {
			return d.errorf(tok.pos, "invalid value for %s key: %s", kfd.Kind, tok.rawString())
		}
		if _, dup := mp.Get(key); dup {
			return d.errorf(tok.pos, "duplicate map key %s", tok.rawString())
		}
		v, ok, err := d.singular(vfd, depth)
		if err != nil {
			return err
		}
		if ok {
			if err := mp.Set(key, v); err != nil {
				return d.errorf(tok.pos, "%v", err)
			}
		}
	}
}

// parseMapKey converts an object name to a map key of kind k.
func parseMapKey(s string, k desc.Kind) (any, bool) {
	switch k {
	case desc.KindString:
		return s, true
	case desc.KindBool:
		switch s {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	case desc.KindInt32, desc.KindSint32, desc.KindSfixed32:
		if n, err := strconv.ParseInt(s, 10, 32); err == nil {
			return int32(n), true
		}
	case desc.KindInt64, desc.KindSint64, desc.KindSfixed64:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, true
		}
	case desc.KindUint32, desc.KindFixed32:
		if n, err := strconv.ParseUint(s, 10, 32); err == nil {
			return uint32(n), true
		}
	case desc.KindUint64, desc.KindFixed64:
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			return n, true
		}
	}
	return nil, false
}

// skipValue skips one JSON value, checking its syntax.
func (d *decoder) skipValue(depth int) error {
	open := 0
	for {
		tok, err := d.s.read()
		if err != nil {
			return err
		}
		switch tok.kind {
		case kindObjectClose, kindArrayClose:
			open--
		case kindObjectOpen, kindArrayOpen:
			open++
			if open > depth {
				return d.errorf(tok.pos, "exceeded maximum recursion depth")
			}
		case kindEOF:
			return d.s.unexpected(tok)
		}
		if open == 0 {
			return nil
		}
	}
}
