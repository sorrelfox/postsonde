package datadir

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Store is the data directory setting while the app runs. Its methods are
// safe for concurrent use.
type Store struct {
	mu  sync.Mutex
	st  Startup
	now func() time.Time
}

// NewStore returns a Store over what Start returned.
func NewStore(st Startup) *Store {
	return &Store{st: st, now: time.Now}
}

// Info describes the data directory for the settings screen.
type Info struct {
	Mode Mode
	// DataDir is the data directory in use ("" when there is none).
	DataDir string
	// Wanted is the data directory the settings ask for (the one in use,
	// unless it is not available).
	Wanted string
	// Default is the OS default data directory.
	Default string
	// AppFolder and Root: see Resolution.
	AppFolder, Root string
	// Marker is the portable marker file (ModePortable).
	Marker string
	// LocationPath is the location file ("" in ModeEnv and ModePortable,
	// where it is not used).
	LocationPath string
	// Pending is the change scheduled for the next start.
	Pending *Pending
	// Previous is the old folder of the last move, until it is deleted or
	// forgotten.
	Previous *Previous
	// Moved is true when the move to DataDir happened at this start.
	Moved bool
	// Problems are the problems found at start.
	Problems []*errs.Error
	// ChangeBlocked says why the data directory cannot be changed (nil
	// when it can).
	ChangeBlocked *errs.Error
}

// Info returns the current state.
func (s *Store) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.infoLocked()
}

func (s *Store) infoLocked() Info {
	st := s.st
	info := Info{
		Mode:      st.Mode,
		DataDir:   st.Dirs.Data,
		Wanted:    s.wantedLocked(),
		Default:   st.Default,
		AppFolder: st.AppFolder,
		Root:      st.Root,
		Pending:   clonePending(st.Location.Pending),
		Previous:  clonePrevious(st.Location.Previous),
		Moved:     st.Moved != nil,
		Problems:  slices.Clone(st.Problems),
	}
	if st.Mode == ModePortable {
		info.Marker = filepath.Join(st.AppFolder, MarkerName)
	}
	if st.Mode != ModeEnv && st.Mode != ModePortable {
		info.LocationPath = st.LocationPath
	}
	info.ChangeBlocked = s.blockedLocked()
	return info
}

// wantedLocked is the data directory the settings ask for.
func (s *Store) wantedLocked() string {
	switch s.st.Mode {
	case ModeEnv, ModePortable:
		return s.st.Dirs.Data
	}
	if d := s.st.Location.DataDir; d != "" {
		return filepath.Clean(d)
	}
	if s.st.hasBrokenLocation() {
		return ""
	}
	return s.st.Default
}

func (st Startup) hasBrokenLocation() bool {
	for _, p := range st.Problems {
		if p.Code == CodeLocationInvalid {
			return true
		}
	}
	return false
}

func (s *Store) blockedLocked() *errs.Error {
	switch s.st.Mode {
	case ModeEnv:
		return errs.New(errs.Unsupported, "The data directory is set by POSTSONDE_HOME.").
			WithCode(CodeChangeUnavailable).
			WithHint("Unset POSTSONDE_HOME to choose a data directory here.")
	case ModePortable:
		return errs.New(errs.Unsupported, "PostSonde runs in portable mode, so its data stays next to the app.").
			WithCode(CodeChangeUnavailable).
			WithHint("Remove the postsonde.portable file beside the app to keep the data elsewhere.")
	}
	if s.st.hasBrokenLocation() {
		return errs.New(errs.Unsupported, "The data directory setting file is broken.").
			WithCode(CodeChangeUnavailable).
			WithHint("Fix or delete " + s.st.LocationPath + ", then restart PostSonde.")
	}
	return nil
}

// Candidate is a folder the user picked, checked by Check.
type Candidate struct {
	// Path is the folder, cleaned and with symlinks resolved.
	Path string
	// Exists is false for a folder that will be created.
	Exists bool
	// HasData is true when it holds PostSonde data (only ActionUse fits);
	// otherwise it is empty (ActionMove or ActionFresh).
	HasData bool
	// IsDefault is true for the OS default data directory.
	IsDefault bool
	// CanMove is false when there is no data directory in use to move.
	CanMove bool
}

// Check validates path as the new data directory. It must be absolute, not
// the current data directory or nested in it (either way), not in a project
// folder, writable, and either empty or already a PostSonde data directory.
// The OS default data directory may hold other files (the settings, the
// web view's storage), as long as it has no PostSonde data for a move.
func (s *Store) Check(path string) (Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkLocked(path)
}

func (s *Store) checkLocked(path string) (Candidate, error) {
	if b := s.blockedLocked(); b != nil {
		return Candidate{}, b
	}
	invalid := func(msg, hint string) error {
		return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidPath).WithHint(hint).WithDetail("path", path)
	}
	if path == "" || !filepath.IsAbs(path) {
		return Candidate{}, invalid("Choose a folder by its full path.", "")
	}
	c := Candidate{Path: realPath(path), CanMove: s.st.Dirs.Data != ""}
	fi, err := os.Stat(c.Path)
	switch {
	case err == nil && !fi.IsDir():
		return Candidate{}, invalid("This is a file, not a folder.", "Choose a folder.")
	case err == nil:
		c.Exists = true
	case errors.Is(err, fs.ErrNotExist):
		if pfi, perr := os.Stat(filepath.Dir(c.Path)); perr != nil || !pfi.IsDir() {
			return Candidate{}, invalid("The folder does not exist.", "Choose an existing folder or make a new one in the dialog.")
		}
	default:
		return Candidate{}, errs.Wrap(err, errs.KindOf(err), "The folder cannot be read.").WithCode(CodeInvalidPath).WithDetail("path", path)
	}
	c.IsDefault = samePath(c.Path, s.st.Default)

	current := s.wantedLocked()
	if samePath(c.Path, current) {
		return Candidate{}, invalid("This folder is already the data directory.", "")
	}
	if current != "" {
		cur := realPath(current)
		if within(cur, c.Path) {
			return Candidate{}, invalid("This folder is inside the current data directory.", "Choose a folder outside "+current+".")
		}
		if within(c.Path, cur) {
			return Candidate{}, invalid("The current data directory is inside this folder.", "Choose a folder that is not a parent of "+current+".")
		}
	}
	if project := projectAbove(c.Path); project != "" {
		return Candidate{}, invalid("This folder is inside the project "+project+".",
			"Local data such as secrets must never be kept in project files. Choose a folder outside every project.")
	}
	for _, own := range []string{s.st.Dirs.Cache, s.st.Dirs.Logs} {
		if own != "" && within(realPath(own), c.Path) {
			return Candidate{}, invalid("This folder is inside PostSonde's cache or log folder.", "Choose another folder.")
		}
	}

	c.HasData = c.Exists && hasData(c.Path)
	if c.Exists && !c.HasData && !c.IsDefault {
		empty, err := isEmpty(c.Path)
		if err != nil {
			return Candidate{}, errs.Wrap(err, errs.KindOf(err), "The folder cannot be read.").WithCode(CodeInvalidPath).WithDetail("path", path)
		}
		if !empty {
			return Candidate{}, errs.New(errs.InvalidArgument, "This folder already has other files in it.").
				WithCode(CodeNotEmpty).
				WithHint("Choose an empty folder, or make a new one in the dialog, so PostSonde's data is not mixed with other files.").
				WithDetail("path", path)
		}
	}
	probeDir := c.Path
	if !c.Exists {
		probeDir = filepath.Dir(c.Path)
	}
	if err := probeWrite(probeDir); err != nil {
		return Candidate{}, errs.Wrap(err, errs.KindOf(err), "PostSonde cannot write to this folder.").
			WithCode(CodeUnwritable).
			WithHint("Choose a folder you can write to.").
			WithDetail("path", path)
	}
	return c, nil
}

// projectAbove returns the nearest folder at or above dir holding a
// project marker (postsonde.json), or "".
func projectAbove(dir string) string {
	for d := dir; ; {
		if fi, err := os.Stat(filepath.Join(d, projectfile.WorkspaceFileName)); err == nil && !fi.IsDir() {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// Schedule records a change to path for the next start, replacing any
// scheduled one. action must fit the folder: ActionUse for one holding
// PostSonde data, ActionMove (when a data directory is in use) or
// ActionFresh for an empty one.
func (s *Store) Schedule(path string, action Action) (Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.checkLocked(path)
	if err != nil {
		return Info{}, err
	}
	badAction := func(msg string) error {
		return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidAction).WithDetail("action", string(action))
	}
	switch action {
	case ActionUse:
		if !c.HasData {
			return Info{}, badAction("This folder has no PostSonde data to use.")
		}
	case ActionMove, ActionFresh:
		if c.HasData {
			return Info{}, badAction("This folder already holds PostSonde data; use it, or choose an empty folder.")
		}
		if action == ActionMove && !c.CanMove {
			return Info{}, badAction("There is no data directory in use to move.")
		}
	default:
		return Info{}, badAction("Unknown data directory action.")
	}
	loc := s.st.Location
	loc.Pending = &Pending{Action: action, DataDir: c.Path, From: s.wantedLocked(), RequestedAt: s.now().UTC().Format(time.RFC3339)}
	if err := s.saveLocked(loc); err != nil {
		return Info{}, err
	}
	return s.infoLocked(), nil
}

// Cancel drops the scheduled change. Nothing scheduled is not an error.
func (s *Store) Cancel() (Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Location.Pending == nil {
		return s.infoLocked(), nil
	}
	loc := s.st.Location
	loc.Pending = nil
	if err := s.saveLocked(loc); err != nil {
		return Info{}, err
	}
	return s.infoLocked(), nil
}

// DeletePrevious removes the PostSonde data a move left in the old folder
// (only the entries it copied), then the folder itself if nothing else is
// in it. The UI asks the user first. It refuses when the old folder is, or
// overlaps, the data directory in use or the one a scheduled change goes
// to.
func (s *Store) DeletePrevious() (Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.st.Location.Previous
	if p == nil {
		return s.infoLocked(), nil
	}
	fail := func(err error, msg string) error {
		return errs.Wrap(err, errs.KindOf(err), msg).WithCode(CodeDeleteFailed).WithDetail("path", p.DataDir)
	}
	old := realPath(p.DataDir)
	for _, keep := range []string{s.st.Dirs.Data, s.wantedLocked(), pendingDir(s.st.Location.Pending)} {
		if keep == "" {
			continue
		}
		k := realPath(keep)
		if within(k, old) || within(old, k) {
			return Info{}, fail(errors.New("the folder is in use"), "The old data folder is in use, so it was not deleted.")
		}
	}
	if err := removeEntries(old, p.Entries); err != nil {
		return Info{}, fail(err, "The old data could not be deleted completely.")
	}
	if empty, err := isEmpty(old); err == nil && empty {
		_ = os.RemoveAll(old) // only junk files are left
	}
	loc := s.st.Location
	loc.Previous = nil
	if err := s.saveLocked(loc); err != nil {
		return Info{}, err
	}
	return s.infoLocked(), nil
}

// ForgetPrevious drops the note about the old folder without deleting it.
func (s *Store) ForgetPrevious() (Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Location.Previous == nil {
		return s.infoLocked(), nil
	}
	loc := s.st.Location
	loc.Previous = nil
	if err := s.saveLocked(loc); err != nil {
		return Info{}, err
	}
	return s.infoLocked(), nil
}

func (s *Store) saveLocked(loc Location) error {
	if err := writeLocation(s.st.LocationPath, loc); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "The data directory setting could not be saved.").
			WithCode(CodeUnwritable).WithDetail("path", s.st.LocationPath)
	}
	loc.Version = LocationVersion
	s.st.Location = loc
	return nil
}

func pendingDir(p *Pending) string {
	if p == nil {
		return ""
	}
	return p.DataDir
}

func clonePending(p *Pending) *Pending {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

func clonePrevious(p *Previous) *Previous {
	if p == nil {
		return nil
	}
	c := *p
	c.Entries = slices.Clone(p.Entries)
	return &c
}
