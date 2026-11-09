// Package servers runs the local servers PostSonde offers (SRV-01, SRV-03):
// a Manager keeps the servers the user defined, starts and stops them, and
// records their incoming traffic as Events.
//
// A kind of server is a Type. The Manager opens the listening socket
// through internal/core/netx/listen (loopback unless the server allows
// LAN access), hands it to Type.Start, and gets back an Instance it stops
// later. Everything an instance sees (connections, requests, messages,
// responses, errors) goes to its Sink as Events; the Manager numbers them,
// caps their content, keeps the most recent ones per server and passes
// each to Options.OnTraffic.
//
// This package defines the model and the Manager. Server types live in
// subpackages (echo, and later the HTTP mock, WebSocket, TCP and webhook
// servers). See docs/servers.md.
package servers
