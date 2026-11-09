package stream

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeTicker is a TickerFunc whose ticks the test sends by hand.
type fakeTicker struct {
	ch      chan time.Time
	stopped chan struct{}
}

func newFakeTicker() *fakeTicker {
	// Unbuffered: a send returns only once the batcher's loop has taken
	// the tick.
	return &fakeTicker{ch: make(chan time.Time), stopped: make(chan struct{})}
}

func (f *fakeTicker) start(time.Duration) (<-chan time.Time, func()) {
	return f.ch, func() { close(f.stopped) }
}

// tick fires one tick and waits until the flush it causes has finished. The
// loop handles one event at a time and calls flush synchronously, so once it
// accepts a second tick the first one has been fully processed. The second
// tick finds nothing new pending (the test adds nothing in between) and
// flushes nothing.
func (f *fakeTicker) tick(t *testing.T) {
	t.Helper()
	for range 2 {
		select {
		case f.ch <- time.Time{}:
		case <-time.After(5 * time.Second):
			t.Fatal("batcher did not take the tick")
		}
	}
}

// recorder collects flushed batches.
type recorder[T any] struct {
	mu      sync.Mutex
	batches [][]T
}

func (r *recorder[T]) flush(batch []T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, batch)
}

func (r *recorder[T]) get() [][]T {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.batches)
}

// step is one action in a scripted batcher test.
type step struct {
	add    []int // values to Add, in order
	tick   bool  // fire a tick and wait for its flush
	cancel bool  // cancel the batcher's context and wait until it stops
	close  bool  // Close the batcher
	// want is the complete list of batches expected after this step, or nil
	// to skip the check. Checks after a size-triggered flush are avoided:
	// that flush runs asynchronously.
	want [][]int
}

// progress marks negative values as "latest wins" under one key.
func progress(v int) (string, bool) { return "progress", v < 0 }

func TestBatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		maxItems int
		coalesce func(int) (string, bool)
		steps    []step
		// wantAfter is the complete list of batches after the script and a
		// final Close.
		wantAfter [][]int
	}{
		{
			name:     "flush by time",
			maxItems: 10,
			steps: []step{
				{add: []int{1, 2, 3}},
				{tick: true, want: [][]int{{1, 2, 3}}},
				{add: []int{4, 5}},
				{tick: true, want: [][]int{{1, 2, 3}, {4, 5}}},
			},
			wantAfter: [][]int{{1, 2, 3}, {4, 5}},
		},
		{
			name:     "empty tick flushes nothing",
			maxItems: 10,
			steps: []step{
				{tick: true, want: [][]int{}},
				{tick: true, want: [][]int{}},
			},
			wantAfter: [][]int{},
		},
		{
			name:     "flush by size before the tick",
			maxItems: 3,
			steps: []step{
				// Two full batches go out without a tick; 7 waits for Close.
				{add: []int{1, 2, 3, 4, 5, 6, 7}},
			},
			wantAfter: [][]int{{1, 2, 3}, {4, 5, 6}, {7}},
		},
		{
			name:     "tick splits a large backlog into MaxItems batches",
			maxItems: 2,
			steps: []step{
				{add: []int{1, 2, 3, 4, 5}},
				{tick: true, want: [][]int{{1, 2}, {3, 4}, {5}}},
			},
			wantAfter: [][]int{{1, 2}, {3, 4}, {5}},
		},
		{
			name:      "close flushes the rest",
			maxItems:  10,
			steps:     []step{{add: []int{1, 2}}},
			wantAfter: [][]int{{1, 2}},
		},
		{
			name:     "close after a flush",
			maxItems: 10,
			steps: []step{
				{add: []int{1}},
				{tick: true, want: [][]int{{1}}},
				{add: []int{2, 3}},
				{close: true, want: [][]int{{1}, {2, 3}}},
			},
			wantAfter: [][]int{{1}, {2, 3}},
		},
		{
			name:      "cancelled context flushes the rest",
			maxItems:  10,
			steps:     []step{{add: []int{1, 2}}, {cancel: true, want: [][]int{{1, 2}}}},
			wantAfter: [][]int{{1, 2}},
		},
		{
			name:     "coalesce keeps the latest progress in its first slot",
			maxItems: 10,
			coalesce: progress,
			steps: []step{
				{add: []int{1, -1, 2, -2, 3, -3}},
				{tick: true, want: [][]int{{1, -3, 2, 3}}},
				{add: []int{-4, 4, -5}},
				{tick: true, want: [][]int{{1, -3, 2, 3}, {-5, 4}}},
			},
			wantAfter: [][]int{{1, -3, 2, 3}, {-5, 4}},
		},
		{
			name:     "coalesced values do not count toward MaxItems",
			maxItems: 3,
			coalesce: progress,
			steps: []step{
				{add: []int{-1, -2, -3, -4, 1}},
				{tick: true, want: [][]int{{-4, 1}}},
			},
			wantAfter: [][]int{{-4, 1}},
		},
		{
			name:     "coalesce key is forgotten after a size flush",
			maxItems: 2,
			coalesce: progress,
			steps: []step{
				// [-1 1] is flushed by size; -2 must start a new slot, not
				// overwrite a slot that was already flushed.
				{add: []int{-1, 1, -2}},
			},
			wantAfter: [][]int{{-1, 1}, {-2}},
		},
		{
			name:     "without coalesce negative values are kept",
			maxItems: 10,
			steps: []step{
				{add: []int{-1, -2}},
				{tick: true, want: [][]int{{-1, -2}}},
			},
			wantAfter: [][]int{{-1, -2}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ticker := newFakeTicker()
			var rec recorder[int]
			b := New(ctx, Options[int]{
				Interval:  time.Hour, // only fake ticks
				MaxItems:  tt.maxItems,
				Coalesce:  tt.coalesce,
				NewTicker: ticker.start,
			}, rec.flush)

			for i, s := range tt.steps {
				for _, v := range s.add {
					if err := b.Add(v); err != nil {
						t.Fatalf("step %d: Add(%d): %v", i, v, err)
					}
				}
				if s.tick {
					ticker.tick(t)
				}
				if s.cancel {
					cancel()
					<-b.Done()
				}
				if s.close {
					b.Close()
				}
				if s.want != nil {
					assertBatches(t, rec.get(), s.want)
				}
			}

			b.Close()
			assertBatches(t, rec.get(), tt.wantAfter)
			select {
			case <-ticker.stopped:
			default:
				t.Error("ticker not stopped after Close")
			}
		})
	}
}

func assertBatches(t *testing.T, got, want [][]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("batches = %v, want %v", got, want)
	}
	for i := range got {
		if !slices.Equal(got[i], want[i]) {
			t.Fatalf("batches = %v, want %v", got, want)
		}
	}
}

func TestBatcherAddAfterClose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		stop func(b *Batcher[int], cancel context.CancelFunc)
	}{
		{"closed", func(b *Batcher[int], _ context.CancelFunc) { b.Close() }},
		{"closed twice", func(b *Batcher[int], _ context.CancelFunc) { b.Close(); b.Close() }},
		{"context cancelled", func(b *Batcher[int], cancel context.CancelFunc) { cancel(); <-b.Done() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var rec recorder[int]
			b := New(ctx, Options[int]{NewTicker: newFakeTicker().start}, rec.flush)
			tt.stop(b, cancel)

			if err := b.Add(1); !errors.Is(err, ErrClosed) {
				t.Errorf("Add after stop: err = %v, want ErrClosed", err)
			}
			b.Close() // still safe
			if got := rec.get(); len(got) != 0 {
				t.Errorf("flushed %v, want nothing", got)
			}
		})
	}
}

func TestBatcherDefaults(t *testing.T) {
	t.Parallel()

	var gotInterval time.Duration
	ticker := newFakeTicker()
	var rec recorder[int]
	b := New(t.Context(), Options[int]{NewTicker: func(d time.Duration) (<-chan time.Time, func()) {
		gotInterval = d
		return ticker.start(d)
	}}, rec.flush)

	if gotInterval != DefaultInterval {
		t.Errorf("interval = %v, want %v", gotInterval, DefaultInterval)
	}
	for i := range DefaultMaxItems + 1 {
		if err := b.Add(i); err != nil {
			t.Fatal(err)
		}
	}
	b.Close()
	batches := rec.get()
	if len(batches) != 2 || len(batches[0]) != DefaultMaxItems || len(batches[1]) != 1 {
		t.Errorf("batch sizes = %v, want [%d 1]", sizes(batches), DefaultMaxItems)
	}
}

// TestBatcherConcurrent adds from many goroutines against a real ticker and
// checks that nothing is lost or duplicated, that batches respect MaxItems
// and that each goroutine's values stay in order.
func TestBatcherConcurrent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		workers  int
		each     int
		maxItems int
		interval time.Duration
	}{
		{"many small batches", 8, 2000, 16, time.Millisecond},
		{"size bound dominates", 4, 5000, 64, time.Hour},
		{"time bound dominates", 16, 200, 100_000, 2 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			type item struct{ worker, n int }
			var rec recorder[item]
			b := New(t.Context(), Options[item]{Interval: tt.interval, MaxItems: tt.maxItems}, rec.flush)

			var wg sync.WaitGroup
			for w := range tt.workers {
				wg.Go(func() {
					for n := range tt.each {
						if err := b.Add(item{w, n}); err != nil {
							t.Errorf("Add: %v", err)
							return
						}
					}
				})
			}
			wg.Wait()
			b.Close()

			next := make([]int, tt.workers)
			total := 0
			for _, batch := range rec.get() {
				if len(batch) == 0 || len(batch) > tt.maxItems {
					t.Fatalf("batch of %d values, want 1..%d", len(batch), tt.maxItems)
				}
				for _, it := range batch {
					if it.n != next[it.worker] {
						t.Fatalf("worker %d: got value %d, want %d (lost, duplicated or reordered)", it.worker, it.n, next[it.worker])
					}
					next[it.worker]++
					total++
				}
			}
			if want := tt.workers * tt.each; total != want {
				t.Errorf("flushed %d values, want %d", total, want)
			}
		})
	}
}

func sizes[T any](batches [][]T) []int {
	out := make([]int, len(batches))
	for i, b := range batches {
		out[i] = len(b)
	}
	return out
}
