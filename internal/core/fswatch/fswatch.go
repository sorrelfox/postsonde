// Package fswatch notices files that change on disk by polling (STO-05).
// The standard library has no portable file notification API, so a
// Watcher calls a scan function every interval, compares the file
// metadata it returns (size, modification time, type) with the last
// snapshot it reported, and calls OnChange with the paths that were
// added, removed or modified.
//
// Changes are coalesced: once a scan differs, the Watcher waits until a
// scan finds nothing new (the files were quiet for one interval) before it
// reports, so an editor's save, a git checkout or a burst of writes arrive
// as one call. MaxWait bounds the wait when files keep changing. A file
// changed and changed back within that window is not reported.
//
// A Watcher does not know who changed a file: an owner that writes into
// the watched files itself compares what it finds with what it wrote
// (package collection keeps a hash of every file it read or wrote). See
// docs/collection.md#external-changes.
package fswatch

import (
	"io/fs"
	"slices"
	"sync"
	"time"
)

// Default intervals.
const (
	// DefaultInterval is how often a Watcher scans.
	DefaultInterval = time.Second
	// DefaultMaxWaitIntervals bounds, in intervals, how long changes that
	// keep coming are held back before they are reported anyway.
	DefaultMaxWaitIntervals = 5
)

// Stat is what a scan records about one file.
type Stat struct {
	Size    int64
	ModTime time.Time
	// Type is the file's type bits (fs.ModeDir, fs.ModeSymlink, 0 for a
	// regular file).
	Type fs.FileMode
}

// StatOf returns the Stat of fi.
func StatOf(fi fs.FileInfo) Stat {
	return Stat{Size: fi.Size(), ModTime: fi.ModTime(), Type: fi.Mode().Type()}
}

func (s Stat) equal(o Stat) bool {
	return s.Size == o.Size && s.Type == o.Type && s.ModTime.Equal(o.ModTime)
}

// Snapshot maps the slash paths of the watched files, relative to the
// watched root, to their Stat.
type Snapshot map[string]Stat

// Equal reports whether s and o list the same files with the same Stat.
func (s Snapshot) Equal(o Snapshot) bool {
	if len(s) != len(o) {
		return false
	}
	for p, st := range s {
		if ot, ok := o[p]; !ok || !st.equal(ot) {
			return false
		}
	}
	return true
}

// Changes are the paths that differ between two snapshots, each list
// sorted.
type Changes struct {
	Added    []string
	Removed  []string
	Modified []string
}

// Empty reports whether nothing changed.
func (c Changes) Empty() bool {
	return len(c.Added) == 0 && len(c.Removed) == 0 && len(c.Modified) == 0
}

// Compare returns what changed from old to next. A rename shows as one
// path removed and another added.
func Compare(old, next Snapshot) Changes {
	var c Changes
	for p, st := range next {
		ot, ok := old[p]
		switch {
		case !ok:
			c.Added = append(c.Added, p)
		case !st.equal(ot):
			c.Modified = append(c.Modified, p)
		}
	}
	for p := range old {
		if _, ok := next[p]; !ok {
			c.Removed = append(c.Removed, p)
		}
	}
	slices.Sort(c.Added)
	slices.Sort(c.Removed)
	slices.Sort(c.Modified)
	return c
}

// Clock is the time source of a Watcher; autosave.Clock and the tests'
// fakeclock.Clock have this method set.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f in its own goroutine after d and returns a function
	// that stops the timer.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// Options configure a Watcher. Scan and OnChange are required.
type Options struct {
	// Interval is the time between scans; zero means DefaultInterval.
	Interval time.Duration
	// MaxWait is the longest changes are held back while files keep
	// changing; zero means DefaultMaxWaitIntervals intervals. It is never
	// less than Interval.
	MaxWait time.Duration
	// Clock is the time source; nil means the real clock.
	Clock Clock
	// Scan lists the watched files. It should be cheap: it runs every
	// interval. An error skips the poll (the files may be half written or
	// the root briefly gone); the next one tries again.
	Scan func() (Snapshot, error)
	// OnChange is called, from the poll that noticed it and never twice at
	// once, with what changed since the previous call (or since Start).
	OnChange func(Changes)
}

// Watcher polls a set of files. Its methods are safe for concurrent use.
type Watcher struct {
	opts Options

	// pollMu serialises polls, and with them the OnChange calls; it is
	// taken before mu.
	pollMu sync.Mutex
	mu     sync.Mutex
	// base is the snapshot last reported (or taken by Start or Rebase).
	base Snapshot
	// pending is the latest snapshot that differs from base and has not
	// been reported yet; since is when the first such scan was taken.
	pending Snapshot
	since   time.Time
	running bool
	paused  bool
	stop    func() bool
	// gen identifies the scheduled timer, so one that fires after a Stop,
	// Pause or reschedule does nothing.
	gen uint64
}

// New returns a Watcher that is not started. It panics without Scan or
// OnChange.
func New(opts Options) *Watcher {
	if opts.Scan == nil || opts.OnChange == nil {
		panic("fswatch: Options.Scan and Options.OnChange are required")
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.MaxWait <= 0 {
		opts.MaxWait = DefaultMaxWaitIntervals * opts.Interval
	}
	opts.MaxWait = max(opts.MaxWait, opts.Interval)
	if opts.Clock == nil {
		opts.Clock = systemClock{}
	}
	return &Watcher{opts: opts}
}

// Start takes the first snapshot, now, and starts polling. Changes are
// reported relative to that snapshot. Starting a running Watcher does
// nothing.
func (w *Watcher) Start() {
	snap, _ := w.opts.Scan()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running {
		return
	}
	w.running, w.paused = true, false
	w.base, w.pending = snap, nil
	w.scheduleLocked(w.opts.Interval)
}

// Stop stops polling; a poll already running finishes. The Watcher can be
// started again.
func (w *Watcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.running = false
	w.cancelLocked()
}

// Pause stops polling until Resume, for a hidden window. Changes made
// meanwhile are reported after Resume.
func (w *Watcher) Pause() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.running || w.paused {
		return
	}
	w.paused = true
	w.cancelLocked()
}

// Resume polls again after Pause, starting with a poll right away.
func (w *Watcher) Resume() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.running || !w.paused {
		return
	}
	w.paused = false
	w.scheduleLocked(0)
}

// Poke schedules a poll right away, for an owner that has reason to think
// a file changed (a write that found the file not as expected). It does
// nothing while the Watcher is stopped or paused.
func (w *Watcher) Poke() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running && !w.paused {
		w.scheduleLocked(0)
	}
}

// Rebase takes a new snapshot as the reference, dropping what changed
// since the last report without reporting it: for an owner that has just
// read everything again itself.
func (w *Watcher) Rebase() {
	snap, err := w.opts.Scan()
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.base, w.pending = snap, nil
}

// Poll scans once and reports what changed when the files have settled.
// The timer calls it; tests call it directly.
func (w *Watcher) Poll() {
	w.pollMu.Lock()
	defer w.pollMu.Unlock()
	snap, err := w.opts.Scan()
	if err != nil {
		return
	}
	now := w.opts.Clock.Now()
	w.mu.Lock()
	var report *Changes
	switch {
	case w.pending == nil && snap.Equal(w.base):
		// Nothing new.
	case w.pending == nil:
		// Something changed: wait for a quiet interval before reporting.
		w.pending, w.since = snap, now
	case snap.Equal(w.pending) || now.Sub(w.since) >= w.opts.MaxWait:
		c := Compare(w.base, snap)
		w.base, w.pending = snap, nil
		report = &c
	default:
		w.pending = snap
	}
	w.mu.Unlock()
	if report != nil && !report.Empty() {
		w.opts.OnChange(*report)
	}
}

// tick is a timer's poll: it polls and schedules the next one.
func (w *Watcher) tick(gen uint64) {
	w.mu.Lock()
	current := w.running && !w.paused && w.gen == gen
	w.mu.Unlock()
	if !current {
		return
	}
	w.Poll()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running && !w.paused && w.gen == gen {
		w.scheduleLocked(w.opts.Interval)
	}
}

func (w *Watcher) scheduleLocked(d time.Duration) {
	w.cancelLocked()
	w.gen++
	gen := w.gen
	w.stop = w.opts.Clock.AfterFunc(d, func() { w.tick(gen) })
}

func (w *Watcher) cancelLocked() {
	if w.stop != nil {
		w.stop()
		w.stop = nil
	}
	w.gen++
}
