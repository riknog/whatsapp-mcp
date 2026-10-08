package sendqueue

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/toolerr"
	"github.com/riknog/whatsapp-mcp/internal/wa"
)

// admitLocked runs the checks of docs/03 §2 in order and, when all pass,
// enqueues the item. It returns the item id and the channel for its outcome.
// The caller must hold q.mu: each check then sees every earlier admission.
func (q *Queue) admitLocked(ctx context.Context, r Request) (int64, chan outcome, error) {
	now := q.clk.Now()
	ts := now.Unix()

	if !q.cfg.Enabled {
		return 0, nil, q.refuse(ctx, r, toolerr.New(toolerr.CodeSendDisabled, "envio desativado na configuração", nil))
	}
	hash, runes, err := q.validate(r)
	if err != nil {
		return 0, nil, q.refuse(ctx, r, err)
	}
	if q.quietErr != nil {
		return 0, nil, q.refuse(ctx, r, toolerr.New(toolerr.CodeInvalidArgument, "quiet_hours inválido na configuração", nil))
	}
	if q.quiet.Contains(now) {
		return 0, nil, q.refuse(ctx, r, toolerr.New(toolerr.CodeQuietHours, "fora do horário de envio", nil))
	}

	dup, err := q.st.RecentDuplicate(ctx, string(r.Chat), hash, ts-duplicateWindowS)
	if err != nil {
		return 0, nil, fmt.Errorf("sendqueue: verificar duplicata: %w", err)
	}
	if dup {
		return 0, nil, q.refuse(ctx, r, toolerr.New(toolerr.CodeDuplicateMessage,
			"mesma mensagem para o mesmo contato há menos de 60 s", nil))
	}

	queued, err := q.st.CountPending(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("sendqueue: contar fila: %w", err)
	}
	if queued >= int64(q.cfg.MaxQueue) {
		return 0, nil, q.refuse(ctx, r, toolerr.New(toolerr.CodeQueueFull, "fila de envios cheia, tente mais tarde", nil))
	}

	if err := q.checkRateLimits(ctx, ts); err != nil {
		return 0, nil, q.refuse(ctx, r, err)
	}
	if err := q.checkNewRecipient(ctx, r.Chat, ts); err != nil {
		return 0, nil, q.refuse(ctx, r, err)
	}

	id, err := q.st.EnqueueSend(ctx, q.item(r))
	if err != nil {
		return 0, nil, fmt.Errorf("sendqueue: enfileirar: %w", err)
	}
	p := &pendingItem{id: id, chat: r.Chat, chatRef: r.ChatRef, ts: ts, runes: runes}
	q.pending = append(q.pending, p)
	ch := make(chan outcome, 1)
	q.waiters[id] = ch
	return id, ch, nil
}

// item maps a request to a queue row. For a card the display name goes in the
// text column (the card has no body); the phone number is never stored.
func (q *Queue) item(r Request) store.SendItem {
	it := store.SendItem{ChatJID: string(r.Chat), Kind: r.Kind, QuotedID: r.QuotedID}
	if r.Kind == store.KindContact {
		it.SharedJID = string(r.SharedJID)
		it.Text = cleanName(r.ContactName)
	} else {
		it.Text = r.Text
	}
	return it
}

// validate checks the request shape and returns the duplicate key and the text
// length in characters. Its errors are toolerr.Error values.
func (q *Queue) validate(r Request) (string, int, error) {
	if r.Chat.IsZero() {
		return "", 0, toolerr.New(toolerr.CodeInvalidArgument, "destinatário vazio", nil)
	}
	switch {
	case r.Chat.IsGroup():
		if !q.cfg.AllowGroups {
			return "", 0, toolerr.New(toolerr.CodeGroupSendDisabled, "envio para grupos está desativado", nil)
		}
	case r.Chat.IsDirect():
	default:
		return "", 0, toolerr.New(toolerr.CodeInvalidArgument, "destino não suportado", nil)
	}

	switch r.Kind {
	case store.KindText:
		if strings.TrimSpace(r.Text) == "" {
			return "", 0, toolerr.New(toolerr.CodeInvalidArgument, "texto vazio", nil)
		}
		n := runeCount(r.Text)
		if n > MaxTextRunes {
			return "", 0, toolerr.New(toolerr.CodeMessageTooLong,
				fmt.Sprintf("mensagem com mais de %d caracteres", MaxTextRunes), nil)
		}
		return store.TextHash(r.Text), n, nil
	case store.KindContact:
		if r.Chat.IsGroup() {
			return "", 0, toolerr.New(toolerr.CodeInvalidArgument, "cartão só pode ir para conversa individual", nil)
		}
		if r.SharedJID.IsZero() {
			return "", 0, toolerr.New(toolerr.CodeInvalidArgument, "contato a compartilhar vazio", nil)
		}
		if cleanName(r.ContactName) == "" {
			return "", 0, toolerr.New(toolerr.CodeInvalidArgument, "nome do cartão vazio", nil)
		}
		if _, ok := q.c.ContactPhone(r.SharedJID); !ok {
			return "", 0, toolerr.New(toolerr.CodeInvalidArgument, "contato sem número salvo", nil)
		}
		return contactKey(string(r.SharedJID)), 0, nil
	default:
		return "", 0, toolerr.New(toolerr.CodeInvalidArgument, "tipo de envio inválido", nil)
	}
}

// checkRateLimits applies the global caps. Each window counts sent items in the
// store plus the pending items in memory. retry_after_s is the window length,
// an upper bound: after that long every counted item has left the window.
func (q *Queue) checkRateLimits(ctx context.Context, ts int64) error {
	windows := []struct {
		secs int64
		max  int
	}{
		{minuteS, q.cfg.MaxPerMinute},
		{hourS, q.cfg.MaxPerHour},
		{dayS, q.cfg.MaxPerDay},
	}
	for _, w := range windows {
		since := ts - w.secs
		sent, err := q.st.SentSince(ctx, since)
		if err != nil {
			return fmt.Errorf("sendqueue: contar envios: %w", err)
		}
		if sent+q.pendingSince(since) >= int64(w.max) {
			return rateLimited(w.secs, fmt.Sprintf("limite de envios por %s atingido", windowName(w.secs)))
		}
	}
	return nil
}

// checkNewRecipient allows at most MaxNewRecipientsPerHour distinct recipients
// in an hour. A chat already reached in the hour is never new.
func (q *Queue) checkNewRecipient(ctx context.Context, chat wa.JID, ts int64) error {
	since := ts - hourS
	if q.sentByUsSince(chat, since) || q.hasPending(chat) {
		return nil
	}
	n, err := q.recipientsSince(ctx, since)
	if err != nil {
		return err
	}
	if n >= int64(q.cfg.MaxNewRecipientsPerHour) {
		return rateLimited(hourS, "limite de destinatários novos por hora atingido")
	}
	return nil
}

// recipientsSince estimates distinct recipients since the given time. The store
// gives the sent count. Pending chats that this process has not sent to yet are
// added. A chat sent before this process started may be counted twice, which
// makes the estimate stricter, never looser. Caller holds q.mu.
func (q *Queue) recipientsSince(ctx context.Context, since int64) (int64, error) {
	for chat, t := range q.sentRecent {
		if t < since {
			delete(q.sentRecent, chat)
		}
	}
	n, err := q.st.DistinctRecipientsSince(ctx, since)
	if err != nil {
		return 0, fmt.Errorf("sendqueue: contar destinatários: %w", err)
	}
	seen := map[wa.JID]bool{}
	for _, p := range q.pending {
		if seen[p.chat] || q.sentByUsSince(p.chat, since) {
			continue
		}
		seen[p.chat] = true
		n++
	}
	return n, nil
}

// refuse turns a check failure into the error returned to the caller. A
// toolerr.Error is a refusal and is audited (no content). Anything else is a
// store failure, wrapped and not audited.
func (q *Queue) refuse(ctx context.Context, r Request, err error) error {
	te, ok := err.(toolerr.Error)
	if !ok {
		return fmt.Errorf("sendqueue: verificar envio: %w", err)
	}
	if aerr := q.st.Audit(ctx, auditAction(r.Kind), r.ChatRef, "rejected "+string(te.Code)); aerr != nil {
		q.log.Error("auditar recusa", "err", aerr)
	}
	return te
}

func (q *Queue) pendingSince(since int64) int64 {
	var n int64
	for _, p := range q.pending {
		if p.ts >= since {
			n++
		}
	}
	return n
}

func (q *Queue) hasPending(chat wa.JID) bool {
	for _, p := range q.pending {
		if p.chat == chat {
			return true
		}
	}
	return false
}

func (q *Queue) sentByUsSince(chat wa.JID, since int64) bool {
	t, ok := q.sentRecent[chat]
	return ok && t >= since
}

func rateLimited(retryS int64, msg string) error {
	return toolerr.New(toolerr.CodeRateLimited, msg, map[string]any{"retry_after_s": retryS})
}

func auditAction(kind string) string {
	if kind == store.KindContact {
		return auditShare
	}
	return auditSend
}

func windowName(secs int64) string {
	switch secs {
	case minuteS:
		return "minuto"
	case hourS:
		return "hora"
	default:
		return "dia"
	}
}

// typingNominal is the typing time without jitter: 40 ms per character, clamped.
func typingNominal(runes int) time.Duration {
	ms := 40 * runes
	if ms < 600 {
		ms = 600
	}
	if ms > 4000 {
		ms = 4000
	}
	return time.Duration(ms) * time.Millisecond
}
