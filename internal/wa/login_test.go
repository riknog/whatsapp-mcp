package wa

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/ingest"
)

// syncBuffer is a bytes.Buffer safe for the adapter's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// openWith opens an unpaired adapter on a fake clock; logs go to logs.
func openWith(t *testing.T, opts Options, clk *clock.Fake, logs io.Writer) *Real {
	t.Helper()
	opts.SessionPath = t.TempDir() + "/session.db"
	opts.Clock = clk
	opts.Logger = slog.New(slog.NewTextHandler(logs, nil))
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func historyWith(convs int) *events.HistorySync {
	h := &waHistorySync.HistorySync{SyncType: waHistorySync.HistorySync_INITIAL_BOOTSTRAP.Enum()}
	for i := 0; i < convs; i++ {
		jid := types.NewJID("55119999000"+string(rune('0'+i)), types.DefaultUserServer).String()
		h.Conversations = append(h.Conversations, &waHistorySync.Conversation{
			ID: &jid,
			Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{
				Key:     &waCommon.MessageKey{RemoteJID: &jid, FromMe: boolPtr(false), ID: strPtr("h-" + string(rune('0'+i)))},
				Message: &waE2E.Message{Conversation: strPtr("oi")},
			}}},
		})
	}
	return &events.HistorySync{Data: h}
}

func boolPtr(b bool) *bool { return &b }

// stepClock advances the fake clock one second at a time. Before each step it
// lets the login goroutine reach its wait, and it stops early when done is ready.
// It returns the step number at which done fired, or 0.
func stepClock(t *testing.T, clk *clock.Fake, done <-chan error, max int, before func(step int)) (int, error) {
	t.Helper()
	for step := 1; step <= max; step++ {
		if before != nil {
			before(step)
		}
		waitPending(t, clk)
		clk.Advance(time.Second)
		select {
		case err := <-done:
			return step, err
		case <-time.After(20 * time.Millisecond):
		}
	}
	return 0, nil
}

// TestLoginSinkReceivesEventsEmittedDuringLogin: nothing emitted during the
// login is dropped; history and labels go to the sink, in order, and the
// progress lines say counts only.
func TestLoginSinkReceivesEventsEmittedDuringLogin(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	var logs syncBuffer
	r := openWith(t, Options{}, clk, &logs)
	var out syncBuffer
	var got []any
	sink := func(_ context.Context, ev any) error {
		got = append(got, ev)
		return nil
	}
	r.attach(context.Background(), &out, sink)
	r.onEvent(historyWith(2))
	r.onEvent(&events.LabelEdit{LabelID: "L-1", Action: &waSyncAction.LabelEditAction{
		Name: strPtr("Família"), Type: waSyncAction.LabelEditAction_CUSTOM.Enum()}})
	r.detach()

	if len(got) != 2 {
		t.Fatalf("sink got %d events, want 2 (none dropped)", len(got))
	}
	if h, ok := got[0].(ingest.HistorySyncEvent); !ok || len(h.Conversations) != 2 {
		t.Errorf("first event = %#v", got[0])
	}
	if l, ok := got[1].(ingest.LabelEditEvent); !ok || l.Name != "Família" {
		t.Errorf("second event = %#v", got[1])
	}
	if len(r.events) != 0 {
		t.Errorf("events leaked to Events() during login: %d", len(r.events))
	}
	if !strings.Contains(out.String(), "Recebendo histórico") {
		t.Errorf("no progress line: %q", out.String())
	}
	if strings.Contains(out.String(), "Família") || strings.Contains(out.String(), "5511") {
		t.Errorf("progress leaks content: %q", out.String())
	}
}

// TestSyncEndsWhenIdle: once paired and connected, the login ends when the sync
// has been quiet for SyncIdle, and not before.
func TestSyncEndsWhenIdle(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	var logs syncBuffer
	r := openWith(t, Options{SyncIdle: 10 * time.Second, SyncCap: 5 * time.Minute}, clk, &logs)
	var out syncBuffer
	r.attach(context.Background(), &out, func(context.Context, any) error { return nil })
	r.onEvent(&events.PairSuccess{})
	r.onEvent(&events.Connected{}) // touches at t0
	done := make(chan error, 1)
	go func() { done <- r.awaitSync(context.Background()) }()

	step, err := stepClock(t, clk, done, 30, nil)
	if step != 10 || err != nil {
		t.Fatalf("finished at step %d with %v; want step 10 (idle)", step, err)
	}
	if !strings.Contains(out.String(), "Sincronização concluída") {
		t.Errorf("no closing line: %q", out.String())
	}
}

// TestSyncEndsAtCapWhileEventsKeepComing: a sync that never goes quiet still
// ends at SyncCap.
func TestSyncEndsAtCapWhileEventsKeepComing(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	var logs syncBuffer
	r := openWith(t, Options{SyncIdle: 10 * time.Second, SyncCap: 20 * time.Second}, clk, &logs)
	var out syncBuffer
	r.attach(context.Background(), &out, func(context.Context, any) error { return nil })
	r.onEvent(&events.PairSuccess{})
	r.onEvent(&events.Connected{})
	done := make(chan error, 1)
	go func() { done <- r.awaitSync(context.Background()) }()

	touch := func(int) { r.onEvent(&events.HistorySync{}) } // a sync event every second
	step, err := stepClock(t, clk, done, 60, touch)
	if step != 20 || err != nil {
		t.Fatalf("finished at step %d with %v; want step 20 (cap)", step, err)
	}
	if !strings.Contains(out.String(), "Tempo máximo") {
		t.Errorf("cap not reported: %q", out.String())
	}
}

// TestLoginGivesUpWithoutPairing: no device paired within the pairing window.
func TestLoginGivesUpWithoutPairing(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	var logs syncBuffer
	r := openWith(t, Options{}, clk, &logs)
	var out syncBuffer
	r.attach(context.Background(), &out, func(context.Context, any) error { return nil })
	done := make(chan error, 1)
	go func() { done <- r.awaitSync(context.Background()) }()
	step, err := stepClock(t, clk, done, 200, nil)
	if err == nil || step != 120 {
		t.Fatalf("step %d err %v; want error at the 2 min pairing window", step, err)
	}
}

func TestAwaitSyncStopsOnContext(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	var logs syncBuffer
	r := openWith(t, Options{}, clk, &logs)
	r.attach(context.Background(), io.Discard, func(context.Context, any) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.awaitSync(ctx) }()
	waitPending(t, clk)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestLoginRequiresSink(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	var logs syncBuffer
	r := openWith(t, Options{}, clk, &logs)
	if err := r.Login(context.Background(), io.Discard, "", nil); err == nil {
		t.Error("Login without sink accepted")
	}
}

// TestBlockedConsumerWarnsAndLosesNothing: with Events() not read, emit waits,
// logs one warning after 10 s without content, and then delivers.
func TestBlockedConsumerWarnsAndLosesNothing(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	var logs syncBuffer
	r := openWith(t, Options{}, clk, &logs)
	for i := 0; i < cap(r.events); i++ {
		r.emit(ingest.ReceiptEvent{})
	}
	done := make(chan struct{})
	go func() {
		r.emit(ingest.ReceiptEvent{Chat: "5511999998888@s.whatsapp.net"})
		close(done)
	}()
	waitPending(t, clk)
	clk.Advance(9 * time.Second)
	if strings.Contains(logs.String(), "bloqueada") {
		t.Fatal("warned before 10 s")
	}
	clk.Advance(time.Second)
	waitForLog(t, &logs, "bloqueada")
	if strings.Contains(logs.String(), "5511999998888") {
		t.Errorf("warning carries a JID: %s", logs.String())
	}
	<-r.events // free one slot
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("emit did not deliver after the consumer read")
	}
	if n := len(r.events); n != cap(r.events) {
		t.Errorf("buffer = %d, want full again (no event lost)", n)
	}
}

func waitForLog(t *testing.T, logs *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("log %q never written: %s", want, logs.String())
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSyncWaitsForSlowSink: a history write longer than SyncIdle keeps the
// login open; it ends SyncIdle after the write finishes.
func TestSyncWaitsForSlowSink(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	var logs syncBuffer
	r := openWith(t, Options{SyncIdle: 10 * time.Second, SyncCap: 5 * time.Minute}, clk, &logs)
	var out syncBuffer
	release := make(chan struct{})
	sink := func(context.Context, any) error { <-release; return nil }
	r.attach(context.Background(), &out, sink)
	r.onEvent(&events.PairSuccess{})
	r.onEvent(&events.Connected{})
	go r.deliverLogin(context.Background(), sink, "history")
	waitInflight(r, 1)
	done := make(chan error, 1)
	go func() { done <- r.awaitSync(context.Background()) }()

	if step, _ := stepClock(t, clk, done, 30, nil); step != 0 {
		t.Fatalf("login ended at step %d while the sink was still writing", step)
	}
	close(release)
	// The write ends (and touches the sync clock) before the clock moves again,
	// so the 10 s of quiet are counted from here even on a loaded machine.
	waitInflight(r, 0)
	step, err := stepClock(t, clk, done, 30, nil)
	if err != nil || step < 10 || step > 11 {
		t.Fatalf("finished at step %d with %v; want ~10 steps after the write", step, err)
	}
}

// waitInflight waits until n sink calls are running.
func waitInflight(r *Real, n int) {
	for {
		r.loginMu.Lock()
		got := r.lg.inflight
		r.loginMu.Unlock()
		if got == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
}
