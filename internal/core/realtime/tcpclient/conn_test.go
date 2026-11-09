package tcpclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/realtime/hexbytes"
)

// recorder records what a Conn reports.
type recorder struct {
	mu      sync.Mutex
	changes []StateChange
	chunks  []Chunk
	nrecv   int           // bytes received
	got     chan struct{} // signalled on every received chunk
}

func newRecorder() *recorder { return &recorder{got: make(chan struct{}, 1)} }

func (r *recorder) options(address string) Options {
	return Options{
		Address: address,
		Dialer:  netx.NewDirect(netx.Options{}),
		OnState: func(ch StateChange) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.changes = append(r.changes, ch)
		},
		OnData: func(c Chunk) {
			r.mu.Lock()
			r.chunks = append(r.chunks, c)
			if c.Direction == Received {
				r.nrecv += len(c.Data)
			}
			r.mu.Unlock()
			if c.Direction == Received {
				select {
				case r.got <- struct{}{}:
				default: // a wake-up is pending already
				}
			}
		},
	}
}

func (r *recorder) states() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, len(r.changes))
	for i, c := range r.changes {
		names[i] = string(c.State)
	}
	return strings.Join(names, ",")
}

func (r *recorder) last() StateChange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.changes[len(r.changes)-1]
}

// bytes returns what went in direction d, concatenated.
func (r *recorder) bytes(d Direction) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b []byte
	for _, c := range r.chunks {
		if c.Direction == d {
			b = append(b, c.Data...)
		}
	}
	return b
}

// waitReceived waits until n bytes were received.
func (r *recorder) waitReceived(t *testing.T, n int) []byte {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		r.mu.Lock()
		have := r.nrecv
		r.mu.Unlock()
		if have >= n {
			return r.bytes(Received)
		}
		select {
		case <-r.got:
		case <-deadline:
			t.Fatalf("received %d bytes, want %d", have, n)
		}
	}
}

// listen starts a loopback TCP server that runs handle for each
// connection and returns its address.
func listen(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
	})
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer func() { _ = c.Close() }()
				handle(c)
			})
		}
	})
	return ln.Addr().String()
}

func echo(c net.Conn) { _, _ = io.Copy(c, c) }

func checkCode(t *testing.T, err error, kind errs.Kind, code string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != kind || e.Code != code {
		t.Fatalf("error = %v (%#v), want %s %s", err, e, kind, code)
	}
}

func TestEchoText(t *testing.T) {
	t.Parallel()
	addr := listen(t, echo)
	rec := newRecorder()
	c, err := Connect(t.Context(), rec.options(addr))
	if err != nil {
		t.Fatal(err)
	}
	if c.State() != StateOpen || c.RemoteAddr() != addr || c.LocalAddr() == "" {
		t.Errorf("state %s, remote %s, local %s", c.State(), c.RemoteAddr(), c.LocalAddr())
	}
	for _, msg := range []string{"hello\n", "Zoë ✓\r\n"} {
		if err := c.Send([]byte(msg)); err != nil {
			t.Fatal(err)
		}
	}
	want := "hello\nZoë ✓\r\n"
	if got := rec.waitReceived(t, len(want)); string(got) != want {
		t.Errorf("received %q", got)
	}
	// The sent chunk is reported before its echo.
	rec.mu.Lock()
	if first := rec.chunks[0]; first.Direction != Sent || string(first.Data) != "hello\n" {
		t.Errorf("first chunk = %+v", first)
	}
	rec.mu.Unlock()

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if got := rec.states(); got != "connecting,open,closing,closed" {
		t.Errorf("states = %s", got)
	}
	info := c.CloseInfo()
	if info == nil || info.InitiatedBy != InitiatorLocal || info.Err != nil || c.Err() != nil ||
		info.BytesSent != int64(len(want)) || info.BytesReceived != int64(len(want)) || info.ChunksSent != 2 ||
		info.OpenedAt.IsZero() || info.ClosedAt.Before(info.OpenedAt) {
		t.Errorf("close info = %+v", info)
	}
	if last := rec.last(); last.Close == nil || last.Err != nil {
		t.Errorf("closed change = %+v", last)
	}
	// Closed: sending fails, closing again does nothing.
	checkCode(t, c.Send([]byte("x")), errs.InvalidArgument, CodeNotOpen)
	if err := c.Close(); err != nil {
		t.Error(err)
	}
	select {
	case <-c.Done():
	default:
		t.Error("Done not closed")
	}
}

func TestEchoHex(t *testing.T) {
	t.Parallel()
	addr := listen(t, echo)
	rec := newRecorder()
	c, err := Connect(t.Context(), rec.options(addr))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	data, err := hexbytes.Parse("00 ff 0x0a,0x0d de:ad:be:ef")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(data); err != nil {
		t.Fatal(err)
	}
	if got := rec.waitReceived(t, len(data)); !bytes.Equal(got, data) {
		t.Errorf("received %x, want %x", got, data)
	}
	if got := rec.bytes(Sent); !bytes.Equal(got, data) {
		t.Errorf("sent %x", got)
	}
	// An empty send writes nothing.
	if err := c.Send(nil); err != nil || c.CloseInfo() != nil {
		t.Errorf("Send(nil) = %v", err)
	}
}

func TestLargeEcho(t *testing.T) {
	t.Parallel()
	addr := listen(t, echo)
	rec := newRecorder()
	o := rec.options(addr)
	o.ReadBufferSize = 1024
	c, err := Connect(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	// Bigger than the socket buffers: sending and reading the echo at the
	// same time must not deadlock.
	data := bytes.Repeat([]byte("0123456789abcdef"), 1<<15)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := c.Send(data); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got := rec.waitReceived(t, 4*len(data))
	if len(got) != 4*len(data) || !bytes.Equal(got[:len(data)], data) {
		t.Errorf("received %d bytes", len(got))
	}
	rec.mu.Lock()
	for _, ch := range rec.chunks {
		if ch.Direction == Received && len(ch.Data) > 1024 {
			t.Fatalf("chunk of %d bytes", len(ch.Data))
		}
	}
	rec.mu.Unlock()
}

func TestRemoteClose(t *testing.T) {
	t.Parallel()
	addr := listen(t, func(c net.Conn) { _, _ = c.Write([]byte("bye")) })
	rec := newRecorder()
	c, err := Connect(t.Context(), rec.options(addr))
	if err != nil {
		t.Fatal(err)
	}
	<-c.Done()
	if got := rec.bytes(Received); string(got) != "bye" {
		t.Errorf("received %q", got)
	}
	if got := rec.states(); got != "connecting,open,closed" {
		t.Errorf("states = %s", got)
	}
	info := c.CloseInfo()
	if info.InitiatedBy != InitiatorRemote || info.Err != nil || info.BytesReceived != 3 {
		t.Errorf("close info = %+v", info)
	}
}

func TestConnectionReset(t *testing.T) {
	t.Parallel()
	addr := listen(t, func(c net.Conn) {
		// Reset only once the client sent a byte, so the connection is
		// open on its side: an RST racing the dial fails the dial instead.
		if _, err := io.ReadFull(c, make([]byte, 1)); err != nil {
			return
		}
		_ = c.(*net.TCPConn).SetLinger(0) // close with RST
	})
	rec := newRecorder()
	c, err := Connect(t.Context(), rec.options(addr))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send([]byte("x")); err != nil {
		t.Fatal(err)
	}
	<-c.Done()
	info := c.CloseInfo()
	checkCode(t, info.Err, errs.Network, netx.CodeConnectionReset)
	if e, _ := errors.AsType[*errs.Error](info.Err); e.Message != "The server reset the connection." || e.Hint == "" {
		t.Errorf("message %q, hint %q", e.Message, e.Hint)
	}
	if info.InitiatedBy != InitiatorRemote || rec.last().Err == nil {
		t.Errorf("close info = %+v", info)
	}
}

func TestCloseWrite(t *testing.T) {
	t.Parallel()
	addr := listen(t, func(c net.Conn) {
		got, _ := io.ReadAll(c) // until the client's FIN
		_, _ = c.Write(append([]byte("got "), got...))
	})
	rec := newRecorder()
	c, err := Connect(t.Context(), rec.options(addr))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseWrite(); err != nil {
		t.Errorf("second CloseWrite = %v", err)
	}
	checkCode(t, c.Send([]byte("more")), errs.InvalidArgument, CodeWriteClosed)
	<-c.Done()
	if got := rec.bytes(Received); string(got) != "got ping" {
		t.Errorf("received %q", got)
	}
	if info := c.CloseInfo(); info.InitiatedBy != InitiatorRemote || info.Err != nil {
		t.Errorf("close info = %+v", info)
	}
	checkCode(t, c.CloseWrite(), errs.InvalidArgument, CodeNotOpen)
}

func TestConnectRefused(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	rec := newRecorder()
	c, err := Connect(t.Context(), rec.options(addr))
	if c != nil {
		t.Errorf("Connect returned a connection")
	}
	checkCode(t, err, errs.Network, netx.CodeConnectionRefused)
	if got := rec.states(); got != "connecting,closed" {
		t.Errorf("states = %s", got)
	}
	last := rec.last()
	if last.Err != err || last.Close == nil || last.Close.InitiatedBy != InitiatorNone || !last.Close.OpenedAt.IsZero() {
		t.Errorf("closed = %+v", last)
	}
}

func TestConnectDNSNotFound(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	o := rec.options("nothing-here.invalid:7")
	o.Dialer = netx.NewDirect(netx.Options{Resolver: netxtest.NXDomainResolver()})
	_, err := Connect(t.Context(), o)
	checkCode(t, err, errs.Network, netx.CodeDNSNotFound)
	if e, _ := errors.AsType[*errs.Error](err); e.Message != "The host name in nothing-here.invalid:7 was not found." || e.Hint != netx.HintDNSNotFound {
		t.Errorf("message %q, hint %q", e.Message, e.Hint)
	}
}

// blockingDialer blocks until the dial context ends.
type blockingDialer struct{}

func (blockingDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestConnectTimeoutAndCancel(t *testing.T) {
	t.Parallel()
	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		rec := newRecorder()
		o := rec.options("example.invalid:7")
		o.Dialer, o.ConnectTimeout = blockingDialer{}, 50*time.Millisecond
		start := time.Now()
		_, err := Connect(t.Context(), o)
		checkCode(t, err, errs.Timeout, netx.CodeTimeout)
		if time.Since(start) > 3*time.Second {
			t.Errorf("took %v", time.Since(start))
		}
		if last := rec.last(); last.Close.InitiatedBy != InitiatorNone {
			t.Errorf("closed = %+v", last)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		t.Parallel()
		rec := newRecorder()
		o := rec.options("example.invalid:7")
		o.Dialer, o.ConnectTimeout = blockingDialer{}, -1
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(20*time.Millisecond, cancel)
		_, err := Connect(ctx, o)
		checkCode(t, err, errs.Cancelled, netx.CodeCancelled)
		if last := rec.last(); last.Close.InitiatedBy != InitiatorLocal || rec.states() != "connecting,closed" {
			t.Errorf("closed = %+v (%s)", last, rec.states())
		}
	})
}

// failingConn is a connection whose writes fail and whose reads block
// until it is closed.
type failingConn struct {
	net.Conn // nil; only the methods below are used
	closed   chan struct{}
	once     sync.Once
}

func (c *failingConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, net.ErrClosed
}
func (c *failingConn) Write([]byte) (int, error) { return 0, errors.New("disk on fire") }
func (c *failingConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}
func (c *failingConn) LocalAddr() net.Addr  { return nil }
func (c *failingConn) RemoteAddr() net.Addr { return nil }

type connDialer struct{ c net.Conn }

func (d connDialer) DialContext(context.Context, string, string) (net.Conn, error) { return d.c, nil }

func TestWriteFailureClosesTheConnection(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	o := rec.options("localhost:7")
	fc := &failingConn{closed: make(chan struct{})}
	o.Dialer = connDialer{fc}
	c, err := Connect(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	checkCode(t, c.Send([]byte("x")), errs.Network, CodeWriteFailed)
	<-c.Done()
	info := c.CloseInfo()
	checkCode(t, info.Err, errs.Network, CodeWriteFailed)
	if info.InitiatedBy != InitiatorNone || rec.states() != "connecting,open,closed" {
		t.Errorf("close info = %+v (%s)", info, rec.states())
	}
	// CloseWrite needs a connection that supports it.
	fc2 := &failingConn{closed: make(chan struct{})}
	o.Dialer = connDialer{fc2}
	c2, err := Connect(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	checkCode(t, c2.CloseWrite(), errs.Unsupported, CodeHalfCloseUnsupported)
	_ = c2.Close()
	if info := c2.CloseInfo(); info.InitiatedBy != InitiatorLocal || info.Err != nil {
		t.Errorf("close info = %+v", info)
	}
}

func TestInvalidAddress(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{"", " ", "localhost", "localhost:", ":7000", "::1:7000", "localhost:0", "localhost:65536", "localhost:http", "localhost:+80", " localhost:7", "a:b:c"} {
		_, _, err := ParseAddress(addr)
		checkCode(t, err, errs.InvalidArgument, CodeInvalidAddress)
		var e *errs.Error
		if errors.As(err, &e); e.Hint == "" || e.Details["address"] != addr {
			t.Errorf("%q: %+v", addr, e)
		}
		rec := newRecorder()
		if _, err := Connect(t.Context(), rec.options(addr)); err == nil || rec.states() != "" {
			t.Errorf("Connect(%q) = %v, states %q", addr, err, rec.states())
		}
	}
	for addr, want := range map[string][2]string{
		"localhost:7000":   {"localhost", "7000"},
		"[::1]:1":          {"::1", "1"},
		"10.0.0.1:65535":   {"10.0.0.1", "65535"},
		"{{echoHost}}:700": {"{{echoHost}}", "700"},
	} {
		host, port, err := ParseAddress(addr)
		if err != nil || host != want[0] || port != want[1] {
			t.Errorf("ParseAddress(%q) = %q, %q, %v", addr, host, port, err)
		}
	}
}

func TestCloseDuringCallbacks(t *testing.T) {
	t.Parallel()
	addr := listen(t, func(c net.Conn) {
		for {
			if _, err := c.Write([]byte("tick")); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	rec := newRecorder()
	o := rec.options(addr)
	onData := o.OnData
	o.OnData = func(c Chunk) {
		time.Sleep(2 * time.Millisecond) // a slow consumer
		onData(c)
	}
	c, err := Connect(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	rec.waitReceived(t, 8)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if got := rec.states(); got != "connecting,open,closing,closed" {
		t.Errorf("states = %s", got)
	}
	if info := c.CloseInfo(); info.InitiatedBy != InitiatorLocal || info.Err != nil {
		t.Errorf("close info = %+v", info)
	}
}
