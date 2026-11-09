package complete

import (
	"sort"
	"strconv"
	"strings"
)

// Match tiers; lower is better.
const (
	matchPrefix     = iota // the name starts with the prefix
	matchFoldPrefix        // ... ignoring ASCII case
	matchFuzzy             // see match
	noMatch
)

// match returns how name matches the typed prefix. An empty prefix matches
// everything as a prefix.
//
// The fuzzy match ignores ASCII case and needs the prefix's first character
// to start a word of the name (the name's start, an upper-case letter after
// a lower-case one, or a character after "_"), and the remaining characters
// to follow in order: "ur" matches "mirrorUrl" and "usr" matches "user", but
// "na" does not match "hasWikiEnabled".
func match(prefix, name string) int {
	switch {
	case strings.HasPrefix(name, prefix):
		return matchPrefix
	case len(name) >= len(prefix) && strings.EqualFold(name[:len(prefix)], prefix):
		return matchFoldPrefix
	}
	j := 0
	for ; j < len(name); j++ {
		if lower(name[j]) == lower(prefix[0]) && wordStart(name, j) {
			break
		}
	}
	i := 1
	for j++; j < len(name) && i < len(prefix); j++ {
		if lower(name[j]) == lower(prefix[i]) {
			i++
		}
	}
	if j <= len(name) && i == len(prefix) {
		return matchFuzzy
	}
	return noMatch
}

func wordStart(name string, j int) bool {
	if j == 0 {
		return true
	}
	prev, c := name[j-1], name[j]
	return prev == '_' || isUpper(c) && !isUpper(prev)
}

func isUpper(b byte) bool { return 'A' <= b && b <= 'Z' }

func lower(b byte) byte {
	if isUpper(b) {
		return b + 'a' - 'A'
	}
	return b
}

type candidate struct {
	item   Item
	filter string // the text matched against the prefix
	class  int
	tier   int
	meta   bool // a "__" name: ranked last
}

// add collects an item filtered by its label.
func (c *completer) add(it Item, class int) { c.addFiltered(it, it.Label, class) }

// addFiltered collects an item filtered by filter, if it matches the prefix.
// Items are added in schema declaration order, which breaks ties.
func (c *completer) addFiltered(it Item, filter string, class int) {
	prefix := strings.TrimPrefix(c.res.Prefix, "$")
	tier := match(prefix, filter)
	if tier == noMatch {
		return
	}
	if it.InsertText == "" {
		it.InsertText = it.Label
	}
	c.cands = append(c.cands, candidate{item: it, filter: filter, class: class, tier: tier, meta: strings.HasPrefix(filter, "__")})
}

// finish ranks the candidates: match tier, then "__" names last, then
// deprecated items last, then the context's class (required arguments
// first, ...), then declaration order.
func (c *completer) finish() {
	sort.SliceStable(c.cands, func(i, j int) bool {
		a, b := c.cands[i], c.cands[j]
		switch {
		case a.tier != b.tier:
			return a.tier < b.tier
		case a.meta != b.meta:
			return b.meta
		case a.item.Deprecated != b.item.Deprecated:
			return b.item.Deprecated
		}
		return a.class < b.class
	})
	c.res.Items = make([]Item, len(c.cands))
	for i, cand := range c.cands {
		cand.item.SortText = pad(i)
		c.res.Items[i] = cand.item
	}
}

func pad(i int) string {
	s := strconv.Itoa(i)
	if len(s) < 4 {
		s = strings.Repeat("0", 4-len(s)) + s
	}
	return s
}
