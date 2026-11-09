package http

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/localvalues"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// A local value (SEC-02) replaces the shared value of its own
// variable, at its own level: it does not move the variable up the chain,
// so a nearer level still wins over it (docs/environments.md#local-values).
func TestLocalValuePrecedence(t *testing.T) {
	t.Parallel()
	ws, err := coreworkspace.New(t.TempDir()).Create(filepath.Join(t.TempDir(), "p"), "P")
	if err != nil {
		t.Fatal(err)
	}
	ws.File.Variables = []projectfile.Variable{
		{Name: "a", Value: "global-a"},
		{Name: "c", Value: "global-c"},
		{Name: "d", Value: "global-d"},
		{Name: "off", Value: "global-off", Disabled: true},
	}
	if err := localvalues.New(ws.LocalDir).Write(localvalues.Global(ws.ID()), map[string]string{
		"a": "global-a-local", "c": "global-c-local", "off": "global-off-local", "undefined": "orphan",
	}); err != nil {
		t.Fatal(err)
	}
	store := environments.New(ws.Root, ws.LocalDir, false)
	env, err := store.Create("Dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveVariables(env.ID, []environments.Variable{
		{Name: "a", Value: "env-a", Local: true, LocalValue: "env-a-local"},
		{Name: "b", Value: "env-b", Local: true, LocalValue: "env-b-local"},
		{Name: "d", Value: "env-d", Disabled: true, Local: true, LocalValue: "env-d-local"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetActive(env.ID); err != nil {
		t.Fatal(err)
	}
	src := httpSource("/")
	src.Workspace = ws
	src.FolderVariables = [][]projectfile.Variable{{{Name: "b", Value: "folder-b"}}}

	chain, err := scopeChain(src)
	if err != nil {
		t.Fatal(err)
	}
	r := template.Resolver{Scopes: chain}
	tests := []struct{ ref, want string }{
		// The environment's local value wins over its shared value and
		// over the global variable (and its local value).
		{"{{a}}", "env-a-local"},
		// A folder's shared value still wins over the environment's local
		// value: levels first, then local over shared within a level.
		{"{{b}}", "folder-b"},
		// A global local value alone.
		{"{{c}}", "global-c-local"},
		// A disabled environment variable is left out with its local
		// value; the global one is used.
		{"{{d}}", "global-d"},
	}
	for _, tt := range tests {
		if res := r.Render(tt.ref); res.Text != tt.want || res.Err() != nil {
			t.Errorf("%s = %q (%v), want %q", tt.ref, res.Text, res.Err(), tt.want)
		}
	}
	// A disabled variable's local value, and one for a name no variable
	// has, define nothing.
	for _, ref := range []string{"{{off}}", "{{undefined}}"} {
		if res := r.Render(ref); res.Err() == nil {
			t.Errorf("%s resolved to %q", ref, res.Text)
		}
	}

	// The hover says where a value comes from and that it is local.
	f := newFixture(t, src, nil)
	vars, err := f.svc.Variables(t.Context(), "req_1")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]VariableDTO{}
	for _, v := range vars.Variables {
		byName[v.Name] = v
	}
	if v := byName["a"]; v.Value != "env-a-local" || !v.Local || v.Scope != "Environment: Dev" || !slices.Equal(v.Overrides, []string{"Global"}) {
		t.Errorf("a = %+v", v)
	}
	if v := byName["b"]; v.Value != "folder-b" || v.Local || !slices.Equal(v.Overrides, []string{"Environment: Dev"}) {
		t.Errorf("b = %+v", v)
	}
	if v := byName["c"]; v.Value != "global-c-local" || !v.Local || v.Scope != "Global" {
		t.Errorf("c = %+v", v)
	}
	if v := byName["d"]; v.Value != "global-d" || v.Local {
		t.Errorf("d = %+v", v)
	}
}
