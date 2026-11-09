package workspace

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Error codes, in addition to the errs kinds and the projectfile codes that
// reading the marker can return.
const (
	// CodeInvalidPath: the path is empty or not absolute (kind
	// invalid_argument). Details: path.
	CodeInvalidPath = "workspace.invalid_path"
	// CodeFolderNotFound: the folder does not exist, or is a file (kind
	// not_found). Details: path.
	CodeFolderNotFound = "workspace.folder_not_found"
	// CodeNotAProject: the folder has no postsonde.json (kind not_found).
	// Details: path. The UI offers to create a project there.
	CodeNotAProject = "workspace.not_a_project"
	// CodeAlreadyAProject: Create was given a folder that already has a
	// postsonde.json (kind already_exists). Details: path.
	CodeAlreadyAProject = "workspace.already_a_project"
	// CodeMissingID: the marker has no workspace ID (kind parse). Details:
	// path.
	CodeMissingID = "workspace.missing_id"
	// CodeCreateFailed: the folder or the marker could not be written. The
	// kind comes from the cause. Details: path.
	CodeCreateFailed = "workspace.create_failed"
	// CodeLocalDataFailed: the local side-file directory or one of the
	// app's own workspace files could not be written. The kind comes from
	// the cause. Details: path.
	CodeLocalDataFailed = "workspace.local_data_failed"
	// CodeNoWorkspace: no workspace is open (kind not_found).
	CodeNoWorkspace = "workspace.none_open"
	// CodeDataDirInProject: the app data directory is inside the project
	// folder, so local data would end up in it (kind invalid_argument).
	// Details: path.
	CodeDataDirInProject = "workspace.data_dir_in_project"
	// CodeNoDataDir: the Manager has no app data directory (kind internal).
	CodeNoDataDir = "workspace.no_data_dir"
	// CodeSharedByWindows: another project cannot be opened, created or
	// the open one closed while other windows show it: every window of the
	// app shows the same project (kind invalid_argument; the app's
	// WorkspaceService, docs/windows.md). Details: path when a folder was
	// given.
	CodeSharedByWindows = "workspace.shared_by_windows"
)

// Permissions of what this package creates in the app data directory: only
// the user may read local data, since it holds secrets.
const (
	localDirPerm  fs.FileMode = 0o700
	localFilePerm fs.FileMode = 0o600
	// projectDirPerm is used when Create makes the project folder itself.
	projectDirPerm fs.FileMode = 0o755
)

// LocalStatus says how the local directory was found when a workspace was
// opened. See the package documentation.
type LocalStatus string

const (
	// LocalNew: the workspace had no local directory yet; one was created.
	LocalNew LocalStatus = "new"
	// LocalExisting: the local directory belongs to this folder.
	LocalExisting LocalStatus = "existing"
	// LocalMoved: the local directory belonged to a folder that no longer
	// holds this workspace; it now belongs to this one (PreviousPath).
	LocalMoved LocalStatus = "moved"
	// LocalCopy: another folder (PreviousPath) holds the same workspace ID;
	// this folder has a local directory of its own.
	LocalCopy LocalStatus = "copy"
)

// Workspace is an open project. It is a snapshot: a later Open returns a
// new value.
type Workspace struct {
	// Root is the absolute, symlink-resolved path of the project folder.
	Root string
	// File is the decoded marker. File.ID is the workspace ID.
	File *projectfile.Workspace
	// LocalDir is the workspace's local side-file directory in the app data
	// directory. It exists. Never inside Root.
	LocalDir string
	// LocalStatus and PreviousPath say how LocalDir was found.
	LocalStatus  LocalStatus
	PreviousPath string
	// OpenedAt is when the workspace was opened.
	OpenedAt time.Time
	// RecentErr is set when the workspace opened but the recent-workspaces
	// list could not be updated. It is worth logging, not failing for.
	RecentErr error
}

// ID returns the workspace ID.
func (w *Workspace) ID() string { return w.File.ID }

// Name returns the display name, or the folder name when the marker has
// none.
func (w *Workspace) Name() string {
	if n := strings.TrimSpace(w.File.Name); n != "" {
		return n
	}
	return filepath.Base(w.Root)
}

// Newer reports whether the marker is from a newer PostSonde. Features that
// write project files must refuse to for such a workspace.
func (w *Workspace) Newer() bool { return w.File.Newer() }

// MarkerPath returns the path of postsonde.json.
func (w *Workspace) MarkerPath() string {
	return filepath.Join(w.Root, projectfile.WorkspaceFileName)
}

// Manager opens and creates workspaces, keeps track of the open one and of
// the recent ones. It is safe for concurrent use.
type Manager struct {
	dataDir string
	now     func() time.Time

	mu      sync.Mutex
	current *Workspace
	// localDirs are the local directories of the workspaces opened since
	// the Manager was created, by sessionKey, for SaveUIState.
	localDirs map[string]string
}

// New returns a Manager keeping local data under dataDir/workspaces (the
// app data directory, package appdirs). With an empty dataDir every method
// that needs local data fails with CodeNoDataDir.
func New(dataDir string) *Manager {
	return &Manager{dataDir: dataDir, now: time.Now, localDirs: map[string]string{}}
}

// Current returns the open workspace, or nil.
func (m *Manager) Current() *Workspace {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// IsCurrent reports whether path (a folder, or the postsonde.json in one)
// is the open workspace's folder, also when it is spelled differently
// (symlinks, case). False when no workspace is open or path does not
// resolve.
func (m *Manager) IsCurrent(path string) bool {
	w := m.Current()
	if w == nil {
		return false
	}
	root, err := resolveFolder(path)
	return err == nil && samePath(root, w.Root)
}

// LocalDir returns the local side-file directory of the open workspace.
// Without one it fails with CodeNoWorkspace.
func (m *Manager) LocalDir() (string, error) {
	if w := m.Current(); w != nil {
		return w.LocalDir, nil
	}
	return "", errs.New(errs.NotFound, "No project is open.").
		WithCode(CodeNoWorkspace).
		WithHint("Open a project folder or create a project first.")
}

// Open opens the project in the folder at path (an absolute path; the path
// of its postsonde.json is accepted too), makes it the current workspace
// and records it as the most recent one. It never writes into the folder.
func (m *Manager) Open(path string) (*Workspace, error) {
	if err := m.checkDataDir(); err != nil {
		return nil, err
	}
	root, err := resolveFolder(path)
	if err != nil {
		return nil, err
	}
	marker := filepath.Join(root, projectfile.WorkspaceFileName)
	switch fi, err := os.Stat(marker); {
	case errors.Is(err, fs.ErrNotExist) || (err == nil && fi.IsDir()):
		return nil, errs.New(errs.NotFound, "This folder is not a PostSonde project.").
			WithCode(CodeNotAProject).
			WithHint("Choose the folder that contains postsonde.json, or create a project in this folder.").
			WithDetail("path", root)
	case err != nil:
		return nil, errs.Wrap(err, errs.KindOf(err), "The project folder could not be read.").
			WithCode(CodeFolderNotFound).
			WithHint("Check that the folder is readable.").
			WithDetail("path", root)
	}
	f, err := readMarker(marker)
	if err != nil {
		return nil, err
	}
	return m.activate(root, f)
}

// Create makes the folder at path (an absolute path) a new project: it
// creates the folder if needed and writes a postsonde.json with a new
// workspace ID and name (the folder name when name is blank). A folder that
// already has a postsonde.json is refused with CodeAlreadyAProject. The new
// workspace becomes the current one.
func (m *Manager) Create(path, name string) (*Workspace, error) {
	if err := m.checkDataDir(); err != nil {
		return nil, err
	}
	if err := checkPath(path); err != nil {
		return nil, err
	}
	clean := filepath.Clean(path)
	if isFile(clean) {
		return nil, notAFolder(clean)
	}
	if err := os.MkdirAll(clean, projectDirPerm); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "The project folder could not be created.").
			WithCode(CodeCreateFailed).
			WithHint("Check that the location exists and is writable.").
			WithDetail("path", clean)
	}
	root, err := resolveFolder(clean)
	if err != nil {
		return nil, err
	}
	marker := filepath.Join(root, projectfile.WorkspaceFileName)
	if _, err := os.Lstat(marker); !errors.Is(err, fs.ErrNotExist) {
		return nil, errs.New(errs.AlreadyExists, "This folder is already a PostSonde project.").
			WithCode(CodeAlreadyAProject).
			WithHint("Open it instead, or choose another folder.").
			WithDetail("path", root)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(root)
	}
	f := &projectfile.Workspace{ID: projectfile.NewID(projectfile.KindWorkspace), Name: name}
	if err := projectfile.WriteFile(marker, f); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "The project could not be created.").
			WithCode(CodeCreateFailed).
			WithHint("Check that the folder is writable and the disk is not full.").
			WithDetail("path", root)
	}
	// Read it back, so the value is exactly what Open would return.
	if f, err = readMarker(marker); err != nil {
		return nil, err
	}
	return m.activate(root, f)
}

// Close closes the current workspace, if any, so the app does not reopen it
// on the next start. It reports whether one was open.
func (m *Manager) Close() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return false, nil
	}
	m.current = nil
	if m.dataDir == "" {
		return true, nil
	}
	return true, m.updateRecentLocked(func(r *recentFile) { r.LastOpen = "" })
}

// ReopenLast opens the workspace that was open when the app last quit. It
// returns nil and no error when there is none; when its folder can no
// longer be opened it returns the error and keeps the entry, so a folder on
// a disk that is not mounted is reopened once it is back.
func (m *Manager) ReopenLast() (*Workspace, error) {
	if m.dataDir == "" {
		return nil, nil
	}
	m.mu.Lock()
	r := m.readRecentLocked()
	m.mu.Unlock()
	if r.LastOpen == "" {
		return nil, nil
	}
	return m.Open(r.LastOpen)
}

// activate finds or creates the local directory of the project at root,
// makes it current and records it in the recent list.
func (m *Manager) activate(root string, f *projectfile.Workspace) (*Workspace, error) {
	if insideProject(root, m.dataDir) {
		return nil, errs.New(errs.InvalidArgument, "PostSonde's data directory is inside this project folder.").
			WithCode(CodeDataDirInProject).
			WithHint("Local data such as secrets must not be kept in project files. Move the project, or choose a data directory outside it in Settings, General.").
			WithDetail("path", root)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	local, status, previous, err := m.attachLocalLocked(root, f.ID, now)
	if err != nil {
		return nil, err
	}
	w := &Workspace{Root: root, File: f, LocalDir: local, LocalStatus: status, PreviousPath: previous, OpenedAt: now}
	// The workspace is usable even when the recent list cannot be saved.
	w.RecentErr = m.updateRecentLocked(func(r *recentFile) { r.touch(w) })
	m.current = w
	m.localDirs[sessionKey(f.ID, root)] = local
	return w, nil
}

func (m *Manager) checkDataDir() error {
	if m.dataDir == "" {
		return errs.New(errs.Internal, "PostSonde has no data directory to keep local project data in.").
			WithCode(CodeNoDataDir).
			WithHint("Settings, General says why and lets you choose another data directory.")
	}
	return nil
}

// readMarker reads and checks postsonde.json. projectfile errors (parse,
// unknown kind, wrong kind) are returned as they are: they already carry the
// path and a hint.
func readMarker(marker string) (*projectfile.Workspace, error) {
	data, err := os.ReadFile(marker)
	if err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "The project file could not be read.").
			WithCode(projectfile.CodeReadFailed).
			WithHint("Check that postsonde.json is readable.").
			WithDetail("path", marker)
	}
	f, err := projectfile.DecodeWorkspace(data)
	if err != nil {
		var e *errs.Error
		if errors.As(err, &e) {
			return nil, e.WithDetail("path", marker)
		}
		return nil, err
	}
	if strings.TrimSpace(f.ID) == "" {
		return nil, errs.New(errs.Parse, "The project file has no workspace ID.").
			WithCode(CodeMissingID).
			WithHint(`Add an "id" such as "ws_…" to postsonde.json, or restore it from version control.`).
			WithDetail("path", marker)
	}
	return f, nil
}

func checkPath(path string) error {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return errs.New(errs.InvalidArgument, "Choose a project folder.").
			WithCode(CodeInvalidPath).
			WithHint("The folder must be given as a full path.").
			WithDetail("path", path)
	}
	return nil
}

// resolveFolder checks that path is an absolute path to an existing folder
// (or to the postsonde.json in one) and returns the folder's absolute path
// with symlinks resolved.
func resolveFolder(path string) (string, error) {
	if err := checkPath(path); err != nil {
		return "", err
	}
	p := filepath.Clean(path)
	if filepath.Base(p) == projectfile.WorkspaceFileName && isFile(p) {
		p = filepath.Dir(p)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", errs.Wrap(err, errs.NotFound, "The folder does not exist.").
				WithCode(CodeFolderNotFound).
				WithHint("It may have been moved, renamed or deleted, or its disk is not connected.").
				WithDetail("path", p)
		}
		return "", errs.Wrap(err, errs.KindOf(err), "The folder could not be read.").
			WithCode(CodeFolderNotFound).
			WithDetail("path", p)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", errs.Wrap(err, errs.KindOf(err), "The folder could not be read.").
			WithCode(CodeFolderNotFound).
			WithDetail("path", p)
	}
	if !fi.IsDir() {
		return "", notAFolder(p)
	}
	return resolved, nil
}

// insideProject reports whether dir is root or below it, following
// symlinks where the paths exist.
func insideProject(root, dir string) bool {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func notAFolder(path string) error {
	return errs.New(errs.NotFound, "This is a file, not a folder.").
		WithCode(CodeFolderNotFound).
		WithHint("Choose a folder.").
		WithDetail("path", path)
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
