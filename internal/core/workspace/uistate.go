package workspace

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// UI state (STO-06): small values the UI keeps per workspace on this
// computer, such as the open tabs and the expanded tree folders. They live
// in the workspace's local directory, never in the project folder, so
// switching projects does not change files in the user's repository.

// UIStateFileName is the UI state file in each local directory.
const UIStateFileName = "ui-state.json"

// uiStateVersion is the version of ui-state.json.
const uiStateVersion = 1

// Limits of the UI state of one workspace, so a bug in the UI cannot fill
// the disk.
const (
	// MaxUIStateKeys is the number of keys a workspace keeps.
	MaxUIStateKeys = 32
	// MaxUIStateValue is the largest value, in bytes of JSON.
	MaxUIStateValue = 1 << 20
)

// CodeInvalidUIState: SaveUIState was given a bad key or value, or the
// workspace already has MaxUIStateKeys other keys (kind invalid_argument).
// Details: key.
const CodeInvalidUIState = "workspace.invalid_ui_state"

// uiKeyPattern is what a UI state key looks like: "tabs", "tree.expanded".
var uiKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,63}$`)

// uiStateFile is ui-state.json. Like recent.json it is a cache: a file that
// cannot be read counts as empty and is replaced on the next save.
type uiStateFile struct {
	Version int                        `json:"version"`
	Entries map[string]json.RawMessage `json:"entries"`
}

// ReadUIState returns the UI state saved in the local directory dir, by
// key, as compact JSON. A missing or unreadable file gives an empty map;
// entries that are not valid JSON are dropped.
func ReadUIState(dir string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	if dir == "" {
		return out
	}
	f := readUIStateFile(dir)
	for k, v := range f.Entries {
		var compact bytes.Buffer
		if uiKeyPattern.MatchString(k) && json.Compact(&compact, v) == nil {
			out[k] = compact.Bytes()
		}
	}
	return out
}

func readUIStateFile(dir string) uiStateFile {
	var f uiStateFile
	data, err := os.ReadFile(filepath.Join(dir, UIStateFileName))
	if err != nil || json.Unmarshal(data, &f) != nil || f.Entries == nil {
		return uiStateFile{Entries: map[string]json.RawMessage{}}
	}
	return f
}

// SaveUIState stores value (JSON) under key in the local directory of the
// workspace id at root. The workspace must be the open one or one opened
// earlier since the app started, so the UI can still save the state of the
// project it is leaving; any other fails with CodeNoWorkspace. A null value
// removes the key.
func (m *Manager) SaveUIState(id, root, key string, value json.RawMessage) error {
	if err := m.checkDataDir(); err != nil {
		return err
	}
	if !uiKeyPattern.MatchString(key) {
		return invalidUIState(key, "The name of the saved view state is not valid.")
	}
	if len(value) > MaxUIStateValue {
		return invalidUIState(key, "The saved view state is too large.")
	}
	if !json.Valid(value) {
		return invalidUIState(key, "The saved view state is not valid JSON.")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, ok := m.localDirs[sessionKey(id, root)]
	if !ok {
		return errs.New(errs.NotFound, "This project is not open.").
			WithCode(CodeNoWorkspace).
			WithDetail("path", root)
	}
	f := readUIStateFile(dir)
	if string(value) == "null" {
		delete(f.Entries, key)
	} else {
		if _, exists := f.Entries[key]; !exists && len(f.Entries) >= MaxUIStateKeys {
			return invalidUIState(key, "Too many kinds of view state are saved for this project.")
		}
		f.Entries[key] = append(json.RawMessage(nil), value...)
	}
	f.Version = uiStateVersion
	if err := os.MkdirAll(dir, localDirPerm); err != nil {
		return localDataError(err, dir)
	}
	path := filepath.Join(dir, UIStateFileName)
	if err := atomicfile.WriteJSON(path, f, localFilePerm); err != nil {
		return localDataError(err, path)
	}
	return nil
}

func invalidUIState(key, message string) error {
	return errs.New(errs.InvalidArgument, message).
		WithCode(CodeInvalidUIState).
		WithDetail("key", key)
}

// sessionKey names a workspace opened in this session: its ID and folder,
// since two folders can hold the same ID.
func sessionKey(id, root string) string { return id + "\x00" + root }
