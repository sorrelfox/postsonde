package windowctx

import (
	"context"
	"testing"
)

func TestID(t *testing.T) {
	t.Parallel()
	if got := ID(context.Background()); got != 0 {
		t.Errorf("ID(no window) = %d, want 0", got)
	}
	ctx := With(context.Background(), 7)
	if got := ID(ctx); got != 7 {
		t.Errorf("ID(With 7) = %d", got)
	}
	if got := ID(With(ctx, 3)); got != 3 {
		t.Errorf("ID(With 3 over 7) = %d", got)
	}
}
