# XML tree and XPath

`internal/core/xmldom` parses XML into a read-only tree and
`internal/core/xpath` evaluates XPath expressions on it. Response filtering
(HTTP-36), assertions (TEST-02) and variable extraction
(TEST-05, [extraction.md](extraction.md)) use them for XML bodies, as they use
[JSONPath](jsonpath.md) for JSON.

| Piece | Where |
|---|---|
| Tree, canonical paths, positions | `xmldom/node.go` |
| Parser (tokens to tree), options, limits | `xmldom/parse.go` |
| Encoding detection and decoding | `xmldom/charset.go` |
| Errors | `xmldom/errors.go`, `xpath/errors.go` |
| Tokenizer, parser | `xpath/lexer.go`, `xpath/parser.go`, `xpath/ast.go` |
| Evaluator, axes, name matching, limits | `xpath/eval.go` |
| Conversions and comparisons | `xpath/value.go` |
| Core function library | `xpath/functions.go` |
| API | `xpath/expr.go` |
| Realistic sample documents | `xpath/testdata/` |

## Use

```go
r, err := xpath.QueryXML(`//item[position() < 3]/title`, body)
// err: errs InvalidArgument xpath.invalid_query, Parse xmldom.invalid_xml,
//      Unsupported xmldom.unsupported_encoding / xmldom.too_large / xpath.too_complex
for _, n := range r.Nodes {
    fmt.Println(n.Path(), n.StringValue()) // /rss[1]/channel[1]/item[1]/title[1] Designing ...
}

// Parse once, run many times (Expr and Document are safe to share).
x, err := xpath.Parse(expr)                  // err is *xpath.SyntaxError
doc, err := xmldom.ParseOptions(ctx, body, xmldom.Options{
    HTML:     false,            // true for lenient HTML-ish markup
    Encoding: "iso-8859-1",     // Content-Type charset, used without BOM or declaration
})                                           // err: *SyntaxError, *LimitError, *EncodingError; xmldom.AsErr for errs
r, err := x.Eval(ctx, doc.Root, xpath.Options{
    Namespaces:  map[string]string{"s": "http://schemas.xmlsoap.org/soap/envelope/"},
    StrictNames: false,
    Limits:      xpath.DefaultLimits,
})
r = x.Query(doc.Root)                        // default options, no limit: trusted input only
x.ResultType()                               // NodeSet, String, Number or Boolean, known statically
```

- **`Result`** has `Type` and either `Nodes` (document order, no
  duplicates) or `Str`/`Num`/`Bool`. `String()`, `Number()` and
  `Boolean()` convert it as XPath's `string()`, `number()` and `boolean()`
  do (for a node-set: the first node's string-value), so an assertion can
  compare any result. `Values()` gives the string-values, `Paths()` the
  canonical paths.
- `xpath.FormatNumber` and `xpath.ParseNumber` are XPath's number/string
  conversions (`NaN`, `Infinity`, no exponent; `number('1e3')` is NaN).

## The tree

`xmldom.Parse` builds the tree from `encoding/xml`'s `RawToken`s and does
namespace resolution, tag matching and well-formedness checks itself.

| Node type | Kept |
|---|---|
| `DocumentNode` | `Root`; children: root element, comments, PIs, DOCTYPE |
| `ElementNode` | `Name{Space, Prefix, Local}` (URI resolved, prefix as written), `Attrs`, `NSDecls` (`xmlns`/`xmlns:p` declarations, kept apart from attributes), `Children` |
| `AttributeNode` | `Name`, `Value` (references replaced), `Parent` is the element |
| `TextNode` | `Value`; `CDATA` true for a CDATA section |
| `CommentNode` | `Value` |
| `ProcInstNode` | target in `Name.Local`, data in `Value` (the XML declaration is not a node) |
| `DoctypeNode` | the raw declaration in `Value` (`DOCTYPE note SYSTEM "note.dtd"`); not an XPath node |

Every node has:

- `Offset`/`End`: byte offsets of its markup in `Document.Source` (the
  input converted to UTF-8 without a byte order mark, which is also what
  the UI shows). An element spans its start tag to its end tag; an
  attribute spans `name="value"`. `Document.Position(offset)` gives the
  1-based line and byte column and the UTF-16 offset for CodeMirror.
- `Index`: document order (document 0, an element, then its attributes,
  then its children).
- `Path()`: the canonical path, below.
- `StringValue()`: XPath's string-value (all descendant text for an
  element).

Whitespace-only text outside the root element is dropped; inside elements
it is kept (XPath counts it: `node()`, `text()`). A text node and a CDATA
section next to each other stay separate nodes, so `text()` can return
two nodes where XPath 1.0's data model has one; `string()` and
comparisons on the element are unaffected.

### Canonical paths

`Node.Path()` is an XPath location path that selects exactly that node
with the default (lenient) name matching and no namespace bindings:

```
/soap:Envelope[1]/soap:Body[1]/detail[1]/ns1:sku[2]
/rss[1]/channel[1]/atom:link[1]/@href
/beans[1]/bean[3]/text()[2]
/comment()[1]
/processing-instruction('xml-stylesheet')[1]
/                     the document
""                    a DOCTYPE
```

Names are as written. An element without a prefix is positioned among the
preceding siblings with the same local name in any namespace (that is
what an unprefixed name test matches); with a prefix, among those with
the same prefix and local name. Text, comments and PIs (per target) are
positioned among their own kind. A path can be pasted into the filter box
as is. `TestPathsSelectTheirNode` checks this for every node of every
sample, and the fuzz tests for every result. The one exception is an
attribute name repeated on one element, which only HTML mode allows.

### Strict and HTML mode

Strict (default) requires well-formed XML with namespaces: one root
element, matching tags, declared prefixes, no repeated attributes, quoted
values, only the five predefined entities and character references, no
text outside the root, the XML declaration only at the start, a DOCTYPE
only before the root, valid UTF-8.

`Options.HTML` is for HTML-ish responses: `encoding/xml`'s non-strict
tokenizer (attributes without values or with unquoted values, unknown
entities kept as literal text), HTML entities (`&nbsp;`, `&copy;`), void
elements (`br`, `img`, `input`, `meta` ...) closed automatically, end tags
matched case-insensitively and closing any open elements inside them,
unmatched end tags ignored, open elements closed at the end, unbound
prefixes and several top-level nodes allowed. It is not an HTML5 parser:
`<p>` and `<li>` are not closed implicitly (a second `<li>` nests inside
the first), and unquoted values stop at characters other than letters,
digits, `_ : -` (`src=x.png` fails).

Decision: strict by default. A response filter on an `application/xml`
body should say when the body is broken instead of guessing; the UI can
offer HTML mode for `text/html`.

### Entities, XXE and expansion bombs

External entities are never fetched and entities declared in a DTD are
never expanded: `encoding/xml` only knows the predefined entities (plus
the HTML set in HTML mode). The DOCTYPE, including an internal subset, is
kept as raw text and nothing in it is interpreted. So:

- an XXE document (`<!ENTITY xxe SYSTEM "file:///etc/passwd">` ...
  `&xxe;`) or a parameter-entity SSRF is a syntax error in strict mode
  (`invalid character entity &xxe;`) and keeps `&xxe;` as text in HTML
  mode; no file or network access ever happens;
- a "billion laughs" document fails the same way, after reading only the
  input once.

`TestEntityExpansion` covers these.

### Encodings

| Input | Handling |
|---|---|
| UTF-8, with or without BOM | as is (the BOM is removed from `Source`) |
| UTF-16 LE/BE with a BOM, or detected from `<?` | converted; unpaired surrogates or an odd length are an `EncodingError` |
| `encoding="ISO-8859-1"` (`latin1`, `l1` ...) | converted |
| `encoding="windows-1252"` (`cp1252`) | converted (0x80-0x9F to `€ ‚ ƒ ...`) |
| `encoding="US-ASCII"` | checked: a byte above 0x7F is an `EncodingError` |
| `encoding="UTF-16"` on ASCII-compatible bytes | read as UTF-8 (the body was re-encoded without updating the declaration) |
| no BOM and no declaration | `Options.Encoding` (e.g. the Content-Type charset), else UTF-8 |
| anything else (UTF-32, EBCDIC, Shift_JIS, KOI8-R ...) | `EncodingError` with `Unsupported`, errs code `xmldom.unsupported_encoding` |

Only the standard library is used; the conversion is done before
tokenizing, so offsets always refer to UTF-8 text.

`Options.Decoded` skips all of this for text that is UTF-8 already (a
response body decoded with its charset by the HTTP service): a UTF-8 BOM
is dropped and the declaration is ignored.

### Indent

`xmldom.Indent(ctx, text, indent)` pretty-prints a decoded document for
the response viewer (HTTP-31): one element, comment, processing
instruction or DOCTYPE per line, markup copied as written, elements with
text or mixed content kept as they are. See
[http-client.md](http-client.md#response-body-http-31).

`Document.IndentNode(n, indent)` does the same for one node of a parsed
document, for the response filter's result (HTTP-36): an element with its
content, a comment or PI as written, an attribute as its markup
(`id="1"`), a text node as its text (references replaced), the document
node as all its children. Namespace declarations on the node's ancestors
are not repeated, so an element may show prefixes it does not declare.

### Response filter

The response view's filter bar (HTTP-36) evaluates XPath through
`HTTPService.FilterBody` ([http-client.md](http-client.md#response-filter-http-36)):
`xpath.Parse` first (a `SyntaxError`'s `Offset16` marks the position in
the filter box), then `xmldom.ParseOptions` on the bytes received,
decoded with the response charset (`Decoded: true`), strict for XML and
with `HTML: true` for a `text/html` body, then `Eval` with lenient names
and `DefaultLimits`. A node-set is shown as its nodes indented one after
another (`IndentNode`), with `Paths()` for the Paths view; a string,
number or boolean as XPath's string of it.

### Limits

`Options.Limits` (zero fields take `DefaultLimits`, negative means none):

| Limit | Default | Error |
|---|---|---|
| `MaxBytes` input size | 64 MiB | `LimitError{Limit: "bytes"}` |
| `MaxDepth` element nesting | 1024 | `LimitError{Limit: "depth"}` |
| `MaxNodes` all nodes | 4 000 000 | `LimitError{Limit: "nodes"}` |

`LimitError.Err()` is `unsupported` / `xmldom.too_large`. Parsing checks
the context every 1024 tokens. Work is linear in the input: namespace
lookups, repeated-attribute checks and HTML end-tag matching use maps. A
3 MB document with 360 000 nodes parses in about 0.13 s.

## XPath

XPath 1.0, with the semantics of the XPath 1.0 recommendation wherever a
feature is supported:

| Feature | Syntax |
|---|---|
| Location paths | `/`, `/a/b`, `a/b`, `//a`, `a//b`, `.`, `..` |
| Axes | `child::`, `descendant::`, `descendant-or-self::`, `parent::`, `ancestor::`, `ancestor-or-self::`, `following-sibling::`, `preceding-sibling::`, `following::`, `preceding::`, `attribute::` (`@`), `self::` |
| Name tests | `name`, `prefix:name`, `*`, `prefix:*`, `@id`, `@*`, `@xlink:href` |
| Node type tests | `text()`, `node()`, `comment()`, `processing-instruction()`, `processing-instruction('target')` |
| Predicates | `[2]`, `[last()]`, `[position() < 3]`, `[@id='x']`, `[name='y']`, `[not(scope)]`, several in a row (`[@a][2]`); reverse axes count backwards (`preceding-sibling::*[1]` is the nearest) |
| Filter expressions | `(//item)[2]`, `(//a \| //b)[last()]/c` |
| Union | `\|` |
| Operators | `or`, `and`, `= != < <= > >=`, `+ - * div mod`, unary `-`, parentheses |
| Literals | `'text'`, `"text"`, `12`, `1.5`, `.5` |
| Node-set functions | `last()`, `position()`, `count()`, `local-name()`, `namespace-uri()`, `name()` |
| String functions | `string()`, `concat()`, `starts-with()`, `contains()`, `substring-before()`, `substring-after()`, `substring()`, `string-length()`, `normalize-space()`, `translate()` |
| Boolean functions | `boolean()`, `not()`, `true()`, `false()`, `lang()` |
| Number functions | `number()`, `sum()`, `floor()`, `ceiling()`, `round()` |

Not supported (a `SyntaxError` says so): variables (`$x`), the
`namespace::` axis, `id()` (it needs DTD attribute types; use
`//*[@id='x']`), XPath 2.0+ (`matches()`, `if`, sequences, ...).

A result is a node-set in document order without duplicates, or a
string, number or boolean for an expression such as `count(//item)`.

### Comparisons and conversions

As XPath 1.0 §3.4 says:

- Node-set with node-set: true if **some pair** of string-values
  compares true; `=`/`!=` compare strings, `< <= > >=` numbers.
  `//a != 'x'` is true if some `a` is not `x`; both `=` and `!=` are false
  on an empty node-set.
- Node-set with a number: some node's `number(string-value)`; with a
  string: some node's string-value (`=`/`!=`) or number (relational);
  with a boolean: `boolean(node-set)`.
- Scalars: `=`/`!=` compare as booleans if either side is a boolean,
  else as numbers if either is a number, else as strings; `< <= > >=`
  always compare numbers. So `'10' < '9'` is false and
  `lastmod >= '2026-09-01'` is always false (both NaN): compare
  `translate(lastmod, '-', '') >= 20260901` instead.
- `number()` accepts only optional whitespace, `-`, digits and one `.`
  (`number('1e3')`, `number('+1')` are NaN). Numbers are IEEE doubles:
  `0.1 + 0.2` is `0.30000000000000004`.
- `string()` of a number: `NaN`, `Infinity`, `-Infinity`, integers
  without `.0`, no exponent (`1000000000000000000000`), `-0` as `0`.
- `round()` rounds halves up (`round(-2.5)` is -2); `substring()` uses the
  spec's rounding and NaN/Infinity rules; string lengths and positions
  count characters, not bytes.
- `mod` is truncating (`-7 mod 3` is -1); `div` by zero gives
  `Infinity`/`NaN`.

The tokenizer applies §3.7's disambiguation: `*` and `and or mod div` are
operators after an operand and names otherwise (`div div div` is valid).
Names may contain `.` and `-` (`java.version`, `a-b`).

### Namespaces and name matching

Decision: **lenient by default**, because API testers query SOAP and Atom
responses without setting up namespace maps, and paths copied from the
tree should just work.

| Name test | Lenient (default) | `StrictNames: true` (XPath 1.0) |
|---|---|---|
| `Quote` (element) | local name `Quote`, any namespace | `Quote` in no namespace |
| `Quote` with `Namespaces[""]` set | `Quote` in that namespace | `Quote` in that namespace |
| `q:Quote`, `q` in `Namespaces` | `Quote` in the mapped URI | the same |
| `q:Quote`, `q` not in `Namespaces` | written prefix `q`, local `Quote` | error `xpath.unknown_prefix` |
| `q:*` | the same rules for the namespace | the same |
| `@id` | attributes without a prefix (never namespaced) | the same |
| `xml:lang` | the XML namespace, always bound | the same |

A prefix in `Namespaces` always wins over the document's prefixes:
`//soap:Body` with `soap` mapped to the SOAP 1.2 URI finds `env:Body`, and
`//env:Body` with `env` mapped to another URI finds nothing.
`Namespaces[""]` is a default element namespace (an XPath 2.0 idea; XPath
1.0 has no way to name elements in a default namespace).
`Document.Namespaces()` returns every prefix declared in a document, a
ready-made map for strict mode. `local-name()` and `namespace-uri()` work
in both modes: `//*[local-name()='Quote' and namespace-uri()='urn:x']`.

## Errors

`xpath.Parse` returns `*SyntaxError` with `Offset` (bytes), `Offset16`
(UTF-16 code units) and `Message`:

```
xpath: unexpected end of expression: expected a node test (a name, '*', text(), node(), comment() or processing-instruction()) at 3
xpath: count() needs a node-set here; this is a string at 6
xpath: '|' combines node-sets only; this is a number at 6
xpath: id() is not supported (it needs DTD attribute types); use //*[@id='...'] at 0
xpath: unexpected name "foo": expected an operator at 4
```

`SyntaxError.Err()` is `invalid_argument` / `xpath.invalid_query` with
`offset` and `offset16` details. Types are checked statically, so a
function argument or a path step that needs a node-set is a parse error.
Nesting is limited to 64 levels of parentheses, predicates and calls
(`MaxNesting`) and expressions to 64 KiB (`MaxLength`).

`xmldom` errors: `*SyntaxError` (`Offset`, `Line`, `Column`, `Message`;
`parse` / `xmldom.invalid_xml`), `*LimitError` (`unsupported` /
`xmldom.too_large`), `*EncodingError` (`unsupported` /
`xmldom.unsupported_encoding`, or `parse` / `xmldom.invalid_encoding`).
`xmldom.AsErr` converts any of them.

## Limits

`Eval` counts work units (each node an axis visits, each expression
evaluated, each 64 bytes of string-value read, each node sorted) and stops
over `Limits.MaxSteps` with `unsupported` / `xpath.too_complex`, or when
the context is done. `DefaultLimits` (20 million) runs ordinary queries on
multi-megabyte documents in milliseconds (`count(//item[price > 100]/name)`
on a 3 MB document: about 30 ms) and stops a quadratic
`count(//*[count(//*) > 0])` after about 0.6 s. Node-sets stay
deduplicated while a step runs, so `//a//a` on deeply nested elements
does not blow up memory. `//x` without predicates runs as
`descendant::x`. `Query` has no limit and is for trusted input only.

## Tests

- `xpath/documents_test.go`: every feature on realistic documents in
  `testdata/`: a SOAP 1.1 fault with WS-Security headers, a SOAP 1.2
  response with a default namespace, an RSS 2.0 feed with Dublin Core and
  CDATA, an Atom feed with XHTML content and `xml:lang`, a Maven
  `pom.xml`, a sitemap with an image extension, an XHTML order page and a
  Spring bean config with comments, CDATA and a PI. Each case lists the
  expected node paths or value.
- `xpath/xpath_test.go`: operators, conversions and every function
  (edge cases from the spec), `Result` conversions, parse error offsets,
  strict prefix errors, canonical paths selecting every node of every
  sample, limits and cancellation, `QueryXML` errors, a document parsed in
  HTML mode.
- `xmldom/xmldom_test.go`, `charset_test.go`: tree shape, document order,
  namespaces, paths, offsets and line:col (multi-byte and UTF-16),
  well-formedness errors with positions, XXE and entity expansion, HTML
  mode, limits, cancellation, encodings.
- Fuzzing: `xmldom.FuzzParse` (no panic, typed errors, tree invariants),
  `xpath.FuzzParse` (errors inside the expression, static type equals the
  result type, every result path selects its node, in both name modes)
  and `xpath.FuzzQuery` (random expressions on random strict and HTML
  documents).
