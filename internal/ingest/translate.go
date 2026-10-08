package ingest

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Placeholders stored in text for media and contacts, so that the model reads
// a short marker and never a vCard or a file name.
const (
	markImage       = "[imagem]"
	markVideo       = "[vídeo]"
	markDocument    = "[documento]"
	markSticker     = "[figurinha]"
	markLocation    = "[localização]"
	markLiveLoc     = "[localização ao vivo]"
	markContact     = "[contato]"
	markContacts    = "[contatos]"
	markUnsupported = "[conteúdo não suportado]"
)

// Translate converts a whatsmeow event into the matching ingest event. It
// returns false for events that ingest does not store, such as presence, app
// state kinds it does not use, protocol messages, reactions, status updates
// and newsletters. The returned value is one of the types in events.go.
func Translate(evt any) (any, bool) {
	switch e := evt.(type) {
	case *events.Message:
		if e.Message == nil {
			return nil, false
		}
		m, ok := messageFromInfo(e.Info, e.Message)
		if !ok {
			return nil, false
		}
		// whatsmeow has already unwrapped a view-once message: only its flags remain.
		if e.IsViewOnce || e.IsViewOnceV2 || e.IsViewOnceV2Extension || isViewOnce(e.RawMessage) {
			m.Media = nil
		}
		return m, true

	case *events.Receipt:
		return ReceiptEvent{
			Chat:     jidString(e.Chat),
			IsFromMe: e.IsFromMe,
			Type:     string(e.Type),
			Time:     e.Timestamp,
		}, true

	case *events.MarkChatAsRead:
		return ChatReadEvent{
			Chat: jidString(e.JID),
			Read: e.Action.GetRead(),
			Time: e.Timestamp,
		}, true

	case *events.Contact:
		return ContactEvent{
			JID:       jidString(e.JID),
			FullName:  e.Action.GetFullName(),
			FirstName: e.Action.GetFirstName(),
			LIDJID:    normalize(e.Action.GetLidJID()),
			PNJID:     normalize(e.Action.GetPnJID()),
		}, true

	case *events.PushName:
		return PushNameEvent{
			JID:  jidString(e.JID),
			Name: e.NewPushName,
			Alt:  jidString(e.JIDAlt),
		}, true

	case *events.BusinessName:
		return BusinessNameEvent{JID: jidString(e.JID), Name: e.NewBusinessName}, true

	case *events.LabelEdit:
		return LabelEditEvent{
			ID:           e.LabelID,
			Name:         e.Action.GetName(),
			Color:        e.Action.GetColor(),
			PredefinedID: e.Action.GetPredefinedID(),
			Deleted:      e.Action.GetDeleted(),
			Inactive:     e.Action.IsActive != nil && !e.Action.GetIsActive(),
			ListType:     e.Action.GetType().String(),
		}, true

	case *events.LabelAssociationChat:
		return LabelAssocEvent{
			Chat:    jidString(e.JID),
			Label:   e.LabelID,
			Labeled: e.Action.GetLabeled(),
		}, true

	case *events.GroupInfo:
		if e.Name == nil {
			return nil, false
		}
		return GroupNameEvent{JID: jidString(e.JID), Name: e.Name.Name}, true

	case *events.HistorySync:
		if e.Data == nil {
			return nil, false
		}
		return historySync(e.Data), true
	}
	return nil, false
}

// messageFromInfo builds a MessageEvent from a live message. Aliases hold every
// LID/PN pair that WhatsApp attached to the message.
func messageFromInfo(info types.MessageInfo, m *waE2E.Message) (MessageEvent, bool) {
	src := info.MessageSource
	ev, ok := buildMessage(jidString(src.Chat), jidString(src.Sender), string(info.ID), src.IsFromMe, info.Timestamp, m)
	if !ok {
		return MessageEvent{}, false
	}
	if !src.IsGroup {
		if src.IsFromMe {
			ev.Aliases = appendPair(ev.Aliases, src.Chat, src.RecipientAlt)
		} else {
			ev.Aliases = appendPair(ev.Aliases, src.Chat, src.SenderAlt)
		}
	}
	ev.Aliases = appendPair(ev.Aliases, src.Sender, src.SenderAlt)
	return ev, true
}

// webMessage builds a MessageEvent from one message of a history sync.
func webMessage(wm *waWeb.WebMessageInfo) (MessageEvent, bool) {
	key := wm.GetKey()
	if key == nil {
		return MessageEvent{}, false
	}
	chat := normalize(key.GetRemoteJID())
	sender := normalize(key.GetParticipant())
	if sender == "" {
		sender = chat
	}
	var ts time.Time
	if sec := wm.GetMessageTimestamp(); sec > 0 && sec <= math.MaxInt64 {
		ts = time.Unix(int64(sec), 0)
	}
	return buildMessage(chat, sender, key.GetID(), key.GetFromMe(), ts, wm.GetMessage())
}

// buildMessage applies the storage rules shared by live and history messages.
// The sender of an owner message is dropped, because the store keeps no owner JID.
func buildMessage(chat, sender, id string, fromMe bool, ts time.Time, m *waE2E.Message) (MessageEvent, bool) {
	if chat == "" || id == "" || skipChat(chat) {
		return MessageEvent{}, false
	}
	c, ok := contentOf(m)
	if !ok {
		return MessageEvent{}, false
	}
	ev := MessageEvent{
		Chat:     chat,
		ID:       id,
		FromMe:   fromMe,
		Time:     ts,
		Kind:     c.kind,
		Text:     c.text,
		Caption:  c.caption,
		QuotedID: c.quoted,
		Media:    c.media,
	}
	if !fromMe {
		ev.Sender = sender
	}
	return ev, true
}

// historySync translates a whole history sync payload.
func historySync(h *waHistorySync.HistorySync) HistorySyncEvent {
	ev := HistorySyncEvent{Type: h.GetSyncType().String()}
	for _, m := range h.GetPhoneNumberToLidMappings() {
		ev.Mappings = append(ev.Mappings, AliasPair{A: normalize(m.GetLidJID()), B: normalize(m.GetPnJID())})
	}
	for _, p := range h.GetPushnames() {
		ev.PushNames = append(ev.PushNames, PushNameEvent{JID: normalize(p.GetID()), Name: p.GetPushname()})
	}
	for _, c := range h.GetConversations() {
		conv := Conversation{
			JID:         normalize(c.GetID()),
			OldJID:      normalize(c.GetOldJID()),
			NewJID:      normalize(c.GetNewJID()),
			Name:        c.GetName(),
			UnreadCount: c.GetUnreadCount(),
		}
		for _, hm := range c.GetMessages() {
			if m, ok := webMessage(hm.GetMessage()); ok {
				conv.Messages = append(conv.Messages, m)
			}
		}
		ev.Conversations = append(ev.Conversations, conv)
	}
	return ev
}

// content is what a message contributes to the store.
type content struct {
	kind    string
	text    string
	caption string
	quoted  string
	media   *MediaRef
}

// downloadable is the part of a whatsmeow media message that read_media needs.
type downloadable interface {
	GetDirectPath() string
	GetMediaKey() []byte
	GetFileSHA256() []byte
	GetFileEncSHA256() []byte
	GetFileLength() uint64
	GetMimetype() string
}

// mediaRef keeps the download keys of a media message, or nil without them.
func mediaRef(kind string, x downloadable) *MediaRef {
	if x.GetDirectPath() == "" || len(x.GetMediaKey()) == 0 {
		return nil
	}
	return &MediaRef{
		Kind:          kind,
		Mimetype:      x.GetMimetype(),
		DirectPath:    x.GetDirectPath(),
		MediaKey:      x.GetMediaKey(),
		FileSHA256:    x.GetFileSHA256(),
		FileEncSHA256: x.GetFileEncSHA256(),
		FileLength:    x.GetFileLength(),
	}
}

// contentOf maps a WhatsApp message to a kind and its text. It returns false
// for protocol messages, reactions and other messages that are not stored.
func contentOf(m *waE2E.Message) (content, bool) {
	c, ok := contentOfUnwrapped(unwrap(m))
	if ok && isViewOnce(m) {
		// A view-once photo or audio was meant to be seen once by the owner: it
		// is never kept for read_media.
		c.media = nil
	}
	return c, ok
}

func contentOfUnwrapped(m *waE2E.Message) (content, bool) {
	if m == nil || skipMessage(m) {
		return content{}, false
	}
	switch {
	case m.Conversation != nil:
		return content{kind: "text", text: m.GetConversation()}, true
	case m.ExtendedTextMessage != nil:
		x := m.GetExtendedTextMessage()
		return content{kind: "text", text: x.GetText(), quoted: x.GetContextInfo().GetStanzaID()}, true
	case m.ImageMessage != nil:
		x := m.GetImageMessage()
		return content{kind: "image", text: markImage, caption: x.GetCaption(), quoted: x.GetContextInfo().GetStanzaID(),
			media: mediaRef("image", x)}, true
	case m.VideoMessage != nil:
		x := m.GetVideoMessage()
		return content{kind: "video", text: markVideo, caption: x.GetCaption(), quoted: x.GetContextInfo().GetStanzaID()}, true
	case m.AudioMessage != nil:
		x := m.GetAudioMessage()
		return content{kind: "audio", text: audioMark(x.GetSeconds()), quoted: x.GetContextInfo().GetStanzaID(),
			media: mediaRef("audio", x)}, true
	case m.DocumentMessage != nil:
		x := m.GetDocumentMessage()
		return content{kind: "document", text: markDocument, caption: x.GetCaption(), quoted: x.GetContextInfo().GetStanzaID()}, true
	case m.StickerMessage != nil:
		return content{kind: "sticker", text: markSticker, quoted: m.GetStickerMessage().GetContextInfo().GetStanzaID()}, true
	case m.LocationMessage != nil:
		x := m.GetLocationMessage()
		return content{kind: "location", text: markLocation, caption: x.GetName(), quoted: x.GetContextInfo().GetStanzaID()}, true
	case m.LiveLocationMessage != nil:
		x := m.GetLiveLocationMessage()
		return content{kind: "location", text: markLiveLoc, caption: x.GetCaption(), quoted: x.GetContextInfo().GetStanzaID()}, true
	case m.ContactMessage != nil:
		// The vCard is never stored: it carries the phone number.
		x := m.GetContactMessage()
		return content{kind: "contact", text: markContact, caption: x.GetDisplayName(), quoted: x.GetContextInfo().GetStanzaID()}, true
	case m.ContactsArrayMessage != nil:
		x := m.GetContactsArrayMessage()
		return content{kind: "contact", text: markContacts, caption: x.GetDisplayName(), quoted: x.GetContextInfo().GetStanzaID()}, true
	case m.InteractiveMessage != nil:
		x := m.GetInteractiveMessage()
		return content{kind: "text", text: x.GetBody().GetText(), quoted: x.GetContextInfo().GetStanzaID()}, true
	}
	return content{kind: "other", text: markUnsupported}, true
}

// skipMessage reports message kinds that are protocol traffic, not chat content.
func skipMessage(m *waE2E.Message) bool {
	return m.ProtocolMessage != nil || m.ReactionMessage != nil || m.EncReactionMessage != nil ||
		m.EncCommentMessage != nil || m.PinInChatMessage != nil || m.KeepInChatMessage != nil ||
		m.SenderKeyDistributionMessage != nil
}

// unwrap removes the containers WhatsApp wraps around the real message.
func unwrap(m *waE2E.Message) *waE2E.Message {
	for i := 0; i < 4 && m != nil; i++ {
		switch {
		case m.GetEphemeralMessage() != nil:
			m = m.GetEphemeralMessage().GetMessage()
		case m.GetViewOnceMessage() != nil:
			m = m.GetViewOnceMessage().GetMessage()
		case m.GetViewOnceMessageV2() != nil:
			m = m.GetViewOnceMessageV2().GetMessage()
		case m.GetViewOnceMessageV2Extension() != nil:
			m = m.GetViewOnceMessageV2Extension().GetMessage()
		case m.GetDocumentWithCaptionMessage() != nil:
			m = m.GetDocumentWithCaptionMessage().GetMessage()
		default:
			return m
		}
	}
	return m
}

// isViewOnce reports whether m, or a container inside it, is a view-once message.
func isViewOnce(m *waE2E.Message) bool {
	for i := 0; i < 4 && m != nil; i++ {
		if m.GetViewOnceMessage() != nil || m.GetViewOnceMessageV2() != nil || m.GetViewOnceMessageV2Extension() != nil {
			return true
		}
		switch {
		case m.GetEphemeralMessage() != nil:
			m = m.GetEphemeralMessage().GetMessage()
		case m.GetDocumentWithCaptionMessage() != nil:
			m = m.GetDocumentWithCaptionMessage().GetMessage()
		default:
			return m.GetImageMessage().GetViewOnce() || m.GetAudioMessage().GetViewOnce()
		}
	}
	return false
}

// audioMark shows the duration of an audio message, as in "[áudio 0:42]".
func audioMark(seconds uint32) string {
	if seconds == 0 {
		return "[áudio]"
	}
	return fmt.Sprintf("[áudio %d:%02d]", seconds/60, seconds%60)
}

// jidString is the store form of a whatsmeow JID: user@server, no device.
func jidString(j types.JID) string {
	if j.IsEmpty() {
		return ""
	}
	return j.ToNonAD().String()
}

// normalize parses s and returns its store form, or "" if it is not a JID.
func normalize(s string) string {
	// whatsmeow reads a string without "@" as a phone number: require user@server.
	if !strings.Contains(s, "@") {
		return ""
	}
	j, err := types.ParseJID(s)
	if err != nil {
		return ""
	}
	return jidString(j)
}

// appendPair adds (a, b) when both are set.
func appendPair(pairs []AliasPair, a, b types.JID) []AliasPair {
	if a.IsEmpty() || b.IsEmpty() {
		return pairs
	}
	return append(pairs, AliasPair{A: jidString(a), B: jidString(b)})
}

// skipChat reports chats that never become rows: status updates, broadcast
// lists and newsletters.
func skipChat(jid string) bool {
	server := jid[strings.LastIndexByte(jid, '@')+1:]
	return jid == "" || server == types.BroadcastServer || server == types.NewsletterServer
}

// cleanName removes formatting characters (category Cf, such as U+200E) and
// spaces from both ends of a name. Characters inside the name stay, so emoji
// joiners are kept.
func cleanName(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return unicode.Is(unicode.Cf, r) || unicode.IsSpace(r)
	})
}
