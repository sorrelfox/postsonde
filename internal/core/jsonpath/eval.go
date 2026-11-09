package jsonpath

import (
	"context"
	"regexp"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// maxRegexpCache bounds the patterns taken from the document that one
// evaluation keeps compiled.
const maxRegexpCache = 64

// loc is a node's location as a parent-linked list, turned into a
// NormalizedPath only for result nodes.
type loc struct {
	parent *loc
	name   string
	index  int
	isIdx  bool
	depth  int
}

func (l *loc) child(name string) *loc {
	if l == nil {
		return nil
	}
	return &loc{parent: l, name: name, depth: l.depth + 1}
}

func (l *loc) elem(i int) *loc {
	if l == nil {
		return nil
	}
	return &loc{parent: l, index: i, isIdx: true, depth: l.depth + 1}
}

func (l *loc) path() NormalizedPath {
	if l == nil || l.depth == 0 {
		return NormalizedPath{}
	}
	p := make(NormalizedPath, l.depth)
	for c := l; c.depth > 0; c = c.parent {
		p[c.depth-1] = PathElement{Name: c.name, Index: c.index, IsIndex: c.isIdx}
	}
	return p
}

// node is a value during evaluation; loc is nil inside filter queries,
// where locations are never needed.
type node struct {
	v   any
	loc *loc
}

type evaluator struct {
	root     any
	ctx      context.Context
	maxSteps int
	steps    int
	stop     error
	res      map[string]*regexp.Regexp // "m"/"s" + pattern; nil = invalid
}

// stopEval is panicked with to unwind when a limit is hit.
type stopEval struct{}

func (e *evaluator) run(q *query) (nodes []node, err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(stopEval); !ok {
				panic(r)
			}
			nodes, err = nil, e.stop
		}
	}()
	return e.query(q, node{v: e.root, loc: &loc{}}), nil
}

// charge counts n work units and stops the evaluation over the limit or
// when the context is done.
func (e *evaluator) charge(n int) {
	before := e.steps
	e.steps += n + 1
	if e.maxSteps > 0 && e.steps > e.maxSteps {
		e.stop = errs.New(errs.Unsupported, "The JSONPath query needs too much work on this document.").
			WithCode(CodeTooComplex).
			WithHint("Use fewer descendant segments ('..') or nested filters, or a more specific path.").
			WithDetail("maxSteps", strconv.Itoa(e.maxSteps))
		panic(stopEval{})
	}
	if e.ctx != nil && before>>12 != e.steps>>12 {
		if err := e.ctx.Err(); err != nil {
			e.stop = errs.Wrap(err, errs.KindOf(err), "The JSONPath query was stopped.")
			panic(stopEval{})
		}
	}
}

// query evaluates q with cur as the current node. For an absolute query
// the start is the root; locations are tracked only if start has one.
func (e *evaluator) query(q *query, start node) []node {
	nodes := []node{start}
	for i := range q.segments {
		nodes = e.segment(&q.segments[i], nodes)
		if len(nodes) == 0 {
			break
		}
	}
	return nodes
}

func (e *evaluator) segment(s *segment, in []node) []node {
	var out []node
	for _, n := range in {
		if s.descendant {
			out = e.descend(s.selectors, n, out)
		} else {
			out = e.selectAll(s.selectors, n, out)
		}
	}
	return out
}

// descend applies the selectors to n and then to each of its descendants,
// in document order (a node before its children).
func (e *evaluator) descend(sels []selector, n node, out []node) []node {
	e.charge(0)
	out = e.selectAll(sels, n, out)
	if a, ok := arrayOf(n.v); ok {
		for i, c := range a {
			if isContainer(c) {
				out = e.descend(sels, node{c, n.loc.elem(i)}, out)
			} else {
				e.charge(0)
			}
		}
	} else if isObject(n.v) {
		objectEach(n.v, func(name string, c any) bool {
			if isContainer(c) {
				out = e.descend(sels, node{c, n.loc.child(name)}, out)
			} else {
				e.charge(0)
			}
			return true
		})
	}
	return out
}

// isContainer reports whether v can have children; selectors applied to
// anything else select nothing.
func isContainer(v any) bool {
	if _, ok := v.([]any); ok {
		return true
	}
	return isObject(v)
}

func (e *evaluator) selectAll(sels []selector, n node, out []node) []node {
	for _, s := range sels {
		out = e.selectOne(s, n, out)
	}
	return out
}

func (e *evaluator) selectOne(s selector, n node, out []node) []node {
	switch s := s.(type) {
	case nameSel:
		if v, ok := objectGet(n.v, s.name); ok {
			e.charge(0)
			out = append(out, node{v, n.loc.child(s.name)})
		}
	case indexSel:
		if a, ok := arrayOf(n.v); ok {
			i := s.index
			if i < 0 {
				i += int64(len(a))
			}
			if i >= 0 && i < int64(len(a)) {
				e.charge(0)
				out = append(out, node{a[i], n.loc.elem(int(i))})
			}
		}
	case wildcardSel:
		if a, ok := arrayOf(n.v); ok {
			for i, c := range a {
				e.charge(0)
				out = append(out, node{c, n.loc.elem(i)})
			}
		} else {
			objectEach(n.v, func(name string, c any) bool {
				e.charge(0)
				out = append(out, node{c, n.loc.child(name)})
				return true
			})
		}
	case sliceSel:
		if a, ok := arrayOf(n.v); ok {
			for _, i := range sliceIndexes(s, len(a)) {
				e.charge(0)
				out = append(out, node{a[i], n.loc.elem(i)})
			}
		}
	case filterSel:
		if a, ok := arrayOf(n.v); ok {
			for i, c := range a {
				e.charge(0)
				if e.test(s.expr, c) {
					out = append(out, node{c, n.loc.elem(i)})
				}
			}
		} else {
			objectEach(n.v, func(name string, c any) bool {
				e.charge(0)
				if e.test(s.expr, c) {
					out = append(out, node{c, n.loc.child(name)})
				}
				return true
			})
		}
	}
	return out
}

// sliceIndexes implements the slice algorithm of RFC 9535 §2.3.4.2.2.
func sliceIndexes(s sliceSel, n int) []int {
	length := int64(n)
	step := int64(1)
	if s.hasStep {
		step = s.step
	}
	if step == 0 {
		return nil
	}
	normalize := func(i int64) int64 {
		if i >= 0 {
			return i
		}
		return length + i
	}
	var start, end int64
	if step > 0 {
		start, end = 0, length
	} else {
		start, end = length-1, -length-1
	}
	if s.hasStart {
		start = s.start
	}
	if s.hasEnd {
		end = s.end
	}
	nStart, nEnd := normalize(start), normalize(end)
	var idx []int
	if step > 0 {
		lower := min(max(nStart, 0), length)
		upper := min(max(nEnd, 0), length)
		for i := lower; i < upper; i += step {
			idx = append(idx, int(i))
		}
	} else {
		upper := min(max(nStart, -1), length-1)
		lower := min(max(nEnd, -1), length-1)
		for i := upper; lower < i; i += step {
			idx = append(idx, int(i))
		}
	}
	return idx
}

// ---- filter expressions ----

// test evaluates a logical expression with cur as '@'.
func (e *evaluator) test(x expr, cur any) bool {
	switch x := x.(type) {
	case existExpr:
		return len(e.subquery(x.q, cur)) > 0
	case funcTestExpr:
		if x.f.fn.result == nodesType {
			return len(e.nodes(x.f, cur)) > 0
		}
		return e.callLogical(x.f, cur)
	case notExpr:
		return !e.test(x.e, cur)
	case andExpr:
		for _, s := range x.es {
			if !e.test(s, cur) {
				return false
			}
		}
		return true
	case orExpr:
		for _, s := range x.es {
			if e.test(s, cur) {
				return true
			}
		}
		return false
	case compareExpr:
		l, r := e.value(x.l, cur), e.value(x.r, cur)
		switch x.op {
		case "==":
			return e.equal(l, r)
		case "!=":
			return !e.equal(l, r)
		case "<":
			return less(l, r)
		case "<=":
			return less(l, r) || e.equal(l, r)
		case ">":
			return less(r, l)
		case ">=":
			return less(r, l) || e.equal(l, r)
		}
	}
	return false
}

func (e *evaluator) subquery(q *query, cur any) []node {
	start := node{v: e.root}
	if q.relative {
		start.v = cur
	}
	return e.query(q, start)
}

// value evaluates a ValueType operand; the result may be nothing.
func (e *evaluator) value(x expr, cur any) any {
	switch x := x.(type) {
	case literalExpr:
		return x.value
	case queryExpr:
		ns := e.subquery(x.q, cur)
		if len(ns) == 1 {
			return ns[0].v
		}
		return nothing
	case *funcExpr:
		return e.callValue(x, cur)
	}
	return nothing
}

// nodes evaluates a NodesType operand.
func (e *evaluator) nodes(x expr, cur any) []node {
	if q, ok := x.(queryExpr); ok {
		return e.subquery(q.q, cur)
	}
	return nil
}

// equal implements == (RFC 9535 §2.3.5.2.2): Nothing equals only Nothing,
// numbers compare numerically, arrays and objects deeply.
func (e *evaluator) equal(a, b any) bool {
	e.charge(0)
	an, bn := a == nothing, b == nothing
	if an || bn {
		return an && bn
	}
	ka, kb := kindOf(a), kindOf(b)
	if ka != kb {
		return false
	}
	switch ka {
	case kNull:
		return true
	case kBool:
		return a.(bool) == b.(bool)
	case kString:
		return a.(string) == b.(string)
	case kNumber:
		da, _ := numberOf(a)
		db, _ := numberOf(b)
		return da.cmp(db) == 0
	case kArray:
		aa, ba := a.([]any), b.([]any)
		if len(aa) != len(ba) {
			return false
		}
		for i := range aa {
			if !e.equal(aa[i], ba[i]) {
				return false
			}
		}
		return true
	case kObject:
		if objectLen(a) != objectLen(b) {
			return false
		}
		eq := true
		objectEach(a, func(name string, av any) bool {
			bv, ok := objectGet(b, name)
			eq = ok && e.equal(av, bv)
			return eq
		})
		return eq
	}
	return false
}

// less implements <: numbers numerically, strings by Unicode scalar
// values (byte order of UTF-8), false for anything else.
func less(a, b any) bool {
	if a == nothing || b == nothing {
		return false
	}
	if as, ok := a.(string); ok {
		bs, ok := b.(string)
		return ok && as < bs
	}
	da, ok := numberOf(a)
	if !ok {
		return false
	}
	db, ok := numberOf(b)
	return ok && da.cmp(db) < 0
}

// regexp returns the compiled I-Regexp for a pattern from the document,
// or nil when it is not a valid I-Regexp.
func (e *evaluator) regexp(pat string, full bool) *regexp.Regexp {
	key := "s" + pat
	if full {
		key = "m" + pat
	}
	if re, ok := e.res[key]; ok {
		return re
	}
	// Compiling costs far more per byte than walking nodes (a 4 KiB
	// pattern takes tens of milliseconds).
	e.charge(256 + 128*len(pat))
	re, err := compileIRegexp(pat, full)
	if err != nil {
		re = nil
	}
	if e.res == nil {
		e.res = map[string]*regexp.Regexp{}
	}
	if len(e.res) < maxRegexpCache {
		e.res[key] = re
	}
	return re
}
