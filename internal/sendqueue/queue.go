// Package sendqueue is the single FIFO send queue with cooldowns and rate limits.
// Every outgoing message and contact card goes through it (docs/03 §2).
//
// Counting: the caps (per minute, hour, day, new recipients per hour) count
// queued and sent items. The store only counts sent items, so the queue keeps
// the pending items in memory, under the same mutex as admission, and adds them
// to the store counts. An item leaves the in-memory list only after its outcome
// is in the store, so a count is never lower than the truth.
package sendqueue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/config"
	"github.com/riknog/whatsapp-mcp/internal/wa"
)

// Limits and windows from docs/02-TOOLS.md and docs/03 §2.
const (
	MaxTextRunes     = 4096
	duplicateWindowS = 60
	minuteS          = 60
	hourS            = 3600
	dayS             = 86400
	retryDelay       = 5 * time.Second
	// switchEstimate is the mean switch cooldown, used only for the ETA.
	switchEstimate = 2500 * time.Millisecond
)

// Result statuses returned by Submit.
const (
	StatusSent   = "sent"
	StatusQueued = "queued"
	StatusFailed = "failed" // internal: mapped to ErrSendFailed by Submit
)

// Audit actions. Audit rows never carry message content.
const (
	auditSend  = "send"
	auditShare = "share_contact"
)

// ErrSendFailed is returned by Submit when the message was accepted but WhatsApp
// refused it, or the network failed twice. The tool layer maps it to an error.
var ErrSendFailed = errors.New("sendqueue: envio falhou")

// ErrSendUncertain is returned by Submit when WhatsApp did not answer in time:
// the message may have been delivered. It is not retried, and the caller must
// not send it again without checking.
var ErrSendUncertain = errors.New("sendqueue: envio incerto")

var errNotStarted = errors.New("sendqueue: Start não foi chamado")

// Request is one message (Kind store.KindText) or one contact card (Kind
// store.KindContact). Chat and SharedJID are internal identifiers; ChatRef is
// the opaque reference used in audit rows.
type Request struct {
	Chat        wa.JID
	ChatRef     string
	Kind        string
	Text        string // store.KindText: the message body
	SharedJID   wa.JID // store.KindContact: the contact being shared
	ContactName string // store.KindContact: display name written on the card
	QuotedID    string
}

// Result is what Submit returns. QueuePosition and ETA are set only when Status
// is "queued": position 1 is the next item (or the one being sent). ETA is a
// rough estimate that ignores burst cooldowns.
type Result struct {
	Status        string
	MessageID     string
	QueuePosition int
	ETA           time.Duration
}

// Stats is a snapshot for the status tool.
type Stats struct {
	Pending            int
	SentLastHour       int64
	SentLastDay        int64
	RecipientsLastHour int64
}

// Queue is the send queue. Create it with New, then call Start once.
type Queue struct {
	cfg      config.SendConfig
	st       Store
	c        wa.Client
	clk      clock.Clock
	rnd      *rand.Rand // used only by the worker goroutine
	log      *slog.Logger
	quiet    *config.QuietWindow
	quietErr error
	wake     chan struct{}
	done     chan struct{} // closed when the worker ends

	mu         sync.Mutex
	started    bool
	pending    []*pendingItem // queued and sending items, FIFO
	waiters    map[int64]chan outcome
	sentRecent map[wa.JID]int64 // last successful send per chat, by this process

	// Worker-only state. Touched only by loop/process.
	lastChat wa.JID
	burst    int
}

type pendingItem struct {
	id      int64
	chat    wa.JID
	chatRef string // opaque reference for audit rows
	ts      int64  // enqueue time, unix seconds
	runes   int    // text length in characters (0 for cards)
}

type outcome struct {
	status    string // StatusSent or StatusFailed
	msgID     string
	uncertain bool // failed with wa.ErrSendUncertain
}

// New builds a queue. A nil rnd gets a time-seeded source and a nil log
// discards output. An invalid quiet_hours value is reported by Submit.
func New(cfg config.SendConfig, st Store, c wa.Client, clk clock.Clock, rnd *rand.Rand, log *slog.Logger) *Queue {
	if clk == nil {
		clk = clock.Real{}
	}
	if rnd == nil {
		rnd = rand.New(rand.NewSource(time.Now().UnixNano())) // #nosec G404 -- cooldown jitter, not security
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	q := &Queue{
		cfg:        cfg,
		st:         st,
		c:          c,
		clk:        clk,
		rnd:        rnd,
		log:        log,
		wake:       make(chan struct{}, 1),
		done:       make(chan struct{}),
		waiters:    map[int64]chan outcome{},
		sentRecent: map[wa.JID]int64{},
	}
	quiet, err := config.ParseQuietHours(cfg.QuietHours)
	q.quiet, q.quietErr = quiet, err
	return q
}

// Start expires whatever a previous run left queued (§2.10, so that nothing is
// sent late on its own) and starts the single worker. It returns at once; the
// worker stops when ctx is done. Call it before Submit.
func (q *Queue) Start(ctx context.Context) error {
	q.mu.Lock()
	if q.started {
		q.mu.Unlock()
		return errors.New("sendqueue: Start chamado duas vezes")
	}
	q.started = true
	q.mu.Unlock()

	n, err := q.st.ExpireStale(ctx)
	if err != nil {
		return fmt.Errorf("sendqueue: expirar pendentes: %w", err)
	}
	if n > 0 {
		q.log.Info("envios pendentes de execução anterior expirados", "count", n)
	}
	go func() {
		defer close(q.done)
		q.loop(ctx)
	}()
	return nil
}

// Wait blocks until the worker has ended, after the Start context is done. It
// returns at once when the queue was never started.
func (q *Queue) Wait() {
	q.mu.Lock()
	started := q.started
	q.mu.Unlock()
	if started {
		<-q.done
	}
}

// Submit validates the request, enqueues it and waits up to send.wait_timeout_s
// for the outcome. A message that is still queued when the wait ends (or when
// ctx ends) stays queued and is sent later; Submit then returns StatusQueued.
// Rejections are toolerr.Error values and nothing is enqueued.
func (q *Queue) Submit(ctx context.Context, r Request) (Result, error) {
	q.mu.Lock()
	if !q.started {
		q.mu.Unlock()
		return Result{}, errNotStarted
	}
	id, ch, err := q.admitLocked(ctx, r)
	q.mu.Unlock()
	if err != nil {
		return Result{}, err
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}

	timer := q.clk.After(time.Duration(q.cfg.WaitTimeoutS) * time.Second)
	select {
	case out := <-ch:
		return q.resultOf(out)
	case <-timer:
	case <-ctx.Done():
	}
	select {
	case out := <-ch:
		return q.resultOf(out)
	default:
	}
	return q.queuedResult(id), nil
}

func (q *Queue) resultOf(out outcome) (Result, error) {
	if out.status == StatusSent {
		return Result{Status: StatusSent, MessageID: out.msgID}, nil
	}
	if out.uncertain {
		return Result{Status: StatusFailed}, ErrSendUncertain
	}
	return Result{Status: StatusFailed}, ErrSendFailed
}

// queuedResult computes the position and ETA of a still-queued item.
func (q *Queue) queuedResult(id int64) Result {
	q.mu.Lock()
	defer q.mu.Unlock()
	var eta time.Duration
	for i, p := range q.pending {
		eta += q.estimate(p)
		if p.id == id {
			return Result{Status: StatusQueued, QueuePosition: i + 1, ETA: eta}
		}
	}
	// Already finished between the wait and this call: report it as queued
	// with no position, the caller's next step is the same.
	return Result{Status: StatusQueued}
}

// estimate is the rough time one pending item takes: typing plus a mean switch.
func (q *Queue) estimate(p *pendingItem) time.Duration {
	return typingNominal(p.runes) + switchEstimate
}

// Stats returns a snapshot. Store errors leave the counter at zero and are logged.
func (q *Queue) Stats() Stats {
	ctx := context.Background()
	now := q.clk.Now().Unix()
	q.mu.Lock()
	defer q.mu.Unlock()
	s := Stats{Pending: len(q.pending)}
	var err error
	if s.SentLastHour, err = q.st.SentSince(ctx, now-hourS); err != nil {
		q.log.Error("contar envios da hora", "err", err)
	}
	if s.SentLastDay, err = q.st.SentSince(ctx, now-dayS); err != nil {
		q.log.Error("contar envios do dia", "err", err)
	}
	if s.RecipientsLastHour, err = q.recipientsSince(ctx, now-hourS); err != nil {
		q.log.Error("contar destinatários da hora", "err", err)
	}
	return s
}
