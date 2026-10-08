package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

// maxUnreadInbound caps one mark_as_read batch.
const maxUnreadInbound = 500

// RecordSent stores a message the server sent, as from_me, and moves the chat's
// owner_read_at to its time: the owner answered, so the chat is read. The chat
// row is created when missing (ref from the caller, empty display name). The
// insert is idempotent on (chat, id), so an echo of the same message later is
// ignored. Everything runs in one transaction.
func (s *Store) RecordSent(ctx context.Context, chatRef string, m Message) error {
	if m.ChatJID == "" || m.ID == "" || m.Kind == "" {
		return toolerr.New(toolerr.CodeInvalidArgument, "mensagem sem chat, id ou tipo", nil)
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: iniciar transação: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if chatRef != "" {
		kind := "direct"
		if isGroupJID(m.ChatJID) {
			kind = "group"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO chats (jid, ref, kind) VALUES (?, ?, ?)
			ON CONFLICT DO NOTHING`, m.ChatJID, chatRef, kind); err != nil {
			return fmt.Errorf("store: criar chat do envio: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO messages (chat_jid, id, sender_jid, from_me, ts, kind, text, caption, quoted_id)
		VALUES (?, ?, '', 1, ?, ?, ?, ?, ?)
		ON CONFLICT (chat_jid, id) DO NOTHING`,
		m.ChatJID, m.ID, m.TS, m.Kind, m.Text, m.Caption, m.QuotedID); err != nil {
		return fmt.Errorf("store: gravar envio: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE chats SET
		  last_message_at = MAX(last_message_at, ?),
		  owner_read_at   = MAX(owner_read_at, ?)
		WHERE jid = ?`, m.TS, m.TS, m.ChatJID); err != nil {
		return fmt.Errorf("store: atualizar chat do envio: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: confirmar envio: %w", err)
	}
	return nil
}

func isGroupJID(jid string) bool { return strings.HasSuffix(jid, "@g.us") }

// UnreadInbound returns the inbound messages of a chat that the owner has not
// read (ts > owner_read_at), oldest first, at most 500. ErrNotFound if the chat
// is unknown, ErrHidden if it is hidden.
func (s *Store) UnreadInbound(ctx context.Context, chatJID string) ([]Message, error) {
	tx, err := s.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := requireVisibleChat(ctx, tx, chatJID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT `+messageColumns+`
		FROM messages m JOIN chats c ON c.jid = m.chat_jid
		WHERE m.chat_jid = ? AND m.from_me = 0 AND m.ts > c.owner_read_at
		ORDER BY m.ts, m.pk
		LIMIT ?`, chatJID, maxUnreadInbound)
	if err != nil {
		return nil, fmt.Errorf("store: listar não lidas: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("store: ler não lida: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// LastInboundAt returns the time of the newest inbound message of a chat, or 0
// when there is none (or the chat is unknown).
func (s *Store) LastInboundAt(ctx context.Context, chatJID string) (int64, error) {
	var ts sql.NullInt64
	err := s.r.QueryRowContext(ctx,
		`SELECT MAX(ts) FROM messages WHERE chat_jid = ? AND from_me = 0`, chatJID).Scan(&ts)
	if err != nil {
		return 0, fmt.Errorf("store: última recebida: %w", err)
	}
	return ts.Int64, nil
}

// HasMessage reports whether message id exists in the chat.
func (s *Store) HasMessage(ctx context.Context, chatJID, id string) (bool, error) {
	var one int
	err := s.r.QueryRowContext(ctx,
		`SELECT 1 FROM messages WHERE chat_jid = ? AND id = ?`, chatJID, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: procurar mensagem: %w", err)
	}
	return true, nil
}

// Totals are the counts shown by the status command.
type Totals struct {
	Chats    int64 // every chat row, hidden ones included
	Hidden   int64
	Messages int64
	Pending  int64 // queued or sending
}

// Totals counts chats, hidden chats, messages and pending sends.
func (s *Store) Totals(ctx context.Context) (Totals, error) {
	var t Totals
	err := s.r.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM chats),
		       (SELECT COUNT(*) FROM chats WHERE hidden = 1),
		       (SELECT COUNT(*) FROM messages),
		       (SELECT COUNT(*) FROM send_queue WHERE status IN ('queued', 'sending'))`).
		Scan(&t.Chats, &t.Hidden, &t.Messages, &t.Pending)
	if err != nil {
		return Totals{}, fmt.Errorf("store: totais: %w", err)
	}
	return t, nil
}
