package openapiimport

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/openapi"
)

// Direction says which side of an exchange an example is for: request
// examples leave out readOnly properties, response examples writeOnly ones.
type Direction int

// Directions.
const (
	ForRequest Direction = iota
	ForResponse
)

// ExampleOptions configure SchemaExample.
type ExampleOptions struct {
	Direction Direction
	// MaxDepth bounds the nesting of generated objects and arrays (default
	// 8); MaxValues the values generated in all (default 2000).
	MaxDepth  int
	MaxValues int
	// Names maps component schemas to their names (see SchemaNames), for
	// discriminator values and XML element names.
	Names map[*openapi.Schema]string
}

// ExampleNotes say where a generated example is shorter than its schema.
type ExampleNotes struct {
	// Recursive: a property or item that leads back into a schema being
	// generated was left out (or its array left empty).
	Recursive bool
	// Truncated: MaxDepth or MaxValues was reached.
	Truncated bool
}

// SchemaNames maps each component schema of doc to its name.
func SchemaNames(doc *openapi.Document) map[*openapi.Schema]string {
	names := map[*openapi.Schema]string{}
	for _, e := range doc.Components.Schemas {
		if e.Value != nil {
			if _, ok := names[e.Value]; !ok {
				names[e.Value] = e.Name
			}
		}
	}
	return names
}

// SchemaExample returns an example value of s as indented JSON, or nil
// when s allows nothing to be generated (nil, false, an unresolved
// reference). The schema's own example, examples, const, default or first
// enum value is used where there is one; otherwise a value is made from
// its type and format, within its bounds. Recursive schemas are walked
// with the set of schemas on the current path, so cycles end.
func SchemaExample(s *openapi.Schema, opts ExampleOptions) (json.RawMessage, ExampleNotes) {
	g := newGen(opts)
	v, ok := g.value(s, 0)
	if !ok {
		return nil, g.notes
	}
	return indent(v), g.notes
}

// MediaTypeExample returns the example of a media type: its example, the
// first of its examples with a value, or one generated from its schema.
// name is the named example used ("" otherwise); external is set when the
// examples only point at external files.
func MediaTypeExample(mt *openapi.MediaType, opts ExampleOptions) (v json.RawMessage, name string, external bool, notes ExampleNotes) {
	if mt == nil {
		return nil, "", false, notes
	}
	if len(mt.Example) > 0 {
		return indentRaw(mt.Example), "", false, notes
	}
	for _, e := range mt.Examples {
		if e.Value == nil {
			continue
		}
		if len(e.Value.Value) > 0 {
			return indentRaw(e.Value.Value), e.Name, false, notes
		}
		if e.Value.ExternalValue != "" {
			external = true
		}
	}
	v, notes = SchemaExample(mt.Schema, opts)
	return v, "", external, notes
}

// value is a generated or decoded JSON value: json.RawMessage for
// scalars and examples, *object, or []value.
type value any

// object is a JSON object with its keys in order.
type object struct {
	keys []string
	vals []value
}

func (o *object) set(k string, v value) {
	for i, x := range o.keys {
		if x == k {
			o.vals[i] = v
			return
		}
	}
	o.keys = append(o.keys, k)
	o.vals = append(o.vals, v)
}

func (o *object) has(k string) bool {
	for _, x := range o.keys {
		if x == k {
			return true
		}
	}
	return false
}

type gen struct {
	opts   ExampleOptions
	onPath map[*openapi.Schema]bool
	count  int
	notes  ExampleNotes
}

func newGen(opts ExampleOptions) *gen {
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 8
	}
	if opts.MaxValues <= 0 {
		opts.MaxValues = 2000
	}
	return &gen{opts: opts, onPath: map[*openapi.Schema]bool{}}
}

var rawNull = json.RawMessage("null")

// literal returns the value a schema gives itself: example, examples[0],
// const, default or the first non-null enum value.
func literal(s *openapi.Schema) json.RawMessage {
	switch {
	case len(s.Example) > 0:
		return s.Example
	case len(s.Examples) > 0 && len(s.Examples[0]) > 0:
		return s.Examples[0]
	case len(s.Const) > 0:
		return s.Const
	case len(s.Default) > 0:
		return s.Default
	}
	for _, e := range s.Enum {
		if string(bytes.TrimSpace(e)) != "null" {
			return e
		}
	}
	if len(s.Enum) > 0 {
		return s.Enum[0]
	}
	return nil
}

// value generates s at depth; false means leave it out.
func (g *gen) value(s *openapi.Schema, depth int) (value, bool) {
	if s == nil || s.Ref != "" {
		return nil, false
	}
	if s.Bool != nil {
		if !*s.Bool {
			return nil, false
		}
		return rawNull, true
	}
	if lit := literal(s); lit != nil {
		return decodeRaw(lit), true
	}
	if g.onPath[s] {
		g.notes.Recursive = true
		return nil, false
	}
	if depth > g.opts.MaxDepth || g.count >= g.opts.MaxValues {
		g.notes.Truncated = true
		return nil, false
	}
	g.count++
	g.onPath[s] = true
	defer delete(g.onPath, s)

	// A wrapper around one schema (3.1's $ref with siblings, allOf of one)
	// is that schema, with its literals.
	if only := single(s); only != nil {
		return g.value(only, depth)
	}

	m := g.merge(s)
	if lit := m.literal(); lit != nil {
		return decodeRaw(lit), true
	}
	typ := m.typ()
	var alt value
	altOK := false
	if len(m.alts) > 0 {
		alt, altOK = g.alternative(m.alts, depth)
		if typ == "" || typ == "null" {
			if altOK {
				return alt, true
			}
			if typ == "" {
				return nil, false
			}
		}
	}
	switch typ {
	case "object":
		o := g.object(s, m, depth)
		if ao, ok := alt.(*object); ok && altOK {
			for i, k := range ao.keys {
				if !o.has(k) {
					o.set(k, ao.vals[i])
				}
			}
		}
		return o, true
	case "array":
		return g.array(m, depth), true
	case "null":
		return rawNull, true
	case "":
		return rawNull, true
	}
	return scalar(typ, m), true
}

// single returns the one schema s only wraps: s has exactly one allOf,
// anyOf or oneOf member and no type, properties or items of its own.
func single(s *openapi.Schema) *openapi.Schema {
	if len(s.Type) > 0 || len(s.Properties) > 0 || s.Items != nil || len(s.PrefixItems) > 0 || s.AdditionalProperties != nil {
		return nil
	}
	n := len(s.AllOf) + len(s.AnyOf) + len(s.OneOf)
	if n != 1 {
		return nil
	}
	switch {
	case len(s.AllOf) == 1:
		return s.AllOf[0]
	case len(s.AnyOf) == 1:
		return s.AnyOf[0]
	}
	return s.OneOf[0]
}

// alternative generates the first oneOf/anyOf alternative that is not
// only null and not being generated already; else null when one allows
// it.
func (g *gen) alternative(alts []*openapi.Schema, depth int) (value, bool) {
	nullable := false
	for _, a := range alts {
		if a == nil {
			continue
		}
		if len(a.Type) == 1 && a.Type[0] == "null" {
			nullable = true
			continue
		}
		if g.onPath[a] {
			g.notes.Recursive = true
			continue
		}
		if v, ok := g.value(a, depth); ok {
			return v, true
		}
	}
	if nullable {
		return rawNull, true
	}
	return nil, false
}

// merged is a schema with its allOf members folded in.
type merged struct {
	parts []*openapi.Schema // s and its allOf members, depth first
	props []openapi.Entry[*openapi.Schema]
	alts  []*openapi.Schema
	disc  *openapi.Discriminator
	xml   *openapi.XML
}

func (g *gen) merge(s *openapi.Schema) *merged {
	m := &merged{}
	seen := map[*openapi.Schema]bool{}
	var walk func(x *openapi.Schema, top bool)
	walk = func(x *openapi.Schema, top bool) {
		if x == nil || seen[x] || x.Bool != nil || x.Ref != "" {
			return
		}
		if !top && g.onPath[x] {
			g.notes.Recursive = true
			return
		}
		seen[x] = true
		m.parts = append(m.parts, x)
		for _, p := range x.Properties {
			found := false
			for i := range m.props {
				if m.props[i].Name == p.Name {
					m.props[i].Value, found = p.Value, true
				}
			}
			if !found {
				m.props = append(m.props, p)
			}
		}
		if m.alts == nil {
			if len(x.OneOf) > 0 {
				m.alts = x.OneOf
			} else if len(x.AnyOf) > 0 {
				m.alts = x.AnyOf
			}
		}
		if m.disc == nil && x.Discriminator != nil {
			m.disc = x.Discriminator
		}
		if m.xml == nil && x.XML != nil {
			m.xml = x.XML
		}
		for _, a := range x.AllOf {
			walk(a, false)
		}
	}
	walk(s, true)
	return m
}

// literal returns the first literal of an allOf member.
func (m *merged) literal() json.RawMessage {
	for _, p := range m.parts[1:] {
		if lit := literal(p); lit != nil {
			return lit
		}
	}
	return nil
}

// typ returns the first non-null type of the parts, or one inferred from
// their keywords; "null" when null is the only type; "" when nothing
// says.
func (m *merged) typ() string {
	null := false
	for _, p := range m.parts {
		for _, t := range p.Type {
			if t != "null" {
				return t
			}
			null = true
		}
	}
	for _, p := range m.parts {
		switch {
		case len(p.Properties) > 0 || p.AdditionalProperties != nil || len(p.Required) > 0 || len(p.PatternProperties) > 0:
			return "object"
		case p.Items != nil || len(p.PrefixItems) > 0:
			return "array"
		case p.Format != "" || p.MinLength != nil || p.MaxLength != nil || p.Pattern != "":
			return "string"
		case p.Minimum != "" || p.Maximum != "" || p.ExclusiveMinimum != "" || p.ExclusiveMaximum != "" || p.MultipleOf != "":
			return "number"
		}
	}
	if null {
		return "null"
	}
	return ""
}

func (m *merged) first(get func(*openapi.Schema) bool) *openapi.Schema {
	for _, p := range m.parts {
		if get(p) {
			return p
		}
	}
	return nil
}

func (g *gen) object(s *openapi.Schema, m *merged, depth int) *object {
	o := &object{}
	for _, p := range m.props {
		ps := p.Value
		if ps == nil {
			continue
		}
		if g.opts.Direction == ForRequest && ps.ReadOnly || g.opts.Direction == ForResponse && ps.WriteOnly {
			continue
		}
		if v, ok := g.value(ps, depth+1); ok {
			o.set(p.Name, v)
		}
	}
	if len(m.props) == 0 {
		if ap := m.first(func(p *openapi.Schema) bool {
			return p.AdditionalProperties != nil && p.AdditionalProperties.Bool == nil
		}); ap != nil {
			if v, ok := g.value(ap.AdditionalProperties, depth+1); ok {
				o.set("additionalProp1", v)
			}
		}
	}
	if d := m.disc; d != nil && d.PropertyName != "" {
		if v := discriminatorValue(d, g.opts.Names[s]); v != "" {
			prop := m.propSchema(d.PropertyName)
			if prop == nil || literal(prop) == nil {
				q, _ := json.Marshal(v)
				o.set(d.PropertyName, json.RawMessage(q))
			}
		}
	}
	return o
}

func (m *merged) propSchema(name string) *openapi.Schema {
	for _, p := range m.props {
		if p.Name == name {
			return p.Value
		}
	}
	return nil
}

// discriminatorValue is the value of the discriminator property for the
// schema named name: the mapping key that points at it, else its name when
// the mapping is empty, else the first mapping key.
func discriminatorValue(d *openapi.Discriminator, name string) string {
	if name != "" {
		for _, e := range d.Mapping {
			if e.Value == name || strings.HasSuffix(e.Value, "/"+name) {
				return e.Name
			}
		}
		if len(d.Mapping) == 0 {
			return name
		}
	}
	if len(d.Mapping) > 0 {
		return d.Mapping[0].Name
	}
	return ""
}

func (g *gen) array(m *merged, depth int) []value {
	out := []value{}
	if p := m.first(func(p *openapi.Schema) bool { return len(p.PrefixItems) > 0 }); p != nil {
		for _, it := range p.PrefixItems {
			if v, ok := g.value(it, depth+1); ok {
				out = append(out, v)
			} else {
				out = append(out, rawNull)
			}
		}
		return out
	}
	if p := m.first(func(p *openapi.Schema) bool { return p.Items != nil }); p != nil {
		if v, ok := g.value(p.Items, depth+1); ok {
			out = append(out, v)
		}
	}
	return out
}

// Example strings by format.
var formatExamples = map[string]string{
	"date-time":     "2024-01-01T00:00:00Z",
	"date":          "2024-01-01",
	"time":          "00:00:00Z",
	"duration":      "P1D",
	"email":         "user@example.com",
	"idn-email":     "user@example.com",
	"hostname":      "example.com",
	"idn-hostname":  "example.com",
	"ipv4":          "192.0.2.1",
	"ipv6":          "2001:db8::1",
	"uri":           "https://example.com",
	"iri":           "https://example.com",
	"uri-reference": "/example",
	"iri-reference": "/example",
	"uri-template":  "https://example.com/{id}",
	"url":           "https://example.com",
	"uuid":          "3fa85f64-5717-4562-b3fc-2c963f66afa6",
	"byte":          "c3RyaW5n",
	"password":      "password",
	"json-pointer":  "/example",
	"regex":         ".*",
}

// scalar makes a string, number, integer or boolean for the merged parts.
func scalar(typ string, m *merged) value {
	switch typ {
	case "boolean":
		return json.RawMessage("true")
	case "integer", "number":
		return json.RawMessage(number(typ == "integer", m))
	}
	s := "string"
	if f := m.first(func(p *openapi.Schema) bool { return p.Format != "" }); f != nil {
		if ex, ok := formatExamples[f.Format]; ok {
			s = ex
		}
	}
	if p := m.first(func(p *openapi.Schema) bool { return p.MinLength != nil }); p != nil && len(s) < *p.MinLength {
		n := *p.MinLength
		if n > 256 {
			n = 256
		}
		s += strings.Repeat("x", n-len(s))
	}
	if p := m.first(func(p *openapi.Schema) bool { return p.MaxLength != nil }); p != nil && *p.MaxLength >= 0 && len(s) > *p.MaxLength {
		s = s[:*p.MaxLength]
	}
	q, _ := json.Marshal(s)
	return json.RawMessage(q)
}

// number returns 0, or the value nearest to 0 that the bounds allow.
func number(integer bool, m *merged) string {
	get := func(f func(*openapi.Schema) json.Number) (float64, bool) {
		for _, p := range m.parts {
			if n := f(p); n != "" {
				if v, err := n.Float64(); err == nil && !math.IsInf(v, 0) && !math.IsNaN(v) {
					return v, true
				}
			}
		}
		return 0, false
	}
	v := 0.0
	if lo, ok := get(func(p *openapi.Schema) json.Number { return p.Minimum }); ok && v < lo {
		v = lo
	}
	if lo, ok := get(func(p *openapi.Schema) json.Number { return p.ExclusiveMinimum }); ok && v <= lo {
		v = lo + 1
		if integer {
			v = math.Floor(lo) + 1
		}
	}
	if hi, ok := get(func(p *openapi.Schema) json.Number { return p.Maximum }); ok && v > hi {
		v = hi
	}
	if hi, ok := get(func(p *openapi.Schema) json.Number { return p.ExclusiveMaximum }); ok && v >= hi {
		v = hi - 1
		if integer {
			v = math.Ceil(hi) - 1
		}
	}
	if mul, ok := get(func(p *openapi.Schema) json.Number { return p.MultipleOf }); ok && mul > 0 && v != 0 {
		v = math.Ceil(v/mul) * mul
	}
	if integer {
		v = math.Ceil(v)
		if math.Abs(v) < 1<<53 {
			return strconv.FormatInt(int64(v), 10)
		}
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// decodeRaw decodes JSON text into a value, keeping object key order.
// Text that is not valid JSON is kept as it is.
func decodeRaw(raw json.RawMessage) value {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := decodeValue(dec, 0)
	if err != nil {
		return raw
	}
	return v
}

func decodeValue(dec *json.Decoder, depth int) (value, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if depth > 200 {
		return nil, io.ErrUnexpectedEOF
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := &object{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, _ := kt.(string)
				v, err := decodeValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				o.set(k, v)
			}
			_, err := dec.Token()
			return o, err
		case '[':
			out := []value{}
			for dec.More() {
				v, err := decodeValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
			_, err := dec.Token()
			return out, err
		}
		return nil, io.ErrUnexpectedEOF
	case string:
		q, _ := marshalString(t)
		return json.RawMessage(q), nil
	case json.Number:
		return json.RawMessage(t.String()), nil
	case bool:
		if t {
			return json.RawMessage("true"), nil
		}
		return json.RawMessage("false"), nil
	case nil:
		return rawNull, nil
	}
	return nil, io.ErrUnexpectedEOF
}

func marshalString(s string) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// compact writes v as compact JSON.
func compact(b *bytes.Buffer, v value) {
	switch t := v.(type) {
	case json.RawMessage:
		b.Write(t)
	case *object:
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			q, _ := marshalString(k)
			b.Write(q)
			b.WriteByte(':')
			compact(b, t.vals[i])
		}
		b.WriteByte('}')
	case []value:
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			compact(b, x)
		}
		b.WriteByte(']')
	default:
		b.WriteString("null")
	}
}

func compactString(v value) string {
	var b bytes.Buffer
	compact(&b, v)
	return b.String()
}

// indent returns v as JSON indented by two spaces.
func indent(v value) json.RawMessage {
	var b bytes.Buffer
	compact(&b, v)
	return indentRaw(b.Bytes())
}

func indentRaw(raw json.RawMessage) json.RawMessage {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return raw
	}
	return out.Bytes()
}

// text returns a scalar's text as a parameter or form value: a string
// unquoted, null empty, other scalars as written, objects and arrays as
// compact JSON.
func text(v value) string {
	if v == nil {
		return ""
	}
	raw, ok := v.(json.RawMessage)
	if !ok {
		return compactString(v)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	if string(raw) == "null" {
		return ""
	}
	return string(raw)
}
