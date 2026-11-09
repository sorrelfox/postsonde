// Package report is the record of a folder run that ended (RUN-08,
// RUN-09): its summary and the result of each request of each iteration,
// as kept in the run history (package runstore) and exported as JUnit XML,
// JSON or HTML. It holds no responses. Standard library only. See
// docs/runner.md#reports-and-run-history.
package report

import (
	"slices"
	"unicode/utf8"
)

// Schema names the format of a record, in its "schema" member.
const Schema = "postsonde.run/1"

// Statuses of a run that ended.
const (
	// StatusOK: the run got through every step.
	StatusOK = "ok"
	// StatusStopped: the run stopped at a failure (stop on failure).
	StatusStopped = "stopped"
	// StatusCancelled: the run was stopped by the user.
	StatusCancelled = "cancelled"
)

// Outcomes of a result, as the runner's.
const (
	OutcomePassed  = "passed"
	OutcomeFailed  = "failed"
	OutcomeError   = "error"
	OutcomeSkipped = "skipped"
)

// Limits of a record.
const (
	// MaxResults is the most results a record keeps: the first ones. The
	// counts cover every result.
	MaxResults = 20_000
	// MaxText is the longest text of a result kept (a message, a status,
	// an assertion's values), in bytes; longer ones are cut with "…".
	MaxText = 4 << 10
)

// Counts are the results of a run by outcome.
type Counts struct {
	Results int `json:"results"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Errors  int `json:"errors"`
	Skipped int `json:"skipped"`
}

// Summary is what a run was and how it ended: the run history lists
// summaries.
type Summary struct {
	Schema string `json:"schema"`
	// ID is the run's operation ID.
	ID string `json:"id"`
	// FolderID is the folder run ("" for the whole project); Folder its
	// name, Project the project's.
	FolderID string `json:"folderId"`
	Folder   string `json:"folder"`
	Project  string `json:"project"`
	// StartedAt and EndedAt are Unix milliseconds; ElapsedMs is between.
	StartedAt int64 `json:"startedAt"`
	EndedAt   int64 `json:"endedAt"`
	ElapsedMs int64 `json:"elapsedMs"`
	// Status is StatusOK, StatusStopped or StatusCancelled.
	Status string `json:"status"`
	// Iterations and Requests (items per iteration) are the run's size;
	// Total is Iterations × Requests, the results it would have had.
	Iterations int `json:"iterations"`
	Requests   int `json:"requests"`
	Total      int `json:"total"`
	// DelayMs and StopOnFailure are the run's options.
	DelayMs       int64 `json:"delayMs"`
	StopOnFailure bool  `json:"stopOnFailure"`
	// DataFile is the base name of the data file (RUN-05), never its
	// path; "" for none. DataRows is its rows.
	DataFile string `json:"dataFile"`
	DataRows int    `json:"dataRows"`
	// Counts are the results by outcome, all of them.
	Counts Counts `json:"counts"`
	// TotalMs is the time of the requests sent (every result but the
	// skipped ones).
	TotalMs int64 `json:"totalMs"`
	// Omitted is the results not kept (over MaxResults).
	Omitted int `json:"omitted"`
}

// Test is the result of one assertion, as the response's Tests tab has it.
type Test struct {
	Index    int    `json:"index"`
	Target   string `json:"target"`
	Expr     string `json:"expr"`
	Op       string `json:"op"`
	Expected string `json:"expected"`
	// Outcome is "passed", "failed" or "invalid".
	Outcome string `json:"outcome"`
	Actual  string `json:"actual"`
	Exists  bool   `json:"exists"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

// Error is why a request could not be built or sent.
type Error struct {
	Kind    string `json:"kind"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

// Result is one request of one iteration.
type Result struct {
	Iteration  int      `json:"iteration"`
	Index      int      `json:"index"`
	Position   int      `json:"position"`
	RequestID  string   `json:"requestId"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Method     string   `json:"method"`
	Path       []string `json:"path"`
	Skip       string   `json:"skip"`
	Outcome    string   `json:"outcome"`
	StartedAt  int64    `json:"startedAt"`
	DurationMs int64    `json:"durationMs"`
	StatusCode int      `json:"statusCode"`
	Status     string   `json:"status"`
	Size       int64    `json:"size"`
	// TestsPassed and TestsFailed count the assertions; Tests has each.
	TestsPassed int    `json:"testsPassed"`
	TestsFailed int    `json:"testsFailed"`
	Tests       []Test `json:"tests"`
	// Events is the number of events of an SSE request.
	Events int    `json:"events"`
	Error  *Error `json:"error"`
}

// Run is a whole record: the summary and the results kept, in the order
// sent.
type Run struct {
	Summary
	Results []Result `json:"results"`
}

// Builder collects the results of a run as they come.
type Builder struct {
	run Run
	max int
}

// NewBuilder starts the record of a run whose summary is s (without its
// counts, status and end), keeping at most maxResults results (0:
// MaxResults).
func NewBuilder(s Summary, maxResults int) *Builder {
	if maxResults <= 0 {
		maxResults = MaxResults
	}
	s.Schema = Schema
	return &Builder{run: Run{Summary: s, Results: []Result{}}, max: maxResults}
}

// Add counts r and keeps it while there is room, its texts cut to MaxText.
func (b *Builder) Add(r Result) {
	c := &b.run.Counts
	c.Results++
	switch r.Outcome {
	case OutcomePassed:
		c.Passed++
	case OutcomeFailed:
		c.Failed++
	case OutcomeError:
		c.Errors++
	case OutcomeSkipped:
		c.Skipped++
	}
	if r.Outcome != OutcomeSkipped {
		b.run.TotalMs += r.DurationMs
	}
	if len(b.run.Results) >= b.max {
		b.run.Omitted++
		return
	}
	b.run.Results = append(b.run.Results, r.mapText(clip))
}

// Len is the number of results added.
func (b *Builder) Len() int { return b.run.Counts.Results }

// Finish ends the record: status and end time (Unix milliseconds).
func (b *Builder) Finish(status string, endedAt int64) Run {
	b.run.Status = status
	b.run.EndedAt = endedAt
	b.run.ElapsedMs = max(0, endedAt-b.run.StartedAt)
	return b.run
}

// Masked returns r with mask applied to every text that may hold a
// secret: names, paths, the data file, statuses, assertions and errors.
// IDs, kinds, outcomes and codes are kept.
func (r Run) Masked(mask func(string) string) Run {
	out := r
	out.Folder, out.Project, out.DataFile = mask(r.Folder), mask(r.Project), mask(r.DataFile)
	out.Results = make([]Result, len(r.Results))
	for i, res := range r.Results {
		out.Results[i] = res.mapText(mask)
	}
	return out
}

// mapText returns a copy of r with f applied to its texts.
func (r Result) mapText(f func(string) string) Result {
	out := r
	out.Name, out.Status = f(r.Name), f(r.Status)
	out.Path = make([]string, len(r.Path))
	for i, p := range r.Path {
		out.Path[i] = f(p)
	}
	out.Tests = make([]Test, len(r.Tests))
	for i, t := range r.Tests {
		t.Target, t.Expr, t.Expected, t.Actual, t.Message = f(t.Target), f(t.Expr), f(t.Expected), f(t.Actual), f(t.Message)
		out.Tests[i] = t
	}
	if r.Error != nil {
		e := *r.Error
		e.Message, e.Hint = f(e.Message), f(e.Hint)
		out.Error = &e
	}
	return out
}

// clip cuts s to MaxText bytes on a character boundary, with "…".
func clip(s string) string {
	if len(s) <= MaxText {
		return s
	}
	cut := MaxText - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// FailedTests are the assertions of r that failed or could not be
// evaluated.
func (r Result) FailedTests() []Test {
	return slices.DeleteFunc(slices.Clone(r.Tests), func(t Test) bool { return t.Outcome == "passed" })
}

// Why says in a sentence why r did not pass: its error, its first failed
// assertion's message, an event stream's status, or why it was skipped;
// "" for a result that passed.
func (r Result) Why() string {
	switch r.Outcome {
	case OutcomeError:
		if r.Error != nil {
			return r.Error.Message
		}
		return "The request could not be sent."
	case OutcomeFailed:
		if failed := r.FailedTests(); len(failed) > 0 {
			return failed[0].Message
		}
		if r.Status != "" {
			return "The response's status was " + r.Status + "."
		}
		return "The request failed."
	case OutcomeSkipped:
		return SkipText(r.Skip)
	}
	return ""
}

// SkipText says why a run did not send an item of a kind.
func SkipText(skip string) string {
	switch skip {
	case "subscription":
		return "Not run: GraphQL subscriptions and operations over WebSocket are not part of a run."
	case "unknown":
		return "Not run: this version does not know this kind of request."
	case "invalid":
		return "Not run: the file could not be loaded."
	}
	return "Not run: a run sends HTTP, GraphQL and SSE requests only."
}

// Title names the run: its folder, or the project for a run of the whole
// project.
func (s Summary) Title() string {
	switch {
	case s.Folder != "":
		return s.Folder
	case s.Project != "":
		return s.Project
	}
	return "PostSonde run"
}
