package request

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// GraphQL over HTTP (GQL-01, GQL-02): a GraphQL request file becomes an
// HTTP request, POST with a JSON body or GET with the query in the URL, as
// the GraphQL-over-HTTP spec describes. See docs/graphql.md.

// Error codes and warnings of BuildGraphQL, besides those of BuildHTTP.
const (
	// CodeGraphQLQueryMissing: the query is blank.
	CodeGraphQLQueryMissing = "request.graphql_query_missing"
	// CodeGraphQLInvalidVariables: the variables (after their
	// {{variables}} are rendered) are not a JSON object. Details: field
	// ("variables"), offset (the byte offset of a syntax error, when there
	// is one).
	CodeGraphQLInvalidVariables = "request.graphql_invalid_variables"
	// CodeGraphQLInvalidExtensions: the extensions (after their
	// {{variables}} are rendered) are not a JSON object. Details: field
	// ("extensions"), offset (of a syntax error).
	CodeGraphQLInvalidExtensions = "request.graphql_invalid_extensions"
	// CodeGraphQLInvalidMethod: the file's method is neither empty, POST
	// nor GET (a hand-edited file). Details: method.
	CodeGraphQLInvalidMethod = "request.graphql_invalid_method"
	// WarnGraphQLMutationOverGET: the operation sent in GET mode is a
	// mutation, which servers reject over GET.
	WarnGraphQLMutationOverGET = "request.graphql_mutation_get"
)

// GraphQLAccept is the Accept header a GraphQL request sends unless its
// Headers table has an enabled Accept row: the GraphQL-over-HTTP media
// type first, plain JSON for older servers.
const GraphQLAccept = "application/graphql-response+json, application/json;q=0.9"

// GraphQLIncrementalAccept is the Accept header sent instead of
// GraphQLAccept when the document uses @defer or @stream (GQL-08): the
// multipart incremental delivery format first (Apollo's deferSpec
// parameter, which Apollo Router requires and other servers ignore), then
// the plain GraphQL responses of a server without incremental delivery.
const GraphQLIncrementalAccept = "multipart/mixed;deferSpec=20220824, application/graphql-response+json;q=0.9, application/json;q=0.8"

// UsesIncrementalDelivery reports whether a parsed document has a @defer
// or @stream directive anywhere (an operation or a fragment).
func UsesIncrementalDelivery(doc *ast.Document) bool {
	found := false
	ast.Inspect(doc, func(n ast.Node) bool {
		if d, ok := n.(*ast.Directive); ok && d.Name != nil && (d.Name.Value == "defer" || d.Name.Value == "stream") {
			found = true
		}
		return !found
	})
	return found
}

// GraphQLOperation is an operation of a GraphQL document.
type GraphQLOperation struct {
	// Name is empty for an anonymous operation.
	Name string
	// Type is "query", "mutation" or "subscription".
	Type string
}

// GraphQLOperations lists the operations of a GraphQL document in source
// order, also when the document has syntax errors (what the parser could
// recover).
func GraphQLOperations(query string) []GraphQLOperation {
	doc, _ := parser.Parse(query)
	return operationsOf(doc)
}

func operationsOf(doc *ast.Document) []GraphQLOperation {
	var out []GraphQLOperation
	for _, d := range doc.Definitions {
		op, ok := d.(*ast.OperationDefinition)
		if !ok {
			continue
		}
		name := ""
		if op.Name != nil {
			name = op.Name.Value
		}
		out = append(out, GraphQLOperation{Name: name, Type: operationType(op.Operation)})
	}
	return out
}

func operationType(t ast.OperationType) string {
	switch t {
	case ast.Mutation:
		return "mutation"
	case ast.Subscription:
		return "subscription"
	}
	return "query"
}

// GraphQLOperationName is the operationName a GraphQL request sends: the
// name of the document's only operation (empty when it is anonymous), or
// else the name saved in the file. A document with one operation always
// runs that one, so a stale saved name cannot make the server fail.
func GraphQLOperationName(ops []GraphQLOperation, saved string) string {
	if len(ops) == 1 {
		return ops[0].Name
	}
	return saved
}

// graphQLBody is the JSON body of a POST request (and the operations of
// a multipart one). Variables and extensions are left out when there are
// none, operationName when it is empty.
type graphQLBody struct {
	Query         string          `json:"query"`
	Variables     json.RawMessage `json:"variables,omitempty"`
	OperationName string          `json:"operationName,omitempty"`
	Extensions    json.RawMessage `json:"extensions,omitempty"`
}

// BuildGraphQL maps a GraphQL request file onto the request httpclient
// sends (GQL-02). auth is the auth the request sends, as for BuildHTTP.
//
// The URL, headers, auth and settings are built by BuildHTTP. Then:
//
//   - The query has its {{variables}} rendered (ENV-10), like every other
//     text field; GraphQL's own $variables are untouched, since they use
//     another syntax. A query that is blank after rendering is an error.
//   - The variables text has its {{variables}} rendered and must then be
//     a JSON object (or null, or blank for none); it is sent compacted.
//   - The extensions text (GQL-06) is rendered and checked like the
//     variables.
//   - operationName is GraphQLOperationName.
//   - POST (the default, method "" or "POST") sends
//     {"query", "variables", "operationName", "extensions"} as
//     application/json. GET adds query, variables, operationName and
//     extensions to the URL's query string, unless the URL already has a
//     parameter of that name; a mutation over GET adds a warning.
//   - With files (GQL-07, graphQLUploads) the POST body is instead a
//     GraphQL multipart request (graphQLMultipart), with the
//     GraphQLPreflightHeader unless a row sets it or settings.omitHeaders
//     names it; files over GET are CodeGraphQLUploadOverGET.
//   - Accept is GraphQLAccept, or GraphQLIncrementalAccept when the
//     document has @defer or @stream (GQL-08), unless a header row sets it.
func BuildGraphQL(f projectfile.GraphQLRequest, auth projectfile.Auth, o HTTPOptions) (HTTPBuild, error) {
	return buildGraphQL(f, auth, o, "")
}

// buildGraphQL is BuildGraphQL with accept, when not "", as the Accept
// header sent without an Accept row of the request's own.
func buildGraphQL(f projectfile.GraphQLRequest, auth projectfile.Auth, o HTTPOptions, accept string) (HTTPBuild, error) {
	var out HTTPBuild
	method := f.Method
	switch method {
	case "", "POST":
		method = "POST"
	case "GET":
	default:
		return out, errs.New(errs.InvalidArgument, "GraphQL requests are sent with POST or GET.").
			WithCode(CodeGraphQLInvalidMethod).
			WithHint("Pick POST or GET in the request's method menu.").
			WithDetail("method", f.Method)
	}

	// Signed at the end (sign), once the query parameters or the body are
	// added.

	b, err := buildHTTP(projectfile.HTTPRequest{
		Method:   method,
		URL:      f.URL,
		Headers:  f.Headers,
		Body:     projectfile.Body{Mode: projectfile.BodyNone},
		Settings: f.Settings,
	}, auth, o)
	if err != nil {
		return out, err
	}

	query := f.Query
	if o.Expand != nil && strings.Contains(query, "{{") {
		if query, err = o.Expand(query); err != nil {
			e, ok := err.(*errs.Error)
			if !ok {
				e = errs.Wrap(err, errs.InvalidArgument, "The query could not be rendered.").
					WithHint("Check the {{variables}} used in the Query tab.")
			}
			return out, e.WithDetail(DetailField, "query")
		}
	}
	if strings.TrimSpace(query) == "" {
		return out, errs.New(errs.InvalidArgument, "The GraphQL request has no query.").
			WithCode(CodeGraphQLQueryMissing).
			WithHint("Write the operation to run in the Query tab.")
	}
	vars, err := graphQLVariables(f.Variables, o.Expand)
	if err != nil {
		return out, err
	}
	exts, err := graphQLExtensions(f.Extensions, o.Expand)
	if err != nil {
		return out, err
	}
	uploads, err := graphQLUploads(f.Files)
	if err != nil {
		return out, err
	}
	if method == "GET" && len(uploads) > 0 {
		return out, errs.New(errs.InvalidArgument, "Files cannot be uploaded with GET.").
			WithCode(CodeGraphQLUploadOverGET).
			WithHint("Switch the request to POST, or disable the rows in the Files tab.").
			WithDetail(DetailField, "files")
	}
	doc, _ := parser.Parse(query)
	ops := operationsOf(doc)
	opName := GraphQLOperationName(ops, f.OperationName)

	if method == "GET" {
		params := []QueryParam{{Name: "query", Value: query}}
		if vars != nil {
			params = append(params, QueryParam{Name: "variables", Value: string(vars)})
		}
		if opName != "" {
			params = append(params, QueryParam{Name: "operationName", Value: opName})
		}
		if exts != nil {
			params = append(params, QueryParam{Name: "extensions", Value: string(exts)})
		}
		b.Request.URL = WithAuthQuery(b.Request.URL, params)
		if isMutation(ops, opName) {
			b.Warnings = append(b.Warnings, Warning{
				Code:    WarnGraphQLMutationOverGET,
				Message: "Mutations are usually rejected over GET; switch the request to POST if the server refuses it.",
			})
		}
	} else if body := (graphQLBody{Query: query, Variables: vars, OperationName: opName, Extensions: exts}); len(uploads) > 0 {
		mb, err := graphQLMultipart(body, uploads, o.Root)
		if err != nil {
			return out, err
		}
		b.Request.Body = mb
		b.Request.Headers = withPreflightHeader(b.Request.Headers, f.Settings.OmitHeaders)
	} else {
		data, err := marshalJSON(body)
		if err != nil {
			return out, errs.Wrap(err, errs.Internal, "The GraphQL request body could not be written.")
		}
		b.Request.Body = httpclient.RawBody{Data: data, Type: "application/json"}
	}

	if !hasHeader(b.Request.Headers, "Accept") {
		switch {
		case accept != "":
		case UsesIncrementalDelivery(doc):
			accept = GraphQLIncrementalAccept
		default:
			accept = GraphQLAccept
		}
		b.Request.Headers = append(b.Request.Headers, httpclient.Header{Name: "Accept", Value: accept, Enabled: true})
	}
	// Signed last, once the query parameters or the body and the headers
	// are added.
	if err := b.sign(); err != nil {
		return out, err
	}
	return b, nil
}

// graphQLVariables renders and checks the variables text. It returns nil
// for none (blank text or JSON null), else the compacted JSON object.
func graphQLVariables(text string, expand Expand) (json.RawMessage, error) {
	return graphQLObject(text, expand, objectField{
		field: "variables", code: CodeGraphQLInvalidVariables,
		noun: "variables", title: "GraphQL variables", tab: "Variables", example: `{"id": 1}`,
	})
}

// graphQLExtensions renders and checks the extensions text (GQL-06), as
// graphQLVariables does the variables.
func graphQLExtensions(text string, expand Expand) (json.RawMessage, error) {
	return graphQLObject(text, expand, objectField{
		field: "extensions", code: CodeGraphQLInvalidExtensions,
		noun: "extensions", title: "GraphQL extensions", tab: "Extensions",
		example: `{"persistedQuery": {"version": 1, "sha256Hash": "…"}}`,
	})
}

// objectField names a JSON-object text field of a GraphQL request for the
// errors of graphQLObject.
type objectField struct {
	field, code, noun, title, tab, example string
}

// graphQLObject renders and checks a JSON-object text field (the
// variables, the connection params). It returns nil for none (blank text
// or JSON null), else the compacted JSON object.
func graphQLObject(text string, expand Expand, f objectField) (json.RawMessage, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	if expand != nil {
		var err error
		if text, err = expand(text); err != nil {
			e, ok := err.(*errs.Error)
			if !ok {
				e = errs.Wrap(err, errs.InvalidArgument, "The "+f.noun+" could not be rendered.").
					WithHint("Check the {{variables}} used in the " + f.tab + " tab.")
			}
			return nil, e.WithDetail(DetailField, f.field)
		}
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(trimmed)); err != nil {
		e := errs.New(errs.InvalidArgument, "The "+f.title+" are not valid JSON.").
			WithCode(f.code).
			WithHint("Fix the JSON in the "+f.tab+" tab, or clear it to send no "+f.noun+".").
			WithDetail(DetailField, f.field)
		if se, ok := err.(*json.SyntaxError); ok {
			e = e.WithDetail("offset", strconv.FormatInt(se.Offset, 10))
		}
		return nil, e
	}
	if buf.Bytes()[0] != '{' {
		return nil, errs.New(errs.InvalidArgument, "The "+f.title+" must be a JSON object.").
			WithCode(f.code).
			WithHint("Write the "+f.noun+" as an object, e.g. "+f.example+".").
			WithDetail(DetailField, f.field)
	}
	return buf.Bytes(), nil
}

// isMutation reports whether the operation that runs is a mutation.
func isMutation(ops []GraphQLOperation, name string) bool {
	for _, op := range ops {
		if op.Name == name || len(ops) == 1 {
			return op.Type == "mutation"
		}
	}
	return false
}

// hasHeader reports whether an enabled header is named name, ignoring case.
func hasHeader(headers []httpclient.Header, name string) bool {
	for _, h := range headers {
		if h.Enabled && strings.EqualFold(h.Name, name) {
			return true
		}
	}
	return false
}
