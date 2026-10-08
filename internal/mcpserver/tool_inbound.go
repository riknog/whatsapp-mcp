package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/riknog/whatsapp-mcp/internal/store"
)

// scanLimit bounds how many unseen chats a category filter reads. The filter
// runs after the store query, so it needs more than max_chats of them.
const scanLimit = 5000

type newMessagesIn struct {
	MaxChats      int    `json:"max_chats,omitempty" jsonschema:"how many chats to return, most recent first (default 10, max 30)"`
	PerChat       int    `json:"per_chat,omitempty" jsonschema:"newest unseen messages of each chat (default 5, max 20)"`
	IncludeGroups bool   `json:"include_groups,omitempty" jsonschema:"include group chats (default false)"`
	Category      string `json:"category,omitempty" jsonschema:"only chats in this category; see list_categories"`
	Peek          bool   `json:"peek,omitempty" jsonschema:"true does not mark the messages as seen for this agent"`
}

type newChatOut struct {
	Contact    string           `json:"contact"`
	ContactRef string           `json:"contact_ref"`
	Kind       string           `json:"kind" jsonschema:"direct or group"`
	Categories list[string]     `json:"categories"`
	NewCount   int              `json:"new_count" jsonschema:"all unseen messages of the chat, not only the ones returned"`
	Truncated  bool             `json:"truncated" jsonschema:"true when new_count is larger than the messages returned"`
	Messages   list[messageOut] `json:"messages" jsonschema:"the newest unseen messages, oldest first"`
}

type newMessagesOut struct {
	Chats     list[newChatOut] `json:"chats"`
	MoreChats int              `json:"more_chats" jsonschema:"chats with unseen messages that were not returned"`
	Notes     list[string]     `json:"notes,omitempty"`
	Error     *errBody         `json:"error,omitempty"`
}

func (o *newMessagesOut) setError(e *errBody) { o.Error = e }

func (e *env) listNewMessages(ctx context.Context, _ *mcp.CallToolRequest, in newMessagesIn) (*mcp.CallToolResult, newMessagesOut, error) {
	out, err := e.newMessages(ctx, in)
	if err != nil {
		return failed[newMessagesOut](err)
	}
	return nil, out, nil
}

func (e *env) newMessages(ctx context.Context, in newMessagesIn) (newMessagesOut, error) {
	var out newMessagesOut
	if err := e.requireSession(); err != nil {
		return out, err
	}
	maxChats := clampInt("max_chats", in.MaxChats, 10, 30, &out.Notes)
	perChat := clampInt("per_chat", in.PerChat, 5, 20, &out.Notes)
	cat := trimmed(in.Category)

	var selected []store.InboundChat
	var more int
	var includeGroups bool
	if cat == "" {
		includeGroups = in.IncludeGroups
		res, err := e.st.NewInbound(ctx, store.InboundFilter{
			IncludeGroups: includeGroups, MaxChats: maxChats, PerChat: perChat,
		})
		if err != nil {
			return out, err
		}
		selected, more = res.Chats, res.MoreChats
	} else {
		categories, err := e.categories(ctx)
		if err != nil {
			return out, err
		}
		c, ok := findCategory(categories, cat)
		if !ok {
			return out, unknownCategory()
		}
		// Grupos is itself a request for groups.
		includeGroups = in.IncludeGroups || c.name == catGroups
		res, err := e.st.NewInbound(ctx, store.InboundFilter{
			IncludeGroups: true, MaxChats: scanLimit, PerChat: perChat,
		})
		if err != nil {
			return out, err
		}
		var matched []store.InboundChat
		for _, ic := range res.Chats {
			if ic.Chat.Kind == "group" && !includeGroups {
				continue
			}
			labels, err := e.labelNames(ctx, ic.Chat.JID)
			if err != nil {
				return out, err
			}
			if hasCategory(categoriesOf(ic.Chat.Kind, labels), c.name) {
				matched = append(matched, ic)
			}
		}
		if len(matched) > maxChats {
			more = len(matched) - maxChats
			matched = matched[:maxChats]
		}
		selected = matched
		more += res.MoreChats
	}

	p, err := e.newPresenter(ctx)
	if err != nil {
		return out, err
	}
	out.Chats = list[newChatOut]{}
	var consumed []store.InboundChat
	for _, ic := range selected {
		labels, err := e.labelNames(ctx, ic.Chat.JID)
		if err != nil {
			return out, err
		}
		msgs := list[messageOut]{}
		for _, m := range ic.Messages {
			mo, err := p.message(ctx, m, ic.Chat)
			if err != nil {
				return out, err
			}
			msgs = append(msgs, mo)
		}
		out.Chats = append(out.Chats, newChatOut{
			Contact:    p.chatName(ic.Chat),
			ContactRef: ic.Chat.Ref,
			Kind:       ic.Chat.Kind,
			Categories: categoriesOf(ic.Chat.Kind, labels),
			NewCount:   ic.NewCount,
			Truncated:  ic.NewCount > len(ic.Messages),
			Messages:   msgs,
		})
		consumed = append(consumed, ic)
		if hidden := ic.NewCount - len(ic.Messages); hidden > 0 && !in.Peek {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"%s: %d mensagens novas mais antigas não foram mostradas e já contam como vistas; "+
					"use get_chat_messages(contact=%q, limit=%d) para lê-las.",
				p.chatName(ic.Chat), hidden, ic.Chat.Ref, min(ic.NewCount, maxWindow)))
		}
	}
	out.MoreChats = more

	// The cursor moves only after the output is built, and only for the chats
	// the model receives. Peek leaves it alone.
	if !in.Peek {
		for _, ic := range consumed {
			if err := e.st.AdvanceAgentCursor(ctx, ic.Chat.JID, ic.LatestPK); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}
