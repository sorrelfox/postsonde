package streamcheck

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

func TestPrepareAndRun(t *testing.T) {
	scope := template.MapScope{Label: "env", Variables: map[string]template.Variable{
		"n": {Value: "2"}, "token": {Value: "s3cret", Secret: true},
	}}
	checks := Prepare(template.Resolver{Scopes: template.Chain{scope}}, []projectfile.Assertion{
		{Target: "message_count", Op: "equals", Expected: "{{n}}"},
		{Target: "body", Op: "exists", Disabled: true},
		{Messages: "any", Target: "body", Op: "contains", Expected: "{{token}}"},
		{Messages: "last", Target: "body", Op: "equals", Expected: "{{missing}}"},
		{Messages: "nth", Index: 2, Target: "body", Op: "exists", Expected: "{{not rendered}}"},
	}, nil)
	if len(checks.Items) != 4 || checks.Items[1].Index != 2 || checks.Items[3].Assertion.Index != 2 {
		t.Fatalf("items %+v", checks.Items)
	}
	log := NewLog(time.Now())
	log.Add([]byte("hello"), nil, time.Now())
	log.Add([]byte("token=s3cret"), nil, time.Now())
	got := checks.Run(context.Background(), log.Stream(), nil)
	if len(got) != 4 {
		t.Fatalf("results %+v", got)
	}
	if got[0].Outcome != "passed" || got[0].Expected != "2" {
		t.Errorf("count %+v", got[0])
	}
	if r := got[1]; r.Outcome != "passed" || r.Expected != "••••••" || r.Actual != `"token=••••••"` || r.Messages != "any" {
		t.Errorf("secret %+v", r)
	}
	if r := got[2]; r.Outcome != "invalid" || r.Code != template.CodeUnresolved || r.Index != 3 {
		t.Errorf("unresolved %+v", r)
	}
	if r := got[3]; r.Outcome != "passed" || r.MessageIndex != 2 || r.Expected != "{{not rendered}}" {
		t.Errorf("nth %+v", r)
	}
	if empty := (Checks{}).Run(context.Background(), log.Stream(), nil); empty == nil || len(empty) != 0 {
		t.Errorf("empty %#v", empty)
	}
}

func TestLogLimits(t *testing.T) {
	start := time.Now()
	log := NewLog(start)
	for i := range MaxMessages + 5 {
		log.Add([]byte(strconv.Itoa(i)), nil, start.Add(time.Duration(i)*time.Millisecond))
	}
	s := log.Stream()
	if len(s.Messages) != MaxMessages || s.Total != MaxMessages+5 || s.Last == nil || string(s.Last.Body) != strconv.Itoa(MaxMessages+4) {
		t.Fatalf("kept %d of %d, last %v", len(s.Messages), s.Total, s.Last)
	}
	if s.Messages[1].Time != time.Millisecond {
		t.Errorf("time %v", s.Messages[1].Time)
	}
	// A snapshot does not change with later messages.
	log.Add([]byte("more"), nil, time.Now())
	if s.Total != MaxMessages+5 || string(s.Last.Body) == "more" {
		t.Error("snapshot changed")
	}

	big := NewLog(start)
	big.Add(make([]byte, MaxBytes-1), nil, start)
	big.Add([]byte("xx"), nil, start)
	if s := big.Stream(); len(s.Messages) != 1 || s.Total != 2 || s.Last == nil {
		t.Errorf("bytes: kept %d of %d", len(s.Messages), s.Total)
	}
	var none *Log
	none.Add([]byte("x"), nil, start)
	if none.Count() != 0 || none.Stream().Total != 0 {
		t.Error("nil log")
	}
}

func TestRecent(t *testing.T) {
	var r Recent[int]
	for i := range MaxRecent + 2 {
		r.Put(strconv.Itoa(i), i)
	}
	if _, ok := r.Get("0"); ok {
		t.Error("oldest kept")
	}
	if v, ok := r.Get(strconv.Itoa(MaxRecent + 1)); !ok || v != MaxRecent+1 {
		t.Error("newest missing")
	}
}

func TestFailed(t *testing.T) {
	got := Failed(errs.New(errs.NotFound, "The request is no longer in the project.").WithCode("collection.not_found"))
	if len(got) != 1 || got[0].Outcome != "invalid" || got[0].Code != "collection.not_found" ||
		got[0].Message != "The assertions could not be loaded. The request is no longer in the project." {
		t.Errorf("%+v", got)
	}
	if got := Failed(errors.New("boom")); got[0].Message != "The assertions could not be loaded: boom" {
		t.Errorf("%+v", got)
	}
}
