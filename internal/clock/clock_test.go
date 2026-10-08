package clock

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRealSleepReturnsNilAfterDuration(t *testing.T) {
	if err := (Real{}).Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("Sleep: unexpected error %v", err)
	}
}

func TestRealSleepRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (Real{}).Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sleep with cancelled ctx: got %v, want context.Canceled", err)
	}
}

func TestRealSleepNonPositiveReturnsImmediately(t *testing.T) {
	if err := (Real{}).Sleep(context.Background(), 0); err != nil {
		t.Fatalf("Sleep(0): unexpected error %v", err)
	}
}

func TestRealNowAndAfter(t *testing.T) {
	var c Real
	if c.Now().IsZero() {
		t.Fatal("Now returned zero time")
	}
	select {
	case <-c.After(time.Millisecond):
	case <-time.After(5 * time.Second):
		t.Fatal("After did not fire")
	}
}
