// Package httpclient sends HTTP requests exactly as the user configured
// them, with net/http over a netx.Dialer.
//
// A Request holds what the request editor shows: the method, the URL as
// typed, an ordered list of header rows, a Body and per-request Settings.
// Client.Send converts it into an http.Request without adding headers of
// its own (no User-Agent, no Accept-Encoding, no cookies, no proxy), sends
// it over a Transport whose every connection comes from the dialer, and
// captures the response: status, headers and the raw body bytes, capped by
// Settings.MaxBodyBytes. Client.SendTo writes the body to a writer as it
// arrives instead, decompressing gzip and deflate on the way (HTTP-37).
//
//	c := httpclient.New(dialer, httpclient.TransportOptions{})
//	resp, err := c.Send(ctx, httpclient.Request{
//		Method:   "GET",
//		URL:      "https://api.example.com/items?page=2",
//		Headers:  []httpclient.Header{{Name: "Accept", Value: "application/json", Enabled: true}},
//		Settings: httpclient.DefaultSettings(),
//	})
//
// The context cancels the request at any stage, and carries the hooks of
// the timeline: a netx.Trace and a net/http/httptrace ClientTrace. Errors
// are *errs.Error values with kinds and httpclient.* codes.
//
// Every Response carries its Timing (DNS, connect, TLS, request write,
// wait, download), its Conn (reused or new, addresses, protocol, TLS
// session), the Redirects hops and the raw Events of the send, recorded
// with httptrace.
//
// What net/http still decides (header order on HTTP/1.1, lowercase names
// on HTTP/2, a few headers written in canonical spelling) is listed in
// docs/http-client.md.
//
// The package imports only the standard library, like all of internal/core.
package httpclient
