package validate

import (
	"slices"
	"strings"
)

// MaxSuggestions caps the suggestions of a diagnostic, like graphql-js.
const MaxSuggestions = 5

// SuggestionList returns the options similar to input, best first, like
// graphql-js's suggestionList: an option is kept when its optimal string
// alignment distance (Damerau-Levenshtein with adjacent transpositions) to
// input, ignoring case, is at most floor(len(input)*0.4)+1. An option equal
// to input scores 0 and one differing only in case scores 1. Ties are in
// natural order ("a2" before "a10"). The result is not capped.
func SuggestionList(input string, options []string) []string {
	d := newLexicalDistance(input)
	threshold := len(d.inputRunes)*4/10 + 1
	type scored struct {
		option   string
		distance int
	}
	var kept []scored
	for _, o := range options {
		if dist, ok := d.measure(o, threshold); ok {
			kept = append(kept, scored{o, dist})
		}
	}
	slices.SortStableFunc(kept, func(a, b scored) int {
		if a.distance != b.distance {
			return a.distance - b.distance
		}
		return naturalCompare(a.option, b.option)
	})
	out := make([]string, len(kept))
	for i, k := range kept {
		out[i] = k.option
	}
	return out
}

// suggest returns at most MaxSuggestions options similar to input.
func suggest(input string, options []string) []string {
	s := SuggestionList(input, options)
	if len(s) == 0 {
		return nil
	}
	if len(s) > MaxSuggestions {
		s = s[:MaxSuggestions]
	}
	return s
}

// didYouMean formats suggestions the way graphql-js does, with a leading
// space: ` Did you mean "a", "b", or "c"?`, or "" without suggestions.
// subMessage, when set, goes after "mean" ("the enum value").
func didYouMean(subMessage string, suggestions []string) string {
	if len(suggestions) == 0 {
		return ""
	}
	if len(suggestions) > MaxSuggestions {
		suggestions = suggestions[:MaxSuggestions]
	}
	var b strings.Builder
	b.WriteString(" Did you mean ")
	if subMessage != "" {
		b.WriteString(subMessage)
		b.WriteByte(' ')
	}
	q := func(s string) string { return `"` + s + `"` }
	switch len(suggestions) {
	case 1:
		b.WriteString(q(suggestions[0]))
	case 2:
		b.WriteString(q(suggestions[0]) + " or " + q(suggestions[1]))
	default:
		for _, s := range suggestions[:len(suggestions)-1] {
			b.WriteString(q(s) + ", ")
		}
		b.WriteString("or " + q(suggestions[len(suggestions)-1]))
	}
	b.WriteByte('?')
	return b.String()
}

type lexicalDistance struct {
	input      string
	inputLower string
	inputRunes []rune
	rows       [3][]int
}

func newLexicalDistance(input string) *lexicalDistance {
	lower := strings.ToLower(input)
	d := &lexicalDistance{input: input, inputLower: lower, inputRunes: []rune(lower)}
	for i := range d.rows {
		d.rows[i] = make([]int, len(d.inputRunes)+1)
	}
	return d
}

// measure returns the distance of option to the input, and false when it
// exceeds threshold.
func (d *lexicalDistance) measure(option string, threshold int) (int, bool) {
	if option == d.input {
		return 0, true
	}
	lower := strings.ToLower(option)
	if lower == d.inputLower {
		return 1, true
	}
	a, b := []rune(lower), d.inputRunes
	if len(a) < len(b) {
		a, b = b, a
	}
	if len(a)-len(b) > threshold {
		return 0, false
	}
	rows := d.rows // b is never longer than the input, so the rows fit
	for j := 0; j <= len(b); j++ {
		rows[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		up, cur := rows[(i-1)%3], rows[i%3]
		cur[0] = i
		smallest := i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cell := min(up[j]+1, cur[j-1]+1, up[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				cell = min(cell, rows[(i-2)%3][j-2]+1)
			}
			smallest = min(smallest, cell)
			cur[j] = cell
		}
		if smallest > threshold {
			return 0, false
		}
	}
	dist := rows[len(a)%3][len(b)]
	return dist, dist <= threshold
}

// naturalCompare orders strings bytewise, except that runs of digits compare
// by numeric value, like graphql-js's naturalCompare.
func naturalCompare(a, b string) int {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		ca, cb := a[i], b[j]
		if isDigit(ca) && isDigit(cb) {
			var na, nb uint64
			na, i = readNumber(a, i)
			nb, j = readNumber(b, j)
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
			continue
		}
		if ca != cb {
			if ca < cb {
				return -1
			}
			return 1
		}
		i++
		j++
	}
	return len(a) - len(b)
}

// readNumber reads digits from s[i:] the way graphql-js does: a leading zero
// ends the number after itself. The value saturates instead of overflowing.
func readNumber(s string, i int) (uint64, int) {
	var n uint64
	for {
		d := uint64(s[i] - '0')
		i++
		if n > (1<<64-1-d)/10 {
			n = 1<<64 - 1
		} else {
			n = n*10 + d
		}
		if i >= len(s) || !isDigit(s[i]) || n == 0 {
			return n, i
		}
	}
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }
