package history

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/history"
)

type events struct {
	mu  sync.Mutex
	got []ChangedEventDTO
}

func (e *events) emit(name string, data any) {
	if name != EventChanged {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got = append(e.got, data.(ChangedEventDTO))
}

func (e *events) list() []ChangedEventDTO {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]ChangedEventDTO(nil), e.got...)
}

// workspace is a switchable open project for LocalDir.
type workspace struct {
	mu  sync.Mutex
	dir string
}

func (w *workspace) set(dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dir = dir
}

func (w *workspace) localDir() (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dir == "" {
		return "", errs.New(errs.InvalidArgument, "No project is open.").WithCode("workspace.none_open")
	}
	return w.dir, nil
}

func entry(i int) history.Entry {
	return history.Entry{
		Summary: history.Summary{
			Kind: history.KindHTTP, RequestID: "req_1", Method: "GET",
			URL: fmt.Sprintf("https://example.test/%d", i), Outcome: history.OutcomeOK, StatusCode: 200, Status: "200 OK",
		},
		Request:  history.RequestSnapshot{Method: "GET", URL: fmt.Sprintf("https://example.test/%d", i)},
		Response: &history.ResponseSnapshot{StatusCode: 200, Status: "200 OK", Body: history.Body{Text: "hi", Size: 2}},
	}
}

func TestService(t *testing.T) {
	t.Parallel()
	ws := &workspace{}
	ev := &events{}
	// The projects' folders before the service: cleanups run in reverse,
	// so the service closes its history files before the folders go, and
	// Windows does not remove a file that is open.
	a, b := t.TempDir(), t.TempDir()
	svc := New(nil, ws.localDir, ev.emit, nil)
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	rec := Recorder(svc)

	if _, err := svc.List(t.Context(), ListInputDTO{}); !errors.Is(err, &errs.Error{Code: "workspace.none_open"}) {
		t.Errorf("List without a project = %v", err)
	}
	if rec() != nil {
		t.Error("a send is recorded without a project")
	}

	ws.set(a)
	for i := range 3 {
		rec()(entry(i))
	}
	// A send started in project a that ends after the switch to b goes
	// into a's history, without an event for b's list.
	inA := rec()
	ws.set(b)
	inA(entry(3))
	rec()(entry(10))

	p, err := svc.List(t.Context(), ListInputDTO{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 1 || p.Entries[0].URL != "https://example.test/10" {
		t.Errorf("b's history = %+v", p)
	}
	ws.set(a)
	p, err = svc.List(t.Context(), ListInputDTO{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 4 || p.All != 4 || len(p.Entries) != 2 || p.Entries[0].URL != "https://example.test/2" {
		t.Errorf("a's page = %+v", p)
	}
	if _, err := time.Parse(time.RFC3339Nano, p.Entries[0].Time); err != nil {
		t.Errorf("time = %q", p.Entries[0].Time)
	}
	got, err := svc.Get(t.Context(), p.Entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Response == nil || got.Response.Body.Text != "hi" || got.Request.Headers == nil {
		t.Errorf("Get = %+v", got)
	}
	if _, err := svc.Get(t.Context(), "h_nope"); !errors.Is(err, &errs.Error{Code: history.CodeNotFound}) {
		t.Errorf("Get(unknown) = %v", err)
	}
	if err := svc.Delete(t.Context(), got.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := svc.List(t.Context(), ListInputDTO{}); p.Total != 3 {
		t.Errorf("after Delete: %d entries", p.Total)
	}
	if err := svc.Clear(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p, _ := svc.List(t.Context(), ListInputDTO{}); p.Total != 0 {
		t.Errorf("after Clear: %d entries", p.Total)
	}

	var actions []string
	for _, e := range ev.list() {
		actions = append(actions, e.Action)
		if e.Action == ActionAdded && (e.Entry == nil || e.Entry.ID != e.ID) {
			t.Errorf("added event = %+v", e)
		}
	}
	want := []string{ActionAdded, ActionAdded, ActionAdded, ActionAdded, ActionDeleted, ActionCleared}
	if fmt.Sprint(actions) != fmt.Sprint(want) {
		t.Errorf("events = %v, want %v", actions, want)
	}
}

func TestConcurrentRecording(t *testing.T) {
	t.Parallel()
	ws := &workspace{dir: t.TempDir()}
	svc := New(history.NewRegistry(history.Limits{MaxEntries: 30}), ws.localDir, nil, nil)
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	rec := Recorder(svc)
	var wg sync.WaitGroup
	for w := range 6 {
		wg.Go(func() {
			for i := range 20 {
				rec()(entry(w*100 + i))
				if _, err := svc.List(t.Context(), ListInputDTO{Text: "example"}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if p, _ := svc.List(t.Context(), ListInputDTO{}); p.All != 30 {
		t.Errorf("%d entries kept, want 30", p.All)
	}
}
