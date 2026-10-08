package ingest

import (
	"bytes"
	"math"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
)

func voiceNote(path string) *waE2E.Message {
	return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
		Seconds: u32(7), PTT: boolp(true), Mimetype: str("audio/ogg; codecs=opus"),
		DirectPath: str(path), MediaKey: []byte{1, 2}, FileSHA256: []byte{3}, FileEncSHA256: []byte{4},
		FileLength: u64(2048),
	}}
}

func photo(path string, viewOnce bool) *waE2E.Message {
	return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: str("olha"), Mimetype: str("image/jpeg"), ViewOnce: boolp(viewOnce),
		DirectPath: str(path), MediaKey: []byte{9}, FileSHA256: []byte{8}, FileEncSHA256: []byte{7},
		FileLength: u64(4096),
	}}
}

func TestMediaKeysAreStoredForAudioAndImage(t *testing.T) {
	h := newHarness(t)
	ana := mustJID(phoneAna)
	h.feed(liveMsg(ana, ana, "AU-1", false, at(1), voiceNote("/v/au")))
	h.feed(liveMsg(ana, ana, "IM-1", false, at(2), photo("/v/im", false)))

	_, md, err := h.st.GetMedia(h.ctx, phoneAna, "AU-1")
	if err != nil || md == nil {
		t.Fatalf("audio media = %+v, %v", md, err)
	}
	if md.Kind != "audio" || md.DirectPath != "/v/au" || !bytes.Equal(md.MediaKey, []byte{1, 2}) ||
		md.Mimetype != "audio/ogg; codecs=opus" || md.FileLength != 2048 {
		t.Errorf("audio media = %+v", md)
	}
	if _, md, _ := h.st.GetMedia(h.ctx, phoneAna, "IM-1"); md == nil || md.Kind != "image" || md.FileLength != 4096 {
		t.Errorf("image media = %+v", md)
	}
}

func TestMediaWithoutKeysOrViewOnceIsNotStored(t *testing.T) {
	h := newHarness(t)
	ana := mustJID(phoneAna)
	noKey := voiceNote("/v/x")
	noKey.AudioMessage.MediaKey = nil
	wrapped := &waE2E.Message{ViewOnceMessageV2: &waE2E.FutureProofMessage{Message: photo("/v/vo2", false)}}
	inEphemeral := &waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{
		Message: &waE2E.Message{ViewOnceMessage: &waE2E.FutureProofMessage{Message: voiceNote("/v/vo3")}}}}

	h.feed(liveMsg(ana, ana, "NK-1", false, at(1), noKey))
	h.feed(liveMsg(ana, ana, "VO-1", false, at(2), photo("/v/vo1", true)))
	h.feed(liveMsg(ana, ana, "VO-2", false, at(3), wrapped))
	h.feed(liveMsg(ana, ana, "VO-3", false, at(4), inEphemeral))
	// whatsmeow unwraps live view-once messages and keeps only the flag.
	unwrapped := liveMsg(ana, ana, "VO-4", false, at(5), photo("/v/vo4", false))
	unwrapped.IsViewOnce = true
	h.feed(unwrapped)

	for _, id := range []string{"NK-1", "VO-1", "VO-2", "VO-3", "VO-4"} {
		m, md, err := h.st.GetMedia(h.ctx, phoneAna, id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if md != nil {
			t.Errorf("%s: media stored: %+v", id, md)
		}
		if m.Kind != "audio" && m.Kind != "image" {
			t.Errorf("%s: kind = %q", id, m.Kind)
		}
	}
}

func TestHistoryMessageKeepsMediaKeys(t *testing.T) {
	h := newHarness(t)
	h.feed(bootstrap(&waHistorySync.Conversation{ID: str(phoneAna), Messages: []*waHistorySync.HistorySyncMsg{
		historyMsg(phoneAna, "HA-1", false, 10, voiceNote("/v/hist")),
	}}))
	if _, md, err := h.st.GetMedia(h.ctx, phoneAna, "HA-1"); err != nil || md == nil || md.DirectPath != "/v/hist" {
		t.Fatalf("history media = %+v, %v", md, err)
	}
}

func TestStoreMediaClampsLength(t *testing.T) {
	if storeMedia(nil) != nil {
		t.Error("nil ref gave a row")
	}
	if got := storeMedia(&MediaRef{FileLength: math.MaxUint64}); got.FileLength != math.MaxInt64 {
		t.Errorf("length = %d", got.FileLength)
	}
	if isViewOnce(nil) {
		t.Error("nil message is view-once")
	}
}
