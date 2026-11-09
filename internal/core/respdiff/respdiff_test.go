package respdiff_test

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/respdiff"
)

func body(s string) respdiff.Response { return respdiff.Response{StatusCode: 200, Body: []byte(s)} }

// changes renders the changes as "kind path left -> right (types)".
func changes(d respdiff.BodyDiff) []string {
	var out []string
	for _, c := range d.Changes {
		s := string(c.Kind) + " " + c.Path
		switch c.Kind {
		case respdiff.Added:
			s += " " + c.Right
		case respdiff.Removed:
			s += " " + c.Left
		default:
			s += " " + c.Left + " -> " + c.Right
		}
		if c.Kind == respdiff.Changed && c.LeftType != "" {
			s += " (" + c.LeftType + " -> " + c.RightType + ")"
		}
		out = append(out, s)
	}
	return out
}

func codes(d respdiff.BodyDiff) []string {
	var out []string
	for _, n := range d.Notices {
		out = append(out, n.Code)
	}
	return out
}

func TestJSONChanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		left, right string
		want        []string
		identical   bool
		notices     []string
	}{
		{name: "same values, other formatting", left: `{"a":1,"b":[1,2]}`, right: "{\n  \"a\": 1,\n  \"b\": [1, 2]\n}", identical: true},
		{name: "numbers by value", left: `{"n":1.0,"e":1e2}`, right: `{"n":1,"e":100}`, identical: true},
		{name: "changed value", left: `{"user":{"name":"Ada","age":36}}`, right: `{"user":{"name":"Ada","age":37}}`,
			want: []string{"changed $.user.age 36 -> 37"}},
		{name: "added and removed members", left: `{"a":1,"b":2}`, right: `{"a":1,"c":{"d":true}}`,
			want: []string{"removed $.b 2", "added $.c {\"d\":true}"}},
		{name: "type change", left: `{"id":"42","tags":null}`, right: `{"id":42,"tags":[]}`,
			want: []string{"changed $.id \"42\" -> 42 (string -> number)", "changed $.tags null -> [] (null -> array)"}},
		{name: "arrays by index", left: `[1,2,3]`, right: `[1,3]`,
			want: []string{"changed $[1] 2 -> 3", "removed $[2] 3"}, notices: []string{respdiff.CodeArraysByIndex}},
		{name: "array grown", left: `{"items":[]}`, right: `{"items":[{"id":1}]}`,
			want: []string{"added $.items[0] {\"id\":1}"}, notices: []string{respdiff.CodeArraysByIndex}},
		{name: "odd keys", left: `{"a b":1,"it's":2,"x":{"1st":3}}`, right: `{"a b":2,"it's":3,"x":{"1st":4}}`,
			want: []string{"changed $['a b'] 1 -> 2", `changed $['it\'s'] 2 -> 3`, "changed $.x['1st'] 3 -> 4"}},
		{name: "member order only", left: `{"a":1,"b":2}`, right: `{"b":2,"a":1}`, identical: true, notices: []string{respdiff.CodeKeyOrder}},
		{name: "duplicate keys paired in order", left: `{"k":1,"k":2}`, right: `{"k":1,"k":3}`, want: []string{"changed $.k 2 -> 3"}},
		{name: "top-level scalars", left: `"a"`, right: `"b"`, want: []string{`changed $ "a" -> "b"`}},
		{name: "html not escaped", left: `{"h":"<b>"}`, right: `{"h":"<i>"}`, want: []string{`changed $.h "<b>" -> "<i>"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := respdiff.Body(body(tt.left), body(tt.right), respdiff.Limits{})
			if d.Mode != respdiff.ModeJSON {
				t.Fatalf("mode %s, notices %v", d.Mode, d.Notices)
			}
			if got := changes(d); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("changes\n got %q\nwant %q", got, tt.want)
			}
			if d.Identical != tt.identical {
				t.Errorf("identical = %v", d.Identical)
			}
			if got := codes(d); !reflect.DeepEqual(got, tt.notices) {
				t.Errorf("notices %q, want %q", got, tt.notices)
			}
			if d.Added+d.Removed+d.Changed != len(tt.want) {
				t.Errorf("counts %d/%d/%d", d.Added, d.Removed, d.Changed)
			}
		})
	}
}

// side returns one side's text of the rows, as shown.
func side(rows []respdiff.Row, left bool) string {
	var b strings.Builder
	n := 0
	for _, r := range rows {
		l := r.Right
		if left {
			l = r.Left
		}
		if l == nil {
			continue
		}
		n++
		if l.N != n {
			return fmt.Sprintf("line %d numbered %d", n, l.N)
		}
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

func render(rows []respdiff.Row) []string {
	var out []string
	for _, r := range rows {
		l, rt := "", ""
		if r.Left != nil {
			l = r.Left.Text
		}
		if r.Right != nil {
			rt = r.Right.Text
		}
		out = append(out, fmt.Sprintf("%-7s %-18s|%s", r.Kind, l, rt))
	}
	return out
}

// The rows show each side pretty-printed, lined up by path.
func TestJSONRows(t *testing.T) {
	t.Parallel()
	d := respdiff.Body(body(`{"id":1,"tags":["a","b"],"old":true}`), body(`{"id":2,"tags":["a"],"new":{"x":null}}`), respdiff.Limits{})
	want := []string{
		`same    {                 |{`,
		`changed   "id": 1,        |  "id": 2,`,
		`same      "tags": [       |  "tags": [`,
		`same        "a",          |    "a"`,
		`removed     "b"           |`,
		`same      ],              |  ],`,
		`removed   "old": true     |`,
		`added                     |  "new": {`,
		`added                     |    "x": null`,
		`added                     |  }`,
		`same    }                 |}`,
	}
	if got := render(d.Rows); !reflect.DeepEqual(got, want) {
		t.Errorf("rows\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got, want := side(d.Rows, true), "{\n  \"id\": 1,\n  \"tags\": [\n    \"a\",\n    \"b\"\n  ],\n  \"old\": true\n}\n"; got != want {
		t.Errorf("left side\n%s", got)
	}
	if got, want := side(d.Rows, false), "{\n  \"id\": 2,\n  \"tags\": [\n    \"a\"\n  ],\n  \"new\": {\n    \"x\": null\n  }\n}\n"; got != want {
		t.Errorf("right side\n%s", got)
	}
	paths := map[string]bool{}
	for _, r := range d.Rows {
		paths[r.Path] = true
	}
	for _, p := range []string{"$", "$.id", "$.tags[1]", "$.new"} {
		if !paths[p] {
			t.Errorf("no row with path %s", p)
		}
	}
}

func TestHeaders(t *testing.T) {
	t.Parallel()
	left := []respdiff.Header{{"Content-Type", "application/json"}, {"Set-Cookie", "a=1"}, {"Set-Cookie", "b=2"}, {"X-Old", "1"}, {"ETag", `"v1"`}}
	right := []respdiff.Header{{"content-type", "application/json"}, {"set-cookie", "a=1"}, {"ETAG", `"v2"`}, {"X-New", "yes"}}
	got := respdiff.Headers(left, right)
	var s []string
	for _, h := range got {
		s = append(s, fmt.Sprintf("%s %s %q %q", h.Kind, h.Name, h.Left, h.Right))
	}
	want := []string{
		`same Content-Type ["application/json"] ["application/json"]`,
		`changed Set-Cookie ["a=1" "b=2"] ["a=1"]`,
		`removed X-Old ["1"] []`,
		`changed ETag ["\"v1\""] ["\"v2\""]`,
		`added X-New [] ["yes"]`,
	}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("headers\n got %q\nwant %q", s, want)
	}
	res := respdiff.Compare(respdiff.Response{StatusCode: 200, Status: "200 OK", Headers: left},
		respdiff.Response{StatusCode: 404, Status: "Not Found", Headers: right}, respdiff.Limits{})
	if res.HeadersAdded != 1 || res.HeadersRemoved != 1 || res.HeadersChanged != 2 {
		t.Errorf("counts %d %d %d", res.HeadersAdded, res.HeadersRemoved, res.HeadersChanged)
	}
	if res.Status != (respdiff.StatusDiff{Left: "200 OK", Right: "404 Not Found", Changed: true}) {
		t.Errorf("status %+v", res.Status)
	}
}

func TestLineDiff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, left, right string
		want              []string
	}{
		{name: "same", left: "a\nb\n", right: "a\nb", want: []string{"same a|a", "same b|b"}},
		{name: "changed line", left: "a\nb\nc", right: "a\nB\nc", want: []string{"same a|a", "changed b|B", "same c|c"}},
		{name: "added and removed", left: "a\nb\nc\nd", right: "a\nc\nd\ne",
			want: []string{"same a|a", "removed b|", "same c|c", "same d|d", "added |e"}},
		{name: "run of changes pairs up", left: "x\n1\n2\n3\ny", right: "x\nA\nB\ny",
			want: []string{"same x|x", "changed 1|A", "changed 2|B", "removed 3|", "same y|y"}},
		{name: "empty left", left: "", right: "a\nb", want: []string{"added |a", "added |b"}},
		{name: "CRLF", left: "a\r\nb\r\n", right: "a\nb\n", want: []string{"same a|a", "same b|b"}},
		{name: "not JSON", left: "<p>hi</p>", right: "<p>ho</p>", want: []string{"changed <p>hi</p>|<p>ho</p>"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := respdiff.Body(body(tt.left), body(tt.right), respdiff.Limits{})
			if d.Mode != respdiff.ModeText {
				t.Fatalf("mode %s", d.Mode)
			}
			var got []string
			for _, r := range d.Rows {
				l, rt := "", ""
				if r.Left != nil {
					l = r.Left.Text
				}
				if r.Right != nil {
					rt = r.Right.Text
				}
				got = append(got, string(r.Kind)+" "+l+"|"+rt)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("rows\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// The line diff is minimal on random edits and always rebuilds both
// texts.
func TestLineDiffRebuildsBothSides(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range 200 {
		var a []string
		for range rng.IntN(40) {
			a = append(a, string(rune('a'+rng.IntN(5))))
		}
		b := slices.Clone(a)
		for range rng.IntN(6) {
			switch p := rng.IntN(len(b) + 1); rng.IntN(3) {
			case 0:
				b = slices.Insert(b, p, "+"+string(rune('a'+rng.IntN(5))))
			case 1:
				if p < len(b) {
					b = slices.Delete(b, p, p+1)
				}
			default:
				if p < len(b) {
					b[p] = "~"
				}
			}
		}
		left, right := strings.Join(a, "\n"), strings.Join(b, "\n")
		d := respdiff.Body(body("x\n"+left), body("x\n"+right), respdiff.Limits{})
		if got := side(d.Rows, true); got != "x\n"+joinLines(a) {
			t.Fatalf("case %d: left side %q, want %q", i, got, "x\n"+joinLines(a))
		}
		if got := side(d.Rows, false); got != "x\n"+joinLines(b) {
			t.Fatalf("case %d: right side %q, want %q", i, got, "x\n"+joinLines(b))
		}
		if d.Identical != (left == right) {
			t.Fatalf("case %d: identical %v", i, d.Identical)
		}
		// Minimal: as many lines removed and added as the longest common
		// subsequence leaves.
		if edits, want := d.Removed+d.Added+2*d.Changed, len(a)+len(b)-2*lcs(a, b); edits != want {
			t.Fatalf("case %d: %d lines removed and added, want %d", i, edits, want)
		}
	}
}

// lcs is the length of the longest common subsequence of a and b.
func lcs(a, b []string) int {
	prev := make([]int, len(b)+1)
	for i := range a {
		cur := make([]int, len(b)+1)
		for j := range b {
			if a[i] == b[j] {
				cur[j+1] = prev[j] + 1
			} else {
				cur[j+1] = max(prev[j+1], cur[j])
			}
		}
		prev = cur
	}
	return prev[len(b)]
}

func joinLines(ls []string) string {
	if len(ls) == 0 {
		return ""
	}
	return strings.Join(ls, "\n") + "\n"
}

func TestLimits(t *testing.T) {
	t.Parallel()
	many := func(n int, f string) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, f+"\n", i)
		}
		return b.String()
	}
	deep := strings.Repeat("[", 30) + strings.Repeat("]", 30)
	tests := []struct {
		name        string
		left, right respdiff.Response
		lim         respdiff.Limits
		mode        string
		identical   bool
		notices     []string
		maxRows     int
	}{
		{name: "too large, equal", left: body(strings.Repeat("x", 100)), right: body(strings.Repeat("x", 100)), lim: respdiff.Limits{MaxBodyBytes: 50},
			mode: respdiff.ModeTooLarge, identical: true, notices: []string{respdiff.CodeTooLarge}},
		{name: "too large, different", left: body(strings.Repeat("x", 100)), right: body("y"), lim: respdiff.Limits{MaxBodyBytes: 50},
			mode: respdiff.ModeTooLarge, notices: []string{respdiff.CodeTooLarge}},
		{name: "too many edits", left: body(many(50, "a%d")), right: body(many(50, "b%d")), lim: respdiff.Limits{MaxEdits: 10},
			mode: respdiff.ModeText, notices: []string{respdiff.CodeTooManyChanges}},
		{name: "too many lines", left: body(many(30, "%d")), right: body(many(30, "%d")), lim: respdiff.Limits{MaxLines: 10},
			mode: respdiff.ModeText, identical: true, notices: []string{respdiff.CodeTooManyLines}, maxRows: 10},
		{name: "rows limited", left: body(many(30, "%d")), right: body(many(30, "x%d")), lim: respdiff.Limits{MaxRows: 5},
			mode: respdiff.ModeText, notices: []string{respdiff.CodeRowsLimited}, maxRows: 5},
		{name: "JSON too deep", left: body(deep), right: body(deep), lim: respdiff.Limits{MaxDepth: 10},
			mode: respdiff.ModeText, identical: true, notices: []string{respdiff.CodeJSONLimits}},
		{name: "JSON too many values", left: body(`[1,2,3,4,5]`), right: body(`[1,2,3,4,6]`), lim: respdiff.Limits{MaxNodes: 3},
			mode: respdiff.ModeText, notices: []string{respdiff.CodeJSONLimits}},
		{name: "changes limited", left: body(`[1,2,3,4,5]`), right: body(`[6,7,8,9,10]`), lim: respdiff.Limits{MaxChanges: 2},
			mode: respdiff.ModeJSON, notices: []string{respdiff.CodeArraysByIndex, respdiff.CodeChangesLimited}},
		{name: "one side not JSON", left: body(`{"a":1}`), right: body(`<html>`),
			mode: respdiff.ModeText, notices: []string{respdiff.CodeNotJSON}},
		{name: "clipped", left: respdiff.Response{Body: []byte(`{"a":`), Clipped: true, Size: 100}, right: body(`{"a":1}`),
			mode: respdiff.ModeText, notices: []string{respdiff.CodeClipped, respdiff.CodeNotJSON}},
		{name: "binary", left: body("\xff\x00\x01"), right: body("\xff\x00\x02"), mode: respdiff.ModeBinary, notices: []string{respdiff.CodeBinary}},
		{name: "empty", left: body(""), right: body(""), mode: respdiff.ModeEmpty, identical: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := respdiff.Body(tt.left, tt.right, tt.lim)
			if d.Mode != tt.mode || d.Identical != tt.identical {
				t.Errorf("mode %s identical %v, want %s %v", d.Mode, d.Identical, tt.mode, tt.identical)
			}
			if got := codes(d); !reflect.DeepEqual(got, tt.notices) {
				t.Errorf("notices %q, want %q", got, tt.notices)
			}
			for _, n := range d.Notices {
				if n.Message == "" {
					t.Errorf("notice %s has no message", n.Code)
				}
			}
			if tt.maxRows > 0 && len(d.Rows) > tt.maxRows {
				t.Errorf("%d rows, want at most %d", len(d.Rows), tt.maxRows)
			}
		})
	}
	// The fallback still shows both texts.
	d := respdiff.Body(body(many(50, "a%d")), body(many(50, "b%d")), respdiff.Limits{MaxEdits: 10})
	if got := side(d.Rows, true); got != many(50, "a%d") {
		t.Errorf("fallback left side %q", got)
	}
	if d.Changed != 50 || d.Added != 0 || d.Removed != 0 {
		t.Errorf("fallback counts %d %d %d", d.Added, d.Removed, d.Changed)
	}
	if !strings.Contains(respdiff.Body(body("\xff\x01"), body("\xff\x02"), respdiff.Limits{}).Notices[0].Message, "byte 1") {
		t.Error("binary notice does not say where the bodies differ")
	}
}

func BenchmarkJSON64KiB(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("[")
	for i := range 1500 {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"id":%d,"name":"user %d","tags":["a","b"]}`, i, i)
	}
	sb.WriteString("]")
	left := body(sb.String())
	right := body(strings.Replace(sb.String(), `"user 700"`, `"user seven hundred"`, 1))
	for b.Loop() {
		respdiff.Body(left, right, respdiff.Limits{})
	}
}
