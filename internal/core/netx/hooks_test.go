package netx

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"sync"
	"syscall"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// log collects names in order; safe for concurrent use.
type log struct {
	mu    sync.Mutex
	names []string
}

func (l *log) add(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.names = append(l.names, name)
}

func (l *log) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.names)
}

// named is a test-only middleware that logs its name and checks that the
// trace reaches it.
func named(t *testing.T, l *log, name string) Middleware {
	return func(next Dialer) Dialer {
		return DialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
			l.add(name)
			if ContextTrace(ctx) == nil {
				t.Errorf("middleware %s: no trace in ctx", name)
			}
			return next.DialContext(ctx, network, address)
		})
	}
}

// fakeProxy is a test-only stand-in for a proxy dialer: it reaches the
// proxy at proxyAddr through next and records the target, which it gets
// unresolved (what SOCKS5 with remote DNS sends to the proxy).
func fakeProxy(proxyAddr string, targets *log) Middleware {
	return func(next Dialer) Dialer {
		return DialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
			targets.add(address)
			return next.DialContext(ctx, network, proxyAddr)
		})
	}
}

func TestChainOrderAndTracePassThrough(t *testing.T) {
	t.Parallel()
	addr := mustServe(t)
	var l log
	d := Chain(NewDirect(Options{}), named(t, &l, "a"), nil, named(t, &l, "b"), named(t, &l, "c"))
	rec := &recorder{}

	conn, err := d.DialContext(WithTrace(t.Context(), rec.trace()), "tcp", addr)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	conn.Close()
	if got, want := l.get(), []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("middleware order = %v, want %v", got, want)
	}
	starts, dones := rec.events()
	if len(starts) != 1 || len(dones) != 1 || dones[0].Address != addr {
		t.Errorf("events = %+v %+v, want one dial to %s", starts, dones, addr)
	}

	direct := NewDirect(Options{})
	if Chain(direct) != Dialer(direct) {
		t.Error("Chain without middleware does not return the dialer")
	}
}

func TestRouted(t *testing.T) {
	t.Parallel()
	targetAddr := mustServe(t)
	proxyAddr := mustServe(t)
	direct := NewDirect(Options{})
	var targets log
	proxied := Chain(direct, fakeProxy(proxyAddr, &targets))
	route := Route(func(_ context.Context, _, address string) (Dialer, error) {
		host, _, _ := net.SplitHostPort(address)
		switch host {
		case "bad.test":
			return nil, errs.New(errs.InvalidArgument, "The proxy URL is invalid.").WithCode("proxy.invalid_url")
		case "nil.test":
			return nil, nil
		}
		if ip, err := netip.ParseAddr(host); err == nil && ip.IsLoopback() {
			return direct, nil // the bypass list
		}
		return proxied, nil
	})

	tests := []struct {
		name        string
		address     string
		wantKind    errs.Kind
		wantCode    string
		wantRemote  string
		wantTargets []string
	}{
		{name: "loopback bypasses the proxy", address: targetAddr, wantRemote: targetAddr},
		{name: "other hosts use the proxy", address: "api.example.test:443", wantRemote: proxyAddr,
			wantTargets: []string{"api.example.test:443"}},
		{name: "route error", address: "bad.test:80", wantKind: errs.InvalidArgument, wantCode: "proxy.invalid_url"},
		{name: "no dialer", address: "nil.test:80", wantKind: errs.Internal, wantCode: CodeNoRoute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: the cases share the proxy's target log.
			targets.mu.Lock()
			targets.names = nil
			targets.mu.Unlock()
			rec := &recorder{}
			conn, err := Routed(route).DialContext(WithTrace(t.Context(), rec.trace()), "tcp", tt.address)
			if tt.wantKind != "" {
				wantErr(t, err, tt.wantKind, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("DialContext: %v", err)
			}
			conn.Close()
			if got := conn.RemoteAddr().String(); got != tt.wantRemote {
				t.Errorf("connected to %s, want %s", got, tt.wantRemote)
			}
			if got := targets.get(); !slices.Equal(got, tt.wantTargets) {
				t.Errorf("proxy targets = %v, want %v", got, tt.wantTargets)
			}
			// Direct reports the hop it made: to the proxy when proxied.
			if starts, _ := rec.events(); len(starts) != 1 || starts[0].Address != tt.wantRemote {
				t.Errorf("DialStart events = %+v, want one to %s", starts, tt.wantRemote)
			}
		})
	}
}

func TestGuarded(t *testing.T) {
	t.Parallel()
	offline := errors.New("offline")
	guard := Guard(func(_ context.Context, c DialCheck) error {
		if c.Resolved.IsValid() {
			return errors.New("Guarded must not see a resolved address")
		}
		switch c.Address {
		case "blocked.test:80":
			return Deny(c, "The host is on the block list.")
		case "offline.test:80":
			return offline
		case "other.test:80":
			return errs.New(errs.Unsupported, "Not supported.").WithCode("test.other")
		}
		return nil
	})

	tests := []struct {
		name     string
		guard    Guard
		address  string
		wantKind errs.Kind
		wantCode string
		wantHint string
		wantIs   error
	}{
		{name: "allowed", guard: guard, address: "ok.test:80"},
		{name: "allow all", guard: AllowAll, address: "blocked.test:80"},
		{name: "nil guard", guard: nil, address: "blocked.test:80"},
		{name: "denied", guard: guard, address: "blocked.test:80",
			wantKind: errs.PermissionDenied, wantCode: CodeDialDenied, wantHint: "The host is on the block list."},
		{name: "plain error", guard: guard, address: "offline.test:80",
			wantKind: errs.PermissionDenied, wantCode: CodeDialDenied, wantIs: offline},
		{name: "own errs error", guard: guard, address: "other.test:80",
			wantKind: errs.Unsupported, wantCode: "test.other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls log
			next := DialerFunc(func(_ context.Context, _, address string) (net.Conn, error) {
				calls.add(address)
				c1, c2 := net.Pipe()
				c2.Close()
				return c1, nil
			})
			conn, err := Guarded(tt.guard)(next).DialContext(t.Context(), "tcp", tt.address)
			if tt.wantKind == "" {
				if err != nil {
					t.Fatalf("DialContext: %v", err)
				}
				conn.Close()
				if got := calls.get(); !slices.Equal(got, []string{tt.address}) {
					t.Errorf("next dialed %v, want %s", got, tt.address)
				}
				return
			}
			e := wantErr(t, err, tt.wantKind, tt.wantCode)
			if e.Hint != tt.wantHint {
				t.Errorf("hint = %q, want %q", e.Hint, tt.wantHint)
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Errorf("errors.Is(err, %v) = false", tt.wantIs)
			}
			if got := calls.get(); len(got) != 0 {
				t.Errorf("a refused dial reached the next dialer: %v", got)
			}
		})
	}
}

// TestDirectGuardResolved checks Options.Guard: it sees the resolved
// address before the connect, and runs before ControlContext.
func TestDirectGuardResolved(t *testing.T) {
	t.Parallel()
	addr := mustServe(t)
	denyLoopback := Guard(func(_ context.Context, c DialCheck) error {
		if c.Resolved.Addr().IsLoopback() {
			return Deny(c, "Connections to this computer are blocked.")
		}
		return nil
	})
	tests := []struct {
		name      string
		guard     Guard
		wantKind  errs.Kind
		wantOrder []string
	}{
		{name: "denied after resolving", guard: denyLoopback, wantKind: errs.PermissionDenied, wantOrder: []string{"guard"}},
		{name: "allowed", guard: AllowAll, wantOrder: []string{"guard", "control"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var order log
			var seen []DialCheck
			var mu sync.Mutex
			d := NewDirect(Options{
				Guard: func(ctx context.Context, c DialCheck) error {
					order.add("guard")
					mu.Lock()
					seen = append(seen, c)
					mu.Unlock()
					return tt.guard(ctx, c)
				},
				ControlContext: func(context.Context, string, string, syscall.RawConn) error {
					order.add("control")
					return nil
				},
			})
			rec := &recorder{}
			conn, err := d.DialContext(WithTrace(t.Context(), rec.trace()), "tcp", addr)

			if got := order.get(); !slices.Equal(got, tt.wantOrder) {
				t.Errorf("hook order = %v, want %v", got, tt.wantOrder)
			}
			mu.Lock()
			if len(seen) != 1 || seen[0].Resolved != netip.MustParseAddrPort(addr) {
				t.Errorf("guard saw %+v, want Resolved %s", seen, addr)
			}
			mu.Unlock()
			if tt.wantKind == "" {
				if err != nil {
					t.Fatalf("DialContext: %v", err)
				}
				conn.Close()
				return
			}
			e := wantErr(t, err, tt.wantKind, CodeDialDenied)
			if e.Hint != "Connections to this computer are blocked." {
				t.Errorf("hint = %q", e.Hint)
			}
			if _, dones := rec.events(); len(dones) != 1 || dones[0].Err != err {
				t.Errorf("DialDone events = %+v, want the refusal", dones)
			}
		})
	}
}

func TestWithTrace(t *testing.T) {
	t.Parallel()
	var l log
	outer := &Trace{
		DialStart: func(i DialStartInfo) { l.add("outer start " + i.Address) },
		DialDone:  func(i DialDoneInfo) { l.add("outer done " + i.Address) },
	}
	inner := &Trace{DialStart: func(i DialStartInfo) { l.add("inner start " + i.Address) }}

	ctx := t.Context()
	if WithTrace(ctx, nil) != ctx {
		t.Error("WithTrace(ctx, nil) changed ctx")
	}
	if ContextTrace(ctx) != nil {
		t.Error("ContextTrace of a plain context is not nil")
	}
	ctx = WithTrace(WithTrace(ctx, outer), inner)
	tr := ContextTrace(ctx)
	tr.dialStart(DialStartInfo{Address: "a"})
	tr.dialDone(DialDoneInfo{Address: "a"})
	(*Trace)(nil).dialStart(DialStartInfo{}) // a nil trace is a no-op

	want := []string{"inner start a", "outer start a", "outer done a"}
	if got := l.get(); !slices.Equal(got, want) {
		t.Errorf("hooks called = %v, want %v", got, want)
	}
}
