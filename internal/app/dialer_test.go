package app

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netlog"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// countingListener accepts connections on loopback until the test ends and
// counts them.
func countingListener(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var n atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			c.Close()
		}
	}()
	return ln.Addr().String(), &n
}

// TestDialerRefusesNonUserDials proves PRV-05 for the dialer the app gives
// every protocol client: a dial without a user operation in its context is
// refused and logged, and nothing reaches the server; a dial from an
// operation started through the registry goes through. Both are in the
// connection log (PRV-06), the second with the operation that caused it.
func TestDialerRefusesNonUserDials(t *testing.T) {
	t.Parallel()

	var logs lockedBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	connLog := netlog.New(netlog.Options{})
	d := newDialer(logger, connLog, dialerConfig{})
	addr, accepted := countingListener(t)

	conn, err := d.DialContext(t.Context(), "tcp", addr)
	if conn != nil {
		conn.Close()
	}
	if !errorsIs(err, netx.CodeNotUserInitiated) || errs.KindOf(err) != errs.PermissionDenied {
		t.Fatalf("unmarked dial: err = %v, want permission_denied %s", err, netx.CodeNotUserInitiated)
	}
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "dial refused") || !strings.Contains(out, addr) {
		t.Errorf("the refusal was not logged:\n%s", out)
	}

	// The same dial from an operation, as HTTP sends and gRPC calls run.
	done := make(chan string, 1)
	ops := operation.NewRegistry(func(_ string, data any) {
		if d, ok := data.(operation.DoneEventDTO); ok {
			done <- string(d.Status)
		}
	}, logger)
	t.Cleanup(func() { _ = ops.Shutdown(context.Background()) })
	_, err = operation.Start(ops, operation.Spec{Domain: "test", Kind: "dial"}, func(ctx context.Context, _ *operation.Emitter[int]) error {
		c, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		return c.Close()
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-done:
		if status != string(operation.StatusOK) {
			t.Fatalf("dial from an operation: status %s, want ok", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the operation did not finish")
	}

	deadline := time.Now().Add(5 * time.Second)
	for accepted.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := accepted.Load(); n != 1 {
		t.Errorf("the server accepted %d connections, want 1 (the operation's)", n)
	}

	conns := connLog.Conns()
	if len(conns) != 2 {
		t.Fatalf("connection log = %+v, want 2 dials", conns)
	}
	if c := conns[0]; c.State != netlog.Refused || c.Op.Kind != "" || c.Address != addr || c.ErrCode != netx.CodeNotUserInitiated {
		t.Errorf("refused dial logged as %+v", c)
	}
	if c := conns[1]; c.State != netlog.Closed || c.Op.Kind != "test.dial" || c.Op.ID == "" || c.Address != addr || c.RemoteAddr != addr {
		t.Errorf("operation's dial logged as %+v", c)
	}
	if n := connLog.Active(); n != 0 {
		t.Errorf("%d connections still active", n)
	}
}

func errorsIs(err error, code string) bool {
	e, ok := err.(*errs.Error)
	return ok && e.Code == code
}
