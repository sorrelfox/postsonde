package environments

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/localvalues"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

func TestGlobalVariables(t *testing.T) {
	t.Parallel()
	m := workspace.New(tempDir(t))
	if _, err := m.Create(filepath.Join(tempDir(t), "p"), "P"); err != nil {
		t.Fatal(err)
	}
	var stored []projectfile.Variable
	var events []string
	svc := New(m, nil,
		WithEmit(func(name string, _ any) { events = append(events, name) }),
		WithGlobals(Globals{
			Load: func(context.Context) ([]projectfile.Variable, bool, error) { return stored, true, nil },
			Save: func(_ context.Context, vars []projectfile.Variable) ([]projectfile.Variable, error) {
				stored = vars
				return vars, nil
			},
		}))
	ctx := t.Context()

	in := []VariableDTO{
		{Name: "base", Value: "https://api.test", Enabled: true},
		{Name: "key", Value: "s3cret", Secret: true, Enabled: true},
		{Name: "off", Value: "x", Description: "unused"},
	}
	got, err := svc.SaveGlobalVariables(ctx, SaveGlobalVariablesInputDTO{Variables: in})
	if err != nil {
		t.Fatal(err)
	}
	want := []projectfile.Variable{
		{Name: "base", Value: "https://api.test"},
		{Name: "key", Value: "s3cret", Secret: true},
		{Name: "off", Value: "x", Description: "unused", Disabled: true},
	}
	if !reflect.DeepEqual(stored, want) || !reflect.DeepEqual(got.Variables, in) {
		t.Errorf("saved %+v, returned %+v", stored, got)
	}
	if !reflect.DeepEqual(events, []string{EventChanged}) {
		t.Errorf("events = %v, want one %s", events, EventChanged)
	}
	loaded, err := svc.GlobalVariables(ctx)
	if err != nil || !loaded.ReadOnly || !reflect.DeepEqual(loaded.Variables, in) {
		t.Errorf("GlobalVariables = %+v, %v", loaded, err)
	}

	// Without a local value the DTO says so with null.
	if data, err := json.Marshal(loaded.Variables[0]); err != nil || !strings.Contains(string(data), `"localValue":null`) {
		t.Errorf("JSON = %s, %v", data, err)
	}

	bare := New(m, nil)
	if _, err := bare.GlobalVariables(ctx); errs.KindOf(err) != errs.Unsupported {
		t.Errorf("without globals: err = %v", err)
	}
	if _, err := bare.SaveGlobalVariables(ctx, SaveGlobalVariablesInputDTO{}); errs.KindOf(err) != errs.Unsupported {
		t.Errorf("without globals: err = %v", err)
	}
}

// Local values of environment and global variables (SEC-02) are
// kept in the workspace's local directory and never reach a project file:
// the global ones are not even handed to the function that writes
// postsonde.json.
func TestLocalValues(t *testing.T) {
	t.Parallel()
	m := workspace.New(tempDir(t))
	project := filepath.Join(tempDir(t), "p")
	ws, err := m.Create(project, "P")
	if err != nil {
		t.Fatal(err)
	}
	var stored []projectfile.Variable
	svc := New(m, nil, WithGlobals(Globals{
		Load: func(context.Context) ([]projectfile.Variable, bool, error) { return stored, false, nil },
		Save: func(_ context.Context, vars []projectfile.Variable) ([]projectfile.Variable, error) {
			stored = vars
			return vars, nil
		},
	}))
	ctx := t.Context()
	local := func(v string) *string { return &v }

	// Global variables.
	in := []VariableDTO{
		{Name: "base", Value: "https://api.shared", Enabled: true, LocalValue: local("http://localhost:3000")},
		{Name: "user", Value: "team", Enabled: true, LocalValue: local("")},
		{Name: "key", Value: "gl0bal-key", Secret: true, Enabled: true, LocalValue: local("dropped-for-a-secret")},
		{Name: "plain", Value: "shared", Enabled: true},
	}
	got, err := svc.SaveGlobalVariables(ctx, SaveGlobalVariablesInputDTO{Variables: in})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range stored {
		if v.Value == "http://localhost:3000" || v.Value == "dropped-for-a-secret" {
			t.Errorf("a local value went to the project writer: %+v", v)
		}
	}
	if stored[0].Value != "https://api.shared" || stored[1].Value != "team" {
		t.Errorf("stored = %+v", stored)
	}
	check := func(what string, vars []VariableDTO) {
		t.Helper()
		if vars[0].LocalValue == nil || *vars[0].LocalValue != "http://localhost:3000" || vars[0].Value != "https://api.shared" {
			t.Errorf("%s base = %+v", what, vars[0])
		}
		if vars[1].LocalValue == nil || *vars[1].LocalValue != "" {
			t.Errorf("%s user = %+v", what, vars[1])
		}
		if vars[2].LocalValue != nil || vars[3].LocalValue != nil {
			t.Errorf("%s key, plain = %+v, %+v", what, vars[2], vars[3])
		}
	}
	check("saved", got.Variables)
	loaded, err := svc.GlobalVariables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check("loaded", loaded.Variables)
	if values, err := localvalues.New(ws.LocalDir).Read(localvalues.Global(ws.ID())); err != nil ||
		values["base"] != "http://localhost:3000" || len(values) != 2 {
		t.Errorf("local values file = %v, %v", values, err)
	}

	// An environment's.
	env, err := svc.Create(ctx, "Dev")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := svc.SaveVariables(ctx, SaveVariablesInputDTO{ID: env.ID, Variables: []VariableDTO{
		{Name: "base", Value: "https://dev.shared", Enabled: true, LocalValue: local("http://localhost:4000")},
		{Name: "token", Value: "dev-t0ken", Secret: true, Enabled: true, LocalValue: local("dropped-too")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if lv := saved.Variables[0].LocalValue; lv == nil || *lv != "http://localhost:4000" || saved.Variables[1].LocalValue != nil {
		t.Errorf("SaveVariables = %+v", saved.Variables)
	}
	if _, err := svc.Rename(ctx, RenameInputDTO{ID: env.ID, Name: "Development"}); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx)
	if err != nil || list.Environments[0].Variables[0].LocalValue == nil {
		t.Errorf("List = %+v, %v", list, err)
	}

	// Nothing local in the project folder.
	err = filepath.WalkDir(project, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, v := range []string{"localhost", "dropped", "dev-t0ken", "gl0bal-key"} {
			if strings.Contains(string(data), v) {
				t.Errorf("%s holds %q:\n%s", path, v, data)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
