package wsclient

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// Error codes. Dial errors keep their netx.* codes and frame errors their
// wsframe.* codes; connection resets and TLS failures use the netx.* codes
// every protocol client shares (netx.Classify). See docs/websocket.md.
const (
	// Options are not valid; nothing was sent.
	CodeInvalidURL         = "wsclient.invalid_url"
	CodeSchemeUnsupported  = "wsclient.scheme_unsupported"
	CodeInvalidHeader      = "wsclient.invalid_header"
	CodeReservedHeader     = "wsclient.reserved_header"
	CodeInvalidSubprotocol = "wsclient.invalid_subprotocol"
	CodeInvalidOption      = "wsclient.invalid_option"

	// Using the Client in the wrong state.
	CodeAlreadyConnected = "wsclient.already_connected"
	CodeNotOpen          = "wsclient.not_open"

	// Connecting failed.
	CodeCancelled             = "wsclient.cancelled"
	CodeTimeout               = "wsclient.timeout"
	CodeTLSUnknownAuthority   = netx.CodeTLSUnknownAuthority
	CodeTLSHostnameMismatch   = netx.CodeTLSHostnameMismatch
	CodeTLSCertificateExpired = netx.CodeTLSCertificateExpired
	CodeTLSCertificateInvalid = netx.CodeTLSCertificateInvalid
	CodeTLSServerNotTLS       = netx.CodeTLSServerNotTLS
	CodeTLSHandshakeFailed    = netx.CodeTLSHandshakeFailed
	CodeConnectionReset       = netx.CodeConnectionReset
	CodeConnectionClosed      = "wsclient.connection_closed"
	CodeConnectionFailed      = "wsclient.connection_failed"
	CodeMalformedResponse     = "wsclient.malformed_response"

	// The server answered, but not with a valid upgrade.
	CodeUpgradeRejected        = "wsclient.upgrade_rejected"
	CodeInvalidUpgrade         = "wsclient.invalid_upgrade"
	CodeInvalidAccept          = "wsclient.invalid_accept"
	CodeUnrequestedSubprotocol = "wsclient.unrequested_subprotocol"
	CodeUnrequestedExtension   = "wsclient.unrequested_extension"

	// Closing.
	CodeCloseTimeout = "wsclient.close_timeout"

	// No pong within Options.PongTimeout of a keepalive ping (WS-09).
	CodePongTimeout = "wsclient.pong_timeout"
)

// Hints.
const (
	HintUpgradeRejected = "The server did not accept the WebSocket upgrade. Check the URL path, headers, cookies and authentication; the response is shown."
	HintNotWebSocket    = "Check that this URL is a WebSocket endpoint."
	HintReservedHeader  = "PostSonde sets this header for the handshake. Remove the row, or allow overriding handshake headers."
	HintNotHTTP         = "Check that the server speaks HTTP on this port (use wss:// for a TLS port)."
	HintServerNotTLS    = "The server answered without TLS. Use a ws:// URL, or check the port."
)

func invalid(code, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(code)
}

func handshakeFailure(code, host, msg, hint string) *errs.Error {
	e := errs.New(errs.Protocol, msg).WithCode(code).WithDetail("host", host)
	if hint != "" {
		e = e.WithHint(hint)
	}
	return e
}

// errHandshakeTimeout is the cause of the handshake context when
// Options.HandshakeTimeout expires.
var errHandshakeTimeout = errors.New("wsclient: handshake timeout")

// mapConnectError turns an error of dialing, the TLS handshake or the
// upgrade exchange into an *errs.Error. ctx is the handshake's own context,
// whose cause tells the handshake timeout apart from the caller's
// cancellation.
func mapConnectError(ctx context.Context, err error, host string) *errs.Error {
	wrap := func(kind errs.Kind, code, msg, hint string) *errs.Error {
		e := errs.Wrap(err, kind, msg).WithCode(code).WithDetail("host", host)
		if hint != "" {
			e = e.WithHint(hint)
		}
		return e
	}
	if ctx.Err() != nil {
		if errors.Is(context.Cause(ctx), errHandshakeTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return wrap(errs.Timeout, CodeTimeout, "The WebSocket connection to "+host+" timed out.",
				"Check that the server is responding, or increase the handshake timeout.")
		}
		return wrap(errs.Cancelled, CodeCancelled, "Connecting to "+host+" was cancelled.", "")
	}
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e
	}
	if f := netx.Classify(err); f != netx.FailureNone {
		e := netx.ConnError(err, host).WithDetail("host", host)
		if f == netx.FailureTLSNotTLS {
			e = e.WithHint(HintServerNotTLS)
		}
		return e
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return wrap(errs.Timeout, CodeTimeout, "The WebSocket connection to "+host+" timed out.", "")
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return wrap(errs.Network, CodeConnectionClosed, "The server at "+host+" closed the connection without answering the WebSocket upgrade.", HintNotHTTP)
	}
	return wrap(errs.Network, CodeConnectionFailed, "The connection to "+host+" failed.", "")
}
