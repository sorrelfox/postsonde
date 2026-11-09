package graphql

import (
	"context"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/generate"
	"github.com/sorrelfox/postsonde/internal/core/graphql/lexer"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// The editor's schema actions (package generate): the docs target of the
// name under a Ctrl/Cmd-click (GQL-15), an operation or fragment written
// from the docs explorer (GQL-16) and the fields filled in at the cursor
// (GQL-17). All work on the cached schema, offline, and answer "nothing
// to do" in the DTO (Reason and Message) instead of failing.

// DocsTarget returns what the name at in.Offset (UTF-16) of the document
// in.Text refers to in the cached schema in.Key, as the docs explorer
// opens it. Found is false for anything else, and without a schema.
func (s *GraphQLService) DocsTarget(ctx context.Context, in DocsTargetInputDTO) (DocsTargetDTO, error) {
	if err := interrupted(ctx, "Finding the docs"); err != nil {
		return DocsTargetDTO{}, err
	}
	sch := s.schemaFor(in.Key)
	if sch == nil {
		return DocsTargetDTO{}, nil
	}
	idx := lexer.NewUTF16Index(in.Text)
	t, ok := generate.TargetAt(sch, in.Text, idx.ToByte(max(in.Offset, 0)))
	if !ok {
		return DocsTargetDTO{}, nil
	}
	return DocsTargetDTO{
		Found:     true,
		Kind:      string(t.Kind),
		Type:      t.Type,
		Member:    t.Member,
		Directive: t.Directive,
		Name:      t.Name,
		From:      idx.ToUTF16(t.Start),
		To:        idx.ToUTF16(t.End),
	}, nil
}

// GenerateOperation writes, from the cached schema in.Key, an operation
// for the root field in.Type.in.Field (Kind "operation"), or a fragment on
// in.Type for all its fields or only in.Field (Kind "fragment"), in.Depth
// selection sets deep. The name is one the document in.Text does not use
// yet; Insert is the text to put at [From, To) of in.Text (its end, or all
// of it when it is blank), and Cursor where the new definition starts
// after the edit, all in UTF-16 code units.
func (s *GraphQLService) GenerateOperation(ctx context.Context, in GenerateInputDTO) (GeneratedDTO, error) {
	if err := interrupted(ctx, "Writing the operation"); err != nil {
		return GeneratedDTO{}, err
	}
	sch := s.schemaFor(in.Key)
	if sch == nil {
		return GeneratedDTO{Message: "Fetch the schema first."}, nil
	}
	out := GeneratedDTO{Schema: true}
	opts := generate.Options{Depth: in.Depth}
	taken := definitionNames(in.Text)
	var res generate.Result
	switch in.Kind {
	case "operation":
		op := rootOperation(sch, in.Type)
		if op == "" {
			out.Reason, out.Message = string(generate.ReasonUnknownType), in.Type+" is not a root type of the schema."
			return out, nil
		}
		res = generate.Operation(sch, op, in.Field, opts, taken)
	case "fragment":
		res = generate.Fragment(sch, in.Type, in.Field, opts, taken)
	default:
		out.Reason, out.Message = string(generate.ReasonUnknownType), "Choose an operation or a fragment."
		return out, nil
	}
	if res.Reason != "" {
		out.Reason, out.Message = string(res.Reason), generateMessage(res.Reason, in.Type, in.Field)
		return out, nil
	}
	out.Text, out.Name, out.Truncated = res.Text, res.Name, res.Truncated
	if out.Truncated {
		out.Message = "The selection was cut short: choose a smaller depth for all of it."
	}
	n := lexer.NewUTF16Index(in.Text).Len16()
	var prefix string
	if strings.TrimSpace(in.Text) == "" {
		out.From, out.To = 0, n
	} else {
		out.From, out.To = n, n
		switch {
		case strings.HasSuffix(in.Text, "\n\n"):
		case strings.HasSuffix(in.Text, "\n"):
			prefix = "\n"
		default:
			prefix = "\n\n"
		}
	}
	out.Insert = prefix + res.Text + "\n"
	out.Cursor = out.From + utf16Len(prefix)
	return out, nil
}

// FillFields writes the fields of the selection set at in.Offset (UTF-16)
// of in.Text that are not selected yet, from the cached schema in.Key,
// in.Depth selection sets deep (1: the leaf fields only). Text replaces
// [From, To); Cursor is the end of the inserted text, all in UTF-16 code
// units. Reason and Message say why nothing can be filled in.
func (s *GraphQLService) FillFields(ctx context.Context, in FillFieldsInputDTO) (FillFieldsDTO, error) {
	if err := interrupted(ctx, "Filling in the fields"); err != nil {
		return FillFieldsDTO{}, err
	}
	sch := s.schemaFor(in.Key)
	if sch == nil {
		return FillFieldsDTO{Message: "Fetch the schema to fill in fields."}, nil
	}
	idx := lexer.NewUTF16Index(in.Text)
	r := generate.Fill(sch, in.Text, idx.ToByte(max(in.Offset, 0)), generate.Options{Depth: in.Depth})
	out := FillFieldsDTO{
		Schema:    true,
		From:      idx.ToUTF16(r.From),
		To:        idx.ToUTF16(r.To),
		Text:      r.Text,
		Type:      r.Type,
		Count:     r.Count,
		Truncated: r.Truncated,
		Reason:    string(r.Reason),
	}
	out.Cursor = out.From + utf16Len(r.Text)
	switch r.Reason {
	case "":
		if r.Truncated {
			out.Message = "The fields were cut short: choose a smaller depth for all of them."
		}
	case generate.ReasonNotInSelection:
		out.Message = "Put the cursor between the braces of a selection set to fill in its fields."
	case generate.ReasonUnknownType:
		out.Message = "The type of this selection set is unknown: check the fields around it."
	case generate.ReasonNothingToAdd:
		out.Message = "Every field of " + r.Type + " that can be filled in is selected already."
	}
	return out, nil
}

// rootOperation returns the operation whose root type is name, or "".
func rootOperation(sch *schema.Schema, name string) schema.Operation {
	for _, op := range []schema.Operation{schema.OpQuery, schema.OpMutation, schema.OpSubscription} {
		if t := sch.RootType(op); t != nil && t.Name == name {
			return op
		}
	}
	return ""
}

func generateMessage(r generate.Reason, typeName, field string) string {
	switch r {
	case generate.ReasonUnknownField:
		return typeName + " has no field " + field + "."
	case generate.ReasonRequiredArguments:
		return typeName + "." + field + " takes required arguments, which a fragment cannot declare. Write a query from a root field, or add the arguments by hand."
	}
	return "The schema has no object, interface or union " + typeName + "."
}

// definitionNames returns a Taken over the operation and fragment names of
// text.
func definitionNames(text string) generate.Taken {
	names := map[string]bool{}
	doc, _ := parser.Parse(text)
	for _, d := range doc.Definitions {
		switch d := d.(type) {
		case *ast.OperationDefinition:
			if d.Name != nil {
				names[d.Name.Value] = true
			}
		case *ast.FragmentDefinition:
			if d.Name != nil {
				names[d.Name.Value] = true
			}
		}
	}
	return func(name string) bool { return names[name] }
}

func utf16Len(s string) int { return lexer.NewUTF16Index(s).Len16() }
