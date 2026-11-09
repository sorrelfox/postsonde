package http

import (
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// TestRunSessionSend: a run's send is built like Send's, with the run's
// variables above every other scope, keeps no body and goes into the
// history once per request, when the session closes.
func TestRunSessionSend(t *testing.T) {
	t.Parallel()
	srv := newHitServer(t, false)
	src := httpSource("{{base}}/x")
	src.FolderVariables = [][]projectfile.Variable{{{Name: "base", Value: "http://127.0.0.1:1"}}}
	src.Request.Assertions = []projectfile.Assertion{{Target: "status", Op: "equals", Expected: "{{want}}"}}
	var hist historyEntries
	f := newFixture(t, src, nil, WithSaveBody(t.TempDir(), nil), hist.option())

	// A run's sends go out on its operation's context.
	ctx := netx.WithUserOp(t.Context(), netx.UserOp{Kind: "runner.run", ID: "1"})
	r := NewRunSession(f.svc)
	vars := template.Vars("Run", map[string]string{"base": srv.URL, "want": "200"})
	var stages []string
	for range 3 {
		out := r.Send(ctx, "req_1", RunOptions{Vars: vars}, func(msg string) { stages = append(stages, msg) })
		if out.Error != nil || out.Response == nil {
			t.Fatalf("Send = %+v", out)
		}
		if out.Response.StatusCode != 200 || out.Response.BodyID != "" || out.Started.IsZero() {
			t.Fatalf("response = %+v", out.Response)
		}
		if tests := out.Response.Tests; len(tests) != 1 || tests[0].Outcome != "passed" || tests[0].Expected != "200" {
			t.Fatalf("tests = %+v", tests)
		}
	}
	if got := srv.n.Load(); got != 3 {
		t.Fatalf("hits = %d", got)
	}
	if len(stages) == 0 || stages[0] != StageConnecting {
		t.Errorf("stages = %v", stages)
	}
	if n := len(hist.list()); n != 0 {
		t.Fatalf("history before Close = %d entries", n)
	}
	r.Close()
	if list := hist.list(); len(list) != 1 || list[0].RequestID != "req_1" {
		t.Fatalf("history = %+v", list)
	}

	// Without the run's variables the folder's value is used: the send
	// fails, and is listed with its error.
	out := NewRunSession(f.svc).Send(ctx, "req_1", RunOptions{AllowUnresolved: true}, nil)
	if out.Response != nil || out.Error == nil || out.Error.Kind != "network" {
		t.Fatalf("Send = %+v", out)
	}
	// A request that cannot be built is a result, not a failure of the run.
	out = NewRunSession(f.svc).Send(ctx, "req_gone", RunOptions{}, nil)
	if out.Error == nil || out.Error.Code != "collection.not_found" {
		t.Fatalf("Send = %+v", out)
	}
}
