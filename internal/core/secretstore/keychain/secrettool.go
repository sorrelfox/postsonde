package keychain

import (
	"context"
	"encoding/hex"
	"os/exec"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// SecretToolName is libsecret's command-line tool, looked up in PATH.
const SecretToolName = "secret-tool"

const secretToolBackend = "Secret Service"

// secretToolHint says how to get a keychain on Linux.
const secretToolHint = "Install secret-tool (package libsecret-tools on Debian and Ubuntu, libsecret on Fedora and Arch) " +
	"and run a Secret Service such as GNOME Keyring, KWallet or KeePassXC. " + fallbackHint

// SecretTool keeps the key in the Secret Service (the freedesktop.org API
// of GNOME Keyring, KWallet and KeePassXC) through secret-tool, as an item
// with the attributes application=Service and account=AccountPrefix+keyID,
// labelled Label, the key hex-encoded as the secret. `secret-tool store`
// reads the secret from its standard input, and `secret-tool lookup`
// prints it on its standard output, so the key never appears in a command
// line.
//
// Without secret-tool, or without a Secret Service on the session bus (a
// server, a minimal desktop, no D-Bus), every call fails with
// CodeUnavailable, and the master password unlocks the secrets.
type SecretTool struct {
	// Run runs the commands; nil means Run.
	Run Runner
	// LookPath finds secret-tool; nil means exec.LookPath.
	LookPath func(string) (string, error)
	// Service replaces Service (tests).
	Service string
}

// Name names the keychain.
func (s *SecretTool) Name() string { return secretToolBackend }

func (s *SecretTool) attrs(acct string) []string {
	svc := s.Service
	if svc == "" {
		svc = Service
	}
	return []string{"application", svc, "account", acct}
}

func (s *SecretTool) run(stdin []byte, args ...string) (Output, error) {
	look := s.LookPath
	if look == nil {
		look = exec.LookPath
	}
	path, err := look(SecretToolName)
	if err != nil {
		return Output{}, unavailable(secretToolBackend, "secret-tool, which PostSonde needs to use the keychain, is not installed.", secretToolHint)
	}
	run := s.Run
	if run == nil {
		run = Run
	}
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	out, err := run(ctx, stdin, path, args...)
	if err != nil {
		return out, failed(err, errs.KindOf(err), secretToolBackend, "PostSonde could not run secret-tool.", secretToolHint, out)
	}
	if out.ExitCode != 0 && noService(out) {
		return out, unavailable(secretToolBackend, "No Secret Service (keychain) is running in this session.", secretToolHint).
			WithDetail("output", message(out))
	}
	return out, nil
}

// noService reports whether secret-tool failed because nothing provides
// the Secret Service: no D-Bus session, or no keyring daemon.
func noService(out Output) bool {
	m := strings.ToLower(string(out.Stderr))
	for _, s := range []string{"org.freedesktop.secrets", "d-bus", "dbus", "serviceunknown", "no such secret collection", "autolaunch"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// LoadKey returns the key of keyID, nil when there is none.
func (s *SecretTool) LoadKey(keyID string) ([]byte, error) {
	acct, err := account(keyID)
	if err != nil {
		return nil, err
	}
	out, err := s.run(nil, append([]string{"lookup"}, s.attrs(acct)...)...)
	if err != nil {
		return nil, err
	}
	switch {
	case out.ExitCode == 0 && len(strings.TrimSpace(string(out.Stdout))) > 0:
		return decodeKey(secretToolBackend, out.Stdout)
	case out.ExitCode == 0, len(strings.TrimSpace(string(out.Stderr))) == 0:
		// secret-tool exits 1 without a word when nothing matches.
		return nil, nil
	}
	return nil, s.refused(out, "PostSonde could not read its key from the Secret Service.")
}

// StoreKey stores key for keyID, replacing any.
func (s *SecretTool) StoreKey(keyID string, key []byte) error {
	acct, err := account(keyID)
	if err != nil {
		return err
	}
	stdin := hex.AppendEncode(nil, key)
	defer clear(stdin)
	out, err := s.run(stdin, append([]string{"store", "--label=" + Label}, s.attrs(acct)...)...)
	if err != nil {
		return err
	}
	if out.ExitCode != 0 {
		return s.refused(out, "PostSonde could not store its key in the Secret Service.")
	}
	return nil
}

// DeleteKey removes the key of keyID; none stored is not an error.
func (s *SecretTool) DeleteKey(keyID string) error {
	acct, err := account(keyID)
	if err != nil {
		return err
	}
	out, err := s.run(nil, append([]string{"clear"}, s.attrs(acct)...)...)
	if err != nil {
		return err
	}
	if out.ExitCode != 0 && len(strings.TrimSpace(string(out.Stderr))) > 0 {
		return s.refused(out, "PostSonde could not remove its key from the Secret Service.")
	}
	return nil
}

func (s *SecretTool) refused(out Output, msg string) error {
	kind := errs.Internal
	hint := "Unlock your keyring (for example in Seahorse) and try again. " + fallbackHint
	if m := strings.ToLower(string(out.Stderr)); strings.Contains(m, "dismissed") || strings.Contains(m, "cancel") || strings.Contains(m, "denied") {
		kind = errs.PermissionDenied
		hint = "The keyring was not unlocked. Unlock it when asked. " + fallbackHint
	}
	return failed(nil, kind, secretToolBackend, msg, hint, out)
}
