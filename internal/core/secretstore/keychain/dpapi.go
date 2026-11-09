package keychain

import (
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// DPAPIDir is the directory of the DPAPI key files, relative to the data
// directory. It is not a secrets directory: the vault's migrations never
// touch it.
const DPAPIDir = "keychain"

const dpapiBackend = "Windows DPAPI"

// DPAPI keeps the key in <data>/keychain/vault-<keyID>.dpapi (0600),
// sealed with the Windows Data Protection API for the current user
// (CryptProtectData with CRYPTPROTECT_UI_FORBIDDEN, the entropy binding
// the blob to its key ID). Only the same Windows account, on the same
// computer (or with a roaming profile), can unseal it; a copied data
// directory falls back to the master password.
type DPAPI struct {
	// Dir is the data directory.
	Dir string
	// Protect and Unprotect seal and unseal; nil means the real DPAPI
	// (Windows only; other systems fail with CodeUnavailable). Tests pass
	// fakes.
	Protect, Unprotect func(data, entropy []byte) ([]byte, error)
}

// Name names the keychain.
func (d *DPAPI) Name() string { return dpapiBackend }

func (d *DPAPI) path(keyID string) (string, error) {
	acct, err := account(keyID)
	if err != nil {
		return "", err
	}
	if d.Dir == "" {
		return "", unavailable(dpapiBackend, "PostSonde has no data directory to keep its key in.", fallbackHint)
	}
	return filepath.Join(d.Dir, DPAPIDir, acct+".dpapi"), nil
}

func dpapiEntropy(keyID string) []byte { return []byte("postsonde/secrets-key/1\n" + keyID) }

// LoadKey returns the key of keyID, nil when there is none.
func (d *DPAPI) LoadKey(keyID string) ([]byte, error) {
	path, err := d.path(keyID)
	if err != nil {
		return nil, err
	}
	blob, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, failed(err, errs.KindOf(err), dpapiBackend, "PostSonde could not read its key file.", fallbackHint, Output{}).
			WithDetail("path", path)
	}
	unprotect := d.Unprotect
	if unprotect == nil {
		unprotect = unprotectData
	}
	text, err := unprotect(blob, dpapiEntropy(keyID))
	if err != nil {
		return nil, failed(err, errs.PermissionDenied, dpapiBackend, "Windows could not unseal PostSonde's key.",
			"The key file was sealed by another Windows account or computer. "+fallbackHint, Output{}).WithDetail("path", path)
	}
	defer clear(text)
	return decodeKey(dpapiBackend, text)
}

// StoreKey stores key for keyID, replacing any.
func (d *DPAPI) StoreKey(keyID string, key []byte) error {
	path, err := d.path(keyID)
	if err != nil {
		return err
	}
	protect := d.Protect
	if protect == nil {
		protect = protectData
	}
	text := hex.AppendEncode(nil, key)
	defer clear(text)
	blob, err := protect(text, dpapiEntropy(keyID))
	if err != nil {
		var e *errs.Error
		if errors.As(err, &e) && e.Code == CodeUnavailable {
			return err
		}
		return failed(err, errs.Internal, dpapiBackend, "Windows could not seal PostSonde's key.", fallbackHint, Output{})
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return failed(err, errs.KindOf(err), dpapiBackend, "PostSonde could not save its key file.", fallbackHint, Output{}).
			WithDetail("path", path)
	}
	if err := atomicfile.WriteFile(path, blob, 0o600); err != nil {
		return failed(err, errs.KindOf(err), dpapiBackend, "PostSonde could not save its key file.", fallbackHint, Output{}).
			WithDetail("path", path)
	}
	return nil
}

// DeleteKey removes the key of keyID; none stored is not an error.
func (d *DPAPI) DeleteKey(keyID string) error {
	path, err := d.path(keyID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return failed(err, errs.KindOf(err), dpapiBackend, "PostSonde could not delete its key file.", fallbackHint, Output{}).
			WithDetail("path", path)
	}
	return nil
}
