package wa

import (
	"context"
	"sync"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
)

// Fake is an in-memory Client for tests. It is safe for concurrent use. Times
// come from the injected clock. Errors can be scripted per call.
type Fake struct {
	clk clock.Clock

	mu          sync.Mutex
	connected   bool
	loggedIn    bool
	accountName string
	accountType string
	phones      map[JID]string
	textScript  []error
	contactScr  []error
	presenceErr error
	nextID      int
	attempts    int
	sent        []SentText
	contacts    []SentContact
	presence    []PresenceEvent
	reads       []ReadEvent
	events      chan any
}

// SentText is one successful SendText call.
type SentText struct {
	At       time.Time
	Chat     JID
	Text     string
	QuotedID string
	ID       string
}

// SentContact is one successful SendContact call.
type SentContact struct {
	At          time.Time
	Chat        JID
	DisplayName string
	VCard       string
	QuotedID    string
	ID          string
}

// PresenceEvent is one SendPresence call (successful or not).
type PresenceEvent struct {
	At     time.Time
	Chat   JID
	Typing bool
}

// ReadEvent is one MarkRead call.
type ReadEvent struct {
	At     time.Time
	Chat   JID
	Sender JID
	IDs    []string
}

// NewFake returns a connected, logged-in fake account of type "personal".
func NewFake(clk clock.Clock) *Fake {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Fake{
		clk:         clk,
		connected:   true,
		loggedIn:    true,
		accountName: "Conta de teste",
		accountType: "personal",
		phones:      map[JID]string{},
		events:      make(chan any, 256),
	}
}

// Connect marks the fake connected.
func (f *Fake) Connect(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = true
	return nil
}

// Disconnect marks the fake disconnected.
func (f *Fake) Disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = false
}

// IsConnected reports the connection flag.
func (f *Fake) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

// IsLoggedIn reports the login flag.
func (f *Fake) IsLoggedIn() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loggedIn
}

// AccountName returns the fake account name.
func (f *Fake) AccountName() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accountName
}

// AccountType returns the fake account type.
func (f *Fake) AccountType() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accountType
}

// SetConnected sets the connection flag. A send while disconnected fails with ErrNetwork.
func (f *Fake) SetConnected(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = v
}

// SetLoggedIn sets the login flag.
func (f *Fake) SetLoggedIn(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loggedIn = v
}

// SetAccount sets the account name and type ("business" | "personal").
func (f *Fake) SetAccount(name, accountType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accountName = name
	f.accountType = accountType
}

// SetPhone registers the phone number that ContactPhone returns for jid.
func (f *Fake) SetPhone(jid JID, phone string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.phones[jid] = phone
}

// ContactPhone returns the registered phone number for jid.
func (f *Fake) ContactPhone(jid JID) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.phones[jid]
	return p, ok
}

// FailSendText scripts the results of the next SendText calls, one per call.
// A nil entry succeeds. When the script runs out, calls succeed.
func (f *Fake) FailSendText(results ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.textScript = append(f.textScript, results...)
}

// FailSendContact scripts the results of the next SendContact calls, one per call.
func (f *Fake) FailSendContact(results ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contactScr = append(f.contactScr, results...)
}

// SetPresenceError makes every SendPresence call fail with err. Nil clears it.
func (f *Fake) SetPresenceError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presenceErr = err
}

// SendText records a successful text message and returns its id.
func (f *Fake) SendText(_ context.Context, chat JID, text, quotedID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if err := f.scriptedErr(&f.textScript); err != nil {
		return "", err
	}
	f.nextID++
	id := fakeID(f.nextID)
	f.sent = append(f.sent, SentText{At: f.clk.Now(), Chat: chat, Text: text, QuotedID: quotedID, ID: id})
	return id, nil
}

// SendContact records a successful contact card and returns its id.
func (f *Fake) SendContact(_ context.Context, chat JID, displayName, vcard, quotedID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if err := f.scriptedErr(&f.contactScr); err != nil {
		return "", err
	}
	f.nextID++
	id := fakeID(f.nextID)
	f.contacts = append(f.contacts, SentContact{At: f.clk.Now(), Chat: chat, DisplayName: displayName,
		VCard: vcard, QuotedID: quotedID, ID: id})
	return id, nil
}

// scriptedErr pops the next scripted result. It must be called with f.mu held.
// A disconnected fake fails with ErrNetwork before the script is consumed.
func (f *Fake) scriptedErr(script *[]error) error {
	if !f.connected {
		return ErrNetwork
	}
	if len(*script) == 0 {
		return nil
	}
	err := (*script)[0]
	*script = (*script)[1:]
	return err
}

// SendPresence records a presence update unless SetPresenceError is set.
func (f *Fake) SendPresence(_ context.Context, chat JID, typing bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.presenceErr != nil {
		return f.presenceErr
	}
	f.presence = append(f.presence, PresenceEvent{At: f.clk.Now(), Chat: chat, Typing: typing})
	return nil
}

// MarkRead records a read receipt.
func (f *Fake) MarkRead(_ context.Context, chat, sender JID, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, ReadEvent{At: f.clk.Now(), Chat: chat, Sender: sender, IDs: append([]string(nil), ids...)})
	return nil
}

// Events returns the channel that Inject feeds.
func (f *Fake) Events() <-chan any { return f.events }

// Inject queues an event for Events. It returns false when the buffer is full.
func (f *Fake) Inject(ev any) bool {
	select {
	case f.events <- ev:
		return true
	default:
		return false
	}
}

// Sent returns a copy of the successful text sends, in order.
func (f *Fake) Sent() []SentText {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SentText(nil), f.sent...)
}

// Contacts returns a copy of the successful contact sends, in order.
func (f *Fake) Contacts() []SentContact {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SentContact(nil), f.contacts...)
}

// Presence returns a copy of the presence calls, in order.
func (f *Fake) Presence() []PresenceEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PresenceEvent(nil), f.presence...)
}

// Reads returns a copy of the MarkRead calls, in order.
func (f *Fake) Reads() []ReadEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ReadEvent(nil), f.reads...)
}

// Attempts counts every SendText and SendContact call, successful or not.
func (f *Fake) Attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func fakeID(n int) string {
	// Letters only after the prefix: a run of 8 digits in an id would look like
	// a phone number to privacy.AssertNoPII in the tool tests.
	const digits = "ABCDEFGHIJKLMNOP"
	b := []byte("3EB0AAAAAAAAAAAA")
	for i := len(b) - 1; i >= 4 && n > 0; i-- {
		b[i] = digits[n%16]
		n /= 16
	}
	return string(b)
}

var _ Client = (*Fake)(nil)
