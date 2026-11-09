package mockopenapi

import (
	"bytes"
	"encoding/json"
	"mime"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/importer/openapiimport"
	"github.com/sorrelfox/postsonde/internal/core/openapi"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockroute"
)

// Severity is how bad a diagnostic is; the values are openapi's.
type Severity = openapi.Severity

// Severities.
const (
	SeverityError   = openapi.SeverityError
	SeverityWarning = openapi.SeverityWarning
	SeverityInfo    = openapi.SeverityInfo
)

// Diagnostic codes of the generator. The reader's diagnostics (openapi.*,
// docs/openapi.md) come too.
const (
	// CodeNoOperations: the document has no operations to mock.
	CodeNoOperations = "mockopenapi.no_operations"
	// CodePath: the path had to change beyond the {param} syntax.
	CodePath = "mockopenapi.path"
	// CodePrefix: the prefix could not be taken from the servers.
	CodePrefix = "mockopenapi.prefix"
	// CodeStatus: the operation has no 2xx or default response.
	CodeStatus = "mockopenapi.status"
	// CodeMediaType: the response has several media types, or one the
	// body cannot be made for (a file); which one was used.
	CodeMediaType = "mockopenapi.media_type"
	// CodeExample: which named example the body is, or that the
	// document's examples are external files.
	CodeExample = "mockopenapi.example"
	// CodeExampleTruncated: the generated body stops at a recursive
	// schema or a size limit.
	CodeExampleTruncated = "mockopenapi.example_truncated"
	// CodeNoExample: the response has content but neither an example nor
	// a schema to make one from; the body is empty.
	CodeNoExample = "mockopenapi.no_example"
	// CodeBodyTooLarge: the example is larger than a route's body can be.
	CodeBodyTooLarge = "mockopenapi.body_too_large"
	// CodeHeader: a response header's example cannot be sent.
	CodeHeader = "mockopenapi.header"
	// CodeDuplicateRoute: two operations give the same method and path
	// pattern; the later one is left out.
	CodeDuplicateRoute = "mockopenapi.duplicate_route"
	// CodeInvalidRoute: the generated route is not valid; it is left out.
	CodeInvalidRoute = "mockopenapi.invalid_route"
	// CodeTooManyRoutes: more operations than a server has routes.
	CodeTooManyRoutes = "mockopenapi.too_many_routes"
	// CodeNotMocked: webhooks and callbacks, which the API calls rather
	// than answers, get no routes.
	CodeNotMocked = "mockopenapi.not_mocked"
	// CodeExistingRoute: Merge kept an existing route with the same method
	// and path instead of the generated one.
	CodeExistingRoute = "mockopenapi.existing_route"
)

// Diagnostic is a problem or a note about the document or one operation.
type Diagnostic struct {
	Severity Severity
	Code     string
	Message  string
	// Operation is "GET /pets/{petId}" as the document writes it; "" for
	// the whole document.
	Operation string
	// RouteIndex is the index in Result.Routes of the operation's route,
	// -1 when there is none.
	RouteIndex int
	// Pos is where in the document, when known.
	Pos openapi.Pos
}

// Options configure Generate.
type Options struct {
	// Prefix is put before every path ("/v1"). nil uses DefaultPrefix; a
	// server URL is taken as its path (PrefixOf).
	Prefix *string
}

// Operation says which operation a generated route answers.
type Operation struct {
	// Method is upper case; Path is the path as the document writes it.
	Method      string
	Path        string
	OperationID string
	Summary     string
	// Response is the response the route sends ("200", "2XX",
	// "default"), "" when the operation documents none.
	Response string
	// MediaType is the media type of the body, "" for none.
	MediaType string
}

// Result is the routes of a document.
type Result struct {
	// Title and Version are the document's info; OpenAPI its version, and
	// Swagger "2.0" for a converted Swagger document.
	Title   string
	Version string
	OpenAPI string
	Swagger string
	// DefaultPrefix is the path of the document's first server; Prefix
	// is the prefix the routes have.
	DefaultPrefix string
	Prefix        string
	// Routes are enabled, in document order, with IDs from the operation
	// IDs; Operations[i] is what Routes[i] answers.
	Routes     []mockhttp.Route
	Operations []Operation
	// Diagnostics are the reader's, then the generator's in document
	// order.
	Diagnostics []Diagnostic
}

// DefaultPrefix is the path of the document's first server ("" for "/"),
// with its variables at their defaults.
func DefaultPrefix(doc *openapi.Document) string {
	if doc == nil || len(doc.Servers) == 0 {
		return ""
	}
	return PrefixOf(doc.Servers[0].DefaultURL())
}

// Parse reads an OpenAPI 3.0/3.1 or Swagger 2.0 document (JSON or YAML)
// and generates its routes. The error is the reader's: data is not such a
// document at all.
func Parse(data []byte, opts Options) (Result, error) {
	doc, diags, err := openapi.Parse(data)
	if err != nil {
		return Result{}, err
	}
	return Generate(doc, diags, opts), nil
}

// Generate makes one route per operation of doc. readerDiags are the
// reader's diagnostics, passed through. Every returned route is valid
// (mockhttp.Check): an operation whose route would not be is reported and
// left out.
func Generate(doc *openapi.Document, readerDiags []openapi.Diagnostic, opts Options) Result {
	res := Result{}
	for _, d := range readerDiags {
		res.Diagnostics = append(res.Diagnostics, Diagnostic{Severity: d.Severity, Code: d.Code, Message: d.Message, RouteIndex: -1, Pos: d.Pos})
	}
	if doc == nil {
		return res
	}
	res.Title, res.Version, res.OpenAPI, res.Swagger = doc.Info.Title, doc.Info.Version, doc.OpenAPI, doc.Swagger
	res.DefaultPrefix = DefaultPrefix(doc)
	res.Prefix = res.DefaultPrefix
	if opts.Prefix != nil {
		res.Prefix = PrefixOf(*opts.Prefix)
	}
	docDiag := func(sev Severity, code, msg string) {
		res.Diagnostics = append(res.Diagnostics, Diagnostic{Severity: sev, Code: code, Message: msg, RouteIndex: -1})
	}
	if opts.Prefix == nil && len(doc.Servers) > 0 && strings.Contains(res.Prefix, "{") {
		docDiag(SeverityWarning, CodePrefix, "The server URL "+doc.Servers[0].URL+" has a variable without a default in its path; the routes start with it as a parameter.")
	}

	g := &generator{names: openapiimport.SchemaNames(doc), usedIDs: map[string]bool{}}
	ops := doc.Operations()
	if len(ops) == 0 {
		docDiag(SeverityWarning, CodeNoOperations, "The document has no operations under paths, so there are no routes to generate.")
	}
	if len(ops) > mockhttp.MaxRoutes {
		docDiag(SeverityError, CodeTooManyRoutes, "The document has "+strconv.Itoa(len(ops))+" operations; a mock server has at most "+
			strconv.Itoa(mockhttp.MaxRoutes)+" routes, so only the first "+strconv.Itoa(mockhttp.MaxRoutes)+" are generated.")
		ops = ops[:mockhttp.MaxRoutes]
	}
	if n := callbacks(doc); len(doc.Webhooks) > 0 || n > 0 {
		docDiag(SeverityInfo, CodeNotMocked, "Webhooks and callbacks get no routes: the API sends those requests instead of answering them.")
	}

	// Generate every operation, then drop the routes mockhttp rejects.
	type pending struct {
		route mockhttp.Route
		op    Operation
		diags []Diagnostic
		pos   openapi.Pos
	}
	all := make([]pending, 0, len(ops))
	for _, op := range ops {
		route, info, diags := g.operation(op, res.Prefix)
		all = append(all, pending{route: route, op: info, diags: diags, pos: op.Pos})
	}
	cfg := mockhttp.Config{Routes: make([]mockhttp.Route, len(all))}
	for i, p := range all {
		cfg.Routes[i] = p.route
	}
	rejected := map[int]bool{}
	for _, d := range mockhttp.Check(cfg) {
		if rejected[d.RouteIndex] {
			continue
		}
		rejected[d.RouteIndex] = true
		p := &all[d.RouteIndex]
		if d.Code == mockroute.CodeDuplicateRoute {
			p.diags = append(p.diags, Diagnostic{Severity: SeverityWarning, Code: CodeDuplicateRoute,
				Message: "Another operation already has the route " + p.route.Method + " " + p.route.Path + "; this one is left out."})
			continue
		}
		p.diags = append(p.diags, Diagnostic{Severity: SeverityError, Code: CodeInvalidRoute,
			Message: "The route " + p.route.Method + " " + p.route.Path + " is not valid and is left out: " + d.Message})
	}
	for i, p := range all {
		idx := -1
		if !rejected[i] {
			idx = len(res.Routes)
			res.Routes = append(res.Routes, p.route)
			res.Operations = append(res.Operations, p.op)
		}
		for _, d := range p.diags {
			d.Operation = p.op.Method + " " + p.op.Path
			d.RouteIndex = idx
			if d.Pos == (openapi.Pos{}) {
				d.Pos = p.pos
			}
			res.Diagnostics = append(res.Diagnostics, d)
		}
	}
	return res
}

// callbacks counts the callbacks of the document's operations.
func callbacks(doc *openapi.Document) int {
	n := 0
	for _, op := range doc.Operations() {
		n += len(op.Callbacks)
	}
	return n
}

type generator struct {
	names   map[*openapi.Schema]string
	usedIDs map[string]bool
}

func (g *generator) operation(op *openapi.Operation, prefix string) (mockhttp.Route, Operation, []Diagnostic) {
	var diags []Diagnostic
	add := func(sev Severity, code, msg string) {
		diags = append(diags, Diagnostic{Severity: sev, Code: code, Message: msg})
	}
	info := Operation{Method: op.Method, Path: op.Path, OperationID: op.OperationID, Summary: op.Summary}
	pattern, notes := ConvertPath(prefix + op.Path)
	for _, n := range notes {
		add(SeverityInfo, CodePath, n)
	}
	route := mockhttp.Route{
		ID:      g.routeID(op),
		Enabled: true,
		Method:  op.Method,
		Path:    pattern,
		Status:  200,
		Headers: []mockhttp.Header{},
		Body:    mockhttp.Body{Mode: mockhttp.BodyNone},
		Delay:   mockhttp.Delay{Mode: mockhttp.DelayNone},
	}

	name, resp, status := pickResponse(op.Responses)
	info.Response = name
	switch {
	case name == "":
		add(SeverityWarning, CodeStatus, "The operation documents no responses; the route answers 200 with no body.")
	case !isSuccess(name):
		add(SeverityWarning, CodeStatus, "The operation has no 2xx or default response; the route answers with the "+name+" response.")
	}
	route.Status = status
	if resp == nil {
		return route, info, diags
	}
	route.Headers = g.headers(resp, add)
	if status == 204 || status == 304 {
		if len(resp.Content) > 0 {
			add(SeverityInfo, CodeMediaType, "A "+strconv.Itoa(status)+" response has no body; the documented content is left out.")
		}
		return route, info, diags
	}
	body, mt, extra := g.body(resp, add)
	route.Body = body
	info.MediaType = mt
	route.Headers = append(route.Headers, extra...)
	return route, info, diags
}

// routeID is rt_ and the operation ID (or method and path) in lower case,
// letters, digits and _, unique within the document.
func (g *generator) routeID(op *openapi.Operation) string {
	src := op.OperationID
	if src == "" {
		src = op.Method + "_" + op.Path
	}
	var b strings.Builder
	b.WriteString("rt_")
	for _, r := range strings.ToLower(src) {
		switch {
		case 'a' <= r && r <= 'z', '0' <= r && r <= '9':
			b.WriteRune(r)
		default:
			if s := b.String(); s[len(s)-1] != '_' {
				b.WriteByte('_')
			}
		}
	}
	id := strings.TrimRight(b.String(), "_")
	if id == "rt" {
		id = "rt_op"
	}
	if len(id) > 64 {
		id = id[:64]
	}
	base := id
	for n := 2; g.usedIDs[id]; n++ {
		id = base + "_" + strconv.Itoa(n)
	}
	g.usedIDs[id] = true
	return id
}

func isSuccess(name string) bool {
	return name == "default" || strings.HasPrefix(name, "2")
}

// pickResponse chooses the response a route sends: the first 2xx code in
// document order, else a 2XX range (200), else default (200), else the
// first code from 200 to 599 (or a range, as x00). It returns the
// response's name, the response and the status.
func pickResponse(rs openapi.Entries[*openapi.Response]) (string, *openapi.Response, int) {
	type choice struct {
		name   string
		resp   *openapi.Response
		status int
	}
	var exact2, range2, def, other *choice
	for _, e := range rs {
		c := &choice{name: e.Name, resp: e.Value}
		code, err := strconv.Atoi(e.Name)
		switch {
		case err == nil && code >= 200 && code <= 299:
			if exact2 == nil {
				c.status = code
				exact2 = c
			}
		case strings.EqualFold(e.Name, "2XX"):
			if range2 == nil {
				c.status = 200
				range2 = c
			}
		case e.Name == "default":
			if def == nil {
				c.status = 200
				def = c
			}
		case err == nil && code >= 300 && code <= 599:
			if other == nil {
				c.status = code
				other = c
			}
		case len(e.Name) == 3 && strings.EqualFold(e.Name[1:], "XX") && e.Name[0] >= '3' && e.Name[0] <= '5':
			if other == nil {
				c.status = int(e.Name[0]-'0') * 100
				other = c
			}
		}
	}
	for _, c := range []*choice{exact2, range2, def, other} {
		if c != nil {
			return c.name, c.resp, c.status
		}
	}
	return "", nil, 200
}

// headers returns the response's headers that have an example (their
// own, or their schema's example, default, enum or const) or are
// required. Content-Type is left out, as the specification says, and so
// are the headers the server sets itself.
func (g *generator) headers(resp *openapi.Response, add func(Severity, string, string)) []mockhttp.Header {
	out := []mockhttp.Header{}
	for _, e := range resp.Headers {
		h := e.Value
		if h == nil {
			continue
		}
		switch strings.ToLower(e.Name) {
		case "content-type", "content-length", "transfer-encoding":
			continue
		}
		v, ok := g.headerExample(h)
		if !ok {
			continue
		}
		text := headerText(v)
		if !validHeaderValue(text) {
			add(SeverityInfo, CodeHeader, "The example of the header "+e.Name+" has a line break or control character; the header is left out.")
			continue
		}
		out = append(out, mockhttp.Header{Enabled: true, Name: e.Name, Value: text})
	}
	return out
}

func (g *generator) headerExample(h *openapi.Header) (json.RawMessage, bool) {
	if len(h.Example) > 0 {
		return h.Example, true
	}
	for _, e := range h.Examples {
		if e.Value != nil && len(e.Value.Value) > 0 {
			return e.Value.Value, true
		}
	}
	for _, c := range h.Content {
		if c.Value == nil {
			continue
		}
		if !h.Required && len(c.Value.Example) == 0 && len(c.Value.Examples) == 0 && !hasSchemaExample(c.Value.Schema) {
			continue
		}
		v, _, _, _ := openapiimport.MediaTypeExample(c.Value, g.exampleOptions())
		return v, v != nil
	}
	if s := h.Schema; s != nil && (h.Required || hasSchemaExample(s)) {
		v, _ := openapiimport.SchemaExample(s, g.exampleOptions())
		return v, v != nil
	}
	return nil, false
}

func hasSchemaExample(s *openapi.Schema) bool {
	return s != nil && (len(s.Example) > 0 || len(s.Examples) > 0 || len(s.Default) > 0 || len(s.Enum) > 0 || len(s.Const) > 0)
}

func (g *generator) exampleOptions() openapiimport.ExampleOptions {
	return openapiimport.ExampleOptions{Direction: openapiimport.ForResponse, Names: g.names}
}

// headerText is a JSON value as a header value, in the simple style: a
// string unquoted, an array's items joined with commas, an object's keys
// and values joined with commas.
func headerText(raw json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return strings.TrimSpace(string(raw))
	}
	return simple(v, true)
}

func simple(v any, top bool) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	case []any:
		if !top {
			b, _ := json.Marshal(t)
			return string(b)
		}
		parts := make([]string, len(t))
		for i, x := range t {
			parts[i] = simple(x, false)
		}
		return strings.Join(parts, ",")
	case map[string]any:
		if !top {
			b, _ := json.Marshal(t)
			return string(b)
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		parts := make([]string, 0, 2*len(keys))
		for _, k := range keys {
			parts = append(parts, k, simple(t[k], false))
		}
		return strings.Join(parts, ",")
	}
	return ""
}

func validHeaderValue(v string) bool {
	for i := 0; i < len(v); i++ {
		if c := v[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

// Media type kinds, in order of preference for the body.
const (
	kindJSON = iota
	kindXML
	kindYAML
	kindHTML
	kindText
	kindWildcard
	kindOther
	kindBinary
)

func mediaKind(name string) (int, string) {
	mt, _, err := mime.ParseMediaType(name)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(name))
	}
	switch {
	case strings.Contains(mt, "*"):
		return kindWildcard, mt
	case mt == "application/json" || strings.HasSuffix(mt, "+json") || strings.HasSuffix(mt, "/json"):
		return kindJSON, mt
	case strings.HasSuffix(mt, "/xml") || strings.HasSuffix(mt, "+xml"):
		return kindXML, mt
	case strings.HasSuffix(mt, "/yaml") || strings.HasSuffix(mt, "/x-yaml") || strings.HasSuffix(mt, "+yaml"):
		return kindYAML, mt
	case mt == "text/html":
		return kindHTML, mt
	case strings.HasPrefix(mt, "text/"):
		return kindText, mt
	case mt == "application/octet-stream" || strings.HasPrefix(mt, "image/") || strings.HasPrefix(mt, "audio/") ||
		strings.HasPrefix(mt, "video/") || strings.HasPrefix(mt, "font/") || mt == "application/pdf" ||
		mt == "application/zip" || strings.HasPrefix(mt, "multipart/"):
		return kindBinary, mt
	}
	return kindOther, mt
}

// body makes the route body from the response's preferred media type. It
// returns the body, the media type used and a Content-Type header when
// the body's language would imply another one.
func (g *generator) body(resp *openapi.Response, add func(Severity, string, string)) (mockhttp.Body, string, []mockhttp.Header) {
	none := mockhttp.Body{Mode: mockhttp.BodyNone}
	if len(resp.Content) == 0 {
		return none, "", nil
	}
	best, bestKind := -1, 0
	for i, e := range resp.Content {
		k, _ := mediaKind(e.Name)
		if best < 0 || k < bestKind {
			best, bestKind = i, k
		}
	}
	entry := resp.Content[best]
	if len(resp.Content) > 1 {
		add(SeverityInfo, CodeMediaType, "The body is "+entry.Name+"; the response is also documented as "+strings.Join(otherNames(resp.Content, best), ", ")+".")
	}
	mt := entry.Value
	if mt == nil {
		mt = &openapi.MediaType{}
	}
	if bestKind == kindBinary || (bestKind == kindOther && isBinary(mt.Schema)) {
		add(SeverityInfo, CodeMediaType, "The response is a file ("+entry.Name+"); the route sends no body. Paste one or change the body by hand.")
		return none, entry.Name, nil
	}

	v, exName, external, notes := openapiimport.MediaTypeExample(mt, g.exampleOptions())
	if exName != "" && len(mt.Examples) > 1 {
		add(SeverityInfo, CodeExample, "The body is the example “"+exName+"”, the first of "+strconv.Itoa(len(mt.Examples))+" in the document.")
	}
	if external {
		add(SeverityInfo, CodeExample, "The document's examples of this response are external files, which are not read; the body was generated from the schema.")
	}
	switch {
	case notes.Truncated:
		add(SeverityInfo, CodeExampleTruncated, "The generated body was cut short at the depth or size limit.")
	case notes.Recursive:
		add(SeverityInfo, CodeExampleTruncated, "The generated body stops where the schema refers back to itself.")
	}
	if v == nil {
		add(SeverityInfo, CodeNoExample, "The "+entry.Name+" response has no example and no schema to make one from; the body is empty.")
	}

	typ := "text"
	var text string
	str, isStr := jsonString(v)
	switch bestKind {
	case kindWildcard:
		// Any media type: a string example as text, anything else as JSON.
		if isStr {
			text = str
		} else {
			typ = "json"
			text = string(v)
		}
	case kindJSON:
		typ = "json"
		text = string(v)
	case kindXML:
		typ = "xml"
		if isStr {
			text = str
		} else if v != nil {
			text = openapiimport.XMLExample(mt.Schema, v, g.names)
		}
	default:
		switch bestKind {
		case kindYAML:
			typ = "yaml"
		case kindHTML:
			typ = "html"
		}
		if isStr {
			text = str
		} else if v != nil {
			text = string(v)
		}
	}
	if len(text) > mockhttp.MaxBodyBytes {
		add(SeverityWarning, CodeBodyTooLarge, "The example is larger than "+strconv.Itoa(mockhttp.MaxBodyBytes>>20)+" MiB; the body is empty.")
		text = ""
	}
	var headers []mockhttp.Header
	if bestKind != kindWildcard && !impliedContentType(entry.Name, typ) {
		headers = []mockhttp.Header{{Enabled: true, Name: "Content-Type", Value: entry.Name}}
	}
	return mockhttp.Body{Mode: mockhttp.BodyRaw, Type: typ, Text: text}, entry.Name, headers
}

// impliedContentType reports whether the mock server's implicit
// Content-Type of a body language (mockhttp: the request body's media
// type, with charset=utf-8 on text types) already says what name does.
func impliedContentType(name, typ string) bool {
	mt, params, err := mime.ParseMediaType(name)
	if err != nil || mt != request.RawMediaType(typ) {
		return false
	}
	switch len(params) {
	case 0:
		return true
	case 1:
		cs, ok := params["charset"]
		return ok && strings.HasPrefix(mt, "text/") && strings.EqualFold(cs, "utf-8")
	}
	return false
}

func otherNames(es openapi.Entries[*openapi.MediaType], skip int) []string {
	var out []string
	for i, e := range es {
		if i != skip {
			out = append(out, e.Name)
		}
	}
	return out
}

// isBinary reports whether a schema is a file: format binary, or a 3.1
// contentEncoding or contentMediaType that is not text.
func isBinary(s *openapi.Schema) bool {
	if s == nil {
		return false
	}
	if s.Format == "binary" || s.ContentEncoding != "" {
		return true
	}
	if s.ContentMediaType != "" {
		k, _ := mediaKind(s.ContentMediaType)
		return k == kindBinary
	}
	return false
}

// jsonString returns the text of a JSON string value.
func jsonString(v json.RawMessage) (string, bool) {
	t := bytes.TrimSpace(v)
	if len(t) == 0 || t[0] != '"' {
		return "", false
	}
	var s string
	if json.Unmarshal(t, &s) != nil {
		return "", false
	}
	return s, true
}
