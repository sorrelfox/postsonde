package netx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func TestNewDirectOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                          string
		opts                          Options
		wantTimeout, wantKA, wantFall time.Duration
	}{
		{"defaults", Options{}, DefaultTimeout, DefaultKeepAlive, 0},
		{"explicit", Options{Timeout: 5 * time.Second, KeepAlive: 10 * time.Second, FallbackDelay: time.Second},
			5 * time.Second, 10 * time.Second, time.Second},
		{"disabled", Options{Timeout: -1, KeepAlive: -1, FallbackDelay: -1}, 0, -1, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := NewDirect(tt.opts)
			if d.dialer.Timeout != tt.wantTimeout || d.dialer.KeepAlive != tt.wantKA || d.dialer.FallbackDelay != tt.wantFall {
				t.Errorf("timeout, keep-alive, fallback = %v, %v, %v, want %v, %v, %v",
					d.dialer.Timeout, d.dialer.KeepAlive, d.dialer.FallbackDelay, tt.wantTimeout, tt.wantKA, tt.wantFall)
			}
			if d.dialer.ControlContext != nil {
				t.Error("ControlContext set without a Guard or ControlContext option")
			}
		})
	}
}

func TestDirectDialSuccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		network string
		listen  string
	}{
		{"ipv4", "tcp", "127.0.0.1:0"},
		{"ipv4 on tcp4", "tcp4", "127.0.0.1:0"},
		{"ipv6", "tcp", "[::1]:0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			addr, err := serve(t, tt.listen)
			if err != nil {
				t.Skipf("cannot listen on %s: %v", tt.listen, err)
			}
			clock := &fakeClock{now: time.Unix(1000, 0), step: 42 * time.Millisecond}
			d := NewDirect(Options{Now: clock.Now})
			rec := &recorder{}

			conn, err := d.DialContext(WithTrace(t.Context(), rec.trace()), tt.network, addr)
			if err != nil {
				t.Fatalf("DialContext: %v", err)
			}
			defer conn.Close()
			if got := conn.RemoteAddr().String(); got != addr {
				t.Errorf("RemoteAddr = %s, want %s", got, addr)
			}

			starts, dones := rec.events()
			if want := []DialStartInfo{{Network: tt.network, Address: addr}}; len(starts) != 1 || starts[0] != want[0] {
				t.Errorf("DialStart events = %+v, want %+v", starts, want)
			}
			if len(dones) != 1 {
				t.Fatalf("DialDone events = %+v, want one", dones)
			}
			done := dones[0]
			if done.Network != tt.network || done.Address != addr || done.Err != nil || done.Duration != 42*time.Millisecond {
				t.Errorf("DialDone = %+v, want %s %s, 42ms, no error", done, tt.network, addr)
			}
			if done.RemoteAddr.String() != addr || done.LocalAddr.String() != conn.LocalAddr().String() {
				t.Errorf("DialDone addresses = %v -> %v, want %v -> %v", done.LocalAddr, done.RemoteAddr, conn.LocalAddr(), addr)
			}
		})
	}
}

func TestDirectCancelAndTimeout(t *testing.T) {
	t.Parallel()
	addr := mustServe(t)
	cancelled := func(t *testing.T) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		return ctx, cancel
	}
	expired := func(t *testing.T) (context.Context, context.CancelFunc) {
		return context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	}
	running := func(t *testing.T) (context.Context, context.CancelFunc) {
		return context.WithCancel(t.Context())
	}
	short := func(t *testing.T) (context.Context, context.CancelFunc) {
		return context.WithTimeout(t.Context(), 50*time.Millisecond)
	}

	tests := []struct {
		name          string
		timeout       time.Duration
		ctx           func(*testing.T) (context.Context, context.CancelFunc)
		cancelOnEnter bool
		wantKind      errs.Kind
		wantCode      string
		wantIs        error
		wantDialing   bool // the dial started: trace events, a *net.OpError
	}{
		{name: "cancelled before the dial", ctx: cancelled,
			wantKind: errs.Cancelled, wantCode: CodeCancelled, wantIs: context.Canceled},
		{name: "deadline passed before the dial", ctx: expired,
			wantKind: errs.Timeout, wantCode: CodeTimeout, wantIs: context.DeadlineExceeded},
		{name: "cancelled during the dial", ctx: running, cancelOnEnter: true,
			wantKind: errs.Cancelled, wantCode: CodeCancelled, wantIs: context.Canceled, wantDialing: true},
		{name: "dialer timeout", ctx: running, timeout: 50 * time.Millisecond,
			wantKind: errs.Timeout, wantCode: CodeTimeout, wantIs: context.DeadlineExceeded, wantDialing: true},
		{name: "context deadline during the dial", ctx: short,
			wantKind: errs.Timeout, wantCode: CodeTimeout, wantIs: context.DeadlineExceeded, wantDialing: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := tt.ctx(t)
			defer cancel()
			var entered atomic.Int32
			d := NewDirect(Options{
				Timeout: tt.timeout,
				ControlContext: blockingControl(func() {
					entered.Add(1)
					if tt.cancelOnEnter {
						cancel()
					}
				}),
			})
			rec := &recorder{}

			conn, err := d.DialContext(WithTrace(ctx, rec.trace()), "tcp", addr)
			if conn != nil {
				conn.Close()
				t.Fatal("DialContext returned a connection")
			}
			e := wantErr(t, err, tt.wantKind, tt.wantCode)
			if !errors.Is(err, tt.wantIs) {
				t.Errorf("errors.Is(err, %v) = false: %v", tt.wantIs, err)
			}
			if e.Details["address"] != addr || e.Details["network"] != "tcp" {
				t.Errorf("details = %v", e.Details)
			}
			if tt.wantKind == errs.Timeout && e.Hint != HintTimeout {
				t.Errorf("hint = %q, want %q", e.Hint, HintTimeout)
			}

			starts, dones := rec.events()
			if !tt.wantDialing {
				if entered.Load() != 0 || len(starts) != 0 || len(dones) != 0 {
					t.Errorf("dial started: control calls %d, events %v %v", entered.Load(), starts, dones)
				}
				return
			}
			if _, ok := errors.AsType[*net.OpError](err); !ok {
				t.Errorf("no *net.OpError in %v", err)
			}
			if len(starts) != 1 || len(dones) != 1 || dones[0].Err != err || dones[0].RemoteAddr != nil {
				t.Errorf("events = %+v %+v, want one start and one done with the error", starts, dones)
			}
		})
	}
}

// TestDirectHTTPTransport proves the integration point for the HTTP client:
// http.Transport dials through Direct, and the request's trace sees it.
func TestDirectHTTPTransport(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "pong")
	}))
	defer srv.Close()

	tr := &http.Transport{DialContext: NewDirect(Options{}).DialContext}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	rec := &recorder{}

	req, err := http.NewRequestWithContext(WithTrace(t.Context(), rec.trace()), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(body) != "pong" {
		t.Fatalf("body = %q, %v, want pong", body, err)
	}

	addr := srv.Listener.Addr().String()
	starts, dones := rec.events()
	if len(starts) != 1 || starts[0].Address != addr || !strings.HasPrefix(starts[0].Network, "tcp") {
		t.Errorf("DialStart events = %+v, want one for %s", starts, addr)
	}
	if len(dones) != 1 || dones[0].Err != nil || dones[0].RemoteAddr.String() != addr {
		t.Errorf("DialDone events = %+v, want one successful dial to %s", dones, addr)
	}
}
