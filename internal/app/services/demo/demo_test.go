package demo

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
)

// capture records the demo's events and signals the done event.
type capture struct {
	mu       sync.Mutex
	data     []operation.DataEventDTO[ItemDTO]
	progress []operation.ProgressEventDTO
	names    []string
	seqs     []int64
	done     chan operation.DoneEventDTO
	first    chan struct{} // closed on the first data event
	once     sync.Once
}

func newCapture() *capture {
	return &capture{done: make(chan operation.DoneEventDTO, 1), first: make(chan struct{})}
}

func (c *capture) emit(name string, data any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.names = append(c.names, name)
	switch d := data.(type) {
	case operation.DataEventDTO[ItemDTO]:
		c.data = append(c.data, d)
		c.seqs = append(c.seqs, d.Seq)
		c.once.Do(func() { close(c.first) })
	case operation.ProgressEventDTO:
		c.progress = append(c.progress, d)
		c.seqs = append(c.seqs, d.Seq)
	case operation.DoneEventDTO:
		c.seqs = append(c.seqs, d.Seq)
		c.done <- d
	}
}

func (c *capture) waitDone(t *testing.T) operation.DoneEventDTO {
	t.Helper()
	select {
	case d := <-c.done:
		return d
	case <-time.After(20 * time.Second):
		t.Fatal("no done event")
		return operation.DoneEventDTO{}
	}
}

func TestEventNames(t *testing.T) {
	t.Parallel()

	for name, phase := range map[string]string{
		EventData:     operation.PhaseData,
		EventProgress: operation.PhaseProgress,
		EventDone:     operation.PhaseDone,
	} {
		if want := operation.EventName(Domain, phase); name != want {
			t.Errorf("event constant %q, want %q", name, want)
		}
	}
}

func TestStreamInvalid(t *testing.T) {
	t.Parallel()

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	tests := []struct {
		name       string
		ctx        context.Context
		rate, ms   int
		wantCancel bool
	}{
		{name: "zero rate", ctx: t.Context(), rate: 0, ms: 100},
		{name: "rate too high", ctx: t.Context(), rate: MaxRatePerSec + 1, ms: 100},
		{name: "zero duration", ctx: t.Context(), rate: 10, ms: 0},
		{name: "duration too long", ctx: t.Context(), rate: 10, ms: MaxDurationMs + 1},
		{name: "cancelled call", ctx: cancelled, rate: 10, ms: 100, wantCancel: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := operation.NewRegistry(newCapture().emit, nil)
			_, err := New(reg, nil).Stream(tt.ctx, tt.rate, tt.ms)
			if err == nil {
				t.Fatal("Stream succeeded, want an error")
			}
			if tt.wantCancel != errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want context.Canceled: %v", err, tt.wantCancel)
			}
			if reg.Running() != 0 {
				t.Error("an invalid call started an operation")
			}
		})
	}
}

// TestStreamThrottles runs the demo at 2000 items/s for 500 ms: every item
// must arrive, in order, in about 10 batches (one per 50 ms), not 1000.
func TestStreamThrottles(t *testing.T) {
	t.Parallel()

	c := newCapture()
	reg := operation.NewRegistry(c.emit, nil)
	started, err := New(reg, nil).Stream(t.Context(), 2000, 500)
	if err != nil {
		t.Fatal(err)
	}
	done := c.waitDone(t)

	c.mu.Lock()
	defer c.mu.Unlock()
	if done.OpID != started.OpID || done.Status != operation.StatusOK {
		t.Fatalf("done = %+v, want ok for %s", done, started.OpID)
	}
	var next int64
	for _, batch := range c.data {
		for _, it := range batch.Items {
			if it.Index != next {
				t.Fatalf("item %d arrived where %d was expected", it.Index, next)
			}
			next++
		}
	}
	if next != 1000 || done.Items != 1000 {
		t.Errorf("received %d items (done says %d), want 1000", next, done.Items)
	}
	// 500 ms / 50 ms = 10, plus the final flush; generous for -race.
	if n := len(c.data); n < 2 || n > 20 || done.Batches != int64(n) {
		t.Errorf("%d data events (done says %d), want about 10", n, done.Batches)
	}
	if len(c.progress) == 0 || c.progress[len(c.progress)-1].Done != 1000 || c.progress[0].Total != 1000 {
		t.Errorf("progress events %+v, want the last at 1000/1000", c.progress)
	}
	for i, s := range c.seqs {
		if s != int64(i+1) {
			t.Fatalf("event %d (%s) has seq %d, want %d", i, c.names[i], s, i+1)
		}
	}
	if c.names[len(c.names)-1] != EventDone {
		t.Errorf("last event %q, want %q", c.names[len(c.names)-1], EventDone)
	}
}

func TestStreamCancel(t *testing.T) {
	t.Parallel()

	c := newCapture()
	reg := operation.NewRegistry(c.emit, nil)
	started, err := New(reg, nil).Stream(t.Context(), 2000, MaxDurationMs)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.first:
	case <-time.After(10 * time.Second):
		t.Fatal("no data event")
	}

	begin := time.Now()
	if !reg.Cancel(started.OpID) {
		t.Fatal("Cancel = false for a running stream")
	}
	done := c.waitDone(t)
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Errorf("done %v after Cancel, want promptly", elapsed)
	}
	if done.Status != operation.StatusCancelled {
		t.Errorf("status = %q, want cancelled", done.Status)
	}
	if done.Items == 0 || done.Items >= int64(2*MaxDurationMs) {
		t.Errorf("items = %d, want some but not all", done.Items)
	}
}
