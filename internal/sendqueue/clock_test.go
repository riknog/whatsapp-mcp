package sendqueue

import (
	"context"
	"runtime"
	"sync"
	"time"
)

// tclock is a test clock that fires each timer exactly at its deadline. Time
// moves only when a waiter fires or Advance is called, never by a wall-clock
// step, so every measured interval equals the duration the code asked for.
// It implements clock.Clock.
type tclock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*twaiter
}

type twaiter struct {
	at time.Time
	ch chan time.Time
}

// shortWait bounds the timers the driver fires on its own. Worker sleeps are at
// most 15 s; Submit's wait timer is far longer, so Submit waits are left to the test.
const shortWait = 60 * time.Second

func newTClock(start time.Time) *tclock { return &tclock{now: start} }

// Now returns the current test time.
func (c *tclock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After returns a channel that fires at now+d.
func (c *tclock) After(d time.Duration) <-chan time.Time {
	_, ch := c.add(d)
	return ch
}

// Sleep blocks until the clock reaches now+d or ctx ends.
func (c *tclock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w, ch := c.add(d)
	select {
	case <-ctx.Done():
		c.remove(w)
		return ctx.Err()
	case <-ch:
		return nil
	}
}

func (c *tclock) add(d time.Duration) (*twaiter, chan time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := &twaiter{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		w.ch <- c.now
		return w, w.ch
	}
	c.waiters = append(c.waiters, w)
	return w, w.ch
}

func (c *tclock) remove(target *twaiter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, w := range c.waiters {
		if w == target {
			c.waiters = append(c.waiters[:i], c.waiters[i+1:]...)
			return
		}
	}
}

// fireLocked fires the earliest waiter due at or before limit, moving time to
// its deadline. It reports whether one fired. The caller holds c.mu.
func (c *tclock) fireLocked(limit time.Time) bool {
	idx := -1
	for i, w := range c.waiters {
		if !w.at.After(limit) && (idx < 0 || w.at.Before(c.waiters[idx].at)) {
			idx = i
		}
	}
	if idx < 0 {
		return false
	}
	w := c.waiters[idx]
	c.waiters = append(c.waiters[:idx], c.waiters[idx+1:]...)
	if w.at.After(c.now) {
		c.now = w.at
	}
	w.ch <- c.now
	return true
}

// step fires the earliest waiter due within d of now, if any.
func (c *tclock) step(d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fireLocked(c.now.Add(d))
}

// Advance moves time forward by d, firing every waiter that comes due on the way.
func (c *tclock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	target := c.now.Add(d)
	for c.fireLocked(target) {
	}
	c.now = target
}

// driveClock lets the worker run while the test waits: whenever a short timer
// is pending it fires that timer at its exact deadline. Stop it with the
// returned function.
func driveClock(c *tclock) func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			default:
			}
			if !c.step(shortWait) {
				runtime.Gosched()
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}
