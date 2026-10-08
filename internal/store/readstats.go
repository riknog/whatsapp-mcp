package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// UnreadCounts returns, for every visible chat with unread inbound messages, how
// many inbound messages the owner has not read on the phone (ts > owner_read_at).
// It is the badge count of the chat. Chats with none are absent from the map.
// Hidden chats are never counted.
func (s *Store) UnreadCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT m.chat_jid, COUNT(*)
		FROM messages m
		JOIN chats c ON c.jid = m.chat_jid
		WHERE m.from_me = 0
		  AND c.hidden = 0
		  AND m.ts > c.owner_read_at
		GROUP BY m.chat_jid`)
	if err != nil {
		return nil, fmt.Errorf("store: contar não lidas: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int64)
	for rows.Next() {
		var jid string
		var n int64
		if err := rows.Scan(&jid, &n); err != nil {
			return nil, fmt.Errorf("store: ler não lidas: %w", err)
		}
		out[jid] = n
	}
	return out, rows.Err()
}

// PositionOf returns how many messages of the chat are newer than message id,
// in the same order RecentMessages uses. So it is the offset that makes that
// message the newest one of a page. ErrNotFound if the chat or the message is
// unknown, ErrHidden if the chat is hidden. It runs in one read-only snapshot.
func (s *Store) PositionOf(ctx context.Context, chatJID, id string) (int64, error) {
	tx, err := s.beginRead(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := requireVisibleChat(ctx, tx, chatJID); err != nil {
		return 0, err
	}
	var ts, pk int64
	err = tx.QueryRowContext(ctx,
		`SELECT ts, pk FROM messages WHERE chat_jid = ? AND id = ?`, chatJID, id).Scan(&ts, &pk)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("store: localizar mensagem: %w", err)
	}
	var n int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM messages
		WHERE chat_jid = ? AND (ts > ? OR (ts = ? AND pk > ?))`,
		chatJID, ts, ts, pk).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: posição da mensagem: %w", err)
	}
	return n, nil
}
