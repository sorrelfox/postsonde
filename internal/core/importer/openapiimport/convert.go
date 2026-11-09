package openapiimport

import (
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/openapi"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// BaseURLVariable is the collection variable the requests' URLs start
// with.
const BaseURLVariable = "baseUrl"

// conv converts one document.
type conv struct {
	doc   *openapi.Document
	names map[*openapi.Schema]string
	// base is the first server's URL with its variables at their
	// defaults, to tell whether an operation's own server is another.
	base string
	// vars are the root folder's variables; varSeen their names.
	vars    []projectfile.Variable
	varSeen map[string]bool
	// rootAuth is the document's auth, which requests inherit.
	rootAuth projectfile.Auth
}

// FromDocument converts a read OpenAPI document (with the reader's
// diagnostics) into the import model:
//
//   - Model.Name is info.title; the root folder's description is the
//     info's summary and description, its variables baseUrl (the first
//     server, its {variables} as variables of their own) and those the
//     auth refers to, and its auth the document's security.
//   - One folder per tag, in the order of the document's tags, then of
//     first use; an operation goes into its first tag's folder, untagged
//     ones after the folders.
//   - One http request per operation: the name from the summary, else the
//     operationId, else "METHOD /path"; the URL {{baseUrl}}/path with the
//     path's {placeholders} as path parameters; query, header and cookie
//     parameters with their examples; an example body; the auth when the
//     operation has its own security.
//
// Webhooks and callbacks are not requests to send and are only reported.
func FromDocument(doc *openapi.Document, diags []openapi.Diagnostic) *importer.Model {
	c := &conv{doc: doc, names: SchemaNames(doc), varSeen: map[string]bool{}}
	m := &importer.Model{Name: strings.TrimSpace(doc.Info.Title)}
	root := &m.Root
	root.Description = joinParas(doc.Info.Summary, doc.Info.Description, externalDocs(doc.ExternalDocs))
	root.Source = importer.Location{Line: 1, Column: 1}
	root.Diagnostics = append(root.Diagnostics, c.servers()...)

	auth, extra, adiags := c.security(doc.Security, "the document")
	root.Headers = extra
	if doc.Security == nil {
		auth = projectfile.Auth{Type: projectfile.AuthInherit}
	}
	c.rootAuth = auth
	root.Auth = auth
	for i := range adiags {
		adiags[i].Source = importer.Location{Path: "security"}
	}
	root.Diagnostics = append(root.Diagnostics, adiags...)

	// Folders by tag.
	folders := map[string]*importer.Folder{}
	var order []string
	tagFolder := func(name string) *importer.Folder {
		if f, ok := folders[name]; ok {
			return f
		}
		f := &importer.Folder{Name: folderName(name), Auth: projectfile.Auth{Type: projectfile.AuthInherit}}
		for i, t := range doc.Tags {
			if t.Name == name {
				f.Description = joinParas(t.Description, externalDocs(t.ExternalDocs))
				f.Source = location(t.Pos, "tags["+strconv.Itoa(i)+"]")
				break
			}
		}
		folders[name] = f
		order = append(order, name)
		return f
	}
	// Declared tags first, in their order, when they are used.
	used := map[string]bool{}
	for _, op := range doc.Operations() {
		if len(op.Tags) > 0 {
			used[op.Tags[0]] = true
		}
	}
	for _, t := range doc.Tags {
		if used[t.Name] {
			tagFolder(t.Name)
		}
	}

	byPointer := map[string]*importer.Request{}
	var untagged []importer.Item
	for _, p := range doc.Paths {
		for _, op := range p.Operations {
			q := c.operation(p, op)
			if op.Pos.Pointer != "" {
				byPointer[op.Pos.Pointer] = q
			}
			if len(op.Tags) == 0 {
				untagged = append(untagged, importer.RequestItem(q))
				continue
			}
			f := tagFolder(op.Tags[0])
			f.Items = append(f.Items, importer.RequestItem(q))
		}
	}
	for _, name := range order {
		root.Items = append(root.Items, importer.FolderItem(folders[name]))
	}
	root.Items = append(root.Items, untagged...)
	root.Variables = c.vars

	if n := len(doc.Webhooks); n > 0 {
		m.Diagnostics = append(m.Diagnostics, importer.Diagnostic{
			Code: CodeWebhooks, Severity: importer.SeverityInfo, Source: importer.Location{Path: "webhooks"},
			Message: "The document's " + plural(n, "webhook", "webhooks") + " (" + strings.Join(doc.Webhooks.Names(), ", ") + ") " + plural(n, "describes a request", "describe requests") + " the API sends, and " + plural(n, "was", "were") + " not imported.",
		})
	}

	// The reader's diagnostics go to the operation they are in, else to
	// the file.
	for _, d := range diags {
		id := importer.Diagnostic{
			Code: d.Code, Severity: importer.Severity(d.Severity), Message: d.Message,
			Source: importer.Location{Line: d.Pos.Line, Column: d.Pos.Column, Path: d.Pos.Pointer},
		}
		if q := owner(byPointer, d.Pos.Pointer); q != nil {
			q.Diagnostics = append(q.Diagnostics, id)
			continue
		}
		m.Diagnostics = append(m.Diagnostics, id)
	}
	return m
}

// owner returns the request of the operation whose pointer is pointer or
// a prefix of it.
func owner(byPointer map[string]*importer.Request, pointer string) *importer.Request {
	for p := pointer; p != "" && p != "#"; {
		if q, ok := byPointer[p]; ok {
			return q
		}
		i := strings.LastIndexByte(p, '/')
		if i < 0 {
			break
		}
		p = p[:i]
	}
	return nil
}

// servers sets up baseUrl and the server variables.
func (c *conv) servers() []importer.Diagnostic {
	var diags []importer.Diagnostic
	loc := importer.Location{Path: "servers"}
	if len(c.doc.Servers) == 0 {
		c.addVar(projectfile.Variable{Name: BaseURLVariable, Description: "The API's address."})
		return nil
	}
	s := c.doc.Servers[0]
	if s.Pos.Line > 0 {
		loc = location(s.Pos, "servers[0]")
	}
	c.base = trimBase(s.DefaultURL())
	// Declare baseUrl first so it leads the variables.
	c.addVar(projectfile.Variable{Name: BaseURLVariable})
	u := trimBase(templated(s))
	desc := strings.TrimSpace(s.Description)
	for _, v := range s.Variables {
		if v.Value == nil || !strings.Contains(s.URL, "{"+v.Name+"}") || v.Name == BaseURLVariable {
			continue
		}
		d := strings.TrimSpace(v.Value.Description)
		if len(v.Value.Enum) > 0 {
			d = joinSentences(d, "One of: "+strings.Join(v.Value.Enum, ", ")+".")
		}
		c.addVar(projectfile.Variable{Name: v.Name, Value: v.Value.Default, Description: d})
	}
	if len(c.doc.Servers) > 1 {
		var others []string
		for _, o := range c.doc.Servers[1:] {
			others = append(others, o.URL)
		}
		desc = joinSentences(desc, "Other servers: "+strings.Join(others, ", ")+".")
		diags = append(diags, importer.Diagnostic{
			Code: CodeServers, Severity: importer.SeverityInfo, Source: loc,
			Message: "The document lists " + strconv.Itoa(len(c.doc.Servers)) + " servers; baseUrl is the first. The others are in the variable's description.",
		})
	}
	switch {
	case c.base == "":
		desc = joinSentences(desc, "The API's address.")
		diags = append(diags, importer.Diagnostic{
			Code: CodeNoServer, Severity: importer.SeverityWarning, Source: loc,
			Message: "The document names no server (or only “/”): set the collection's baseUrl variable to the API's address (e.g. https://api.example.com).",
		})
	case !strings.Contains(c.base, "://"):
		diags = append(diags, importer.Diagnostic{
			Code: CodeRelativeServer, Severity: importer.SeverityWarning, Source: loc,
			Message: "The server URL “" + c.base + "” is relative to where the document is served: put the API's scheme and host in front of it in the collection's baseUrl variable.",
		})
	}
	for i := range c.vars {
		if c.vars[i].Name == BaseURLVariable {
			c.vars[i].Value, c.vars[i].Description = u, desc
		}
	}
	return diags
}

// templated returns a server URL with its declared {variables} as
// {{variables}}.
func templated(s openapi.Server) string {
	values := map[string]string{}
	for _, v := range s.Variables {
		if v.Name != BaseURLVariable {
			values[v.Name] = "{{" + v.Name + "}}"
		}
	}
	return s.Expand(values)
}

// trimBase removes the trailing slashes of a base URL, so paths can be
// appended ("/" becomes "").
func trimBase(u string) string {
	return strings.TrimRight(strings.TrimSpace(u), "/")
}

func (c *conv) addVar(v projectfile.Variable) {
	if c.varSeen[v.Name] {
		return
	}
	c.varSeen[v.Name] = true
	c.vars = append(c.vars, v)
}

// operation maps one operation to a request.
func (c *conv) operation(p *openapi.PathItem, op *openapi.Operation) *importer.Request {
	loc := location(op.Pos, "paths."+op.Path+"."+strings.ToLower(op.Method))
	q := &importer.Request{Name: requestName(op), Source: loc}
	h := &projectfile.HTTPRequest{Method: op.Method}
	var diags []importer.Diagnostic
	note := func(sev importer.Severity, code, msg string) {
		diags = append(diags, importer.Diagnostic{Code: code, Severity: sev, Source: loc, Message: msg})
	}

	desc := []string{}
	if op.Deprecated {
		desc = append(desc, "**Deprecated.**")
	}
	desc = append(desc, op.Description, externalDocs(op.ExternalDocs))
	if op.RequestBody != nil {
		desc = append(desc, op.RequestBody.Description)
	}
	h.Description = joinParas(desc...)

	// Base URL: {{baseUrl}}, or the operation's own server.
	base := "{{" + BaseURLVariable + "}}"
	servers := op.Servers
	if servers == nil {
		servers = p.Servers
	}
	if len(servers) > 0 {
		if own := trimBase(servers[0].DefaultURL()); own != c.base {
			base = own
			note(importer.SeverityInfo, CodeServers, "The operation has its own server, “"+own+"”, which its URL uses instead of baseUrl.")
		}
	}
	path := op.Path
	if path == "" {
		path = "/"
	}

	// Auth.
	if op.Security != nil {
		var extra []projectfile.KeyValue
		var ad []importer.Diagnostic
		h.Auth, extra, ad = c.security(op.Security, "the operation")
		h.Headers = append(h.Headers, extra...)
		for i := range ad {
			ad[i].Source = loc
		}
		diags = append(diags, ad...)
	} else {
		h.Auth = projectfile.Auth{Type: projectfile.AuthInherit}
	}
	effective := h.Auth
	if effective.Type == projectfile.AuthInherit {
		effective = c.rootAuth
	}

	ps := c.parameters(op, effective, note)
	h.PathParams = ps.path
	h.Headers = append(ps.headers, h.Headers...)
	h.URL, h.Params = withQuery(base+path, ps.query)
	names := request.PathParamNames(h.URL)
	for _, pp := range ps.path {
		found := false
		for _, n := range names {
			found = found || n == pp.Name
		}
		if !found {
			note(importer.SeverityWarning, CodePathParameter, "The path parameter “"+pp.Name+"” is not a placeholder PostSonde fills in (names may hold letters, digits, _, - and .): edit the URL by hand.")
		}
	}

	// Body.
	if rb := op.RequestBody; rb != nil && len(rb.Content) > 0 {
		var bh []projectfile.KeyValue
		var bd []importer.Diagnostic
		h.Body, bh, bd = c.body(rb, loc)
		h.Headers = append(h.Headers, bh...)
		diags = append(diags, bd...)
	} else {
		h.Body = projectfile.Body{Mode: projectfile.BodyNone}
	}
	if accept := acceptType(op); accept != "" && !hasHeader(h.Headers, "Accept") {
		h.Headers = append(h.Headers, projectfile.KeyValue{Name: "Accept", Value: accept})
	}

	if len(op.Tags) > 1 {
		note(importer.SeverityInfo, CodeTags, "The operation is also tagged "+quoteList(op.Tags[1:])+"; it is in the folder of its first tag.")
	}
	if n := len(op.Callbacks); n > 0 {
		note(importer.SeverityInfo, CodeCallback, "The operation's "+plural(n, "callback", "callbacks")+" ("+strings.Join(op.Callbacks.Names(), ", ")+") "+plural(n, "describes a request", "describe requests")+" the API sends, and "+plural(n, "was", "were")+" not imported.")
	}
	q.File = h
	q.Diagnostics = diags
	return q
}

// requestName is the summary, else the operationId, else "METHOD /path".
func requestName(op *openapi.Operation) string {
	for _, s := range []string{op.Summary, op.OperationID} {
		if s = strings.Join(strings.Fields(s), " "); s != "" {
			if len(s) > 120 {
				s = strings.TrimSpace(truncateUTF8(s, 120)) + "…"
			}
			return s
		}
	}
	return op.Method + " " + op.Path
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

func folderName(n string) string {
	if n = strings.TrimSpace(n); n == "" {
		return "Folder"
	}
	return n
}

// acceptType returns the first media type of the first success response
// (else the default response) that has content, unless it is a wildcard.
func acceptType(op *openapi.Operation) string {
	pick := func(ok func(code string) bool) string {
		for _, r := range op.Responses {
			if r.Value == nil || !ok(r.Name) || len(r.Value.Content) == 0 {
				continue
			}
			mt := r.Value.Content[0].Name
			if strings.Contains(mt, "*") {
				return ""
			}
			return mt
		}
		return ""
	}
	if a := pick(func(code string) bool { return strings.HasPrefix(code, "2") }); a != "" {
		return a
	}
	return pick(func(code string) bool { return code == "default" })
}

func location(p openapi.Pos, path string) importer.Location {
	return importer.Location{Line: p.Line, Column: p.Column, Path: path}
}

func externalDocs(d *openapi.ExternalDocs) string {
	if d == nil || strings.TrimSpace(d.URL) == "" {
		return ""
	}
	if t := strings.TrimSpace(d.Description); t != "" {
		return "[" + t + "](" + d.URL + ")"
	}
	return d.URL
}

// joinParas joins the non-blank texts as paragraphs.
func joinParas(ts ...string) string {
	var out []string
	for _, t := range ts {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n\n")
}

// joinSentences joins two sentences, ending the first with a full stop
// when it has no final punctuation.
func joinSentences(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if !strings.ContainsAny(a[len(a)-1:], ".!?:") {
		a += "."
	}
	return a + " " + b
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func quoteList(xs []string) string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "“" + x + "”"
	}
	return strings.Join(out, ", ")
}

func hasHeader(rows []projectfile.KeyValue, name string) bool {
	for _, kv := range rows {
		if strings.EqualFold(strings.TrimSpace(kv.Name), name) {
			return true
		}
	}
	return false
}
