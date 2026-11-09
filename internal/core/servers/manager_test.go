package servers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx/listen"
)

// fakeType accepts connections and closes them at once. Tests reach its
// instances and sinks to emit events or make serving fail.
type fakeType struct {
	startErr error

	mu        sync.Mutex
	instances []*fakeInstance
}

func (*fakeType) Name() string  { return "fake" }
func (*fakeType) Label() string { return "Fake" }

func (t *fakeType) Start(_ Spec, ln net.Listener, sink Sink) (Instance, error) {
	if t.startErr != nil {
		ln.Close()
		return nil, t.startErr
	}
	i := &fakeInstance{ln: ln, sink: sink, done: make(chan struct{})}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				i.finish(err)
				return
			}
			c.Close()
		}
	}()
	t.mu.Lock()
	t.instances = append(t.instances, i)
	t.mu.Unlock()
	return i, nil
}

func (t *fakeType) last() *fakeInstance {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.instances[len(t.instances)-1]
}

type fakeInstance struct {
	ln   net.Listener
	sink Sink
	done chan struct{}

	mu       sync.Mutex
	stopping bool
	err      error
	once     sync.Once
}

func (i *fakeInstance) finish(err error) {
	i.once.Do(func() {
		i.mu.Lock()
		if !i.stopping {
			i.err = err
		}
		i.mu.Unlock()
		close(i.done)
	})
}

func (i *fakeInstance) Done() <-chan struct{} { return i.done }

func (i *fakeInstance) Err() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.err
}

func (i *fakeInstance) Stop(context.Context) error {
	i.mu.Lock()
	i.stopping = true
	i.mu.Unlock()
	i.ln.Close()
	<-i.done
	return nil
}

// recorder collects OnChange and OnTraffic calls.
type recorder struct {
	mu      sync.Mutex
	changes []Server
	removed []string
	events  []Event
}

func (r *recorder) onChange(s Server, removed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if removed {
		r.removed = append(r.removed, s.ID)
		return
	}
	r.changes = append(r.changes, s)
}

func (r *recorder) onTraffic(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) states(id string) []State {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []State
	for _, s := range r.changes {
		if s.ID == id {
			out = append(out, s.State)
		}
	}
	return out
}

func (r *recorder) kinds(id string) []EventKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []EventKind
	for _, e := range r.events {
		if e.ServerID == id {
			out = append(out, e.Kind)
		}
	}
	return out
}

func newTestManager(t *testing.T, opts Options) (*Manager, *fakeType, *recorder) {
	t.Helper()
	typ := &fakeType{}
	rec := &recorder{}
	opts.Types = append(opts.Types, typ)
	opts.OnChange = rec.onChange
	opts.OnTraffic = rec.onTraffic
	opts.Logger = slog.New(slog.DiscardHandler)
	m := NewManager(opts)
	t.Cleanup(func() { m.StopAll(context.Background()) })
	return m, typ, rec
}

func mustAdd(t *testing.T, m *Manager, spec Spec) Server {
	t.Helper()
	if spec.Type == "" {
		spec.Type = "fake"
	}
	s, err := m.Add(spec)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return s
}

func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

func equalStates(a, b []State) bool {
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func TestStartStopEphemeralPort(t *testing.T) {
	t.Parallel()
	m, _, rec := newTestManager(t, Options{})
	s := mustAdd(t, m, Spec{})
	if s.State != StateStopped || s.Spec.Name != "Fake" || s.TypeLabel != "Fake" || len(s.ID) != len(IDPrefix)+16 {
		t.Fatalf("added = %+v", s)
	}

	s, err := m.Start(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != StateRunning || s.Port() == 0 || s.Scope != listen.ScopeLoopback || s.Warning != nil || s.StartedAt.IsZero() {
		t.Fatalf("started = %+v", s)
	}
	for _, a := range s.Addrs {
		if !a.Addr().IsLoopback() || int(a.Port()) != s.Port() {
			t.Errorf("bound %v, want loopback on port %d", a, s.Port())
		}
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Port()))
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial running server: %v", err)
	}
	c.Close()

	// Starting again does nothing.
	again, err := m.Start(t.Context(), s.ID)
	if err != nil || again.State != StateRunning || again.Port() != s.Port() {
		t.Errorf("second Start = %+v, %v", again, err)
	}

	s, err = m.Stop(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != StateStopped || s.Port() != 0 || s.StoppedAt.IsZero() {
		t.Errorf("stopped = %+v", s)
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Error("port still accepts after Stop")
	}
	// Stopping a stopped server does nothing.
	if s2, err := m.Stop(t.Context(), s.ID); err != nil || s2.State != StateStopped {
		t.Errorf("second Stop = %+v, %v", s2, err)
	}

	want := []State{StateStopped, StateStarting, StateRunning, StateStopping, StateStopped}
	if got := rec.states(s.ID); !equalStates(got, want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
	if got := rec.kinds(s.ID); fmt.Sprint(got) != fmt.Sprint([]EventKind{KindStarted, KindStopped}) {
		t.Errorf("events = %v", got)
	}
	if got, _ := m.Get(s.ID); got.Events != 2 {
		t.Errorf("Events = %d, want 2", got.Events)
	}
}

func TestStartFailures(t *testing.T) {
	t.Parallel()
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { taken.Close() })
	takenPort := taken.Addr().(*net.TCPAddr).Port

	tests := []struct {
		name     string
		spec     Spec
		startErr error
		kind     errs.Kind
		code     string
	}{
		{"port in use", Spec{Host: "127.0.0.1", Port: takenPort}, nil, errs.Network, listen.CodeAddressInUse},
		{"LAN without opt-in", Spec{Host: "0.0.0.0"}, nil, errs.PermissionDenied, listen.CodeLANNotAllowed},
		{"type fails", Spec{}, errors.New("boom"), errs.Internal, CodeStartFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, typ, rec := newTestManager(t, Options{})
			typ.startErr = tt.startErr
			s := mustAdd(t, m, tt.spec)
			got, err := m.Start(t.Context(), s.ID)
			if errs.KindOf(err) != tt.kind || codeOf(err) != tt.code {
				t.Fatalf("Start err = %v (%s, %q), want %s %s", err, errs.KindOf(err), codeOf(err), tt.kind, tt.code)
			}
			if got.State != StateFailed || codeOf(got.Err) != tt.code || got.Port() != 0 {
				t.Errorf("server = %+v", got)
			}
			want := []State{StateStopped, StateStarting, StateFailed}
			if states := rec.states(s.ID); !equalStates(states, want) {
				t.Errorf("changes = %v, want %v", states, want)
			}
			events, _ := m.Traffic(s.ID)
			last := events[len(events)-1]
			if last.Kind != KindError || last.Direction != System || last.Summary == "" {
				t.Errorf("last event = %+v, want an error", last)
			}
		})
	}
}

func TestFailedServerRestarts(t *testing.T) {
	t.Parallel()
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := taken.Addr().(*net.TCPAddr).Port
	m, _, _ := newTestManager(t, Options{})
	s := mustAdd(t, m, Spec{Host: "127.0.0.1", Port: port})
	if _, err := m.Start(t.Context(), s.ID); codeOf(err) != listen.CodeAddressInUse {
		t.Fatalf("Start = %v", err)
	}
	taken.Close()
	got, err := m.Start(t.Context(), s.ID)
	if err != nil || got.State != StateRunning || got.Port() != port || got.Err != nil {
		t.Fatalf("restart = %+v, %v", got, err)
	}
}

func TestAllowLANWarning(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestManager(t, Options{})
	s := mustAdd(t, m, Spec{Host: "0.0.0.0", AllowLAN: true})
	got, err := m.Start(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scope != listen.ScopeAllInterfaces || got.Warning == nil || got.Warning.Code != listen.WarnAllInterfacesExposed {
		t.Fatalf("server = %+v", got)
	}
	events, _ := m.Traffic(s.ID)
	if len(events) != 1 || events[0].Kind != KindStarted || events[0].Summary == "" {
		t.Errorf("events = %+v", events)
	}
}

func TestAddValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		spec Spec
		code string
	}{
		{"unknown type", Spec{Type: "nope"}, CodeUnknownType},
		{"negative port", Spec{Type: "fake", Port: -1}, listen.CodeInvalidPort},
		{"port too large", Spec{Type: "fake", Port: 70000}, listen.CodeInvalidPort},
		{"host name", Spec{Type: "fake", Host: "example.com"}, listen.CodeInvalidHost},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, _, _ := newTestManager(t, Options{})
			_, err := m.Add(tt.spec)
			if errs.KindOf(err) != errs.InvalidArgument || codeOf(err) != tt.code {
				t.Errorf("Add err = %v, want invalid_argument %s", err, tt.code)
			}
			if len(m.List()) != 0 {
				t.Error("an invalid spec was added")
			}
		})
	}
}

func TestUnknownID(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestManager(t, Options{})
	calls := map[string]func() error{
		"Get":          func() error { _, err := m.Get("srv_x"); return err },
		"Start":        func() error { _, err := m.Start(t.Context(), "srv_x"); return err },
		"Stop":         func() error { _, err := m.Stop(t.Context(), "srv_x"); return err },
		"Remove":       func() error { return m.Remove(t.Context(), "srv_x") },
		"Traffic":      func() error { _, err := m.Traffic("srv_x"); return err },
		"ClearTraffic": func() error { return m.ClearTraffic("srv_x") },
	}
	for name, call := range calls {
		if err := call(); errs.KindOf(err) != errs.NotFound || codeOf(err) != CodeNotFound {
			t.Errorf("%s err = %v, want not_found", name, err)
		}
	}
}

func TestServeFailure(t *testing.T) {
	t.Parallel()
	m, typ, rec := newTestManager(t, Options{})
	s := mustAdd(t, m, Spec{})
	if _, err := m.Start(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	// The listener breaks without Stop.
	typ.last().ln.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := m.Get(s.ID)
		if got.State == StateFailed {
			if codeOf(got.Err) != CodeServeFailed || got.Port() != 0 {
				t.Errorf("failed server = %+v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %s, want failed", got.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
	want := []State{StateStopped, StateStarting, StateRunning, StateFailed}
	if got := rec.states(s.ID); !equalStates(got, want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
	if got := rec.kinds(s.ID); fmt.Sprint(got) != fmt.Sprint([]EventKind{KindStarted, KindError}) {
		t.Errorf("events = %v", got)
	}
	// It can start again.
	if got, err := m.Start(t.Context(), s.ID); err != nil || got.State != StateRunning {
		t.Errorf("restart = %+v, %v", got, err)
	}
}

func TestStopAll(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestManager(t, Options{})
	var ids []string
	for range 3 {
		s := mustAdd(t, m, Spec{})
		if _, err := m.Start(t.Context(), s.ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID)
	}
	idle := mustAdd(t, m, Spec{})

	m.StopAll(t.Context())
	for _, s := range m.List() {
		if s.State != StateStopped {
			t.Errorf("%s is %s after StopAll", s.ID, s.State)
		}
	}
	if _, err := m.Start(t.Context(), ids[0]); codeOf(err) != CodeShuttingDown {
		t.Errorf("Start after StopAll = %v, want %s", err, CodeShuttingDown)
	}
	if _, err := m.Start(t.Context(), idle.ID); codeOf(err) != CodeShuttingDown {
		t.Errorf("Start after StopAll = %v, want %s", err, CodeShuttingDown)
	}
	if _, err := m.Add(Spec{Type: "fake"}); codeOf(err) != CodeShuttingDown {
		t.Errorf("Add after StopAll = %v, want %s", err, CodeShuttingDown)
	}
}

func TestStopWithCancelledContext(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestManager(t, Options{})
	s := mustAdd(t, m, Spec{})
	if _, err := m.Start(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := m.Stop(ctx, s.ID); err != nil || got.State != StateStopped {
		t.Errorf("Stop = %+v, %v", got, err)
	}
}

func TestRemove(t *testing.T) {
	t.Parallel()
	m, _, rec := newTestManager(t, Options{})
	s := mustAdd(t, m, Spec{})
	other := mustAdd(t, m, Spec{Name: "  Other "})
	if other.Spec.Name != "Other" {
		t.Errorf("name = %q, want trimmed", other.Spec.Name)
	}
	if _, err := m.Start(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	if list := m.List(); len(list) != 1 || list[0].ID != other.ID {
		t.Errorf("List = %+v", list)
	}
	if rec.removed[0] != s.ID {
		t.Errorf("removed = %v", rec.removed)
	}
	if got := rec.states(s.ID); got[len(got)-1] != StateStopped {
		t.Errorf("changes = %v, want it stopped before removal", got)
	}
}

func TestEventsCappedAndKept(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	m, typ, rec := newTestManager(t, Options{
		ContentLimit: 8,
		RecentEvents: 4,
		RecentBytes:  1 << 20,
		Now:          func() time.Time { return now },
	})
	s := mustAdd(t, m, Spec{})
	if _, err := m.Start(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	sink := typ.last().sink
	if sink.ContentLimit() != 8 {
		t.Errorf("ContentLimit = %d", sink.ContentLimit())
	}
	at := now.Add(-time.Minute)
	sink.Emit(Event{Direction: In, Kind: KindMessage, Content: []byte("short"), Time: at, Peer: "127.0.0.1:1"})
	sink.Emit(Event{Direction: In, Kind: KindMessage, Content: []byte("0123456789abcdef")})
	sink.Emit(Event{Direction: In, Kind: KindMessage, Content: []byte{0xff, 0x00, 0x01}})
	sink.Emit(Event{Direction: In, Kind: KindMessage, Content: []byte("abcdefgé")}) // cut inside é
	sink.Emit(Event{Direction: Out, Kind: KindMessage, Content: []byte("abc"), Size: 100})

	events, err := m.Traffic(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("kept %d events, want 4", len(events))
	}
	type want struct {
		seq       uint64
		content   string
		size      int
		truncated bool
		binary    bool
	}
	wants := []want{
		{3, "01234567", 16, true, false},
		{4, "\xff\x00\x01", 3, false, true},
		{5, "abcdefg\xc3", 9, true, false},
		{6, "abc", 100, true, false},
	}
	for i, w := range wants {
		e := events[i]
		if e.ServerID != s.ID || e.Seq != w.seq || string(e.Content) != w.content || e.Size != w.size || e.Truncated != w.truncated || e.Binary != w.binary {
			t.Errorf("event %d = %+v, want %+v", i, e, w)
		}
		if !e.Time.Equal(now) {
			t.Errorf("event %d time = %v, want the manager clock", i, e.Time)
		}
	}
	if first := rec.events[1]; !first.Time.Equal(at) || first.Peer != "127.0.0.1:1" || string(first.Content) != "short" {
		t.Errorf("first emitted = %+v, want its own time kept", first)
	}

	if err := m.ClearTraffic(s.ID); err != nil {
		t.Fatal(err)
	}
	if events, _ := m.Traffic(s.ID); len(events) != 0 {
		t.Errorf("after clear: %d events", len(events))
	}

	if _, err := m.Stop(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	// A late emit from the old run is dropped.
	sink.Emit(Event{Direction: In, Kind: KindMessage, Content: []byte("late")})
	events, _ = m.Traffic(s.ID)
	if len(events) != 1 || events[0].Kind != KindStopped || events[0].Seq != 7 {
		t.Errorf("after stop: %+v, want only the stopped event with seq 7", events)
	}
}

func TestRecentBytesBudget(t *testing.T) {
	t.Parallel()
	b := recentBuffer{maxEvents: 100, maxBytes: 10}
	for i := range 5 {
		b.add(Event{Seq: uint64(i + 1), Content: []byte("abcd")})
	}
	got := b.list()
	if len(got) != 2 || got[0].Seq != 4 || b.bytes != 8 {
		t.Errorf("kept %d events from seq %d (%d bytes), want 2 from 4", len(got), got[0].Seq, b.bytes)
	}
	// One event larger than the budget is still kept.
	b.add(Event{Seq: 6, Content: make([]byte, 50)})
	if got := b.list(); len(got) != 1 || got[0].Seq != 6 {
		t.Errorf("kept %+v", got)
	}
}

func TestConcurrentStartStop(t *testing.T) {
	t.Parallel()
	m, typ, _ := newTestManager(t, Options{})
	s := mustAdd(t, m, Spec{})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for range 10 {
				if i%2 == 0 {
					_, _ = m.Start(t.Context(), s.ID)
				} else {
					_, _ = m.Stop(t.Context(), s.ID)
				}
				_ = m.List()
				_, _ = m.Traffic(s.ID)
			}
		})
	}
	wg.Go(func() {
		for range 50 {
			typ.mu.Lock()
			n := len(typ.instances)
			typ.mu.Unlock()
			if n > 0 {
				typ.last().sink.Emit(Event{Direction: In, Kind: KindMessage})
			}
		}
	})
	wg.Wait()
	m.StopAll(t.Context())
	if got, _ := m.Get(s.ID); got.State != StateStopped {
		t.Errorf("state = %s", got.State)
	}
	events, _ := m.Traffic(s.ID)
	for i := 1; i < len(events); i++ {
		if events[i].Seq != events[i-1].Seq+1 {
			t.Fatalf("seq %d after %d", events[i].Seq, events[i-1].Seq)
		}
	}
}
