package runner

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	corelogexport "github.com/sorrelfox/postsonde/internal/core/logexport"
	corerunner "github.com/sorrelfox/postsonde/internal/core/runner"
	"github.com/sorrelfox/postsonde/internal/core/runner/report"
	"github.com/sorrelfox/postsonde/internal/core/runner/runstore"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// Reports and run history (RUN-08, RUN-09): every run that listed a
// result is saved, when it ends, in the open project's local directory
// (runstore, <local>/runs/), secrets masked; Runs lists them, Run reopens
// one, ExportRun saves one as JUnit XML, JSON or HTML where the user
// picks. Responses are never kept in the history. See
// docs/runner.md#reports-and-run-history.

// Error codes of reports and the run history.
const (
	// CodeHistoryUnavailable: there is no run history (no project open,
	// or the service was built without one; kind unsupported).
	CodeHistoryUnavailable = "runner.history_unavailable"
	// CodeInvalidFormat: a report format other than "junit", "json" or
	// "html" (kind invalid_argument). Details: format.
	CodeInvalidFormat = "runner.invalid_format"
	// CodeSaveFailed: the report could not be written (kind io,
	// not_found or permission_denied). Details: path.
	CodeSaveFailed = "runner.save_failed"
)

// Report formats of ExportRun.
const (
	FormatJUnit = "junit"
	FormatJSON  = "json"
	FormatHTML  = "html"
)

// Secrets returns the secret values to mask in the run history and
// reports: the open project's and the app's (as copied logs are masked).
// Values may come with an error for the ones that could not be read.
type Secrets func(ctx context.Context) ([]string, error)

// SaveDialog describes the native save dialog of a report.
type SaveDialog struct {
	Title      string
	Message    string
	ButtonText string
	// Filename is the suggested file name.
	Filename string
}

// SavePicker shows the native save dialog and returns the chosen path, or
// "" when the user cancelled.
type SavePicker func(ctx context.Context, d SaveDialog) (string, error)

// WithRunHistory keeps the runs that end in the open project's local
// directory, localDir (workspace.Manager.LocalDir), under runs/.
func WithRunHistory(localDir func() (string, error), limits runstore.Limits) Option {
	return func(s *RunnerService) { s.localDir, s.historyLimits = localDir, limits }
}

// WithSecrets masks the secrets the app keeps in the run history and the
// reports.
func WithSecrets(secrets Secrets) Option {
	return func(s *RunnerService) { s.secrets = secrets }
}

// WithReportDialog lets ExportRun ask where to save a report.
func WithReportDialog(pick SavePicker) Option {
	return func(s *RunnerService) { s.pickSave = pick }
}

// history is the run history of the open project.
func (s *RunnerService) history() (*runstore.Store, error) {
	unavailable := func(err error) error {
		e := errs.New(errs.Unsupported, "The run history is not available.").WithCode(CodeHistoryUnavailable)
		if err != nil {
			e = errs.Wrap(err, errs.Unsupported, "The run history is not available.").WithCode(CodeHistoryUnavailable).
				WithHint("Open a project first.")
		}
		return e
	}
	if s.localDir == nil {
		return nil, unavailable(nil)
	}
	local, err := s.localDir()
	if err != nil {
		return nil, unavailable(err)
	}
	dir := filepath.Join(local, runstore.Dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.stores[dir]
	if !ok {
		st = runstore.Open(dir, s.historyLimits)
		s.stores[dir] = st
	}
	return st, nil
}

// masker returns the function that masks the secrets known now.
func (s *RunnerService) masker(ctx context.Context) func(string) string {
	var secrets []string
	if s.secrets != nil {
		var err error
		secrets, err = s.secrets(context.WithoutCancel(ctx))
		if err != nil {
			s.logger.Warn("some secrets could not be read for masking", "err", err)
		}
	}
	return corelogexport.NewMasker(secrets).Text
}

// Runs returns the past runs of the open project, newest first (RUN-09):
// at most runstore.DefaultMaxRuns, the oldest removed first.
//
// Errors: runner.history_unavailable without an open project.
func (s *RunnerService) Runs(ctx context.Context) ([]RunSummaryDTO, error) {
	st, err := s.history()
	if err != nil {
		return nil, err
	}
	list, err := st.List()
	if err != nil {
		return nil, err
	}
	out := make([]RunSummaryDTO, len(list))
	for i, sum := range list {
		out[i] = toRunSummaryDTO(sum)
	}
	return out, nil
}

// Run returns past run id with its results, as its run listed them
// (hasResponse false: responses are not kept in the history).
//
// Errors: runner.run_not_found, runner.invalid_run_id,
// runner.history_unavailable.
func (s *RunnerService) Run(ctx context.Context, id string) (RunRecordDTO, error) {
	st, err := s.history()
	if err != nil {
		return RunRecordDTO{}, err
	}
	r, err := st.Get(id)
	if err != nil {
		return RunRecordDTO{}, err
	}
	out := RunRecordDTO{Summary: toRunSummaryDTO(r.Summary), Results: make([]RunResultDTO, len(r.Results))}
	for i, res := range r.Results {
		out.Results[i] = fromReportResult(res)
	}
	return out, nil
}

// DeleteRun removes past run id from the history; one no longer there is
// not an error.
func (s *RunnerService) DeleteRun(ctx context.Context, id string) error {
	st, err := s.history()
	if err != nil {
		return err
	}
	return st.Delete(id)
}

// ClearRuns removes every past run of the open project.
func (s *RunnerService) ClearRuns(ctx context.Context) error {
	st, err := s.history()
	if err != nil {
		return err
	}
	return st.Clear()
}

// ExportRun saves past run in.RunID (a run that just ended included: its
// ID is its operation ID) as a report (RUN-08): "junit" (JUnit XML, as CI
// servers read it), "json" (the record) or "html" (a page on its own),
// with the secrets the app keeps masked again, to the path the user picks
// in the native save dialog. Nothing is written when the user cancels.
//
// Errors: runner.invalid_format, runner.run_not_found,
// runner.history_unavailable, runner.save_failed, runner.unavailable
// without a save dialog.
func (s *RunnerService) ExportRun(ctx context.Context, in ExportRunInputDTO) (ExportRunResultDTO, error) {
	var write func(*bytes.Buffer, report.Run) error
	var ext, kind string
	switch in.Format {
	case FormatJUnit:
		write, ext, kind = func(b *bytes.Buffer, r report.Run) error { return report.WriteJUnit(b, r) }, ".xml", "JUnit XML"
	case FormatJSON:
		write, ext, kind = func(b *bytes.Buffer, r report.Run) error { return report.WriteJSON(b, r) }, ".json", "JSON"
	case FormatHTML:
		write, ext, kind = func(b *bytes.Buffer, r report.Run) error { return report.WriteHTML(b, r) }, ".html", "HTML"
	default:
		return ExportRunResultDTO{}, errs.New(errs.InvalidArgument, "A run report is JUnit XML, JSON or HTML.").
			WithCode(CodeInvalidFormat).WithDetail("format", in.Format)
	}
	if s.pickSave == nil {
		return ExportRunResultDTO{}, unavailable()
	}
	st, err := s.history()
	if err != nil {
		return ExportRunResultDTO{}, err
	}
	r, err := st.Get(in.RunID)
	if err != nil {
		return ExportRunResultDTO{}, err
	}
	var buf bytes.Buffer
	if err := write(&buf, r.Masked(s.masker(ctx))); err != nil {
		return ExportRunResultDTO{}, errs.Wrap(err, errs.Internal, "The report could not be written.")
	}
	path, err := s.pickSave(ctx, SaveDialog{
		Title:      "Save run report",
		Message:    "Save the run of " + r.Title() + " as " + kind + ", secrets masked.",
		ButtonText: "Save",
		Filename:   reportFileName(r.Summary, ext),
	})
	if err != nil {
		return ExportRunResultDTO{}, errs.Wrap(err, errs.Internal, "The save dialog could not be shown.")
	}
	if path == "" {
		return ExportRunResultDTO{}, nil
	}
	if err := atomicfile.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		kind := errs.KindOf(err)
		if kind == errs.Internal {
			kind = errs.IO
		}
		return ExportRunResultDTO{}, errs.Wrap(err, kind, "The report could not be saved to "+path+".").
			WithCode(CodeSaveFailed).WithDetail("path", path).
			WithHint("Check that the folder exists and can be written to, and that the disk is not full.")
	}
	s.logger.Info("run report saved", "format", in.Format, "bytes", buf.Len())
	return ExportRunResultDTO{Saved: true, Path: path, Size: int64(buf.Len())}, nil
}

// reportFileName is "<folder>-run-<date>-<time><ext>", the folder's name
// reduced to letters, digits, "-" and "_".
func reportFileName(sum report.Summary, ext string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
			return r
		case unicode.IsSpace(r), r == '.', r == '/':
			return '-'
		}
		return -1
	}, sum.Title())
	name = strings.Trim(name, "-")
	if name == "" {
		name = "postsonde"
	}
	when := time.UnixMilli(sum.StartedAt).Format("20060102-150405")
	return name + "-run-" + when + ext
}

// record is what a run keeps for its record in the history.
type record struct {
	builder *report.Builder
	store   *runstore.Store
}

// newRecord starts the record of a run of plan with opts, in the open
// project's history; nil without one.
func (s *RunnerService) newRecord(plan corerunner.Plan, opts corerunner.Options, data *corerunner.Data, dataPath string) *record {
	if s.localDir == nil {
		return nil
	}
	st, err := s.history()
	if err != nil {
		s.logger.Warn("the run will not be kept in the run history", "err", err)
		return nil
	}
	sum := report.Summary{
		Folder: plan.Folder, Project: plan.Project,
		StartedAt:  time.Now().UnixMilli(),
		Iterations: opts.Iterations, Requests: len(plan.Items), Total: opts.Iterations * len(plan.Items),
		DelayMs: opts.Delay.Milliseconds(), StopOnFailure: opts.StopOnFailure,
	}
	if data != nil {
		sum.DataFile, sum.DataRows = filepath.Base(dataPath), len(data.Rows)
	}
	return &record{builder: report.NewBuilder(sum, 0), store: st}
}

// save masks the record and saves it as run id, when it has a result.
func (r *run) save(ctx context.Context, id, status string) {
	rec := r.record
	if rec == nil || rec.builder.Len() == 0 {
		return
	}
	full := rec.builder.Finish(status, time.Now().UnixMilli())
	full.ID, full.FolderID = id, r.folderID
	if err := rec.store.Save(full.Masked(r.s.masker(ctx))); err != nil {
		r.s.logger.Warn("the run could not be saved in the run history", "opId", id, "err", err)
	}
}

// toReportResult is res as the record keeps it.
func toReportResult(res RunResultDTO) report.Result {
	out := report.Result{
		Iteration: res.Iteration, Index: res.Index, Position: res.Position, RequestID: res.RequestID, Name: res.Name,
		Kind: res.Kind, Method: res.Method, Path: res.Path, Skip: res.Skip, Outcome: res.Outcome,
		StartedAt: res.StartedAt, DurationMs: res.DurationMs, StatusCode: res.StatusCode, Status: res.Status, Size: res.Size,
		TestsPassed: res.TestsPassed, TestsFailed: res.TestsFailed, Tests: make([]report.Test, len(res.Tests)), Events: res.Events,
	}
	for i, t := range res.Tests {
		out.Tests[i] = report.Test{Index: t.Index, Target: t.Target, Expr: t.Expr, Op: t.Op, Expected: t.Expected,
			Outcome: t.Outcome, Actual: t.Actual, Exists: t.Exists, Message: t.Message, Code: t.Code}
	}
	if res.Error != nil {
		out.Error = &report.Error{Kind: res.Error.Kind, Code: res.Error.Code, Message: res.Error.Message, Hint: res.Error.Hint}
	}
	return out
}

// fromReportResult is a kept result as a run lists it, without a response.
func fromReportResult(r report.Result) RunResultDTO {
	path := r.Path
	if path == nil {
		path = []string{}
	}
	out := RunResultDTO{
		Iteration: r.Iteration, Index: r.Index, Position: r.Position, RequestID: r.RequestID, Name: r.Name,
		Kind: r.Kind, Method: r.Method, Path: path, Skip: r.Skip, Outcome: r.Outcome,
		StartedAt: r.StartedAt, DurationMs: r.DurationMs, StatusCode: r.StatusCode, Status: r.Status, Size: r.Size,
		TestsPassed: r.TestsPassed, TestsFailed: r.TestsFailed, Tests: make([]httpservice.TestResultDTO, len(r.Tests)), Events: r.Events,
	}
	for i, t := range r.Tests {
		out.Tests[i] = httpservice.TestResultDTO{Index: t.Index, Target: t.Target, Expr: t.Expr, Op: t.Op, Expected: t.Expected,
			Outcome: t.Outcome, Actual: t.Actual, Exists: t.Exists, Message: t.Message, Code: t.Code, SchemaErrors: []httpservice.SchemaErrorDTO{}}
	}
	if r.Error != nil {
		out.Error = &apperr.ErrorDTO{Kind: r.Error.Kind, Code: r.Error.Code, Message: r.Error.Message, Hint: r.Error.Hint,
			Details: map[string]string{}, Causes: []string{}}
	}
	return out
}

func toRunSummaryDTO(s report.Summary) RunSummaryDTO {
	return RunSummaryDTO{
		ID: s.ID, FolderID: s.FolderID, Folder: s.Folder, Project: s.Project,
		StartedAt: s.StartedAt, EndedAt: s.EndedAt, ElapsedMs: s.ElapsedMs, Status: s.Status,
		Iterations: s.Iterations, Requests: s.Requests, Total: s.Total, DelayMs: s.DelayMs, StopOnFailure: s.StopOnFailure,
		DataFile: s.DataFile, DataRows: s.DataRows,
		Results: s.Counts.Results, Passed: s.Counts.Passed, Failed: s.Counts.Failed, Errors: s.Counts.Errors, Skipped: s.Counts.Skipped,
		TotalMs: s.TotalMs, Omitted: s.Omitted,
	}
}
