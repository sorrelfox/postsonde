package openapi

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// securityUse is a security requirement entry, checked against the
// components once the whole document is read.
type securityUse struct {
	name string
	node *yaml.Node
	ptr  string
}

func (b *builder) document() *Document {
	root := b.root
	d := &Document{OpenAPI: scalarText(root.Get("openapi")), Minor: min(b.minor, 1)}
	if b.minor > 1 {
		v := root.Get("openapi").Resolve()
		b.diag(SeverityWarning, CodeUnsupported, v, "#/openapi", "OpenAPI "+d.OpenAPI+" is newer than 3.1; it is read as 3.1.")
	}
	var paths, webhooks, components *yaml.Node
	for _, p := range root.Pairs {
		k := keyText(p.Key)
		ptr := child("#", k)
		switch k {
		case "openapi":
		case "info":
			d.Info = b.info(p.Value, ptr)
		case "jsonSchemaDialect":
			d.JSONSchemaDialect = b.str(p.Value, ptr, "jsonSchemaDialect")
		case "servers":
			d.Servers = b.servers(p.Value, ptr)
		case "paths":
			paths = p.Value
		case "webhooks":
			webhooks = p.Value
			if b.minor == 0 {
				b.diag(SeverityInfo, CodeUnsupported, p.Key, ptr, "webhooks are part of OpenAPI 3.1, not 3.0; they are read anyway.")
			}
		case "components":
			components = p.Value
		case "security":
			d.Security = b.security(p.Value, ptr)
		case "tags":
			d.Tags = b.tags(p.Value, ptr)
		case "externalDocs":
			d.ExternalDocs = b.externalDocs(p.Value, ptr)
		default:
			if !strings.HasPrefix(k, "x-") {
				b.diag(SeverityWarning, CodeIgnored, p.Key, ptr, "The field "+strconv.Quote(k)+" is not part of OpenAPI; it is ignored.")
			}
		}
	}
	d.Extensions = b.extensions(root, "#")
	if root.Get("info") == nil {
		b.diag(SeverityWarning, CodeMissingField, root, "#", "The document has no info object.")
	}
	if components != nil {
		d.Components = b.components(components, "#/components")
	}
	if paths != nil {
		d.Paths = b.paths(paths, "#/paths")
	} else if b.minor == 0 || (webhooks == nil && components == nil) {
		b.diag(SeverityWarning, CodeMissingField, root, "#", "The document has no paths.")
	}
	if webhooks != nil {
		for _, p := range pairs(b.mapping(webhooks, "#/webhooks", "webhooks")) {
			k := keyText(p.Key)
			if strings.HasPrefix(k, "x-") {
				continue
			}
			ptr := child("#/webhooks", k)
			item := resolve(b, p.Value, ptr, "A path item", b.pathItems, b.fillPathItem)
			if item != nil {
				d.Webhooks = append(d.Webhooks, Entry[*PathItem]{Name: k, Value: item})
			}
		}
	}
	if len(d.Servers) == 0 {
		d.Servers = []Server{{URL: "/"}}
	}
	b.finish()
	for _, item := range d.Paths {
		for _, op := range item.Operations {
			b.checkPathParams(op, item.Path)
		}
	}
	for _, u := range b.secUses {
		if _, ok := d.Components.SecuritySchemes.Get(u.name); !ok {
			b.diag(SeverityWarning, CodeBadRef, u.node, u.ptr, "The security scheme "+strconv.Quote(u.name)+" is not defined in components.securitySchemes.")
		}
	}
	return d
}

func (b *builder) info(v *yaml.Node, ptr string) Info {
	m := b.mapping(v, ptr, "info")
	var in Info
	if m == nil {
		return in
	}
	in.Title = b.str(m.Get("title"), child(ptr, "title"), "info.title")
	in.Summary = b.str(m.Get("summary"), child(ptr, "summary"), "info.summary")
	in.Description = b.str(m.Get("description"), child(ptr, "description"), "info.description")
	in.TermsOfService = b.str(m.Get("termsOfService"), child(ptr, "termsOfService"), "info.termsOfService")
	in.Version = b.str(m.Get("version"), child(ptr, "version"), "info.version")
	if c := b.mapping(m.Get("contact"), child(ptr, "contact"), "info.contact"); c != nil {
		cp := child(ptr, "contact")
		in.Contact = &Contact{
			Name:  b.str(c.Get("name"), child(cp, "name"), "contact.name"),
			URL:   b.str(c.Get("url"), child(cp, "url"), "contact.url"),
			Email: b.str(c.Get("email"), child(cp, "email"), "contact.email"),
		}
	}
	if l := b.mapping(m.Get("license"), child(ptr, "license"), "info.license"); l != nil {
		lp := child(ptr, "license")
		in.License = &License{
			Name:       b.str(l.Get("name"), child(lp, "name"), "license.name"),
			Identifier: b.str(l.Get("identifier"), child(lp, "identifier"), "license.identifier"),
			URL:        b.str(l.Get("url"), child(lp, "url"), "license.url"),
		}
	}
	in.Extensions = b.extensions(m, ptr)
	if m.Get("title") == nil {
		b.diag(SeverityWarning, CodeMissingField, m, ptr, "info has no title.")
	}
	if m.Get("version") == nil {
		b.diag(SeverityWarning, CodeMissingField, m, ptr, "info has no version.")
	}
	return in
}

func (b *builder) externalDocs(v *yaml.Node, ptr string) *ExternalDocs {
	m := b.mapping(v, ptr, "externalDocs")
	if m == nil {
		return nil
	}
	return &ExternalDocs{
		Description: b.str(m.Get("description"), child(ptr, "description"), "externalDocs.description"),
		URL:         b.str(m.Get("url"), child(ptr, "url"), "externalDocs.url"),
	}
}

func (b *builder) tags(v *yaml.Node, ptr string) []Tag {
	var out []Tag
	seen := map[string]bool{}
	for i, it := range b.sequence(v, ptr, "tags") {
		ip := index(ptr, i)
		m := b.mapping(it, ip, "A tag")
		if m == nil {
			continue
		}
		t := Tag{
			Name:         b.str(m.Get("name"), child(ip, "name"), "tag.name"),
			Description:  b.str(m.Get("description"), child(ip, "description"), "tag.description"),
			ExternalDocs: b.externalDocs(m.Get("externalDocs"), child(ip, "externalDocs")),
			Extensions:   b.extensions(m, ip),
			Pos:          b.pos(m, ip),
		}
		if t.Name == "" {
			b.diag(SeverityWarning, CodeMissingField, m, ip, "The tag has no name.")
		} else if seen[t.Name] {
			b.diag(SeverityWarning, CodeDuplicate, m, ip, "The tag "+strconv.Quote(t.Name)+" is listed twice.")
		}
		seen[t.Name] = true
		out = append(out, t)
	}
	return out
}

// servers reads a servers array. The result is non-nil when the array is
// there, even when it is empty.
func (b *builder) servers(v *yaml.Node, ptr string) []Server {
	items := b.sequence(v, ptr, "servers")
	if vn := v.Resolve(); vn == nil || vn.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]Server, 0, len(items))
	for i, it := range items {
		ip := index(ptr, i)
		m := b.mapping(it, ip, "A server")
		if m == nil {
			continue
		}
		s := Server{
			URL:         b.str(m.Get("url"), child(ip, "url"), "server.url"),
			Description: b.str(m.Get("description"), child(ip, "description"), "server.description"),
			Extensions:  b.extensions(m, ip),
			Pos:         b.pos(m, ip),
		}
		if m.Get("url") == nil {
			b.diag(SeverityWarning, CodeMissingField, m, ip, "The server has no url.")
		}
		vp := child(ip, "variables")
		for _, p := range pairs(b.mapping(m.Get("variables"), vp, "server.variables")) {
			name := keyText(p.Key)
			np := child(vp, name)
			vm := b.mapping(p.Value, np, "A server variable")
			if vm == nil {
				continue
			}
			sv := &ServerVariable{
				Default:     b.str(vm.Get("default"), child(np, "default"), "variable.default"),
				Enum:        b.strList(vm.Get("enum"), child(np, "enum"), "variable.enum"),
				Description: b.str(vm.Get("description"), child(np, "description"), "variable.description"),
			}
			if vm.Get("default") == nil {
				b.diag(SeverityWarning, CodeMissingField, vm, np, "The server variable "+strconv.Quote(name)+" has no default.")
			}
			s.Variables = append(s.Variables, Entry[*ServerVariable]{Name: name, Value: sv})
		}
		for _, name := range templateNames(s.URL) {
			if _, ok := s.Variables.Get(name); !ok {
				b.diag(SeverityWarning, CodeMissingField, m.Get("url").Resolve(), child(ip, "url"), "The server URL uses {"+name+"}, which is not in variables.")
			}
		}
		out = append(out, s)
	}
	return out
}

// templateNames returns the {names} in a path or URL template.
func templateNames(s string) []string {
	var out []string
	for {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			return out
		}
		out = append(out, s[i+1:i+j])
		s = s[i+j+1:]
	}
}

func (b *builder) security(v *yaml.Node, ptr string) []SecurityRequirement {
	items := b.sequence(v, ptr, "security")
	if vn := v.Resolve(); vn == nil || vn.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]SecurityRequirement, 0, len(items))
	for i, it := range items {
		ip := index(ptr, i)
		m := b.mapping(it, ip, "A security requirement")
		if m == nil {
			continue
		}
		req := SecurityRequirement{}
		for _, p := range m.Pairs {
			name := keyText(p.Key)
			np := child(ip, name)
			scopes := b.strList(p.Value, np, "The scopes of "+strconv.Quote(name))
			if scopes == nil {
				scopes = []string{}
			}
			req = append(req, Entry[[]string]{Name: name, Value: scopes})
			b.secUses = append(b.secUses, securityUse{name: name, node: p.Key, ptr: np})
		}
		out = append(out, req)
	}
	return out
}

func (b *builder) components(v *yaml.Node, ptr string) Components {
	var c Components
	m := b.mapping(v, ptr, "components")
	if m == nil {
		return c
	}
	for _, p := range m.Pairs {
		k := keyText(p.Key)
		kp := child(ptr, k)
		entries := pairs(b.mapping(p.Value, kp, "components."+k))
		for _, e := range entries {
			name := keyText(e.Key)
			np := child(kp, name)
			switch k {
			case "schemas":
				c.Schemas = append(c.Schemas, Entry[*Schema]{name, b.schema(e.Value, np)})
			case "responses":
				c.Responses = append(c.Responses, Entry[*Response]{name, resolve(b, e.Value, np, "A response", b.responses, b.fillResponse)})
			case "parameters":
				c.Parameters = append(c.Parameters, Entry[*Parameter]{name, resolve(b, e.Value, np, "A parameter", b.params, b.fillParameter)})
			case "examples":
				c.Examples = append(c.Examples, Entry[*Example]{name, resolve(b, e.Value, np, "An example", b.examples, b.fillExample)})
			case "requestBodies":
				c.RequestBodies = append(c.RequestBodies, Entry[*RequestBody]{name, resolve(b, e.Value, np, "A request body", b.bodies, b.fillRequestBody)})
			case "headers":
				c.Headers = append(c.Headers, Entry[*Header]{name, resolve(b, e.Value, np, "A header", b.headers, b.fillHeader)})
			case "securitySchemes":
				c.SecuritySchemes = append(c.SecuritySchemes, Entry[*SecurityScheme]{name, resolve(b, e.Value, np, "A security scheme", b.schemes, b.fillSecurityScheme)})
			case "links":
				// Links are not modelled; see Response.Links.
			case "callbacks":
				c.Callbacks = append(c.Callbacks, Entry[*Callback]{name, resolve(b, e.Value, np, "A callback", b.callbacks, b.fillCallback)})
			case "pathItems":
				c.PathItems = append(c.PathItems, Entry[*PathItem]{name, resolve(b, e.Value, np, "A path item", b.pathItems, b.fillPathItem)})
			}
		}
		switch k {
		case "schemas", "responses", "parameters", "examples", "requestBodies", "headers", "securitySchemes", "links", "callbacks":
		case "pathItems":
			if b.minor == 0 {
				b.diag(SeverityInfo, CodeUnsupported, p.Key, kp, "components.pathItems is part of OpenAPI 3.1, not 3.0; it is read anyway.")
			}
		default:
			if !strings.HasPrefix(k, "x-") {
				b.diag(SeverityWarning, CodeIgnored, p.Key, kp, "The field "+strconv.Quote(k)+" is not part of components; it is ignored.")
			}
		}
	}
	c.Extensions = b.extensions(m, ptr)
	return c
}

func (b *builder) paths(v *yaml.Node, ptr string) []*PathItem {
	m := b.mapping(v, ptr, "paths")
	var out []*PathItem
	for _, p := range pairs(m) {
		path := keyText(p.Key)
		if strings.HasPrefix(path, "x-") {
			continue
		}
		pp := child(ptr, path)
		if !strings.HasPrefix(path, "/") {
			b.diag(SeverityWarning, CodeInvalidValue, p.Key, pp, "The path "+strconv.Quote(path)+" does not start with /.")
		}
		item := resolve(b, p.Value, pp, "A path item", b.pathItems, b.fillPathItem)
		if item == nil {
			continue
		}
		out = append(out, b.withPath(item, path, refOf(p.Value) != nil))
	}
	return out
}

// withPath sets the path of a path item and its operations once they
// are complete. A path item reached through a $ref, or already used for
// another path, is shared, so a copy gets the path instead.
func (b *builder) withPath(item *PathItem, path string, viaRef bool) *PathItem {
	if !viaRef && !b.pathSet[item] {
		b.pathSet[item] = true
		b.post2 = append(b.post2, func() {
			item.Path = path
			for _, op := range item.Operations {
				op.Path = path
			}
		})
		return item
	}
	c := &PathItem{}
	b.post2 = append(b.post2, func() {
		*c = *item
		c.Path = path
		c.Operations = make([]*Operation, len(item.Operations))
		for i, op := range item.Operations {
			oc := *op
			oc.Path = path
			c.Operations[i] = &oc
		}
	})
	return c
}

// checkPathParams reports {names} in path without a path parameter, and
// path parameters that are not in the path.
func (b *builder) checkPathParams(op *Operation, path string) {
	names := templateNames(path)
	inPath := map[string]bool{}
	for _, n := range names {
		inPath[n] = true
	}
	defined := map[string]bool{}
	for _, p := range op.Parameters {
		if p.In != InPath {
			continue
		}
		defined[p.Name] = true
		if !inPath[p.Name] && p.Name != "" {
			b.diag(SeverityWarning, CodePathParameter, &yaml.Node{Line: op.Pos.Line, Column: op.Pos.Column}, op.Pos.Pointer,
				op.Method+" "+path+" has a path parameter "+strconv.Quote(p.Name)+" that is not in the path.")
		}
	}
	for _, n := range names {
		if !defined[n] {
			b.diag(SeverityWarning, CodePathParameter, &yaml.Node{Line: op.Pos.Line, Column: op.Pos.Column}, op.Pos.Pointer,
				op.Method+" "+path+" does not define the path parameter {"+n+"}.")
		}
	}
}

func (b *builder) fillPathItem(pi *PathItem, m *yaml.Node, ptr string) {
	pi.Pos = b.pos(m, ptr)
	pi.Summary = b.str(m.Get("summary"), child(ptr, "summary"), "summary")
	pi.Description = b.str(m.Get("description"), child(ptr, "description"), "description")
	if s := m.Get("servers"); s != nil {
		pi.Servers = b.servers(s, child(ptr, "servers"))
	}
	pi.Parameters = b.parameters(m.Get("parameters"), child(ptr, "parameters"))
	for _, p := range m.Pairs {
		k := keyText(p.Key)
		if !isMethod(k) {
			continue
		}
		op := b.operation(p.Value, child(ptr, k), strings.ToUpper(k), pi.Parameters)
		if op != nil {
			pi.Operations = append(pi.Operations, op)
		}
	}
	pi.Extensions = b.extensions(m, ptr)
}

func isMethod(k string) bool {
	for _, m := range Methods {
		if k == m {
			return true
		}
	}
	return false
}

// parameters reads a parameters array, reporting duplicates once the
// referenced parameters are complete.
func (b *builder) parameters(v *yaml.Node, ptr string) []*Parameter {
	var out []*Parameter
	var nodes []*yaml.Node
	for i, it := range b.sequence(v, ptr, "parameters") {
		p := resolve(b, it, index(ptr, i), "A parameter", b.params, b.fillParameter)
		if p == nil {
			continue
		}
		out = append(out, p)
		nodes = append(nodes, it.Resolve())
	}
	if len(out) > 1 {
		b.post1 = append(b.post1, func() {
			seen := map[string]bool{}
			for i, p := range out {
				key := p.In + "\x00" + p.Name
				if p.Name != "" && seen[key] {
					b.diag(SeverityWarning, CodeDuplicate, nodes[i], index(ptr, i), "The "+p.In+" parameter "+strconv.Quote(p.Name)+" is listed twice.")
				}
				seen[key] = true
			}
		})
	}
	return out
}

func (b *builder) operation(v *yaml.Node, ptr, method string, inherited []*Parameter) *Operation {
	m := b.mapping(v, ptr, "An operation")
	if m == nil {
		return nil
	}
	op := &Operation{Method: method, Pos: b.pos(m, ptr)}
	op.Tags = b.strList(m.Get("tags"), child(ptr, "tags"), "tags")
	op.Summary = b.str(m.Get("summary"), child(ptr, "summary"), "summary")
	op.Description = b.str(m.Get("description"), child(ptr, "description"), "description")
	op.ExternalDocs = b.externalDocs(m.Get("externalDocs"), child(ptr, "externalDocs"))
	op.OperationID = b.str(m.Get("operationId"), child(ptr, "operationId"), "operationId")
	if op.OperationID != "" {
		if b.opIDs[op.OperationID] {
			b.diag(SeverityWarning, CodeDuplicate, m.Get("operationId").Resolve(), child(ptr, "operationId"),
				"The operationId "+strconv.Quote(op.OperationID)+" is used by more than one operation.")
		}
		b.opIDs[op.OperationID] = true
	}
	own := b.parameters(m.Get("parameters"), child(ptr, "parameters"))
	// Referenced parameters are complete only after finish's pending
	// phase, so the merge waits for it.
	b.post1 = append(b.post1, func() { op.Parameters = mergeParameters(inherited, own) })
	op.RequestBody = resolve(b, m.Get("requestBody"), child(ptr, "requestBody"), "A request body", b.bodies, b.fillRequestBody)
	if r := m.Get("responses"); r != nil {
		rp := child(ptr, "responses")
		for _, p := range pairs(b.mapping(r, rp, "responses")) {
			code := keyText(p.Key)
			if strings.HasPrefix(code, "x-") {
				continue
			}
			cp := child(rp, code)
			if !validStatus(code) {
				b.diag(SeverityWarning, CodeInvalidValue, p.Key, cp, "The response key "+strconv.Quote(code)+" is not a status code, a range like 4XX, or default.")
			}
			op.Responses = append(op.Responses, Entry[*Response]{code, resolve(b, p.Value, cp, "A response", b.responses, b.fillResponse)})
		}
	} else if b.minor == 0 {
		b.diag(SeverityWarning, CodeMissingField, m, ptr, method+" operation has no responses.")
	}
	cbp := child(ptr, "callbacks")
	for _, p := range pairs(b.mapping(m.Get("callbacks"), cbp, "callbacks")) {
		name := keyText(p.Key)
		op.Callbacks = append(op.Callbacks, Entry[*Callback]{name, resolve(b, p.Value, child(cbp, name), "A callback", b.callbacks, b.fillCallback)})
	}
	op.Deprecated = b.boolean(m.Get("deprecated"), child(ptr, "deprecated"), "deprecated")
	if s := m.Get("security"); s != nil {
		op.Security = b.security(s, child(ptr, "security"))
	}
	if s := m.Get("servers"); s != nil {
		op.Servers = b.servers(s, child(ptr, "servers"))
	}
	op.Extensions = b.extensions(m, ptr)
	return op
}

// mergeParameters returns the path item's parameters overridden by the
// operation's (same name and location), then the operation's others.
func mergeParameters(inherited, own []*Parameter) []*Parameter {
	if len(inherited) == 0 {
		return own
	}
	out := make([]*Parameter, 0, len(inherited)+len(own))
	used := make([]bool, len(own))
	for _, ip := range inherited {
		replaced := false
		for j, p := range own {
			if !used[j] && p.Name == ip.Name && p.In == ip.In {
				out = append(out, p)
				used[j], replaced = true, true
				break
			}
		}
		if !replaced {
			out = append(out, ip)
		}
	}
	for j, p := range own {
		if !used[j] {
			out = append(out, p)
		}
	}
	return out
}

func validStatus(code string) bool {
	if code == "default" {
		return true
	}
	if len(code) != 3 || code[0] < '1' || code[0] > '5' {
		return false
	}
	if code[1:] == "XX" || code[1:] == "xx" {
		return true
	}
	return code[1] >= '0' && code[1] <= '9' && code[2] >= '0' && code[2] <= '9'
}

func (b *builder) fillParameter(p *Parameter, m *yaml.Node, ptr string) {
	p.Pos = b.pos(m, ptr)
	p.Name = b.str(m.Get("name"), child(ptr, "name"), "name")
	p.In = b.str(m.Get("in"), child(ptr, "in"), "in")
	p.Description = b.str(m.Get("description"), child(ptr, "description"), "description")
	p.Required = b.boolean(m.Get("required"), child(ptr, "required"), "required")
	p.Deprecated = b.boolean(m.Get("deprecated"), child(ptr, "deprecated"), "deprecated")
	p.AllowEmptyValue = b.boolean(m.Get("allowEmptyValue"), child(ptr, "allowEmptyValue"), "allowEmptyValue")
	p.Style = b.str(m.Get("style"), child(ptr, "style"), "style")
	p.Explode = b.boolPtr(m.Get("explode"), child(ptr, "explode"), "explode")
	p.AllowReserved = b.boolean(m.Get("allowReserved"), child(ptr, "allowReserved"), "allowReserved")
	if s := m.Get("schema"); s != nil {
		p.Schema = b.schema(s, child(ptr, "schema"))
	}
	if e := m.Get("example"); e != nil {
		p.Example = b.value(e, child(ptr, "example"))
	}
	p.Examples = b.exampleMap(m.Get("examples"), child(ptr, "examples"))
	p.Content = b.content(m.Get("content"), child(ptr, "content"))
	p.Extensions = b.extensions(m, ptr)

	if m.Get("name") == nil {
		b.diag(SeverityWarning, CodeMissingField, m, ptr, "The parameter has no name.")
	}
	switch p.In {
	case InPath:
		if !p.Required {
			b.diag(SeverityWarning, CodeInvalidValue, m, ptr, "The path parameter "+strconv.Quote(p.Name)+" must be required; it is read as required.")
			p.Required = true
		}
	case InQuery, InHeader, InCookie:
	case "":
		b.diag(SeverityWarning, CodeMissingField, m, ptr, "The parameter "+strconv.Quote(p.Name)+" has no in.")
	default:
		b.diag(SeverityWarning, CodeInvalidValue, m.Get("in").Resolve(), child(ptr, "in"),
			"The parameter location "+strconv.Quote(p.In)+" is not path, query, header or cookie.")
	}
	if m.Get("schema") != nil && m.Get("content") != nil {
		b.diag(SeverityWarning, CodeInvalidValue, m, ptr, "The parameter "+strconv.Quote(p.Name)+" has both schema and content; only one is allowed.")
	}
	if len(p.Content) > 1 {
		b.diag(SeverityWarning, CodeInvalidValue, m.Get("content").Resolve(), child(ptr, "content"), "A parameter's content must have exactly one media type.")
	}
}

func (b *builder) exampleMap(v *yaml.Node, ptr string) Entries[*Example] {
	var out Entries[*Example]
	for _, p := range pairs(b.mapping(v, ptr, "examples")) {
		name := keyText(p.Key)
		out = append(out, Entry[*Example]{name, resolve(b, p.Value, child(ptr, name), "An example", b.examples, b.fillExample)})
	}
	return out
}

func (b *builder) fillExample(e *Example, m *yaml.Node, ptr string) {
	e.Pos = b.pos(m, ptr)
	e.Summary = b.str(m.Get("summary"), child(ptr, "summary"), "summary")
	e.Description = b.str(m.Get("description"), child(ptr, "description"), "description")
	if v := m.Get("value"); v != nil {
		e.Value = b.value(v, child(ptr, "value"))
	}
	e.ExternalValue = b.str(m.Get("externalValue"), child(ptr, "externalValue"), "externalValue")
	e.Extensions = b.extensions(m, ptr)
	if m.Get("value") != nil && m.Get("externalValue") != nil {
		b.diag(SeverityWarning, CodeInvalidValue, m, ptr, "The example has both value and externalValue; only one is allowed.")
	}
}

func (b *builder) content(v *yaml.Node, ptr string) Entries[*MediaType] {
	var out Entries[*MediaType]
	for _, p := range pairs(b.mapping(v, ptr, "content")) {
		name := keyText(p.Key)
		np := child(ptr, name)
		mt := b.mediaType(p.Value, np)
		if mt != nil {
			out = append(out, Entry[*MediaType]{name, mt})
		}
	}
	return out
}

func (b *builder) mediaType(v *yaml.Node, ptr string) *MediaType {
	v = v.Resolve()
	if v != nil && v.Kind == yaml.ScalarNode && v.Tag == yaml.TagNull {
		return &MediaType{Pos: b.pos(v, ptr)} // "application/json: " with no schema
	}
	m := b.mapping(v, ptr, "A media type")
	if m == nil {
		return nil
	}
	mt := &MediaType{Pos: b.pos(m, ptr)}
	if s := m.Get("schema"); s != nil {
		mt.Schema = b.schema(s, child(ptr, "schema"))
	}
	if e := m.Get("example"); e != nil {
		mt.Example = b.value(e, child(ptr, "example"))
	}
	mt.Examples = b.exampleMap(m.Get("examples"), child(ptr, "examples"))
	if m.Get("example") != nil && m.Get("examples") != nil {
		b.diag(SeverityWarning, CodeInvalidValue, m, ptr, "The media type has both example and examples; only one is allowed.")
	}
	ep := child(ptr, "encoding")
	for _, p := range pairs(b.mapping(m.Get("encoding"), ep, "encoding")) {
		name := keyText(p.Key)
		np := child(ep, name)
		em := b.mapping(p.Value, np, "An encoding")
		if em == nil {
			continue
		}
		mt.Encoding = append(mt.Encoding, Entry[*Encoding]{name, &Encoding{
			ContentType:   b.str(em.Get("contentType"), child(np, "contentType"), "contentType"),
			Headers:       b.headerMap(em.Get("headers"), child(np, "headers")),
			Style:         b.str(em.Get("style"), child(np, "style"), "style"),
			Explode:       b.boolPtr(em.Get("explode"), child(np, "explode"), "explode"),
			AllowReserved: b.boolean(em.Get("allowReserved"), child(np, "allowReserved"), "allowReserved"),
		}})
	}
	mt.Extensions = b.extensions(m, ptr)
	return mt
}

func (b *builder) headerMap(v *yaml.Node, ptr string) Entries[*Header] {
	var out Entries[*Header]
	for _, p := range pairs(b.mapping(v, ptr, "headers")) {
		name := keyText(p.Key)
		out = append(out, Entry[*Header]{name, resolve(b, p.Value, child(ptr, name), "A header", b.headers, b.fillHeader)})
	}
	return out
}

func (b *builder) fillRequestBody(r *RequestBody, m *yaml.Node, ptr string) {
	r.Pos = b.pos(m, ptr)
	r.Description = b.str(m.Get("description"), child(ptr, "description"), "description")
	r.Content = b.content(m.Get("content"), child(ptr, "content"))
	r.Required = b.boolean(m.Get("required"), child(ptr, "required"), "required")
	r.Extensions = b.extensions(m, ptr)
	if m.Get("content") == nil {
		b.diag(SeverityWarning, CodeMissingField, m, ptr, "The request body has no content.")
	}
}

func (b *builder) fillResponse(r *Response, m *yaml.Node, ptr string) {
	r.Pos = b.pos(m, ptr)
	r.Description = b.str(m.Get("description"), child(ptr, "description"), "description")
	r.Headers = b.headerMap(m.Get("headers"), child(ptr, "headers"))
	r.Content = b.content(m.Get("content"), child(ptr, "content"))
	lp := child(ptr, "links")
	for _, p := range pairs(b.mapping(m.Get("links"), lp, "links")) {
		name := keyText(p.Key)
		r.Links = append(r.Links, Entry[json.RawMessage]{name, b.value(p.Value, child(lp, name))})
	}
	r.Extensions = b.extensions(m, ptr)
}

func (b *builder) fillHeader(h *Header, m *yaml.Node, ptr string) {
	h.Pos = b.pos(m, ptr)
	h.Description = b.str(m.Get("description"), child(ptr, "description"), "description")
	h.Required = b.boolean(m.Get("required"), child(ptr, "required"), "required")
	h.Deprecated = b.boolean(m.Get("deprecated"), child(ptr, "deprecated"), "deprecated")
	h.Style = b.str(m.Get("style"), child(ptr, "style"), "style")
	h.Explode = b.boolPtr(m.Get("explode"), child(ptr, "explode"), "explode")
	if s := m.Get("schema"); s != nil {
		h.Schema = b.schema(s, child(ptr, "schema"))
	}
	if e := m.Get("example"); e != nil {
		h.Example = b.value(e, child(ptr, "example"))
	}
	h.Examples = b.exampleMap(m.Get("examples"), child(ptr, "examples"))
	h.Content = b.content(m.Get("content"), child(ptr, "content"))
	h.Extensions = b.extensions(m, ptr)
	for _, k := range []string{"name", "in"} {
		if m.Get(k) != nil {
			b.diag(SeverityInfo, CodeIgnored, m.Get(k).Resolve(), child(ptr, k), "A header must not have "+k+"; it is ignored.")
		}
	}
}

func (b *builder) fillCallback(c *Callback, m *yaml.Node, ptr string) {
	c.Pos = b.pos(m, ptr)
	for _, p := range m.Pairs {
		expr := keyText(p.Key)
		if strings.HasPrefix(expr, "x-") {
			continue
		}
		ep := child(ptr, expr)
		item := resolve(b, p.Value, ep, "A path item", b.pathItems, b.fillPathItem)
		if item != nil {
			c.Paths = append(c.Paths, b.withPath(item, expr, refOf(p.Value) != nil))
		}
	}
}

var schemeTypes = map[string]bool{"apiKey": true, "http": true, "mutualTLS": true, "oauth2": true, "openIdConnect": true}

func (b *builder) fillSecurityScheme(s *SecurityScheme, m *yaml.Node, ptr string) {
	s.Pos = b.pos(m, ptr)
	s.Type = b.str(m.Get("type"), child(ptr, "type"), "type")
	s.Description = b.str(m.Get("description"), child(ptr, "description"), "description")
	s.Name = b.str(m.Get("name"), child(ptr, "name"), "name")
	s.In = b.str(m.Get("in"), child(ptr, "in"), "in")
	s.Scheme = b.str(m.Get("scheme"), child(ptr, "scheme"), "scheme")
	s.BearerFormat = b.str(m.Get("bearerFormat"), child(ptr, "bearerFormat"), "bearerFormat")
	s.OpenIDConnectURL = b.str(m.Get("openIdConnectUrl"), child(ptr, "openIdConnectUrl"), "openIdConnectUrl")
	if f := b.mapping(m.Get("flows"), child(ptr, "flows"), "flows"); f != nil {
		fp := child(ptr, "flows")
		s.Flows = &OAuthFlows{
			Implicit:          b.oauthFlow(f.Get("implicit"), child(fp, "implicit")),
			Password:          b.oauthFlow(f.Get("password"), child(fp, "password")),
			ClientCredentials: b.oauthFlow(f.Get("clientCredentials"), child(fp, "clientCredentials")),
			AuthorizationCode: b.oauthFlow(f.Get("authorizationCode"), child(fp, "authorizationCode")),
		}
	}
	s.Extensions = b.extensions(m, ptr)

	missing := func(field string) {
		if m.Get(field) == nil {
			b.diag(SeverityWarning, CodeMissingField, m, ptr, "The "+s.Type+" security scheme has no "+field+".")
		}
	}
	switch s.Type {
	case "apiKey":
		missing("name")
		missing("in")
	case "http":
		missing("scheme")
	case "oauth2":
		missing("flows")
	case "openIdConnect":
		missing("openIdConnectUrl")
	case "mutualTLS":
		if b.minor == 0 {
			b.diag(SeverityInfo, CodeUnsupported, m, ptr, "mutualTLS security schemes are part of OpenAPI 3.1, not 3.0.")
		}
	}
	if s.Type == "" {
		b.diag(SeverityWarning, CodeMissingField, m, ptr, "The security scheme has no type.")
	} else if !schemeTypes[s.Type] {
		b.diag(SeverityWarning, CodeInvalidValue, m.Get("type").Resolve(), child(ptr, "type"), "The security scheme type "+strconv.Quote(s.Type)+" is not known.")
	}
}

func (b *builder) oauthFlow(v *yaml.Node, ptr string) *OAuthFlow {
	m := b.mapping(v, ptr, "An OAuth flow")
	if m == nil {
		return nil
	}
	f := &OAuthFlow{
		AuthorizationURL: b.str(m.Get("authorizationUrl"), child(ptr, "authorizationUrl"), "authorizationUrl"),
		TokenURL:         b.str(m.Get("tokenUrl"), child(ptr, "tokenUrl"), "tokenUrl"),
		RefreshURL:       b.str(m.Get("refreshUrl"), child(ptr, "refreshUrl"), "refreshUrl"),
	}
	sp := child(ptr, "scopes")
	for _, p := range pairs(b.mapping(m.Get("scopes"), sp, "scopes")) {
		name := keyText(p.Key)
		f.Scopes = append(f.Scopes, Entry[string]{name, b.str(p.Value, child(sp, name), "A scope description")})
	}
	return f
}
