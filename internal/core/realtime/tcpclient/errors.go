package tcpclient

import (
	"errors"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// Error codes. Dial errors keep their netx.* codes. See docs/tcp.md.
const (
	// CodeInvalidAddress: the address is not host:port with a port from 1
	// to 65535 (invalid_argument). Details: address.
	CodeInvalidAddress = "tcpclient.invalid_address"
	// CodeNotOpen: Send or CloseWrite on a connection that is not open
	// (invalid_argument).
	CodeNotOpen = "tcpclient.not_open"
	// CodeWriteClosed: Send after CloseWrite (invalid_argument).
	CodeWriteClosed = "tcpclient.write_closed"
	// CodeHalfCloseUnsupported: CloseWrite on a connection the dialer
	// returned without half-close support (unsupported).
	CodeHalfCloseUnsupported = "tcpclient.half_close_unsupported"
	// CodeConnectionReset: the peer reset the connection (network). The
	// code every protocol client shares for a reset.
	CodeConnectionReset = netx.CodeConnectionReset
	// CodeReadFailed, CodeWriteFailed: other transport errors; the kind
	// comes from the cause (network, timeout, …).
	CodeReadFailed  = "tcpclient.read_failed"
	CodeWriteFailed = "tcpclient.write_failed"
)

// ParseAddress checks that address is host:port, with a host and a numeric
// port from 1 to 65535, and returns both. IPv6 hosts need brackets:
// [::1]:7000. The host is not resolved.
func ParseAddress(address string) (host, port string, err error) {
	invalid := func(msg, hint string) error {
		return errs.New(errs.InvalidArgument, msg).
			WithCode(CodeInvalidAddress).
			WithHint(hint).
			WithDetail("address", address)
	}
	trimmed := strings.TrimSpace(address)
	if trimmed == "" {
		return "", "", invalid("Enter an address to connect to.", "Write host:port, e.g. localhost:7000.")
	}
	if trimmed != address {
		return "", "", invalid("The address has spaces around it.", "Remove the spaces: write host:port, e.g. localhost:7000.")
	}
	host, port, splitErr := net.SplitHostPort(address)
	if splitErr != nil {
		var addrErr *net.AddrError
		switch {
		case errors.As(splitErr, &addrErr) && addrErr.Err == "missing port in address":
			return "", "", invalid("The address has no port.", "Add the port after a colon, e.g. localhost:7000.")
		case errors.As(splitErr, &addrErr) && addrErr.Err == "too many colons in address":
			return "", "", invalid("The address has too many colons.", "Put an IPv6 address in brackets, e.g. [::1]:7000.")
		default:
			return "", "", invalid("The address is not host:port.", "Write host:port, e.g. localhost:7000 or [::1]:7000.")
		}
	}
	if host == "" {
		return "", "", invalid("The address has no host.", "Write the host before the port, e.g. localhost:7000.")
	}
	n, convErr := strconv.Atoi(port)
	if convErr != nil || n < 1 || n > 65535 || port[0] == '+' {
		return "", "", invalid("The port must be a number from 1 to 65535.", "Write host:port, e.g. localhost:7000.")
	}
	return host, port, nil
}

func notOpen() *errs.Error {
	return errs.New(errs.InvalidArgument, "The TCP connection is not open.").
		WithCode(CodeNotOpen).
		WithHint("Connect first.")
}

// readError maps an error of reading an open connection; c.mu is held.
// io.EOF is not an error (the peer closed its side) and never reaches it.
// A TLS alert is mapped like a handshake failure: with TLS 1.3 the
// server's verdict on the client certificate arrives after the handshake,
// before any data.
func (c *Conn) readError(err error) *errs.Error {
	address := c.o.Address
	if c.certRequested && c.stats.BytesReceived == 0 {
		if e := netx.FailureError(netx.ClassifyClientCert(err, c.certSent), err, address); e != nil {
			return e.WithDetail("address", address)
		}
	}
	if netx.Classify(err).IsTLS() {
		if e := netx.ConnError(err, address); e != nil {
			return e.WithDetail("address", address)
		}
	}
	if isReset(err) {
		return errs.Wrap(err, errs.Network, "The server reset the connection.").
			WithCode(CodeConnectionReset).
			WithHint("The server closed the connection abruptly, e.g. because it crashed or rejected what was sent.").
			WithDetail("address", address)
	}
	return errs.Wrap(err, transportKind(err), "Reading from the TCP connection failed.").
		WithCode(CodeReadFailed).
		WithDetail("address", address)
}

func writeError(err error, address string) *errs.Error {
	if isReset(err) {
		return errs.Wrap(err, errs.Network, "The server reset the connection.").
			WithCode(CodeConnectionReset).
			WithHint("The server closed the connection before the data was sent.").
			WithDetail("address", address)
	}
	return errs.Wrap(err, transportKind(err), "Sending on the TCP connection failed.").
		WithCode(CodeWriteFailed).
		WithDetail("address", address)
}

// transportKind is the kind of a transport error: timeout or cancelled
// when the cause says so, network otherwise.
func transportKind(err error) errs.Kind {
	if k := errs.KindOf(err); k == errs.Timeout || k == errs.Cancelled {
		return k
	}
	return errs.Network
}

// isReset reports a connection reset, an aborted connection or a broken
// pipe (netx.IsReset, which knows the Windows error numbers too).
func isReset(err error) bool {
	return netx.IsReset(err)
}

// isEOF reports the peer's orderly close.
func isEOF(err error) bool {
	return errors.Is(err, io.EOF)
}
