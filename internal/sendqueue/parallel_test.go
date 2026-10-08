package sendqueue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/config"
	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/toolerr"
	"github.com/riknog/whatsapp-mcp/internal/wa"
)

type subOut struct {
	res Result
	err error
}

// submitParallel fires every request at once with an already-canceled context:
// Submit then returns as soon as it is admitted, and the worker stays parked
// because nothing advances the fake clock. Admission order is still serialized.
func submitParallel(h *harness, reqs []Request) []subOut {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make([]subOut, len(reqs))
	var wg sync.WaitGroup
	for i, r := range reqs {
		wg.Add(1)
		go func(i int, r Request) {
			defer wg.Done()
			res, err := h.q.Submit(ctx, r)
			out[i] = subOut{res, err}
		}(i, r)
	}
	wg.Wait()
	return out
}

// queueOne admits one request without waiting for its send.
func (h *harness) queueOne(r Request) (Result, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return h.q.Submit(ctx, r)
}

// tally counts admitted requests and collects the toolerr rejections.
func tally(t *testing.T, outs []subOut) (accepted int, rejected []toolerr.Error) {
	t.Helper()
	for _, o := range outs {
		if o.err == nil {
			accepted++
			continue
		}
		var te toolerr.Error
		if !errors.As(o.err, &te) {
			t.Fatalf("unexpected non-tool error: %v", o.err)
		}
		rejected = append(rejected, te)
	}
	return accepted, rejected
}

func TestParallelSameRecipientTwentyAcceptedTenRateLimited(t *testing.T) {
	h := newHarness(t, func(c *config.SendConfig) { c.MaxQueue = 30 }, false)
	reqs := make([]Request, 30)
	for i := range reqs {
		reqs[i] = txt(jidA, "a", fmt.Sprintf("texto distinto %02d", i))
	}
	accepted, rejected := tally(t, submitParallel(h, reqs))
	if accepted != 20 || len(rejected) != 10 {
		t.Fatalf("accepted=%d rejected=%d, want 20 and 10", accepted, len(rejected))
	}
	for _, te := range rejected {
		requireCode(t, te, toolerr.CodeRateLimited)
		if got, _ := te.Details["retry_after_s"].(int64); got != 60 {
			t.Errorf("retry_after_s = %v, want 60 (minute cap)", te.Details["retry_after_s"])
		}
	}
}

func TestParallelDistinctRecipientsFifteenAcceptedRestRateLimited(t *testing.T) {
	h := newHarness(t, func(c *config.SendConfig) {
		c.MaxQueue = 30
		c.MaxPerMinute = 1000 // keep the minute cap out of the way: only new recipients may refuse
	}, false)
	reqs := make([]Request, 30)
	for i := range reqs {
		chat := wa.JID(fmt.Sprintf("55110000%05d@s.whatsapp.net", i))
		reqs[i] = txt(chat, fmt.Sprintf("r%d", i), "oi")
	}
	accepted, rejected := tally(t, submitParallel(h, reqs))
	if accepted != 15 || len(rejected) != 15 {
		t.Fatalf("accepted=%d rejected=%d, want 15 and 15", accepted, len(rejected))
	}
	for _, te := range rejected {
		requireCode(t, te, toolerr.CodeRateLimited)
		if got, _ := te.Details["retry_after_s"].(int64); got != 3600 {
			t.Errorf("retry_after_s = %v, want 3600 (new recipients per hour)", te.Details["retry_after_s"])
		}
	}
}

func TestParallelIdenticalTextsOneAcceptedOneDuplicate(t *testing.T) {
	h := newHarness(t, nil, false)
	accepted, rejected := tally(t, submitParallel(h, []Request{
		txt(jidA, "a", "oi, tudo bem?"),
		txt(jidA, "a", "  OI,  tudo bem? "),
	}))
	if accepted != 1 || len(rejected) != 1 {
		t.Fatalf("accepted=%d rejected=%d, want 1 and 1", accepted, len(rejected))
	}
	requireCode(t, rejected[0], toolerr.CodeDuplicateMessage)
}

// seedSent writes a history item that was sent at the given fake time, so that
// the caps see it. It bypasses the worker on purpose.
func (h *harness) seedSent(chat wa.JID, text string, at time.Time) {
	h.t.Helper()
	ctx := context.Background()
	id, err := h.st.EnqueueSend(ctx, store.SendItem{ChatJID: string(chat), Kind: store.KindText, Text: text})
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.st.MarkSending(ctx, id); err != nil {
		h.t.Fatal(err)
	}
	if d := at.Sub(h.clk.Now()); d > 0 {
		h.clk.Advance(d)
	}
	if err := h.st.MarkSent(ctx, id, "seed"); err != nil {
		h.t.Fatal(err)
	}
}

func TestHourlyCapAndPartialWindow(t *testing.T) {
	h := newUnstartedHarness(t, func(c *config.SendConfig) {
		c.MaxQueue = 50
		c.MaxPerMinute = 1000
		c.MaxPerDay = 1000
	})
	// 200 sends spread over the hour, all to one chat (so the new-recipient cap is not involved).
	// The worker starts only after the history is written, so it cannot pick up a seeded item.
	for i := 0; i < 200; i++ {
		h.seedSent(jidA, fmt.Sprintf("historico %d", i), testStart.Add(time.Duration(i)*18*time.Second))
	}
	h.start(false)
	h.clk.Advance(18 * time.Second) // now = start + 3600 s: all 200 are inside the last hour

	_, err := h.queueOne(txt(jidA, "a", "201"))
	te := requireCode(t, err, toolerr.CodeRateLimited)
	if got, _ := te.Details["retry_after_s"].(int64); got != 3600 {
		t.Errorf("retry_after_s = %v, want 3600", te.Details["retry_after_s"])
	}

	// 30 minutes later the first 100 sends have left the window: 100 < 200.
	h.clk.Advance(1800 * time.Second)
	if _, err := h.queueOne(txt(jidA, "a", "depois da janela")); err != nil {
		t.Fatalf("after partial window: %v", err)
	}
	if s := h.q.Stats(); s.SentLastHour != 100 {
		t.Errorf("SentLastHour = %d, want 100", s.SentLastHour)
	}
}

func TestCardsAndTextsShareCapsAndSwitchCooldown(t *testing.T) {
	h := newHarness(t, nil, true)
	shared := make([]wa.JID, 10)
	for i := range shared {
		shared[i] = wa.JID(fmt.Sprintf("55119000%05d@s.whatsapp.net", i))
		h.fk.SetPhone(shared[i], fmt.Sprintf("+5511900%05d", i))
	}
	for i := 1; i <= 10; i++ {
		h.mustSend(txt(jidA, "a", fmt.Sprintf("texto %d", i)))
	}
	for i := 0; i < 10; i++ {
		h.mustSend(card(jidB, "b", shared[i], fmt.Sprintf("Contato %d", i)))
	}
	// 20 sends in the minute: the cap counts texts and cards together.
	_, err := h.submit(txt(jidC, "c", "21 texto"))
	requireCode(t, err, toolerr.CodeRateLimited)
	h.fk.SetPhone(jidC, "+5511333333333")
	_, err = h.submit(card(jidC, "c", shared[0], "Contato 21"))
	requireCode(t, err, toolerr.CodeRateLimited)
}

func TestSwitchCooldownAppliesBetweenTextAndCard(t *testing.T) {
	h := newHarness(t, nil, true)
	h.fk.SetPhone(jidC, "+5511333333333")
	h.mustSend(txt(jidA, "a", "primeiro"))
	h.mustSend(card(jidB, "b", jidC, "Carlos"))
	h.mustSend(txt(jidC, "c", "depois do cartão"))

	sent, cards, typ := h.fk.Sent(), h.fk.Contacts(), typingEvents(h.fk)
	if len(sent) != 2 || len(cards) != 1 || len(typ) != 3 {
		t.Fatalf("sent=%d cards=%d typing=%d", len(sent), len(cards), len(typ))
	}
	within(t, "switch text A -> card B", typ[1].At.Sub(sent[0].At), 1500*time.Millisecond, 3500*time.Millisecond)
	within(t, "switch card B -> text B", typ[2].At.Sub(cards[0].At), 1500*time.Millisecond, 3500*time.Millisecond)
}
