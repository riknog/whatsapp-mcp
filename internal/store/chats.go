package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

const (
	defaultChatLimit = 50
	maxChatLimit     = 200
)

const chatColumns = `jid, ref, kind, display_name, last_message_at, agent_cursor, owner_read_at, hidden`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanChat(sc rowScanner) (Chat, error) {
	var c Chat
	var hidden int
	err := sc.Scan(&c.JID, &c.Ref, &c.Kind, &c.DisplayName, &c.LastMessageAt, &c.AgentCursor, &c.OwnerReadAt, &hidden)
	c.Hidden = hidden == 1
	return c, err
}

// UpsertChat inserts a chat or updates kind, display name and last message time.
// It never touches Ref, the cursors or Hidden: those are owned by other methods.
// LastMessageAt only moves forward.
func (s *Store) UpsertChat(ctx context.Context, c Chat) error {
	if c.JID == "" || c.Ref == "" {
		return toolerr.New(toolerr.CodeInvalidArgument, "chat sem jid ou ref", nil)
	}
	if c.Kind != "direct" && c.Kind != "group" {
		return toolerr.New(toolerr.CodeInvalidArgument, "kind de chat inválido", nil)
	}
	_, err := s.w.ExecContext(ctx, `
		INSERT INTO chats (jid, ref, kind, display_name, last_message_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (jid) DO UPDATE SET
		  kind            = excluded.kind,
		  display_name    = CASE WHEN excluded.display_name = '' THEN chats.display_name ELSE excluded.display_name END,
		  last_message_at = MAX(chats.last_message_at, excluded.last_message_at)`,
		c.JID, c.Ref, c.Kind, c.DisplayName, c.LastMessageAt)
	if err != nil {
		return fmt.Errorf("store: upsert chat: %w", err)
	}
	return nil
}

// GetChat returns the chat with this JID, or ErrNotFound.
func (s *Store) GetChat(ctx context.Context, jid string) (Chat, error) {
	return s.getChatWhere(ctx, s.r, "jid = ?", jid)
}

// GetChatByRef returns the chat with this contact_ref, or ErrNotFound.
func (s *Store) GetChatByRef(ctx context.Context, ref string) (Chat, error) {
	return s.getChatWhere(ctx, s.r, "ref = ?", ref)
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) getChatWhere(ctx context.Context, q queryer, where string, arg any) (Chat, error) {
	c, err := scanChat(q.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, ErrNotFound
	}
	if err != nil {
		return Chat{}, fmt.Errorf("store: ler chat: %w", err)
	}
	return c, nil
}

// ChatFilter narrows ListChats. Zero values mean "no filter" and the default page.
type ChatFilter struct {
	Kind          string // "" | "direct" | "group"
	LabelID       string // only chats carrying this label (category)
	IncludeHidden bool   // false in every tool; true only for the CLI
	Limit         int    // default 50, max 200
	Offset        int
}

// ListChats returns chats ordered by last message, most recent first.
func (s *Store) ListChats(ctx context.Context, f ChatFilter) ([]Chat, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultChatLimit
	}
	if limit > maxChatLimit {
		limit = maxChatLimit
	}
	var where []string
	var args []any
	if !f.IncludeHidden {
		where = append(where, "c.hidden = 0")
	}
	if f.Kind != "" {
		where = append(where, "c.kind = ?")
		args = append(args, f.Kind)
	}
	if f.LabelID != "" {
		where = append(where, "EXISTS (SELECT 1 FROM chat_labels cl WHERE cl.chat_jid = c.jid AND cl.label_id = ?)")
		args = append(args, f.LabelID)
	}
	q := `SELECT c.jid, c.ref, c.kind, c.display_name, c.last_message_at, c.agent_cursor, c.owner_read_at, c.hidden FROM chats c`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ") // #nosec G202 -- constant fragments; values are ? parameters
	}
	q += " ORDER BY c.last_message_at DESC, c.jid LIMIT ? OFFSET ?"
	args = append(args, limit, f.Offset)

	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: listar chats: %w", err)
	}
	defer rows.Close()
	var out []Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, fmt.Errorf("store: ler chat: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetHidden marks a chat hidden or visible. ErrNotFound if the chat is unknown.
func (s *Store) SetHidden(ctx context.Context, jid string, hidden bool) error {
	v := 0
	if hidden {
		v = 1
	}
	return s.updateChat(ctx, `UPDATE chats SET hidden = ? WHERE jid = ?`, v, jid)
}

// AdvanceAgentCursor moves agent_cursor forward to pk (a messages.pk value, the
// ingestion sequence). It never moves it back, so a late, old-timestamp message
// with a higher pk is still delivered.
func (s *Store) AdvanceAgentCursor(ctx context.Context, jid string, pk int64) error {
	return s.updateChat(ctx, `UPDATE chats SET agent_cursor = MAX(agent_cursor, ?) WHERE jid = ?`, pk, jid)
}

// SetOwnerReadAt moves owner_read_at forward to ts. It never moves it back.
func (s *Store) SetOwnerReadAt(ctx context.Context, jid string, ts int64) error {
	return s.updateChat(ctx, `UPDATE chats SET owner_read_at = MAX(owner_read_at, ?) WHERE jid = ?`, ts, jid)
}

func (s *Store) updateChat(ctx context.Context, query string, args ...any) error {
	res, err := s.w.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: atualizar chat: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: atualizar chat: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// PutAlias maps an alias JID (LID) to its canonical JID (PN).
func (s *Store) PutAlias(ctx context.Context, alias, canonical string) error {
	if alias == "" || canonical == "" || alias == canonical {
		return toolerr.New(toolerr.CodeInvalidArgument, "alias inválido", nil)
	}
	_, err := s.w.ExecContext(ctx, `
		INSERT INTO jid_aliases (alias_jid, canonical_jid) VALUES (?, ?)
		ON CONFLICT (alias_jid) DO UPDATE SET canonical_jid = excluded.canonical_jid`, alias, canonical)
	if err != nil {
		return fmt.Errorf("store: gravar alias: %w", err)
	}
	return nil
}

// Canonical returns the canonical JID for jid, or jid itself when it has no alias.
func (s *Store) Canonical(ctx context.Context, jid string) (string, error) {
	var canonical string
	err := s.r.QueryRowContext(ctx, `SELECT canonical_jid FROM jid_aliases WHERE alias_jid = ?`, jid).Scan(&canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return jid, nil
	}
	if err != nil {
		return "", fmt.Errorf("store: ler alias: %w", err)
	}
	return canonical, nil
}

// UpsertContact inserts or replaces the names of a contact.
func (s *Store) UpsertContact(ctx context.Context, c Contact) error {
	if c.JID == "" {
		return toolerr.New(toolerr.CodeInvalidArgument, "contato sem jid", nil)
	}
	_, err := s.w.ExecContext(ctx, `
		INSERT INTO contacts (jid, full_name, first_name, push_name, business_name, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (jid) DO UPDATE SET
		  full_name     = excluded.full_name,
		  first_name    = excluded.first_name,
		  push_name     = excluded.push_name,
		  business_name = excluded.business_name,
		  updated_at    = excluded.updated_at`,
		c.JID, c.FullName, c.FirstName, c.PushName, c.BusinessName, s.now())
	if err != nil {
		return fmt.Errorf("store: upsert contato: %w", err)
	}
	return nil
}

// ContactFilter pages ListContacts. Limit defaults to 50, max 200.
type ContactFilter struct {
	Limit  int
	Offset int
}

// ListContacts returns contacts ordered by JID, so that pages are stable.
func (s *Store) ListContacts(ctx context.Context, f ContactFilter) ([]Contact, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultChatLimit
	}
	if limit > maxChatLimit {
		limit = maxChatLimit
	}
	rows, err := s.r.QueryContext(ctx, `
		SELECT jid, full_name, first_name, push_name, business_name, updated_at
		FROM contacts ORDER BY jid LIMIT ? OFFSET ?`, limit, f.Offset)
	if err != nil {
		return nil, fmt.Errorf("store: listar contatos: %w", err)
	}
	defer rows.Close()
	var out []Contact
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.JID, &c.FullName, &c.FirstName, &c.PushName, &c.BusinessName, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: ler contato: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AllContactNames maps every JID that has a name to its display name (design
// §5 priority). Contacts with no name at all are left out.
func (s *Store) AllContactNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT jid, full_name, first_name, push_name, business_name FROM contacts`)
	if err != nil {
		return nil, fmt.Errorf("store: listar nomes: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.JID, &c.FullName, &c.FirstName, &c.PushName, &c.BusinessName); err != nil {
			return nil, fmt.Errorf("store: ler nome: %w", err)
		}
		if name := c.DisplayName(); name != "" {
			out[c.JID] = name
		}
	}
	return out, rows.Err()
}
