package wa

import (
	"context"
	"errors"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/riknog/whatsapp-mcp/internal/privacy"
)

// SendText sends a text message, quoting quotedID when it is set.
func (r *Real) SendText(ctx context.Context, chat JID, text, quotedID string) (string, error) {
	to, err := parseJID(chat)
	if err != nil {
		return "", err
	}
	msg := &waE2E.Message{Conversation: strPtr(text)}
	if quotedID != "" {
		msg = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        strPtr(text),
			ContextInfo: &waE2E.ContextInfo{StanzaID: strPtr(quotedID)},
		}}
	}
	return r.send(ctx, to, msg)
}

// SendContact sends a contact card. The vCard is built by the caller and carries the number.
func (r *Real) SendContact(ctx context.Context, chat JID, displayName, vcard, quotedID string) (string, error) {
	to, err := parseJID(chat)
	if err != nil {
		return "", err
	}
	msg := &waE2E.Message{ContactMessage: &waE2E.ContactMessage{
		DisplayName: strPtr(displayName),
		Vcard:       strPtr(vcard),
	}}
	if quotedID != "" {
		msg.ContactMessage.ContextInfo = &waE2E.ContextInfo{StanzaID: strPtr(quotedID)}
	}
	return r.send(ctx, to, msg)
}

func (r *Real) send(ctx context.Context, to types.JID, msg *waE2E.Message) (string, error) {
	resp, err := r.cl.SendMessage(ctx, to, msg)
	if err != nil {
		return "", sendError("envio", err)
	}
	return string(resp.ID), nil
}

// SendPresence shows "typing" or "paused" in a chat.
func (r *Real) SendPresence(ctx context.Context, chat JID, typing bool) error {
	to, err := parseJID(chat)
	if err != nil {
		return err
	}
	state := types.ChatPresencePaused
	if typing {
		state = types.ChatPresenceComposing
	}
	if err := r.cl.SendChatPresence(ctx, to, state, types.ChatPresenceMediaText); err != nil {
		return sendError("presença", err)
	}
	return nil
}

// MarkRead sends read receipts for ids in chat. An empty sender means a direct
// chat, where the sender is the chat itself.
func (r *Real) MarkRead(ctx context.Context, chat, sender JID, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	to, err := parseJID(chat)
	if err != nil {
		return err
	}
	from := to
	if !sender.IsZero() {
		if from, err = parseJID(sender); err != nil {
			return err
		}
	}
	mids := make([]types.MessageID, 0, len(ids))
	for _, id := range ids {
		mids = append(mids, types.MessageID(id))
	}
	if err := r.cl.MarkRead(ctx, mids, r.clk.Now(), to, from); err != nil {
		return sendError("leitura", err)
	}
	return nil
}

// ErrSendUncertain means WhatsApp did not answer in time. The message may have
// been delivered, so the caller must not send it again automatically.
var ErrSendUncertain = errors.New("wa: envio incerto: o WhatsApp não respondeu a tempo")

// sendError keeps the error text out of the output: whatsmeow messages can carry
// JIDs. Only "not connected" is transient: nothing was sent, so one retry is safe.
// A timeout is ErrSendUncertain and is not retried.
func sendError(op string, err error) error {
	if errors.Is(err, whatsmeow.ErrNotConnected) {
		return fmt.Errorf("%w: sem conexão no momento do %s", ErrNetwork, op)
	}
	if errors.Is(err, whatsmeow.ErrIQTimedOut) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w (%s)", ErrSendUncertain, op)
	}
	return fmt.Errorf("wa: %s recusado: %s", op, privacy.RedactLog(err.Error()))
}

// parseJID converts the package JID to a whatsmeow JID.
func parseJID(j JID) (types.JID, error) {
	// The wa form must be "user@server" first: whatsmeow would read a bare
	// string as a phone number.
	if _, err := ParseJID(string(j)); err != nil {
		return types.EmptyJID, ErrInvalidJID
	}
	parsed, err := types.ParseJID(string(j))
	if err != nil {
		return types.EmptyJID, ErrInvalidJID
	}
	return parsed, nil
}

func strPtr(s string) *string { return &s }
