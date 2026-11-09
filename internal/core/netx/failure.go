package netx

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Failure is the class of a network failure, as Classify finds it. Every
// protocol client maps the same failure to the same netx.* code and hint,
// so the UI explains a DNS, connection or TLS problem the same way for
// HTTP, WebSocket, gRPC and TCP (DBG-09). See docs/network.md.
type Failure int

// Failure classes. FailureNone means err is not one of them: the protocol
// client maps it with its own codes (a malformed response, a timeout of
// its own, a cancellation).
const (
	FailureNone Failure = iota
	FailureDNSNotFound
	FailureDNSFailed
	FailureRefused
	FailureUnreachable
	FailureReset
	FailureTLSUnknownAuthority
	FailureTLSHostnameMismatch
	FailureTLSExpired
	FailureTLSInvalid
	FailureTLSNotTLS
	FailureTLSHandshake
	// FailureTLSClientCertRequired: the server asked for a client
	// certificate and got none (alert "certificate required").
	FailureTLSClientCertRequired
	// FailureTLSClientCertRejected: the server refused the client
	// certificate, or the lack of one (alerts "bad certificate",
	// "unknown certificate authority", ...; TLS 1.2 servers send these
	// for a missing certificate too).
	FailureTLSClientCertRejected
)

// Codes of the connection and TLS failures. Dial failures use the codes in
// errors.go (CodeDNSNotFound, CodeConnectionRefused, ...).
const (
	CodeConnectionReset       = "netx.connection_reset"
	CodeTLSUnknownAuthority   = "netx.tls_unknown_authority"
	CodeTLSHostnameMismatch   = "netx.tls_hostname_mismatch"
	CodeTLSCertificateExpired = "netx.tls_certificate_expired"
	CodeTLSCertificateInvalid = "netx.tls_certificate_invalid"
	CodeTLSServerNotTLS       = "netx.tls_server_not_tls"
	CodeTLSHandshakeFailed    = "netx.tls_handshake_failed"
	CodeTLSClientCertRequired = "netx.tls_client_cert_required"
	CodeTLSClientCertRejected = "netx.tls_client_cert_rejected"
)

// Hints of the connection and TLS failures.
const (
	HintConnectionReset       = "The server closed the connection abruptly. Check that it speaks this protocol on this port, and look at its logs."
	HintTLSUnknownAuthority   = "If you trust this server, add the CA certificate that signed it in Settings, Network, Custom CA certificates, or turn off certificate verification."
	HintTLSHostnameMismatch   = "Check the host name: the certificate is issued for other names."
	HintTLSExpired            = "Check the server's certificate, and this computer's date and time."
	HintTLSInvalid            = "Check the server's certificate, or turn off certificate verification."
	HintTLSServerNotTLS       = "The server does not use TLS on this port. Use a plain (non-TLS) URL, or check the port."
	HintTLSHandshake          = "Check that the server supports TLS 1.2 or newer on this port."
	HintTLSClientCertRequired = "The server requires a client certificate (mTLS). Add one for this host in Settings, Network, Client certificates."
	HintTLSClientCertRejected = "If the server requires a client certificate, add one for this host in Settings, Network, Client certificates. If one is set, check that the server trusts its CA, that it has not expired and that it is issued for client authentication."
)

// Classify returns the failure class of err, or FailureNone. It looks
// through the whole error tree (errors.As), so wrapped *url.Error,
// *net.OpError and *errs.Error values are classified by their cause.
// Timeouts and cancellations are not classified here: each caller knows
// best which deadline expired (see errs.KindOf).
func Classify(err error) Failure {
	if err == nil {
		return FailureNone
	}
	if de, ok := errors.AsType[*net.DNSError](err); ok && !de.IsTimeout {
		if de.IsNotFound {
			return FailureDNSNotFound
		}
		return FailureDNSFailed
	}
	if f := classifyTLS(err); f != FailureNone {
		return f
	}
	switch {
	case isErrno(err, refusedErrnos):
		return FailureRefused
	case isErrno(err, unreachableErrnos):
		return FailureUnreachable
	case IsReset(err):
		return FailureReset
	}
	return FailureNone
}

func classifyTLS(err error) Failure {
	switch RemoteAlert(err) {
	case "certificate required":
		return FailureTLSClientCertRequired
	case "bad certificate", "unsupported certificate", "revoked certificate", "expired certificate",
		"unknown certificate", "unknown certificate authority", "access denied":
		return FailureTLSClientCertRejected
	case "":
	default:
		return FailureTLSHandshake // any other alert the server sent
	}
	if _, ok := errors.AsType[tls.RecordHeaderError](err); ok || errors.Is(err, http.ErrSchemeMismatch) {
		return FailureTLSNotTLS
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return FailureTLSUnknownAuthority
	}
	if _, ok := errors.AsType[x509.HostnameError](err); ok {
		return FailureTLSHostnameMismatch
	}
	if ci, ok := errors.AsType[x509.CertificateInvalidError](err); ok {
		if ci.Reason == x509.Expired {
			return FailureTLSExpired
		}
		return FailureTLSInvalid
	}
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		// The macOS platform verifier (system roots) reports some
		// untrusted chains only as text.
		if strings.Contains(err.Error(), "certificate is not trusted") {
			return FailureTLSUnknownAuthority
		}
		return FailureTLSInvalid
	}
	if errs.KindOf(err) == errs.TLS {
		return FailureTLSHandshake
	}
	return FailureNone
}

// RemoteAlert returns the TLS alert the peer sent ("certificate
// required", "bad certificate"), or "". crypto/tls reports it as a
// *net.OpError with Op "remote error" around an unexported alert type, so
// only the text is available; some paths (the HTTP/2 client reading after
// the handshake) flatten the error into text.
func RemoteAlert(err error) string {
	if err == nil {
		return ""
	}
	if oe, ok := errors.AsType[*net.OpError](err); ok && oe.Op == "remote error" && oe.Err != nil {
		return strings.TrimPrefix(oe.Err.Error(), "tls: ")
	}
	if _, rest, ok := strings.Cut(err.Error(), "remote error: tls: "); ok {
		return rest
	}
	return ""
}

// ClassifyClientCert classifies err, a failure on a TLS connection whose
// server asked for a client certificate in the handshake, before any
// response arrived; sent says whether a certificate was sent. It returns
// FailureTLSClientCertRequired, FailureTLSClientCertRejected or
// FailureNone (err is something else: the caller classifies it as usual).
//
// Servers rarely say why they refuse: a TLS 1.2 server may answer with a
// plain "handshake failure", and with TLS 1.3 the client's side of the
// handshake is complete before the server checks the certificate, so its
// alert races with the client's first write and read. The client may see
// the alert, a broken pipe or reset (the server already closed), an EOF,
// or net/http's "client conn could not be established" (HTTP/2) or
// "server closed idle connection" (HTTP/1.1, a fresh connection closed
// before the request went out). Once the
// server asked for a certificate, each of these means it refused the
// certificate, or the lack of one. Timeouts are not classified.
func ClassifyClientCert(err error, sent bool) Failure {
	if err == nil {
		return FailureNone
	}
	refused := FailureTLSClientCertRequired
	if sent {
		refused = FailureTLSClientCertRejected
	}
	switch RemoteAlert(err) {
	case "":
	case "certificate required":
		return FailureTLSClientCertRequired
	case "bad certificate", "unsupported certificate", "revoked certificate", "expired certificate",
		"unknown certificate", "unknown certificate authority", "access denied", "handshake failure":
		return FailureTLSClientCertRejected
	default:
		return FailureNone
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return FailureNone
	}
	if IsReset(err) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		strings.Contains(err.Error(), "client conn could not be established") ||
		strings.Contains(err.Error(), "server closed idle connection") {
		return refused
	}
	return FailureNone
}

// FailureError is ConnError for a failure class the caller determined
// (ClassifyClientCert); nil for FailureNone.
func FailureError(f Failure, err error, address string) *errs.Error {
	info, ok := failures[f]
	if !ok {
		return nil
	}
	return errs.Wrap(err, info.kind, info.msg(address)).WithCode(info.code).WithHint(info.hint)
}

// IsReset reports a connection reset by the peer, an aborted connection
// or a broken pipe (writing to a connection the peer reset).
func IsReset(err error) bool {
	return isErrno(err, resetErrnos)
}

// IsTLS reports whether f is one of the TLS failures.
func (f Failure) IsTLS() bool {
	return f >= FailureTLSUnknownAuthority && f <= FailureTLSClientCertRejected
}

// failureInfo is what ConnError reports for a failure: kind, code, a
// message about address, and a hint.
type failureInfo struct {
	kind errs.Kind
	code string
	msg  func(address string) string
	hint string
}

var failures = map[Failure]failureInfo{
	FailureDNSNotFound: {errs.Network, CodeDNSNotFound, func(a string) string { return "The host name in " + a + " was not found." }, HintDNSNotFound},
	FailureDNSFailed:   {errs.Network, CodeDNSFailed, func(a string) string { return "The host name in " + a + " could not be looked up." }, HintDNSFailed},
	FailureRefused:     {errs.Network, CodeConnectionRefused, func(a string) string { return "The connection to " + a + " was refused." }, HintConnectionRefused},
	FailureUnreachable: {errs.Network, CodeUnreachable, func(a string) string { return a + " cannot be reached from this computer." }, HintUnreachable},
	FailureReset:       {errs.Network, CodeConnectionReset, func(a string) string { return "The server at " + a + " reset the connection." }, HintConnectionReset},
	FailureTLSUnknownAuthority: {errs.TLS, CodeTLSUnknownAuthority,
		func(a string) string { return "The certificate of " + a + " is not signed by a trusted authority." }, HintTLSUnknownAuthority},
	FailureTLSHostnameMismatch: {errs.TLS, CodeTLSHostnameMismatch,
		func(a string) string { return "The certificate of " + a + " is not valid for this host name." }, HintTLSHostnameMismatch},
	FailureTLSExpired: {errs.TLS, CodeTLSCertificateExpired,
		func(a string) string { return "The certificate of " + a + " has expired or is not valid yet." }, HintTLSExpired},
	FailureTLSInvalid: {errs.TLS, CodeTLSCertificateInvalid,
		func(a string) string { return "The certificate of " + a + " could not be verified." }, HintTLSInvalid},
	FailureTLSNotTLS: {errs.TLS, CodeTLSServerNotTLS,
		func(a string) string { return "The server at " + a + " did not answer with TLS." }, HintTLSServerNotTLS},
	FailureTLSHandshake: {errs.TLS, CodeTLSHandshakeFailed,
		func(a string) string { return "The secure connection (TLS) to " + a + " failed." }, HintTLSHandshake},
	FailureTLSClientCertRequired: {errs.TLS, CodeTLSClientCertRequired,
		func(a string) string { return "The server at " + a + " requires a client certificate." }, HintTLSClientCertRequired},
	FailureTLSClientCertRejected: {errs.TLS, CodeTLSClientCertRejected,
		func(a string) string {
			return "The server at " + a + " did not accept the client certificate, or the lack of one."
		}, HintTLSClientCertRejected},
}

// ConnError maps a failure on a connection to address (dialing, the TLS
// handshake, reading or writing) to an *errs.Error with the failure's
// kind, netx.* code, message and hint, keeping err as the cause. It
// returns nil when Classify finds no failure class, so the caller maps
// err itself. Callers may add details and replace the hint with a more
// specific one (the gRPC client names its TLS server name setting).
//
// An error that already contains an *errs.Error is not re-mapped: ConnError
// returns nil for it, and the caller keeps it.
func ConnError(err error, address string) *errs.Error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*errs.Error](err); ok {
		return nil
	}
	info, ok := failures[Classify(err)]
	if !ok {
		return nil
	}
	return errs.Wrap(err, info.kind, info.msg(address)).WithCode(info.code).WithHint(info.hint)
}
