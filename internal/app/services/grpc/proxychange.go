package grpc

// OnProxyChanged closes the idle connections of every cached transport
// client (SOCKS-05, docs/network.md#pools-and-proxy-changes): they were
// opened through the proxy in effect before, so the next call opens a
// connection with the new route. Calls in progress keep theirs.
// internal/app calls it from the dialer's netx.ProxyRouter.OnChange. It is
// a function, not a method, so it is not bound to the frontend.
func OnProxyChanged(s *GRPCService) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	for _, c := range s.clients {
		c.CloseIdleConnections()
	}
}
