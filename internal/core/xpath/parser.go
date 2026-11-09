package xpath

import (
	"strconv"
	"strings"
)

// MaxNesting is the deepest nesting of parentheses, predicates and
// function calls Parse accepts.
const MaxNesting = 64

// MaxLength is the longest expression Parse accepts, in bytes.
const MaxLength = 64 << 10

// Parse parses an XPath expression. The error is a *SyntaxError.
func Parse(src string) (*Expr, error) {
	if len(src) > MaxLength {
		return nil, syntaxErr(src, MaxLength, "the expression is longer than "+strconv.Itoa(MaxLength)+" bytes")
	}
	toks, serr := tokenize(src)
	if serr != nil {
		return nil, serr
	}
	p := &parser{src: src, toks: toks}
	root, err := p.parse()
	if err != nil {
		return nil, err
	}
	return &Expr{src: src, root: root}, nil
}

// MustParse is Parse that panics on an invalid expression; for tests and
// package-level constants.
func MustParse(src string) *Expr {
	e, err := Parse(src)
	if err != nil {
		panic(err)
	}
	return e
}

type parser struct {
	src   string
	toks  []token
	i     int
	depth int
	err   *SyntaxError
}

// bail is panicked with to unwind the recursive descent on the first error.
type bail struct{}

func (p *parser) parse() (x expr, err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(bail); !ok {
				panic(r)
			}
			x, err = nil, p.err
		}
	}()
	if p.peek().kind == tEOF {
		p.fail(0, "the expression is empty")
	}
	x = p.parseOr()
	if t := p.peek(); t.kind != tEOF {
		p.unexpected(t, "an operator or the end of the expression")
	}
	return x, nil
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}

func (p *parser) fail(pos int, msg string) {
	p.err = syntaxErr(p.src, pos, msg)
	panic(bail{})
}

func (p *parser) unexpected(t token, want string) {
	p.fail(t.pos, "unexpected "+t.describe(p.src)+": expected "+want)
}

func (p *parser) expect(k tokKind, want string) token {
	t := p.peek()
	if t.kind != k {
		p.unexpected(t, want)
	}
	return p.next()
}

func (p *parser) nest(pos int) {
	p.depth++
	if p.depth > MaxNesting {
		p.fail(pos, "the expression is nested too deeply (over "+strconv.Itoa(MaxNesting)+" levels)")
	}
}

func (p *parser) isOpName(name string) bool {
	t := p.peek()
	return t.kind == tOpName && t.text == name
}

func (p *parser) parseOr() expr {
	l := p.parseAnd()
	for p.isOpName("or") {
		p.next()
		l = &binaryExpr{op: opOr, l: l, r: p.parseAnd()}
	}
	return l
}

func (p *parser) parseAnd() expr {
	l := p.parseEquality()
	for p.isOpName("and") {
		p.next()
		l = &binaryExpr{op: opAnd, l: l, r: p.parseEquality()}
	}
	return l
}

func (p *parser) parseEquality() expr {
	l := p.parseRelational()
	for {
		var op opKind
		switch p.peek().kind {
		case tEq:
			op = opEq
		case tNeq:
			op = opNeq
		default:
			return l
		}
		p.next()
		l = &binaryExpr{op: op, l: l, r: p.parseRelational()}
	}
}

func (p *parser) parseRelational() expr {
	l := p.parseAdditive()
	for {
		var op opKind
		switch p.peek().kind {
		case tLt:
			op = opLt
		case tLe:
			op = opLe
		case tGt:
			op = opGt
		case tGe:
			op = opGe
		default:
			return l
		}
		p.next()
		l = &binaryExpr{op: op, l: l, r: p.parseAdditive()}
	}
}

func (p *parser) parseAdditive() expr {
	l := p.parseMultiplicative()
	for {
		var op opKind
		switch p.peek().kind {
		case tPlus:
			op = opAdd
		case tMinus:
			op = opSub
		default:
			return l
		}
		p.next()
		l = &binaryExpr{op: op, l: l, r: p.parseMultiplicative()}
	}
}

func (p *parser) parseMultiplicative() expr {
	l := p.parseUnary()
	for {
		var op opKind
		switch t := p.peek(); {
		case t.kind == tMul:
			op = opMul
		case t.kind == tOpName && t.text == "div":
			op = opDiv
		case t.kind == tOpName && t.text == "mod":
			op = opMod
		default:
			return l
		}
		p.next()
		l = &binaryExpr{op: op, l: l, r: p.parseUnary()}
	}
}

func (p *parser) parseUnary() expr {
	n := 0
	for p.peek().kind == tMinus {
		p.next()
		n++
	}
	x := p.parseUnion()
	switch {
	case n == 0:
		return x
	case n%2 == 1:
		return &negExpr{x}
	}
	return &negExpr{&negExpr{x}} // an even count: number(x)
}

func (p *parser) parseUnion() expr {
	start := p.peek()
	x := p.parsePath()
	if p.peek().kind != tPipe {
		return x
	}
	p.needNodeSet(x, start.pos, "'|' combines node-sets only")
	u := &unionExpr{parts: []expr{x}}
	for p.peek().kind == tPipe {
		p.next()
		t := p.peek()
		y := p.parsePath()
		p.needNodeSet(y, t.pos, "'|' combines node-sets only")
		u.parts = append(u.parts, y)
	}
	return u
}

func (p *parser) needNodeSet(x expr, pos int, msg string) {
	if x.resultType() != NodeSet {
		p.fail(pos, msg+"; this is a "+x.resultType().String())
	}
}

func startsStep(k tokKind) bool {
	switch k {
	case tNameTest, tNodeType, tAxisName, tAt, tDot, tDDot:
		return true
	}
	return false
}

func (p *parser) parsePath() expr {
	t := p.peek()
	switch {
	case t.kind == tSlash:
		p.next()
		path := &pathExpr{absolute: true}
		if startsStep(p.peek().kind) {
			path.steps = p.parseSteps(nil)
		}
		return path
	case t.kind == tDSlash:
		p.next()
		return &pathExpr{absolute: true, steps: p.parseSteps(descendantOrSelf())}
	case startsStep(t.kind):
		return &pathExpr{steps: p.parseSteps(nil)}
	}

	primary := p.parsePrimary()
	path := &pathExpr{filter: primary}
	if p.peek().kind == tLBrack {
		p.needNodeSet(primary, t.pos, "a predicate filters node-sets only")
		path.preds = p.parsePredicates()
	}
	switch p.peek().kind {
	case tSlash:
		p.needNodeSet(primary, t.pos, "a path step applies to node-sets only")
		p.next()
		path.steps = p.parseSteps(nil)
	case tDSlash:
		p.needNodeSet(primary, t.pos, "a path step applies to node-sets only")
		p.next()
		path.steps = p.parseSteps(descendantOrSelf())
	}
	if path.preds == nil && path.steps == nil {
		return primary
	}
	return path
}

func descendantOrSelf() []step {
	return []step{{axis: axisDescendantOrSelf, test: nodeTest{kind: testNode}}}
}

// parseSteps parses a relative location path after the steps given.
func (p *parser) parseSteps(steps []step) []step {
	for {
		steps = append(steps, p.parseStep())
		switch p.peek().kind {
		case tSlash:
			p.next()
		case tDSlash:
			p.next()
			steps = append(steps, descendantOrSelf()...)
		default:
			return optimize(steps)
		}
	}
}

// optimize rewrites descendant-or-self::node()/child::T (the expansion of
// "//T") into descendant::T when the child step has no predicates: the
// same nodes, without visiting every node's children.
func optimize(steps []step) []step {
	out := steps[:0]
	for i := 0; i < len(steps); i++ {
		s := steps[i]
		if s.axis == axisDescendantOrSelf && s.test.kind == testNode && len(s.preds) == 0 && i+1 < len(steps) {
			if n := steps[i+1]; n.axis == axisChild && len(n.preds) == 0 {
				n.axis = axisDescendant
				out = append(out, n)
				i++
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

func (p *parser) parseStep() step {
	t := p.peek()
	switch t.kind {
	case tDot, tDDot:
		p.next()
		if p.peek().kind == tLBrack {
			p.fail(p.peek().pos, "a predicate cannot follow '"+p.src[t.pos:t.end]+"'; use self::node()[...] or parent::node()[...]")
		}
		if t.kind == tDot {
			return step{axis: axisSelf, test: nodeTest{kind: testNode, pos: t.pos}}
		}
		return step{axis: axisParent, test: nodeTest{kind: testNode, pos: t.pos}}
	}

	s := step{axis: axisChild}
	switch t.kind {
	case tAt:
		p.next()
		s.axis = axisAttribute
	case tAxisName:
		p.next()
		a, ok := axisNames[t.text]
		if !ok {
			if t.text == "namespace" {
				p.fail(t.pos, "the namespace axis is not supported")
			}
			p.fail(t.pos, "unknown axis "+strconv.Quote(t.text))
		}
		s.axis = a
		p.expect(tDColon, "'::'")
	}
	s.test = p.parseNodeTest()
	s.preds = p.parsePredicates()
	return s
}

func (p *parser) parseNodeTest() nodeTest {
	t := p.peek()
	switch t.kind {
	case tNameTest:
		p.next()
		switch {
		case t.text == "*" && t.prefix == "":
			return nodeTest{kind: testAny, pos: t.pos}
		case t.text == "*":
			return nodeTest{kind: testNSAny, prefix: t.prefix, pos: t.pos}
		}
		return nodeTest{kind: testName, prefix: t.prefix, local: t.text, pos: t.pos}
	case tNodeType:
		p.next()
		p.expect(tLParen, "'('")
		nt := nodeTest{pos: t.pos}
		switch t.text {
		case "node":
			nt.kind = testNode
		case "text":
			nt.kind = testText
		case "comment":
			nt.kind = testComment
		case "processing-instruction":
			nt.kind = testPI
			if lit := p.peek(); lit.kind == tLiteral {
				p.next()
				nt.local = strings.TrimSpace(lit.text)
			}
		}
		p.expect(tRParen, "')'")
		return nt
	}
	p.unexpected(t, "a node test (a name, '*', text(), node(), comment() or processing-instruction())")
	return nodeTest{}
}

func (p *parser) parsePredicates() []expr {
	var preds []expr
	for p.peek().kind == tLBrack {
		open := p.next()
		p.nest(open.pos)
		preds = append(preds, p.parseOr())
		p.expect(tRBrack, "']'")
		p.depth--
	}
	return preds
}

func (p *parser) parsePrimary() expr {
	t := p.peek()
	switch t.kind {
	case tLParen:
		p.next()
		p.nest(t.pos)
		x := p.parseOr()
		p.expect(tRParen, "')'")
		p.depth--
		return x
	case tLiteral:
		p.next()
		return literalExpr(t.text)
	case tNumber:
		p.next()
		return numberExpr(t.num)
	case tVariable:
		p.fail(t.pos, "variables are not supported")
	case tFuncName:
		return p.parseCall()
	}
	p.unexpected(t, "a location path, a literal, a number, a function call or '('")
	return nil
}

func (p *parser) parseCall() expr {
	t := p.next()
	name := t.text
	if t.prefix != "" {
		name = t.prefix + ":" + t.text
	}
	fn, ok := functions[name]
	if !ok {
		if name == "id" {
			p.fail(t.pos, "id() is not supported (it needs DTD attribute types); use //*[@id='...']")
		}
		p.fail(t.pos, "unknown function "+name+"()")
	}
	open := p.expect(tLParen, "'('")
	p.nest(open.pos)
	call := &callExpr{fn: fn}
	var argPos []int
	if p.peek().kind != tRParen {
		for {
			argPos = append(argPos, p.peek().pos)
			call.args = append(call.args, p.parseOr())
			if p.peek().kind != tComma {
				break
			}
			p.next()
		}
	}
	p.expect(tRParen, "',' or ')'")
	p.depth--

	if n := len(call.args); n < fn.minArgs || (fn.maxArgs >= 0 && n > fn.maxArgs) {
		p.fail(t.pos, name+"() "+fn.arity())
	}
	for i, a := range call.args {
		if i < len(fn.nodeSetArgs) && fn.nodeSetArgs[i] && a.resultType() != NodeSet {
			p.fail(argPos[i], name+"() needs a node-set here; this is a "+a.resultType().String())
		}
	}
	return call
}
