package httpclient

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// Error codes of the errors Send returns. Dial errors keep their netx.*
// codes, and connection resets and TLS failures use the netx.* codes every
// protocol client shares (netx.Classify). See docs/http-client.md.
const (
	// The request is not valid; nothing was sent.
	CodeInvalidMethod         = "httpclient.invalid_method"
	CodeMethodUnsupported     = "httpclient.method_unsupported"
	CodeInvalidURL            = "httpclient.invalid_url"
	CodeSchemeUnsupported     = "httpclient.scheme_unsupported"
	CodeInvalidHeader         = "httpclient.invalid_header"
	CodeHeaderUnsupported     = "httpclient.header_unsupported"
	CodeContentLengthMismatch = "httpclient.content_length_mismatch"
	CodeInvalidSetting        = "httpclient.invalid_setting"
	CodeBodyUnavailable       = "httpclient.body_unavailable"

	// Sending or receiving failed.
	CodeCancelled             = "httpclient.cancelled"
	CodeTimeout               = "httpclient.timeout"
	CodeResponseHeaderTimeout = "httpclient.response_header_timeout"
	CodeConnectionTimeout     = "httpclient.connection_timeout"
	CodeTLSUnknownAuthority   = netx.CodeTLSUnknownAuthority
	CodeTLSHostnameMismatch   = netx.CodeTLSHostnameMismatch
	CodeTLSCertificateExpired = netx.CodeTLSCertificateExpired
	CodeTLSCertificateInvalid = netx.CodeTLSCertificateInvalid
	CodeTLSServerSentHTTP     = netx.CodeTLSServerNotTLS
	CodeTLSHandshakeFailed    = netx.CodeTLSHandshakeFailed
	CodeConnectionReset       = netx.CodeConnectionReset
	CodeConnectionClosed      = "httpclient.connection_closed"
	CodeConnectionFailed      = "httpclient.connection_failed"
	CodeMalformedResponse     = "httpclient.malformed_response"
	CodeTooManyRedirects      = "httpclient.too_many_redirects"
	CodeBodyIncomplete        = "httpclient.body_incomplete"
	CodeBodyReadFailed        = "httpclient.body_read_failed"
	CodeMalformedBody         = "httpclient.malformed_body"
)

// Hints of the errors Send returns.
const (
	HintTimeout             = "Increase the request timeout, or check that the server is responding."
	HintResponseHeader      = "The server accepted the request but did not answer in time. Increase the response header timeout."
	HintConnectionTimeout   = "Check that the server is responding, then try again."
	HintTLSUnknownAuthority = "If you trust this server, add the CA certificate that signed it in Settings, Network, Custom CA certificates, or turn off certificate verification in the request's Settings tab."
	HintTLSHostnameMismatch = "Check the host in the URL: the certificate is issued for other names."
	HintTLSExpired          = netx.HintTLSExpired
	HintTLSInvalid          = "Check the server's certificate, or turn off certificate verification in the request's Settings tab."
	HintTLSServerSentHTTP   = "The server answered in plain HTTP. Use an http:// URL, or check the port."
	HintTLSHandshake        = netx.HintTLSHandshake
	HintNotHTTP             = "Check that the server speaks HTTP on this port (use https:// for a TLS port)."
)

// stagePhrase completes "The request to HOST was cancelled …".
var stagePhrase = [...]string{
	stageBeforeSend: "before it was sent",
	stageConnecting: "while connecting",
	stageWaiting:    "while waiting for the response",
	stageBody:       "while reading the response body",
}

// mapError turns an error from sending to host at stage into an
// *errs.Error, keeping err as the cause and the stage as the "stage"
// detail. ctx is the send's own context, whose cause tells PostSonde's
// timeouts apart from the caller's cancellation. An error that already
// contains an *errs.Error (a dial error from netx, the redirect limit) is
// returned as that error.
func mapError(ctx context.Context, err error, st stage, host string) *errs.Error {
	wrap := func(kind errs.Kind, code, msg, hint string) *errs.Error {
		e := errs.Wrap(err, kind, msg).WithCode(code)
		if hint != "" {
			e = e.WithHint(hint)
		}
		return e.WithDetail("stage", st.String())
	}
	server := "The server at " + host

	if ctx.Err() != nil {
		switch cause := context.Cause(ctx); {
		case errors.Is(cause, errRequestTimeout):
			return wrap(errs.Timeout, CodeTimeout, "The request to "+host+" timed out "+stagePhrase[st]+".", HintTimeout)
		case errors.Is(cause, errHeaderTimeout):
			return wrap(errs.Timeout, CodeResponseHeaderTimeout, server+" did not send the response headers in time.", HintResponseHeader)
		}
	}
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || (ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded)) {
			return wrap(errs.Timeout, CodeTimeout, "The request to "+host+" timed out "+stagePhrase[st]+".", HintTimeout)
		}
		return wrap(errs.Cancelled, CodeCancelled, "The request to "+host+" was cancelled "+stagePhrase[st]+".", "")
	}

	if s, ok := ctx.Value(stateKey{}).(*sendState); ok && s.certRequested.Load() && st != stageBody {
		// The server asked for a client certificate and the send failed
		// before any response: a refused certificate (or none), whichever
		// way the TLS 1.3 alert raced with the first write and read.
		if e := netx.FailureError(netx.ClassifyClientCert(err, s.certSent.Load()), err, host); e != nil {
			return e.WithDetail("stage", st.String())
		}
	}
	if f := netx.Classify(err); f != netx.FailureNone {
		e := netx.ConnError(err, host)
		switch f {
		case netx.FailureTLSNotTLS:
			e = e.WithHint(HintTLSServerSentHTTP)
		case netx.FailureTLSUnknownAuthority:
			e = e.WithHint(HintTLSUnknownAuthority)
		case netx.FailureTLSHostnameMismatch:
			e = e.WithHint(HintTLSHostnameMismatch)
		case netx.FailureTLSInvalid:
			e = e.WithHint(HintTLSInvalid)
		case netx.FailureReset:
			e.Message = server + " reset the connection " + stagePhrase[st] + "."
		}
		return e.WithDetail("stage", st.String())
	}

	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return wrap(errs.Timeout, CodeConnectionTimeout, "The connection to "+host+" timed out "+stagePhrase[st]+".", HintConnectionTimeout)
	}
	eof := errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
	_, opErr := errors.AsType[*net.OpError](err)
	if st == stageBody {
		switch {
		case eof:
			return wrap(errs.Network, CodeBodyIncomplete, "The connection to "+host+" closed before the whole response body arrived.", "")
		case opErr:
			return wrap(errs.Network, CodeBodyReadFailed, "Reading the response body from "+host+" failed.", "")
		}
		return wrap(errs.Protocol, CodeMalformedBody, server+" sent a response body that is not valid HTTP.", "")
	}
	switch {
	case eof:
		return wrap(errs.Network, CodeConnectionClosed, server+" closed the connection without sending a response.", HintNotHTTP)
	case opErr:
		return wrap(errs.Network, CodeConnectionFailed, "The connection to "+host+" failed.", "")
	}
	return wrap(errs.Protocol, CodeMalformedResponse, server+" sent a response that is not valid HTTP.", HintNotHTTP)
}
