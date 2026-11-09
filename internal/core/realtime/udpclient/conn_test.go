package udpclient_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/realtime/udpclient"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/udpserver"
)

// udpServer starts the app's own UDP server (SSRV-06) with cfg on host
// and returns its port.
func udpServer(t *testing.T, host string, cfg udpserver.Config) string {
	t.Helper()
	m := servers.NewManager(servers.Options{
		Types:  []servers.Type{udpserver.New(udpserver.Options{})},
		Logger: slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() { m.StopAll(context.Background()) })
	s, err := m.Add(servers.Spec{Type: udpserver.TypeName, Host: host, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if s, err = m.Start(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	return strconv.Itoa(s.Port())
}

func echoServer(t *testing.T) string {
	t.Helper()
	return udpServer(t, "127.0.0.1", udpserver.Config{Echo: true})
}

// recorder collects a connection's callbacks.
type recorder struct {
	mu        sync.Mutex
	states    []udpclient.StateChange
	datagrams []udpclient.Datagram
	notes     []udpclient.Note
}

func (r *recorder) options(o udpclient.Options) udpclient.Options {
	o.OnState = func(ch udpclient.StateChange) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.states = append(r.states, ch)
	}
	o.OnDatagram = func(d udpclient.Datagram) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.datagrams = append(r.datagrams, d)
	}
	o.OnNote = func(n udpclient.Note) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.notes = append(r.notes, n)
	}
	return o
}

func (r *recorder) received() []udpclient.Datagram {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []udpclient.Datagram
	for _, d := range r.datagrams {
		if d.Direction == udpclient.Received {
			out = append(out, d)
		}
	}
	return out
}

func (r *recorder) stateNames() []udpclient.State {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []udpclient.State
	for _, s := range r.states {
		out = append(out, s.State)
	}
	return out
}

func (r *recorder) allNotes() []udpclient.Note {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.notes)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (r *recorder) waitReceived(t *testing.T, n int) []udpclient.Datagram {
	t.Helper()
	waitFor(t, strconv.Itoa(n)+" datagrams", func() bool { return len(r.received()) >= n })
	return r.received()
}

func directDialer() netx.Dialer { return netx.NewDirect(netx.Options{}) }

func open(t *testing.T, r *recorder, o udpclient.Options) *udpclient.Conn {
	t.Helper()
	if o.Dialer == nil {
		o.Dialer = directDialer()
	}
	c, err := udpclient.Open(t.Context(), r.options(o))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

// TestEcho (UDP-01): text, binary and empty datagrams round-trip with the
// in-repo UDP server, connected and unconnected, over IPv4 and IPv6; the
// replies carry the server's address, and closing reports the totals.
func TestEcho(t *testing.T) {
	t.Parallel()
	v4 := echoServer(t)
	v6 := ""
	if ln, err := net.ListenPacket("udp6", "[::1]:0"); err == nil {
		ln.Close()
		v6 = udpServer(t, "::1", udpserver.Config{Echo: true})
	}
	tests := []struct {
		name, network, address string
		connected              bool
	}{
		{"connected", "", "127.0.0.1:" + v4, true},
		{"unconnected", "", "127.0.0.1:" + v4, false},
		{"udp4 by name", "udp4", "localhost:" + v4, false},
		{"udp6", "udp6", "[::1]:" + v6, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.network == "udp6" && v6 == "" {
				t.Skip("no IPv6 loopback")
			}
			r := &recorder{}
			c := open(t, r, udpclient.Options{Address: tt.address, Network: tt.network, Connected: tt.connected})
			msgs := []string{"hello", "\x00\xff\x10", "", strings.Repeat("u", 40000)}
			for _, m := range msgs {
				if err := c.Send([]byte(m)); err != nil {
					t.Fatal(err)
				}
				r.waitReceived(t, 1) // in order, one at a time
				r.mu.Lock()
				r.datagrams = nil
				r.mu.Unlock()
			}
			_ = c.Close()
			got := r.stateNames()
			want := []udpclient.State{udpclient.StateOpening, udpclient.StateOpen, udpclient.StateClosing, udpclient.StateClosed}
			if !slices.Equal(got, want) {
				t.Errorf("states = %v", got)
			}
			openState := r.states[1]
			wantMax := netx.MaxUDPPayloadIPv4
			if tt.network == "udp6" {
				wantMax = netx.MaxUDPPayloadIPv6
			}
			if openState.Connected != tt.connected || openState.Via != "" || openState.LocalAddr == "" || openState.MaxPayload != wantMax {
				t.Errorf("open = %+v", openState)
			}
			info := c.CloseInfo()
			size := int64(len(strings.Join(msgs, "")))
			if info.InitiatedBy != udpclient.InitiatorLocal || info.Err != nil || info.DatagramsSent != 4 || info.DatagramsReceived != 4 ||
				info.BytesSent != size || info.BytesReceived != size {
				t.Errorf("close = %+v", info)
			}
		})
	}
}

// TestPeer: a received datagram names its sender; sent ones the target.
func TestPeer(t *testing.T) {
	t.Parallel()
	port := echoServer(t)
	r := &recorder{}
	c := open(t, r, udpclient.Options{Address: "localhost:" + port, Network: "udp4"})
	if err := c.Send([]byte("x")); err != nil {
		t.Fatal(err)
	}
	got := r.waitReceived(t, 1)
	if got[0].Peer != "127.0.0.1:"+port || string(got[0].Data) != "x" {
		t.Errorf("received %+v", got[0])
	}
	if sent := r.datagrams[0]; sent.Direction != udpclient.Sent || sent.Peer != "127.0.0.1:"+port {
		t.Errorf("sent %+v", sent)
	}
}

// TestThroughSOCKS5 (SOCKS-07): datagrams round-trip with the UDP server
// through the in-process SOCKS5 proxy's UDP ASSOCIATE relay, the name
// sent to the proxy unresolved with socks5h, and the open state shows
// the hop.
func TestThroughSOCKS5(t *testing.T) {
	t.Parallel()
	port := echoServer(t)
	for _, connected := range []bool{true, false} {
		p := &netxtest.SOCKS5Server{Hosts: map[string]string{"udp.test": "127.0.0.1"}}
		s, err := netx.NewSOCKS5(netx.SOCKS5Config{Address: p.Start(t), RemoteDNS: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		r := &recorder{}
		c := open(t, r, udpclient.Options{Address: "udp.test:" + port, Dialer: s, Connected: connected})
		for _, m := range []string{"one", "two"} {
			if err := c.Send([]byte(m)); err != nil {
				t.Fatal(err)
			}
		}
		got := r.waitReceived(t, 2)
		if string(got[0].Data) != "one" || string(got[1].Data) != "two" || got[0].Peer != "127.0.0.1:"+port {
			t.Errorf("connected=%v: received %+v", connected, got)
		}
		st := r.states[1]
		if st.Via != s.Via() || st.Proxy == nil || st.Proxy.Command != netx.CommandUDPAssociate || st.Proxy.RelayAddr == "" ||
			st.Connected != connected || st.MaxPayload != netx.MaxUDPPayloadIPv4-(3+1+1+len("udp.test")+2) {
			t.Errorf("connected=%v: open = %+v, hop %+v", connected, st, st.Proxy)
		}
		if d := p.UDPDatagrams(); len(d) != 2 || !d[0].Name || d[0].Host != "udp.test" {
			t.Errorf("relay saw %+v", d)
		}
		_ = c.Close()
		waitFor(t, "the association to end", func() bool { return p.UDPAssociations() == 0 })
	}
}

// TestFragmentNote: a fragment the relay sends is dropped with a note;
// the socket stays open and the reply after it arrives.
func TestFragmentNote(t *testing.T) {
	t.Parallel()
	port := echoServer(t)
	p := &netxtest.SOCKS5Server{UDPFragmentFirst: true}
	s, _ := netx.NewSOCKS5(netx.SOCKS5Config{Address: p.Start(t)}, nil)
	r := &recorder{}
	c := open(t, r, udpclient.Options{Address: "127.0.0.1:" + port, Dialer: s})
	if err := c.Send([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if got := r.waitReceived(t, 1); string(got[0].Data) != "ping" {
		t.Errorf("received %+v", got)
	}
	notes := r.allNotes()
	if len(notes) != 1 || codeOf(notes[0].Err) != netx.CodeSOCKSUDPFragment {
		t.Errorf("notes = %+v", notes)
	}
	if c.State() != udpclient.StateOpen {
		t.Errorf("state = %s", c.State())
	}
	_ = c.Close()
	if info := c.CloseInfo(); info.Notes != 1 {
		t.Errorf("close = %+v", info)
	}
}

// TestPortUnreachable: a connected socket reports the ICMP answer for a
// closed port as a note and stays open; unconnected, nothing is noted.
func TestPortUnreachable(t *testing.T) {
	t.Parallel()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := pc.LocalAddr().String()
	pc.Close()

	r := &recorder{}
	c := open(t, r, udpclient.Options{Address: closed, Connected: true})
	if err := c.Send([]byte("anyone?")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a note", func() bool { return len(r.allNotes()) > 0 })
	if n := r.allNotes()[0]; codeOf(n.Err) != udpclient.CodePortUnreachable {
		t.Errorf("note = %v", n.Err)
	}
	if err := c.Send([]byte("still there")); err != nil && codeOf(err) != udpclient.CodePortUnreachable {
		t.Errorf("second send: %v", err)
	}
	if c.State() != udpclient.StateOpen {
		t.Errorf("state = %s", c.State())
	}

	u := open(t, &recorder{}, udpclient.Options{Address: closed})
	if err := u.Send([]byte("anyone?")); err != nil {
		t.Errorf("unconnected send: %v", err)
	}
}

// TestDatagramLimits: Send refuses datagrams over the IP limit or the
// request's maximum; a longer received datagram is cut and marked.
func TestDatagramLimits(t *testing.T) {
	t.Parallel()
	port := udpServer(t, "127.0.0.1", udpserver.Config{FixedReply: true, Reply: "hello world"})
	r := &recorder{}
	c := open(t, r, udpclient.Options{Address: "127.0.0.1:" + port, MaxDatagramSize: 4})
	if err := c.Send([]byte("12345")); codeOf(err) != udpclient.CodeDatagramTooLarge {
		t.Errorf("over the maximum: %v", err)
	}
	if err := c.Send([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	got := r.waitReceived(t, 1)
	if string(got[0].Data) != "hell" || got[0].Size != 11 || !got[0].Truncated {
		t.Errorf("received %+v", got[0])
	}

	d := open(t, &recorder{}, udpclient.Options{Address: "127.0.0.1:" + port})
	if err := d.Send(make([]byte, netx.MaxUDPPayloadIPv4+1)); codeOf(err) != udpclient.CodeDatagramTooLarge {
		t.Errorf("over the IPv4 limit: %v", err)
	}
	if err := d.Send(make([]byte, netx.MaxUDPPayloadIPv4)); err != nil {
		t.Errorf("at the IPv4 limit: %v", err)
	}
}

func TestInvalidOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		o    udpclient.Options
		code string
	}{
		{udpclient.Options{Address: "no-port"}, udpclient.CodeInvalidAddress},
		{udpclient.Options{Address: "host:0"}, udpclient.CodeInvalidAddress},
		{udpclient.Options{Address: "host:53", Network: "tcp"}, udpclient.CodeInvalidOptions},
		{udpclient.Options{Address: "[::1]:53", Network: "udp4"}, udpclient.CodeFamilyMismatch},
		{udpclient.Options{Address: "127.0.0.1:53", Network: "udp6"}, udpclient.CodeFamilyMismatch},
		{udpclient.Options{Address: "host:53", MaxDatagramSize: 70000}, udpclient.CodeInvalidOptions},
	}
	for _, tt := range tests {
		r := &recorder{}
		tt.o.Dialer = directDialer()
		_, err := udpclient.Open(t.Context(), r.options(tt.o))
		if codeOf(err) != tt.code {
			t.Errorf("%+v: %v, want %s", tt.o, err, tt.code)
		}
		if len(r.states) != 0 {
			t.Errorf("%+v: states %v reported", tt.o, r.stateNames())
		}
	}
}

// TestRefusedByProxy: a proxy without UDP ASSOCIATE fails opening with
// its reply, and the closed state carries the hop as far as it got.
func TestRefusedByProxy(t *testing.T) {
	t.Parallel()
	p := &netxtest.SOCKS5Server{NoUDP: true}
	s, _ := netx.NewSOCKS5(netx.SOCKS5Config{Address: p.Start(t)}, nil)
	r := &recorder{}
	_, err := udpclient.Open(t.Context(), r.options(udpclient.Options{Address: "192.0.2.1:53", Dialer: s}))
	if codeOf(err) != netx.CodeSOCKSCommandNotSupported {
		t.Fatalf("err = %v", err)
	}
	if got := r.stateNames(); !slices.Equal(got, []udpclient.State{udpclient.StateOpening, udpclient.StateClosed}) {
		t.Errorf("states = %v", got)
	}
	if last := r.states[1]; last.Proxy == nil || last.Proxy.Reply != 7 || last.Close.InitiatedBy != udpclient.InitiatorNone {
		t.Errorf("closed = %+v", last)
	}
}

// TestAssociationEnded: the proxy closing the control connection closes
// the socket with netx.socks_udp_association_closed.
func TestAssociationEnded(t *testing.T) {
	t.Parallel()
	port := echoServer(t)
	p := &netxtest.SOCKS5Server{}
	s, _ := netx.NewSOCKS5(netx.SOCKS5Config{Address: p.Start(t)}, nil)
	r := &recorder{}
	c := open(t, r, udpclient.Options{Address: "127.0.0.1:" + port, Dialer: s})
	p.DropUDPAssociations()
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("still open")
	}
	info := c.CloseInfo()
	if info.InitiatedBy != udpclient.InitiatorNone || codeOf(info.Err) != netx.CodeSOCKSUDPAssociationClosed {
		t.Errorf("close = %+v", info)
	}
	if err := c.Send([]byte("x")); codeOf(err) != udpclient.CodeNotOpen {
		t.Errorf("send after close: %v", err)
	}
}

// TestOpenCancelled: a cancelled open is closed by this side.
func TestOpenCancelled(t *testing.T) {
	t.Parallel()
	block := netx.DialerFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(20*time.Millisecond, cancel)
	r := &recorder{}
	_, err := udpclient.Open(ctx, r.options(udpclient.Options{Address: "127.0.0.1:9", Dialer: block}))
	if codeOf(err) != netx.CodeCancelled {
		t.Errorf("err = %v", err)
	}
	if last := r.states[len(r.states)-1]; last.Close.InitiatedBy != udpclient.InitiatorLocal {
		t.Errorf("closed = %+v", last.Close)
	}
	_, err = udpclient.Open(t.Context(), r.options(udpclient.Options{Address: "127.0.0.1:9", Dialer: block, OpenTimeout: 20 * time.Millisecond}))
	if codeOf(err) != netx.CodeTimeout {
		t.Errorf("timeout: %v", err)
	}
}

// TestCloseDoesNotLeak: opening and closing, directly and through the
// proxy, leaves no goroutine and no association behind.
func TestCloseDoesNotLeak(t *testing.T) {
	port := echoServer(t)
	p := &netxtest.SOCKS5Server{}
	s, _ := netx.NewSOCKS5(netx.SOCKS5Config{Address: p.Start(t)}, nil)
	waitFor(t, "the servers to settle", func() bool { return true })
	before := runtime.NumGoroutine()
	for i := range 20 {
		d := netx.Dialer(directDialer())
		if i%2 == 1 {
			d = s
		}
		c, err := udpclient.Open(t.Context(), udpclient.Options{Address: "127.0.0.1:" + port, Dialer: d, Connected: i%4 < 2})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Send([]byte("x")); err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
	}
	waitFor(t, "the associations to end", func() bool { return p.UDPAssociations() == 0 })
	waitFor(t, "the goroutines to end", func() bool { return runtime.NumGoroutine() <= before+2 })
}
