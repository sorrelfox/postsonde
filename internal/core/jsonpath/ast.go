package jsonpath

import "regexp"

// query is a JSONPath query: absolute ($) or relative (@) followed by
// segments.
type query struct {
	relative bool
	segments []segment
}

// singular reports whether q is a singular query (RFC 9535 §2.3.5.1):
// only child segments with one name or index selector each.
func (q *query) singular() bool {
	for _, s := range q.segments {
		if s.descendant || len(s.selectors) != 1 {
			return false
		}
		switch s.selectors[0].(type) {
		case nameSel, indexSel:
		default:
			return false
		}
	}
	return true
}

type segment struct {
	descendant bool
	selectors  []selector
}

type selector interface{ isSelector() }

type nameSel struct{ name string }

type wildcardSel struct{}

type indexSel struct{ index int64 }

type sliceSel struct {
	start, end, step          int64
	hasStart, hasEnd, hasStep bool
}

type filterSel struct{ expr expr }

func (nameSel) isSelector()     {}
func (wildcardSel) isSelector() {}
func (indexSel) isSelector()    {}
func (sliceSel) isSelector()    {}
func (filterSel) isSelector()   {}

// expr is a filter expression node. Parsing produces "raw" nodes
// (literalExpr, queryExpr, funcExpr) that are converted by their context:
// a query tested for existence becomes existExpr, and so on.
type expr interface{ isExpr() }

// literalExpr is a string, number (json.Number), true, false or null.
type literalExpr struct{ value any }

// queryExpr is a filter query in a value or nodes position.
type queryExpr struct{ q *query }

// funcExpr calls one of the function extensions.
type funcExpr struct {
	fn   *function
	args []expr
	// re is the compiled pattern of match()/search() when the pattern is a
	// string literal; reErr is set when that literal is not an I-Regexp.
	re    *regexp.Regexp
	reErr bool
	reSet bool
}

// Logical nodes.
type (
	existExpr    struct{ q *query }
	funcTestExpr struct{ f *funcExpr } // LogicalType or NodesType result
	notExpr      struct{ e expr }
	andExpr      struct{ es []expr }
	orExpr       struct{ es []expr }
	compareExpr  struct {
		op   string
		l, r expr
	}
)

func (literalExpr) isExpr()  {}
func (queryExpr) isExpr()    {}
func (*funcExpr) isExpr()    {}
func (existExpr) isExpr()    {}
func (funcTestExpr) isExpr() {}
func (notExpr) isExpr()      {}
func (andExpr) isExpr()      {}
func (orExpr) isExpr()       {}
func (compareExpr) isExpr()  {}

// isLogical reports whether e is already a logical expression.
func isLogical(e expr) bool {
	switch e.(type) {
	case existExpr, funcTestExpr, notExpr, andExpr, orExpr, compareExpr:
		return true
	}
	return false
}
