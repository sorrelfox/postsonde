package netx

import (
	"context"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
)

// recorder is a Trace that records events; safe for concurrent use.
type recorder struct {
	mu     sync.Mutex
	starts []DialStartInfo
	dones  []DialDoneInfo
}

func (r *recorder) trace() *Trace {
	return &Trace{
		DialStart: func(i DialStartInfo) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.starts = append(r.starts, i)
		},
		DialDone: func(i DialDoneInfo) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.dones = append(r.dones, i)
		},
	}
}

func (r *recorder) events() ([]DialStartInfo, []DialDoneInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]DialStartInfo(nil), r.starts...), append([]DialDoneInfo(nil), r.dones...)
}

// fakeClock advances by step on every reading.
type fakeClock struct {
	mu   sync.Mutex
	now  time.Time
	step time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(c.step)
	return c.now
}

// serve listens on address and accepts (and closes) connections until the
// test ends. It returns the listener's address, or the listen error.
func serve(t *testing.T, address string) (string, error) {
	t.Helper()
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return "", err
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return ln.Addr().String(), nil
}

func mustServe(t *testing.T) string {
	t.Helper()
	addr, err := serve(t, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return addr
}

// closedPort returns a local address nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// blockingControl is a ControlContext hook that calls onEnter and then
// blocks until the dial's context is done, making cancellation and
// timeouts during a dial deterministic.
func blockingControl(onEnter func()) func(context.Context, string, string, syscall.RawConn) error {
	return func(ctx context.Context, _, _ string, _ syscall.RawConn) error {
		if onEnter != nil {
			onEnter()
		}
		<-ctx.Done()
		return ctx.Err()
	}
}

// nxdomainResolver and failingResolver fail lookups in process; see
// netxtest.
func nxdomainResolver() *net.Resolver { return netxtest.NXDomainResolver() }

func failingResolver() *net.Resolver { return netxtest.FailingResolver() }

// wantErr checks that err is an *errs.Error itself with kind and code.
func wantErr(t *testing.T, err error, kind errs.Kind, code string) *errs.Error {
	t.Helper()
	e, ok := err.(*errs.Error)
	if !ok {
		t.Fatalf("error = %T %v, want *errs.Error", err, err)
	}
	if e.Kind != kind || e.Code != code {
		t.Fatalf("error kind, code = %q, %q, want %q, %q (%v)", e.Kind, e.Code, kind, code, err)
	}
	if got := errs.KindOf(err); got != kind {
		t.Errorf("errs.KindOf = %q, want %q", got, kind)
	}
	return e
}
