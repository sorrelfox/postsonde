package tcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/hexbytes"
	"github.com/sorrelfox/postsonde/internal/core/realtime/tcpclient"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// events records what the service emits.
type events struct {
	mu     sync.Mutex
	names  []string
	states []StateEventDTO
	data   []DataEventDTO
	seqs   map[string][]int64
	wake   chan struct{}
}

func newEvents() *events { return &events{seqs: map[string][]int64{}, wake: make(chan struct{}, 1)} }

func (e *events) emit(name string, data any) {
	e.mu.Lock()
	e.names = append(e.names, name)
	switch d := data.(type) {
	case StateEventDTO:
		if name != EventState {
			panic(name)
		}
		e.states = append(e.states, d)
		e.seqs[d.ConnID] = append(e.seqs[d.ConnID], d.Seq)
	case DataEventDTO:
		if name != EventData || len(d.Chunks) == 0 {
			panic(name)
		}
		e.data = append(e.data, d)
		e.seqs[d.ConnID] = append(e.seqs[d.ConnID], d.Seq)
	default:
		panic(name)
	}
	e.mu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// waitFor waits until cond holds for the recorded events.
func (e *events) waitFor(t *testing.T, what string, cond func(e *events) bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		e.mu.Lock()
		ok := cond(e)
		e.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-e.wake:
		case <-deadline:
			e.mu.Lock()
			defer e.mu.Unlock()
			t.Fatalf("timed out waiting for %s; states %+v", what, e.states)
		}
	}
}

// statesOf lists the states of connection id; e.mu must be held.
func (e *events) statesOf(id string) string {
	var s []string
	for _, ev := range e.states {
		if ev.ConnID == id {
			s = append(s, ev.State)
		}
	}
	return strings.Join(s, ",")
}

// bytesOf returns the bytes in direction dir of connection id; e.mu must
// be held.
func (e *events) bytesOf(t *testing.T, id, dir string) string {
	var b []byte
	for _, ev := range e.data {
		if ev.ConnID != id {
			continue
		}
		for _, c := range ev.Chunks {
			if c.Direction != dir {
				continue
			}
			d, err := base64.StdEncoding.DecodeString(c.Data)
			if err != nil || len(d) != c.Size || c.At == "" {
				t.Errorf("chunk %+v: %v", c, err)
			}
			b = append(b, d...)
		}
	}
	return string(b)
}

func (e *events) closed(id string) *StateEventDTO {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.states {
		if e.states[i].ConnID == id && e.states[i].State == "closed" {
			ev := e.states[i]
			return &ev
		}
	}
	return nil
}

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

func newService(t *testing.T) (*TCPService, *events) {
	t.Helper()
	ev := newEvents()
	s := New(netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(nil))), ev.emit, nil, withInterval(5*time.Millisecond))
	t.Cleanup(func() { _ = s.ServiceShutdown() })
	return s, ev
}

func checkCode(t *testing.T, err error, kind errs.Kind, code string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != kind || e.Code != code {
		t.Fatalf("error = %v, want %s %s", err, kind, code)
	}
}

func TestEchoTextAndHex(t *testing.T) {
	t.Parallel()
	addr := listen(t, func(c net.Conn) { _, _ = io.Copy(c, c) })
	s, ev := newService(t)
	ctx := t.Context()

	conn, err := s.Connect(ctx, ConnectInputDTO{RequestID: "req_echo", Address: addr})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(conn.ConnID, "tcp_") || conn.RequestID != "req_echo" || conn.Address != addr {
		t.Fatalf("connection = %+v", conn)
	}
	id := conn.ConnID
	ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(id) == "connecting,open" })

	res, err := s.Send(ctx, SendInputDTO{ConnID: id, Mode: "text", Text: "hello", LineEnding: "crlf"})
	if err != nil || res.Bytes != 7 {
		t.Fatalf("Send text = %+v, %v", res, err)
	}
	res, err = s.Send(ctx, SendInputDTO{ConnID: id, Mode: "hex", Text: "00 FF 0x0a", LineEnding: "crlf"})
	if err != nil || res.Bytes != 3 {
		t.Fatalf("Send hex = %+v, %v", res, err)
	}
	want := "hello\r\n\x00\xff\n"
	ev.waitFor(t, "the echo", func(e *events) bool { return e.bytesOf(t, id, "received") == want })
	ev.mu.Lock()
	if got := ev.bytesOf(t, id, "sent"); got != want {
		t.Errorf("sent %q", got)
	}
	ev.mu.Unlock()

	if err := s.Close(ctx, id); err != nil {
		t.Fatal(err)
	}
	closed := ev.closed(id)
	if closed == nil || closed.Error != nil || closed.Close == nil || closed.Close.InitiatedBy != "local" ||
		closed.Close.BytesSent != 10 || closed.Close.BytesReceived != 10 {
		t.Fatalf("closed = %+v", closed)
	}
	ev.mu.Lock()
	if got := ev.statesOf(id); got != "connecting,open,closing,closed" {
		t.Errorf("states = %s", got)
	}
	// seq counts every event of the connection, in order.
	for i, seq := range ev.seqs[id] {
		if seq != int64(i+1) {
			t.Errorf("seqs = %v", ev.seqs[id])
			break
		}
	}
	ev.mu.Unlock()

	// Gone: sending fails, closing again does nothing.
	_, err = s.Send(ctx, SendInputDTO{ConnID: id, Mode: "text", Text: "x"})
	checkCode(t, err, errs.NotFound, CodeNotFound)
	if err := s.Close(ctx, id); err != nil {
		t.Error(err)
	}
}

func TestRemoteCloseAndFailures(t *testing.T) {
	t.Parallel()
	s, ev := newService(t)
	ctx := t.Context()

	t.Run("remote close", func(t *testing.T) {
		addr := listen(t, func(c net.Conn) { _, _ = c.Write([]byte("bye")) })
		conn, err := s.Connect(ctx, ConnectInputDTO{Address: addr})
		if err != nil {
			t.Fatal(err)
		}
		ev.waitFor(t, "closed", func(e *events) bool { return strings.HasSuffix(e.statesOf(conn.ConnID), "closed") })
		closed := ev.closed(conn.ConnID)
		if closed.Close.InitiatedBy != "remote" || closed.Error != nil || closed.RemoteAddr != addr {
			t.Errorf("closed = %+v", closed)
		}
		ev.mu.Lock()
		if got := ev.bytesOf(t, conn.ConnID, "received"); got != "bye" {
			t.Errorf("received %q", got)
		}
		// The data comes before the closed state.
		last := ev.names[len(ev.names)-1]
		ev.mu.Unlock()
		if last != EventState {
			t.Errorf("last event = %s", last)
		}
	})

	t.Run("refused", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		conn, err := s.Connect(ctx, ConnectInputDTO{Address: addr})
		if err != nil {
			t.Fatal(err)
		}
		ev.waitFor(t, "closed", func(e *events) bool { return e.statesOf(conn.ConnID) == "connecting,closed" })
		closed := ev.closed(conn.ConnID)
		if closed.Error == nil || closed.Error.Code != netx.CodeConnectionRefused || closed.Close.InitiatedBy != "none" || closed.Close.DurationMs != 0 {
			t.Errorf("closed = %+v, error %+v", closed, closed.Error)
		}
	})

	t.Run("invalid input", func(t *testing.T) {
		_, err := s.Connect(ctx, ConnectInputDTO{Address: "localhost"})
		checkCode(t, err, errs.InvalidArgument, tcpclient.CodeInvalidAddress)
		_, err = s.Connect(ctx, ConnectInputDTO{Address: "{{host}}:7000"})
		checkCode(t, err, errs.Unsupported, CodeVariablesUnsupported)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.Connect(cancelled, ConnectInputDTO{Address: "localhost:7"}); !errors.Is(err, context.Canceled) {
			t.Errorf("Connect with a cancelled context = %v", err)
		}
	})
}

// blockingDialer blocks until the dial context ends.
type blockingDialer struct{}

func (blockingDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestTimeoutsAndCloseWhileConnecting(t *testing.T) {
	t.Parallel()
	ev := newEvents()
	s := New(blockingDialer{}, ev.emit, nil, withInterval(5*time.Millisecond))
	ctx := t.Context()

	short := int64(30)
	conn, err := s.Connect(ctx, ConnectInputDTO{Address: "example.invalid:7", ConnectTimeoutMs: &short})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "timeout", func(e *events) bool { return e.statesOf(conn.ConnID) == "connecting,closed" })
	if closed := ev.closed(conn.ConnID); closed.Error == nil || closed.Error.Code != netx.CodeTimeout {
		t.Errorf("closed = %+v", closed)
	}

	// No limit (0): only Close ends it, as a local cancel.
	none := int64(0)
	conn, err = s.Connect(ctx, ConnectInputDTO{Address: "example.invalid:7", ConnectTimeoutMs: &none})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "connecting", func(e *events) bool { return e.statesOf(conn.ConnID) == "connecting" })
	_, err = s.Send(ctx, SendInputDTO{ConnID: conn.ConnID, Mode: "text", Text: "early"})
	checkCode(t, err, errs.InvalidArgument, CodeNotOpen)
	if err := s.Close(ctx, conn.ConnID); err != nil {
		t.Fatal(err)
	}
	closed := ev.closed(conn.ConnID)
	if closed == nil || closed.Error == nil || closed.Error.Code != netx.CodeCancelled || closed.Close.InitiatedBy != "local" {
		t.Errorf("closed = %+v", closed)
	}
}

func TestCloseAllAndWorkspaceChange(t *testing.T) {
	t.Parallel()
	addr := listen(t, func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
	s, ev := newService(t)
	ctx := t.Context()
	var ids []string
	for range 3 {
		conn, err := s.Connect(ctx, ConnectInputDTO{Address: addr})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, conn.ConnID)
	}
	for _, id := range ids {
		ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(id) == "connecting,open" })
	}
	OnWorkspaceChanged(s)
	for _, id := range ids {
		if closed := ev.closed(id); closed == nil || closed.Close.InitiatedBy != "local" {
			t.Errorf("%s closed = %+v", id, closed)
		}
	}
	if n, err := s.CloseAll(ctx); n != 0 || err != nil {
		t.Errorf("CloseAll = %d, %v", n, err)
	}
}

func TestSendValidation(t *testing.T) {
	t.Parallel()
	s, _ := newService(t)
	ctx := t.Context()
	tests := []struct {
		in   SendInputDTO
		kind errs.Kind
		code string
	}{
		{SendInputDTO{ConnID: "tcp_x", Mode: "base64", Text: "AA=="}, errs.InvalidArgument, CodeInvalidMode},
		{SendInputDTO{ConnID: "tcp_x", Mode: "text", Text: "a", LineEnding: "cr"}, errs.InvalidArgument, CodeInvalidLineEnding},
		{SendInputDTO{ConnID: "tcp_x", Mode: "text", Text: ""}, errs.InvalidArgument, CodeEmptyMessage},
		{SendInputDTO{ConnID: "tcp_x", Mode: "hex", Text: " , "}, errs.InvalidArgument, CodeEmptyMessage},
		{SendInputDTO{ConnID: "tcp_x", Mode: "hex", Text: "DE AG"}, errs.InvalidArgument, hexbytes.CodeInvalidChar},
		{SendInputDTO{ConnID: "tcp_x", Mode: "text", Text: "a"}, errs.NotFound, CodeNotFound},
	}
	for _, tt := range tests {
		_, err := s.Send(ctx, tt.in)
		checkCode(t, err, tt.kind, tt.code)
	}
	// A line ending alone is something to send.
	if b, err := encode(SendInputDTO{Mode: "text", LineEnding: "lf"}, false); err != nil || string(b) != "\n" {
		t.Errorf("encode = %q, %v", b, err)
	}
	// With a framing the line ending is left out, and an empty message is
	// a message.
	if b, err := encode(SendInputDTO{Mode: "text", Text: "a", LineEnding: "crlf"}, true); err != nil || string(b) != "a" {
		t.Errorf("framed encode = %q, %v", b, err)
	}
	if b, err := encode(SendInputDTO{Mode: "text"}, true); err != nil || len(b) != 0 {
		t.Errorf("framed empty encode = %q, %v", b, err)
	}
}

func TestDTOs(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 16, 10, 0, 0, 123_000_000, time.UTC)
	info := &tcpclient.CloseInfo{
		InitiatedBy: tcpclient.InitiatorRemote, BytesSent: 7, BytesReceived: 10, ChunksSent: 1, ChunksReceived: 2,
		OpenedAt: at, ClosedAt: at.Add(1500 * time.Millisecond),
	}
	c := &connection{id: "tcp_0123456789abcdef", requestID: "req_echo", address: "localhost:7000"}
	var got []any
	c.emit = func(_ string, data any) { got = append(got, data) }
	c.flush([]item{
		{state: &StateEventDTO{ConnID: c.id, RequestID: c.requestID, Address: c.address, State: "open", At: formatTime(at), LocalAddr: "127.0.0.1:50000", RemoteAddr: "127.0.0.1:7000",
			TLS: toTLSDTO(&httpclient.TLSInfo{Version: "TLS 1.3", CipherSuite: "TLS_AES_128_GCM_SHA256", ServerName: "localhost", PeerCertificates: []httpclient.CertInfo{{Subject: "CN=server"}}})}},
		{chunk: ptr(toChunkDTO(tcpclient.Chunk{Direction: tcpclient.Sent, Data: []byte("hello\r\n"), At: at}))},
		{chunk: ptr(toChunkDTO(tcpclient.Chunk{Direction: tcpclient.Received, Data: []byte{0, 0xff}, At: at}))},
		{chunk: ptr(toChunkDTO(tcpclient.Chunk{Direction: tcpclient.Received, Data: []byte("par"), At: at, Incomplete: true}))},
		{state: &StateEventDTO{ConnID: c.id, RequestID: c.requestID, Address: c.address, State: "closed", At: formatTime(info.ClosedAt), Close: toCloseInfoDTO(info)}},
	})
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "dto/events", append(data, '\n'))
}

func ptr[T any](v T) *T { return &v }
