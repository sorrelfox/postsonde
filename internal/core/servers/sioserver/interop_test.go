package sioserver_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/sioserver"
	"github.com/sorrelfox/postsonde/internal/testutil/nodeharness"
)

// TestInteropOfficialClient connects the official socket.io-client v4
// (Node, scripts/sio-interop/client.js) to the server over long-polling
// only, WebSocket only and the default upgrade, with "/" and "/admin"
// joined over one connection, both needing auth: a rule's ack and event
// with binary data, an echoed emit with binary data, the heartbeat, the
// server's event with an ack request answered with binary data, the
// client's disconnects, and the refusals. Opt-in: skipped unless
// POSTSONDE_SIO_INTEROP is set (docs/socketio.md#interop-with-the-official-implementations).
func TestInteropOfficialClient(t *testing.T) {
	dir := filepath.Join(nodeharness.Dir(t), "v4")
	for _, mode := range []string{"polling", "websocket", "upgrade"} {
		t.Run(mode, func(t *testing.T) {
			f := start(t, sioserver.Config{
				Namespaces: []string{"/admin"}, Auth: `{"token":"s3cret"}`, Echo: true, AckMode: sioserver.AckEcho,
				PingIntervalMs: 500, PingTimeoutMs: 2000,
				Rules: []sioserver.Rule{{
					Enabled: true, Pattern: "hello", Ack: `["hi", {"$base64": "3q2+7w=="}]`,
					Emit: "greeting", EmitArgs: `[{"n": 1}, {"$base64": "AQID"}]`,
				}},
			})
			node, ready := nodeharness.Start(t, "client.js", dir, f.url, mode)
			t.Logf("%s: %v", mode, ready["versions"])
			want := "websocket"
			if mode == "polling" {
				want = "polling"
			}

			var left []string
			for _, nsp := range []string{"/", "/admin"} {
				ask := node.Wait(t, "ask "+nsp, func(l nodeharness.Line) bool {
					return (l["step"] == "ask" && l["nsp"] == nsp) || l["step"] == "fail"
				})
				if ask["step"] == "fail" {
					t.Fatalf("client.js: %v", ask["error"])
				}
				var so sioserver.Socket
				for _, s := range f.hub(t).Sockets() {
					if s.SID == ask["id"] {
						so = s
					}
				}
				if so.ID == "" || so.Namespace != nsp || so.Transport != want || !strings.Contains(so.Auth, `"s3cret"`) {
					t.Fatalf("socket of %v: %+v", ask, so)
				}
				// What the server got from the official client: binary
				// arguments as attachments.
				f.rec.waitFor(t, "hello from "+so.ID, func(ev servers.Event) bool {
					return ev.SIO != nil && ev.SIO.SocketID == so.ID && ev.SIO.Event == "hello" && ev.Direction == servers.In &&
						ev.SIO.Packet == "BINARY_EVENT" && ev.SIO.Attachments == 1 && strings.Contains(string(ev.Content), `"AAEC/w=="`)
				})
				res, err := f.hub(t).Emit(sioserver.Target{SocketID: so.ID}, "ask", []any{"q", []byte{5, 6}}, true)
				if err != nil || res.Sent != 1 {
					t.Fatalf("Emit ask = %+v, %v", res, err)
				}
				ack := f.rec.waitFor(t, "ack of ask from "+so.ID, func(ev servers.Event) bool {
					return ev.SIO != nil && ev.SIO.SocketID == so.ID && ev.SIO.Event == "ask" && ev.Direction == servers.In && ev.SIO.HasAck
				})
				c := string(ack.Content)
				if ack.SIO.Packet != "BINARY_ACK" || ack.SIO.Attachments != 2 || !strings.Contains(c, `"answer"`) ||
					!strings.Contains(c, `"yv4="`) || !strings.Contains(c, `"q"`) || !strings.Contains(c, `"BQY="`) {
					t.Errorf("ack of ask: %s %+v", c, ack.SIO)
				}
				if _, err := f.hub(t).Emit(sioserver.Target{SocketID: so.ID}, "finish", nil, false); err != nil {
					t.Fatal(err)
				}
				left = append(left, "Socket "+so.ID+" left "+nsp)
			}

			end := node.Wait(t, "done", func(l nodeharness.Line) bool { return l["step"] == "done" || l["step"] == "fail" })
			if end["step"] != "done" || end["transport"] != want {
				t.Fatalf("client.js: %v", end)
			}
			for _, l := range left {
				f.rec.waitFor(t, l, summaryHas(l))
			}
			for _, l := range node.Lines() {
				t.Logf("client.js: %v", l)
			}
		})
	}
}
