package http

import (
	"context"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// The variable resolution view (DBG-08, ENV-12): for every
// {{reference}} a request uses, the value it is sent with, the scope that
// supplies it and the value of every scope that defines the name. It is
// not a second resolver: Resolution builds the request with build, the
// very function Send builds it with, and lists what that expansion's
// renders substituted (expansion.results). See docs/templates.md.

// Kinds of a resolved reference, for ResolvedVariableDTO.Kind.
const (
	// ResolutionVariable: a variable a scope defines (or none: unresolved).
	ResolutionVariable = "variable"
	// ResolutionPath: a dotted reference read from a variable's JSON
	// value (ENV-13); Base names the variable.
	ResolutionPath = "path"
	// ResolutionDynamic: a dynamic value ({{$uuid}}), generated afresh
	// for every reference of every send.
	ResolutionDynamic = "dynamic"
	// ResolutionPrompt: {{$prompt}}, asked when the request is sent.
	ResolutionPrompt = "prompt"
	// ResolutionFile: {{$file}}, read when the request is sent.
	ResolutionFile = "file"
	// ResolutionFilter: a filter pipe ({{token | base64}}); its input and
	// keys are listed as references of their own.
	ResolutionFilter = "filter"
	// ResolutionResponse: {{$response}}, read from another request's last
	// response (DYN-06); the value shown is the stored one, which a send
	// may replace by sending that request first.
	ResolutionResponse = "response"
)

// SourceResponse is a value read from another request's last response
// ({{$response}}, DYN-06), for VariableDTO.Source and
// ResolvedVariableDTO.Source.
const SourceResponse = "response"

// SourceRun is a folder run's own variables (runner.Vars), for
// ResolvedVariableDTO.Source and ResolutionScopeDTO.Source.
const SourceRun = "run"

// runScopeName is runner.VarsScopeName, the name of a run's variables.
const runScopeName = "Run"

// ResolutionScopeDTO is one scope that defines a name.
type ResolutionScopeDTO struct {
	// Scope is the scope's name: "Global", "Environment: dev", "Run".
	Scope string `json:"scope"`
	// Source is SourceGlobal, SourceEnvironment, SourceCollection,
	// SourceFolder, SourceRuntime or SourceRun.
	Source string `json:"source"`
	// Value is the value as the scope defines it, its own {{references}}
	// not rendered; "" when Secret.
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
	// Local is set when Value is this computer's local value (SEC-02).
	Local bool `json:"local,omitempty"`
	// Wins is set on the scope whose value is sent.
	Wins bool `json:"wins"`
}

// ResolvedVariableDTO is one name a request uses.
type ResolvedVariableDTO struct {
	// Name is the reference as written, without braces: "host",
	// "auth.token", "$randomInt 1 6", "body | sha256".
	Name string `json:"name"`
	// Kind is ResolutionVariable, ResolutionPath, ResolutionDynamic,
	// ResolutionPrompt, ResolutionFile or ResolutionFilter.
	Kind string `json:"kind"`
	// Resolved is false when the send would find no value (an undefined
	// name, a cycle...): Problem says why. A prompt or a file is not
	// resolved here: it is asked for or read when the request is sent.
	Resolved bool `json:"resolved"`
	// Value is the value the request is built with; "" when Secret or
	// Generated.
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
	// Generated is set when the value is made afresh for each send: a
	// dynamic value, a prompt, a file, or a variable or pipe that uses
	// one. Value is then left out: this build's would not be the send's.
	Generated bool `json:"generated"`
	// Description says what a dynamic value generates.
	Description string `json:"description,omitempty"`
	// Scope names the scope that supplies the value ("Environment: dev",
	// "Dynamic"); Source is its kind, as in VariableDTO, or SourceRun.
	// Both are "" when the reference does not resolve.
	Scope  string `json:"scope"`
	Source string `json:"source"`
	// Local is set when the value is this computer's local value.
	Local bool `json:"local,omitempty"`
	// Base is the variable a dotted reference reads from.
	Base string `json:"base,omitempty"`
	// Via are the variables whose values use this name, outermost first,
	// when the request does not use it directly: base = {{scheme}}://…
	// lists scheme via ["base"].
	Via []string `json:"via,omitempty"`
	// Count is how many times the request's fields use it directly.
	Count int `json:"count"`
	// Scopes are the scopes that define the name (for a dotted reference,
	// its base), lowest precedence first; the one that Wins is sent.
	Scopes []ResolutionScopeDTO `json:"scopes"`
	// Problem is the template.ProblemKind that stops it ("unresolved",
	// "cycle", "prompt", "file", ...); "" when resolved.
	Problem string `json:"problem,omitempty"`
	// Reason is Go's explanation of Problem, when it has one (names
	// only, never values).
	Reason string `json:"reason,omitempty"`
}

// ResolutionDTO is what Resolution returns.
type ResolutionDTO struct {
	// Variables are the names the request uses, in the order the build
	// met them, a name used inside another variable's value after it.
	Variables []ResolvedVariableDTO `json:"variables"`
	// Environment is the active environment's name, "" for none.
	Environment string `json:"environment"`
	// URL is the URL the request is sent to, secrets masked, undefined
	// references as written.
	URL string `json:"url"`
}

// resolutionBuild builds as the header preview does: undefined names and
// prompts left as written, no file read.
var resolutionBuild = buildOptions{allowUnresolved: true, preview: true, deferNetrc: true, deferFiles: true, record: true}

// Resolution returns the variable resolution of HTTP, GraphQL or SSE
// request id (DBG-08): every {{reference}} its URL, path parameters,
// headers, auth and body use, with the value it is sent with (secrets
// masked), the scope that wins and every scope that defines it, in the
// precedence global < environment < collection < folder < runtime. The
// request is built as Send builds it, with the same scopes and resolver,
// sending nothing; undefined names, prompts and files are listed, not
// refused. It fails as Send does for other build errors.
func (s *HTTPService) Resolution(ctx context.Context, id string) (ResolutionDTO, error) {
	if err := ctx.Err(); err != nil {
		return ResolutionDTO{}, errs.Wrap(err, errs.KindOf(err), "Resolving the request's variables was interrupted.")
	}
	return s.resolution(ctx, id, resolutionBuild)
}

// Resolution is HTTPService.Resolution for request id in this run: its
// variables resolved as RunSession.Send resolves them, with the run's own
// (o.Vars, the Run scope) above every other scope and o.Prompts answering
// the prompts.
func (r *RunSession) Resolution(ctx context.Context, id string, o RunOptions) (ResolutionDTO, error) {
	if err := ctx.Err(); err != nil {
		return ResolutionDTO{}, errs.Wrap(err, errs.KindOf(err), "Resolving the request's variables was interrupted.")
	}
	b := resolutionBuild
	b.runVars, b.prompts = o.Vars, o.Prompts
	return r.s.resolution(ctx, id, b)
}

// resolution builds request id with o (record set) and lists what the
// build substituted.
func (s *HTTPService) resolution(ctx context.Context, id string, o buildOptions) (ResolutionDTO, error) {
	o.record = true
	_, x, b, err := s.build(ctx, id, o)
	if err != nil {
		return ResolutionDTO{}, err
	}
	chain := x.resolver.Scopes
	out := ResolutionDTO{Variables: []ResolvedVariableDTO{}, URL: httpclient.NewSecretMasker(secretsOf(x, b)).MaskText(b.Request.URL)}
	for _, sc := range chain {
		if name, ok := strings.CutPrefix(sc.Name(), labelEnvironment); ok {
			out.Environment = name
		}
	}
	r := resolver{chain: chain, index: map[string]int{}, out: &out}
	for _, res := range x.results {
		r.problems = res.Problems
		for _, ref := range res.Refs {
			r.add(ref, nil)
		}
	}
	return out, nil
}

type resolver struct {
	chain    template.Chain
	index    map[string]int
	out      *ResolutionDTO
	problems []template.Problem
}

// add lists ref (reached through the variables via) and the references
// nested in it.
func (r *resolver) add(ref template.Ref, via []string) {
	i, seen := r.index[ref.Name]
	if !seen {
		i = len(r.out.Variables)
		r.index[ref.Name] = i
		r.out.Variables = append(r.out.Variables, r.entry(ref, via))
	}
	e := &r.out.Variables[i]
	if len(via) == 0 {
		e.Count++
		e.Via = nil
	}
	if seen {
		return
	}
	next := append(slices.Clone(via), ref.Name)
	for _, n := range ref.Nested {
		r.add(n, next)
	}
}

func (r *resolver) entry(ref template.Ref, via []string) ResolvedVariableDTO {
	kind := r.kindOf(ref)
	e := ResolvedVariableDTO{
		Name:      ref.Name,
		Kind:      kind,
		Resolved:  ref.Resolved,
		Secret:    ref.Secret,
		Generated: r.generated(ref),
		Scope:     ref.Scope,
		Source:    resolutionSource(ref.Scope),
		Base:      ref.Base,
		Via:       slices.Clone(via),
		Scopes:    []ResolutionScopeDTO{},
	}
	if fields := strings.Fields(ref.Name); kind == ResolutionDynamic && len(fields) > 0 {
		e.Description = template.DynamicDescription(fields[0])
	}
	if rr, ok, err := template.ParseResponse(ref.Name); kind == ResolutionResponse && ok && err == nil {
		e.Description = sendDescription(rr)
	}
	if ref.Resolved && !ref.Secret && !e.Generated {
		e.Value = ref.Value
	}
	if kind == ResolutionVariable || kind == ResolutionPath {
		name := ref.Name
		if ref.Base != "" {
			name = ref.Base
		}
		all := r.chain.All(name)
		for j := len(all) - 1; j >= 0; j-- {
			bnd := all[j]
			sc := ResolutionScopeDTO{Scope: bnd.Scope, Source: resolutionSource(bnd.Scope), Secret: bnd.Secret, Local: bnd.Local,
				Wins: j == 0 && ref.Resolved && bnd.Scope == ref.Scope}
			if !bnd.Secret {
				sc.Value = bnd.Value
			}
			e.Scopes = append(e.Scopes, sc)
		}
		if len(all) > 0 && all[0].Scope == ref.Scope {
			e.Local = all[0].Local
		}
	}
	if !ref.Resolved {
		for _, p := range r.problems {
			if p.Name == ref.Name || (len(p.Path) > 0 && p.Path[len(p.Path)-1] == ref.Name) {
				e.Problem, e.Reason = string(p.Kind), p.Message
				break
			}
		}
		if e.Problem == "" {
			e.Problem = string(template.ProblemUnresolved)
		}
	}
	return e
}

// kindOf says what ref is, as the resolver decided: a name a scope
// defines is a variable whatever it looks like.
func (r *resolver) kindOf(ref template.Ref) string {
	if _, ok := r.chain.Lookup(ref.Name); ok {
		return ResolutionVariable
	}
	if _, ok, _ := template.ParsePrompt(ref.Name); ok {
		return ResolutionPrompt
	}
	if _, ok, _ := template.ParseFile(ref.Name); ok {
		return ResolutionFile
	}
	if _, ok, _ := template.ParseResponse(ref.Name); ok {
		return ResolutionResponse
	}
	switch {
	case template.IsDynamic(ref.Name):
		return ResolutionDynamic
	case template.IsPipe(ref.Name):
		return ResolutionFilter
	case ref.Base != "":
		return ResolutionPath
	}
	return ResolutionVariable
}

// generated reports whether ref's value is made afresh at every send.
func (r *resolver) generated(ref template.Ref) bool {
	switch r.kindOf(ref) {
	case ResolutionDynamic, ResolutionPrompt, ResolutionFile:
		return true
	}
	return slices.ContainsFunc(ref.Nested, r.generated)
}

// resolutionSource is sourceOf for the scopes of a build: a run's
// variables and the values no scope supplies too.
func resolutionSource(scope string) string {
	switch scope {
	case "":
		return "" // unresolved
	case runScopeName:
		return SourceRun
	case template.DynamicScope, template.PromptScope, template.FileScope:
		return SourceDynamic
	}
	if template.IsResponseScope(scope) {
		return SourceResponse
	}
	return sourceOf(scope)
}
