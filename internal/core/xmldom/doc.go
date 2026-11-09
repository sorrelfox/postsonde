// Package xmldom builds a read-only DOM of an XML document from
// encoding/xml tokens. The XPath evaluator (internal/core/xpath), response
// filtering and assertions query it.
//
//	doc, err := xmldom.Parse(body) // err: *SyntaxError, *LimitError or *EncodingError
//	for _, n := range doc.Root.Children {
//		fmt.Println(n.Type, n.Name.Local, n.Path(), doc.Position(n.Offset))
//	}
//
// The tree keeps what XPath and the UI need: elements with their namespace
// URI, local name and prefix as written; attributes, with the xmlns
// declarations kept apart; text and CDATA sections (flagged); comments;
// processing instructions; the DOCTYPE, raw. Every node has its byte
// offsets in the source, its document order index and a canonical XPath
// path such as /root[1]/item[2]/@id.
//
// Parsing is strict (well-formed XML with namespaces) by default;
// Options.HTML parses HTML-ish markup leniently. External entities are
// never fetched and entities declared in a DTD are never expanded, so XXE
// and entity-expansion ("billion laughs") documents are rejected (strict)
// or kept as literal text (HTML mode). Input size, depth and node count are
// bounded by Limits.
//
// UTF-8, UTF-16 (with a byte order mark or detected from "<?"), ISO-8859-1,
// Windows-1252 and US-ASCII are decoded; other encodings are an
// *EncodingError. See docs/xpath.md.
package xmldom
