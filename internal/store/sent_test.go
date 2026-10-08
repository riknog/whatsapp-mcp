package store

import (
	"context"
	"errors"
	"testing"
)

func TestRecordSentCreatesChatAndIsIdempotent(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const jid = "5511900000001@s.whatsapp.net"
	m := Message{ChatJID: jid, ID: "S1", TS: 500, Kind: "text", Text: "oi"}
	for i := 0; i < 2; i++ {
		if err := s.RecordSent(ctx, "c_aaaaaaaaaa", m); err != nil {
			t.Fatalf("RecordSent #%d: %v", i, err)
		}
	}
	c, err := s.GetChat(ctx, jid)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Ref != "c_aaaaaaaaaa" || c.Kind != "direct" || c.LastMessageAt != 500 || c.OwnerReadAt != 500 {
		t.Fatalf("chat = %+v", c)
	}
	if n, _ := s.CountMessages(ctx, jid); n != 1 {
		t.Fatalf("messages = %d, want 1", n)
	}
	got, err := s.RecentMessages(ctx, jid, 10, 0)
	if err != nil || len(got) != 1 || !got[0].FromMe || got[0].SenderJID != "" {
		t.Fatalf("RecentMessages = %+v, %v", got, err)
	}
	// An echo through ingestion is ignored.
	if ok, err := s.InsertMessage(ctx, Message{ChatJID: jid, ID: "S1", TS: 501, Kind: "text", Text: "oi", FromMe: true}); ok || err != nil {
		t.Fatalf("echo inserted: %v %v", ok, err)
	}
	// Without a ref and without a chat, nothing is created.
	if err := s.RecordSent(ctx, "", Message{ChatJID: "5511900000009@s.whatsapp.net", ID: "S2", TS: 1, Kind: "text"}); err != nil {
		t.Fatalf("RecordSent without ref: %v", err)
	}
	if _, err := s.GetChat(ctx, "5511900000009@s.whatsapp.net"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("chat created without ref: %v", err)
	}
	if err := s.RecordSent(ctx, "c_gggggggggg", Message{ChatJID: "123-456@g.us", ID: "G1", TS: 1, Kind: "text"}); err != nil {
		t.Fatalf("RecordSent group: %v", err)
	}
	if g, err := s.GetChat(ctx, "123-456@g.us"); err != nil || g.Kind != "group" {
		t.Fatalf("group chat = %+v, %v", g, err)
	}
}

func TestUnreadInboundLastInboundHasMessage(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const jid = "5511900000001@s.whatsapp.net"
	const hid = "5511900000002@s.whatsapp.net"
	for _, c := range []Chat{
		{JID: jid, Ref: "c_aaaaaaaaaa", Kind: "direct"},
		{JID: hid, Ref: "c_bbbbbbbbbb", Kind: "direct"},
	} {
		if err := s.UpsertChat(ctx, c); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}
	if err := s.SetHidden(ctx, hid, true); err != nil {
		t.Fatalf("SetHidden: %v", err)
	}
	if ts, err := s.LastInboundAt(ctx, jid); err != nil || ts != 0 {
		t.Fatalf("LastInboundAt(empty) = %d, %v", ts, err)
	}
	msgs := []Message{
		{ChatJID: jid, ID: "M1", SenderJID: jid, TS: 100, Kind: "text", Text: "um"},
		{ChatJID: jid, ID: "M2", SenderJID: jid, TS: 200, Kind: "text", Text: "dois"},
		{ChatJID: jid, ID: "M3", TS: 300, Kind: "text", Text: "tres", FromMe: true},
		{ChatJID: jid, ID: "M4", SenderJID: jid, TS: 250, Kind: "text", Text: "quatro"},
	}
	if _, err := s.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
	if err := s.SetOwnerReadAt(ctx, jid, 150); err != nil {
		t.Fatalf("SetOwnerReadAt: %v", err)
	}
	un, err := s.UnreadInbound(ctx, jid)
	if err != nil {
		t.Fatalf("UnreadInbound: %v", err)
	}
	if len(un) != 2 || un[0].ID != "M2" || un[1].ID != "M4" {
		t.Fatalf("UnreadInbound = %+v, want M2, M4", un)
	}
	if _, err := s.UnreadInbound(ctx, hid); !errors.Is(err, ErrHidden) {
		t.Fatalf("hidden chat: err = %v", err)
	}
	if _, err := s.UnreadInbound(ctx, "x@s.whatsapp.net"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown chat: err = %v", err)
	}
	if ts, err := s.LastInboundAt(ctx, jid); err != nil || ts != 250 {
		t.Fatalf("LastInboundAt = %d, %v; want 250 (from_me ignored)", ts, err)
	}
	for id, want := range map[string]bool{"M3": true, "nope": false} {
		if ok, err := s.HasMessage(ctx, jid, id); err != nil || ok != want {
			t.Errorf("HasMessage(%s) = %v, %v", id, ok, err)
		}
	}
	if ok, _ := s.HasMessage(ctx, hid, "M1"); ok {
		t.Error("HasMessage crosses chats")
	}
}

func TestTotals(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()
	if got, err := st.Totals(ctx); err != nil || got != (Totals{}) {
		t.Fatalf("empty Totals = %+v, %v", got, err)
	}
	for _, c := range []Chat{{JID: "a@s.whatsapp.net", Ref: "c_a", Kind: "direct"}, {JID: "b@s.whatsapp.net", Ref: "c_b", Kind: "direct"}} {
		if err := st.UpsertChat(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetHidden(ctx, "b@s.whatsapp.net", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertMessage(ctx, Message{ChatJID: "a@s.whatsapp.net", ID: "M1", TS: 1, Kind: KindText, Text: "oi"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Totals(ctx)
	if err != nil || got != (Totals{Chats: 2, Hidden: 1, Messages: 1}) {
		t.Fatalf("Totals = %+v, %v", got, err)
	}
}
