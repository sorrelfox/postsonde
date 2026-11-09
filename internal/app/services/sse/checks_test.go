package sse

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// TestAssertionsOnEvents checks a request's assertions against the
// events of its stream when it ends, and again on demand.
func TestAssertionsOnEvents(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Feed", "prices")
		_, _ = io.WriteString(w, "event: price\nid: 1\ndata: {\"px\":12.5}\n\nevent: price\nid: 2\ndata: {\"px\":13}\n\nevent: done\ndata: bye\n\n")
	}))
	defer srv.Close()
	src := sseSource(srv.URL)
	src.FolderVariables = [][]projectfile.Variable{{{Name: "max", Value: "20", Secret: true}}}
	src.Request.Assertions = []projectfile.Assertion{
		{Target: "status", Op: "equals", Expected: "200"},
		{Target: "header", Expr: "X-Feed", Op: "equals", Expected: "prices"},
		{Target: "message_count", Op: "equals", Expected: "3"},
		{Messages: "first", Target: "jsonpath", Expr: "$.px", Op: "lt", Expected: "{{max}}"},
		{Messages: "last", Target: "header", Expr: "Event", Op: "equals", Expected: "done"},
		{Messages: "any", Target: "header", Expr: "Id", Op: "equals", Expected: "2"},
		{Messages: "every", Target: "header", Expr: "Event", Op: "equals", Expected: "price"},
		{Messages: "nth", Index: 2, Target: "body", Op: "contains", Expected: "13", Disabled: true},
	}
	f := newFixture(t, src)
	id := f.connect(t)
	f.rec.wait(t, id)
	items := f.rec.list(id)
	end := items[len(items)-1]
	if end.Type != ItemEnd || len(end.Tests) != 7 {
		t.Fatalf("end %+v", end)
	}
	want := []string{"passed", "passed", "passed", "passed", "passed", "passed", "failed"}
	for i, r := range end.Tests {
		if r.Outcome != want[i] {
			t.Errorf("%d: %s %s", i, r.Outcome, r.Message)
		}
	}
	// The secret the assertion used is masked.
	if r := end.Tests[3]; r.Expected != "••••••" || r.Message != "The first message's JSONPath $.px is less than ••••••: it was 12.5." {
		t.Errorf("secret: %+v", r)
	}
	if r := end.Tests[6]; r.Message != `Expected every message's header Event to equal "price", but in message 3 it was "done".` {
		t.Errorf("every: %q", r.Message)
	}
	for _, it := range items[:len(items)-1] {
		if it.Tests != nil {
			t.Errorf("%s item has tests", it.Type)
		}
	}

	again, err := f.svc.CheckAssertions(t.Context(), CheckInputDTO{OpID: id})
	if err != nil || len(again) != 7 || again[2].Outcome != "passed" {
		t.Errorf("again %+v %v", again, err)
	}
	if _, err := f.svc.CheckAssertions(t.Context(), CheckInputDTO{OpID: "op_x"}); err == nil {
		t.Error("unknown stream checked")
	}
}

// TestProbeRecordsEvents: a folder run reads events into a log for the
// checks.
func TestProbeRecordsEvents(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: a\n\ndata: b\n\n")
	}))
	defer srv.Close()
	f := newFixture(t, sseSource(srv.URL))
	target, err := f.svc.open(t.Context(), "req_sse", false)
	if err != nil {
		t.Fatal(err)
	}
	log := streamcheck.NewLog(time.Now())
	ctx := t.Context()
	p, err := ProbeInto(netx.WithUserOp(ctx, netx.UserOp{Kind: "test", ID: "probe"}), target.Stream, 5*time.Second, log)
	if err != nil || p.Events != 2 {
		t.Fatalf("probe %+v %v", p, err)
	}
	s := log.Stream()
	if len(s.Messages) != 2 || string(s.Messages[1].Body) != "b" || s.Status != 200 || s.Messages[0].Header.Get("Event") != "message" {
		t.Errorf("log %+v", s)
	}
}
