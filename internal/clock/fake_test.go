package clock

import (
	"context"
	"errors"
	"testing"
	"time"
)

var start = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func fired(ch <-chan time.Time) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestFakeNowOnlyMovesOnAdvance(t *testing.T) {
	f := NewFake(start)
	if !f.Now().Equal(start) {
		t.Fatalf("Now = %v, want %v", f.Now(), start)
	}
	f.Advance(3 * time.Second)
	if want := start.Add(3 * time.Second); !f.Now().Equal(want) {
		t.Fatalf("Now after Advance = %v, want %v", f.Now(), want)
	}
}

func TestFakeAfterFiresOnlyAfterEnoughAdvance(t *testing.T) {
	f := NewFake(start)
	ch := f.After(10 * time.Second)

	f.Advance(9 * time.Second)
	if fired(ch) {
		t.Fatal("After fired after only 9s of a 10s timer")
	}

	f.Advance(1 * time.Second)
	select {
	case got := <-ch:
		if want := start.Add(10 * time.Second); !got.Equal(want) {
			t.Fatalf("fired with %v, want %v", got, want)
		}
	default:
		t.Fatal("After did not fire at its deadline")
	}
}

func TestFakeAfterNonPositiveFiresImmediately(t *testing.T) {
	f := NewFake(start)
	if !fired(f.After(0)) {
		t.Fatal("After(0) did not fire immediately")
	}
	if !fired(f.After(-time.Second)) {
		t.Fatal("After(-1s) did not fire immediately")
	}
}

func TestFakeAdvanceFiresInDeadlineOrder(t *testing.T) {
	f := NewFake(start)
	late := f.After(5 * time.Second)
	early := f.After(2 * time.Second)
	f.Advance(5 * time.Second)
	if !fired(early) || !fired(late) {
		t.Fatal("expected both timers to fire after Advance(5s)")
	}
	if f.Pending() != 0 {
		t.Fatalf("Pending = %d, want 0", f.Pending())
	}
}

func TestFakeSleepReturnsAfterAdvance(t *testing.T) {
	f := NewFake(start)
	done := make(chan error, 1)
	go func() { done <- f.Sleep(context.Background(), time.Minute) }()

	waitForPending(t, f, 1)
	f.Advance(time.Minute)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Sleep: unexpected error %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Sleep did not return after Advance")
	}
}

func TestFakeSleepRespectsCancellation(t *testing.T) {
	f := NewFake(start)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Sleep(ctx, time.Hour) }()

	waitForPending(t, f, 1)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Sleep: got %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Sleep did not return after cancellation")
	}
	if f.Pending() != 0 {
		t.Fatalf("cancelled Sleep left %d timer(s) behind", f.Pending())
	}
}

func TestFakeSleepAlreadyCancelled(t *testing.T) {
	f := NewFake(start)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestFakeSleepNonPositiveReturnsImmediately(t *testing.T) {
	f := NewFake(start)
	if err := f.Sleep(context.Background(), 0); err != nil {
		t.Fatalf("Sleep(0): unexpected error %v", err)
	}
}

// waitForPending spins until the fake clock has n registered timers, so tests
// do not race the goroutine that is about to call Sleep or After.
func waitForPending(t *testing.T, f *Fake, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.Pending() < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d pending timer(s)", n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFakeNextReportsEarliestTimer(t *testing.T) {
	f := NewFake(time.Unix(0, 0))
	if _, ok := f.Next(); ok {
		t.Fatal("Next with no timers reported one")
	}
	f.After(5 * time.Second)
	f.After(2 * time.Second)
	if d, ok := f.Next(); !ok || d != 2*time.Second {
		t.Fatalf("Next = %v, %v; want 2s, true", d, ok)
	}
	f.Advance(2 * time.Second)
	if d, ok := f.Next(); !ok || d != 3*time.Second {
		t.Fatalf("Next after Advance = %v, %v; want 3s, true", d, ok)
	}
}
