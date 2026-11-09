package sioclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/socketio"
	"github.com/sorrelfox/postsonde/internal/testutil/nodeharness"
)

// The interop tests run the client against the official socket.io servers
// (Node, scripts/sio-interop/server.js). They are opt-in: they are skipped
// unless POSTSONDE_SIO_INTEROP names the install directory that
// scripts/sio-interop.sh made, so `go test ./...` never needs Node. See
// docs/socketio.md#interop-with-the-official-implementations.

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// inNamespace narrows match to packets of namespace nsp: a socket.io v2
// server also joins the client to "/" and greets it there.
func inNamespace(nsp string, match func(Trace) bool) func(Trace) bool {
	return func(x Trace) bool {
		if !match(x) {
			return false
		}
		got := x.Packet.Namespace
		if got == "" {
			got = socketio.MainNamespace
		}
		return got == nsp
	}
}

// TestInteropReferenceServers connects to socket.io 2.x, 3.x and 4.x
// servers (and 4.x with allowEIO3 in v2 mode) over long-polling only,
// WebSocket only and the upgrade, to the main namespace and to one that
// needs auth: the server's event with a Buffer, an emit with text, JSON
// and binary arguments and an ack whose arguments say how the server saw
// them, a large non-ASCII emit, the heartbeat and the disconnect; then the
// refusals and a server-side disconnect.
func TestInteropReferenceServers(t *testing.T) {
	root := nodeharness.Dir(t)
	modes := []struct {
		mode, dir string
		version   Version
	}{
		{"v2", "v2", VersionV2},
		{"v3", "v3", VersionV4},
		{"v4", "v4", VersionV4},
		{"v4-eio3", "v4", VersionV2},
	}
	for _, m := range modes {
		t.Run(m.mode, func(t *testing.T) {
			nlog, ready := nodeharness.Start(t, "server.js", filepath.Join(root, m.dir), m.mode)
			t.Logf("%s: %v", m.mode, ready["versions"])
			base := "http://127.0.0.1:" + strconv.Itoa(int(ready["port"].(float64)))
			for _, transport := range []Transport{TransportPolling, TransportWebSocket, TransportUpgrade} {
				for _, nsp := range []string{"/", "/admin"} {
					t.Run(string(transport)+nsp, func(t *testing.T) {
						interopSession(t, nlog, base, m.version, transport, nsp)
					})
				}
			}
			t.Run("refusals", func(t *testing.T) {
				for _, tc := range []struct{ nsp, token, message string }{
					{"/admin", "nope", "not authorized"},
					{"/admin", "", "not authorized"},
					{"/nope", "", "Invalid namespace"},
				} {
					o := interopOptions(base, m.version, TransportAuto, tc.nsp, tc.token, nil)
					c, err := New(o)
					if err != nil {
						t.Fatal(err)
					}
					_, err = c.Connect(context.Background())
					if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != CodeConnectError || e.Details["message"] != tc.message {
						t.Errorf("%s with token %q: %v", tc.nsp, tc.token, err)
					}
				}
			})
			t.Run("server disconnect", func(t *testing.T) {
				tr := newTraces()
				o := interopOptions(base, m.version, TransportUpgrade, "/admin", "s3cret", tr.add)
				c, _ := connect(t, o)
				if _, err := c.Emit("bye", nil, false); err != nil {
					t.Fatal(err)
				}
				waitDone(t, c)
				if c.Reason() != ReasonServerDisconnect {
					t.Errorf("reason %q, err %v", c.Reason(), c.Err())
				}
			})
		})
	}
}

func interopOptions(base string, v Version, transport Transport, nsp, token string, onTrace func(Trace)) Options {
	o := Options{
		URL: base + strings.TrimSuffix(nsp, "/"), Version: v, Transport: transport,
		Dialer: netx.NewDirect(netx.Options{}), OnTrace: onTrace,
		HandshakeTimeout: 10 * time.Second, AckTimeout: 5 * time.Second,
	}
	if token != "" {
		if v == VersionV2 {
			o.URL += "?token=" + token
		} else {
			o.Auth = json.RawMessage(`{"token":` + strconv.Quote(token) + `}`)
		}
	}
	return o
}

func interopSession(t *testing.T, nlog *nodeharness.Process, base string, v Version, transport Transport, nsp string) {
	tr := newTraces()
	token := ""
	if nsp == "/admin" {
		token = "s3cret"
	}
	c, info := connect(t, interopOptions(base, v, transport, nsp, token, tr.add))
	wantTransport := "websocket"
	if transport == TransportPolling {
		wantTransport = "polling"
	}
	if info.Transport != wantTransport || info.Namespace != nsp || info.SocketID == "" {
		t.Fatalf("info = %+v", info)
	}
	if transport == TransportUpgrade {
		tr.wait(t, "open over polling", func(x Trace) bool { return x.Kind == TraceOpen && x.Transport == "polling" })
		tr.wait(t, "upgrade", func(x Trace) bool { return x.Kind == TraceUpgrade && x.Transport == "websocket" })
	}
	id := info.SocketID
	nlog.Wait(t, "connection", func(m nodeharness.Line) bool { return m["ev"] == "connection" && m["id"] == id })

	// The server's event, with an object and a Buffer.
	welcome := tr.wait(t, "welcome", inNamespace(nsp, isEvent("welcome", false)))
	args, err := welcome.Packet.Args()
	if err != nil || len(args) != 3 {
		t.Fatalf("welcome args = %v, %v", args, err)
	}
	if got := asJSON(t, args[1]); got != asJSON(t, map[string]any{"nsp": nsp, "id": id}) {
		t.Errorf("welcome object = %s", got)
	}
	if b, ok := args[2].([]byte); !ok || !bytes.Equal(b, []byte{1, 2, 3}) {
		t.Errorf("welcome binary = %#v", args[2])
	}

	// An emit with text, JSON and binary arguments and an ack.
	res, err := c.Emit("echo", []any{"hi", map[string]any{"n": 1, "s": "€😀"}, []byte{0, 1, 2, 0xff}}, true)
	if err != nil || !res.HasAck || res.Attachments != 1 {
		t.Fatalf("Emit = %+v, %v", res, err)
	}
	ack := tr.wait(t, "ack", func(x Trace) bool { return x.Kind == TracePacket && x.Matched && x.AckID == res.AckID })
	args, err = ack.Packet.Args()
	if err != nil || len(args) != 4 {
		t.Fatalf("ack args = %v, %v", args, err)
	}
	if args[0] != "ok" || args[2] != wantTransport {
		t.Errorf("ack = %v, want ok and transport %s", args, wantTransport)
	}
	if got, want := asJSON(t, args[1]), `["hi",{"n":1,"s":"€😀"},{"binary":"000102ff"}]`; got != want {
		t.Errorf("the server received %s, want %s", got, want)
	}
	if b, ok := args[3].([]byte); !ok || !bytes.Equal(b, []byte{0xde, 0xad, 0xbe, 0xef}) {
		t.Errorf("ack binary = %#v", args[3])
	}
	echo := tr.wait(t, "echo", inNamespace(nsp, isEvent("echo", false)))
	args, err = echo.Packet.Args()
	if err != nil || len(args) != 4 || args[1] != "hi" || asJSON(t, args[2]) != `{"n":1,"s":"€😀"}` {
		t.Errorf("echo args = %v, %v", args, err)
	} else if b, ok := args[3].([]byte); !ok || !bytes.Equal(b, []byte{0, 1, 2, 0xff}) {
		t.Errorf("echo binary = %#v", args[3])
	}

	// A large text argument with characters outside the BMP (UTF-16
	// lengths in Engine.IO 3 payloads), without an ack.
	big := strings.Repeat("a€😀", 20_000)
	if _, err := c.Emit("echo", []any{big}, false); err != nil {
		t.Fatal(err)
	}
	tr.wait(t, "big echo", func(x Trace) bool {
		if !inNamespace(nsp, isEvent("echo", false))(x) {
			return false
		}
		a, err := x.Packet.Args()
		return err == nil && len(a) == 2 && a[1] == big
	})

	// The heartbeat keeps the connection up (pingInterval 500 ms).
	time.Sleep(1300 * time.Millisecond)
	beats := 0
	for _, x := range tr.all() {
		if (v == VersionV2 && x.Kind == TracePong && !x.Sent) || (v != VersionV2 && x.Kind == TracePing && !x.Sent) {
			beats++
		}
	}
	if beats < 2 || c.State() != StateOpen {
		t.Errorf("after 1.3 s: %d heartbeats, state %s, err %v", beats, c.State(), c.Err())
	}

	c.Disconnect(context.Background())
	waitDone(t, c)
	if c.Reason() != ReasonClientDisconnect || c.Err() != nil {
		t.Errorf("reason %q, err %v", c.Reason(), c.Err())
	}
	dis := nlog.Wait(t, "disconnect", func(m nodeharness.Line) bool { return m["ev"] == "disconnect" && m["id"] == id })
	if dis["reason"] != "client namespace disconnect" {
		t.Errorf("the server saw the disconnect as %q", dis["reason"])
	}
}
