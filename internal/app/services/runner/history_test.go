package runner

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/runner/report"
	"github.com/sorrelfox/postsonde/internal/core/runner/runstore"
)

const runSecret = "s3cr3t-value"

// secretFolder has a request whose assertion fails with the secret in its
// message (the server echoes the path), one that passes, and a WebSocket
// request a run skips.
func secretFolder() map[string]string {
	return map[string]string{
		"sec/_folder.json": `{"kind": "folder", "id": "fld_sec", "name": "Secure API", "order": ["token.json", "ok.json", "socket.json"]}`,
		"sec/token.json": httpFile("req_token", "Token", "GET", "/token/"+runSecret,
			`, "assertions": [{"target": "jsonpath", "expr": "$.path", "op": "equals", "expected": "/nope"}]`),
		"sec/ok.json":     httpFile("req_ok", "OK", "GET", "/ok", `, "assertions": [{"target": "status", "op": "equals", "expected": "200"}]`),
		"sec/socket.json": `{"kind": "websocket", "id": "req_socket", "name": "Socket", "url": "ws://localhost/feed"}`,
	}
}

// saves records the report dialogs and answers with paths in dir.
type saves struct {
	mu      sync.Mutex
	dir     string
	dialogs []SaveDialog
	cancel  bool
}

func (s *saves) pick(ctx context.Context, d SaveDialog) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dialogs = append(s.dialogs, d)
	if s.cancel {
		return "", nil
	}
	return filepath.Join(s.dir, d.Filename), nil
}

// withHistory keeps f's runs in its project's local directory, masking
// runSecret, with limits.
func (f *fixture) withHistory(limits runstore.Limits) *saves {
	sv := &saves{}
	WithRunHistory(f.localDir, limits)(f.svc)
	WithSecrets(func(context.Context) ([]string, error) { return []string{runSecret}, nil })(f.svc)
	WithReportDialog(sv.pick)(f.svc)
	return sv
}

func (f *fixture) runsDir(t *testing.T) string {
	t.Helper()
	local, err := f.localDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(local, runstore.Dir)
}

// noSecretIn fails when a file under dir holds the secret.
func noSecretIn(t *testing.T, dir string) {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n++
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), runSecret) {
			t.Errorf("%s holds the secret", path)
		}
		return nil
	})
	if err != nil || n == 0 {
		t.Fatalf("walk %s: %d files, %v", dir, n, err)
	}
}

// TestRunHistory (RUN-09): a run that ended is kept, secrets masked, in
// the project's local directory, listed, reopened and removed.
func TestRunHistory(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	f := newFixture(t, srv.URL, secretFolder())
	f.withHistory(runstore.Limits{})

	id := f.start(t, RunInputDTO{FolderID: "fld_sec", Iterations: 2, DelayMs: 10})
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	// Saved before the done event.
	runs, err := f.svc.Runs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	s := runs[0]
	if s.ID != id || s.FolderID != "fld_sec" || s.Folder != "Secure API" || s.Project != "Shop" || s.Status != "ok" ||
		s.Iterations != 2 || s.Requests != 3 || s.Total != 6 || s.DelayMs != 10 || s.Results != 6 || s.Passed != 2 || s.Failed != 2 ||
		s.Skipped != 2 || s.Errors != 0 || s.StartedAt == 0 || s.EndedAt < s.StartedAt || s.DataFile != "" {
		t.Fatalf("summary = %+v", s)
	}

	rec, err := f.svc.Run(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	live := f.rec.resultsOf(id)
	if len(rec.Results) != len(live) {
		t.Fatalf("results = %d, want %d", len(rec.Results), len(live))
	}
	for i, r := range rec.Results {
		l := live[i]
		if r.Index != l.Index || r.Iteration != l.Iteration || r.RequestID != l.RequestID || r.Outcome != l.Outcome ||
			r.StatusCode != l.StatusCode || r.TestsFailed != l.TestsFailed || r.HasResponse || r.Path == nil || r.Tests == nil {
			t.Errorf("result %d = %+v\nlive %+v", i, r, l)
		}
	}
	token := rec.Results[0]
	if len(token.Tests) != 1 || strings.Contains(token.Tests[0].Message, runSecret) || !strings.Contains(token.Tests[0].Message, "••••••") ||
		strings.Contains(token.Tests[0].Actual, runSecret) {
		t.Errorf("token test = %+v", token.Tests)
	}
	if !strings.Contains(live[0].Tests[0].Message, runSecret) {
		t.Fatalf("the live result should hold the secret, the test proves nothing: %+v", live[0].Tests)
	}
	if skip := rec.Results[2]; skip.Outcome != OutcomeSkipped || skip.Skip != "kind" {
		t.Errorf("skipped = %+v", skip)
	}
	noSecretIn(t, f.runsDir(t))
	// Never in the project.
	if _, err := os.Stat(filepath.Join(f.dir, runstore.Dir)); !os.IsNotExist(err) {
		t.Errorf("runs in the project: %v", err)
	}

	if err := f.svc.DeleteRun(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Run(t.Context(), id)
	checkCode(t, err, errs.NotFound, runstore.CodeNotFound)
	_, err = f.svc.Run(t.Context(), "../../x")
	checkCode(t, err, errs.InvalidArgument, runstore.CodeInvalidID)
}

// TestRunHistoryStatuses: stopped at a failure, cancelled with results,
// and cancelled before any result (not kept); a data file is named, never
// its path.
func TestRunHistoryStatuses(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	files := secretFolder()
	files["sec/block.json"] = httpFile("req_block", "Block", "GET", "/block", "")
	f := newFixture(t, srv.URL, files)
	f.withHistory(runstore.Limits{})

	stopped := f.start(t, RunInputDTO{FolderID: "fld_sec", Iterations: 3, StopOnFailure: true, RequestIDs: []string{"req_ok", "req_token", "req_block"}})
	f.rec.wait(t, stopped)

	data := f.writeData(t, "rows.csv", "n\n1\n2\n")
	f.pickFrom(data)
	if _, err := f.svc.PickDataFile(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	cancelled := f.start(t, RunInputDTO{FolderID: "fld_sec", RequestIDs: []string{"req_ok", "req_block"}, DataFile: data})
	f.rec.waitResults(t, cancelled, 1)
	<-srv.blocked
	f.ops.Cancel(cancelled)
	f.rec.wait(t, cancelled)

	none := f.start(t, RunInputDTO{FolderID: "fld_sec", Iterations: 1, RequestIDs: []string{"req_block"}})
	<-srv.blocked
	f.ops.Cancel(none)
	f.rec.wait(t, none)

	runs, err := f.svc.Runs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range runs {
		got = append(got, r.ID+" "+r.Status)
	}
	if !slices.Equal(got, []string{cancelled + " cancelled", stopped + " stopped"}) {
		t.Fatalf("runs = %v (none = %s)", got, none)
	}
	if c := runs[0]; c.DataFile != "rows.csv" || c.DataRows != 2 || c.Iterations != 2 || c.Results != 1 || c.Total != 4 {
		t.Errorf("cancelled = %+v", c)
	}
	if s := runs[1]; s.Results != 2 || s.Failed != 1 || !s.StopOnFailure {
		t.Errorf("stopped = %+v", s)
	}
	b, err := os.ReadFile(filepath.Join(f.runsDir(t), cancelled+".json"))
	if err != nil || strings.Contains(string(b), filepath.Dir(data)) {
		t.Errorf("the data file's path is in the record (%v)", err)
	}
}

func TestRunHistoryRetention(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	f := newFixture(t, srv.URL, secretFolder())
	f.withHistory(runstore.Limits{MaxRuns: 2})
	var ids []string
	for range 3 {
		id := f.start(t, RunInputDTO{FolderID: "fld_sec", Iterations: 1})
		f.rec.wait(t, id)
		ids = append(ids, id)
		time.Sleep(2 * time.Millisecond) // distinct end times
	}
	runs, err := f.svc.Runs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != ids[2] || runs[1].ID != ids[1] {
		t.Fatalf("runs = %+v, want the two newest of %v", runs, ids)
	}
	if err := f.svc.ClearRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	if runs, _ := f.svc.Runs(t.Context()); len(runs) != 0 {
		t.Errorf("after clear: %+v", runs)
	}
}

// TestExportRun (RUN-08): JUnit XML, JSON and HTML reports, secrets
// masked, written where the dialog says.
func TestExportRun(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	f := newFixture(t, srv.URL, secretFolder())
	sv := f.withHistory(runstore.Limits{})
	sv.dir = t.TempDir()
	id := f.start(t, RunInputDTO{FolderID: "fld_sec", Iterations: 2})
	f.rec.wait(t, id)

	for _, format := range []string{FormatJUnit, FormatJSON, FormatHTML} {
		res, err := f.svc.ExportRun(t.Context(), ExportRunInputDTO{RunID: id, Format: format})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		b, err := os.ReadFile(res.Path)
		if err != nil || !res.Saved || res.Size != int64(len(b)) || filepath.Dir(res.Path) != sv.dir {
			t.Fatalf("%s: %+v, %v", format, res, err)
		}
		if strings.Contains(string(b), runSecret) {
			t.Errorf("%s report holds the secret", format)
		}
		switch format {
		case FormatJUnit:
			var doc struct {
				XMLName  xml.Name `xml:"testsuites"`
				Tests    int      `xml:"tests,attr"`
				Failures int      `xml:"failures,attr"`
				Skipped  int      `xml:"skipped,attr"`
				Suites   []struct {
					Name  string `xml:"name,attr"`
					Cases []struct {
						Name      string `xml:"name,attr"`
						Classname string `xml:"classname,attr"`
					} `xml:"testcase"`
				} `xml:"testsuite"`
			}
			if err := xml.Unmarshal(b, &doc); err != nil {
				t.Fatalf("JUnit: %v", err)
			}
			if doc.Tests != 6 || doc.Failures != 2 || doc.Skipped != 2 || len(doc.Suites) != 2 || doc.Suites[1].Name != "Secure API, iteration 2" ||
				doc.Suites[0].Cases[0].Name != "Token (iteration 1)" || doc.Suites[0].Cases[0].Classname != "Secure API" {
				t.Errorf("JUnit = %+v", doc)
			}
			if !strings.HasSuffix(res.Path, ".xml") || !strings.HasPrefix(filepath.Base(res.Path), "Secure-API-run-") {
				t.Errorf("file name = %s", res.Path)
			}
		case FormatJSON:
			var r report.Run
			if err := json.Unmarshal(b, &r); err != nil || r.ID != id || len(r.Results) != 6 || r.Schema != report.Schema {
				t.Errorf("JSON = %+v, %v", r.Summary, err)
			}
		case FormatHTML:
			if !strings.Contains(string(b), "<title>Secure API: run report</title>") || strings.Contains(string(b), "<script") {
				t.Errorf("HTML = %.300s", b)
			}
		}
	}
	if len(sv.dialogs) != 3 || !strings.Contains(sv.dialogs[0].Message, "JUnit XML") {
		t.Errorf("dialogs = %+v", sv.dialogs)
	}

	sv.cancel = true
	if res, err := f.svc.ExportRun(t.Context(), ExportRunInputDTO{RunID: id, Format: FormatJSON}); err != nil || res.Saved {
		t.Errorf("cancelled = %+v, %v", res, err)
	}
	_, err := f.svc.ExportRun(t.Context(), ExportRunInputDTO{RunID: id, Format: "pdf"})
	checkCode(t, err, errs.InvalidArgument, CodeInvalidFormat)
	_, err = f.svc.ExportRun(t.Context(), ExportRunInputDTO{RunID: "op_0000000000000000", Format: FormatJSON})
	checkCode(t, err, errs.NotFound, runstore.CodeNotFound)

	sv.cancel = false
	sv.dir = filepath.Join(t.TempDir(), "missing", "folder")
	_, err = f.svc.ExportRun(t.Context(), ExportRunInputDTO{RunID: id, Format: FormatHTML})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != CodeSaveFailed {
		t.Errorf("unwritable = %v", err)
	}
}

func TestRunHistoryUnavailable(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "http://localhost:1", secretFolder())
	_, err := f.svc.Runs(t.Context())
	checkCode(t, err, errs.Unsupported, CodeHistoryUnavailable)
	_, err = f.svc.ExportRun(t.Context(), ExportRunInputDTO{RunID: "op_1", Format: FormatJUnit})
	checkCode(t, err, errs.Unsupported, CodeUnavailable)
	WithRunHistory(func() (string, error) { return "", errs.New(errs.NotFound, "No project is open.") }, runstore.Limits{})(f.svc)
	_, err = f.svc.Runs(t.Context())
	checkCode(t, err, errs.Unsupported, CodeHistoryUnavailable)
	// A run without a history still runs.
	id := f.start(t, RunInputDTO{FolderID: "fld_sec", Iterations: 1})
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Errorf("done = %+v", done)
	}
}
