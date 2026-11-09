package request

import (
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Per-operation transport (GQL-35): a GraphQL request runs its queries,
// mutations and subscriptions over HTTP, WebSocket (graphql-transport-ws
// or the legacy subscriptions-transport-ws) or Server-Sent Events, as its
// settings.graphqlTransport says. See docs/graphql-subscriptions.md.

// CodeGraphQLInvalidTransport: the transport picked for the operation's
// type is not one this version knows, or is http for a subscription.
// Details: field ("settings.graphqlTransport.subscription"), transport.
const CodeGraphQLInvalidTransport = "request.graphql_invalid_transport"

// EventStreamAccept is the Accept header of GraphQL over SSE (GQL-32).
const EventStreamAccept = "text/event-stream"

// GraphQLTransportFor returns the transport an operation of type opType
// ("query", "mutation", "subscription"; "" counts as a query) runs over:
// the one t picks, or the default, projectfile.GraphQLTransportHTTP for
// queries and mutations and projectfile.GraphQLTransportWS for
// subscriptions. An unknown value, or http for a subscription, is
// CodeGraphQLInvalidTransport.
func GraphQLTransportFor(t *projectfile.GraphQLTransport, opType string) (string, error) {
	member, def, picked := "query", projectfile.GraphQLTransportHTTP, ""
	switch opType {
	case "mutation":
		member = "mutation"
		if t != nil {
			picked = t.Mutation
		}
	case "subscription":
		member, def = "subscription", projectfile.GraphQLTransportWS
		if t != nil {
			picked = t.Subscription
		}
	default:
		if t != nil {
			picked = t.Query
		}
	}
	switch picked {
	case "":
		return def, nil
	case projectfile.GraphQLTransportWS, projectfile.GraphQLTransportLegacyWS, projectfile.GraphQLTransportSSE:
		return picked, nil
	case projectfile.GraphQLTransportHTTP:
		if member != "subscription" {
			return picked, nil
		}
		return "", errs.New(errs.InvalidArgument, "Subscriptions cannot run over plain HTTP.").
			WithCode(CodeGraphQLInvalidTransport).
			WithHint("Pick WebSocket or Server-Sent Events for subscriptions in the Connection tab.").
			WithDetail(DetailField, "settings.graphqlTransport."+member).
			WithDetail("transport", picked)
	}
	return "", errs.New(errs.InvalidArgument, "The "+member+" transport "+strconv.Quote(picked)+" is not known.").
		WithCode(CodeGraphQLInvalidTransport).
		WithHint("Pick a transport for "+member+" operations in the Connection tab.").
		WithDetail(DetailField, "settings.graphqlTransport."+member).
		WithDetail("transport", picked)
}

// GraphQLTransportOf returns the transport the request's operation that
// runs uses (GraphQLTransportFor) and that operation's type, "" when it
// cannot be told. The document is read as typed, {{variables}} and all:
// they cannot change an operation's type.
func GraphQLTransportOf(f projectfile.GraphQLRequest) (transport, opType string, err error) {
	opType = operationTypeOf(GraphQLOperations(f.Query), f.OperationName)
	transport, err = GraphQLTransportFor(f.Settings.GraphQLTransport, opType)
	return transport, opType, err
}

// BuildGraphQLSSE maps a GraphQL request file onto a GraphQL over
// Server-Sent Events request (GQL-32, graphql-sse's distinct connections
// mode): BuildGraphQL's request, POST with the JSON body (or GET with the
// query string), with Accept: text/event-stream unless a header row sets
// Accept. Files (GQL-07) are not sent.
func BuildGraphQLSSE(f projectfile.GraphQLRequest, auth projectfile.Auth, o HTTPOptions) (HTTPBuild, error) {
	f.Files = nil
	return buildGraphQL(f, auth, o, EventStreamAccept)
}
