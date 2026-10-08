package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/riknog/whatsapp-mcp/internal/privacy"
)

// AddShareable allows share_contact to send this contact's card. Adding twice is a no-op.
func (s *Store) AddShareable(ctx context.Context, jid string) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO shareable_contacts (jid, added_at) VALUES (?, ?) ON CONFLICT (jid) DO NOTHING`,
		jid, s.now())
	if err != nil {
		return fmt.Errorf("store: adicionar compartilhável: %w", err)
	}
	return nil
}

// RemoveShareable removes a contact from the allowlist. ErrNotFound if it was not there.
func (s *Store) RemoveShareable(ctx context.Context, jid string) error {
	res, err := s.w.ExecContext(ctx, `DELETE FROM shareable_contacts WHERE jid = ?`, jid)
	if err != nil {
		return fmt.Errorf("store: remover compartilhável: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// IsShareable reports whether the contact is on the allowlist.
func (s *Store) IsShareable(ctx context.Context, jid string) (bool, error) {
	var one int
	err := s.r.QueryRowContext(ctx, `SELECT 1 FROM shareable_contacts WHERE jid = ?`, jid).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: verificar compartilhável: %w", err)
	}
	return true, nil
}

// ListShareable returns the allowlisted JIDs. Internal use only: never print them.
func (s *Store) ListShareable(ctx context.Context) ([]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT jid FROM shareable_contacts ORDER BY jid`)
	if err != nil {
		return nil, fmt.Errorf("store: listar compartilháveis: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return nil, fmt.Errorf("store: ler compartilhável: %w", err)
		}
		out = append(out, jid)
	}
	return out, rows.Err()
}

// Audit appends an entry to audit_log. The fields pass through privacy.RedactLog,
// so a phone number or JID that reaches them is masked before it is stored.
func (s *Store) Audit(ctx context.Context, action, chatRef, detail string) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO audit_log (ts, action, chat_ref, detail) VALUES (?, ?, ?, ?)`,
		s.now(), privacy.RedactLog(action), privacy.RedactLog(chatRef), privacy.RedactLog(detail))
	if err != nil {
		return fmt.Errorf("store: auditoria: %w", err)
	}
	return nil
}
