package runner

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

func testPlan() Plan {
	return Plan{Items: []Item{
		{ID: "a", Name: "A", Kind: "http"},
		{ID: "ws", Name: "Socket", Kind: "websocket", Skip: SkipKind},
		{ID: "b", Name: "B", Kind: "graphql", Path: []string{"Sub"}},
	}}
}

func TestSteps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		iterations int
		want       []string
	}{
		{"one iteration", 1, []string{"1.1 a #1", "1.2 ws #2", "1.3 b #3"}},
		{"three iterations", 3, []string{
			"1.1 a #1", "1.2 ws #2", "1.3 b #3",
			"2.1 a #4", "2.2 ws #5", "2.3 b #6",
			"3.1 a #7", "3.2 ws #8", "3.3 b #9",
		}},
		{"none", 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for st := range testPlan().Steps(tt.iterations) {
				got = append(got, stepText(st))
			}
			if !equal(got, tt.want) {
				t.Fatalf("steps = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStepsStopEarly(t *testing.T) {
	t.Parallel()
	n := 0
	for st := range testPlan().Steps(5) {
		n++
		if st.Index == 4 {
			break
		}
	}
	if n != 4 {
		t.Fatalf("visited %d steps, want 4", n)
	}
}

func stepText(st Step) string {
	return strconv.Itoa(st.Iteration) + "." + strconv.Itoa(st.Position) + " " + st.Item.ID + " #" + strconv.Itoa(st.Index)
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRunnable(t *testing.T) {
	t.Parallel()
	if got := testPlan().Runnable(); got != 2 {
		t.Fatalf("Runnable = %d, want 2", got)
	}
	if got := (Plan{}).Runnable(); got != 0 {
		t.Fatalf("empty Runnable = %d", got)
	}
}

// TestSelect: a selection keeps the items it names, in its own order,
// and leaves the plan it came from as it was (RUN-02).
func TestSelect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		ids    []string
		want   []string
		reason string
	}{
		{"all in the plan's order", []string{"a", "ws", "b"}, []string{"1.1 a #1", "1.2 ws #2", "1.3 b #3"}, ""},
		{"a custom order", []string{"b", "a"}, []string{"1.1 b #1", "1.2 a #2"}, ""},
		{"one left out", []string{"a", "b"}, []string{"1.1 a #1", "1.2 b #2"}, ""},
		{"a skipped item named", []string{"ws"}, []string{"1.1 ws #1"}, ""},
		{"none", []string{}, nil, ""},
		{"unknown", []string{"a", "gone"}, nil, "unknown"},
		{"twice", []string{"b", "a", "b"}, nil, "duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			plan := testPlan()
			got, err := plan.Select(tt.ids)
			if tt.reason != "" {
				var e *errs.Error
				if !errors.As(err, &e) || e.Kind != errs.InvalidArgument || e.Code != CodeInvalidSelection || e.Details["reason"] != tt.reason {
					t.Fatalf("Select = %#v, want reason %s", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("Select = %v", err)
			}
			var steps []string
			for st := range got.Steps(1) {
				steps = append(steps, stepText(st))
			}
			if !equal(steps, tt.want) {
				t.Fatalf("steps = %v, want %v", steps, tt.want)
			}
			if plan.Items[0].ID != "a" || plan.Items[2].ID != "b" || len(plan.Items) != 3 {
				t.Fatalf("the plan changed: %+v", plan.Items)
			}
		})
	}

	// The skipped item keeps its reason; iterations repeat the order.
	sel, err := testPlan().Select([]string{"b", "ws"})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Items[1].Skip != SkipKind || sel.Runnable() != 1 {
		t.Fatalf("selection = %+v", sel.Items)
	}
	var steps []string
	for st := range sel.Steps(2) {
		steps = append(steps, stepText(st))
	}
	if want := []string{"1.1 b #1", "1.2 ws #2", "2.1 b #3", "2.2 ws #4"}; !equal(steps, want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
}

func TestStops(t *testing.T) {
	t.Parallel()
	bail := Options{Iterations: 1, StopOnFailure: true}
	if !bail.Stops(true) || bail.Stops(false) {
		t.Fatal("a run that stops on failure should stop after a failure only")
	}
	if (Options{Iterations: 1}).Stops(true) {
		t.Fatal("a run that goes on stopped after a failure")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		o     Options
		field string
	}{
		{"one iteration", Options{Iterations: 1}, ""},
		{"the most", Options{Iterations: MaxIterations, Delay: MaxDelay, StreamWindow: MaxStreamWindow}, ""},
		{"no iterations", Options{Iterations: 0}, "iterations"},
		{"too many iterations", Options{Iterations: MaxIterations + 1}, "iterations"},
		{"negative delay", Options{Iterations: 1, Delay: -time.Millisecond}, "delayMs"},
		{"long delay", Options{Iterations: 1, Delay: MaxDelay + time.Millisecond}, "delayMs"},
		{"negative window", Options{Iterations: 1, StreamWindow: -1}, "streamMs"},
		{"long window", Options{Iterations: 1, StreamWindow: MaxStreamWindow + 1}, "streamMs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.o.Validate()
			if tt.field == "" {
				if err != nil {
					t.Fatalf("Validate = %v", err)
				}
				return
			}
			var e *errs.Error
			if !errors.As(err, &e) || e.Kind != errs.InvalidArgument || e.Code != CodeInvalidOptions || e.Details["field"] != tt.field {
				t.Fatalf("Validate = %#v, want field %s", err, tt.field)
			}
		})
	}
}

func TestWindow(t *testing.T) {
	t.Parallel()
	if got := (Options{}).Window(); got != DefaultStreamWindow {
		t.Errorf("default window = %v", got)
	}
	if got := (Options{StreamWindow: time.Second}).Window(); got != time.Second {
		t.Errorf("window = %v", got)
	}
}

// sleeps records the waits of a Pacer instead of sleeping.
type sleeps struct {
	mu  sync.Mutex
	got []time.Duration
}

func (s *sleeps) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.got = append(s.got, d)
	s.mu.Unlock()
	return ctx.Err()
}

func TestPacer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		delay time.Duration
		sends int
		want  []time.Duration
	}{
		{"no delay", 0, 3, nil},
		{"one send", time.Second, 1, nil},
		{"between sends", 250 * time.Millisecond, 4, []time.Duration{250 * time.Millisecond, 250 * time.Millisecond, 250 * time.Millisecond}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var s sleeps
			p := NewPacer(tt.delay, s.sleep)
			for range tt.sends {
				if err := p.Wait(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if len(s.got) != len(tt.want) {
				t.Fatalf("waits = %v, want %v", s.got, tt.want)
			}
			for i := range s.got {
				if s.got[i] != tt.want[i] {
					t.Fatalf("waits = %v, want %v", s.got, tt.want)
				}
			}
		})
	}
}

func TestPacerStopped(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	p := NewPacer(time.Hour, nil)
	if err := p.Wait(ctx); err != nil {
		t.Fatalf("first Wait = %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- p.Wait(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not return when the run was stopped")
	}
	// A stopped run does not send its first request either.
	if err := NewPacer(0, nil).Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait after stop = %v", err)
	}
}

func TestSleep(t *testing.T) {
	t.Parallel()
	if err := Sleep(t.Context(), 0); err != nil {
		t.Fatalf("Sleep(0) = %v", err)
	}
	if err := Sleep(t.Context(), time.Millisecond); err != nil {
		t.Fatalf("Sleep = %v", err)
	}
}

func TestVars(t *testing.T) {
	t.Parallel()
	v := NewVars()
	var _ template.Scope = v
	var _ template.Lister = v
	if v.Name() != VarsScopeName {
		t.Errorf("Name = %q", v.Name())
	}
	if _, ok := v.Lookup("id"); ok {
		t.Fatal("a new run has variables")
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			v.Set("k"+strconv.Itoa(i), template.Variable{Value: strconv.Itoa(i)})
			_, _ = v.Lookup("id")
			_ = v.Names()
		})
	}
	wg.Wait()
	v.Set("id", template.Variable{Value: "42", Secret: true})
	if got, ok := v.Lookup("id"); !ok || got.Value != "42" || !got.Secret {
		t.Fatalf("Lookup = %+v, %v", got, ok)
	}
	if names := v.Names(); len(names) != 9 || names[0] != "id" {
		t.Fatalf("Names = %v", names)
	}
	// The run's variables win over every other scope.
	chain := template.Chain{template.Vars("Folder", map[string]string{"id": "1"}), v}
	if b, ok := chain.Lookup("id"); !ok || b.Value != "42" || b.Scope != VarsScopeName {
		t.Fatalf("chain lookup = %+v", b)
	}
}
