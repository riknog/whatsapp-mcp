package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

// InsertMessages stores a batch in one transaction, with the same rules as
// InsertMessage for each row: idempotent on (chat_jid, id), and last_message_at
// moves forward per chat. It returns how many rows were new. The batch is all or
// nothing: one invalid row rejects the whole call.
func (s *Store) InsertMessages(ctx context.Context, msgs []Message) (int, error) {
	for _, m := range msgs {
		if m.ChatJID == "" || m.ID == "" || m.Kind == "" {
			return 0, toolerr.New(toolerr.CodeInvalidArgument, "mensagem sem chat, id ou tipo", nil)
		}
	}
	if len(msgs) == 0 {
		return 0, nil
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: iniciar transação: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ins, err := tx.PrepareContext(ctx, `
		INSERT INTO messages (chat_jid, id, sender_jid, from_me, ts, kind, text, caption, quoted_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (chat_jid, id) DO NOTHING`)
	if err != nil {
		return 0, fmt.Errorf("store: preparar inserção: %w", err)
	}
	defer ins.Close()

	inserted := 0
	newest := map[string]int64{}
	for _, m := range msgs {
		res, err := ins.ExecContext(ctx,
			m.ChatJID, m.ID, m.SenderJID, boolInt(m.FromMe), m.TS, m.Kind, m.Text, m.Caption, m.QuotedID)
		if err != nil {
			return 0, fmt.Errorf("store: inserir mensagem: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("store: inserir mensagem: %w", err)
		}
		if n == 0 {
			continue
		}
		inserted++
		if m.TS > newest[m.ChatJID] {
			newest[m.ChatJID] = m.TS
		}
	}
	for chat, ts := range newest {
		if _, err := tx.ExecContext(ctx,
			`UPDATE chats SET last_message_at = MAX(last_message_at, ?) WHERE jid = ?`, ts, chat); err != nil {
			return 0, fmt.Errorf("store: atualizar último horário: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: confirmar mensagens: %w", err)
	}
	return inserted, nil
}

// LinkAlias records that alias (a LID) and canonical (a phone-number JID) are
// the same account. It is one transaction: the alias is stored, and every row
// keyed by the alias moves to the canonical JID: chat and its messages (rows
// already present under the canonical chat are dropped as duplicates), chat
// labels, the contact names, and message senders.
//
// Flags and read points merge by maximum: last_message_at, owner_read_at and
// hidden. A chat that was hidden or read stays so. agent_cursor merges by
// minimum: an unconsumed message of the alias chat whose pk is below the
// canonical cursor must still reach the agent. Showing a message twice is the
// lesser harm; hiding it is not. Calling it again with the same pair is a no-op.
func (s *Store) LinkAlias(ctx context.Context, alias, canonical string) error {
	if alias == "" || canonical == "" || alias == canonical {
		return toolerr.New(toolerr.CodeInvalidArgument, "alias inválido", nil)
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: iniciar transação: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO jid_aliases (alias_jid, canonical_jid) VALUES (?, ?)
		ON CONFLICT (alias_jid) DO UPDATE SET canonical_jid = excluded.canonical_jid`, alias, canonical); err != nil {
		return fmt.Errorf("store: gravar alias: %w", err)
	}
	// Aliases that pointed at the alias now point at the canonical JID.
	if _, err := tx.ExecContext(ctx,
		`UPDATE jid_aliases SET canonical_jid = ? WHERE canonical_jid = ? AND alias_jid <> ?`,
		canonical, alias, canonical); err != nil {
		return fmt.Errorf("store: encadear aliases: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE messages SET sender_jid = ? WHERE sender_jid = ?`, canonical, alias); err != nil {
		return fmt.Errorf("store: mover remetentes: %w", err)
	}
	if err := mergeChatRow(ctx, tx, alias, canonical); err != nil {
		return err
	}
	if err := mergeContactRow(ctx, tx, alias, canonical); err != nil {
		return err
	}
	return tx.Commit()
}

// mergeChatRow moves the chat from to the canonical chat. It runs inside the
// LinkAlias transaction.
func mergeChatRow(ctx context.Context, tx *sql.Tx, from, to string) error {
	// Messages already stored under the canonical chat win; the alias copies are dropped.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM messages WHERE chat_jid = ? AND id IN (SELECT id FROM messages WHERE chat_jid = ?)`,
		from, to); err != nil {
		return fmt.Errorf("store: descartar mensagens duplicadas: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE messages SET chat_jid = ? WHERE chat_jid = ?`, to, from); err != nil {
		return fmt.Errorf("store: mover mensagens: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO chat_labels (chat_jid, label_id)
		SELECT ?, label_id FROM chat_labels WHERE chat_jid = ?
		ON CONFLICT DO NOTHING`, to, from); err != nil {
		return fmt.Errorf("store: mover etiquetas: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chat_labels WHERE chat_jid = ?`, from); err != nil {
		return fmt.Errorf("store: remover etiquetas antigas: %w", err)
	}

	fromChat, errFrom := scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE jid = ?`, from))
	if errors.Is(errFrom, sql.ErrNoRows) {
		return nil // nothing stored under the alias chat
	}
	if errFrom != nil {
		return fmt.Errorf("store: ler chat antigo: %w", errFrom)
	}
	toChat, errTo := scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE jid = ?`, to))
	if errors.Is(errTo, sql.ErrNoRows) {
		// The canonical chat does not exist yet: the alias row takes its JID and keeps its ref.
		if _, err := tx.ExecContext(ctx, `UPDATE chats SET jid = ? WHERE jid = ?`, to, from); err != nil {
			return fmt.Errorf("store: renomear chat: %w", err)
		}
		return nil
	}
	if errTo != nil {
		return fmt.Errorf("store: ler chat canônico: %w", errTo)
	}
	displayName := toChat.DisplayName
	if displayName == "" {
		displayName = fromChat.DisplayName
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE chats SET
		  last_message_at = MAX(last_message_at, ?),
		  owner_read_at   = MAX(owner_read_at, ?),
		  agent_cursor    = MIN(agent_cursor, ?),
		  hidden          = MAX(hidden, ?),
		  display_name    = ?
		WHERE jid = ?`,
		fromChat.LastMessageAt, fromChat.OwnerReadAt, fromChat.AgentCursor, boolInt(fromChat.Hidden),
		displayName, to); err != nil {
		return fmt.Errorf("store: unir chats: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chats WHERE jid = ?`, from); err != nil {
		return fmt.Errorf("store: remover chat antigo: %w", err)
	}
	return nil
}

// mergeContactRow moves the names stored under the alias to the canonical
// contact. Names already on the canonical row win; empty fields are filled.
func mergeContactRow(ctx context.Context, tx *sql.Tx, from, to string) error {
	var old Contact
	err := tx.QueryRowContext(ctx, `
		SELECT jid, full_name, first_name, push_name, business_name, updated_at
		FROM contacts WHERE jid = ?`, from).
		Scan(&old.JID, &old.FullName, &old.FirstName, &old.PushName, &old.BusinessName, &old.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: ler contato antigo: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO contacts (jid, full_name, first_name, push_name, business_name, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (jid) DO UPDATE SET
		  full_name     = CASE WHEN contacts.full_name = ''     THEN excluded.full_name     ELSE contacts.full_name END,
		  first_name    = CASE WHEN contacts.first_name = ''    THEN excluded.first_name    ELSE contacts.first_name END,
		  push_name     = CASE WHEN contacts.push_name = ''     THEN excluded.push_name     ELSE contacts.push_name END,
		  business_name = CASE WHEN contacts.business_name = '' THEN excluded.business_name ELSE contacts.business_name END`,
		to, old.FullName, old.FirstName, old.PushName, old.BusinessName, old.UpdatedAt); err != nil {
		return fmt.Errorf("store: unir contatos: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM contacts WHERE jid = ?`, from); err != nil {
		return fmt.Errorf("store: remover contato antigo: %w", err)
	}
	return nil
}

// UpsertContactNames stores the non-empty names of c and keeps the stored value
// for every name that c leaves empty. Use it for live events, which carry one
// name at a time; UpsertContact replaces the whole row.
func (s *Store) UpsertContactNames(ctx context.Context, c Contact) error {
	if c.JID == "" {
		return toolerr.New(toolerr.CodeInvalidArgument, "contato sem jid", nil)
	}
	_, err := s.w.ExecContext(ctx, `
		INSERT INTO contacts (jid, full_name, first_name, push_name, business_name, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (jid) DO UPDATE SET
		  full_name     = CASE WHEN excluded.full_name = ''     THEN contacts.full_name     ELSE excluded.full_name END,
		  first_name    = CASE WHEN excluded.first_name = ''    THEN contacts.first_name    ELSE excluded.first_name END,
		  push_name     = CASE WHEN excluded.push_name = ''     THEN contacts.push_name     ELSE excluded.push_name END,
		  business_name = CASE WHEN excluded.business_name = '' THEN contacts.business_name ELSE excluded.business_name END,
		  updated_at    = excluded.updated_at`,
		c.JID, c.FullName, c.FirstName, c.PushName, c.BusinessName, s.now())
	if err != nil {
		return fmt.Errorf("store: atualizar nomes: %w", err)
	}
	return nil
}
