// Package format is the bound service for the editors' text tools: JSON
// validate, beautify and minify for request bodies (HTTP-12),
// GraphQL prettify and minify for queries (GQL-18), the GraphQL
// check behind the query editor's marker and operation picker (GQL-01),
// XML indent for response bodies (HTTP-31) and the SOAP envelope helper
// (HTTP-18). The logic is internal/core/jsonfmt,
// internal/core/graphql/printer, internal/core/xmldom and
// internal/core/soap. See docs/json-tools.md, docs/graphql-engine.md and
// docs/http-client.md#soap-helper.
package format

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
	"github.com/sorrelfox/postsonde/internal/core/graphql/printer"
	"github.com/sorrelfox/postsonde/internal/core/jsonfmt"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/soap"
	"github.com/sorrelfox/postsonde/internal/core/xmldom"
)

// Domain names the format service.
const Domain = "format"

// CodeIndentOutOfRange is the errs code of the error BeautifyJSON returns
// for an indent outside 0..jsonfmt.MaxIndent.
const CodeIndentOutOfRange = "format.indent_out_of_range"

// CodeSOAPVersion is the errs code of the error SOAPEnvelope returns for
// a version other than "1.1" and "1.2".
const CodeSOAPVersion = "format.soap_version"

// FormatService checks and reformats editor text. It is stateless.
//
// Invalid JSON is a result, not an error: the methods return it with its
// position so the editor can mark it. Errors are for bad arguments and
// cancelled calls.
type FormatService struct {
	logger *slog.Logger
}

// New returns a FormatService. A nil logger means slog.Default().
func New(logger *slog.Logger) *FormatService {
	if logger == nil {
		logger = slog.Default()
	}
	return &FormatService{logger: logger.With("service", Domain)}
}

// ServiceName names the service in Wails logs.
func (s *FormatService) ServiceName() string { return Domain }

// ValidateJSON checks that text is one JSON value. {{variables}} count as
// values (see jsonfmt). Blank text is invalid.
func (s *FormatService) ValidateJSON(ctx context.Context, text string) (JSONCheckDTO, error) {
	if err := interrupted(ctx, "Checking the JSON"); err != nil {
		return JSONCheckDTO{}, err
	}
	return toJSONCheckDTO(jsonfmt.Validate(text)), nil
}

// BeautifyJSON indents text with indent spaces per level, or a tab when
// indent is 0. Invalid JSON comes back unchanged as Text, with Valid false
// and the error.
func (s *FormatService) BeautifyJSON(ctx context.Context, text string, indent int) (JSONFormatDTO, error) {
	if indent < 0 || indent > jsonfmt.MaxIndent {
		return JSONFormatDTO{}, errs.New(errs.InvalidArgument,
			fmt.Sprintf("The indent must be between 0 and %d spaces, not %d.", jsonfmt.MaxIndent, indent)).
			WithCode(CodeIndentOutOfRange).
			WithHint(fmt.Sprintf("Use 0 for a tab or 1 to %d spaces.", jsonfmt.MaxIndent)).
			WithDetail("field", "indent").
			WithDetail("min", "0").
			WithDetail("max", strconv.Itoa(jsonfmt.MaxIndent))
	}
	if err := interrupted(ctx, "Beautifying the JSON"); err != nil {
		return JSONFormatDTO{}, err
	}
	out, serr := jsonfmt.Beautify(text, indent)
	return toJSONFormatDTO(text, out, serr), nil
}

// MinifyJSON removes the whitespace outside strings from text. Invalid JSON
// comes back unchanged as Text, with Valid false and the error.
func (s *FormatService) MinifyJSON(ctx context.Context, text string) (JSONFormatDTO, error) {
	if err := interrupted(ctx, "Minifying the JSON"); err != nil {
		return JSONFormatDTO{}, err
	}
	out, serr := jsonfmt.Minify(text)
	return toJSONFormatDTO(text, out, serr), nil
}

// PrettifyGraphQL lays out a GraphQL executable document, keeping its
// comments (see graphql/printer). A document with syntax errors comes back
// unchanged as Text, with Valid false and the first error.
func (s *FormatService) PrettifyGraphQL(ctx context.Context, text string) (GraphQLFormatDTO, error) {
	if err := interrupted(ctx, "Prettifying the GraphQL document"); err != nil {
		return GraphQLFormatDTO{}, err
	}
	out, perrs := printer.Prettify(text)
	return toGraphQLFormatDTO(text, out, perrs), nil
}

// MinifyGraphQL prints a GraphQL executable document on one line without
// comments. A document with syntax errors comes back unchanged as Text,
// with Valid false and the first error.
func (s *FormatService) MinifyGraphQL(ctx context.Context, text string) (GraphQLFormatDTO, error) {
	if err := interrupted(ctx, "Minifying the GraphQL document"); err != nil {
		return GraphQLFormatDTO{}, err
	}
	out, perrs := printer.Minify(text)
	return toGraphQLFormatDTO(text, out, perrs), nil
}

// CheckGraphQL parses a GraphQL document for the query editor (GQL-01):
// its operations in source order, for the operation picker, and its first
// syntax error, for the editor's marker. Operations are listed also when
// the document has syntax errors, as far as the parser recovered them.
// Blank text is valid with no operations.
func (s *FormatService) CheckGraphQL(ctx context.Context, text string) (GraphQLCheckDTO, error) {
	if err := interrupted(ctx, "Checking the GraphQL document"); err != nil {
		return GraphQLCheckDTO{}, err
	}
	doc, perrs := parser.Parse(text)
	return toGraphQLCheckDTO(text, doc, perrs), nil
}

// CheckGraphQLVariables checks the variables text of a GraphQL request
// for the Variables tab (GQL-03, GQL-04): that it is JSON (with
// {{variables}}) and an object, and which variables the operation that
// runs declares but the object lacks, or the object sets but the
// operation does not declare. The operation is the only one of query, or
// the one named operationName (request.CheckGraphQLVariables). Blank text
// and null count as an empty object.
func (s *FormatService) CheckGraphQLVariables(ctx context.Context, query, variables, operationName string) (GraphQLVariablesCheckDTO, error) {
	if err := interrupted(ctx, "Checking the GraphQL variables"); err != nil {
		return GraphQLVariablesCheckDTO{}, err
	}
	return toGraphQLVariablesCheckDTO(variables, request.CheckGraphQLVariables(query, variables, operationName)), nil
}

// IndentXML indents an XML document that is UTF-8 text already (a
// response body decoded with its charset; its encoding declaration is not
// used), two spaces per level, keeping text content and markup as written
// (xmldom.Indent). The response viewer calls it for bodies too large to be
// pretty-printed on arrival (HTTP-31). A document that is not well-formed
// comes back unchanged as Text, with Valid false and the error.
func (s *FormatService) IndentXML(ctx context.Context, text string) (XMLFormatDTO, error) {
	if err := interrupted(ctx, "Indenting the XML"); err != nil {
		return XMLFormatDTO{}, err
	}
	out, err := xmldom.Indent(ctx, []byte(text), "  ")
	var se *xmldom.SyntaxError
	var le *xmldom.LimitError
	switch {
	case err == nil:
		return XMLFormatDTO{Valid: true, Text: out}, nil
	case errors.As(err, &se):
		return XMLFormatDTO{Text: text, Error: &XMLErrorDTO{Line: se.Line, Column: se.Column, Message: se.Message}}, nil
	case errors.As(err, &le):
		return XMLFormatDTO{Text: text, Error: &XMLErrorDTO{Message: le.Error()}}, nil
	}
	return XMLFormatDTO{}, interrupted(ctx, "Indenting the XML")
}

// SOAPEnvelope is the body editor's SOAP helper (HTTP-18): the raw XML
// body and the headers of a SOAP call of in.Version ("1.1" or "1.2") with
// in.Action. in.Text is the current body text: blank gives a new envelope,
// an envelope of either version is kept with its namespace switched to
// in.Version's, anything else is wrapped in a new envelope's Body
// (soap.Envelope). Nothing is saved: the editor applies the result.
func (s *FormatService) SOAPEnvelope(ctx context.Context, in SOAPInputDTO) (SOAPEnvelopeDTO, error) {
	if err := interrupted(ctx, "Making the SOAP envelope"); err != nil {
		return SOAPEnvelopeDTO{}, err
	}
	text, err := soap.Envelope(in.Version, in.Text)
	if err != nil {
		return SOAPEnvelopeDTO{}, errs.New(errs.InvalidArgument,
			fmt.Sprintf("The SOAP version must be 1.1 or 1.2, not %q.", in.Version)).
			WithCode(CodeSOAPVersion).
			WithHint("Pick SOAP 1.1 or SOAP 1.2.").
			WithDetail("field", "version")
	}
	headers, _ := soap.Headers(in.Version, in.Action)
	out := SOAPEnvelopeDTO{Text: text, Headers: make([]SOAPHeaderDTO, len(headers))}
	for i, h := range headers {
		out.Headers[i] = SOAPHeaderDTO(h)
	}
	if in.Version == soap.V12 {
		out.Remove = []string{soap.ActionHeader}
	} else {
		out.Remove = []string{}
	}
	return out, nil
}

// interrupted returns a cancelled or timeout error when ctx is done.
func interrupted(ctx context.Context, what string) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), what+" was interrupted.")
	}
	return nil
}
