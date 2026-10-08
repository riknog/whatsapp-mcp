package store

import (
	"context"
	"errors"
	"testing"
)

func TestUnreadCountsAndPositionOf(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const jid = "5511900000001@s.whatsapp.net"
	const hid = "5511900000002@s.whatsapp.net"
	for _, c := range []Chat{
		{JID: jid, Ref: "c_aaaaaaaaaa", Kind: "direct"},
		{JID: hid, Ref: "c_bbbbbbbbbb", Kind: "direct", Hidden: true},
	} {
		if err := s.UpsertChat(ctx, c); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}
	if err := s.SetHidden(ctx, hid, true); err != nil {
		t.Fatalf("SetHidden: %v", err)
	}
	msgs := []Message{
		{ChatJID: jid, ID: "M1", TS: 100, Kind: "text", Text: "um"},
		{ChatJID: jid, ID: "M2", TS: 200, Kind: "text", Text: "dois"},
		{ChatJID: jid, ID: "M3", TS: 300, Kind: "text", Text: "tres", FromMe: true},
		{ChatJID: hid, ID: "H1", TS: 400, Kind: "text", Text: "oculto"},
	}
	if _, err := s.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	counts, err := s.UnreadCounts(ctx)
	if err != nil {
		t.Fatalf("UnreadCounts: %v", err)
	}
	if counts[jid] != 2 {
		t.Fatalf("unread(jid) = %d, want 2 (inbound only)", counts[jid])
	}
	if _, ok := counts[hid]; ok {
		t.Fatalf("hidden chat counted: %v", counts)
	}

	if err := s.SetOwnerReadAt(ctx, jid, 150); err != nil {
		t.Fatalf("SetOwnerReadAt: %v", err)
	}
	counts, err = s.UnreadCounts(ctx)
	if err != nil {
		t.Fatalf("UnreadCounts: %v", err)
	}
	if counts[jid] != 1 {
		t.Fatalf("after read point 150: unread = %d, want 1", counts[jid])
	}

	// Newest is M3 (ts 300): nothing is newer. Oldest is M1: two are newer.
	for id, want := range map[string]int64{"M3": 0, "M2": 1, "M1": 2} {
		got, err := s.PositionOf(ctx, jid, id)
		if err != nil {
			t.Fatalf("PositionOf(%s): %v", id, err)
		}
		if got != want {
			t.Errorf("PositionOf(%s) = %d, want %d", id, got, want)
		}
	}
	if _, err := s.PositionOf(ctx, jid, "NADA"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown message: err = %v, want ErrNotFound", err)
	}
	if _, err := s.PositionOf(ctx, hid, "H1"); !errors.Is(err, ErrHidden) {
		t.Errorf("hidden chat: err = %v, want ErrHidden", err)
	}
	if _, err := s.PositionOf(ctx, "x@s.whatsapp.net", "M1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown chat: err = %v, want ErrNotFound", err)
	}
}
