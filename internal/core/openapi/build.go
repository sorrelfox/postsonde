package openapi

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// builder turns the document tree into the model. Every object that can
// be the target of a $ref is memoized by its tree node, and a memo entry
// is made before the object is filled in, so references (including
// recursive ones) resolve to shared pointers and each node is built once.
type builder struct {
	root  *yaml.Node
	minor int
	lim   Limits
	diags []Diagnostic

	schemas     map[*yaml.Node]*Schema
	schemaOrder []*Schema // every memoized schema, in creation order
	// pending fills objects reached through $ref after the object being
	// built, so references never nest on the Go stack. The post phases
	// run after that: parameter merging, then copies of shared objects
	// (paths on referenced path items, 3.1 description overrides), which
	// need their sources complete.
	pending    []func()
	post1      []func()
	post2      []func()
	pathSet    map[*PathItem]bool
	noted      map[*yaml.Node]bool // 3.0 $ref siblings already reported
	params     map[*yaml.Node]*Parameter
	responses  map[*yaml.Node]*Response
	bodies     map[*yaml.Node]*RequestBody
	headers    map[*yaml.Node]*Header
	examples   map[*yaml.Node]*Example
	schemes    map[*yaml.Node]*SecurityScheme
	callbacks  map[*yaml.Node]*Callback
	pathItems  map[*yaml.Node]*PathItem
	values     map[*yaml.Node]json.RawMessage
	anchors    map[string]anchor
	refs       int
	valueBytes int
	limitHit   map[string]bool
	opIDs      map[string]bool
	secUses    []securityUse
	// srcPtr maps the pointers of a converted Swagger 2.0 tree back to
	// the source document; nil for OpenAPI 3.
	srcPtr func(string) string
}

func newBuilder(root *yaml.Node, minor int, lim Limits) *builder {
	b := &builder{
		root: root, minor: minor, lim: lim,
		schemas:   map[*yaml.Node]*Schema{},
		pathSet:   map[*PathItem]bool{},
		noted:     map[*yaml.Node]bool{},
		params:    map[*yaml.Node]*Parameter{},
		responses: map[*yaml.Node]*Response{},
		bodies:    map[*yaml.Node]*RequestBody{},
		headers:   map[*yaml.Node]*Header{},
		examples:  map[*yaml.Node]*Example{},
		schemes:   map[*yaml.Node]*SecurityScheme{},
		callbacks: map[*yaml.Node]*Callback{},
		pathItems: map[*yaml.Node]*PathItem{},
		values:    map[*yaml.Node]json.RawMessage{},
		limitHit:  map[string]bool{},
		opIDs:     map[string]bool{},
	}
	expandMerges(root)
	return b
}

// --- diagnostics and positions ---

func pos(n *yaml.Node, ptr string) Pos {
	if n == nil {
		return Pos{Pointer: ptr}
	}
	return Pos{Line: n.Line, Column: n.Column, Pointer: ptr}
}

// pos is the position of n, with ptr mapped to the source document.
func (b *builder) pos(n *yaml.Node, ptr string) Pos {
	if b.srcPtr != nil {
		ptr = b.srcPtr(ptr)
	}
	return pos(n, ptr)
}

// refText is a $ref (or a pointer) as the source document writes it: a
// rewritten Swagger 2.0 reference maps back to the Swagger one.
func (b *builder) refText(ref string) string {
	if b.srcPtr != nil && strings.HasPrefix(ref, "#/") {
		return b.srcPtr(ref)
	}
	return ref
}

func (b *builder) diag(sev Severity, code string, n *yaml.Node, ptr, msg string) {
	b.diags = append(b.diags, Diagnostic{Severity: sev, Code: code, Message: msg, Pos: b.pos(n, ptr)})
}

// limit reports a reached limit once per kind.
func (b *builder) limit(kind string, n *yaml.Node, ptr, msg string) {
	if b.limitHit[kind] {
		return
	}
	b.limitHit[kind] = true
	b.diag(SeverityError, CodeLimit, n, ptr, msg)
}

// child returns the JSON pointer of member key of ptr.
func child(ptr, key string) string {
	key = strings.ReplaceAll(key, "~", "~0")
	key = strings.ReplaceAll(key, "/", "~1")
	return ptr + "/" + key
}

func index(ptr string, i int) string { return ptr + "/" + strconv.Itoa(i) }

// --- tree access ---

func keyText(k *yaml.Node) string {
	k = k.Resolve()
	if k == nil {
		return ""
	}
	return k.Value
}

// pairs returns the entries of a mapping node (nil for other nodes).
func pairs(n *yaml.Node) []yaml.Pair {
	n = n.Resolve()
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	return n.Pairs
}

func kindName(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "an object"
	case yaml.SequenceNode:
		return "an array"
	}
	switch n.Tag {
	case yaml.TagNull:
		return "null"
	case yaml.TagBool:
		return "a boolean"
	case yaml.TagInt, yaml.TagFloat:
		return "a number"
	}
	return "a string"
}

// mapping returns v as a mapping node, or nil with a diagnostic when it is
// something else. A null value is an absent one.
func (b *builder) mapping(v *yaml.Node, ptr, what string) *yaml.Node {
	v = v.Resolve()
	if v == nil || (v.Kind == yaml.ScalarNode && v.Tag == yaml.TagNull) {
		return nil
	}
	if v.Kind != yaml.MappingNode {
		b.diag(SeverityError, CodeInvalidValue, v, ptr, what+" must be an object, not "+kindName(v)+".")
		return nil
	}
	return v
}

// sequence returns v's items, or nil with a diagnostic when v is not a
// sequence.
func (b *builder) sequence(v *yaml.Node, ptr, what string) []*yaml.Node {
	v = v.Resolve()
	if v == nil || (v.Kind == yaml.ScalarNode && v.Tag == yaml.TagNull) {
		return nil
	}
	if v.Kind != yaml.SequenceNode {
		b.diag(SeverityError, CodeInvalidValue, v, ptr, what+" must be an array, not "+kindName(v)+".")
		return nil
	}
	return v.Items
}

// str returns a scalar's text. Numbers and booleans are accepted as their
// text (version: 1.0); null is "".
func (b *builder) str(v *yaml.Node, ptr, what string) string {
	v = v.Resolve()
	if v == nil {
		return ""
	}
	if v.Kind != yaml.ScalarNode {
		b.diag(SeverityError, CodeInvalidValue, v, ptr, what+" must be a string, not "+kindName(v)+".")
		return ""
	}
	if v.Tag == yaml.TagNull {
		return ""
	}
	return v.Value
}

func (b *builder) boolean(v *yaml.Node, ptr, what string) bool {
	p := b.boolPtr(v, ptr, what)
	return p != nil && *p
}

func (b *builder) boolPtr(v *yaml.Node, ptr, what string) *bool {
	v = v.Resolve()
	if v == nil || (v.Kind == yaml.ScalarNode && v.Tag == yaml.TagNull) {
		return nil
	}
	if v.Kind == yaml.ScalarNode && v.Tag == yaml.TagBool {
		t := strings.EqualFold(v.Value, "true")
		return &t
	}
	// "true"/"false" strings are common enough to accept with a note.
	if v.Kind == yaml.ScalarNode && (v.Value == "true" || v.Value == "false") {
		b.diag(SeverityWarning, CodeInvalidValue, v, ptr, what+" must be a boolean, not a string; read as "+v.Value+".")
		t := v.Value == "true"
		return &t
	}
	b.diag(SeverityError, CodeInvalidValue, v, ptr, what+" must be a boolean, not "+kindName(v)+".")
	return nil
}

// intPtr reads a non-negative integer.
func (b *builder) intPtr(v *yaml.Node, ptr, what string) *int {
	v = v.Resolve()
	if v == nil || (v.Kind == yaml.ScalarNode && v.Tag == yaml.TagNull) {
		return nil
	}
	if v.Kind == yaml.ScalarNode && (v.Tag == yaml.TagInt || v.Tag == yaml.TagFloat) {
		if f, err := strconv.ParseFloat(normalNumber(v), 64); err == nil && f >= 0 && f == float64(int(f)) && f < 1<<53 {
			n := int(f)
			return &n
		}
	}
	b.diag(SeverityError, CodeInvalidValue, v, ptr, what+" must be a non-negative integer.")
	return nil
}

// number reads a number as JSON text.
func (b *builder) number(v *yaml.Node, ptr, what string) json.Number {
	v = v.Resolve()
	if v == nil || (v.Kind == yaml.ScalarNode && v.Tag == yaml.TagNull) {
		return ""
	}
	if v.Kind == yaml.ScalarNode && (v.Tag == yaml.TagInt || v.Tag == yaml.TagFloat) {
		if s := normalNumber(v); s != "" {
			return json.Number(s)
		}
	}
	b.diag(SeverityError, CodeInvalidValue, v, ptr, what+" must be a number.")
	return ""
}

// normalNumber returns a numeric scalar as JSON number text ("" for .inf
// and .nan).
func normalNumber(v *yaml.Node) string {
	js, err := v.JSON()
	if err != nil {
		return ""
	}
	return string(js)
}

func (b *builder) strList(v *yaml.Node, ptr, what string) []string {
	items := b.sequence(v, ptr, what)
	if items == nil {
		return nil
	}
	out := make([]string, 0, len(items))
	for i, it := range items {
		out = append(out, b.str(it, index(ptr, i), what+" entries"))
	}
	return out
}

// value returns any value as JSON text, keeping key order. Values are
// memoized by node and their total size is bounded by MaxValueBytes.
func (b *builder) value(v *yaml.Node, ptr string) json.RawMessage {
	v = v.Resolve()
	if v == nil {
		return nil
	}
	if js, ok := b.values[v]; ok {
		return js
	}
	if b.valueBytes > b.lim.MaxValueBytes {
		b.limit("values", v, ptr, "Example and default values are larger than "+strconv.Itoa(b.lim.MaxValueBytes)+" bytes in total; the rest are left out.")
		return nil
	}
	js, err := v.JSONLimits(yaml.Limits{MaxBytes: b.lim.MaxBytes, MaxDepth: b.lim.MaxDepth, MaxNodes: b.lim.MaxNodes})
	if err != nil {
		msg := err.Error()
		if ye, ok := err.(*yaml.Error); ok {
			msg = ye.Message
		}
		b.diag(SeverityError, CodeInvalidValue, v, ptr, "The value cannot be read as JSON: "+msg+".")
		b.values[v] = nil
		return nil
	}
	b.valueBytes += len(js)
	b.values[v] = js
	return js
}

// extensions collects the x- members of a mapping.
func (b *builder) extensions(m *yaml.Node, ptr string) Extensions {
	var out Extensions
	for _, p := range pairs(m) {
		if k := keyText(p.Key); strings.HasPrefix(k, "x-") {
			out = append(out, Entry[json.RawMessage]{Name: k, Value: b.value(p.Value, child(ptr, k))})
		}
	}
	return out
}

// --- references ---

// refOf returns the $ref member of a mapping node, or nil.
func refOf(n *yaml.Node) *yaml.Node {
	n = n.Resolve()
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	return n.Get("$ref")
}

// deref follows a chain of $refs starting at the mapping n (which has a
// $ref). It returns the first node of the chain that is not a reference,
// and its pointer. When stop is given, the chain also ends at a node for
// which stop returns true (a 3.1 schema with keywords next to $ref). On
// failure it reports a diagnostic and returns ok false with the $ref text
// that failed.
func (b *builder) deref(n *yaml.Node, ptr string, stop func(*yaml.Node) bool) (target *yaml.Node, tptr, ref string, ok bool) {
	seen := map[*yaml.Node]bool{}
	cur, curPtr := n.Resolve(), ptr
	for {
		refNode := refOf(cur)
		if refNode == nil || (cur != n.Resolve() && stop != nil && stop(cur)) {
			return cur, curPtr, "", true
		}
		refPtr := child(curPtr, "$ref")
		if seen[cur] {
			b.diag(SeverityError, CodeRefCycle, n, ptr, "The reference "+strconv.Quote(b.refText(scalarText(refOf(n))))+" leads to a cycle of references with no object at the end.")
			return nil, "", scalarText(refOf(n)), false
		}
		seen[cur] = true
		b.refs++
		if b.refs > b.lim.MaxRefs {
			b.limit("refs", refNode, refPtr, "The document needs more than "+strconv.Itoa(b.lim.MaxRefs)+" reference resolutions; the rest are left unresolved.")
			return nil, "", scalarText(refNode), false
		}
		rv := refNode.Resolve()
		if rv.Kind != yaml.ScalarNode || rv.Tag != yaml.TagStr {
			b.diag(SeverityError, CodeBadRef, rv, refPtr, "$ref must be a string, not "+kindName(rv)+".")
			return nil, "", "", false
		}
		t, tp, msg, sev, code := b.lookup(rv.Value)
		if t == nil {
			if b.refs <= b.lim.MaxRefs { // over the limit, it is reported once
				b.diag(sev, code, rv, refPtr, msg)
			}
			return nil, "", rv.Value, false
		}
		cur, curPtr = t, tp
	}
}

// lookup finds the node a $ref names. Only local references are
// supported: "#", "#/json/pointer" and "#anchor" (a 3.1 $anchor).
func (b *builder) lookup(ref string) (n *yaml.Node, ptr, msg string, sev Severity, code string) {
	hash := strings.IndexByte(ref, '#')
	if hash != 0 {
		return nil, "", "The reference " + strconv.Quote(b.refText(ref)) + " points outside this document; only references within the document (#/...) are resolved.",
			SeverityWarning, CodeExternalRef
	}
	frag := ref[1:]
	if u, err := url.PathUnescape(frag); err == nil {
		frag = u
	}
	switch {
	case frag == "":
		return b.root, "#", "", "", ""
	case frag[0] == '/':
		t, err := b.pointer(frag, 0)
		if err != "" {
			return nil, "", "The reference " + strconv.Quote(b.refText(ref)) + " cannot be resolved: " + err + ".", SeverityError, CodeBadRef
		}
		return t, "#" + frag, "", "", ""
	default:
		b.scanAnchors()
		if a, ok := b.anchors[frag]; ok {
			return a.n, a.ptr, "", "", ""
		}
		return nil, "", "The reference " + strconv.Quote(b.refText(ref)) + " names no $anchor in this document.", SeverityError, CodeBadRef
	}
}

// pointer resolves a JSON pointer (RFC 6901) against the document. A $ref
// met on the way is followed, so "#/components/schemas/A/properties/b"
// works when A is itself a reference.
func (b *builder) pointer(p string, depth int) (*yaml.Node, string) {
	cur := b.root
	segs := strings.Split(p[1:], "/")
	hops := 0
	for i, seg := range segs {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		at := func() string { return b.refText("#/" + strings.Join(segs[:i], "/"))[1:] }
		cur = cur.Resolve()
		// Each step counts towards MaxRefs, so long pointers through
		// chains of references cannot multiply the work.
		b.refs++
		if b.refs > b.lim.MaxRefs {
			b.limit("refs", nil, "", "The document needs more than "+strconv.Itoa(b.lim.MaxRefs)+" reference resolution steps; the rest are left unresolved.")
			return nil, "the reference limit was reached"
		}
		for cur.Kind == yaml.MappingNode && cur.Get(seg) == nil && refOf(cur) != nil {
			hops++
			b.refs++
			if hops > 64 || depth > 64 || b.refs > b.lim.MaxRefs {
				return nil, "too many references on the way to " + strconv.Quote(p)
			}
			rv := refOf(cur).Resolve()
			if rv.Kind != yaml.ScalarNode || !strings.HasPrefix(rv.Value, "#/") {
				return nil, "the member at " + strconv.Quote(at()) + " is a reference that cannot be followed"
			}
			u := rv.Value[1:]
			if x, err := url.PathUnescape(u); err == nil {
				u = x
			}
			if u == p { // a pointer through itself
				return nil, "the path loops through the reference at " + strconv.Quote(at())
			}
			next, err := b.pointer(u, depth+1)
			if next == nil {
				return nil, err
			}
			cur = next.Resolve()
		}
		switch cur.Kind {
		case yaml.MappingNode:
			next := cur.Get(seg)
			if next == nil {
				return nil, "there is no member " + strconv.Quote(seg) + " at " + strconv.Quote(at())
			}
			cur = next
		case yaml.SequenceNode:
			idx, ok := atoiStrict(seg)
			if !ok || (len(seg) > 1 && seg[0] == '0') || idx >= len(cur.Items) {
				return nil, "there is no item " + strconv.Quote(seg) + " at " + strconv.Quote(at())
			}
			cur = cur.Items[idx]
		default:
			return nil, "the value at " + strconv.Quote(at()) + " is " + kindName(cur) + ", not an object or array"
		}
	}
	return cur.Resolve(), ""
}

// scanAnchors collects the $anchor (and $dynamicAnchor) names of the
// document once, with the pointers of the schemas that declare them.
func (b *builder) scanAnchors() {
	if b.anchors != nil {
		return
	}
	b.anchors = map[string]anchor{}
	type item struct {
		n   *yaml.Node
		ptr string
	}
	stack := []item{{b.root, "#"}}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch it.n.Kind {
		case yaml.MappingNode:
			for i := len(it.n.Pairs) - 1; i >= 0; i-- {
				p := it.n.Pairs[i]
				k := keyText(p.Key)
				if v := p.Value.Resolve(); (k == "$anchor" || k == "$dynamicAnchor") && v.Kind == yaml.ScalarNode {
					if _, dup := b.anchors[v.Value]; !dup {
						b.anchors[v.Value] = anchor{it.n, it.ptr}
					}
				}
				if p.Value.Kind != yaml.AliasNode {
					stack = append(stack, item{p.Value, child(it.ptr, k)})
				}
			}
		case yaml.SequenceNode:
			for i := len(it.n.Items) - 1; i >= 0; i-- {
				if c := it.n.Items[i]; c.Kind != yaml.AliasNode {
					stack = append(stack, item{c, index(it.ptr, i)})
				}
			}
		}
	}
}

// anchor is a node named by $anchor, with its pointer.
type anchor struct {
	n   *yaml.Node
	ptr string
}

// resolve builds the object of type T at node n: n itself, or the target
// of its $ref chain. Objects are memoized by node. Reference nesting is
// bounded by MaxDepth. A broken reference, or a value that is not an
// object, gives a zero placeholder with the position; nil means n is nil.
func resolve[T any](b *builder, n *yaml.Node, ptr, what string, memo map[*yaml.Node]*T, fill func(*T, *yaml.Node, string)) *T {
	n = n.Resolve()
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode && n.Tag == yaml.TagNull {
		b.diag(SeverityError, CodeInvalidValue, n, ptr, what+" must be an object, not null.")
		return placeholder[T](b, n, ptr)
	}
	refNode := refOf(n)
	target, tptr := n, ptr
	if refNode != nil {
		t, tp, _, ok := b.deref(n, ptr, nil)
		if !ok {
			return placeholder[T](b, n, ptr)
		}
		target, tptr = t, tp
	}
	obj, ok := memo[target]
	if !ok {
		if b.mapping(target, tptr, what) == nil {
			return placeholder[T](b, target, tptr)
		}
		obj = new(T)
		memo[target] = obj
		if refNode != nil {
			b.pending = append(b.pending, func() { fill(obj, target, tptr) })
		} else {
			fill(obj, target, tptr)
		}
	}
	if refNode != nil && b.minor >= 1 {
		return refOverride(b, obj, n)
	}
	return obj
}

// finish fills the objects reached through references, then runs the
// post phases and marks recursive schemas.
func (b *builder) finish() {
	for len(b.pending) > 0 {
		f := b.pending[0]
		b.pending = b.pending[1:]
		f()
	}
	for _, f := range b.post1 {
		f()
	}
	for _, f := range b.post2 {
		f()
	}
	markRecursive(b.schemaOrder)
}

func placeholder[T any](b *builder, n *yaml.Node, ptr string) *T {
	obj := new(T)
	p := b.pos(n, ptr)
	switch v := any(obj).(type) {
	case *Parameter:
		v.Pos = p
	case *Response:
		v.Pos = p
	case *RequestBody:
		v.Pos = p
	case *Header:
		v.Pos = p
	case *Example:
		v.Pos = p
	case *SecurityScheme:
		v.Pos = p
	case *Callback:
		v.Pos = p
	case *PathItem:
		v.Pos = p
	}
	return obj
}

// refOverride applies a 3.1 Reference Object's summary and description,
// which replace the target's, to a copy of the target. The copy is made
// once the target is complete.
func refOverride[T any](b *builder, obj *T, ref *yaml.Node) *T {
	desc, hasDesc := "", false
	summary, hasSummary := "", false
	if d := ref.Get("description").Resolve(); d != nil && d.Kind == yaml.ScalarNode {
		desc, hasDesc = d.Value, true
	}
	if s := ref.Get("summary").Resolve(); s != nil && s.Kind == yaml.ScalarNode {
		summary, hasSummary = s.Value, true
	}
	switch any(obj).(type) {
	case *Parameter, *Response, *RequestBody, *Header, *SecurityScheme:
		hasSummary = false
	case *Example, *PathItem:
	default:
		return obj
	}
	if !hasDesc && !hasSummary {
		return obj
	}
	c := new(T)
	b.post2 = append(b.post2, func() {
		*c = *obj
		switch v := any(c).(type) {
		case *Parameter:
			v.Description = desc
		case *Response:
			v.Description = desc
		case *RequestBody:
			v.Description = desc
		case *Header:
			v.Description = desc
		case *SecurityScheme:
			v.Description = desc
		case *Example:
			if hasDesc {
				v.Description = desc
			}
			if hasSummary {
				v.Summary = summary
			}
		case *PathItem:
			if hasDesc {
				v.Description = desc
			}
			if hasSummary {
				v.Summary = summary
			}
		}
	})
	return c
}

// expandMerges applies YAML merge keys (<<) in place, so that Get sees
// merged members: explicit members win, then earlier merged mappings.
func expandMerges(root *yaml.Node) {
	done := map[*yaml.Node]bool{}
	var visit func(n *yaml.Node, depth int)
	visit = func(n *yaml.Node, depth int) {
		n = n.Resolve()
		if n == nil || done[n] || depth > 10_000 {
			return
		}
		done[n] = true
		switch n.Kind {
		case yaml.SequenceNode:
			for _, it := range n.Items {
				visit(it, depth+1)
			}
		case yaml.MappingNode:
			var merged []yaml.Pair
			var own []yaml.Pair
			for _, p := range n.Pairs {
				k := p.Key.Resolve()
				visit(p.Value, depth+1)
				if k.Kind == yaml.ScalarNode && k.Value == "<<" && k.Style == yaml.Plain {
					v := p.Value.Resolve()
					var srcs []*yaml.Node
					if v.Kind == yaml.SequenceNode {
						for _, it := range v.Items {
							srcs = append(srcs, it.Resolve())
						}
					} else {
						srcs = []*yaml.Node{v}
					}
					for _, s := range srcs {
						if s.Kind == yaml.MappingNode {
							merged = append(merged, s.Pairs...)
						}
					}
					continue
				}
				own = append(own, p)
			}
			if len(merged) == 0 && len(own) == len(n.Pairs) {
				return
			}
			have := map[string]bool{}
			for _, p := range own {
				have[keyText(p.Key)] = true
			}
			for _, p := range merged {
				if k := keyText(p.Key); !have[k] {
					have[k] = true
					own = append(own, p)
				}
			}
			n.Pairs = own
		}
	}
	visit(root, 0)
}
