package netx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// socksServer is an in-process SOCKS5 server for tests. After a
// successful CONNECT it echoes what the client sends, so tests can check
// that the tunnel carries data and starts clean.
type socksServer struct {
	// Auth: when User is set, the server accepts only username/password
	// with these credentials. Methods overrides the method it picks (e.g.
	// 0xff); nil picks by User.
	User, Pass string
	Method     *byte
	// Reply is the CONNECT reply code; 0 succeeds.
	Reply byte
	// BindName makes a successful reply carry a domain-name BND.ADDR, and
	// BindIPv6 an IPv6 one, instead of IPv4.
	BindName, BindIPv6 bool
	// Hang makes the server accept and then never answer.
	Hang bool
	// Version is the SOCKS version the server answers the greeting with;
	// 0 means 5.
	Version byte
	// CloseAfterGreeting closes the connection instead of answering.
	CloseAfterGreeting bool

	addr     string
	accepted atomic.Int32

	mu       sync.Mutex
	methods  [][]byte
	creds    []string // "user:pass"
	requests []socksRequest
}

// socksRequest is a CONNECT the server received.
type socksRequest struct {
	atyp byte
	host string // IP address or name
	port int
}

func (s *socksServer) start(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	t.Cleanup(func() {
		close(done)
		ln.Close()
		wg.Wait()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.accepted.Add(1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				go func() { <-done; c.Close() }()
				s.serve(c)
			}()
		}
	}()
	s.addr = ln.Addr().String()
	return s.addr
}

func (s *socksServer) serve(c net.Conn) {
	if s.Hang {
		_, _ = io.Copy(io.Discard, c)
		return
	}
	var head [2]byte
	if _, err := io.ReadFull(c, head[:]); err != nil || head[0] != 5 {
		return
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	s.mu.Lock()
	s.methods = append(s.methods, methods)
	s.mu.Unlock()
	if s.CloseAfterGreeting {
		return
	}
	ver := s.Version
	if ver == 0 {
		ver = 5
	}
	method := byte(0x00)
	switch {
	case s.Method != nil:
		method = *s.Method
	case s.User != "":
		method = 0x02
		if !strings.Contains(string(methods), "\x02") {
			method = 0xff
		}
	}
	if _, err := c.Write([]byte{ver, method}); err != nil || method == 0xff || ver != 5 {
		return
	}
	if method == 0x02 {
		var b [2]byte
		if _, err := io.ReadFull(c, b[:]); err != nil {
			return
		}
		user := make([]byte, b[1])
		if _, err := io.ReadFull(c, user); err != nil {
			return
		}
		if _, err := io.ReadFull(c, b[:1]); err != nil {
			return
		}
		pass := make([]byte, b[0])
		if _, err := io.ReadFull(c, pass); err != nil {
			return
		}
		s.mu.Lock()
		s.creds = append(s.creds, string(user)+":"+string(pass))
		s.mu.Unlock()
		status := byte(0)
		if string(user) != s.User || string(pass) != s.Pass {
			status = 1
		}
		if _, err := c.Write([]byte{1, status}); err != nil || status != 0 {
			return
		}
	}
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil || req[0] != 5 || req[1] != 1 {
		return
	}
	r := socksRequest{atyp: req[3]}
	switch req[3] {
	case 1, 4:
		ip := make([]byte, 4)
		if req[3] == 4 {
			ip = make([]byte, 16)
		}
		if _, err := io.ReadFull(c, ip); err != nil {
			return
		}
		a, _ := netip.AddrFromSlice(ip)
		r.host = a.String()
	case 3:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return
		}
		name := make([]byte, l[0])
		if _, err := io.ReadFull(c, name); err != nil {
			return
		}
		r.host = string(name)
	default:
		return
	}
	var port [2]byte
	if _, err := io.ReadFull(c, port[:]); err != nil {
		return
	}
	r.port = int(port[0])<<8 | int(port[1])
	s.mu.Lock()
	s.requests = append(s.requests, r)
	s.mu.Unlock()
	reply := []byte{5, s.Reply, 0}
	switch {
	case s.BindName:
		reply = append(reply, 3, 9)
		reply = append(reply, "proxy.lan"...)
	case s.BindIPv6:
		reply = append(reply, 4)
		reply = append(reply, netip.IPv6Loopback().AsSlice()...)
	default:
		reply = append(reply, 1, 127, 0, 0, 1)
	}
	reply = append(reply, 0x04, 0x38)
	if _, err := c.Write(reply); err != nil || s.Reply != 0 {
		return
	}
	_, _ = io.Copy(c, c)
}

func (s *socksServer) lastRequest(t *testing.T) socksRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("the proxy got no CONNECT request")
	}
	return s.requests[len(s.requests)-1]
}

func newSOCKS(t *testing.T, cfg SOCKS5Config) *SOCKS5 {
	t.Helper()
	d, err := NewSOCKS5(cfg, NewDirect(Options{}))
	if err != nil {
		t.Fatalf("NewSOCKS5: %v", err)
	}
	return d
}

// echo checks that conn carries data both ways.
func echo(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var b [4]byte
	if _, err := io.ReadFull(conn, b[:]); err != nil || string(b[:]) != "ping" {
		t.Fatalf("read = %q, %v, want ping", b[:], err)
	}
}

func TestSOCKS5AddressTypes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		remoteDNS bool
		network   string
		address   string
		bind      func(*socksServer)
		atyp      byte
		host      func(string) bool
		port      int
	}{
		{name: "ipv4", network: "tcp", address: "192.0.2.10:80", atyp: 1, host: is("192.0.2.10"), port: 80},
		{name: "ipv6", network: "tcp", address: "[2001:db8::1]:443", atyp: 4, host: is("2001:db8::1"), port: 443},
		{name: "ipv6 zone dropped", network: "tcp6", address: "[fe80::1%lo0]:22", atyp: 4, host: is("fe80::1"), port: 22},
		{name: "ipv4-mapped ipv6 sent as ipv4", network: "tcp", address: "[::ffff:10.0.0.1]:8080", atyp: 1, host: is("10.0.0.1"), port: 8080},
		{name: "domain, remote dns", remoteDNS: true, network: "tcp", address: "api.internal.invalid:8443", atyp: 3, host: is("api.internal.invalid"), port: 8443},
		{name: "ip literal, remote dns", remoteDNS: true, network: "tcp", address: "198.51.100.7:1", atyp: 1, host: is("198.51.100.7"), port: 1},
		{name: "domain, local dns", network: "tcp4", address: "localhost:9000", atyp: 1, host: is("127.0.0.1"), port: 9000},
		{name: "service name port", remoteDNS: true, network: "tcp", address: "example.invalid:https", atyp: 3, host: is("example.invalid"), port: 443},
		{name: "domain bind address", remoteDNS: true, network: "tcp", address: "a.invalid:1", bind: func(s *socksServer) { s.BindName = true }, atyp: 3, host: is("a.invalid"), port: 1},
		{name: "ipv6 bind address", network: "tcp", address: "192.0.2.1:2", bind: func(s *socksServer) { s.BindIPv6 = true }, atyp: 1, host: is("192.0.2.1"), port: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := &socksServer{}
			if tt.bind != nil {
				tt.bind(srv)
			}
			proxy := srv.start(t)
			d := newSOCKS(t, SOCKS5Config{Address: proxy, RemoteDNS: tt.remoteDNS})
			conn, err := d.DialContext(t.Context(), tt.network, tt.address)
			if err != nil {
				t.Fatalf("DialContext: %v", err)
			}
			defer conn.Close()
			echo(t, conn)
			r := srv.lastRequest(t)
			if r.atyp != tt.atyp || !tt.host(r.host) || r.port != tt.port {
				t.Errorf("CONNECT = %+v, want atyp %d port %d", r, tt.atyp, tt.port)
			}
			if got := conn.RemoteAddr().String(); got != proxy {
				t.Errorf("RemoteAddr = %s, want the proxy %s", got, proxy)
			}
			scheme := "socks5://"
			if tt.remoteDNS {
				scheme = "socks5h://"
			}
			if got := ViaOf(conn); got != scheme+proxy {
				t.Errorf("ViaOf = %q, want %q", got, scheme+proxy)
			}
			if _, ok := conn.(interface{ CloseWrite() error }); !ok {
				t.Error("the tunnel lost CloseWrite of the TCP connection")
			}
		})
	}
}

func is(want string) func(string) bool { return func(got string) bool { return got == want } }

func TestSOCKS5LocalDNSFailureNeverReachesProxy(t *testing.T) {
	t.Parallel()
	srv := &socksServer{}
	proxy := srv.start(t)
	d, err := NewSOCKS5(SOCKS5Config{Address: proxy, Resolver: nxdomainResolver()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.DialContext(t.Context(), "tcp", "nothing.invalid:80")
	e := wantErr(t, err, errs.Network, CodeDNSNotFound)
	if e.Details["via"] != "socks5://"+proxy {
		t.Errorf("via detail = %q", e.Details["via"])
	}
	if n := srv.accepted.Load(); n != 0 {
		t.Errorf("the proxy got %d connections, want none", n)
	}
}

func TestSOCKS5Auth(t *testing.T) {
	t.Parallel()
	gssapiOnly := byte(0xff)
	tests := []struct {
		name        string
		server      *socksServer
		user, pass  string
		wantMethods string
		wantCreds   string
		wantKind    errs.Kind
		wantCode    string
	}{
		{name: "no auth", server: &socksServer{}, wantMethods: "\x00"},
		{name: "no auth offered with credentials", server: &socksServer{}, user: "u", pass: "p", wantMethods: "\x00\x02"},
		{name: "user/pass ok", server: &socksServer{User: "alice", Pass: "s3cret"}, user: "alice", pass: "s3cret", wantMethods: "\x00\x02", wantCreds: "alice:s3cret"},
		{name: "user with empty password", server: &socksServer{User: "bob"}, user: "bob", wantMethods: "\x00\x02", wantCreds: "bob:"},
		{name: "wrong password", server: &socksServer{User: "alice", Pass: "s3cret"}, user: "alice", pass: "nope", wantMethods: "\x00\x02", wantCreds: "alice:nope",
			wantKind: errs.Network, wantCode: CodeSOCKSAuthFailed},
		{name: "credentials required", server: &socksServer{User: "alice", Pass: "s3cret"}, wantMethods: "\x00",
			wantKind: errs.Network, wantCode: CodeSOCKSAuthRequired},
		{name: "no supported method", server: &socksServer{Method: &gssapiOnly}, user: "u", pass: "p", wantMethods: "\x00\x02",
			wantKind: errs.Network, wantCode: CodeSOCKSAuthUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := tt.server
			proxy := srv.start(t)
			d := newSOCKS(t, SOCKS5Config{Address: proxy, RemoteDNS: true, Username: tt.user, Password: tt.pass})
			conn, err := d.DialContext(t.Context(), "tcp", "target.invalid:80")
			srv.mu.Lock()
			methods, creds := srv.methods, srv.creds
			srv.mu.Unlock()
			if len(methods) != 1 || string(methods[0]) != tt.wantMethods {
				t.Errorf("offered methods = %q, want %q", methods, tt.wantMethods)
			}
			if got := strings.Join(creds, ","); got != tt.wantCreds {
				t.Errorf("credentials sent = %q, want %q", got, tt.wantCreds)
			}
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("DialContext: %v", err)
				}
				defer conn.Close()
				echo(t, conn)
				return
			}
			e := wantErr(t, err, tt.wantKind, tt.wantCode)
			if e.Hint == "" {
				t.Error("no hint")
			}
			if strings.Contains(e.Error(), "s3cret") || strings.Contains(e.Error(), "nope") {
				t.Errorf("the error leaks the password: %v", e)
			}
		})
	}
}

func TestSOCKS5ReplyCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		reply     byte
		remoteDNS bool
		code      string
		msg       string
		hint      string
	}{
		{0x01, false, CodeSOCKSGeneralFailure, "general failure", HintSOCKSGeneralFailure},
		{0x02, false, CodeSOCKSNotAllowed, "does not allow connections to", HintSOCKSNotAllowed},
		{0x03, false, CodeSOCKSNetworkUnreachable, "cannot reach the network", HintSOCKSNetworkUnreachable},
		{0x04, false, CodeSOCKSHostUnreachable, "cannot reach the host", HintSOCKSHostUnreachable},
		{0x05, false, CodeSOCKSConnectionRefused, "refused by the destination", HintSOCKSConnectionRefused},
		{0x06, false, CodeSOCKSTTLExpired, "TTL expired", HintSOCKSTTLExpired},
		{0x07, false, CodeSOCKSCommandNotSupported, "CONNECT command", HintSOCKSCommandNotSupported},
		{0x08, false, CodeSOCKSAddressTypeNotSupported, "kind of address", HintSOCKSIPNotSupported},
		{0x08, true, CodeSOCKSAddressTypeNotSupported, "kind of address", HintSOCKSDomainNotSupported},
		{0x09, false, CodeSOCKSUnknownReply, "reply code 9", HintSOCKSUnknownReply},
		{0xfe, false, CodeSOCKSUnknownReply, "reply code 254", HintSOCKSUnknownReply},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(int(tt.reply))+"/"+strconv.FormatBool(tt.remoteDNS), func(t *testing.T) {
			t.Parallel()
			srv := &socksServer{Reply: tt.reply}
			proxy := srv.start(t)
			d := newSOCKS(t, SOCKS5Config{Address: proxy, RemoteDNS: tt.remoteDNS})
			target := "192.0.2.5:443"
			if tt.remoteDNS {
				target = "api.example.invalid:443"
			}
			_, err := d.DialContext(t.Context(), "tcp", target)
			e := wantErr(t, err, errs.Network, tt.code)
			if !strings.Contains(e.Message, tt.msg) || !strings.Contains(e.Message, proxy) {
				t.Errorf("message = %q, want it to name %q and the proxy", e.Message, tt.msg)
			}
			if tt.reply != 0x07 && tt.reply != 0x08 && !strings.Contains(e.Message, target) {
				t.Errorf("message = %q, want it to name the target %s", e.Message, target)
			}
			if e.Hint != tt.hint {
				t.Errorf("hint = %q, want %q", e.Hint, tt.hint)
			}
			if e.Details["socksReply"] != strconv.Itoa(int(tt.reply)) || e.Details["address"] != target || e.Details["network"] != "tcp" {
				t.Errorf("details = %v", e.Details)
			}
			if re, ok := errors.AsType[*SOCKSReplyError](err); !ok || re.Reply != tt.reply {
				t.Errorf("errors.As SOCKSReplyError = %v, %v", re, ok)
			}
		})
	}
}

func TestSOCKS5HangingProxy(t *testing.T) {
	t.Parallel()
	t.Run("cancel", func(t *testing.T) {
		t.Parallel()
		srv := &socksServer{Hang: true}
		proxy := srv.start(t)
		d := newSOCKS(t, SOCKS5Config{Address: proxy, Timeout: -1})
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(50*time.Millisecond, cancel)
		start := time.Now()
		_, err := d.DialContext(ctx, "tcp", "192.0.2.1:80")
		wantErr(t, err, errs.Cancelled, CodeCancelled)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("errors.Is(err, context.Canceled) = false: %v", err)
		}
		if el := time.Since(start); el > 5*time.Second {
			t.Errorf("cancel took %v", el)
		}
	})
	t.Run("ctx deadline", func(t *testing.T) {
		t.Parallel()
		srv := &socksServer{Hang: true}
		proxy := srv.start(t)
		d := newSOCKS(t, SOCKS5Config{Address: proxy})
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		_, err := d.DialContext(ctx, "tcp", "192.0.2.1:80")
		wantErr(t, err, errs.Timeout, CodeTimeout)
	})
	t.Run("handshake timeout", func(t *testing.T) {
		t.Parallel()
		srv := &socksServer{Hang: true}
		proxy := srv.start(t)
		d := newSOCKS(t, SOCKS5Config{Address: proxy, Timeout: 50 * time.Millisecond})
		_, err := d.DialContext(t.Context(), "tcp", "192.0.2.1:80")
		e := wantErr(t, err, errs.Timeout, CodeSOCKSTimeout)
		if !strings.Contains(e.Message, "greeting") || e.Hint != HintSOCKSTimeout {
			t.Errorf("error = %q, hint %q", e.Message, e.Hint)
		}
	})
	t.Run("already cancelled", func(t *testing.T) {
		t.Parallel()
		srv := &socksServer{}
		proxy := srv.start(t)
		d := newSOCKS(t, SOCKS5Config{Address: proxy})
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := d.DialContext(ctx, "tcp", "192.0.2.1:80")
		wantErr(t, err, errs.Cancelled, CodeCancelled)
		if n := srv.accepted.Load(); n != 0 {
			t.Errorf("the proxy got %d connections", n)
		}
	})
}

func TestSOCKS5ProxyFailures(t *testing.T) {
	t.Parallel()
	t.Run("unreachable", func(t *testing.T) {
		t.Parallel()
		proxy := closedPort(t)
		d := newSOCKS(t, SOCKS5Config{Address: proxy, RemoteDNS: true})
		_, err := d.DialContext(t.Context(), "tcp", "example.invalid:443")
		e := wantErr(t, err, errs.Network, CodeSOCKSProxyUnreachable)
		if !strings.Contains(e.Message, proxy) || e.Hint != HintSOCKSProxyUnreachable {
			t.Errorf("error = %q, hint %q", e.Message, e.Hint)
		}
		if e.Details["address"] != "example.invalid:443" || e.Details["via"] != "socks5h://"+proxy {
			t.Errorf("details = %v", e.Details)
		}
		if Classify(err) != FailureRefused {
			t.Errorf("Classify = %v, want the cause kept (refused)", Classify(err))
		}
	})
	t.Run("not socks5", func(t *testing.T) {
		t.Parallel()
		srv := &socksServer{Version: 4}
		proxy := srv.start(t)
		d := newSOCKS(t, SOCKS5Config{Address: proxy})
		_, err := d.DialContext(t.Context(), "tcp", "192.0.2.1:80")
		e := wantErr(t, err, errs.Protocol, CodeSOCKSProtocolError)
		if e.Hint != HintSOCKSProtocol {
			t.Errorf("hint = %q", e.Hint)
		}
	})
	t.Run("closed during greeting", func(t *testing.T) {
		t.Parallel()
		srv := &socksServer{CloseAfterGreeting: true}
		proxy := srv.start(t)
		d := newSOCKS(t, SOCKS5Config{Address: proxy})
		_, err := d.DialContext(t.Context(), "tcp", "192.0.2.1:80")
		e := wantErr(t, err, errs.Protocol, CodeSOCKSProtocolError)
		if !strings.Contains(e.Message, "closed the connection during the greeting") {
			t.Errorf("message = %q", e.Message)
		}
	})
	t.Run("user/pass chosen but not offered", func(t *testing.T) {
		t.Parallel()
		up := byte(0x02)
		srv := &socksServer{Method: &up}
		proxy := srv.start(t)
		d := newSOCKS(t, SOCKS5Config{Address: proxy})
		_, err := d.DialContext(t.Context(), "tcp", "192.0.2.1:80")
		wantErr(t, err, errs.Protocol, CodeSOCKSProtocolError)
	})
	t.Run("udp", func(t *testing.T) {
		t.Parallel()
		// UDP ASSOCIATE needs the control connection to the proxy too.
		d := newSOCKS(t, SOCKS5Config{Address: "127.0.0.1:1"})
		_, err := d.DialContext(t.Context(), "udp", "192.0.2.1:53")
		wantErr(t, err, errs.Network, CodeSOCKSProxyUnreachable)
	})
	t.Run("unix", func(t *testing.T) {
		t.Parallel()
		d := newSOCKS(t, SOCKS5Config{Address: "127.0.0.1:1"})
		_, err := d.DialContext(t.Context(), "unix", "/tmp/x.sock")
		wantErr(t, err, errs.Unsupported, CodeSOCKSUnsupportedNetwork)
	})
	t.Run("bad target", func(t *testing.T) {
		t.Parallel()
		d := newSOCKS(t, SOCKS5Config{Address: "127.0.0.1:1", RemoteDNS: true})
		for _, a := range []string{"no-port", ":80", strings.Repeat("a", 256) + ":80", "host:notaport"} {
			_, err := d.DialContext(t.Context(), "tcp", a)
			wantErr(t, err, errs.InvalidArgument, CodeSOCKSInvalidAddress)
		}
	})
}

func TestNewSOCKS5InvalidConfig(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 256)
	for _, cfg := range []SOCKS5Config{
		{},
		{Address: "proxy"},
		{Address: ":1080"},
		{Address: "proxy:1080", Username: long},
		{Address: "proxy:1080", Username: "u", Password: long},
		{Address: "proxy:1080", Password: "hunter2"},
	} {
		_, err := NewSOCKS5(cfg, nil)
		wantErr(t, err, errs.InvalidArgument, CodeSOCKSInvalidConfig)
		if cfg.Password != "" && strings.Contains(err.Error(), cfg.Password) {
			t.Errorf("the error leaks the password")
		}
	}
	d, err := NewSOCKS5(SOCKS5Config{Address: "proxy:1080", Username: "u", Password: "p", RemoteDNS: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Via() != "socks5h://proxy:1080" {
		t.Errorf("Via = %q", d.Via())
	}
}

// The SOCKS5 dialer reports its hop around the forward dialer's, and the
// proxy dial goes through the forward dialer (its guard sees the proxy).
func TestSOCKS5TraceAndForward(t *testing.T) {
	t.Parallel()
	srv := &socksServer{}
	proxy := srv.start(t)
	var checked []string
	var mu sync.Mutex
	direct := NewDirect(Options{Guard: func(_ context.Context, c DialCheck) error {
		mu.Lock()
		defer mu.Unlock()
		checked = append(checked, c.Address)
		return nil
	}})
	d, err := NewSOCKS5(SOCKS5Config{Address: proxy, RemoteDNS: true}, direct)
	if err != nil {
		t.Fatal(err)
	}
	var rec recorder
	conn, err := d.DialContext(WithTrace(t.Context(), rec.trace()), "tcp", "api.example.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	starts, dones := rec.events()
	via := "socks5h://" + proxy
	if len(starts) != 2 || starts[0] != (DialStartInfo{Network: "tcp", Address: "api.example.invalid:443", Via: via}) ||
		starts[1] != (DialStartInfo{Network: "tcp", Address: proxy}) {
		t.Errorf("starts = %+v", starts)
	}
	if len(dones) != 2 || dones[0].Address != proxy || dones[0].Via != "" ||
		dones[1].Address != "api.example.invalid:443" || dones[1].Via != via || dones[1].RemoteAddr.String() != proxy || dones[1].Err != nil {
		t.Errorf("dones = %+v", dones)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(checked) != 1 || checked[0] != proxy {
		t.Errorf("the forward guard checked %v, want only the proxy %s", checked, proxy)
	}

	// A guard refusing the proxy hop fails the dial with its refusal.
	deny := NewDirect(Options{Guard: func(_ context.Context, c DialCheck) error { return Deny(c, "Offline.") }})
	d, _ = NewSOCKS5(SOCKS5Config{Address: proxy}, deny)
	_, err = d.DialContext(t.Context(), "tcp", "192.0.2.1:80")
	e := wantErr(t, err, errs.PermissionDenied, CodeDialDenied)
	if e.Details["via"] != "socks5://"+proxy {
		t.Errorf("details = %v", e.Details)
	}
}

func TestViaOf(t *testing.T) {
	t.Parallel()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if ViaOf(a) != "" {
		t.Error("a direct connection has a proxy")
	}
	p := proxied(a, "socks5://p:1")
	if ViaOf(p) != "socks5://p:1" || ViaOf(unwrapper{p}) != "socks5://p:1" {
		t.Error("ViaOf does not find the proxy")
	}
	if _, ok := p.(interface{ CloseWrite() error }); ok {
		t.Error("a pipe gained CloseWrite")
	}
}

type unwrapper struct{ net.Conn }

func (u unwrapper) Unwrap() net.Conn { return u.Conn }
