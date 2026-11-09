package socketio

import (
	"context"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/realtime/sioclient/siotest"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// TestAssertionsOnEvents: the events received are checked when the
// connection closes, and on demand while it is open.
func TestAssertionsOnEvents(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{}, t.Cleanup)
	f := newFixture(t, nil)
	list := []projectfile.Assertion{
		{Target: "message_count", Op: "equals", Expected: "2"},
		{Messages: "every", Target: "header", Expr: "Event", Op: "equals", Expected: "echo"},
		{Messages: "last", Target: "jsonpath", Expr: "$[0].n", Op: "equals", Expected: "2"},
		{Messages: "any", Target: "jsonpath", Expr: "$[1]", Op: "equals", Expected: "x"},
	}
	f.svc.checks = func(_ context.Context, id string) (streamcheck.Checks, error) {
		return streamcheck.Prepare(template.Resolver{}, list, nil), nil
	}
	id := f.connect(t, ConnectInputDTO{RequestID: "req_1", URL: srv.URL, Transport: "websocket"})
	f.rec.waitFor(t, id, "connected", isType(ItemConnected))
	for _, args := range []string{`[{"n": 1}, "x"]`, `[{"n": 2}]`} {
		if _, err := f.svc.Emit(t.Context(), EmitInputDTO{OpID: id, Event: "echo", Args: args}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	received := func() int {
		f.svc.mu.Lock()
		c := f.svc.conns[id]
		f.svc.mu.Unlock()
		return c.log.Count()
	}
	for received() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	now, err := f.svc.CheckAssertions(t.Context(), CheckInputDTO{OpID: id})
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range now {
		if r.Outcome != "passed" {
			t.Errorf("open %d: %s", i, r.Message)
		}
	}
	if _, err := f.svc.Emit(t.Context(), EmitInputDTO{OpID: id, Event: "bye"}); err != nil {
		t.Fatal(err)
	}
	f.rec.wait(t, id)
	closed := f.rec.waitFor(t, id, "closed", isType(ItemClosed))
	if len(closed.Tests) != 4 || closed.Tests[0].Outcome != "passed" || closed.Tests[3].Outcome != "passed" {
		t.Errorf("closed tests %+v", closed.Tests)
	}
	list = []projectfile.Assertion{{Target: "message_count", Op: "equals", Expected: "3"}}
	after, err := f.svc.CheckAssertions(t.Context(), CheckInputDTO{OpID: id})
	if err != nil || len(after) != 1 || after[0].Outcome != "failed" {
		t.Errorf("after %+v %v", after, err)
	}
}
