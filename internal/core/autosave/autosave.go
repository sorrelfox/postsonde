// Package autosave writes edited values to disk a short time after they
// change (STO-03). A Saver keeps the latest unsaved value per key in
// memory, so readers see it at once, and writes it once edits pause:
// rapid updates of one key coalesce into one write. Flush and FlushAll
// write pending values now, for closing a tab, switching projects and
// quitting. A failed write keeps the value pending, so the next update or
// flush tries again. See docs/collection.md.
package autosave

import (
	"cmp"
	"errors"
	"slices"
	"sync"
	"time"
)

// Default delays. Delay matches the other debounced saves of the app
// (settings, environment variables); MaxDelay bounds how long continuous
// typing can hold a write back.
const (
	DefaultDelay    = 400 * time.Millisecond
	DefaultMaxDelay = 2 * time.Second
)

// Clock is the time source of a Saver. Tests replace it with a manual one,
// so no test depends on real sleeps.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f in its own goroutine after d and returns a function
	// that stops the timer, reporting whether it stopped it before it
	// fired, as time.AfterFunc does.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

// SystemClock returns the real clock.
func SystemClock() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// Options configure a Saver. Write is required.
type Options[K comparable, V any] struct {
	// Delay is how long after the last update a value is written. Zero
	// means DefaultDelay.
	Delay time.Duration
	// MaxDelay is the longest a value waits after the first unsaved
	// update, however often it is updated. Zero means DefaultMaxDelay; it
	// is never less than Delay.
	MaxDelay time.Duration
	// Clock is the time source; nil means SystemClock.
	Clock Clock
	// Write stores value. It is never called for the same Saver twice at
	// once.
	Write func(key K, value V) error
	// Due is called, in the timer's goroutine, when the delay of key has
	// passed. An owner that serialises writes with a lock of its own takes
	// it here and calls Flush. Nil calls Flush directly.
	Due func(key K)
	// Saved is called after value was written, Failed after writing it
	// failed. Both run before Flush returns, without the Saver's internal
	// lock: they may call Get, Put and Discard, but not Flush or FlushAll.
	Saved  func(key K, value V, at time.Time)
	Failed func(key K, value V, err error)
}

// Saver holds pending values and writes them after a delay. It is safe
// for concurrent use.
type Saver[K comparable, V any] struct {
	opts Options[K, V]

	// writeMu serialises flushes; it is taken before mu.
	writeMu sync.Mutex
	mu      sync.Mutex
	entries map[K]*entry[V]
	seq     uint64
}

type entry[V any] struct {
	value V
	// version counts updates, so a flush knows whether the value changed
	// while it was being written.
	version uint64
	// seq orders FlushAll by first update.
	seq uint64
	// first is when the oldest unsaved update was made; MaxDelay counts
	// from it.
	first time.Time
	stop  func() bool
	// gen identifies the current timer; a timer of an older generation
	// that fires anyway does nothing.
	gen    uint64
	failed bool
}

// New returns a Saver. It panics if opts.Write is nil.
func New[K comparable, V any](opts Options[K, V]) *Saver[K, V] {
	if opts.Write == nil {
		panic("autosave: Options.Write is nil")
	}
	if opts.Delay <= 0 {
		opts.Delay = DefaultDelay
	}
	if opts.MaxDelay <= 0 {
		opts.MaxDelay = DefaultMaxDelay
	}
	opts.MaxDelay = max(opts.MaxDelay, opts.Delay)
	if opts.Clock == nil {
		opts.Clock = SystemClock()
	}
	return &Saver[K, V]{opts: opts, entries: make(map[K]*entry[V])}
}

// Put makes value the pending value of key and schedules its write: Delay
// from now, but no later than MaxDelay after the first unsaved update.
func (s *Saver[K, V]) Put(key K, value V) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Clock.Now()
	e := s.entries[key]
	if e == nil {
		s.seq++
		e = &entry[V]{seq: s.seq, first: now}
		s.entries[key] = e
	}
	if e.first.IsZero() {
		e.first = now
	}
	e.value = value
	e.version++
	deadline := now.Add(s.opts.Delay)
	if limit := e.first.Add(s.opts.MaxDelay); limit.Before(deadline) {
		deadline = limit
	}
	s.scheduleLocked(key, e, max(deadline.Sub(now), 0))
}

func (s *Saver[K, V]) scheduleLocked(key K, e *entry[V], d time.Duration) {
	if e.stop != nil {
		e.stop()
	}
	e.gen++
	gen := e.gen
	e.stop = s.opts.Clock.AfterFunc(d, func() { s.fire(key, gen) })
}

func (s *Saver[K, V]) fire(key K, gen uint64) {
	s.mu.Lock()
	e := s.entries[key]
	current := e != nil && e.gen == gen
	s.mu.Unlock()
	if !current {
		return
	}
	if s.opts.Due != nil {
		s.opts.Due(key)
		return
	}
	_ = s.Flush(key)
}

// Get returns the pending value of key, if it has one.
func (s *Saver[K, V]) Get(key K) (V, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.entries[key]; e != nil {
		return e.value, true
	}
	var zero V
	return zero, false
}

// Failed reports whether the last write of key's pending value failed.
func (s *Saver[K, V]) Failed(key K) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[key]
	return e != nil && e.failed
}

// Keys returns the keys with a pending value, oldest first.
func (s *Saver[K, V]) Keys() []K {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keysLocked()
}

func (s *Saver[K, V]) keysLocked() []K {
	type item struct {
		key K
		seq uint64
	}
	items := make([]item, 0, len(s.entries))
	for k, e := range s.entries {
		items = append(items, item{k, e.seq})
	}
	slices.SortFunc(items, func(a, b item) int { return cmp.Compare(a.seq, b.seq) })
	keys := make([]K, len(items))
	for i, it := range items {
		keys[i] = it.key
	}
	return keys
}

// Discard drops the pending value of key without writing it, for an item
// that was deleted. It returns the value, if there was one.
func (s *Saver[K, V]) Discard(key K) (V, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[key]
	if e == nil {
		var zero V
		return zero, false
	}
	if e.stop != nil {
		e.stop()
	}
	delete(s.entries, key)
	return e.value, true
}

// Flush writes the pending value of key now, if it has one, and returns
// the write's error. On success the value is no longer pending, unless it
// was updated while being written; on failure it stays pending.
func (s *Saver[K, V]) Flush(key K) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	e := s.entries[key]
	if e == nil {
		s.mu.Unlock()
		return nil
	}
	if e.stop != nil {
		e.stop()
		e.stop = nil
	}
	e.gen++
	value, version := e.value, e.version
	s.mu.Unlock()

	err := s.opts.Write(key, value)
	now := s.opts.Clock.Now()

	s.mu.Lock()
	if s.entries[key] == e {
		switch {
		case err != nil:
			// Keep it for the next update or flush. MaxDelay counts again
			// from that next update, not from the first edit.
			e.failed, e.first = true, time.Time{}
		case e.version == version:
			delete(s.entries, key)
		default:
			// Updated during the write: the newer value keeps its timer.
			e.failed, e.first = false, now
		}
	}
	s.mu.Unlock()

	if err != nil {
		if s.opts.Failed != nil {
			s.opts.Failed(key, value, err)
		}
		return err
	}
	if s.opts.Saved != nil {
		s.opts.Saved(key, value, now)
	}
	return nil
}

// FlushAll flushes every pending value, oldest first, and returns the
// errors joined. One failing write does not stop the others.
func (s *Saver[K, V]) FlushAll() error {
	return s.FlushWhere(func(K) bool { return true })
}

// FlushWhere flushes the pending values whose key matches, oldest first,
// as FlushAll does.
func (s *Saver[K, V]) FlushWhere(match func(K) bool) error {
	var all []error
	for _, k := range s.Keys() {
		if match(k) {
			if err := s.Flush(k); err != nil {
				all = append(all, err)
			}
		}
	}
	return errors.Join(all...)
}
