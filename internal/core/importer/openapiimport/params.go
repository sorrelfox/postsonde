package openapiimport

import (
	"encoding/json"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/openapi"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// params are an operation's parameters as request rows.
type params struct {
	path, query, headers []projectfile.KeyValue
}

// parameters maps the operation's parameters. Required query, header and
// cookie parameters are enabled rows; optional ones are disabled rows, so
// the request sends only what it must and the rest is one click away.
// Values come from the parameter's or its schema's example, default or
// enum; a uuid or date-time without one gets a dynamic value.
func (c *conv) parameters(op *openapi.Operation, auth projectfile.Auth, note func(importer.Severity, string, string)) params {
	var out params
	var cookies []string
	cookieRequired := false
	var ignored, byAuth []string
	for _, p := range op.Parameters {
		if p == nil || p.Name == "" {
			continue
		}
		v := paramValue(p)
		desc := strings.TrimSpace(p.Description)
		if p.Deprecated {
			desc = joinSentences("Deprecated.", desc)
		}
		switch p.In {
		case openapi.InPath:
			out.path = append(out.path, projectfile.KeyValue{Name: p.Name, Value: serializePath(p, v), Description: desc})
		case openapi.InQuery:
			if auth.Type == projectfile.AuthAPIKey && auth.In == projectfile.APIKeyInQuery && auth.Key == p.Name {
				byAuth = append(byAuth, p.Name)
				continue
			}
			for _, kv := range serializeQuery(p, v) {
				out.query = append(out.query, projectfile.KeyValue{Name: kv[0], Value: kv[1], Description: desc, Disabled: !p.Required})
			}
		case openapi.InHeader:
			switch strings.ToLower(p.Name) {
			case "accept", "content-type", "authorization":
				ignored = append(ignored, p.Name)
				continue
			}
			if auth.Type == projectfile.AuthAPIKey && auth.In != projectfile.APIKeyInQuery && strings.EqualFold(auth.Key, p.Name) {
				byAuth = append(byAuth, p.Name)
				continue
			}
			out.headers = append(out.headers, projectfile.KeyValue{Name: p.Name, Value: serializeSimple(p, v), Description: desc, Disabled: !p.Required})
		case openapi.InCookie:
			cookies = append(cookies, p.Name+"="+scalarList(v, ","))
			cookieRequired = cookieRequired || p.Required
		}
	}
	if len(cookies) > 0 {
		out.headers = append(out.headers, projectfile.KeyValue{
			Name: "Cookie", Value: strings.Join(cookies, "; "), Disabled: !cookieRequired,
			Description: "Cookie parameters of the operation.",
		})
		note(importer.SeverityInfo, CodeCookieParameter, "The cookie "+plural(len(cookies), "parameter", "parameters")+" became a Cookie header.")
	}
	if n := len(ignored); n > 0 {
		note(importer.SeverityInfo, CodeParameterIgnored, "The header "+plural(n, "parameter", "parameters")+" "+quoteList(ignored)+" "+plural(n, "was", "were")+" left out, as the specification says: the request's body, response type and auth set "+plural(n, "it", "them")+".")
	}
	if n := len(byAuth); n > 0 {
		note(importer.SeverityInfo, CodeParameterIgnored, "The "+plural(n, "parameter", "parameters")+" "+quoteList(byAuth)+" "+plural(n, "was", "were")+" left out: the auth sends "+plural(n, "it", "them")+".")
	}
	return out
}

// paramValue returns a parameter's example value, or nil.
func paramValue(p *openapi.Parameter) value {
	if len(p.Example) > 0 {
		return decodeRaw(p.Example)
	}
	for _, e := range p.Examples {
		if e.Value != nil && len(e.Value.Value) > 0 {
			return decodeRaw(e.Value.Value)
		}
	}
	for _, mt := range p.Content {
		if mt.Value == nil {
			continue
		}
		if v, _, _, _ := MediaTypeExample(mt.Value, ExampleOptions{MaxDepth: 4, MaxValues: 100}); v != nil {
			// A parameter with content is sent as that media type's text.
			q, _ := marshalString(compactString(decodeRaw(v)))
			return json.RawMessage(q)
		}
		break
	}
	s := p.Schema
	if s == nil || s.Bool != nil {
		return nil
	}
	if lit := literal(s); lit != nil {
		return decodeRaw(lit)
	}
	if s.Items != nil && s.Items.Bool == nil {
		if lit := literal(s.Items); lit != nil {
			return []value{decodeRaw(lit)}
		}
	}
	switch s.Format {
	case "uuid":
		return json.RawMessage(`"{{$uuid}}"`)
	case "date-time":
		return json.RawMessage(`"{{$isoTimestamp}}"`)
	}
	return nil
}

// scalarList returns v's scalars joined by sep: an array's items, an
// object's keys and values in turn, or v itself.
func scalarList(v value, sep string) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []value:
		out := make([]string, len(t))
		for i, x := range t {
			out[i] = text(x)
		}
		return strings.Join(out, sep)
	case *object:
		var out []string
		for i, k := range t.keys {
			out = append(out, k, text(t.vals[i]))
		}
		return strings.Join(out, sep)
	}
	return text(v)
}

// pairs returns an object's members as k=v joined by sep.
func pairs(o *object, sep string) string {
	out := make([]string, len(o.keys))
	for i, k := range o.keys {
		out[i] = k + "=" + text(o.vals[i])
	}
	return strings.Join(out, sep)
}

// serializeSimple serializes a header (or simple path) value.
func serializeSimple(p *openapi.Parameter, v value) string {
	if o, ok := v.(*object); ok && p.EffectiveExplode() {
		return pairs(o, ",")
	}
	return scalarList(v, ",")
}

// serializePath serializes a path parameter's value by its style
// (simple, label, matrix); the value replaces the {name} placeholder.
func serializePath(p *openapi.Parameter, v value) string {
	if v == nil {
		return ""
	}
	explode := p.EffectiveExplode()
	switch p.EffectiveStyle() {
	case "label":
		if o, ok := v.(*object); ok && explode {
			return "." + pairs(o, ".")
		}
		if explode {
			return "." + scalarList(v, ".")
		}
		return "." + scalarList(v, ",")
	case "matrix":
		switch t := v.(type) {
		case []value:
			if explode {
				var b strings.Builder
				for _, x := range t {
					b.WriteString(";" + p.Name + "=" + text(x))
				}
				return b.String()
			}
		case *object:
			if explode {
				return ";" + pairs(t, ";")
			}
		}
		return ";" + p.Name + "=" + scalarList(v, ",")
	}
	return serializeSimple(p, v)
}

// serializeQuery returns the query entries of a parameter by its style
// (form, spaceDelimited, pipeDelimited, deepObject): one entry, or one per
// item or member when exploded. A parameter without a value is one entry
// with an empty value.
func serializeQuery(p *openapi.Parameter, v value) [][2]string {
	explode := p.EffectiveExplode()
	switch t := v.(type) {
	case []value:
		sep := ","
		switch p.EffectiveStyle() {
		case "spaceDelimited":
			sep = " "
		case "pipeDelimited":
			sep = "|"
		}
		if explode {
			var out [][2]string
			for _, x := range t {
				out = append(out, [2]string{p.Name, text(x)})
			}
			if len(out) == 0 {
				out = append(out, [2]string{p.Name, ""})
			}
			return out
		}
		return [][2]string{{p.Name, scalarList(t, sep)}}
	case *object:
		switch {
		case p.EffectiveStyle() == "deepObject":
			var out [][2]string
			for i, k := range t.keys {
				out = append(out, [2]string{p.Name + "[" + k + "]", text(t.vals[i])})
			}
			return out
		case explode:
			var out [][2]string
			for i, k := range t.keys {
				out = append(out, [2]string{k, text(t.vals[i])})
			}
			return out
		}
		return [][2]string{{p.Name, scalarList(t, ",")}}
	}
	return [][2]string{{p.Name, text(v)}}
}

// withQuery returns the URL with the enabled rows added to its query, as
// the editor keeps them (the URL holds the enabled rows), and the rows.
func withQuery(u string, rows []projectfile.KeyValue) (string, []projectfile.KeyValue) {
	var add []string
	for _, r := range rows {
		if r.Disabled {
			continue
		}
		entry := encodeQuery(r.Name, "&#=")
		if r.Value != "" {
			entry += "=" + encodeQuery(r.Value, "&#")
		}
		add = append(add, entry)
	}
	if len(add) == 0 {
		return u, rows
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + strings.Join(add, "&"), rows
}

// encodeQuery percent-encodes chars outside {{variables}}, as the editor
// does when it writes a row into the URL.
func encodeQuery(s, chars string) string {
	if !strings.ContainsAny(s, chars) {
		return s
	}
	var b strings.Builder
	for _, seg := range template.Parse(s) {
		raw := s[seg.Start:seg.End]
		if seg.Kind == template.Var {
			b.WriteString(raw)
			continue
		}
		for i := 0; i < len(raw); i++ {
			switch c := raw[i]; {
			case c == '&' && strings.IndexByte(chars, c) >= 0:
				b.WriteString("%26")
			case c == '#' && strings.IndexByte(chars, c) >= 0:
				b.WriteString("%23")
			case c == '=' && strings.IndexByte(chars, c) >= 0:
				b.WriteString("%3D")
			default:
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}
