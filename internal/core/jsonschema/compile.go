package jsonschema

import (
	"math/big"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// node is a compiled schema at one location of the document.
type node struct {
	loc string // JSON Pointer from the document root
	res *resource

	boolean *bool // a true or false schema

	ref        *node
	dynRef     *node  // the static target of $dynamicRef
	dynAnchor  string // its anchor name, when the target is a $dynamicAnchor
	refIsOnly  bool   // draft-07 and earlier: $ref ignores its siblings
	dynRefWord string // "$dynamicRef" or "$recursiveRef"

	types    []string // "integer" among the JSON types
	enum     []any
	hasEnum  bool
	constV   any
	hasConst bool

	minimum, maximum, exclMin, exclMax *number
	multipleOf                         *number

	minLength, maxLength int // -1 when absent
	pattern              *regexp.Regexp
	patternSrc           string
	format               string

	prefixItems      []*node
	prefixWord       string // "prefixItems" or "items" (array form)
	items            *node
	itemsWord        string // "items" or "additionalItems"
	contains         *node
	minContains      int // 1 unless set
	maxContains      int // -1 when absent
	minItems         int
	maxItems         int
	uniqueItems      bool
	unevaluatedItems *node

	properties            []namedNode
	patternProps          []patternNode
	additional            *node
	propertyNames         *node
	required              []string
	minProps, maxProps    int
	dependentRequired     []dependency
	dependentSchemas      []dependency
	unevaluatedProperties *node

	allOf, anyOf, oneOf []*node
	not                 *node
	ifS, thenS, elseS   *node
}

type namedNode struct {
	name string
	n    *node
}

type patternNode struct {
	src string
	re  *regexp.Regexp
	n   *node
}

// dependency is an entry of dependentRequired or dependentSchemas, or of
// the dependencies keyword that combines them (word).
type dependency struct {
	word     string
	name     string
	required []string
	n        *node
}

// resource is a schema resource: the document root or a subschema with
// an $id.
type resource struct {
	uri        string // absolute, without fragment
	loc        string
	anchors    map[string]string // name → location
	dynAnchors map[string]string
}

type locInfo struct {
	base *url.URL
	res  *resource
}

type compiler struct {
	doc       any
	docs      map[string]any // the documents of Options.Resources loaded, by URI
	remotes   map[string]any // Options.Resources, by normalized URI
	draft     Draft
	opts      Options
	infos     map[string]locInfo
	resources map[string]*resource
	nodes     map[string]*node
	annots    bool
	err       error
}

var bigZero = new(big.Rat)

// rootURI is the base URI of a document without an $id.
const rootURI = "postsonde:///schema.json"

func newCompiler(doc any, opts Options) *compiler {
	c := &compiler{doc: doc, opts: opts, draft: opts.Draft, infos: map[string]locInfo{}, resources: map[string]*resource{}, nodes: map[string]*node{}, docs: map[string]any{}}
	if len(opts.Resources) > 0 {
		c.remotes = make(map[string]any, len(opts.Resources))
		for uri, d := range opts.Resources {
			// A reference resolves to an absolute URI: other keys could
			// never be reached.
			if u, err := url.Parse(uri); err == nil && u.IsAbs() {
				u.Fragment, u.RawFragment = "", ""
				c.remotes[u.String()] = d
			}
		}
	}
	if o, ok := asObject(doc); ok {
		if s, ok := o.Get("$schema"); ok {
			if str, ok := s.(string); ok {
				if d, ok := draftOf(str); ok {
					c.draft = d
				}
			}
		}
	}
	return c
}

func draftOf(uri string) (Draft, bool) {
	switch {
	case strings.Contains(uri, "draft/2020-12"):
		return Draft2020, true
	case strings.Contains(uri, "draft/2019-09"):
		return Draft2019, true
	case strings.Contains(uri, "draft-07"):
		return Draft7, true
	case strings.Contains(uri, "draft-06"):
		return Draft6, true
	case strings.Contains(uri, "draft-04"):
		return Draft4, true
	}
	return 0, false
}

func (c *compiler) compileRoot() (*node, error) {
	base, _ := url.Parse(rootURI)
	rootRes := &resource{uri: rootURI, loc: "", anchors: map[string]string{}, dynAnchors: map[string]string{}}
	c.resources[rootURI] = rootRes
	if err := c.scan(c.doc, "", base, rootRes, true, 0); err != nil {
		return nil, err
	}
	root := c.compile("")
	// Every dynamic anchor can become a $dynamicRef's target at run time.
	// Compiling one may load another document, with anchors of its own.
	for done := 0; done != len(c.resources); {
		done = len(c.resources)
		for _, res := range c.resources {
			for _, loc := range res.dynAnchors {
				c.compile(loc)
			}
		}
	}
	if c.err != nil {
		return nil, c.err
	}
	return root, nil
}

// maxSchemaDepth bounds the nesting of the schema document.
const maxSchemaDepth = 1000

// idWord is the keyword that declares a resource's URI.
func (c *compiler) idWord() string {
	if c.draft == Draft4 {
		return "id"
	}
	return "$id"
}

// schemaKeywords are the keywords whose values are, or hold, subschemas.
// Below any other keyword (an unknown one), $id and anchors are not
// identifiers, though a JSON Pointer may still point there.
var schemaKeywords = map[string]bool{
	"properties": true, "patternProperties": true, "$defs": true, "definitions": true,
	"dependentSchemas": true, "dependencies": true, "allOf": true, "anyOf": true, "oneOf": true,
	"prefixItems": true, "items": true, "additionalItems": true, "additionalProperties": true,
	"unevaluatedItems": true, "unevaluatedProperties": true, "contains": true,
	"propertyNames": true, "not": true, "if": true, "then": true, "else": true, "contentSchema": true,
}

// scan records the base URI and resource of every schema location and
// registers the $id resources and anchors, before anything is compiled,
// so references can point forward. ids is false below an unknown
// keyword.
func (c *compiler) scan(v any, loc string, base *url.URL, res *resource, ids bool, depth int) error {
	if depth > maxSchemaDepth {
		return invalidSchema(loc, "the schema is nested too deeply")
	}
	o, ok := asObject(v)
	if !ok {
		if _, isBool := v.(bool); isBool {
			c.infos[loc] = locInfo{base: base, res: res}
		}
		return nil
	}
	if id, ok := o.Get(c.idWord()); ok && ids {
		if s, ok := id.(string); ok && s != "" {
			if strings.HasPrefix(s, "#") {
				// draft-07 and earlier: "$id": "#name" is an anchor. Later
				// drafts do not allow a fragment in $id.
				if c.draft >= Draft7 && len(s) > 1 {
					res.anchors[s[1:]] = loc
				}
			} else if _, isRef := o.Get("$ref"); !(isRef && c.draft >= Draft7) {
				u, err := url.Parse(s)
				if err != nil {
					return invalidSchema(loc+"/"+c.idWord(), c.idWord()+" is not a valid URI reference")
				}
				nb := base.ResolveReference(u)
				nb.Fragment, nb.RawFragment = "", ""
				key := nb.String()
				if prev, ok := c.resources[key]; ok && prev.loc != loc {
					return invalidSchema(loc+"/"+c.idWord(), "the URI "+quote(key)+" is declared twice")
				}
				res = &resource{uri: key, loc: loc, anchors: map[string]string{}, dynAnchors: map[string]string{}}
				c.resources[key] = res
				base = nb
			}
		}
	}
	c.infos[loc] = locInfo{base: base, res: res}
	if a, ok := o.Get("$anchor"); ok && ids {
		if s, ok := a.(string); ok && s != "" {
			res.anchors[s] = loc
		}
	}
	if c.draft == Draft2020 && ids {
		if a, ok := o.Get("$dynamicAnchor"); ok {
			if s, ok := a.(string); ok && s != "" {
				res.anchors[s] = loc
				res.dynAnchors[s] = loc
			}
		}
	}
	for i := range o.Len() {
		name, val := o.Member(i)
		sub := loc + "/" + escapeToken(name)
		subIDs := ids && schemaKeywords[name]
		switch name {
		case "enum", "const", "default", "examples", "required", "type", "dependentRequired", "$comment", "title", "description":
			continue
		case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas":
			if m, ok := asObject(val); ok {
				for j := range m.Len() {
					k, sv := m.Member(j)
					if err := c.scan(sv, sub+"/"+escapeToken(k), base, res, subIDs, depth+1); err != nil {
						return err
					}
				}
			}
			continue
		case "dependencies":
			if m, ok := asObject(val); ok {
				for j := range m.Len() {
					k, sv := m.Member(j)
					if _, isArr := sv.([]any); !isArr {
						if err := c.scan(sv, sub+"/"+escapeToken(k), base, res, subIDs, depth+1); err != nil {
							return err
						}
					}
				}
			}
			continue
		case "allOf", "anyOf", "oneOf", "prefixItems", "items":
			if arr, ok := val.([]any); ok {
				for j, sv := range arr {
					if err := c.scan(sv, sub+"/"+strconv.Itoa(j), base, res, subIDs, depth+1); err != nil {
						return err
					}
				}
				continue
			}
		}
		// Subschema keywords, and unknown keywords holding an object,
		// which a $ref may point into.
		if err := c.scan(val, sub, base, res, subIDs, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// A location is a JSON Pointer into the schema document ("",
// "/$defs/a"), or the URI of a document of Options.Resources, "#" and a
// JSON Pointer into it ("http://example.com/a.json#/$defs/a").

// splitLoc returns the document of loc and the pointer into it.
func (c *compiler) splitLoc(loc string) (any, string) {
	if loc == "" || loc[0] == '/' {
		return c.doc, loc
	}
	i := strings.IndexByte(loc, '#')
	if i < 0 {
		return nil, ""
	}
	return c.docs[loc[:i]], loc[i+1:]
}

// lookup returns the raw value at loc.
func (c *compiler) lookup(loc string) (any, bool) {
	v, ptr := c.splitLoc(loc)
	if v == nil {
		return nil, false
	}
	if ptr == "" {
		return v, true
	}
	for tok := range strings.SplitSeq(ptr[1:], "/") {
		tok = unescapeToken(tok)
		if o, ok := asObject(v); ok {
			if v, ok = o.Get(tok); !ok {
				return nil, false
			}
			continue
		}
		if a, ok := v.([]any); ok {
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(a) || strconv.Itoa(i) != tok {
				return nil, false
			}
			v = a[i]
			continue
		}
		return nil, false
	}
	return v, true
}

// info returns the base and resource of loc, from the nearest scanned
// location at or above it.
func (c *compiler) info(loc string) locInfo {
	for {
		if in, ok := c.infos[loc]; ok {
			return in
		}
		i := strings.LastIndexByte(loc, '/')
		if i < 0 {
			return c.infos[""]
		}
		loc = loc[:i]
	}
}

// compile returns the node at loc, compiling it once. Errors are kept in
// c.err; the first one wins.
func (c *compiler) compile(loc string) *node {
	if n, ok := c.nodes[loc]; ok {
		return n
	}
	in := c.info(loc)
	n := &node{loc: loc, res: in.res, minLength: -1, maxLength: -1, maxContains: -1, minContains: 1, maxItems: -1, maxProps: -1}
	c.nodes[loc] = n
	raw, _ := c.lookup(loc)
	if c.err == nil {
		if err := c.fill(n, raw, in); err != nil {
			c.err = err
		}
	}
	return n
}

func (c *compiler) sub(loc string, v any) (*node, error) {
	if _, ok := v.(bool); !ok {
		if _, ok := asObject(v); !ok {
			return nil, invalidSchema(loc, "a schema must be an object or a boolean")
		}
	}
	return c.compile(loc), nil
}

func (c *compiler) fill(n *node, raw any, in locInfo) error {
	if b, ok := raw.(bool); ok {
		n.boolean = &b
		return nil
	}
	o, ok := asObject(raw)
	if !ok {
		return invalidSchema(n.loc, "a schema must be an object or a boolean")
	}
	get := func(name string) (any, bool) { return o.Get(name) }
	at := func(name string) string { return n.loc + "/" + escapeToken(name) }

	if r, ok := get("$ref"); ok {
		s, ok := r.(string)
		if !ok {
			return invalidSchema(at("$ref"), "$ref must be a string")
		}
		target, err := c.resolve(in.base, s, at("$ref"))
		if err != nil {
			return err
		}
		n.ref = c.compile(target)
		if c.draft >= Draft7 {
			n.refIsOnly = true
			return nil
		}
	}
	for _, word := range []string{"$dynamicRef", "$recursiveRef"} {
		r, ok := get(word)
		if !ok || (word == "$dynamicRef") != (c.draft == Draft2020) || (word == "$recursiveRef") != (c.draft == Draft2019) {
			continue
		}
		s, ok := r.(string)
		if !ok {
			return invalidSchema(at(word), word+" must be a string")
		}
		target, err := c.resolve(in.base, s, at(word))
		if err != nil {
			return err
		}
		n.dynRef, n.dynRefWord = c.compile(target), word
		if word == "$dynamicRef" {
			if i := strings.IndexByte(s, '#'); i >= 0 {
				name := s[i+1:]
				if tr := c.info(target).res; tr != nil && !strings.HasPrefix(name, "/") && tr.dynAnchors[name] == target {
					n.dynAnchor = name
				}
			}
		}
	}

	if t, ok := get("type"); ok {
		switch t := t.(type) {
		case string:
			n.types = []string{t}
		case []any:
			for _, e := range t {
				s, ok := e.(string)
				if !ok {
					return invalidSchema(at("type"), "type must be a type name or an array of them")
				}
				n.types = append(n.types, s)
			}
		default:
			return invalidSchema(at("type"), "type must be a type name or an array of them")
		}
		for _, s := range n.types {
			switch s {
			case "null", "boolean", "object", "array", "number", "string", "integer":
			default:
				return invalidSchema(at("type"), quote(s)+" is not a JSON Schema type")
			}
		}
	}
	if e, ok := get("enum"); ok {
		arr, ok := e.([]any)
		if !ok {
			return invalidSchema(at("enum"), "enum must be an array")
		}
		n.enum, n.hasEnum = arr, true
	}
	if v, ok := get("const"); ok {
		n.constV, n.hasConst = v, true
	}

	// Numbers.
	var err error
	num := func(name string) *number {
		v, ok := get(name)
		if !ok || err != nil {
			return nil
		}
		x, ok := asNumber(v)
		if !ok {
			err = invalidSchema(at(name), name+" must be a number")
			return nil
		}
		return &x
	}
	n.minimum, n.maximum = num("minimum"), num("maximum")
	if c.draft == Draft4 {
		for _, w := range []string{"exclusiveMinimum", "exclusiveMaximum"} {
			v, ok := get(w)
			if !ok {
				continue
			}
			b, ok := v.(bool)
			if !ok {
				return invalidSchema(at(w), w+" must be a boolean in draft-04")
			}
			if b && w == "exclusiveMinimum" {
				n.exclMin, n.minimum = n.minimum, nil
			} else if b {
				n.exclMax, n.maximum = n.maximum, nil
			}
		}
	} else {
		n.exclMin, n.exclMax = num("exclusiveMinimum"), num("exclusiveMaximum")
	}
	n.multipleOf = num("multipleOf")
	if err != nil {
		return err
	}
	if n.multipleOf != nil && n.multipleOf.cmp(number{r: bigZero, f: 0}) <= 0 {
		return invalidSchema(at("multipleOf"), "multipleOf must be greater than 0")
	}

	count := func(name string, dst *int) {
		v, ok := get(name)
		if !ok || err != nil {
			return
		}
		x, ok := asNumber(v)
		if !ok || !x.isInt() || x.f < 0 {
			err = invalidSchema(at(name), name+" must be a non-negative integer")
			return
		}
		if x.f > 1<<53 {
			*dst = 1 << 53
			return
		}
		*dst = int(x.f)
	}
	count("minLength", &n.minLength)
	count("maxLength", &n.maxLength)
	count("minItems", &n.minItems)
	count("maxItems", &n.maxItems)
	count("minProperties", &n.minProps)
	count("maxProperties", &n.maxProps)
	count("minContains", &n.minContains)
	count("maxContains", &n.maxContains)
	if err != nil {
		return err
	}
	if c.draft > Draft2019 {
		// minContains and maxContains are 2019-09 keywords.
		n.minContains, n.maxContains = 1, -1
	}
	if p, ok := get("pattern"); ok {
		s, ok := p.(string)
		if !ok {
			return invalidSchema(at("pattern"), "pattern must be a string")
		}
		re, err := compilePattern(s, at("pattern"))
		if err != nil {
			return err
		}
		n.pattern, n.patternSrc = re, s
	}
	if f, ok := get("format"); ok {
		s, ok := f.(string)
		if !ok {
			return invalidSchema(at("format"), "format must be a string")
		}
		n.format = s
	}

	// Arrays.
	if v, ok := get("prefixItems"); ok && c.draft == Draft2020 {
		arr, ok := v.([]any)
		if !ok {
			return invalidSchema(at("prefixItems"), "prefixItems must be an array of schemas")
		}
		for i, sv := range arr {
			s, err := c.sub(at("prefixItems")+"/"+strconv.Itoa(i), sv)
			if err != nil {
				return err
			}
			n.prefixItems = append(n.prefixItems, s)
		}
		n.prefixWord = "prefixItems"
	}
	if v, ok := get("items"); ok {
		if arr, isArr := v.([]any); isArr {
			// The tuple form of draft 2019-09 and earlier, accepted in
			// 2020-12 too.
			n.prefixItems, n.prefixWord = nil, "items"
			for i, sv := range arr {
				s, err := c.sub(at("items")+"/"+strconv.Itoa(i), sv)
				if err != nil {
					return err
				}
				n.prefixItems = append(n.prefixItems, s)
			}
			if av, ok := get("additionalItems"); ok {
				if n.items, err = c.sub(at("additionalItems"), av); err != nil {
					return err
				}
				n.itemsWord = "additionalItems"
			}
		} else {
			if n.items, err = c.sub(at("items"), v); err != nil {
				return err
			}
			n.itemsWord = "items"
		}
	}
	if v, ok := get("contains"); ok {
		if n.contains, err = c.sub(at("contains"), v); err != nil {
			return err
		}
	}
	if v, ok := get("uniqueItems"); ok {
		b, ok := v.(bool)
		if !ok {
			return invalidSchema(at("uniqueItems"), "uniqueItems must be a boolean")
		}
		n.uniqueItems = b
	}
	if v, ok := get("unevaluatedItems"); ok && c.draft <= Draft2019 {
		if n.unevaluatedItems, err = c.sub(at("unevaluatedItems"), v); err != nil {
			return err
		}
		c.annots = true
	}

	// Objects.
	schemaMap := func(name string, fn func(k string, s *node)) error {
		v, ok := get(name)
		if !ok {
			return nil
		}
		m, ok := asObject(v)
		if !ok {
			return invalidSchema(at(name), name+" must be an object")
		}
		for i := range m.Len() {
			k, sv := m.Member(i)
			s, err := c.sub(at(name)+"/"+escapeToken(k), sv)
			if err != nil {
				return err
			}
			fn(k, s)
		}
		return nil
	}
	if err := schemaMap("properties", func(k string, s *node) { n.properties = append(n.properties, namedNode{k, s}) }); err != nil {
		return err
	}
	if v, ok := get("patternProperties"); ok {
		m, ok := asObject(v)
		if !ok {
			return invalidSchema(at("patternProperties"), "patternProperties must be an object")
		}
		for i := range m.Len() {
			k, sv := m.Member(i)
			re, err := compilePattern(k, at("patternProperties")+"/"+escapeToken(k))
			if err != nil {
				return err
			}
			s, err := c.sub(at("patternProperties")+"/"+escapeToken(k), sv)
			if err != nil {
				return err
			}
			n.patternProps = append(n.patternProps, patternNode{src: k, re: re, n: s})
		}
	}
	if v, ok := get("additionalProperties"); ok {
		if n.additional, err = c.sub(at("additionalProperties"), v); err != nil {
			return err
		}
	}
	if v, ok := get("propertyNames"); ok {
		if n.propertyNames, err = c.sub(at("propertyNames"), v); err != nil {
			return err
		}
	}
	if v, ok := get("required"); ok {
		names, ok := stringList(v)
		if !ok {
			return invalidSchema(at("required"), "required must be an array of strings")
		}
		n.required = names
	}
	if v, ok := get("dependentRequired"); ok && c.draft <= Draft2019 {
		m, ok := asObject(v)
		if !ok {
			return invalidSchema(at("dependentRequired"), "dependentRequired must be an object")
		}
		for i := range m.Len() {
			k, dv := m.Member(i)
			names, ok := stringList(dv)
			if !ok {
				return invalidSchema(at("dependentRequired")+"/"+escapeToken(k), "dependentRequired values must be arrays of strings")
			}
			n.dependentRequired = append(n.dependentRequired, dependency{word: "dependentRequired", name: k, required: names})
		}
	}
	if c.draft <= Draft2019 {
		if err := schemaMap("dependentSchemas", func(k string, s *node) {
			n.dependentSchemas = append(n.dependentSchemas, dependency{word: "dependentSchemas", name: k, n: s})
		}); err != nil {
			return err
		}
	}
	// dependencies is the draft-07 and earlier keyword; later drafts split
	// it in two, and it is still applied there, for compatibility.
	if v, ok := get("dependencies"); ok {
		m, ok := asObject(v)
		if !ok {
			return invalidSchema(at("dependencies"), "dependencies must be an object")
		}
		for i := range m.Len() {
			k, dv := m.Member(i)
			if names, ok := stringList(dv); ok {
				n.dependentRequired = append(n.dependentRequired, dependency{word: "dependencies", name: k, required: names})
				continue
			}
			s, err := c.sub(at("dependencies")+"/"+escapeToken(k), dv)
			if err != nil {
				return err
			}
			n.dependentSchemas = append(n.dependentSchemas, dependency{word: "dependencies", name: k, n: s})
		}
	}
	if v, ok := get("unevaluatedProperties"); ok && c.draft <= Draft2019 {
		if n.unevaluatedProperties, err = c.sub(at("unevaluatedProperties"), v); err != nil {
			return err
		}
		c.annots = true
	}

	// Applicators.
	list := func(name string) ([]*node, error) {
		v, ok := get(name)
		if !ok {
			return nil, nil
		}
		arr, ok := v.([]any)
		if !ok || len(arr) == 0 {
			return nil, invalidSchema(at(name), name+" must be a non-empty array of schemas")
		}
		out := make([]*node, len(arr))
		for i, sv := range arr {
			s, err := c.sub(at(name)+"/"+strconv.Itoa(i), sv)
			if err != nil {
				return nil, err
			}
			out[i] = s
		}
		return out, nil
	}
	if n.allOf, err = list("allOf"); err != nil {
		return err
	}
	if n.anyOf, err = list("anyOf"); err != nil {
		return err
	}
	if n.oneOf, err = list("oneOf"); err != nil {
		return err
	}
	for _, kw := range []struct {
		name string
		dst  **node
	}{{"not", &n.not}, {"if", &n.ifS}, {"then", &n.thenS}, {"else", &n.elseS}} {
		if kw.name != "not" && c.draft == Draft6 || kw.name != "not" && c.draft == Draft4 {
			continue
		}
		if v, ok := get(kw.name); ok {
			if *kw.dst, err = c.sub(at(kw.name), v); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolve returns the location a reference points to.
func (c *compiler) resolve(base *url.URL, ref, loc string) (string, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return "", unresolved(loc, ref, "it is not a valid URI reference")
	}
	abs := base.ResolveReference(u)
	frag := abs.Fragment
	abs.Fragment, abs.RawFragment = "", ""
	res, ok := c.resources[abs.String()]
	if !ok {
		if err := c.load(abs); err != nil {
			return "", err
		}
		if res, ok = c.resources[abs.String()]; !ok {
			if len(c.remotes) > 0 {
				return "", unresolved(loc, ref, "it points outside the schema and the resources given")
			}
			return "", unresolved(loc, ref, "it points outside the schema; only references inside it are resolved")
		}
	}
	switch {
	case frag == "":
		return res.loc, nil
	case strings.HasPrefix(frag, "/"):
		target := res.loc + frag
		if _, ok := c.lookup(target); !ok {
			return "", unresolved(loc, ref, "there is nothing at "+fragment(target))
		}
		return target, nil
	}
	target, ok := res.anchors[frag]
	if !ok {
		return "", unresolved(loc, ref, "no $anchor is named "+quote(frag))
	}
	return target, nil
}

// load scans the document of Options.Resources at uri, if there is one
// not loaded yet, registering its resources and anchors.
func (c *compiler) load(uri *url.URL) error {
	key := uri.String()
	d, ok := c.remotes[key]
	if !ok {
		return nil
	}
	if _, loaded := c.docs[key]; loaded {
		return nil
	}
	c.docs[key] = d
	loc := key + "#"
	res := &resource{uri: key, loc: loc, anchors: map[string]string{}, dynAnchors: map[string]string{}}
	if _, taken := c.resources[key]; !taken {
		c.resources[key] = res
	}
	base := *uri
	return c.scan(d, loc, &base, res, true, 0)
}

func stringList(v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return slices.Clip(out), true
}

// fragment renders a location as a URI fragment, "#/properties/id", or,
// in a document of Options.Resources, a URI with one,
// "http://example.com/a.json#/$defs/a".
func fragment(loc string) string {
	if loc == "" || loc[0] == '/' {
		return "#" + loc
	}
	return loc
}

func quote(s string) string { return strconv.Quote(s) }

func invalidSchema(loc, msg string) error {
	return errs.New(errs.InvalidArgument, "The JSON schema is not valid at "+fragment(loc)+": "+msg+".").
		WithCode(CodeInvalidSchema).
		WithDetail("location", fragment(loc))
}

func unresolved(loc, ref, why string) error {
	return errs.New(errs.InvalidArgument, "The reference "+quote(ref)+" at "+fragment(loc)+" cannot be resolved: "+why+".").
		WithCode(CodeUnresolvedRef).
		WithHint("Put the referenced schema under $defs in the same schema.").
		WithDetail("location", fragment(loc)).
		WithDetail("ref", ref)
}
