package websocket

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// ProxySource returns the settings.proxy of websocket request requestID,
// as the editor has it, its password merged from the local side file
// (docs/network.md#a-requests-own-proxy). It is called on every connect
// and reconnect.
type ProxySource func(ctx context.Context, requestID string) (projectfile.RequestProxy, error)

// WithProxy connects through the request's own proxy (settings.proxy),
// which source returns, its URL and password rendered with the request's
// scopes like the URL: through it when custom, directly when none,
// through the proxy in effect when it inherits. Every attempt reads it
// anew, so a reconnect uses the proxy as it is then. Without it, and for
// a connection with no request ID, every connection inherits.
func WithProxy(source ProxySource) Option {
	return func(s *WebSocketService) { s.proxies = source }
}

// requestProxy builds the proxy of request requestID with r, so its
// undefined variables are reported with the URL's.
func (s *WebSocketService) requestProxy(ctx context.Context, requestID string, r *rendering) (request.Proxy, error) {
	if s.proxies == nil || requestID == "" {
		return request.Proxy{}, nil
	}
	p, err := s.proxies(ctx, requestID)
	if err != nil {
		return request.Proxy{}, err
	}
	proxy, err := request.BuildProxy(p, r.render)
	if err != nil {
		if uerr := r.unresolvedErr(); uerr != nil {
			return request.Proxy{}, uerr
		}
		return request.Proxy{}, err
	}
	return proxy, nil
}
