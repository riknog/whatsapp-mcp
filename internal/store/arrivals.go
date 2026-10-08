package store

import (
	"context"
	"fmt"
)

// MaxMessagePK returns the highest messages.pk, or 0 when there is no message.
// A watcher starts from it so that only later arrivals are reported.
func (s *Store) MaxMessagePK(ctx context.Context) (int64, error) {
	var pk int64
	if err := s.r.QueryRowContext(ctx, `SELECT COALESCE(MAX(pk), 0) FROM messages`).Scan(&pk); err != nil {
		return 0, fmt.Errorf("store: ler última mensagem: %w", err)
	}
	return pk, nil
}

// Arrival is a visible chat that received inbound messages after a point.
type Arrival struct {
	Chat     Chat
	Count    int   // inbound messages after the point that are still unseen
	LatestPK int64 // highest pk among them; the next point
}

// ArrivalsSince returns the chats with inbound messages ingested after the pk
// after, most recent first. A message counts only while it is unseen, as in
// NewInbound: not from the owner, chat not hidden, not consumed by the agent
// and not read on the owner's phone. Groups count only with includeGroups.
func (s *Store) ArrivalsSince(ctx context.Context, after int64, includeGroups bool) ([]Arrival, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT `+chatColumnsC+`, COUNT(*), MAX(m.pk)
		FROM messages m
		JOIN chats c ON c.jid = m.chat_jid
		WHERE m.pk > ?
		  AND m.from_me = 0
		  AND c.hidden = 0
		  AND m.pk > c.agent_cursor
		  AND m.ts > c.owner_read_at
		  AND (? = 1 OR c.kind <> 'group')
		GROUP BY m.chat_jid
		ORDER BY MAX(m.ts) DESC, m.chat_jid`, after, boolInt(includeGroups))
	if err != nil {
		return nil, fmt.Errorf("store: ler chegadas: %w", err)
	}
	defer rows.Close()
	var out []Arrival
	for rows.Next() {
		var a Arrival
		var hidden int
		c := &a.Chat
		if err := rows.Scan(&c.JID, &c.Ref, &c.Kind, &c.DisplayName, &c.LastMessageAt, &c.AgentCursor,
			&c.OwnerReadAt, &hidden, &a.Count, &a.LatestPK); err != nil {
			return nil, fmt.Errorf("store: ler chegada: %w", err)
		}
		c.Hidden = hidden == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

// chatColumnsC is chatColumns qualified with the alias c.
const chatColumnsC = `c.jid, c.ref, c.kind, c.display_name, c.last_message_at, c.agent_cursor, c.owner_read_at, c.hidden`
