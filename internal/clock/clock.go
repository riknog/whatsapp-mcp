// Package clock abstracts time so that cooldowns and timeouts can be tested
// without real waiting. Production code uses Real; tests use Fake.
package clock

import (
	"context"
	"time"
)

// Clock is the time source injected into any component with cooldowns or timeouts.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// After returns a channel that receives the time once d has elapsed.
	After(d time.Duration) <-chan time.Time
	// Sleep blocks for d or until ctx is done, whichever comes first.
	// It returns ctx.Err() if the context ends first, nil otherwise.
	Sleep(ctx context.Context, d time.Duration) error
}

// Real is the wall-clock implementation of Clock.
type Real struct{}

// Now returns time.Now().
func (Real) Now() time.Time { return time.Now() }

// After wraps time.After.
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Sleep waits for d or for ctx cancellation.
func (Real) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
