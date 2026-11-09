package xpath

import (
	"context"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/xmldom"
)

// Expr is a parsed XPath expression. It is immutable and safe for
// concurrent use.
type Expr struct {
	src  string
	root expr
}

// String returns the expression text as given to Parse.
func (x *Expr) String() string { return x.src }

// ResultType is the type the expression evaluates to, known statically:
// NodeSet for location paths, unions and filtered node-sets; the
// operator's or function's type otherwise.
func (x *Expr) ResultType() ResultType { return x.root.resultType() }

// Limits bound the work of one evaluation.
type Limits struct {
	// MaxSteps bounds the work units of an evaluation: each node an axis
	// visits, each expression evaluated, each 64 bytes of a string-value
	// read (16 for contains()) and each node sorted into document order
	// count as one. 0 means no limit.
	MaxSteps int
}

// DefaultLimits is what QueryXML uses: enough for any reasonable
// expression on a multi-megabyte document, while stopping pathological
// ones (nested '//' steps and predicates) within about a second.
var DefaultLimits = Limits{MaxSteps: 20_000_000}

// Options configure an evaluation.
type Options struct {
	// Namespaces maps prefixes used in the expression to namespace URIs.
	// The entry "" is a default namespace for unprefixed element names
	// (as in XPath 2.0); "xml" is always bound.
	Namespaces map[string]string
	// StrictNames selects XPath 1.0 name matching: an unprefixed element
	// name matches only elements in no namespace (or in Namespaces[""]),
	// and every prefix must be in Namespaces. When false (the default,
	// convenient for API responses), an unprefixed element name matches
	// its local name in any namespace, and a prefix missing from
	// Namespaces matches the prefix as written in the document.
	// Unprefixed attribute names match only unprefixed attributes either
	// way.
	StrictNames bool
	// Limits bound the work.
	Limits Limits
}

// Result is the value of an expression: a node-set in document order
// without duplicates, a string, a number or a boolean.
type Result struct {
	Type ResultType
	// Nodes is the node-set (Type NodeSet).
	Nodes []*xmldom.Node
	// Str, Num and Bool hold a scalar result.
	Str  string
	Num  float64
	Bool bool
}

// String converts the result as XPath's string() does: the string-value
// of the first node for a node-set.
func (r Result) String() string {
	switch r.Type {
	case NodeSet:
		if len(r.Nodes) == 0 {
			return ""
		}
		return r.Nodes[0].StringValue()
	case Number:
		return FormatNumber(r.Num)
	case Boolean:
		return strconv.FormatBool(r.Bool)
	}
	return r.Str
}

// Number converts the result as XPath's number() does.
func (r Result) Number() float64 {
	switch r.Type {
	case Number:
		return r.Num
	case Boolean:
		if r.Bool {
			return 1
		}
		return 0
	}
	return ParseNumber(r.String())
}

// Boolean converts the result as XPath's boolean() does.
func (r Result) Boolean() bool {
	switch r.Type {
	case NodeSet:
		return len(r.Nodes) > 0
	case String:
		return r.Str != ""
	case Number:
		return toBoolean(r.Num)
	}
	return r.Bool
}

// Values returns the string-values of the nodes, or the scalar converted
// to a string as the only value.
func (r Result) Values() []string {
	if r.Type != NodeSet {
		return []string{r.String()}
	}
	vs := make([]string, len(r.Nodes))
	for i, n := range r.Nodes {
		vs[i] = n.StringValue()
	}
	return vs
}

// Paths returns the canonical path of each node (xmldom.Node.Path); nil
// for a scalar result.
func (r Result) Paths() []string {
	if r.Type != NodeSet {
		return nil
	}
	ps := make([]string, len(r.Nodes))
	for i, n := range r.Nodes {
		ps[i] = n.Path()
	}
	return ps
}

// Query evaluates the expression with node as the context node, default
// Options and no work limit. Use Eval for documents or expressions that
// are not trusted to be small.
func (x *Expr) Query(node *xmldom.Node) Result {
	r, _ := x.Eval(context.Background(), node, Options{})
	return r
}

// Eval evaluates the expression with node as the context node (position
// and size 1); an absolute path starts at node's document. It stops when
// ctx is done (ctx's error, wrapped by errs) or over opts.Limits.MaxSteps
// (errs.Unsupported, code xpath.too_complex). With StrictNames, a prefix
// missing from Namespaces is errs.InvalidArgument with code
// xpath.unknown_prefix.
func (x *Expr) Eval(ctx context.Context, node *xmldom.Node, opts Options) (Result, error) {
	if opts.StrictNames {
		if err := x.checkPrefixes(opts.Namespaces); err != nil {
			return Result{}, err
		}
	}
	e := &evaluator{ctx: ctx, maxSteps: opts.Limits.MaxSteps, ns: opts.Namespaces, strict: opts.StrictNames}
	v, err := e.run(x.root, node)
	if err != nil {
		return Result{}, err
	}
	switch v := v.(type) {
	case nodeSet:
		return Result{Type: NodeSet, Nodes: v}, nil
	case string:
		return Result{Type: String, Str: v}, nil
	case float64:
		return Result{Type: Number, Num: v}, nil
	}
	return Result{Type: Boolean, Bool: v.(bool)}, nil
}

func (x *Expr) checkPrefixes(ns map[string]string) error {
	var bad *nodeTest
	walk(x.root, func(t *nodeTest) {
		if bad != nil || t.prefix == "" || t.prefix == "xml" {
			return
		}
		if _, ok := ns[t.prefix]; !ok {
			bad = t
		}
	})
	if bad == nil {
		return nil
	}
	off16 := offset16(x.src, bad.pos)
	return errs.New(errs.InvalidArgument, "The namespace prefix "+strconv.Quote(bad.prefix)+" is not defined (at position "+strconv.Itoa(off16)+").").
		WithCode(CodeUnknownPrefix).
		WithHint("Add the prefix to the namespace map, or turn off strict name matching.").
		WithDetail("prefix", bad.prefix).
		WithDetail("offset", strconv.Itoa(bad.pos)).
		WithDetail("offset16", strconv.Itoa(off16))
}

// walk calls f for every node test of x, in expression order.
func walk(x expr, f func(*nodeTest)) {
	switch x := x.(type) {
	case *binaryExpr:
		walk(x.l, f)
		walk(x.r, f)
	case *negExpr:
		walk(x.x, f)
	case *unionExpr:
		for _, p := range x.parts {
			walk(p, f)
		}
	case *callExpr:
		for _, a := range x.args {
			walk(a, f)
		}
	case *pathExpr:
		if x.filter != nil {
			walk(x.filter, f)
		}
		for _, p := range x.preds {
			walk(p, f)
		}
		for i := range x.steps {
			f(&x.steps[i].test)
			for _, p := range x.steps[i].preds {
				walk(p, f)
			}
		}
	}
}

// QueryXML parses data as an XML document (strict, xmldom.DefaultLimits),
// parses expr and evaluates it on the document with default Options and
// DefaultLimits. Errors are errs errors: InvalidArgument with code
// xpath.invalid_query, the xmldom codes (xmldom.invalid_xml, ...), or
// Unsupported with xpath.too_complex.
func QueryXML(expr string, data []byte) (Result, error) {
	x, err := Parse(expr)
	if err != nil {
		return Result{}, err.(*SyntaxError).Err()
	}
	doc, err := xmldom.Parse(data)
	if err != nil {
		return Result{}, xmldom.AsErr(err)
	}
	return x.Eval(context.Background(), doc.Root, Options{Limits: DefaultLimits})
}
