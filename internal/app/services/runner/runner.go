// Package runner is the bound service of the folder runner (RUN-01 to
// RUN-04, RUN-06, RUN-07): Start sends the requests of a folder, and of
// its subfolders (the chosen ones, in the chosen order), for a number of
// iterations with a delay between the requests, stopping at the first
// failure if asked, as one operation of the runner domain (docs/events.md)
// whose items are the result of each request of each iteration,
// assertions included. Each request is built and sent by the HTTP service
// as Send does it (internal/app/services/http, RunSession); SSE requests
// are read for a while (sse.Probe); other kinds are listed as skipped.
// OpsService.Cancel stops a run; Response returns the kept response of a
// result. See docs/runner.md.
package runner

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/app/operation"
	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	"github.com/sorrelfox/postsonde/internal/app/services/sse"
	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	corerunner "github.com/sorrelfox/postsonde/internal/core/runner"
	"github.com/sorrelfox/postsonde/internal/core/runner/runstore"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Domain names the runner service and its operations.
const Domain = "runner"

// Error codes of the service.
const (
	// CodeUnavailable: the service was built without what running needs
	// (kind unsupported).
	CodeUnavailable = "runner.unavailable"
	// CodeEmpty: the folder has no requests (kind invalid_argument).
	// Details: folderId.
	CodeEmpty = "runner.empty"
)

// Progress messages of a run, in the progress events' message: the stage
// of the step the run is at. The UI compares them; it words them itself.
const (
	// StageSending: a request is being sent.
	StageSending = "Sending"
	// StageDelay: the run waits the delay before the next request.
	StageDelay = "Waiting before the next request"
	// StageStopped: the run stopped at a failure (StopOnFailure); the
	// last progress event of such a run, before its done event.
	StageStopped = "Stopped at a failure"
)

// maxItems is the most results in one runner:op:data event.
const maxItems = 64

// Planner returns the plan of a run of folder id ("" for the project):
// collection.RunPlan.
type Planner func(ctx context.Context, id string) (corerunner.Plan, error)

// RunnerService runs folders for the UI.
type RunnerService struct {
	plan   Planner
	http   *httpservice.HTTPService
	ops    *operation.Registry
	logger *slog.Logger
	// sleep waits the delay between requests; tests replace it.
	sleep corerunner.SleepFunc
	// responses keeps the responses of the runs for Response (RUN-07).
	responses *responseStore
	// pickData shows the open dialog of a data file (RUN-05); nil for
	// none.
	pickData FilePicker

	// localDir is the open project's local directory, where the run
	// history is (RUN-09); nil for none. secrets masks the history and
	// reports; pickSave asks where a report goes (RUN-08).
	localDir      func() (string, error)
	historyLimits runstore.Limits
	secrets       Secrets
	pickSave      SavePicker

	mu sync.Mutex
	// picked are the data files the user chose in this session: the only
	// ones a run reads.
	picked map[string]bool
	// stores are the run histories by directory, one per project.
	stores map[string]*runstore.Store
}

// Option configures a RunnerService.
type Option func(*RunnerService)

// New returns a RunnerService that takes plans from plan, sends through
// http and runs as operations in ops. A nil plan, http or ops makes Start
// fail; a nil logger means slog.Default().
func New(plan Planner, http *httpservice.HTTPService, ops *operation.Registry, logger *slog.Logger, opts ...Option) *RunnerService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &RunnerService{plan: plan, http: http, ops: ops, logger: logger.With("service", Domain), sleep: corerunner.Sleep,
		responses: newResponseStore(MaxKeptResponses, MaxKeptResponseBytes), picked: map[string]bool{}, stores: map[string]*runstore.Store{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Response returns the response of result index of run opID (RUN-07): the
// response of an HTTP or GraphQL request of the run, as Send returns it
// (the request as sent is in its timeline), without a kept body for
// saving or paging. It fails with CodeResponseGone when the result has no
// response (skipped, an error, an SSE request) or it is no longer kept:
// the runner keeps the last MaxKeptResponses responses, about
// MaxKeptResponseBytes, over every run, until Release.
func (s *RunnerService) Response(ctx context.Context, opID string, index int) (httpservice.ResponseDTO, error) {
	if resp, ok := s.responses.get(opID, index); ok {
		return resp, nil
	}
	return httpservice.ResponseDTO{}, errs.New(errs.NotFound, "The response of this result is no longer kept.").
		WithCode(CodeResponseGone).
		WithHint("Runs keep the responses of their last 1,000 requests while their results are shown. Run the folder again to see it.").
		WithDetail("opId", opID).
		WithDetail("index", strconv.Itoa(index))
}

// Release drops the kept responses of run opID: the UI no longer shows
// its results. A running run keeps none after it. Unknown IDs are
// ignored.
func (s *RunnerService) Release(ctx context.Context, opID string) {
	s.responses.release(opID)
}

// ServiceName names the service in Wails logs.
func (s *RunnerService) ServiceName() string { return Domain }

// Plan returns what a run of folder id sends ("" for the whole project):
// its requests in order, each with why it is skipped when it is. It fails
// with collection.not_found and collection.not_folder.
func (s *RunnerService) Plan(ctx context.Context, id string) (PlanDTO, error) {
	if s.plan == nil {
		return PlanDTO{}, unavailable()
	}
	p, err := s.plan(ctx, id)
	if err != nil {
		return PlanDTO{}, err
	}
	return toPlanDTO(p), nil
}

// Start starts a run of folder in.FolderID and returns its operation ID at
// once (RUN-01, RUN-03). Every iteration sends the folder's requests in
// the order of the tree, subfolders at their place, each built as Send
// builds it when its turn comes (edits and environment changes made
// during the run are picked up) with the run's variables above every
// other scope, and waits in.DelayMs between two requests. Each request's
// outcome is one runner:op:data item (RunResultDTO), in order: a request
// that cannot be built or sent is listed with its error and the run goes
// on, and so is a failed assertion. Progress is {done: results listed,
// total: iterations × requests, message: StageSending or StageDelay}.
// OpsService.Cancel stops the run at once, also during a request (which
// is dropped, not listed) or a delay; the done event is then "cancelled".
// A run that got through is "ok".
//
// in.RequestIDs, when set, chooses the requests of the plan the run sends
// and their order (RUN-02); in.StopOnFailure ends the run, "ok", after the
// first result that failed or is an error, with a last progress event
// saying StageStopped (RUN-04).
//
// in.DataFile, when set, is a data file chosen with PickDataFile (RUN-05):
// it is read again, the run makes one iteration per row (in.Iterations is
// not used) and each iteration starts by setting its row's values as run
// variables (Vars.SetRow).
//
// The list of requests is taken when the run starts. A run adds one entry
// per request sent to the request history (docs/history.md). A run that
// listed a result is saved in the run history when it ends (RUN-09), its
// ID the operation ID, before the done event.
//
// It fails at once, sending nothing, with runner.invalid_options for
// iterations, a delay or a stream window out of range, the errors of
// PreviewDataFile for the data file, CodeEmpty for a
// folder without requests or an empty in.RequestIDs,
// runner.invalid_selection for an ID of in.RequestIDs not in the plan or
// named twice, the errors of Plan, and template.prompt when
// the requests have {{$prompt}} references in.Prompts does not answer:
// details.prompts lists them all, so the UI asks once and starts again
// with the answers.
func (s *RunnerService) Start(ctx context.Context, in RunInputDTO) (operation.StartedDTO, error) {
	if err := ctx.Err(); err != nil {
		return operation.StartedDTO{}, errs.Wrap(err, errs.KindOf(err), "Starting the run was interrupted.")
	}
	if s.plan == nil || s.http == nil || s.ops == nil {
		return operation.StartedDTO{}, unavailable()
	}
	opts := corerunner.Options{
		Iterations:    in.Iterations,
		Delay:         millis(in.DelayMs, corerunner.MaxDelay),
		StreamWindow:  millis(in.StreamMs, corerunner.MaxStreamWindow),
		StopOnFailure: in.StopOnFailure,
	}
	var data *corerunner.Data
	if in.DataFile != "" {
		d, err := s.readData(in.DataFile, in.DataDelimiter)
		if err != nil {
			return operation.StartedDTO{}, err
		}
		data, opts.Iterations = &d, len(d.Rows)
	}
	if err := opts.Validate(); err != nil {
		return operation.StartedDTO{}, err
	}
	plan, err := s.plan(ctx, in.FolderID)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	if len(plan.Items) == 0 {
		return operation.StartedDTO{}, errs.New(errs.InvalidArgument, "The folder has no requests to run.").
			WithCode(CodeEmpty).
			WithDetail("folderId", in.FolderID)
	}
	if in.RequestIDs != nil {
		if plan, err = plan.Select(in.RequestIDs); err != nil {
			return operation.StartedDTO{}, err
		}
		if len(plan.Items) == 0 {
			return operation.StartedDTO{}, errs.New(errs.InvalidArgument, "No requests are chosen for the run.").
				WithCode(CodeEmpty).
				WithHint("Choose at least one request to run.").
				WithDetail("folderId", in.FolderID)
		}
	}
	vars := corerunner.NewVars()
	r := &run{
		s: s, plan: plan, opts: opts, vars: vars, data: data, folderID: in.FolderID,
		record:  s.newRecord(plan, opts, data, in.DataFile),
		send:    httpservice.RunOptions{AllowUnresolved: in.AllowUnresolved, Prompts: in.Prompts, Vars: vars},
		session: httpservice.NewRunSession(s.http),
		kept:    s.responses.begin(),
	}
	if err := r.session.MissingPrompts(ctx, r.sentIDs(), r.send); err != nil {
		return operation.StartedDTO{}, err
	}
	started, err := operation.Start(s.ops, operation.Spec{Domain: Domain, Kind: "run", MaxItems: maxItems}, r.run)
	if err != nil {
		return operation.StartedDTO{}, errs.Wrap(err, errs.Internal, "The run could not be started.")
	}
	// Before the ID reaches the UI, which asks for responses by it.
	s.responses.name(r.kept, started.OpID)
	s.logger.Debug("folder run started", "opId", started.OpID, "folderId", in.FolderID,
		"requests", len(plan.Items), "iterations", opts.Iterations, "delayMs", in.DelayMs, "dataFile", data != nil)
	return started, nil
}

// millis is ms milliseconds, or a duration just out of [0, limit] when ms
// is out of it, so that a huge value cannot overflow into range.
func millis(ms int64, limit time.Duration) time.Duration {
	switch {
	case ms < 0:
		return -1
	case ms > int64(limit/time.Millisecond):
		return limit + 1
	}
	return time.Duration(ms) * time.Millisecond
}

func unavailable() error {
	return errs.New(errs.Unsupported, "Running folders is not available.").WithCode(CodeUnavailable)
}

// run is one running run.
type run struct {
	s    *RunnerService
	plan corerunner.Plan
	opts corerunner.Options
	send httpservice.RunOptions
	// vars are the run's variables (send.Vars); data the rows of its data
	// file, one per iteration, nil for none (RUN-05).
	vars    *corerunner.Vars
	data    *corerunner.Data
	session *httpservice.RunSession
	// kept keeps the responses of the run's requests (RUN-07).
	kept *keptRun
	// folderID is the folder run; record its record for the run history
	// (RUN-09), nil for none.
	folderID string
	record   *record
}

// sentIDs are the HTTP and GraphQL requests of the plan, which may ask
// for {{$prompt}} values (streams never ask).
func (r *run) sentIDs() []string {
	var ids []string
	for _, it := range r.plan.Items {
		if it.Skip == corerunner.Runnable && it.Kind != string(projectfile.KindSSE) {
			ids = append(ids, it.ID)
		}
	}
	return ids
}

// run is the operation: every step of the plan, in order, then the
// record of the run saved.
func (r *run) run(ctx context.Context, emit *operation.Emitter[RunResultDTO]) error {
	defer r.session.Close()
	defer r.s.responses.finish(r.kept)
	status, err := r.steps(ctx, emit)
	if ctx.Err() != nil {
		status = "cancelled"
	}
	op, _ := netx.UserOpFrom(ctx)
	r.save(ctx, op.ID, status)
	return err
}

// steps runs every step of the plan, in order, and says how the run ended:
// "ok" or "stopped" (at a failure).
func (r *run) steps(ctx context.Context, emit *operation.Emitter[RunResultDTO]) (string, error) {
	total := int64(r.opts.Iterations * len(r.plan.Items))
	pacer := corerunner.NewPacer(r.opts.Delay, r.s.sleep)
	for st := range r.plan.Steps(r.opts.Iterations) {
		if r.data != nil && st.Position == 1 {
			// Each iteration starts with its row's values (RUN-05).
			r.vars.SetRow(r.data.Rows[st.Iteration-1])
		}
		done := int64(st.Index - 1)
		res := newResult(st)
		if st.Item.Skip == corerunner.Runnable {
			if pacer.Waits() {
				_ = emit.Progress(done, total, StageDelay)
			}
			if pacer.Wait(ctx) != nil {
				return "cancelled", nil // stopped: the done event says "cancelled"
			}
			_ = emit.Progress(done, total, StageSending)
			if st.Item.Kind == string(projectfile.KindSSE) {
				r.stream(ctx, st, &res)
			} else {
				r.request(ctx, st, &res)
			}
			if ctx.Err() != nil {
				return "cancelled", nil // cut short by the stop: not listed
			}
		}
		if err := emit.Send(res); err != nil {
			return "cancelled", err
		}
		if r.record != nil {
			r.record.builder.Add(toReportResult(res))
		}
		if r.opts.Stops(res.Outcome == OutcomeFailed || res.Outcome == OutcomeError) {
			// Bail (RUN-04): nothing after the failure is sent.
			_ = emit.Progress(int64(st.Index), total, StageStopped)
			return "stopped", nil
		}
	}
	_ = emit.Progress(total, total, "")
	return "ok", nil
}

// newResult is the result of step st before it runs: a skipped item's
// whole result.
func newResult(st corerunner.Step) RunResultDTO {
	it := toItemDTO(st.Position, st.Item)
	res := RunResultDTO{
		Iteration: st.Iteration, Index: st.Index,
		Position: it.Position, RequestID: it.RequestID, Name: it.Name, Kind: it.Kind, Method: it.Method, Path: it.Path, Skip: it.Skip,
		Tests: []httpservice.TestResultDTO{}, Extractions: []httpservice.ExtractionResultDTO{},
	}
	if st.Item.Skip != corerunner.Runnable {
		res.Outcome = OutcomeSkipped
	}
	return res
}

// request sends an HTTP or GraphQL request into res.
func (r *run) request(ctx context.Context, st corerunner.Step, res *RunResultDTO) {
	out := r.session.Send(ctx, st.Item.ID, r.send, nil)
	res.StartedAt, res.DurationMs = out.Started.UnixMilli(), out.Duration.Milliseconds()
	if out.Response == nil {
		res.Outcome, res.Error = OutcomeError, out.Error
		return
	}
	resp := out.Response
	if ctx.Err() == nil {
		// A send cut short by the stop is not listed: its response is not kept.
		r.s.responses.put(r.kept, st.Index, *resp)
		res.HasResponse = true
	}
	res.DurationMs, res.StatusCode, res.Status, res.Size = resp.DurationMs, resp.StatusCode, resp.Status, resp.Size
	if resp.Tests != nil {
		res.Tests = resp.Tests
	}
	for _, t := range res.Tests {
		if t.Outcome == "passed" {
			res.TestsPassed++
		} else {
			res.TestsFailed++
		}
	}
	res.Outcome = OutcomePassed
	if res.TestsFailed > 0 {
		res.Outcome = OutcomeFailed
	}
	// A value the next requests need was not set (TEST-05): the result
	// fails, and a run that stops on failure stops here.
	if resp.Extractions != nil {
		res.Extractions = resp.Extractions
	}
	for _, e := range res.Extractions {
		if e.Outcome == httpservice.ExtractionFailed || e.Outcome == httpservice.ExtractionInvalid {
			res.Outcome = OutcomeFailed
		}
	}
}

// stream reads an SSE request for the run's stream window into res.
func (r *run) stream(ctx context.Context, st corerunner.Step, res *RunResultDTO) {
	start := time.Now()
	res.StartedAt = start.UnixMilli()
	fail := func(err error) {
		dto := apperr.ToDTO(err)
		res.Outcome, res.Error, res.DurationMs = OutcomeError, &dto, time.Since(start).Milliseconds()
	}
	stream, err := r.session.Stream(ctx, st.Item.ID, r.send)
	if err != nil {
		fail(err)
		return
	}
	log := streamcheck.NewLog(start)
	p, err := sse.ProbeInto(ctx, stream, r.opts.Window(), log)
	res.StatusCode, res.Status, res.Size, res.Events = p.StatusCode, p.Status, p.Bytes, p.Events
	if err != nil {
		fail(err)
		return
	}
	res.DurationMs = p.Duration.Milliseconds()
	res.Outcome = OutcomePassed
	if p.StatusCode < http.StatusOK || p.StatusCode >= http.StatusMultipleChoices {
		res.Outcome = OutcomeFailed
	}
	// The request's assertions, checked against the events read in the
	// window (TEST-08), with the run's variables.
	for _, t := range stream.Checks.Run(ctx, log.Stream(), stream.Mask) {
		res.Tests = append(res.Tests, streamTest(t))
		if t.Outcome == "passed" {
			res.TestsPassed++
		} else {
			res.TestsFailed++
		}
	}
	if res.TestsFailed > 0 {
		res.Outcome = OutcomeFailed
	}
}

// streamTest is a stream's test result as a run lists it, with the
// message scope in the expression ("last: $.done") so reports say it.
func streamTest(t streamcheck.TestResultDTO) httpservice.TestResultDTO {
	errors := make([]httpservice.SchemaErrorDTO, len(t.SchemaErrors))
	for i, e := range t.SchemaErrors {
		errors[i] = httpservice.SchemaErrorDTO{Path: e.Path, InstancePath: e.InstancePath, SchemaPath: e.SchemaPath, Keyword: e.Keyword, Message: e.Message}
	}
	expr := t.Expr
	if t.Messages != "" {
		scope := t.Messages
		if scope == "nth" {
			scope = "message " + strconv.Itoa(t.MessageIndex)
		}
		expr = strings.TrimSpace(scope + ": " + expr)
	}
	return httpservice.TestResultDTO{
		Index: t.Index, Target: t.Target, Expr: expr, Op: t.Op, Expected: t.Expected,
		Outcome: t.Outcome, Actual: t.Actual, Exists: t.Exists, Message: t.Message, Code: t.Code, SchemaErrors: errors,
	}
}
