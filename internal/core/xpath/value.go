package xpath

import (
	"math"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/xmldom"
)

// A value during evaluation is a nodeSet (in document order, without
// duplicates), a string, a float64 or a bool.
type nodeSet = []*xmldom.Node

// FormatNumber converts a number to a string as XPath's string() does:
// NaN, Infinity, -Infinity, integers without a decimal point, other
// numbers in decimal notation without an exponent.
func FormatNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// ParseNumber converts a string to a number as XPath's number() does:
// optional whitespace, an optional minus sign, digits with an optional
// decimal point, optional whitespace; anything else is NaN.
func ParseNumber(s string) float64 {
	s = strings.Trim(s, " \t\r\n")
	body := strings.TrimPrefix(s, "-")
	digits, dot := 0, false
	for i := 0; i < len(body); i++ {
		switch c := body[i]; {
		case isDigit(c):
			digits++
		case c == '.' && !dot:
			dot = true
		default:
			return math.NaN()
		}
	}
	if digits == 0 {
		return math.NaN()
	}
	f, _ := strconv.ParseFloat(s, 64) // ±Inf when out of range
	return f
}

func (e *evaluator) toString(v any) string {
	switch v := v.(type) {
	case nodeSet:
		if len(v) == 0 {
			return ""
		}
		return e.stringValue(v[0])
	case string:
		return v
	case float64:
		return FormatNumber(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	}
	return ""
}

func (e *evaluator) toNumber(v any) float64 {
	switch v := v.(type) {
	case float64:
		return v
	case bool:
		if v {
			return 1
		}
		return 0
	}
	return ParseNumber(e.toString(v))
}

func toBoolean(v any) bool {
	switch v := v.(type) {
	case nodeSet:
		return len(v) > 0
	case string:
		return v != ""
	case float64:
		return v != 0 && !math.IsNaN(v)
	case bool:
		return v
	}
	return false
}

// stringValue is Node.StringValue with the work charged.
func (e *evaluator) stringValue(n *xmldom.Node) string {
	s := n.StringValue()
	e.charge(len(s) / 64)
	return s
}

// compare applies a comparison operator with the conversions of XPath 1.0
// §3.4.
func (e *evaluator) compare(op opKind, l, r any) bool {
	ln, lok := l.(nodeSet)
	rn, rok := r.(nodeSet)
	switch {
	case lok && rok:
		return e.compareNodeSets(op, ln, rn)
	case lok:
		return e.compareNodeSet(op, ln, r)
	case rok:
		return e.compareNodeSet(flip(op), rn, l)
	}
	if op == opEq || op == opNeq {
		switch {
		case isBool(l) || isBool(r):
			return (toBoolean(l) == toBoolean(r)) == (op == opEq)
		case isNumber(l) || isNumber(r):
			return compareNumbers(op, e.toNumber(l), e.toNumber(r))
		}
		return (e.toString(l) == e.toString(r)) == (op == opEq)
	}
	return compareNumbers(op, e.toNumber(l), e.toNumber(r))
}

func isBool(v any) bool   { _, ok := v.(bool); return ok }
func isNumber(v any) bool { _, ok := v.(float64); return ok }

// flip mirrors an operator for swapped operands.
func flip(op opKind) opKind {
	switch op {
	case opLt:
		return opGt
	case opLe:
		return opGe
	case opGt:
		return opLt
	case opGe:
		return opLe
	}
	return op
}

func compareNumbers(op opKind, a, b float64) bool {
	switch op {
	case opEq:
		return a == b
	case opNeq:
		return a != b
	case opLt:
		return a < b
	case opLe:
		return a <= b
	case opGt:
		return a > b
	case opGe:
		return a >= b
	}
	return false
}

// compareNodeSet: true if some node's string-value (converted as the
// other operand requires) satisfies the comparison.
func (e *evaluator) compareNodeSet(op opKind, ns nodeSet, v any) bool {
	switch v := v.(type) {
	case bool:
		return e.compare(op, toBoolean(ns), v)
	case float64:
		for _, n := range ns {
			if compareNumbers(op, ParseNumber(e.stringValue(n)), v) {
				return true
			}
		}
		return false
	}
	s := v.(string)
	if op == opEq || op == opNeq {
		for _, n := range ns {
			if (e.stringValue(n) == s) == (op == opEq) {
				return true
			}
		}
		return false
	}
	f := ParseNumber(s)
	for _, n := range ns {
		if compareNumbers(op, ParseNumber(e.stringValue(n)), f) {
			return true
		}
	}
	return false
}

// compareNodeSets: true if some pair of nodes satisfies the comparison,
// in linear time.
func (e *evaluator) compareNodeSets(op opKind, l, r nodeSet) bool {
	if len(l) == 0 || len(r) == 0 {
		return false
	}
	if op == opEq || op == opNeq {
		left := make(map[string]struct{}, len(l))
		for _, n := range l {
			left[e.stringValue(n)] = struct{}{}
		}
		if op == opEq {
			for _, n := range r {
				e.charge(0)
				if _, ok := left[e.stringValue(n)]; ok {
					return true
				}
			}
			return false
		}
		// Some pair differs unless both sides hold one and the same string.
		if len(left) > 1 {
			return true
		}
		for _, n := range r {
			if _, ok := left[e.stringValue(n)]; !ok {
				return true
			}
		}
		return false
	}
	minL, maxL, okL := e.numberRange(l)
	minR, maxR, okR := e.numberRange(r)
	if !okL || !okR {
		return false
	}
	switch op {
	case opLt:
		return minL < maxR
	case opLe:
		return minL <= maxR
	case opGt:
		return maxL > minR
	default: // opGe
		return maxL >= minR
	}
}

// numberRange returns the smallest and largest non-NaN numbers of the
// nodes' string-values.
func (e *evaluator) numberRange(ns nodeSet) (lo, hi float64, ok bool) {
	for _, n := range ns {
		f := ParseNumber(e.stringValue(n))
		if math.IsNaN(f) {
			continue
		}
		if !ok {
			lo, hi, ok = f, f, true
			continue
		}
		lo, hi = min(lo, f), max(hi, f)
	}
	return lo, hi, ok
}

// round is XPath's round(): the closest integer, halves toward positive
// infinity, -0 for [-0.5, 0).
func round(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) || f == 0 {
		return f
	}
	r := math.Floor(f)
	if f-r >= 0.5 {
		r++
	}
	if r == 0 && f < 0 {
		return math.Copysign(0, -1)
	}
	return r
}
