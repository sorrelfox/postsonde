// Package keychain keeps the data key of the encrypted secrets
// (internal/core/secretstore) in the operating system's keychain, so the
// secrets unlock at startup without the master password (SEC-03).
// Each backend is a secretstore.KeyStore:
//
//   - macOS: a generic password item of the login keychain, through
//     /usr/bin/security (Security).
//   - Linux and the other Unix systems: the Secret Service (GNOME Keyring,
//     KWallet, KeePassXC) through secret-tool from libsecret (SecretTool).
//   - Windows: the key sealed with DPAPI (CryptProtectData, for the
//     current user) in a file of the data directory (DPAPI).
//
// No backend links a library: the commands are run without a shell, and
// DPAPI is called through syscall. The key never appears in a command
// line: it is written to the command's standard input. Keys are stored by
// the vault's key ID, hex-encoded. See docs/privacy.md#keychain.
package keychain

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
)

// Error codes. Every error of this package is an *errs.Error with one of
// them, and a hint.
const (
	// CodeUnavailable: there is no keychain to use: the command is
	// missing, or no Secret Service answers (kind unsupported). Details:
	// backend.
	CodeUnavailable = "keychain.unavailable"
	// CodeFailed: the keychain refused or failed (kind internal, or
	// permission_denied when the user denied access). Details: backend,
	// and the command's message as output.
	CodeFailed = "keychain.failed"
	// CodeInvalid: what the keychain holds is not a key (kind parse).
	// Details: backend.
	CodeInvalid = "keychain.invalid"
)

// Names of the stored items, the same on every system.
const (
	// Service is the macOS item's service and the Secret Service item's
	// "application" attribute: PostSonde's app ID (the bundle identifier)
	// plus ".secrets". Builds before the move to io.github.sorrelfox.PostSonde
	// used app.postsonde.secrets; their item is not found, so the vault
	// unlocks with the master password once and stores the key again.
	Service = "io.github.sorrelfox.PostSonde.secrets"
	// Label is the item's name in Keychain Access, Seahorse or KeePassXC.
	Label = "PostSonde secrets key"
	// AccountPrefix plus the key ID is the macOS item's account and the
	// Secret Service item's "account" attribute; the DPAPI file is
	// AccountPrefix+keyID+".dpapi".
	AccountPrefix = "vault-"
)

// Timeout bounds one keychain command. It is long because the system may
// ask the user to unlock the keychain first.
const Timeout = 2 * time.Minute

// KeyStore is a secretstore.KeyStore with a name for the UI.
type KeyStore interface {
	secretstore.KeyStore
	// Name is the keychain's name: "macOS Keychain", "Secret Service",
	// "Windows DPAPI".
	Name() string
}

// Output is what a command printed and how it exited.
type Output struct {
	Stdout, Stderr []byte
	// ExitCode is the command's exit status; 0 when it succeeded.
	ExitCode int
}

// Runner runs name with args, without a shell, with stdin as its standard
// input. The error is for a command that could not be run or did not
// finish (not found, timed out), not for an exit status other than 0.
// Tests pass a fake.
type Runner func(ctx context.Context, stdin []byte, name string, args ...string) (Output, error)

// Run is the Runner of the real commands.
func Run(ctx context.Context, stdin []byte, name string, args ...string) (Output, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	out := Output{Stdout: []byte(stdout.String()), Stderr: []byte(stderr.String())}
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		out.ExitCode = exit.ExitCode()
		return out, nil
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return out, err
}

// account is the item's account for keyID, which must be the hex key ID
// of a vault: it ends up in a command and in a file name.
func account(keyID string) (string, error) {
	if b, err := hex.DecodeString(keyID); err != nil || len(b) == 0 || len(keyID) > 64 {
		return "", errs.New(errs.InvalidArgument, "The key ID of the secrets is not valid.").
			WithCode(CodeInvalid).WithHint("This is a bug in PostSonde; please report it.")
	}
	return AccountPrefix + keyID, nil
}

// decodeKey decodes the hex key a keychain returned.
func decodeKey(backend string, text []byte) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(string(text)))
	if err != nil || len(key) == 0 {
		return nil, errs.New(errs.Parse, "The key PostSonde keeps in the "+backend+" cannot be read.").
			WithCode(CodeInvalid).
			WithHint("Unlock with the master password: PostSonde then stores the key again.").
			WithDetail("backend", backend)
	}
	return key, nil
}

// message is a command's error output, trimmed and bounded, for an error's
// details. It never holds the key: the key is only ever on standard input
// and standard output.
func message(out Output) string {
	s := strings.TrimSpace(string(out.Stderr))
	const limit = 500
	if len(s) > limit {
		s = s[:limit] + "…"
	}
	return s
}

func unavailable(backend, msg, hint string) *errs.Error {
	return errs.New(errs.Unsupported, msg).
		WithCode(CodeUnavailable).
		WithHint(hint).
		WithDetail("backend", backend)
}

func failed(cause error, kind errs.Kind, backend, msg, hint string, out Output) *errs.Error {
	var e *errs.Error
	if cause != nil {
		e = errs.Wrap(cause, kind, msg)
	} else {
		e = errs.New(kind, msg)
	}
	e = e.WithCode(CodeFailed).WithHint(hint).WithDetail("backend", backend)
	if m := message(out); m != "" {
		e = e.WithDetail("output", m)
	}
	return e
}

// fallbackHint ends every hint: the master password always works.
const fallbackHint = "The master password still unlocks the secrets; to stop using the keychain, choose Master password in Settings, Privacy, Stored secrets."
