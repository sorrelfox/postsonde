package format

import (
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/lexer"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
	"github.com/sorrelfox/postsonde/internal/core/jsonfmt"
	"github.com/sorrelfox/postsonde/internal/core/request"
)

// DTOs are the only types FormatService methods take or return. See
// docs/bindings.md.

// SOAPInputDTO is the input of SOAPEnvelope (HTTP-18).
type SOAPInputDTO struct {
	// Version is "1.1" or "1.2".
	Version string `json:"version"`
	// Action is the SOAP action as typed ({{variables}} are rendered when
	// the request is sent); "" for none.
	Action string `json:"action"`
	// Text is the request's current raw body text.
	Text string `json:"text"`
}

// SOAPEnvelopeDTO is the result of SOAPEnvelope: what the editor puts in
// the request.
type SOAPEnvelopeDTO struct {
	// Text is the raw XML body.
	Text string `json:"text"`
	// Headers are set in the header table, replacing the rows of the same
	// name: Content-Type, and SOAPAction for 1.1.
	Headers []SOAPHeaderDTO `json:"headers"`
	// Remove names the headers to take out of the table: SOAPAction for
	// 1.2, which sends the action in Content-Type. Never null.
	Remove []string `json:"remove"`
}

// SOAPHeaderDTO is a header of a SOAP call.
type SOAPHeaderDTO struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// JSONErrorDTO is where and why text is not valid JSON.
type JSONErrorDTO struct {
	// Offset is the position of the offending character in UTF-16 code
	// units, as CodeMirror and JavaScript strings count; the text's length
	// when it ends too early.
	Offset int `json:"offset"`
	// ByteOffset is the same position in UTF-8 bytes.
	ByteOffset int `json:"byteOffset"`
	// Line and Column are 1-based; Column counts UTF-16 code units.
	Line   int `json:"line"`
	Column int `json:"column"`
	// Message says what is wrong ("Invalid character '}' looking for
	// beginning of value").
	Message string `json:"message"`
}

// JSONCheckDTO is the result of ValidateJSON.
type JSONCheckDTO struct {
	Valid bool `json:"valid"`
	// Error is null when Valid.
	Error *JSONErrorDTO `json:"error"`
}

// JSONFormatDTO is the result of BeautifyJSON and MinifyJSON.
type JSONFormatDTO struct {
	Valid bool `json:"valid"`
	// Text is the formatted text, or the input unchanged when not Valid.
	Text string `json:"text"`
	// Error is null when Valid.
	Error *JSONErrorDTO `json:"error"`
}

func toJSONErrorDTO(e *jsonfmt.SyntaxError) *JSONErrorDTO {
	if e == nil {
		return nil
	}
	return &JSONErrorDTO{
		Offset:     e.Offset16,
		ByteOffset: e.Offset,
		Line:       e.Line,
		Column:     e.Column,
		Message:    e.Message,
	}
}

func toJSONCheckDTO(e *jsonfmt.SyntaxError) JSONCheckDTO {
	return JSONCheckDTO{Valid: e == nil, Error: toJSONErrorDTO(e)}
}

func toJSONFormatDTO(in, out string, e *jsonfmt.SyntaxError) JSONFormatDTO {
	if e != nil {
		return JSONFormatDTO{Valid: false, Text: in, Error: toJSONErrorDTO(e)}
	}
	return JSONFormatDTO{Valid: true, Text: out}
}

// GraphQLErrorDTO is a syntax error that keeps a GraphQL document from
// being formatted.
type GraphQLErrorDTO struct {
	// Offset and EndOffset are the error's range in UTF-16 code units, as
	// CodeMirror and JavaScript strings count.
	Offset    int `json:"offset"`
	EndOffset int `json:"endOffset"`
	// ByteOffset is Offset in UTF-8 bytes.
	ByteOffset int `json:"byteOffset"`
	// Line and Column are 1-based; Column counts UTF-16 code units.
	Line   int `json:"line"`
	Column int `json:"column"`
	// Message says what is wrong (`expected value, found ")"`).
	Message string `json:"message"`
}

// GraphQLFormatDTO is the result of PrettifyGraphQL and MinifyGraphQL.
type GraphQLFormatDTO struct {
	Valid bool `json:"valid"`
	// Text is the formatted document, or the input unchanged when not Valid.
	Text string `json:"text"`
	// Error is the first syntax error, null when Valid.
	Error *GraphQLErrorDTO `json:"error"`
	// ErrorCount is the number of syntax errors, 0 when Valid.
	ErrorCount int `json:"errorCount"`
}

func toGraphQLFormatDTO(in, out string, errors []parser.Error) GraphQLFormatDTO {
	if len(errors) == 0 {
		return GraphQLFormatDTO{Valid: true, Text: out}
	}
	return GraphQLFormatDTO{
		Text:       in,
		Error:      toGraphQLErrorDTO(in, errors[0]),
		ErrorCount: len(errors),
	}
}

func toGraphQLErrorDTO(in string, e parser.Error) *GraphQLErrorDTO {
	idx := lexer.NewUTF16Index(in)
	lineStart := strings.LastIndexAny(in[:e.Start], "\r\n") + 1
	return &GraphQLErrorDTO{
		Offset:     idx.ToUTF16(e.Start),
		EndOffset:  idx.ToUTF16(e.End),
		ByteOffset: e.Start,
		Line:       ast.NewDocument(in).Position(e.Start).Line,
		Column:     idx.ToUTF16(e.Start) - idx.ToUTF16(lineStart) + 1,
		Message:    e.Message,
	}
}

// GraphQLOperationDTO is an operation of a GraphQL document.
type GraphQLOperationDTO struct {
	// Name is "" for an anonymous operation.
	Name string `json:"name"`
	// Type is "query", "mutation" or "subscription".
	Type string `json:"type"`
}

// GraphQLCheckDTO is the result of CheckGraphQL.
type GraphQLCheckDTO struct {
	Valid bool `json:"valid"`
	// Operations are the document's operations in source order.
	Operations []GraphQLOperationDTO `json:"operations"`
	// Error is the first syntax error, null when Valid.
	Error *GraphQLErrorDTO `json:"error"`
	// ErrorCount is the number of syntax errors, 0 when Valid.
	ErrorCount int `json:"errorCount"`
}

func toGraphQLCheckDTO(in string, doc *ast.Document, errors []parser.Error) GraphQLCheckDTO {
	out := GraphQLCheckDTO{Valid: len(errors) == 0, Operations: make([]GraphQLOperationDTO, 0), ErrorCount: len(errors)}
	for _, d := range doc.Definitions {
		op, ok := d.(*ast.OperationDefinition)
		if !ok {
			continue
		}
		dto := GraphQLOperationDTO{Type: "query"}
		if op.Name != nil {
			dto.Name = op.Name.Value
		}
		switch op.Operation {
		case ast.Mutation:
			dto.Type = "mutation"
		case ast.Subscription:
			dto.Type = "subscription"
		}
		out.Operations = append(out.Operations, dto)
	}
	if len(errors) > 0 {
		out.Error = toGraphQLErrorDTO(in, errors[0])
	}
	return out
}

// XMLErrorDTO is why an XML document could not be indented.
type XMLErrorDTO struct {
	// Line and Column are 1-based (Column counts bytes); 0 when the
	// document is over a limit rather than malformed.
	Line   int `json:"line"`
	Column int `json:"column"`
	// Message says what is wrong ("element <a> closed by </b>").
	Message string `json:"message"`
}

// XMLFormatDTO is the result of IndentXML.
type XMLFormatDTO struct {
	Valid bool `json:"valid"`
	// Text is the indented document, or the input unchanged when not Valid.
	Text string `json:"text"`
	// Error is null when Valid.
	Error *XMLErrorDTO `json:"error"`
}

// GraphQLVariableDTO is a variable the operation declares.
type GraphQLVariableDTO struct {
	// Name is without the "$".
	Name string `json:"name"`
	// Type is the declared type, such as "[ID!]!".
	Type string `json:"type"`
	// Required: a non-null type without a default value.
	Required bool `json:"required"`
}

// GraphQLUnknownVariableDTO is a member of the variables object the
// operation does not declare.
type GraphQLUnknownVariableDTO struct {
	Name string `json:"name"`
	// Offset and EndOffset are the member name's range in the variables
	// text (quotes included), in UTF-16 code units.
	Offset    int `json:"offset"`
	EndOffset int `json:"endOffset"`
	// Line and Column are 1-based; Column counts UTF-16 code units.
	Line   int `json:"line"`
	Column int `json:"column"`
}

// GraphQLVariablesCheckDTO is the result of CheckGraphQLVariables.
type GraphQLVariablesCheckDTO struct {
	// Error is the JSON syntax error of the variables, null when they are
	// valid JSON.
	Error *JSONErrorDTO `json:"error"`
	// NotObject: valid JSON, but not an object (nor null or blank).
	NotObject bool `json:"notObject"`
	// Resolved is false when the operation that runs is not known: no
	// operation, or several and none picked. Nothing is compared then.
	Resolved bool `json:"resolved"`
	// Operation is the name of that operation, "" when anonymous.
	Operation string `json:"operation"`
	// Declared are the operation's variables in source order.
	Declared []GraphQLVariableDTO `json:"declared"`
	// Missing are declared variables the object does not set.
	Missing []GraphQLVariableDTO `json:"missing"`
	// Unknown are the object's members the operation does not declare.
	Unknown []GraphQLUnknownVariableDTO `json:"unknown"`
}

func toGraphQLVariableDTOs(vs []request.GraphQLVariable) []GraphQLVariableDTO {
	out := make([]GraphQLVariableDTO, 0, len(vs))
	for _, v := range vs {
		out = append(out, GraphQLVariableDTO{Name: v.Name, Type: v.Type, Required: v.Required})
	}
	return out
}

func toGraphQLVariablesCheckDTO(text string, c request.GraphQLVariablesCheck) GraphQLVariablesCheckDTO {
	out := GraphQLVariablesCheckDTO{
		Error:     toJSONErrorDTO(c.Error),
		NotObject: c.NotObject,
		Resolved:  c.Resolved,
		Operation: c.Operation,
		Declared:  toGraphQLVariableDTOs(c.Declared),
		Missing:   toGraphQLVariableDTOs(c.Missing),
		Unknown:   make([]GraphQLUnknownVariableDTO, 0, len(c.Unknown)),
	}
	if len(c.Unknown) == 0 {
		return out
	}
	idx := lexer.NewUTF16Index(text)
	for _, k := range c.Unknown {
		lineStart := strings.LastIndexAny(text[:k.Start], "\r\n") + 1
		out.Unknown = append(out.Unknown, GraphQLUnknownVariableDTO{
			Name:      k.Name,
			Offset:    idx.ToUTF16(k.Start),
			EndOffset: idx.ToUTF16(k.End),
			Line:      lineOf(text, k.Start),
			Column:    idx.ToUTF16(k.Start) - idx.ToUTF16(lineStart) + 1,
		})
	}
	return out
}

// lineOf is the 1-based line of byte offset off: "\n", "\r\n" and a lone
// "\r" end a line, as in CodeMirror.
func lineOf(text string, off int) int {
	line := 1
	for i := 0; i < off && i < len(text); i++ {
		if text[i] == '\n' || (text[i] == '\r' && (i+1 >= len(text) || text[i+1] != '\n')) {
			line++
		}
	}
	return line
}
