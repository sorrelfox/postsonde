package console

import (
	"log/slog"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/logging"
)

// Levels, as the UI filters them.
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// EntryDTO is one log record, redacted (docs/privacy.md).
type EntryDTO struct {
	// Seq numbers the records over the app run, from 1; the UI drops a
	// Seq it already has.
	Seq uint64 `json:"seq"`
	// At is when it was logged, RFC 3339 in UTC.
	At string `json:"at"`
	// Level is "debug", "info", "warn" or "error".
	Level   string `json:"level"`
	Message string `json:"message"`
	// Source is the part of the app that logged it ("netx", "http"), or "".
	Source string `json:"source"`
	// OpID and OpKind are the operation or connection it belongs to
	// ("op_…", "http.send"), or "".
	OpID   string `json:"opId"`
	OpKind string `json:"opKind"`
	// Fields are the record's attributes in order.
	Fields []FieldDTO `json:"fields"`
}

// FieldDTO is one attribute of a record.
type FieldDTO struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ListDTO is what List returns.
type ListDTO struct {
	// Entries are the kept records, oldest first.
	Entries []EntryDTO `json:"entries"`
	// Capacity is how many records the console keeps.
	Capacity int `json:"capacity"`
}

func toEntryDTO(e logging.Entry) EntryDTO {
	fields := make([]FieldDTO, len(e.Fields))
	for i, f := range e.Fields {
		fields[i] = FieldDTO{Key: f.Key, Value: f.Value}
	}
	return EntryDTO{
		Seq:     e.Seq,
		At:      e.Time.UTC().Format(time.RFC3339Nano),
		Level:   levelName(e.Level),
		Message: e.Message,
		Source:  e.Source,
		OpID:    e.OpID,
		OpKind:  e.OpKind,
		Fields:  fields,
	}
}

// levelName maps a slog level to the four the UI knows; levels between
// them go to the one below.
func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return LevelError
	case l >= slog.LevelWarn:
		return LevelWarn
	case l >= slog.LevelInfo:
		return LevelInfo
	default:
		return LevelDebug
	}
}
