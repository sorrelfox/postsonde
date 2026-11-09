package respdiff

import (
	"slices"
	"strconv"
	"strings"
)

// op is one step of a line diff.
type op byte

const (
	opSame op = iota
	opDel
	opIns
)

// lineOps returns the line diff of a and b: the Myers shortest edit
// script (E. Myers, "An O(ND) difference algorithm and its variations",
// 1986) after the common first and last lines are set aside. When more
// than maxEdits lines would be added and removed, ok is false and the
// lines between the common ones are all removed, then all added. Each
// step is opSame (a[i] = b[j]), opDel (a[i]) or opIns (b[j]).
func lineOps(a, b []string, maxEdits int) (ops []op, ok bool) {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	mid, ok := myers(a[pre:len(a)-suf], b[pre:len(b)-suf], maxEdits)
	ops = make([]op, 0, pre+len(mid)+suf)
	for range pre {
		ops = append(ops, opSame)
	}
	ops = append(ops, mid...)
	for range suf {
		ops = append(ops, opSame)
	}
	return ops, ok
}

// myers returns the shortest edit script of a and b, or, past maxEdits
// edits, every line of a removed and every line of b added.
func myers(a, b []string, maxEdits int) ([]op, bool) {
	n, m := len(a), len(b)
	fallback := func() []op {
		out := make([]op, 0, n+m)
		for range n {
			out = append(out, opDel)
		}
		for range m {
			out = append(out, opIns)
		}
		return out
	}
	if n == 0 || m == 0 {
		return fallback(), true
	}
	limit := min(n+m, maxEdits)
	off := limit + 1
	v := make([]int32, 2*limit+3)
	// trace[d] is v[-d..d] after step d.
	var trace [][]int32
	found := -1
	for d := 0; d <= limit && found < 0; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = int(v[off+k+1])
			} else {
				x = int(v[off+k-1]) + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = int32(x)
			if x >= n && y >= m {
				found = d
				break
			}
		}
		trace = append(trace, slices.Clone(v[off-d:off+d+1]))
	}
	if found < 0 {
		return fallback(), false
	}

	var rev []op
	x, y := n, m
	for d := found; d > 0; d-- {
		prev := trace[d-1]
		get := func(k int) int { return int(prev[k+d-1]) }
		k := x - y
		var pk int
		if k == -d || (k != d && get(k-1) < get(k+1)) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := get(pk)
		py := px - pk
		sx, sy := px+1, py // a deletion
		if pk == k+1 {
			sx, sy = px, py+1 // an insertion
		}
		for x > sx && y > sy {
			rev = append(rev, opSame)
			x--
			y--
		}
		if pk == k+1 {
			rev = append(rev, opIns)
		} else {
			rev = append(rev, opDel)
		}
		x, y = px, py
	}
	for x > 0 && y > 0 {
		rev = append(rev, opSame)
		x--
		y--
	}
	slices.Reverse(rev)
	return rev, true
}

// splitLines splits text into lines; a final line break does not start
// another line, and a CR before a LF is dropped.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// textDiff fills out with the line diff of a and b.
func textDiff(out *BodyDiff, a, b string, lim Limits) {
	la, lb := splitLines(a), splitLines(b)
	if len(la) > lim.MaxLines || len(lb) > lim.MaxLines {
		out.Notices = append(out.Notices, Notice{Code: CodeTooManyLines,
			Message: "A body has more than " + strconv.Itoa(lim.MaxLines) + " lines (" + strconv.Itoa(len(la)) + " and " +
				strconv.Itoa(len(lb)) + "); only the first " + strconv.Itoa(lim.MaxLines) + " are compared."})
		la = la[:min(len(la), lim.MaxLines)]
		lb = lb[:min(len(lb), lim.MaxLines)]
	}
	ops, ok := lineOps(la, lb, lim.MaxEdits)
	if !ok {
		out.Notices = append(out.Notices, Notice{Code: CodeTooManyChanges,
			Message: "The bodies differ in more than " + strconv.Itoa(lim.MaxEdits) + " lines; the lines that differ are shown removed, then added."})
	}
	r := &rows{max: lim.MaxRows}
	i, j := 0, 0
	for p := 0; p < len(ops); {
		if ops[p] == opSame {
			r.add(Same, &la[i], &lb[j], "")
			i++
			j++
			p++
			continue
		}
		// A run of removed and added lines between two same ones: pair
		// them as changed, the rest removed or added.
		var dels, inss []string
		for ; p < len(ops) && ops[p] != opSame; p++ {
			if ops[p] == opDel {
				dels = append(dels, la[i])
				i++
			} else {
				inss = append(inss, lb[j])
				j++
			}
		}
		for k := range max(len(dels), len(inss)) {
			switch {
			case k < len(dels) && k < len(inss):
				r.add(Changed, &dels[k], &inss[k], "")
				out.Changed++
			case k < len(dels):
				r.add(Removed, &dels[k], nil, "")
				out.Removed++
			default:
				r.add(Added, nil, &inss[k], "")
				out.Added++
			}
		}
	}
	out.Identical = a == b
	r.finish(out)
}
