package servers

import (
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx/listen"
)

// configType is a fake type with a string configuration whose instances
// record Reconfigure calls.
type configType struct {
	fakeType
}

func (*configType) Name() string { return "configurable" }

func (*configType) NormalizeConfig(config any) (any, error) {
	switch c := config.(type) {
	case nil:
		return "default", nil
	case string:
		if c == "bad" {
			return nil, errs.New(errs.InvalidArgument, "bad config")
		}
		return c, nil
	}
	return nil, errs.New(errs.InvalidArgument, "not a string")
}

func (t *configType) Start(spec Spec, ln net.Listener, sink Sink) (Instance, error) {
	inst, err := t.fakeType.Start(spec, ln, sink)
	if err != nil {
		return nil, err
	}
	return &configInstance{fakeInstance: inst.(*fakeInstance), configs: []any{spec.Config}}, nil
}

type configInstance struct {
	*fakeInstance
	cmu     sync.Mutex
	configs []any
}

func (i *configInstance) Reconfigure(config any) {
	i.cmu.Lock()
	defer i.cmu.Unlock()
	i.configs = append(i.configs, config)
}

func TestUpdateConfig(t *testing.T) {
	t.Parallel()
	ct := &configType{}
	m, _, rec := newTestManager(t, Options{Types: []Type{ct}})

	// A type without configuration drops it.
	plain := mustAdd(t, m, Spec{Config: "ignored"})
	if plain.Spec.Config != nil {
		t.Errorf("fake config = %v", plain.Spec.Config)
	}
	if s, err := m.UpdateConfig(plain.ID, "x"); err != nil || s.Spec.Config != nil {
		t.Errorf("UpdateConfig on fake = %+v, %v", s, err)
	}

	if _, err := m.Add(Spec{Type: "configurable", Config: "bad"}); err == nil {
		t.Error("Add accepted a bad config")
	}
	s := mustAdd(t, m, Spec{Type: "configurable"})
	if s.Spec.Config != "default" {
		t.Errorf("default config = %v", s.Spec.Config)
	}
	if s, err := m.UpdateConfig(s.ID, "stopped"); err != nil || s.Spec.Config != "stopped" {
		t.Errorf("UpdateConfig stopped = %+v, %v", s, err)
	}
	if _, err := m.Start(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	changes := len(rec.states(s.ID))
	if got, err := m.UpdateConfig(s.ID, "running"); err != nil || got.Spec.Config != "running" || got.State != StateRunning {
		t.Errorf("UpdateConfig running = %+v, %v", got, err)
	}
	if _, err := m.UpdateConfig(s.ID, "bad"); err == nil {
		t.Error("UpdateConfig accepted a bad config")
	}
	if got, _ := m.Get(s.ID); got.Spec.Config != "running" {
		t.Errorf("config after a rejected update = %v", got.Spec.Config)
	}
	if len(rec.states(s.ID)) != changes {
		t.Error("UpdateConfig reported a change")
	}
	m.mu.Lock()
	inst := m.servers[s.ID].inst.(*configInstance)
	m.mu.Unlock()
	inst.cmu.Lock()
	if fmt.Sprint(inst.configs) != "[stopped running]" {
		t.Errorf("instance configs = %v", inst.configs)
	}
	inst.cmu.Unlock()

	if _, err := m.UpdateConfig("srv_missing", "x"); codeOf(err) != CodeNotFound {
		t.Errorf("UpdateConfig on a missing server = %v", err)
	}
}

// TestRedefine: a server keeps its ID, traffic and state when its
// definition changes; its address changes only while it holds no socket,
// its type never, and the name, owner and configuration at any time.
func TestRedefine(t *testing.T) {
	t.Parallel()
	ct := &configType{}
	m, _, rec := newTestManager(t, Options{Types: []Type{ct}})
	owner := Owner{Workspace: "ws_1", Folder: "fld_1", Label: "Mocks"}

	checked, err := m.Check(Spec{Type: "configurable", Name: "  ", Owner: owner})
	if err != nil || checked.Name != "Fake" || checked.Config != "default" || checked.Owner != owner {
		t.Errorf("Check = %+v, %v", checked, err)
	}
	if _, err := m.Check(Spec{Type: "configurable", Port: 70000}); codeOf(err) != listen.CodeInvalidPort {
		t.Errorf("Check of a bad port = %v", err)
	}

	s := mustAdd(t, m, checked)
	if _, err := m.Start(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Redefine(s.ID, Spec{Type: "configurable", Port: 1234, Owner: owner}); codeOf(err) != CodeActive {
		t.Errorf("Redefine of the port while running = %v, want %s", err, CodeActive)
	}
	if _, err := m.Redefine(s.ID, Spec{Type: "fake", Owner: owner}); codeOf(err) != CodeTypeChanged {
		t.Errorf("Redefine of the type = %v, want %s", err, CodeTypeChanged)
	}
	changes := len(rec.states(s.ID))
	got, err := m.Redefine(s.ID, Spec{Type: "configurable", Name: "Renamed", Config: "live", Owner: Owner{Workspace: "ws_1", Folder: "fld_1", Label: "New"}})
	if err != nil || got.ID != s.ID || got.State != StateRunning || got.Spec.Name != "Renamed" || got.Spec.Config != "live" || got.Spec.Owner.Label != "New" {
		t.Errorf("Redefine while running = %+v, %v", got, err)
	}
	if len(rec.states(s.ID)) != changes+1 {
		t.Error("Redefine did not report the change")
	}
	m.mu.Lock()
	inst := m.servers[s.ID].inst.(*configInstance)
	m.mu.Unlock()
	inst.cmu.Lock()
	if fmt.Sprint(inst.configs) != "[default live]" {
		t.Errorf("instance configs = %v", inst.configs)
	}
	inst.cmu.Unlock()

	if _, err := m.Stop(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	got, err = m.Redefine(s.ID, Spec{Type: "configurable", Host: "127.0.0.1", Port: 0, AllowLAN: true, Config: "later"})
	if err != nil || got.Spec.Host != "127.0.0.1" || !got.Spec.AllowLAN || got.State != StateStopped || got.Events == 0 {
		t.Errorf("Redefine while stopped = %+v, %v", got, err)
	}
	if _, err := m.Redefine(s.ID, Spec{Type: "configurable", Host: "not a host"}); err == nil {
		t.Error("Redefine accepted a bad host")
	}
	if _, err := m.Redefine("srv_missing", Spec{Type: "configurable"}); codeOf(err) != CodeNotFound {
		t.Errorf("Redefine of a missing server = %v", err)
	}
}
