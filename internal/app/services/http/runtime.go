package http

import (
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/runtimevars"
	"github.com/sorrelfox/postsonde/internal/core/template"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// SourceRuntime is a runtime variable's (ENV-15), for VariableDTO.Source.
const SourceRuntime = "runtime"

// WithRuntimeVariables renders every request with the open project's
// runtime variables (ENV-15, docs/environments.md#runtime-variables): the
// "Runtime" scope, above the folders and below a folder run's own
// variables. Without it there is no runtime scope.
func WithRuntimeVariables(r *runtimevars.Registry) Option {
	return func(s *HTTPService) { s.runtime = r }
}

// RuntimeScope returns chain with the runtime variables of ws last, as
// every sender renders them: global < environment < collection < folder <
// runtime. A nil registry or workspace adds nothing. The WebSocket and
// Socket.IO services' scopes add them with it (app.services).
func RuntimeScope(r *runtimevars.Registry, ws *coreworkspace.Workspace, chain template.Chain) (template.Chain, error) {
	if r == nil || ws == nil {
		return chain, nil
	}
	st, err := r.For(ws)
	if err != nil {
		return nil, err
	}
	return append(chain, st.Scope()), nil
}

// chainOf is scopeChain(src) with the runtime variables of the request's
// project last. A folder run adds its own variables after them (build).
func (s *HTTPService) chainOf(src collection.HTTPSource) (template.Chain, error) {
	chain, err := scopeChain(src)
	if err != nil {
		return nil, err
	}
	return RuntimeScope(s.runtime, src.Workspace, chain)
}

// runtimeVariables returns the runtime variables of the project ws (the
// src.Workspace of a send). Extraction into variables sets a value
// with its Set after a response, from a single send (Send, Repeat) as
// from a request of a run (RunSession.Send): the next render of any
// request of the project sees it, and runtimevars.Registry's change hook
// tells the UI. A run's own variables (RunOptions.Vars) still win over it
// within the run. Nil without WithRuntimeVariables.
func (s *HTTPService) runtimeVariables(ws *coreworkspace.Workspace) (*runtimevars.Store, error) {
	if s.runtime == nil {
		return nil, nil
	}
	return s.runtime.For(ws)
}
