package http

import (
	"context"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/localvalues"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/runtimevars"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Sources of a variable, for VariableDTO.Source.
const (
	SourceGlobal      = "global"
	SourceEnvironment = "environment"
	// SourceCollection is a top-level folder's variables (ENV-03).
	SourceCollection = "collection"
	SourceFolder     = "folder"
	// SourceDynamic is a dynamic value ({{$uuid}}, {{$timestamp}}...),
	// generated at send time by the template resolver.
	SourceDynamic = "dynamic"
)

// Scope labels of the chain, as hover shows them. The environment's label
// is template's "Environment: <name>" (environments.Environment.Scope); a
// top-level folder's is "Collection: <name>", a deeper folder's
// "Folder: <name>" ("Folder" when the source does not name it).
const (
	labelGlobal      = "Global"
	labelFolder      = "Folder"
	labelEnvironment = "Environment: "
	labelCollection  = "Collection: "
)

// VariableDTO is a variable a request can use, as the editor shows it: the
// {{name}} chip and its hover (ENV-11, ENV-12).
type VariableDTO struct {
	Name string `json:"name"`
	// Value is the rendered value (variables inside it expanded); "" when
	// Secret, which the editor shows masked. For a dynamic value it is a
	// description ("A random UUID (v4), generated at send time").
	Value string `json:"value"`
	// Secret is set when the value, or a value nested in it, is secret.
	Secret bool `json:"secret"`
	// Resolved is false when the value refers to a variable that is not
	// defined, or loops back on itself: a reference to it cannot be sent
	// as it is either.
	Resolved bool `json:"resolved"`
	// Scope names the scope that supplies the value: "Global",
	// "Environment: dev", "Collection: Users", "Folder: Admins",
	// "Runtime" or "Dynamic".
	Scope string `json:"scope"`
	// Source is SourceGlobal, SourceEnvironment, SourceCollection,
	// SourceFolder, SourceRuntime or SourceDynamic.
	Source string `json:"source"`
	// Overrides are the scopes of lower precedence that define the name
	// too, nearest first ("Environment: dev", "Global"): the value shown
	// wins over theirs (ENV-06). Left out when only one scope defines it.
	Overrides []string `json:"overrides,omitempty"`
	// Local is set when the value is this computer's local value of the
	// variable (SEC-02), which replaces the shared value of the project
	// file in its scope. Left out otherwise.
	Local bool `json:"local,omitempty"`
}

// VariablesDTO is what Variables returns.
type VariablesDTO struct {
	// Variables are every name the request's scopes define, and every
	// dynamic value name no scope defines (Source "dynamic", always
	// resolved; with arguments such as {{$randomInt 1 6}} a reference
	// resolves through its first word), sorted by name.
	Variables []VariableDTO `json:"variables"`
	// Environment is the active environment's name, "" for none.
	Environment string `json:"environment"`
}

// VariablesLoader returns the variable scopes of request id, of any kind
// (collection.LoadVariablesSource).
type VariablesLoader func(ctx context.Context, id string) (collection.VariablesSource, error)

// WithVariablesLoader makes Variables load the scopes with l, so it
// answers for requests of every kind (a websocket request's chips, WS-11),
// not only those Send can send.
func WithVariablesLoader(l VariablesLoader) Option {
	return func(s *HTTPService) { s.loadVariables = l }
}

// Variables returns the variables request id can use, resolved the way
// Send resolves them (global < active environment < folders < runtime),
// so the editor marks each {{name}} as resolved or not and shows its value
// and where it comes from (ENV-11). Secret values are left out. A name the
// result does not list is not defined, except a dynamic value with
// arguments, listed under its name alone. With WithVariablesLoader it
// works for a request of any kind (the WebSocket service resolves with the
// same scopes, ScopeChain and RuntimeScope); without, only for HTTP and
// GraphQL requests.
func (s *HTTPService) Variables(ctx context.Context, id string) (VariablesDTO, error) {
	if err := ctx.Err(); err != nil {
		return VariablesDTO{}, errs.Wrap(err, errs.KindOf(err), "Loading the variables was interrupted.")
	}
	var chain template.Chain
	switch {
	case s.loadVariables != nil:
		src, err := s.loadVariables(ctx, id)
		if err != nil {
			return VariablesDTO{}, err
		}
		if chain, err = ScopeChain(src); err != nil {
			return VariablesDTO{}, err
		}
		if chain, err = RuntimeScope(s.runtime, src.Workspace, chain); err != nil {
			return VariablesDTO{}, err
		}
	case s.load != nil:
		src, err := s.load(ctx, id)
		if err != nil {
			return VariablesDTO{}, err
		}
		if chain, err = s.chainOf(src); err != nil {
			return VariablesDTO{}, err
		}
	default:
		return VariablesDTO{}, errs.New(errs.Unsupported, "Sending HTTP requests is not available.").WithCode(CodeUnavailable)
	}
	out := VariablesDTO{Variables: []VariableDTO{}}
	for _, sc := range chain {
		if name, ok := strings.CutPrefix(sc.Name(), labelEnvironment); ok {
			out.Environment = name
		}
	}
	r := template.Resolver{Scopes: chain}
	names := chain.Names()
	for _, name := range names {
		ref := "{{" + name + "}}"
		segs := template.Parse(ref)
		if len(segs) != 1 || segs[0].Kind != template.Var || segs[0].Name != name {
			continue // a name no {{reference}} can spell
		}
		res := r.Render(ref)
		if len(res.Refs) != 1 {
			continue
		}
		v := VariableDTO{
			Name:     name,
			Secret:   res.Refs[0].Secret,
			Resolved: res.Err() == nil,
			Scope:    res.Refs[0].Scope,
			Source:   sourceOf(res.Refs[0].Scope),
		}
		if !v.Secret {
			v.Value = res.Refs[0].Value
		}
		all := chain.All(name)
		if len(all) > 0 && all[0].Scope == v.Scope {
			v.Local = all[0].Local
		}
		if len(all) > 1 {
			for _, b := range all[1:] {
				v.Overrides = append(v.Overrides, b.Scope)
			}
		}
		out.Variables = append(out.Variables, v)
	}
	// Dynamic values resolve without a scope; a scope that defines the
	// same name wins, as at send time.
	for _, name := range template.DynamicNames() {
		if _, defined := slices.BinarySearch(names, name); defined {
			continue
		}
		out.Variables = append(out.Variables, VariableDTO{
			Name:     name,
			Value:    template.DynamicDescription(name),
			Resolved: true,
			Scope:    template.DynamicScope,
			Source:   SourceDynamic,
		})
	}
	slices.SortFunc(out.Variables, func(a, b VariableDTO) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func sourceOf(label string) string {
	switch {
	case label == labelGlobal:
		return SourceGlobal
	case label == runtimevars.ScopeName:
		return SourceRuntime
	case strings.HasPrefix(label, labelEnvironment):
		return SourceEnvironment
	case strings.HasPrefix(label, labelCollection):
		return SourceCollection
	}
	return SourceFolder
}

// scopeChain is the scopes a request's {{variables}} are rendered with
// (ENV-06): the project's global variables, the active environment, then
// the folders from the top down. The active environment is read on every
// call, so switching it changes the next send.
func scopeChain(src collection.HTTPSource) (template.Chain, error) {
	return ScopeChain(collection.VariablesSource{
		Globals:         src.Globals,
		FolderVariables: src.FolderVariables,
		Folders:         src.Folders,
		Workspace:       src.Workspace,
	})
}

// ScopeChain is the scopes the {{variables}} of a request of any kind are
// rendered with, as for an HTTP send, in the fixed precedence of ENV-06,
// lowest first: global (postsonde.json), the active environment (read on
// every call), the collection (the top-level folder), then the folders
// inside it from the top down, the nearest last. The runtime variables
// (ENV-15) come after them all: RuntimeScope adds them, as HTTPService's
// sends do. The WebSocket service renders its URL, headers and messages
// with both (WS-11).
func ScopeChain(src collection.VariablesSource) (template.Chain, error) {
	var chain template.Chain
	// The local values of the global variables (SEC-02) replace their
	// shared values, within the global scope.
	var globalLocals map[string]string
	if ws := src.Workspace; ws != nil && (src.Globals != nil || ws.File != nil) {
		var err error
		if globalLocals, err = localvalues.New(ws.LocalDir).Read(localvalues.Global(ws.ID())); err != nil {
			return nil, err
		}
	}
	if src.Globals != nil {
		chain = append(chain, withLocals(variablesScope(labelGlobal, src.Globals), src.Globals, globalLocals))
	}
	if ws := src.Workspace; ws != nil {
		if src.Globals == nil && ws.File != nil {
			vars := ws.File.Variables
			if requestsecrets.HasSecretVariables(vars) {
				values, err := requestsecrets.New(ws.LocalDir).Read(ws.ID())
				if err != nil {
					return nil, err
				}
				vars = requestsecrets.MergeVariables(vars, values)
			}
			chain = append(chain, withLocals(variablesScope(labelGlobal, vars), vars, globalLocals))
		}
		env, ok, err := environments.New(ws.Root, ws.LocalDir, ws.Newer()).Active()
		if err != nil {
			return nil, err
		}
		if ok {
			chain = append(chain, env.Scope())
		}
	}
	for i, vars := range src.FolderVariables {
		label := labelFolder
		if i < len(src.Folders) {
			if f := src.Folders[i]; f.Collection {
				label = labelCollection + f.Name
			} else {
				label = labelFolder + ": " + f.Name
			}
		}
		chain = append(chain, variablesScope(label, vars))
	}
	return chain, nil
}

// variablesScope is the enabled variables of a project or folder file,
// secret ones with their local value merged in (requestsecrets). A secret
// variable keeps its flag, so its value is masked in the editor and the
// timeline (DBG-01); one without a value on this computer (typed on
// another one) is left out, so a reference to it reports it undefined
// instead of sending "".
func variablesScope(label string, vars []projectfile.Variable) template.MapScope {
	m := make(map[string]template.Variable, len(vars))
	for _, v := range vars {
		if v.Disabled || (v.Secret && v.Value == "") {
			continue
		}
		m[v.Name] = template.Variable{Value: v.Value, Secret: v.Secret}
	}
	return template.MapScope{Label: label, Variables: m}
}

// withLocals returns sc (variablesScope of vars) with the local values
// (SEC-02, by name) of vars's enabled variables that are not secret: the
// value this computer sends instead of the shared one, marked Local. A
// local value for a name vars does not define is ignored.
func withLocals(sc template.MapScope, vars []projectfile.Variable, locals map[string]string) template.MapScope {
	if len(locals) == 0 {
		return sc
	}
	for _, v := range vars {
		lv, ok := locals[v.Name]
		if !ok || v.Disabled || v.Secret {
			continue
		}
		sc.Variables[v.Name] = template.Variable{Value: lv, Local: true}
	}
	return sc
}

// expansion renders the {{variables}} of one send. Undefined names do not
// stop the build: they are left as written and collected, so one error can
// list those of every field (unresolvedErr), or, with allowUnresolved, they
// are sent as written. Every other problem stops the build at once.
type expansion struct {
	resolver        template.Resolver
	allowUnresolved bool
	unresolved      []template.Problem
	// askPrompts collects the {{$prompt}} references without an answer in
	// resolver.Prompts, so one error lists those of every field
	// (promptErr, DYN-07). Without it they are left as written with
	// allowUnresolved (previews), and refuse otherwise.
	askPrompts bool
	prompts    []template.Problem
	// skipped are the undefined names left as written with
	// allowUnresolved.
	skipped []string
	// secrets are the values of the secret variables substituted, for the
	// timeline's masking (DBG-01).
	secrets []string
	// record keeps every render's result in results, for the resolution
	// view (DBG-08, resolution.go): what this very expansion substituted.
	record  bool
	results []template.Result
}

func (x *expansion) expand(text string) (string, error) {
	res := x.resolver.Render(text)
	if x.record {
		x.results = append(x.results, res)
	}
	collectSecrets(&x.secrets, res.Refs)
	var other []template.Problem
	for _, p := range res.Problems {
		if p.Kind == template.ProblemPrompt && x.askPrompts {
			x.prompts = append(x.prompts, p)
			continue
		}
		// A preview leaves unanswered prompts, and {{$file}} references
		// it does not read, as written, listed with the undefined names.
		if p.Kind == template.ProblemUnresolved || (x.allowUnresolved && (p.Kind == template.ProblemPrompt || filesDeferred(p) || responsesDeferred(p))) {
			if !x.allowUnresolved {
				x.unresolved = append(x.unresolved, p)
			} else if p.Name != "" {
				x.skipped = append(x.skipped, p.Name)
			}
			continue
		}
		other = append(other, p)
	}
	if err := (template.Result{Problems: other}).Err(); err != nil {
		return "", err
	}
	return res.Text, nil
}

// collectSecrets appends the values of the secret references in refs, and
// of the secret references nested in them.
func collectSecrets(out *[]string, refs []template.Ref) {
	for _, r := range refs {
		if r.Secret && r.Resolved && r.Value != "" {
			*out = append(*out, r.Value)
		}
		collectSecrets(out, r.Nested)
	}
}

// promptErr is the template.prompt error listing every {{$prompt}} label
// without an answer met so far (details.prompts), or nil.
func (x *expansion) promptErr() error {
	if len(x.prompts) == 0 {
		return nil
	}
	return template.Result{Problems: x.prompts}.Err()
}

// unresolvedErr is the template.unresolved error listing every undefined
// name met so far (details.names), or nil.
func (x *expansion) unresolvedErr() error {
	if len(x.unresolved) == 0 {
		return nil
	}
	return template.Result{Problems: x.unresolved}.Err()
}
