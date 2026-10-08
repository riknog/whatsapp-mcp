package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/store"
)

// Store is what the ingestor writes to. *store.Store satisfies it.
type Store interface {
	UpsertChat(ctx context.Context, c store.Chat) error
	InsertMessage(ctx context.Context, m store.Message) (bool, error)
	InsertMessages(ctx context.Context, msgs []store.Message) (int, error)
	SetOwnerReadAt(ctx context.Context, jid string, ts int64) error
	Canonical(ctx context.Context, jid string) (string, error)
	LinkAlias(ctx context.Context, alias, canonical string) error
	UpsertContactNames(ctx context.Context, c store.Contact) error
	UpsertLabel(ctx context.Context, l store.Label) error
	DeleteLabel(ctx context.Context, id string) error
	SetChatLabel(ctx context.Context, chatJID, labelID string, on bool) error
}

// Refs makes the opaque contact_ref of a chat. identity.Refs satisfies it.
type Refs interface {
	Ref(jid string) string
}

var _ Store = (*store.Store)(nil)

// maxPending bounds the label associations waiting for their label or chat.
const maxPending = 1000

// maxIgnored bounds the set of system list ids remembered as non-categories.
const maxIgnored = 1000

type labelLink struct {
	chat  string
	label string
}

// Ingestor applies events to the store. Handle is not safe for concurrent use:
// Run calls it from one goroutine, and tests call it directly.
type Ingestor struct {
	st   Store
	refs Refs
	clk  clock.Clock
	log  *slog.Logger

	pending []labelLink         // label attached to a chat that is not stored yet
	ignored map[string]struct{} // system list ids (never categories)
}

// New returns an Ingestor. A nil clock means clock.Real; a nil logger discards logs.
func New(st Store, refs Refs, clk clock.Clock, log *slog.Logger) *Ingestor {
	if clk == nil {
		clk = clock.Real{}
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Ingestor{st: st, refs: refs, clk: clk, log: log, ignored: map[string]struct{}{}}
}

// Run handles translated events (the values Translate returns, as
// wa.Client.Events delivers them) until ctx ends or the channel closes. A failed
// event is logged by type only and skipped; it does not stop the loop.
func (in *Ingestor) Run(ctx context.Context, events <-chan any) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if err := in.Handle(ctx, ev); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				in.log.Warn("evento não gravado", "tipo", fmt.Sprintf("%T", ev), "err", err)
			}
		}
	}
}

// Handle applies one event. Unknown types are ignored.
func (in *Ingestor) Handle(ctx context.Context, ev any) error {
	switch e := ev.(type) {
	case MessageEvent:
		return in.message(ctx, e)
	case ReceiptEvent:
		return in.receipt(ctx, e)
	case ChatReadEvent:
		return in.chatRead(ctx, e)
	case ContactEvent:
		return in.contact(ctx, e)
	case PushNameEvent:
		return in.pushName(ctx, e)
	case BusinessNameEvent:
		return in.businessName(ctx, e)
	case LabelEditEvent:
		return in.labelEdit(ctx, e)
	case LabelAssocEvent:
		return in.labelAssoc(ctx, e)
	case GroupNameEvent:
		return in.groupName(ctx, e)
	case HistorySyncEvent:
		return in.historySync(ctx, e)
	}
	return nil
}

// message stores one live message. An owner message also marks the chat as
// read, because the owner answered it (design §7).
func (in *Ingestor) message(ctx context.Context, e MessageEvent) error {
	if err := in.linkAll(ctx, e.Aliases); err != nil {
		return err
	}
	chat, err := in.canonical(ctx, e.Chat)
	if err != nil || chat == "" || skipChat(chat) {
		return err
	}
	if err := in.ensureChat(ctx, chat, ""); err != nil {
		return err
	}
	sender := ""
	if !e.FromMe {
		if sender, err = in.canonical(ctx, e.Sender); err != nil {
			return err
		}
	}
	ts := unixOr(e.Time, in.clk.Now())
	m := store.Message{
		ChatJID: chat, ID: e.ID, SenderJID: sender, FromMe: e.FromMe, TS: ts,
		Kind: e.Kind, Text: e.Text, Caption: e.Caption, QuotedID: e.QuotedID,
		Media: storeMedia(e.Media),
	}
	if _, err := in.st.InsertMessage(ctx, m); err != nil {
		return fmt.Errorf("ingest: gravar mensagem: %w", err)
	}
	if e.FromMe {
		return in.setOwnerRead(ctx, chat, ts)
	}
	return nil
}

// receipt marks a chat as read when the owner read it on another device. Only
// receipts sent by the owner count, and only the read and played kinds. The
// receipt sender is ignored: "sender" receipts go to the owner's own devices.
func (in *Ingestor) receipt(ctx context.Context, e ReceiptEvent) error {
	if !e.IsFromMe {
		return nil
	}
	switch e.Type {
	case "read", "read-self", "played", "played-self":
	default:
		return nil
	}
	chat, err := in.canonical(ctx, e.Chat)
	if err != nil || chat == "" {
		return err
	}
	return in.setOwnerRead(ctx, chat, unixOr(e.Time, in.clk.Now()))
}

// chatRead applies a whole-chat mark-as-read. Marking as unread does not move
// the cursor in v1.
func (in *Ingestor) chatRead(ctx context.Context, e ChatReadEvent) error {
	if !e.Read {
		return nil
	}
	chat, err := in.canonical(ctx, e.Chat)
	if err != nil || chat == "" {
		return err
	}
	return in.setOwnerRead(ctx, chat, unixOr(e.Time, in.clk.Now()))
}

func (in *Ingestor) contact(ctx context.Context, e ContactEvent) error {
	if err := in.link(ctx, e.JID, e.LIDJID); err != nil {
		return err
	}
	if err := in.link(ctx, e.JID, e.PNJID); err != nil {
		return err
	}
	return in.setNames(ctx, e.JID, store.Contact{
		FullName:     cleanName(e.FullName),
		FirstName:    cleanName(e.FirstName),
		PushName:     cleanName(e.PushName),
		BusinessName: cleanName(e.BusinessName),
	})
}

func (in *Ingestor) pushName(ctx context.Context, e PushNameEvent) error {
	if err := in.link(ctx, e.JID, e.Alt); err != nil {
		return err
	}
	return in.setNames(ctx, e.JID, store.Contact{PushName: cleanName(e.Name)})
}

func (in *Ingestor) businessName(ctx context.Context, e BusinessNameEvent) error {
	return in.setNames(ctx, e.JID, store.Contact{BusinessName: cleanName(e.Name)})
}

// setNames stores the given names for the canonical contact of jid. Empty
// names leave the stored values unchanged.
func (in *Ingestor) setNames(ctx context.Context, jid string, c store.Contact) error {
	if c.FullName == "" && c.FirstName == "" && c.PushName == "" && c.BusinessName == "" {
		return nil
	}
	canon, err := in.canonical(ctx, jid)
	if err != nil || canon == "" || skipChat(canon) {
		return err
	}
	c.JID = canon
	return in.st.UpsertContactNames(ctx, c)
}

// labelEdit stores a category label. System lists (UNREAD, FAVORITES, GROUPS and
// the other automatic ones) are not categories and are never stored.
func (in *Ingestor) labelEdit(ctx context.Context, e LabelEditEvent) error {
	if e.ID == "" {
		return nil
	}
	if !isCategoryList(e.ListType) {
		in.rememberIgnored(e.ID)
		in.dropPending(e.ID)
		return nil
	}
	if e.Deleted || e.Inactive {
		in.dropPending(e.ID)
		if err := in.st.DeleteLabel(ctx, e.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("ingest: apagar etiqueta: %w", err)
		}
		return nil
	}
	name := cleanName(e.Name)
	if name == "" {
		return nil
	}
	if err := in.st.UpsertLabel(ctx, store.Label{ID: e.ID, Name: name, Color: int(e.Color), Source: "whatsapp"}); err != nil {
		return fmt.Errorf("ingest: gravar etiqueta: %w", err)
	}
	return in.flushPending(ctx, "")
}

// labelAssoc attaches or detaches a category on a chat. An attach that arrives
// before its label or chat is kept and retried later.
func (in *Ingestor) labelAssoc(ctx context.Context, e LabelAssocEvent) error {
	chat, err := in.canonical(ctx, e.Chat)
	if err != nil || chat == "" || e.Label == "" {
		return err
	}
	if !e.Labeled {
		in.dropPendingPair(chat, e.Label)
		if err := in.st.SetChatLabel(ctx, chat, e.Label, false); err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("ingest: desmarcar etiqueta: %w", err)
		}
		return nil
	}
	if _, ignored := in.ignored[e.Label]; ignored {
		return nil
	}
	err = in.st.SetChatLabel(ctx, chat, e.Label, true)
	if errors.Is(err, store.ErrNotFound) {
		in.addPending(labelLink{chat: chat, label: e.Label})
		return nil
	}
	if err != nil {
		return fmt.Errorf("ingest: marcar etiqueta: %w", err)
	}
	return nil
}

func (in *Ingestor) groupName(ctx context.Context, e GroupNameEvent) error {
	name := cleanName(e.Name)
	chat, err := in.canonical(ctx, e.JID)
	if err != nil || chat == "" || name == "" {
		return err
	}
	return in.ensureChat(ctx, chat, name)
}

// historySync applies a history payload: aliases and names first, then each
// conversation in its own batch. A conversation that fails is logged and the
// rest still load.
func (in *Ingestor) historySync(ctx context.Context, e HistorySyncEvent) error {
	if e.Type == "INITIAL_STATUS_V3" {
		return nil // status updates are not chats
	}
	for _, p := range e.Mappings {
		if err := in.link(ctx, p.A, p.B); err != nil {
			return err
		}
	}
	for _, p := range e.PushNames {
		if err := in.pushName(ctx, p); err != nil {
			return err
		}
	}
	for _, c := range e.Conversations {
		if err := in.conversation(ctx, e.Type, c); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			in.log.Warn("conversa do histórico não gravada", "tipo_sync", e.Type, "err", err)
		}
	}
	return in.flushPending(ctx, "")
}

// conversation stores one chat of a history sync in a single batch.
func (in *Ingestor) conversation(ctx context.Context, syncType string, c Conversation) error {
	if err := in.link(ctx, c.OldJID, c.NewJID); err != nil {
		return err
	}
	if err := in.link(ctx, c.JID, c.NewJID); err != nil {
		return err
	}
	chat, err := in.canonical(ctx, c.JID)
	if err != nil || chat == "" || skipChat(chat) {
		return err
	}
	if err := in.ensureChat(ctx, chat, cleanName(c.Name)); err != nil {
		return err
	}
	msgs := make([]store.Message, 0, len(c.Messages))
	// Most senders of a conversation repeat: resolve each JID once per conversation.
	resolved := map[string]string{}
	for _, m := range c.Messages {
		sender := ""
		if !m.FromMe {
			if cached, ok := resolved[m.Sender]; ok {
				sender = cached
			} else {
				if sender, err = in.canonical(ctx, m.Sender); err != nil {
					return err
				}
				resolved[m.Sender] = sender
			}
		}
		msgs = append(msgs, store.Message{
			ChatJID: chat, ID: m.ID, SenderJID: sender, FromMe: m.FromMe, TS: unixOr(m.Time, time.Time{}),
			Kind: m.Kind, Text: m.Text, Caption: m.Caption, QuotedID: m.QuotedID,
			Media: storeMedia(m.Media),
		})
	}
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].TS < msgs[j].TS })
	if _, err := in.st.InsertMessages(ctx, msgs); err != nil {
		return fmt.Errorf("ingest: gravar conversa: %w", err)
	}
	if syncType == "INITIAL_BOOTSTRAP" {
		return in.seedOwnerRead(ctx, chat, msgs, c.UnreadCount)
	}
	return nil
}

// seedOwnerRead sets the owner's read point from the unread count of the first
// load, so that old history is not reported as new. With no unread messages the
// whole chat is read. With n unread, the point sits just before the n-th most
// recent inbound message. If every message is unread, nothing is set.
func (in *Ingestor) seedOwnerRead(ctx context.Context, chat string, msgs []store.Message, unread uint32) error {
	if len(msgs) == 0 {
		return nil
	}
	var ts int64
	if unread == 0 {
		ts = msgs[len(msgs)-1].TS
	} else {
		seen := uint32(0)
		boundary := -1
		for i := len(msgs) - 1; i >= 0 && boundary < 0; i-- {
			if msgs[i].FromMe {
				continue
			}
			seen++
			if seen == unread {
				boundary = i
			}
		}
		if boundary <= 0 {
			return nil
		}
		ts = msgs[boundary-1].TS
	}
	return in.setOwnerRead(ctx, chat, ts)
}

// setOwnerRead moves the owner's read point forward. A chat that is not stored
// yet is not an error: there is nothing to mark.
func (in *Ingestor) setOwnerRead(ctx context.Context, chat string, ts int64) error {
	err := in.st.SetOwnerReadAt(ctx, chat, ts)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ingest: marcar leitura: %w", err)
	}
	return nil
}

// ensureChat creates the chat row when it is missing and refreshes its kind and
// name. The ref is derived from the canonical JID once and never changes.
func (in *Ingestor) ensureChat(ctx context.Context, chat, name string) error {
	if skipChat(chat) {
		return nil
	}
	kind := "direct"
	if strings.HasSuffix(chat, "@g.us") {
		kind = "group"
	}
	err := in.st.UpsertChat(ctx, store.Chat{JID: chat, Ref: in.refs.Ref(chat), Kind: kind, DisplayName: name})
	if err != nil {
		return fmt.Errorf("ingest: gravar chat: %w", err)
	}
	// A label attached before this chat existed can be applied now.
	return in.flushPending(ctx, chat)
}

// canonical returns the canonical JID of jid (phone number when a LID is mapped),
// or "" when jid is not a JID.
func (in *Ingestor) canonical(ctx context.Context, jid string) (string, error) {
	j := normalize(jid)
	if j == "" {
		return "", nil
	}
	c, err := in.st.Canonical(ctx, j)
	if err != nil {
		return "", fmt.Errorf("ingest: resolver jid: %w", err)
	}
	return c, nil
}

func (in *Ingestor) linkAll(ctx context.Context, pairs []AliasPair) error {
	for _, p := range pairs {
		if err := in.link(ctx, p.A, p.B); err != nil {
			return err
		}
	}
	return nil
}

// link records that a LID and a phone-number JID are one account. The order of
// the two arguments does not matter. Pairs of any other shape are ignored.
func (in *Ingestor) link(ctx context.Context, a, b string) error {
	ja, jb := normalize(a), normalize(b)
	if ja == "" || jb == "" {
		return nil
	}
	lid, pn := ja, jb
	switch {
	case isLID(ja) && isPN(jb):
	case isLID(jb) && isPN(ja):
		lid, pn = jb, ja
	default:
		return nil
	}
	cur, err := in.st.Canonical(ctx, lid)
	if err != nil {
		return fmt.Errorf("ingest: ler alias: %w", err)
	}
	if cur == pn {
		return nil
	}
	if err := in.st.LinkAlias(ctx, lid, pn); err != nil {
		return fmt.Errorf("ingest: gravar alias: %w", err)
	}
	// Label attachments waiting on the LID now wait on the phone-number chat.
	for i := range in.pending {
		if in.pending[i].chat == lid {
			in.pending[i].chat = pn
		}
	}
	return nil
}

func (in *Ingestor) addPending(l labelLink) {
	for _, p := range in.pending {
		if p == l {
			return
		}
	}
	if len(in.pending) >= maxPending {
		in.pending = in.pending[1:]
	}
	in.pending = append(in.pending, l)
}

// flushPending retries the waiting label attachments of one chat, or of every
// chat when chat is "". An entry that still fails with ErrNotFound stays; any
// other error is logged and the entry is dropped.
func (in *Ingestor) flushPending(ctx context.Context, chat string) error {
	if len(in.pending) == 0 {
		return nil
	}
	rest := make([]labelLink, 0, len(in.pending))
	for _, p := range in.pending {
		if chat != "" && p.chat != chat {
			rest = append(rest, p)
			continue
		}
		err := in.st.SetChatLabel(ctx, p.chat, p.label, true)
		switch {
		case err == nil:
		case errors.Is(err, store.ErrNotFound):
			rest = append(rest, p)
		default:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			in.log.Warn("etiqueta pendente descartada", "err", err)
		}
	}
	in.pending = rest
	return nil
}

func (in *Ingestor) dropPending(label string) {
	kept := in.pending[:0]
	for _, p := range in.pending {
		if p.label != label {
			kept = append(kept, p)
		}
	}
	in.pending = kept
}

func (in *Ingestor) dropPendingPair(chat, label string) {
	kept := in.pending[:0]
	for _, p := range in.pending {
		if p.chat != chat || p.label != label {
			kept = append(kept, p)
		}
	}
	in.pending = kept
}

func (in *Ingestor) rememberIgnored(id string) {
	if len(in.ignored) >= maxIgnored {
		in.ignored = map[string]struct{}{}
	}
	in.ignored[id] = struct{}{}
}

// isCategoryList reports whether a label of this list type is a category. The
// rule: CUSTOM lists made by the owner, Business labels (predefined, or with no
// list type), and PREDEFINED ones. System lists are not categories.
func isCategoryList(listType string) bool {
	switch listType {
	case "NONE", "CUSTOM", "PREDEFINED":
		return true
	}
	return false
}

func isLID(jid string) bool { return strings.HasSuffix(jid, "@lid") }
func isPN(jid string) bool  { return strings.HasSuffix(jid, "@s.whatsapp.net") }

// storeMedia converts the download keys of a message to the store row, or nil.
func storeMedia(r *MediaRef) *store.Media {
	if r == nil {
		return nil
	}
	length := int64(math.MaxInt64)
	if r.FileLength <= math.MaxInt64 {
		length = int64(r.FileLength) // #nosec G115 -- bounded by the check above
	}
	return &store.Media{
		Kind:          r.Kind,
		Mimetype:      r.Mimetype,
		DirectPath:    r.DirectPath,
		MediaKey:      r.MediaKey,
		FileSHA256:    r.FileSHA256,
		FileEncSHA256: r.FileEncSHA256,
		FileLength:    length,
	}
}

// unixOr returns t as Unix seconds, or fallback when t is zero. A zero fallback
// yields 0, which no unread check treats as new.
func unixOr(t time.Time, fallback time.Time) int64 {
	if !t.IsZero() {
		return t.Unix()
	}
	if fallback.IsZero() {
		return 0
	}
	return fallback.Unix()
}
