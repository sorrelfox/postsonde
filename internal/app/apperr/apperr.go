// Package apperr turns Go errors into the one JSON shape the UI receives
// when a bound method fails. See docs/errors.md.
//
// Services return errors built with internal/core/errs. They do not convert
// them: the app registers Marshaler as Wails' application-wide
// MarshalError, so every error a bound method returns crosses the boundary
// as an ErrorDTO in the rejected promise's `cause`.
package apperr

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// ErrorDTO is the JSON shape of an error sent to the UI. Every field is
// always present; frontend/src/lib/errors.ts has the matching AppError type.
type ErrorDTO struct {
	// Kind is one of the errs kinds, e.g. "invalid_argument".
	Kind string `json:"kind"`
	// Code is the finer machine code, or "".
	Code string `json:"code"`
	// Message is the user-facing message; never empty.
	Message string `json:"message"`
	// Hint is what the user can do about it, or "".
	Hint string `json:"hint"`
	// Details is optional machine-readable context; {} when there is none.
	Details map[string]string `json:"details"`
	// Causes are the underlying error messages, outermost first, for
	// diagnostics; [] when there are none.
	Causes []string `json:"causes"`
}

// defaultMessages are used when an error carries no message of its own,
// e.g. a context or file-system error returned without errs.Wrap.
var defaultMessages = map[errs.Kind]string{
	errs.InvalidArgument:  "The input is not valid.",
	errs.NotFound:         "The item was not found.",
	errs.AlreadyExists:    "The item already exists.",
	errs.PermissionDenied: "Permission was denied.",
	errs.Cancelled:        "The operation was cancelled.",
	errs.Timeout:          "The operation timed out.",
	errs.Network:          "A network error occurred.",
	errs.TLS:              "The secure connection (TLS) failed.",
	errs.Protocol:         "The server sent a response PostSonde could not understand.",
	errs.Parse:            "The content could not be parsed.",
	errs.IO:               "Reading or writing a file failed.",
	errs.Internal:         "Something went wrong inside PostSonde.",
	errs.Unsupported:      "This is not supported.",
}

// DefaultMessage returns the generic user-facing message for kind.
func DefaultMessage(kind errs.Kind) string {
	if m, ok := defaultMessages[kind]; ok {
		return m
	}
	return defaultMessages[errs.Internal]
}

// ToDTO maps any error to an ErrorDTO.
//
// The first *errs.Error in err's tree supplies the kind, code, message,
// hint and details; its message is left out of Causes. Other errors are
// classified with errs.KindOf and get the kind's generic message, so raw
// library text never becomes the message; it stays in Causes. ToDTO(nil)
// is a generic internal error.
func ToDTO(err error) ErrorDTO {
	kind := errs.KindOf(err)
	if kind == "" {
		kind = errs.Internal
	}
	dto := ErrorDTO{
		Kind:    string(kind),
		Details: map[string]string{},
		Causes:  errs.Chain(err),
	}
	if e, ok := errors.AsType[*errs.Error](err); ok {
		dto.Code = e.Code
		dto.Message = e.Message
		dto.Hint = e.Hint
		maps.Copy(dto.Details, e.Details)
		if i := slices.Index(dto.Causes, e.Message); i >= 0 {
			dto.Causes = slices.Delete(dto.Causes, i, i+1)
		}
	}
	if dto.Message == "" {
		dto.Message = DefaultMessage(kind)
	}
	return dto
}

// fallbackJSON is sent if an ErrorDTO somehow fails to marshal.
var fallbackJSON = []byte(`{"kind":"internal","code":"","message":"Something went wrong inside PostSonde.","hint":"","details":{},"causes":[]}`)

// Marshaler returns the function the app passes to Wails as
// application.Options.MarshalError. It encodes each error as an ErrorDTO
// and logs it: internal errors at error level with the full cause chain,
// others at debug level. A nil logger means slog.Default().
func Marshaler(logger *slog.Logger) func(error) []byte {
	if logger == nil {
		logger = slog.Default()
	}
	return func(err error) []byte {
		dto := ToDTO(err)
		level := slog.LevelDebug
		if dto.Kind == string(errs.Internal) {
			level = slog.LevelError
		}
		// Wails does not pass the call's context to MarshalError.
		logger.Log(context.Background(), level, "bound method failed",
			"kind", dto.Kind, "code", dto.Code, "error", err, "causes", dto.Causes)

		data, jsonErr := json.Marshal(dto)
		if jsonErr != nil {
			return fallbackJSON
		}
		return data
	}
}
