package openapi

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// Swagger 2.0 documents are converted to an OpenAPI 3.0 tree before the
// builder reads them (docs/openapi.md#swagger-20):
//
//   - host, basePath and schemes become servers;
//   - consumes and produces become the media types of request bodies and
//     responses;
//   - body parameters become request bodies, formData parameters a form
//     or multipart body (multipart when a parameter is a file);
//   - the other parameters get a schema made from their type keywords,
//     and collectionFormat becomes style and explode;
//   - definitions, parameters, responses and securityDefinitions become
//     components, and $refs to them are rewritten;
//   - type: file becomes type: string, format: binary; a string
//     discriminator becomes a Discriminator Object; x-nullable becomes
//     nullable;
//   - basic security becomes HTTP basic, OAuth 2 flows are renamed.
//
// The converted tree keeps the source's line and column on every node
// (made-up nodes take the position of what they were made from), and the
// builder maps the JSON pointers of its diagnostics and objects back to
// the source document with sourcePointer.

// swagger2 converts a Swagger 2.0 tree.
type swagger2 struct {
	src  *yaml.Node
	lim  Limits
	diag []Diagnostic
	// toSrc maps pointers of the converted tree to the source's; toNew
	// maps source pointers to the converted tree's, for $ref rewriting.
	toSrc map[string]string
	toNew map[string]string
	// schemas and values are the copies of source nodes, so shared and
	// aliased nodes stay shared.
	schemas map[*yaml.Node]*yaml.Node
	values  map[*yaml.Node]*yaml.Node
	// refs are the $ref scalars of the converted tree, rewritten once
	// the whole document is converted.
	refs  []*yaml.Node
	nodes int

	consumes, produces []string
	hasHost            bool
	host, basePath     string
	schemesUsed        []string
}

// convertSwagger2 returns the OpenAPI 3.0 form of a Swagger 2.0 root, with
// the conversion's diagnostics and the pointer map for the builder.
func convertSwagger2(root *yaml.Node, lim Limits) (*yaml.Node, *swagger2, error) {
	c := &swagger2{
		src: root, lim: lim,
		toSrc:   map[string]string{},
		toNew:   map[string]string{},
		schemas: map[*yaml.Node]*yaml.Node{},
		values:  map[*yaml.Node]*yaml.Node{},
	}
	out, err := c.convert()
	if err != nil {
		return nil, nil, err
	}
	c.rewriteRefs()
	return out, c, nil
}

// errTooLarge stops a conversion that makes more nodes than the limit:
// objects reached through YAML aliases are converted once per use, so
// aliases of aliases could otherwise multiply the work.
type errTooLarge struct{}

func (c *swagger2) convert() (out *yaml.Node, err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(errTooLarge); !ok {
				panic(r)
			}
			err = errs.New(errs.Unsupported, "The Swagger document is too large to convert: it expands to more than "+
				strconv.Itoa(c.maxNodes())+" values.").WithCode(CodeTooLarge)
		}
	}()
	return c.document(), nil
}

func (c *swagger2) maxNodes() int { return 2 * c.lim.MaxNodes }

func (c *swagger2) warn(sev Severity, code string, n *yaml.Node, ptr, msg string) {
	c.diag = append(c.diag, Diagnostic{Severity: sev, Code: code, Message: msg, Pos: pos(n, ptr)})
}

// mapPtr records that newPtr in the converted tree is srcPtr in the
// source; with ref, a $ref to srcPtr is rewritten to newPtr.
func (c *swagger2) mapPtr(newPtr, srcPtr string, ref bool) {
	if newPtr == srcPtr {
		return
	}
	c.toSrc[newPtr] = srcPtr
	if _, done := c.toNew[srcPtr]; ref && !done {
		c.toNew[srcPtr] = newPtr
	}
}

// longest translates p through m by its longest mapped prefix (whole
// pointer segments); p itself when nothing matches.
func longest(m map[string]string, p string) string {
	if len(m) == 0 {
		return p
	}
	for cut := len(p); cut > 0; {
		if to, ok := m[p[:cut]]; ok {
			return to + p[cut:]
		}
		cut = strings.LastIndexByte(p[:cut], '/')
	}
	return p
}

// sourcePointer maps a pointer of the converted tree to the source.
func (c *swagger2) sourcePointer(p string) string { return longest(c.toSrc, p) }

// rewriteRefs points the local $refs of the converted tree at the
// converted objects: #/definitions/Pet becomes #/components/schemas/Pet.
// References to what does not exist are left as written, so the builder
// reports them with the source's text.
func (c *swagger2) rewriteRefs() {
	for _, r := range c.refs {
		v := r.Value
		if !strings.HasPrefix(v, "#/") {
			continue
		}
		p := v
		if u, err := url.PathUnescape(v); err == nil {
			p = u
		}
		if to := longest(c.toNew, p); to != p {
			r.Value = to
		}
	}
}

// --- nodes ---

func (c *swagger2) node(kind yaml.Kind, tag string, at *yaml.Node) *yaml.Node {
	c.nodes++
	if c.nodes > c.maxNodes() {
		panic(errTooLarge{})
	}
	n := &yaml.Node{Kind: kind, Tag: tag}
	if at = at.Resolve(); at != nil {
		n.Line, n.Column = at.Line, at.Column
	}
	return n
}

func (c *swagger2) mapNode(at *yaml.Node) *yaml.Node {
	return c.node(yaml.MappingNode, yaml.TagMap, at)
}

func (c *swagger2) seqNode(at *yaml.Node) *yaml.Node {
	return c.node(yaml.SequenceNode, yaml.TagSeq, at)
}

func (c *swagger2) strNode(s string, at *yaml.Node) *yaml.Node {
	n := c.node(yaml.ScalarNode, yaml.TagStr, at)
	n.Value, n.Style = s, yaml.DoubleQuoted
	return n
}

func (c *swagger2) boolNode(v bool, at *yaml.Node) *yaml.Node {
	n := c.node(yaml.ScalarNode, yaml.TagBool, at)
	n.Value = strconv.FormatBool(v)
	return n
}

// set adds key: v to the mapping m (v nil is skipped).
func (c *swagger2) set(m *yaml.Node, key string, v *yaml.Node) {
	if v == nil {
		return
	}
	at := v
	if at.Kind == yaml.AliasNode {
		at = m
	}
	m.Pairs = append(m.Pairs, yaml.Pair{Key: c.strNode(key, at), Value: v})
}

// get returns member key of mapping m in the converted tree, or nil.
func get(m *yaml.Node, key string) *yaml.Node {
	for _, p := range m.Pairs {
		if p.Key.Value == key {
			return p.Value
		}
	}
	return nil
}

// value copies a source value as it is, keeping aliases (so a document
// built from aliases does not grow) and marking $refs for rewriting.
func (c *swagger2) value(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode {
		a := c.node(yaml.AliasNode, "", n)
		a.Value = n.Value
		a.Target = c.value(n.Target)
		return a
	}
	if cp, ok := c.values[n]; ok {
		return cp
	}
	cp := c.node(n.Kind, n.Tag, n)
	cp.Style, cp.Value, cp.Anchor = n.Style, n.Value, n.Anchor
	c.values[n] = cp
	switch n.Kind {
	case yaml.MappingNode:
		for _, p := range n.Pairs {
			cp.Pairs = append(cp.Pairs, yaml.Pair{Key: c.value(p.Key), Value: c.value(p.Value)})
		}
	case yaml.SequenceNode:
		for _, it := range n.Items {
			cp.Items = append(cp.Items, c.value(it))
		}
	}
	return cp
}

// ref copies a Reference Object (or any value) as it is, marking its
// $ref for rewriting.
func (c *swagger2) ref(n *yaml.Node) *yaml.Node {
	cp := c.value(n)
	if r := cp.Resolve(); r != nil && r.Kind == yaml.MappingNode {
		if v := get(r, "$ref"); v != nil && v.Kind == yaml.ScalarNode {
			c.refs = append(c.refs, v)
		}
	}
	return cp
}

// extensions copies the x- members of the source mapping m to out.
func (c *swagger2) extensions(out, m *yaml.Node) {
	for _, p := range pairs(m) {
		if strings.HasPrefix(keyText(p.Key), "x-") {
			c.set(out, keyText(p.Key), c.value(p.Value))
		}
	}
}

// copyKeys copies the named members of the source mapping m to out.
func (c *swagger2) copyKeys(out, m *yaml.Node, keys ...string) {
	for _, k := range keys {
		if v := m.Get(k); v != nil {
			c.set(out, k, c.value(v))
		}
	}
}

// --- schemas ---

// schema converts a Schema Object: type: file, a string discriminator
// and x-nullable change; subschemas are converted the same way.
func (c *swagger2) schema(n *yaml.Node, ptr string) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode {
		a := c.node(yaml.AliasNode, "", n)
		a.Value = n.Value
		a.Target = c.schema(n.Target, ptr)
		return a
	}
	if n.Kind != yaml.MappingNode {
		return c.value(n) // the builder reports it
	}
	if cp, ok := c.schemas[n]; ok {
		return cp
	}
	cp := c.mapNode(n)
	cp.Anchor = n.Anchor
	c.schemas[n] = cp
	file := false
	for _, p := range n.Pairs {
		k := keyText(p.Key)
		kp := child(ptr, k)
		v := p.Value
		switch k {
		case "$ref":
			r := c.value(v)
			if r.Kind == yaml.ScalarNode {
				c.refs = append(c.refs, r)
			}
			c.set(cp, k, r)
			continue
		case "type":
			if t := v.Resolve(); t.Kind == yaml.ScalarNode && t.Value == "file" {
				file = true
				c.set(cp, "type", c.strNode("string", t))
				continue
			}
			c.set(cp, k, c.value(v))
		case "items", "additionalProperties", "not":
			c.set(cp, k, c.schema(v, kp))
		case "allOf", "anyOf", "oneOf":
			if s := v.Resolve(); s.Kind == yaml.SequenceNode {
				seq := c.seqNode(s)
				for i, it := range s.Items {
					seq.Items = append(seq.Items, c.schema(it, index(kp, i)))
				}
				c.set(cp, k, seq)
				continue
			}
			c.set(cp, k, c.value(v))
		case "properties", "patternProperties":
			if m := v.Resolve(); m.Kind == yaml.MappingNode {
				props := c.mapNode(m)
				for _, pp := range m.Pairs {
					c.set(props, keyText(pp.Key), c.schema(pp.Value, child(kp, keyText(pp.Key))))
				}
				c.set(cp, k, props)
				continue
			}
			c.set(cp, k, c.value(v))
		case "discriminator":
			if d := v.Resolve(); d.Kind == yaml.ScalarNode && d.Tag == yaml.TagStr {
				dm := c.mapNode(d)
				c.set(dm, "propertyName", c.value(d))
				c.set(cp, k, dm)
				c.mapPtr(child(kp, "propertyName"), kp, false)
				continue
			}
			c.set(cp, k, c.value(v))
		case "x-nullable":
			if b := v.Resolve(); b.Kind == yaml.ScalarNode && b.Tag == yaml.TagBool && n.Get("nullable") == nil {
				c.set(cp, "nullable", c.value(v))
				c.mapPtr(child(ptr, "nullable"), kp, false)
				continue
			}
			c.set(cp, k, c.value(v))
		default:
			c.set(cp, k, c.value(v))
		}
	}
	if file && n.Get("format") == nil {
		c.set(cp, "format", c.strNode("binary", n.Get("type")))
		c.mapPtr(child(ptr, "format"), child(ptr, "type"), false)
	}
	return cp
}

// itemsSchema makes a schema from the type keywords of a non-body
// parameter, a header or an items object. ptr is the source's pointer
// of m.
func (c *swagger2) itemsSchema(m *yaml.Node, ptr string) *yaml.Node {
	s := c.mapNode(m)
	for _, k := range []string{"type", "format", "items", "default", "maximum", "exclusiveMaximum", "minimum", "exclusiveMinimum",
		"maxLength", "minLength", "pattern", "maxItems", "minItems", "uniqueItems", "enum", "multipleOf"} {
		v := m.Get(k)
		if v == nil {
			continue
		}
		switch k {
		case "type":
			if t := v.Resolve(); t.Kind == yaml.ScalarNode && t.Value == "file" {
				c.set(s, "type", c.strNode("string", t))
				if m.Get("format") == nil {
					c.set(s, "format", c.strNode("binary", t))
				}
				continue
			}
		case "items":
			if im := v.Resolve(); im.Kind == yaml.MappingNode {
				ip := child(ptr, "items")
				if cf := im.Get("collectionFormat"); cf != nil {
					c.warn(SeverityInfo, CodeUnsupported, cf.Resolve(), child(ip, "collectionFormat"),
						"The collectionFormat of nested arrays is not supported; the inner array is read as a list of values.")
				}
				if im.Get("$ref") != nil {
					c.set(s, k, c.schema(im, ip))
				} else {
					c.set(s, k, c.itemsSchema(im, ip))
				}
				continue
			}
		}
		c.set(s, k, c.value(v))
	}
	if x := m.Get("x-nullable"); x != nil {
		c.set(s, "nullable", c.value(x))
	}
	return s
}

// --- document ---

var swaggerTop = map[string]bool{
	"swagger": true, "info": true, "host": true, "basePath": true, "schemes": true, "consumes": true, "produces": true,
	"paths": true, "definitions": true, "parameters": true, "responses": true, "securityDefinitions": true,
	"security": true, "tags": true, "externalDocs": true,
}

func (c *swagger2) document() *yaml.Node {
	root := c.src
	out := c.mapNode(root)
	sw := root.Get("swagger").Resolve()
	c.set(out, "openapi", c.strNode("3.0.3", sw))
	c.mapPtr("#/openapi", "#/swagger", false)
	c.consumes = c.mediaTypes(root.Get("consumes"), "#/consumes")
	c.produces = c.mediaTypes(root.Get("produces"), "#/produces")
	c.copyKeys(out, root, "info", "externalDocs")
	c.servers(out)
	c.copyKeys(out, root, "security", "tags")
	for _, p := range root.Pairs {
		k := keyText(p.Key)
		if !swaggerTop[k] && !strings.HasPrefix(k, "x-") {
			c.warn(SeverityWarning, CodeIgnored, p.Key, child("#", k), "The field "+strconv.Quote(k)+" is not part of Swagger 2.0; it is ignored.")
		}
	}
	comps := c.components()
	if paths := root.Get("paths"); paths != nil {
		c.set(out, "paths", c.paths(paths))
	}
	if comps != nil {
		c.set(out, "components", comps)
	}
	c.extensions(out, root)
	return out
}

// mediaTypes reads a consumes or produces list.
func (c *swagger2) mediaTypes(v *yaml.Node, ptr string) []string {
	s := v.Resolve()
	if s == nil || (s.Kind == yaml.ScalarNode && s.Tag == yaml.TagNull) {
		return nil
	}
	if s.Kind != yaml.SequenceNode {
		c.warn(SeverityError, CodeInvalidValue, s, ptr, ptr[2:]+" must be an array of media types, not "+kindName(s)+".")
		return nil
	}
	out := []string{}
	for i, it := range s.Items {
		t := it.Resolve()
		if t.Kind != yaml.ScalarNode || strings.TrimSpace(t.Value) == "" {
			c.warn(SeverityError, CodeInvalidValue, t, index(ptr, i), "A media type must be a string.")
			continue
		}
		out = append(out, strings.TrimSpace(t.Value))
	}
	return out
}

// servers turns host, basePath and schemes into servers: one per scheme.
// Without a host the server is the base path alone, relative to where the
// document is served.
func (c *swagger2) servers(out *yaml.Node) {
	root := c.src
	hostN, baseN := root.Get("host").Resolve(), root.Get("basePath").Resolve()
	if hostN == nil && baseN == nil {
		return
	}
	if hostN != nil {
		c.host = strings.TrimSuffix(scalarText(hostN), "/")
		if hostN.Kind != yaml.ScalarNode || c.host == "" || strings.Contains(c.host, "/") {
			c.warn(SeverityError, CodeInvalidValue, hostN, "#/host", "host must be a host name with an optional port, such as api.example.com:8080.")
			c.host = ""
		}
	}
	if baseN != nil {
		c.basePath = scalarText(baseN)
		if !strings.HasPrefix(c.basePath, "/") {
			c.warn(SeverityWarning, CodeInvalidValue, baseN, "#/basePath", "basePath must start with /; it is read as /"+c.basePath+".")
			c.basePath = "/" + c.basePath
		}
		c.basePath = strings.TrimRight(c.basePath, "/")
	}
	c.hasHost = c.host != ""
	at, atPtr := hostN, "#/host"
	if at == nil || !c.hasHost {
		at, atPtr = baseN, "#/basePath"
	}
	if at == nil {
		return
	}
	var schemes []string
	if c.hasHost {
		schemes = c.schemes(root.Get("schemes"), "#/schemes")
		if len(schemes) == 0 {
			schemes = []string{"https"}
			c.warn(SeverityInfo, CodeMissingField, hostN, "#/host", "The document has no schemes; https is assumed.")
		}
	}
	c.schemesUsed = schemes
	c.set(out, "servers", c.serverList(schemes, at, "#/servers", atPtr))
}

// serverList makes the servers at np from the source's schemes, host
// and base path; sp is the source pointer the servers come from.
func (c *swagger2) serverList(schemes []string, at *yaml.Node, np, sp string) *yaml.Node {
	seq := c.seqNode(at)
	c.mapPtr(np, sp, false)
	add := func(u string) {
		s := c.mapNode(at)
		c.set(s, "url", c.strNode(u, at))
		c.mapPtr(index(np, len(seq.Items)), sp, false)
		seq.Items = append(seq.Items, s)
	}
	if !c.hasHost {
		base := c.basePath
		if base == "" {
			base = "/"
		}
		add(base)
		return seq
	}
	for _, s := range schemes {
		add(s + "://" + c.host + c.basePath)
	}
	return seq
}

// schemes reads a schemes list (http, https, ws, wss).
func (c *swagger2) schemes(v *yaml.Node, ptr string) []string {
	var out []string
	s := v.Resolve()
	if s == nil {
		return nil
	}
	if s.Kind != yaml.SequenceNode {
		c.warn(SeverityError, CodeInvalidValue, s, ptr, "schemes must be an array, not "+kindName(s)+".")
		return nil
	}
	for i, it := range s.Items {
		t := strings.ToLower(scalarText(it))
		switch t {
		case "http", "https", "ws", "wss":
			out = append(out, t)
		default:
			c.warn(SeverityWarning, CodeInvalidValue, it.Resolve(), index(ptr, i), "The scheme "+strconv.Quote(t)+" is not http, https, ws or wss; it is ignored.")
		}
	}
	return out
}

// --- components ---

func (c *swagger2) components() *yaml.Node {
	root := c.src
	out := c.mapNode(root)
	section := func(key, newKey string, conv func(name string, v *yaml.Node, srcPtr, newPtr string) *yaml.Node) {
		v := root.Get(key)
		if v == nil {
			return
		}
		srcSec, newSec := child("#", key), child("#/components", newKey)
		m := v.Resolve()
		if m.Kind != yaml.MappingNode {
			if m.Kind != yaml.ScalarNode || m.Tag != yaml.TagNull {
				c.warn(SeverityError, CodeInvalidValue, m, srcSec, key+" must be an object, not "+kindName(m)+".")
			}
			return
		}
		sec := get(out, newKey)
		if sec == nil {
			sec = c.mapNode(m)
			c.set(out, newKey, sec)
			c.mapPtr(newSec, srcSec, true)
		}
		for _, p := range m.Pairs {
			name := keyText(p.Key)
			if strings.HasPrefix(name, "x-") {
				continue
			}
			sp, np := child(srcSec, name), child(newSec, name)
			if cv := conv(name, p.Value, sp, np); cv != nil {
				c.set(sec, name, cv)
			}
		}
	}
	section("definitions", "schemas", func(_ string, v *yaml.Node, sp, np string) *yaml.Node {
		c.mapPtr(np, sp, true)
		return c.schema(v, sp)
	})
	section("parameters", "parameters", func(name string, v *yaml.Node, sp, np string) *yaml.Node {
		m := v.Resolve()
		if m.Kind != yaml.MappingNode {
			c.mapPtr(np, sp, true)
			return c.value(v)
		}
		switch scalarText(m.Get("in")) {
		case "body":
			// A shared request body: operations with the document's
			// consumes use it, others get a copy with their media
			// types.
			rp := child("#/components/requestBodies", name)
			body := c.requestBody(m, sp, rp, c.consumes)
			if sec := get(out, "requestBodies"); sec != nil {
				c.set(sec, name, body)
			} else {
				sec = c.mapNode(root.Get("parameters"))
				c.set(sec, name, body)
				c.set(out, "requestBodies", sec)
			}
			return nil
		case "formData":
			return nil // inlined in each operation's form body
		}
		c.mapPtr(np, sp, true)
		return c.parameter(m, sp, np)
	})
	section("responses", "responses", func(_ string, v *yaml.Node, sp, np string) *yaml.Node {
		c.mapPtr(np, sp, true)
		return c.response(v, sp, np, c.produces)
	})
	section("securityDefinitions", "securitySchemes", func(_ string, v *yaml.Node, sp, np string) *yaml.Node {
		c.mapPtr(np, sp, true)
		return c.securityScheme(v, sp, np)
	})
	if len(out.Pairs) == 0 {
		return nil
	}
	return out
}

// securityScheme converts basic, apiKey and oauth2 schemes.
func (c *swagger2) securityScheme(v *yaml.Node, sp, np string) *yaml.Node {
	m := v.Resolve()
	if m.Kind != yaml.MappingNode {
		return c.value(v)
	}
	out := c.mapNode(m)
	typ := scalarText(m.Get("type"))
	switch typ {
	case "basic":
		c.set(out, "type", c.strNode("http", m.Get("type")))
		c.set(out, "scheme", c.strNode("basic", m.Get("type")))
		c.mapPtr(child(np, "scheme"), child(sp, "type"), false)
		c.copyKeys(out, m, "description")
	case "apiKey":
		c.copyKeys(out, m, "type", "description", "name", "in")
	case "oauth2":
		c.copyKeys(out, m, "type", "description")
		flowN := m.Get("flow").Resolve()
		flow := scalarText(flowN)
		name := map[string]string{"implicit": "implicit", "password": "password", "application": "clientCredentials", "accessCode": "authorizationCode"}[flow]
		if name == "" {
			if flowN == nil {
				c.warn(SeverityWarning, CodeMissingField, m, sp, "The oauth2 security scheme has no flow.")
			} else {
				c.warn(SeverityWarning, CodeInvalidValue, flowN, child(sp, "flow"),
					"The OAuth 2 flow "+strconv.Quote(flow)+" is not implicit, password, application or accessCode.")
			}
			break
		}
		f := c.mapNode(m)
		if name == "implicit" || name == "authorizationCode" {
			c.copyKeys(f, m, "authorizationUrl")
		}
		if name != "implicit" {
			c.copyKeys(f, m, "tokenUrl")
		}
		if s := m.Get("scopes"); s != nil {
			c.set(f, "scopes", c.value(s))
		} else {
			c.set(f, "scopes", c.mapNode(m))
		}
		flows := c.mapNode(flowN)
		c.set(flows, name, f)
		c.set(out, "flows", flows)
		fp := child(child(np, "flows"), name)
		c.mapPtr(child(np, "flows"), child(sp, "flow"), false)
		c.mapPtr(fp, sp, false)
	default:
		c.copyKeys(out, m, "type", "description") // the builder reports it
	}
	c.extensions(out, m)
	return out
}

// --- parameters ---

// parameter converts a path, query or header parameter; sp and np are
// its source and converted pointers.
func (c *swagger2) parameter(m *yaml.Node, sp, np string) *yaml.Node {
	out := c.mapNode(m)
	c.copyKeys(out, m, "name", "in", "description", "required", "allowEmptyValue")
	in := scalarText(m.Get("in"))
	if s := m.Get("schema"); s != nil && m.Get("type") == nil {
		// Not allowed outside body parameters, but written anyway.
		c.set(out, "schema", c.schema(s, child(sp, "schema")))
		c.mapPtr(child(np, "schema"), child(sp, "schema"), false)
	} else {
		c.set(out, "schema", c.itemsSchema(m, sp))
		c.mapPtr(child(np, "schema"), sp, false)
	}
	if scalarText(m.Get("type")) == "array" {
		c.collectionFormat(out, m, sp, in)
	}
	if x := m.Get("x-example"); x != nil {
		c.set(out, "example", c.value(x))
		c.mapPtr(child(np, "example"), child(sp, "x-example"), false)
	}
	for _, p := range m.Pairs {
		if k := keyText(p.Key); strings.HasPrefix(k, "x-") && k != "x-example" {
			c.set(out, k, c.value(p.Value))
		}
	}
	return out
}

// collectionFormat sets style and explode for an array parameter:
// csv (the default) is form (query) or simple (path, header) without
// explode, multi is form with explode, ssv and pipes are spaceDelimited
// and pipeDelimited.
func (c *swagger2) collectionFormat(out, m *yaml.Node, sp, in string) {
	cfN := m.Get("collectionFormat").Resolve()
	cf := "csv"
	at := m
	if cfN != nil {
		cf, at = scalarText(cfN), cfN
	}
	cp := child(sp, "collectionFormat")
	style, explode := "", false
	switch {
	case cf == "csv" && in == InQuery:
		style = "form"
	case cf == "csv":
		style = "simple"
	case cf == "multi" && in == InQuery:
		style, explode = "form", true
	case cf == "ssv" && in == InQuery:
		style = "spaceDelimited"
	case cf == "pipes" && in == InQuery:
		style = "pipeDelimited"
	case cf == "tsv":
		c.warn(SeverityWarning, CodeUnsupported, at, cp, "Tab-separated arrays (collectionFormat tsv) have no OpenAPI 3 style; the values are separated by commas.")
		if in == InQuery {
			style = "form"
		} else {
			style = "simple"
		}
	case cf == "multi" || cf == "ssv" || cf == "pipes":
		c.warn(SeverityWarning, CodeUnsupported, at, cp, "collectionFormat "+cf+" is not allowed for "+in+" parameters; the values are separated by commas.")
		style = "simple"
	default:
		c.warn(SeverityWarning, CodeInvalidValue, at, cp, "The collectionFormat "+strconv.Quote(cf)+" is not csv, ssv, tsv, pipes or multi; csv is used.")
		style = "form"
		if in != InQuery {
			style = "simple"
		}
	}
	c.set(out, "style", c.strNode(style, at))
	c.set(out, "explode", c.boolNode(explode, at))
}

// paramRef is a parameter of an operation or path item: the source
// mapping (the $ref target for references), where it is listed, and its
// location.
type paramRef struct {
	item   *yaml.Node // the list entry as written
	m      *yaml.Node // the parameter object; nil when a $ref is broken
	ptr    string     // source pointer of the list entry
	objPtr string     // source pointer of m
	in     string
	name   string
	global string // the name under #/parameters for a direct reference
}

// params reads a source parameters list, following references.
func (c *swagger2) params(v *yaml.Node, ptr string) []paramRef {
	s := v.Resolve()
	if s == nil {
		return nil
	}
	if s.Kind != yaml.SequenceNode {
		c.warn(SeverityError, CodeInvalidValue, s, ptr, "parameters must be an array, not "+kindName(s)+".")
		return nil
	}
	var out []paramRef
	for i, it := range s.Items {
		ip := index(ptr, i)
		pr := paramRef{item: it, ptr: ip, objPtr: ip}
		m := it.Resolve()
		if ref := refOf(m); ref != nil {
			m, pr.objPtr = c.follow(m)
			if strings.HasPrefix(pr.objPtr, "#/parameters/") && strings.Count(pr.objPtr, "/") == 2 {
				pr.global = pr.objPtr[len("#/parameters/"):]
			}
		}
		if m != nil && m.Kind == yaml.MappingNode {
			pr.m = m
			pr.in, pr.name = scalarText(m.Get("in")), scalarText(m.Get("name"))
		}
		out = append(out, pr)
	}
	return out
}

// follow resolves a chain of local $refs in the source, returning nil
// when one is broken, external or cyclic (the builder reports it from
// the rewritten $ref that is kept).
func (c *swagger2) follow(m *yaml.Node) (*yaml.Node, string) {
	seen := map[*yaml.Node]bool{}
	ptr := ""
	for i := 0; i < 64; i++ {
		ref := refOf(m)
		if ref == nil {
			return m, ptr
		}
		if seen[m] {
			return nil, ""
		}
		seen[m] = true
		r := scalarText(ref)
		if !strings.HasPrefix(r, "#/") {
			return nil, ""
		}
		if u, err := url.PathUnescape(r); err == nil {
			r = u
		}
		cur := c.src
		for _, seg := range strings.Split(r[2:], "/") {
			seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
			if cur.Kind == yaml.SequenceNode {
				i, ok := atoiStrict(seg)
				if !ok || i >= len(cur.Items) {
					return nil, ""
				}
				cur = cur.Items[i].Resolve()
			} else {
				cur = cur.Get(seg).Resolve()
			}
			if cur == nil {
				return nil, ""
			}
		}
		m, ptr = cur, r
	}
	return nil, ""
}

// --- paths and operations ---

func (c *swagger2) paths(v *yaml.Node) *yaml.Node {
	m := v.Resolve()
	if m.Kind != yaml.MappingNode {
		return c.value(v) // the builder reports it
	}
	out := c.mapNode(m)
	for _, p := range m.Pairs {
		k := keyText(p.Key)
		if strings.HasPrefix(k, "x-") {
			c.set(out, k, c.value(p.Value))
			continue
		}
		c.set(out, k, c.pathItem(p.Value, child("#/paths", k)))
	}
	return out
}

func (c *swagger2) pathItem(v *yaml.Node, ptr string) *yaml.Node {
	m := v.Resolve()
	if m.Kind != yaml.MappingNode || refOf(m) != nil {
		return c.ref(v)
	}
	out := c.mapNode(m)
	shared := c.params(m.Get("parameters"), child(ptr, "parameters"))
	if list := c.paramList(shared, child(ptr, "parameters")); list != nil {
		c.set(out, "parameters", list)
	}
	for _, p := range m.Pairs {
		k := keyText(p.Key)
		if isMethod(k) {
			c.set(out, k, c.operation(p.Value, child(ptr, k), shared))
		}
	}
	c.extensions(out, m)
	return out
}

// paramList converts the path, query and header parameters of a list;
// body and formData ones become the request body.
func (c *swagger2) paramList(ps []paramRef, ptr string) *yaml.Node {
	var seq *yaml.Node
	for _, pr := range ps {
		if pr.in == "body" || pr.in == "formData" {
			continue
		}
		if seq == nil {
			seq = c.seqNode(pr.item)
		}
		np := index(ptr, len(seq.Items))
		c.mapPtr(np, pr.ptr, false)
		var n *yaml.Node
		switch {
		case pr.global != "" || pr.m == nil:
			n = c.ref(pr.item) // a $ref, rewritten to the component
		default:
			c.mapPtr(np, pr.objPtr, false)
			n = c.parameter(pr.m, pr.objPtr, np)
		}
		seq.Items = append(seq.Items, n)
	}
	return seq
}

func (c *swagger2) operation(v *yaml.Node, ptr string, shared []paramRef) *yaml.Node {
	m := v.Resolve()
	if m.Kind != yaml.MappingNode {
		return c.value(v)
	}
	out := c.mapNode(m)
	c.copyKeys(out, m, "tags", "summary", "description", "externalDocs", "operationId")
	consumes, produces := c.consumes, c.produces
	if v := m.Get("consumes"); v != nil {
		consumes = c.mediaTypes(v, child(ptr, "consumes"))
	}
	if v := m.Get("produces"); v != nil {
		produces = c.mediaTypes(v, child(ptr, "produces"))
	}
	own := c.params(m.Get("parameters"), child(ptr, "parameters"))
	if list := c.paramList(own, child(ptr, "parameters")); list != nil {
		c.set(out, "parameters", list)
	}
	// The body: the operation's body or form parameters, over the path
	// item's.
	var body *paramRef
	var form []paramRef
	add := func(pr paramRef) {
		switch pr.in {
		case "body":
			if body != nil {
				c.warn(SeverityWarning, CodeDuplicate, pr.item.Resolve(), pr.ptr, "An operation can have only one body parameter; the first is used.")
				return
			}
			body = &pr
		case "formData":
			form = append(form, pr)
		}
	}
	for _, pr := range shared {
		overridden := false
		for _, o := range own {
			overridden = overridden || (o.in == pr.in && (pr.in == "body" || o.name == pr.name))
		}
		if !overridden {
			add(pr)
		}
	}
	for _, pr := range own {
		add(pr)
	}
	rp := child(ptr, "requestBody")
	if body != nil && len(form) > 0 {
		c.warn(SeverityWarning, CodeInvalidValue, form[0].item.Resolve(), form[0].ptr, "An operation cannot have both body and formData parameters; the formData parameters are ignored.")
	}
	switch {
	case body != nil && body.global != "" && sameList(consumes, c.consumes):
		// The shared body, as it is in components.
		rb := c.mapNode(body.item)
		c.set(rb, "$ref", c.strNode("#/components/requestBodies/"+escape(body.global), body.item))
		c.set(out, "requestBody", rb)
		c.mapPtr(rp, body.ptr, false)
	case body != nil:
		c.set(out, "requestBody", c.requestBody(body.m, body.objPtr, rp, consumes))
	case len(form) > 0:
		c.set(out, "requestBody", c.formBody(form, rp, consumes))
	}
	c.set(out, "responses", c.responses(m.Get("responses"), child(ptr, "responses"), produces))
	if s := m.Get("schemes"); s != nil && c.hasHost {
		schemes := c.schemes(s, child(ptr, "schemes"))
		if len(schemes) > 0 && strings.Join(schemes, ",") != strings.Join(c.schemesUsed, ",") {
			c.set(out, "servers", c.serverList(schemes, s, child(ptr, "servers"), child(ptr, "schemes")))
		}
	}
	c.copyKeys(out, m, "deprecated", "security")
	c.extensions(out, m)
	return out
}

func sameList(a, b []string) bool {
	return strings.Join(a, "\x00") == strings.Join(b, "\x00") && len(a) == len(b)
}

// escape escapes a name for a JSON pointer.
func escape(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}

// requestBody converts a body parameter m (at source pointer sp) into a
// request body at np with a media type per consumes entry.
func (c *swagger2) requestBody(m *yaml.Node, sp, np string, consumes []string) *yaml.Node {
	out := c.mapNode(m)
	c.mapPtr(np, sp, false)
	c.copyKeys(out, m, "description", "required")
	if len(consumes) == 0 {
		consumes = []string{"application/json"}
	}
	content := c.mapNode(m)
	schema := m.Get("schema")
	if schema == nil {
		c.warn(SeverityWarning, CodeMissingField, m, sp, "The body parameter "+strconv.Quote(scalarText(m.Get("name")))+" has no schema.")
	}
	examples := m.Get("x-examples").Resolve()
	for i, mt := range consumes {
		media := c.mapNode(m)
		mp := child(child(np, "content"), mt)
		c.mapPtr(child(np, "content"), sp, false)
		c.mapPtr(mp, sp, false)
		if schema != nil {
			c.set(media, "schema", c.schema(schema, child(sp, "schema")))
			c.mapPtr(child(mp, "schema"), child(sp, "schema"), i == 0)
		}
		if ex := examples.Get(mt); ex != nil {
			c.set(media, "example", c.value(ex))
			c.mapPtr(child(mp, "example"), child(child(sp, "x-examples"), mt), false)
		}
		c.set(content, mt, media)
	}
	c.set(out, "content", content)
	c.extensions(out, m)
	return out
}

// formBody makes a form (or multipart, when a parameter is a file) body
// from formData parameters: an object schema with a property each.
func (c *swagger2) formBody(form []paramRef, np string, consumes []string) *yaml.Node {
	at := form[0].item
	out := c.mapNode(at)
	c.mapPtr(np, form[0].ptr, false)
	c.mapPtr(child(np, "content"), form[0].ptr, false)
	file := false
	for _, pr := range form {
		if pr.m != nil && scalarText(pr.m.Get("type")) == "file" {
			file = true
		}
	}
	var mts []string
	for _, mt := range consumes {
		bare := strings.ToLower(strings.TrimSpace(strings.Split(mt, ";")[0]))
		if bare == "multipart/form-data" || (bare == "application/x-www-form-urlencoded" && !file) {
			mts = append(mts, mt)
		}
	}
	if len(mts) == 0 {
		if file {
			mts = []string{"multipart/form-data"}
		} else {
			mts = []string{"application/x-www-form-urlencoded"}
		}
		if len(consumes) > 0 {
			c.warn(SeverityInfo, CodeInvalidValue, at.Resolve(), form[0].ptr,
				"formData parameters need consumes multipart/form-data or application/x-www-form-urlencoded; "+mts[0]+" is used.")
		}
	}
	schema := c.mapNode(at)
	c.set(schema, "type", c.strNode("object", at))
	props := c.mapNode(at)
	required := c.seqNode(at)
	enc := c.mapNode(at)
	for _, pr := range form {
		if pr.m == nil {
			c.warn(SeverityError, CodeBadRef, pr.item.Resolve(), pr.ptr, "The formData parameter's reference cannot be resolved; it is left out.")
			continue
		}
		ps := c.itemsSchema(pr.m, pr.objPtr)
		c.copyKeys(ps, pr.m, "description")
		c.set(props, pr.name, ps)
		for _, mt := range mts {
			c.mapPtr(child(child(child(child(child(np, "content"), mt), "schema"), "properties"), pr.name), pr.objPtr, false)
		}
		if c.boolean(pr.m.Get("required")) {
			required.Items = append(required.Items, c.strNode(pr.name, pr.m.Get("name")))
		}
		if scalarText(pr.m.Get("type")) == "array" {
			e := c.mapNode(pr.m)
			cf := scalarText(pr.m.Get("collectionFormat"))
			c.set(e, "explode", c.boolNode(cf == "multi", pr.m))
			c.set(enc, pr.name, e)
		}
	}
	c.set(schema, "properties", props)
	if len(required.Items) > 0 {
		c.set(schema, "required", required)
		c.set(out, "required", c.boolNode(true, at))
	}
	content := c.mapNode(at)
	for _, mt := range mts {
		mp := child(child(np, "content"), mt)
		c.mapPtr(mp, form[0].ptr, false)
		c.mapPtr(child(mp, "schema"), form[0].ptr, false)
		media := c.mapNode(at)
		c.set(media, "schema", schema)
		if len(enc.Pairs) > 0 {
			c.set(media, "encoding", enc)
		}
		c.set(content, mt, media)
	}
	c.set(out, "content", content)
	return out
}

func (c *swagger2) boolean(v *yaml.Node) bool {
	v = v.Resolve()
	return v != nil && v.Kind == yaml.ScalarNode && strings.EqualFold(v.Value, "true")
}

// --- responses ---

func (c *swagger2) responses(v *yaml.Node, ptr string, produces []string) *yaml.Node {
	m := v.Resolve()
	if m == nil || m.Kind != yaml.MappingNode {
		return c.value(v)
	}
	out := c.mapNode(m)
	for _, p := range m.Pairs {
		k := keyText(p.Key)
		if strings.HasPrefix(k, "x-") {
			c.set(out, k, c.value(p.Value))
			continue
		}
		c.set(out, k, c.response(p.Value, child(ptr, k), child(ptr, k), produces))
	}
	return out
}

// response converts a Response Object: its schema goes under each
// produces media type, its examples to the media types they name, its
// headers get a schema.
func (c *swagger2) response(v *yaml.Node, sp, np string, produces []string) *yaml.Node {
	m := v.Resolve()
	if m.Kind != yaml.MappingNode || refOf(m) != nil {
		return c.ref(v)
	}
	out := c.mapNode(m)
	c.copyKeys(out, m, "description")
	if h := m.Get("headers").Resolve(); h != nil && h.Kind == yaml.MappingNode {
		hs := c.mapNode(h)
		for _, p := range h.Pairs {
			name := keyText(p.Key)
			hsp, hnp := child(child(sp, "headers"), name), child(child(np, "headers"), name)
			hm := p.Value.Resolve()
			if hm.Kind != yaml.MappingNode {
				c.set(hs, name, c.value(p.Value))
				continue
			}
			hdr := c.mapNode(hm)
			c.copyKeys(hdr, hm, "description")
			c.set(hdr, "schema", c.itemsSchema(hm, hsp))
			c.mapPtr(child(hnp, "schema"), hsp, false)
			c.extensions(hdr, hm)
			c.set(hs, name, hdr)
		}
		c.set(out, "headers", hs)
	} else if h != nil {
		c.set(out, "headers", c.value(h))
	}
	schema := m.Get("schema")
	examples := m.Get("examples").Resolve()
	mts := []string{}
	if schema != nil {
		mts = append(mts, produces...)
		if len(mts) == 0 {
			mts = []string{"application/json"}
		}
	}
	for _, p := range pairs(examples) {
		mt := keyText(p.Key)
		found := false
		for _, x := range mts {
			found = found || x == mt
		}
		if !found {
			mts = append(mts, mt)
		}
	}
	if len(mts) > 0 {
		content := c.mapNode(m)
		for i, mt := range mts {
			media := c.mapNode(m)
			mp := child(child(np, "content"), mt)
			c.mapPtr(child(np, "content"), sp, false)
			c.mapPtr(mp, sp, false)
			if schema != nil {
				c.set(media, "schema", c.schema(schema, child(sp, "schema")))
				c.mapPtr(child(mp, "schema"), child(sp, "schema"), i == 0)
			}
			if ex := examples.Get(mt); ex != nil {
				c.set(media, "example", c.value(ex))
				c.mapPtr(child(mp, "example"), child(child(sp, "examples"), mt), false)
			}
			c.set(content, mt, media)
		}
		c.set(out, "content", content)
	}
	c.extensions(out, m)
	return out
}
