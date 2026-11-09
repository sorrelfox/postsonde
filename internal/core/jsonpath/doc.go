// Package jsonpath evaluates JSONPath queries (RFC 9535) on JSON values.
// It is shared by response filtering, assertions and variable extraction.
//
// The whole of RFC 9535 is implemented: root and current node
// identifiers, name, wildcard, index, slice and filter selectors, child
// and descendant segments, comparisons with the RFC's semantics (numbers
// numerically, strings by Unicode scalar values, deep equality, Nothing),
// logical operators, existence tests and the function extensions
// length(), count(), match(), search() and value(), with the
// well-typedness rules checked by Parse. match() and search() take
// I-Regexp (RFC 9485) patterns, translated to Go's RE2 syntax.
//
//	p, err := jsonpath.Parse(`$.store.book[?@.price < 10].title`)
//	doc, err := jsonpath.Decode(body) // keeps member order and number literals
//	for _, n := range p.Query(doc) {
//		fmt.Println(n.Path, n.Value) // $['store']['book'][0]['title'] Sayings of the Century
//	}
//
// Numbers are compared as exact decimals, never as float64, so large
// integers and long fractions compare correctly. Parse errors are
// *SyntaxError with a byte and UTF-16 offset. Eval bounds the work with
// Limits and a context.
//
// The package passes the JSONPath Compliance Test Suite, vendored in
// testdata/cts. See docs/jsonpath.md.
package jsonpath
