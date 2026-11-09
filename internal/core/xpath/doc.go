// Package xpath evaluates XPath 1.0 expressions on an xmldom tree. It is
// shared by response filtering, assertions and variable extraction.
//
// The supported language is XPath 1.0 without variables, the namespace
// axis and id(): location paths (absolute and relative, abbreviated and
// with all other axes), name tests with prefixes, *, prefix:*, text(),
// node(), comment() and processing-instruction(), predicates (numeric and
// boolean), union, filter expressions, the or/and/=/!=/</<=/>/>= and
// arithmetic operators with XPath 1.0's conversion rules, and the core
// function library.
//
//	x, err := xpath.Parse(`//item[position() < 3]/title`)
//	doc, err := xmldom.Parse(body)
//	r, err := x.Eval(ctx, doc.Root, xpath.Options{Limits: xpath.DefaultLimits})
//	for _, n := range r.Nodes {
//		fmt.Println(n.Path(), n.StringValue()) // /rss[1]/channel[1]/item[1]/title[1] ...
//	}
//
// Name matching is lenient by default: an unprefixed element name matches
// in any namespace and an unbound prefix matches the prefix as written, so
// //soap:Body/GetQuoteResponse works on a SOAP response without a
// namespace map. Options.StrictNames selects XPath 1.0 matching.
//
// Parse errors are *SyntaxError with a byte and UTF-16 offset. Eval bounds
// the work with Limits and a context. See docs/xpath.md.
package xpath
