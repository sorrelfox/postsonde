package grpc

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// ProxySource returns the settings.proxy of grpc request requestID, as
// the editor has it, its password merged from the local side file
// (docs/network.md#a-requests-own-proxy). It is called on every call.
type ProxySource func(ctx context.Context, requestID string) (projectfile.RequestProxy, error)

// WithProxy calls through the request's own proxy (settings.proxy),
// which source returns, its URL and password rendered with the scopes of
// the request (scopes, the same as WithAuth's; nil keeps those of
// WithAuth): through it when custom, directly when none, through the
// proxy in effect when it inherits. The connections of each proxy are
// kept in a pool of their own, never shared with a call of another
// proxy setting. Without it, and for a call with no request ID, every
// call inherits.
func WithProxy(source ProxySource, scopes Scopes) Option {
	return func(s *GRPCService) {
		s.proxies = source
		if scopes != nil {
			s.scopes = scopes
		}
	}
}

// requestProxy builds the proxy of request requestID. Undefined variables
// in it refuse the call (template.unresolved), as does a proxy that
// cannot be used (netx.proxy_invalid_url, request.*).
func (s *GRPCService) requestProxy(ctx context.Context, requestID string) (request.Proxy, error) {
	if s.proxies == nil || requestID == "" {
		return request.Proxy{}, nil
	}
	p, err := s.proxies(ctx, requestID)
	if err != nil {
		return request.Proxy{}, err
	}
	if p.EffectiveMode() == projectfile.ProxyInherit {
		return request.Proxy{}, nil
	}
	expand, err := s.expander(ctx, requestID)
	if err != nil {
		return request.Proxy{}, err
	}
	return request.BuildProxy(p, expand)
}

// expander renders {{variables}} with the scopes of request requestID;
// any problem, an undefined name included, is an error.
func (s *GRPCService) expander(ctx context.Context, requestID string) (request.Expand, error) {
	var chain template.Chain
	if s.scopes != nil {
		c, err := s.scopes(ctx, requestID)
		if err != nil {
			return nil, err
		}
		chain = c
	}
	resolver := template.Resolver{Scopes: chain}
	return func(text string) (string, error) {
		res := resolver.Render(text)
		if err := res.Err(); err != nil {
			return "", err
		}
		return res.Text, nil
	}, nil
}
