package openapi

import (
	"encoding/json"
	"fmt"
	"strings"
)

// dumper renders a Document as indented text for golden files and for
// comparing the JSON and YAML forms of a document. An object met a second
// time (a resolved $ref, a recursive schema) is printed as "see <pointer>".
type dumper struct {
	b       strings.Builder
	withPos bool
	seen    map[any]bool
}

func dump(d *Document, withPos bool) string {
	w := &dumper{withPos: withPos, seen: map[any]bool{}}
	w.document(d)
	return w.b.String()
}

func (w *dumper) line(depth int, format string, args ...any) {
	w.b.WriteString(strings.Repeat("  ", depth))
	fmt.Fprintf(&w.b, format, args...)
	w.b.WriteByte('\n')
}

func (w *dumper) pos(p Pos) string {
	if w.withPos {
		return fmt.Sprintf(" @%d:%d %s", p.Line, p.Column, p.Pointer)
	}
	return " @" + p.Pointer
}

// once reports whether obj is printed for the first time; otherwise it
// prints a "see" line.
func (w *dumper) once(depth int, label string, obj any, p Pos) bool {
	if w.seen[obj] {
		w.line(depth, "%s see %s", label, p.Pointer)
		return false
	}
	w.seen[obj] = true
	return true
}

func raw(m json.RawMessage) string {
	if m == nil {
		return "<nil>"
	}
	return string(m)
}

func (w *dumper) opt(depth int, name, v string) {
	if v != "" {
		w.line(depth, "%s: %q", name, v)
	}
}

func (w *dumper) rawOpt(depth int, name string, v json.RawMessage) {
	if v != nil {
		w.line(depth, "%s: %s", name, v)
	}
}

func (w *dumper) ext(depth int, e Extensions) {
	for _, x := range e {
		w.line(depth, "%s: %s", x.Name, raw(x.Value))
	}
}

func (w *dumper) document(d *Document) {
	w.line(0, "openapi %s (3.%d) dialect=%q", d.OpenAPI, d.Minor, d.JSONSchemaDialect)
	w.opt(0, "swagger", d.Swagger)
	in := d.Info
	w.line(0, "info %q version %q", in.Title, in.Version)
	w.opt(1, "summary", in.Summary)
	w.opt(1, "description", in.Description)
	w.opt(1, "termsOfService", in.TermsOfService)
	if in.Contact != nil {
		w.line(1, "contact %+v", *in.Contact)
	}
	if in.License != nil {
		w.line(1, "license %+v", *in.License)
	}
	w.ext(1, in.Extensions)
	w.servers(0, d.Servers)
	for _, r := range d.Security {
		w.line(0, "security %s", secReq(r))
	}
	for _, t := range d.Tags {
		w.line(0, "tag %q %q%s", t.Name, t.Description, w.pos(t.Pos))
	}
	if d.ExternalDocs != nil {
		w.line(0, "externalDocs %+v", *d.ExternalDocs)
	}
	w.ext(0, d.Extensions)
	c := d.Components
	for _, e := range c.Schemas {
		w.schema(0, "components.schemas."+e.Name, e.Value)
	}
	for _, e := range c.Parameters {
		w.parameter(0, "components.parameters."+e.Name, e.Value)
	}
	for _, e := range c.RequestBodies {
		w.requestBody(0, "components.requestBodies."+e.Name, e.Value)
	}
	for _, e := range c.Responses {
		w.response(0, "components.responses."+e.Name, e.Value)
	}
	for _, e := range c.Headers {
		w.header(0, "components.headers."+e.Name, e.Value)
	}
	for _, e := range c.Examples {
		w.example(0, "components.examples."+e.Name, e.Value)
	}
	for _, e := range c.Callbacks {
		w.callback(0, "components.callbacks."+e.Name, e.Value)
	}
	for _, e := range c.PathItems {
		w.pathItem(0, "components.pathItems."+e.Name, e.Value)
	}
	for _, e := range c.SecuritySchemes {
		s := e.Value
		w.line(0, "components.securitySchemes.%s type=%s name=%q in=%q scheme=%q bearerFormat=%q oidc=%q%s",
			e.Name, s.Type, s.Name, s.In, s.Scheme, s.BearerFormat, s.OpenIDConnectURL, w.pos(s.Pos))
		if s.Flows != nil {
			for _, f := range []struct {
				n string
				f *OAuthFlow
			}{{"implicit", s.Flows.Implicit}, {"password", s.Flows.Password}, {"clientCredentials", s.Flows.ClientCredentials}, {"authorizationCode", s.Flows.AuthorizationCode}} {
				if f.f != nil {
					w.line(1, "flow %s auth=%q token=%q refresh=%q scopes=%v", f.n, f.f.AuthorizationURL, f.f.TokenURL, f.f.RefreshURL, f.f.Scopes)
				}
			}
		}
	}
	w.ext(0, c.Extensions)
	for _, p := range d.Paths {
		w.pathItem(0, "path "+p.Path, p)
	}
	for _, e := range d.Webhooks {
		w.pathItem(0, "webhook "+e.Name, e.Value)
	}
}

func secReq(r SecurityRequirement) string {
	if len(r) == 0 {
		return "{}"
	}
	var parts []string
	for _, e := range r {
		parts = append(parts, e.Name+"["+strings.Join(e.Value, ",")+"]")
	}
	return strings.Join(parts, " ")
}

func (w *dumper) servers(depth int, ss []Server) {
	for _, s := range ss {
		w.line(depth, "server %q %q -> %q%s", s.URL, s.Description, s.DefaultURL(), w.pos(s.Pos))
		for _, v := range s.Variables {
			w.line(depth+1, "var %s default=%q enum=%q %q", v.Name, v.Value.Default, v.Value.Enum, v.Value.Description)
		}
	}
}

func (w *dumper) pathItem(depth int, label string, p *PathItem) {
	if p == nil {
		w.line(depth, "%s <nil>", label)
		return
	}
	if !w.once(depth, label, p, p.Pos) {
		return
	}
	w.line(depth, "%s%s", label, w.pos(p.Pos))
	w.opt(depth+1, "summary", p.Summary)
	w.opt(depth+1, "description", p.Description)
	if p.Servers != nil {
		w.servers(depth+1, p.Servers)
	}
	for _, prm := range p.Parameters {
		w.parameter(depth+1, "param", prm)
	}
	for _, op := range p.Operations {
		w.operation(depth+1, op)
	}
	w.ext(depth+1, p.Extensions)
}

func (w *dumper) operation(depth int, op *Operation) {
	w.line(depth, "%s %s id=%q tags=%q deprecated=%v%s", op.Method, op.Path, op.OperationID, op.Tags, op.Deprecated, w.pos(op.Pos))
	w.opt(depth+1, "summary", op.Summary)
	w.opt(depth+1, "description", op.Description)
	if op.Security != nil {
		w.line(depth+1, "security %d", len(op.Security))
		for _, r := range op.Security {
			w.line(depth+2, "%s", secReq(r))
		}
	}
	if op.Servers != nil {
		w.servers(depth+1, op.Servers)
	}
	for _, p := range op.Parameters {
		w.parameter(depth+1, "param", p)
	}
	if op.RequestBody != nil {
		w.requestBody(depth+1, "requestBody", op.RequestBody)
	}
	for _, r := range op.Responses {
		w.response(depth+1, "response "+r.Name, r.Value)
	}
	for _, c := range op.Callbacks {
		w.callback(depth+1, "callback "+c.Name, c.Value)
	}
	w.ext(depth+1, op.Extensions)
}

func (w *dumper) callback(depth int, label string, c *Callback) {
	if c == nil || !w.once(depth, label, c, c.Pos) {
		return
	}
	w.line(depth, "%s%s", label, w.pos(c.Pos))
	for _, p := range c.Paths {
		w.pathItem(depth+1, "expression "+p.Path, p)
	}
}

func (w *dumper) parameter(depth int, label string, p *Parameter) {
	if p == nil {
		w.line(depth, "%s <nil>", label)
		return
	}
	if !w.once(depth, label+" "+p.In+" "+p.Name, p, p.Pos) {
		return
	}
	explode := "-"
	if p.Explode != nil {
		explode = fmt.Sprint(*p.Explode)
	}
	w.line(depth, "%s %s %s required=%v style=%s(%s) explode=%s(%v)%s", label, p.In, p.Name, p.Required,
		p.Style, p.EffectiveStyle(), explode, p.EffectiveExplode(), w.pos(p.Pos))
	w.opt(depth+1, "description", p.Description)
	w.rawOpt(depth+1, "example", p.Example)
	if p.Schema != nil {
		w.schema(depth+1, "schema", p.Schema)
	}
	w.examples(depth+1, p.Examples)
	w.content(depth+1, p.Content)
}

func (w *dumper) requestBody(depth int, label string, r *RequestBody) {
	if !w.once(depth, label, r, r.Pos) {
		return
	}
	w.line(depth, "%s required=%v%s", label, r.Required, w.pos(r.Pos))
	w.opt(depth+1, "description", r.Description)
	w.content(depth+1, r.Content)
}

func (w *dumper) response(depth int, label string, r *Response) {
	if r == nil {
		w.line(depth, "%s <nil>", label)
		return
	}
	if !w.once(depth, label, r, r.Pos) {
		return
	}
	w.line(depth, "%s %q%s", label, r.Description, w.pos(r.Pos))
	for _, h := range r.Headers {
		w.header(depth+1, "header "+h.Name, h.Value)
	}
	w.content(depth+1, r.Content)
	for _, l := range r.Links {
		w.line(depth+1, "link %s %s", l.Name, raw(l.Value))
	}
}

func (w *dumper) header(depth int, label string, h *Header) {
	if !w.once(depth, label, h, h.Pos) {
		return
	}
	w.line(depth, "%s required=%v %q%s", label, h.Required, h.Description, w.pos(h.Pos))
	if h.Schema != nil {
		w.schema(depth+1, "schema", h.Schema)
	}
	w.rawOpt(depth+1, "example", h.Example)
	w.examples(depth+1, h.Examples)
	w.content(depth+1, h.Content)
}

func (w *dumper) examples(depth int, es Entries[*Example]) {
	for _, e := range es {
		w.example(depth, "example "+e.Name, e.Value)
	}
}

func (w *dumper) example(depth int, label string, e *Example) {
	if !w.once(depth, label, e, e.Pos) {
		return
	}
	w.line(depth, "%s summary=%q value=%s external=%q%s", label, e.Summary, raw(e.Value), e.ExternalValue, w.pos(e.Pos))
}

func (w *dumper) content(depth int, c Entries[*MediaType]) {
	for _, e := range c {
		mt := e.Value
		w.line(depth, "content %s%s", e.Name, w.pos(mt.Pos))
		if mt.Schema != nil {
			w.schema(depth+1, "schema", mt.Schema)
		}
		w.rawOpt(depth+1, "example", mt.Example)
		w.examples(depth+1, mt.Examples)
		for _, en := range mt.Encoding {
			x := "-"
			if en.Value.Explode != nil {
				x = fmt.Sprint(*en.Value.Explode)
			}
			w.line(depth+1, "encoding %s contentType=%q style=%q explode=%s", en.Name, en.Value.ContentType, en.Value.Style, x)
		}
	}
}

func (w *dumper) schema(depth int, label string, s *Schema) {
	if s == nil {
		w.line(depth, "%s <nil>", label)
		return
	}
	if s.Bool != nil {
		w.line(depth, "%s %v", label, *s.Bool)
		return
	}
	if s.Ref != "" {
		w.line(depth, "%s unresolved %q%s", label, s.Ref, w.pos(s.Pos))
		return
	}
	if !w.once(depth, label, s, s.Pos) {
		return
	}
	var attrs []string
	add := func(k, v string) {
		if v != "" {
			attrs = append(attrs, k+"="+v)
		}
	}
	add("type", strings.Join(s.Type, "|"))
	add("format", s.Format)
	if s.Title != "" {
		add("title", fmt.Sprintf("%q", s.Title))
	}
	if s.Recursive {
		add("recursive", "true")
	}
	if s.Nullable {
		add("nullable", "true")
	}
	if s.ReadOnly {
		add("readOnly", "true")
	}
	if s.WriteOnly {
		add("writeOnly", "true")
	}
	if s.Deprecated {
		add("deprecated", "true")
	}
	if s.UniqueItems {
		add("uniqueItems", "true")
	}
	add("multipleOf", string(s.MultipleOf))
	add("minimum", string(s.Minimum))
	add("maximum", string(s.Maximum))
	add("exclusiveMinimum", string(s.ExclusiveMinimum))
	add("exclusiveMaximum", string(s.ExclusiveMaximum))
	for _, ip := range []struct {
		k string
		v *int
	}{{"minLength", s.MinLength}, {"maxLength", s.MaxLength}, {"minItems", s.MinItems}, {"maxItems", s.MaxItems}, {"minProperties", s.MinProperties}, {"maxProperties", s.MaxProperties}} {
		if ip.v != nil {
			add(ip.k, fmt.Sprint(*ip.v))
		}
	}
	add("pattern", s.Pattern)
	add("contentMediaType", s.ContentMediaType)
	add("contentEncoding", s.ContentEncoding)
	add("$id", s.ID)
	add("$anchor", s.Anchor)
	add("$schema", s.Schema)
	if len(s.Required) > 0 {
		add("required", strings.Join(s.Required, ","))
	}
	w.line(depth, "%s %s%s", label, strings.Join(attrs, " "), w.pos(s.Pos))
	d := depth + 1
	w.opt(d, "description", s.Description)
	w.rawOpt(d, "default", s.Default)
	w.rawOpt(d, "example", s.Example)
	w.rawOpt(d, "const", s.Const)
	for _, e := range s.Examples {
		w.line(d, "examples[] %s", raw(e))
	}
	for _, e := range s.Enum {
		w.line(d, "enum[] %s", raw(e))
	}
	if s.Discriminator != nil {
		w.line(d, "discriminator %s %v", s.Discriminator.PropertyName, s.Discriminator.Mapping)
	}
	if s.XML != nil {
		w.line(d, "xml %+v", *s.XML)
	}
	for _, p := range s.Properties {
		w.schema(d, "property "+p.Name, p.Value)
	}
	for _, p := range s.PatternProperties {
		w.schema(d, "patternProperty "+p.Name, p.Value)
	}
	for _, sub := range []struct {
		n string
		s *Schema
	}{{"additionalProperties", s.AdditionalProperties}, {"propertyNames", s.PropertyNames}, {"items", s.Items}, {"contains", s.Contains}, {"not", s.Not}, {"if", s.If}, {"then", s.Then}, {"else", s.Else}} {
		if sub.s != nil {
			w.schema(d, sub.n, sub.s)
		}
	}
	for _, l := range []struct {
		n string
		l []*Schema
	}{{"prefixItems", s.PrefixItems}, {"allOf", s.AllOf}, {"anyOf", s.AnyOf}, {"oneOf", s.OneOf}} {
		for _, x := range l.l {
			w.schema(d, l.n+"[]", x)
		}
	}
	for _, p := range s.Defs {
		w.schema(d, "$defs "+p.Name, p.Value)
	}
	w.ext(d, s.Extensions)
}
