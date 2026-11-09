package graphqlmock

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Values are JSON as encoding/json decodes it with UseNumber: nil, bool,
// string, json.Number, []any and map[string]any. A response adds object,
// whose members keep the order of the selection set, and introspection
// objects hold lazyField members, computed with the field's arguments.

// member is one member of an object.
type member struct {
	key string
	val any
}

// object is a JSON object whose members keep their order.
type object []member

// lazyField is a member of an introspection object computed from the
// field's arguments (fields(includeDeprecated:)) or on demand (ofType).
type lazyField func(args map[string]any) any

// decodeJSON decodes one JSON value with numbers kept as json.Number.
func decodeJSON(text string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errTrailing
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err == nil {
		return nil, errTrailing
	}
	return v, nil
}

type jsonError string

func (e jsonError) Error() string { return string(e) }

const errTrailing = jsonError("unexpected text after the JSON value")

// writeJSON writes v as compact JSON. Maps are written with sorted keys,
// strings without HTML escaping.
func writeJSON(b *bytes.Buffer, v any) {
	switch v := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeString(b, v)
	case json.Number:
		b.WriteString(v.String())
	case int:
		b.WriteString(strconv.Itoa(v))
	case int64:
		b.WriteString(strconv.FormatInt(v, 10))
	case float64:
		if math.IsInf(v, 0) || math.IsNaN(v) {
			b.WriteString("null")
			return
		}
		b.WriteString(strconv.FormatFloat(v, 'g', -1, 64))
	case []any:
		b.WriteByte('[')
		for i, x := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSON(b, x)
		}
		b.WriteByte(']')
	case object:
		b.WriteByte('{')
		for i, m := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, m.key)
			b.WriteByte(':')
			writeJSON(b, m.val)
		}
		b.WriteByte('}')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k)
			b.WriteByte(':')
			writeJSON(b, v[k])
		}
		b.WriteByte('}')
	default:
		// Not produced by the mock; written as JSON null rather than
		// failing the response.
		b.WriteString("null")
	}
}

const hexDigits = "0123456789abcdef"

func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			b.WriteString(s[start:i])
			switch c {
			case '"', '\\':
				b.WriteByte('\\')
				b.WriteByte(c)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[c>>4])
				b.WriteByte(hexDigits[c&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteString(s[start:i])
			b.WriteString(`�`)
			i += size
			start = i
			continue
		}
		if r == ' ' || r == ' ' {
			b.WriteString(s[start:i])
			b.WriteString(`\u202`)
			b.WriteByte(hexDigits[r&0xf])
			i += size
			start = i
			continue
		}
		i += size
	}
	b.WriteString(s[start:])
	b.WriteByte('"')
}

// inspect prints a value for a message, as graphql-js does: JSON, cut
// after 60 characters.
func inspect(v any) string {
	var b bytes.Buffer
	writeJSON(&b, v)
	s := b.String()
	if utf8.RuneCountInString(s) > 60 {
		r := []rune(s)
		s = string(r[:57]) + "..."
	}
	return s
}

// looseEqual compares an argument value with the value an override
// matches: scalars by their text (1 matches "1" for an ID), lists item by
// item, objects member by member.
func looseEqual(got, want any) bool {
	switch w := want.(type) {
	case nil:
		return got == nil
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !looseEqual(g[i], w[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok || !looseEqual(gv, wv) {
				return false
			}
		}
		return true
	}
	gs, ok1 := scalarText(got)
	ws, ok2 := scalarText(want)
	if !ok1 || !ok2 {
		return false
	}
	if gs == ws {
		return true
	}
	// 1 and 1.0 are the same number.
	gf, err1 := strconv.ParseFloat(gs, 64)
	wf, err2 := strconv.ParseFloat(ws, 64)
	return err1 == nil && err2 == nil && gf == wf && isNumber(got) && isNumber(want)
}

func scalarText(v any) (string, bool) {
	switch v := v.(type) {
	case string:
		return v, true
	case json.Number:
		return v.String(), true
	case bool:
		return strconv.FormatBool(v), true
	}
	return "", false
}

func isNumber(v any) bool {
	_, ok := v.(json.Number)
	return ok
}

// canonical is the JSON text of v with object keys sorted, for seeds.
func canonical(v any) string {
	var b bytes.Buffer
	writeJSON(&b, v)
	return b.String()
}
