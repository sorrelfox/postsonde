package operation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/opid"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// sent is one recorded Emit call.
type sent struct {
	name string
	data any
}

// recorder is an Emit that records events and signals done events.
type recorder struct {
	mu     sync.Mutex
	events []sent
	done   chan string // opId of every done event
}

func newRecorder() *recorder { return &recorder{done: make(chan string, 1000)} }

func (r *recorder) emit(name string, data any) {
	r.mu.Lock()
	r.events = append(r.events, sent{name, data})
	r.mu.Unlock()
	if d, ok := data.(DoneEventDTO); ok {
		r.done <- d.OpID
	}
}

// waitDone waits for the done event of every id in ids.
func (r *recorder) waitDone(t *testing.T, ids ...string) {
	t.Helper()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	for len(want) > 0 {
		select {
		case id := <-r.done:
			delete(want, id)
		case <-time.After(10 * time.Second):
			t.Fatalf("no done event for %v", want)
		}
	}
}

// of returns the events of one operation, in emit order.
func (r *recorder) of(id string) []sent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []sent
	for _, e := range r.events {
		if opIDOf(e.data) == id {
			out = append(out, e)
		}
	}
	return out
}

func opIDOf(data any) string {
	switch d := data.(type) {
	case DataEventDTO[int]:
		return d.OpID
	case ProgressEventDTO:
		return d.OpID
	case DoneEventDTO:
		return d.OpID
	}
	return ""
}

func seqOf(data any) int64 {
	switch d := data.(type) {
	case DataEventDTO[int]:
		return d.Seq
	case ProgressEventDTO:
		return d.Seq
	case DoneEventDTO:
		return d.Seq
	}
	return -1
}

// describe renders an operation's events compactly, e.g.
// "data[1 2] progress(2/5) done(ok)", with the seq numbers checked.
func describe(t *testing.T, events []sent) string {
	t.Helper()
	parts := make([]string, 0, len(events))
	for i, e := range events {
		if got := seqOf(e.data); got != int64(i+1) {
			t.Errorf("event %d (%s) has seq %d, want %d", i, e.name, got, i+1)
		}
		switch d := e.data.(type) {
		case DataEventDTO[int]:
			parts = append(parts, fmt.Sprintf("%s%v", e.name, d.Items))
		case ProgressEventDTO:
			parts = append(parts, fmt.Sprintf("%s(%d/%d%s)", e.name, d.Done, d.Total, d.Message))
		case DoneEventDTO:
			s := fmt.Sprintf("%s(%s", e.name, d.Status)
			if d.Error != nil {
				s += fmt.Sprintf(" %s:%s", d.Error.Kind, d.Error.Message)
			}
			parts = append(parts, s+fmt.Sprintf(" items=%d batches=%d)", d.Items, d.Batches))
		default:
			parts = append(parts, fmt.Sprintf("%s:%T", e.name, e.data))
		}
	}
	return strings.Join(parts, " ")
}

func TestStart(t *testing.T) {
	t.Parallel()

	block := func(ctx context.Context, emit *Emitter[int]) error {
		_ = emit.Send(1, 2)
		<-ctx.Done()
		_ = emit.Send(3) // sent while winding down: still delivered
		return ctx.Err()
	}

	tests := []struct {
		name     string
		maxItems int
		fn       Func[int]
		cancel   bool // cancel once started
		want     string
	}{
		{
			name: "ok with progress",
			fn: func(_ context.Context, emit *Emitter[int]) error {
				_ = emit.Send(1, 2, 3)
				_ = emit.Progress(1, 5, "")
				_ = emit.Progress(3, 5, " half")
				return nil
			},
			want: "t:op:data[1 2 3] t:op:progress(3/5 half) t:op:done(ok items=3 batches=1)",
		},
		{
			name: "nothing sent",
			fn:   func(context.Context, *Emitter[int]) error { return nil },
			want: "t:op:done(ok items=0 batches=0)",
		},
		{
			name: "error after items",
			fn: func(_ context.Context, emit *Emitter[int]) error {
				_ = emit.Send(1)
				return errs.New(errs.Network, "boom").WithCode("t.boom")
			},
			want: "t:op:data[1] t:op:done(error network:boom items=1 batches=1)",
		},
		{
			name: "panic",
			fn:   func(context.Context, *Emitter[int]) error { panic("bad") },
			want: "t:op:done(error internal:Something went wrong inside PostSonde. items=0 batches=0)",
		},
		{
			name:     "max items splits batches",
			maxItems: 2,
			fn: func(_ context.Context, emit *Emitter[int]) error {
				return emit.Send(1, 2, 3, 4, 5)
			},
			want: "t:op:data[1 2] t:op:data[3 4] t:op:data[5] t:op:done(ok items=5 batches=3)",
		},
		{
			name:   "cancelled",
			fn:     block,
			cancel: true,
			want:   "t:op:data[1 2 3] t:op:done(cancelled items=3 batches=1)",
		},
		{
			name:   "cancelled even if the work returns nil",
			cancel: true,
			fn: func(ctx context.Context, _ *Emitter[int]) error {
				<-ctx.Done()
				return nil
			},
			want: "t:op:done(cancelled items=0 batches=0)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := newRecorder()
			r := NewRegistry(rec.emit, nil)
			started, err := Start(r, Spec{Domain: "t", Kind: "test", Interval: time.Hour, MaxItems: tt.maxItems}, tt.fn)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if !opid.Valid(started.OpID) {
				t.Errorf("OpID %q is not a valid operation ID", started.OpID)
			}
			if tt.cancel {
				if !r.Cancel(started.OpID) {
					t.Error("Cancel of a running operation = false, want true")
				}
			}
			rec.waitDone(t, started.OpID)

			if got := describe(t, rec.of(started.OpID)); got != tt.want {
				t.Errorf("events:\n got %s\nwant %s", got, tt.want)
			}
			if r.Running() != 0 {
				t.Errorf("Running = %d after done, want 0", r.Running())
			}
			if r.Cancel(started.OpID) {
				t.Error("Cancel after done = true, want false")
			}
		})
	}
}

func TestStartInvalid(t *testing.T) {
	t.Parallel()

	r := NewRegistry(newRecorder().emit, nil)
	if _, err := Start(r, Spec{}, func(context.Context, *Emitter[int]) error { return nil }); err == nil {
		t.Error("Start without a domain succeeded, want an error")
	}
}

// TestStartMarksUserOp checks that an operation's context carries the
// user-operation mark the app's dialer requires (PRV-05).
func TestStartMarksUserOp(t *testing.T) {
	t.Parallel()

	rec := newRecorder()
	r := NewRegistry(rec.emit, nil)
	for _, tt := range []struct {
		spec Spec
		kind string
	}{
		{Spec{Domain: "http", Kind: "send"}, "http.send"},
		{Spec{Domain: "demo"}, "demo"},
	} {
		got := make(chan netx.UserOp, 1)
		started, err := Start(r, tt.spec, func(ctx context.Context, _ *Emitter[int]) error {
			op, ok := netx.UserOpFrom(ctx)
			if !ok {
				t.Error("operation context is not marked as a user operation")
			}
			got <- op
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		rec.waitDone(t, started.OpID)
		if op := <-got; op != (netx.UserOp{Kind: tt.kind, ID: started.OpID}) {
			t.Errorf("user op = %+v, want kind %q and ID %q", op, tt.kind, started.OpID)
		}
	}
}

func TestCancelUnknown(t *testing.T) {
	t.Parallel()

	r := NewRegistry(newRecorder().emit, nil)
	for _, id := range []string{"", "op_0000000000000000", "nonsense"} {
		if r.Cancel(id) {
			t.Errorf("Cancel(%q) = true, want false", id)
		}
	}
}

// TestFlushByInterval checks that a slow producer's items go out on the
// ticker, before the operation ends.
func TestFlushByInterval(t *testing.T) {
	t.Parallel()

	ticks := make(chan time.Time)
	rec := newRecorder()
	r := NewRegistry(rec.emit, nil)
	r.newTicker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }

	proceed := make(chan struct{})
	started, err := Start(r, Spec{Domain: "t"}, func(ctx context.Context, emit *Emitter[int]) error {
		_ = emit.Send(1)
		_ = emit.Progress(1, 2, "")
		proceed <- struct{}{} // item and progress are queued
		<-proceed
		_ = emit.Send(2)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-proceed
	// Two ticks: once the second is taken, the first one's flush is done.
	ticks <- time.Time{}
	ticks <- time.Time{}
	if got, want := describe(t, rec.of(started.OpID)), "t:op:data[1] t:op:progress(1/2)"; got != want {
		t.Errorf("events after a tick:\n got %s\nwant %s", got, want)
	}
	proceed <- struct{}{}
	rec.waitDone(t, started.OpID)
	if got, want := describe(t, rec.of(started.OpID)), "t:op:data[1] t:op:progress(1/2) t:op:data[2] t:op:done(ok items=2 batches=2)"; got != want {
		t.Errorf("events:\n got %s\nwant %s", got, want)
	}
}

func TestConcurrentOperations(t *testing.T) {
	t.Parallel()

	const ops, each = 20, 1000
	rec := newRecorder()
	r := NewRegistry(rec.emit, nil)

	ids := make([]string, 0, ops)
	for range ops {
		started, err := Start(r, Spec{Domain: "t", Interval: time.Millisecond, MaxItems: 64},
			func(_ context.Context, emit *Emitter[int]) error {
				for i := range each {
					if err := emit.Send(i); err != nil {
						return err
					}
					if i%100 == 0 {
						_ = emit.Progress(int64(i), each, "")
					}
				}
				return nil
			})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, started.OpID)
	}
	rec.waitDone(t, ids...)

	for _, id := range ids {
		events := rec.of(id)
		var items []int
		for i, e := range events {
			if seqOf(e.data) != int64(i+1) {
				t.Fatalf("%s: event %d has seq %d", id, i, seqOf(e.data))
			}
			if d, ok := e.data.(DataEventDTO[int]); ok {
				items = append(items, d.Items...)
			}
		}
		if len(items) != each || !slices.IsSorted(items) || items[0] != 0 || items[each-1] != each-1 {
			t.Fatalf("%s: got %d items, want 0..%d in order", id, len(items), each-1)
		}
		last, ok := events[len(events)-1].data.(DoneEventDTO)
		if !ok || last.Status != StatusOK || last.Items != each {
			t.Fatalf("%s: last event = %+v, want done ok with %d items", id, events[len(events)-1].data, each)
		}
	}
}

func TestShutdown(t *testing.T) {
	t.Parallel()

	rec := newRecorder()
	r := NewRegistry(rec.emit, nil)
	var ids []string
	for range 3 {
		started, err := Start(r, Spec{Domain: "t"}, func(ctx context.Context, _ *Emitter[int]) error {
			<-ctx.Done()
			return ctx.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, started.OpID)
	}
	if r.Running() != 3 {
		t.Fatalf("Running = %d, want 3", r.Running())
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for _, id := range ids {
		if got := describe(t, rec.of(id)); got != "t:op:done(cancelled items=0 batches=0)" {
			t.Errorf("%s: %s", id, got)
		}
	}
	if _, err := Start(r, Spec{Domain: "t"}, func(context.Context, *Emitter[int]) error { return nil }); !errors.Is(err, ErrShutdown) {
		t.Errorf("Start after Shutdown: err = %v, want ErrShutdown", err)
	}
}

func TestShutdownTimeout(t *testing.T) {
	t.Parallel()

	r := NewRegistry(newRecorder().emit, nil)
	release := make(chan struct{})
	defer close(release)
	if _, err := Start(r, Spec{Domain: "t"}, func(context.Context, *Emitter[int]) error {
		<-release // ignores cancellation
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := r.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown: err = %v, want DeadlineExceeded", err)
	}
}

func TestSendAfterDone(t *testing.T) {
	t.Parallel()

	rec := newRecorder()
	r := NewRegistry(rec.emit, nil)
	leaked := make(chan *Emitter[int], 1)
	started, err := Start(r, Spec{Domain: "t"}, func(_ context.Context, emit *Emitter[int]) error {
		leaked <- emit
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rec.waitDone(t, started.OpID)
	emit := <-leaked
	if err := emit.Send(1); err == nil {
		t.Error("Send after done succeeded, want an error")
	}
	if err := emit.Progress(1, 1, ""); err == nil {
		t.Error("Progress after done succeeded, want an error")
	}
}

func TestEventName(t *testing.T) {
	t.Parallel()

	tests := []struct{ domain, phase, want string }{
		{"demo", PhaseData, "demo:op:data"},
		{"http", PhaseProgress, "http:op:progress"},
		{"grpc", PhaseDone, "grpc:op:done"},
	}
	for _, tt := range tests {
		if got := EventName(tt.domain, tt.phase); got != tt.want {
			t.Errorf("EventName(%q, %q) = %q, want %q", tt.domain, tt.phase, got, tt.want)
		}
	}
}

// TestDTOJSON pins the JSON shape of the event payloads, the contract with
// the UI (docs/events.md).
func TestDTOJSON(t *testing.T) {
	t.Parallel()

	type item struct {
		Index int64 `json:"index"`
	}
	const id = "op_0123456789abcdef"
	refused := apperr.ToDTO(errs.Wrap(errors.New("dial tcp 127.0.0.1:9: connect: connection refused"),
		errs.Network, "The connection to 127.0.0.1:9 was refused.").
		WithCode("netx.connection_refused").
		WithHint("Is the server running on that host and port?").
		WithDetail("address", "127.0.0.1:9"))
	tests := []struct {
		name string
		dto  any
	}{
		{"started", StartedDTO{OpID: id}},
		{"data_event", DataEventDTO[item]{OpID: id, Seq: 1, Items: []item{{0}, {1}}}},
		{"progress_event", ProgressEventDTO{OpID: id, Seq: 2, Done: 2, Total: 10, Message: "receiving"}},
		{"done_event_ok", DoneEventDTO{OpID: id, Seq: 3, Status: StatusOK, Items: 2, Batches: 1, ElapsedMs: 120}},
		{"done_event_error", DoneEventDTO{OpID: id, Seq: 3, Status: StatusError, Error: &refused, ElapsedMs: 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.MarshalIndent(tt.dto, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, "dto/"+tt.name, append(got, '\n'))
		})
	}
}
