package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// InboundFilter drives NewInbound. Zero values mean the defaults.
type InboundFilter struct {
	IncludeGroups bool
	LabelID       string // "" = any chat; otherwise only chats carrying this label (category)
	MaxChats      int    // default 10
	PerChat       int    // default 5: the newest N messages of each chat are returned
}

// InboundChat is one conversation with unseen inbound messages.
type InboundChat struct {
	Chat     Chat
	NewCount int       // all unseen inbound messages in the chat, not just the returned ones
	Messages []Message // the newest PerChat of them, chronological
	LatestPK int64     // pk of the newest unseen message; pass to AdvanceAgentCursor to consume them
}

// InboundResult is the answer to NewInbound.
type InboundResult struct {
	Chats     []InboundChat // most recent first, at most MaxChats
	MoreChats int           // chats with unseen messages that did not fit in MaxChats
}

// NewInbound returns the unseen inbound messages, per chat. A message is unseen
// when all of these hold:
//
//	from_me = 0
//	chat not hidden
//	pk > chats.agent_cursor       (ingestion sequence: not yet consumed by the agent)
//	ts > chats.owner_read_at      (not already read on the owner's phone)
//
// Using pk for the agent cursor (not ts) means a message that arrives late with
// an old timestamp is still delivered. The whole read runs in one snapshot. It
// does not move the agent cursor; the caller does that with
// AdvanceAgentCursor(chat, LatestPK), unless it is a peek.
func (s *Store) NewInbound(ctx context.Context, f InboundFilter) (InboundResult, error) {
	maxChats := f.MaxChats
	if maxChats <= 0 {
		maxChats = 10
	}
	perChat := f.PerChat
	if perChat <= 0 {
		perChat = 5
	}

	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return InboundResult{}, fmt.Errorf("store: iniciar leitura: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	type unseen struct {
		jid      string
		count    int64
		latestTS int64
		latestPK int64
	}
	conds := []string{"(? = 1 OR c.kind <> 'group')"}
	args := []any{boolInt(f.IncludeGroups)}
	if f.LabelID != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM chat_labels cl WHERE cl.chat_jid = c.jid AND cl.label_id = ?)")
		args = append(args, f.LabelID)
	}
	// conds holds constant fragments; values are ? parameters.
	extra := strings.Join(conds, " AND ")
	// #nosec G202 -- extra is built from the constant fragments above
	q := `
		SELECT m.chat_jid, COUNT(*), MAX(m.ts), MAX(m.pk)
		FROM messages m
		JOIN chats c ON c.jid = m.chat_jid
		WHERE m.from_me = 0
		  AND c.hidden = 0
		  AND m.pk > c.agent_cursor
		  AND m.ts > c.owner_read_at
		  AND ` + extra + `
		GROUP BY m.chat_jid
		ORDER BY MAX(m.ts) DESC, m.chat_jid`
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return InboundResult{}, fmt.Errorf("store: contar novas: %w", err)
	}
	var all []unseen
	for rows.Next() {
		var u unseen
		if err := rows.Scan(&u.jid, &u.count, &u.latestTS, &u.latestPK); err != nil {
			_ = rows.Close()
			return InboundResult{}, fmt.Errorf("store: ler novas: %w", err)
		}
		all = append(all, u)
	}
	if err := rows.Close(); err != nil {
		return InboundResult{}, fmt.Errorf("store: fechar novas: %w", err)
	}
	if err := rows.Err(); err != nil {
		return InboundResult{}, fmt.Errorf("store: ler novas: %w", err)
	}

	res := InboundResult{}
	if len(all) > maxChats {
		res.MoreChats = len(all) - maxChats
		all = all[:maxChats]
	}
	for _, u := range all {
		chat, err := s.getChatWhere(ctx, tx, "jid = ?", u.jid)
		if err != nil {
			return InboundResult{}, err
		}
		msgRows, err := tx.QueryContext(ctx, `
			SELECT `+messageColumns+` FROM messages m
			WHERE m.chat_jid = ? AND m.from_me = 0 AND m.pk > ? AND m.ts > ?
			ORDER BY m.ts DESC, m.pk DESC LIMIT ?`, u.jid, chat.AgentCursor, chat.OwnerReadAt, perChat)
		if err != nil {
			return InboundResult{}, fmt.Errorf("store: ler novas mensagens: %w", err)
		}
		msgs, err := collectMessages(msgRows)
		if err != nil {
			return InboundResult{}, err
		}
		reverse(msgs)
		res.Chats = append(res.Chats, InboundChat{
			Chat:     chat,
			NewCount: int(u.count),
			Messages: msgs,
			LatestPK: u.latestPK,
		})
	}
	return res, nil
}
