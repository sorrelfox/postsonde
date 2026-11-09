package openapi

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// schema builds the Schema at n. A $ref is resolved to the shared schema
// it names; in 3.1 keywords next to a $ref are kept (the target goes
// first in AllOf), in 3.0 they are ignored with a diagnostic.
func (b *builder) schema(n *yaml.Node, ptr string) *Schema {
	n = n.Resolve()
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode && n.Tag == yaml.TagBool {
		v := strings.EqualFold(n.Value, "true")
		return &Schema{Bool: &v, Pos: b.pos(n, ptr)}
	}
	if n.Kind != yaml.MappingNode {
		b.diag(SeverityError, CodeInvalidValue, n, ptr, "A schema must be an object, not "+kindName(n)+".")
		return &Schema{Pos: b.pos(n, ptr)}
	}
	if refOf(n) == nil {
		return b.schemaNode(n, ptr, false, false)
	}
	if b.minor >= 1 && hasSiblings(n) {
		return b.schemaNode(n, ptr, true, false)
	}
	if b.minor == 0 && hasSiblings(n) {
		if !b.noted[n] {
			b.noted[n] = true // report once per node
			b.diag(SeverityInfo, CodeIgnored, n, ptr, "In OpenAPI 3.0 the keywords next to $ref are ignored.")
		}
	}
	return b.refSchema(n, ptr)
}

// hasSiblings reports whether a mapping with $ref has other keywords
// (extensions and $comment do not count).
func hasSiblings(n *yaml.Node) bool {
	for _, p := range n.Pairs {
		k := keyText(p.Key)
		if k != "$ref" && k != "$comment" && !strings.HasPrefix(k, "x-") {
			return true
		}
	}
	return false
}

// refSchema returns the schema a $ref at n points to, or a placeholder
// with Ref set when it cannot be resolved. The target is filled in later
// (see builder.pending), so long chains of references do not recurse.
func (b *builder) refSchema(n *yaml.Node, ptr string) *Schema {
	stop := func(t *yaml.Node) bool { return b.minor >= 1 && hasSiblings(t) }
	target, tptr, ref, ok := b.deref(n, ptr, stop)
	if !ok {
		if ref == "" {
			ref = scalarText(refOf(n))
		}
		return &Schema{Ref: b.refText(ref), Pos: b.pos(n, ptr)}
	}
	if target.Kind == yaml.ScalarNode && target.Tag == yaml.TagBool {
		return b.schema(target, tptr)
	}
	if target.Kind != yaml.MappingNode {
		b.diag(SeverityError, CodeBadRef, n, ptr, "The reference "+strconv.Quote(b.refText(scalarText(refOf(n))))+" points at "+kindName(target)+", not a schema.")
		return &Schema{Ref: b.refText(scalarText(refOf(n))), Pos: b.pos(n, ptr)}
	}
	return b.schemaNode(target, tptr, refOf(target) != nil, true)
}

// schemaNode returns the schema of the mapping n, made once. withRef is
// set for a 3.1 schema with $ref and other keywords. A lazy schema is
// filled in after the object being built (see builder.pending).
func (b *builder) schemaNode(n *yaml.Node, ptr string, withRef, lazy bool) *Schema {
	if s := b.schemas[n]; s != nil {
		return s
	}
	s := &Schema{Pos: b.pos(n, ptr)}
	b.schemas[n] = s
	b.schemaOrder = append(b.schemaOrder, s)
	fill := func() {
		if withRef {
			s.AllOf = append(s.AllOf, b.refSchema(n, ptr))
		}
		b.fillSchema(s, n, ptr)
	}
	if lazy {
		b.pending = append(b.pending, fill)
	} else {
		fill()
	}
	return s
}

func (b *builder) schemaList(v *yaml.Node, ptr, what string) []*Schema {
	items := b.sequence(v, ptr, what)
	if items == nil {
		return nil
	}
	out := make([]*Schema, 0, len(items))
	for i, it := range items {
		out = append(out, b.schema(it, index(ptr, i)))
	}
	return out
}

func (b *builder) schemaMap(v *yaml.Node, ptr, what string) Entries[*Schema] {
	var out Entries[*Schema]
	for _, p := range pairs(b.mapping(v, ptr, what)) {
		name := keyText(p.Key)
		out = append(out, Entry[*Schema]{name, b.schema(p.Value, child(ptr, name))})
	}
	return out
}

func (b *builder) valueList(v *yaml.Node, ptr, what string) []json.RawMessage {
	items := b.sequence(v, ptr, what)
	if items == nil {
		return nil
	}
	out := make([]json.RawMessage, 0, len(items))
	for i, it := range items {
		out = append(out, b.value(it, index(ptr, i)))
	}
	return out
}

func (b *builder) fillSchema(s *Schema, m *yaml.Node, ptr string) {
	var exclusiveMin, exclusiveMax bool // 3.0 boolean forms
	for _, p := range m.Pairs {
		k := keyText(p.Key)
		v := p.Value
		kp := child(ptr, k)
		switch k {
		case "$ref":
			// handled by schemaNode
		case "type":
			vn := v.Resolve()
			if vn.Kind == yaml.SequenceNode {
				if b.minor == 0 {
					b.diag(SeverityWarning, CodeInvalidValue, vn, kp, "In OpenAPI 3.0 type must be a single string; the array is read the 3.1 way.")
				}
				s.Type = b.strList(vn, kp, "type")
			} else if t := b.str(vn, kp, "type"); t != "" {
				s.Type = []string{t}
			}
			for _, t := range s.Type {
				switch t {
				case "string", "number", "integer", "boolean", "array", "object", "null":
				default:
					b.diag(SeverityWarning, CodeInvalidValue, vn, kp, "The schema type "+strconv.Quote(t)+" is not a JSON Schema type.")
				}
			}
		case "format":
			s.Format = b.str(v, kp, "format")
		case "title":
			s.Title = b.str(v, kp, "title")
		case "description":
			s.Description = b.str(v, kp, "description")
		case "default":
			s.Default = b.value(v, kp)
		case "example":
			s.Example = b.value(v, kp)
		case "examples":
			if vn := v.Resolve(); vn.Kind == yaml.MappingNode {
				b.diag(SeverityWarning, CodeInvalidValue, vn, kp, "A schema's examples must be an array; the values of the object are used.")
				for _, e := range vn.Pairs {
					s.Examples = append(s.Examples, b.value(e.Value, child(kp, keyText(e.Key))))
				}
			} else {
				s.Examples = b.valueList(v, kp, "examples")
			}
		case "enum":
			s.Enum = b.valueList(v, kp, "enum")
		case "const":
			s.Const = b.value(v, kp)
		case "nullable":
			s.Nullable = b.boolean(v, kp, "nullable")
			if b.minor >= 1 {
				b.diag(SeverityInfo, CodeIgnored, p.Key, kp, "nullable is not part of OpenAPI 3.1 (use type: [..., \"null\"]); it is read the 3.0 way.")
			}
		case "readOnly":
			s.ReadOnly = b.boolean(v, kp, "readOnly")
		case "writeOnly":
			s.WriteOnly = b.boolean(v, kp, "writeOnly")
		case "deprecated":
			s.Deprecated = b.boolean(v, kp, "deprecated")
		case "multipleOf":
			s.MultipleOf = b.number(v, kp, "multipleOf")
		case "minimum":
			s.Minimum = b.number(v, kp, "minimum")
		case "maximum":
			s.Maximum = b.number(v, kp, "maximum")
		case "exclusiveMinimum":
			if vn := v.Resolve(); vn.Kind == yaml.ScalarNode && vn.Tag == yaml.TagBool {
				exclusiveMin = b.boolean(vn, kp, k)
			} else {
				s.ExclusiveMinimum = b.number(v, kp, k)
			}
		case "exclusiveMaximum":
			if vn := v.Resolve(); vn.Kind == yaml.ScalarNode && vn.Tag == yaml.TagBool {
				exclusiveMax = b.boolean(vn, kp, k)
			} else {
				s.ExclusiveMaximum = b.number(v, kp, k)
			}
		case "minLength":
			s.MinLength = b.intPtr(v, kp, k)
		case "maxLength":
			s.MaxLength = b.intPtr(v, kp, k)
		case "pattern":
			s.Pattern = b.str(v, kp, k)
		case "contentMediaType":
			s.ContentMediaType = b.str(v, kp, k)
		case "contentEncoding":
			s.ContentEncoding = b.str(v, kp, k)
		case "items":
			if vn := v.Resolve(); vn.Kind == yaml.SequenceNode {
				b.diag(SeverityWarning, CodeInvalidValue, vn, kp, "items must be a schema; the array is read as prefixItems.")
				s.PrefixItems = b.schemaList(vn, kp, k)
			} else {
				s.Items = b.schema(v, kp)
			}
		case "prefixItems":
			s.PrefixItems = b.schemaList(v, kp, k)
		case "contains":
			s.Contains = b.schema(v, kp)
		case "minItems":
			s.MinItems = b.intPtr(v, kp, k)
		case "maxItems":
			s.MaxItems = b.intPtr(v, kp, k)
		case "uniqueItems":
			s.UniqueItems = b.boolean(v, kp, k)
		case "properties":
			s.Properties = b.schemaMap(v, kp, k)
		case "patternProperties":
			s.PatternProperties = b.schemaMap(v, kp, k)
		case "additionalProperties":
			s.AdditionalProperties = b.schema(v, kp)
		case "propertyNames":
			s.PropertyNames = b.schema(v, kp)
		case "required":
			if vn := v.Resolve(); vn.Kind == yaml.ScalarNode && vn.Tag == yaml.TagBool {
				b.diag(SeverityWarning, CodeInvalidValue, vn, kp, "required in a schema must be an array of property names; a boolean is ignored.")
			} else {
				s.Required = b.strList(v, kp, k)
			}
		case "minProperties":
			s.MinProperties = b.intPtr(v, kp, k)
		case "maxProperties":
			s.MaxProperties = b.intPtr(v, kp, k)
		case "allOf":
			s.AllOf = append(s.AllOf, b.schemaList(v, kp, k)...)
		case "anyOf":
			s.AnyOf = b.schemaList(v, kp, k)
		case "oneOf":
			s.OneOf = b.schemaList(v, kp, k)
		case "not":
			s.Not = b.schema(v, kp)
		case "if":
			s.If = b.schema(v, kp)
		case "then":
			s.Then = b.schema(v, kp)
		case "else":
			s.Else = b.schema(v, kp)
		case "discriminator":
			s.Discriminator = b.discriminator(v, kp)
		case "xml":
			if xm := b.mapping(v, kp, "xml"); xm != nil {
				s.XML = &XML{
					Name:      b.str(xm.Get("name"), child(kp, "name"), "xml.name"),
					Namespace: b.str(xm.Get("namespace"), child(kp, "namespace"), "xml.namespace"),
					Prefix:    b.str(xm.Get("prefix"), child(kp, "prefix"), "xml.prefix"),
					Attribute: b.boolean(xm.Get("attribute"), child(kp, "attribute"), "xml.attribute"),
					Wrapped:   b.boolean(xm.Get("wrapped"), child(kp, "wrapped"), "xml.wrapped"),
				}
			}
		case "externalDocs":
			s.ExternalDocs = b.externalDocs(v, kp)
		case "$defs", "definitions":
			s.Defs = append(s.Defs, b.schemaMap(v, kp, k)...)
		case "$id":
			s.ID = b.str(v, kp, k)
			if b.minor >= 1 && !strings.HasPrefix(s.ID, "#") {
				b.diag(SeverityInfo, CodeUnsupported, p.Key, kp, "$id does not change how references are resolved here; they are resolved against this document.")
			}
		case "$anchor":
			s.Anchor = b.str(v, kp, k)
		case "$schema":
			s.Schema = b.str(v, kp, k)
		case "$comment":
			s.Comment = b.str(v, kp, k)
		case "$dynamicRef", "$recursiveRef", "$dynamicAnchor", "$recursiveAnchor",
			"unevaluatedProperties", "unevaluatedItems", "dependentSchemas", "dependentRequired", "dependencies":
			b.diag(SeverityInfo, CodeUnsupported, p.Key, kp, "The keyword "+k+" is not supported; it is ignored.")
		default:
			if strings.HasPrefix(k, "x-") {
				s.Extensions = append(s.Extensions, Entry[json.RawMessage]{k, b.value(v, kp)})
			}
		}
	}
	if exclusiveMin && s.Minimum != "" {
		s.ExclusiveMinimum, s.Minimum = s.Minimum, ""
	}
	if exclusiveMax && s.Maximum != "" {
		s.ExclusiveMaximum, s.Maximum = s.Maximum, ""
	}
	if s.Nullable && len(s.Type) > 0 && !slices.Contains(s.Type, "null") {
		s.Type = append(s.Type, "null")
	}
}

func (b *builder) discriminator(v *yaml.Node, ptr string) *Discriminator {
	m := b.mapping(v, ptr, "discriminator")
	if m == nil {
		return nil
	}
	d := &Discriminator{PropertyName: b.str(m.Get("propertyName"), child(ptr, "propertyName"), "propertyName")}
	if m.Get("propertyName") == nil {
		b.diag(SeverityWarning, CodeMissingField, m, ptr, "The discriminator has no propertyName.")
	}
	mp := child(ptr, "mapping")
	for _, p := range pairs(b.mapping(m.Get("mapping"), mp, "mapping")) {
		name := keyText(p.Key)
		d.Mapping = append(d.Mapping, Entry[string]{name, b.str(p.Value, child(mp, name), "A discriminator mapping")})
	}
	return d
}

// subschemas returns the schemas s refers to directly.
func subschemas(s *Schema) []*Schema {
	out := make([]*Schema, 0, len(s.Properties)+len(s.AllOf)+len(s.AnyOf)+len(s.OneOf)+4)
	for _, one := range []*Schema{s.Items, s.Contains, s.AdditionalProperties, s.PropertyNames, s.Not, s.If, s.Then, s.Else} {
		if one != nil {
			out = append(out, one)
		}
	}
	for _, list := range [][]*Schema{s.PrefixItems, s.AllOf, s.AnyOf, s.OneOf} {
		out = append(out, list...)
	}
	for _, es := range []Entries[*Schema]{s.Properties, s.PatternProperties, s.Defs} {
		for _, e := range es {
			if e.Value != nil {
				out = append(out, e.Value)
			}
		}
	}
	return out
}

// markRecursive sets Recursive on every schema that is on a cycle, with
// Tarjan's strongly connected components algorithm, run iteratively so
// deep schema graphs do not grow the stack.
func markRecursive(all []*Schema) {
	index := map[*Schema]int{}
	low := map[*Schema]int{}
	onStack := map[*Schema]bool{}
	var stack []*Schema
	type frame struct {
		s    *Schema
		kids []*Schema
		next int
	}
	counter := 0
	visit := func(s *Schema) frame {
		index[s], low[s] = counter, counter
		counter++
		stack = append(stack, s)
		onStack[s] = true
		return frame{s: s, kids: subschemas(s)}
	}
	for _, root := range all {
		if _, seen := index[root]; seen {
			continue
		}
		work := []frame{visit(root)}
		for len(work) > 0 {
			f := &work[len(work)-1]
			if f.next < len(f.kids) {
				k := f.kids[f.next]
				f.next++
				if k == f.s {
					k.Recursive = true // a direct self-reference
				}
				if _, seen := index[k]; !seen {
					work = append(work, visit(k))
				} else if onStack[k] {
					low[f.s] = min(low[f.s], index[k])
				}
				continue
			}
			s := f.s
			work = work[:len(work)-1]
			if len(work) > 0 {
				p := work[len(work)-1].s
				low[p] = min(low[p], low[s])
			}
			if low[s] == index[s] {
				i := len(stack) - 1
				for stack[i] != s {
					i--
				}
				if len(stack)-i > 1 {
					for _, x := range stack[i:] {
						x.Recursive = true
					}
				}
				for _, x := range stack[i:] {
					onStack[x] = false
				}
				stack = stack[:i]
			}
		}
	}
}
