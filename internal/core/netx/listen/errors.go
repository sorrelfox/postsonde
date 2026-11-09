package listen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"syscall"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes. See docs/network.md.
const (
	CodeInvalidHost          = "listen.invalid_host"
	CodeInvalidNetwork       = "listen.invalid_network"
	CodeInvalidPort          = "listen.invalid_port"
	CodeDualStackUnsupported = "listen.dual_stack_unsupported"
	CodeLANNotAllowed        = "listen.lan_not_allowed"
	CodeAddressInUse         = "listen.address_in_use"
	CodeAddressUnavailable   = "listen.address_unavailable"
	CodeBindDenied           = "listen.bind_denied"
	CodeCancelled            = "listen.cancelled"
	CodeTimeout              = "listen.timeout"
	CodeFailed               = "listen.failed"
)

// Hints of the errors Listen and ListenPacket return.
const (
	HintInvalidHost        = "Use 127.0.0.1 or localhost to listen on this computer only, or the IP address of one of its network interfaces."
	HintLANNotAllowed      = "Allow LAN access for this server to accept connections from other devices."
	HintAddressInUse       = "Another program is using this port. Pick another port, or 0 for any free one."
	HintAddressUnavailable = "Check that this IP address belongs to one of this computer's network interfaces."
	HintPrivilegedPort     = "Ports below 1024 may need administrator rights. Pick a port from 1024 to 65535."
	HintBindDenied         = "The operating system or a firewall refused to open this port."
)

func invalidHost(host string) *errs.Error {
	return errs.New(errs.InvalidArgument, fmt.Sprintf("%q is not an IP address or localhost.", host)).
		WithCode(CodeInvalidHost).WithHint(HintInvalidHost).WithDetail("host", host)
}

func familyMismatch(network, host string) *errs.Error {
	return errs.New(errs.InvalidArgument, fmt.Sprintf("%s cannot listen on %s.", network, host)).
		WithCode(CodeInvalidHost).WithDetail("host", host).WithDetail("network", network)
}

func invalidNetwork(network, want string) *errs.Error {
	return errs.New(errs.InvalidArgument, fmt.Sprintf("Cannot listen on network %q.", network)).
		WithCode(CodeInvalidNetwork).
		WithHint(fmt.Sprintf("Use %s, %s4 or %s6.", want, want, want)).
		WithDetail("network", network)
}

func invalidPort(port int) *errs.Error {
	return errs.New(errs.InvalidArgument, fmt.Sprintf("The port must be between 0 and 65535, not %d.", port)).
		WithCode(CodeInvalidPort).
		WithHint("Enter a port from 1 to 65535, or 0 for any free one.").
		WithDetail("port", strconv.Itoa(port))
}

func dualStackUnsupported() *errs.Error {
	return errs.New(errs.Unsupported, "A UDP listener cannot bind IPv4 and IPv6 loopback at once.").
		WithCode(CodeDualStackUnsupported)
}

func lanNotAllowed(network, address string, scope Scope) *errs.Error {
	msg := "Listening on " + address + " would let other devices connect."
	if scope == ScopeAllInterfaces {
		msg = "Listening on all network interfaces (" + address + ") would let other devices connect."
	}
	return errs.New(errs.PermissionDenied, msg).
		WithCode(CodeLANNotAllowed).WithHint(HintLANNotAllowed).
		WithDetail("network", network).WithDetail("address", address).WithDetail("scope", string(scope))
}

// wrapBindError turns an error from binding address into an *errs.Error,
// keeping err as the cause:
//
//	context.Canceled             cancelled          listen.cancelled
//	deadline                     timeout            listen.timeout
//	address in use               network            listen.address_in_use
//	address or family missing    network            listen.address_unavailable
//	access denied                permission_denied  listen.bind_denied
//	anything else                network            listen.failed
func wrapBindError(err error, network, address string, port int) *errs.Error {
	var e *errs.Error
	switch {
	case errors.Is(err, context.Canceled):
		e = errs.Wrap(err, errs.Cancelled, "Opening "+address+" was cancelled.").WithCode(CodeCancelled)
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded):
		e = errs.Wrap(err, errs.Timeout, "Opening "+address+" timed out.").WithCode(CodeTimeout)
	case isErrno(err, inUseErrnos):
		e = errs.Wrap(err, errs.Network, address+" is already in use.").
			WithCode(CodeAddressInUse).WithHint(HintAddressInUse)
	case isErrno(err, unavailableErrnos):
		e = errs.Wrap(err, errs.Network, "Cannot listen on "+address+": the address is not available on this computer.").
			WithCode(CodeAddressUnavailable).WithHint(HintAddressUnavailable)
	case isErrno(err, accessErrnos):
		hint := HintBindDenied
		if port > 0 && port < 1024 {
			hint = HintPrivilegedPort
		}
		e = errs.Wrap(err, errs.PermissionDenied, "Not allowed to listen on "+address+".").
			WithCode(CodeBindDenied).WithHint(hint)
	default:
		e = errs.Wrap(err, errs.Network, "Could not listen on "+address+".").WithCode(CodeFailed)
	}
	return e.WithDetail("network", network).WithDetail("address", address)
}

func isErrno(err error, set []syscall.Errno) bool {
	errno, ok := errors.AsType[syscall.Errno](err)
	return ok && slices.Contains(set, errno)
}
