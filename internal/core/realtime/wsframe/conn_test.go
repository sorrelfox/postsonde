package wsframe

import (
	"bufio"
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
)

// fakeRW reads scripted input and records output.
type fakeRW struct {
	in  *bytes.Reader
	mu  sync.Mutex
	out bytes.Buffer
}

func (f *fakeRW) Read(p []byte) (int, error) { return f.in.Read(p) }
func (f *fakeRW) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.out.Write(p)
}

// outFrames decodes everything the Conn wrote.
func (f *fakeRW) outFrames(t *testing.T) []Frame {
	t.Helper()
	r := bytes.NewReader(f.out.Bytes())
	var frames []Frame
	for {
		fr, err := ReadFrame(r, Limits{MaxPayload: -1})
		if err == io.EOF {
			return frames
		}
		if err != nil {
			t.Fatalf("decoding output: %v", err)
		}
		frames = append(frames, fr)
	}
}

var testKey = [4]byte{0x37, 0xfa, 0x21, 0x3d}

// wire encodes frames as a peer of role would send them: a client peer
// masks, a server peer does not.
func wire(t *testing.T, peer Role, frames ...Frame) []byte {
	t.Helper()
	var b []byte
	for _, f := range frames {
		if peer == RoleClient {
			f.Masked, f.MaskKey = true, testKey
		}
		var err error
		if b, err = AppendFrame(b, f); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func fr(op Opcode, fin bool, payload string) Frame {
	return Frame{Header: Header{Fin: fin, Opcode: op}, Payload: []byte(payload)}
}

func closeFrame(code StatusCode, reason string) Frame {
	p, err := AppendClosePayload(nil, code, reason)
	if err != nil {
		panic(err)
	}
	return Frame{Header: Header{Fin: true, Opcode: OpClose}, Payload: p}
}

// newTestConn returns a Conn of role reading the frames a peer sends.
func newTestConn(t *testing.T, role Role, o Options, frames ...Frame) (*Conn, *fakeRW) {
	t.Helper()
	peer := RoleServer
	if role == RoleServer {
		peer = RoleClient
	}
	rw := &fakeRW{in: bytes.NewReader(wire(t, peer, frames...))}
	o.Role = role
	o.Rand = bytes.NewReader(bytes.Repeat([]byte{1, 2, 3, 4}, 1000))
	return NewConn(rw, o), rw
}

func checkSentClose(t *testing.T, rw *fakeRW, want StatusCode) {
	t.Helper()
	frames := rw.outFrames(t)
	if len(frames) != 1 || frames[0].Opcode != OpClose {
		t.Fatalf("sent %d frames, want one close: %+v", len(frames), frames)
	}
	code, _, err := ParseClosePayload(frames[0].Payload)
	if err != nil || code != want {
		t.Fatalf("sent close %d (%v), want %d", code, err, want)
	}
}

func TestConnReadFragmentedWithControlFrames(t *testing.T) {
	t.Parallel()
	var seen []string
	c, rw := newTestConn(t, RoleServer, Options{OnFrame: func(d Direction, f Frame) {
		seen = append(seen, map[Direction]string{Received: "<", Sent: ">"}[d]+f.Opcode.String()+":"+string(f.Payload))
	}},
		fr(OpText, false, "Hel"),
		fr(OpPing, true, "p1"),
		fr(OpPong, true, "unsolicited"),
		fr(OpContinuation, false, ""),
		fr(OpContinuation, true, "lo"),
		fr(OpBinary, true, "\x00\xff"),
		fr(OpBinary, true, ""),
	)
	op, msg, err := c.ReadMessage()
	if err != nil || op != OpText || string(msg) != "Hello" {
		t.Fatalf("got %v %q %v", op, msg, err)
	}
	op, msg, err = c.ReadMessage()
	if err != nil || op != OpBinary || string(msg) != "\x00\xff" {
		t.Fatalf("got %v %q %v", op, msg, err)
	}
	op, msg, err = c.ReadMessage()
	if err != nil || op != OpBinary || msg == nil || len(msg) != 0 {
		t.Fatalf("empty message: %v %q %v", op, msg, err)
	}
	frames := rw.outFrames(t)
	if len(frames) != 1 || frames[0].Opcode != OpPong || string(frames[0].Payload) != "p1" || frames[0].Masked {
		t.Fatalf("output %+v", frames)
	}
	want := "<text:Hel <ping:p1 >pong:p1 <pong:unsolicited <continuation: <continuation:lo <binary:\x00\xff <binary:"
	if strings.Join(seen, " ") != want {
		t.Fatalf("OnFrame saw %q", strings.Join(seen, " "))
	}

	// End of input at a frame boundary: abnormal closure, no close frame sent.
	_, _, err = c.ReadMessage()
	if errCode(err) != CodeAbnormalClosure || CloseCodeOf(err) != CloseAbnormalClosure || !errors.Is(err, io.EOF) {
		t.Fatalf("EOF: %v", err)
	}
	if _, _, again := c.ReadMessage(); again != err {
		t.Fatalf("error not sticky: %v", again)
	}
	if len(rw.outFrames(t)) != 1 {
		t.Fatal("a close frame was sent for an abnormal closure")
	}
}

func TestConnProtocolErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		role   Role
		o      Options
		raw    []byte // used instead of frames when set
		frames []Frame
		code   string
		status StatusCode
	}{
		{
			name: "server gets unmasked frame", role: RoleServer,
			raw:  []byte{0x81, 0x02, 'h', 'i'},
			code: CodeProtocolError, status: CloseProtocolError,
		},
		{
			name: "client gets masked frame", role: RoleClient,
			raw:  wire(t, RoleClient, fr(OpText, true, "hi")),
			code: CodeProtocolError, status: CloseProtocolError,
		},
		{
			name: "continuation without start", role: RoleServer,
			frames: []Frame{fr(OpContinuation, true, "x")},
			code:   CodeProtocolError, status: CloseProtocolError,
		},
		{
			name: "new message inside fragmented message", role: RoleClient,
			frames: []Frame{fr(OpText, false, "a"), fr(OpBinary, true, "b")},
			code:   CodeProtocolError, status: CloseProtocolError,
		},
		{
			name: "fragmented ping", role: RoleServer,
			frames: []Frame{fr(OpPing, false, "")},
			code:   CodeProtocolError, status: CloseProtocolError,
		},
		{
			name: "long ping", role: RoleClient,
			frames: []Frame{fr(OpPing, true, strings.Repeat("x", 126))},
			code:   CodeProtocolError, status: CloseProtocolError,
		},
		{
			name: "reserved opcode", role: RoleClient,
			frames: []Frame{fr(Opcode(5), true, "")},
			code:   CodeProtocolError, status: CloseProtocolError,
		},
		{
			name: "rsv bit", role: RoleServer,
			frames: []Frame{{Header: Header{Fin: true, Opcode: OpText, Rsv1: true}}},
			code:   CodeProtocolError, status: CloseProtocolError,
		},
		{
			name: "frame over limit", role: RoleServer, o: Options{Limits: Limits{MaxPayload: 4}},
			frames: []Frame{fr(OpBinary, true, "12345")},
			code:   CodeTooBig, status: CloseMessageTooBig,
		},
		{
			name: "message over limit", role: RoleClient, o: Options{MaxMessageSize: 5},
			frames: []Frame{fr(OpBinary, false, "123"), fr(OpContinuation, true, "456")},
			code:   CodeTooBig, status: CloseMessageTooBig,
		},
		{
			name: "invalid utf-8", role: RoleServer,
			frames: []Frame{fr(OpText, true, "ok\xff")},
			code:   CodeInvalidUTF8, status: CloseInvalidPayload,
		},
		{
			// Fails on the first fragment without waiting for the rest.
			name: "invalid utf-8 fails fast", role: RoleClient,
			frames: []Frame{fr(OpText, false, "\xce\xba\xe1\xbd\xb9\xcf\x83\xce\xbc\xce\xb5\xed\xa0\x80")},
			code:   CodeInvalidUTF8, status: CloseInvalidPayload,
		},
		{
			name: "text ends inside a sequence", role: RoleClient,
			frames: []Frame{fr(OpText, false, "\xe2\x82"), fr(OpContinuation, true, "")},
			code:   CodeInvalidUTF8, status: CloseInvalidPayload,
		},
		{
			name: "close with one byte", role: RoleServer,
			frames: []Frame{fr(OpClose, true, "\x03")},
			code:   CodeInvalidClosePayload, status: CloseProtocolError,
		},
		{
			name: "close with 1005", role: RoleServer,
			frames: []Frame{fr(OpClose, true, "\x03\xed")},
			code:   CodeInvalidClosePayload, status: CloseProtocolError,
		},
		{
			name: "close with 2999", role: RoleClient,
			frames: []Frame{fr(OpClose, true, "\x0b\xb7")},
			code:   CodeInvalidClosePayload, status: CloseProtocolError,
		},
		{
			name: "close reason not utf-8", role: RoleServer,
			frames: []Frame{fr(OpClose, true, "\x03\xe8\xff")},
			code:   CodeInvalidUTF8, status: CloseInvalidPayload,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, rw := newTestConn(t, tt.role, tt.o, tt.frames...)
			if tt.raw != nil {
				rw.in = bytes.NewReader(tt.raw)
			}
			_, _, err := c.ReadMessage()
			if errCode(err) != tt.code || CloseCodeOf(err) != tt.status {
				t.Fatalf("err = %v (code %q, close %d)", err, errCode(err), CloseCodeOf(err))
			}
			if errs.KindOf(err) != errs.Protocol {
				t.Fatalf("kind %s", errs.KindOf(err))
			}
			checkSentClose(t, rw, tt.status)
			if f := rw.outFrames(t)[0]; f.Masked != (tt.role == RoleClient) {
				t.Fatalf("close frame masked = %v for %s", f.Masked, tt.role)
			}
			if err := c.WriteMessage(OpText, []byte("late")); errCode(err) != CodeCloseSent {
				t.Fatalf("write after failure: %v", err)
			}
		})
	}
}

func TestConnUTF8AcrossFragments(t *testing.T) {
	t.Parallel()
	s := "κόσμε \U0001F600"
	b := []byte(s)
	for i := 0; i <= len(b); i++ {
		c, _ := newTestConn(t, RoleServer, Options{},
			fr(OpText, false, string(b[:i])), fr(OpPing, true, ""), fr(OpContinuation, true, string(b[i:])))
		op, msg, err := c.ReadMessage()
		if err != nil || op != OpText || string(msg) != s {
			t.Fatalf("split %d: %v %q %v", i, op, msg, err)
		}
	}
}

func TestConnPeerClose(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		frame  Frame
		code   StatusCode
		reason string
	}{
		{"with reason", closeFrame(CloseGoingAway, "bye"), CloseGoingAway, "bye"},
		{"private code", closeFrame(4321, ""), 4321, ""},
		{"empty", fr(OpClose, true, ""), CloseNoStatus, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, rw := newTestConn(t, RoleClient, Options{}, fr(OpText, false, "part"), tc.frame, fr(OpText, true, "never read"))
			if c.PeerClose() != nil {
				t.Fatal("PeerClose before close")
			}
			_, _, err := c.ReadMessage()
			ce, ok := errors.AsType[*CloseError](err)
			if !ok || ce.Code != tc.code || ce.Reason != tc.reason || errCode(err) != CodePeerClosed || CloseCodeOf(err) != tc.code {
				t.Fatalf("err = %v", err)
			}
			if pc := c.PeerClose(); pc == nil || *pc != *ce {
				t.Fatalf("PeerClose = %v", pc)
			}
			frames := rw.outFrames(t)
			if len(frames) != 1 || frames[0].Opcode != OpClose || !frames[0].Masked {
				t.Fatalf("echo: %+v", frames)
			}
			if tc.code == CloseNoStatus {
				if len(frames[0].Payload) != 0 {
					t.Fatalf("echo of empty close has payload % x", frames[0].Payload)
				}
			} else if code, reason, _ := ParseClosePayload(frames[0].Payload); code != tc.code || reason != "" {
				t.Fatalf("echo %d %q", code, reason)
			}
			if err := c.Ping(nil); errCode(err) != CodeCloseSent {
				t.Fatalf("ping after close: %v", err)
			}
			// Close after the handshake finished returns at once.
			if err := c.Close(context.Background(), CloseNormalClosure, ""); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if len(rw.outFrames(t)) != 1 {
				t.Fatal("Close sent a second close frame")
			}
		})
	}
}

func TestConnDisableAutoPong(t *testing.T) {
	t.Parallel()
	var c *Conn
	pings := 0
	c, rw := newTestConn(t, RoleServer, Options{DisableAutoPong: true, OnFrame: func(d Direction, f Frame) {
		if d == Received && f.Opcode == OpPing {
			pings++
			if err := c.Pong([]byte("manual")); err != nil {
				t.Error(err)
			}
		}
	}}, fr(OpPing, true, "x"), fr(OpText, true, "m"))
	if _, msg, err := c.ReadMessage(); err != nil || string(msg) != "m" {
		t.Fatal(msg, err)
	}
	frames := rw.outFrames(t)
	if pings != 1 || len(frames) != 1 || string(frames[0].Payload) != "manual" {
		t.Fatalf("pings %d, frames %+v", pings, frames)
	}
}

func TestConnWrite(t *testing.T) {
	t.Parallel()
	c, rw := newTestConn(t, RoleServer, Options{})
	if err := c.WriteMessage(OpText, []byte("Hello")); err != nil {
		t.Fatal(err)
	}
	w, err := c.NextWriter(OpText)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("Hel")); err != nil {
		t.Fatal(err)
	}
	if err := c.Ping([]byte("Hello")); err != nil { // interleaves
		t.Fatal(err)
	}
	if n, err := w.Write(nil); n != 0 || err != nil {
		t.Fatal("empty write", n, err)
	}
	if _, err := w.Write([]byte("lo")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); errCode(err) != CodeInvalidArgument {
		t.Fatalf("write after close: %v", err)
	}
	want := mustHex(t, "81 05 48 65 6c 6c 6f   01 03 48 65 6c   89 05 48 65 6c 6c 6f   00 02 6c 6f   80 00")
	if !bytes.Equal(rw.out.Bytes(), want) {
		t.Fatalf("wire % x\nwant % x", rw.out.Bytes(), want)
	}

	// Argument checks.
	if err := c.WriteMessage(OpPing, nil); errCode(err) != CodeInvalidArgument {
		t.Errorf("ping as message: %v", err)
	}
	if err := c.WriteMessage(OpText, []byte{0xff}); errCode(err) != CodeInvalidArgument {
		t.Errorf("invalid text: %v", err)
	}
	if _, err := c.NextWriter(OpClose); errCode(err) != CodeInvalidArgument {
		t.Errorf("close writer: %v", err)
	}
	if err := c.Ping(make([]byte, 126)); errCode(err) != CodeInvalidArgument {
		t.Errorf("long ping: %v", err)
	}
	if err := c.Close(context.Background(), CloseAbnormalClosure, ""); errCode(err) != CodeInvalidArgument {
		t.Errorf("close 1006: %v", err)
	}
	tw, _ := c.NextWriter(OpText)
	if _, err := tw.Write([]byte{0xff}); errCode(err) != CodeInvalidArgument {
		t.Errorf("invalid text fragment: %v", err)
	}
	if _, err := tw.Write([]byte{0xe2, 0x82}); err != nil {
		t.Errorf("partial sequence: %v", err)
	}
	if err := tw.Close(); errCode(err) != CodeInvalidArgument {
		t.Errorf("text ending in a sequence: %v", err)
	}
	// The message lock was released.
	if err := c.WriteMessage(OpBinary, nil); err != nil {
		t.Fatal(err)
	}
}

func TestConnClientMasks(t *testing.T) {
	t.Parallel()
	rw := &fakeRW{in: bytes.NewReader(nil)}
	c := NewConn(rw, Options{Role: RoleClient, Rand: bytes.NewReader([]byte{0x37, 0xfa, 0x21, 0x3d})})
	if err := c.WriteMessage(OpText, []byte("Hello")); err != nil {
		t.Fatal(err)
	}
	if want := mustHex(t, "81 85 37 fa 21 3d 7f 9f 4d 51 58"); !bytes.Equal(rw.out.Bytes(), want) {
		t.Fatalf("wire % x", rw.out.Bytes())
	}
	// The random source is exhausted: the error is reported.
	if err := c.WriteMessage(OpText, []byte("x")); errs.KindOf(err) != errs.Internal {
		t.Fatalf("no randomness: %v", err)
	}
}

func TestConnRawWriteFrame(t *testing.T) {
	t.Parallel()
	c, rw := newTestConn(t, RoleClient, Options{})
	bad := Frame{Header: Header{Fin: false, Opcode: OpPing, Rsv2: true}, Payload: []byte("x")}
	if err := c.WriteFrame(bad); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rw.out.Bytes(), []byte{0x29, 0x01, 'x'}) {
		t.Fatalf("% x", rw.out.Bytes())
	}
	if err := c.WriteFrame(closeFrame(1000, "")); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteMessage(OpBinary, nil); errCode(err) != CodeCloseSent {
		t.Fatalf("after raw close: %v", err)
	}
}

func TestConnBufferedReader(t *testing.T) {
	t.Parallel()
	rest := bytes.NewReader(wire(t, RoleServer, fr(OpText, true, "after")))
	pre := append([]byte("HTTP/1.1 101 Switching Protocols\r\n\r\n"), wire(t, RoleServer, fr(OpText, true, "early"))...)
	br := bufio.NewReader(io.MultiReader(bytes.NewReader(pre), rest))
	if line, _ := br.ReadString('\n'); !strings.HasPrefix(line, "HTTP/1.1") {
		t.Fatal(line)
	}
	_, _ = br.ReadString('\n')
	c := NewConn(&fakeRW{in: bytes.NewReader(nil)}, Options{Role: RoleClient, Reader: br})
	for _, want := range []string{"early", "after"} {
		if _, msg, err := c.ReadMessage(); err != nil || string(msg) != want {
			t.Fatalf("got %q %v", msg, err)
		}
	}
}

func TestNewConnNeedsRole(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	NewConn(&fakeRW{}, Options{})
}

// pipePair returns a client and a server Conn over net.Pipe.
func pipePair(t *testing.T) (client, server *Conn, cc, sc net.Conn) {
	t.Helper()
	cc, sc = net.Pipe()
	t.Cleanup(func() {
		cc.Close()
		sc.Close()
	})
	return NewConn(cc, Options{Role: RoleClient}), NewConn(sc, Options{Role: RoleServer}), cc, sc
}

func TestCloseHandshakeOverPipe(t *testing.T) {
	t.Parallel()
	for _, initiator := range []Role{RoleClient, RoleServer} {
		t.Run(initiator.String()+" closes", func(t *testing.T) {
			t.Parallel()
			client, server, _, _ := pipePair(t)
			closer, peer := client, server
			if initiator == RoleServer {
				closer, peer = server, client
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// The peer reads messages until the close arrives.
			peerDone := make(chan error, 1)
			go func() {
				for {
					op, msg, err := peer.ReadMessage()
					if err != nil {
						peerDone <- err
						return
					}
					if err := peer.WriteMessage(op, append([]byte("echo:"), msg...)); err != nil {
						peerDone <- err
						return
					}
				}
			}()

			// The closer has its own reader, which sees the echoes and then
			// the close.
			readerDone := make(chan error, 1)
			var got []string
			go func() {
				for {
					_, msg, err := closer.ReadMessage()
					if err != nil {
						readerDone <- err
						return
					}
					got = append(got, string(msg))
				}
			}()
			for _, m := range []string{"one", "two"} {
				if err := closer.WriteMessage(OpText, []byte(m)); err != nil {
					t.Fatal(err)
				}
			}
			if err := closer.Ping([]byte("still there?")); err != nil {
				t.Fatal(err)
			}
			if err := closer.Close(ctx, CloseNormalClosure, "done"); err != nil {
				t.Fatalf("Close: %v", err)
			}
			perr := <-peerDone
			ce, ok := errors.AsType[*CloseError](perr)
			if !ok || ce.Code != CloseNormalClosure || ce.Reason != "done" {
				t.Fatalf("peer: %v", perr)
			}
			rerr := <-readerDone
			if CloseCodeOf(rerr) != CloseNormalClosure || errCode(rerr) != CodePeerClosed {
				t.Fatalf("closer reader: %v", rerr)
			}
			if strings.Join(got, ",") != "echo:one,echo:two" {
				t.Fatalf("closer read %q", got)
			}
		})
	}
}

func TestCloseReadsItselfWithoutReader(t *testing.T) {
	t.Parallel()
	client, server, _, _ := pipePair(t)
	// net.Pipe has no buffer: the server writes and reads concurrently.
	go func() { _ = server.WriteMessage(OpBinary, []byte("unread")) }() // discarded by Close
	go func() { _, _, _ = server.ReadMessage() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Close(ctx, CloseGoingAway, ""); err != nil {
		t.Fatal(err)
	}
	if pc := client.PeerClose(); pc == nil || pc.Code != CloseGoingAway {
		t.Fatalf("PeerClose = %v", pc)
	}
}

func TestCloseTimeout(t *testing.T) {
	t.Parallel()
	client, _, _, sc := pipePair(t)
	// The peer reads the close frame but never answers.
	go func() { _, _ = ReadFrame(sc, Limits{}) }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := client.Close(ctx, CloseNormalClosure, "")
	if errCode(err) != CodeCloseTimeout || errs.KindOf(err) != errs.Timeout || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("Close did not honour the context")
	}
}

func TestCloseEndsWithAbnormalClosure(t *testing.T) {
	t.Parallel()
	client, _, _, sc := pipePair(t)
	go func() {
		_, _ = ReadFrame(sc, Limits{})
		sc.Close()
	}()
	err := client.Close(context.Background(), CloseNormalClosure, "")
	if CloseCodeOf(err) != CloseAbnormalClosure {
		t.Fatalf("err = %v", err)
	}
}

func TestWriteClose(t *testing.T) {
	t.Parallel()
	rw := &fakeRW{in: bytes.NewReader(nil)}
	c := NewConn(rw, Options{Role: RoleClient})
	if _, err := c.WriteClose(1004, ""); errCode(err) != CodeInvalidArgument {
		t.Fatalf("reserved code: err = %v", err)
	}
	sent, err := c.WriteClose(4000, "bye")
	if !sent || err != nil {
		t.Fatalf("first WriteClose = %v, %v", sent, err)
	}
	if sent, err := c.WriteClose(CloseNormalClosure, ""); sent || err != nil {
		t.Fatalf("second WriteClose = %v, %v", sent, err)
	}
	if err := c.WriteMessage(OpText, []byte("late")); errCode(err) != CodeCloseSent {
		t.Fatalf("write after close: %v", err)
	}
	frames := rw.outFrames(t)
	if len(frames) != 1 || frames[0].Opcode != OpClose || !frames[0].Masked {
		t.Fatalf("frames = %+v", frames)
	}
	if code, reason, err := ParseClosePayload(frames[0].Payload); code != 4000 || reason != "bye" || err != nil {
		t.Fatalf("close payload = %v %q %v", code, reason, err)
	}
}

func TestConcurrentWrites(t *testing.T) {
	t.Parallel()
	rw := &fakeRW{in: bytes.NewReader(nil)}
	c := NewConn(rw, Options{Role: RoleClient})
	const writers, perWriter = 8, 50
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			for j := range perWriter {
				payload := bytes.Repeat([]byte{byte(i)}, 10+j)
				if i%2 == 0 {
					if err := c.WriteMessage(OpBinary, payload); err != nil {
						t.Error(err)
					}
					continue
				}
				w, err := c.NextWriter(OpBinary)
				if err != nil {
					t.Error(err)
					continue
				}
				_, _ = w.Write(payload[:5])
				_, _ = w.Write(payload[5:])
				_ = w.Close()
			}
		})
		wg.Go(func() {
			for range perWriter {
				if err := c.Ping([]byte{0xAA}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()

	// Decode the output as a server would: every message is intact.
	server := NewConn(&fakeRW{in: bytes.NewReader(rw.out.Bytes())}, Options{Role: RoleServer, DisableAutoPong: true})
	count := 0
	for {
		_, msg, err := server.ReadMessage()
		if err != nil {
			if CloseCodeOf(err) != CloseAbnormalClosure {
				t.Fatalf("after %d messages: %v", count, err)
			}
			break
		}
		for _, b := range msg {
			if b != msg[0] {
				t.Fatalf("message %d mixes writers", count)
			}
		}
		count++
	}
	if count != writers*perWriter {
		t.Fatalf("read %d messages", count)
	}
}
