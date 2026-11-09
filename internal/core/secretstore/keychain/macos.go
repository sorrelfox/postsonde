package keychain

import (
	"context"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// SecurityPath is the macOS keychain command.
const SecurityPath = "/usr/bin/security"

// Exit statuses of security: the low byte of the Security framework's
// OSStatus.
const (
	securityNotFound     = 44  // errSecItemNotFound (-25300)
	securityCanceled     = 128 // errSecUserCanceled (-128)
	securityAuthFailed   = 51  // errSecAuthFailed (-25293)
	securityInteractions = 36  // errSecInteractionNotAllowed (-25308)
)

const securityBackend = "macOS Keychain"

// Security keeps the key as a generic password item of the default (login)
// keychain: service Service, account AccountPrefix+keyID, label Label, the
// key hex-encoded as the password.
//
// The key never appears in a command line, where any process could read
// it: `security add-generic-password -w KEY` would put it there, so the
// command is given to `security -i` (interactive mode) on its standard
// input instead, and `security find-generic-password -w` prints the key on
// its standard output. Created by /usr/bin/security, the item trusts that
// tool: PostSonde reads it without a prompt, and so can any program of
// the same user that runs security, which can read the secret files of
// the unlocked app too.
type Security struct {
	// Run runs the commands; nil means Run.
	Run Runner
	// Path is the security command; "" means SecurityPath.
	Path string
	// Service replaces Service (the opt-in round-trip test uses its own).
	Service string
}

// Name names the keychain.
func (s *Security) Name() string { return securityBackend }

func (s *Security) run(stdin []byte, args ...string) (Output, error) {
	run := s.Run
	if run == nil {
		run = Run
	}
	path := s.Path
	if path == "" {
		path = SecurityPath
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return Output{}, unavailable(securityBackend, "The macOS keychain command ("+path+") is missing.",
				fallbackHint)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	out, err := run(ctx, stdin, path, args...)
	if err != nil {
		return out, failed(err, errs.KindOf(err), securityBackend, "PostSonde could not run the macOS keychain command.",
			fallbackHint, out)
	}
	return out, nil
}

func (s *Security) service() string {
	if s.Service != "" {
		return s.Service
	}
	return Service
}

// LoadKey returns the key of keyID, nil when there is none.
func (s *Security) LoadKey(keyID string) ([]byte, error) {
	acct, err := account(keyID)
	if err != nil {
		return nil, err
	}
	out, err := s.run(nil, "find-generic-password", "-s", s.service(), "-a", acct, "-w")
	if err != nil {
		return nil, err
	}
	switch out.ExitCode {
	case 0:
		return decodeKey(securityBackend, out.Stdout)
	case securityNotFound:
		return nil, nil
	}
	return nil, s.refused(out, "PostSonde could not read its key from the macOS keychain.")
}

// StoreKey stores key for keyID, replacing any.
func (s *Security) StoreKey(keyID string, key []byte) error {
	acct, err := account(keyID)
	if err != nil {
		return err
	}
	// One command on standard input; the service and account are fixed
	// and hex, so nothing needs quoting but the label. The key is only
	// ever in this buffer, cleared afterwards.
	stdin := []byte("add-generic-password -U -s " + strconv.Quote(s.service()) + " -a " + acct + " -l " + strconv.Quote(Label) + " -w ")
	stdin = hex.AppendEncode(stdin, key)
	stdin = append(stdin, '\n')
	defer clear(stdin)
	out, err := s.run(stdin, "-i")
	if err != nil {
		return err
	}
	if out.ExitCode != 0 {
		return s.refused(out, "PostSonde could not store its key in the macOS keychain.")
	}
	return nil
}

// DeleteKey removes the key of keyID; none stored is not an error.
func (s *Security) DeleteKey(keyID string) error {
	acct, err := account(keyID)
	if err != nil {
		return err
	}
	out, err := s.run(nil, "delete-generic-password", "-s", s.service(), "-a", acct)
	if err != nil {
		return err
	}
	if out.ExitCode != 0 && out.ExitCode != securityNotFound {
		return s.refused(out, "PostSonde could not remove its key from the macOS keychain.")
	}
	return nil
}

func (s *Security) refused(out Output, msg string) error {
	switch out.ExitCode {
	case securityCanceled, securityAuthFailed:
		return failed(nil, errs.PermissionDenied, securityBackend, msg,
			"Access to the keychain was denied. Allow PostSonde when macOS asks. "+fallbackHint, out)
	case securityInteractions:
		return failed(nil, errs.PermissionDenied, securityBackend, msg,
			"The keychain is locked and cannot ask for its password now. Unlock it in Keychain Access. "+fallbackHint, out)
	}
	return failed(nil, errs.Internal, securityBackend, msg,
		"Check the item \""+Label+"\" in Keychain Access. "+fallbackHint, out).
		WithDetail("exitCode", strconv.Itoa(out.ExitCode))
}
