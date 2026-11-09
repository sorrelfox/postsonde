package environments

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func checkCode(t *testing.T, err error, kind errs.Kind, code string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an *errs.Error", err)
	}
	if errs.KindOf(err) != kind || e.Code != code || e.Hint == "" {
		t.Errorf("err = kind %s code %q hint %q, want kind %s code %q and a hint", errs.KindOf(err), e.Code, e.Hint, kind, code)
	}
}

func TestNoWorkspace(t *testing.T) {
	t.Parallel()
	svc := New(workspace.New(tempDir(t)), nil)
	ctx := t.Context()
	_, err := svc.List(ctx)
	checkCode(t, err, errs.NotFound, workspace.CodeNoWorkspace)
	_, err = svc.Create(ctx, "Local")
	checkCode(t, err, errs.NotFound, workspace.CodeNoWorkspace)
	checkCode(t, svc.SetActive(ctx, ""), errs.NotFound, workspace.CodeNoWorkspace)
}

func TestFlow(t *testing.T) {
	t.Parallel()
	data := tempDir(t)
	project := filepath.Join(tempDir(t), "shop-api")
	m := workspace.New(data)
	if _, err := m.Create(project, "Shop API"); err != nil {
		t.Fatal(err)
	}
	svc := New(m, nil)
	ctx := t.Context()

	list, err := svc.List(ctx)
	if err != nil || len(list.Environments) != 0 || list.ActiveID != "" || list.Problems == nil {
		t.Fatalf("List = %+v, %v", list, err)
	}
	local, err := svc.Create(ctx, "Local")
	if err != nil {
		t.Fatal(err)
	}
	if local.Variables == nil || local.FileName != "local.json" {
		t.Errorf("Create = %+v", local)
	}
	_, err = svc.Create(ctx, "local")
	checkCode(t, err, errs.AlreadyExists, core.CodeNameTaken)

	saved, err := svc.SaveVariables(ctx, SaveVariablesInputDTO{ID: local.ID, Variables: []VariableDTO{
		{Name: "baseUrl", Value: "http://localhost:8080", Enabled: true},
		{Name: "token", Value: "dev-9f2a71c0", Secret: true, Enabled: true},
		{Name: "old", Value: "x", Enabled: false},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Variables) != 3 || saved.Variables[1].Value != "dev-9f2a71c0" || saved.Variables[2].Enabled {
		t.Errorf("SaveVariables = %+v", saved)
	}
	file, err := os.ReadFile(filepath.Join(project, "environments", "local.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(file), "dev-9f2a71c0") || !strings.Contains(string(file), `"disabled": true`) {
		t.Errorf("project file:\n%s", file)
	}

	renamed, err := svc.Rename(ctx, RenameInputDTO{ID: local.ID, Name: "Local dev"})
	if err != nil || renamed.FileName != "local-dev.json" || renamed.Variables[1].Value != "dev-9f2a71c0" {
		t.Errorf("Rename = %+v, %v", renamed, err)
	}

	colored, err := svc.SetColor(ctx, SetColorInputDTO{ID: local.ID, Color: "#1F8A4C"})
	if err != nil || colored.Color != "#1f8a4c" {
		t.Errorf("SetColor = %+v, %v", colored, err)
	}
	file, err = os.ReadFile(filepath.Join(project, "environments", "local-dev.json"))
	if err != nil || !strings.Contains(string(file), `"color": "#1f8a4c"`) {
		t.Errorf("project file after SetColor (%v):\n%s", err, file)
	}
	_, err = svc.SetColor(ctx, SetColorInputDTO{ID: local.ID, Color: "red; background: url(x)"})
	checkCode(t, err, errs.InvalidArgument, core.CodeInvalidColor)

	if err := svc.SetActive(ctx, local.ID); err != nil {
		t.Fatal(err)
	}
	list, err = svc.List(ctx)
	if err == nil && len(list.Environments) == 1 && list.Environments[0].Color != "#1f8a4c" {
		t.Errorf("List colour = %q", list.Environments[0].Color)
	}
	if err != nil || list.ActiveID != local.ID || len(list.Environments) != 1 {
		t.Errorf("List = %+v, %v", list, err)
	}
	checkCode(t, svc.SetActive(ctx, "env_nope"), errs.NotFound, core.CodeNotFound)

	if err := svc.Delete(ctx, local.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = svc.List(ctx)
	if len(list.Environments) != 0 || list.ActiveID != "" {
		t.Errorf("after Delete: %+v", list)
	}
}

// Every successful change sends environments:changed with the active
// environment; reads and failed calls send nothing.
func TestChangedEvents(t *testing.T) {
	t.Parallel()
	m := workspace.New(tempDir(t))
	if _, err := m.Create(filepath.Join(tempDir(t), "p"), "P"); err != nil {
		t.Fatal(err)
	}
	var got []ChangedDTO
	svc := New(m, nil, WithEmit(func(name string, data any) {
		if name != EventChanged {
			t.Errorf("event %q", name)
		}
		got = append(got, data.(ChangedDTO))
	}))
	ctx := t.Context()
	env, err := svc.Create(ctx, "Dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetActive(ctx, env.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveVariables(ctx, SaveVariablesInputDTO{ID: env.ID, Variables: []VariableDTO{{Name: "a", Value: "1", Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Rename(ctx, RenameInputDTO{ID: env.ID, Name: "Local"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.List(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetActive(ctx, "env_missing"); err == nil {
		t.Fatal("SetActive of an unknown environment succeeded")
	}
	if err := svc.Delete(ctx, env.ID); err != nil {
		t.Fatal(err)
	}
	want := []ChangedDTO{{}, {ActiveID: env.ID}, {ActiveID: env.ID}, {ActiveID: env.ID}, {}}
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCancelled(t *testing.T) {
	t.Parallel()
	m := workspace.New(tempDir(t))
	project := filepath.Join(tempDir(t), "p")
	if _, err := m.Create(project, "P"); err != nil {
		t.Fatal(err)
	}
	svc := New(m, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := map[string]error{}
	_, calls["List"] = svc.List(ctx)
	_, calls["Create"] = svc.Create(ctx, "x")
	_, calls["Rename"] = svc.Rename(ctx, RenameInputDTO{ID: "a", Name: "b"})
	calls["Delete"] = svc.Delete(ctx, "a")
	_, calls["SaveVariables"] = svc.SaveVariables(ctx, SaveVariablesInputDTO{ID: "a"})
	calls["SetActive"] = svc.SetActive(ctx, "")
	for name, err := range calls {
		if !errors.Is(err, context.Canceled) || errs.KindOf(err) != errs.Cancelled {
			t.Errorf("%s: err = %v, want cancelled", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(project, "environments")); err == nil {
		t.Error("a cancelled call created the environments folder")
	}
}

// TestDTOJSON pins the JSON shape of the DTOs, the contract with the
// generated TypeScript models.
func TestDTOJSON(t *testing.T) {
	t.Parallel()
	env := core.Environment{
		ID:       "env_92f0d5b8a61c3e47",
		Name:     "Production",
		Color:    "#d0342c",
		FileName: "production.json",
		Variables: []core.Variable{
			{Name: "baseUrl", Value: "https://api.acme.example"},
			{Name: "token", Value: "prd-token", Secret: true, Description: "Personal access token"},
			{Name: "old", Value: "1", Disabled: true},
			{Name: "host", Value: "api.acme.example", Local: true, LocalValue: "localhost:8080"},
		},
	}
	tests := []struct {
		name string
		dto  any
	}{
		{"environments", toEnvironmentsDTO(core.Snapshot{
			Environments: []core.Environment{env, {ID: "env_0000000000000001", Name: "Empty", FileName: "empty.json", ReadOnly: true}},
			ActiveID:     env.ID,
			Problems: []core.Problem{
				{FileName: "broken.json", Err: errs.New(errs.Parse, "The file is not valid JSON.").WithCode("projectfile.parse_failed")},
				{FileName: "odd.json", Err: errors.New("plain error")},
			},
		})},
		{"environments_empty", toEnvironmentsDTO(core.Snapshot{})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.MarshalIndent(tt.dto, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, "dto/"+tt.name, append(got, '\n'))
		})
	}
}
