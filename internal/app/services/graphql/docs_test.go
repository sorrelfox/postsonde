package graphql

import (
	"cmp"
	"slices"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/testutil/testenv"
)

func TestDocsOverview(t *testing.T) {
	t.Parallel()
	svc := cachedService(t)
	got, err := svc.DocsOverview(t.Context(), DocsInputDTO{Key: testEndpoint})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Schema || got.Description == "" {
		t.Errorf("overview = %+v", got)
	}
	wantRoots := []DocsRootDTO{{"query", "Query"}, {"mutation", "Mutation"}, {"subscription", "Subscription"}}
	if !slices.Equal(got.Roots, wantRoots) {
		t.Errorf("roots = %+v", got.Roots)
	}
	if len(got.Types) != 37 || got.Types[0].Name != "Actor" {
		t.Errorf("types = %d, first %+v", len(got.Types), got.Types[0])
	}
	if !slices.IsSortedFunc(got.Types, func(a, b DocsTypeNameDTO) int { return cmp.Compare(a.Name, b.Name) }) {
		t.Error("types are not sorted")
	}
	for _, ty := range got.Types {
		if ty.Name[0] == '_' {
			t.Errorf("introspection type %s", ty.Name)
		}
	}

	// Without a schema: empty lists, not null.
	none, err := svc.DocsOverview(t.Context(), DocsInputDTO{Key: "https://other.test/graphql"})
	if err != nil || none.Schema || none.Types == nil || none.Roots == nil || none.Directives == nil {
		t.Errorf("no schema = %+v, %v", none, err)
	}
}

func TestDocsType(t *testing.T) {
	t.Parallel()
	svc := cachedService(t)
	get := func(name string) DocsTypeDTO {
		t.Helper()
		got, err := svc.DocsType(t.Context(), DocsTypeInputDTO{Key: testEndpoint, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	q := get("Query")
	if !q.Found || q.Kind != "OBJECT" || q.Root != "query" || len(q.Fields) != 7 {
		t.Fatalf("Query = %+v", q)
	}
	repo := q.Fields[slices.IndexFunc(q.Fields, func(f DocsFieldDTO) bool { return f.Name == "repository" })]
	if repo.TypeName != "Repository" || repo.TypeKind != "OBJECT" || len(repo.Args) == 0 {
		t.Errorf("Query.repository = %+v", repo)
	}
	if a := repo.Args[0]; a.Type == "" || a.TypeName == "" || a.TypeKind == "" {
		t.Errorf("argument = %+v", a)
	}

	user := get("User")
	if !slices.Contains(user.Interfaces, "Node") {
		t.Errorf("User interfaces = %v", user.Interfaces)
	}
	i := slices.IndexFunc(user.Fields, func(f DocsFieldDTO) bool { return f.Name == "databaseId" })
	if i < 0 || !user.Fields[i].Deprecated {
		t.Errorf("User.databaseId = %+v", user.Fields)
	}

	node := get("Node")
	if node.Kind != "INTERFACE" || !slices.Contains(node.Implementations, "User") || !slices.Contains(node.PossibleTypes, "User") {
		t.Errorf("Node = %+v %+v", node.Implementations, node.PossibleTypes)
	}
	union := get("SearchResultItem")
	if union.Kind != "UNION" || len(union.PossibleTypes) != 5 {
		t.Errorf("SearchResultItem = %+v", union.PossibleTypes)
	}
	enum := get("SearchType")
	if len(enum.EnumValues) != 4 || !enum.EnumValues[3].Deprecated || enum.EnumValues[3].DeprecationReason == "" {
		t.Errorf("SearchType = %+v", enum.EnumValues)
	}
	input := get("IssueOrder")
	if input.Kind != "INPUT_OBJECT" || len(input.InputFields) != 2 {
		t.Errorf("IssueOrder = %+v", input)
	}

	// Introspection types are found though not listed; unknown names are not.
	if ty := get("__Type"); !ty.Found || len(ty.Fields) == 0 {
		t.Errorf("__Type = %+v", ty)
	}
	if ty := get("Nope"); ty.Found || ty.Fields == nil {
		t.Errorf("Nope = %+v", ty)
	}
	if ty, _ := svc.DocsType(t.Context(), DocsTypeInputDTO{Key: "", Name: "User"}); ty.Found {
		t.Errorf("no key = %+v", ty)
	}
}

func TestDocsSearch(t *testing.T) {
	t.Parallel()
	svc := cachedService(t)
	got, err := svc.DocsSearch(t.Context(), DocsSearchInputDTO{Key: testEndpoint, Query: "login"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Schema || got.Count < 3 || len(got.Items) != got.Count {
		t.Fatalf("login = %+v", got)
	}
	if h := got.Items[0]; h.Kind != "field" || h.Name != "login" || h.Type == "" || h.Member != "login" || h.Detail == "" {
		t.Errorf("first hit = %+v", h)
	}
	got, _ = svc.DocsSearch(t.Context(), DocsSearchInputDTO{Key: testEndpoint, Query: "i", Limit: 2})
	if len(got.Items) != 2 || got.Count <= 2 {
		t.Errorf("limit = %d of %d", len(got.Items), got.Count)
	}
	got, _ = svc.DocsSearch(t.Context(), DocsSearchInputDTO{Key: "", Query: "login"})
	if got.Schema || got.Items == nil || len(got.Items) != 0 {
		t.Errorf("no schema = %+v", got)
	}
}

// TestDocsLargeSchemaIsFast: the docs explorer stays interactive on a
// schema of 3000 object types of 60 fields, 3000 enums and 3000 input
// objects: the overview, the 3000-field root and a search, each well under
// 100 ms once the schema and its index are in memory.
func TestDocsLargeSchemaIsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	svc := largeService(t)
	ctx := t.Context()
	// Load the schema and build the index once.
	if _, err := svc.DocsSearch(ctx, DocsSearchInputDTO{Key: testEndpoint, Query: "x"}); err != nil {
		t.Fatal(err)
	}
	// The best of several rounds is checked rather than the slowest: a
	// loaded machine (a parallel go test ./...) or a GC pause can push any
	// one round past the bound, but a real regression makes every round
	// slow.
	const rounds = 5
	var best time.Duration
	for r := range rounds {
		start := time.Now()
		ov, _ := svc.DocsOverview(ctx, DocsInputDTO{Key: testEndpoint})
		q, _ := svc.DocsType(ctx, DocsTypeInputDTO{Key: testEndpoint, Name: "Query"})
		s, _ := svc.DocsSearch(ctx, DocsSearchInputDTO{Key: testEndpoint, Query: "fld9"})
		if took := time.Since(start); r == 0 || took < best {
			best = took
		}
		if len(ov.Types) < 9000 || len(q.Fields) != 3000 || s.Count == 0 {
			t.Fatalf("overview %d types, Query %d fields, search %d", len(ov.Types), len(q.Fields), s.Count)
		}
	}
	if limit := testenv.Budget(100 * time.Millisecond); best > limit {
		t.Errorf("overview + root + search took %v at best of %d, want under %v", best, rounds, limit)
	}
	t.Logf("overview + root + search: %v at best of %d", best, rounds)
}
