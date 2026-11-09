package netx

import (
	"context"
	"errors"
	"net"
	"os"
	"slices"
	"syscall"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes of the errors dialers return. See docs/network.md.
const (
	CodeConnectionRefused = "netx.connection_refused"
	CodeDNSNotFound       = "netx.dns_not_found"
	CodeDNSFailed         = "netx.dns_failed"
	CodeTimeout           = "netx.dial_timeout"
	CodeCancelled         = "netx.dial_cancelled"
	CodeUnreachable       = "netx.unreachable"
	CodeDialFailed        = "netx.dial_failed"
	CodeDialDenied        = "netx.dial_denied"
	CodeNoRoute           = "netx.no_route"
)

// Hints of the errors dialers return.
const (
	HintConnectionRefused = "Is the server running on that host and port?"
	HintDNSNotFound       = "Check the host name."
	HintDNSFailed         = "Check your network connection and DNS settings."
	HintTimeout           = "Check that the host is reachable, or increase the connect timeout."
	HintUnreachable       = "Check the address and your network connection."
)

// WrapDialError turns an error from dialing address into an *errs.Error
// with a kind, code and hint, keeping err as the cause so errors.Is and
// errors.As still reach *net.OpError, *net.DNSError and the context errors:
//
//	context.Canceled              cancelled  netx.dial_cancelled
//	deadline or net timeout       timeout    netx.dial_timeout
//	*net.DNSError, not found      network    netx.dns_not_found
//	*net.DNSError, other          network    netx.dns_failed
//	connection refused            network    netx.connection_refused
//	host or network unreachable   network    netx.unreachable
//	connection reset or aborted   network    netx.connection_reset
//	TLS failure (a dialer that    tls        netx.tls_* (see Classify)
//	  does TLS itself)
//	anything else                 network    netx.dial_failed
//
// An error that already contains an *errs.Error (a Guard's refusal, an
// inner dialer's mapped error) returns that *errs.Error unchanged.
// WrapDialError(nil, ...) is nil. Proxy dialers use it too.
func WrapDialError(err error, network, address string) error {
	if err == nil {
		return nil
	}
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e
	}
	var e *errs.Error
	switch f := Classify(err); {
	case errors.Is(err, context.Canceled):
		e = errs.Wrap(err, errs.Cancelled, "Connecting to "+address+" was cancelled.").
			WithCode(CodeCancelled)
	case isTimeout(err):
		e = errs.Wrap(err, errs.Timeout, "Connecting to "+address+" timed out.").
			WithCode(CodeTimeout).WithHint(HintTimeout)
	case f != FailureNone:
		e = ConnError(err, address)
	default:
		e = errs.Wrap(err, errs.Network, "Could not connect to "+address+".").
			WithCode(CodeDialFailed)
	}
	return withAddr(e, network, address)
}

func withAddr(e *errs.Error, network, address string) *errs.Error {
	return e.WithDetail("network", network).WithDetail("address", address)
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	ne, ok := errors.AsType[net.Error](err)
	return ok && ne.Timeout()
}

func isErrno(err error, set []syscall.Errno) bool {
	errno, ok := errors.AsType[syscall.Errno](err)
	return ok && slices.Contains(set, errno)
}
