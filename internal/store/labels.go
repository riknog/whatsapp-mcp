package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

// UpsertLabel inserts or updates a label. Source must be "whatsapp" or "local".
func (s *Store) UpsertLabel(ctx context.Context, l Label) error {
	if l.ID == "" || l.Name == "" {
		return toolerr.New(toolerr.CodeInvalidArgument, "etiqueta sem id ou nome", nil)
	}
	if l.Source != "whatsapp" && l.Source != "local" {
		return toolerr.New(toolerr.CodeInvalidArgument, "origem de etiqueta inválida", nil)
	}
	_, err := s.w.ExecContext(ctx, `
		INSERT INTO labels (id, name, color, deleted, source) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
		  name = excluded.name, color = excluded.color,
		  deleted = excluded.deleted, source = excluded.source`,
		l.ID, l.Name, l.Color, boolInt(l.Deleted), l.Source)
	if err != nil {
		return fmt.Errorf("store: upsert etiqueta: %w", err)
	}
	return nil
}

// DeleteLabel soft-deletes a label and removes it from every chat. ErrNotFound
// if the label is unknown.
func (s *Store) DeleteLabel(ctx context.Context, id string) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: iniciar transação: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `UPDATE labels SET deleted = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: apagar etiqueta: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chat_labels WHERE label_id = ?`, id); err != nil {
		return fmt.Errorf("store: remover etiqueta dos chats: %w", err)
	}
	return tx.Commit()
}

// SetChatLabel attaches or detaches a label. Both the chat and the live label
// must exist (ErrNotFound otherwise). Attaching twice is a no-op.
func (s *Store) SetChatLabel(ctx context.Context, chatJID, labelID string, on bool) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: iniciar transação: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if on {
		if err := requireExists(ctx, tx, `SELECT 1 FROM chats WHERE jid = ?`, chatJID); err != nil {
			return err
		}
		if err := requireExists(ctx, tx, `SELECT 1 FROM labels WHERE id = ? AND deleted = 0`, labelID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chat_labels (chat_jid, label_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, chatJID, labelID); err != nil {
			return fmt.Errorf("store: marcar etiqueta: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM chat_labels WHERE chat_jid = ? AND label_id = ?`, chatJID, labelID); err != nil {
			return fmt.Errorf("store: desmarcar etiqueta: %w", err)
		}
	}
	return tx.Commit()
}

func requireExists(ctx context.Context, tx *sql.Tx, query string, arg string) error {
	var one int
	err := tx.QueryRowContext(ctx, query, arg).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: verificar existência: %w", err)
	}
	return nil
}

// ListLabels returns the live labels, sorted by name.
func (s *Store) ListLabels(ctx context.Context) ([]Label, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT id, name, color, deleted, source FROM labels WHERE deleted = 0 ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("store: listar etiquetas: %w", err)
	}
	return collectLabels(rows)
}

// LabelsOf returns the live labels attached to a chat, sorted by name.
func (s *Store) LabelsOf(ctx context.Context, chatJID string) ([]Label, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT l.id, l.name, l.color, l.deleted, l.source
		FROM chat_labels cl JOIN labels l ON l.id = cl.label_id
		WHERE cl.chat_jid = ? AND l.deleted = 0
		ORDER BY l.name, l.id`, chatJID)
	if err != nil {
		return nil, fmt.Errorf("store: listar etiquetas do chat: %w", err)
	}
	return collectLabels(rows)
}

func collectLabels(rows *sql.Rows) ([]Label, error) {
	defer rows.Close()
	var out []Label
	for rows.Next() {
		var l Label
		var deleted int
		if err := rows.Scan(&l.ID, &l.Name, &l.Color, &deleted, &l.Source); err != nil {
			return nil, fmt.Errorf("store: ler etiqueta: %w", err)
		}
		l.Deleted = deleted == 1
		out = append(out, l)
	}
	return out, rows.Err()
}
