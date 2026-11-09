package respdiff

import (
	"slices"
	"strings"
)

// Headers compares two lists of header fields by name, letter case
// ignored (Content-Type and content-type are one name). A name's values
// are compared as the list of its fields' values, in order, so a second
// Set-Cookie counts. Names come in the left response's order, then those
// only the right one has, in its order.
func Headers(left, right []Header) []HeaderDiff {
	type entry struct {
		name        string
		left, right []string
		inL, inR    bool
	}
	byKey := map[string]*entry{}
	var order []string
	get := func(name string) *entry {
		key := strings.ToLower(strings.TrimSpace(name))
		e, ok := byKey[key]
		if !ok {
			e = &entry{name: strings.TrimSpace(name)}
			byKey[key] = e
			order = append(order, key)
		}
		return e
	}
	for _, h := range left {
		e := get(h.Name)
		e.inL = true
		e.left = append(e.left, h.Value)
	}
	for _, h := range right {
		e := get(h.Name)
		e.inR = true
		e.right = append(e.right, h.Value)
	}
	out := make([]HeaderDiff, 0, len(order))
	for _, key := range order {
		e := byKey[key]
		d := HeaderDiff{Name: e.name, Left: e.left, Right: e.right}
		switch {
		case !e.inL:
			d.Kind = Added
		case !e.inR:
			d.Kind = Removed
		case slices.Equal(e.left, e.right):
			d.Kind = Same
		default:
			d.Kind = Changed
		}
		out = append(out, d)
	}
	return out
}
