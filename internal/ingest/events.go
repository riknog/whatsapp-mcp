package ingest

import "time"

// Event types produced by Translate and consumed by Ingestor.Handle. JIDs are
// strings in "user@server" form. Times are wall-clock values from WhatsApp; a
// zero time means the source had none.

// AliasPair names two JIDs of the same account. Ingest keeps the pair only when
// one is a LID and the other a phone-number JID.
type AliasPair struct {
	A, B string
}

// MessageEvent is one chat message, live or from history.
type MessageEvent struct {
	Chat     string
	Sender   string // empty for messages sent by the owner
	ID       string
	FromMe   bool
	Time     time.Time
	Kind     string // text|image|audio|video|document|sticker|location|contact|other
	Text     string
	Caption  string
	QuotedID string
	Aliases  []AliasPair // LID/PN pairs seen on this message
	Media    *MediaRef   // set for audio and image messages that can be downloaded
}

// MediaRef is what read_media needs to download an audio or image message
// later. The keys only decrypt that one file.
type MediaRef struct {
	Kind          string // "audio" | "image"
	Mimetype      string
	DirectPath    string
	MediaKey      []byte
	FileSHA256    []byte
	FileEncSHA256 []byte
	FileLength    uint64
}

// ReceiptEvent is a delivery or read receipt. Type is the whatsmeow receipt type
// ("read", "read-self", "played", "played-self", "sender", ...).
type ReceiptEvent struct {
	Chat     string
	IsFromMe bool
	Type     string
	Time     time.Time
}

// ChatReadEvent is an app-state mark-as-read (or unread) of a whole chat.
type ChatReadEvent struct {
	Chat string
	Read bool
	Time time.Time
}

// ContactEvent carries the names WhatsApp knows for a contact. Empty fields mean unknown.
type ContactEvent struct {
	JID          string
	FullName     string
	FirstName    string
	PushName     string
	BusinessName string
	LIDJID       string // LID of JID, when the source gives one
	PNJID        string // phone-number JID of JID, when the source gives one
}

// PushNameEvent is a push name change. Alt is the other address of the same account, if known.
type PushNameEvent struct {
	JID  string
	Name string
	Alt  string
}

// BusinessNameEvent is a verified business name change for a contact.
type BusinessNameEvent struct {
	JID  string
	Name string
}

// LabelEditEvent is a label or list created, changed or removed in the app state.
type LabelEditEvent struct {
	ID           string
	Name         string
	Color        int32
	PredefinedID int32
	Deleted      bool
	Inactive     bool   // IsActive=false
	ListType     string // whatsmeow ListType name: NONE, CUSTOM, UNREAD, GROUPS, FAVORITES, ...
}

// LabelAssocEvent attaches (Labeled=true) or detaches a label on a chat.
type LabelAssocEvent struct {
	Chat    string
	Label   string
	Labeled bool
}

// GroupNameEvent is a group subject change.
type GroupNameEvent struct {
	JID  string
	Name string
}

// HistorySyncEvent is one history sync payload. Type is the sync type name,
// such as INITIAL_BOOTSTRAP, RECENT, PUSH_NAME or INITIAL_STATUS_V3.
type HistorySyncEvent struct {
	Type          string
	Mappings      []AliasPair // PN and LID pairs sent with the sync
	PushNames     []PushNameEvent
	Conversations []Conversation
}

// Conversation is one chat inside a history sync.
type Conversation struct {
	JID         string
	OldJID      string
	NewJID      string
	Name        string
	UnreadCount uint32
	Messages    []MessageEvent
}
