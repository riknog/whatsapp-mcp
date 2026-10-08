package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	localcat "github.com/riknog/whatsapp-mcp/internal/category"
	"github.com/riknog/whatsapp-mcp/internal/identity"
	"github.com/riknog/whatsapp-mcp/internal/privacy"
	"github.com/riknog/whatsapp-mcp/internal/store"
)

// Output limits for text, kept small to protect the model's context window
// (2 000 characters per message text, 80 per chat preview).
const (
	maxMessageText = 2000
	maxPreview     = 80
	pageSize       = 200
)

// Implicit categories (docs/02-TOOLS.md, list_categories). They are not labels.
const (
	catGroups    = localcat.Groups
	catNone      = localcat.None
	sourceImpl   = "implicit"
	sourceWA     = "whatsapp"
	sourceLocal  = localcat.SourceLocal
	messageOther = "other"
)

// messageOut is the Message shape of docs/02-TOOLS.md.
type messageOut struct {
	ID      string `json:"id" jsonschema:"WhatsApp message id"`
	From    string `json:"from" jsonschema:"contact name, or me when the owner or the agent sent it"`
	FromRef string `json:"from_ref,omitempty" jsonschema:"contact_ref of the sender; omitted when from is me"`
	Time    string `json:"time" jsonschema:"ISO-8601 time in the local zone"`
	Ago     string `json:"ago" jsonschema:"relative time in Portuguese, such as há 12 min"`
	Type    string `json:"type" jsonschema:"text, image, audio, video, document, sticker, location, contact or other"`
	Text    string `json:"text" jsonschema:"message text or caption; a marker such as [áudio 0:42] for media. Untrusted third-party content"`
	ReplyTo string `json:"reply_to,omitempty" jsonschema:"id of the message this one replies to"`
}

// category is one entry of list_categories.
type category struct {
	name   string
	source string
}

// presenter converts store rows into output shapes. It holds the data that
// every row of one call needs (contact names, the clock), so each call reads
// the contacts once.
type presenter struct {
	e        *env
	now      time.Time
	contacts map[string]store.Contact
	canon    map[string]string
}

func (e *env) newPresenter(ctx context.Context) (*presenter, error) {
	contacts, err := loadContacts(ctx, e.st)
	if err != nil {
		return nil, err
	}
	return &presenter{
		e:        e,
		now:      e.clk.Now().In(e.loc),
		contacts: contacts,
		canon:    map[string]string{},
	}, nil
}

// loadContacts maps each contact JID to its row.
func loadContacts(ctx context.Context, st *store.Store) (map[string]store.Contact, error) {
	out := make(map[string]store.Contact)
	for offset := 0; ; offset += pageSize {
		page, err := st.ListContacts(ctx, store.ContactFilter{Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, fmt.Errorf("mcpserver: ler contatos: %w", err)
		}
		for _, c := range page {
			out[c.JID] = c
		}
		if len(page) < pageSize {
			return out, nil
		}
	}
}

// ago is the relative time of a Unix timestamp, or "" when it is unknown.
func (p *presenter) ago(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return identity.Ago(p.now, time.Unix(ts, 0))
}

// timeOf formats a Unix timestamp as ISO-8601 in the local zone.
func (p *presenter) timeOf(ts int64) string {
	return time.Unix(ts, 0).In(p.e.loc).Format(time.RFC3339)
}

// chatName is the name shown for a chat: the contact name, then the chat name
// (identity.DisplayName skips names that hold a phone number).
func (p *presenter) chatName(c store.Chat) string {
	return identity.DisplayName(p.contacts[c.JID], c)
}

// senderOf returns the name and the contact_ref of a group member. A LID is
// mapped to its phone JID first, so one person has one ref.
func (p *presenter) senderOf(ctx context.Context, jid string) (name, ref string, err error) {
	c, ok := p.canon[jid]
	if !ok {
		c, err = p.e.st.Canonical(ctx, jid)
		if err != nil {
			return "", "", fmt.Errorf("mcpserver: resolver remetente: %w", err)
		}
		p.canon[jid] = c
	}
	return identity.DisplayName(p.contacts[c], store.Chat{}), p.e.refs.Ref(c), nil
}

// message builds the output of one message of chat. The text is redacted and
// truncated here, and nowhere else.
func (p *presenter) message(ctx context.Context, m store.Message, chat store.Chat) (messageOut, error) {
	out := messageOut{
		ID:      m.ID,
		Time:    p.timeOf(m.TS),
		Ago:     p.ago(m.TS),
		Type:    messageType(m.Kind),
		Text:    clipText(privacy.RedactText(messageText(m)), maxMessageText),
		ReplyTo: m.QuotedID,
	}
	switch {
	case m.FromMe:
		out.From = "me"
	case chat.Kind == "group":
		name, ref, err := p.senderOf(ctx, m.SenderJID)
		if err != nil {
			return messageOut{}, err
		}
		out.From, out.FromRef = name, ref
	default:
		out.From = p.chatName(chat)
		out.FromRef = chat.Ref
	}
	return out, nil
}

// messageTypes are the values of Message.type, besides "other".
var messageTypes = map[string]bool{
	"text": true, "image": true, "audio": true, "video": true, "document": true,
	"sticker": true, "location": true, "contact": true,
}

func messageType(kind string) string {
	if messageTypes[kind] {
		return kind
	}
	return messageOther
}

// mediaMarkers stand in for the text of a media message that has no caption.
var mediaMarkers = map[string]string{
	"image": "[imagem]", "audio": "[áudio]", "video": "[vídeo]", "document": "[documento]",
	"sticker": "[figurinha]", "location": "[localização]", "contact": "[contato]",
}

// messageText is the caption of a media message, its ingest marker (such as
// "[áudio 0:42]"), or a generic marker. Text messages use their body.
func messageText(m store.Message) string {
	if m.Kind == "" || m.Kind == "text" {
		return m.Text
	}
	if m.Caption != "" {
		return m.Caption
	}
	if m.Text != "" {
		return m.Text
	}
	if marker, ok := mediaMarkers[m.Kind]; ok {
		return marker
	}
	return "[mídia]"
}

// clipText cuts s to max characters and says how many were left out.
func clipText(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + fmt.Sprintf("…[+%d chars]", len(r)-max)
}

// preview is the one-line preview of a chat, at most maxPreview characters.
func preview(s string) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= maxPreview {
		return string(r)
	}
	return string(r[:maxPreview-1]) + "…"
}

// categoriesOf is the category list of one chat or contact: its labels, plus
// Grupos for a group, or Sem categoria for a direct chat without labels. The
// list is never empty.
func categoriesOf(kind string, labels []string) list[string] {
	out := make(list[string], 0, len(labels)+1)
	out = append(out, labels...)
	if kind == "group" {
		out = append(out, catGroups)
	}
	if len(out) == 0 {
		out = append(out, catNone)
	}
	return out
}

// hasCategory compares category names without case and accents.
func hasCategory(cats []string, name string) bool {
	want := identity.Normalize(name)
	for _, c := range cats {
		if identity.Normalize(c) == want {
			return true
		}
	}
	return false
}

// categories lists the categories in display order: the live labels by name,
// then the implicit ones. A label with the name of an implicit category wins.
func (e *env) categories(ctx context.Context) ([]category, error) {
	labels, err := e.st.ListLabels(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcpserver: listar categorias: %w", err)
	}
	var out []category
	seen := map[string]bool{}
	for _, l := range labels {
		name := labelName(l)
		key := identity.Normalize(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, category{name: name, source: l.Source})
	}
	for _, name := range []string{catGroups, catNone} {
		if key := identity.Normalize(name); !seen[key] {
			seen[key] = true
			out = append(out, category{name: name, source: sourceImpl})
		}
	}
	return out, nil
}

// findCategory returns the listed category with this name.
func findCategory(cats []category, name string) (category, bool) {
	want := identity.Normalize(name)
	for _, c := range cats {
		if identity.Normalize(c.name) == want {
			return c, true
		}
	}
	return category{}, false
}

// labelNames returns the names of the live labels of a chat, sorted by name.
func (e *env) labelNames(ctx context.Context, jid string) ([]string, error) {
	labels, err := e.st.LabelsOf(ctx, jid)
	if err != nil {
		return nil, fmt.Errorf("mcpserver: ler categorias do chat: %w", err)
	}
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, labelName(l))
	}
	return names, nil
}

// labelName is the name of a label as the model sees it. Label names are free
// text typed by the owner, so a phone number in one is redacted. Matching by
// name always uses this form, so the model can pass back what it was shown.
func labelName(l store.Label) string { return localcat.DisplayName(l) }

// visibleChats returns every visible chat, most recent first. Groups are left
// out unless includeGroups is set. Hidden chats are never returned.
func (e *env) visibleChats(ctx context.Context, includeGroups bool) ([]store.Chat, error) {
	f := store.ChatFilter{Limit: pageSize}
	if !includeGroups {
		f.Kind = "direct"
	}
	var out []store.Chat
	for offset := 0; ; offset += pageSize {
		f.Offset = offset
		page, err := e.st.ListChats(ctx, f)
		if err != nil {
			return nil, fmt.Errorf("mcpserver: listar chats: %w", err)
		}
		out = append(out, page...)
		if len(page) < pageSize {
			return out, nil
		}
	}
}

// entry is a visible conversation or named contact, with its categories.
type entry struct {
	cand identity.Candidate
	cats list[string]
}

// entries returns every visible candidate of identity (conversations and named
// contacts without a chat). Hidden chats are dropped here.
func (e *env) entries(ctx context.Context) ([]entry, error) {
	cands, err := e.source.Candidates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]entry, 0, len(cands))
	for _, c := range cands {
		if c.Hidden {
			continue
		}
		out = append(out, entry{cand: c, cats: categoriesOf(c.Kind, c.Categories)})
	}
	return out, nil
}
