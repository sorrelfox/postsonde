package yaml

import (
	"bytes"
	"encoding/json"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Core schema (YAML 1.2) patterns for plain scalars.
var (
	reInt   = regexp.MustCompile(`^(?:[-+]?[0-9]+|0o[0-7]+|0x[0-9a-fA-F]+)$`)
	reFloat = regexp.MustCompile(`^(?:[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][-+]?[0-9]+)?|[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN))$`)
	// reJSONNumber is the JSON number grammar (RFC 8259).
	reJSONNumber = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][-+]?[0-9]+)?$`)
)

// resolvePlain returns the core schema tag of a plain scalar.
func resolvePlain(s string) string {
	switch s {
	case "", "~", "null", "Null", "NULL":
		return TagNull
	case "true", "True", "TRUE", "false", "False", "FALSE":
		return TagBool
	}
	if s[0] != '-' && s[0] != '+' && s[0] != '.' && (s[0] < '0' || s[0] > '9') {
		return TagStr
	}
	if reInt.MatchString(s) {
		return TagInt
	}
	if reFloat.MatchString(s) {
		return TagFloat
	}
	return TagStr
}

// jsonInt returns an !!int's value as a JSON number.
func jsonInt(s string) string {
	neg := strings.HasPrefix(s, "-")
	digits := strings.TrimLeft(s, "+-")
	var v big.Int
	switch {
	case strings.HasPrefix(digits, "0x"):
		v.SetString(digits[2:], 16)
	case strings.HasPrefix(digits, "0o"):
		v.SetString(digits[2:], 8)
	default:
		v.SetString(digits, 10)
	}
	if neg {
		v.Neg(&v)
	}
	return v.String()
}

// jsonFloat returns a !!float's value as a JSON number; ok is false for
// .inf and .nan, which JSON cannot represent.
func jsonFloat(s string) (string, bool) {
	if t := strings.TrimPrefix(s, "+"); reJSONNumber.MatchString(t) {
		return t, true
	}
	f, err := strconv.ParseFloat(strings.TrimPrefix(s, "+"), 64)
	if err != nil {
		switch strings.ToLower(strings.TrimLeft(s, "+-")) {
		case ".inf", ".nan":
			return "", false
		}
		// Out of range for float64: keep the digits, normalized.
		r, ok := new(big.Float).SetPrec(256).SetString(s)
		if !ok {
			return "", false
		}
		return r.Text('g', -1), true
	}
	if f != f || f > 1.7976931348623157e308 || f < -1.7976931348623157e308 {
		return "", false
	}
	return strconv.FormatFloat(f, 'g', -1, 64), true
}

// JSON returns the node as compact JSON with the DefaultLimits: mappings
// become objects with their keys in document order, aliases are expanded
// and merge keys (<<) applied. Scalar keys become strings. Errors are
// *Error: duplicate keys (CodeInvalidYAML); collection keys, .inf and
// .nan (CodeUnsupported); too many nodes after alias expansion
// (CodeTooLarge).
func (n *Node) JSON() ([]byte, error) { return n.JSONLimits(Limits{}) }

// JSONLimits is JSON with explicit limits (MaxDepth and MaxNodes apply).
func (n *Node) JSONLimits(lim Limits) (out []byte, err error) {
	w := &jsonWriter{lim: lim.withDefaults()}
	defer func() {
		if r := recover(); r != nil {
			a, ok := r.(abort)
			if !ok {
				panic(r)
			}
			out, err = nil, a.err
		}
	}()
	w.node(n, 0)
	return w.buf.Bytes(), nil
}

// Decode returns the node as the JSON data model: nil, bool, string,
// json.Number, []any and map[string]any.
func (n *Node) Decode() (any, error) {
	data, err := n.JSON()
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// ToJSON parses a one-document stream and returns it as JSON.
func ToJSON(data []byte) ([]byte, error) {
	n, err := Parse(data)
	if err != nil {
		return nil, err
	}
	return n.JSON()
}

// Unmarshal parses a one-document stream and decodes it into v with
// encoding/json, so v uses json struct tags. Errors from Parse and JSON
// are *Error; type mismatches are encoding/json errors.
func Unmarshal(data []byte, v any) error {
	js, err := ToJSON(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(js, v)
}

type jsonWriter struct {
	buf   bytes.Buffer
	lim   Limits
	nodes int
}

func (w *jsonWriter) fail(n *Node, code, msg string) {
	panic(abort{&Error{Code: code, Message: msg, Line: n.Line, Column: n.Column}})
}

func (w *jsonWriter) count(n *Node, depth int) {
	w.nodes++
	if w.nodes > w.lim.MaxNodes {
		w.fail(n, CodeTooLarge, "more than "+strconv.Itoa(w.lim.MaxNodes)+" nodes after expanding aliases")
	}
	if depth > w.lim.MaxDepth {
		w.fail(n, CodeTooLarge, "nested more than "+strconv.Itoa(w.lim.MaxDepth)+" levels deep after expanding aliases")
	}
}

func (w *jsonWriter) node(n *Node, depth int) {
	w.count(n, depth)
	switch n.Kind {
	case AliasNode:
		w.node(n.Target, depth)
	case ScalarNode:
		w.buf.WriteString(w.scalar(n))
	case SequenceNode:
		w.buf.WriteByte('[')
		for i, it := range n.Items {
			if i > 0 {
				w.buf.WriteByte(',')
			}
			w.node(it, depth+1)
		}
		w.buf.WriteByte(']')
	case MappingNode:
		w.buf.WriteByte('{')
		for i, e := range w.entries(n, depth) {
			if i > 0 {
				w.buf.WriteByte(',')
			}
			w.buf.WriteString(quote(e.key))
			w.buf.WriteByte(':')
			w.node(e.value, depth+1)
		}
		w.buf.WriteByte('}')
	}
}

func (w *jsonWriter) scalar(n *Node) string {
	switch n.Tag {
	case TagNull:
		return "null"
	case TagBool:
		return strings.ToLower(n.Value)
	case TagInt:
		return jsonInt(n.Value)
	case TagFloat:
		s, ok := jsonFloat(n.Value)
		if !ok {
			w.fail(n, CodeUnsupported, n.Value+" has no JSON equivalent")
		}
		return s
	}
	return quote(n.Value)
}

type entry struct {
	key   string
	value *Node
}

// keyString returns a scalar key as a JSON object key.
func (w *jsonWriter) keyString(k *Node) string {
	r := k.Resolve()
	if r.Kind != ScalarNode {
		w.fail(k, CodeUnsupported, "a "+r.Kind.String()+" cannot be a key in JSON")
	}
	switch r.Tag {
	case TagStr:
		return r.Value
	case TagFloat:
		if s, ok := jsonFloat(r.Value); ok {
			return s
		}
		return r.Value
	}
	return w.scalar(r)
}

func isMergeKey(k *Node) bool {
	return k.Kind == ScalarNode && k.Style == Plain && k.Value == "<<"
}

// entries returns a mapping's entries with merge keys applied: explicit
// keys win over merged ones, and earlier merged mappings over later ones.
func (w *jsonWriter) entries(n *Node, depth int) []entry {
	if depth > w.lim.MaxDepth {
		w.fail(n, CodeTooLarge, "nested more than "+strconv.Itoa(w.lim.MaxDepth)+" levels deep after expanding aliases")
	}
	explicit := map[string]bool{}
	for _, p := range n.Pairs {
		if isMergeKey(p.Key) {
			continue
		}
		k := w.keyString(p.Key)
		if explicit[k] {
			w.fail(p.Key, CodeInvalidYAML, "duplicate mapping key "+strconv.Quote(k))
		}
		explicit[k] = true
	}
	var out []entry
	have := map[string]bool{}
	for _, p := range n.Pairs {
		if !isMergeKey(p.Key) {
			out = append(out, entry{w.keyString(p.Key), p.Value})
			have[w.keyString(p.Key)] = true
			continue
		}
		v := p.Value.Resolve()
		sources := []*Node{p.Value}
		if v.Kind == SequenceNode {
			sources = v.Items
		}
		for _, s := range sources {
			m := s.Resolve()
			if m.Kind != MappingNode {
				w.fail(s, CodeInvalidYAML, "a merge key (<<) needs a mapping or a list of mappings")
			}
			for _, e := range w.entries(m, depth+1) {
				w.count(e.value, depth)
				if !have[e.key] && !explicit[e.key] {
					out = append(out, e)
					have[e.key] = true
				}
			}
		}
	}
	return out
}

// quote returns s as a JSON string. HTML characters are not escaped.
func quote(s string) string {
	b := make([]byte, 0, len(s)+2)
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' && c < utf8.RuneSelf {
			b = append(b, c)
			i++
			continue
		}
		if c >= utf8.RuneSelf {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == ' ' || r == ' ' {
				b = append(b, `\u202`...)
				b = append(b, "89"[r-' '])
			} else {
				b = append(b, s[i:i+size]...)
			}
			i += size
			continue
		}
		switch c {
		case '"':
			b = append(b, `\"`...)
		case '\\':
			b = append(b, `\\`...)
		case '\n':
			b = append(b, `\n`...)
		case '\r':
			b = append(b, `\r`...)
		case '\t':
			b = append(b, `\t`...)
		default:
			b = append(b, `\u00`...)
			b = append(b, "0123456789abcdef"[c>>4], "0123456789abcdef"[c&15])
		}
		i++
	}
	return string(append(b, '"'))
}
