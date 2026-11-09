package postman

import (
	"mime"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// request maps an item with a request: an http request, or a graphql
// request for a body in graphql mode.
func (c *collector) request(it *yaml.Node, loc importer.Location, inherited behavior) *importer.Request {
	t := &texts{}
	q := &importer.Request{Name: strings.TrimSpace(sget(it, "name")), Source: loc}
	rn := get(it, "request")
	if q.Name == "" {
		q.Name = "Request"
	}
	// v2.0 and v2.1 allow the request to be only its URL.
	if !isMap(rn) {
		rn = &yaml.Node{Kind: yaml.MappingNode, Tag: yaml.TagMap, Line: rn.Line, Column: rn.Column,
			Pairs: []yaml.Pair{{Key: &yaml.Node{Kind: yaml.ScalarNode, Tag: yaml.TagStr, Value: "url"}, Value: rn}}}
	}
	desc := description(get(rn, "description"))
	if desc == "" {
		desc = description(get(it, "description"))
	}
	desc, diags := withScripts(desc, get(it, "event"), "request", loc)
	examples, d := c.examples(get(it, "response"), loc)
	diags = append(diags, d...)
	if len(items(get(it, "variable"))) > 0 {
		diags = append(diags, importer.Diagnostic{
			Code: CodeVariablesSkipped, Severity: importer.SeverityWarning, Source: loc,
			Message: "The request's own variables were not imported: PostSonde requests have no variables. Add them to its folder.",
		})
	}

	method := strings.ToUpper(strings.TrimSpace(sget(rn, "method")))
	if method == "" {
		method = "GET"
	}
	u, params, pathRows := c.url(get(rn, "url"), t)
	headers := headerRows(get(rn, "header"), t)
	auth, d := c.auth(get(rn, "auth"), t, loc)
	diags = append(diags, d...)
	b := inherited.with(get(it, "protocolProfileBehavior"))
	settings, lost := b.settings()
	for _, k := range []string{"proxy", "certificate"} {
		if isMap(get(rn, k)) {
			lost = append(lost, k)
		}
	}
	if len(lost) > 0 {
		slices.Sort(lost)
		diags = append(diags, importer.Diagnostic{
			Code: CodeSettingSkipped, Severity: importer.SeverityInfo, Source: loc,
			Message: "Postman settings PostSonde does not have are not imported: " + strings.Join(lost, ", ") + ".",
		})
	}

	body := get(rn, "body")
	if sget(body, "mode") == "graphql" && !isTrue(get(body, "disabled")) {
		g := &projectfile.GraphQLRequest{Description: desc, URL: u, Headers: headers, Auth: auth, Settings: settings, ResponseExamples: examples}
		switch method {
		case "POST":
		case "GET":
			g.Method = "GET"
		default:
			diags = append(diags, importer.Diagnostic{
				Code: CodeGraphQLMethod, Severity: importer.SeverityWarning, Source: loc,
				Message: "The GraphQL request used the " + clip(method) + " method; PostSonde sends GraphQL requests with POST or GET, so it is sent with POST.",
			})
		}
		gq := get(body, "graphql")
		g.Query = t.conv(sget(gq, "query"), "the query")
		if v := strings.TrimSpace(sget(gq, "variables")); v != "" && v != "{}" {
			g.Variables = t.conv(v, "the variables")
		}
		q.File = g
	} else {
		h := &projectfile.HTTPRequest{Description: desc, Method: method, URL: u, Params: params, PathParams: pathRows, Headers: headers, Auth: auth, Settings: settings,
			ResponseExamples: examples}
		h.Body, h.Headers, d = mapBody(body, h.Headers, t, loc)
		diags = append(diags, d...)
		oauth1BodyIn(h)
		q.File = h
	}
	q.Diagnostics = append(diags, t.diagnostics(loc)...)
	return q
}

// url returns a request's URL as PostSonde keeps it (the enabled query
// rows in its query string), the query table (with disabled rows) and the
// path parameter rows. u is a string or a URL object.
func (c *collector) url(u *yaml.Node, t *texts) (string, []projectfile.KeyValue, []projectfile.KeyValue) {
	if !isMap(u) {
		raw := t.conv(strings.TrimSpace(str(u)), "the URL")
		return raw, queryRows(raw), pathParams(raw, nil, t)
	}
	raw := strings.TrimSpace(sget(u, "raw"))
	if raw == "" {
		raw = buildURL(u)
	}
	raw = t.conv(raw, "the URL")
	query := get(u, "query")
	if query == nil {
		return raw, queryRows(raw), pathParams(raw, get(u, "variable"), t)
	}
	// The query list is the table, disabled rows included; the URL gets
	// the enabled ones, in place of raw's own query.
	base, frag := raw, ""
	if i := strings.IndexByte(base, '#'); i >= 0 {
		base, frag = base[:i], base[i:]
	}
	if i := strings.IndexByte(base, '?'); i >= 0 {
		base = base[:i]
	}
	var table []projectfile.KeyValue
	var entries []string
	for _, r := range items(query) {
		key := t.conv(sget(r, "key"), "a query parameter")
		vn := get(r, "value")
		value := t.conv(str(vn), "a query parameter")
		if key == "" && value == "" {
			continue
		}
		kv := projectfile.KeyValue{Name: key, Value: value, Description: description(get(r, "description")), Disabled: isTrue(get(r, "disabled"))}
		table = append(table, kv)
		if kv.Disabled {
			continue
		}
		entry := key
		if vn != nil && vn.Tag != yaml.TagNull {
			entry += "=" + value
		}
		entries = append(entries, entry)
	}
	if len(entries) > 0 {
		base += "?" + strings.Join(entries, "&")
	}
	full := base + frag
	return full, table, pathParams(full, get(u, "variable"), t)
}

// buildURL writes a URL object without raw from its parts.
func buildURL(u *yaml.Node) string {
	var b strings.Builder
	if p := sget(u, "protocol"); p != "" {
		b.WriteString(strings.TrimSuffix(p, "://") + "://")
	}
	b.WriteString(strings.Join(parts(get(u, "host")), "."))
	if p := sget(u, "port"); p != "" {
		b.WriteString(":" + p)
	}
	if segs := parts(get(u, "path")); len(segs) > 0 {
		b.WriteString("/" + strings.Join(segs, "/"))
	}
	var q []string
	for _, r := range items(get(u, "query")) {
		if isTrue(get(r, "disabled")) {
			continue
		}
		e := sget(r, "key")
		if vn := get(r, "value"); vn != nil && vn.Tag != yaml.TagNull {
			e += "=" + str(vn)
		}
		q = append(q, e)
	}
	if len(q) > 0 {
		b.WriteString("?" + strings.Join(q, "&"))
	}
	if h := sget(u, "hash"); h != "" {
		b.WriteString("#" + h)
	}
	return b.String()
}

// parts returns a host or path: a string, or a list of strings (or, in
// v2.0, of {type, value} objects).
func parts(n *yaml.Node) []string {
	if n == nil {
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		s := str(n)
		if s == "" {
			return nil
		}
		return []string{strings.TrimPrefix(s, "/")}
	}
	var out []string
	for _, it := range n.Items {
		if isMap(it) {
			out = append(out, sget(it, "value"))
		} else {
			out = append(out, str(it))
		}
	}
	return out
}

// queryRows returns the query entries of u as rows, as written.
func queryRows(u string) []projectfile.KeyValue {
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	_, q, ok := strings.Cut(u, "?")
	if !ok {
		return nil
	}
	var out []projectfile.KeyValue
	for entry := range strings.SplitSeq(q, "&") {
		if entry == "" {
			continue
		}
		k, v, _ := strings.Cut(entry, "=")
		out = append(out, projectfile.KeyValue{Name: k, Value: v})
	}
	return out
}

// pathParams returns the path parameter rows: Postman's values for the
// URL's :name placeholders (url.variable), and an empty row for each other
// placeholder.
func pathParams(u string, vars *yaml.Node, t *texts) []projectfile.KeyValue {
	var out []projectfile.KeyValue
	for _, v := range items(vars) {
		name := sget(v, "key")
		if name == "" {
			name = sget(v, "id")
		}
		if name == "" {
			continue
		}
		out = append(out, projectfile.KeyValue{Name: name, Value: t.conv(str(get(v, "value")), "a path parameter"), Description: description(get(v, "description"))})
	}
	for _, name := range request.PathParamNames(u) {
		if !slices.ContainsFunc(out, func(kv projectfile.KeyValue) bool { return kv.Name == name }) {
			out = append(out, projectfile.KeyValue{Name: name})
		}
	}
	return out
}

// headerRows maps a header list, or (v2.0) the headers as one text of
// "Name: value" lines.
func headerRows(n *yaml.Node, t *texts) []projectfile.KeyValue {
	var out []projectfile.KeyValue
	if n != nil && n.Kind == yaml.ScalarNode {
		for line := range strings.SplitSeq(str(n), "\n") {
			line = strings.TrimSpace(line)
			disabled := strings.HasPrefix(line, "//")
			line = strings.TrimSpace(strings.TrimPrefix(line, "//"))
			name, value, ok := strings.Cut(line, ":")
			if !ok || strings.TrimSpace(name) == "" {
				continue
			}
			out = append(out, projectfile.KeyValue{
				Name: t.conv(strings.TrimSpace(name), "a header"), Value: t.conv(strings.TrimSpace(value), "a header"), Disabled: disabled,
			})
		}
		return out
	}
	return rows(n, t, "a header")
}

// rows maps key/value rows (headers, form fields). Rows without a key and
// a value are left out.
func rows(n *yaml.Node, t *texts, where string) []projectfile.KeyValue {
	var out []projectfile.KeyValue
	for _, it := range items(n) {
		key, value := sget(it, "key"), str(get(it, "value"))
		if key == "" && value == "" {
			continue
		}
		out = append(out, projectfile.KeyValue{
			Name:        t.conv(key, where),
			Value:       t.conv(value, where),
			Description: description(get(it, "description")),
			Disabled:    isTrue(get(it, "disabled")),
		})
	}
	return out
}

// rawLanguages are the raw body languages Postman offers, with the
// PostSonde raw type and the Content-Type Postman sends for them.
var rawLanguages = map[string]struct{ typ, contentType string }{
	"json":       {projectfile.RawJSON, "application/json"},
	"xml":        {projectfile.RawXML, "application/xml"},
	"html":       {projectfile.RawHTML, "text/html"},
	"text":       {projectfile.RawText, "text/plain"},
	"javascript": {projectfile.RawText, "application/javascript"},
}

// mapBody maps an HTTP request's body. A Content-Type Postman would send
// and PostSonde would not is added to the headers.
func mapBody(n *yaml.Node, headers []projectfile.KeyValue, t *texts, loc importer.Location) (projectfile.Body, []projectfile.KeyValue, []importer.Diagnostic) {
	none := projectfile.Body{Mode: projectfile.BodyNone}
	mode := sget(n, "mode")
	if !isMap(n) || mode == "" {
		return none, headers, nil
	}
	if isTrue(get(n, "disabled")) {
		return none, headers, []importer.Diagnostic{{
			Code: CodeBodySkipped, Severity: importer.SeverityInfo, Source: loc,
			Message: "The body was turned off in Postman and is not imported.",
		}}
	}
	switch mode {
	case "raw":
		text := sget(n, "raw")
		if text == "" {
			return none, headers, nil
		}
		lang := strings.ToLower(sget(path(n, "options", "raw"), "language"))
		typ, ct := "", ""
		if l, ok := rawLanguages[lang]; ok {
			typ, ct = l.typ, l.contentType
		} else {
			typ = typeOf(headerValue(headers, "Content-Type"))
		}
		if ct != "" && ct != request.RawMediaType(typ) && !hasHeader(headers, "Content-Type") {
			headers = append(headers, projectfile.KeyValue{Name: "Content-Type", Value: ct})
		}
		return projectfile.Body{Mode: projectfile.BodyRaw, Type: typ, Text: t.conv(text, "the body")}, headers, nil
	case "urlencoded":
		return projectfile.Body{Mode: projectfile.BodyURLEncoded, Fields: rows(get(n, "urlencoded"), t, "the form")}, headers, nil
	case "formdata":
		b := projectfile.Body{Mode: projectfile.BodyMultipart}
		files := false
		for _, it := range items(get(n, "formdata")) {
			p := projectfile.Part{
				Name:        t.conv(sget(it, "key"), "the form"),
				ContentType: sget(it, "contentType"),
				Description: description(get(it, "description")),
				Disabled:    isTrue(get(it, "disabled")),
			}
			if sget(it, "type") != "file" {
				p.Value = t.conv(str(get(it, "value")), "the form")
				if p.Name != "" || p.Value != "" {
					b.Parts = append(b.Parts, p)
				}
				continue
			}
			// src is a path, a list of paths (one part each), or null.
			srcs := []string{str(get(it, "src"))}
			if s := get(it, "src"); s != nil && s.Kind == yaml.SequenceNode {
				srcs = srcs[:0]
				for _, x := range s.Items {
					srcs = append(srcs, str(x))
				}
				if len(srcs) == 0 {
					srcs = []string{""}
				}
			}
			for _, src := range srcs {
				p.File = src
				files = true
				b.Parts = append(b.Parts, p)
			}
		}
		var diags []importer.Diagnostic
		if files {
			diags = append(diags, filePathDiag(loc))
		}
		return b, headers, diags
	case "file":
		src := sget(get(n, "file"), "src")
		if src == "" {
			if content := sget(get(n, "file"), "content"); content != "" {
				return projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawText, Text: t.conv(content, "the body")}, headers, nil
			}
			return none, headers, nil
		}
		return projectfile.Body{Mode: projectfile.BodyFile, Path: src}, headers, []importer.Diagnostic{filePathDiag(loc)}
	}
	return none, headers, []importer.Diagnostic{{
		Code: CodeBodySkipped, Severity: importer.SeverityWarning, Source: loc,
		Message: "The body's mode “" + clip(mode) + "” is not one PostSonde knows; the body is not imported.",
	}}
}

// typeOf returns the raw type of a Content-Type header's media type.
func typeOf(ct string) string {
	mt := strings.ToLower(strings.TrimSpace(ct))
	if parsed, _, err := mime.ParseMediaType(mt); err == nil {
		mt = parsed
	}
	switch {
	case strings.Contains(mt, "json"):
		return projectfile.RawJSON
	case strings.Contains(mt, "xml"):
		return projectfile.RawXML
	case strings.Contains(mt, "html"):
		return projectfile.RawHTML
	case strings.Contains(mt, "yaml"):
		return projectfile.RawYAML
	}
	return projectfile.RawText
}

func headerValue(rows []projectfile.KeyValue, name string) string {
	for _, kv := range rows {
		if !kv.Disabled && strings.EqualFold(strings.TrimSpace(kv.Name), name) {
			return kv.Value
		}
	}
	return ""
}

func hasHeader(rows []projectfile.KeyValue, name string) bool {
	return slices.ContainsFunc(rows, func(kv projectfile.KeyValue) bool {
		return !kv.Disabled && strings.EqualFold(strings.TrimSpace(kv.Name), name)
	})
}

func filePathDiag(loc importer.Location) importer.Diagnostic {
	return importer.Diagnostic{
		Code: CodeFilePath, Severity: importer.SeverityInfo, Source: loc,
		Message: "The body sends a file by its path on the machine the export was made on; check that the path exists here.",
	}
}
