package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

const (
	defaultMessageLimit = 20
	maxMessageLimit     = 50
)

const messageColumns = `m.chat_jid, m.id, m.sender_jid, m.from_me, m.ts, m.kind, m.text, m.caption, m.quoted_id`

// dbtx is what the read helpers need. Both *sql.DB and *sql.Tx satisfy it, so a
// chat-scoped read can run its visibility check and its query in one snapshot.
type dbtx interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func scanMessage(sc rowScanner) (Message, error) {
	var m Message
	var fromMe int
	err := sc.Scan(&m.ChatJID, &m.ID, &m.SenderJID, &fromMe, &m.TS, &m.Kind, &m.Text, &m.Caption, &m.QuotedID)
	m.FromMe = fromMe == 1
	return m, err
}

// InsertMessage stores a message. It is idempotent on (chat_jid, id): a repeat
// is ignored and returns false. The FTS index follows through its triggers. A
// new message also moves chats.last_message_at forward, in the same transaction.
func (s *Store) InsertMessage(ctx context.Context, m Message) (bool, error) {
	if m.ChatJID == "" || m.ID == "" || m.Kind == "" {
		return false, toolerr.New(toolerr.CodeInvalidArgument, "mensagem sem chat, id ou tipo", nil)
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: iniciar transação: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO messages (chat_jid, id, sender_jid, from_me, ts, kind, text, caption, quoted_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (chat_jid, id) DO NOTHING`,
		m.ChatJID, m.ID, m.SenderJID, boolInt(m.FromMe), m.TS, m.Kind, m.Text, m.Caption, m.QuotedID)
	if err != nil {
		return false, fmt.Errorf("store: inserir mensagem: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: inserir mensagem: %w", err)
	}
	if err := insertMedia(ctx, tx, m); err != nil {
		return false, err
	}
	if n == 0 {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("store: confirmar mídia: %w", err)
		}
		return false, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE chats SET last_message_at = MAX(last_message_at, ?) WHERE jid = ?`, m.TS, m.ChatJID); err != nil {
		return false, fmt.Errorf("store: atualizar último horário: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: confirmar mensagem: %w", err)
	}
	return true, nil
}

// beginRead starts a read-only snapshot. Chat-scoped reads use it so the
// visibility check and the query see the same data.
func (s *Store) beginRead(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("store: iniciar leitura: %w", err)
	}
	return tx, nil
}

// RecentMessages returns a page of a chat, newest first in the query and
// reversed to chronological order in the result. offset 0 is the latest message.
// limit defaults to 20 and is capped at 50. Ties on ts break by insertion order.
// ErrNotFound if the chat is unknown, ErrHidden if it is hidden.
func (s *Store) RecentMessages(ctx context.Context, chatJID string, limit, offset int) ([]Message, error) {
	limit = clampLimit(limit, defaultMessageLimit, maxMessageLimit)
	if offset < 0 {
		offset = 0
	}
	tx, err := s.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := requireVisibleChat(ctx, tx, chatJID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT `+messageColumns+` FROM messages m
		WHERE m.chat_jid = ?
		ORDER BY m.ts DESC, m.pk DESC
		LIMIT ? OFFSET ?`, chatJID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("store: listar mensagens: %w", err)
	}
	out, err := collectMessages(rows)
	if err != nil {
		return nil, err
	}
	reverse(out)
	return out, nil
}

// MessagesAround returns up to limit messages centred on message id in its chat,
// chronological, including the target. ErrNotFound if the chat is unknown or the
// message is not in it, ErrHidden if the chat is hidden. It runs in one read-only
// snapshot.
func (s *Store) MessagesAround(ctx context.Context, chatJID, id string, limit int) ([]Message, error) {
	limit = clampLimit(limit, defaultMessageLimit, maxMessageLimit)

	tx, err := s.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := requireVisibleChat(ctx, tx, chatJID); err != nil {
		return nil, err
	}
	var ts, pk int64
	err = tx.QueryRowContext(ctx,
		`SELECT ts, pk FROM messages WHERE chat_jid = ? AND id = ?`, chatJID, id).Scan(&ts, &pk)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: localizar mensagem: %w", err)
	}

	// Take up to half the window before the target. If the target is near the
	// start of the chat, the missing rows are taken from the newer side, so the
	// window keeps its size.
	before := (limit - 1) / 2
	older, err := windowRows(ctx, tx, chatJID, ts, pk, before, false)
	if err != nil {
		return nil, err
	}
	newer, err := windowRows(ctx, tx, chatJID, ts, pk, limit-len(older), true)
	if err != nil {
		return nil, err
	}
	if short := limit - len(older) - len(newer); short > 0 {
		if older, err = windowRows(ctx, tx, chatJID, ts, pk, before+short, false); err != nil {
			return nil, err
		}
	}
	reverse(older)
	return append(older, newer...), nil
}

// windowRows reads up to n messages strictly before (newer=false) or at-or-after
// (newer=true) the anchor (ts, pk). Older rows come back newest first; newer
// rows come back oldest first, and the anchor itself is the first of them.
func windowRows(ctx context.Context, q dbtx, chatJID string, ts, pk int64, n int, newer bool) ([]Message, error) {
	if n <= 0 {
		return nil, nil
	}
	var query string
	if newer {
		query = `SELECT ` + messageColumns + ` FROM messages m
		     WHERE m.chat_jid = ? AND (m.ts > ? OR (m.ts = ? AND m.pk >= ?))
		     ORDER BY m.ts ASC, m.pk ASC LIMIT ?`
	} else {
		query = `SELECT ` + messageColumns + ` FROM messages m
		     WHERE m.chat_jid = ? AND (m.ts < ? OR (m.ts = ? AND m.pk < ?))
		     ORDER BY m.ts DESC, m.pk DESC LIMIT ?`
	}
	rows, err := q.QueryContext(ctx, query, chatJID, ts, ts, pk, n)
	if err != nil {
		return nil, fmt.Errorf("store: janela de mensagens: %w", err)
	}
	return collectMessages(rows)
}

// CountMessages returns how many messages a chat holds. ErrNotFound if the chat
// is unknown, ErrHidden if it is hidden.
func (s *Store) CountMessages(ctx context.Context, chatJID string) (int64, error) {
	tx, err := s.beginRead(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := requireVisibleChat(ctx, tx, chatJID); err != nil {
		return 0, err
	}
	var n int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE chat_jid = ?`, chatJID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: contar mensagens: %w", err)
	}
	return n, nil
}

// requireVisibleChat is the single gate for chat-scoped reads: an unknown chat
// is ErrNotFound and a hidden one is ErrHidden. Run it in the same snapshot as
// the query that follows.
func requireVisibleChat(ctx context.Context, q dbtx, chatJID string) error {
	var hidden int
	err := q.QueryRowContext(ctx, `SELECT hidden FROM chats WHERE jid = ?`, chatJID).Scan(&hidden)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: verificar chat: %w", err)
	}
	if hidden == 1 {
		return ErrHidden
	}
	return nil
}

func collectMessages(rows *sql.Rows) ([]Message, error) {
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("store: ler mensagem: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func reverse(ms []Message) {
	for i, j := 0, len(ms)-1; i < j; i, j = i+1, j-1 {
		ms[i], ms[j] = ms[j], ms[i]
	}
}

func clampLimit(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
