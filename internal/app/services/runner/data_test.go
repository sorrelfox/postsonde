package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	corerunner "github.com/sorrelfox/postsonde/internal/core/runner"
)

// dataFolder has two requests that use the row's values; the folder
// defines id too, which the row wins over.
func dataFolder() map[string]string {
	return map[string]string{
		"rows/_folder.json": `{"kind": "folder", "id": "fld_rows", "name": "Rows", "order": ["get.json", "echo.json"], "variables": [{"name": "id", "value": "folder"}]}`,
		"rows/get.json": httpFile("req_get", "Get", "GET", "/users/{{id}}/{{name}}",
			`, "assertions": [{"target": "jsonpath", "expr": "$.path", "op": "equals", "expected": "/users/{{id}}/{{name}}"}]`),
		"rows/echo.json": httpFile("req_echo", "Echo", "GET", "/echo/{{id}}", ""),
	}
}

// pickFrom makes f's service pick path in the open dialog.
func (f *fixture) pickFrom(path string) {
	f.svc.pickData = func(ctx context.Context, d FileDialog) (string, error) { return path, nil }
}

func (f *fixture) writeData(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(f.dir), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDataDrivenRun (RUN-05): a run over a 100-row CSV file makes 100
// iterations, each sending its row's values.
func TestDataDrivenRun(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	f := newFixture(t, srv.URL, dataFolder())
	var csv strings.Builder
	csv.WriteString("\xef\xbb\xbfid,name\r\n")
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&csv, "%d,\"user-%d\"\r\n", i, i)
	}
	path := f.writeData(t, "users.csv", csv.String())
	f.pickFrom(path)
	preview, err := f.svc.PickDataFile(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if preview.RowCount != 100 || preview.Format != "csv" || preview.Delimiter != "," || !slices.Equal(preview.Columns, []string{"id", "name"}) ||
		len(preview.Rows) != MaxPreviewRows || !slices.Equal(preview.Rows[0], []string{"1", "user-1"}) || preview.Name != "users.csv" {
		t.Fatalf("preview = %+v", preview)
	}

	id := f.start(t, RunInputDTO{FolderID: "fld_rows", Iterations: 1, DataFile: path})
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusOK || done.Items != 200 {
		t.Fatalf("done = %+v", done)
	}
	var want []string
	for i := 1; i <= 100; i++ {
		want = append(want, fmt.Sprintf("/users/%d/user-%d", i, i), fmt.Sprintf("/echo/%d", i))
	}
	if got := srv.got(); !slices.Equal(got, want) {
		t.Fatalf("paths =\n%v\nwant\n%v", got, want)
	}
	results := f.rec.resultsOf(id)
	for i, r := range results {
		if r.Iteration != i/2+1 || r.Outcome != OutcomePassed {
			t.Fatalf("result %d = iteration %d, %s (%+v)", i, r.Iteration, r.Outcome, r)
		}
	}
	if len(results[198].Tests) != 1 || results[198].Tests[0].Outcome != "passed" {
		t.Errorf("last assertion = %+v", results[198].Tests)
	}
	f.rec.mu.Lock()
	progress := slices.Clone(f.rec.progress[id])
	f.rec.mu.Unlock()
	if last := progress[len(progress)-1]; last.Total != 200 {
		t.Errorf("progress total = %d, want 200", last.Total)
	}
}

// TestDataDrivenRunJSON: JSON rows keep their text; a row without a
// member leaves the variable to the scopes below (here the folder's).
func TestDataDrivenRunJSON(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	f := newFixture(t, srv.URL, dataFolder())
	path := f.writeData(t, "rows.json", `[{"id": 1.50, "name": true}, {"name": "no-id"}, {"id": "3", "name": null}]`)
	f.pickFrom(path)
	if _, err := f.svc.PickDataFile(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	id := f.start(t, RunInputDTO{FolderID: "fld_rows", DataFile: path})
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK || done.Items != 6 {
		t.Fatalf("done = %+v", done)
	}
	want := []string{"/users/1.50/true", "/echo/1.50", "/users/folder/no-id", "/echo/folder", "/users/3/", "/echo/3"}
	if got := srv.got(); !slices.Equal(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

func TestPickDataFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "http://localhost:1", dataFolder())

	// No dialog.
	_, err := f.svc.PickDataFile(t.Context(), "")
	checkCode(t, err, errs.Unsupported, CodeUnavailable)

	// Cancelled.
	f.pickFrom("")
	if p, err := f.svc.PickDataFile(t.Context(), ""); err != nil || p.Path != "" {
		t.Fatalf("cancelled = %+v, %v", p, err)
	}

	// A semicolon file read with commas, then again with semicolons.
	path := f.writeData(t, "semi.csv", "id;name\n1;a\n")
	f.pickFrom(path)
	p, err := f.svc.PickDataFile(t.Context(), ",")
	if err != nil || !slices.Equal(p.Columns, []string{"id;name"}) || p.Delimiter != "," {
		t.Fatalf("with commas = %+v, %v", p, err)
	}
	p, err = f.svc.PreviewDataFile(t.Context(), path, ";")
	if err != nil || !slices.Equal(p.Columns, []string{"id", "name"}) || p.Delimiter != ";" || p.Rows[0][1] != "a" {
		t.Fatalf("with semicolons = %+v, %v", p, err)
	}
	_, err = f.svc.PreviewDataFile(t.Context(), path, ":")
	checkCode(t, err, errs.InvalidArgument, CodeInvalidDelimiter)
	_, err = f.svc.PickDataFile(t.Context(), ",;")
	checkCode(t, err, errs.InvalidArgument, CodeInvalidDelimiter)

	// A file never picked is not read.
	other := f.writeData(t, "other.csv", "id\n1\n")
	_, err = f.svc.PreviewDataFile(t.Context(), other, "")
	checkCode(t, err, errs.PermissionDenied, CodeDataNotPicked)

	// A file that is not a data file: the error names the path picked.
	bad := f.writeData(t, "bad.json", `{"id": 1}`)
	f.pickFrom(bad)
	_, err = f.svc.PickDataFile(t.Context(), "")
	checkCode(t, err, errs.InvalidArgument, corerunner.CodeDataInvalid)
	if e, _ := errors.AsType[*errs.Error](err); e.Details["path"] != bad {
		t.Errorf("details = %v", e.Details)
	}
}

func TestStartRefusesDataFile(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	f := newFixture(t, srv.URL, dataFolder())
	notPicked := f.writeData(t, "notpicked.csv", "id\n1\n")
	empty := f.writeData(t, "empty.csv", "id\n")
	f.pickFrom(empty)
	if _, err := f.svc.PickDataFile(t.Context(), ""); err == nil {
		t.Fatal("empty file previewed")
	}
	tests := []struct {
		name string
		in   RunInputDTO
		kind errs.Kind
		code string
	}{
		{"not picked", RunInputDTO{FolderID: "fld_rows", DataFile: notPicked}, errs.PermissionDenied, CodeDataNotPicked},
		{"no rows", RunInputDTO{FolderID: "fld_rows", DataFile: empty}, errs.InvalidArgument, corerunner.CodeDataEmpty},
		{"bad delimiter", RunInputDTO{FolderID: "fld_rows", DataFile: empty, DataDelimiter: "x"}, errs.InvalidArgument, CodeInvalidDelimiter},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.svc.Start(t.Context(), tt.in)
			checkCode(t, err, tt.kind, tt.code)
		})
	}
	if got := srv.got(); len(got) != 0 {
		t.Fatalf("refused runs sent %v", got)
	}

	// A file changed since it was picked is read again at the start. Its
	// row has no name: Get is listed with the undefined variable.
	if err := os.WriteFile(empty, []byte("id\n9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := f.start(t, RunInputDTO{FolderID: "fld_rows", DataFile: empty})
	f.rec.wait(t, id)
	if got := srv.got(); !slices.Equal(got, []string{"/echo/9"}) {
		t.Errorf("paths = %v", got)
	}
	if r := f.rec.resultsOf(id); len(r) != 2 || r[0].Outcome != OutcomeError || r[0].Error.Details["names"] != "name" {
		t.Errorf("results = %+v", r)
	}
}
