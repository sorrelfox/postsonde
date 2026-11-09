package http

import (
	"context"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// GraphQLSubscription is what running GraphQL request id over WebSocket
// needs (GQL-30, GQL-33): the WebSocket options of the handshake (the
// endpoint as ws:// or wss://, the request's headers and auth, TLS
// verification (the request's verifyTls, else network.verifyTls), the
// custom CA bundles (NET-04), timeout, the app's dialer and the project's
// cookie jar)
// and the rendered payloads. It is not bound; the GraphQL service gets it
// through NewGraphQLSubscription.
type GraphQLSubscription struct {
	// Transport is the transport the request's settings pick for the
	// operation that runs (GQL-35, request.GraphQLTransportOf), and
	// OperationType that operation's type ("" when it cannot be told).
	// For projectfile.GraphQLTransportHTTP nothing else is set.
	Transport     string
	OperationType string
	// WS and Build are set for the WebSocket transports.
	WS    wsclient.Options
	Build request.GraphQLWSBuild
	// SSE is set for projectfile.GraphQLTransportSSE (GQL-32): the request
	// built as a send with Accept: text/event-stream
	// (request.BuildGraphQLSSE), and SSEBody its JSON body with secrets
	// masked ("" for GET), for the event log.
	SSE     *Stream
	SSEBody string
}

// NewGraphQLSubscription loads the GraphQL request id (pending edits
// included) and builds it for the transport its settings pick: WebSocket
// (request.BuildGraphQLWS) or SSE (request.BuildGraphQLSSE, as Send
// builds it). It fails as Send does: an unknown ID, CodeNotGraphQL for
// another kind, undefined {{variables}} of every field together as one
// template.unresolved error unless allowUnresolved (ENV-11), then the
// build's errors.
func NewGraphQLSubscription(ctx context.Context, s *HTTPService, id string, allowUnresolved bool) (*GraphQLSubscription, error) {
	src, chain, err := loadGraphQL(ctx, s, id)
	if err != nil {
		return nil, err
	}
	transport, opType, err := request.GraphQLTransportOf(*src.GraphQL)
	if err != nil {
		return nil, err
	}
	switch transport {
	case projectfile.GraphQLTransportHTTP:
		return &GraphQLSubscription{Transport: transport, OperationType: opType}, nil
	case projectfile.GraphQLTransportSSE:
		return newGraphQLSSE(ctx, s, id, allowUnresolved, opType)
	}
	var def time.Duration
	if s.defaultTimeout != nil {
		def = s.defaultTimeout()
	}
	root := ""
	if src.Workspace != nil {
		root = src.Workspace.Root
	}
	// A subscription does not ask for {{$prompt}} values (DYN-07): they
	// refuse with template.prompt.
	x := &expansion{resolver: template.Resolver{Scopes: chain}, allowUnresolved: allowUnresolved, askPrompts: true}
	opts := request.HTTPOptions{Expand: x.expand, Root: root, AllowedDirs: s.allowed(), DefaultTimeout: def, DefaultInsecure: s.defaultInsecure(), FolderHeaders: src.FolderHeaders, Netrc: s.netrcOptions(false)}
	b, err := request.BuildGraphQLWS(*src.GraphQL, src.Auth, opts)
	// Prompts, then undefined variables first, as in Send.
	if perr := x.promptErr(); perr != nil {
		return nil, perr
	}
	if uerr := x.unresolvedErr(); uerr != nil {
		return nil, uerr
	}
	if err != nil {
		return nil, err
	}
	if err := s.checkTLSPolicy(b.InsecureSkipVerify, "GraphQL"); err != nil {
		return nil, err
	}
	o := wsclient.Options{
		URL:                b.URL,
		Headers:            b.Headers,
		Dialer:             s.dialer,
		InsecureSkipVerify: b.InsecureSkipVerify,
		HandshakeTimeout:   b.HandshakeTimeout,
	}
	if !o.InsecureSkipVerify && s.roots != nil {
		// The custom CA bundles (NET-04), as for a send: only wss uses
		// them, so a broken one does not stop a ws connection.
		roots, err := s.roots()
		switch {
		case err == nil:
			o.RootCAs = roots
		case strings.HasPrefix(o.URL, "wss:"):
			return nil, err
		default:
			s.logger.Warn("custom CA certificates not used", "err", err)
		}
	}
	// The client certificate of the host (AUTH-30), for wss.
	cert, err := httpclient.ClientCertForURL(s.clientCerts, o.URL)
	if err != nil {
		return nil, err
	}
	o.ClientCertificate = cert
	// sendCookies false (HTTP-26) leaves the jar out of the handshake, as
	// for a WebSocket request; the handshake then stores none either.
	if send := src.GraphQL.Settings.SendCookies; s.jars != nil && (send == nil || *send) {
		if jar, err := s.jars.Jar(); err != nil {
			s.logger.Warn("graphql subscription connects without cookies", "err", err)
		} else if jar != nil {
			o.Jar = jar
		}
	}
	return &GraphQLSubscription{Transport: transport, OperationType: opType, WS: o, Build: b}, nil
}

// newGraphQLSSE builds GraphQL request id for GraphQL over SSE: the
// request Send sends (the app's HTTP client, TLS trust, client
// certificates and cookie jar go with Stream.Open) and its payload.
func newGraphQLSSE(ctx context.Context, s *HTTPService, id string, allowUnresolved bool, opType string) (*GraphQLSubscription, error) {
	_, x, b, err := s.build(ctx, id, buildOptions{allowUnresolved: allowUnresolved})
	if err != nil {
		return nil, err
	}
	masker := httpclient.NewSecretMasker(secretsOf(x, b))
	body := ""
	if raw, ok := b.Request.Body.(httpclient.RawBody); ok {
		body = masker.MaskText(string(raw.Data))
	}
	return &GraphQLSubscription{
		Transport:     projectfile.GraphQLTransportSSE,
		OperationType: opType,
		SSE: &Stream{
			Method: b.Request.Method,
			URL:    masker.MaskText(b.Request.URL),
			s:      s,
			build:  b,
			masker: masker,
		},
		SSEBody: body,
	}, nil
}
