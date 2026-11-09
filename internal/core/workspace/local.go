package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Names in the app data directory.
const (
	// DirName is the directory under the app data directory that holds the
	// recent list and one local directory per workspace.
	DirName = "workspaces"
	// RecentFileName is the recent-workspaces list in DirName.
	RecentFileName = "recent.json"
	// LocalFileName is the metadata file in each local directory.
	LocalFileName = "local.json"
)

// localVersion is the version of local.json.
const localVersion = 1

// maxPlainKey is the longest workspace ID used as a directory name as is.
const maxPlainKey = 64

// localFile is local.json: which project folder a local directory belongs
// to. It is PostSonde's own bookkeeping, never shared.
type localFile struct {
	Version      int       `json:"version"`
	WorkspaceID  string    `json:"workspaceId"`
	Path         string    `json:"path"`
	CreatedAt    time.Time `json:"createdAt"`
	LastOpenedAt time.Time `json:"lastOpenedAt"`
}

// LocalKey returns the directory name of a workspace's local directory. A
// generated ID ("ws_" and lower-case letters, digits, "_" or "-") is used as
// it is. Any other ID, which can only come from a hand-edited marker, is
// replaced by "x." and a hash of it, so it can never escape the directory,
// clash on a case-insensitive file system or be a reserved name on Windows.
func LocalKey(id string) string {
	if isPlainID(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "x." + hex.EncodeToString(sum[:16])
}

func isPlainID(id string) bool {
	rest, ok := strings.CutPrefix(id, projectfile.WorkspaceIDPrefix)
	if !ok || rest == "" || len(id) > maxPlainKey {
		return false
	}
	for _, c := range rest {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// copyKey returns the directory name of the local directory of a copy of
// the workspace at root: the key, "@" and a hash of the path.
func copyKey(key, root string) string {
	sum := sha256.Sum256([]byte(root))
	return key + "@" + hex.EncodeToString(sum[:8])
}

func (m *Manager) baseDir() string { return filepath.Join(m.dataDir, DirName) }

// attachLocalLocked returns the local directory for the project at root with
// workspace id, creating it if needed, and records root in its local.json.
// See the package documentation for how copies and moves are told apart.
func (m *Manager) attachLocalLocked(root, id string, now time.Time) (dir string, status LocalStatus, previous string, err error) {
	key := LocalKey(id)
	primary := filepath.Join(m.baseDir(), key)
	copyDir := filepath.Join(m.baseDir(), copyKey(key, root))

	meta, found := readLocal(primary)

	// A copy opened before keeps its own directory.
	if isDir(copyDir) {
		copyMeta, _ := readLocal(copyDir)
		return copyDir, LocalCopy, meta.Path, writeLocal(copyDir, copyMeta, id, root, now)
	}

	switch {
	case !isDir(primary):
		status = LocalNew
	case !found || meta.Path == "" || samePath(meta.Path, root):
		// A directory without usable metadata is taken to be this one's.
		status = LocalExisting
	case holdsWorkspace(meta.Path, id):
		// The recorded folder still has this workspace: root is a copy.
		return copyDir, LocalCopy, meta.Path, writeLocal(copyDir, localFile{}, id, root, now)
	default:
		status, previous = LocalMoved, meta.Path
	}
	return primary, status, previous, writeLocal(primary, meta, id, root, now)
}

// readLocal reads dir/local.json. found is false when it is missing or
// cannot be parsed.
func readLocal(dir string) (meta localFile, found bool) {
	data, err := os.ReadFile(filepath.Join(dir, LocalFileName))
	if err != nil {
		return localFile{}, false
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return localFile{}, false
	}
	return meta, true
}

// writeLocal creates dir if needed and writes local.json, keeping the
// creation time of meta.
func writeLocal(dir string, meta localFile, id, root string, now time.Time) error {
	if err := os.MkdirAll(dir, localDirPerm); err != nil {
		return localDataError(err, dir)
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now.UTC()
	}
	meta.Version = localVersion
	meta.WorkspaceID = id
	meta.Path = root
	meta.LastOpenedAt = now.UTC()
	path := filepath.Join(dir, LocalFileName)
	if err := atomicfile.WriteJSON(path, meta, localFilePerm); err != nil {
		return localDataError(err, path)
	}
	return nil
}

func localDataError(err error, path string) error {
	return errs.Wrap(err, errs.KindOf(err), "PostSonde could not save its local data for this project.").
		WithCode(CodeLocalDataFailed).
		WithHint("Check that the PostSonde data directory is writable and the disk is not full.").
		WithDetail("path", path)
}

// holdsWorkspace reports whether the folder at path has a marker with the
// workspace ID id.
func holdsWorkspace(path, id string) bool {
	data, err := os.ReadFile(filepath.Join(path, projectfile.WorkspaceFileName))
	if err != nil {
		return false
	}
	f, err := projectfile.DecodeWorkspace(data)
	return err == nil && f.ID == id
}

// samePath reports whether a and b name the same folder, also when they are
// spelled differently (case on a case-insensitive file system, symlinks).
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
