package netx

import (
	"context"
	"log/slog"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// CodeNotUserInitiated is the code of the error RequireUserOp returns for a
// dial whose context carries no user operation (PRV-05).
const CodeNotUserInitiated = "netx.not_user_initiated"

// HintNotUserInitiated is the hint of that error. The refusal is a bug in
// PostSonde, never something the user did, so the hint says so.
const HintNotUserInitiated = "PostSonde only connects when you send a request, run a runner or sign in. This connection was not started by you, so it was blocked; please report it."

// UserOp describes the user action a dial belongs to: sending a request,
// connecting a TCP or WebSocket client, a runner, an OAuth flow, schema
// introspection. It travels in the dial's context (WithUserOp).
type UserOp struct {
	// Kind names the action, "<domain>.<kind>", e.g. "http.send",
	// "grpc.call", "tcp.connect". Required: an op without a Kind does not
	// count as user-initiated.
	Kind string
	// ID is the operation or connection ID the UI follows, when there is
	// one.
	ID string
}

type userOpKey struct{}

// WithUserOp returns a copy of ctx marked as belonging to the user action
// op. Only code that runs because the user asked for it may call it: the
// operations registry marks every operation it starts, and services whose
// connections are not operations (TCP, WebSocket) mark their connect
// contexts. A later WithUserOp replaces the mark.
func WithUserOp(ctx context.Context, op UserOp) context.Context {
	return context.WithValue(ctx, userOpKey{}, op)
}

// UserOpFrom returns the user operation ctx belongs to, if any.
func UserOpFrom(ctx context.Context) (UserOp, bool) {
	op, ok := ctx.Value(userOpKey{}).(UserOp)
	if !ok || op.Kind == "" {
		return UserOp{}, false
	}
	return op, true
}

// RequireUserOp returns a Guard that allows a dial only when its context
// carries a user operation (WithUserOp), so PostSonde opens no connection
// the user did not ask for (PRV-05). A refused dial is logged at warn level
// with the target, and fails with kind permission_denied and code
// netx.not_user_initiated. Allowed dials are logged at debug level with the
// operation. A nil logger means slog.Default().
//
// Use it outermost, with Guarded:
//
//	netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(logger)))
func RequireUserOp(logger *slog.Logger) Guard {
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "netx")
	return func(ctx context.Context, c DialCheck) error {
		op, ok := UserOpFrom(ctx)
		if !ok {
			logger.WarnContext(ctx, "dial refused: not started by a user operation", "network", c.Network, "address", c.Address)
			return withAddr(errs.New(errs.PermissionDenied, "PostSonde did not connect to "+c.Address+" because no action of yours started the connection.").
				WithCode(CodeNotUserInitiated).
				WithHint(HintNotUserInitiated), c.Network, c.Address)
		}
		logger.DebugContext(ctx, "dial allowed", "network", c.Network, "address", c.Address, "op", op.Kind, "opId", op.ID)
		return nil
	}
}
