package udpclient

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"syscall"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/tcpclient"
)

// Error codes. Dial errors keep their netx.* codes. See docs/udp.md.
const (
	// CodeInvalidAddress: the address is not host:port with a port from 1
	// to 65535 (invalid_argument). Details: address.
	CodeInvalidAddress = "udpclient.invalid_address"
	// CodeInvalidOptions: a network or maximum datagram size Open cannot
	// use (invalid_argument). Details: field, value.
	CodeInvalidOptions = "udpclient.invalid_options"
	// CodeFamilyMismatch: an IP address of the other version than the
	// network asks for, e.g. ::1 with "udp4" (invalid_argument). Details:
	// address, network.
	CodeFamilyMismatch = "udpclient.family_mismatch"
	// CodeNotOpen: Send on a connection that is not open
	// (invalid_argument).
	CodeNotOpen = "udpclient.not_open"
	// CodeDatagramTooLarge: Send of a datagram over the limit: the IP
	// limit (65,507 bytes over IPv4, 65,527 over IPv6), less the SOCKS
	// header through a proxy, or Options.MaxDatagramSize; or one the
	// operating system refused as too long (invalid_argument). Details:
	// size, max.
	CodeDatagramTooLarge = "udpclient.datagram_too_large"
	// CodePortUnreachable: an ICMP "port unreachable" a connected socket
	// got for an earlier datagram, reported as a note (network): nothing
	// listens there. The socket stays open.
	CodePortUnreachable = "udpclient.port_unreachable"
	// CodeReadFailed, CodeWriteFailed: other socket errors; the kind
	// comes from the cause.
	CodeReadFailed  = "udpclient.read_failed"
	CodeWriteFailed = "udpclient.write_failed"
)

// ParseAddress checks that address is host:port, with a host and a numeric
// port from 1 to 65535, and returns both, as tcpclient.ParseAddress does
// (IPv6 hosts in brackets: [::1]:5353). The host is not resolved.
func ParseAddress(address string) (host, port string, err error) {
	host, port, err = tcpclient.ParseAddress(address)
	if err != nil {
		if e, ok := errors.AsType[*errs.Error](err); ok {
			ne := errs.New(e.Kind, e.Message).WithCode(CodeInvalidAddress).WithHint(udpHint(e.Hint)).WithDetail("address", address)
			return "", "", ne
		}
		return "", "", err
	}
	return host, port, nil
}

// udpHint swaps the TCP example port for a UDP one.
func udpHint(h string) string { return strings.ReplaceAll(h, "7000", "5353") }

// checkFamily refuses an IP literal of the other version than network.
func checkFamily(network, host, address string) error {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return nil // a name: resolved with the family asked for
	}
	ip = ip.Unmap()
	if (network == "udp4" && !ip.Is4()) || (network == "udp6" && ip.Is4()) {
		version := "IPv4"
		if network == "udp6" {
			version = "IPv6"
		}
		return errs.New(errs.InvalidArgument, "The address "+host+" is not an "+version+" address.").
			WithCode(CodeFamilyMismatch).
			WithHint("Choose IP version Any, or write an "+version+" address.").
			WithDetail("address", address).
			WithDetail("network", network)
	}
	return nil
}

func invalidOptions(field, value, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).
		WithCode(CodeInvalidOptions).
		WithDetail("field", field).
		WithDetail("value", value)
}

func notOpen() *errs.Error {
	return errs.New(errs.InvalidArgument, "The UDP socket is not open.").
		WithCode(CodeNotOpen).
		WithHint("Open it first.")
}

func tooLarge(size, max int, bySetting bool) *errs.Error {
	hint := "Send a shorter datagram: UDP over IPv4 carries at most 65,507 bytes (65,527 over IPv6), less the SOCKS header through a proxy."
	if bySetting {
		hint = "Send a shorter datagram, or raise the maximum datagram size of the request."
	}
	return errs.New(errs.InvalidArgument, "The datagram is "+itoa(size)+" bytes; at most "+itoa(max)+" can be sent.").
		WithCode(CodeDatagramTooLarge).
		WithHint(hint).
		WithDetail("size", itoa(size)).
		WithDetail("max", itoa(max))
}

func refused(err error, address string) *errs.Error {
	return errs.Wrap(err, errs.Network, "Nothing listens on "+address+": the host answered “port unreachable” for an earlier datagram.").
		WithCode(CodePortUnreachable).
		WithHint("Is the server running on that host and port? The socket stays open.").
		WithDetail("address", address)
}

func writeError(err error, address string, size, max int) error {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e // netx.socks_udp_association_closed, …
	}
	if errors.Is(err, syscall.EMSGSIZE) {
		return tooLarge(size, max, false)
	}
	if netx.Classify(err) == netx.FailureRefused {
		return refused(err, address)
	}
	if e := netx.ConnError(err, address); e != nil {
		return e.WithDetail("address", address)
	}
	return errs.Wrap(err, transportKind(err), "Sending the datagram failed.").
		WithCode(CodeWriteFailed).
		WithDetail("address", address)
}

func readError(err error, address string) error {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e
	}
	if errors.Is(err, net.ErrClosed) {
		return errs.Wrap(err, errs.Network, "The UDP socket was closed.").WithCode(CodeReadFailed).WithDetail("address", address)
	}
	return errs.Wrap(err, transportKind(err), "Reading from the UDP socket failed.").
		WithCode(CodeReadFailed).
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

func itoa(n int) string { return strconv.Itoa(n) }
