package wsserver_test

import (
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/wsserver"
)

// End to end: a WebSocket client against the running server.

func TestAutoReplyRules(t *testing.T) {
	t.Parallel()
	f := start(t, wsserver.Config{Echo: true, Rules: []wsserver.Rule{
		{Enabled: true, Match: wsserver.MatchExact, Pattern: "ping", Reply: "pong"},
		{Enabled: true, Match: wsserver.MatchRegex, Pattern: `^get (?P<id>\d+)$`, Reply: `{"id": ${id}}`},
		{Enabled: true, Match: wsserver.MatchContains, Pattern: "slow", Reply: "late", DelayMs: 150},
	}})
	c := dial(t, f, "/")
	cl := f.waitClients(t, 1)[0]

	send := func(s string) {
		t.Helper()
		if err := c.c.SendText(s); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(op wsframe.Opcode, want string) {
		t.Helper()
		if m := c.next(t); m.Opcode != op || string(m.Data) != want {
			t.Errorf("client got %v %q, want %v %q", m.Opcode, m.Data, op, want)
		}
	}
	send("ping")
	expect(wsframe.OpText, "pong")
	send("get 42")
	expect(wsframe.OpText, `{"id": 42}`)
	// No rule matches: echo is the fallback, for text and binary.
	send("unknown")
	expect(wsframe.OpText, "unknown")
	if err := c.c.SendBinary([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	expect(wsframe.OpBinary, "ping")

	// A delayed reply does not hold up the next message.
	sent := time.Now()
	send("slow please")
	send("ping")
	expect(wsframe.OpText, "pong")
	expect(wsframe.OpText, "late")
	if d := time.Since(sent); d < 150*time.Millisecond {
		t.Errorf("the delayed reply came after %v", d)
	}

	// Replies are logged like sends, naming the rule, after the message
	// they answer.
	msgs := f.rec.waitFor(t, servers.KindMessage, 12)
	want := []struct {
		dir     servers.Direction
		summary string
		content string
	}{
		{servers.In, "Text message from client " + cl.ID, "ping"},
		{servers.Out, "Text message to client " + cl.ID + " (auto-reply rule 1)", "pong"},
		{servers.In, "Text message from client " + cl.ID, "get 42"},
		{servers.Out, "Text message to client " + cl.ID + " (auto-reply rule 2)", `{"id": 42}`},
		{servers.In, "Text message from client " + cl.ID, "unknown"},
		{servers.Out, "Text message echoed to client " + cl.ID, "unknown"},
		{servers.In, "Binary message from client " + cl.ID, "ping"},
		{servers.Out, "Binary message echoed to client " + cl.ID, "ping"},
	}
	for i, w := range want {
		m := msgs[i]
		if m.Direction != w.dir || m.Summary != w.summary || string(m.Content) != w.content || m.WS.ClientID != cl.ID {
			t.Errorf("event %d = %v %q %q, want %v %q %q", i, m.Direction, m.Summary, m.Content, w.dir, w.summary, w.content)
		}
	}
	if last := msgs[11]; last.Summary != "Text message to client "+cl.ID+" (auto-reply rule 3)" || string(last.Content) != "late" {
		t.Errorf("delayed reply event = %q %q", last.Summary, last.Content)
	}
	if cl := f.hub(t).Clients()[0]; cl.MessagesOut != 6 {
		t.Errorf("messages out = %d", cl.MessagesOut)
	}

	// New rules apply to the connected client; without echo an unmatched
	// message gets no answer (the next reply is the next rule's).
	if _, err := f.m.UpdateConfig(f.id, wsserver.Config{Rules: []wsserver.Rule{
		{Enabled: false, Match: wsserver.MatchExact, Pattern: "ping", Reply: "off"},
		{Enabled: true, Match: wsserver.MatchRegex, Pattern: `(\w+)@`, Reply: "user $1"},
	}}); err != nil {
		t.Fatal(err)
	}
	send("ping")
	send("mail bob@example.com")
	expect(wsframe.OpText, "user bob")
	if _, err := f.m.UpdateConfig(f.id, wsserver.Config{Rules: []wsserver.Rule{{Enabled: true, Match: wsserver.MatchRegex, Pattern: "("}}}); !hasCode(err, wsserver.CodeInvalidConfig) {
		t.Errorf("a bad regex = %v", err)
	}
}

func TestDelayedReplyToAClientThatLeft(t *testing.T) {
	t.Parallel()
	f := start(t, wsserver.Config{Rules: []wsserver.Rule{{Enabled: true, Match: wsserver.MatchExact, Pattern: "x", Reply: "y", DelayMs: 100}}})
	c := dial(t, f, "/")
	f.waitClients(t, 1)
	if err := c.c.SendText("x"); err != nil {
		t.Fatal(err)
	}
	f.rec.waitFor(t, servers.KindMessage, 1)
	c.c.Abort()
	f.rec.waitFor(t, servers.KindConnectionClose, 1)
	time.Sleep(200 * time.Millisecond)
	if n := len(f.rec.ofKind(servers.KindMessage)); n != 1 {
		t.Errorf("message events = %d, want only the one received", n)
	}
	if errEv := f.rec.ofKind(servers.KindError); len(errEv) != 0 {
		t.Errorf("error events = %+v", errEv)
	}
}
