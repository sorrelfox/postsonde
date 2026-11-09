package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// Error codes. Dial errors keep their netx.* codes. See
// docs/grpc-transport.md.
const (
	// The call is not valid; nothing was sent.
	CodeInvalidAddress   = "grpctransport.invalid_address"
	CodeInvalidMethod    = "grpctransport.invalid_method"
	CodeInvalidMetadata  = "grpctransport.invalid_metadata"
	CodeReservedMetadata = "grpctransport.reserved_metadata"
	CodeInvalidOption    = "grpctransport.invalid_option"
	CodeSendTooLarge     = "grpctransport.send_too_large"
	CodeSendClosed       = "grpctransport.send_closed"

	// The call ran and ended with a non-OK status.
	CodeStatus         = "grpctransport.status"          // grpc-status sent by the server
	CodeHTTPStatus     = "grpctransport.http_status"     // non-200 response without grpc-status
	CodeNotGRPC        = "grpctransport.not_grpc"        // 200 without an application/grpc content-type
	CodeMissingStatus  = "grpctransport.missing_status"  // the stream ended without grpc-status
	CodeRecvTooLarge   = "grpctransport.recv_too_large"  // a message above the receive limit
	CodeMalformedFrame = "grpctransport.malformed_frame" // truncated message or invalid flag
	CodeCompression    = "grpctransport.compression"     // compressed message that cannot be decompressed
	// CodeUnsupportedEncoding: a compressed message in a grpc-encoding
	// other than gzip (UNIMPLEMENTED); detail "encoding".
	CodeUnsupportedEncoding = "grpctransport.unsupported_encoding"
	CodeStreamReset         = "grpctransport.stream_reset" // RST_STREAM or GOAWAY from the server
	CodeCardinality         = "grpctransport.cardinality"  // a unary call got zero or several messages

	// The call failed locally.
	CodeCancelled        = "grpctransport.cancelled"
	CodeTimeout          = "grpctransport.deadline_exceeded"
	CodeConnectionFailed = "grpctransport.connection_failed"
	CodeTLSFailed        = netx.CodeTLSHandshakeFailed
	CodeNotHTTP2         = "grpctransport.not_http2" // the server does not speak HTTP/2 (ALPN or h2c preface)

	// TLS failures (GRPC-23), all kind tls and status UNAVAILABLE.
	// CodeTLSFailed covers the rest (a version or cipher mismatch, other
	// alerts). The server certificate failures and CodeTLSFailed are the
	// netx.* codes every protocol client shares (netx.Classify, DBG-09);
	// the client certificate ones are gRPC's own.
	CodeTLSUnknownAuthority   = netx.CodeTLSUnknownAuthority             // server certificate not signed by a trusted CA
	CodeTLSHostnameMismatch   = netx.CodeTLSHostnameMismatch             // server certificate not valid for the server name
	CodeTLSCertificateExpired = netx.CodeTLSCertificateExpired           // server certificate expired or not yet valid
	CodeTLSCertificateInvalid = netx.CodeTLSCertificateInvalid           // server certificate rejected for another reason
	CodeTLSClientCertRequired = "grpctransport.tls_client_cert_required" // the server wants a client certificate and got none
	CodeTLSClientCertRejected = "grpctransport.tls_client_cert_rejected" // the server refused the client certificate
	CodeTLSServerNotTLS       = netx.CodeTLSServerNotTLS                 // TLS was chosen, the server answered in plaintext

	// CodeConnectionReset: the server reset the connection (kind network,
	// status UNAVAILABLE), shared with the other protocol clients.
	CodeConnectionReset = netx.CodeConnectionReset
)

// Hints.
const (
	HintTimeout     = "Increase the call's deadline, or check that the server is responding."
	HintPlaintext   = "Check that the server speaks gRPC on this port, and whether it needs TLS (or plaintext)."
	HintTLS         = "Check the server's certificate, add its CA certificate, or turn off certificate verification."
	HintTooLarge    = "Increase the maximum receive message size."
	HintUnavailable = "Check that the server is running and reachable."
	HintWeb         = "Check that the address serves gRPC-Web (a gRPC-Web proxy such as Envoy, or a server with gRPC-Web support), or switch the transport to gRPC."

	HintUnsupportedEncoding = "Configure the server to answer with gzip or without compression; PostSonde advertises gzip in grpc-accept-encoding when request compression is gzip."

	HintPlaintextClosed       = "If the server uses TLS on this port, turn TLS on. Otherwise check that it speaks gRPC."
	HintTLSUnknownAuthority   = "Add the CA certificate that signed the server's certificate in Settings, Network, Custom CA certificates, or turn off certificate verification."
	HintTLSHostnameMismatch   = "Connect with a name the certificate is issued for, or set the TLS server name to one of them."
	HintTLSExpired            = "Check the server's certificate, and this computer's date and time."
	HintTLSClientCertRequired = netx.HintTLSClientCertRequired
	HintTLSClientCertRejected = "Check that the client certificate is signed by a CA the server trusts, is still valid, and is issued for client authentication (Settings, Network, Client certificates)."
	HintTLSServerNotTLS       = "The server does not use TLS on this port. Turn TLS off to use plaintext (h2c), or check the port."
)

// StatusFromError returns the gRPC status carried by err. Every error a
// Stream returns after the call started has one; for nil it is OK. Other
// errors give Unknown with the error text.
func StatusFromError(err error) Status {
	if err == nil {
		return Status{Code: OK}
	}
	if se, ok := errors.AsType[*StatusError](err); ok {
		return se.Status
	}
	return Status{Code: Unknown, Message: err.Error()}
}

// statusErr builds the error of a non-OK status: kind from the code, the
// given code, message and hint, a StatusError wrapping cause.
func statusErr(st Status, cause error, code, msg, hint string) *errs.Error {
	e := errs.Wrap(&StatusError{Status: st, Cause: cause}, kindForCode(st.Code), msg).
		WithCode(code).
		WithDetail("grpc_status", st.Code.String()).
		WithDetail("grpc_code", strconv.FormatUint(uint64(st.Code), 10))
	if hint != "" {
		e = e.WithHint(hint)
	}
	return e
}

// kindForCode picks the errs.Kind of a status. Only the codes that match
// a PostSonde kind closely are mapped; the rest are protocol errors (the
// server answered, with an error).
func kindForCode(c Code) errs.Kind {
	switch c {
	case Canceled:
		return errs.Cancelled
	case DeadlineExceeded:
		return errs.Timeout
	case Unavailable:
		return errs.Network
	}
	return errs.Protocol
}

// serverStatusErr is the error for a non-OK grpc-status from the server.
func serverStatusErr(st Status) *errs.Error {
	msg := "The server returned " + st.Code.String()
	if st.Message != "" {
		msg += ": " + st.Message
	} else {
		msg += "."
	}
	return statusErr(st, nil, CodeStatus, msg, "")
}

// h2StreamError has the shape of net/http's internal HTTP/2 StreamError,
// whose As method converts into any struct with the same fields.
type h2StreamError struct {
	StreamID uint32
	Code     h2ErrCode
	Cause    error
}

func (e h2StreamError) Error() string { return "http2 stream error" }

type h2ErrCode uint32

// HTTP/2 error codes (RFC 9113 section 7) that map to a specific status.
const (
	h2ErrCodeFlowControl        h2ErrCode = 0x3
	h2ErrCodeRefusedStream      h2ErrCode = 0x7
	h2ErrCodeCancel             h2ErrCode = 0x8
	h2ErrCodeEnhanceYourCalm    h2ErrCode = 0xb
	h2ErrCodeInadequateSecurity h2ErrCode = 0xc
)

// codeForH2Error maps an HTTP/2 RST_STREAM error code as grpc-go does.
func codeForH2Error(c h2ErrCode) Code {
	switch c {
	case h2ErrCodeRefusedStream:
		return Unavailable
	case h2ErrCodeCancel:
		return Canceled
	case h2ErrCodeFlowControl, h2ErrCodeEnhanceYourCalm:
		return ResourceExhausted
	case h2ErrCodeInadequateSecurity:
		return PermissionDenied
	}
	return Internal
}

// security describes how a call connects, for choosing error hints.
type security struct {
	tls           bool         // the call uses TLS
	clientCert    bool         // the Client has a client certificate
	certRequested *atomic.Bool // TLS calls: the server asked for a client certificate
	roundTrip     bool         // the error ended the round trip: no response arrived
}

func (s security) requested() bool { return s.certRequested != nil && s.certRequested.Load() }

// transportErr maps an error from the HTTP/2 round trip or the response
// body to an *errs.Error carrying a status. ctx is the call's context:
// once it is done, its error decides (DeadlineExceeded or Cancelled).
func transportErr(ctx context.Context, err error, address string, sec security) *errs.Error {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return statusErr(Status{Code: DeadlineExceeded, Message: "context deadline exceeded"}, ctx.Err(),
				CodeTimeout, "The call to "+address+" exceeded its deadline.", HintTimeout)
		}
		return statusErr(Status{Code: Canceled, Message: "context canceled"}, ctx.Err(),
			CodeCancelled, "The call to "+address+" was cancelled.", "")
	}
	if e, ok := errors.AsType[*errs.Error](err); ok {
		// A dial error from netx: keep its kind, code, message and hint.
		st := Status{Code: Unavailable, Message: e.Error()}
		switch e.Kind {
		case errs.Timeout:
			st.Code = DeadlineExceeded
		case errs.Cancelled:
			st.Code = Canceled
		case errs.PermissionDenied:
			st.Code = PermissionDenied
		}
		c := *e
		c.Err = &StatusError{Status: st, Cause: e.Err}
		return &c
	}
	var se h2StreamError
	if errors.As(err, &se) {
		st := Status{Code: codeForH2Error(se.Code), Message: err.Error()}
		return statusErr(st, err, CodeStreamReset, "The server reset the stream ("+err.Error()+").", "")
	}
	// Neither error has a type to match: the TLS alert is unexported and
	// the HTTP/2 framer's error is a plain string.
	if text := err.Error(); strings.Contains(text, "tls: no application protocol") || strings.Contains(text, "looked like an HTTP/1.1 header") {
		e := statusErr(Status{Code: Unavailable, Message: text}, err,
			CodeNotHTTP2, "The server at "+address+" does not speak HTTP/2, which gRPC needs.", HintPlaintext)
		if strings.Contains(text, "tls:") {
			e.Kind = errs.TLS
		}
		return e
	}
	if e := tlsErr(err, address, sec); e != nil {
		return e
	}
	st := Status{Code: Unavailable, Message: err.Error()}
	e := statusErr(st, err, CodeConnectionFailed, "The connection to "+address+" failed.", HintPlaintext)
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		e.Kind = errs.Timeout
		return e
	} else if netx.IsReset(err) {
		e.Code = CodeConnectionReset
		e.Message = "The server at " + address + " reset the connection."
		e.Hint = netx.HintConnectionReset
	} else if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		e.Message = "The server at " + address + " closed the connection."
	}
	if !sec.tls {
		// Dial errors never get here: the connection was open. A TLS
		// server drops a plaintext HTTP/2 preface without an answer (EOF,
		// a reset, or "client conn could not be established").
		e.Hint = HintPlaintextClosed
	}
	return e
}

// tlsErr maps TLS handshake and certificate failures, or returns nil.
func tlsErr(err error, address string, sec security) *errs.Error {
	mk := func(code, msg, hint string) *errs.Error {
		e := statusErr(Status{Code: Unavailable, Message: err.Error()}, err, code, msg, hint)
		e.Kind = errs.TLS
		return e
	}
	switch netx.Classify(err) {
	case netx.FailureTLSNotTLS:
		return mk(CodeTLSServerNotTLS, "The server at "+address+" did not answer with TLS.", HintTLSServerNotTLS)
	case netx.FailureTLSUnknownAuthority:
		return mk(CodeTLSUnknownAuthority, "The certificate of "+address+" is not signed by a trusted authority.", HintTLSUnknownAuthority)
	case netx.FailureTLSHostnameMismatch:
		return mk(CodeTLSHostnameMismatch, "The certificate of "+address+" is not valid for the server name.", HintTLSHostnameMismatch)
	case netx.FailureTLSExpired:
		return mk(CodeTLSCertificateExpired, "The certificate of "+address+" has expired or is not valid yet.", HintTLSExpired)
	case netx.FailureTLSInvalid:
		return mk(CodeTLSCertificateInvalid, "The certificate of "+address+" could not be verified.", HintTLS)
	}
	required := func() *errs.Error {
		return mk(CodeTLSClientCertRequired, "The server at "+address+" requires a client certificate.", HintTLSClientCertRequired)
	}
	rejected := func(why string) *errs.Error {
		return mk(CodeTLSClientCertRejected, "The server at "+address+" rejected the client certificate"+why+".", HintTLSClientCertRejected)
	}
	if alert := netx.RemoteAlert(err); alert != "" {
		switch alert {
		case "certificate required":
			return required()
		case "bad certificate", "unsupported certificate", "revoked certificate", "expired certificate",
			"unknown certificate", "unknown certificate authority", "handshake failure", "access denied":
			// Servers answer a missing or refused certificate with one of
			// these; "handshake failure" counts only when the server asked
			// for a certificate.
			if alert != "handshake failure" || sec.requested() {
				if !sec.clientCert {
					return required()
				}
				return rejected(" (" + alert + ")")
			}
		}
		return mk(CodeTLSFailed, "The secure connection (TLS) to "+address+" failed: the server sent \""+alert+"\".", HintTLS)
	}
	if ne, ok := errors.AsType[net.Error](err); sec.requested() && sec.roundTrip && !(ok && ne.Timeout()) {
		// TLS 1.3: the client's side of the handshake completed, the
		// server asked for a certificate, then dropped the connection
		// before any response. net/http reports that as "client conn could
		// not be established", a broken pipe or a reset, without the alert.
		if !sec.clientCert {
			return required()
		}
		return rejected("")
	}
	if errs.KindOf(err) == errs.TLS {
		return mk(CodeTLSFailed, "The secure connection (TLS) to "+address+" failed.", HintTLS)
	}
	return nil
}
