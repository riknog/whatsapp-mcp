package ingest

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/identity"
	"github.com/riknog/whatsapp-mcp/internal/store"
)

// The real contact_ref generator must satisfy Refs; this fails if identity changes its method.
var _ Refs = (*identity.Refs)(nil)

var testStart = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

const (
	phoneAna  = "5511999998888@s.whatsapp.net"
	phoneBia  = "5521988887777@s.whatsapp.net"
	groupJID  = "120363000000000001@g.us"
	lidAna    = "100200300@lid"
	lidGroup  = "555@lid"
	statusJID = "status@broadcast"
)

// fakeRefs gives each chat a readable ref. The real ref is an HMAC; ingest does not depend on its form.
type fakeRefs struct{}

func (fakeRefs) Ref(jid string) string { return "c_" + jid }

type harness struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	clk *clock.Fake
	in  *Ingestor
	log *bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewFake(testStart)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "data.db"), clk)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))
	return &harness{t: t, ctx: ctx, st: st, clk: clk, in: New(st, fakeRefs{}, clk, log), log: &logBuf}
}

// feed translates one whatsmeow event and applies it. It fails the test when the event is not translated.
func (h *harness) feed(evt any) {
	h.t.Helper()
	ev, ok := Translate(evt)
	if !ok {
		h.t.Fatalf("Translate(%T) did not translate", evt)
	}
	if err := h.in.Handle(h.ctx, ev); err != nil {
		h.t.Fatalf("Handle(%T): %v", ev, err)
	}
}

// feedAll applies several events in order.
func (h *harness) feedAll(evts ...any) {
	h.t.Helper()
	for _, e := range evts {
		h.feed(e)
	}
}

// notTranslated checks that an event is ignored by Translate.
func (h *harness) notTranslated(evt any) {
	h.t.Helper()
	if ev, ok := Translate(evt); ok {
		h.t.Fatalf("Translate(%T) = %#v, want ignored", evt, ev)
	}
}

func (h *harness) messages(chat string) []store.Message {
	h.t.Helper()
	msgs, err := h.st.RecentMessages(h.ctx, chat, 200, 0)
	if err != nil {
		h.t.Fatalf("RecentMessages(%s): %v", chat, err)
	}
	return msgs
}

func (h *harness) chat(jid string) store.Chat {
	h.t.Helper()
	c, err := h.st.GetChat(h.ctx, jid)
	if err != nil {
		h.t.Fatalf("GetChat(%s): %v", jid, err)
	}
	return c
}

func (h *harness) chatExists(jid string) bool {
	_, err := h.st.GetChat(h.ctx, jid)
	return err == nil
}

func mustJID(s string) types.JID {
	j, err := types.ParseJID(s)
	if err != nil {
		panic(err)
	}
	return j
}

func at(sec int64) time.Time { return testStart.Add(time.Duration(sec) * time.Second) }

func str(s string) *string { return &s }
func u32(v uint32) *uint32 { return &v }
func i32(v int32) *int32   { return &v }
func u64(v uint64) *uint64 { return &v }
func boolp(v bool) *bool   { return &v }

// liveMsg builds a whatsmeow message event. Sender and chat are full JIDs.
func liveMsg(chat, sender types.JID, id string, fromMe bool, ts time.Time, m *waE2E.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: fromMe, IsGroup: chat.Server == types.GroupServer,
			},
			ID:        types.MessageID(id),
			Timestamp: ts,
		},
		Message: m,
	}
}

func text(s string) *waE2E.Message { return &waE2E.Message{Conversation: str(s)} }

// historyMsg is one message inside a history sync conversation.
func historyMsg(chat, id string, fromMe bool, sec int64, m *waE2E.Message) *waHistorySync.HistorySyncMsg {
	return &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
		Key:              &waCommon.MessageKey{RemoteJID: str(chat), FromMe: boolp(fromMe), ID: str(id)},
		MessageTimestamp: u64(uint64(testStart.Add(time.Duration(sec) * time.Second).Unix())),
		Message:          m,
	}}
}

// historyEvent builds a history sync event with one conversation.
func historyEvent(syncType waHistorySync.HistorySync_HistorySyncType, conv *waHistorySync.Conversation) *events.HistorySync {
	return &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType:      syncType.Enum(),
		Conversations: []*waHistorySync.Conversation{conv},
	}}
}

func labelEdit(id, name string, listType waSyncAction.LabelEditAction_ListType) *events.LabelEdit {
	return &events.LabelEdit{
		LabelID: id,
		Action:  &waSyncAction.LabelEditAction{Name: str(name), Type: listType.Enum(), Color: i32(3)},
	}
}

func labelAssoc(chat types.JID, labelID string, labeled bool) *events.LabelAssociationChat {
	return &events.LabelAssociationChat{JID: chat, LabelID: labelID, Action: &waSyncAction.LabelAssociationAction{Labeled: boolp(labeled)}}
}
