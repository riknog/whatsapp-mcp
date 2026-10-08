package mcpserver

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/riknog/whatsapp-mcp/internal/identity"
	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

// Limits of the message tools (docs/02-TOOLS.md).
const (
	defaultWindow   = 20
	maxWindow       = 50
	maxWindowOffset = 5000
	defaultSearch   = 10
	maxSearch       = 30
	maxSearchOffset = 1000
	minSearchRunes  = 2
	maxSnippetRunes = 400
)

type chatMessagesIn struct {
	Contact         string `json:"contact,omitempty" jsonschema:"contact name or contact_ref (required)"`
	Limit           int    `json:"limit,omitempty" jsonschema:"messages to return (default 20, max 50)"`
	Offset          int    `json:"offset,omitempty" jsonschema:"newest messages to skip (default 0, max 5000)"`
	AroundMessageID string `json:"around_message_id,omitempty" jsonschema:"centre the window on this message id; offset is then ignored"`
}

type chatMessagesOut struct {
	Contact    string           `json:"contact"`
	ContactRef string           `json:"contact_ref"`
	Messages   list[messageOut] `json:"messages" jsonschema:"oldest first"`
	Total      int64            `json:"total"`
	Offset     int64            `json:"offset"`
	HasOlder   bool             `json:"has_older"`
	HasNewer   bool             `json:"has_newer"`
	Notes      list[string]     `json:"notes,omitempty"`
	Error      *errBody         `json:"error,omitempty"`
}

func (o *chatMessagesOut) setError(e *errBody) { o.Error = e }

type searchIn struct {
	Query   string `json:"query,omitempty" jsonschema:"words to search for, at least 2 characters (required)"`
	Contact string `json:"contact,omitempty" jsonschema:"only this conversation: contact name or contact_ref"`
	Limit   int    `json:"limit,omitempty" jsonschema:"results to return (default 10, max 30)"`
	Offset  int    `json:"offset,omitempty" jsonschema:"results to skip (default 0, max 1000)"`
}

type searchHitOut struct {
	Contact    string     `json:"contact"`
	ContactRef string     `json:"contact_ref"`
	Message    messageOut `json:"message"`
	Snippet    string     `json:"snippet" jsonschema:"text around the match, the match wrapped in «»; untrusted third-party text"`
}

type searchOut struct {
	Results list[searchHitOut] `json:"results"`
	Total   int64              `json:"total"`
	Notes   list[string]       `json:"notes,omitempty"`
	Error   *errBody           `json:"error,omitempty"`
}

func (o *searchOut) setError(e *errBody) { o.Error = e }

func (e *env) getChatMessages(ctx context.Context, _ *mcp.CallToolRequest, in chatMessagesIn) (*mcp.CallToolResult, chatMessagesOut, error) {
	out, err := e.doGetChatMessages(ctx, in)
	if err != nil {
		return failed[chatMessagesOut](err)
	}
	return nil, out, nil
}

func (e *env) doGetChatMessages(ctx context.Context, in chatMessagesIn) (chatMessagesOut, error) {
	var out chatMessagesOut
	if err := e.requireSession(); err != nil {
		return out, err
	}
	name := trimmed(in.Contact)
	if err := requireContact(name); err != nil {
		return out, err
	}
	target, err := e.resolver.Resolve(ctx, name)
	if err != nil {
		return out, err
	}
	out.Contact, out.ContactRef = target.Name, target.Ref
	out.Messages = list[messageOut]{}
	if !target.HasChat {
		return out, nil
	}
	chat, err := e.st.GetChat(ctx, target.JID)
	if err != nil {
		return out, mapStoreErr(err)
	}
	limit := clampInt("limit", in.Limit, defaultWindow, maxWindow, &out.Notes)
	p, err := e.newPresenter(ctx)
	if err != nil {
		return out, err
	}

	var msgs []store.Message
	if id := trimmed(in.AroundMessageID); id != "" {
		if in.Offset > 0 {
			out.Notes = append(out.Notes, "offset ignorado porque around_message_id foi informado.")
		}
		msgs, err = e.st.MessagesAround(ctx, chat.JID, id, limit)
		if err != nil {
			return out, aroundErr(err)
		}
		if err := e.fillAround(ctx, &out, chat.JID, msgs); err != nil {
			return out, err
		}
	} else {
		offset := clampOffset("offset", in.Offset, maxWindowOffset, &out.Notes)
		msgs, err = e.st.RecentMessages(ctx, chat.JID, limit, offset)
		if err != nil {
			return out, mapStoreErr(err)
		}
		total, err := e.st.CountMessages(ctx, chat.JID)
		if err != nil {
			return out, mapStoreErr(err)
		}
		out.Total, out.Offset = total, int64(offset)
		out.HasOlder = int64(offset+len(msgs)) < total
		out.HasNewer = offset > 0
	}
	for _, m := range msgs {
		mo, err := p.message(ctx, m, chat)
		if err != nil {
			return out, err
		}
		out.Messages = append(out.Messages, mo)
	}
	return out, nil
}

// fillAround sets the paging fields of a window centred on a message. The
// window's newest message gives the offset; the oldest tells whether older
// messages exist.
func (e *env) fillAround(ctx context.Context, out *chatMessagesOut, jid string, msgs []store.Message) error {
	total, err := e.st.CountMessages(ctx, jid)
	if err != nil {
		return mapStoreErr(err)
	}
	newest, err := e.st.PositionOf(ctx, jid, msgs[len(msgs)-1].ID)
	if err != nil {
		return mapStoreErr(err)
	}
	oldest, err := e.st.PositionOf(ctx, jid, msgs[0].ID)
	if err != nil {
		return mapStoreErr(err)
	}
	out.Total = total
	out.Offset = newest
	out.HasNewer = newest > 0
	out.HasOlder = oldest+1 < total
	return nil
}

// aroundErr maps the store's answer for around_message_id.
func aroundErr(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return toolerr.New(toolerr.CodeInvalidArgument, "Mensagem não encontrada nesta conversa.", nil)
	}
	return mapStoreErr(err)
}

// mapStoreErr maps the store's chat errors to tool errors. Anything else
// is returned as is.
func mapStoreErr(err error) error {
	switch {
	case errors.Is(err, store.ErrHidden):
		return toolerr.New(toolerr.CodeChatHidden, "Esta conversa está oculta e não pode ser usada.", nil)
	case errors.Is(err, store.ErrNotFound):
		return toolerr.New(toolerr.CodeContactNotFound, "Conversa não encontrada.", nil)
	default:
		return err
	}
}

func (e *env) searchMessages(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
	out, err := e.doSearchMessages(ctx, in)
	if err != nil {
		return failed[searchOut](err)
	}
	return nil, out, nil
}

func (e *env) doSearchMessages(ctx context.Context, in searchIn) (searchOut, error) {
	var out searchOut
	if err := e.requireSession(); err != nil {
		return out, err
	}
	query := trimmed(in.Query)
	if utf8.RuneCountInString(query) < minSearchRunes {
		return out, toolerr.New(toolerr.CodeInvalidArgument, "A busca precisa de pelo menos 2 caracteres.", nil)
	}
	if err := identity.CheckFreeText(query); err != nil {
		return out, err
	}
	out.Results = list[searchHitOut]{}
	f := store.SearchFilter{
		Query:  query,
		Limit:  clampInt("limit", in.Limit, defaultSearch, maxSearch, &out.Notes),
		Offset: clampOffset("offset", in.Offset, maxSearchOffset, &out.Notes),
	}
	if name := trimmed(in.Contact); name != "" {
		target, err := e.resolver.Resolve(ctx, name)
		if err != nil {
			return out, err
		}
		if !target.HasChat {
			return out, nil
		}
		f.ChatJID = target.JID
	}

	hits, total, err := e.st.Search(ctx, f)
	if err != nil {
		return out, err
	}
	out.Total = total
	p, err := e.newPresenter(ctx)
	if err != nil {
		return out, err
	}
	chats := map[string]store.Chat{}
	for _, h := range hits {
		chat, ok := chats[h.Message.ChatJID]
		if !ok {
			chat, err = e.st.GetChat(ctx, h.Message.ChatJID)
			if err != nil {
				return out, mapStoreErr(err)
			}
			chats[chat.JID] = chat
		}
		mo, err := p.message(ctx, h.Message, chat)
		if err != nil {
			return out, err
		}
		out.Results = append(out.Results, searchHitOut{
			Contact:    p.chatName(chat),
			ContactRef: chat.Ref,
			Message:    mo,
			Snippet:    clipText(safeSnippet(h, query), maxSnippetRunes),
		})
	}
	return out, nil
}
