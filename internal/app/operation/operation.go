// Package operation runs long-running operations for the bound services and
// streams their results to the UI as events.
//
// A service starts an operation with Start and returns its StartedDTO at
// once; the work runs in its own goroutine. What the work sends goes through
// a stream.Batcher, so the UI gets at most one data event and one progress
// event per interval however fast the work produces, followed by exactly one
// done event. The UI cancels by ID through OpsService.Cancel
// (internal/app/services/ops). Conventions: docs/events.md.
//
// The package does not import Wails: events leave through the Emit function
// the app layer passes to NewRegistry.
package operation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/opid"
	"github.com/sorrelfox/postsonde/internal/core/stream"
)

// Event name phases. A domain's events are <domain>:op:<phase>.
const (
	PhaseData     = "data"
	PhaseProgress = "progress"
	PhaseDone     = "done"
)

// EventName returns the name of a domain's event for phase, e.g.
// EventName("demo", PhaseData) == "demo:op:data". Services declare their
// names as constants (application.RegisterEvent needs constants); this
// function builds the same names at run time.
func EventName(domain, phase string) string {
	return domain + ":op:" + phase
}

// Status is the outcome of an operation, reported in its done event.
type Status string

// Operation outcomes.
const (
	StatusOK        Status = "ok"
	StatusError     Status = "error"
	StatusCancelled Status = "cancelled"
)

// Emit sends one event to the UI. The app layer implements it with the Wails
// event manager; tests record the calls.
type Emit func(name string, data any)

// Spec describes an operation to Start.
type Spec struct {
	// Domain names the service; events are <Domain>:op:<phase>. Required.
	Domain string
	// Kind names the operation within the domain, for logs ("stream").
	Kind string
	// Interval and MaxItems tune batching; zero means stream.DefaultInterval
	// and stream.DefaultMaxItems. See docs/events.md for choosing them.
	Interval time.Duration
	MaxItems int
}

// Func is the work of an operation. It runs in its own goroutine and must
// return promptly once ctx is cancelled. What it returns decides the status:
// nil is "ok", an error is "error" — unless ctx was cancelled, which is
// always "cancelled".
type Func[T any] func(ctx context.Context, emit *Emitter[T]) error

// Registry tracks running operations so they can be cancelled by ID. One
// Registry is shared by all services.
type Registry struct {
	emit      Emit
	logger    *slog.Logger
	now       func() time.Time
	newTicker stream.TickerFunc // nil: real ticker

	base context.Context // parent of every operation; cancelled by Shutdown
	stop context.CancelFunc

	mu  sync.Mutex
	ops map[string]context.CancelFunc
	wg  sync.WaitGroup
}

// NewRegistry returns a Registry that sends events through emit. A nil
// logger means slog.Default().
func NewRegistry(emit Emit, logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	base, stop := context.WithCancel(context.Background())
	return &Registry{
		emit:   emit,
		logger: logger.With("component", "operation"),
		now:    time.Now,
		base:   base,
		stop:   stop,
		ops:    map[string]context.CancelFunc{},
	}
}

// ErrShutdown is returned by Start after Shutdown.
var ErrShutdown = errors.New("operation: registry is shut down")

// Start runs fn as a new operation and returns its ID at once.
//
// fn runs in a new goroutine with a context that is cancelled by Cancel or
// Shutdown, not by the context of the bound call that started it: that call
// returns before the work is done. When fn returns, pending items are
// flushed and the done event is sent.
//
// The context is marked as a user operation (netx.WithUserOp, kind
// "<Domain>.<Kind>" and the operation ID): operations are started by the
// user, and the app's dialer refuses connections without that mark
// (PRV-05, docs/network.md). Pass ctx on to every dial.
func Start[T any](r *Registry, spec Spec, fn Func[T]) (StartedDTO, error) {
	if spec.Domain == "" {
		return StartedDTO{}, errors.New("operation: Spec.Domain is required")
	}
	id := opid.New()

	r.mu.Lock()
	if r.base.Err() != nil {
		r.mu.Unlock()
		return StartedDTO{}, ErrShutdown
	}
	ctx, cancel := context.WithCancel(netx.WithUserOp(r.base, userOp(spec, id)))
	r.ops[id] = cancel
	r.wg.Add(1)
	r.mu.Unlock()

	o := &op[T]{
		r:            r,
		id:           id,
		spec:         spec,
		dataName:     EventName(spec.Domain, PhaseData),
		progressName: EventName(spec.Domain, PhaseProgress),
		doneName:     EventName(spec.Domain, PhaseDone),
		logger:       r.logger.With("opId", id, "domain", spec.Domain, "kind", spec.Kind),
	}
	// The batcher is closed explicitly after fn returns, not by ctx, so
	// items sent while fn winds down after a cancel still reach the UI
	// before the done event.
	o.batcher = stream.New(context.WithoutCancel(ctx), stream.Options[message[T]]{
		Interval:  spec.Interval,
		MaxItems:  spec.MaxItems,
		Coalesce:  coalesceProgress[T],
		NewTicker: r.newTicker,
	}, o.flush)

	go o.run(ctx, cancel, fn)
	return StartedDTO{OpID: id}, nil
}

// userOp is the user-operation mark of an operation.
func userOp(spec Spec, id string) netx.UserOp {
	kind := spec.Domain
	if spec.Kind != "" {
		kind += "." + spec.Kind
	}
	return netx.UserOp{Kind: kind, ID: id}
}

// Cancel cancels the running operation id. It reports whether id was
// running; an unknown or finished operation returns false. The operation
// still sends its done event (status "cancelled") once its work returns.
func (r *Registry) Cancel(id string) bool {
	r.mu.Lock()
	cancel, ok := r.ops[id]
	r.mu.Unlock()
	if ok {
		cancel()
		r.logger.Info("operation cancel requested", "opId", id)
	}
	return ok
}

// Running returns the number of operations that have not finished.
func (r *Registry) Running() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ops)
}

// Shutdown cancels every operation, refuses new ones and waits until all
// have sent their done event or ctx ends.
func (r *Registry) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	r.stop()
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("operation: shutdown: %w", ctx.Err())
	}
}

func (r *Registry) remove(id string) {
	r.mu.Lock()
	delete(r.ops, id)
	r.mu.Unlock()
}

// message is what goes through an operation's batcher: an item or, when
// progress is set, a progress update.
type message[T any] struct {
	item     T
	progress *progress
}

type progress struct {
	done, total int64
	message     string
}

// coalesceProgress keeps only the latest progress update per batch.
func coalesceProgress[T any](m message[T]) (string, bool) {
	return "progress", m.progress != nil
}

// Emitter is what an operation's Func sends through. It is safe for
// concurrent use.
type Emitter[T any] struct {
	batcher *stream.Batcher[message[T]]
}

// Send queues items for the next data event. It never blocks on the UI and
// never drops an item. It returns stream.ErrClosed if the operation has
// already finished (a goroutine that outlived its Func).
func (e *Emitter[T]) Send(items ...T) error {
	for _, it := range items {
		if err := e.batcher.Add(message[T]{item: it}); err != nil {
			return err
		}
	}
	return nil
}

// Progress records the operation's progress. Only the latest update in each
// interval is sent. total is 0 when unknown.
func (e *Emitter[T]) Progress(done, total int64, msg string) error {
	return e.batcher.Add(message[T]{progress: &progress{done: done, total: total, message: msg}})
}

// op is one running operation.
type op[T any] struct {
	r       *Registry
	id      string
	spec    Spec
	batcher *stream.Batcher[message[T]]
	logger  *slog.Logger

	dataName, progressName, doneName string

	// Written only by the batcher's flush goroutine and then, after
	// batcher.Close has returned, by run.
	seq     int64
	items   int64
	batches int64
}

func (o *op[T]) flush(batch []message[T]) {
	items := make([]T, 0, len(batch))
	var last *progress
	for _, m := range batch {
		if m.progress != nil {
			last = m.progress
		} else {
			items = append(items, m.item)
		}
	}
	if len(items) > 0 {
		o.seq++
		o.items += int64(len(items))
		o.batches++
		o.r.emit(o.dataName, DataEventDTO[T]{OpID: o.id, Seq: o.seq, Items: items})
	}
	if last != nil {
		o.seq++
		o.r.emit(o.progressName, ProgressEventDTO{
			OpID: o.id, Seq: o.seq, Done: last.done, Total: last.total, Message: last.message,
		})
	}
}

func (o *op[T]) run(ctx context.Context, cancel context.CancelFunc, fn Func[T]) {
	defer o.r.wg.Done()
	defer cancel()

	start := o.r.now()
	o.logger.Debug("operation started")
	err := callSafely(ctx, fn, &Emitter[T]{batcher: o.batcher})
	o.batcher.Close() // flushes the rest; after this, flush is not called again

	done := DoneEventDTO{OpID: o.id, Status: StatusOK}
	switch {
	case ctx.Err() != nil:
		done.Status = StatusCancelled
	case err != nil:
		done.Status = StatusError
		dto := apperr.ToDTO(err)
		done.Error = &dto
	}
	// Remove before the done event, so a Cancel the UI sends after seeing
	// it returns false.
	o.r.remove(o.id)

	o.seq++
	done.Seq = o.seq
	done.Items = o.items
	done.Batches = o.batches
	done.ElapsedMs = o.r.now().Sub(start).Milliseconds()
	o.r.emit(o.doneName, done)
	o.logger.Info("operation finished", "status", done.Status, "items", done.Items,
		"batches", done.Batches, "elapsedMs", done.ElapsedMs, "error", err)
}

// callSafely runs fn and turns a panic into an error, so a bug in one
// operation ends it with status "error" instead of crashing the app.
func callSafely[T any](ctx context.Context, fn Func[T], e *Emitter[T]) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("operation panicked: %v", p)
		}
	}()
	return fn(ctx, e)
}
