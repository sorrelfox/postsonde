package netx

import (
	"context"
	"errors"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
)

func TestParseProxyURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want ProxyURL
		str  string
	}{
		{"socks5h", "socks5h://proxy.lan:1080", ProxyURL{Scheme: "socks5h", Host: "proxy.lan", Port: 1080}, "socks5h://proxy.lan:1080"},
		{"socks5 default port", "socks5://proxy.lan", ProxyURL{Scheme: "socks5", Host: "proxy.lan", Port: 1080}, "socks5://proxy.lan:1080"},
		{"scheme in any case, spaces around", "  SOCKS5H://Proxy.lan:9050/ ", ProxyURL{Scheme: "socks5h", Host: "Proxy.lan", Port: 9050}, "socks5h://Proxy.lan:9050"},
		{"user and password", "socks5h://alice:s3cret@proxy.lan:1080", ProxyURL{Scheme: "socks5h", Host: "proxy.lan", Port: 1080, Username: "alice", Password: "s3cret"}, "socks5h://alice@proxy.lan:1080"},
		{"user only", "socks5://alice@proxy.lan:1080", ProxyURL{Scheme: "socks5", Host: "proxy.lan", Port: 1080, Username: "alice"}, "socks5://alice@proxy.lan:1080"},
		{"percent-encoded credentials", "socks5h://a%40b:p%3Aw%2Fd@proxy.lan", ProxyURL{Scheme: "socks5h", Host: "proxy.lan", Port: 1080, Username: "a@b", Password: "p:w/d"}, "socks5h://a%40b@proxy.lan:1080"},
		{"colon in password", "socks5h://alice:a:b@proxy.lan", ProxyURL{Scheme: "socks5h", Host: "proxy.lan", Port: 1080, Username: "alice", Password: "a:b"}, "socks5h://alice@proxy.lan:1080"},
		{"IPv4", "socks5://127.0.0.1:1081", ProxyURL{Scheme: "socks5", Host: "127.0.0.1", Port: 1081}, "socks5://127.0.0.1:1081"},
		{"IPv6", "socks5h://[::1]:1080", ProxyURL{Scheme: "socks5h", Host: "::1", Port: 1080}, "socks5h://[::1]:1080"},
		{"IPv6 default port", "socks5h://[2001:db8::1]", ProxyURL{Scheme: "socks5h", Host: "2001:db8::1", Port: 1080}, "socks5h://[2001:db8::1]:1080"},
		{"socks4 with user ID", "socks4://bob@proxy.lan", ProxyURL{Scheme: "socks4", Host: "proxy.lan", Port: 1080, Username: "bob"}, "socks4://bob@proxy.lan:1080"},
		{"socks4a", "socks4a://proxy.lan:1080", ProxyURL{Scheme: "socks4a", Host: "proxy.lan", Port: 1080}, "socks4a://proxy.lan:1080"},
		{"http", "http://proxy.lan:3128", ProxyURL{Scheme: "http", Host: "proxy.lan", Port: 3128}, "http://proxy.lan:3128"},
		{"https default port", "https://proxy.lan", ProxyURL{Scheme: "https", Host: "proxy.lan", Port: 443}, "https://proxy.lan:443"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseProxyURL(tt.raw)
			if err != nil {
				t.Fatalf("ParseProxyURL(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("ParseProxyURL(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
			if s := got.String(); s != tt.str {
				t.Errorf("String() = %q, want %q", s, tt.str)
			}
			// The string form parses back to the URL without its password.
			again, err := ParseProxyURL(got.String())
			if err != nil || again != got.WithoutPassword() {
				t.Errorf("ParseProxyURL(String()) = %+v, %v; want %+v", again, err, got.WithoutPassword())
			}
		})
	}
}

func TestParseProxyURLErrors(t *testing.T) {
	t.Parallel()
	const secret = "hunter2secret"
	tests := []struct {
		name, raw, msg string
	}{
		{"empty", "  ", "empty"},
		{"no scheme", "proxy.lan:1080", "no scheme"},
		{"no scheme with credentials", "alice:" + secret + "@proxy.lan:1080", "no scheme"},
		{"unknown scheme", "ftp://proxy.lan", "scheme"},
		{"no host", "socks5h://:1080", "no host"},
		{"credentials but no host", "socks5h://alice:" + secret + "@", "no host"},
		{"bad port", "socks5h://alice:" + secret + "@proxy.lan:99999", "port"},
		{"port zero", "socks5h://proxy.lan:0", "port"},
		{"empty port", "socks5h://proxy.lan:", "port"},
		{"non-numeric port", "socks5h://alice:" + secret + "@proxy.lan:socks", "valid URL"},
		{"path", "socks5h://proxy.lan:1080/tunnel", "path"},
		{"query", "socks5h://proxy.lan:1080?x=1", "query"},
		{"fragment", "socks5h://proxy.lan:1080#top", "fragment"},
		{"empty fragment", "socks5h://proxy.lan:1080#", "fragment"},
		{"unencoded # in password", "socks5h://alice:" + secret + "#@proxy.lan", "valid URL"},
		{"password without user", "socks5h://:" + secret + "@proxy.lan", "no user name"},
		{"socks4 password", "socks4://bob:" + secret + "@proxy.lan", "SOCKS4"},
		{"long user", "socks5h://" + strings.Repeat("u", 256) + "@proxy.lan", "longer than 255"},
		{"long password", "socks5h://alice:" + strings.Repeat("p", 256) + "@proxy.lan", "longer than 255"},
		{"control character", "socks5h://alice:" + secret + "%0A@proxy.lan", "control character"},
		{"space in host", "socks5h://alice:" + secret + "@proxy lan", "valid URL"},
		{"bracketed name", "socks5h://[proxy.lan]:1080", "valid URL"},
		{"colon in an HTTP proxy's user name", "http://a%3Ab:" + secret + "@proxy.lan", "colon"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseProxyURL(tt.raw)
			var e *errs.Error
			if !errors.As(err, &e) || e.Kind != errs.InvalidArgument || e.Code != CodeProxyInvalidURL {
				t.Fatalf("ParseProxyURL(%q) = %v, want invalid_argument %s", tt.raw, err, CodeProxyInvalidURL)
			}
			if !strings.Contains(e.Message, tt.msg) {
				t.Errorf("message %q does not mention %q", e.Message, tt.msg)
			}
			if all := strings.Join(errs.Chain(err), " ") + e.Hint + err.Error(); strings.Contains(all, secret) {
				t.Errorf("the error shows the password: %q", all)
			}
		})
	}
}

func TestProxyURLCheckSupported(t *testing.T) {
	t.Parallel()
	for _, scheme := range ProxySchemes {
		u, err := ParseProxyURL(scheme + "://proxy.lan")
		if err != nil {
			t.Fatal(err)
		}
		err = u.CheckSupported()
		d, derr := u.Dialer(nil, ProxyOptions{})
		if scheme == ProxySOCKS5 || scheme == ProxySOCKS5H {
			if err != nil || derr != nil || d == nil {
				t.Errorf("%s: CheckSupported = %v, Dialer = %v, %v", scheme, err, d, derr)
			}
			if s, ok := d.(*SOCKS5); !ok || s.Via() != scheme+"://proxy.lan:1080" {
				t.Errorf("%s: dialer %T via %v", scheme, d, d)
			}
			continue
		}
		if scheme == ProxyHTTP || scheme == ProxyHTTPS {
			want := map[string]string{ProxyHTTP: "http://proxy.lan:1080", ProxyHTTPS: "https://proxy.lan:443"}[scheme]
			if err != nil || derr != nil {
				t.Errorf("%s: CheckSupported = %v, Dialer error = %v", scheme, err, derr)
			}
			if h, ok := d.(*HTTPProxy); !ok || h.Via() != want {
				t.Errorf("%s: dialer %T via %v, want %s", scheme, d, d, want)
			}
			continue
		}
		// SOCKS-06: socks4 and socks4a.
		if err != nil || derr != nil {
			t.Errorf("%s: CheckSupported = %v, Dialer error = %v", scheme, err, derr)
		}
		if s, ok := d.(*SOCKS4); !ok || s.Via() != scheme+"://proxy.lan:1080" {
			t.Errorf("%s: dialer %T via %v", scheme, d, d)
		}
	}
	// A scheme no URL can have is refused.
	odd := ProxyURL{Scheme: "socks6", Host: "proxy.lan", Port: 1080}
	if err := odd.CheckSupported(); errs.KindOf(err) != errs.Unsupported || !errorsIsCode(err, CodeProxyUnsupported) {
		t.Errorf("socks6: CheckSupported = %v", err)
	}
	if _, err := odd.Dialer(nil, ProxyOptions{}); !errorsIsCode(err, CodeProxyUnsupported) {
		t.Errorf("socks6: Dialer = %v", err)
	}
}

func errorsIsCode(err error, code string) bool {
	e, ok := errors.AsType[*errs.Error](err)
	return ok && e.Code == code
}

func TestProxyFromEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		env       map[string]string
		wantName  string
		wantValue string
	}{
		{"none", nil, "", ""},
		{"upper", map[string]string{"ALL_PROXY": "socks5h://a:1"}, "ALL_PROXY", "socks5h://a:1"},
		{"lower", map[string]string{"all_proxy": " socks5://b:2 "}, "all_proxy", "socks5://b:2"},
		{"upper wins", map[string]string{"ALL_PROXY": "socks5h://a:1", "all_proxy": "socks5://b:2"}, "ALL_PROXY", "socks5h://a:1"},
		{"blank upper is unset", map[string]string{"ALL_PROXY": "  ", "all_proxy": "socks5://b:2"}, "all_proxy", "socks5://b:2"},
		{"other variables are not read", map[string]string{"HTTPS_PROXY": "http://c:3", "SOCKS_PROXY": "socks5://d:4"}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			name, value := ProxyFromEnv(func(k string) string { return tt.env[k] })
			if name != tt.wantName || value != tt.wantValue {
				t.Errorf("ProxyFromEnv = %q, %q; want %q, %q", name, value, tt.wantName, tt.wantValue)
			}
		})
	}
	if n, v := ProxyFromEnv(nil); n != "" || v != "" {
		t.Errorf("ProxyFromEnv(nil) = %q, %q", n, v)
	}
}

func TestIsLoopbackTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		address string
		want    bool
	}{
		{"localhost:80", true},
		{"LOCALHOST.:80", true},
		{"api.localhost:8080", true},
		{"127.0.0.1:80", true},
		{"127.8.9.10:80", true},
		{"[::1]:443", true},
		{"[::ffff:127.0.0.1]:443", true},
		{"::1", true},
		{"[::1]", true},
		{"localhost", true},
		{"localhost.example.com:80", false},
		{"mylocalhost:80", false},
		{"10.0.0.1:80", false},
		{"api.example.test:443", false},
		{"[2001:db8::1]:443", false},
	}
	for _, tt := range tests {
		if got := IsLoopbackTarget(tt.address); got != tt.want {
			t.Errorf("IsLoopbackTarget(%q) = %v, want %v", tt.address, got, tt.want)
		}
	}
}

// echoServer accepts on loopback and echoes what it reads.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func roundTrip(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	defer c.Close()
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != msg {
		t.Fatalf("echo through the tunnel = %q, %v; want %q", buf, err, msg)
	}
}

// TestProxyRouterRemoteDNS is SOCKS-03 from a proxy URL: with socks5h the
// target's host name goes to the proxy as a name and nothing is looked up
// locally; with socks5 it is resolved here and the proxy gets the IP.
func TestProxyRouterRemoteDNS(t *testing.T) {
	t.Parallel()
	target := echoServer(t)
	_, port, _ := net.SplitHostPort(target)
	const name = "api.example.test"

	tests := []struct {
		scheme     string
		wantName   bool
		wantLookup bool
	}{
		{"socks5h", true, false},
		{"socks5", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.scheme, func(t *testing.T) {
			t.Parallel()
			proxy := &netxtest.SOCKS5Server{Username: "alice", Password: "pw", Hosts: map[string]string{name: "127.0.0.1"}}
			addr := proxy.Start(t)
			resolver, lookups := netxtest.RecordingResolver(map[string]string{name: "127.0.0.1"})
			u, err := ParseProxyURL(tt.scheme + "://alice:pw@" + addr)
			if err != nil {
				t.Fatal(err)
			}
			r := NewProxyRouter(NewDirect(Options{Resolver: resolver}),
				func(context.Context) (ProxyURL, error) { return u, nil }, ProxyOptions{Resolver: resolver})
			c, err := Routed(r.Route).DialContext(t.Context(), "tcp", net.JoinHostPort(name, port))
			if err != nil {
				t.Fatalf("dial through %s: %v", tt.scheme, err)
			}
			if via := ViaOf(c); via != tt.scheme+"://"+addr {
				t.Errorf("ViaOf = %q, want %s://%s", via, tt.scheme, addr)
			}
			roundTrip(t, c, "hello "+tt.scheme)

			reqs := proxy.Requests()
			if len(reqs) != 1 {
				t.Fatalf("the proxy got %d CONNECTs, want 1", len(reqs))
			}
			wantHost := "127.0.0.1"
			if tt.wantName {
				wantHost = name
			}
			if reqs[0].Name != tt.wantName || reqs[0].Host != wantHost || strconv.Itoa(reqs[0].Port) != port {
				t.Errorf("CONNECT = %+v, want name=%v host %s port %s", reqs[0], tt.wantName, wantHost, port)
			}
			if creds := proxy.Credentials(); len(creds) != 1 || creds[0] != "alice:pw" {
				t.Errorf("credentials sent = %q", creds)
			}
			names := lookups.Names()
			if tt.wantLookup && !slices.Contains(names, name) {
				t.Errorf("socks5: %s was not resolved locally (lookups %q)", name, names)
			}
			if !tt.wantLookup && len(names) != 0 {
				t.Errorf("socks5h: local DNS lookups %q, want none", names)
			}
		})
	}
}

// countingDialer counts its dials; a pointer, so tests can compare it.
type countingDialer struct {
	next Dialer
	n    atomic.Int32
}

func (d *countingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.n.Add(1)
	return d.next.DialContext(ctx, network, address)
}

func TestProxyRouter(t *testing.T) {
	t.Parallel()
	target := echoServer(t)
	proxy := &netxtest.SOCKS5Server{}
	addr := proxy.Start(t)
	socks, _ := ParseProxyURL("socks5h://" + addr)

	counting := &countingDialer{next: NewDirect(Options{})}
	direct := &counting.n

	t.Run("no proxy connects directly", func(t *testing.T) {
		r := NewProxyRouter(counting, func(context.Context) (ProxyURL, error) { return ProxyURL{}, nil }, ProxyOptions{})
		d, err := r.Route(t.Context(), "tcp", "api.example.test:443")
		if err != nil || d != Dialer(counting) {
			t.Fatalf("Route = %v, %v; want the direct dialer", d, err)
		}
		if d, err := NewProxyRouter(counting, nil, ProxyOptions{}).Route(t.Context(), "tcp", "a.test:1"); err != nil || d != Dialer(counting) {
			t.Fatalf("nil source: Route = %v, %v", d, err)
		}
	})

	t.Run("loopback targets bypass the proxy", func(t *testing.T) {
		r := NewProxyRouter(counting, func(context.Context) (ProxyURL, error) { return socks, nil }, ProxyOptions{})
		for _, a := range []string{target, "localhost:80", "[::1]:80"} {
			if d, err := r.Route(t.Context(), "tcp", a); err != nil || d != Dialer(counting) {
				t.Errorf("Route(%s) = %v, %v; want direct", a, d, err)
			}
		}
		before := len(proxy.Requests())
		c, err := Routed(r.Route).DialContext(t.Context(), "tcp", target)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip(t, c, "direct")
		if len(proxy.Requests()) != before {
			t.Error("a loopback target went through the proxy")
		}
	})

	t.Run("other targets go through the proxy, one dialer per proxy", func(t *testing.T) {
		current := socks
		r := NewProxyRouter(counting, func(context.Context) (ProxyURL, error) { return current, nil }, ProxyOptions{})
		d1, err := r.Route(t.Context(), "tcp", "api.example.test:443")
		if err != nil {
			t.Fatal(err)
		}
		d2, _ := r.Route(t.Context(), "tcp", "other.example.test:443")
		if d1 != d2 || d1.(*SOCKS5).Via() != "socks5h://"+addr {
			t.Errorf("dialers %v and %v, want the same SOCKS5 dialer", d1, d2)
		}
		current, _ = ParseProxyURL("socks5://" + addr)
		d3, _ := r.Route(t.Context(), "tcp", "api.example.test:443")
		if d3 == d1 || d3.(*SOCKS5).Via() != "socks5://"+addr {
			t.Errorf("after a change: dialer %v, want a new socks5 one", d3)
		}
	})

	t.Run("an invalid or unsupported proxy fails the dial", func(t *testing.T) {
		n := direct.Load()
		bad := errs.New(errs.InvalidArgument, "bad proxy").WithCode(CodeProxyInvalidURL)
		r := NewProxyRouter(counting, func(context.Context) (ProxyURL, error) { return ProxyURL{}, bad }, ProxyOptions{})
		_, err := Routed(r.Route).DialContext(t.Context(), "tcp", "api.example.test:443")
		if !errorsIsCode(err, CodeProxyInvalidURL) {
			t.Fatalf("err = %v, want %s", err, CodeProxyInvalidURL)
		}
		if e, _ := errors.AsType[*errs.Error](err); e.Details["address"] != "api.example.test:443" {
			t.Errorf("details %v", e.Details)
		}
		odd := ProxyURL{Scheme: "socks6", Host: "127.0.0.1", Port: 1080}
		r = NewProxyRouter(counting, func(context.Context) (ProxyURL, error) { return odd, nil }, ProxyOptions{})
		_, err = Routed(r.Route).DialContext(t.Context(), "tcp", "api.example.test:443")
		if !errorsIsCode(err, CodeProxyUnsupported) {
			t.Fatalf("socks6: err = %v, want %s", err, CodeProxyUnsupported)
		}
		if e, _ := errors.AsType[*errs.Error](err); e.Details["via"] != "socks6://127.0.0.1:1080" {
			t.Errorf("details %v", e.Details)
		}
		plain := errors.New("settings unreadable")
		r = NewProxyRouter(counting, func(context.Context) (ProxyURL, error) { return ProxyURL{}, plain }, ProxyOptions{})
		_, err = Routed(r.Route).DialContext(t.Context(), "tcp", "api.example.test:443")
		if !errors.Is(err, plain) {
			t.Fatalf("err = %v, want it to wrap the source's error", err)
		}
		if direct.Load() != n {
			t.Error("a failed route connected directly")
		}
	})
}
