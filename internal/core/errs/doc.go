// Package errs is PostSonde's shared error model.
//
// An *Error carries a Kind (a small, stable category the UI can act on), an
// optional finer Code, a user-facing Message, an optional actionable Hint,
// optional Details and the underlying cause. The app layer turns any error
// into one JSON shape for the UI (internal/app/apperr); see docs/errors.md.
//
//	return errs.New(errs.InvalidArgument, "The timeout must be a positive number.").
//		WithCode("settings.invalid_value").
//		WithHint("Enter a value from 1 to 600 seconds.").
//		WithDetail("field", "timeoutSeconds")
//
//	if err := os.WriteFile(path, data, 0o600); err != nil {
//		return errs.Wrap(err, errs.IO, "Could not save the settings.")
//	}
//
// Messages and hints are shown to the user as they are. Write them as
// sentences, and never put secret values (tokens, passwords, header values,
// full URLs with credentials) in a Message, Hint or Details. The redacting
// logger in internal/core/logging is only a safety net for logs.
//
// The package imports only the standard library, like all of internal/core.
package errs
