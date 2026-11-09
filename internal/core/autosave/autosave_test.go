package autosave_test

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/autosave"
	"github.com/sorrelfox/postsonde/internal/testutil/fakeclock"
)

var start = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

// recorder is a Write, Saved and Failed that records what happened.
type recorder struct {
	mu     sync.Mutex
	writes []string // "key=value"
	saved  []string
	failed []string
	fail   map[string]error
}

func (r *recorder) options(clock autosave.Clock) autosave.Options[string, string] {
	return autosave.Options[string, string]{
		Delay:    400 * time.Millisecond,
		MaxDelay: 2 * time.Second,
		Clock:    clock,
		Write: func(k, v string) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.writes = append(r.writes, k+"="+v)
			return r.fail[k]
		},
		Saved: func(k, v string, _ time.Time) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.saved = append(r.saved, k+"="+v)
		},
		Failed: func(k, v string, _ error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.failed = append(r.failed, k+"="+v)
		},
	}
}

func (r *recorder) check(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

func newSaver(t *testing.T) (*autosave.Saver[string, string], *fakeclock.Clock, *recorder) {
	t.Helper()
	clock := fakeclock.New(start)
	r := &recorder{fail: map[string]error{}}
	return autosave.New(r.options(clock)), clock, r
}

func TestCoalescesRapidUpdates(t *testing.T) {
	t.Parallel()
	s, clock, r := newSaver(t)
	for _, v := range []string{"a", "ab", "abc"} {
		s.Put("req", v)
		clock.Advance(100 * time.Millisecond)
	}
	if got, ok := s.Get("req"); !ok || got != "abc" {
		t.Fatalf("Get = %q, %v; want the latest value pending", got, ok)
	}
	r.check(t, "writes before the delay", r.writes)
	clock.Advance(300 * time.Millisecond) // 400 ms after the last update
	r.check(t, "writes", r.writes, "req=abc")
	r.check(t, "saved", r.saved, "req=abc")
	if _, ok := s.Get("req"); ok {
		t.Fatal("value still pending after a successful write")
	}
	if clock.Pending() != 0 {
		t.Fatalf("%d timers left", clock.Pending())
	}
}

func TestWritesWithinDelay(t *testing.T) {
	t.Parallel()
	s, clock, r := newSaver(t)
	s.Put("req", "v1")
	clock.Advance(399 * time.Millisecond)
	r.check(t, "writes at 399 ms", r.writes)
	clock.Advance(time.Millisecond)
	r.check(t, "writes at 400 ms", r.writes, "req=v1")
}

func TestMaxDelayBoundsContinuousTyping(t *testing.T) {
	t.Parallel()
	s, clock, r := newSaver(t)
	// An update every 300 ms never lets the 400 ms delay pass.
	for i := range 10 {
		s.Put("req", string(rune('a'+i)))
		clock.Advance(300 * time.Millisecond)
	}
	// Updates at 0, 300, …, 2700 ms: the first write is due 2 s after the
	// first update, then 2 s after the write.
	r.check(t, "writes", r.writes, "req=g")
	clock.Advance(time.Second)
	r.check(t, "writes", r.writes, "req=g", "req=j")
}

func TestKeysAreIndependent(t *testing.T) {
	t.Parallel()
	s, clock, r := newSaver(t)
	s.Put("a", "1")
	clock.Advance(200 * time.Millisecond)
	s.Put("b", "2")
	clock.Advance(200 * time.Millisecond)
	r.check(t, "writes", r.writes, "a=1")
	clock.Advance(200 * time.Millisecond)
	r.check(t, "writes", r.writes, "a=1", "b=2")
}

func TestFlushAllWritesEverythingPending(t *testing.T) {
	t.Parallel()
	s, clock, r := newSaver(t)
	s.Put("b", "2")
	s.Put("a", "1")
	s.Put("c", "3")
	if err := s.Flush("a"); err != nil {
		t.Fatal(err)
	}
	r.check(t, "writes after Flush(a)", r.writes, "a=1")
	if err := s.FlushAll(); err != nil {
		t.Fatal(err)
	}
	r.check(t, "writes", r.writes, "a=1", "b=2", "c=3")
	if keys := s.Keys(); len(keys) != 0 {
		t.Fatalf("Keys = %q after FlushAll", keys)
	}
	// The stopped timers do not write again.
	clock.Advance(time.Minute)
	r.check(t, "writes after the delay", r.writes, "a=1", "b=2", "c=3")
	if err := s.Flush("missing"); err != nil {
		t.Fatalf("Flush of nothing pending = %v", err)
	}
}

func TestFailedWriteStaysPendingForRetry(t *testing.T) {
	t.Parallel()
	s, clock, r := newSaver(t)
	boom := errors.New("disk full")
	r.fail["req"] = boom
	s.Put("req", "v1")
	s.Put("ok", "x")
	clock.Advance(400 * time.Millisecond)
	r.check(t, "failed", r.failed, "req=v1")
	r.check(t, "saved", r.saved, "ok=x")
	if v, ok := s.Get("req"); !ok || v != "v1" || !s.Failed("req") {
		t.Fatalf("Get = %q, %v, Failed = %v; want v1 pending and failed", v, ok, s.Failed("req"))
	}
	// No retry loop on a timer: it waits for an update or a flush.
	clock.Advance(time.Minute)
	r.check(t, "writes", r.writes, "req=v1", "ok=x")

	if err := s.FlushAll(); !errors.Is(err, boom) {
		t.Fatalf("FlushAll = %v, want %v", err, boom)
	}
	delete(r.fail, "req")
	s.Put("req", "v2")
	clock.Advance(400 * time.Millisecond)
	r.check(t, "saved", r.saved, "ok=x", "req=v2")
	if s.Failed("req") || len(s.Keys()) != 0 {
		t.Fatal("still pending after a successful retry")
	}
}

func TestUpdateDuringWriteKeepsNewerValue(t *testing.T) {
	t.Parallel()
	clock := fakeclock.New(start)
	var s *autosave.Saver[string, string]
	var writes []string
	s = autosave.New(autosave.Options[string, string]{
		Clock: clock,
		Write: func(k, v string) error {
			writes = append(writes, v)
			if v == "v1" {
				s.Put(k, "v2") // an edit arrives while v1 is being written
			}
			return nil
		},
	})
	s.Put("req", "v1")
	if err := s.Flush("req"); err != nil {
		t.Fatal(err)
	}
	if v, ok := s.Get("req"); !ok || v != "v2" {
		t.Fatalf("Get = %q, %v; want v2 still pending", v, ok)
	}
	clock.Advance(autosave.DefaultDelay)
	if !slices.Equal(writes, []string{"v1", "v2"}) {
		t.Fatalf("writes = %q", writes)
	}
}

func TestDiscard(t *testing.T) {
	t.Parallel()
	s, clock, r := newSaver(t)
	s.Put("req", "v1")
	if v, ok := s.Discard("req"); !ok || v != "v1" {
		t.Fatalf("Discard = %q, %v", v, ok)
	}
	clock.Advance(time.Minute)
	r.check(t, "writes", r.writes)
	if _, ok := s.Discard("req"); ok {
		t.Fatal("second Discard found a value")
	}
}

func TestDueHook(t *testing.T) {
	t.Parallel()
	clock := fakeclock.New(start)
	var owner sync.Mutex
	var due []string
	var s *autosave.Saver[string, string]
	s = autosave.New(autosave.Options[string, string]{
		Clock: clock,
		Write: func(string, string) error {
			if owner.TryLock() {
				t.Error("write ran without the owner's lock")
				owner.Unlock()
			}
			return nil
		},
		Due: func(k string) {
			owner.Lock()
			defer owner.Unlock()
			due = append(due, k)
			_ = s.Flush(k)
		},
	})
	s.Put("req", "v")
	clock.Advance(autosave.DefaultDelay)
	if !slices.Equal(due, []string{"req"}) || len(s.Keys()) != 0 {
		t.Fatalf("due = %q, keys = %q", due, s.Keys())
	}
}

func TestSystemClockConcurrent(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	written := map[string]string{}
	s := autosave.New(autosave.Options[string, string]{
		Delay: time.Millisecond,
		Write: func(k, v string) error {
			mu.Lock()
			defer mu.Unlock()
			written[k] = v
			return nil
		},
	})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			key := string(rune('a' + i))
			for j := range 50 {
				s.Put(key, key+string(rune('0'+j%10)))
			}
			s.Put(key, "last")
		})
	}
	wg.Wait()
	if err := s.FlushAll(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for i := range 8 {
		if k := string(rune('a' + i)); written[k] != "last" {
			t.Errorf("%s = %q, want last", k, written[k])
		}
	}
}

func TestNewPanicsWithoutWrite(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	autosave.New(autosave.Options[string, int]{})
}
