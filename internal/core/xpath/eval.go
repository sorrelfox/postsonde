package xpath

import (
	"context"
	"math"
	"slices"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/xmldom"
)

type evaluator struct {
	ctx      context.Context
	maxSteps int
	steps    int
	stop     error
	ns       map[string]string
	strict   bool
}

// evalCtx is the XPath context: node, proximity position and size.
type evalCtx struct {
	node      *xmldom.Node
	pos, size int
}

// stopEval is panicked with to unwind when a limit is hit.
type stopEval struct{}

func (e *evaluator) run(x expr, node *xmldom.Node) (v any, err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(stopEval); !ok {
				panic(r)
			}
			v, err = nil, e.stop
		}
	}()
	return e.eval(x, evalCtx{node: node, pos: 1, size: 1}), nil
}

// charge counts n work units and stops the evaluation over the limit or
// when the context is done.
func (e *evaluator) charge(n int) {
	before := e.steps
	e.steps += n + 1
	if e.maxSteps > 0 && e.steps > e.maxSteps {
		e.stop = errs.New(errs.Unsupported, "The XPath expression needs too much work on this document.").
			WithCode(CodeTooComplex).
			WithHint("Use fewer '//' steps and nested predicates, or a more specific path.").
			WithDetail("maxSteps", strconv.Itoa(e.maxSteps))
		panic(stopEval{})
	}
	if e.ctx != nil && before>>12 != e.steps>>12 {
		if err := e.ctx.Err(); err != nil {
			e.stop = errs.Wrap(err, errs.KindOf(err), "The XPath evaluation was stopped.")
			panic(stopEval{})
		}
	}
}

func (e *evaluator) eval(x expr, c evalCtx) any {
	e.charge(0)
	switch x := x.(type) {
	case *binaryExpr:
		switch x.op {
		case opOr:
			return toBoolean(e.eval(x.l, c)) || toBoolean(e.eval(x.r, c))
		case opAnd:
			return toBoolean(e.eval(x.l, c)) && toBoolean(e.eval(x.r, c))
		case opEq, opNeq, opLt, opLe, opGt, opGe:
			return e.compare(x.op, e.eval(x.l, c), e.eval(x.r, c))
		}
		a, b := e.toNumber(e.eval(x.l, c)), e.toNumber(e.eval(x.r, c))
		switch x.op {
		case opAdd:
			return a + b
		case opSub:
			return a - b
		case opMul:
			return a * b
		case opDiv:
			return a / b
		}
		return math.Mod(a, b) // truncating, with the dividend's sign, as XPath's mod
	case *negExpr:
		return -e.toNumber(e.eval(x.x, c))
	case *unionExpr:
		var out nodeSet
		for _, p := range x.parts {
			out = append(out, e.eval(p, c).(nodeSet)...)
		}
		return e.normalize(out)
	case literalExpr:
		return string(x)
	case numberExpr:
		return float64(x)
	case *callExpr:
		return x.fn.call(e, c, x.args)
	case *pathExpr:
		return e.path(x, c)
	}
	panic("xpath: unknown expression")
}

func (e *evaluator) path(p *pathExpr, c evalCtx) nodeSet {
	var cur nodeSet
	switch {
	case p.filter != nil:
		cur = e.eval(p.filter, c).(nodeSet)
		if len(p.preds) > 0 {
			cur = slices.Clone(cur)
			for _, pred := range p.preds {
				cur = e.filter(cur, pred)
			}
		}
	case p.absolute:
		cur = nodeSet{c.node.Root()}
	default:
		cur = nodeSet{c.node}
	}
	for i := range p.steps {
		if len(cur) == 0 {
			break
		}
		cur = e.step(&p.steps[i], cur)
	}
	return cur
}

// step applies a location step to every node of in and returns the union
// in document order.
func (e *evaluator) step(s *step, in nodeSet) nodeSet {
	var out, buf nodeSet
	flushAt := 1024
	for _, n := range in {
		buf = e.axis(s, n, buf[:0])
		for _, pred := range s.preds {
			buf = e.filter(buf, pred)
		}
		if s.axis.reverse() {
			for i := len(buf) - 1; i >= 0; i-- {
				out = append(out, buf[i])
			}
		} else {
			out = append(out, buf...)
		}
		if len(out) > flushAt {
			// Keep the union small when context nodes select the same
			// nodes over and over (//x//y).
			out = e.normalize(out)
			flushAt = 2*len(out) + 1024
		}
	}
	return e.normalize(out)
}

// filter keeps the nodes for which pred is true; positions are in the
// order of ns (axis order).
func (e *evaluator) filter(ns nodeSet, pred expr) nodeSet {
	size := len(ns)
	kept := 0
	for i, n := range ns {
		v := e.eval(pred, evalCtx{node: n, pos: i + 1, size: size})
		var keep bool
		if f, ok := v.(float64); ok {
			keep = f == float64(i+1)
		} else {
			keep = toBoolean(v)
		}
		if keep {
			ns[kept] = n
			kept++
		}
	}
	return ns[:kept]
}

// normalize sorts ns into document order and removes duplicates.
func (e *evaluator) normalize(ns nodeSet) nodeSet {
	for i := 1; i < len(ns); i++ {
		if ns[i-1].Index >= ns[i].Index {
			e.charge(len(ns))
			slices.SortFunc(ns, func(a, b *xmldom.Node) int { return a.Index - b.Index })
			return slices.CompactFunc(ns, func(a, b *xmldom.Node) bool { return a == b })
		}
	}
	return ns
}

// axis appends the nodes of s's axis from n that pass its node test, in
// axis order.
func (e *evaluator) axis(s *step, n *xmldom.Node, out nodeSet) nodeSet {
	t := &s.test
	principal := xmldom.ElementNode
	switch s.axis {
	case axisChild:
		out = e.children(t, n, principal, out)
	case axisDescendant:
		out = e.descendants(t, n, out)
	case axisDescendantOrSelf:
		out = e.test(t, n, principal, out)
		out = e.descendants(t, n, out)
	case axisSelf:
		out = e.test(t, n, principal, out)
	case axisParent:
		if n.Parent != nil {
			out = e.test(t, n.Parent, principal, out)
		}
	case axisAncestor:
		for a := n.Parent; a != nil; a = a.Parent {
			out = e.test(t, a, principal, out)
		}
	case axisAncestorOrSelf:
		for a := n; a != nil; a = a.Parent {
			out = e.test(t, a, principal, out)
		}
	case axisAttribute:
		for _, a := range n.Attrs {
			out = e.test(t, a, xmldom.AttributeNode, out)
		}
	case axisFollowingSibling:
		if n.Parent != nil && n.Type != xmldom.AttributeNode {
			for _, c := range n.Parent.Children[n.SiblingIndex()+1:] {
				out = e.test(t, c, principal, out)
			}
		}
	case axisPrecedingSibling:
		if n.Parent != nil && n.Type != xmldom.AttributeNode {
			sib := n.Parent.Children
			for i := n.SiblingIndex() - 1; i >= 0; i-- {
				out = e.test(t, sib[i], principal, out)
			}
		}
	case axisFollowing:
		if n.Type == xmldom.AttributeNode {
			// An attribute is followed by its element's content.
			n = n.Parent
			out = e.descendants(t, n, out)
		}
		for a := n; a.Parent != nil; a = a.Parent {
			for _, c := range a.Parent.Children[a.SiblingIndex()+1:] {
				out = e.test(t, c, principal, out)
				out = e.descendants(t, c, out)
			}
		}
	case axisPreceding:
		if n.Type == xmldom.AttributeNode {
			n = n.Parent
		}
		for a := n; a.Parent != nil; a = a.Parent {
			sib := a.Parent.Children
			for i := a.SiblingIndex() - 1; i >= 0; i-- {
				out = e.descendantsReverse(t, sib[i], out)
				out = e.test(t, sib[i], principal, out)
			}
		}
	}
	return out
}

func (e *evaluator) children(t *nodeTest, n *xmldom.Node, principal xmldom.NodeType, out nodeSet) nodeSet {
	for _, c := range n.Children {
		out = e.test(t, c, principal, out)
	}
	return out
}

func (e *evaluator) descendants(t *nodeTest, n *xmldom.Node, out nodeSet) nodeSet {
	for _, c := range n.Children {
		out = e.test(t, c, xmldom.ElementNode, out)
		if len(c.Children) > 0 {
			out = e.descendants(t, c, out)
		}
	}
	return out
}

func (e *evaluator) descendantsReverse(t *nodeTest, n *xmldom.Node, out nodeSet) nodeSet {
	for i := len(n.Children) - 1; i >= 0; i-- {
		c := n.Children[i]
		if len(c.Children) > 0 {
			out = e.descendantsReverse(t, c, out)
		}
		out = e.test(t, c, xmldom.ElementNode, out)
	}
	return out
}

// test appends n if it passes the node test; principal is the axis's
// principal node type.
func (e *evaluator) test(t *nodeTest, n *xmldom.Node, principal xmldom.NodeType, out nodeSet) nodeSet {
	e.charge(0)
	if e.matches(t, n, principal) {
		out = append(out, n)
	}
	return out
}

func (e *evaluator) matches(t *nodeTest, n *xmldom.Node, principal xmldom.NodeType) bool {
	switch t.kind {
	case testNode:
		return n.Type != xmldom.DoctypeNode
	case testText:
		return n.Type == xmldom.TextNode
	case testComment:
		return n.Type == xmldom.CommentNode
	case testPI:
		return n.Type == xmldom.ProcInstNode && (t.local == "" || n.Name.Local == t.local)
	case testAny:
		return n.Type == principal
	case testNSAny:
		return n.Type == principal && e.namespaceMatches(t, n)
	}
	return n.Type == principal && n.Name.Local == t.local && e.namespaceMatches(t, n)
}

// namespaceMatches applies the name matching rules: see Options.
func (e *evaluator) namespaceMatches(t *nodeTest, n *xmldom.Node) bool {
	if t.prefix == "" {
		if n.Type == xmldom.AttributeNode {
			return n.Name.Space == "" && n.Name.Prefix == ""
		}
		if uri, ok := e.ns[""]; ok {
			return n.Name.Space == uri
		}
		return !e.strict || n.Name.Space == ""
	}
	if uri, ok := e.lookup(t.prefix); ok {
		return n.Name.Space == uri
	}
	return !e.strict && n.Name.Prefix == t.prefix
}

func (e *evaluator) lookup(prefix string) (string, bool) {
	if uri, ok := e.ns[prefix]; ok {
		return uri, true
	}
	if prefix == "xml" {
		return xmldom.XMLNamespace, true
	}
	return "", false
}
