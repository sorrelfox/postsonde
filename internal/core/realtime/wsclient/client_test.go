package wsclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/testutil/testenv"
)

func TestConnectAndClientClose(t *testing.T) {
	t.Parallel()
	for _, secure := range []bool{false, true} {
		name := "ws"
		if secure {
			name = "wss"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			serverErr := make(chan error, 1)
			reqs := make(chan *http.Request, 1)
			srv := newServer(t, secure, func(w http.ResponseWriter, r *http.Request) {
				reqs <- r
				sc, nc := upgrade(t, w, r, "", nil)
				echo(sc, nc, serverErr)
			})
			o, st := options(srv, "/socket?x=1")
			c := connect(t, o)

			r := <-reqs
			if r.URL.RequestURI() != "/socket?x=1" || r.Header.Get("Upgrade") != "websocket" ||
				r.Header.Get("Connection") != "Upgrade" || r.Header.Get("Sec-WebSocket-Version") != "13" ||
				len(r.Header.Get("Sec-WebSocket-Key")) != 24 || r.Header.Get("Origin") != "" {
				t.Fatalf("request: %s %v", r.URL, r.Header)
			}
			if c.State() != StateOpen {
				t.Fatalf("state = %s", c.State())
			}
			if err := c.SendText("hello"); err != nil {
				t.Fatal(err)
			}
			if m := recvMessage(t, st); m.Opcode != wsframe.OpText || string(m.Data) != "echo:hello" {
				t.Fatalf("message = %v %q", m.Opcode, m.Data)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := c.Close(ctx, 4001, "bye"); err != nil {
				t.Fatalf("Close: %v", err)
			}
			// The server observed the chosen code and reason.
			serr := <-serverErr
			if ce, ok := errors.AsType[*wsframe.CloseError](serr); !ok || ce.Code != 4001 || ce.Reason != "bye" {
				t.Fatalf("server: %v", serr)
			}
			info := c.CloseInfo()
			want := CloseInfo{Code: 4001, WasClean: true, InitiatedBy: InitiatorLocal}
			if info == nil || info.Code != want.Code || info.Reason != "" || !info.WasClean || info.InitiatedBy != InitiatorLocal ||
				*info.Sent != (CloseFrame{4001, "bye"}) || *info.Received != (CloseFrame{4001, ""}) {
				t.Fatalf("CloseInfo = %+v", info)
			}
			if c.Err() != nil {
				t.Fatalf("Err = %v", c.Err())
			}
			if got := st.names(); got != "connecting,open,closing,closed" {
				t.Fatalf("states = %s", got)
			}

			hs := c.Handshake()
			if hs.Response == nil || hs.Response.StatusCode != 101 || hs.Response.Status != "101 Switching Protocols" {
				t.Fatalf("response = %+v", hs.Response)
			}
			if hs.Conn.Protocol != "HTTP/1.1" || hs.Conn.RemoteAddr == "" {
				t.Fatalf("conn = %+v", hs.Conn)
			}
			wantPhases := httpclient.PhaseConnect | httpclient.PhaseRequestWrite | httpclient.PhaseWait | httpclient.PhaseDownload
			if secure {
				wantPhases |= httpclient.PhaseTLS
				if hs.Conn.TLS == nil || hs.Conn.TLS.NegotiatedProtocol != "http/1.1" {
					t.Fatalf("TLS = %+v", hs.Conn.TLS)
				}
			} else if hs.Conn.TLS != nil || hs.Timing.Observed.Has(httpclient.PhaseTLS) {
				t.Fatalf("TLS on ws: %+v %v", hs.Conn.TLS, hs.Timing.Observed)
			}
			if !hs.Timing.Observed.Has(wantPhases) || !testenv.Measured(hs.Timing.Total) {
				t.Fatalf("timing = %+v (%v)", hs.Timing, hs.Timing.Observed)
			}
		})
	}
}

func TestClientCloseCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code   wsframe.StatusCode
		reason string
	}{
		{wsframe.CloseNormalClosure, ""},
		{wsframe.CloseGoingAway, "leaving"},
		{wsframe.ClosePolicyViolation, "policy"},
		{3000, "registered"},
		{4999, "private"},
		{wsframe.CloseNoStatus, ""}, // an empty close frame
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(int(tc.code)), func(t *testing.T) {
			t.Parallel()
			serverErr := make(chan error, 1)
			srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
				sc, nc := upgrade(t, w, r, "", nil)
				echo(sc, nc, serverErr)
			})
			o, _ := options(srv, "/")
			c := connect(t, o)
			if err := c.Close(context.Background(), tc.code, tc.reason); err != nil {
				t.Fatal(err)
			}
			ce, ok := errors.AsType[*wsframe.CloseError](<-serverErr)
			if !ok || ce.Code != tc.code || ce.Reason != tc.reason {
				t.Fatalf("server saw %+v", ce)
			}
			if info := c.CloseInfo(); info.Code != tc.code || !info.WasClean {
				t.Fatalf("CloseInfo = %+v", info)
			}
		})
	}
}

func TestCloseValidation(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		sc, nc := upgrade(t, w, r, "", nil)
		echo(sc, nc, make(chan error, 1))
	})
	o, _ := options(srv, "/")
	c := connect(t, o)
	long := string(make([]byte, 124))
	for _, tc := range []struct {
		code   wsframe.StatusCode
		reason string
	}{{1006, ""}, {1005, "reason"}, {999, ""}, {5000, ""}, {1000, long}, {1000, "\xff"}} {
		if err := c.Close(context.Background(), tc.code, tc.reason); errs.KindOf(err) != errs.InvalidArgument {
			t.Errorf("Close(%d, %q) = %v", tc.code, tc.reason, err)
		}
	}
	if c.State() != StateOpen {
		t.Fatalf("state = %s", c.State())
	}
	if err := c.Close(context.Background(), 1000, ""); err != nil {
		t.Fatal(err)
	}
	// Closing again is a no-op; sending fails.
	if err := c.Close(context.Background(), 1000, ""); err != nil {
		t.Fatal(err)
	}
	if err := c.SendText("late"); errCode(err) != CodeNotOpen {
		t.Fatalf("Send after close: %v", err)
	}
}

func TestServerClose(t *testing.T) {
	t.Parallel()
	serverErr := make(chan error, 1)
	srv := newServer(t, true, func(w http.ResponseWriter, r *http.Request) {
		sc, nc := upgrade(t, w, r, "", nil)
		defer nc.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		serverErr <- sc.Close(ctx, wsframe.CloseServiceRestart, "restarting")
	})
	o, st := options(srv, "/")
	c := connect(t, o)
	waitDone(t, c)
	if err := <-serverErr; err != nil {
		t.Fatalf("server Close: %v", err)
	}
	info := c.CloseInfo()
	if info.Code != wsframe.CloseServiceRestart || info.Reason != "restarting" || !info.WasClean ||
		info.InitiatedBy != InitiatorRemote || *info.Sent != (CloseFrame{wsframe.CloseServiceRestart, ""}) {
		t.Fatalf("CloseInfo = %+v sent %+v", info, info.Sent)
	}
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
	if got := st.names(); got != "connecting,open,closing,closed" {
		t.Fatalf("states = %s", got)
	}
	last := st.changes[len(st.changes)-1]
	if last.Close != info || last.Err != nil || last.At.IsZero() {
		t.Fatalf("last change = %+v", last)
	}
}

func TestAbnormalClosure(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		_, nc := upgrade(t, w, r, "", nil)
		nc.Close() // no close frame
	})
	o, st := options(srv, "/")
	c := connect(t, o)
	waitDone(t, c)
	info := c.CloseInfo()
	if info.Code != wsframe.CloseAbnormalClosure || info.WasClean || info.InitiatedBy != InitiatorNone || info.Sent != nil || info.Received != nil {
		t.Fatalf("CloseInfo = %+v", info)
	}
	if errCode(c.Err()) != wsframe.CodeAbnormalClosure {
		t.Fatalf("Err = %v", c.Err())
	}
	if got := st.names(); got != "connecting,open,closed" {
		t.Fatalf("states = %s", got)
	}
}

func TestAbort(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		sc, nc := upgrade(t, w, r, "", nil)
		echo(sc, nc, make(chan error, 1))
	})
	o, _ := options(srv, "/")
	c := connect(t, o)
	c.Abort()
	waitDone(t, c)
	info := c.CloseInfo()
	if info.Code != wsframe.CloseAbnormalClosure || info.WasClean || info.InitiatedBy != InitiatorLocal || c.Err() != nil {
		t.Fatalf("CloseInfo = %+v, Err = %v", info, c.Err())
	}
	c.Abort() // no-op
}

func TestCloseTimeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		_, nc := upgrade(t, w, r, "", nil)
		defer nc.Close()
		<-release // never answers the close frame
	})
	t.Cleanup(func() { close(release) })
	o, _ := options(srv, "/")
	c := connect(t, o)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.Close(ctx, wsframe.CloseNormalClosure, "")
	if errCode(err) != CodeCloseTimeout || errs.KindOf(err) != errs.Timeout {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("Close did not honour the context")
	}
	info := c.CloseInfo()
	if info.Code != wsframe.CloseAbnormalClosure || info.WasClean || info.InitiatedBy != InitiatorLocal || info.Sent == nil {
		t.Fatalf("CloseInfo = %+v", info)
	}
	if errCode(c.Err()) != CodeCloseTimeout {
		t.Fatalf("Err = %v", c.Err())
	}
}

func TestServerProtocolError(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		sc, nc := upgrade(t, w, r, "", nil)
		defer nc.Close()
		// A masked frame from the server fails the connection with 1002.
		_ = sc.WriteFrame(wsframe.Frame{Header: wsframe.Header{Fin: true, Opcode: wsframe.OpText, Masked: true}, Payload: []byte("x")})
		_, _, _ = sc.ReadMessage()
	})
	o, _ := options(srv, "/")
	c := connect(t, o)
	waitDone(t, c)
	info := c.CloseInfo()
	if info.Code != wsframe.CloseAbnormalClosure || info.InitiatedBy != InitiatorLocal ||
		info.Sent == nil || info.Sent.Code != wsframe.CloseProtocolError {
		t.Fatalf("CloseInfo = %+v", info)
	}
	if errCode(c.Err()) != wsframe.CodeProtocolError {
		t.Fatalf("Err = %v", c.Err())
	}
}

// TestLeftoverBytes: a frame that arrives in the same packet as the 101
// response is not lost.
func TestLeftoverBytes(t *testing.T) {
	t.Parallel()
	frame, err := wsframe.AppendFrame(nil, wsframe.Frame{Header: wsframe.Header{Fin: true, Opcode: wsframe.OpText}, Payload: []byte("early")})
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		sc, nc := upgrade(t, w, r, "", frame)
		echo(sc, nc, make(chan error, 1))
	})
	o, st := options(srv, "/")
	c := connect(t, o)
	if m := recvMessage(t, st); string(m.Data) != "early" {
		t.Fatalf("first message = %q", m.Data)
	}
	if err := c.SendBinary([]byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if m := recvMessage(t, st); m.Opcode != wsframe.OpBinary || string(m.Data) != "echo:\x01\x02" {
		t.Fatalf("second message = %q", m.Data)
	}
}

func TestConcurrentSends(t *testing.T) {
	t.Parallel()
	const senders, perSender = 8, 50
	counts := make(chan map[string]int, 1)
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		sc, nc := upgrade(t, w, r, "", nil)
		defer nc.Close()
		got := map[string]int{}
		for {
			_, msg, err := sc.ReadMessage()
			if err != nil {
				counts <- got
				return
			}
			got[string(msg)]++
		}
	})
	var frames sync.Map
	o, _ := options(srv, "/")
	o.OnFrame = func(dir wsframe.Direction, f wsframe.Frame) { frames.Store(dir, true) }
	c := connect(t, o)
	var wg sync.WaitGroup
	for s := range senders {
		wg.Go(func() {
			for i := range perSender {
				if err := c.SendText("m" + strconv.Itoa(s) + "-" + strconv.Itoa(i)); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Go(func() {
		for range 10 {
			if err := c.Ping([]byte("p")); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Wait()
	if err := c.Close(context.Background(), wsframe.CloseNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	got := <-counts
	if len(got) != senders*perSender {
		t.Fatalf("server got %d distinct messages", len(got))
	}
	if _, ok := frames.Load(wsframe.Sent); !ok {
		t.Fatal("OnFrame not called for sent frames")
	}
}

func TestClientMisuse(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		sc, nc := upgrade(t, w, r, "", nil)
		echo(sc, nc, make(chan error, 1))
	})
	o, _ := options(srv, "/")
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if c.State() != StateIdle || c.Handshake() != nil || c.CloseInfo() != nil {
		t.Fatal("new client not idle")
	}
	if err := c.SendText("x"); errCode(err) != CodeNotOpen {
		t.Fatalf("Send before Connect: %v", err)
	}
	if err := c.Close(context.Background(), 1000, ""); errCode(err) != CodeNotOpen {
		t.Fatalf("Close before Connect: %v", err)
	}
	c.Abort() // no-op
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Abort()
	if err := c.Connect(context.Background()); errCode(err) != CodeAlreadyConnected {
		t.Fatalf("second Connect: %v", err)
	}
}

// TestCloseEchoReadFirst closes when the server's echo is read before the
// write of our close frame returns (a loaded machine): the close is still
// initiated locally.
func TestCloseEchoReadFirst(t *testing.T) {
	t.Parallel()
	serverErr := make(chan error, 1)
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		sc, nc := upgrade(t, w, r, "", nil)
		echo(sc, nc, serverErr)
	})
	o, _ := options(srv, "/")
	echoRead := make(chan struct{})
	var once sync.Once
	o.OnFrame = func(dir wsframe.Direction, f wsframe.Frame) {
		if dir == wsframe.Received && f.Opcode == wsframe.OpClose {
			once.Do(func() { close(echoRead) })
		}
	}
	direct := netx.NewDirect(netx.Options{})
	o.Dialer = netx.DialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		nc, err := direct.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &echoFirstConn{Conn: nc, echoRead: echoRead}, nil
	})
	c := connect(t, o)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Close(ctx, wsframe.CloseGoingAway, ""); err != nil {
		t.Fatalf("Close: %v", err)
	}
	<-serverErr
	if info := c.CloseInfo(); info == nil || info.InitiatedBy != InitiatorLocal || info.Code != wsframe.CloseGoingAway || !info.WasClean {
		t.Fatalf("CloseInfo = %+v", info)
	}
}

// echoFirstConn holds the write of a close frame (FIN + opcode 8, the
// first byte) until the server's echo was read.
type echoFirstConn struct {
	net.Conn
	echoRead chan struct{}
}

func (c *echoFirstConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err == nil && len(p) > 0 && p[0] == 0x88 {
		select {
		case <-c.echoRead:
		case <-time.After(5 * time.Second):
		}
	}
	return n, err
}
