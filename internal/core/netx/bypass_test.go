package netx

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
)

// TestBypassMatch (SOCKS-09): every kind of entry, with and without a
// port, names and IPv4 and IPv6 targets.
func TestBypassMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		entry    string
		match    []string
		no       []string
		wantText string
	}{
		{"*", []string{"a.b:1", "10.0.0.1:443", "[::1]:80", "x"}, nil, "*"},
		{"*:8080", []string{"a.b:8080", "10.0.0.1:8080"}, []string{"a.b:80"}, "*:8080"},
		{"example.com", []string{"example.com:443", "api.example.com:80", "a.b.example.com:1", "EXAMPLE.COM.:443", "example.com"},
			[]string{"notexample.com:443", "example.com.evil.test:443", "com:443", "93.184.216.34:443"}, "example.com"},
		{"Example.COM.", []string{"example.com:443"}, nil, "example.com"},
		{".example.com", []string{"api.example.com:443", "a.b.example.com:1"}, []string{"example.com:443", "xexample.com:443"}, ".example.com"},
		{"*.local", []string{"printer.local:631", "a.b.local:80"}, []string{"local:80", "localhost.test:80"}, ".local"},
		{"<local>", []string{"intranet:80", "wiki:443"}, []string{"wiki.corp:443", "10.0.0.1:80", "[::1]:80"}, "<local>"},
		{"example.com:8443", []string{"example.com:8443", "api.example.com:8443"}, []string{"example.com:443", "example.com"}, "example.com:8443"},
		{"*.corp:443", []string{"git.corp:443"}, []string{"git.corp:80", "corp:443"}, ".corp:443"},
		{"10.1.2.3", []string{"10.1.2.3:443", "10.1.2.3", "[::ffff:10.1.2.3]:443"}, []string{"10.1.2.4:443", "host.test:443"}, "10.1.2.3"},
		{"10.1.2.3:80", []string{"10.1.2.3:80"}, []string{"10.1.2.3:443"}, "10.1.2.3:80"},
		{"10.0.0.0/8", []string{"10.200.1.1:443", "10.0.0.0:1", "[::ffff:10.9.9.9]:80"}, []string{"11.0.0.1:443", "ten.test:443"}, "10.0.0.0/8"},
		{"10.9.8.7/8", []string{"10.1.1.1:443"}, nil, "10.0.0.0/8"},
		{"169.254/16", []string{"169.254.3.4:80"}, []string{"169.255.0.1:80"}, "169.254.0.0/16"},
		{"::1", []string{"[::1]:80"}, []string{"[::2]:80"}, "::1"},
		{"[2001:db8::1]", []string{"[2001:db8::1]:443", "[2001:DB8:0::1]:443"}, []string{"[2001:db8::2]:443"}, "2001:db8::1"},
		{"[2001:db8::1]:8080", []string{"[2001:db8::1]:8080"}, []string{"[2001:db8::1]:80"}, "[2001:db8::1]:8080"},
		{"2001:db8::/32", []string{"[2001:db8:1::5]:443", "[2001:db8::1%eth0]:443"}, []string{"[2001:db9::1]:443", "10.0.0.1:443"}, "2001:db8::/32"},
		{"[fd00::]/8", []string{"[fd12::1]:443"}, []string{"[fe80::1]:443"}, "fd00::/8"},
		{"::ffff:10.0.0.0/104", []string{"10.1.1.1:80"}, []string{"11.1.1.1:80"}, "10.0.0.0/8"},
		{"xn--bcher-kva.example", []string{"xn--bcher-kva.example:443"}, nil, "xn--bcher-kva.example"},
		{"my_host.corp", []string{"my_host.corp:80"}, nil, "my_host.corp"},
	}
	for _, tt := range tests {
		t.Run(tt.entry, func(t *testing.T) {
			t.Parallel()
			b, err := ParseBypass([]string{" " + tt.entry + " "})
			if err != nil {
				t.Fatalf("ParseBypass(%q): %v", tt.entry, err)
			}
			if b.String() != tt.wantText {
				t.Errorf("String() = %q, want %q", b.String(), tt.wantText)
			}
			for _, a := range tt.match {
				if !b.Match(a) {
					t.Errorf("%q does not match %q", tt.entry, a)
				}
			}
			for _, a := range tt.no {
				if b.Match(a) {
					t.Errorf("%q matches %q", tt.entry, a)
				}
			}
			// The canonical form parses to the same list.
			again, err := ParseBypass(b.Entries())
			if err != nil || again.String() != b.String() {
				t.Errorf("ParseBypass(Entries()) = %q, %v", again.String(), err)
			}
		})
	}
	if (Bypass{}).Match("example.com:443") || !(Bypass{}).IsZero() {
		t.Error("the zero Bypass matches something")
	}
}

func TestParseBypassErrors(t *testing.T) {
	t.Parallel()
	for _, entry := range []string{
		"", "  ", "a b", "exa*mple.com", "*example.com", "example.*", "a..b", "example.com:0", "example.com:65536",
		"example.com:http", "1::2::3", "[::1", "[::1]x", "[10.0.0.1]", "10.0.0.0/33", "fd00::/129", "10.0.0.0/8:443",
		"fe80::1%eth0", "exam!ple.com", "*.", ".", "https://example.com",
	} {
		_, err := ParseBypass([]string{"ok.example", entry})
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != CodeProxyInvalidBypass || e.Kind != errs.InvalidArgument || e.Details["entry"] != entry {
			t.Errorf("ParseBypass(%q) = %v, want %s with the entry", entry, err, CodeProxyInvalidBypass)
		}
	}
	long := make([]string, MaxBypassEntries+1)
	for i := range long {
		long[i] = "a.example"
	}
	if _, err := ParseBypass(long); !errorsIsCode(err, CodeProxyInvalidBypass) {
		t.Errorf("too many entries: %v", err)
	}
	if b, err := ParseBypass(nil); err != nil || !b.IsZero() {
		t.Errorf("ParseBypass(nil) = %v, %v", b, err)
	}
}

// TestParseBypassList: NO_PROXY and the OS lists, separated by commas,
// semicolons or spaces; entries that cannot be used are skipped, not
// fatal, and duplicates are dropped.
func TestParseBypassList(t *testing.T) {
	t.Parallel()
	b, skipped := ParseBypassList(" example.com, .corp;10.0.0.0/8  [::1]:8080,,bad*entry;example.com ;<local>\n*.test")
	want := "example.com,.corp,10.0.0.0/8,[::1]:8080,<local>,.test"
	if b.String() != want {
		t.Errorf("list = %q, want %q", b.String(), want)
	}
	if !slices.Equal(skipped, []string{"bad*entry"}) {
		t.Errorf("skipped = %q", skipped)
	}
	if !b.Match("git.corp:22") || !b.Match("10.1.1.1:443") || b.Match("[::1]:80") {
		t.Error("the list does not match as parsed")
	}
	joined := JoinBypass(b, mustBypass(t, "other.test", ".corp"))
	if joined.String() != want+",other.test" {
		t.Errorf("JoinBypass = %q", joined.String())
	}
	if JoinBypass().String() != "" {
		t.Error("JoinBypass() is not empty")
	}
}

func TestNoProxyFromEnv(t *testing.T) {
	t.Parallel()
	env := map[string]string{"no_proxy": " .corp "}
	if n, v := NoProxyFromEnv(func(k string) string { return env[k] }); n != "no_proxy" || v != ".corp" {
		t.Errorf("NoProxyFromEnv = %q, %q", n, v)
	}
	env["NO_PROXY"] = "example.com"
	if n, v := NoProxyFromEnv(func(k string) string { return env[k] }); n != "NO_PROXY" || v != "example.com" {
		t.Errorf("NoProxyFromEnv = %q, %q; NO_PROXY first", n, v)
	}
	if n, v := NoProxyFromEnv(nil); n != "" || v != "" {
		t.Errorf("nil getenv: %q, %q", n, v)
	}
}

// TestProxyRouterBypass: a bypassed target connects directly, for Route and
// ForwardProxy, and the proxy is never asked; others go through it. A
// changed bypass list is a change of the proxy in effect.
func TestProxyRouterBypass(t *testing.T) {
	t.Parallel()
	target := echoServer(t)
	proxy := &netxtest.SOCKS5Server{}
	addr := proxy.Start(t)
	socks, _ := ParseProxyURL("socks5h://" + addr)
	httpProxy, _ := ParseProxyURL("http://" + addr)
	counting := &countingDialer{next: NewDirect(Options{})}

	cfg := ProxyConfig{Proxy: socks, Bypass: mustBypass(t, "*.internal.test", "192.0.2.0/24", "[2001:db8::5]:443")}
	var current atomic.Pointer[ProxyConfig]
	current.Store(&cfg)
	r := NewProxyConfigRouter(counting, func(context.Context) (ProxyConfig, error) { return *current.Load(), nil }, ProxyOptions{})
	var changes atomic.Int32
	r.OnChange(func() { changes.Add(1) })

	for _, a := range []string{"api.internal.test:443", "192.0.2.10:80", "[2001:db8::5]:443", "localhost:1"} {
		if d, err := r.Route(t.Context(), "tcp", a); err != nil || d != Dialer(counting) {
			t.Errorf("Route(%s) = %v, %v; want direct", a, d, err)
		}
	}
	for _, a := range []string{"internal.test:443", "192.0.3.1:80", "[2001:db8::5]:80", "example.test:443"} {
		if d, err := r.Route(t.Context(), "tcp", a); err != nil || d == Dialer(counting) {
			t.Errorf("Route(%s) = %v, %v; want the proxy", a, d, err)
		}
	}

	// A bypassed loopback target: connected directly, the proxy not asked.
	withLoopback := ProxyConfig{Proxy: socks, Bypass: mustBypass(t, "127.0.0.0/8")}
	current.Store(&withLoopback)
	before := len(proxy.Requests())
	c, err := Routed(r.Route).DialContext(t.Context(), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, c, "bypassed")
	if len(proxy.Requests()) != before {
		t.Error("a bypassed target went through the proxy")
	}
	if changes.Load() != 1 {
		t.Errorf("%d changes after a new bypass list, want 1", changes.Load())
	}

	// ForwardProxy leaves bypassed targets out.
	fwd := ProxyConfig{Proxy: httpProxy, Bypass: mustBypass(t, "plain.test")}
	current.Store(&fwd)
	if p := r.ForwardProxy(t.Context(), "www.plain.test:80"); p != nil {
		t.Errorf("ForwardProxy of a bypassed target = %v", p.Via())
	}
	if p := r.ForwardProxy(t.Context(), "other.test:80"); p == nil {
		t.Error("ForwardProxy of another target = nil")
	}

	// The connection test goes through the proxy anyway, and says so.
	res := r.Test(t.Context(), "www.plain.test:1")
	if !res.Bypassed || res.Proxy != httpProxy {
		t.Errorf("Test = %+v, want Bypassed through %v", res, httpProxy)
	}
	if res := r.Test(t.Context(), "other.test:1"); res.Bypassed {
		t.Error("Test says a target that is not bypassed is")
	}
}

func mustBypass(t *testing.T, entries ...string) Bypass {
	t.Helper()
	b, err := ParseBypass(entries)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
