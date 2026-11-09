package secretstore

import (
	"errors"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes. Every error of this package is an *errs.Error with one of
// them; IsError reports whether an error is one.
const (
	// CodeLocked: a secret is needed but the secrets are locked (kind
	// permission_denied). Details: path when a file was read.
	CodeLocked = "secretstore.locked"
	// CodeWrongPassword: the master password is not the right one (kind
	// permission_denied).
	CodeWrongPassword = "secretstore.wrong_password"
	// CodePasswordTooShort: a new master password is shorter than
	// MinPasswordLength (kind invalid_argument). Details: min.
	CodePasswordTooShort = "secretstore.password_too_short"
	// CodeCorrupt: an encrypted file is damaged, truncated, was changed
	// outside PostSonde or copied over another file (kind parse). Details:
	// path.
	CodeCorrupt = "secretstore.corrupt"
	// CodeOtherKey: an encrypted file was encrypted with a key this
	// installation does not have (restored from an older setup; kind
	// parse). Details: path.
	CodeOtherKey = "secretstore.other_key"
	// CodeNotSetUp: an encrypted file was found but encryption is not set
	// up (vault.json is missing; kind not_found). Details: path.
	CodeNotSetUp = "secretstore.not_set_up"
	// CodeNotEnabled: an operation needs encryption on, and it is off
	// (kind invalid_argument).
	CodeNotEnabled = "secretstore.not_enabled"
	// CodeAlreadyEnabled: encryption is on already (kind already_exists).
	CodeAlreadyEnabled = "secretstore.already_enabled"
	// CodeRequired: encryption is required by the administrator: turning
	// it off, or writing a secret while it is off, is refused (kind
	// permission_denied).
	CodeRequired = "secretstore.required"
	// CodeConfigInvalid: vault.json cannot be read or used (kind parse).
	// Details: path.
	CodeConfigInvalid = "secretstore.config_invalid"
	// CodeFailed: reading or writing a file failed (the kind comes from
	// the cause). Details: path.
	CodeFailed = "secretstore.failed"
)

// IsError reports whether err is an error of this package (its code
// starts with "secretstore."). Stores return such errors as they are, so
// the UI sees CodeLocked and can ask for the password.
func IsError(err error) bool {
	var e *errs.Error
	return errors.As(err, &e) && strings.HasPrefix(e.Code, "secretstore.")
}

// IsLocked reports whether err is, or wraps, CodeLocked.
func IsLocked(err error) bool {
	return errors.Is(err, &errs.Error{Code: CodeLocked})
}

func lockedError(path string) error {
	e := errs.New(errs.PermissionDenied, "Your stored secrets are locked.").
		WithCode(CodeLocked).
		WithHint("Unlock them with your master password: the lock in the status bar, or Settings, Privacy.")
	if path != "" {
		e = e.WithDetail("path", path)
	}
	return e
}

func wrongPassword() error {
	return errs.New(errs.PermissionDenied, "The master password is not correct.").
		WithCode(CodeWrongPassword).
		WithHint("Type it again. If you have forgotten it, the stored secrets can only be deleted (Forgot password).")
}

func corrupt(path string, cause error) error {
	return errs.Wrap(cause, errs.Parse, "An encrypted secrets file is damaged or was changed outside PostSonde.").
		WithCode(CodeCorrupt).
		WithHint("Restore the file from a backup, or delete it; the secrets it held then have to be entered again.").
		WithDetail("path", path)
}

func otherKey(path string) error {
	return errs.New(errs.Parse, "An encrypted secrets file was encrypted with another master key.").
		WithCode(CodeOtherKey).
		WithHint("It may come from an older setup or another computer. Delete it; the secrets it held then have to be entered again.").
		WithDetail("path", path)
}

func notSetUp(path string) error {
	return errs.New(errs.NotFound, "A secrets file is encrypted, but encryption is not set up here.").
		WithCode(CodeNotSetUp).
		WithHint("Restore secrets/vault.json in the data directory, or delete the encrypted secrets (Settings, Privacy, Reset).").
		WithDetail("path", path)
}

func notEnabled() error {
	return errs.New(errs.InvalidArgument, "Encryption of stored secrets is off.").
		WithCode(CodeNotEnabled).
		WithHint("Turn it on in Settings, Privacy.")
}

func alreadyEnabled() error {
	return errs.New(errs.AlreadyExists, "Encryption of stored secrets is already on.").
		WithCode(CodeAlreadyEnabled)
}

func required() error {
	return errs.New(errs.PermissionDenied, "Your administrator requires stored secrets to be encrypted.").
		WithCode(CodeRequired).
		WithHint("Set a master password in Settings, Privacy.")
}

func failed(err error, msg, path string) error {
	return errs.Wrap(err, errs.KindOf(err), msg).
		WithCode(CodeFailed).
		WithHint("Check that the PostSonde data directory is writable and the disk is not full.").
		WithDetail("path", path)
}
