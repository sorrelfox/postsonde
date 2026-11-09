package netxtest

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/netx/listen"
)

// SOCKS4Server is an in-process SOCKS4 and SOCKS4a proxy for tests
// (SOCKS-06). It records every CONNECT, and whether the client sent a host
// name (SOCKS4a) or an IPv4 address (SOCKS4). Like SOCKS5Server it relays
// an IP address as it is and a host name through Hosts only, never DNS; a
// name that is not there, or a target that refuses, gets reply 91.
type SOCKS4Server struct {
	// UserID, when set, is the only user ID the proxy accepts; another one
	// gets reply 93 (as identd would say).
	UserID string
	// Hosts maps the host names the proxy can "resolve" to IP addresses.
	Hosts map[string]string
	// Reply, when not 0, is the reply every CONNECT gets instead of
	// connecting (91, 92, 93...).
	Reply byte

	addr     string
	accepted atomic.Int32

	mu       sync.Mutex
	requests []SOCKS4Request
}

// SOCKS4Request is one CONNECT the server received.
type SOCKS4Request struct {
	// Name is true for a SOCKS4a request with a host name, false for an
	// IPv4 address.
	Name bool
	// Host is the name or the IP address, as sent.
	Host string
	Port int
	// UserID is the request's USERID field.
	UserID string
}

// Start listens on 127.0.0.1 and serves until the test ends; it returns
// the proxy's host:port.
func (s *SOCKS4Server) Start(t testing.TB) string {
	t.Helper()
	ln, _, err := listen.Listen(context.Background(), listen.Spec{Host: "127.0.0.1"})
	if err != nil {
		t.Fatalf("socks4 test server: %v", err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	t.Cleanup(func() {
		close(done)
		ln.Close()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
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
				s.serve(c, done)
			}()
		}
	}()
	s.addr = ln.Addr().String()
	return s.addr
}

// Addr is the proxy's host:port, once started.
func (s *SOCKS4Server) Addr() string { return s.addr }

// Accepted is the number of connections the proxy has accepted.
func (s *SOCKS4Server) Accepted() int { return int(s.accepted.Load()) }

// Requests returns the CONNECTs received so far, oldest first.
func (s *SOCKS4Server) Requests() []SOCKS4Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SOCKS4Request(nil), s.requests...)
}

func (s *SOCKS4Server) serve(c net.Conn, stop <-chan struct{}) {
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)
	var head [8]byte
	if _, err := io.ReadFull(br, head[:]); err != nil || head[0] != 4 || head[1] != 1 {
		return
	}
	userID, err := readNul(br)
	if err != nil {
		return
	}
	r := SOCKS4Request{Port: int(head[2])<<8 | int(head[3]), UserID: userID}
	ip := netip.AddrFrom4([4]byte(head[4:8]))
	if head[4] == 0 && head[5] == 0 && head[6] == 0 && head[7] != 0 {
		name, err := readNul(br)
		if err != nil {
			return
		}
		r.Name, r.Host = true, name
	} else {
		r.Host = ip.String()
	}
	s.mu.Lock()
	s.requests = append(s.requests, r)
	s.mu.Unlock()

	reply := func(code byte) error {
		_, err := c.Write([]byte{0, code, 0, 0, 0, 0, 0, 0})
		return err
	}
	switch {
	case s.Reply != 0:
		_ = reply(s.Reply)
		return
	case s.UserID != "" && userID != s.UserID:
		_ = reply(93)
		return
	}
	target := r.Host
	if r.Name {
		target = s.Hosts[r.Host]
	}
	var upstream net.Conn
	if target != "" {
		var d net.Dialer
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		upstream, _ = d.DialContext(ctx, "tcp", net.JoinHostPort(target, strconv.Itoa(r.Port)))
		cancel()
	}
	if upstream == nil {
		_ = reply(91)
		return
	}
	defer upstream.Close()
	if err := reply(90); err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	relay(c, br, upstream, stop)
}

// readNul reads a NUL-terminated field of at most 1024 bytes.
func readNul(br *bufio.Reader) (string, error) {
	var b []byte
	for len(b) < 1024 {
		c, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if c == 0 {
			return string(b), nil
		}
		b = append(b, c)
	}
	return "", io.ErrShortBuffer
}

// relay copies between the client c (read through r, which may hold bytes
// already read) and upstream until both directions end or stop closes.
func relay(c net.Conn, r io.Reader, upstream net.Conn, stop <-chan struct{}) {
	finished := make(chan struct{}, 2)
	copyTo := func(dst net.Conn, src io.Reader) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		finished <- struct{}{}
	}
	go copyTo(upstream, r)
	go copyTo(c, upstream)
	for n := 0; n < 2; {
		select {
		case <-finished:
			n++
		case <-stop:
			c.Close()
			upstream.Close()
			stop = nil
		}
	}
}
