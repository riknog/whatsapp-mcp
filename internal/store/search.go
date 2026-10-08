package store

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

// SearchFilter drives Search. ChatJID "" searches every visible chat.
type SearchFilter struct {
	Query   string
	ChatJID string
	Limit   int // default 10, max 30
	Offset  int
}

// SearchHit is one matching message with an FTS snippet. The match is wrapped in «».
type SearchHit struct {
	Message Message
	Snippet string
}

// Search runs an FTS5 query over text and caption, skipping hidden chats. It
// returns one page and the total number of matches. Each query token is quoted
// as a phrase, so operators such as OR, NEAR, * or quotes typed by the user have
// no effect. Tokens are ANDed. An empty query after trimming is invalid_argument.
func (s *Store) Search(ctx context.Context, f SearchFilter) ([]SearchHit, int64, error) {
	match, err := ftsQuery(f.Query)
	if err != nil {
		return nil, 0, err
	}
	limit := clampLimit(f.Limit, 10, 30)
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	where := `messages_fts MATCH ? AND c.hidden = 0`
	args := []any{match}
	if f.ChatJID != "" {
		where += ` AND m.chat_jid = ?`
		args = append(args, f.ChatJID)
	}
	from := `FROM messages_fts
		JOIN messages m ON m.pk = messages_fts.rowid
		JOIN chats c ON c.jid = m.chat_jid
		WHERE ` + where

	var total int64
	if err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) `+from, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: contar busca: %w", err)
	}

	pageArgs := append([]any{"«", "»", "…"}, args...)
	pageArgs = append(pageArgs, limit, offset)
	rows, err := s.r.QueryContext(ctx, `
		SELECT `+messageColumns+`, snippet(messages_fts, -1, ?, ?, ?, 12) `+from+`
		ORDER BY m.ts DESC, m.pk DESC
		LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: buscar mensagens: %w", err)
	}
	defer rows.Close()

	var hits []SearchHit
	for rows.Next() {
		var m Message
		var fromMe int
		var snip string
		if err := rows.Scan(&m.ChatJID, &m.ID, &m.SenderJID, &fromMe, &m.TS, &m.Kind, &m.Text, &m.Caption, &m.QuotedID, &snip); err != nil {
			return nil, 0, fmt.Errorf("store: ler resultado: %w", err)
		}
		m.FromMe = fromMe == 1
		hits = append(hits, SearchHit{Message: m, Snippet: snip})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: ler resultados: %w", err)
	}
	return hits, total, nil
}

// ftsQuery turns free text into a safe FTS5 expression. Whitespace-separated
// tokens that contain no letter or digit (such as "!!!", "*" or a lone quote) are
// dropped: they have nothing to index and would only add an empty phrase. Each
// remaining token becomes a double-quoted phrase with inner quotes doubled, and
// the phrases are joined by spaces (implicit AND). NUL bytes are removed first.
// If no token is left, the query is invalid_argument.
//
// Example: "coracao !!!" searches for "coracao" only.
func ftsQuery(q string) (string, error) {
	var parts []string
	for _, f := range strings.Fields(strings.ReplaceAll(q, "\x00", "")) {
		if !hasLetterOrDigit(f) {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(f, `"`, `""`)+`"`)
	}
	if len(parts) == 0 {
		return "", toolerr.New(toolerr.CodeInvalidArgument, "a busca não pode ser vazia", nil)
	}
	return strings.Join(parts, " "), nil
}

func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
