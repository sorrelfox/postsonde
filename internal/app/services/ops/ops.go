// Package ops is the bound service shared by every long-running operation:
// the UI cancels any operation by its ID here, whatever domain started it.
// See docs/events.md.
package ops

import (
	"context"
	"log/slog"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
)

// shutdownTimeout bounds how long app shutdown waits for operations to stop.
const shutdownTimeout = 5 * time.Second

// OpsService cancels running operations. Domain services start them (and
// return their ID); this one service stops them, so the UI needs a single
// cancel call and no domain reimplements it.
type OpsService struct {
	ops    *operation.Registry
	logger *slog.Logger
}

// New returns an OpsService for the shared registry. A nil logger means
// slog.Default().
func New(ops *operation.Registry, logger *slog.Logger) *OpsService {
	if logger == nil {
		logger = slog.Default()
	}
	return &OpsService{ops: ops, logger: logger.With("service", "ops")}
}

// ServiceName names the service in Wails logs.
func (s *OpsService) ServiceName() string { return "ops" }

// ServiceShutdown cancels every running operation when the app quits and
// waits briefly for them to finish.
func (s *OpsService) ServiceShutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return s.ops.Shutdown(ctx)
}

// Cancel cancels the operation opID. It reports whether the operation was
// running; false means it had already finished or never existed, which the
// UI can ignore. A cancelled operation still ends with its done event,
// status "cancelled".
func (s *OpsService) Cancel(_ context.Context, opID string) bool {
	return s.ops.Cancel(opID)
}
