package settings

import (
	"bytes"
	"errors"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Store is the settings file of a running app. It is safe for concurrent
// use.
//
// It keeps the current settings in memory, writes every change with Save,
// and re-reads the file on Reload, so a hand edit is picked up while the app
// runs. Its own writes are recognised by content and are not reported as
// changes.
//
// The file is never overwritten silently:
//
//   - A broken file (not a JSON object) is copied with Backup before the
//     first change is written over it.
//   - A file from a newer version, or one that cannot be read, is not
//     written at all: changes are kept in memory for the session, and
//     Problems says so.
//
// With an admin policy (WithPolicy, SET-02), the settings in effect are
// the file's with every locked setting at its locked value. An Update
// that changes a locked setting fails with CodeLocked; a locked setting
// edited by hand in the file is ignored, and the file keeps the user's
// own value (it is not rewritten for it), which applies again once the
// policy no longer locks it.
type Store struct {
	path   string
	logger *slog.Logger
	now    func() time.Time
	policy Policy

	mu       sync.Mutex
	cur      Settings // in effect: user with the policy applied
	user     Settings // as in the file (or set in the app), before the policy
	unknown  Unknown
	problems []*errs.Error
	exists   bool   // the file was there when last read or written
	broken   bool   // back the file up before writing over it
	readOnly bool   // never write the file (newer version, unreadable)
	last     []byte // content last read or written; nil if none

	// readFailedAt is when reading the file, which was there before,
	// started to fail; zero while reads work. See transientReadFailure.
	readFailedAt time.Time
}

// transientReadFailure is how long reading a file that was there may fail
// before it counts as a problem. Replacing a file is not atomic for readers
// on Windows: while an editor renames its copy over the old one, opening
// the file fails with a sharing violation or access denied.
const transientReadFailure = 250 * time.Millisecond

// State is a snapshot of a Store.
type State struct {
	// Settings are the settings in effect.
	Settings Settings
	// Problems are the load problems of the file as last read: invalid
	// values, a parse or read error, a newer version. A successful save
	// clears them.
	Problems []*errs.Error
	// Exists is true when the file exists.
	Exists bool
	// ReadOnly is true when changes are not written to the file.
	ReadOnly bool
}

// Open loads the settings file at path and returns its Store. It never
// fails: when the file cannot be used the settings are the defaults and
// Problems says why. An empty path keeps the settings in memory only
// (for tests). A nil logger discards log records.
func Open(path string, logger *slog.Logger, opts ...OpenOption) *Store {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := &Store{path: path, logger: logger, now: time.Now, user: Defaults()}
	for _, opt := range opts {
		opt(s)
	}
	s.cur = s.policy.Enforce(s.user)
	if path != "" {
		s.mu.Lock()
		s.refreshLocked()
		s.mu.Unlock()
	}
	return s
}

// An OpenOption configures Open.
type OpenOption func(*Store)

// WithPolicy applies the admin policy (LoadPolicy, SET-02) to the
// settings.
func WithPolicy(p Policy) OpenOption {
	return func(s *Store) { s.policy = p }
}

// Policy returns the admin policy the Store applies (the zero Policy
// when there is none).
func (s *Store) Policy() Policy { return s.policy }

// setUserLocked sets the user's settings and, from them, the settings in
// effect. s.mu must be held.
func (s *Store) setUserLocked(user Settings) {
	s.user = user
	s.cur = s.policy.Enforce(user)
}

// Path returns the path of the settings file ("" for an in-memory Store).
func (s *Store) Path() string { return s.path }

// State returns the current settings and problems.
func (s *Store) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return State{
		Settings: s.cur,
		Problems: slices.Clone(s.problems),
		Exists:   s.exists,
		ReadOnly: s.readOnly,
	}
}

// Settings returns the current settings.
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Update applies p and writes the result. It returns the new settings in
// effect and whether they changed. An invalid p returns the
// CodeInvalidValue error from Settings.Apply and changes nothing; so does
// a failed write (CodeSaveFailed), and a p that sets a setting the admin
// policy locks to another value (CodeLocked, kind permission_denied). A
// patch that changes nothing writes nothing, so the file is created by
// the first real change.
func (s *Store) Update(p Patch) (Settings, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next, err := s.cur.Apply(p)
	if err != nil {
		return s.cur, false, err
	}
	if err := s.policy.checkLocks(next); err != nil {
		return s.cur, false, err
	}
	if next, err = s.user.Apply(p); err != nil {
		return s.cur, false, err
	}
	if len(s.policy.Locked) > 0 {
		// A locked setting keeps the user's own value in the file, even
		// when p sets it to the locked one.
		keep, err := fieldValues(s.user, s.policy.LockedKeys())
		if err == nil {
			next, _, err = overlay(next, keep)
		}
		if err != nil {
			return s.cur, false, errs.Wrap(err, errs.Internal, "The settings could not be updated.")
		}
	}
	if next.Equal(s.user) {
		return s.cur, false, nil
	}
	if s.path == "" || s.readOnly {
		s.setUserLocked(next)
		return s.cur, true, nil
	}

	data, err := Marshal(next, s.unknown)
	if err != nil {
		return s.cur, false, err
	}
	if s.broken {
		backup, err := Backup(s.path, s.now())
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// The broken file is gone: nothing to keep.
		case err != nil:
			return s.cur, false, saveFailed("The broken settings file could not be backed up, so it was not replaced.", s.path, err)
		default:
			s.logger.Warn("settings: backed up the broken settings file before replacing it", "backup", backup)
		}
	}
	if err := write(s.path, data); err != nil {
		return s.cur, false, err
	}
	s.setUserLocked(next)
	s.last = data
	s.exists = true
	s.broken = false
	s.problems = nil
	return s.cur, true, nil
}

// Create writes the settings in effect (without the admin policy's locked
// values, which are not the user's) when the file does not exist yet,
// so it can be opened for editing by hand. It does nothing when the file
// exists (even a broken one), for an in-memory Store, or when the file is
// not written (a newer version, unreadable).
func (s *Store) Create() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return nil
	}
	if _, err := os.Lstat(s.path); err == nil || !errors.Is(err, fs.ErrNotExist) {
		// There already (it is read on the next Reload), or not knowable:
		// never write over it.
		return nil
	}
	data, err := Marshal(s.user, s.unknown)
	if err != nil {
		return err
	}
	if err := write(s.path, data); err != nil {
		return err
	}
	s.last = data
	s.exists = true
	s.broken = false
	return nil
}

// Reload re-reads the file if its content differs from what the Store last
// read or wrote, and reports whether the settings or the problems changed.
//
//   - A valid file replaces the settings; unknown members are kept.
//   - A deleted file resets the settings to the defaults.
//   - A broken or unreadable file keeps the current settings (an editor
//     may be half-way through saving it) and reports the problem.
//   - An empty file keeps the current settings without a problem, and is
//     read again next time.
func (s *Store) Reload() bool {
	if s.path == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshLocked()
}

// refreshLocked reads the file and updates the Store. s.mu must be held.
func (s *Store) refreshLocked() bool {
	beforeSettings, beforeProblems := s.cur, s.problems

	data, err := os.ReadFile(s.path)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		s.readFailedAt = time.Time{}
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if !s.exists && !s.readOnly && len(s.problems) == 0 {
			return false
		}
		s.setUserLocked(Defaults())
		s.unknown = nil
		s.problems = nil
		s.exists, s.broken, s.readOnly = false, false, false
		s.last = nil

	case err != nil:
		if s.exists && !s.readOnly {
			// Possibly an editor replacing the file: keep the settings in
			// effect and look again next time, unless reads keep failing.
			now := s.now()
			if s.readFailedAt.IsZero() {
				s.readFailedAt = now
			}
			if now.Sub(s.readFailedAt) < transientReadFailure {
				return false
			}
		}
		s.problems = []*errs.Error{readFailed(s.path, err)}
		s.exists, s.broken, s.readOnly = true, false, true
		s.last = nil

	default:
		if s.last != nil && bytes.Equal(data, s.last) {
			return false
		}
		if s.exists && len(bytes.TrimSpace(data)) == 0 {
			// Emptied while the app runs: most likely an editor that
			// truncates before writing, caught half-way. Keep the settings
			// in effect and look again next time. (At startup an empty file
			// means the defaults.)
			return false
		}
		settings, report, err := parse(s.path, data)
		s.exists = true
		s.last = data
		if err != nil {
			// Keep the current settings and unknown members; back the
			// file up before writing over it.
			var e *errs.Error
			if !errors.As(err, &e) {
				e = errs.Wrap(err, errs.Parse, "The settings file could not be parsed.")
			}
			s.problems = []*errs.Error{e}
			s.broken, s.readOnly = true, false
			break
		}
		s.setUserLocked(settings)
		s.unknown = report.Unknown
		s.problems = report.Problems
		s.broken, s.readOnly = false, report.Newer
	}

	for _, p := range s.problems {
		s.logger.Warn("settings: "+p.Message, "code", p.Code, "details", p.Details, "err", p.Err)
	}
	return !s.cur.Equal(beforeSettings) || !sameProblems(beforeProblems, s.problems)
}

func sameProblems(a, b []*errs.Error) bool {
	return slices.EqualFunc(a, b, func(x, y *errs.Error) bool {
		return x.Kind == y.Kind && x.Code == y.Code && x.Message == y.Message &&
			x.Hint == y.Hint && maps.Equal(x.Details, y.Details)
	})
}
