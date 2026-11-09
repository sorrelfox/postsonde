package grpc

import (
	"context"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// staticChecks is a loader of fixed assertions, as the request file would
// hold them; *list may change between calls.
func staticChecks(list *[]projectfile.Assertion) streamcheck.Loader {
	return func(_ context.Context, id string) (streamcheck.Checks, error) {
		if id != "req_1" {
			return streamcheck.Checks{}, nil
		}
		return streamcheck.Prepare(template.Resolver{}, *list, nil), nil
	}
}

func outcomes(tests []streamcheck.TestResultDTO) []string {
	out := make([]string, len(tests))
	for i, r := range tests {
		out[i] = r.Outcome + ": " + r.Message
	}
	return out
}

func TestServerStreamingAssertions(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	list := []projectfile.Assertion{
		{Target: "message_count", Op: "equals", Expected: "3"},
		{Target: "status", Op: "equals", Expected: "0"},
		{Target: "header", Expr: "grpc-status", Op: "equals", Expected: "0"},
		{Messages: "every", Target: "jsonpath", Expr: "$.count", Op: "equals", Expected: "7"},
		{Messages: "last", Target: "jsonpath", Expr: "$.text", Op: "equals", Expected: "#2"},
		{Messages: "nth", Index: 2, Target: "jsonpath", Expr: "$.text", Op: "equals", Expected: "#1"},
		{Messages: "any", Target: "body", Op: "contains", Expected: "#0"},
		{Messages: "none", Target: "jsonpath", Expr: "$.text", Op: "equals", Expected: "hi"},
		{Target: "message_count", Op: "gt", Expected: "3", Disabled: true},
		// Failing ones.
		{Messages: "first", Target: "jsonpath", Expr: "$.text", Op: "equals", Expected: "#9"},
		{Target: "message_count", Op: "lt", Expected: "2"},
	}
	s, rec := newCallService(t, WithChecks(staticChecks(&list)))
	id := start(t, s, input(srv.Addr, "Repeat"))
	items, _ := rec.wait(t, id)
	end := items[len(items)-1]
	if end.Type != CallEnd || len(end.Tests) != 10 {
		t.Fatalf("end %+v", end)
	}
	for i, r := range end.Tests[:8] {
		if r.Outcome != "passed" {
			t.Errorf("%d: %s", i, outcomes(end.Tests)[i])
		}
	}
	if r := end.Tests[8]; r.Outcome != "failed" || r.Index != 9 || r.Message != `Expected the first message's JSONPath $.text to equal "#9", but it was "#0".` {
		t.Errorf("first: %+v", r)
	}
	if r := end.Tests[9]; r.Outcome != "failed" || r.Message != "Expected message count to be less than 2, but it was 3." {
		t.Errorf("count: %+v", r)
	}

	// On demand, after the end, with the assertions as they are now.
	list = []projectfile.Assertion{{Messages: "last", Target: "jsonpath", Expr: "$.text", Op: "equals", Expected: "#0"}}
	again, err := s.CheckAssertions(t.Context(), CallRefDTO{OpID: id})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].Outcome != "failed" || again[0].Messages != "last" {
		t.Errorf("again %v", outcomes(again))
	}
	if _, err := s.CheckAssertions(t.Context(), CallRefDTO{OpID: "op_unknown"}); errCode(err) != streamcheck.CodeNotFound {
		t.Errorf("unknown call: %v", err)
	}
}

func TestBidiAssertionsOnDemand(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	list := []projectfile.Assertion{
		{Target: "message_count", Op: "equals", Expected: "1"},
		{Messages: "every", Target: "jsonpath", Expr: "$.text", Op: "matches", Expected: "^echo:"},
	}
	s, rec := newCallService(t, WithChecks(staticChecks(&list)))
	id := start(t, s, input(srv.Addr, "Chat"))
	send(t, s, id, "one")
	rec.waitFor(t, id, "reply", func(items []CallEventDTO) bool { return received(items) == 1 })
	// While the stream is open.
	now, err := s.CheckAssertions(t.Context(), CallRefDTO{OpID: id})
	if err != nil {
		t.Fatal(err)
	}
	if len(now) != 2 || now[0].Outcome != "passed" || now[1].Outcome != "passed" {
		t.Errorf("open: %v", outcomes(now))
	}
	send(t, s, id, "two")
	rec.waitFor(t, id, "reply", func(items []CallEventDTO) bool { return received(items) == 2 })
	if err := s.CloseSend(t.Context(), CallRefDTO{OpID: id}); err != nil {
		t.Fatal(err)
	}
	items, _ := rec.wait(t, id)
	end := items[len(items)-1]
	if len(end.Tests) != 2 || end.Tests[0].Outcome != "failed" || end.Tests[0].Message != "Expected message count to equal 1, but it was 2." || end.Tests[1].Outcome != "passed" {
		t.Errorf("end %v", outcomes(end.Tests))
	}
}

func TestCallWithoutAssertions(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t) // no loader
	items, _ := invoke(t, s, rec, input(srv.Addr, "Echo"))
	end := items[len(items)-1]
	if end.Tests == nil || len(end.Tests) != 0 {
		t.Errorf("tests %#v", end.Tests)
	}
	if items[0].Tests != nil {
		t.Errorf("request item has tests")
	}
	if _, err := s.CheckAssertions(t.Context(), CallRefDTO{OpID: "x"}); errCode(err) != streamcheck.CodeUnavailable {
		t.Errorf("without a loader: %v", err)
	}
}
