package sendqueue

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/config"
	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/toolerr"
	"github.com/riknog/whatsapp-mcp/internal/wa"
)

// testStart is 2026-10-07 12:00 UTC, outside no quiet window by default.
var testStart = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

var (
	jidA     = wa.JID("5511111111111@s.whatsapp.net")
	jidB     = wa.JID("5511222222222@s.whatsapp.net")
	jidC     = wa.JID("5511333333333@s.whatsapp.net")
	jidNoNum = wa.JID("5511444444444@s.whatsapp.net")
	jidGroup = wa.JID("120363000000000000@g.us")
)

func testConfig() config.SendConfig {
	cfg := config.Defaults().Send
	cfg.WaitTimeoutS = 3600 // Submit waits for the send; tests never hit the timeout
	return cfg
}

type harness struct {
	t   *testing.T
	clk *tclock
	fk  *wa.Fake
	st  *memStore
	q   *Queue
}

// newHarness builds a started queue. With drive set, the fake clock runs on its
// own so that Submit calls return; without it, time stands still until Advance.
func newHarness(t *testing.T, mutate func(*config.SendConfig), drive bool) *harness {
	t.Helper()
	h := newUnstartedHarness(t, mutate)
	h.start(drive)
	return h
}

// newUnstartedHarness builds the queue without starting its worker, so a test
// can write history to the store before the worker can see any queued item.
func newUnstartedHarness(t *testing.T, mutate func(*config.SendConfig)) *harness {
	t.Helper()
	cfg := testConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	clk := newTClock(testStart)
	fk := wa.NewFake(clk)
	st := newMemStore(clk)
	q := New(cfg, st, fk, clk, rand.New(rand.NewSource(7)), nil)
	return &harness{t: t, clk: clk, fk: fk, st: st, q: q}
}

// start starts the worker, and the clock driver when drive is set.
func (h *harness) start(drive bool) {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.t.Cleanup(cancel)
	if err := h.q.Start(ctx); err != nil {
		h.t.Fatalf("Start: %v", err)
	}
	if drive {
		h.t.Cleanup(driveClock(h.clk))
	}
}

// submit calls Submit and fails the test if it does not return in real time
// (a watchdog only; all waiting is on the fake clock).
func (h *harness) submit(r Request) (Result, error) {
	h.t.Helper()
	type res struct {
		r   Result
		err error
	}
	ch := make(chan res, 1)
	go func() {
		out, err := h.q.Submit(context.Background(), r)
		ch <- res{out, err}
	}()
	select {
	case x := <-ch:
		return x.r, x.err
	case <-time.After(20 * time.Second):
		h.t.Fatal("Submit não retornou")
		return Result{}, nil
	}
}

func (h *harness) mustSend(r Request) Result {
	h.t.Helper()
	res, err := h.submit(r)
	if err != nil {
		h.t.Fatalf("Submit: %v", err)
	}
	if res.Status != StatusSent {
		h.t.Fatalf("status = %q, want sent", res.Status)
	}
	return res
}

func txt(chat wa.JID, ref, text string) Request {
	return Request{Chat: chat, ChatRef: ref, Kind: store.KindText, Text: text}
}

func card(chat wa.JID, ref string, shared wa.JID, name string) Request {
	return Request{Chat: chat, ChatRef: ref, Kind: store.KindContact, SharedJID: shared, ContactName: name}
}

func requireCode(t *testing.T, err error, code toolerr.Code) toolerr.Error {
	t.Helper()
	var te toolerr.Error
	if !errors.As(err, &te) {
		t.Fatalf("err = %v (%T), want toolerr %s", err, err, code)
	}
	if te.Code != code {
		t.Fatalf("code = %s, want %s (%s)", te.Code, code, te.Message)
	}
	return te
}

func typingEvents(fk *wa.Fake) []wa.PresenceEvent {
	var out []wa.PresenceEvent
	for _, p := range fk.Presence() {
		if p.Typing {
			out = append(out, p)
		}
	}
	return out
}

func within(t *testing.T, name string, d, lo, hi time.Duration) {
	t.Helper()
	if d < lo || d > hi {
		t.Errorf("%s = %v, want in [%v, %v]", name, d, lo, hi)
	}
}

func TestBurstTenThenCooldown(t *testing.T) {
	h := newHarness(t, nil, true)
	for i := 1; i <= 12; i++ {
		h.mustSend(txt(jidA, "ref-a", fmt.Sprintf("mensagem %d", i)))
	}
	sent, typ := h.fk.Sent(), typingEvents(h.fk)
	if len(sent) != 12 || len(typ) != 12 {
		t.Fatalf("sent=%d typing=%d, want 12 and 12", len(sent), len(typ))
	}
	for i := range sent {
		// "mensagem N" is 10-11 characters: 40 ms each is under the 600 ms floor.
		within(t, fmt.Sprintf("typing before %d", i+1), sent[i].At.Sub(typ[i].At), 480*time.Millisecond, 720*time.Millisecond)
	}
	within(t, "cooldown before 11th", typ[10].At.Sub(sent[9].At), 8*time.Second, 15*time.Second)
	for _, i := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 11} {
		if d := typ[i].At.Sub(sent[i-1].At); d != 0 {
			t.Errorf("message %d waited %v after the previous one, want only typing", i+1, d)
		}
	}
}

func TestSwitchCooldownBetweenRecipients(t *testing.T) {
	h := newHarness(t, nil, true)
	h.mustSend(txt(jidA, "a", "um"))
	h.mustSend(txt(jidB, "b", "dois"))
	h.mustSend(txt(jidA, "a", "tres"))
	sent, typ := h.fk.Sent(), typingEvents(h.fk)
	within(t, "switch A->B", typ[1].At.Sub(sent[0].At), 1500*time.Millisecond, 3500*time.Millisecond)
	within(t, "switch B->A", typ[2].At.Sub(sent[1].At), 1500*time.Millisecond, 3500*time.Millisecond)
}

func TestConcurrentSubmitsKeepArrivalOrder(t *testing.T) {
	h := newHarness(t, func(c *config.SendConfig) {
		c.MaxQueue = 50
		c.MaxPerMinute = 1000
		c.MaxPerHour = 1000
		c.MaxPerDay = 1000
	}, true)
	chats := []wa.JID{jidA, jidB, jidC, jidNoNum, wa.JID("5511555555555@s.whatsapp.net")}
	var wg sync.WaitGroup
	errs := make(chan error, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := h.q.Submit(context.Background(), txt(chats[i%5], "r", fmt.Sprintf("msg %02d", i)))
			if err != nil {
				errs <- err
			}
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("submits não terminaram")
	}
	close(errs)
	for err := range errs {
		t.Errorf("Submit: %v", err)
	}
	sent := h.fk.Sent()
	if len(sent) != 30 {
		t.Fatalf("sent %d, want 30", len(sent))
	}
	seen := map[string]bool{}
	for k, s := range sent {
		if seen[s.Text] {
			t.Errorf("duplicate send %q", s.Text)
		}
		seen[s.Text] = true
		// Ids are assigned in admission order, so the k-th send must be item k+1.
		it, ok := h.st.GetSend(int64(k + 1))
		if !ok || it.Text != s.Text || it.ChatJID != string(s.Chat) {
			t.Fatalf("send %d = %q, want queue item %d (%q)", k, s.Text, k+1, it.Text)
		}
	}
}

func TestMinuteCapCountsSentAndResetsAfterWindow(t *testing.T) {
	h := newHarness(t, nil, true)
	for i := 1; i <= 20; i++ {
		h.mustSend(txt(jidA, "a", fmt.Sprintf("m%d", i)))
	}
	_, err := h.submit(txt(jidA, "a", "m21"))
	te := requireCode(t, err, toolerr.CodeRateLimited)
	if got, _ := te.Details["retry_after_s"].(int64); got != 60 {
		t.Errorf("retry_after_s = %v, want 60", te.Details["retry_after_s"])
	}
	h.clk.Advance(90 * time.Second)
	h.mustSend(txt(jidA, "a", "m21"))
}

func TestNewRecipientsLimitPerHour(t *testing.T) {
	h := newHarness(t, nil, true)
	chats := make([]wa.JID, 0, 16)
	for i := 0; i < 16; i++ {
		chats = append(chats, wa.JID(fmt.Sprintf("551100000%04d@s.whatsapp.net", i)))
	}
	for _, c := range chats[:15] {
		h.mustSend(txt(c, "r", "oi"))
	}
	_, err := h.submit(txt(chats[15], "r", "oi"))
	te := requireCode(t, err, toolerr.CodeRateLimited)
	if got, _ := te.Details["retry_after_s"].(int64); got != 3600 {
		t.Errorf("retry_after_s = %v, want 3600", te.Details["retry_after_s"])
	}
	// A recipient already reached this hour is not new.
	h.mustSend(txt(chats[0], "r", "outra"))
	h.clk.Advance(3601 * time.Second)
	h.mustSend(txt(chats[15], "r", "oi"))
}

func TestDuplicateWithinSixtySeconds(t *testing.T) {
	h := newHarness(t, nil, true)
	h.mustSend(txt(jidA, "ref-a", "oi, tudo bem?"))
	_, err := h.submit(txt(jidA, "ref-a", "  OI,   tudo bem? "))
	requireCode(t, err, toolerr.CodeDuplicateMessage)
	h.mustSend(txt(jidB, "ref-b", "oi, tudo bem?"))
	h.clk.Advance(61 * time.Second)
	h.mustSend(txt(jidA, "ref-a", "oi, tudo bem?"))

	for _, a := range h.st.Audits() {
		if strings.Contains(a.detail, "tudo") || strings.Contains(a.ref, "5511") {
			t.Errorf("audit row leaks content or id: %+v", a)
		}
	}
	found := false
	for _, a := range h.st.Audits() {
		if a.action == "send" && a.ref == "ref-a" && a.detail == "rejected duplicate_message" {
			found = true
		}
	}
	if !found {
		t.Errorf("rejected duplicate not audited: %+v", h.st.Audits())
	}
}

func TestQueueFullAtTwentyAndPositions(t *testing.T) {
	h := newHarness(t, nil, false) // no driver: the first send parks in its typing wait
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	var lastETA time.Duration
	for i := 1; i <= 20; i++ {
		res, err := h.q.Submit(canceled, txt(jidA, "a", fmt.Sprintf("fila %d", i)))
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		if res.Status != StatusQueued || res.QueuePosition != i {
			t.Fatalf("submit %d: status=%s pos=%d, want queued at %d", i, res.Status, res.QueuePosition, i)
		}
		if res.ETA <= lastETA {
			t.Errorf("ETA %v not increasing (prev %v)", res.ETA, lastETA)
		}
		lastETA = res.ETA
	}
	_, err := h.q.Submit(canceled, txt(jidA, "a", "fila 21"))
	requireCode(t, err, toolerr.CodeQueueFull)
	if s := h.q.Stats(); s.Pending != 20 {
		t.Errorf("Stats.Pending = %d, want 20", s.Pending)
	}
}

func TestNetworkErrorRetriedOnce(t *testing.T) {
	h := newHarness(t, nil, true)
	h.fk.FailSendText(wa.ErrNetwork, nil)
	start := h.clk.Now()
	h.mustSend(txt(jidA, "a", "rede"))
	if n := h.fk.Attempts(); n != 2 {
		t.Fatalf("attempts = %d, want 2", n)
	}
	within(t, "retry delay", h.fk.Sent()[0].At.Sub(start), 5*time.Second, 5*time.Second+10*time.Second)
}

func TestNetworkErrorTwiceFailsWithoutThirdAttempt(t *testing.T) {
	h := newHarness(t, nil, true)
	h.fk.FailSendText(wa.ErrNetwork, wa.ErrNetwork)
	_, err := h.submit(txt(jidA, "a", "rede2"))
	if !errors.Is(err, ErrSendFailed) {
		t.Fatalf("err = %v, want ErrSendFailed", err)
	}
	h.clk.Advance(2 * time.Minute)
	if n := h.fk.Attempts(); n != 2 {
		t.Fatalf("attempts = %d, want 2", n)
	}
	if got := h.st.Statuses(); len(got) != 1 || got[0] != store.StatusFailed {
		t.Fatalf("statuses = %v, want [failed]", got)
	}
	if a := h.st.Audits(); len(a) == 0 || a[len(a)-1].detail != "error network" {
		t.Errorf("audit = %+v, want error network", a)
	}
}

func TestPermanentErrorIsNotRetried(t *testing.T) {
	h := newHarness(t, nil, true)
	h.fk.FailSendText(errors.New("recusado pelo servidor"))
	_, err := h.submit(txt(jidA, "a", "recusada"))
	if !errors.Is(err, ErrSendFailed) {
		t.Fatalf("err = %v, want ErrSendFailed", err)
	}
	if n := h.fk.Attempts(); n != 1 {
		t.Fatalf("attempts = %d, want 1", n)
	}
}

func TestPresenceErrorDoesNotBlockSend(t *testing.T) {
	h := newHarness(t, nil, true)
	h.fk.SetPresenceError(errors.New("presença indisponível"))
	h.mustSend(txt(jidA, "a", "sem presença"))
}

func TestTypingIndicatorOffSendsWithoutPresence(t *testing.T) {
	h := newHarness(t, func(c *config.SendConfig) { c.TypingIndicator = false }, true)
	h.mustSend(txt(jidA, "a", "direto"))
	if p := h.fk.Presence(); len(p) != 0 {
		t.Errorf("presence calls = %d, want 0", len(p))
	}
}

func TestValidationRejections(t *testing.T) {
	h := newHarness(t, func(c *config.SendConfig) { c.AllowGroups = false }, false)
	cases := []struct {
		name string
		req  Request
		code toolerr.Code
	}{
		{"empty chat", txt("", "a", "x"), toolerr.CodeInvalidArgument},
		{"empty text", txt(jidA, "a", "   "), toolerr.CodeInvalidArgument},
		{"too long", txt(jidA, "a", strings.Repeat("á", MaxTextRunes+1)), toolerr.CodeMessageTooLong},
		{"group disabled", txt(jidGroup, "g", "oi"), toolerr.CodeGroupSendDisabled},
		{"broadcast", txt("status@broadcast", "s", "oi"), toolerr.CodeInvalidArgument},
		{"bad kind", Request{Chat: jidA, Kind: "video"}, toolerr.CodeInvalidArgument},
		{"card without name", card(jidA, "a", jidB, "  "), toolerr.CodeInvalidArgument},
		{"card without shared", card(jidA, "a", "", "Nome"), toolerr.CodeInvalidArgument},
		{"card without phone", card(jidA, "a", jidNoNum, "Nome"), toolerr.CodeInvalidArgument},
		{"card to group", card(jidGroup, "g", jidB, "Nome"), toolerr.CodeGroupSendDisabled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := h.submit(c.req)
			requireCode(t, err, c.code)
		})
	}
	if n := h.st.Statuses(); len(n) != 0 {
		t.Errorf("rejected requests were enqueued: %v", n)
	}
}

func TestSendDisabled(t *testing.T) {
	h := newHarness(t, func(c *config.SendConfig) { c.Enabled = false }, false)
	_, err := h.submit(txt(jidA, "a", "oi"))
	requireCode(t, err, toolerr.CodeSendDisabled)
}

func TestMaxLengthAccepted(t *testing.T) {
	h := newHarness(t, nil, true)
	h.mustSend(txt(jidA, "a", strings.Repeat("a", MaxTextRunes)))
}

func TestQuietHours(t *testing.T) {
	h := newHarness(t, func(c *config.SendConfig) { c.QuietHours = "12:00-13:00" }, false)
	_, err := h.submit(txt(jidA, "a", "oi"))
	requireCode(t, err, toolerr.CodeQuietHours)

	bad := newHarness(t, func(c *config.SendConfig) { c.QuietHours = "das 22 às 8" }, false)
	_, err = bad.submit(txt(jidA, "a", "oi"))
	requireCode(t, err, toolerr.CodeInvalidArgument)
}

func TestContactShareUsesSendContactAndDedup(t *testing.T) {
	h := newHarness(t, nil, true)
	h.fk.SetPhone(jidB, "+55 (11) 99999-8888")
	h.mustSend(card(jidA, "ref-a", jidB, "Fulana; Silva"))

	cards := h.fk.Contacts()
	if len(cards) != 1 {
		t.Fatalf("contact sends = %d, want 1", len(cards))
	}
	c := cards[0]
	if c.DisplayName != "Fulana; Silva" {
		t.Errorf("display name = %q", c.DisplayName)
	}
	if !strings.Contains(c.VCard, "waid=5511999998888:+5511999998888") || !strings.Contains(c.VCard, `FN:Fulana\; Silva`) {
		t.Errorf("vCard = %q", c.VCard)
	}
	if n := len(h.fk.Sent()); n != 0 {
		t.Errorf("text sends = %d, want 0", n)
	}

	_, err := h.submit(card(jidA, "ref-a", jidB, "Fulana; Silva"))
	requireCode(t, err, toolerr.CodeDuplicateMessage)
	h.mustSend(card(jidC, "ref-c", jidB, "Fulana; Silva"))

	found := false
	for _, a := range h.st.Audits() {
		if a.action == "share_contact" && a.detail == "ok" {
			found = true
		}
	}
	if !found {
		t.Errorf("share not audited: %+v", h.st.Audits())
	}
}

func TestStartExpiresLeftoversAndWorkerStartsClean(t *testing.T) {
	clk := newTClock(testStart)
	fk := wa.NewFake(clk)
	st := newMemStore(clk)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := st.EnqueueSend(ctx, store.SendItem{ChatJID: string(jidA), Kind: store.KindText, Text: fmt.Sprint("antiga ", i)}); err != nil {
			t.Fatal(err)
		}
	}
	q := New(testConfig(), st, fk, clk, rand.New(rand.NewSource(1)), nil)
	stop := driveClock(clk)
	defer stop()
	if err := q.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, s := range st.Statuses() {
		if s != store.StatusExpired {
			t.Fatalf("status = %s, want expired", s)
		}
	}
	clk.Advance(time.Minute)
	if n := len(fk.Sent()); n != 0 {
		t.Fatalf("old items were sent: %d", n)
	}
	if _, err := q.Submit(ctx, txt(jidA, "a", "nova")); err != nil {
		t.Fatalf("Submit after Start: %v", err)
	}
}

func TestLifecycleErrors(t *testing.T) {
	clk := newTClock(testStart)
	st := newMemStore(clk)
	q := New(testConfig(), st, wa.NewFake(clk), clk, nil, nil)
	if _, err := q.Submit(context.Background(), txt(jidA, "a", "x")); err == nil {
		t.Fatal("Submit before Start: want error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := q.Start(ctx); err == nil {
		t.Fatal("second Start: want error")
	}
}

func TestStatsReportsSentCounts(t *testing.T) {
	h := newHarness(t, nil, true)
	h.mustSend(txt(jidA, "a", "um"))
	h.mustSend(txt(jidB, "b", "dois"))
	s := h.q.Stats()
	if s.Pending != 0 || s.SentLastHour != 2 || s.SentLastDay != 2 || s.RecipientsLastHour != 2 {
		t.Errorf("Stats = %+v", s)
	}
}

func TestStoreFailureIsNotAToolError(t *testing.T) {
	clk := newTClock(testStart)
	st := &failingStore{memStore: newMemStore(clk)}
	fk := wa.NewFake(clk)
	q := New(testConfig(), st, fk, clk, rand.New(rand.NewSource(1)), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	st.fail = true
	_, err := q.Submit(ctx, txt(jidA, "a", "x"))
	var te toolerr.Error
	if err == nil || errors.As(err, &te) {
		t.Fatalf("err = %v, want a plain wrapped error", err)
	}
}

// failingStore fails RecentDuplicate on demand.
type failingStore struct {
	*memStore
	fail bool
}

func (f *failingStore) RecentDuplicate(ctx context.Context, chat, hash string, since int64) (bool, error) {
	if f.fail {
		return false, errors.New("disco cheio")
	}
	return f.memStore.RecentDuplicate(ctx, chat, hash, since)
}

func TestSentMessagesAreRecordedOnce(t *testing.T) {
	h := newHarness(t, nil, true)
	h.fk.SetPhone(jidB, "+5511999998888")
	r1 := h.mustSend(txt(jidA, "ref-a", "olá"))
	r2 := h.mustSend(card(jidA, "ref-a", jidB, "Fulana"))
	got := h.st.sentMessages()
	if len(got) != 2 {
		t.Fatalf("recorded = %d, want 2", len(got))
	}
	m := got[0].msg
	if got[0].ref != "ref-a" || m.ID != r1.MessageID || !m.FromMe || m.Kind != store.KindText || m.Text != "olá" || m.ChatJID != string(jidA) {
		t.Errorf("text row = %+v (ref %q)", m, got[0].ref)
	}
	c := got[1].msg
	if c.ID != r2.MessageID || c.Kind != store.KindContact || c.Text != "[contato]" || c.Caption != "Fulana" {
		t.Errorf("contact row = %+v", c)
	}
}

func TestUncertainSendIsNotRetried(t *testing.T) {
	h := newHarness(t, nil, true)
	h.fk.FailSendText(wa.ErrSendUncertain)
	_, err := h.submit(txt(jidA, "a", "talvez"))
	if !errors.Is(err, ErrSendUncertain) {
		t.Fatalf("err = %v, want ErrSendUncertain", err)
	}
	if n := h.fk.Attempts(); n != 1 {
		t.Fatalf("attempts = %d, want 1", n)
	}
	if len(h.st.sentMessages()) != 0 {
		t.Error("uncertain send recorded in history")
	}
}
