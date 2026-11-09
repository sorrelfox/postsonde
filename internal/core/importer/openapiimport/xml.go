package openapiimport

import (
	"encoding/xml"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/openapi"
)

// XMLExample renders an example value (JSON text, as SchemaExample
// returns) as an XML document following the schema's xml hints: element
// names (xml.name, else the component's name, else "root"), attributes,
// namespaces and wrapped arrays.
func XMLExample(s *openapi.Schema, example []byte, names map[*openapi.Schema]string) string {
	return xmlDocument(s, decodeRaw(example), names)
}

func xmlDocument(s *openapi.Schema, v value, names map[*openapi.Schema]string) string {
	w := &xmlWriter{names: names, seen: map[*openapi.Schema]int{}}
	s = pickAlternative(s, v)
	name := w.elementName(s, "")
	if name == "" {
		name = "root"
	}
	w.b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	w.element(name, s, v, 0, true)
	return w.b.String()
}

type xmlWriter struct {
	b     strings.Builder
	names map[*openapi.Schema]string
	// seen counts the schemas on the current path, so cycles in the
	// schema cannot loop (the value is finite anyway).
	seen map[*openapi.Schema]int
}

// unwrap follows wrappers of one schema (see single), cycle-safe.
func unwrap(s *openapi.Schema) *openapi.Schema {
	visited := map[*openapi.Schema]bool{}
	for s != nil && !visited[s] {
		visited[s] = true
		only := single(s)
		if only == nil {
			break
		}
		s = only
	}
	return s
}

func xmlHints(s *openapi.Schema) *openapi.XML {
	visited := map[*openapi.Schema]bool{}
	for s != nil && !visited[s] {
		visited[s] = true
		if s.XML != nil {
			return s.XML
		}
		s = single(s)
	}
	return nil
}

// elementName is the xml.name of s, else the component name, else def.
func (w *xmlWriter) elementName(s *openapi.Schema, def string) string {
	if x := xmlHints(s); x != nil && x.Name != "" {
		return x.Name
	}
	if def != "" {
		return def
	}
	if n := w.names[unwrap(s)]; n != "" {
		return n
	}
	return ""
}

func (w *xmlWriter) indent(depth int) {
	w.b.WriteString(strings.Repeat("  ", depth))
}

func (w *xmlWriter) element(name string, s *openapi.Schema, v value, depth int, top bool) {
	s = pickAlternative(s, v)
	name = xmlName(name)
	x := xmlHints(s)
	qname := name
	nsAttr := ""
	if x != nil {
		if x.Prefix != "" {
			qname = xmlName(x.Prefix) + ":" + name
		}
		if x.Namespace != "" {
			attr := "xmlns"
			if x.Prefix != "" {
				attr += ":" + xmlName(x.Prefix)
			}
			nsAttr = " " + attr + `="` + escape(x.Namespace) + `"`
		}
	}
	us := unwrap(s)
	if us != nil {
		w.seen[us]++
		defer func() { w.seen[us]-- }()
	}
	switch t := v.(type) {
	case []value:
		items := itemsOf(us)
		wrapped := x != nil && x.Wrapped
		itemName := w.elementName(items, name)
		if top {
			if itemName = w.elementName(items, ""); itemName == "" {
				itemName = "item"
			}
		}
		if top || wrapped {
			w.indent(depth)
			w.b.WriteString("<" + qname + nsAttr + ">\n")
			for _, it := range t {
				w.element(itemName, items, it, depth+1, false)
			}
			w.indent(depth)
			w.b.WriteString("</" + qname + ">\n")
			return
		}
		for _, it := range t {
			w.element(itemName, items, it, depth, false)
		}
		return
	case *object:
		props := map[string]*openapi.Schema{}
		if us != nil && w.seen[us] <= 2 {
			g := newGen(ExampleOptions{})
			for _, p := range g.merge(us).props {
				props[p.Name] = p.Value
			}
		}
		var attrs strings.Builder
		var children []int
		for i, k := range t.keys {
			ps := props[k]
			if px := xmlHints(ps); px != nil && px.Attribute {
				an := k
				if px.Name != "" {
					an = px.Name
				}
				attrs.WriteString(" " + xmlName(an) + `="` + escape(text(t.vals[i])) + `"`)
				continue
			}
			if arr, ok := t.vals[i].([]value); ok && len(arr) == 0 {
				if px := xmlHints(ps); px == nil || !px.Wrapped {
					continue // an unwrapped empty array has no elements
				}
			}
			children = append(children, i)
		}
		w.indent(depth)
		if len(children) == 0 {
			w.b.WriteString("<" + qname + nsAttr + attrs.String() + "/>\n")
			return
		}
		w.b.WriteString("<" + qname + nsAttr + attrs.String() + ">\n")
		for _, i := range children {
			k := t.keys[i]
			ps := props[k]
			cn := k
			if px := xmlHints(ps); px != nil && px.Name != "" {
				cn = px.Name
			}
			w.element(cn, ps, t.vals[i], depth+1, false)
		}
		w.indent(depth)
		w.b.WriteString("</" + qname + ">\n")
		return
	}
	w.indent(depth)
	w.b.WriteString("<" + qname + nsAttr + ">" + escape(text(v)) + "</" + qname + ">\n")
}

// pickAlternative returns, for a oneOf/anyOf schema without properties of
// its own and an object value, the alternative whose properties hold the
// most of the value's members; else s.
func pickAlternative(s *openapi.Schema, v value) *openapi.Schema {
	o, ok := v.(*object)
	if !ok || s == nil {
		return s
	}
	m := newGen(ExampleOptions{}).merge(unwrap(s))
	if len(m.props) > 0 || len(m.alts) == 0 {
		return s
	}
	best, score := s, 0
	for _, a := range m.alts {
		if a == nil {
			continue
		}
		n := 0
		for _, p := range newGen(ExampleOptions{}).merge(unwrap(a)).props {
			if o.has(p.Name) {
				n++
			}
		}
		if n > score {
			best, score = a, n
		}
	}
	return best
}

func itemsOf(s *openapi.Schema) *openapi.Schema {
	if s == nil {
		return nil
	}
	g := newGen(ExampleOptions{})
	for _, p := range g.merge(s).parts {
		if p.Items != nil {
			return p.Items
		}
	}
	return nil
}

func escape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// xmlName makes a name a valid XML name: characters that cannot be in one
// become "_".
func xmlName(s string) string {
	if s == "" {
		return "_"
	}
	var b strings.Builder
	for i, r := range s {
		ok := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 0x7f
		if i > 0 {
			ok = ok || r >= '0' && r <= '9' || r == '-' || r == '.'
		}
		if ok {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
