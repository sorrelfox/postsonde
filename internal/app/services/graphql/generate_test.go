package graphql

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/graphql/validate"
)

// problems returns the diagnostics of text against the cached schema key,
// but for unused fragments (a fragment is written on its own).
func problems(t *testing.T, svc *GraphQLService, key, text string) []DiagnosticDTO {
	t.Helper()
	d, err := svc.Diagnostics(t.Context(), DiagnosticsInputDTO{Key: key, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	var out []DiagnosticDTO
	for _, p := range d.Diagnostics {
		if p.Code != validate.CodeUnusedFragment {
			out = append(out, p)
		}
	}
	return out
}

func TestDocsTarget(t *testing.T) {
	t.Parallel()
	svc := cachedService(t)
	text := "# café\n{ viewer { login } }"
	off := strings.Index(text, "login")
	off16 := len([]rune(text[:off]))
	got, err := svc.DocsTarget(t.Context(), DocsTargetInputDTO{Key: testEndpoint, Text: text, Offset: off16 + 2})
	if err != nil {
		t.Fatal(err)
	}
	want := DocsTargetDTO{Found: true, Kind: "field", Type: "User", Member: "login", Name: "login", From: off16, To: off16 + 5}
	if got != want {
		t.Errorf("DocsTarget = %+v, want %+v", got, want)
	}
	if got, _ := svc.DocsTarget(t.Context(), DocsTargetInputDTO{Key: testEndpoint, Text: text, Offset: 1}); got.Found {
		t.Errorf("comment = %+v", got)
	}
	if got, _ := svc.DocsTarget(t.Context(), DocsTargetInputDTO{Key: "", Text: text, Offset: off16}); got.Found {
		t.Errorf("no schema = %+v", got)
	}
}

// TestGenerateOperation writes an operation for every root field and a
// fragment for every composite type of the GitHub subset schema at
// several depths, puts each into a query, and checks the result with the
// editor's own diagnostics.
func TestGenerateOperation(t *testing.T) {
	t.Parallel()
	svc := cachedService(t)
	overview, err := svc.DocsOverview(t.Context(), DocsInputDTO{Key: testEndpoint})
	if err != nil {
		t.Fatal(err)
	}
	for _, depth := range []int{1, 2, 3} {
		for _, root := range overview.Roots {
			ty, _ := svc.DocsType(t.Context(), DocsTypeInputDTO{Key: testEndpoint, Name: root.Type})
			for _, f := range ty.Fields {
				got, err := svc.GenerateOperation(t.Context(), GenerateInputDTO{Key: testEndpoint, Kind: "operation", Type: root.Type, Field: f.Name, Depth: depth})
				if err != nil || got.Reason != "" || !got.Schema {
					t.Fatalf("%s.%s: %+v, %v", root.Type, f.Name, got, err)
				}
				if p := problems(t, svc, testEndpoint, got.Insert); len(p) > 0 {
					t.Fatalf("%s.%s depth %d: %+v in\n%s", root.Type, f.Name, depth, p, got.Insert)
				}
			}
		}
		for _, ty := range overview.Types {
			if ty.Kind != "OBJECT" && ty.Kind != "INTERFACE" && ty.Kind != "UNION" {
				continue
			}
			got, err := svc.GenerateOperation(t.Context(), GenerateInputDTO{Key: testEndpoint, Kind: "fragment", Type: ty.Name, Depth: depth})
			if err != nil || got.Reason != "" {
				t.Fatalf("fragment on %s: %+v, %v", ty.Name, got, err)
			}
			if p := problems(t, svc, testEndpoint, got.Insert); len(p) > 0 {
				t.Fatalf("fragment on %s depth %d: %+v in\n%s", ty.Name, depth, p, got.Insert)
			}
		}
	}

	// Into a query that has one already: a new name after a blank line.
	text := "query Viewer { viewer { login } }"
	got, err := svc.GenerateOperation(t.Context(), GenerateInputDTO{Key: testEndpoint, Kind: "operation", Type: "Query", Field: "viewer", Text: text})
	if err != nil || got.Name != "Viewer2" || got.From != len(text) || got.To != len(text) || got.Cursor != len(text)+2 || !strings.HasPrefix(got.Insert, "\n\nquery Viewer2 {") {
		t.Fatalf("second operation = %+v, %v", got, err)
	}
	if p := problems(t, svc, testEndpoint, text+got.Insert); len(p) > 0 {
		t.Errorf("combined: %+v", p)
	}
	// A blank query is replaced.
	got, _ = svc.GenerateOperation(t.Context(), GenerateInputDTO{Key: testEndpoint, Kind: "operation", Type: "Query", Field: "viewer", Text: "  \n"})
	if got.From != 0 || got.To != 3 || got.Cursor != 0 || !strings.HasPrefix(got.Insert, "query Viewer {") {
		t.Errorf("blank query = %+v", got)
	}
	// Nothing to write.
	if got, _ := svc.GenerateOperation(t.Context(), GenerateInputDTO{Key: testEndpoint, Kind: "operation", Type: "User", Field: "login"}); got.Reason != "unknown-type" || got.Message == "" {
		t.Errorf("not a root = %+v", got)
	}
	if got, _ := svc.GenerateOperation(t.Context(), GenerateInputDTO{Key: testEndpoint, Kind: "fragment", Type: "Query", Field: "nope"}); got.Reason != "unknown-field" || got.Message != "Query has no field nope." {
		t.Errorf("unknown field = %+v", got)
	}
	if got, _ := svc.GenerateOperation(t.Context(), GenerateInputDTO{Key: "", Kind: "operation", Type: "Query", Field: "viewer"}); got.Schema || got.Insert != "" {
		t.Errorf("no schema = %+v", got)
	}
}

func TestFillFields(t *testing.T) {
	t.Parallel()
	svc := cachedService(t)
	text := "# café\nquery {\n  viewer {\n    login\n    \n  }\n}"
	off := strings.Index(text, "login\n    ") + len("login\n    ")
	off16 := len([]rune(text[:off]))
	got, err := svc.FillFields(t.Context(), FillFieldsInputDTO{Key: testEndpoint, Text: text, Offset: off16, Depth: 1})
	if err != nil || got.Reason != "" || got.Type != "User" || got.Count == 0 || strings.Contains(got.Text, "login") {
		t.Fatalf("FillFields = %+v, %v", got, err)
	}
	runes := []rune(text)
	filled := string(runes[:got.From]) + got.Text + string(runes[got.To:])
	if got.From != off16-4 || got.Cursor != got.From+len([]rune(got.Text)) || !strings.HasPrefix(got.Text, "    ") {
		t.Errorf("range = %+v", got)
	}
	if p := problems(t, svc, testEndpoint, filled); len(p) > 0 {
		t.Errorf("filled: %+v in\n%s", p, filled)
	}
	// Again: nothing left at depth 1.
	again, _ := svc.FillFields(t.Context(), FillFieldsInputDTO{Key: testEndpoint, Text: filled, Offset: len([]rune(string(runes[:got.From]) + got.Text)), Depth: 1})
	if again.Reason != "nothing-to-add" || again.Message == "" {
		t.Errorf("again = %+v", again)
	}
	if got, _ := svc.FillFields(t.Context(), FillFieldsInputDTO{Key: testEndpoint, Text: text, Offset: 2}); got.Reason != "not-in-selection" || got.Message == "" {
		t.Errorf("in a comment = %+v", got)
	}
	if got, _ := svc.FillFields(t.Context(), FillFieldsInputDTO{Key: "", Text: text, Offset: off16}); got.Schema || got.Message == "" {
		t.Errorf("no schema = %+v", got)
	}
}

// TestSchemaActionsLargeSchema runs the three actions on the large
// generated schema (3000 object types of 60 fields, whose object fields
// form cycles): every result validates, and each call stays fast.
func TestSchemaActionsLargeSchema(t *testing.T) {
	t.Parallel()
	svc := largeService(t)
	text, cursors := largeDocument()
	// The first call reads the schema from the disk cache; time the others.
	if _, err := svc.DocsOverview(t.Context(), DocsInputDTO{Key: testEndpoint}); err != nil {
		t.Fatal(err)
	}
	var slowest time.Duration
	timed := func(f func()) {
		start := time.Now()
		f()
		slowest = max(slowest, time.Since(start))
	}
	for i := 0; i < 3000; i += 97 {
		for _, depth := range []int{1, 2, 3} {
			var got GeneratedDTO
			timed(func() {
				got, _ = svc.GenerateOperation(t.Context(), GenerateInputDTO{Key: testEndpoint, Kind: "operation", Type: "Query", Field: "type" + strconv.Itoa(i), Depth: depth, Text: text})
			})
			if got.Reason != "" || got.Name == "" {
				t.Fatalf("type%d: %+v", i, got)
			}
			if p := problems(t, svc, testEndpoint, got.Insert); len(p) > 0 {
				t.Fatalf("type%d depth %d: %+v in\n%s", i, depth, p, got.Insert)
			}
		}
	}
	// Fill in the empty selection set of each operation's field2.
	for _, c := range cursors[:10] {
		at := strings.Index(text[c:], "field2 { ") + c + len("field2 { ")
		var got FillFieldsDTO
		timed(func() {
			got, _ = svc.FillFields(t.Context(), FillFieldsInputDTO{Key: testEndpoint, Text: text, Offset: at, Depth: 2})
		})
		if got.Reason != "" {
			t.Fatalf("fill at %d: %+v", at, got)
		}
		filled := text[:got.From] + got.Text + text[got.To:]
		// The document had half-typed fields ("fi"): check only the filled operation.
		if p := problems(t, svc, testEndpoint, filled); len(p) > 0 {
			for _, d := range p {
				if d.Offset >= got.From && d.Offset <= got.From+len(got.Text) {
					t.Fatalf("fill at %d: %+v", at, d)
				}
			}
		}
	}
	var target DocsTargetDTO
	timed(func() {
		target, _ = svc.DocsTarget(t.Context(), DocsTargetInputDTO{Key: testEndpoint, Text: text, Offset: strings.Index(text, "field3")})
	})
	if !target.Found || target.Member != "field3" {
		t.Errorf("target = %+v", target)
	}
	t.Logf("slowest call: %v", slowest)
	if !testing.Short() && slowest > 250*time.Millisecond {
		t.Errorf("slowest call took %v", slowest)
	}
}
