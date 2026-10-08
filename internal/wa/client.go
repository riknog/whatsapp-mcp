package wa

import (
	"context"
	"errors"
)

// ErrNetwork marks a transient failure: the connection dropped or the server
// did not answer. The send queue retries these once. Any other error is final.
var ErrNetwork = errors.New("wa: network error")

// IsTransient reports whether err is worth one retry.
func IsTransient(err error) bool { return errors.Is(err, ErrNetwork) }

// Client is the only way the program reaches WhatsApp.
type Client interface {
	Connect(ctx context.Context) error
	Disconnect()
	IsConnected() bool
	IsLoggedIn() bool
	AccountName() string
	SendText(ctx context.Context, chat JID, text string, quotedID string) (msgID string, err error)
	SendContact(ctx context.Context, chat JID, displayName, vcard string, quotedID string) (msgID string, err error)
	AccountType() string                 // "business" | "personal"
	ContactPhone(jid JID) (string, bool) // only to build a vCard; never leaves the process by another path
	SendPresence(ctx context.Context, chat JID, typing bool) error
	MarkRead(ctx context.Context, chat JID, sender JID, ids []string) error
	Events() <-chan any // events already translated to internal/ingest types
}
