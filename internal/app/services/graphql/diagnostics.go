package graphql

import (
	"context"
	"sort"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/lexer"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
	"github.com/sorrelfox/postsonde/internal/core/graphql/validate"
)

// MaxDiagnostics bounds the diagnostics Diagnostics returns; the count
// says how many there were.
const MaxDiagnostics = 200

// Sources of diagnostics.
const (
	SourceSyntax = "syntax"
	SourceSchema = "schema"
)

// Diagnostics parses a GraphQL document and, when in.Key names a cached
// schema (SchemaInfo's Key), validates it against that schema (GQL-12):
// the live markers and the problems bar of the query editor. Syntax
// errors come from the parser, schema errors from package validate, on
// the parser's partial tree, so both are reported while typing. The
// document's operations come along for the operation picker. It never
// sends anything; an unknown key validates syntax only.
func (s *GraphQLService) Diagnostics(ctx context.Context, in DiagnosticsInputDTO) (DiagnosticsDTO, error) {
	if err := interrupted(ctx, "Checking the GraphQL document"); err != nil {
		return DiagnosticsDTO{}, err
	}
	var sch *schema.Schema
	if in.Key != "" {
		if e := s.cached(in.Key); e != nil {
			sch = e.Schema
		}
	}
	return diagnose(sch, in.Text), nil
}

func diagnose(sch *schema.Schema, text string) DiagnosticsDTO {
	doc, perrs := parser.Parse(text)
	out := DiagnosticsDTO{
		Schema:      sch != nil,
		Operations:  operationsOf(doc),
		Diagnostics: make([]DiagnosticDTO, 0),
		SyntaxCount: len(perrs),
	}
	type diag struct {
		start, end    int
		code, message string
		source        string
		severity      string
	}
	all := make([]diag, 0, len(perrs))
	for _, e := range perrs {
		all = append(all, diag{e.Start, e.End, "graphql.syntax." + strings.ReplaceAll(e.Code.String(), " ", "_"), e.Message, SourceSyntax, string(validate.SeverityError)})
	}
	if sch != nil {
		for _, d := range validate.Validate(sch, doc) {
			all = append(all, diag{d.Start, d.End, d.Code, d.Message, SourceSchema, string(d.Severity)})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].start < all[j].start })
	out.Count = len(all)
	if len(all) > MaxDiagnostics {
		all = all[:MaxDiagnostics]
	}
	if len(all) == 0 {
		return out
	}
	idx := lexer.NewUTF16Index(text)
	lines := newLineIndex(text)
	for _, d := range all {
		start := min(max(d.start, 0), len(text))
		end := min(max(d.end, start), len(text))
		line, lineStart := lines.at(start)
		out.Diagnostics = append(out.Diagnostics, DiagnosticDTO{
			Source:    d.source,
			Code:      d.code,
			Severity:  d.severity,
			Offset:    idx.ToUTF16(start),
			EndOffset: idx.ToUTF16(end),
			Line:      line,
			Column:    idx.ToUTF16(start) - idx.ToUTF16(lineStart) + 1,
			Message:   d.message,
		})
	}
	return out
}

func operationsOf(doc *ast.Document) []OperationDTO {
	out := make([]OperationDTO, 0)
	for _, d := range doc.Definitions {
		op, ok := d.(*ast.OperationDefinition)
		if !ok {
			continue
		}
		dto := OperationDTO{Type: "query"}
		if op.Name != nil {
			dto.Name = op.Name.Value
		}
		switch op.Operation {
		case ast.Mutation:
			dto.Type = "mutation"
		case ast.Subscription:
			dto.Type = "subscription"
		}
		out = append(out, dto)
	}
	return out
}

// lineIndex finds the line of a byte offset: "\n", "\r\n" and a lone "\r"
// end a line, as in CodeMirror.
type lineIndex []int // byte offsets where lines start

func newLineIndex(text string) lineIndex {
	starts := lineIndex{0}
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\n':
			starts = append(starts, i+1)
		case '\r':
			if i+1 < len(text) && text[i+1] == '\n' {
				continue
			}
			starts = append(starts, i+1)
		}
	}
	return starts
}

// at returns the 1-based line of off and the offset its line starts at.
func (l lineIndex) at(off int) (int, int) {
	n := sort.Search(len(l), func(i int) bool { return l[i] > off })
	return n, l[n-1]
}
