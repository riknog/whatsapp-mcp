package wa

import (
	"errors"
	"strings"
)

// JID is the package's own identifier for a WhatsApp chat or account. It is a
// canonical "user@server" string with the device suffix (":N") removed and the
// server in lower case. It is opaque to the rest of the program: only this
// package and the whatsmeow adapter convert to and from it. Never print a JID
// in tool output, errors or logs.
type JID string

// Server names used by WhatsApp.
const (
	ServerUser       = "s.whatsapp.net"
	ServerGroup      = "g.us"
	ServerLID        = "lid"
	ServerBroadcast  = "broadcast"
	ServerNewsletter = "newsletter"
)

// ErrInvalidJID is returned by ParseJID for input that is not "user@server".
var ErrInvalidJID = errors.New("wa: invalid jid")

// ParseJID canonicalizes s. It removes a device suffix ("user:12@server") and
// lower-cases the server. It fails when s has no user part or no server part.
func ParseJID(s string) (JID, error) {
	user, server, ok := strings.Cut(s, "@")
	if !ok || user == "" || server == "" {
		return "", ErrInvalidJID
	}
	if i := strings.IndexByte(user, ':'); i >= 0 {
		user = user[:i]
	}
	if user == "" {
		return "", ErrInvalidJID
	}
	return JID(user + "@" + strings.ToLower(server)), nil
}

// String returns the canonical form. Do not use it in output.
func (j JID) String() string { return string(j) }

// IsZero reports whether j is empty.
func (j JID) IsZero() bool { return j == "" }

// User returns the part before "@".
func (j JID) User() string {
	user, _, _ := strings.Cut(string(j), "@")
	return user
}

// Server returns the part after "@".
func (j JID) Server() string {
	_, server, _ := strings.Cut(string(j), "@")
	return server
}

// IsGroup reports whether j names a group chat.
func (j JID) IsGroup() bool { return j.Server() == ServerGroup }

// IsBroadcast reports whether j is a status or broadcast list (never a chat).
func (j JID) IsBroadcast() bool { return j.Server() == ServerBroadcast }

// IsNewsletter reports whether j is a newsletter channel.
func (j JID) IsNewsletter() bool { return j.Server() == ServerNewsletter }

// IsLID reports whether j is a WhatsApp privacy identifier (LID), not a phone number.
func (j JID) IsLID() bool { return j.Server() == ServerLID }

// IsDirect reports whether j is a one-to-one chat (phone-number or LID account).
func (j JID) IsDirect() bool { return j.Server() == ServerUser || j.IsLID() }
