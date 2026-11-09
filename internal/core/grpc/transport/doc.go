// Package transport carries gRPC calls over Go's standard HTTP/2 client
// (net/http), without grpc-go: plaintext servers are reached with h2c
// prior knowledge and TLS servers with ALPN h2, every connection is opened
// through a netx.Dialer, and messages are raw bytes, so any codec can sit
// on top (package dynamic for protobuf).
//
// Each call picks plaintext or TLS; NewWithTLS gives a Client custom CAs, a
// server name override and a client certificate for mTLS (package
// tlsconf), and TLS failures get specific codes and hints.
//
// A Client holds the connections. Invoke starts a call and returns a
// Stream that works for all four call kinds: Send and CloseSend write
// length-prefixed messages to the request body, Recv reads them from the
// response body, Header gives the response headers and, once Recv has
// returned an error, Trailer and Status give the trailers and the final
// gRPC status. Unary wraps the common request/response case.
//
// CallOptions.Compression "gzip" compresses the messages sent; gzip
// responses are always accepted. CallOptions.Protocol selects gRPC-Web
// (binary or base64 text framing) instead: a half-duplex call over
// HTTP/1.1, or HTTP/2 when TLS negotiates it, whose trailers come in a
// trailer frame at the end of the body.
//
// The package also has the wire helpers a server needs (AppendFrame,
// FrameReader, EncodeTimeout, EncodeStatusMessage), which the in-process
// test server in grpctest uses.
//
// See docs/grpc-transport.md.
package transport
