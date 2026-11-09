package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

func fixture(t testing.TB) *schema.Schema {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "schema", "testdata", "github-subset.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := schema.ParseIntrospection(data)
	if s == nil {
		t.Fatal(err)
	}
	return s
}

func names(hits []Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		if h.Parent == "" {
			out = append(out, h.Name)
		} else {
			out = append(out, h.Parent+"."+h.Name)
		}
	}
	return out
}

func TestMatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		q, name string
		want    int
	}{
		{"user", "User", tierExact},
		{"us", "User", tierPrefix},
		{"owner", "RepositoryOwner", tierWordStart},
		{"ository", "Repository", tierSubstring},
		{"ro", "RepositoryOwner", tierFuzzy}, // R…O(wner): "ro" is also no substring
		{"rpo", "Repository", tierFuzzy},
		{"wke", "hasWikiEnabled", tierFuzzy},
		{"kn", "hasWikiEnabled", noMatch}, // "k" starts no word
		{"cre", "CREATED_AT", tierPrefix},
		{"at", "CREATED_AT", tierWordStart},
		{"xyz", "User", noMatch},
		{"userlong", "User", noMatch},
	}
	for _, c := range cases {
		if got := match(c.q, c.name, strings.ToLower(c.name)); got != c.want {
			t.Errorf("match(%q, %q) = %d, want %d", c.q, c.name, got, c.want)
		}
	}
}

func TestSearch(t *testing.T) {
	t.Parallel()
	idx := NewIndex(fixture(t))

	// The exact type first, then prefixes; types before fields.
	hits, count := idx.Search("User", 5)
	if len(hits) == 0 || hits[0].Kind != KindType || hits[0].Name != "User" || hits[0].Type != "User" || hits[0].Detail != "OBJECT" {
		t.Fatalf("User = %+v", hits)
	}
	if count < len(hits) {
		t.Errorf("count %d < %d hits", count, len(hits))
	}

	// Fields name their parent, type and the member to show.
	hits, _ = idx.Search("databaseid", 10)
	var found bool
	for _, h := range hits {
		if h.Kind == KindField && h.Parent == "User" && h.Name == "databaseId" {
			found = true
			if !h.Deprecated || h.Type != "User" || h.Member != "databaseId" || h.Detail == "" {
				t.Errorf("User.databaseId = %+v", h)
			}
		}
	}
	if !found {
		t.Errorf("databaseid = %v", names(hits))
	}

	// Enum values, input fields and arguments.
	hits, _ = idx.Search("DISCUSSION", 5)
	if len(hits) == 0 || hits[0].Kind != KindEnumValue || hits[0].Parent != "SearchType" || !hits[0].Deprecated || hits[0].Detail != "" {
		t.Errorf("DISCUSSION = %+v", hits)
	}
	hits, _ = idx.Search("assigneeIds", 5)
	if len(hits) == 0 || hits[0].Kind != KindInputField || hits[0].Parent != "CreateIssueInput" || hits[0].Detail != "[ID!]" && !strings.Contains(hits[0].Detail, "ID") {
		t.Errorf("assigneeIds = %+v", hits)
	}
	hits, _ = idx.Search("query.owner", 10)
	var arg bool
	for _, h := range hits {
		if h.Kind == KindArgument && h.Parent == "Query.repository" && h.Name == "owner" && h.Type == "Query" && h.Member == "repository" {
			arg = true
		}
		if h.Kind == KindType {
			t.Errorf("a dotted query found the type %s", h.Name)
		}
	}
	if !arg {
		t.Errorf("query.owner = %v", names(hits))
	}

	// "Type." lists the type's members.
	hits, count = idx.Search("PageInfo.", 50)
	if got := strings.Join(names(hits), " "); got != "PageInfo.endCursor PageInfo.hasNextPage" || count != 2 {
		t.Errorf("PageInfo. = %q (%d)", got, count)
	}

	// Introspection types only when asked; spaces and case ignored.
	for _, h := range must(idx.Search("type", 100)) {
		if strings.HasPrefix(h.Type, "__") {
			t.Errorf("introspection hit %+v", h)
		}
	}
	if hits, _ := idx.Search(" Page Info ", 1); len(hits) != 1 || hits[0].Name != "PageInfo" {
		t.Errorf("spaces = %+v", hits)
	}

	// Nothing for a blank query or no match; the limit holds.
	if hits, count := idx.Search("  ", 10); len(hits) != 0 || count != 0 || hits == nil {
		t.Errorf("blank = %+v, %d", hits, count)
	}
	if hits, count := idx.Search("zzzq", 10); len(hits) != 0 || count != 0 {
		t.Errorf("no match = %+v, %d", hits, count)
	}
	if hits, count := idx.Search("i", 3); len(hits) != 3 || count <= 3 {
		t.Errorf("limit = %d hits of %d", len(hits), count)
	}
}

func must(h []Hit, _ int) []Hit { return h }

func TestSearchRanking(t *testing.T) {
	t.Parallel()
	idx := NewIndex(fixture(t))
	hits, _ := idx.Search("issue", 8)
	got := names(hits)
	// The exact type, then the exact fields and enum values, then prefixes.
	if len(got) < 5 || got[0] != "Issue" || got[1] != "CreateIssuePayload.issue" || got[3] != "SearchType.ISSUE" || got[4] != "IssueEvent" {
		t.Errorf("issue = %v", got)
	}
	// A better tier always wins over the kind; types first within a tier.
	for i := 1; i < len(hits); i++ {
		a, b := hits[i-1], hits[i]
		ta := match("issue", a.Name, strings.ToLower(a.Name))
		tb := match("issue", b.Name, strings.ToLower(b.Name))
		if ta > tb || ta == tb && a.Kind.rank() > b.Kind.rank() {
			t.Errorf("%v before %v", a, b)
		}
	}
}

func TestSummary(t *testing.T) {
	t.Parallel()
	if got := Summary("  First line.\nSecond line."); got != "First line." {
		t.Errorf("Summary = %q", got)
	}
	long := strings.Repeat("é", 200)
	got := Summary(long)
	if n := len([]rune(got)); n != MaxDescription || !strings.HasSuffix(got, "…") {
		t.Errorf("Summary(long) = %d runes %q", n, got)
	}
}

// largeSchema has types object types of fields fields each, plus as many
// enums and input objects.
func largeSchema(types, fields int) *schema.Schema {
	s := &schema.Schema{QueryType: "Query"}
	query := &schema.Type{Kind: schema.KindObject, Name: "Query"}
	for i := range types {
		t := &schema.Type{Kind: schema.KindObject, Name: fmt.Sprintf("Type%d", i), Description: "A generated type."}
		for f := range fields {
			t.Fields = append(t.Fields, &schema.Field{
				Name: fmt.Sprintf("field%d", f),
				Type: schema.Named(schema.KindScalar, "String"),
				Args: []*schema.InputValue{{Name: "first", Type: schema.Named(schema.KindScalar, "Int")}},
			})
		}
		query.Fields = append(query.Fields, &schema.Field{Name: fmt.Sprintf("type%d", i), Type: schema.Named(schema.KindObject, t.Name)})
		s.Types = append(s.Types, t,
			&schema.Type{Kind: schema.KindEnum, Name: fmt.Sprintf("Enum%d", i), EnumValues: []*schema.EnumValue{{Name: "A"}, {Name: "B"}}},
			&schema.Type{Kind: schema.KindInputObject, Name: fmt.Sprintf("Input%d", i), InputFields: []*schema.InputValue{{Name: "value", Type: schema.Named(schema.KindScalar, "String")}}},
		)
	}
	s.Types = append(s.Types, query)
	return s
}

// TestSearchLargeSchemaIsFast: GQL-14's search stays interactive on a
// schema of 3000 types of 60 fields (with an argument each).
func TestSearchLargeSchemaIsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	idx := NewIndex(largeSchema(3000, 60))
	queries := []string{"f", "field5", "type29", "Type2999.field59", "fd9", "value", "zzz"}
	// Each query's best of several runs is checked rather than the worst
	// call: a loaded machine (a parallel go test ./...) or a GC pause can
	// push any single call past the bound, but a real regression makes
	// every run of that query slow.
	const rounds = 5
	best := make([]time.Duration, len(queries))
	for r := range rounds {
		for i, q := range queries {
			start := time.Now()
			hits, count := idx.Search(q, 100)
			if took := time.Since(start); r == 0 || took < best[i] {
				best[i] = took
			}
			if q == "Type2999.field59" && (len(hits) == 0 || hits[0].Parent != "Type2999" || hits[0].Name != "field59") {
				t.Errorf("%s = %v (%d)", q, names(hits), count)
			}
		}
	}
	slowest := slices.Max(best)
	if slowest > 100*time.Millisecond {
		t.Errorf("search for %q took %v at best of %d, want well under 100ms",
			queries[slices.Index(best, slowest)], slowest, rounds)
	}
	t.Logf("%d entries, slowest query %v (best of %d)", idx.Len(), slowest, rounds)
}

func BenchmarkSearchLargeSchema(b *testing.B) {
	idx := NewIndex(largeSchema(3000, 60))
	for b.Loop() {
		idx.Search("fd9", 100)
	}
}
