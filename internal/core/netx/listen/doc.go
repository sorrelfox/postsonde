// Package listen opens every listening socket PostSonde creates: the OAuth
// redirect listener, mock servers and the WebSocket, TCP and UDP servers.
//
// Listeners bind to loopback by default (SRV-02, PRV-07). An empty host or
// "localhost" means 127.0.0.1, or 127.0.0.1 and ::1 with Spec.DualStack.
// Binding a LAN address, a public address or all interfaces (0.0.0.0, ::)
// fails with a permission_denied error unless Spec.AllowLAN is set; then
// Info carries a Warning for the UI to show next to the server.
//
// Host names other than localhost are rejected: a listen address is an IP
// literal and never goes through DNS, so what is bound is exactly what the
// user typed.
//
// Code outside this package does not call net.Listen, net.ListenPacket,
// net.ListenTCP, net.ListenUDP, net.ListenConfig, tls.Listen or
// http.ListenAndServe; TestNoDirectListens enforces it. See "Listening" in
// docs/network.md.
//
// The package imports only the standard library, like all of internal/core.
package listen
