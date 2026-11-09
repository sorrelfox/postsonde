package tcpclient

import (
	"bytes"
	"crypto/tls"
	"io"
	"net"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/framing"
)

// received returns the received chunks.
func (r *recorder) received() []Chunk {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Chunk
	for _, c := range r.chunks {
		if c.Direction == Received {
			out = append(out, c)
		}
	}
	return out
}

// waitMessages waits until n messages were received.
func (r *recorder) waitMessages(t *testing.T, n int) []Chunk {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if got := r.received(); len(got) >= n {
			return got
		}
		select {
		case <-r.got:
		case <-deadline:
			t.Fatalf("received %d messages, want %d: %q", len(r.received()), n, messages(r.received()))
		}
	}
}

func waitDone(t *testing.T, c *Conn) {
	t.Helper()
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the connection did not close")
	}
}

// trickle writes b in pieces of the given sizes (cycling), pausing so the
// pieces arrive in separate reads.
func trickle(c net.Conn, b []byte, sizes ...int) {
	for i, k := 0, 0; i < len(b); k++ {
		n := min(sizes[k%len(sizes)], len(b)-i)
		if _, err := c.Write(b[i : i+n]); err != nil {
			return
		}
		i += n
		time.Sleep(time.Millisecond)
	}
}

func messages(chunks []Chunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = string(c.Data)
	}
	return out
}

func sameStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestNewlineFramingEndToEnd(t *testing.T) {
	t.Parallel()
	sent := make(chan []byte, 1)
	stream := []byte("first\r\nsecond\nthird line\r\n\nlast")
	addr := listen(t, func(c net.Conn) {
		buf := make([]byte, 64)
		var got []byte
		for !bytes.HasSuffix(got, []byte("b\r\n")) {
			n, err := c.Read(buf)
			if err != nil {
				return
			}
			got = append(got, buf[:n]...)
		}
		sent <- got
		trickle(c, stream, 1, 3, 2, 7)
	})
	rec := newRecorder()
	o := rec.options(addr)
	o.Framing = framing.Options{Mode: framing.ModeNewline, Delimiter: framing.DelimiterCRLF}
	c, err := Connect(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"a", "", "b"} {
		if err := c.Send([]byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	if got := string(<-sent); got != "a\r\n\r\nb\r\n" {
		t.Errorf("server got %q", got)
	}
	waitDone(t, c)
	got := rec.received()
	sameStrings(t, messages(got), []string{"first", "second", "third line", "", "last"})
	for i, m := range got {
		if m.Incomplete != (i == len(got)-1) {
			t.Errorf("message %d incomplete = %v", i, m.Incomplete)
		}
	}
	// Sent chunks are the messages, without the delimiter.
	rec.mu.Lock()
	if first := rec.chunks[0]; first.Direction != Sent || string(first.Data) != "a" {
		t.Errorf("first chunk %+v", first)
	}
	rec.mu.Unlock()
	info := c.CloseInfo()
	if info.InitiatedBy != InitiatorRemote || info.Err != nil || info.ChunksReceived != 5 || info.ChunksSent != 3 ||
		info.BytesSent != 8 || info.BytesReceived != int64(len(stream)) {
		t.Errorf("close info %+v", info)
	}
}

func TestLengthFramingEcho(t *testing.T) {
	t.Parallel()
	for _, size := range []int{1, 2, 4, 8} {
		for _, order := range []string{framing.BigEndian, framing.LittleEndian} {
			addr := listen(t, func(c net.Conn) {
				// Echo in awkward pieces.
				buf := make([]byte, 5)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						trickle(c, buf[:n], 1, 2)
					}
					if err != nil {
						return
					}
				}
			})
			rec := newRecorder()
			o := rec.options(addr)
			o.Framing = framing.Options{Mode: framing.ModeLength, PrefixSize: size, ByteOrder: order}
			c, err := Connect(t.Context(), o)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"hello", "", "\x00\n\xff binary", string(bytes.Repeat([]byte("z"), 200))}
			for _, m := range want {
				if err := c.Send([]byte(m)); err != nil {
					t.Fatal(err)
				}
			}
			sameStrings(t, messages(rec.waitMessages(t, len(want))), want)
			if size == 1 {
				checkCode(t, c.Send(make([]byte, 256)), errs.Protocol, framing.CodeMessageTooLarge)
				if c.State() != StateOpen {
					t.Error("a message too long to send closed the connection")
				}
			}
			_ = c.Close()
		}
	}
}

func TestFramingOversizeFailsTheConnection(t *testing.T) {
	t.Parallel()
	addr := listen(t, func(c net.Conn) {
		// One message that fits, then a prefix announcing 1000 bytes.
		_, _ = c.Write([]byte{0, 2, 'o', 'k', 0x03, 0xe8, 'x'})
		_, _ = io.Copy(io.Discard, c)
	})
	rec := newRecorder()
	o := rec.options(addr)
	o.Framing = framing.Options{Mode: framing.ModeLength, PrefixSize: 2, MaxMessageSize: 100}
	c, err := Connect(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, c)
	checkCode(t, c.Err(), errs.Protocol, framing.CodeMessageTooLarge)
	sameStrings(t, messages(rec.received()), []string{"ok"})
	if info := c.CloseInfo(); info.InitiatedBy != InitiatorNone {
		t.Errorf("close info %+v", info)
	}
	if last := rec.last(); last.State != StateClosed || last.Err == nil {
		t.Errorf("last change %+v", last)
	}
}

func TestFramingInvalidOptions(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	o := rec.options("127.0.0.1:1")
	o.Framing = framing.Options{Mode: framing.ModeLength, PrefixSize: 3}
	_, err := Connect(t.Context(), o)
	checkCode(t, err, errs.InvalidArgument, framing.CodeInvalidOptions)
	if got := rec.states(); got != "" {
		t.Errorf("states reported: %s", got)
	}
}

func TestFramingOverTLS(t *testing.T) {
	t.Parallel()
	srv := newTLSServer(t)
	addr := listenTLS(t, &tls.Config{Certificates: []tls.Certificate{srv.server.TLS}}, func(c net.Conn) {
		buf := make([]byte, 3)
		for {
			n, err := c.Read(buf)
			if n > 0 {
				trickle(c, buf[:n], 1)
			}
			if err != nil {
				return
			}
		}
	})
	rec := newRecorder()
	o := rec.options(addr)
	o.TLS = &TLSOptions{RootCAs: srv.ca.Pool()}
	o.Framing = framing.Options{Mode: framing.ModeNewline}
	c, err := Connect(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	for _, m := range []string{"PING", "HELO example"} {
		if err := c.Send([]byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	sameStrings(t, messages(rec.waitMessages(t, 2)), []string{"PING", "HELO example"})
}
