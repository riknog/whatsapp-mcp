package store

import (
	"context"
	"fmt"
)

// terminalSendStatuses are the queue states that can be deleted. Queued and
// sending items belong to the worker and are never touched by purges.
const terminalSendStatuses = `('sent', 'failed', 'expired', 'rejected')`

// PurgeOlderThan deletes messages with ts < before, and the finished send_queue
// rows enqueued before it (they hold outgoing text). The FTS entries go with the
// messages through the triggers. It returns how many messages were deleted.
func (s *Store) PurgeOlderThan(ctx context.Context, before int64) (int64, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: iniciar expurgo: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE ts < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("store: expurgar mensagens: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: expurgar mensagens: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM send_queue WHERE enqueued_at < ? AND status IN `+terminalSendStatuses, before); err != nil {
		return 0, fmt.Errorf("store: expurgar fila: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: confirmar expurgo: %w", err)
	}
	return n, nil
}

// PurgeChat deletes a chat's conversation data: its messages, its labels and its
// finished queue rows. The chats row stays, with jid, ref, kind and hidden kept
// and last_message_at, agent_cursor, owner_read_at and display_name reset. So a
// hidden chat stays hidden when it receives a message again. It returns how many
// messages were deleted. An unknown chat is a no-op.
func (s *Store) PurgeChat(ctx context.Context, jid string) (int64, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: iniciar expurgo: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE chat_jid = ?`, jid)
	if err != nil {
		return 0, fmt.Errorf("store: expurgar chat: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: expurgar chat: %w", err)
	}
	for _, q := range []string{
		`DELETE FROM chat_labels WHERE chat_jid = ?`,
		`DELETE FROM send_queue WHERE chat_jid = ? AND status IN ` + terminalSendStatuses,
		chatResetSQL + ` WHERE jid = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, jid); err != nil {
			return 0, fmt.Errorf("store: expurgar chat: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: confirmar expurgo: %w", err)
	}
	return n, nil
}

// chatResetSQL clears the per-conversation state of a chat row and keeps the
// identity and visibility fields (jid, ref, kind, hidden).
const chatResetSQL = `UPDATE chats SET last_message_at = 0, agent_cursor = 0, owner_read_at = 0, display_name = ''`

// PurgeAll deletes all conversation data: messages, chat state, chat labels,
// contact names, JID aliases and finished queue rows. The chats rows are not
// deleted: they are reset to their identity (jid, ref, kind) and their hidden
// flag is kept, so an owner's hidden chats stay hidden after a purge. It keeps
// the label definitions, the shareable allowlist (an explicit owner decision),
// the audit log and the schema version.
func (s *Store) PurgeAll(ctx context.Context) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: iniciar expurgo: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, q := range []string{
		`DELETE FROM messages`,
		`DELETE FROM send_queue WHERE status IN ` + terminalSendStatuses,
		`DELETE FROM chat_labels`,
		chatResetSQL,
		`DELETE FROM contacts`,
		`DELETE FROM jid_aliases`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("store: expurgar tudo: %w", err)
		}
	}
	return tx.Commit()
}

// Compact reclaims the file space left by purges and makes it unrecoverable.
// It first checkpoints the WAL (so deleted pages in the log reach the database
// file), then runs VACUUM, then merges the FTS index ('optimize') so that the
// tokens of deleted messages are dropped from its segments. secure_delete is
// on for every connection, so freed pages are zeroed too. Call it after a purge
// (the `purge` command). It must not run inside a transaction.
func (s *Store) Compact(ctx context.Context) error {
	if _, err := s.w.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("store: checkpoint: %w", err)
	}
	if _, err := s.w.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("store: vacuum: %w", err)
	}
	if _, err := s.w.ExecContext(ctx, `INSERT INTO messages_fts (messages_fts) VALUES ('optimize')`); err != nil {
		return fmt.Errorf("store: otimizar FTS: %w", err)
	}
	if _, err := s.w.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("store: checkpoint final: %w", err)
	}
	return nil
}
