package sioclient

import (
	"context"
	"errors"
	"net"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// Error codes. WebSocket failures keep their wsclient.* and wsframe.*
// codes, dial and TLS failures their netx.* codes, and packets the server
// sends that cannot be decoded their engineio.* and socketio.* codes. See
// docs/socketio.md.
const (
	// Options are not valid; nothing was sent (kind invalid_argument).
	CodeInvalidURL        = "sioclient.invalid_url"
	CodeSchemeUnsupported = "sioclient.scheme_unsupported"
	CodeInvalidPath       = "sioclient.invalid_path"
	CodeInvalidNamespace  = "sioclient.invalid_namespace"
	CodeInvalidAuth       = "sioclient.invalid_auth"
	CodeInvalidHeader     = "sioclient.invalid_header"
	CodeInvalidTransport  = "sioclient.invalid_transport"
	CodeInvalidVersion    = "sioclient.invalid_version"

	// Using the Client in the wrong state (kind invalid_argument).
	CodeAlreadyConnected = "sioclient.already_connected"
	CodeNotOpen          = "sioclient.not_open"

	// Connecting failed.
	CodeCancelled = "sioclient.cancelled" // kind cancelled
	CodeTimeout   = "sioclient.timeout"   // kind timeout
	// CodeHandshakeRejected: the long-polling handshake was answered with
	// a status other than 200 (kind protocol). Details: status, body.
	CodeHandshakeRejected = "sioclient.handshake_rejected"
	// CodeInvalidHandshake: the first Engine.IO packet is not an open
	// packet (kind protocol).
	CodeInvalidHandshake = "sioclient.invalid_handshake"
	// CodeVersionMismatch: the server answered the handshake in the
	// protocol of another Socket.IO version: an Engine.IO 3 payload to a
	// v3/v4 client (kind protocol). Details: version, the server's.
	CodeVersionMismatch = "sioclient.version_mismatch"
	// CodeConnectError: the server refused the namespace with a
	// CONNECT_ERROR packet, typically a middleware rejecting the auth
	// payload (kind permission_denied). Details: namespace, data.
	CodeConnectError = "sioclient.connect_error"

	// The connection failed after it opened.
	// CodePingTimeout: no ping arrived within pingInterval + pingTimeout
	// (kind timeout).
	CodePingTimeout = "sioclient.ping_timeout"
	// CodeTransportError: a long-polling request failed or was answered
	// with a status other than 200 (kind network or protocol). Details:
	// status.
	CodeTransportError = "sioclient.transport_error"
	// CodeUpgradeFailed: the WebSocket probe of an upgrade failed; the
	// connection stays on long-polling (kind protocol). Only reported
	// through a TraceUpgradeFailed trace.
	CodeUpgradeFailed = "sioclient.upgrade_failed"
)

func invalid(code, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(code)
}

// errHandshakeTimeout is the cause of the connect context when
// Options.HandshakeTimeout expires.
var errHandshakeTimeout = errors.New("sioclient: handshake timeout")

// connectError maps err, a failure while connecting, to an *errs.Error.
// ctx is the connect context, whose cause tells the handshake timeout
// apart from the caller's cancellation.
func connectError(ctx context.Context, err error, host string) error {
	if ctx.Err() != nil {
		if errors.Is(context.Cause(ctx), errHandshakeTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errs.Wrap(err, errs.Timeout, "Connecting to the Socket.IO server at "+host+" timed out.").
				WithCode(CodeTimeout).
				WithHint("Check that the server is responding, or increase the connect timeout.").
				WithDetail("host", host)
		}
		return errs.Wrap(err, errs.Cancelled, "Connecting to "+host+" was cancelled.").WithCode(CodeCancelled)
	}
	return transportError(err, host)
}

// transportError keeps coded errors and classifies the others.
func transportError(err error, host string) error {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e
	}
	if f := netx.Classify(err); f != netx.FailureNone {
		return netx.ConnError(err, host).WithDetail("host", host)
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return errs.Wrap(err, errs.Timeout, "The connection to "+host+" timed out.").WithCode(CodeTransportError)
	}
	return errs.Wrap(err, errs.Network, "The connection to "+host+" failed.").WithCode(CodeTransportError).WithDetail("host", host)
}
