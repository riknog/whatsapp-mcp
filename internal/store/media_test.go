package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func audioMessage(chat, id string, ts int64) Message {
	return Message{
		ChatJID: chat, ID: id, SenderJID: chat, TS: ts, Kind: "audio", Text: "[áudio 0:07]",
		Media: &Media{
			Kind: MediaAudio, Mimetype: "audio/ogg; codecs=opus", DirectPath: "/v/t62/" + id,
			MediaKey: []byte{1, 2, 3}, FileSHA256: []byte{4}, FileEncSHA256: []byte{5}, FileLength: 4096,
		},
	}
}

func TestMediaStoredWithMessageAndRead(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const jid = "5511900000001@s.whatsapp.net"
	mustChat(t, s, jid, "direct")

	if ok, err := s.InsertMessage(ctx, audioMessage(jid, "A1", 100)); err != nil || !ok {
		t.Fatalf("InsertMessage = %v, %v", ok, err)
	}
	// A text message has no media row.
	if _, err := s.InsertMessage(ctx, Message{ChatJID: jid, ID: "T1", TS: 101, Kind: "text", Text: "oi"}); err != nil {
		t.Fatal(err)
	}

	m, md, err := s.GetMedia(ctx, jid, "A1")
	if err != nil || md == nil {
		t.Fatalf("GetMedia = %+v, %+v, %v", m, md, err)
	}
	if m.Kind != "audio" || md.Kind != MediaAudio || md.DirectPath != "/v/t62/A1" ||
		!bytes.Equal(md.MediaKey, []byte{1, 2, 3}) || md.FileLength != 4096 || md.Extracted != "" {
		t.Fatalf("media = %+v", md)
	}
	if _, md, err := s.GetMedia(ctx, jid, "T1"); err != nil || md != nil {
		t.Fatalf("text message media = %+v, %v", md, err)
	}
	if _, _, err := s.GetMedia(ctx, jid, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown message: %v", err)
	}
	if _, _, err := s.GetMedia(ctx, "5511900000099@s.whatsapp.net", "A1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown chat: %v", err)
	}

	if err := s.SetMediaExtracted(ctx, jid, "A1", "bom dia", ExtractedByTranscription); err != nil {
		t.Fatal(err)
	}
	if _, md, _ := s.GetMedia(ctx, jid, "A1"); md.Extracted != "bom dia" || md.ExtractedBy != ExtractedByTranscription || md.ExtractedAt == 0 {
		t.Fatalf("extracted = %+v", md)
	}
	if err := s.SetMediaExtracted(ctx, jid, "T1", "x", ExtractedByOCR); !errors.Is(err, ErrNotFound) {
		t.Fatalf("text message extracted: %v", err)
	}
}

func TestMediaAddedWhenMessageRepeatsWithKeys(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const jid = "5511900000002@s.whatsapp.net"
	mustChat(t, s, jid, "direct")
	bare := audioMessage(jid, "A2", 100)
	bare.Media = nil
	if _, err := s.InsertMessage(ctx, bare); err != nil {
		t.Fatal(err)
	}
	// The same message again, now with keys (a history sync after a live event).
	if ok, err := s.InsertMessages(ctx, []Message{audioMessage(jid, "A2", 100)}); err != nil || ok != 0 {
		t.Fatalf("InsertMessages = %d, %v", ok, err)
	}
	if _, md, err := s.GetMedia(ctx, jid, "A2"); err != nil || md == nil {
		t.Fatalf("media after repeat = %+v, %v", md, err)
	}
	// Without a path or key there is nothing to store.
	noKey := audioMessage(jid, "A3", 101)
	noKey.Media.MediaKey = nil
	if _, err := s.InsertMessage(ctx, noKey); err != nil {
		t.Fatal(err)
	}
	if _, md, _ := s.GetMedia(ctx, jid, "A3"); md != nil {
		t.Fatalf("media without key stored: %+v", md)
	}
}

func TestMediaGoesWithItsMessage(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const jid = "5511900000003@s.whatsapp.net"
	mustChat(t, s, jid, "direct")
	if _, err := s.InsertMessages(ctx, []Message{audioMessage(jid, "OLD", 10), audioMessage(jid, "NEW", 1000)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeOlderThan(ctx, 500); err != nil {
		t.Fatal(err)
	}
	if n := countMedia(t, s); n != 1 {
		t.Fatalf("media rows after retention = %d, want 1", n)
	}
	if _, err := s.PurgeChat(ctx, jid); err != nil {
		t.Fatal(err)
	}
	if n := countMedia(t, s); n != 0 {
		t.Fatalf("media rows after purge = %d, want 0", n)
	}
}

func TestMediaOfHiddenChatIsRefused(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const jid = "5511900000004@s.whatsapp.net"
	mustChat(t, s, jid, "direct")
	if _, err := s.InsertMessage(ctx, audioMessage(jid, "H1", 100)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHidden(ctx, jid, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetMedia(ctx, jid, "H1"); !errors.Is(err, ErrHidden) {
		t.Fatalf("hidden chat: %v", err)
	}
}

func TestArrivalsSince(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const a, b, g, h = "5511900000010@s.whatsapp.net", "5511900000011@s.whatsapp.net",
		"120363000000000009@g.us", "5511900000012@s.whatsapp.net"
	for _, j := range []string{a, b, h} {
		mustChat(t, s, j, "direct")
	}
	if err := s.UpsertChat(ctx, Chat{JID: g, Ref: "c_g", Kind: "group"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHidden(ctx, h, true); err != nil {
		t.Fatal(err)
	}
	if pk, err := s.MaxMessagePK(ctx); err != nil || pk != 0 {
		t.Fatalf("MaxMessagePK on empty = %d, %v", pk, err)
	}
	if _, err := s.InsertMessage(ctx, Message{ChatJID: a, ID: "old", SenderJID: a, TS: 50, Kind: "text"}); err != nil {
		t.Fatal(err)
	}
	start, err := s.MaxMessagePK(ctx)
	if err != nil || start == 0 {
		t.Fatalf("MaxMessagePK = %d, %v", start, err)
	}
	for _, m := range []Message{
		{ChatJID: a, ID: "a1", SenderJID: a, TS: 100, Kind: "text"},
		{ChatJID: a, ID: "a2", SenderJID: a, TS: 101, Kind: "text"},
		{ChatJID: a, ID: "me", TS: 102, Kind: "text", FromMe: true},
		{ChatJID: b, ID: "b1", SenderJID: b, TS: 200, Kind: "text"},
		{ChatJID: g, ID: "g1", SenderJID: a, TS: 300, Kind: "text"},
		{ChatJID: h, ID: "h1", SenderJID: h, TS: 400, Kind: "text"},
	} {
		if _, err := s.InsertMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ArrivalsSince(ctx, start, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Chat.JID != b || got[0].Count != 1 || got[1].Chat.JID != a || got[1].Count != 2 {
		t.Fatalf("arrivals = %+v", got)
	}
	withGroups, _ := s.ArrivalsSince(ctx, start, true)
	if len(withGroups) != 3 || withGroups[0].Chat.JID != g {
		t.Fatalf("arrivals with groups = %+v", withGroups)
	}
	// Nothing after the newest point; a chat read on the phone no longer counts.
	if rest, _ := s.ArrivalsSince(ctx, got[0].LatestPK+100, false); len(rest) != 0 {
		t.Fatalf("arrivals after the end = %+v", rest)
	}
	if err := s.SetOwnerReadAt(ctx, b, 200); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.ArrivalsSince(ctx, start, false); len(again) != 1 || again[0].Chat.JID != a {
		t.Fatalf("arrivals after owner read = %+v", again)
	}
}

func countMedia(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.r.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM media`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
