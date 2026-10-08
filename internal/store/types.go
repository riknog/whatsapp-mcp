package store

import (
	"errors"
	"strings"
)

// ErrNotFound is returned when a row addressed by key does not exist.
var ErrNotFound = errors.New("store: not found")

// ErrInvalidState is returned when a queue transition is not allowed from the
// item's current status.
var ErrInvalidState = errors.New("store: invalid state transition")

// ErrHidden is returned by chat-scoped reads on a hidden chat (defence in depth:
// the tools must not show it, and the store refuses to read it).
var ErrHidden = errors.New("store: chat hidden")

// Chat is a row of chats. Ref is the opaque contact reference shown to the model.
type Chat struct {
	JID           string
	Ref           string
	Kind          string // "direct" | "group"
	DisplayName   string
	LastMessageAt int64 // 0 = no message yet
	AgentCursor   int64 // messages.pk of the last message consumed by the agent
	OwnerReadAt   int64
	Hidden        bool
}

// Contact is a row of contacts. Names come from WhatsApp; empty means unknown.
type Contact struct {
	JID          string
	FullName     string
	FirstName    string
	PushName     string
	BusinessName string
	UpdatedAt    int64
}

// DisplayName applies the design §5 priority: full_name, first_name, push_name,
// business_name. It returns "" when none is known.
func (c Contact) DisplayName() string {
	for _, n := range []string{c.FullName, c.FirstName, c.PushName, c.BusinessName} {
		if s := strings.TrimSpace(n); s != "" {
			return s
		}
	}
	return ""
}

// Message is a row of messages. Text and Caption are third-party content.
type Message struct {
	ChatJID   string
	ID        string
	SenderJID string
	FromMe    bool
	TS        int64
	Kind      string
	Text      string
	Caption   string
	QuotedID  string
	// Media is set by ingest for audio and image messages, and stored with the
	// message. Reads of messages leave it nil; GetMedia returns it.
	Media *Media
}

// Media is a row of media: how to download an audio or image message, and the
// text extracted from it. The keys decrypt the file stored on WhatsApp's
// servers; they never leave the process.
type Media struct {
	Kind          string // "image" | "audio"
	Mimetype      string
	DirectPath    string
	MediaKey      []byte
	FileSHA256    []byte
	FileEncSHA256 []byte
	FileLength    int64
	Extracted     string // raw text; redacted when shown
	ExtractedBy   string // "transcription" | "ocr" | ""
	ExtractedAt   int64
}

// Label is a row of labels. Source is "whatsapp" or "local".
type Label struct {
	ID      string
	Name    string
	Color   int
	Deleted bool
	Source  string
}

// Send queue statuses (design §4 and §8).
const (
	StatusQueued   = "queued"
	StatusSending  = "sending"
	StatusSent     = "sent"
	StatusFailed   = "failed"
	StatusExpired  = "expired"
	StatusRejected = "rejected"
)

// Send queue kinds.
const (
	KindText    = "text"
	KindContact = "contact"
)

// SendItem is a row of send_queue. TextHash is computed by EnqueueSend.
type SendItem struct {
	ID          int64
	ChatJID     string
	Kind        string
	Text        string
	SharedJID   string
	QuotedID    string
	TextHash    string
	Status      string
	EnqueuedAt  int64
	SentAt      int64
	WAMessageID string
	Error       string
}
