// Package demo is the reference streaming operation: it produces
// items at a fixed rate so the UI can see batching, throttling and
// cancellation at work. It is the template for real streaming operations
// (responses, sockets, gRPC streams, local servers); see docs/events.md.
package demo

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
)

// Limits for Stream.
const (
	MaxRatePerSec = 100_000
	MaxDurationMs = 60_000
)

// streamInterval is the flush interval for the demo stream: 20 data events
// a second. It is the default, spelled out because the demo shows it off.
const streamInterval = 50 * time.Millisecond

// produceEvery is how often the producer wakes to send the items that are
// due. It is much shorter than streamInterval, so items reach the batcher
// one by one rather than already batched.
const produceEvery = time.Millisecond

// DemoService runs demo operations.
type DemoService struct {
	ops    *operation.Registry
	logger *slog.Logger
}

// New returns a DemoService that starts operations in ops. A nil logger
// means slog.Default().
func New(ops *operation.Registry, logger *slog.Logger) *DemoService {
	if logger == nil {
		logger = slog.Default()
	}
	return &DemoService{ops: ops, logger: logger.With("service", Domain)}
}

// ServiceName names the service in Wails logs.
func (s *DemoService) ServiceName() string { return Domain }

// Stream starts an operation that sends ratePerSec items a second for
// durationMs milliseconds, then finishes with status "ok". It returns the
// operation ID at once; the items arrive as demo:op:data events, batched
// every 50 ms, with demo:op:progress after each batch and a final
// demo:op:done. Cancel it with OpsService.Cancel.
func (s *DemoService) Stream(ctx context.Context, ratePerSec int, durationMs int) (operation.StartedDTO, error) {
	if ratePerSec < 1 || ratePerSec > MaxRatePerSec {
		return operation.StartedDTO{}, fmt.Errorf("stream: ratePerSec must be between 1 and %d, got %d", MaxRatePerSec, ratePerSec)
	}
	if durationMs < 1 || durationMs > MaxDurationMs {
		return operation.StartedDTO{}, fmt.Errorf("stream: durationMs must be between 1 and %d, got %d", MaxDurationMs, durationMs)
	}
	if err := ctx.Err(); err != nil {
		return operation.StartedDTO{}, err
	}

	total := int64(ratePerSec) * int64(durationMs) / 1000
	spec := operation.Spec{Domain: Domain, Kind: "stream", Interval: streamInterval}
	return operation.Start(s.ops, spec, func(ctx context.Context, emit *operation.Emitter[ItemDTO]) error {
		return produce(ctx, emit, ratePerSec, total)
	})
}

// produce sends total items at ratePerSec, spread evenly over time.
func produce(ctx context.Context, emit *operation.Emitter[ItemDTO], ratePerSec int, total int64) error {
	start := time.Now()
	ticker := time.NewTicker(produceEvery)
	defer ticker.Stop()

	var sent int64
	for sent < total {
		select {
		case <-ctx.Done():
			return fmt.Errorf("stream cancelled after %d of %d items: %w", sent, total, ctx.Err())
		case now := <-ticker.C:
			due := min(total, int64(now.Sub(start))*int64(ratePerSec)/int64(time.Second))
			for ; sent < due; sent++ {
				if err := emit.Send(ItemDTO{Index: sent, Text: fmt.Sprintf("item %d", sent)}); err != nil {
					return err
				}
			}
			if err := emit.Progress(sent, total, ""); err != nil {
				return err
			}
		}
	}
	return nil
}
