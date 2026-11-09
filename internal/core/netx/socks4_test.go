package netx

import (
	"context"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
)

// TestSOCKS4 (SOCKS-06): SOCKS4 sends an IPv4 address (a host name
// resolved here first, IPv4 only), SOCKS4a the name unresolved; the user
// ID goes with the request; the tunnel carries bytes both ways; the trace,
// the hop and ViaOf name the proxy.
func TestSOCKS4(t *testing.T) {
	t.Parallel()
	target := echoServer(t)
	_, port, _ := net.SplitHostPort(target)
	const name = "api.example.test"
	tests := []struct {
		name     string
		remote   bool
		host     string
		userID   string
		wantName bool
		wantHost string
		lookup   bool
	}{
		{name: "socks4, IP", host: "127.0.0.1", wantHost: "127.0.0.1"},
		{name: "socks4, name resolved here", host: name, wantHost: "127.0.0.1", lookup: true},
		{name: "socks4a, name to the proxy", remote: true, host: name, wantName: true, wantHost: name},
		{name: "socks4a, IP", remote: true, host: "127.0.0.1", wantHost: "127.0.0.1"},
		{name: "socks4a, user ID", remote: true, host: name, userID: "bob", wantName: true, wantHost: name},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := &netxtest.SOCKS4Server{UserID: tt.userID, Hosts: map[string]string{name: "127.0.0.1"}}
			proxy := srv.Start(t)
			resolver, lookups := netxtest.RecordingResolver(map[string]string{name: "127.0.0.1"})
			d, err := NewSOCKS4(SOCKS4Config{Address: proxy, RemoteDNS: tt.remote, UserID: tt.userID, Resolver: resolver}, nil)
			if err != nil {
				t.Fatal(err)
			}
			scheme, version := "socks4", "SOCKS4"
			if tt.remote {
				scheme, version = "socks4a", "SOCKS4a"
			}
			via := scheme + "://" + proxy
			if d.Via() != via {
				t.Errorf("Via = %q, want %q", d.Via(), via)
			}
			rec := &recorder{}
			dest := net.JoinHostPort(tt.host, port)
			c, err := d.DialContext(WithTrace(t.Context(), rec.trace()), "tcp", dest)
			if err != nil {
				t.Fatal(err)
			}
			if ViaOf(c) != via || c.RemoteAddr().String() != proxy {
				t.Errorf("ViaOf = %q, RemoteAddr %v", ViaOf(c), c.RemoteAddr())
			}
			roundTrip(t, c, "through socks4")

			reqs := srv.Requests()
			if len(reqs) != 1 || reqs[0].Name != tt.wantName || reqs[0].Host != tt.wantHost || strconv.Itoa(reqs[0].Port) != port || reqs[0].UserID != tt.userID {
				t.Fatalf("CONNECTs = %+v, want name=%v %s:%s user %q", reqs, tt.wantName, tt.wantHost, port, tt.userID)
			}
			if names := lookups.Names(); tt.lookup != slices.Contains(names, name) || (!tt.lookup && len(names) != 0) {
				t.Errorf("local lookups %q, want lookup=%v", names, tt.lookup)
			}
			hop := lastHop(t, rec)
			wantAuth := AuthNone
			if tt.userID != "" {
				wantAuth = AuthUserID
			}
			if hop.Scheme != scheme || hop.Version != version || hop.Proxy != proxy || hop.Auth != wantAuth || hop.RemoteDNS != tt.remote ||
				!hop.Replied || hop.Reply != 90 || hop.ReplyText != "granted" || hop.Connect <= 0 || (hop.Lookup > 0) != tt.lookup {
				t.Errorf("hop = %+v", hop)
			}
			if s := hop.String(); !strings.Contains(s, version+" "+proxy) || !strings.Contains(s, "reply 90 granted") {
				t.Errorf("String() = %q", s)
			}
		})
	}
}

// TestSOCKS4Errors: every failure has its code, kind and details, and the
// ones SOCKS4 cannot carry never reach the proxy.
func TestSOCKS4Errors(t *testing.T) {
	t.Parallel()
	target := echoServer(t)
	_, port, _ := net.SplitHostPort(target)
	tests := []struct {
		name     string
		srv      *netxtest.SOCKS4Server
		proxy    func(t *testing.T) string // instead of srv
		cfg      SOCKS4Config
		network  string
		address  string
		kind     errs.Kind
		code     string
		reply    string
		untouchd bool
	}{
		{name: "rejected", srv: &netxtest.SOCKS4Server{Reply: 91}, address: "127.0.0.1:" + port,
			kind: errs.Network, code: CodeSOCKS4Rejected, reply: "91"},
		{name: "unknown name", srv: &netxtest.SOCKS4Server{}, cfg: SOCKS4Config{RemoteDNS: true}, address: "nowhere.example.test:80",
			kind: errs.Network, code: CodeSOCKS4Rejected, reply: "91"},
		{name: "identd unreachable", srv: &netxtest.SOCKS4Server{Reply: 92}, address: "127.0.0.1:" + port,
			kind: errs.Network, code: CodeSOCKS4IdentdUnreachable, reply: "92"},
		{name: "user ID mismatch", srv: &netxtest.SOCKS4Server{UserID: "bob"}, cfg: SOCKS4Config{UserID: "eve"}, address: "127.0.0.1:" + port,
			kind: errs.Network, code: CodeSOCKS4IdentdMismatch, reply: "93"},
		{name: "unknown reply", srv: &netxtest.SOCKS4Server{Reply: 99}, address: "127.0.0.1:" + port,
			kind: errs.Network, code: CodeSOCKSUnknownReply, reply: "99"},
		{name: "IPv6 target", srv: &netxtest.SOCKS4Server{}, address: "[::1]:" + port,
			kind: errs.Unsupported, code: CodeSOCKS4IPv6, untouchd: true},
		{name: "IPv6 network", srv: &netxtest.SOCKS4Server{}, network: "tcp6", address: "127.0.0.1:" + port,
			kind: errs.Unsupported, code: CodeSOCKSUnsupportedNetwork, untouchd: true},
		{name: "udp", srv: &netxtest.SOCKS4Server{}, network: "udp", address: "127.0.0.1:" + port,
			kind: errs.Unsupported, code: CodeSOCKSUnsupportedNetwork, untouchd: true},
		{name: "local DNS failure", srv: &netxtest.SOCKS4Server{}, cfg: SOCKS4Config{Resolver: nxdomainResolver()}, address: "api.example.invalid:80",
			kind: errs.Network, code: CodeDNSNotFound, untouchd: true},
		{name: "unreachable", proxy: closedPort, address: "127.0.0.1:" + port, kind: errs.Network, code: CodeSOCKSProxyUnreachable},
		{name: "a SOCKS5 proxy", proxy: func(t *testing.T) string { return (&socksServer{}).start(t) }, address: "127.0.0.1:" + port,
			kind: errs.Protocol, code: CodeSOCKSProtocolError},
		{name: "silent proxy", proxy: func(t *testing.T) string { return (&socksServer{Hang: true}).start(t) }, cfg: SOCKS4Config{Timeout: 50 * time.Millisecond},
			address: "127.0.0.1:" + port, kind: errs.Timeout, code: CodeSOCKSTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var proxy string
			if tt.srv != nil {
				proxy = tt.srv.Start(t)
			} else {
				proxy = tt.proxy(t)
			}
			cfg := tt.cfg
			cfg.Address = proxy
			d, err := NewSOCKS4(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			network := tt.network
			if network == "" {
				network = "tcp"
			}
			_, err = d.DialContext(t.Context(), network, tt.address)
			e := wantErr(t, err, tt.kind, tt.code)
			if e.Details["via"] != "socks4://"+proxy && e.Details["via"] != "socks4a://"+proxy {
				t.Errorf("via = %q", e.Details["via"])
			}
			if e.Details["address"] != tt.address || e.Details["socksReply"] != tt.reply {
				t.Errorf("details = %v", e.Details)
			}
			if e.Hint == "" && tt.code != CodeSOCKSUnsupportedNetwork && tt.code != CodeDNSNotFound {
				t.Errorf("no hint: %v", e)
			}
			if tt.untouchd && tt.srv.Accepted() != 0 {
				t.Errorf("the proxy got %d connections, want none", tt.srv.Accepted())
			}
			if step := ProxyFailureStep(err); tt.code == CodeSOCKS4IdentdMismatch && step != ProxyStepAuth || tt.code == CodeSOCKS4Rejected && step != ProxyStepTarget {
				t.Errorf("step = %s", step)
			}
		})
	}

	t.Run("cancel", func(t *testing.T) {
		t.Parallel()
		proxy := (&socksServer{Hang: true}).start(t)
		d, _ := NewSOCKS4(SOCKS4Config{Address: proxy, Timeout: -1}, nil)
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(50*time.Millisecond, cancel)
		_, err := d.DialContext(ctx, "tcp", "127.0.0.1:"+port)
		wantErr(t, err, errs.Cancelled, CodeCancelled)
	})
	t.Run("invalid config", func(t *testing.T) {
		t.Parallel()
		for _, cfg := range []SOCKS4Config{{Address: "proxy"}, {Address: "proxy:1080", UserID: strings.Repeat("u", 256)}, {Address: "proxy:1080", UserID: "a\x00b"}} {
			if _, err := NewSOCKS4(cfg, nil); !errorsIsCode(err, CodeSOCKSInvalidConfig) {
				t.Errorf("NewSOCKS4(%+v) = %v", cfg, err)
			}
		}
	})
}

// TestProxyRouterSOCKS4 (SOCKS-06): socks4a:// and socks4:// proxy URLs
// route through the SOCKS4 dialer with the user ID of the URL, and the
// connection test works through them.
func TestProxyRouterSOCKS4(t *testing.T) {
	t.Parallel()
	target := echoServer(t)
	_, port, _ := net.SplitHostPort(target)
	const name = "api.example.test"
	for _, scheme := range []string{"socks4a", "socks4"} {
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()
			srv := &netxtest.SOCKS4Server{UserID: "bob", Hosts: map[string]string{name: "127.0.0.1"}}
			addr := srv.Start(t)
			resolver, lookups := netxtest.RecordingResolver(map[string]string{name: "127.0.0.1"})
			u, err := ParseProxyURL(scheme + "://bob@" + addr)
			if err != nil {
				t.Fatal(err)
			}
			r := NewProxyRouter(NewDirect(Options{Resolver: resolver}), func(context.Context) (ProxyURL, error) { return u, nil }, ProxyOptions{Resolver: resolver})
			c, err := Routed(r.Route).DialContext(t.Context(), "tcp", net.JoinHostPort(name, port))
			if err != nil {
				t.Fatal(err)
			}
			if ViaOf(c) != scheme+"://"+addr {
				t.Errorf("ViaOf = %q", ViaOf(c))
			}
			roundTrip(t, c, "hello "+scheme)
			reqs := srv.Requests()
			if len(reqs) != 1 || reqs[0].UserID != "bob" || reqs[0].Name != (scheme == "socks4a") {
				t.Errorf("CONNECTs = %+v", reqs)
			}
			if (len(lookups.Names()) == 0) != (scheme == "socks4a") {
				t.Errorf("lookups = %q", lookups.Names())
			}
			res := r.Test(t.Context(), net.JoinHostPort(name, port))
			if !res.OK() || res.Hop == nil || res.Hop.Auth != AuthUserID || res.Hop.Reply != 90 {
				t.Errorf("test = %+v, %v", res, res.Err)
			}
		})
	}
}
