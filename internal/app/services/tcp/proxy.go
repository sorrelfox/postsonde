package tcp

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// ProxySource returns the settings.proxy of tcp request requestID, as the
// editor has it, its password merged from the local side file
// (docs/network.md#a-requests-own-proxy). It is called on every connect.
type ProxySource func(ctx context.Context, requestID string) (projectfile.RequestProxy, error)

// Scopes returns the variable scopes of tcp request requestID, as for an
// HTTP send: the {{variables}} of its proxy are rendered with them.
type Scopes func(ctx context.Context, requestID string) (template.Chain, error)

// WithProxy connects through the request's own proxy (settings.proxy),
// which source returns, its URL and password rendered with the scopes of
// the request (scopes; nil defines no variable): through it when custom,
// directly when none, through the proxy in effect when it inherits.
// Without it, and for a connection with no request ID, every connection
// inherits.
func WithProxy(source ProxySource, scopes Scopes) Option {
	return func(s *TCPService) { s.proxies, s.scopes = source, scopes }
}

// requestProxy builds the proxy of request requestID. Undefined variables
// in it refuse the connect (template.unresolved), as does a proxy that
// cannot be used (netx.proxy_invalid_url, request.*).
func (s *TCPService) requestProxy(ctx context.Context, requestID string) (request.Proxy, error) {
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
	var chain template.Chain
	if s.scopes != nil {
		if chain, err = s.scopes(ctx, requestID); err != nil {
			return request.Proxy{}, err
		}
	}
	resolver := template.Resolver{Scopes: chain}
	return request.BuildProxy(p, func(text string) (string, error) {
		res := resolver.Render(text)
		if err := res.Err(); err != nil {
			return "", err
		}
		return res.Text, nil
	})
}
