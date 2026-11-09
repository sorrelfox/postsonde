package openapiimport

import (
	"bytes"
	"encoding/json"
	"mime"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/openapi"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Media type kinds, in order of preference for the request body.
const (
	mtJSON = iota
	mtForm
	mtMultipart
	mtXML
	mtText
	mtYAML
	mtBinary
	mtOther
	mtWildcard
)

func mediaKind(name string) (int, string) {
	mt, _, err := mime.ParseMediaType(name)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(name))
	}
	switch {
	case strings.Contains(mt, "*"):
		return mtWildcard, mt
	case mt == "application/json" || strings.HasSuffix(mt, "+json") || strings.HasSuffix(mt, "/json"):
		return mtJSON, mt
	case mt == "application/x-www-form-urlencoded":
		return mtForm, mt
	case mt == "multipart/form-data" || strings.HasPrefix(mt, "multipart/"):
		return mtMultipart, mt
	case strings.HasSuffix(mt, "/xml") || strings.HasSuffix(mt, "+xml"):
		return mtXML, mt
	case mt == "text/plain":
		return mtText, mt
	case strings.HasSuffix(mt, "/yaml") || strings.HasSuffix(mt, "/x-yaml") || strings.HasSuffix(mt, "+yaml"):
		return mtYAML, mt
	case mt == "application/octet-stream" || strings.HasPrefix(mt, "image/") || strings.HasPrefix(mt, "audio/") ||
		strings.HasPrefix(mt, "video/") || mt == "application/pdf" || mt == "application/zip":
		return mtBinary, mt
	}
	return mtOther, mt
}

// body maps a request body: the preferred media type (JSON, form,
// multipart, XML, text, YAML, binary, others), with its example. It
// returns the headers to add (a Content-Type PostSonde would not send by
// itself).
func (c *conv) body(rb *openapi.RequestBody, loc importer.Location) (projectfile.Body, []projectfile.KeyValue, []importer.Diagnostic) {
	var diags []importer.Diagnostic
	note := func(code, msg string) {
		diags = append(diags, importer.Diagnostic{Code: code, Severity: importer.SeverityInfo, Source: loc, Message: msg})
	}
	best, bestKind := -1, 0
	for i, e := range rb.Content {
		k, _ := mediaKind(e.Name)
		if best < 0 || k < bestKind {
			best, bestKind = i, k
		}
	}
	entry := rb.Content[best]
	mt := entry.Value
	if mt == nil {
		mt = &openapi.MediaType{}
	}
	_, bare := mediaKind(entry.Name)
	if len(rb.Content) > 1 {
		var others []string
		for i, e := range rb.Content {
			if i != best {
				others = append(others, e.Name)
			}
		}
		note(CodeMediaType, "The body is sent as "+entry.Name+"; the operation also accepts "+strings.Join(others, ", ")+".")
	}
	opts := ExampleOptions{Direction: ForRequest, Names: c.names}
	example := func() value {
		v, name, external, notes := MediaTypeExample(mt, opts)
		if name != "" && len(mt.Examples) > 1 {
			note(CodeExample, "The body is the example “"+name+"”, the first of "+itoaN(len(mt.Examples))+" in the document.")
		}
		if external {
			note(CodeExample, "The document's examples of the body are external files, which are not read; the body was generated from the schema.")
		}
		exampleNotes(notes, note)
		if v == nil {
			return nil
		}
		return decodeRaw(v)
	}
	contentType := func(typ string) []projectfile.KeyValue {
		if bare == request.RawMediaType(typ) && !strings.Contains(entry.Name, ";") {
			return nil
		}
		return []projectfile.KeyValue{{Name: "Content-Type", Value: entry.Name}}
	}

	switch bestKind {
	case mtForm:
		return projectfile.Body{Mode: projectfile.BodyURLEncoded, Fields: c.formFields(mt, example())}, nil, diags
	case mtMultipart:
		parts, files := c.parts(mt, example())
		if files > 0 {
			note(CodeFile, "The body has "+plural(files, "a file part", "file parts")+": choose the "+plural(files, "file", "files")+" to send.")
		}
		var h []projectfile.KeyValue
		if bare != "multipart/form-data" {
			h = []projectfile.KeyValue{{Name: "Content-Type", Value: entry.Name}}
			note(CodeMediaType, "PostSonde sends multipart bodies as multipart/form-data; the document asks for "+bare+".")
		}
		return projectfile.Body{Mode: projectfile.BodyMultipart, Parts: parts}, h, diags
	case mtBinary:
		note(CodeFile, "The body is a file: choose the file to send.")
		b := projectfile.Body{Mode: projectfile.BodyFile}
		if bare != "application/octet-stream" {
			b.ContentType = entry.Name
		}
		return b, nil, diags
	case mtXML:
		v := example()
		txt := ""
		if s, ok := stringValue(v); ok {
			txt = s
		} else if v != nil {
			txt = xmlDocument(mt.Schema, v, c.names)
		}
		return projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawXML, Text: txt}, contentType(projectfile.RawXML), diags
	case mtText, mtYAML, mtOther:
		typ := projectfile.RawText
		if bestKind == mtYAML {
			typ = projectfile.RawYAML
		}
		v := example()
		txt := ""
		if s, ok := stringValue(v); ok {
			txt = s
		} else if v != nil {
			txt = string(indent(v))
		}
		if bestKind == mtOther && isBinarySchema(mt.Schema) {
			note(CodeFile, "The body is a file: choose the file to send.")
			return projectfile.Body{Mode: projectfile.BodyFile, ContentType: entry.Name}, nil, diags
		}
		return projectfile.Body{Mode: projectfile.BodyRaw, Type: typ, Text: txt}, contentType(typ), diags
	}
	// JSON, and wildcards sent as JSON.
	v := example()
	txt := ""
	if v != nil {
		txt = string(indent(v))
	}
	h := contentType(projectfile.RawJSON)
	if bestKind == mtWildcard {
		h = nil
	}
	return projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawJSON, Text: txt}, h, diags
}

func exampleNotes(n ExampleNotes, note func(code, msg string)) {
	switch {
	case n.Truncated:
		note(CodeExampleTruncated, "The generated example was cut short at the depth or size limit: complete it by hand if needed.")
	case n.Recursive:
		note(CodeExampleTruncated, "The generated example stops where the schema refers back to itself.")
	}
}

// stringValue returns v's text when it is a JSON string.
func stringValue(v value) (string, bool) {
	raw, ok := v.(json.RawMessage)
	if !ok {
		return "", false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// isBinarySchema reports whether a schema is a file: format binary, or
// (3.1) a contentMediaType or contentEncoding without a text type.
func isBinarySchema(s *openapi.Schema) bool {
	if s == nil {
		return false
	}
	if s.Format == "binary" || s.ContentEncoding == "binary" {
		return true
	}
	if s.ContentMediaType != "" {
		k, _ := mediaKind(s.ContentMediaType)
		return k == mtBinary || k == mtOther
	}
	return false
}

// objectProps returns the property schemas of an object schema (with its
// allOf members folded in).
func (c *conv) objectProps(s *openapi.Schema) []openapi.Entry[*openapi.Schema] {
	if s == nil {
		return nil
	}
	g := newGen(ExampleOptions{})
	for {
		if only := single(s); only != nil && !g.onPath[only] {
			g.onPath[s] = true
			s = only
			continue
		}
		break
	}
	return g.merge(s).props
}

// formFields maps a form body: the example object's members, or the
// schema's properties, as fields. Arrays are one field per item (the
// form style's default), unless the encoding says explode: false.
func (c *conv) formFields(mt *openapi.MediaType, v value) []projectfile.KeyValue {
	var out []projectfile.KeyValue
	o, _ := v.(*object)
	if o == nil {
		return nil
	}
	props := c.objectProps(mt.Schema)
	desc := func(name string) string {
		for _, p := range props {
			if p.Name == name && p.Value != nil {
				return strings.TrimSpace(p.Value.Description)
			}
		}
		return ""
	}
	for i, k := range o.keys {
		explode := true
		if enc, ok := mt.Encoding.Get(k); ok && enc != nil && enc.Explode != nil {
			explode = *enc.Explode
		}
		if arr, ok := o.vals[i].([]value); ok && explode {
			for _, x := range arr {
				out = append(out, projectfile.KeyValue{Name: k, Value: text(x), Description: desc(k)})
			}
			continue
		}
		out = append(out, projectfile.KeyValue{Name: k, Value: scalarList(o.vals[i], ","), Description: desc(k)})
		if _, isObj := o.vals[i].(*object); isObj {
			out[len(out)-1].Value = compactString(o.vals[i])
		}
	}
	return out
}

// parts maps a multipart body: a file part for each binary property (or
// array of them), a text part for the others (objects and arrays as JSON).
// It returns the parts and the number of file parts.
func (c *conv) parts(mt *openapi.MediaType, v value) ([]projectfile.Part, int) {
	var out []projectfile.Part
	files := 0
	o, _ := v.(*object)
	props := c.objectProps(mt.Schema)
	seen := map[string]bool{}
	add := func(name string, ps *openapi.Schema, val value, has bool) {
		p := projectfile.Part{Name: name}
		if ps != nil {
			p.Description = strings.TrimSpace(ps.Description)
		}
		if enc, ok := mt.Encoding.Get(name); ok && enc != nil && enc.ContentType != "" {
			ct := strings.TrimSpace(strings.Split(enc.ContentType, ",")[0])
			if !strings.Contains(ct, "*") {
				p.ContentType = ct
			}
		}
		file := isBinarySchema(ps) || ps != nil && ps.Items != nil && isBinarySchema(ps.Items)
		switch {
		case file:
			files++
		case has:
			switch val.(type) {
			case *object, []value:
				p.Value = compactString(val)
				if p.ContentType == "" {
					p.ContentType = "application/json"
				}
			default:
				p.Value = text(val)
			}
		}
		out = append(out, p)
	}
	if o != nil {
		for i, k := range o.keys {
			seen[k] = true
			var ps *openapi.Schema
			for _, p := range props {
				if p.Name == k {
					ps = p.Value
				}
			}
			add(k, ps, o.vals[i], true)
		}
	}
	// Binary properties are left out of generated examples' values but
	// still need their part.
	for _, p := range props {
		if seen[p.Name] || p.Value == nil || p.Value.ReadOnly {
			continue
		}
		if isBinarySchema(p.Value) || p.Value.Items != nil && isBinarySchema(p.Value.Items) {
			add(p.Name, p.Value, nil, false)
		}
	}
	return out, files
}

func itoaN(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
