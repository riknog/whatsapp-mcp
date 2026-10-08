package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

const sendColumns = `id, chat_jid, kind, text, shared_jid, quoted_id, text_hash, status, enqueued_at, sent_at, wa_message_id, error`

// TextHash returns the sha256 hex of the normalized text: lower case, runs of
// whitespace collapsed to one space, trimmed. It is the duplicate key of §2.8.
func TextHash(text string) string {
	norm := strings.ToLower(strings.Join(strings.Fields(text), " "))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// contactHash is the duplicate key of a contact card: the shared JID, not the text.
func contactHash(sharedJID string) string {
	sum := sha256.Sum256([]byte("contact:" + sharedJID))
	return hex.EncodeToString(sum[:])
}

// EnqueueSend appends an item with status queued and returns its id. The store
// computes TextHash and EnqueuedAt; any value the caller set for them is ignored.
func (s *Store) EnqueueSend(ctx context.Context, it SendItem) (int64, error) {
	if it.ChatJID == "" {
		return 0, toolerr.New(toolerr.CodeInvalidArgument, "envio sem destinatário", nil)
	}
	var hash string
	switch it.Kind {
	case KindText:
		if strings.TrimSpace(it.Text) == "" {
			return 0, toolerr.New(toolerr.CodeInvalidArgument, "texto vazio", nil)
		}
		hash = TextHash(it.Text)
	case KindContact:
		if it.SharedJID == "" {
			return 0, toolerr.New(toolerr.CodeInvalidArgument, "cartão sem contato", nil)
		}
		hash = contactHash(it.SharedJID)
	default:
		return 0, toolerr.New(toolerr.CodeInvalidArgument, "tipo de envio inválido", nil)
	}
	res, err := s.w.ExecContext(ctx, `
		INSERT INTO send_queue (chat_jid, kind, text, shared_jid, quoted_id, text_hash, status, enqueued_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		it.ChatJID, it.Kind, it.Text, it.SharedJID, it.QuotedID, hash, StatusQueued, s.now())
	if err != nil {
		return 0, fmt.Errorf("store: enfileirar envio: %w", err)
	}
	return res.LastInsertId()
}

// GetSend returns one queue item, or ErrNotFound.
func (s *Store) GetSend(ctx context.Context, id int64) (SendItem, error) {
	it, err := scanSend(s.r.QueryRowContext(ctx, `SELECT `+sendColumns+` FROM send_queue WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return SendItem{}, ErrNotFound
	}
	if err != nil {
		return SendItem{}, fmt.Errorf("store: ler envio: %w", err)
	}
	return it, nil
}

// NextQueued returns the oldest queued item (FIFO), or ErrNotFound when none is waiting.
func (s *Store) NextQueued(ctx context.Context) (SendItem, error) {
	it, err := scanSend(s.r.QueryRowContext(ctx,
		`SELECT `+sendColumns+` FROM send_queue WHERE status = 'queued' ORDER BY enqueued_at, id LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return SendItem{}, ErrNotFound
	}
	if err != nil {
		return SendItem{}, fmt.Errorf("store: próximo envio: %w", err)
	}
	return it, nil
}

// CountPending counts items that are queued or sending (the fila máx. of §2.7).
func (s *Store) CountPending(ctx context.Context) (int64, error) {
	var n int64
	if err := s.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM send_queue WHERE status IN ('queued', 'sending')`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: contar fila: %w", err)
	}
	return n, nil
}

// MarkSending moves queued -> sending. ErrInvalidState from any other status.
func (s *Store) MarkSending(ctx context.Context, id int64) error {
	return s.transition(ctx, id, `status = 'sending'`, `status = 'queued'`)
}

// MarkSent moves sending -> sent, records the WhatsApp message id and sent_at.
func (s *Store) MarkSent(ctx context.Context, id int64, waMessageID string) error {
	return s.transition(ctx, id,
		`status = 'sent', sent_at = ?, wa_message_id = ?`,
		`status = 'sending'`, s.now(), waMessageID)
}

// MarkFailed moves queued or sending -> failed and stores a short reason.
func (s *Store) MarkFailed(ctx context.Context, id int64, reason string) error {
	return s.transition(ctx, id, `status = 'failed', error = ?`, `status IN ('queued', 'sending')`, reason)
}

// transition runs UPDATE send_queue SET <set> WHERE id = ? AND <from>. It tells
// a missing row (ErrNotFound) from a row in the wrong state (ErrInvalidState).
func (s *Store) transition(ctx context.Context, id int64, set, from string, args ...any) error {
	// set and from are constants of this package; values are ? parameters.
	q := `UPDATE send_queue SET ` + set + ` WHERE id = ? AND ` + from // #nosec G202 -- constant fragments
	res, err := s.w.ExecContext(ctx, q, append(args, id)...)
	if err != nil {
		return fmt.Errorf("store: transição de envio: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: transição de envio: %w", err)
	}
	if n == 1 {
		return nil
	}
	var exists int
	err = s.r.QueryRowContext(ctx, `SELECT 1 FROM send_queue WHERE id = ?`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: verificar envio: %w", err)
	}
	return ErrInvalidState
}

// ExpireStale turns every queued or sending item into expired. It runs at boot,
// so that a message from before a crash is never sent later on its own (§2.10).
// It returns how many items changed.
func (s *Store) ExpireStale(ctx context.Context) (int64, error) {
	res, err := s.w.ExecContext(ctx,
		`UPDATE send_queue SET status = 'expired' WHERE status IN ('queued', 'sending')`)
	if err != nil {
		return 0, fmt.Errorf("store: expirar fila: %w", err)
	}
	return res.RowsAffected()
}

// SentSince counts items sent at or after since. The single worker checks this
// before each send, so queued and sending items do not need to be counted here.
func (s *Store) SentSince(ctx context.Context, since int64) (int64, error) {
	var n int64
	if err := s.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM send_queue WHERE status = 'sent' AND sent_at >= ?`, since).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: contar envios: %w", err)
	}
	return n, nil
}

// DistinctRecipientsSince counts distinct chats that received a sent item at or after since.
func (s *Store) DistinctRecipientsSince(ctx context.Context, since int64) (int64, error) {
	var n int64
	if err := s.r.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT chat_jid) FROM send_queue WHERE status = 'sent' AND sent_at >= ?`, since).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: contar destinatários: %w", err)
	}
	return n, nil
}

// RecentDuplicate reports whether chat already has an item with this hash that
// was enqueued at or after since and is not failed, expired or rejected (§2.8).
// Use TextHash(text) for the hash.
func (s *Store) RecentDuplicate(ctx context.Context, chatJID, textHash string, since int64) (bool, error) {
	var one int
	err := s.r.QueryRowContext(ctx, `
		SELECT 1 FROM send_queue
		WHERE chat_jid = ? AND text_hash = ? AND enqueued_at >= ?
		  AND status IN ('queued', 'sending', 'sent')
		LIMIT 1`, chatJID, textHash, since).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: verificar duplicata: %w", err)
	}
	return true, nil
}

func scanSend(sc rowScanner) (SendItem, error) {
	var it SendItem
	err := sc.Scan(&it.ID, &it.ChatJID, &it.Kind, &it.Text, &it.SharedJID, &it.QuotedID, &it.TextHash,
		&it.Status, &it.EnqueuedAt, &it.SentAt, &it.WAMessageID, &it.Error)
	return it, err
}

// CountSendsSince counts items that will count against the global limits: queued,
// sending or sent, enqueued at or after since. It is the figure the rate limiter
// needs before it admits a new item, so pending items are included.
func (s *Store) CountSendsSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	if err := s.r.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM send_queue
		WHERE status IN ('queued', 'sending', 'sent') AND enqueued_at >= ?`, since.Unix()).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: contar envios desde: %w", err)
	}
	return n, nil
}

// DistinctSendRecipientsSince counts the distinct chats among the items that
// CountSendsSince counts (same statuses, same enqueued_at rule).
func (s *Store) DistinctSendRecipientsSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	if err := s.r.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT chat_jid) FROM send_queue
		WHERE status IN ('queued', 'sending', 'sent') AND enqueued_at >= ?`, since.Unix()).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: contar destinatários desde: %w", err)
	}
	return n, nil
}
