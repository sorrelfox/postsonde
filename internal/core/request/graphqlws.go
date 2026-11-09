package request

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// GraphQL over WebSocket (GQL-30, GQL-33, GQL-34): a GraphQL request file
// becomes the handshake and the payloads of a graphql-transport-ws
// session. See docs/graphql-subscriptions.md.

// Error codes of BuildGraphQLWS, besides those of BuildHTTP and
// BuildGraphQL.
const (
	// CodeGraphQLInvalidConnectionParams: the connection params (after
	// their {{variables}} are rendered) are not a JSON object. Details:
	// field ("connectionParams"), offset (of a syntax error).
	CodeGraphQLInvalidConnectionParams = "request.graphql_invalid_connection_params"
	// CodeGraphQLWebSocketScheme: the endpoint's scheme is not http,
	// https, ws or wss. Details: scheme.
	CodeGraphQLWebSocketScheme = "request.graphql_ws_scheme"
)

// GraphQLWSBuild is a GraphQL request ready to run over WebSocket.
type GraphQLWSBuild struct {
	// URL is the endpoint with its scheme mapped to ws:// or wss://.
	URL string
	// Headers are the handshake's extra headers: the request's rows, the
	// folders' and the auth's, as for BuildHTTP (no Accept or
	// Content-Type is added).
	Headers []httpclient.Header
	// InsecureSkipVerify, TLSSource and Proxy are as in HTTPBuild.
	InsecureSkipVerify bool
	TLSSource          string
	Proxy              Proxy
	// HandshakeTimeout is the request's timeout (its own or the app's
	// default), bounding the WebSocket handshake; zero leaves the client's
	// default.
	HandshakeTimeout time.Duration
	// Query is the document with {{variables}} rendered.
	Query string
	// OperationName is GraphQLOperationName; OperationType the type of the
	// operation that runs ("query", "mutation", "subscription"), "" when
	// it cannot be told (several operations and none picked).
	OperationName string
	OperationType string
	// Variables, Extensions (GQL-06, the subscribe payload's extensions)
	// and ConnectionParams are compacted JSON objects, nil for none.
	Variables        json.RawMessage
	Extensions       json.RawMessage
	ConnectionParams json.RawMessage
}

// BuildGraphQLWS maps a GraphQL request file onto a graphql-transport-ws
// session (GQL-30, GQL-33). auth is the auth the request sends, as for
// BuildHTTP.
//
// The endpoint, header rows, folder headers, auth (headers and query
// parameters), timeout and TLS verification are built by BuildHTTP, as a
// GET without a body; then the scheme is mapped: http to ws, https to
// wss, ws and wss kept. The query, variables, extensions and
// operationName are rendered and checked as by BuildGraphQL (files are
// not sent over WebSocket and are ignored); the connection params text has
// its {{variables}} rendered and must then be a JSON object (or null, or
// blank for none).
func BuildGraphQLWS(f projectfile.GraphQLRequest, auth projectfile.Auth, o HTTPOptions) (GraphQLWSBuild, error) {
	var out GraphQLWSBuild
	b, err := BuildHTTP(projectfile.HTTPRequest{
		Method:   "GET",
		URL:      f.URL,
		Headers:  f.Headers,
		Body:     projectfile.Body{Mode: projectfile.BodyNone},
		Settings: f.Settings,
	}, auth, o)
	if err != nil {
		return out, err
	}
	if b.OAuth2 != nil {
		// The token is got as a user operation, which the WebSocket
		// handshake is not yet (docs/auth.md, "Not handled yet").
		return out, errs.New(errs.Unsupported, "PostSonde cannot get an OAuth 2.0 token for a GraphQL subscription over WebSocket yet.").
			WithCode(CodeAuthUnsupported).
			WithHint("Run the subscription over SSE, or send the token as a bearer token from a {{variable}}.").
			WithDetail(DetailAuthType, projectfile.AuthOAuth2)
	}
	if out.URL, err = GraphQLWebSocketURL(b.Request.URL); err != nil {
		return out, err
	}
	out.Headers = b.Request.Headers
	out.InsecureSkipVerify, out.TLSSource, out.Proxy = b.InsecureSkipVerify, b.TLSSource, b.Proxy
	out.HandshakeTimeout = b.Request.Settings.Timeout

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
			WithHint("Write the subscription to run in the Query tab.")
	}
	if out.Variables, err = graphQLVariables(f.Variables, o.Expand); err != nil {
		return out, err
	}
	if out.Extensions, err = graphQLExtensions(f.Extensions, o.Expand); err != nil {
		return out, err
	}
	if out.ConnectionParams, err = graphQLObject(f.ConnectionParams, o.Expand, objectField{
		field: "connectionParams", code: CodeGraphQLInvalidConnectionParams,
		noun: "connection params", title: "connection params", tab: "Connection",
		example: `{"authToken": "…"}`,
	}); err != nil {
		return out, err
	}
	doc, _ := parser.Parse(query)
	ops := operationsOf(doc)
	out.Query = query
	out.OperationName = GraphQLOperationName(ops, f.OperationName)
	out.OperationType = operationTypeOf(ops, f.OperationName)
	return out, nil
}

// operationTypeOf is the type of the operation that runs: the only one,
// or the one named saved; "" when there is none.
func operationTypeOf(ops []GraphQLOperation, saved string) string {
	if len(ops) == 1 {
		return ops[0].Type
	}
	for _, op := range ops {
		if op.Name == saved && saved != "" {
			return op.Type
		}
	}
	return ""
}

// GraphQLWebSocketURL maps a GraphQL endpoint to its WebSocket URL: http
// becomes ws and https wss (the scheme in any case); ws and wss URLs are
// kept. Anything else is CodeGraphQLWebSocketScheme.
func GraphQLWebSocketURL(endpoint string) (string, error) {
	scheme, rest, ok := strings.Cut(endpoint, "://")
	var mapped string
	switch strings.ToLower(scheme) {
	case "http", "ws":
		mapped = "ws"
	case "https", "wss":
		mapped = "wss"
	}
	if !ok || mapped == "" {
		return "", errs.New(errs.InvalidArgument, "Subscriptions need an http, https, ws or wss endpoint.").
			WithCode(CodeGraphQLWebSocketScheme).
			WithHint("Enter the endpoint as https://host/graphql or wss://host/graphql; PostSonde connects over WebSocket.").
			WithDetail("scheme", scheme)
	}
	return mapped + "://" + rest, nil
}
