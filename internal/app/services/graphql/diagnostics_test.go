package graphql

import (
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/graphql/schemacache"
)

const testEndpoint = "https://api.test/graphql"

// cachedService is a service with the GitHub-subset schema cached for
// testEndpoint, and no network.
func cachedService(t *testing.T) *GraphQLService {
	t.Helper()
	sch, _, err := schemacache.Parse(readFixture(t, "github-subset.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := (schemacache.Store{Dir: dir}).Save(&schemacache.Entry{Endpoint: testEndpoint, FetchedAt: time.Now(), Query: schemacache.QueryFull, Schema: sch}); err != nil {
		t.Fatal(err)
	}
	return New(nil, nil, func() (string, error) { return dir, nil }, nil, nil)
}

func TestDiagnostics(t *testing.T) {
	t.Parallel()
	svc := cachedService(t)
	check := func(key, text string) DiagnosticsDTO {
		t.Helper()
		got, err := svc.Diagnostics(t.Context(), DiagnosticsInputDTO{Key: key, Text: text})
		if err != nil {
			t.Fatalf("Diagnostics = %v", err)
		}
		return got
	}

	// Without a schema only syntax counts.
	got := check("", "{ viewer { logn } }")
	if got.Schema || len(got.Diagnostics) != 0 || got.Count != 0 {
		t.Errorf("without a schema = %+v", got)
	}

	// With it, the unknown field is reported on its name, in UTF-16 units
	// after a non-ASCII comment line.
	text := "# café 😀\n{ viewer { logn } }"
	got = check(testEndpoint, text)
	if !got.Schema || len(got.Diagnostics) != 1 {
		t.Fatalf("with a schema = %+v", got)
	}
	d := got.Diagnostics[0]
	start16 := len([]rune("# café ")) + 2 + 1 + len("{ viewer { ")
	if d.Source != SourceSchema || d.Code != "graphql.validate.unknown_field" || d.Severity != "error" ||
		d.Offset != start16 || d.EndOffset != start16+4 || d.Line != 2 || d.Column != 12 ||
		!strings.Contains(d.Message, `Cannot query field "logn"`) {
		t.Errorf("diagnostic = %+v (want offset %d)", d, start16)
	}

	// Fixed: nothing left.
	if got := check(testEndpoint, "{ viewer { login } }"); len(got.Diagnostics) != 0 || got.Count != 0 {
		t.Errorf("valid document = %+v", got)
	}

	// Syntax errors and schema errors together, sorted, with operations.
	got = check(testEndpoint, "query A { viewer { logn } }\nquery B { a(x: ) }")
	if got.SyntaxCount != 1 || got.Count < 2 || len(got.Operations) != 2 || got.Operations[1] != (OperationDTO{Name: "B", Type: "query"}) {
		t.Fatalf("mixed = %+v", got)
	}
	if got.Diagnostics[0].Source != SourceSchema || got.Diagnostics[len(got.Diagnostics)-1].Source != SourceSyntax ||
		got.Diagnostics[len(got.Diagnostics)-1].Code != "graphql.syntax.unexpected_token" {
		t.Errorf("mixed diagnostics = %+v", got.Diagnostics)
	}

	// An unknown key checks syntax only.
	if got := check("https://nowhere.test/graphql", "{ viewer { logn } }"); got.Schema || len(got.Diagnostics) != 0 {
		t.Errorf("unknown key = %+v", got)
	}
	// Blank text: nothing, and empty lists rather than null.
	if got := check(testEndpoint, ""); got.Operations == nil || got.Diagnostics == nil || got.Count != 0 {
		t.Errorf("blank = %+v", got)
	}
}

func TestDiagnosticsCap(t *testing.T) {
	t.Parallel()
	text := "{" + strings.Repeat(" nope", MaxDiagnostics+50) + " }"
	got, err := cachedService(t).Diagnostics(t.Context(), DiagnosticsInputDTO{Key: testEndpoint, Text: text})
	if err != nil || len(got.Diagnostics) != MaxDiagnostics || got.Count != MaxDiagnostics+50 {
		t.Errorf("Diagnostics = %d of %d, %v", len(got.Diagnostics), got.Count, err)
	}
}
