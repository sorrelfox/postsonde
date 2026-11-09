package respdiff

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"strconv"
	"strings"
	"unicode"
)

// A JSON value as written: members in order (duplicates kept), numbers as
// their literal.
type node struct {
	kind    string // "object", "array", "string", "number", "boolean", "null"
	members []member
	items   []*node
	str     string // string value, number literal, "true"/"false"
}

type member struct {
	key string
	val *node
}

var (
	errNotJSON = errors.New("not JSON")
	errLimits  = errors.New("over the JSON limits")
)

// parseJSON reads data as one JSON value within lim's depth and value
// count.
func parseJSON(data []byte, lim Limits) (*node, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !json.Valid(data) {
		return nil, errNotJSON
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	p := &parser{dec: dec, lim: lim}
	n, err := p.value(0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errNotJSON
	}
	return n, nil
}

type parser struct {
	dec   *json.Decoder
	lim   Limits
	nodes int
}

func (p *parser) value(depth int) (*node, error) {
	if depth > p.lim.MaxDepth {
		return nil, errLimits
	}
	if p.nodes++; p.nodes > p.lim.MaxNodes {
		return nil, errLimits
	}
	tok, err := p.dec.Token()
	if err != nil {
		return nil, errNotJSON
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			n := &node{kind: "object"}
			for p.dec.More() {
				kt, err := p.dec.Token()
				if err != nil {
					return nil, errNotJSON
				}
				key, _ := kt.(string)
				v, err := p.value(depth + 1)
				if err != nil {
					return nil, err
				}
				n.members = append(n.members, member{key: key, val: v})
			}
			_, _ = p.dec.Token() // }
			return n, nil
		case '[':
			n := &node{kind: "array"}
			for p.dec.More() {
				v, err := p.value(depth + 1)
				if err != nil {
					return nil, err
				}
				n.items = append(n.items, v)
			}
			_, _ = p.dec.Token() // ]
			return n, nil
		}
	case string:
		return &node{kind: "string", str: t}, nil
	case json.Number:
		return &node{kind: "number", str: t.String()}, nil
	case bool:
		return &node{kind: "boolean", str: strconv.FormatBool(t)}, nil
	case nil:
		return &node{kind: "null", str: "null"}, nil
	}
	return nil, errNotJSON
}

// scalarText is a scalar as JSON.
func scalarText(n *node) string {
	if n.kind == "string" {
		return quote(n.str)
	}
	return n.str
}

// quote writes s as a JSON string, without escaping <, > and &.
func quote(s string) string {
	plain := true
	for i := 0; i < len(s) && plain; i++ {
		c := s[i]
		plain = c >= 0x20 && c < 0x80 && c != '"' && c != '\\'
	}
	if plain {
		return `"` + s + `"`
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// compact is n as compact JSON, cut at max characters (with …).
func compact(n *node, max int) string {
	var b strings.Builder
	var write func(n *node) bool
	write = func(n *node) bool {
		if b.Len() > max {
			return false
		}
		switch n.kind {
		case "object":
			b.WriteByte('{')
			for i, m := range n.members {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(quote(m.key) + ":")
				if !write(m.val) {
					return false
				}
			}
			b.WriteByte('}')
		case "array":
			b.WriteByte('[')
			for i, it := range n.items {
				if i > 0 {
					b.WriteByte(',')
				}
				if !write(it) {
					return false
				}
			}
			b.WriteByte(']')
		default:
			b.WriteString(scalarText(n))
		}
		return true
	}
	write(n)
	s := b.String()
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// equal reports whether a and b are the same value: numbers by value
// (1.0 is 1), members by key whatever their order.
func equal(a, b *node) bool {
	if a.kind != b.kind {
		return false
	}
	switch a.kind {
	case "object":
		if len(a.members) != len(b.members) {
			return false
		}
		pairs, _ := pairMembers(a, b)
		for _, p := range pairs {
			if p.l < 0 || p.r < 0 || !equal(a.members[p.l].val, b.members[p.r].val) {
				return false
			}
		}
		return true
	case "array":
		if len(a.items) != len(b.items) {
			return false
		}
		for i := range a.items {
			if !equal(a.items[i], b.items[i]) {
				return false
			}
		}
		return true
	case "number":
		return numbersEqual(a.str, b.str)
	}
	return a.str == b.str
}

func numbersEqual(a, b string) bool {
	if a == b {
		return true
	}
	x, ok1 := new(big.Rat).SetString(a)
	y, ok2 := new(big.Rat).SetString(b)
	return ok1 && ok2 && x.Cmp(y) == 0
}

// pair is a member index on each side, -1 where that side has none.
type pair struct{ l, r int }

// pairMembers matches the members of two objects by key (the n-th member
// named k on the left with the n-th named k on the right). Pairs come in
// the left object's order; a member only the right one has comes after
// the member both have that precedes it on the right, and after the
// left-only members there. reordered says members both have are in
// another order.
func pairMembers(a, b *node) (pairs []pair, reordered bool) {
	type key struct {
		name string
		n    int
	}
	keysOf := func(ms []member) []key {
		seen := map[string]int{}
		out := make([]key, len(ms))
		for i, m := range ms {
			out[i] = key{m.key, seen[m.key]}
			seen[m.key]++
		}
		return out
	}
	ka, kb := keysOf(a.members), keysOf(b.members)
	inA := make(map[key]int, len(ka))
	for i, k := range ka {
		inA[k] = i
	}
	inB := make(map[key]int, len(kb))
	for i, k := range kb {
		inB[k] = i
	}
	// after[i]: the right-only members that follow left member i (-1:
	// before every left member).
	after := map[int][]int{}
	anchor := -1
	last := -1
	for j, k := range kb {
		if i, ok := inA[k]; ok {
			anchor = i
			if i < last {
				reordered = true
			}
			last = i
			continue
		}
		after[anchor] = append(after[anchor], j)
	}
	// Right-only members wait for the next member both have, so in one
	// gap the left-only members (removed) come before them (added).
	pending := after[-1]
	flush := func() {
		for _, x := range pending {
			pairs = append(pairs, pair{-1, x})
		}
		pending = nil
	}
	for i, k := range ka {
		j, ok := inB[k]
		if !ok {
			pairs = append(pairs, pair{i, -1})
			continue
		}
		flush()
		pairs = append(pairs, pair{i, j})
		pending = after[i]
	}
	flush()
	return pairs, reordered
}

// childPath is the JSONPath of member key or element i under path, in
// normalized form: $.name, $['odd key'], $[0].
func childPath(path, key string, index int) string {
	if index >= 0 {
		return path + "[" + strconv.Itoa(index) + "]"
	}
	if isIdentifier(key) {
		return path + "." + key
	}
	return path + "['" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(key) + "']"
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r != '_' && !unicode.IsLetter(r) && (i == 0 || !unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

// differ walks two JSON values and writes the rows and changes.
type differ struct {
	rows       *rows
	out        *BodyDiff
	maxChanges int
	arrays     bool // an array pair differs
	reordered  bool
}

const indentUnit = "  "

// prettyLines are the lines of n pretty-printed at indent, the first one
// after prefix, the last one followed by a comma when comma.
func prettyLines(n *node, indent, prefix string, comma bool) []string {
	var out []string
	var walk func(n *node, indent, prefix string, comma bool)
	walk = func(n *node, indent, prefix string, comma bool) {
		c := ""
		if comma {
			c = ","
		}
		switch {
		case n.kind == "object" && len(n.members) > 0:
			out = append(out, indent+prefix+"{")
			for i, m := range n.members {
				walk(m.val, indent+indentUnit, quote(m.key)+": ", i < len(n.members)-1)
			}
			out = append(out, indent+"}"+c)
		case n.kind == "array" && len(n.items) > 0:
			out = append(out, indent+prefix+"[")
			for i, it := range n.items {
				walk(it, indent+indentUnit, "", i < len(n.items)-1)
			}
			out = append(out, indent+"]"+c)
		case n.kind == "object":
			out = append(out, indent+prefix+"{}"+c)
		case n.kind == "array":
			out = append(out, indent+prefix+"[]"+c)
		default:
			out = append(out, indent+prefix+scalarText(n)+c)
		}
	}
	walk(n, indent, prefix, comma)
	return out
}

func (d *differ) change(c Change) {
	switch c.Kind {
	case Added:
		d.out.Added++
	case Removed:
		d.out.Removed++
	case Changed:
		d.out.Changed++
	}
	if len(d.out.Changes) >= d.maxChanges {
		return
	}
	d.out.Changes = append(d.out.Changes, c)
}

// side is one side's view of a value: the node (nil when absent), the
// text before it ("key": ) and whether a comma follows.
type side struct {
	n      *node
	prefix string
	comma  bool
}

// walk writes the rows of l and r at path.
func (d *differ) walk(path, indent string, l, r side) {
	switch {
	case l.n == nil:
		for _, line := range prettyLines(r.n, indent, r.prefix, r.comma) {
			d.rows.add(Added, nil, &line, path)
		}
		d.change(Change{Path: path, Kind: Added, Right: compact(r.n, 200), RightType: r.n.kind})
		return
	case r.n == nil:
		for _, line := range prettyLines(l.n, indent, l.prefix, l.comma) {
			d.rows.add(Removed, &line, nil, path)
		}
		d.change(Change{Path: path, Kind: Removed, Left: compact(l.n, 200), LeftType: l.n.kind})
		return
	}
	container := l.n.kind == r.n.kind && (l.n.kind == "object" || l.n.kind == "array")
	switch {
	case container && (len(l.n.members)+len(l.n.items) > 0 || len(r.n.members)+len(r.n.items) > 0):
		open, close := "{", "}"
		if l.n.kind == "array" {
			open, close = "[", "]"
		}
		ll, rl := indent+l.prefix+open, indent+r.prefix+open
		d.rows.add(Same, &ll, &rl, path)
		inner := indent + indentUnit
		if l.n.kind == "object" {
			pairs, reordered := pairMembers(l.n, r.n)
			d.reordered = d.reordered || reordered
			for _, p := range pairs {
				var ls, rs side
				key := ""
				if p.l >= 0 {
					m := l.n.members[p.l]
					key = m.key
					ls = side{n: m.val, prefix: quote(m.key) + ": ", comma: p.l < len(l.n.members)-1}
				}
				if p.r >= 0 {
					m := r.n.members[p.r]
					key = m.key
					rs = side{n: m.val, prefix: quote(m.key) + ": ", comma: p.r < len(r.n.members)-1}
				}
				d.walk(childPath(path, key, -1), inner, ls, rs)
			}
		} else {
			before := d.out.Added + d.out.Removed + d.out.Changed
			for i := range max(len(l.n.items), len(r.n.items)) {
				var ls, rs side
				if i < len(l.n.items) {
					ls = side{n: l.n.items[i], comma: i < len(l.n.items)-1}
				}
				if i < len(r.n.items) {
					rs = side{n: r.n.items[i], comma: i < len(r.n.items)-1}
				}
				d.walk(childPath(path, "", i), inner, ls, rs)
			}
			if d.out.Added+d.out.Removed+d.out.Changed > before {
				d.arrays = true
			}
		}
		lc, rc := indent+close, indent+close
		if l.comma {
			lc += ","
		}
		if r.comma {
			rc += ","
		}
		d.rows.add(Same, &lc, &rc, path)
	case equal(l.n, r.n):
		ll := prettyLines(l.n, indent, l.prefix, l.comma)
		rl := prettyLines(r.n, indent, r.prefix, r.comma)
		for i := range max(len(ll), len(rl)) {
			var a, b *string
			if i < len(ll) {
				a = &ll[i]
			}
			if i < len(rl) {
				b = &rl[i]
			}
			d.rows.add(Same, a, b, path)
		}
	default:
		ll := prettyLines(l.n, indent, l.prefix, l.comma)
		rl := prettyLines(r.n, indent, r.prefix, r.comma)
		for i := range max(len(ll), len(rl)) {
			var a, b *string
			if i < len(ll) {
				a = &ll[i]
			}
			if i < len(rl) {
				b = &rl[i]
			}
			d.rows.add(Changed, a, b, path)
		}
		c := Change{Path: path, Kind: Changed, Left: compact(l.n, 200), Right: compact(r.n, 200)}
		if l.n.kind != r.n.kind {
			c.LeftType, c.RightType = l.n.kind, r.n.kind
		}
		d.change(c)
	}
}

// jsonDiff fills out with the structural diff of a and b.
func jsonDiff(out *BodyDiff, a, b *node, lim Limits) {
	d := &differ{rows: &rows{max: lim.MaxRows}, out: out, maxChanges: lim.MaxChanges}
	d.walk("$", "", side{n: a}, side{n: b})
	out.Identical = out.Added+out.Removed+out.Changed == 0
	d.rows.finish(out)
	if d.arrays {
		out.Notices = append(out.Notices, Notice{Code: CodeArraysByIndex,
			Message: "Arrays are compared element by element, by index: an element added or removed in the middle shows every later element as changed."})
	}
	if d.reordered {
		out.Notices = append(out.Notices, Notice{Code: CodeKeyOrder,
			Message: "Members are in another order on the right; both sides are shown in the left body's order, which does not count as a difference."})
	}
	if total := out.Added + out.Removed + out.Changed; total > len(out.Changes) {
		out.Notices = append(out.Notices, Notice{Code: CodeChangesLimited,
			Message: "Only the first " + strconv.Itoa(len(out.Changes)) + " of " + strconv.Itoa(total) + " changes are listed."})
	}
}
