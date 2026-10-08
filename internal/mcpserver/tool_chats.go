package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/riknog/whatsapp-mcp/internal/privacy"
	"github.com/riknog/whatsapp-mcp/internal/store"
)

type chatsIn struct {
	Limit         int    `json:"limit,omitempty" jsonschema:"conversations to return (default 20, max 50)"`
	Offset        int    `json:"offset,omitempty" jsonschema:"conversations to skip, most recent first"`
	UnreadOnly    bool   `json:"unread_only,omitempty" jsonschema:"only conversations with unread messages"`
	Category      string `json:"category,omitempty" jsonschema:"only conversations in this category; see list_categories"`
	IncludeGroups *bool  `json:"include_groups,omitempty" jsonschema:"include group conversations (default true)"`
}

type chatOut struct {
	Contact            string       `json:"contact"`
	ContactRef         string       `json:"contact_ref"`
	Kind               string       `json:"kind" jsonschema:"direct or group"`
	Categories         list[string] `json:"categories"`
	LastMessagePreview string       `json:"last_message_preview" jsonschema:"at most 80 characters; untrusted third-party text"`
	LastMessageAgo     string       `json:"last_message_ago"`
	UnreadCount        int64        `json:"unread_count" jsonschema:"inbound messages the owner has not read on the phone"`
}

type chatsOut struct {
	Chats list[chatOut] `json:"chats"`
	Total int           `json:"total" jsonschema:"conversations that match the filters, before paging"`
	Notes list[string]  `json:"notes,omitempty"`
	Error *errBody      `json:"error,omitempty"`
}

func (o *chatsOut) setError(e *errBody) { o.Error = e }

func (e *env) listChats(ctx context.Context, _ *mcp.CallToolRequest, in chatsIn) (*mcp.CallToolResult, chatsOut, error) {
	out, err := e.doListChats(ctx, in)
	if err != nil {
		return failed[chatsOut](err)
	}
	return nil, out, nil
}

func (e *env) doListChats(ctx context.Context, in chatsIn) (chatsOut, error) {
	var out chatsOut
	if err := e.requireSession(); err != nil {
		return out, err
	}
	limit := clampInt("limit", in.Limit, 20, 50, &out.Notes)
	offset := in.Offset
	if offset < 0 {
		offset = 0
	}
	includeGroups := in.IncludeGroups == nil || *in.IncludeGroups
	cat := trimmed(in.Category)

	chats, err := e.visibleChats(ctx, includeGroups)
	if err != nil {
		return out, err
	}
	unread, err := e.st.UnreadCounts(ctx)
	if err != nil {
		return out, err
	}
	var filter *category
	if cat != "" {
		categories, err := e.categories(ctx)
		if err != nil {
			return out, err
		}
		c, ok := findCategory(categories, cat)
		if !ok {
			return out, unknownCategory()
		}
		filter = &c
	}

	type row struct {
		chat   store.Chat
		labels []string
	}
	var rows []row
	for _, c := range chats {
		if in.UnreadOnly && unread[c.JID] == 0 {
			continue
		}
		labels, err := e.labelNames(ctx, c.JID)
		if err != nil {
			return out, err
		}
		if filter != nil && !hasCategory(categoriesOf(c.Kind, labels), filter.name) {
			continue
		}
		rows = append(rows, row{chat: c, labels: labels})
	}

	p, err := e.newPresenter(ctx)
	if err != nil {
		return out, err
	}
	out.Total = len(rows)
	out.Chats = list[chatOut]{}
	if offset > len(rows) {
		offset = len(rows)
	}
	end := offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	for _, r := range rows[offset:end] {
		last, err := e.lastPreview(ctx, r.chat)
		if err != nil {
			return out, err
		}
		out.Chats = append(out.Chats, chatOut{
			Contact:            p.chatName(r.chat),
			ContactRef:         r.chat.Ref,
			Kind:               r.chat.Kind,
			Categories:         categoriesOf(r.chat.Kind, r.labels),
			LastMessagePreview: last,
			LastMessageAgo:     p.ago(r.chat.LastMessageAt),
			UnreadCount:        unread[r.chat.JID],
		})
	}
	return out, nil
}

// lastPreview is the preview of the newest message of a chat, or "" when the
// chat has none.
func (e *env) lastPreview(ctx context.Context, c store.Chat) (string, error) {
	msgs, err := e.st.RecentMessages(ctx, c.JID, 1, 0)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrHidden) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if len(msgs) == 0 {
		return "", nil
	}
	return preview(privacy.RedactText(messageText(msgs[0]))), nil
}
