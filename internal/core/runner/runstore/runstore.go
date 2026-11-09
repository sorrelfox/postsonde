// Package runstore is the run history (RUN-09): the records of the folder
// runs of a project (report.Run), kept in the project's local directory,
// never in the project, bounded by count and size. Each run is two files
// in the store's directory, written atomically, 0600 in a 0700 directory:
//
//	<id>.json           the whole record: summary and results
//	<id>.summary.json   its summary, which List reads
//
// The record is written first and the summary last, so a listed run can
// be read; a record left without its summary (a crash between the two) is
// removed by the next List. Standard library only. See
// docs/runner.md#run-history.
package runstore

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/runner/report"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// Dir is the store's directory in a project's local directory.
const Dir = "runs"

// CodeNotFound: the run is not in the history (kind not_found). Details:
// id.
const CodeNotFound = "runner.run_not_found"

// CodeInvalidID: a run ID that cannot name a run (kind
// invalid_argument). Details: id.
const CodeInvalidID = "runner.invalid_run_id"

// Limits bound the history. The newest run is always kept.
type Limits struct {
	// MaxRuns is the most runs kept (0: DefaultMaxRuns).
	MaxRuns int
	// MaxBytes is the most bytes of records kept (0: DefaultMaxBytes).
	MaxBytes int64
}

// Default limits.
const (
	DefaultMaxRuns        = 50
	DefaultMaxBytes int64 = 256 << 20
)

const (
	recordExt  = ".json"
	summaryExt = ".summary.json"
)

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Store is the run history in one directory. Safe for concurrent use;
// one Store per directory.
type Store struct {
	dir    string
	limits Limits
	mu     sync.Mutex
}

// Open returns the store in dir, created when the first run is saved.
func Open(dir string, limits Limits) *Store {
	if limits.MaxRuns <= 0 {
		limits.MaxRuns = DefaultMaxRuns
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = DefaultMaxBytes
	}
	return &Store{dir: dir, limits: limits}
}

// Dir is the store's directory.
func (s *Store) Dir() string { return s.dir }

func checkID(id string) error {
	if !validID.MatchString(id) {
		return errs.New(errs.InvalidArgument, "The run is not a run of the history.").
			WithCode(CodeInvalidID).WithDetail("id", id)
	}
	return nil
}

func notFound(id string) error {
	return errs.New(errs.NotFound, "The run is no longer in the run history.").
		WithCode(CodeNotFound).
		WithHint("The history keeps the last 50 runs of the project; older ones are removed.").
		WithDetail("id", id)
}

func (s *Store) path(id, ext string) string { return filepath.Join(s.dir, id+ext) }

// Save writes run r (replacing a run with its ID), then removes the
// oldest runs over the limits.
func (s *Store) Save(r report.Run) error {
	if err := checkID(r.ID); err != nil {
		return err
	}
	record, err := json.Marshal(r)
	if err != nil {
		return errs.Wrap(err, errs.Internal, "The run could not be saved in the run history.")
	}
	summary, err := json.Marshal(r.Summary)
	if err != nil {
		return errs.Wrap(err, errs.Internal, "The run could not be saved in the run history.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return ioError(err, "The run history could not be created.")
	}
	if err := atomicfile.WriteFile(s.path(r.ID, recordExt), record, 0o600); err != nil {
		return ioError(err, "The run could not be saved in the run history.")
	}
	if err := atomicfile.WriteFile(s.path(r.ID, summaryExt), summary, 0o600); err != nil {
		_ = os.Remove(s.path(r.ID, recordExt))
		return ioError(err, "The run could not be saved in the run history.")
	}
	return s.pruneLocked()
}

func ioError(err error, msg string) error {
	kind := errs.KindOf(err)
	if kind == errs.Internal {
		kind = errs.IO
	}
	return errs.Wrap(err, kind, msg).WithHint("Check that the disk is not full and that PostSonde's data folder can be written to.")
}

// entry is a listed run: its summary and the size of its record.
type entry struct {
	summary report.Summary
	bytes   int64
}

// listLocked reads the summaries, newest first, and removes records
// without one and summaries without a record.
func (s *Store) listLocked() ([]entry, error) {
	files, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ioError(err, "The run history could not be read.")
	}
	records := map[string]int64{}
	var summaries []string
	for _, f := range files {
		name := f.Name()
		switch {
		case f.IsDir() || strings.HasPrefix(name, "."): // atomicfile leftovers
		case strings.HasSuffix(name, summaryExt):
			summaries = append(summaries, strings.TrimSuffix(name, summaryExt))
		case strings.HasSuffix(name, recordExt):
			info, err := f.Info()
			if err == nil {
				records[strings.TrimSuffix(name, recordExt)] = info.Size()
			}
		}
	}
	var out []entry
	for _, id := range summaries {
		size, ok := records[id]
		delete(records, id)
		b, err := os.ReadFile(s.path(id, summaryExt))
		var sum report.Summary
		if !ok || err != nil || json.Unmarshal(b, &sum) != nil || sum.ID != id {
			// Without its record, or unreadable: not a run to list.
			_ = os.Remove(s.path(id, summaryExt))
			_ = os.Remove(s.path(id, recordExt))
			continue
		}
		out = append(out, entry{summary: sum, bytes: size})
	}
	for id := range records {
		_ = os.Remove(s.path(id, recordExt))
	}
	slices.SortFunc(out, func(a, b entry) int {
		if c := cmp.Compare(b.summary.EndedAt, a.summary.EndedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.summary.ID, a.summary.ID)
	})
	return out, nil
}

// pruneLocked removes the oldest runs over the limits; the newest stays.
func (s *Store) pruneLocked() error {
	list, err := s.listLocked()
	if err != nil {
		return err
	}
	var total int64
	var errList []error
	for i, e := range list {
		total += e.bytes
		if i == 0 || (i < s.limits.MaxRuns && total <= s.limits.MaxBytes) {
			continue
		}
		errList = append(errList, s.removeLocked(e.summary.ID))
	}
	return errors.Join(errList...)
}

// List returns the summaries of the runs kept, newest first.
func (s *Store) List() ([]report.Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.listLocked()
	if err != nil {
		return nil, err
	}
	out := make([]report.Summary, len(list))
	for i, e := range list {
		out[i] = e.summary
	}
	return out, nil
}

// Get returns run id. It fails with CodeNotFound when it is not kept.
func (s *Store) Get(id string) (report.Run, error) {
	if err := checkID(id); err != nil {
		return report.Run{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path(id, summaryExt)); err != nil {
		return report.Run{}, notFound(id)
	}
	b, err := os.ReadFile(s.path(id, recordExt))
	if errors.Is(err, fs.ErrNotExist) {
		return report.Run{}, notFound(id)
	}
	if err != nil {
		return report.Run{}, ioError(err, "The run could not be read from the run history.")
	}
	var r report.Run
	if err := json.Unmarshal(b, &r); err != nil || r.ID != id {
		return report.Run{}, errs.New(errs.NotFound, "The run in the run history is damaged and cannot be opened.").
			WithCode(CodeNotFound).WithDetail("id", id)
	}
	if r.Results == nil {
		r.Results = []report.Result{}
	}
	return r, nil
}

// Delete removes run id; an ID not kept is not an error.
func (s *Store) Delete(id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeLocked(id)
}

func (s *Store) removeLocked(id string) error {
	// The summary first: a record without one is not listed.
	var errList []error
	for _, ext := range []string{summaryExt, recordExt} {
		if err := os.Remove(s.path(id, ext)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errList = append(errList, err)
		}
	}
	if err := errors.Join(errList...); err != nil {
		return ioError(err, fmt.Sprintf("The run %s could not be removed from the run history.", id))
	}
	return nil
}

// Clear removes every run.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.listLocked()
	if err != nil {
		return err
	}
	var errList []error
	for _, e := range list {
		errList = append(errList, s.removeLocked(e.summary.ID))
	}
	return errors.Join(errList...)
}
