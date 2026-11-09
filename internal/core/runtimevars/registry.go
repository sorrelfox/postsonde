package runtimevars

import (
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

// Registry hands out the runtime variables of the open project. A session
// is one workspace.Workspace value: Manager.Open returns a new one every
// time a project is opened, so opening a project, even the same one again,
// starts with its persisted variables only. Safe for concurrent use.
type Registry struct {
	current  func() *workspace.Workspace
	onChange func(*Store)

	mu    sync.Mutex
	ws    *workspace.Workspace
	store *Store
}

// NewRegistry returns a Registry. current returns the open workspace
// (workspace.Manager.Current); a Store asked for another one (a request
// loaded just before the project changed) is not kept, so it cannot end
// the open project's session. nil takes every workspace asked for as the
// open one (tests). onChange, when not nil, is called with the Store
// after every change to a kept Store's variables, from whoever made it:
// the Environments view, or extraction from a response.
func NewRegistry(current func() *workspace.Workspace, onChange func(*Store)) *Registry {
	return &Registry{current: current, onChange: onChange}
}

// For returns the runtime variables of ws: the same Store for as long as
// ws is the open project, loaded with its persisted variables the first
// time. nil is workspace.none_open. A file of persisted variables that
// cannot be read is CodeLocalDataFailed (Clear removes it).
func (r *Registry) For(ws *workspace.Workspace) (*Store, error) {
	if ws == nil {
		return nil, noWorkspace()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ws == ws {
		return r.store, nil
	}
	st, err := Open(ws.LocalDir)
	if err != nil {
		return nil, err
	}
	if r.current == nil || r.current() == ws {
		st.onChange = r.onChange
		r.ws, r.store = ws, st
	}
	return st, nil
}

// Current returns the runtime variables of the open project
// (workspace.none_open without one).
func (r *Registry) Current() (*Store, error) {
	if r.current == nil {
		r.mu.Lock()
		ws := r.ws
		r.mu.Unlock()
		return r.For(ws)
	}
	return r.For(r.current())
}

// Clear removes every runtime variable of ws, session and persisted, also
// when the persisted ones cannot be read.
func (r *Registry) Clear(ws *workspace.Workspace) error {
	if ws == nil {
		return noWorkspace()
	}
	if err := Remove(ws.LocalDir); err != nil {
		return err
	}
	st, err := r.For(ws)
	if err != nil {
		return err
	}
	return st.Clear()
}

// Secrets returns the values of the open project's secret runtime
// variables, session ones included, for masking copied and saved logs.
// None when no project is open or its variables cannot be read.
func (r *Registry) Secrets() []string {
	r.mu.Lock()
	st := r.store
	ws := r.ws
	r.mu.Unlock()
	if st == nil || (r.current != nil && r.current() != ws) {
		return nil
	}
	return st.Secrets()
}

func noWorkspace() error {
	return errs.New(errs.NotFound, "No project is open.").
		WithCode(workspace.CodeNoWorkspace).
		WithHint("Open a project folder or create a project first.")
}
