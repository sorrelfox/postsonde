package udp

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// ProxySource returns the settings.proxy of udp request requestID, as the
// editor has it, its password merged from the local side file
// (docs/network.md#a-requests-own-proxy). It is called on every open.
type ProxySource func(ctx context.Context, requestID string) (projectfile.RequestProxy, error)

// WithProxy opens sockets through the request's own proxy
// (settings.proxy), which source returns, its URL and password rendered
// with the request's scopes (WithScopes): a custom SOCKS5 proxy relays the
// datagrams with UDP ASSOCIATE, a SOCKS4 or HTTP one refuses to open
// (netx.socks_unsupported_network, netx.http_proxy_unsupported_network),
// never sending directly; none sends directly; inherit uses the proxy in
// effect. Without it, and for a socket with no request ID, every socket
// inherits.
func WithProxy(source ProxySource) Option {
	return func(s *UDPService) { s.proxies = source }
}

// requestProxy builds the proxy of request requestID. Undefined variables
// in it refuse the open (template.unresolved), as does a proxy that
// cannot be used (netx.proxy_invalid_url, request.*).
func (s *UDPService) requestProxy(ctx context.Context, requestID string) (request.Proxy, error) {
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
	return request.BuildProxy(p, func(text string) (string, error) {
		return s.render(ctx, requestID, text)
	})
}
