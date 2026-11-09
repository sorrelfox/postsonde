package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/charset"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/jsonfmt"
	"github.com/sorrelfox/postsonde/internal/core/jsonpath"
	"github.com/sorrelfox/postsonde/internal/core/xmldom"
	"github.com/sorrelfox/postsonde/internal/core/xpath"
)

// Response filter (HTTP-36): a JSONPath query on a JSON body or an XPath
// expression on an XML (or HTML) body, evaluated on the bytes received.

// Filter syntaxes (FilterBodyInputDTO.Syntax).
const (
	SyntaxJSONPath = "jsonpath"
	SyntaxXPath    = "xpath"
)

// Limits of a filter result.
const (
	// MaxFilterText is how much of a result's text FilterBodyResultDTO
	// carries, like the body view's MaxBodyText.
	MaxFilterText = MaxBodyText
	// MaxFilterPaths is how many result paths FilterBodyResultDTO lists.
	MaxFilterPaths = 1000
	// MaxFilterExpression is the longest expression FilterBody takes.
	MaxFilterExpression = 64 << 10
	// FilterTimeout bounds one evaluation, parsing the body included.
	FilterTimeout = 10 * time.Second
)

// CodeInvalidFilter: FilterBody got an unknown syntax or an expression
// over MaxFilterExpression.
const CodeInvalidFilter = "http.invalid_filter"

// FilterBodyInputDTO is what FilterBody takes.
type FilterBodyInputDTO struct {
	// BodyID is ResponseDTO.BodyID: the bytes received are filtered.
	BodyID string `json:"bodyId"`
	// Text is filtered instead when BodyID is "": the body as the view
	// shows it (ResponseDTO.Body), for a response whose bytes are no
	// longer kept.
	Text string `json:"text"`
	// Syntax is "jsonpath" or "xpath".
	Syntax string `json:"syntax"`
	// Expression is the query.
	Expression string `json:"expression"`
	// Charset is ResponseDTO.Charset, the encoding the bytes are decoded
	// with ("" reads them as UTF-8).
	Charset string `json:"charset"`
	// HTML parses an XPath body leniently (ResponseDTO.BodyLanguage
	// "html").
	HTML bool `json:"html"`
}

// FilterErrorDTO is why a filter has no result.
type FilterErrorDTO struct {
	// Where is "expression" (the query does not parse), "body" (the body
	// is not valid JSON or XML) or "limit" (the evaluation did too much
	// work or took too long).
	Where string `json:"where"`
	// Message says what is wrong, without the position.
	Message string `json:"message"`
	// Offset is the position in the expression, in UTF-16 code units as
	// JavaScript strings count, for Where "expression"; -1 otherwise.
	Offset int `json:"offset"`
	// Line and Column are the 1-based position in the body for Where
	// "body" (0 when not known); Column counts UTF-16 code units for JSON,
	// bytes for XML.
	Line   int `json:"line"`
	Column int `json:"column"`
}

// FilterBodyResultDTO is what FilterBody returns.
type FilterBodyResultDTO struct {
	// Valid is false when Error says why there is no result.
	Valid bool            `json:"valid"`
	Error *FilterErrorDTO `json:"error"`
	// ResultType is "nodes" (a JSONPath node list or an XPath node-set),
	// or "string", "number" or "boolean" (an XPath expression such as
	// count(//item)).
	ResultType string `json:"resultType"`
	// Count is the number of nodes, 1 for a scalar.
	Count int `json:"count"`
	// Text is the result to show: JSONPath values as a pretty JSON array;
	// XPath nodes indented, one after another; a scalar as its string.
	// At most MaxFilterText bytes (Clipped).
	Text    string `json:"text"`
	Clipped bool   `json:"clipped"`
	// Language highlights Text: "json", "xml" or "text".
	Language string `json:"language"`
	// Paths are the nodes' normalized (JSONPath) or canonical (XPath)
	// paths, in result order, at most MaxFilterPaths.
	Paths []string `json:"paths"`
}

// filterDoc is a parsed body, kept for the next keystroke.
type filterDoc struct {
	key  string
	json any
	xml  *xmldom.Document
}

// filterCache keeps the last parsed body, so typing a query does not
// parse a large body again for every character.
type filterCache struct {
	mu   sync.Mutex
	last *filterDoc
}

func (c *filterCache) get(key string) *filterDoc {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last != nil && c.last.key == key {
		return c.last
	}
	return nil
}

func (c *filterCache) put(d *filterDoc) {
	c.mu.Lock()
	c.last = d
	c.mu.Unlock()
}

// FilterBody evaluates a JSONPath query or an XPath expression on a
// response body (HTTP-36) and returns what it selects, ready to show.
// The body is the bytes received (in.BodyID, decoded with in.Charset),
// not the view's clipped text, or in.Text when no BodyID is given. An
// expression that does not parse, a body that is not JSON or XML and an
// evaluation over the limits are a result with an error (and a position),
// not a failure.
//
// Errors: http.body_gone (not_found) when the body is no longer kept (the
// UI may filter the shown text instead), http.invalid_filter
// (invalid_argument) for an unknown syntax or an overlong expression.
func (s *HTTPService) FilterBody(ctx context.Context, in FilterBodyInputDTO) (FilterBodyResultDTO, error) {
	if in.Syntax != SyntaxJSONPath && in.Syntax != SyntaxXPath {
		return FilterBodyResultDTO{}, errs.New(errs.InvalidArgument, "Unknown filter syntax "+fmt.Sprintf("%q", in.Syntax)+": use jsonpath or xpath.").WithCode(CodeInvalidFilter)
	}
	if len(in.Expression) > MaxFilterExpression {
		return FilterBodyResultDTO{}, errs.New(errs.InvalidArgument, "The filter expression is too long.").WithCode(CodeInvalidFilter)
	}
	ctx, cancel := context.WithTimeout(ctx, FilterTimeout)
	defer cancel()

	// The expression first: a typo is reported without reading the body.
	var jp *jsonpath.Path
	var xp *xpath.Expr
	if in.Syntax == SyntaxJSONPath {
		p, err := jsonpath.Parse(in.Expression)
		if err != nil {
			return expressionError(err), nil
		}
		jp = p
	} else {
		x, err := xpath.Parse(in.Expression)
		if err != nil {
			return expressionError(err), nil
		}
		xp = x
	}

	doc, bad, err := s.filterDoc(ctx, in)
	if err != nil {
		return FilterBodyResultDTO{}, err
	}
	if bad != nil {
		return FilterBodyResultDTO{Error: bad, Paths: []string{}}, nil
	}

	if jp != nil {
		nodes, err := jp.Eval(ctx, doc.json, jsonpath.DefaultLimits)
		if err != nil {
			return limitError(ctx, err), nil
		}
		return jsonPathResult(nodes), nil
	}
	r, err := xp.Eval(ctx, doc.xml.Root, xpath.Options{Limits: xpath.DefaultLimits})
	if err != nil {
		return limitError(ctx, err), nil
	}
	return xpathResult(doc.xml, r), nil
}

// filterDoc returns the parsed body of in, from the cache when it was the
// last one parsed. bad is set when the body is not JSON or XML.
func (s *HTTPService) filterDoc(ctx context.Context, in FilterBodyInputDTO) (doc *filterDoc, bad *FilterErrorDTO, err error) {
	key := ""
	if in.BodyID != "" {
		key = fmt.Sprintf("%s\x00%s\x00%s\x00%v", in.BodyID, in.Syntax, in.Charset, in.HTML)
		if d := s.filters.get(key); d != nil {
			return d, nil, nil
		}
	}
	text, bad, err := s.filterText(in)
	if err != nil || bad != nil {
		return nil, bad, err
	}
	doc = &filterDoc{key: key}
	if in.Syntax == SyntaxJSONPath {
		v, err := jsonpath.Decode([]byte(text))
		if err != nil {
			return nil, jsonBodyError(text, err), nil
		}
		doc.json = v
	} else {
		d, err := xmldom.ParseOptions(ctx, []byte(text), xmldom.Options{HTML: in.HTML, Decoded: true})
		if err != nil {
			return nil, xmlBodyError(ctx, err), nil
		}
		doc.xml = d
	}
	if key != "" {
		s.filters.put(doc)
	}
	return doc, nil, nil
}

// filterText returns the body of in as text: the kept bytes decoded with
// in.Charset (a byte order mark wins), or in.Text. A kept body over
// MaxEvalBodyBytes is not read: bad says so (HTTP-37).
func (s *HTTPService) filterText(in FilterBodyInputDTO) (string, *FilterErrorDTO, error) {
	if in.BodyID == "" {
		return in.Text, nil, nil
	}
	if s.bodies == nil {
		return "", nil, bodyGone()
	}
	kb, r, ok, err := s.bodies.open(in.BodyID)
	if err != nil {
		return "", nil, errs.Wrap(err, errs.IO, "The response body could not be read.")
	}
	if !ok {
		return "", nil, bodyGone()
	}
	defer r.Close()
	if kb.size > MaxEvalBodyBytes {
		return "", &FilterErrorDTO{Where: "limit", Message: tooLargeToEval("filter", kb.size), Offset: -1}, nil
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return "", nil, errs.Wrap(err, errs.IO, "The response body could not be read.")
	}
	enc, bom := charset.Canonical(in.Charset), 0
	if e, n := charset.BOM(raw); e != "" {
		enc, bom = e, n
	}
	if enc == "" {
		enc = charset.UTF8
	}
	text, _ := charset.Decode(raw[bom:], enc)
	return text, nil, nil
}

// expressionError is the result for a query that does not parse.
func expressionError(err error) FilterBodyResultDTO {
	e := &FilterErrorDTO{Where: "expression", Message: err.Error(), Offset: 0}
	var je *jsonpath.SyntaxError
	var xe *xpath.SyntaxError
	switch {
	case errors.As(err, &je):
		e.Message, e.Offset = capitalize(je.Message)+".", je.Offset16
	case errors.As(err, &xe):
		e.Message, e.Offset = capitalize(xe.Message)+".", xe.Offset16
	}
	return FilterBodyResultDTO{Error: e, Paths: []string{}}
}

// jsonBodyError says why the body is not JSON, with a position when
// jsonfmt finds one.
func jsonBodyError(text string, err error) *FilterErrorDTO {
	e := &FilterErrorDTO{Where: "body", Offset: -1}
	if se := jsonfmt.Validate(text); se != nil {
		e.Message, e.Line, e.Column = "The body is not valid JSON: "+se.Message+".", se.Line, se.Column
		return e
	}
	e.Message = "The body is not valid JSON: " + strings.TrimPrefix(err.Error(), "jsonpath: ") + "."
	return e
}

// xmlBodyError says why the body is not XML.
func xmlBodyError(ctx context.Context, err error) *FilterErrorDTO {
	e := &FilterErrorDTO{Where: "body", Offset: -1}
	var se *xmldom.SyntaxError
	var le *xmldom.LimitError
	switch {
	case errors.As(err, &se):
		e.Message, e.Line, e.Column = "The body is not well-formed XML: "+se.Message+".", se.Line, se.Column
	case errors.As(err, &le):
		e.Where, e.Message = "limit", "The body is too large to filter: "+strings.TrimPrefix(le.Error(), "xml: ")+"."
	case ctx.Err() != nil:
		e.Where, e.Message = "limit", "Reading the body took too long."
	default:
		e.Message = "The body could not be read as XML: " + strings.TrimPrefix(err.Error(), "xml: ") + "."
	}
	return e
}

// limitError is the result for an evaluation that was stopped: over
// the work limit, over FilterTimeout, or cancelled (a newer keystroke).
func limitError(ctx context.Context, err error) FilterBodyResultDTO {
	var msg string
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		msg = "The filter took longer than " + FilterTimeout.String() + "; make the expression more specific."
	case ctx.Err() != nil:
		msg = "The filter was cancelled."
	case errs.KindOf(err) == errs.Unsupported:
		msg = "The expression does too much work on this body; make it more specific (fewer '..' or '//' steps, simpler filters)."
	default:
		msg = err.Error()
	}
	return FilterBodyResultDTO{Error: &FilterErrorDTO{Where: "limit", Message: msg, Offset: -1}, Paths: []string{}}
}

// jsonPathResult shows a node list as a pretty JSON array of its values,
// numbers as written in the body.
func jsonPathResult(nodes jsonpath.NodeList) FilterBodyResultDTO {
	out := FilterBodyResultDTO{Valid: true, ResultType: "nodes", Count: len(nodes), Language: langJSON, Paths: paths(nodes.Paths())}
	var buf bytes.Buffer
	values := nodes.Values()
	if values == nil {
		values = []any{}
	}
	if err := writeJSON(&buf, values); err != nil {
		out.Text, out.Language = "The result could not be shown: "+err.Error(), langText
		return out
	}
	text, serr := jsonfmt.Beautify(strings.TrimSpace(buf.String()), 2)
	if serr != nil {
		text = buf.String()
	}
	out.Text, out.Clipped = clipText(text)
	return out
}

// writeJSON writes a value of jsonpath's model (*jsonpath.Object,
// []any, json.Number, string, bool, nil) as compact JSON, members in
// document order, numbers as written, and <, > and & not escaped (the
// view shows them as the body has them).
func writeJSON(buf *bytes.Buffer, v any) error {
	switch v := v.(type) {
	case *jsonpath.Object:
		buf.WriteByte('{')
		for i := range v.Len() {
			if i > 0 {
				buf.WriteByte(',')
			}
			name, value := v.Member(i)
			if err := writeJSON(buf, name); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeJSON(buf, value); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil
	case []any:
		buf.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJSON(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	case json.Number:
		buf.WriteString(v.String())
		return nil
	}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	buf.Truncate(buf.Len() - 1) // Encode's newline
	return nil
}

// xpathResult shows a node-set as its nodes indented one after another,
// and a scalar as its string.
func xpathResult(doc *xmldom.Document, r xpath.Result) FilterBodyResultDTO {
	switch r.Type {
	case xpath.NodeSet:
	case xpath.Number:
		return scalar("number", xpath.FormatNumber(r.Num))
	case xpath.Boolean:
		return scalar("boolean", r.String())
	default:
		return scalar("string", r.Str)
	}
	out := FilterBodyResultDTO{Valid: true, ResultType: "nodes", Count: len(r.Nodes), Language: langXML, Paths: paths(r.Paths())}
	var b strings.Builder
	for i, n := range r.Nodes {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(doc.IndentNode(n, "  "))
		if b.Len() > MaxFilterText {
			break
		}
	}
	out.Text, out.Clipped = clipText(b.String())
	return out
}

func scalar(kind, text string) FilterBodyResultDTO {
	t, clipped := clipText(text)
	return FilterBodyResultDTO{Valid: true, ResultType: kind, Count: 1, Text: t, Clipped: clipped, Language: langText, Paths: []string{}}
}

// paths returns at most MaxFilterPaths of ps, never nil.
func paths(ps []string) []string {
	if len(ps) > MaxFilterPaths {
		ps = ps[:MaxFilterPaths]
	}
	if ps == nil {
		ps = []string{}
	}
	return ps
}

// clipText cuts text to MaxFilterText bytes without splitting a
// character.
func clipText(text string) (string, bool) {
	if len(text) <= MaxFilterText {
		return text, false
	}
	return string(clip([]byte(text), charset.UTF8)), true
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
