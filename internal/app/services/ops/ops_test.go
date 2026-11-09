package ops

import (
	"context"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
)

func TestCancel(t *testing.T) {
	t.Parallel()

	done := make(chan operation.DoneEventDTO, 1)
	reg := operation.NewRegistry(func(_ string, data any) {
		if d, ok := data.(operation.DoneEventDTO); ok {
			done <- d
		}
	}, nil)
	s := New(reg, nil)

	started, err := operation.Start(reg, operation.Spec{Domain: "t"}, func(ctx context.Context, _ *operation.Emitter[int]) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"unknown", "op_0000000000000000", false},
		{"empty", "", false},
		{"running", started.OpID, true},
	}
	for _, tt := range tests {
		if got := s.Cancel(t.Context(), tt.id); got != tt.want {
			t.Errorf("%s: Cancel(%q) = %v, want %v", tt.name, tt.id, got, tt.want)
		}
	}

	select {
	case d := <-done:
		if d.OpID != started.OpID || d.Status != operation.StatusCancelled {
			t.Errorf("done = %+v, want status cancelled for %s", d, started.OpID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no done event after Cancel")
	}
	if s.Cancel(t.Context(), started.OpID) {
		t.Error("Cancel after done = true, want false")
	}
}

func TestServiceShutdown(t *testing.T) {
	t.Parallel()

	reg := operation.NewRegistry(func(string, any) {}, nil)
	s := New(reg, nil)
	if _, err := operation.Start(reg, operation.Spec{Domain: "t"}, func(ctx context.Context, _ *operation.Emitter[int]) error {
		<-ctx.Done()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	if n := reg.Running(); n != 0 {
		t.Errorf("Running = %d after shutdown, want 0", n)
	}
}
