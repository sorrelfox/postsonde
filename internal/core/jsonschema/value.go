package jsonschema

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/jsonpath"
)

// object is a JSON object in either value model.
type object interface {
	Len() int
	Member(i int) (string, any)
	Get(name string) (any, bool)
}

// mapObject adapts map[string]any, members in name order.
type mapObject struct {
	m     map[string]any
	names []string
}

func newMapObject(m map[string]any) *mapObject {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	slices.Sort(names)
	return &mapObject{m: m, names: names}
}

func (o *mapObject) Len() int { return len(o.names) }
func (o *mapObject) Member(i int) (string, any) {
	return o.names[i], o.m[o.names[i]]
}
func (o *mapObject) Get(name string) (any, bool) {
	v, ok := o.m[name]
	return v, ok
}

func asObject(v any) (object, bool) {
	switch o := v.(type) {
	case *jsonpath.Object:
		if o == nil {
			return nil, false
		}
		return o, true
	case map[string]any:
		return newMapObject(o), true
	case *mapObject:
		return o, true
	}
	return nil, false
}

// number is a JSON number: exact as a rational when its exponent is
// reasonable, else a 256-bit float (comparisons) and a float64
// (multipleOf).
type number struct {
	r    *big.Rat   // nil when not exact
	bf   *big.Float // when r is nil
	f    float64
	text string
}

// maxExponent bounds the decimal exponents read exactly: 1e1000000000 as
// a big.Rat would take gigabytes.
const maxExponent = 1000

func asNumber(v any) (number, bool) {
	var s string
	switch n := v.(type) {
	case json.Number:
		s = string(n)
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return number{}, false
		}
		s = strconv.FormatFloat(n, 'g', -1, 64)
	case float32:
		s = strconv.FormatFloat(float64(n), 'g', -1, 32)
	case int:
		s = strconv.Itoa(n)
	case int64:
		s = strconv.FormatInt(n, 10)
	case int32:
		s = strconv.FormatInt(int64(n), 10)
	case uint64:
		s = strconv.FormatUint(n, 10)
	default:
		return number{}, false
	}
	return parseNumber(s)
}

func parseNumber(s string) (number, bool) {
	n := number{text: s}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !isRangeErr(err) {
		return number{}, false
	}
	n.f = f
	if exp := exponentOf(s); exp > -maxExponent && exp < maxExponent {
		if r, ok := new(big.Rat).SetString(s); ok {
			n.r = r
		}
	}
	if n.r == nil {
		bf, _, err := big.ParseFloat(s, 10, 256, big.ToNearestEven)
		if err != nil {
			bf = big.NewFloat(f)
		}
		n.bf = bf
	}
	return n, true
}

func isRangeErr(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// exponentOf is the explicit exponent of a number literal (0 without one),
// clamped.
func exponentOf(s string) int {
	i := strings.IndexAny(s, "eE")
	if i < 0 {
		return 0
	}
	e, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return math.MaxInt32
	}
	return e
}

func (n number) isInt() bool {
	if n.r != nil {
		return n.r.IsInt()
	}
	return n.f == math.Trunc(n.f)
}

func (n number) cmp(m number) int {
	if n.r != nil && m.r != nil {
		return n.r.Cmp(m.r)
	}
	return n.float().Cmp(m.float())
}

func (n number) float() *big.Float {
	if n.bf != nil {
		return n.bf
	}
	return new(big.Float).SetPrec(256).SetRat(n.r)
}

// multipleOf reports whether n is an integer multiple of d (d > 0).
func (n number) multipleOf(d number) bool {
	if n.r != nil && d.r != nil {
		q := new(big.Rat).Quo(n.r, d.r)
		return q.IsInt()
	}
	q := n.f / d.f
	if math.IsInf(q, 0) {
		return false
	}
	return q == math.Trunc(q)
}

func (n number) String() string { return n.text }

// typeOf is the JSON type of v: "null", "boolean", "object", "array",
// "number" or "string"; "" for a value outside the JSON model.
func typeOf(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case *jsonpath.Object:
		if x != nil {
			return "object"
		}
	case map[string]any, *mapObject:
		return "object"
	case json.Number, float32, int, int32, int64, uint64:
		return "number"
	case float64:
		if !math.IsNaN(x) && !math.IsInf(x, 0) {
			return "number"
		}
	}
	return ""
}

// equal is JSON equality: numbers by value (1 = 1.0), objects regardless
// of member order.
func equal(a, b any) bool {
	ta, tb := typeOf(a), typeOf(b)
	if ta != tb {
		return false
	}
	switch ta {
	case "null":
		return true
	case "boolean":
		return a.(bool) == b.(bool)
	case "string":
		return a.(string) == b.(string)
	case "number":
		x, _ := asNumber(a)
		y, _ := asNumber(b)
		return x.cmp(y) == 0
	case "array":
		x, y := a.([]any), b.([]any)
		if len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case "object":
		x, _ := asObject(a)
		y, _ := asObject(b)
		if x.Len() != y.Len() {
			return false
		}
		for i := range x.Len() {
			name, xv := x.Member(i)
			yv, ok := y.Get(name)
			if !ok || !equal(xv, yv) {
				return false
			}
		}
		return true
	}
	return false
}

// canonical writes a key for v such that equal values have equal keys:
// members sorted, numbers normalized.
func canonical(b *strings.Builder, v any) {
	switch typeOf(v) {
	case "null":
		b.WriteString("null")
	case "boolean":
		b.WriteString(strconv.FormatBool(v.(bool)))
	case "string":
		b.WriteString(strconv.Quote(v.(string)))
	case "number":
		n, _ := asNumber(v)
		if n.r != nil {
			b.WriteString(n.r.RatString())
		} else {
			b.WriteString("~" + n.bf.Text('g', 80))
		}
	case "array":
		b.WriteByte('[')
		for i, e := range v.([]any) {
			if i > 0 {
				b.WriteByte(',')
			}
			canonical(b, e)
		}
		b.WriteByte(']')
	case "object":
		o, _ := asObject(v)
		idx := make([]int, o.Len())
		for i := range idx {
			idx[i] = i
		}
		slices.SortFunc(idx, func(i, j int) int {
			a, _ := o.Member(i)
			c, _ := o.Member(j)
			return strings.Compare(a, c)
		})
		b.WriteByte('{')
		for k, i := range idx {
			if k > 0 {
				b.WriteByte(',')
			}
			name, mv := o.Member(i)
			b.WriteString(strconv.Quote(name))
			b.WriteByte(':')
			canonical(b, mv)
		}
		b.WriteByte('}')
	}
}

// maxShown bounds a value quoted in a message.
const maxShown = 60

// show renders a value for a message: compact JSON, cut.
func show(v any) string {
	var s string
	switch x := v.(type) {
	case json.Number:
		s = string(x)
	case string:
		s = strconv.Quote(x)
	default:
		if o, ok := x.(*mapObject); ok {
			v = o.m
		}
		b, err := json.Marshal(v)
		if err != nil {
			s = fmt.Sprint(v)
		} else {
			s = string(b)
		}
	}
	if len(s) > maxShown {
		cut := maxShown
		for cut > 0 && s[cut]&0xC0 == 0x80 {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}
