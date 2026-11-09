package collection

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	envservice "github.com/sorrelfox/postsonde/internal/app/services/environments"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/curlimport"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

const importScript = `#!/bin/sh
# List orders
curl https://api.test/orders -u ada:s3cret

# Broken
curl -H

curl -X POST https://api.test/orders --json '{"a":1}' --retry 2
`

// envImporter is a test format with an environment, for the
// environments:changed event.
type envImporter struct{}

func (envImporter) Format() importer.Format { return importer.Format{ID: "env", Name: "Environments"} }
func (envImporter) Detect(src importer.Source) int {
	if strings.HasPrefix(string(src.Data), "ENV") {
		return importer.Sure
	}
	return importer.No
}
func (envImporter) Parse(context.Context, importer.Source) (*importer.Model, error) {
	return &importer.Model{Environments: []importer.Environment{{Name: "Local",
		Variables: []environments.Variable{{Name: "token", Value: "tok-value", Secret: true}}}}}, nil
}

// namedEvents records the names of the events the service emits.
type namedEvents struct {
	mu   sync.Mutex
	list []string
}

func (e *namedEvents) emit(name string, _ any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, name)
}

func (e *namedEvents) names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.list...)
}

func (e *namedEvents) has(name string) bool { return slices.Contains(e.names(), name) }

func writeImportFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPreviewAndImport(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	ev := &namedEvents{}
	svc := New(manager, ev.emit, nil, WithImporters(importer.NewRegistry(curlimport.Importer{}, envImporter{})))
	ctx := t.Context()
	path := writeImportFile(t, "orders.sh", importScript)

	formats := svc.ImportFormats(ctx)
	if len(formats) != 2 || formats[0].ID != "curl" {
		t.Fatalf("formats = %+v", formats)
	}

	pre, err := svc.PreviewImport(ctx, PreviewImportInputDTO{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if pre.Format.ID != "curl" || pre.FileName != "orders.sh" || pre.Digest == "" || len(pre.Items) != 4 {
		t.Fatalf("preview = %+v", pre)
	}
	if pre.Items[0].Name != "orders" || pre.Items[0].Status != "new" || pre.Items[1].Name != "List orders" || pre.Items[1].Method != "GET" {
		t.Errorf("items = %+v", pre.Items)
	}
	if pre.Items[2].Kind != "skipped" || pre.Items[2].Source.Line != 6 || pre.Items[2].Diagnostics[0].Code != "curl.missing_url" {
		t.Errorf("skipped = %+v", pre.Items[2])
	}
	if c := pre.Counts; c.Folders != 1 || c.Requests != 2 || c.Skipped != 1 {
		t.Errorf("counts = %+v", c)
	}
	// Nothing written yet.
	if _, err := os.Stat(filepath.Join(dir, "orders")); !os.IsNotExist(err) {
		t.Fatalf("preview wrote: %v", err)
	}

	res, err := svc.Import(ctx, ImportInputDTO{Path: path, Digest: pre.Digest, ParentID: "fld_users", FolderName: "Orders API", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	rep := res.Report
	if rep.Items[0].Status != "imported" || rep.Items[0].NodeID != res.NodeID || rep.Counts.Requests != 2 {
		t.Fatalf("report = %+v", rep)
	}
	var folder *NodeDTO
	for i := range res.Tree.Nodes {
		if res.Tree.Nodes[i].ID == res.NodeID {
			folder = &res.Tree.Nodes[i]
		}
	}
	if folder == nil || folder.Name != "Orders API" || folder.Path != "users/orders-api" || len(folder.ChildIDs) != 2 {
		t.Fatalf("folder = %+v", folder)
	}
	data := readFile(t, dir, "users/orders-api/list-orders.json")
	if strings.Contains(data, "s3cret") {
		t.Errorf("secret in project: %s", data)
	}
	req := getRequest(t, svc, rep.Items[1].NodeID)
	if req.Auth.Password != "s3cret" || req.Name != "List orders" {
		t.Errorf("request = %+v", req)
	}
	if !ev.has(EventChanged) || ev.has(envservice.EventChanged) {
		t.Errorf("events = %v", ev.names())
	}

	// The file changed after the preview.
	if err := os.WriteFile(path, []byte("curl https://api.test/other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Import(ctx, ImportInputDTO{Path: path, Digest: pre.Digest, Index: -1})
	checkCode(t, err, errs.InvalidArgument, importer.CodeSourceChanged)

	// Environments go next to the project's, and are announced.
	envPath := writeImportFile(t, "local.env", "ENV")
	res, err = svc.Import(ctx, ImportInputDTO{Path: envPath, Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	// A file without requests makes no folder.
	if res.Report.Counts.Environments != 1 || res.Report.Counts.Folders != 0 || !res.Report.IntoParent || !ev.has(envservice.EventChanged) {
		t.Errorf("environment import = %+v, events %v", res.Report.Counts, ev.names())
	}
	entries, _ := os.ReadDir(filepath.Join(dir, projectfile.EnvironmentsDir))
	if len(entries) != 1 || strings.Contains(readFile(t, dir, projectfile.EnvironmentsDir+"/"+entries[0].Name()), "tok-value") {
		t.Errorf("environment files = %v", entries)
	}
}

func TestPreviewImportErrors(t *testing.T) {
	t.Parallel()
	svc := New(nil, nil, nil)
	ctx := t.Context()
	_, err := svc.PreviewImport(ctx, PreviewImportInputDTO{Path: filepath.Join(t.TempDir(), "missing.sh")})
	checkCode(t, err, errs.NotFound, importer.CodeReadFailed)
	_, err = svc.PreviewImport(ctx, PreviewImportInputDTO{Path: "relative.sh"})
	checkCode(t, err, errs.InvalidArgument, importer.CodeReadFailed)
	_, err = svc.PreviewImport(ctx, PreviewImportInputDTO{Path: t.TempDir()})
	checkCode(t, err, errs.InvalidArgument, importer.CodeReadFailed)
	_, err = svc.PreviewImport(ctx, PreviewImportInputDTO{Path: writeImportFile(t, "a.json", `{"x":1}`)})
	checkCode(t, err, errs.Unsupported, importer.CodeUnknownFormat)
	_, err = svc.PreviewImport(ctx, PreviewImportInputDTO{Path: writeImportFile(t, "a.txt", "# nothing"), Format: "curl"})
	checkCode(t, err, errs.Parse, importer.CodeInvalidFile)

	// Import needs an open project.
	svc = New(coreworkspace.New(t.TempDir()), nil, nil)
	_, err = svc.Import(ctx, ImportInputDTO{Path: writeImportFile(t, "a.sh", "curl https://h/")})
	if err == nil {
		t.Error("import without a project succeeded")
	}
	if _, err := svc.PickImportFile(ctx); err == nil {
		t.Error("PickImportFile without a picker succeeded")
	}
	picked := New(nil, nil, nil, WithFilePicker(func(_ context.Context, d FileDialog) (string, error) {
		if d.Title == "" {
			t.Error("no title")
		}
		return "/x/y.sh", nil
	}))
	if p, err := picked.PickImportFile(ctx); p != "/x/y.sh" || err != nil {
		t.Errorf("PickImportFile = %q, %v", p, err)
	}
}
