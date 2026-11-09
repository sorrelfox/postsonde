package xpath

// ResultType is the type of an expression's value (XPath 1.0 §1).
type ResultType uint8

// Result types.
const (
	NodeSet ResultType = iota + 1
	String
	Number
	Boolean
)

// String returns "node-set", "string", "number" or "boolean".
func (t ResultType) String() string {
	switch t {
	case NodeSet:
		return "node-set"
	case String:
		return "string"
	case Number:
		return "number"
	case Boolean:
		return "boolean"
	}
	return "unknown"
}

type expr interface {
	resultType() ResultType
}

type opKind uint8

const (
	opOr opKind = iota
	opAnd
	opEq
	opNeq
	opLt
	opLe
	opGt
	opGe
	opAdd
	opSub
	opMul
	opDiv
	opMod
)

type binaryExpr struct {
	op   opKind
	l, r expr
}

func (b *binaryExpr) resultType() ResultType {
	if b.op >= opAdd {
		return Number
	}
	return Boolean
}

type negExpr struct{ x expr }

func (*negExpr) resultType() ResultType { return Number }

type unionExpr struct{ parts []expr }

func (*unionExpr) resultType() ResultType { return NodeSet }

type literalExpr string

func (literalExpr) resultType() ResultType { return String }

type numberExpr float64

func (numberExpr) resultType() ResultType { return Number }

type callExpr struct {
	fn   *function
	args []expr
}

func (c *callExpr) resultType() ResultType { return c.fn.ret }

// pathExpr is a location path (filter nil) or a filter expression with
// predicates and/or steps.
type pathExpr struct {
	filter   expr
	preds    []expr // on filter, positions in document order
	absolute bool
	steps    []step
}

func (*pathExpr) resultType() ResultType { return NodeSet }

type axis uint8

const (
	axisChild axis = iota
	axisDescendant
	axisParent
	axisAncestor
	axisFollowingSibling
	axisPrecedingSibling
	axisFollowing
	axisPreceding
	axisAttribute
	axisSelf
	axisDescendantOrSelf
	axisAncestorOrSelf
)

var axisNames = map[string]axis{
	"child":              axisChild,
	"descendant":         axisDescendant,
	"parent":             axisParent,
	"ancestor":           axisAncestor,
	"following-sibling":  axisFollowingSibling,
	"preceding-sibling":  axisPrecedingSibling,
	"following":          axisFollowing,
	"preceding":          axisPreceding,
	"attribute":          axisAttribute,
	"self":               axisSelf,
	"descendant-or-self": axisDescendantOrSelf,
	"ancestor-or-self":   axisAncestorOrSelf,
}

// reverse reports whether the axis lists nodes in reverse document order
// (proximity positions count backwards).
func (a axis) reverse() bool {
	switch a {
	case axisParent, axisAncestor, axisAncestorOrSelf, axisPreceding, axisPrecedingSibling:
		return true
	}
	return false
}

type testKind uint8

const (
	testName    testKind = iota // prefix:local or local
	testAny                     // *
	testNSAny                   // prefix:*
	testNode                    // node()
	testText                    // text()
	testComment                 // comment()
	testPI                      // processing-instruction() or ('target')
)

type nodeTest struct {
	kind   testKind
	prefix string
	local  string // testName: local name; testPI: target ("" = any)
	pos    int    // offset of the test in the expression
}

type step struct {
	axis  axis
	test  nodeTest
	preds []expr
}
