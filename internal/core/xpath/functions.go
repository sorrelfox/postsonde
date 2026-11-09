package xpath

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/xmldom"
)

type function struct {
	ret              ResultType
	minArgs, maxArgs int    // maxArgs -1: any number
	nodeSetArgs      []bool // arguments that must be node-sets
	call             func(e *evaluator, c evalCtx, args []expr) any
}

func (f *function) arity() string {
	switch {
	case f.maxArgs < 0:
		return "takes at least " + plural(f.minArgs)
	case f.minArgs == f.maxArgs:
		return "takes " + plural(f.minArgs)
	}
	return "takes " + strconv.Itoa(f.minArgs) + " to " + plural(f.maxArgs)
}

func plural(n int) string {
	if n == 1 {
		return "1 argument"
	}
	return strconv.Itoa(n) + " arguments"
}

// functions is the XPath 1.0 core function library (§4) without id().
var functions map[string]*function

func init() {
	nodeArg := []bool{true}
	functions = map[string]*function{
		// Node-set functions.
		"last":     {ret: Number, call: func(_ *evaluator, c evalCtx, _ []expr) any { return float64(c.size) }},
		"position": {ret: Number, call: func(_ *evaluator, c evalCtx, _ []expr) any { return float64(c.pos) }},
		"count": {ret: Number, minArgs: 1, maxArgs: 1, nodeSetArgs: nodeArg, call: func(e *evaluator, c evalCtx, a []expr) any {
			return float64(len(e.eval(a[0], c).(nodeSet)))
		}},
		"local-name": {ret: String, maxArgs: 1, nodeSetArgs: nodeArg, call: func(e *evaluator, c evalCtx, a []expr) any {
			if n := e.nodeArg(c, a); n != nil && n.Type != xmldom.TextNode && n.Type != xmldom.CommentNode {
				return n.Name.Local
			}
			return ""
		}},
		"namespace-uri": {ret: String, maxArgs: 1, nodeSetArgs: nodeArg, call: func(e *evaluator, c evalCtx, a []expr) any {
			if n := e.nodeArg(c, a); n != nil {
				return n.Name.Space
			}
			return ""
		}},
		"name": {ret: String, maxArgs: 1, nodeSetArgs: nodeArg, call: func(e *evaluator, c evalCtx, a []expr) any {
			if n := e.nodeArg(c, a); n != nil {
				return n.Name.Qualified()
			}
			return ""
		}},

		// String functions.
		"string": {ret: String, maxArgs: 1, call: func(e *evaluator, c evalCtx, a []expr) any {
			return e.stringArg(c, a, 0)
		}},
		"concat": {ret: String, minArgs: 2, maxArgs: -1, call: func(e *evaluator, c evalCtx, a []expr) any {
			var b strings.Builder
			for i := range a {
				b.WriteString(e.stringArg(c, a, i))
				e.charge(b.Len() / 64)
			}
			return b.String()
		}},
		"starts-with": {ret: Boolean, minArgs: 2, maxArgs: 2, call: func(e *evaluator, c evalCtx, a []expr) any {
			return strings.HasPrefix(e.stringArg(c, a, 0), e.stringArg(c, a, 1))
		}},
		"contains": {ret: Boolean, minArgs: 2, maxArgs: 2, call: func(e *evaluator, c evalCtx, a []expr) any {
			s, sub := e.stringArg(c, a, 0), e.stringArg(c, a, 1)
			e.charge(len(s) / 16)
			return strings.Contains(s, sub)
		}},
		"substring-before": {ret: String, minArgs: 2, maxArgs: 2, call: func(e *evaluator, c evalCtx, a []expr) any {
			before, _, found := strings.Cut(e.stringArg(c, a, 0), e.stringArg(c, a, 1))
			if !found {
				return ""
			}
			return before
		}},
		"substring-after": {ret: String, minArgs: 2, maxArgs: 2, call: func(e *evaluator, c evalCtx, a []expr) any {
			_, after, _ := strings.Cut(e.stringArg(c, a, 0), e.stringArg(c, a, 1))
			return after
		}},
		"substring": {ret: String, minArgs: 2, maxArgs: 3, call: func(e *evaluator, c evalCtx, a []expr) any {
			s := e.stringArg(c, a, 0)
			start := round(e.toNumber(e.eval(a[1], c)))
			end := math.Inf(1)
			if len(a) == 3 {
				end = start + round(e.toNumber(e.eval(a[2], c)))
			}
			// Characters at positions p (from 1) with start <= p < end.
			var b strings.Builder
			p := 0
			for _, r := range s {
				p++
				if f := float64(p); f >= start && f < end {
					b.WriteRune(r)
				}
			}
			return b.String()
		}},
		"string-length": {ret: Number, maxArgs: 1, call: func(e *evaluator, c evalCtx, a []expr) any {
			return float64(utf8.RuneCountInString(e.stringArg(c, a, 0)))
		}},
		"normalize-space": {ret: String, maxArgs: 1, call: func(e *evaluator, c evalCtx, a []expr) any {
			return strings.Join(strings.FieldsFunc(e.stringArg(c, a, 0), func(r rune) bool {
				return r == ' ' || r == '\t' || r == '\r' || r == '\n'
			}), " ")
		}},
		"translate": {ret: String, minArgs: 3, maxArgs: 3, call: func(e *evaluator, c evalCtx, a []expr) any {
			s, from, to := e.stringArg(c, a, 0), []rune(e.stringArg(c, a, 1)), []rune(e.stringArg(c, a, 2))
			m := make(map[rune]rune, len(from))
			for i, r := range from {
				if _, ok := m[r]; ok {
					continue // the first occurrence wins
				}
				if i < len(to) {
					m[r] = to[i]
				} else {
					m[r] = -1
				}
			}
			return strings.Map(func(r rune) rune {
				if t, ok := m[r]; ok {
					return t
				}
				return r
			}, s)
		}},

		// Boolean functions.
		"boolean": {ret: Boolean, minArgs: 1, maxArgs: 1, call: func(e *evaluator, c evalCtx, a []expr) any {
			return toBoolean(e.eval(a[0], c))
		}},
		"not": {ret: Boolean, minArgs: 1, maxArgs: 1, call: func(e *evaluator, c evalCtx, a []expr) any {
			return !toBoolean(e.eval(a[0], c))
		}},
		"true":  {ret: Boolean, call: func(*evaluator, evalCtx, []expr) any { return true }},
		"false": {ret: Boolean, call: func(*evaluator, evalCtx, []expr) any { return false }},
		"lang": {ret: Boolean, minArgs: 1, maxArgs: 1, call: func(e *evaluator, c evalCtx, a []expr) any {
			want := e.stringArg(c, a, 0)
			for n := c.node; n != nil; n = n.Parent {
				for _, at := range n.Attrs {
					if at.Name.Space == xmldom.XMLNamespace && at.Name.Local == "lang" {
						lang := at.Value
						return strings.EqualFold(lang, want) ||
							(len(lang) > len(want) && lang[len(want)] == '-' && strings.EqualFold(lang[:len(want)], want))
					}
				}
			}
			return false
		}},

		// Number functions.
		"number": {ret: Number, maxArgs: 1, call: func(e *evaluator, c evalCtx, a []expr) any {
			if len(a) == 0 {
				return ParseNumber(e.stringValue(c.node))
			}
			return e.toNumber(e.eval(a[0], c))
		}},
		"sum": {ret: Number, minArgs: 1, maxArgs: 1, nodeSetArgs: nodeArg, call: func(e *evaluator, c evalCtx, a []expr) any {
			total := 0.0
			for _, n := range e.eval(a[0], c).(nodeSet) {
				total += ParseNumber(e.stringValue(n))
			}
			return total
		}},
		"floor": {ret: Number, minArgs: 1, maxArgs: 1, call: func(e *evaluator, c evalCtx, a []expr) any {
			return math.Floor(e.toNumber(e.eval(a[0], c)))
		}},
		"ceiling": {ret: Number, minArgs: 1, maxArgs: 1, call: func(e *evaluator, c evalCtx, a []expr) any {
			return math.Ceil(e.toNumber(e.eval(a[0], c)))
		}},
		"round": {ret: Number, minArgs: 1, maxArgs: 1, call: func(e *evaluator, c evalCtx, a []expr) any {
			return round(e.toNumber(e.eval(a[0], c)))
		}},
	}
}

// nodeArg returns the first node of the optional node-set argument, or
// the context node when it is omitted; nil for an empty node-set.
func (e *evaluator) nodeArg(c evalCtx, a []expr) *xmldom.Node {
	if len(a) == 0 {
		return c.node
	}
	ns := e.eval(a[0], c).(nodeSet)
	if len(ns) == 0 {
		return nil
	}
	return ns[0]
}

// stringArg converts argument i to a string; a missing argument is the
// context node.
func (e *evaluator) stringArg(c evalCtx, a []expr, i int) string {
	if i >= len(a) {
		return e.stringValue(c.node)
	}
	return e.toString(e.eval(a[i], c))
}
