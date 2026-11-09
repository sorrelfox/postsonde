// Package fakeclock is a manual clock for tests of code that waits: time
// moves only when the test calls Advance, and timers that come due fire
// synchronously inside it. It has the method set of autosave.Clock.
package fakeclock

import (
	"slices"
	"sync"
	"time"
)

// Clock is a manual clock, safe for concurrent use.
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*timer
	nextID uint64
}

type timer struct {
	id  uint64
	at  time.Time
	f   func()
	off bool
}

// New returns a clock set to start.
func New(start time.Time) *Clock { return &Clock{now: start} }

// Now returns the clock's time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc schedules f for d from now. The returned function stops the
// timer and reports whether it had not fired yet.
func (c *Clock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	t := &timer{id: c.nextID, at: c.now.Add(max(d, 0)), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if t.off {
			return false
		}
		t.off = true
		c.remove(t)
		return true
	}
}

func (c *Clock) remove(t *timer) {
	c.timers = slices.DeleteFunc(c.timers, func(x *timer) bool { return x == t })
}

// Advance moves the clock forward by d, firing every timer that comes due,
// in order of due time, each with the clock set to its due time. Timers
// scheduled by a firing timer fire too if they are due by the end.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *timer
		for _, t := range c.timers {
			if !t.at.After(end) && (next == nil || t.at.Before(next.at) || (t.at.Equal(next.at) && t.id < next.id)) {
				next = t
			}
		}
		if next == nil {
			c.now = end
			c.mu.Unlock()
			return
		}
		next.off = true
		c.remove(next)
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.mu.Unlock()
		next.f()
	}
}

// Pending returns the number of timers that have not fired or been
// stopped.
func (c *Clock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}
