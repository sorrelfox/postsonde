// Package stream batches fast streams of values so they reach a slow
// consumer (the UI) as a few large updates instead of many small ones.
//
// A Batcher collects values and hands them to a flush function every
// Interval, or as soon as MaxItems are pending, whichever comes first. It
// never drops a value: every value accepted by Add is flushed exactly once,
// in the order it was added, before Close returns. Values that only matter
// as "the latest one" (progress) can be coalesced so a batch carries at most
// one per key. See docs/events.md for how the app layer uses it.
package stream

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Defaults for Options. 50 ms is 20 UI updates a second, which looks live
// and leaves the UI thread idle between updates; 1000 values caps the size
// of one event payload.
const (
	DefaultInterval = 50 * time.Millisecond
	DefaultMaxItems = 1000
)

// ErrClosed is returned by Add after the batcher has been closed or its
// context cancelled.
var ErrClosed = errors.New("stream: batcher is closed")

// TickerFunc starts a ticker that fires every d. It returns the tick channel
// and a function that stops the ticker. Tests pass a fake to control time.
type TickerFunc func(d time.Duration) (ticks <-chan time.Time, stop func())

// Options configure a Batcher. The zero value uses the defaults.
type Options[T any] struct {
	// Interval is how often pending values are flushed, and so the longest
	// a value waits. Zero means DefaultInterval.
	Interval time.Duration
	// MaxItems flushes as soon as this many values are pending, before the
	// next tick. It is also the largest batch passed to flush. Zero means
	// DefaultMaxItems.
	MaxItems int
	// Coalesce, if set, marks values where only the latest matters: it
	// returns a key and true for them. A value whose key is already pending
	// replaces the pending one in place, so a batch holds at most one value
	// per key. Values for which it returns false are never coalesced.
	Coalesce func(v T) (key string, ok bool)
	// NewTicker replaces the real ticker (time.NewTicker). For tests.
	NewTicker TickerFunc
}

// Batcher collects values and flushes them in batches from one goroutine.
// Add is safe for concurrent use.
type Batcher[T any] struct {
	flush    func([]T)
	maxItems int
	coalesce func(T) (string, bool)

	mu      sync.Mutex
	ready   [][]T          // full batches waiting for the flush goroutine
	pending []T            // the batch being filled, len < maxItems
	keys    map[string]int // coalesce key -> index in pending
	closed  bool

	wake      chan struct{} // MaxItems reached; capacity 1
	closing   chan struct{} // closed by Close
	done      chan struct{} // closed when the flush goroutine has finished
	closeOnce sync.Once
}

// New starts a Batcher that passes batches to flush. flush is called from a
// single goroutine, one batch at a time, in order; it receives a new slice
// each time and may keep it. flush must not call Close.
//
// Cancelling ctx closes the batcher the same way Close does: pending values
// are flushed and later Adds fail with ErrClosed.
func New[T any](ctx context.Context, opts Options[T], flush func(batch []T)) *Batcher[T] {
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	maxItems := opts.MaxItems
	if maxItems <= 0 {
		maxItems = DefaultMaxItems
	}
	newTicker := opts.NewTicker
	if newTicker == nil {
		newTicker = realTicker
	}

	b := &Batcher[T]{
		flush:    flush,
		maxItems: maxItems,
		coalesce: opts.Coalesce,
		keys:     map[string]int{},
		wake:     make(chan struct{}, 1),
		closing:  make(chan struct{}),
		done:     make(chan struct{}),
	}
	ticks, stop := newTicker(interval)
	go b.run(ctx, ticks, stop)
	return b
}

func realTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// Add queues v for the next flush. It never blocks on the flush function
// and never drops v. It returns ErrClosed once the batcher is closed.
func (b *Batcher[T]) Add(v T) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrClosed
	}
	if b.coalesce != nil {
		if key, ok := b.coalesce(v); ok {
			if i, pending := b.keys[key]; pending {
				b.pending[i] = v
				b.mu.Unlock()
				return nil
			}
			b.keys[key] = len(b.pending)
		}
	}
	b.pending = append(b.pending, v)
	full := len(b.pending) >= b.maxItems
	if full {
		// Seal the batch now, so later values (coalesced or not) go into
		// the next one whenever the flush goroutine gets to it.
		b.ready = append(b.ready, b.pending)
		b.pending = nil
		clear(b.keys)
	}
	b.mu.Unlock()

	if full {
		select {
		case b.wake <- struct{}{}:
		default: // a wake-up is already queued
		}
	}
	return nil
}

// Close stops the batcher: it rejects further Adds, flushes every pending
// value and returns once the last flush has returned. It is safe to call
// more than once and from several goroutines.
func (b *Batcher[T]) Close() {
	b.closeOnce.Do(func() {
		b.markClosed()
		close(b.closing)
	})
	<-b.done
}

// Done is closed once the batcher has stopped and flushed everything, after
// Close or after its context was cancelled.
func (b *Batcher[T]) Done() <-chan struct{} { return b.done }

func (b *Batcher[T]) markClosed() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
}

func (b *Batcher[T]) run(ctx context.Context, ticks <-chan time.Time, stop func()) {
	defer close(b.done)
	defer stop()
	for {
		select {
		case <-ticks:
			b.flushPending(false)
		case <-b.wake:
			b.flushPending(true)
		case <-ctx.Done():
			b.markClosed()
			b.flushPending(false)
			return
		case <-b.closing:
			b.flushPending(false)
			return
		}
	}
}

// flushPending flushes the full batches and, unless fullOnly, the partly
// filled one. A size-triggered flush uses fullOnly so it does not also send
// a tiny batch that the next tick would have filled.
func (b *Batcher[T]) flushPending(fullOnly bool) {
	b.mu.Lock()
	batches := b.ready
	b.ready = nil
	if !fullOnly && len(b.pending) > 0 {
		batches = append(batches, b.pending)
		b.pending = nil
		clear(b.keys)
	}
	b.mu.Unlock()

	for _, batch := range batches {
		b.flush(batch)
	}
}
