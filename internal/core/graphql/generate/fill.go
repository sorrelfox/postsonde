package generate

import (
	"sort"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/lexer"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
	"github.com/sorrelfox/postsonde/internal/core/graphql/printer"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
	"github.com/sorrelfox/postsonde/internal/core/graphql/validate"
)

// FillResult is the edit that fills in the fields at a cursor.
type FillResult struct {
	// From and To are the byte range Text replaces: the cursor, widened
	// over the blanks before it on its line when the fields go on lines of
	// their own.
	From, To int
	// Text is the fields, one per line, indented like the selection set's
	// fields.
	Text string
	// Type is the selection set's type, when known.
	Type string
	// Count is the number of selections added at the cursor's level.
	Count     int
	Truncated bool
	Reason    Reason
}

// Fill writes the fields of the selection set at byte offset off of src
// that are not selected yet: the leaf fields, and with a Depth above 1 the
// composite ones with their own selections (see the package comment).
// Fields with required arguments are left out: a selection cannot declare
// variables. The fields go at the cursor, one per line.
func Fill(s *schema.Schema, src string, off int, opts Options) FillResult {
	off = min(max(off, 0), len(src))
	res := FillResult{From: off, To: off}
	if insideToken(src, off) {
		res.Reason = ReasonNotInSelection
		return res
	}
	doc, _ := parser.Parse(src)
	path := ast.NodePathAt(doc, off)
	at := -1
	for i, n := range path {
		if set, ok := n.(*ast.SelectionSet); ok && off > set.LBrace && (set.RBrace < 0 || off <= set.RBrace) {
			at = i
		}
	}
	if at < 0 {
		res.Reason = ReasonNotInSelection
		return res
	}
	set := path[at].(*ast.SelectionSet)
	if at+1 < len(path) {
		// On a selection: only at its very start or end.
		// A placeholder for a missing field (in "{ }") is no selection.
		if f, ok := path[at+1].(*ast.Field); ok && f.Alias == nil && f.Name != nil && f.Name.Missing() {
			path = path[:at+1]
		} else if p := path[at+1].Pos(); p.Start != off && p.End != off {
			res.Reason = ReasonNotInSelection
			return res
		}
	}
	ti := validate.NewTypeInfo(s)
	ti.EnterPath(path[:at+1])
	t := ti.ParentType()
	if t == nil {
		res.Reason = ReasonUnknownType
		return res
	}
	res.Type = t.Name

	// What the set selects already: field names, inline fragment types.
	skip := map[string]bool{}
	for _, sel := range set.Selections {
		switch sel := sel.(type) {
		case *ast.Field:
			if sel.Name != nil && !sel.Name.Missing() {
				skip[sel.Name.Value] = true
			}
		case *ast.InlineFragment:
			if sel.TypeCondition != nil && sel.TypeCondition.Type != nil && sel.TypeCondition.Type.Name != nil {
				skip["... on "+sel.TypeCondition.Type.Name.Value] = true
			}
		}
	}
	g := newGen(s, opts, false)
	nodes := g.selectionAvoiding(t, g.opts.depth(), skip, aliases(set))
	if len(nodes) == 0 {
		res.Reason = ReasonNothingToAdd
		return res
	}
	res.Count = len(nodes)
	res.Truncated = g.truncated

	// Layout: the fields on lines of their own, indented like the set's
	// first field on its own line, else one level past the "{" line.
	indent := lineIndent(src, set.LBrace) + printer.Indent
	if len(set.Selections) > 0 {
		first := set.Selections[0].Pos().Start
		if ls := lineStart(src, first); strings.TrimSpace(src[ls:first]) == "" {
			indent = src[ls:first]
		}
	}
	var b strings.Builder
	writeNodes(&b, nodes, indent)
	text := strings.TrimSuffix(b.String(), "\n")
	ls := lineStart(src, off)
	if strings.TrimSpace(src[ls:off]) == "" {
		res.From = ls // the blanks before the cursor make way for the indent
	} else {
		text = "\n" + text
	}
	le := lineEnd(src, off)
	if rest := strings.TrimSpace(src[off:le]); rest != "" {
		// Text after the cursor goes to the next line: a "}" under the "{"
		// line, anything else under the fields.
		if strings.HasPrefix(rest, "}") {
			text += "\n" + lineIndent(src, set.LBrace)
		} else {
			text += "\n" + indent
		}
		res.To = off + len(src[off:le]) - len(strings.TrimLeft(src[off:le], " \t"))
	}
	res.Text = text
	return res
}

// selectionAvoiding is selection for filling in: the response names in
// taken are in use by aliased selections already there.
func (g *gen) selectionAvoiding(t *schema.Type, depth int, skip map[string]bool, taken map[string]bool) []node {
	for name := range taken {
		skip[name] = true // a field of that name would clash with the alias
	}
	return g.selection(t, depth, []string{t.Name}, skip)
}

// aliases returns the aliases used in set.
func aliases(set *ast.SelectionSet) map[string]bool {
	out := map[string]bool{}
	for _, sel := range set.Selections {
		if f, ok := sel.(*ast.Field); ok && f.Alias != nil && !f.Alias.Missing() {
			out[f.Alias.Value] = true
		}
	}
	return out
}

// insideToken reports whether off lies strictly inside a token or at any
// place in a comment or string, where no field can go.
func insideToken(src string, off int) bool {
	toks := lexer.Tokenize(src, 0)
	i := sort.Search(len(toks), func(i int) bool { return toks[i].End >= off })
	for ; i < len(toks) && toks[i].Start <= off; i++ {
		t := toks[i]
		switch {
		case t.Start < off && off < t.End:
			return true
		case t.Kind == lexer.Comment && t.Start < off && off <= t.End:
			return true // right after a comment: still on the comment line
		}
	}
	return false
}

func lineStart(src string, off int) int {
	return strings.LastIndexAny(src[:off], "\r\n") + 1
}

func lineEnd(src string, off int) int {
	if i := strings.IndexAny(src[off:], "\r\n"); i >= 0 {
		return off + i
	}
	return len(src)
}

// lineIndent returns the blanks that start the line of off.
func lineIndent(src string, off int) string {
	ls := lineStart(src, off)
	line := src[ls:lineEnd(src, ls)]
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}
