// Package docs is the search behind the GraphQL docs explorer (GQL-14):
// every type, field, argument, input field and enum value of a schema,
// matched against what the user types and ranked.
//
// An Index is built once per schema (NewIndex, linear in its size) with
// every name lowered in advance, so a search is a scan of the index
// without allocations per entry: well under 50 ms on a schema of 3000
// types of 60 fields. Build it once and keep it next to the schema.
//
//	idx := docs.NewIndex(s)
//	hits, count := idx.Search("user.log", 50) // fields of User-like types starting with "log"
//
// A query is matched case-insensitively, spaces ignored. "Type.member"
// matches the type part against the parent type's name and the member
// part against the member's name ("User." lists every member of types
// matching "User"). Introspection types (__Type, …) are left out unless
// the query starts with "__". See docs/graphql.md.
package docs

import (
	"container/heap"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// Kind is the kind of a search hit.
type Kind string

// The kinds of hits, ranked in this order between equal matches.
const (
	KindType       Kind = "type"
	KindField      Kind = "field"
	KindInputField Kind = "input-field"
	KindEnumValue  Kind = "enum-value"
	KindArgument   Kind = "argument"
)

func (k Kind) rank() int {
	switch k {
	case KindType:
		return 0
	case KindField:
		return 1
	case KindInputField:
		return 2
	case KindEnumValue:
		return 3
	}
	return 4
}

// Hit is one search result.
type Hit struct {
	Kind Kind
	// Name is the type's or member's name.
	Name string
	// Type is the type that shows the hit: the type itself, or the type
	// the member belongs to.
	Type string
	// Member is the member to show within Type: the field, input field or
	// enum value, or the field of an argument; "" for a type.
	Member string
	// Parent is "" for a type, the type's name for a field, input field or
	// enum value, and "Type.field" for an argument.
	Parent string
	// Detail is the type's kind (OBJECT, …) or the member's type
	// ("[User!]!"); "" for an enum value.
	Detail string
	// Description is the first line of the description, shortened.
	Description string
	Deprecated  bool
}

// MaxDescription is the most runes of a description a Hit keeps.
const MaxDescription = 140

type entry struct {
	kind   Kind
	name   string
	lower  string
	typ    *schema.Type // the type showing the hit
	parent string       // Hit.Parent
	plower string       // the lowered parent type's name
	member string
	field  *schema.Field
	value  *schema.InputValue
	enum   *schema.EnumValue
	intro  bool // an introspection type or one of its members
}

// Index is the searchable index of one schema. It is read-only after
// NewIndex and safe for concurrent searches.
type Index struct {
	entries []entry
}

// NewIndex indexes every type of s (in the server's order) and its
// members.
func NewIndex(s *schema.Schema) *Index {
	idx := &Index{}
	if s == nil {
		return idx
	}
	seen := make(map[string]bool, len(s.Types))
	for _, t := range s.Types {
		if seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		intro := strings.HasPrefix(t.Name, "__")
		tl := strings.ToLower(t.Name)
		idx.entries = append(idx.entries, entry{kind: KindType, name: t.Name, lower: tl, typ: t, intro: intro})
		for _, f := range t.Fields {
			idx.entries = append(idx.entries, entry{kind: KindField, name: f.Name, lower: strings.ToLower(f.Name), typ: t, parent: t.Name, plower: tl, member: f.Name, field: f, intro: intro})
			for _, a := range f.Args {
				idx.entries = append(idx.entries, entry{kind: KindArgument, name: a.Name, lower: strings.ToLower(a.Name), typ: t, parent: t.Name + "." + f.Name, plower: tl, member: f.Name, value: a, intro: intro})
			}
		}
		for _, v := range t.InputFields {
			idx.entries = append(idx.entries, entry{kind: KindInputField, name: v.Name, lower: strings.ToLower(v.Name), typ: t, parent: t.Name, plower: tl, member: v.Name, value: v, intro: intro})
		}
		for _, v := range t.EnumValues {
			idx.entries = append(idx.entries, entry{kind: KindEnumValue, name: v.Name, lower: strings.ToLower(v.Name), typ: t, parent: t.Name, plower: tl, member: v.Name, enum: v, intro: intro})
		}
	}
	return idx
}

// Len is the number of indexed items.
func (idx *Index) Len() int { return len(idx.entries) }

// Match tiers; lower is better.
const (
	tierExact     = iota // the whole name, ignoring case
	tierPrefix           // the name starts with the query
	tierWordStart        // the query starts a word of the name
	tierSubstring        // the query is in the name
	tierFuzzy            // the query's characters in order, the first one starting a word
	noMatch
)

// match returns how name (lower is its lowered text) matches the lowered
// query q. An empty query matches everything as a prefix.
func match(q, name, lower string) int {
	if q == "" {
		return tierPrefix
	}
	if len(q) > len(lower) {
		return noMatch
	}
	if lower == q {
		return tierExact
	}
	if strings.HasPrefix(lower, q) {
		return tierPrefix
	}
	at := strings.Index(lower, q)
	if at >= 0 {
		for i := at; i >= 0; {
			if wordStart(name, i) {
				return tierWordStart
			}
			next := strings.Index(lower[i+1:], q)
			if next < 0 {
				break
			}
			i += 1 + next
		}
		return tierSubstring
	}
	j := 0
	for ; j < len(lower); j++ {
		if lower[j] == q[0] && wordStart(name, j) {
			break
		}
	}
	if j == len(lower) {
		return noMatch
	}
	i := 1
	for j++; j < len(lower) && i < len(q); j++ {
		if lower[j] == q[i] {
			i++
		}
	}
	if i == len(q) {
		return tierFuzzy
	}
	return noMatch
}

// wordStart reports whether a word of name starts at byte j: the start, an
// upper-case letter after a lower-case one or a digit, or a character
// after "_". (Lowering keeps byte offsets of ASCII names; a non-ASCII name
// only loses word starts.)
func wordStart(name string, j int) bool {
	if j == 0 {
		return true
	}
	if j >= len(name) {
		return false
	}
	prev, c := name[j-1], name[j]
	return prev == '_' || isUpper(c) && !isUpper(prev)
}

func isUpper(b byte) bool { return 'A' <= b && b <= 'Z' }

type scored struct {
	e    *entry
	tier int
}

// better reports whether a ranks before b: the better match, then types
// before fields before input fields, enum values and arguments, then the
// shorter name, then by name and parent.
func better(a, b scored) bool {
	if a.tier != b.tier {
		return a.tier < b.tier
	}
	if ra, rb := a.e.kind.rank(), b.e.kind.rank(); ra != rb {
		return ra < rb
	}
	if len(a.e.name) != len(b.e.name) {
		return len(a.e.name) < len(b.e.name)
	}
	if a.e.name != b.e.name {
		return a.e.name < b.e.name
	}
	return a.e.parent < b.e.parent
}

// worst is a heap with the worst kept hit on top.
type worst []scored

func (h worst) Len() int           { return len(h) }
func (h worst) Less(i, j int) bool { return better(h[j], h[i]) }
func (h worst) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *worst) Push(x any)        { *h = append(*h, x.(scored)) }
func (h *worst) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// NormalizeQuery lowers query and drops its spaces.
func NormalizeQuery(query string) string {
	return strings.ToLower(strings.Join(strings.Fields(query), ""))
}

// Search returns the best limit hits for query, best first, and how many
// items matched. A blank query matches nothing.
func (idx *Index) Search(query string, limit int) ([]Hit, int) {
	q := NormalizeQuery(query)
	if q == "" || limit <= 0 {
		return []Hit{}, 0
	}
	intro := strings.HasPrefix(q, "__")
	// "Type.member": the part before the last dot matches the parent.
	parentQ, memberQ, dotted := "", q, false
	if i := strings.LastIndexByte(q, '.'); i >= 0 {
		parentQ, memberQ, dotted = q[:i], q[i+1:], true
	}
	h := make(worst, 0, limit+1)
	count := 0
	for i := range idx.entries {
		e := &idx.entries[i]
		if e.intro && !intro {
			continue
		}
		var tier int
		if dotted {
			if e.kind == KindType {
				continue
			}
			pt := match(parentQ, e.typ.Name, e.plower)
			if pt == noMatch {
				continue
			}
			mt := match(memberQ, e.name, e.lower)
			if mt == noMatch {
				continue
			}
			tier = pt + mt
		} else {
			tier = match(q, e.name, e.lower)
			if tier == noMatch {
				continue
			}
		}
		count++
		s := scored{e, tier}
		if len(h) < limit {
			heap.Push(&h, s)
		} else if better(s, h[0]) {
			h[0] = s
			heap.Fix(&h, 0)
		}
	}
	out := make([]Hit, len(h))
	for i := len(h) - 1; i >= 0; i-- {
		out[i] = heap.Pop(&h).(scored).e.hit()
	}
	return out, count
}

func (e *entry) hit() Hit {
	h := Hit{Kind: e.kind, Name: e.name, Type: e.typ.Name, Member: e.member, Parent: e.parent}
	var desc string
	switch {
	case e.field != nil:
		h.Detail = e.field.Type.String()
		desc = e.field.Description
		h.Deprecated = e.field.IsDeprecated
	case e.value != nil:
		h.Detail = e.value.Type.String()
		desc = e.value.Description
		h.Deprecated = e.value.IsDeprecated
	case e.enum != nil:
		desc = e.enum.Description
		h.Deprecated = e.enum.IsDeprecated
	default:
		h.Detail = string(e.typ.Kind)
		desc = e.typ.Description
	}
	h.Description = Summary(desc)
	return h
}

// Summary is the first line of a description, at most MaxDescription
// runes (cut with "…").
func Summary(desc string) string {
	desc = strings.TrimSpace(desc)
	if i := strings.IndexAny(desc, "\r\n"); i >= 0 {
		desc = strings.TrimSpace(desc[:i])
	}
	if utf8.RuneCountInString(desc) <= MaxDescription {
		return desc
	}
	n := 0
	for i := range desc {
		if n == MaxDescription-1 {
			return strings.TrimRight(desc[:i], " ") + "…"
		}
		n++
	}
	return desc
}
