package graphql

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schemacache"
	"github.com/sorrelfox/postsonde/internal/testutil/testenv"
)

func labels(c CompletionDTO) []string {
	out := make([]string, 0, len(c.Items))
	for _, it := range c.Items {
		out = append(out, it.Label)
	}
	return out
}

func item(t *testing.T, c CompletionDTO, label string) CompletionItemDTO {
	t.Helper()
	for _, it := range c.Items {
		if it.Label == label {
			return it
		}
	}
	t.Fatalf("no item %q in %v", label, labels(c))
	return CompletionItemDTO{}
}

// at completes text at the "|" in it, in UTF-16 units.
func at(t *testing.T, svc *GraphQLService, key, text string) CompletionDTO {
	t.Helper()
	i := strings.Index(text, "|")
	src := text[:i] + text[i+1:]
	offset := len([]rune(text[:i])) // the texts below have no characters outside the BMP before "|"
	got, err := svc.Complete(t.Context(), CompleteInputDTO{Key: key, Text: src, Offset: offset})
	if err != nil {
		t.Fatalf("Complete = %v", err)
	}
	return got
}

func TestComplete(t *testing.T) {
	t.Parallel()
	svc := cachedService(t)

	// Fields with their types, the replace range in UTF-16 after non-ASCII text.
	c := at(t, svc, testEndpoint, "# café\n{ viewer { lo| } }")
	start := len([]rune("# café\n{ viewer { "))
	if c.Context != "selection" || c.ParentType != "User" || c.From != start || c.To != start+2 || c.Prefix != "lo" || !c.Schema {
		t.Errorf("selection = %+v", c)
	}
	if it := item(t, c, "login"); it.Kind != "field" || it.Detail != "String!" || it.InsertText != "login" {
		t.Errorf("login = %+v", it)
	}

	// Arguments with their hint, enum values, directives.
	c = at(t, svc, testEndpoint, "{ repository(|) { id } }")
	if c.Context != "argument" || c.Owner != "Query.repository" {
		t.Errorf("arguments = %+v", c)
	}
	if it := item(t, c, "owner"); it.Kind != "argument" || it.Suffix != ": " || it.SuffixCursor != 2 {
		t.Errorf("owner = %+v", it)
	}
	c = at(t, svc, testEndpoint, `{ search(query: "x", type: |) { issueCount } }`)
	if c.Context != "value" || item(t, c, "REPOSITORY").Kind != "enum-value" {
		t.Errorf("enum values = %+v", labels(c))
	}
	if it := item(t, c, "DISCUSSION"); !it.Deprecated || it.DeprecationReason == "" {
		t.Errorf("deprecated enum value = %+v", it)
	}
	c = at(t, svc, testEndpoint, "{ viewer @| { login } }")
	if c.Context != "directive" || !slices.Contains(labels(c), "include") || item(t, c, "include").Kind != "directive" {
		t.Errorf("directives = %+v", labels(c))
	}

	// Without a schema: syntax only, offline as ever.
	c = at(t, svc, "", "qu|")
	if c.Schema || c.Context != "document" || item(t, c, "query").Kind != "keyword" {
		t.Errorf("no schema = %+v", c)
	}
	// Inside a comment: nothing.
	if c := at(t, svc, testEndpoint, "# vi|"); len(c.Items) != 0 || c.Items == nil {
		t.Errorf("comment = %+v", c)
	}
}

// largeSchema generates a schema of types object types with fields fields
// each, all reachable from Query, plus enums and input objects.
func largeSchema(types, fields int) *schema.Schema {
	str := func(s string) *string { return &s }
	named := func(kind, name string) *schema.IntrospectionTypeRef {
		return &schema.IntrospectionTypeRef{Kind: kind, Name: str(name)}
	}
	nonNull := func(r *schema.IntrospectionTypeRef) *schema.IntrospectionTypeRef {
		return &schema.IntrospectionTypeRef{Kind: "NON_NULL", OfType: r}
	}
	list := func(r *schema.IntrospectionTypeRef) *schema.IntrospectionTypeRef {
		return &schema.IntrospectionTypeRef{Kind: "LIST", OfType: r}
	}
	is := &schema.IntrospectionSchema{QueryType: &schema.IntrospectionRootType{Name: "Query"}}
	query := schema.IntrospectionType{Kind: "OBJECT", Name: str("Query")}
	for i := range types {
		name := fmt.Sprintf("Type%d", i)
		enum := fmt.Sprintf("Enum%d", i)
		input := fmt.Sprintf("Input%d", i)
		query.Fields = append(query.Fields, schema.IntrospectionField{
			Name:        fmt.Sprintf("type%d", i),
			Description: str("Looks up a " + name + "."),
			Args: []schema.IntrospectionInputValue{
				{Name: "id", Type: nonNull(named("SCALAR", "ID"))},
				{Name: "filter", Type: named("INPUT_OBJECT", input)},
			},
			Type: named("OBJECT", name),
		})
		t := schema.IntrospectionType{Kind: "OBJECT", Name: str(name), Description: str("Generated type " + name + ".")}
		for f := range fields {
			var ref *schema.IntrospectionTypeRef
			switch f % 4 {
			case 0:
				ref = nonNull(named("SCALAR", "String"))
			case 1:
				ref = named("ENUM", enum)
			case 2:
				ref = list(nonNull(named("OBJECT", fmt.Sprintf("Type%d", (i+f)%types))))
			default:
				ref = named("SCALAR", "Int")
			}
			t.Fields = append(t.Fields, schema.IntrospectionField{
				Name:              fmt.Sprintf("field%d", f),
				Description:       str(fmt.Sprintf("Field %d of %s.", f, name)),
				Type:              ref,
				IsDeprecated:      f%10 == 9,
				DeprecationReason: str("Use something else."),
			})
		}
		is.Types = append(is.Types, t,
			schema.IntrospectionType{Kind: "ENUM", Name: str(enum), EnumValues: []schema.IntrospectionEnumValue{{Name: "A"}, {Name: "B"}, {Name: "C"}}},
			schema.IntrospectionType{Kind: "INPUT_OBJECT", Name: str(input), InputFields: []schema.IntrospectionInputValue{
				{Name: "value", Type: named("SCALAR", "String")},
				{Name: "kind", Type: named("ENUM", enum)},
			}},
		)
	}
	is.Types = append(is.Types, query)
	for _, s := range []string{"String", "Int", "ID", "Boolean", "Float"} {
		is.Types = append(is.Types, schema.IntrospectionType{Kind: "SCALAR", Name: str(s)})
	}
	sch, err := schema.FromIntrospection(is)
	if err != nil {
		panic(err)
	}
	return sch
}

// largeService caches a large generated schema: 3000 object types of 60
// fields, 3000 enums and 3000 input objects.
func largeService(tb testing.TB) *GraphQLService {
	tb.Helper()
	dir := tb.TempDir()
	e := &schemacache.Entry{Endpoint: testEndpoint, FetchedAt: time.Now(), Query: schemacache.QueryFull, Schema: largeSchema(3000, 60)}
	if err := (schemacache.Store{Dir: dir}).Save(e); err != nil {
		tb.Fatal(err)
	}
	return New(nil, nil, func() (string, error) { return dir, nil }, nil, nil)
}

// largeDocument is a realistic query of a few hundred lines with a cursor
// at each of the returned offsets.
func largeDocument() (string, []int) {
	var b strings.Builder
	var cursors []int
	for i := range 60 {
		fmt.Fprintf(&b, "query Q%d($id: ID!) {\n  type%d(id: $id, filter: {kind: A}) {\n", i, i*7)
		for f := range 8 {
			fmt.Fprintf(&b, "    field%d\n", f*3)
		}
		b.WriteString("    fi")
		cursors = append(cursors, b.Len())
		b.WriteString("\n    field2 { field0 }\n  }\n}\n\n")
	}
	b.WriteString("{ ty")
	cursors = append(cursors, b.Len())
	b.WriteString(" }\n")
	return b.String(), cursors
}

// TestCompleteLargeSchemaIsFast checks GQL-13's budget: under 50 ms per
// request on a large schema, once the schema is cached in memory.
func TestCompleteLargeSchemaIsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	svc := largeService(t)
	text, cursors := largeDocument()
	// The first call loads the cache from disk; later calls reuse it.
	if c, err := svc.Complete(t.Context(), CompleteInputDTO{Key: testEndpoint, Text: text, Offset: cursors[0]}); err != nil || c.ParentType != "Type0" || len(c.Items) == 0 {
		t.Fatalf("first completion = %+v, %v", c.Context, err)
	}
	c, _ := svc.Complete(t.Context(), CompleteInputDTO{Key: testEndpoint, Text: text, Offset: cursors[len(cursors)-1]})
	if c.Count < 3000 || len(c.Items) != MaxCompletionItems { // type0…type2999, __type, __typename
		t.Errorf("root fields = %d of %d, want %d of at least 3000", len(c.Items), c.Count, MaxCompletionItems)
	}

	const rounds = 5
	var took []time.Duration
	for range rounds {
		for _, off := range cursors {
			start := time.Now()
			if _, err := svc.Complete(t.Context(), CompleteInputDTO{Key: testEndpoint, Text: text, Offset: off}); err != nil {
				t.Fatal(err)
			}
			took = append(took, time.Since(start))
		}
	}
	slices.Sort(took)
	median, p90 := took[len(took)/2], took[len(took)*9/10]
	// The budget is per request. The median and the 90th percentile are
	// checked rather than the worst call, which a loaded CI machine or a
	// GC pause can push past any bound.
	if limit := testenv.Budget(50 * time.Millisecond); median > limit || p90 > limit {
		t.Errorf("completion took %v (median), %v (90th percentile), want under %v", median, p90, limit)
	}
	t.Logf("%d completions: median %v, 90th percentile %v, slowest %v (document %d bytes)",
		len(took), median, p90, took[len(took)-1], len(text))
}

func BenchmarkCompleteLargeSchema(b *testing.B) {
	svc := largeService(b)
	text, cursors := largeDocument()
	ctx := b.Context()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if _, err := svc.Complete(ctx, CompleteInputDTO{Key: testEndpoint, Text: text, Offset: cursors[i%len(cursors)]}); err != nil {
			b.Fatal(err)
		}
	}
}
