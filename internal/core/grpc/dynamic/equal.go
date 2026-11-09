package dynamic

import (
	"bytes"
	"math"
)

// Equal reports whether a and b have the same type, the same set fields
// with equal values and the same unknown field bytes. Empty lists and maps
// equal unset ones. Floating-point values are compared bit for bit, except
// that all NaNs are equal to each other (so a decoded message equals
// itself); 0 and -0 differ.
func Equal(a, b *Message) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.md != b.md || !bytes.Equal(a.unknown, b.unknown) {
		return false
	}
	fa, fb := nonEmpty(a.fields), nonEmpty(b.fields)
	if len(fa) != len(fb) {
		return false
	}
	for i := range fa {
		if fa[i].fd != fb[i].fd || !equalValue(fa[i].v, fb[i].v) {
			return false
		}
	}
	return true
}

func nonEmpty(fs []fieldValue) []fieldValue {
	out := fs[:0:0]
	for _, f := range fs {
		if !isEmptyComposite(f.v) {
			out = append(out, f)
		}
	}
	return out
}

func equalValue(x, y any) bool {
	switch x := x.(type) {
	case float64:
		y, ok := y.(float64)
		return ok && (math.Float64bits(x) == math.Float64bits(y) || math.IsNaN(x) && math.IsNaN(y))
	case float32:
		y, ok := y.(float32)
		return ok && (math.Float32bits(x) == math.Float32bits(y) || x != x && y != y)
	case []byte:
		y, ok := y.([]byte)
		return ok && bytes.Equal(x, y)
	case *Message:
		y, ok := y.(*Message)
		return ok && Equal(x, y)
	case *List:
		y, ok := y.(*List)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for i := range x.items {
			if !equalValue(x.items[i], y.items[i]) {
				return false
			}
		}
		return true
	case *Map:
		y, ok := y.(*Map)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for k, xv := range x.vals {
			yv, ok := y.vals[k]
			if !ok || !equalValue(xv, yv) {
				return false
			}
		}
		return true
	}
	return x == y
}
