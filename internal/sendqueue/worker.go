package sendqueue

import (
	"context"
	"errors"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/wa"
)

// Failure reasons stored in send_queue.error. They are codes, never content.
const (
	reasonInternal = "internal"
	reasonNetwork  = "network"
	reasonWA       = "wa_error"
	reasonQuiet    = "quiet_hours"
	reasonNoPhone  = "no_phone"
	reasonUnsure   = "uncertain"
)

// loop is the single worker. It sends the oldest queued item, one at a time.
func (q *Queue) loop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		it, err := q.st.NextQueued(ctx)
		switch {
		case errors.Is(err, store.ErrNotFound):
			select {
			case <-ctx.Done():
				return
			case <-q.wake:
			}
			continue
		case err != nil:
			q.log.Error("ler fila de envio", "err", err)
			if q.clk.Sleep(ctx, time.Second) != nil {
				return
			}
			continue
		}
		q.process(ctx, it)
	}
}

// process sends one item: cooldowns, composing, send, one retry on network
// error, paused. It always ends with a final status for the item.
func (q *Queue) process(ctx context.Context, it store.SendItem) {
	chat := wa.JID(it.ChatJID)
	if err := q.st.MarkSending(ctx, it.ID); err != nil {
		q.log.Error("marcar envio em andamento", "err", err)
		q.fail(ctx, it, reasonInternal)
		return
	}
	if err := q.cooldown(ctx, chat); err != nil {
		return // context ended; the item stays "sending" and expires at next boot
	}
	if q.quiet.Contains(q.clk.Now()) {
		q.fail(ctx, it, reasonQuiet)
		return
	}

	typing := q.cfg.TypingIndicator
	runes := runeCount(it.Text)
	if typing {
		if err := q.c.SendPresence(ctx, chat, true); err != nil {
			// The client error may embed a JID; log only its class.
			q.log.Debug("presença 'digitando' falhou", "transient", wa.IsTransient(err))
		}
		if err := q.clk.Sleep(ctx, q.typingDelay(runes)); err != nil {
			return
		}
	}

	q.lastChat = chat
	q.burst++
	msgID, err := q.sendOnce(ctx, it)
	if err != nil && wa.IsTransient(err) {
		q.log.Warn("erro de rede no envio; nova tentativa em 5 s", "id", it.ID)
		if serr := q.clk.Sleep(ctx, retryDelay); serr != nil {
			return
		}
		msgID, err = q.sendOnce(ctx, it)
	}
	if typing {
		if perr := q.c.SendPresence(ctx, chat, false); perr != nil {
			q.log.Debug("presença 'pausado' falhou", "transient", wa.IsTransient(perr))
		}
	}
	if err != nil {
		reason := reasonWA
		if wa.IsTransient(err) {
			reason = reasonNetwork
		}
		if errors.Is(err, errNoPhone) {
			reason = reasonNoPhone
		}
		if errors.Is(err, wa.ErrSendUncertain) {
			reason = reasonUnsure
		}
		q.fail(ctx, it, reason)
		return
	}

	if err := q.st.MarkSent(ctx, it.ID, msgID); err != nil {
		q.log.Error("gravar envio concluído", "err", err)
	}
	if err := q.st.RecordSent(ctx, q.refOf(it.ID), sentMessage(it, msgID, q.clk.Now().Unix())); err != nil {
		q.log.Error("gravar mensagem enviada no histórico", "err", err)
	}
	q.auditItem(ctx, it, "ok")
	q.finish(it.ID, chat, outcome{status: StatusSent, msgID: msgID})
}

var errNoPhone = errors.New("sendqueue: contato sem número")

// contactMark is the text ingestion stores for a contact card.
const contactMark = "[contato]"

// sentMessage is the history row of a sent item, shaped like the one ingestion
// writes for the same message, so the agent sees it once as from "me".
func sentMessage(it store.SendItem, msgID string, ts int64) store.Message {
	m := store.Message{ChatJID: it.ChatJID, ID: msgID, FromMe: true, TS: ts,
		Kind: store.KindText, Text: it.Text, QuotedID: it.QuotedID}
	if it.Kind == store.KindContact {
		m.Kind, m.Text, m.Caption = store.KindContact, contactMark, it.Text
	}
	return m
}

// sendOnce makes one call to WhatsApp for the item.
func (q *Queue) sendOnce(ctx context.Context, it store.SendItem) (string, error) {
	chat := wa.JID(it.ChatJID)
	if it.Kind == store.KindContact {
		phone, ok := q.c.ContactPhone(wa.JID(it.SharedJID))
		if !ok {
			return "", errNoPhone
		}
		return q.c.SendContact(ctx, chat, it.Text, buildVCard(it.Text, phone), it.QuotedID)
	}
	return q.c.SendText(ctx, chat, it.Text, it.QuotedID)
}

// cooldown waits before a send when the recipient changes (switch cooldown) or
// when the same recipient has had burst_per_recipient messages in a row (burst
// cooldown). The counter resets after each wait.
func (q *Queue) cooldown(ctx context.Context, chat wa.JID) error {
	var d time.Duration
	switch {
	case q.lastChat != "" && chat != q.lastChat:
		d = q.uniform(q.cfg.SwitchCooldownMS)
		q.burst = 0
	case q.burst >= q.cfg.BurstPerRecipient:
		d = q.uniform(q.cfg.BurstCooldownMS)
		q.burst = 0
	default:
		return nil
	}
	return q.clk.Sleep(ctx, d)
}

// fail marks the item failed (no retry beyond sendOnce) and records it.
func (q *Queue) fail(ctx context.Context, it store.SendItem, reason string) {
	if err := q.st.MarkFailed(ctx, it.ID, reason); err != nil {
		q.log.Error("marcar envio com falha", "err", err)
	}
	q.auditItem(ctx, it, "error "+reason)
	q.finish(it.ID, wa.JID(it.ChatJID), outcome{status: StatusFailed, uncertain: reason == reasonUnsure})
}

// auditItem records the outcome with no content, phone or JID.
func (q *Queue) auditItem(ctx context.Context, it store.SendItem, detail string) {
	if err := q.st.Audit(ctx, auditAction(it.Kind), q.refOf(it.ID), detail); err != nil {
		q.log.Error("auditar envio", "err", err)
	}
}

// refOf returns the audit reference the request was submitted with.
func (q *Queue) refOf(id int64) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, p := range q.pending {
		if p.id == id {
			return p.chatRef
		}
	}
	return ""
}

// finish removes the item from the pending list, records a successful send for
// the recipient counts, and wakes the Submit that waits for it.
func (q *Queue) finish(id int64, chat wa.JID, out outcome) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, p := range q.pending {
		if p.id == id {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			break
		}
	}
	if out.status == StatusSent {
		q.sentRecent[chat] = q.clk.Now().Unix()
	}
	if ch, ok := q.waiters[id]; ok {
		ch <- out
		delete(q.waiters, id)
	}
}
