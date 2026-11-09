package http

// OnProxyChanged closes the idle kept-alive connections of every cached
// client (SOCKS-05, docs/network.md#pools-and-proxy-changes): they were
// opened through the proxy in effect before, so the next request of every
// kind (HTTP, GraphQL, SSE, OAuth token calls) opens a connection with the
// new route. Requests in progress keep theirs. internal/app calls it from
// the dialer's netx.ProxyRouter.OnChange. It is a function, not a method,
// so it is not bound to the frontend.
func OnProxyChanged(s *HTTPService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.clients {
		c.CloseIdleConnections()
	}
}
