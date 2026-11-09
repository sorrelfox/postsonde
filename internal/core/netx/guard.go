package netx

import (
	"context"
	"errors"
	"net"
	"net/netip"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// DialCheck is what a Guard sees about a dial before it happens.
type DialCheck struct {
	// Network and Address are as passed to DialContext. Address may hold a
	// host name.
	Network string
	Address string
	// Resolved is the IP address and port about to be connected to. It is
	// valid only when the check runs after name resolution (Options.Guard
	// on Direct); Guarded checks run before it and leave it zero. With
	// remote DNS through a proxy, the target is never resolved locally.
	Resolved netip.AddrPort
}

// Guard decides whether a dial may happen. It returns nil to allow it and
// an error to refuse it, ideally from Deny. An error that is not an
// *errs.Error becomes a permission_denied error with the code
// netx.dial_denied. The app uses it to make sure PostSonde opens only
// connections the user asked for, and for offline mode and blocked hosts.
//
// A Guard must be cheap and safe for concurrent use.
type Guard func(ctx context.Context, c DialCheck) error

// AllowAll is the default Guard: it allows every dial.
func AllowAll(context.Context, DialCheck) error { return nil }

// Deny returns the error a Guard returns to refuse c: kind
// permission_denied, code netx.dial_denied, with hint as the hint (say why
// and what the user can change, e.g. "Offline mode is on.").
func Deny(c DialCheck, hint string) *errs.Error {
	return withAddr(errs.New(errs.PermissionDenied, "PostSonde did not connect to "+c.Address+" because it is blocked.").
		WithCode(CodeDialDenied).
		WithHint(hint), c.Network, c.Address)
}

// Guarded returns a Middleware that runs g before every dial and refuses
// the dial when g returns an error. Put it outermost so it sees the
// target as the protocol client asked for it, whatever route is taken:
//
//	netx.Chain(netx.Routed(route), netx.Guarded(policy))
//
// DialCheck.Resolved is zero here; use Options.Guard on Direct to check
// the resolved addresses too. A nil g allows everything.
func Guarded(g Guard) Middleware {
	return func(next Dialer) Dialer {
		if g == nil {
			return next
		}
		return DialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
			if err := check(ctx, g, DialCheck{Network: network, Address: address}); err != nil {
				return nil, err
			}
			return next.DialContext(ctx, network, address)
		})
	}
}

// check runs g and turns a refusal into an *errs.Error.
func check(ctx context.Context, g Guard, c DialCheck) error {
	err := g(ctx, c)
	if err == nil {
		return nil
	}
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e
	}
	return withAddr(errs.Wrap(err, errs.PermissionDenied, "PostSonde did not connect to "+c.Address+" because it is blocked.").
		WithCode(CodeDialDenied), c.Network, c.Address)
}
