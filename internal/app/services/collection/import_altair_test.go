package collection

import (
	"path/filepath"
	"slices"
	"testing"

	envservice "github.com/sorrelfox/postsonde/internal/app/services/environments"
)

// An Altair window and environment export go through the default formats
// into a project (the collection export is imported and sent in
// internal/app/services/http/import_altair_test.go).
func TestImportAltair(t *testing.T) {
	t.Parallel()
	manager, _ := newProject(t)
	svc := New(manager, nil, nil)
	ctx := t.Context()
	fixture := func(name string) string {
		path, err := filepath.Abs(filepath.Join("..", "..", "..", "core", "importer", "altair", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	if !slices.ContainsFunc(svc.ImportFormats(ctx), func(f ImportFormatDTO) bool { return f.ID == "altair" }) {
		t.Fatalf("formats = %+v", svc.ImportFormats(ctx))
	}

	for _, name := range []string{"starwars-v4.agq", "github-v5.agq"} {
		path := fixture(name)
		pre, err := svc.PreviewImport(ctx, PreviewImportInputDTO{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		res, err := svc.Import(ctx, ImportInputDTO{Path: path, Digest: pre.Digest, Index: -1})
		if err != nil {
			t.Fatal(err)
		}
		if c := res.Report.Counts; pre.Format.ID != "altair" || c.Folders != 1 || c.Requests != 1 || c.Failed != 0 || c.Warnings == 0 {
			t.Errorf("%s: %s, counts %+v", name, pre.Format.ID, c)
		}
		if it := res.Report.Items[1]; it.FileKind != "graphql" || it.Status != "imported" {
			t.Errorf("%s: request %+v", name, it)
		}
	}

	path := fixture("staging-environment.json")
	pre, err := svc.PreviewImport(ctx, PreviewImportInputDTO{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Import(ctx, ImportInputDTO{Path: path, Digest: pre.Digest, Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	if c := res.Report.Counts; c.Environments != 1 || c.Folders != 0 {
		t.Errorf("environment counts = %+v", c)
	}
	envs, err := envservice.New(manager, nil).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs.Environments) != 1 || envs.Environments[0].Name != "Staging" || len(envs.Environments[0].Variables) != 6 {
		t.Errorf("environments = %+v", envs.Environments)
	}
}
