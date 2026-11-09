package format

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

func TestValidateJSON(t *testing.T) {
	t.Parallel()

	svc := New(nil)
	tests := []struct {
		name string
		text string
		want JSONCheckDTO
	}{
		{"valid with variable", `{"id": {{userId}}}`, JSONCheckDTO{Valid: true}},
		{
			"invalid after non-ASCII",
			"{\"名前\": \"x\",\n  \"b\": }",
			JSONCheckDTO{Error: &JSONErrorDTO{Offset: 19, ByteOffset: 23, Line: 2, Column: 8, Message: "Invalid character '}' looking for beginning of value"}},
		},
		{
			"blank",
			"  ",
			JSONCheckDTO{Error: &JSONErrorDTO{Offset: 2, ByteOffset: 2, Line: 1, Column: 3, Message: "Unexpected end of JSON input"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := svc.ValidateJSON(t.Context(), tt.text)
			if err != nil {
				t.Fatalf("ValidateJSON: %v", err)
			}
			if !sameCheck(got, tt.want) {
				t.Errorf("ValidateJSON = %+v %+v, want %+v %+v", got, got.Error, tt.want, tt.want.Error)
			}
		})
	}
}

func sameCheck(a, b JSONCheckDTO) bool {
	if a.Valid != b.Valid || (a.Error == nil) != (b.Error == nil) {
		return false
	}
	return a.Error == nil || *a.Error == *b.Error
}

func TestBeautifyAndMinifyJSON(t *testing.T) {
	t.Parallel()

	svc := New(nil)
	pretty, err := svc.BeautifyJSON(t.Context(), `{"n":1.50e+3,"id":{{ id }}}`, 2)
	if err != nil {
		t.Fatalf("BeautifyJSON: %v", err)
	}
	want := JSONFormatDTO{Valid: true, Text: "{\n  \"n\": 1.50e+3,\n  \"id\": {{ id }}\n}"}
	if pretty != want {
		t.Errorf("BeautifyJSON = %+v, want %+v", pretty, want)
	}

	small, err := svc.MinifyJSON(t.Context(), pretty.Text)
	if err != nil {
		t.Fatalf("MinifyJSON: %v", err)
	}
	if want := (JSONFormatDTO{Valid: true, Text: `{"n":1.50e+3,"id":{{ id }}}`}); small != want {
		t.Errorf("MinifyJSON = %+v, want %+v", small, want)
	}

	// Invalid JSON is returned unchanged with the error.
	const bad = "[1,\n2,]"
	for name, call := range map[string]func() (JSONFormatDTO, error){
		"BeautifyJSON": func() (JSONFormatDTO, error) { return svc.BeautifyJSON(t.Context(), bad, 4) },
		"MinifyJSON":   func() (JSONFormatDTO, error) { return svc.MinifyJSON(t.Context(), bad) },
	} {
		got, err := call()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wantErr := JSONErrorDTO{Offset: 6, ByteOffset: 6, Line: 2, Column: 3, Message: "Invalid character ']' looking for beginning of value"}
		if got.Valid || got.Text != bad || got.Error == nil || *got.Error != wantErr {
			t.Errorf("%s(invalid) = %+v %+v, want unchanged text and %+v", name, got, got.Error, wantErr)
		}
	}
}

func TestPrettifyAndMinifyGraphQL(t *testing.T) {
	t.Parallel()

	svc := New(nil)
	pretty, err := svc.PrettifyGraphQL(t.Context(), "query Q($id: ID!) { user(id: $id) { name # shown\n } }")
	if err != nil {
		t.Fatalf("PrettifyGraphQL: %v", err)
	}
	want := GraphQLFormatDTO{Valid: true, Text: "query Q($id: ID!) {\n  user(id: $id) {\n    name # shown\n  }\n}"}
	if pretty != want {
		t.Errorf("PrettifyGraphQL = %+v, want %+v", pretty, want)
	}

	small, err := svc.MinifyGraphQL(t.Context(), pretty.Text)
	if err != nil {
		t.Fatalf("MinifyGraphQL: %v", err)
	}
	if want := (GraphQLFormatDTO{Valid: true, Text: `query Q($id:ID!){user(id:$id){name}}`}); small != want {
		t.Errorf("MinifyGraphQL = %+v, want %+v", small, want)
	}

	// A document with syntax errors is returned unchanged with the first
	// error; positions count UTF-16 code units ("😀" is two).
	const bad = "{\n  a(s: \"😀\", x: )\n  b(\n}"
	for name, call := range map[string]func() (GraphQLFormatDTO, error){
		"PrettifyGraphQL": func() (GraphQLFormatDTO, error) { return svc.PrettifyGraphQL(t.Context(), bad) },
		"MinifyGraphQL":   func() (GraphQLFormatDTO, error) { return svc.MinifyGraphQL(t.Context(), bad) },
	} {
		got, err := call()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wantErr := GraphQLErrorDTO{Offset: 18, EndOffset: 19, ByteOffset: 20, Line: 2, Column: 17, Message: `expected value, found ")"`}
		if got.Valid || got.Text != bad || got.Error == nil || *got.Error != wantErr || got.ErrorCount != 3 {
			t.Errorf("%s(invalid) = %+v %+v, want unchanged text, %+v and 3 errors", name, got, got.Error, wantErr)
		}
	}
}

func TestCheckGraphQL(t *testing.T) {
	t.Parallel()

	svc := New(nil)
	got, err := svc.CheckGraphQL(t.Context(), "query A { a }\nfragment F on T { x }\nmutation { b }")
	if err != nil {
		t.Fatalf("CheckGraphQL: %v", err)
	}
	want := []GraphQLOperationDTO{{Name: "A", Type: "query"}, {Name: "", Type: "mutation"}}
	if !got.Valid || got.Error != nil || got.ErrorCount != 0 || !slices.Equal(got.Operations, want) {
		t.Errorf("CheckGraphQL = %+v, want valid with %+v", got, want)
	}

	// A syntax error is reported; the operations the parser recovered are
	// still listed.
	got, err = svc.CheckGraphQL(t.Context(), "query A { a(x: ) }\nquery B { b }")
	if err != nil {
		t.Fatalf("CheckGraphQL: %v", err)
	}
	if got.Valid || got.Error == nil || got.Error.Line != 1 || got.ErrorCount == 0 || len(got.Operations) != 2 {
		t.Errorf("CheckGraphQL(invalid) = %+v %+v", got, got.Error)
	}

	got, err = svc.CheckGraphQL(t.Context(), "")
	if err != nil || !got.Valid || got.Operations == nil || len(got.Operations) != 0 {
		t.Errorf("CheckGraphQL(blank) = %+v, %v", got, err)
	}
}

func TestCheckGraphQLVariables(t *testing.T) {
	t.Parallel()

	svc := New(nil)
	query := "query A($id: ID!, $n: Int) { a }\nquery B { b }"
	got, err := svc.CheckGraphQLVariables(t.Context(), query, "{\n  \"é\": 1,\n  \"extra\": true\n}", "A")
	if err != nil {
		t.Fatalf("CheckGraphQLVariables: %v", err)
	}
	wantMissing := []GraphQLVariableDTO{{Name: "id", Type: "ID!", Required: true}, {Name: "n", Type: "Int"}}
	wantUnknown := []GraphQLUnknownVariableDTO{
		{Name: "é", Offset: 4, EndOffset: 7, Line: 2, Column: 3},
		{Name: "extra", Offset: 14, EndOffset: 21, Line: 3, Column: 3},
	}
	if !got.Resolved || got.Operation != "A" || got.Error != nil || len(got.Declared) != 2 ||
		!slices.Equal(got.Missing, wantMissing) || !slices.Equal(got.Unknown, wantUnknown) {
		t.Errorf("CheckGraphQLVariables = %+v", got)
	}

	// Invalid JSON: the error, and nothing compared.
	got, err = svc.CheckGraphQLVariables(t.Context(), query, `{"id": }`, "A")
	if err != nil || got.Error == nil || got.Error.Offset != 7 || len(got.Missing) != 0 || got.Unknown == nil {
		t.Errorf("CheckGraphQLVariables(invalid) = %+v, %v", got, err)
	}

	// No operation picked among several: nothing to compare.
	got, err = svc.CheckGraphQLVariables(t.Context(), query, `{"x": 1}`, "")
	if err != nil || got.Resolved || len(got.Unknown) != 0 || got.Declared == nil {
		t.Errorf("CheckGraphQLVariables(no pick) = %+v, %v", got, err)
	}
}

func TestIndentXML(t *testing.T) {
	t.Parallel()
	svc := New(nil)
	got, err := svc.IndentXML(t.Context(), `<?xml version="1.0" encoding="ISO-8859-1"?><a><b>caf`+"\u00e9"+`</b></a>`)
	want := XMLFormatDTO{Valid: true, Text: "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?>\n<a>\n  <b>caf\u00e9</b>\n</a>"}
	if err != nil || got != want {
		t.Errorf("IndentXML = %+v, %v, want %+v", got, err, want)
	}

	bad := "<a>\n<b></a>"
	got, err = svc.IndentXML(t.Context(), bad)
	if err != nil || got.Valid || got.Text != bad || got.Error == nil || got.Error.Line != 2 || got.Error.Message == "" {
		t.Errorf("IndentXML(bad) = %+v (error %+v), %v", got, got.Error, err)
	}
}

func TestBeautifyJSONIndentOutOfRange(t *testing.T) {
	t.Parallel()

	for _, indent := range []int{-1, 9} {
		_, err := New(nil).BeautifyJSON(t.Context(), "{}", indent)
		if !errors.Is(err, &errs.Error{Kind: errs.InvalidArgument, Code: CodeIndentOutOfRange}) {
			t.Errorf("BeautifyJSON(indent %d) error = %v, want %s", indent, err, CodeIndentOutOfRange)
		}
	}
}

func TestCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	svc := New(nil)
	calls := map[string]func() error{
		"ValidateJSON":    func() error { _, err := svc.ValidateJSON(ctx, "{}"); return err },
		"BeautifyJSON":    func() error { _, err := svc.BeautifyJSON(ctx, "{}", 2); return err },
		"MinifyJSON":      func() error { _, err := svc.MinifyJSON(ctx, "{}"); return err },
		"PrettifyGraphQL": func() error { _, err := svc.PrettifyGraphQL(ctx, "{a}"); return err },
		"MinifyGraphQL":   func() error { _, err := svc.MinifyGraphQL(ctx, "{a}"); return err },
		"IndentXML":       func() error { _, err := svc.IndentXML(ctx, "<a/>"); return err },
		"CheckGraphQLVariables": func() error {
			_, err := svc.CheckGraphQLVariables(ctx, "{a}", "{}", "")
			return err
		},
	}
	for name, call := range calls {
		err := call()
		if !errors.Is(err, context.Canceled) || errs.KindOf(err) != errs.Cancelled {
			t.Errorf("%s with a cancelled context: err = %v, want kind cancelled wrapping context.Canceled", name, err)
		}
	}
}

// TestDTOJSON pins the JSON shape of the DTOs: camelCase names, no omitted
// fields, null for a missing error.
func TestDTOJSON(t *testing.T) {
	t.Parallel()

	jerr := &JSONErrorDTO{Offset: 5, ByteOffset: 6, Line: 1, Column: 6, Message: "Invalid character '}' looking for beginning of value"}
	tests := []struct {
		name string
		dto  any
	}{
		{"check_valid", JSONCheckDTO{Valid: true}},
		{"check_invalid", JSONCheckDTO{Error: jerr}},
		{"format_valid", JSONFormatDTO{Valid: true, Text: "{}"}},
		{"format_invalid", JSONFormatDTO{Text: `{"é":}`, Error: jerr}},
		{"graphql_valid", GraphQLFormatDTO{Valid: true, Text: "{\n  a\n}"}},
		{"graphql_invalid", GraphQLFormatDTO{Text: "{ a( }", Error: &GraphQLErrorDTO{
			Offset: 3, EndOffset: 4, ByteOffset: 3, Line: 1, Column: 4, Message: `"(" has no matching ")"`,
		}, ErrorCount: 2}},
		{"xml_valid", XMLFormatDTO{Valid: true, Text: "<a>\n  <b/>\n</a>"}},
		{"xml_invalid", XMLFormatDTO{Text: "<a><b></a>", Error: &XMLErrorDTO{Line: 1, Column: 7, Message: "element <b> closed by </a>"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.MarshalIndent(tt.dto, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, "dto/"+tt.name, append(got, '\n'))
		})
	}
}
