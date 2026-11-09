package environments

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/postman"
	"github.com/sorrelfox/postsonde/internal/core/importer/postsondeenv"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

// EXP-05: exported environments contain no secret and no local value, in
// either format, whatever the local side files hold.
func TestExportLeavesOutSecretAndLocalValues(t *testing.T) {
	t.Parallel()
	data := tempDir(t)
	project := filepath.Join(tempDir(t), "shop-api")
	m := workspace.New(data)
	if _, err := m.Create(project, "Shop API"); err != nil {
		t.Fatal(err)
	}
	out := tempDir(t)
	var dialogs []SaveDialog
	next := ""
	pick := func(_ context.Context, d SaveDialog) (string, error) {
		dialogs = append(dialogs, d)
		return next, nil
	}
	svc := New(m, nil, WithExport(pick, "1.2.3"))
	ctx := t.Context()
	env, err := svc.Create(ctx, "Staging EU")
	if err != nil {
		t.Fatal(err)
	}
	local := "http://localhost:8080-LOCAL-VALUE"
	_, err = svc.SaveVariables(ctx, SaveVariablesInputDTO{ID: env.ID, Variables: []VariableDTO{
		{Name: "baseUrl", Value: "https://staging.example.test", Enabled: true, LocalValue: &local},
		{Name: "token", Value: "tok-SECRET-VALUE-1", Secret: true, Enabled: true},
		{Name: "apiKey", Value: "key-SECRET-VALUE-2", Secret: true, Enabled: false},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Local values are also kept for the global variables; neither may leak.
	for _, leak := range []string{"SECRET-VALUE", "LOCAL-VALUE"} {
		if !localHolds(t, data, leak) {
			t.Fatalf("the local side files do not hold %q: the test would prove nothing", leak)
		}
	}

	tests := []struct {
		format, file string
		detect       importer.Importer
	}{
		{FormatPostSonde, "staging-eu.json", postsondeenv.Importer{}},
		{FormatPostman, "staging-eu.postman_environment.json", postman.Importer{}},
	}
	for _, tt := range tests {
		next = filepath.Join(out, tt.file)
		res, err := svc.Export(ctx, ExportInputDTO{ID: env.ID, Format: tt.format})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Saved || res.Path != next || res.Variables != 3 || res.Secrets != 2 {
			t.Errorf("%s: Export = %+v", tt.format, res)
		}
		if d := dialogs[len(dialogs)-1]; d.Filename != tt.file || !strings.Contains(d.Message, "not exported") {
			t.Errorf("%s: dialog = %+v", tt.format, d)
		}
		got, err := os.ReadFile(next)
		if err != nil {
			t.Fatal(err)
		}
		for _, leak := range []string{"SECRET-VALUE", "LOCAL-VALUE", "localhost"} {
			if strings.Contains(string(got), leak) {
				t.Errorf("%s: the export holds %q:\n%s", tt.format, leak, got)
			}
		}
		if !json.Valid(got) || !strings.Contains(string(got), "https://staging.example.test") {
			t.Errorf("%s: export = %s", tt.format, got)
		}
		src := importer.Source{Name: tt.file, Data: got}
		if tt.detect.Detect(src) != importer.Sure {
			t.Errorf("%s: its importer does not recognise the export", tt.format)
		}
		model, err := tt.detect.Parse(ctx, src)
		if err != nil || len(model.Environments) != 1 {
			t.Fatalf("%s: Parse = %+v, %v", tt.format, model, err)
		}
		vars := model.Environments[0].Variables
		if len(vars) != 3 || vars[1].Name != "token" || !vars[1].Secret || vars[1].Value != "" || !vars[2].Disabled {
			t.Errorf("%s: imported variables = %+v", tt.format, vars)
		}
	}

	// Cancelled: nothing written.
	next = ""
	res, err := svc.Export(ctx, ExportInputDTO{ID: env.ID, Format: FormatPostSonde})
	if err != nil || res.Saved {
		t.Errorf("cancelled Export = %+v, %v", res, err)
	}
	_, err = svc.Export(ctx, ExportInputDTO{ID: env.ID, Format: "yaml"})
	checkCode(t, err, errs.InvalidArgument, CodeInvalidFormat)
	next = filepath.Join(out, "missing-folder", "x.json")
	_, err = svc.Export(ctx, ExportInputDTO{ID: env.ID, Format: FormatPostSonde})
	checkCode(t, err, errs.NotFound, CodeExportFailed)
	_, err = New(m, nil).Export(ctx, ExportInputDTO{ID: env.ID, Format: FormatPostSonde})
	checkCode(t, err, errs.Unsupported, CodeExportUnavailable)
}

// localHolds reports whether a file under dir contains text.
func localHolds(t *testing.T, dir, text string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(b), text) {
			found = true
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}
