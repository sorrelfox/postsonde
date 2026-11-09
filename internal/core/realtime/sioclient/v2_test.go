package sioclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/sioclient/siotest"
	"github.com/sorrelfox/postsonde/internal/core/realtime/socketio"
)

// SIO-02: the client against in-process reference servers of socket.io v2
// (Engine.IO 3) and v3 (Engine.IO 4 without maxPayload), written from the
// protocol documents. The real servers (socket.io 2.x and 3.x from npm)
// cannot be run here; checking against them is manual.

// TestV2Transports connects to a socket.io v2 server over every
// transport: the main namespace is joined without a CONNECT, the client
// pings, and events, acks and binary data (Base64 in text payloads, the
// XHR2 binary form, WebSocket binary messages with the type byte) go both
// ways.
func TestV2Transports(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		transport Transport
		want      string
	}{
		{"websocket", TransportWebSocket, "websocket"},
		{"polling", TransportPolling, "polling"},
		{"upgrade", TransportUpgrade, "websocket"},
		{"auto", TransportAuto, "websocket"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := siotest.Start(siotest.Config{V2: true, PingInterval: 50 * time.Millisecond, PingTimeout: time.Second}, t.Cleanup)
			o, tr := options(srv)
			o.Transport, o.Version = tt.transport, VersionV2
			c, info := connect(t, o)
			if info.Transport != tt.want || info.SocketID != info.Handshake.SID || info.Handshake.MaxPayload != 0 {
				t.Errorf("info = %+v", info)
			}
			for _, x := range tr.all() {
				if x.Kind == TracePacket && x.Sent && x.Packet.Type == socketio.Connect {
					t.Errorf("a CONNECT was sent for the main namespace: %+v", x.Packet)
				}
			}
			if _, err := c.Emit("echo", []any{"hi", []byte{0, 1, 0xff}}, true); err != nil {
				t.Fatal(err)
			}
			echo := tr.wait(t, "echo", isEvent("echo", false))
			if args, _ := echo.Packet.Args(); len(args) != 3 || !bytes.Equal(args[2].([]byte), []byte{0, 1, 0xff}) {
				t.Errorf("echo = %v", args)
			}
			ack := tr.wait(t, "ack", func(x Trace) bool { return x.Matched })
			if ack.Packet.Type != socketio.BinaryAck {
				t.Errorf("ack = %+v", ack.Packet)
			}
			// The client pings (every 50 ms here) and gets pongs.
			tr.wait(t, "ping sent", func(x Trace) bool { return x.Kind == TracePing && x.Sent })
			tr.wait(t, "pong received", func(x Trace) bool { return x.Kind == TracePong && !x.Sent })
			if tt.transport == TransportUpgrade {
				tr.wait(t, "upgrade", func(x Trace) bool { return x.Kind == TraceUpgrade })
			}
			c.Disconnect(context.Background())
			waitDone(t, c)
			if !srv.WaitReceived(func(ps []socketio.Packet) bool {
				return len(ps) > 0 && ps[len(ps)-1].Type == socketio.Disconnect
			}) {
				t.Errorf("server received %v", srv.Received())
			}
			for _, r := range srv.Requests() {
				if q := r.URL.Query(); q.Get("EIO") != "3" || (q.Get("transport") == "polling" && q.Get("b64") != "1") {
					t.Errorf("request %s", r.URL)
				}
			}
		})
	}
}

// TestV2BinaryPayloads: a v2 server that answers polls in the XHR2 binary
// form (a client without b64=1) is read too.
func TestV2BinaryPayloads(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{V2: true, OnEvent: func(s *siotest.Session, p *socketio.Packet) {
		if p.EventName() == "file" {
			s.Emit("/", "file", []byte("abc"))
		}
	}}, t.Cleanup)
	o, tr := options(srv)
	o.Transport, o.Version = TransportPolling, VersionV2
	c, _ := connect(t, o)
	srv.Sessions()[0].SetB64(false)
	if _, err := c.Emit("file", nil, false); err != nil {
		t.Fatal(err)
	}
	got := tr.wait(t, "file", isEvent("file", false))
	if args, _ := got.Packet.Args(); len(args) != 2 || string(args[1].([]byte)) != "abc" {
		t.Errorf("file = %v", args)
	}
}

// TestV2Namespaces: another namespace is joined with a CONNECT that
// carries the URL's query, as socket.io-client 2 sends it; a refusal is a
// string; the auth payload cannot be used with v2.
func TestV2Namespaces(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{V2: true, Namespaces: []string{"/admin"}, Authorize: func(nsp string, auth json.RawMessage) error {
		var q map[string]string
		_ = json.Unmarshal(auth, &q)
		if nsp == "/admin" && q["token"] != "s3cret" {
			return errors.New("not authorized")
		}
		return nil
	}}, t.Cleanup)
	for _, transport := range []Transport{TransportWebSocket, TransportPolling} {
		o, tr := options(srv)
		o.URL += "/admin?token=s3cret"
		o.Transport, o.Version = transport, VersionV2
		c, info := connect(t, o)
		if info.Namespace != "/admin" || !strings.HasPrefix(info.SocketID, "/admin#") {
			t.Errorf("%s: info = %+v", transport, info)
		}
		sent := tr.wait(t, "CONNECT", func(x Trace) bool { return x.Kind == TracePacket && x.Sent && x.Packet.Type == socketio.Connect })
		if sent.Packet.Namespace != "/admin?token=s3cret" || len(sent.Packet.Data) != 0 {
			t.Errorf("CONNECT = %+v", sent.Packet)
		}
		if _, err := c.Emit("echo", []any{1}, false); err != nil {
			t.Fatal(err)
		}
		if got := tr.wait(t, "echo", isEvent("echo", false)); got.Packet.Namespace != "/admin" {
			t.Errorf("echo on %q", got.Packet.Namespace)
		}

		for url, msg := range map[string]string{"/admin?token=no": "not authorized", "/other": "Invalid namespace"} {
			o, _ := options(srv)
			o.URL += url
			o.Transport, o.Version = transport, VersionV2
			c, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Connect(context.Background())
			if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != CodeConnectError || e.Details["message"] != msg {
				t.Errorf("%s %s: err = %v", transport, url, err)
			}
		}
	}
	o, _ := options(srv)
	o.Version, o.Auth = VersionV2, json.RawMessage(`{"token":"x"}`)
	if _, err := New(o); codeOf(err) != CodeInvalidAuth {
		t.Errorf("auth with v2: %v", err)
	}
	o.Auth, o.Version = nil, "v9"
	if _, err := New(o); codeOf(err) != CodeInvalidVersion {
		t.Errorf("bad version: %v", err)
	}
}

// TestV2PingTimeout: a v2 server that stops answering the client's pings
// ends the connection after the ping interval plus the ping timeout.
func TestV2PingTimeout(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{V2: true, NoPings: true, PingInterval: 40 * time.Millisecond, PingTimeout: 60 * time.Millisecond}, t.Cleanup)
	o, _ := options(srv)
	o.Transport, o.Version = TransportWebSocket, VersionV2
	c, _ := connect(t, o)
	waitDone(t, c)
	if c.Reason() != ReasonPingTimeout || codeOf(c.Err()) != CodePingTimeout {
		t.Errorf("reason %q, err %v", c.Reason(), c.Err())
	}
}

// TestVersionMismatch: a v3/v4 client gets a clear error from a v2
// server, and a v2 client from a v3/v4 server without allowEIO3.
func TestVersionMismatch(t *testing.T) {
	t.Parallel()
	v2 := siotest.Start(siotest.Config{V2: true}, t.Cleanup)
	o, _ := options(v2)
	o.Transport = TransportPolling
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Connect(context.Background())
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != CodeVersionMismatch || !strings.Contains(e.Hint, "Socket.IO v2") {
		t.Errorf("v4 client, v2 server: %v", err)
	}

	v4 := siotest.Start(siotest.Config{}, t.Cleanup)
	o, _ = options(v4)
	o.Transport, o.Version = TransportPolling, VersionV2
	c, err = New(o)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Connect(context.Background())
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != CodeHandshakeRejected || !strings.Contains(e.Hint, "v3 / v4") {
		t.Errorf("v2 client, v4 server: %v", err)
	}
}

// TestV3Server: a socket.io v3 server (a handshake without maxPayload)
// over every transport, with auth, events and acks.
func TestV3Server(t *testing.T) {
	t.Parallel()
	for _, transport := range []Transport{TransportWebSocket, TransportPolling, TransportUpgrade} {
		srv := siotest.Start(siotest.Config{SocketIOv3: true, Namespaces: []string{"/admin"}}, t.Cleanup)
		o, tr := options(srv)
		o.URL += "/admin"
		o.Transport, o.Auth = transport, json.RawMessage(`{"token":"t"}`)
		c, info := connect(t, o)
		if info.Handshake.MaxPayload != 0 || info.SocketID == "" {
			t.Errorf("%s: info = %+v", transport, info)
		}
		if _, err := c.Emit("echo", []any{[]byte{7}}, true); err != nil {
			t.Fatal(err)
		}
		tr.wait(t, "echo", isEvent("echo", false))
		tr.wait(t, "ack", func(x Trace) bool { return x.Matched })
	}
}

// TestDisconnectPollingCloseApart: over long-polling the DISCONNECT and
// the Engine.IO close go in two POSTs, in that order, as engine.io-client
// sends them. socket.io handles a DISCONNECT on the next tick, so with
// both in one payload the real servers (2.x to 4.x) closed the transport
// first and reported "transport close" instead of "client namespace
// disconnect" (found by the interop tests).
func TestDisconnectPollingCloseApart(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		version             Version
		disconnect, closing string
	}{
		{VersionV4, "41", "1"},
		{VersionV2, "2:41", "1:1"},
	} {
		t.Run(string(tt.version), func(t *testing.T) {
			t.Parallel()
			srv := siotest.Start(siotest.Config{V2: tt.version == VersionV2}, t.Cleanup)
			o, _ := options(srv)
			o.Transport, o.Version = TransportPolling, tt.version
			c, _ := connect(t, o)
			c.Disconnect(context.Background())
			waitDone(t, c)
			posts := srv.Posts()
			if len(posts) < 2 || string(posts[len(posts)-2]) != tt.disconnect || string(posts[len(posts)-1]) != tt.closing {
				t.Errorf("POST bodies = %q, want …, %q, %q", posts, tt.disconnect, tt.closing)
			}
		})
	}
}
