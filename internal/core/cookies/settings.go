package cookies

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// SettingsFileName is the file, in a workspace's local directory, of the
// workspace's cookie settings (CK-03).
const SettingsFileName = "cookie-settings.json"

const settingsVersion = 1

// Settings are a workspace's cookie settings. They are kept in the
// workspace's local directory, never in the project, like the jars.
type Settings struct {
	// PerEnvironment keeps a separate jar per environment: sends with an
	// environment active use <local>/cookies/<environment>.json, sends
	// without one the shared jar. Off (the default), every environment
	// shares <local>/cookies.json.
	PerEnvironment bool
}

type settingsFile struct {
	Version        int  `json:"version"`
	PerEnvironment bool `json:"perEnvironment"`
}

// ReadSettings returns the cookie settings of the workspace whose local
// directory is localDir. A missing file, one that cannot be parsed or one
// from a newer version is the default: the settings are a preference, and
// the default keeps the jar every version used before.
func ReadSettings(localDir string) Settings {
	data, err := os.ReadFile(filepath.Join(localDir, SettingsFileName))
	if err != nil {
		return Settings{}
	}
	var f settingsFile
	if json.Unmarshal(data, &f) != nil || f.Version < 1 || f.Version > settingsVersion {
		return Settings{}
	}
	return Settings{PerEnvironment: f.PerEnvironment}
}

// WriteSettings saves the cookie settings of the workspace whose local
// directory is localDir. It fails with CodeLocalDataFailed.
func WriteSettings(localDir string, st Settings) error {
	if err := os.MkdirAll(localDir, localDirPerm); err != nil && !errors.Is(err, fs.ErrExist) {
		return localDataFailed(err, localDir, "PostSonde could not save the cookie settings of this project.")
	}
	path := filepath.Join(localDir, SettingsFileName)
	if err := atomicfile.WriteJSON(path, settingsFile{Version: settingsVersion, PerEnvironment: st.PerEnvironment}, filePerm); err != nil {
		return localDataFailed(err, path, "PostSonde could not save the cookie settings of this project.")
	}
	return nil
}
