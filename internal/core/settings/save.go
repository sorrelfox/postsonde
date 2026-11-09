package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
	"github.com/sorrelfox/postsonde/internal/core/storage/stablejson"
)

// FilePerm is the permission of a new settings file and of backups.
const FilePerm fs.FileMode = 0o600

// Save validates s and writes it, with the unknown members, to path. It
// replaces the file atomically; a new file gets FilePerm. It does not back
// up what was there: a Store does that for a broken file.
func Save(path string, s Settings, unknown Unknown) error {
	data, err := Marshal(s, unknown)
	if err != nil {
		return err
	}
	return write(path, data)
}

// Marshal encodes s and the unknown members as Save writes them: the known
// members in declaration order, then the unknown ones sorted by key, at the
// top level and in each section, in the stablejson format. The version is
// always CurrentVersion. An invalid s is an error with CodeInvalidValue.
func Marshal(s Settings, unknown Unknown) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	ui, err := stablejson.MarshalWithExtra(s.UI, unknown["ui"])
	if err != nil {
		return nil, errs.Wrap(err, errs.Internal, "The settings could not be encoded.")
	}
	network, err := stablejson.MarshalWithExtra(s.Network, unknown["network"])
	if err != nil {
		return nil, errs.Wrap(err, errs.Internal, "The settings could not be encoded.")
	}
	editor, err := stablejson.MarshalWithExtra(s.Editor, unknown["editor"])
	if err != nil {
		return nil, errs.Wrap(err, errs.Internal, "The settings could not be encoded.")
	}
	// The privacy section is left out while it is empty, so files
	// written before it existed stay as they were.
	var privacy json.RawMessage
	// The default key store is left out too.
	p := s.Privacy
	if p.SecretsKeyStore == SecretsKeyStoreKeychain {
		p.SecretsKeyStore = ""
	}
	if len(p.AllowedDirs) > 0 || p.SecretsLockMinutes != 0 || p.RequireSecretEncryption || p.SecretsKeyStore != "" || len(unknown["privacy"]) > 0 {
		if privacy, err = stablejson.MarshalWithExtra(p, unknown["privacy"]); err != nil {
			return nil, errs.Wrap(err, errs.Internal, "The settings could not be encoded.")
		}
	}
	top := struct {
		Version   int                 `json:"version"`
		UI        json.RawMessage     `json:"ui"`
		Network   json.RawMessage     `json:"network"`
		Editor    json.RawMessage     `json:"editor"`
		Privacy   json.RawMessage     `json:"privacy,omitempty"`
		Shortcuts map[string][]string `json:"shortcuts,omitempty"`
	}{CurrentVersion, ui, network, editor, privacy, s.Shortcuts}
	data, err := stablejson.MarshalWithExtra(top, unknown[RootSection])
	if err != nil {
		return nil, errs.Wrap(err, errs.Internal, "The settings could not be encoded.")
	}
	return data, nil
}

// write is atomicfile.WriteJSON for bytes that are already encoded (the
// Store keeps them to recognise its own writes).
func write(path string, data []byte) error {
	if err := atomicfile.WriteFile(path, data, FilePerm); err != nil {
		return saveFailed("The settings could not be saved.", path, err)
	}
	return nil
}

// maxBackups bounds the -2, -3, … suffixes tried by Backup.
const maxBackups = 100

// Backup copies the file at path next to it as
// <name>.bak-<UTC time>, e.g. settings.json.bak-20260915T143000Z, and
// returns the backup's path. If that name is taken, "-2", "-3", … is
// appended; an existing file is never overwritten.
func Backup(path string, now time.Time) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("settings: backup: %w", err)
	}
	base := path + ".bak-" + now.UTC().Format("20060102T150405Z")
	for i := 1; i <= maxBackups; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, FilePerm)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("settings: backup: %w", err)
		}
		_, werr := f.Write(data)
		serr := f.Sync()
		cerr := f.Close()
		if err := errors.Join(werr, serr, cerr); err != nil {
			_ = os.Remove(name)
			return "", fmt.Errorf("settings: backup: %w", err)
		}
		return name, nil
	}
	return "", fmt.Errorf("settings: backup: %s and %d numbered names are taken", base, maxBackups-1)
}

func saveFailed(msg, path string, err error) *errs.Error {
	return errs.Wrap(err, errs.KindOf(err), msg).
		WithCode(CodeSaveFailed).
		WithHint("Check that the config folder is writable and the disk is not full.").
		WithDetail("path", path)
}
