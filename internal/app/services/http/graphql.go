package http

import (
	"context"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// CodeNotGraphQL: the request is not a GraphQL request (kind
// invalid_argument).
const CodeNotGraphQL = "http.not_graphql"

// GraphQLPoster posts documents of the app's own to a GraphQL request's
// endpoint, with the request's headers, auth, settings, cookies and
// environment: the introspection query of the schema fetch (GQL-10). It
// is not bound; the GraphQL service gets it through NewGraphQLPoster.
type GraphQLPoster struct {
	// Endpoint is the request's URL with its {{variables}} rendered and
	// surrounding spaces trimmed: what a schema belongs to.
	Endpoint string

	s     *HTTPService
	file  projectfile.GraphQLRequest
	auth  projectfile.Auth
	chain template.Chain
	opts  request.HTTPOptions
	// files reads the {{$file}} references (DYN-08).
	files template.FileReader
}

// GraphQLEndpoint returns the endpoint of the GraphQL request id, its URL
// with {{variables}} rendered, without sending anything. Undefined
// variables stay as written. It fails as NewGraphQLPoster does otherwise.
func GraphQLEndpoint(ctx context.Context, s *HTTPService, id string) (string, error) {
	src, chain, err := loadGraphQL(ctx, s, id)
	if err != nil {
		return "", err
	}
	x := &expansion{resolver: template.Resolver{Scopes: chain}, allowUnresolved: true}
	// A relative URL goes to {{baseUrl}}, as a send (COL-08).
	endpoint, err := request.ExpandURL(src.GraphQL.URL, x.expand)
	if err != nil {
		return strings.TrimSpace(src.GraphQL.URL), nil
	}
	return endpoint, nil
}

// loadGraphQL loads the GraphQL request id and its variable scopes.
func loadGraphQL(ctx context.Context, s *HTTPService, id string) (collection.HTTPSource, template.Chain, error) {
	if err := ctx.Err(); err != nil {
		return collection.HTTPSource{}, nil, errs.Wrap(err, errs.KindOf(err), "Loading the request was interrupted.")
	}
	if s == nil || s.load == nil || s.dialer == nil {
		return collection.HTTPSource{}, nil, errs.New(errs.Unsupported, "Sending HTTP requests is not available.").WithCode(CodeUnavailable)
	}
	src, err := s.load(ctx, id)
	if err != nil {
		return collection.HTTPSource{}, nil, err
	}
	if src.GraphQL == nil {
		return collection.HTTPSource{}, nil, errs.New(errs.InvalidArgument, "Only GraphQL requests have a schema.").
			WithCode(CodeNotGraphQL).
			WithDetail("requestId", id)
	}
	chain, err := s.chainOf(src)
	if err != nil {
		return collection.HTTPSource{}, nil, err
	}
	return src, chain, nil
}

// NewGraphQLPoster loads the GraphQL request id (pending edits included)
// and renders its endpoint. It fails as Send does for an unknown ID or an
// undefined {{variable}} (template.unresolved, all of the URL, headers and
// auth together), and with CodeNotGraphQL for another kind of request.
func NewGraphQLPoster(ctx context.Context, s *HTTPService, id string) (*GraphQLPoster, error) {
	src, chain, err := loadGraphQL(ctx, s, id)
	if err != nil {
		return nil, err
	}
	var def time.Duration
	if s.defaultTimeout != nil {
		def = s.defaultTimeout()
	}
	root := ""
	if src.Workspace != nil {
		root = src.Workspace.Root
	}
	files := s.fileReader(src)
	x := &expansion{resolver: template.Resolver{Scopes: chain, Files: files}}
	endpoint, err := request.ExpandURL(src.GraphQL.URL, x.expand)
	if err != nil {
		return nil, err
	}
	p := &GraphQLPoster{
		Endpoint: endpoint,
		s:        s,
		file:     *src.GraphQL,
		auth:     src.Auth,
		chain:    chain,
		files:    files,
		opts:     request.HTTPOptions{Root: root, AllowedDirs: s.allowed(), DefaultTimeout: def, DefaultInsecure: s.defaultInsecure(), FolderHeaders: src.FolderHeaders, Netrc: s.netrcOptions(false)},
	}
	// Build once now, so a request that cannot be built fails here, not
	// in the operation.
	if _, err := p.Build(schemaProbe, ""); err != nil {
		return nil, err
	}
	return p, nil
}

// schemaProbe is the document NewGraphQLPoster builds to check the request.
const schemaProbe = "{ __typename }"

// Build returns the request Post sends for query: the GraphQL request with
// query in place of its document, no variables, extensions, files or
// operationName of its own, always
// POST (an introspection query is too long for a URL).
func (p *GraphQLPoster) Build(query, operationName string) (request.HTTPBuild, error) {
	f := p.file
	f.Method = ""
	f.Query = query
	f.Variables = ""
	f.Extensions = ""
	f.Files = nil
	f.OperationName = operationName
	x := &expansion{resolver: template.Resolver{Scopes: p.chain, Files: p.files}}
	opts := p.opts
	opts.Expand = x.expand
	b, err := request.BuildGraphQL(f, p.auth, opts)
	// Undefined variables first, as in Send.
	if uerr := x.unresolvedErr(); uerr != nil {
		return request.HTTPBuild{}, uerr
	}
	return b, err
}

// Post sends query and returns the response. ctx must be an operation's
// context: the app's dialer refuses connections without one (PRV-05).
func (p *GraphQLPoster) Post(ctx context.Context, query, operationName string) (*httpclient.Response, error) {
	b, err := p.Build(query, operationName)
	if err != nil {
		return nil, err
	}
	// An oauth2 auth's token (AUTH-08), stored or got now.
	if _, err := p.s.authorizeOAuth2(ctx, &b); err != nil {
		return nil, err
	}
	client, err := p.s.client(b.InsecureSkipVerify, b.Request.URL, b.Proxy)
	if err != nil {
		return nil, err
	}
	if p.s.jars != nil {
		if jar, err := p.s.jars.Jar(); err != nil {
			p.s.logger.Warn("graphql request sent without cookies", "err", err)
		} else {
			client = client.WithCookieJar(jar)
		}
	}
	resp, err := client.Send(ctx, b.Request)
	if err != nil {
		return nil, timeoutError(err, b)
	}
	return resp, nil
}
