package runner

import (
	"github.com/sorrelfox/postsonde/internal/app/apperr"
	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	corerunner "github.com/sorrelfox/postsonde/internal/core/runner"
)

// Outcomes of a RunResultDTO.
const (
	// OutcomePassed: a response arrived and none of the request's
	// assertions failed (a request without assertions passes); for an SSE
	// request, the stream opened with a 2xx status.
	OutcomePassed = "passed"
	// OutcomeFailed: a response arrived, but an assertion failed or could
	// not be evaluated; for an SSE request, a status other than 2xx.
	OutcomeFailed = "failed"
	// OutcomeError: the request could not be built or sent (an undefined
	// variable, a refused connection, a timeout): Error says why.
	OutcomeError = "error"
	// OutcomeSkipped: the item is not sent by a run: Skip says why.
	OutcomeSkipped = "skipped"
)

// RunInputDTO is what Start takes.
type RunInputDTO struct {
	// FolderID is the folder to run, with its subfolders; "" runs every
	// request of the project.
	FolderID string `json:"folderId"`
	// Iterations is how many times the folder is run, 1 to 10,000.
	Iterations int `json:"iterations"`
	// DelayMs is the wait between two requests, from the end of one to
	// the start of the next, 0 to an hour.
	DelayMs int64 `json:"delayMs"`
	// StreamMs is how long an SSE request is read for, up to 10 minutes;
	// 0 for 5 seconds.
	StreamMs int64 `json:"streamMs,omitempty"`
	// AllowUnresolved sends undefined {{variables}} as written, as
	// SendInputDTO.AllowUnresolved does; without it a request with one
	// is listed with the template.unresolved error, and the run goes on.
	AllowUnresolved bool `json:"allowUnresolved,omitempty"`
	// Prompts are the values typed for the {{$prompt}} references of the
	// run's requests, by label (DYN-07): asked once before the run.
	Prompts map[string]string `json:"prompts,omitempty"`
	// RequestIDs are the requests the run sends, in the order it sends
	// them (RUN-02): IDs of the folder's plan, each at most once. Absent
	// (null) runs every item of the plan in the tree's order; an empty
	// list is refused (runner.empty). The folder itself is not changed.
	RequestIDs []string `json:"requestIds,omitempty"`
	// StopOnFailure ends the run after the first result that failed or is
	// an error (RUN-04, bail); the last progress event then says
	// StageStopped. Without it the run goes on.
	StopOnFailure bool `json:"stopOnFailure,omitempty"`
	// DataFile is a data file chosen with PickDataFile (RUN-05): the run
	// makes one iteration per row, Iterations is not used, and each
	// iteration sets its row's values as run variables. "" for none.
	DataFile string `json:"dataFile,omitempty"`
	// DataDelimiter is the CSV delimiter of DataFile: ",", ";", "\t" or
	// "|"; "" to detect it.
	DataDelimiter string `json:"dataDelimiter,omitempty"`
}

// DataPreviewDTO is a data file as PickDataFile and PreviewDataFile read
// it.
type DataPreviewDTO struct {
	// Path is the file chosen; "" when the dialog was cancelled. Name is
	// its base name.
	Path string `json:"path"`
	Name string `json:"name"`
	// Format is "csv" or "json".
	Format string `json:"format"`
	// Delimiter is the CSV field delimiter used (detected when none was
	// given): ",", ";", "\t" or "|"; "" for JSON.
	Delimiter string `json:"delimiter"`
	// Columns are the variables each row sets, in the file's order.
	Columns []string `json:"columns"`
	// Rows are the first MaxPreviewRows rows, a value per column ("" for
	// a JSON object without it).
	Rows [][]string `json:"rows"`
	// RowCount is the number of rows: the run's iterations.
	RowCount int `json:"rowCount"`
}

// RunItemDTO is one request of a run's plan.
type RunItemDTO struct {
	// Position is the item's place in the plan, from 1.
	Position int `json:"position"`
	// RequestID, Name, Kind (the file's kind: "http", "graphql", "sse",
	// "websocket", …) and Method (HTTP requests, "" for others).
	RequestID string `json:"requestId"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Method    string `json:"method"`
	// Path names the folders between the run's folder and the request,
	// outermost first.
	Path []string `json:"path"`
	// Skip says why the request is not sent: "kind" (WebSocket,
	// Socket.IO, gRPC, TCP), "subscription" (a GraphQL subscription or an
	// operation over WebSocket), "unknown" (a kind this version does not
	// know), "invalid" (a file that did not load); "" when it is sent.
	Skip string `json:"skip"`
}

// PlanDTO is what a run of a folder sends (Plan).
type PlanDTO struct {
	// Items are the requests in the order they are sent in every
	// iteration.
	Items []RunItemDTO `json:"items"`
	// Runnable is the number of items that are sent.
	Runnable int `json:"runnable"`
}

// RunResultDTO is the outcome of one request of one iteration: an item of
// the run's runner:op:data events, in the order sent.
type RunResultDTO struct {
	// Iteration counts from 1; Index counts the results of the run from 1.
	Iteration int `json:"iteration"`
	Index     int `json:"index"`
	// Position, RequestID, Name, Kind, Method, Path and Skip are the
	// request's, as in its RunItemDTO.
	Position  int      `json:"position"`
	RequestID string   `json:"requestId"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Method    string   `json:"method"`
	Path      []string `json:"path"`
	Skip      string   `json:"skip"`
	// Outcome is OutcomePassed, OutcomeFailed, OutcomeError or
	// OutcomeSkipped.
	Outcome string `json:"outcome"`
	// StartedAt is when the request was sent, in Unix milliseconds; 0 for
	// a skipped one. DurationMs is how long it took.
	StartedAt  int64 `json:"startedAt"`
	DurationMs int64 `json:"durationMs"`
	// StatusCode and Status are the response's, 0 and "" without one.
	StatusCode int    `json:"statusCode"`
	Status     string `json:"status"`
	// Size is the number of body bytes received.
	Size int64 `json:"size"`
	// TestsPassed and TestsFailed count the assertions that passed, and
	// that failed or could not be evaluated; Tests has each result, in
	// the request's order (TEST-06), [] for none.
	TestsPassed int                         `json:"testsPassed"`
	TestsFailed int                         `json:"testsFailed"`
	Tests       []httpservice.TestResultDTO `json:"tests"`
	// Extractions are the results of the request's extraction rules
	// (TEST-05), in the request's order, [] for none. A failed or invalid
	// one makes the result failed, as a failed assertion does.
	Extractions []httpservice.ExtractionResultDTO `json:"extractions"`
	// Events is the number of events an SSE request received.
	Events int `json:"events"`
	// HasResponse is set when the runner kept the response of an HTTP or
	// GraphQL request for RunnerService.Response (RUN-07); it may be
	// dropped later for newer ones.
	HasResponse bool `json:"hasResponse"`
	// Error says why the request could not be built or sent; null
	// otherwise.
	Error *apperr.ErrorDTO `json:"error"`
}

func toItemDTO(position int, it corerunner.Item) RunItemDTO {
	path := it.Path
	if path == nil {
		path = []string{}
	}
	return RunItemDTO{Position: position, RequestID: it.ID, Name: it.Name, Kind: it.Kind, Method: it.Method, Path: path, Skip: string(it.Skip)}
}

func toPlanDTO(p corerunner.Plan) PlanDTO {
	items := make([]RunItemDTO, len(p.Items))
	for i, it := range p.Items {
		items[i] = toItemDTO(i+1, it)
	}
	return PlanDTO{Items: items, Runnable: p.Runnable()}
}

// RunSummaryDTO is a past run of the run history (RUN-09).
type RunSummaryDTO struct {
	// ID is the run's operation ID.
	ID string `json:"id"`
	// FolderID is the folder run ("" for the whole project); Folder its
	// name ("" for the project) and Project the project's.
	FolderID string `json:"folderId"`
	Folder   string `json:"folder"`
	Project  string `json:"project"`
	// StartedAt and EndedAt are Unix milliseconds.
	StartedAt int64 `json:"startedAt"`
	EndedAt   int64 `json:"endedAt"`
	ElapsedMs int64 `json:"elapsedMs"`
	// Status is "ok" (got through), "stopped" (at a failure) or
	// "cancelled".
	Status string `json:"status"`
	// Iterations, Requests (per iteration) and Total (the results the run
	// would have had) are its size; DelayMs and StopOnFailure its options.
	Iterations    int   `json:"iterations"`
	Requests      int   `json:"requests"`
	Total         int   `json:"total"`
	DelayMs       int64 `json:"delayMs"`
	StopOnFailure bool  `json:"stopOnFailure"`
	// DataFile is the data file's name (never its path), "" for none;
	// DataRows its rows.
	DataFile string `json:"dataFile"`
	DataRows int    `json:"dataRows"`
	// Results and the counts by outcome cover every result.
	Results int `json:"results"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Errors  int `json:"errors"`
	Skipped int `json:"skipped"`
	// TotalMs is the time in requests.
	TotalMs int64 `json:"totalMs"`
	// Omitted is the results not kept (over 20,000).
	Omitted int `json:"omitted"`
}

// RunRecordDTO is a past run with its results.
type RunRecordDTO struct {
	Summary RunSummaryDTO `json:"summary"`
	// Results are the results kept, in the order sent; hasResponse is
	// false (responses are not kept in the history).
	Results []RunResultDTO `json:"results"`
}

// ExportRunInputDTO is what ExportRun takes.
type ExportRunInputDTO struct {
	// RunID is the run's ID (its operation ID).
	RunID string `json:"runId"`
	// Format is "junit", "json" or "html".
	Format string `json:"format"`
}

// ExportRunResultDTO is what ExportRun returns.
type ExportRunResultDTO struct {
	// Saved is false when the user cancelled the dialog.
	Saved bool   `json:"saved"`
	Path  string `json:"path"`
	// Size is the bytes written.
	Size int64 `json:"size"`
}
