package wa

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
)

func TestBackoffDelayDoublesWithinBoundsAndCaps(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	for attempt := 0; attempt < 40; attempt++ {
		base := minBackoff
		for i := 0; i < attempt && base < maxBackoff; i++ {
			base *= 2
		}
		if base > maxBackoff {
			base = maxBackoff
		}
		for k := 0; k < 50; k++ {
			d := backoffDelay(attempt, rnd)
			if d < base/2 || d > base {
				t.Fatalf("attempt %d: delay %v outside [%v, %v]", attempt, d, base/2, base)
			}
		}
	}
	if d := backoffDelay(0, rnd); d < minBackoff/2 || d > minBackoff {
		t.Errorf("first delay %v not around 1 s", d)
	}
	if d := backoffDelay(30, rnd); d < maxBackoff/2 || d > maxBackoff {
		t.Errorf("capped delay %v not around 5 min", d)
	}
}

// waitPending blocks until the fake clock has a timer waiting, so that the
// supervisor has reached its backoff sleep.
func waitPending(t *testing.T, clk *clock.Fake) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for clk.Pending() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("supervisor did not reach a sleep")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSupervisorRetriesWithBackoffThenReconnects(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	dropped := make(chan struct{}, 1)
	var dials atomic.Int32
	var stopped atomic.Bool
	errDown := errors.New("rede fora")

	s := &supervisor{
		clk: clk, rnd: rand.New(rand.NewSource(7)), log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		dial: func(context.Context) error {
			n := dials.Add(1)
			if n <= 3 {
				return errDown
			}
			return nil
		},
		dropped: dropped,
		stop:    stopped.Load,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.run(ctx); close(done) }()

	// First failure: the wait is at least 500 ms (equal jitter on a 1 s base).
	waitPending(t, clk)
	clk.Advance(minBackoff/2 - time.Millisecond)
	if clk.Pending() != 1 {
		t.Fatal("first backoff shorter than 500 ms")
	}
	clk.Advance(minBackoff/2 + time.Millisecond) // total 1 s: the first wait is over
	// Two more failures; each wait ends once the clock moves past the cap.
	for i := 0; i < 2; i++ {
		waitPending(t, clk)
		clk.Advance(maxBackoff)
	}
	waitFor(t, func() bool { return dials.Load() == 4 })

	// Connected: the supervisor waits for a drop, not for a timer.
	time.Sleep(10 * time.Millisecond)
	if clk.Pending() != 0 {
		t.Errorf("supervisor sleeping while connected: %d timers", clk.Pending())
	}
	dropped <- struct{}{}
	waitPending(t, clk)
	clk.Advance(maxBackoff)
	waitFor(t, func() bool { return dials.Load() == 5 })

	// Logged out: the supervisor stops after the drop, without a sleep and without a dial.
	stopped.Store(true)
	dropped <- struct{}{}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not stop after logout")
	}
	if n := dials.Load(); n != 5 {
		t.Errorf("dials = %d, want 5 (no dial after logout)", n)
	}
}

func TestSupervisorStopsWhenContextEndsDuringSleep(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	ctx, cancel := context.WithCancel(context.Background())
	s := &supervisor{
		clk: clk, rnd: rand.New(rand.NewSource(3)), log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		dial:    func(context.Context) error { return errors.New("sem rede") },
		dropped: make(chan struct{}),
		stop:    func() bool { return false },
	}
	done := make(chan struct{})
	go func() { s.run(ctx); close(done) }()
	waitPending(t, clk)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}

func TestSupervisorReturnsWhenDroppedAndCancelled(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	ctx, cancel := context.WithCancel(context.Background())
	dropped := make(chan struct{})
	connected := make(chan struct{}, 1)
	s := &supervisor{
		clk: clk, rnd: rand.New(rand.NewSource(3)), log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		dial: func(context.Context) error {
			connected <- struct{}{}
			return nil
		},
		dropped: dropped,
		stop:    func() bool { return false },
	}
	done := make(chan struct{})
	go func() { s.run(ctx); close(done) }()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("no dial")
	}
	cancel() // connected and waiting for a drop: cancel must end it
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
