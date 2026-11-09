package websocket

import (
	"context"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

func TestAssertionsOnMessages(t *testing.T) {
	t.Parallel()
	srv, _ := echoServer(t)
	list := []projectfile.Assertion{
		{Target: "message_count", Op: "equals", Expected: "3"},
		{Target: "status", Op: "equals", Expected: "101"},
		{Target: "header", Expr: "Upgrade", Op: "equals", Expected: "websocket"},
		{Messages: "first", Target: "body", Op: "equals", Expected: "echo hello"},
		{Messages: "any", Target: "regex", Expr: `"id":(\d+)`, Op: "equals", Expected: "{{id}}"},
		{Messages: "last", Target: "header", Expr: "Type", Op: "equals", Expected: "binary"},
		{Messages: "none", Target: "body", Op: "contains", Expected: "error"},
		{Messages: "every", Target: "body", Op: "matches", Expected: "^echo "},
	}
	loads := 0
	load := func(_ context.Context, id string) (streamcheck.Checks, error) {
		loads++
		if id != "req_feed" {
			t.Errorf("loaded %q", id)
		}
		r := template.Resolver{Scopes: template.Chain{template.MapScope{Label: "env", Variables: map[string]template.Variable{"id": {Value: "1"}}}}}
		return streamcheck.Prepare(r, list, nil), nil
	}
	s, ev := newService(t, nil, WithChecks(load))
	ctx := t.Context()
	conn, err := s.Connect(ctx, ConnectInputDTO{RequestID: "req_feed", URL: wsURL(srv, "/feed")})
	if err != nil {
		t.Fatal(err)
	}
	id := conn.ConnID
	ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(id) == "connecting,open" })
	for i, text := range []string{"hello", `{"id":1}`, "binary"} {
		if _, err := s.Send(ctx, SendInputDTO{ConnID: id, Text: text}); err != nil {
			t.Fatal(err)
		}
		ev.waitFor(t, "a reply", func(e *events) bool { return len(e.log(id)) >= 2*(i+1) })
	}

	// On demand while open: the binary reply does not start with "echo ".
	now, err := s.CheckAssertions(ctx, CheckInputDTO{ConnID: id})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"passed", "passed", "passed", "passed", "passed", "passed", "passed", "failed"}
	for i, r := range now {
		if r.Outcome != want[i] {
			t.Errorf("open %d: %s %s", i, r.Outcome, r.Message)
		}
	}
	if len(now) != len(want) || now[4].Expected != "1" || now[7].Message != `Expected every message's body to match /^echo /, but in message 3 it was "\x00\xffb".` {
		t.Errorf("open: %+v", now)
	}

	// At the end: the results are in the closed event.
	if err := s.Close(ctx, CloseInputDTO{ConnID: id}); err != nil {
		t.Fatal(err)
	}
	closed := ev.state(id, "closed")
	if closed == nil || len(closed.Tests) != len(want) || closed.Tests[0].Outcome != "passed" || closed.Tests[7].Outcome != "failed" {
		t.Fatalf("closed tests %+v", closed)
	}
	if open := ev.state(id, "open"); open.Tests != nil {
		t.Errorf("open event has tests")
	}

	// After the end, with edited assertions.
	list = []projectfile.Assertion{{Target: "message_count", Op: "gte", Expected: "4"}}
	after, err := s.CheckAssertions(ctx, CheckInputDTO{ConnID: id})
	if err != nil || len(after) != 1 || after[0].Message != "Expected message count to be at least 4, but it was 3." {
		t.Errorf("after: %+v %v", after, err)
	}
	if loads != 3 {
		t.Errorf("loaded %d times", loads)
	}
	_, err = s.CheckAssertions(ctx, CheckInputDTO{ConnID: "ws_nope"})
	checkCode(t, err, errs.NotFound, streamcheck.CodeNotFound)
}

func TestAssertionsWithoutLoader(t *testing.T) {
	t.Parallel()
	srv, _ := echoServer(t)
	s, ev := newService(t, nil)
	conn, err := s.Connect(t.Context(), ConnectInputDTO{RequestID: "req_feed", URL: wsURL(srv, "/feed")})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(conn.ConnID) == "connecting,open" })
	if err := s.Close(t.Context(), CloseInputDTO{ConnID: conn.ConnID}); err != nil {
		t.Fatal(err)
	}
	if closed := ev.state(conn.ConnID, "closed"); closed.Tests == nil || len(closed.Tests) != 0 {
		t.Errorf("tests %#v", closed.Tests)
	}
	_, err = s.CheckAssertions(t.Context(), CheckInputDTO{ConnID: conn.ConnID})
	checkCode(t, err, errs.Unsupported, streamcheck.CodeUnavailable)
}
