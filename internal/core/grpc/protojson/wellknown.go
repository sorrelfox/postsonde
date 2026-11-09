package protojson

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

// The well-known types with a special JSON form, by full name.
const (
	anyName       = "google.protobuf.Any"
	timestampName = "google.protobuf.Timestamp"
	durationName  = "google.protobuf.Duration"
	structName    = "google.protobuf.Struct"
	listValueName = "google.protobuf.ListValue"
	fieldMaskName = "google.protobuf.FieldMask"
	emptyName     = "google.protobuf.Empty"
)

var wrapperKinds = map[string]desc.Kind{
	"google.protobuf.DoubleValue": desc.KindDouble,
	"google.protobuf.FloatValue":  desc.KindFloat,
	"google.protobuf.Int64Value":  desc.KindInt64,
	"google.protobuf.UInt64Value": desc.KindUint64,
	"google.protobuf.Int32Value":  desc.KindInt32,
	"google.protobuf.UInt32Value": desc.KindUint32,
	"google.protobuf.BoolValue":   desc.KindBool,
	"google.protobuf.StringValue": desc.KindString,
	"google.protobuf.BytesValue":  desc.KindBytes,
}

type (
	marshalFunc   func(*encoder, *dynamic.Message) error
	unmarshalFunc func(*decoder, *dynamic.Message, int) error
)

// wellKnownMarshaler returns the special form writer for md, or nil. A
// message with a well-known name but an unexpected shape (a user file that
// redefines google.protobuf) gets a writer that reports the problem.
func wellKnownMarshaler(md *desc.Message) marshalFunc {
	if !isWellKnown(md) {
		return nil
	}
	if err := checkWellKnownShape(md); err != nil {
		return func(*encoder, *dynamic.Message) error { return err }
	}
	switch md.FullName {
	case anyName:
		return (*encoder).anyValue
	case timestampName:
		return (*encoder).timestamp
	case durationName:
		return (*encoder).duration
	case structName:
		return (*encoder).structValue
	case listValueName:
		return (*encoder).listValue
	case valueName:
		return (*encoder).knownValue
	case fieldMaskName:
		return (*encoder).fieldMask
	case emptyName:
		return (*encoder).empty
	}
	return (*encoder).wrapper
}

func wellKnownUnmarshaler(md *desc.Message) unmarshalFunc {
	if !isWellKnown(md) {
		return nil
	}
	if err := checkWellKnownShape(md); err != nil {
		return func(d *decoder, _ *dynamic.Message, _ int) error {
			tok, _ := d.s.peek()
			return d.errorf(tok.pos, "%s", err.(*Error).Message)
		}
	}
	switch md.FullName {
	case anyName:
		return (*decoder).anyValue
	case timestampName:
		return (*decoder).timestamp
	case durationName:
		return (*decoder).duration
	case structName:
		return (*decoder).structValue
	case listValueName:
		return (*decoder).listValue
	case valueName:
		return (*decoder).knownValue
	case fieldMaskName:
		return (*decoder).fieldMask
	case emptyName:
		return (*decoder).empty
	}
	return (*decoder).wrapper
}

func isWellKnown(md *desc.Message) bool {
	switch md.FullName {
	case anyName, timestampName, durationName, structName, listValueName, valueName, fieldMaskName, emptyName:
		return true
	}
	_, ok := wrapperKinds[md.FullName]
	return ok
}

// checkWellKnownShape checks that md has the fields the special form uses.
func checkWellKnownShape(md *desc.Message) error {
	type want struct {
		num      int32
		kind     desc.Kind
		repeated bool
		typ      string // message or enum type
	}
	var fields []want
	switch md.FullName {
	case anyName:
		fields = []want{{1, desc.KindString, false, ""}, {2, desc.KindBytes, false, ""}}
	case timestampName, durationName:
		fields = []want{{1, desc.KindInt64, false, ""}, {2, desc.KindInt32, false, ""}}
	case structName:
		fields = []want{{1, desc.KindMessage, true, ""}}
	case listValueName:
		fields = []want{{1, desc.KindMessage, true, valueName}}
	case valueName:
		fields = []want{{1, desc.KindEnum, false, nullValueName}, {2, desc.KindDouble, false, ""},
			{3, desc.KindString, false, ""}, {4, desc.KindBool, false, ""},
			{5, desc.KindMessage, false, structName}, {6, desc.KindMessage, false, listValueName}}
	case fieldMaskName:
		fields = []want{{1, desc.KindString, true, ""}}
	case emptyName:
	default:
		fields = []want{{1, wrapperKinds[md.FullName], false, ""}}
	}
	for _, w := range fields {
		fd := md.FieldByNumber(w.num)
		ok := fd != nil && checkField(fd) == nil && fd.Kind == w.kind && (fd.Cardinality == desc.Repeated) == w.repeated
		if ok && w.typ != "" {
			ok = fd.Message != nil && fd.Message.FullName == w.typ || fd.Enum != nil && fd.Enum.FullName == w.typ
		}
		if ok && md.FullName == structName {
			ok = fd.IsMap() && fd.MapKey().Kind == desc.KindString && fd.MapValue().Message != nil && fd.MapValue().Message.FullName == valueName
		}
		if ok && md.FullName == valueName {
			ok = fd.Oneof != nil && !fd.Oneof.Synthetic
		}
		if !ok {
			return outputError("%s does not have the fields of the well-known type", md.FullName)
		}
	}
	return nil
}

// ---- Any ----

func (e *encoder) anyValue(m *dynamic.Message) error {
	md := m.Descriptor()
	typeFD, valueFD := md.FieldByNumber(1), md.FieldByNumber(2)
	if !m.Has(typeFD) {
		if !m.Has(valueFD) {
			e.w.startObject()
			e.w.endObject()
			return nil
		}
		return outputError("%s: type_url is not set", anyName)
	}
	typeURL := m.Get(typeFD).(string)
	value := m.Get(valueFD).([]byte)
	em, err := resolveAny(e.o.Resolver, typeURL)
	if err != nil {
		return outputError("%s: unable to resolve %q: %v", anyName, typeURL, err)
	}
	uo := dynamic.UnmarshalOptions{RecursionLimit: DefaultRecursionLimit, MaxSize: -1}
	if e.o.Resolver != nil {
		uo.Resolver = e.o.Resolver
	}
	if err := uo.Unmarshal(value, em); err != nil {
		return &Error{Offset: -1, Message: fmt.Sprintf("%s: unable to unmarshal %q: %v", anyName, typeURL, err), Err: err}
	}
	if fn := wellKnownMarshaler(em.Descriptor()); fn != nil {
		e.w.startObject()
		_ = e.w.name("@type")
		if err := e.w.string(typeURL); err != nil {
			return outputError("%s: type URL contains invalid UTF-8", anyName)
		}
		_ = e.w.name("value")
		if err := fn(e, em); err != nil {
			return err
		}
		e.w.endObject()
		return nil
	}
	return e.message(em, typeURL)
}

var errNotFound = errors.New("not found")

// resolveAny returns an empty message of the type a type URL names: the
// part after the last '/'.
func resolveAny(r Resolver, typeURL string) (*dynamic.Message, error) {
	name := typeURL
	if i := strings.LastIndexByte(typeURL, '/'); i >= 0 {
		name = typeURL[i+1:]
	}
	// Names are exact: *desc.Pool would also accept a leading dot.
	if r == nil || name == "" || name[0] == '.' {
		return nil, errNotFound
	}
	md := r.FindMessage(name)
	if md == nil {
		return nil, errNotFound
	}
	return dynamic.New(md), nil
}

func (d *decoder) anyValue(m *dynamic.Message, depth int) error {
	start, err := d.s.peek()
	if err != nil {
		return err
	}
	if start.kind != kindObjectOpen {
		return d.s.unexpected(start)
	}
	// Find "@type" with a copy of the scanner, since the embedded message's
	// fields may come before it.
	look := &decoder{s: d.s.clone(), o: UnmarshalOptions{RecursionLimit: depth}}
	typeTok, err := look.findTypeURL(depth)
	switch {
	case err == errEmptyObject:
		_, _ = d.s.read()
		_, _ = d.s.read()
		return nil
	case err == errMissingType:
		if d.o.DiscardUnknown {
			return d.skipValue(depth)
		}
		return d.errorf(start.pos, "missing \"@type\" field")
	case err != nil:
		return err
	}
	typeURL := typeTok.str
	em, err := resolveAny(d.o.Resolver, typeURL)
	if err != nil {
		return d.errorf(typeTok.pos, "unable to resolve %s: %v", typeTok.rawString(), err)
	}
	if fn := wellKnownUnmarshaler(em.Descriptor()); fn != nil {
		if err := d.anyWellKnownValue(fn, em, depth); err != nil {
			return err
		}
	} else if err := d.message(em, true, depth); err != nil {
		return err
	}
	b, err := dynamic.MarshalOptions{Deterministic: true}.Marshal(em)
	if err != nil {
		return d.errorf(start.pos, "error in marshaling Any.value field: %v", err)
	}
	md := m.Descriptor()
	if err := m.Set(md.FieldByNumber(1), typeURL); err != nil {
		return d.errorf(start.pos, "%v", err)
	}
	if err := m.Set(md.FieldByNumber(2), b); err != nil {
		return d.errorf(start.pos, "%v", err)
	}
	return nil
}

var (
	errEmptyObject = errors.New("empty object")
	errMissingType = errors.New(`missing "@type" field`)
)

// findTypeURL reads an object (the opening brace not yet read) and returns
// the token of its "@type" string.
func (d *decoder) findTypeURL(depth int) (token, error) {
	_, _ = d.s.read()
	var typeTok token
	found := false
	fields := 0
	for {
		tok, err := d.s.read()
		if err != nil {
			return token{}, err
		}
		switch tok.kind {
		case kindObjectClose:
			if !found {
				if fields > 0 {
					return token{}, errMissingType
				}
				return token{}, errEmptyObject
			}
			return typeTok, nil
		case kindName:
			fields++
			if tok.str != "@type" {
				if err := d.skipValue(depth); err != nil {
					return token{}, err
				}
				continue
			}
			if found {
				return token{}, d.errorf(tok.pos, `duplicate "@type" field`)
			}
			v, err := d.s.read()
			if err != nil {
				return token{}, err
			}
			if v.kind != kindString {
				return token{}, d.errorf(v.pos, "@type field value is not a string: %s", v.rawString())
			}
			if v.str == "" {
				return token{}, d.errorf(v.pos, "@type field contains empty value")
			}
			typeTok, found = v, true
		}
	}
}

// anyWellKnownValue reads {"@type": ..., "value": <special form>}.
func (d *decoder) anyWellKnownValue(fn unmarshalFunc, em *dynamic.Message, depth int) error {
	_, _ = d.s.read()
	found := false
	for {
		tok, err := d.s.read()
		if err != nil {
			return err
		}
		switch tok.kind {
		case kindObjectClose:
			// A missing "value" is accepted for Empty, as protobuf-go does.
			if !found && em.Descriptor().FullName != emptyName {
				return d.errorf(tok.pos, `missing "value" field`)
			}
			return nil
		case kindName:
			switch tok.str {
			case "@type":
				if _, err := d.s.read(); err != nil {
					return err
				}
			case "value":
				if found {
					return d.errorf(tok.pos, `duplicate "value" field`)
				}
				if err := fn(d, em, depth); err != nil {
					return err
				}
				found = true
			default:
				if d.o.DiscardUnknown {
					if err := d.skipValue(depth); err != nil {
						return err
					}
					continue
				}
				return d.errorf(tok.pos, "unknown field %s", tok.rawString())
			}
		}
	}
}

// ---- Wrappers ----

func (e *encoder) wrapper(m *dynamic.Message) error {
	fd := m.Descriptor().FieldByNumber(1)
	return e.singular(fd, m.Get(fd))
}

func (d *decoder) wrapper(m *dynamic.Message, _ int) error {
	fd := m.Descriptor().FieldByNumber(1)
	tok, _ := d.s.peek()
	v, ok, err := d.scalar(fd)
	if err != nil {
		return err
	}
	if ok {
		if err := m.Set(fd, v); err != nil {
			return d.errorf(tok.pos, "%v", err)
		}
	}
	return nil
}

// ---- Empty ----

func (e *encoder) empty(*dynamic.Message) error {
	e.w.startObject()
	e.w.endObject()
	return nil
}

func (d *decoder) empty(_ *dynamic.Message, depth int) error {
	tok, err := d.s.read()
	if err != nil {
		return err
	}
	if tok.kind != kindObjectOpen {
		return d.s.unexpected(tok)
	}
	for {
		tok, err := d.s.read()
		if err != nil {
			return err
		}
		switch tok.kind {
		case kindObjectClose:
			return nil
		case kindName:
			if d.o.DiscardUnknown {
				if err := d.skipValue(depth); err != nil {
					return err
				}
				continue
			}
			return d.errorf(tok.pos, "unknown field %s", tok.rawString())
		default:
			return d.s.unexpected(tok)
		}
	}
}

// ---- Struct, ListValue, Value ----

func (e *encoder) structValue(m *dynamic.Message) error {
	fd := m.Descriptor().FieldByNumber(1)
	mp, _ := m.Get(fd).(*dynamic.Map)
	return e.mapValue(fd, mp)
}

func (d *decoder) structValue(m *dynamic.Message, depth int) error {
	fd := m.Descriptor().FieldByNumber(1)
	return d.mapValue(m.Mutable(fd).(*dynamic.Map), fd, depth)
}

func (e *encoder) listValue(m *dynamic.Message) error {
	fd := m.Descriptor().FieldByNumber(1)
	l, _ := m.Get(fd).(*dynamic.List)
	return e.list(fd, l)
}

func (d *decoder) listValue(m *dynamic.Message, depth int) error {
	fd := m.Descriptor().FieldByNumber(1)
	return d.list(m.Mutable(fd).(*dynamic.List), fd, depth)
}

func (e *encoder) knownValue(m *dynamic.Message) error {
	md := m.Descriptor()
	fd := m.WhichOneof(md.FieldByNumber(1).Oneof)
	if fd == nil {
		return outputError("%s: none of the oneof fields is set", valueName)
	}
	v := m.Get(fd)
	if f, ok := v.(float64); ok && (math.IsNaN(f) || math.IsInf(f, 0)) {
		return outputError("%s.number_value: invalid %v value", valueName, f)
	}
	return e.singular(fd, v)
}

func (d *decoder) knownValue(m *dynamic.Message, depth int) error {
	tok, err := d.s.peek()
	if err != nil {
		return err
	}
	md := m.Descriptor()
	var num int32
	var v any
	switch tok.kind {
	case kindNull:
		_, _ = d.s.read()
		num, v = 1, dynamic.EnumNumber(0)
	case kindBool:
		_, _ = d.s.read()
		num, v = 4, tok.b
	case kindNumber:
		_, _ = d.s.read()
		f, ok := parseFloat(tok, 64)
		if !ok {
			return d.errorf(tok.pos, "invalid %s: %s", valueName, tok.rawString())
		}
		num, v = 2, f
	case kindString:
		// Always string_value, even for "NaN" or "1".
		_, _ = d.s.read()
		num, v = 3, tok.str
	case kindObjectOpen:
		fd := md.FieldByNumber(5)
		sub := dynamic.New(fd.Message)
		if err := d.structValue(sub, depth); err != nil {
			return err
		}
		num, v = 5, sub
	case kindArrayOpen:
		fd := md.FieldByNumber(6)
		sub := dynamic.New(fd.Message)
		if err := d.listValue(sub, depth); err != nil {
			return err
		}
		num, v = 6, sub
	default:
		return d.errorf(tok.pos, "invalid %s: %s", valueName, tok.rawString())
	}
	if err := m.Set(md.FieldByNumber(num), v); err != nil {
		return d.errorf(tok.pos, "%v", err)
	}
	return nil
}

// ---- Duration ----

const (
	maxDurationSeconds  = 315576000000
	maxNanos            = 999999999
	minTimestampSeconds = -62135596800 // 0001-01-01T00:00:00Z
	maxTimestampSeconds = 253402300799 // 9999-12-31T23:59:59Z
)

func secondsNanos(m *dynamic.Message) (int64, int32) {
	md := m.Descriptor()
	return m.Get(md.FieldByNumber(1)).(int64), m.Get(md.FieldByNumber(2)).(int32)
}

func (e *encoder) duration(m *dynamic.Message) error {
	secs, nanos := secondsNanos(m)
	if secs < -maxDurationSeconds || secs > maxDurationSeconds {
		return outputError("%s: seconds out of range %d", durationName, secs)
	}
	if nanos < -maxNanos || nanos > maxNanos {
		return outputError("%s: nanos out of range %d", durationName, nanos)
	}
	if secs > 0 && nanos < 0 || secs < 0 && nanos > 0 {
		return outputError("%s: signs of seconds and nanos do not match", durationName)
	}
	sign := ""
	n := int64(nanos)
	if secs < 0 || nanos < 0 {
		sign, secs, n = "-", -secs, -n
	}
	s := trimFraction(fmt.Sprintf("%s%d.%09d", sign, secs, n))
	return e.w.string(s + "s")
}

// trimFraction shortens a 9-digit fraction to 0, 3 or 6 digits when the
// dropped digits are zero.
func trimFraction(s string) string {
	s = strings.TrimSuffix(s, "000")
	s = strings.TrimSuffix(s, "000")
	return strings.TrimSuffix(s, ".000")
}

func (d *decoder) duration(m *dynamic.Message, _ int) error {
	tok, err := d.s.read()
	if err != nil {
		return err
	}
	if tok.kind != kindString {
		return d.s.unexpected(tok)
	}
	secs, nanos, ok := parseDuration(tok.str)
	if !ok {
		return d.errorf(tok.pos, "invalid %s value %s", durationName, tok.rawString())
	}
	if secs < -maxDurationSeconds || secs > maxDurationSeconds {
		return d.errorf(tok.pos, "%s value out of range: %s", durationName, tok.rawString())
	}
	return setSecondsNanos(d, m, tok, secs, nanos)
}

func setSecondsNanos(d *decoder, m *dynamic.Message, tok token, secs int64, nanos int32) error {
	md := m.Descriptor()
	if err := m.Set(md.FieldByNumber(1), secs); err != nil {
		return d.errorf(tok.pos, "%v", err)
	}
	if err := m.Set(md.FieldByNumber(2), nanos); err != nil {
		return d.errorf(tok.pos, "%v", err)
	}
	return nil
}

// parseDuration parses [+-](digits)[.digits]s with at most 9 fractional
// digits and at least one digit before or after the point ("1s", "1.s",
// ".5s", "-0.1s").
func parseDuration(in string) (int64, int32, bool) {
	if len(in) < 2 || in[len(in)-1] != 's' {
		return 0, 0, false
	}
	b := in[:len(in)-1]
	neg := false
	switch b[0] {
	case '-':
		neg = true
		b = b[1:]
	case '+':
		b = b[1:]
	}
	if b == "" {
		return 0, 0, false
	}
	var intp string
	switch {
	case b[0] == '0':
		b = b[1:]
	case '1' <= b[0] && b[0] <= '9':
		n := 1
		for n < len(b) && isDigit(b[n]) {
			n++
		}
		intp, b = b[:n], b[n:]
	case b[0] == '.':
	default:
		return 0, 0, false
	}
	hasFrac := false
	frac := []byte("000000000")
	if b != "" {
		if b[0] != '.' {
			return 0, 0, false
		}
		b = b[1:]
		n := 0
		for n < len(b) && n < 9 && isDigit(b[n]) {
			frac[n] = b[n]
			n++
		}
		if n < len(b) {
			return 0, 0, false
		}
		hasFrac = true
	}
	var secs int64
	if intp != "" {
		v, err := strconv.ParseInt(intp, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		secs = v
	}
	var nanos int64
	if hasFrac {
		if t := strings.TrimLeft(string(frac), "0"); t != "" {
			v, err := strconv.ParseInt(t, 10, 32)
			if err != nil {
				return 0, 0, false
			}
			nanos = v
		}
	}
	if neg {
		secs, nanos = -secs, -nanos
	}
	return secs, int32(nanos), true
}

// ---- Timestamp ----

func (e *encoder) timestamp(m *dynamic.Message) error {
	secs, nanos := secondsNanos(m)
	if secs < minTimestampSeconds || secs > maxTimestampSeconds {
		return outputError("%s: seconds out of range %d", timestampName, secs)
	}
	if nanos < 0 || nanos > maxNanos {
		return outputError("%s: nanos out of range %d", timestampName, nanos)
	}
	t := time.Unix(secs, int64(nanos)).UTC()
	s := trimFraction(t.Format("2006-01-02T15:04:05.000000000"))
	return e.w.string(s + "Z")
}

func (d *decoder) timestamp(m *dynamic.Message, _ int) error {
	tok, err := d.s.read()
	if err != nil {
		return err
	}
	if tok.kind != kindString {
		return d.s.unexpected(tok)
	}
	s := tok.str
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return d.errorf(tok.pos, "invalid %s value %s", timestampName, tok.rawString())
	}
	secs := t.Unix()
	if secs < minTimestampSeconds || secs > maxTimestampSeconds {
		return d.errorf(tok.pos, "%s value out of range: %s", timestampName, tok.rawString())
	}
	// At most 9 fractional digits.
	i := strings.LastIndexByte(s, '.')
	j := strings.LastIndexAny(s, "Z-+")
	if i >= 0 && j >= i && j-i > len(".999999999") {
		return d.errorf(tok.pos, "invalid %s value %s", timestampName, tok.rawString())
	}
	return setSecondsNanos(d, m, tok, secs, int32(t.Nanosecond()))
}

// ---- FieldMask ----

func (e *encoder) fieldMask(m *dynamic.Message) error {
	fd := m.Descriptor().FieldByNumber(1)
	l, _ := m.Get(fd).(*dynamic.List)
	paths := make([]string, 0, l.Len())
	for i := range l.Len() {
		s := l.Get(i).(string)
		if !validFullName(s) {
			return outputError("%s.paths contains invalid path: %q", fieldMaskName, s)
		}
		cc := camelCase(s)
		if snakeCase(cc) != s {
			return outputError("%s.paths contains irreversible value %q", fieldMaskName, s)
		}
		paths = append(paths, cc)
	}
	return e.w.string(strings.Join(paths, ","))
}

func (d *decoder) fieldMask(m *dynamic.Message, _ int) error {
	tok, err := d.s.read()
	if err != nil {
		return err
	}
	if tok.kind != kindString {
		return d.s.unexpected(tok)
	}
	str := strings.TrimSpace(tok.str)
	if str == "" {
		return nil
	}
	fd := m.Descriptor().FieldByNumber(1)
	l := m.Mutable(fd).(*dynamic.List)
	for _, p := range strings.Split(str, ",") {
		s := snakeCase(p)
		if strings.Contains(p, "_") || !validFullName(s) {
			return d.errorf(tok.pos, "%s.paths contains invalid path: %q", fieldMaskName, p)
		}
		if err := l.Append(s); err != nil {
			return d.errorf(tok.pos, "%v", err)
		}
	}
	return nil
}

// camelCase converts snake_case to lowerCamelCase: an underscore is dropped
// and a lowercase ASCII letter after it is upper-cased.
func camelCase(s string) string {
	var b []byte
	under := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' {
			if under && 'a' <= c && c <= 'z' {
				c -= 'a' - 'A'
			}
			b = append(b, c)
		}
		under = c == '_'
	}
	return string(b)
}

// snakeCase converts lowerCamelCase to snake_case: every uppercase ASCII
// letter becomes an underscore and its lowercase letter.
func snakeCase(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			b = append(b, '_')
			c += 'a' - 'A'
		}
		b = append(b, c)
	}
	return string(b)
}

// validFullName reports whether s is identifiers joined by dots.
func validFullName(s string) bool {
	for part := range strings.SplitSeq(s, ".") {
		if part == "" || isDigit(part[0]) {
			return false
		}
		for i := 0; i < len(part); i++ {
			c := part[i]
			if c != '_' && !isDigit(c) && !('a' <= c && c <= 'z') && !('A' <= c && c <= 'Z') {
				return false
			}
		}
	}
	return true
}
