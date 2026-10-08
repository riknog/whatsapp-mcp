package clock

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Fake is a manually advanced Clock for tests. Time only moves on Advance.
// It is safe for concurrent use.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	deadline time.Time
	ch       chan time.Time // buffered, capacity 1
}

// NewFake returns a Fake clock that starts at start.
func NewFake(start time.Time) *Fake {
	return &Fake{now: start}
}

// Now returns the fake current time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// After returns a channel that fires once the fake clock reaches now+d via Advance.
// A non-positive d fires immediately.
func (f *Fake) After(d time.Duration) <-chan time.Time {
	_, ch := f.addTimer(d)
	return ch
}

// Sleep blocks until Advance moves the clock past d, or until ctx is done.
func (f *Fake) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t, ch := f.addTimer(d)
	select {
	case <-ctx.Done():
		f.removeTimer(t)
		return ctx.Err()
	case <-ch:
		return nil
	}
}

// Advance moves the fake time forward by d and fires every timer whose deadline
// has been reached, in deadline order. Advance with d <= 0 only fires due timers.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d > 0 {
		f.now = f.now.Add(d)
	}
	sort.SliceStable(f.timers, func(i, j int) bool {
		return f.timers[i].deadline.Before(f.timers[j].deadline)
	})
	remaining := f.timers[:0]
	for _, t := range f.timers {
		if t.deadline.After(f.now) {
			remaining = append(remaining, t)
			continue
		}
		t.ch <- f.now
	}
	f.timers = remaining
}

// Pending returns how many timers are waiting to fire. Useful in tests to know
// that a goroutine has reached its Sleep or After call.
func (f *Fake) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}

func (f *Fake) addTimer(d time.Duration) (*fakeTimer, <-chan time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{deadline: f.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.ch <- f.now
		return t, t.ch
	}
	f.timers = append(f.timers, t)
	return t, t.ch
}

func (f *Fake) removeTimer(target *fakeTimer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, t := range f.timers {
		if t == target {
			f.timers = append(f.timers[:i], f.timers[i+1:]...)
			return
		}
	}
}

// Next returns how long until the earliest pending timer fires, and false when
// no timer is pending. Tests use it to fire timers one at a time, exactly at
// their deadlines.
func (f *Fake) Next() (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.timers) == 0 {
		return 0, false
	}
	first := f.timers[0].deadline
	for _, t := range f.timers[1:] {
		if t.deadline.Before(first) {
			first = t.deadline
		}
	}
	return first.Sub(f.now), true
}
