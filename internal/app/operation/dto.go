package operation

import "github.com/sorrelfox/postsonde/internal/app/apperr"

// Event payloads and the value a start method returns. They follow the DTO
// rules in docs/bindings.md (camelCase JSON, no omitempty, nil slices never
// reach JSON) and are the same for every domain; docs/events.md describes
// them.

// StartedDTO is returned by every method that starts an operation.
type StartedDTO struct {
	// OpID identifies the operation in its events and in OpsService.Cancel.
	OpID string `json:"opId"`
}

// DataEventDTO carries one batch of items: the <domain>:op:data event.
type DataEventDTO[T any] struct {
	OpID string `json:"opId"`
	// Seq numbers every event of one operation 1, 2, 3, … across the data,
	// progress and done events, so the UI can detect a missed event.
	Seq int64 `json:"seq"`
	// Items is never empty: batches without items are not sent.
	Items []T `json:"items"`
}

// ProgressEventDTO is the latest progress: the <domain>:op:progress event.
// At most one is sent per flush interval, after that interval's data event.
type ProgressEventDTO struct {
	OpID string `json:"opId"`
	Seq  int64  `json:"seq"`
	// Done is how much work is finished, in the operation's own unit.
	Done int64 `json:"done"`
	// Total is the amount of work expected, or 0 when unknown.
	Total int64 `json:"total"`
	// Message is a short status line, or "".
	Message string `json:"message"`
}

// DoneEventDTO ends every operation: the <domain>:op:done event. It is
// always the last event with this opId.
type DoneEventDTO struct {
	OpID string `json:"opId"`
	Seq  int64  `json:"seq"`
	// Status is "ok", "error" or "cancelled".
	Status Status `json:"status"`
	// Error is the error when Status is "error", otherwise null. It is the
	// same ErrorDTO a rejected call carries (apperr.ToDTO): kind, code,
	// message, hint, details and causes.
	Error *apperr.ErrorDTO `json:"error"`
	// Items is the number of items sent in data events.
	Items int64 `json:"items"`
	// Batches is the number of data events sent.
	Batches int64 `json:"batches"`
	// ElapsedMs is the time from start to finish, in milliseconds.
	ElapsedMs int64 `json:"elapsedMs"`
}
