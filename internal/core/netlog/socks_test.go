package netlog

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strconv"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// relaySOCKS5 is a minimal no-auth SOCKS5 proxy on loopback: it connects
// to IPv4 CONNECT targets and relays, or replies "connection refused".
func relaySOCKS5(t *testing.T) string {
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
			go relay(c)
		}
	}()
	return ln.Addr().String()
}

func relay(c net.Conn) {
	defer c.Close()
	greeting := make([]byte, 3) // VER NMETHODS METHOD
	if _, err := io.ReadFull(c, greeting); err != nil {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	req := make([]byte, 10) // VER CMD RSV ATYP=1 IPv4 PORT
	if _, err := io.ReadFull(c, req); err != nil || req[3] != 1 {
		return
	}
	ip, _ := netip.AddrFromSlice(req[4:8])
	target := net.JoinHostPort(ip.String(), strconv.Itoa(int(req[8])<<8|int(req[9])))
	up, err := net.Dial("tcp", target)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	if _, err := c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	go func() { _, _ = io.Copy(up, c) }()
	_, _ = io.Copy(c, up)
}

// Through a SOCKS5 proxy, the log records the target the client asked
// for, the proxy it went through, and the proxy as the remote address.
func TestRecordsProxiedDials(t *testing.T) {
	t.Parallel()
	proxy := relaySOCKS5(t)
	socks, err := netx.NewSOCKS5(netx.SOCKS5Config{Address: proxy}, netx.NewDirect(netx.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	l := New(Options{})
	route := func(context.Context, string, string) (netx.Dialer, error) { return socks, nil }
	d := netx.Chain(netx.Routed(route), l.Middleware(), netx.Guarded(netx.RequireUserOp(nil)))

	target := echoServer(t)
	conn, err := d.DialContext(userCtx(t), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 2)); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	refused := closedPort(t)
	if _, err := d.DialContext(userCtx(t), "tcp", refused); err == nil {
		t.Fatal("dial to a closed port through the proxy succeeded")
	}
	if _, err := d.DialContext(t.Context(), "tcp", target); err == nil {
		t.Fatal("unmarked dial succeeded")
	}

	conns := l.Conns()
	if len(conns) != 3 {
		t.Fatalf("Conns = %+v", conns)
	}
	via := "socks5://" + proxy
	if c := conns[0]; c.Address != target || c.Via != via || c.RemoteAddr != proxy || c.State != Closed || c.BytesOut != 2 || c.BytesIn != 2 {
		t.Errorf("proxied = %+v", c)
	}
	if c := conns[1]; c.Address != refused || c.Via != via || c.State != Failed || c.ErrCode != netx.CodeSOCKSConnectionRefused {
		t.Errorf("refused by target = %+v", c)
	}
	if c := conns[2]; c.State != Refused || c.Via != "" || c.ErrCode != netx.CodeNotUserInitiated {
		t.Errorf("guard refusal = %+v", c)
	}
}
