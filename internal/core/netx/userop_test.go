package netx

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// syncBuffer is a bytes.Buffer safe for concurrent writes (slog handlers
// write from the dialing goroutine).
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestUserOpContext(t *testing.T) {
	t.Parallel()
	if _, ok := UserOpFrom(t.Context()); ok {
		t.Error("an unmarked context has a user op")
	}
	if _, ok := UserOpFrom(WithUserOp(t.Context(), UserOp{ID: "x"})); ok {
		t.Error("a user op without Kind counts")
	}
	want := UserOp{Kind: "http.send", ID: "op_1"}
	ctx := WithUserOp(t.Context(), want)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if got, ok := UserOpFrom(ctx); !ok || got != want {
		t.Errorf("UserOpFrom = %+v, %v, want %+v", got, ok, want)
	}
	if got, _ := UserOpFrom(context.WithoutCancel(ctx)); got != want {
		t.Errorf("the mark is lost by WithoutCancel: %+v", got)
	}
}

// TestRequireUserOp proves PRV-05: a dial whose context does not carry a
// user operation is refused, logged and never reaches the server, while a
// marked dial goes through.
func TestRequireUserOp(t *testing.T) {
	t.Parallel()

	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var reached atomic.Int32
	counting := DialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		reached.Add(1)
		return NewDirect(Options{}).DialContext(ctx, network, address)
	})
	d := Chain(counting, Guarded(RequireUserOp(logger)))
	addr := mustServe(t)

	for _, ctx := range []context.Context{
		t.Context(),
		WithUserOp(t.Context(), UserOp{}), // no Kind
	} {
		conn, err := d.DialContext(ctx, "tcp", addr)
		if conn != nil {
			conn.Close()
		}
		e := wantErr(t, err, errs.PermissionDenied, CodeNotUserInitiated)
		if e.Hint != HintNotUserInitiated || e.Details["address"] != addr || e.Details["network"] != "tcp" {
			t.Errorf("hint %q, details %v", e.Hint, e.Details)
		}
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("%d refused dials reached the dialer, want 0", n)
	}
	out := logs.String()
	if strings.Count(out, "level=WARN") != 2 || !strings.Contains(out, "dial refused") || !strings.Contains(out, "address="+addr) {
		t.Errorf("refusals not logged as warnings with the address:\n%s", out)
	}

	conn, err := d.DialContext(WithUserOp(t.Context(), UserOp{Kind: "tcp.connect", ID: "c1"}), "tcp", addr)
	if err != nil {
		t.Fatalf("marked dial: %v", err)
	}
	conn.Close()
	if n := reached.Load(); n != 1 {
		t.Errorf("%d dials reached the dialer, want 1", n)
	}
	if out := logs.String(); !strings.Contains(out, "dial allowed") || !strings.Contains(out, "op=tcp.connect") {
		t.Errorf("allowed dial not logged with its operation:\n%s", out)
	}
}

func TestRequireUserOpNilLogger(t *testing.T) {
	t.Parallel()
	g := RequireUserOp(nil)
	if err := g(WithUserOp(t.Context(), UserOp{Kind: "k"}), DialCheck{Network: "tcp", Address: "a:1"}); err != nil {
		t.Errorf("marked: %v", err)
	}
	if err := g(t.Context(), DialCheck{Network: "tcp", Address: "a:1"}); errs.KindOf(err) != errs.PermissionDenied {
		t.Errorf("unmarked: %v", err)
	}
}
