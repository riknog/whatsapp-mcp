package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/riknog/whatsapp-mcp/internal/config"
	"github.com/riknog/whatsapp-mcp/internal/media"
	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/wa"
)

// fakeReader stands in for the external transcriber and OCR.
type fakeReader struct {
	mu    sync.Mutex
	text  string
	err   error
	calls []string
}

func (r *fakeReader) Transcribe(_ context.Context, data []byte, mimetype string) (string, error) {
	return r.read("audio:" + mimetype + ":" + string(data))
}

func (r *fakeReader) OCR(_ context.Context, data []byte, mimetype string) (string, error) {
	return r.read("image:" + mimetype + ":" + string(data))
}

func (r *fakeReader) read(call string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
	return r.text, r.err
}

func (r *fakeReader) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

const (
	fxAudioID = "3EB0MEDIAAUDIO"
	fxImageID = "3EB0MEDIAIMAGE"
)

// newMediaFixture enables [media] with both commands and adds one audio and one
// image message, with download keys, to the chat of Mãe.
func newMediaFixture(t *testing.T, mutate func(*config.MediaConfig)) (*fixture, *fakeReader) {
	t.Helper()
	r := &fakeReader{text: "oi filho, me liga no 11 98765-4321, meu cpf é 123.456.789-09 e o email ana.souza@exemplo.com"}
	f := newFixtureWith(t, func(d *Deps) {
		d.Config.Media.Enabled = true
		d.Config.Media.AudioCommand = []string{"whisper", "{input}"}
		d.Config.Media.OCRCommand = []string{"tesseract", "{input}", "-"}
		if mutate != nil {
			mutate(&d.Config.Media)
		}
		d.Media = r
	})
	mae := f.direct[0].jid
	for _, m := range []store.Message{
		{ChatJID: mae, ID: fxAudioID, SenderJID: mae, TS: fixtureNow.Unix() - 5, Kind: "audio", Text: "[áudio 0:07]",
			Media: &store.Media{Kind: store.MediaAudio, Mimetype: "audio/ogg; codecs=opus", DirectPath: "/v/audio",
				MediaKey: []byte{1}, FileSHA256: []byte{2}, FileEncSHA256: []byte{3}, FileLength: 2048}},
		{ChatJID: mae, ID: fxImageID, SenderJID: mae, TS: fixtureNow.Unix() - 4, Kind: "image",
			Media: &store.Media{Kind: store.MediaImage, Mimetype: "image/png", DirectPath: "/v/image",
				MediaKey: []byte{1}, FileSHA256: []byte{2}, FileEncSHA256: []byte{3}, FileLength: 4096}},
	} {
		if _, err := f.st.InsertMessage(f.ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	f.wa.SetMedia("/v/audio", []byte("OGG"))
	f.wa.SetMedia("/v/image", []byte("PNG"))
	return f, r
}

func (f *fixture) readMedia(id string) (readMediaOut, *mcp.CallToolResult) {
	f.t.Helper()
	res := f.call("read_media", map[string]any{"contact": "Mãe", "message_id": id})
	var out readMediaOut
	decodeInto(f.t, res, &out)
	return out, res
}

func TestReadMediaTranscribesRedactsAndCaches(t *testing.T) {
	f, r := newMediaFixture(t, nil)
	out, res := f.readMedia(fxAudioID)
	mustOK(t, res)
	if out.Type != "audio" || out.Source != sourceTranscription || out.Cached || out.ContactRef != f.direct[0].ref {
		t.Fatalf("out = %+v", out)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	for _, leak := range []string{"98765", "123.456", "ana.souza"} {
		if strings.Contains(out.Text+string(raw), leak) {
			t.Errorf("output leaks %q: %s", leak, out.Text)
		}
	}
	if !strings.Contains(out.Text, "oi filho") || !strings.Contains(out.Text, "-09") {
		t.Errorf("text = %q", out.Text)
	}
	if r.count() != 1 || r.calls[0] != "audio:audio/ogg; codecs=opus:OGG" {
		t.Errorf("reader calls = %v", r.calls)
	}

	again, res := f.readMedia(fxAudioID)
	mustOK(t, res)
	if !again.Cached || again.Text != out.Text {
		t.Errorf("second read = %+v", again)
	}
	if r.count() != 1 || f.wa.Downloads() != 1 {
		t.Errorf("cache missed: reader=%d downloads=%d", r.count(), f.wa.Downloads())
	}
	// The raw transcript is kept locally; only the output is redacted.
	if _, md, err := f.st.GetMedia(f.ctx, f.direct[0].jid, fxAudioID); err != nil || !strings.Contains(md.Extracted, "98765") {
		t.Errorf("stored transcript = %+v, %v", md, err)
	}
}

func TestReadMediaOCR(t *testing.T) {
	f, r := newMediaFixture(t, nil)
	r.text = "   "
	out, res := f.readMedia(fxImageID)
	mustOK(t, res)
	if out.Source != sourceOCR || out.Type != "image" || len(out.Notes) != 1 {
		t.Fatalf("out = %+v", out)
	}
	if r.calls[0] != "image:image/png:PNG" {
		t.Errorf("reader calls = %v", r.calls)
	}
}

func TestReadMediaVisionAttachesImage(t *testing.T) {
	f, r := newMediaFixture(t, func(m *config.MediaConfig) { m.ImageMode = config.ImageModeVision })
	out, res := f.readMedia(fxImageID)
	mustOK(t, res)
	if out.Source != sourceVision || out.Text != "" || len(out.Notes) == 0 {
		t.Fatalf("out = %+v", out)
	}
	var img *mcp.ImageContent
	for _, c := range res.Content {
		if ic, ok := c.(*mcp.ImageContent); ok {
			img = ic
		}
	}
	if img == nil || string(img.Data) != "PNG" || img.MIMEType != "image/png" {
		t.Fatalf("image content = %+v", res.Content)
	}
	if r.count() != 0 {
		t.Errorf("OCR ran in vision mode")
	}
}

func TestReadMediaErrors(t *testing.T) {
	mae := func(f *fixture, kind string) string {
		for _, m := range f.msgs[f.direct[0].jid] {
			if m.Kind == kind {
				return m.ID
			}
		}
		t.Fatalf("no %s message for Mãe", kind)
		return ""
	}
	cases := []struct {
		name   string
		mutate func(*config.MediaConfig)
		setup  func(*fixture, *fakeReader)
		id     func(*fixture) string
		want   string
	}{
		{"disabled", func(m *config.MediaConfig) { m.Enabled = false }, nil, nil, "media_disabled"},
		{"no transcriber", func(m *config.MediaConfig) { m.AudioCommand = nil }, nil, nil, "media_disabled"},
		{"no ocr", func(m *config.MediaConfig) { m.OCRCommand = nil }, nil,
			func(*fixture) string { return fxImageID }, "media_disabled"},
		{"images off", func(m *config.MediaConfig) { m.ImageMode = config.ImageModeOff }, nil,
			func(*fixture) string { return fxImageID }, "media_disabled"},
		{"text message", nil, nil, func(f *fixture) string { return mae(f, "text") }, "invalid_argument"},
		{"unknown message", nil, nil, func(*fixture) string { return "3EB0NADA" }, "invalid_argument"},
		{"no keys", nil, nil, func(f *fixture) string { return mae(f, "image") }, "media_unavailable"},
		{"too large", func(m *config.MediaConfig) { m.MaxMB = 0 }, nil, nil, "media_unavailable"},
		{"gone", nil, func(f *fixture, _ *fakeReader) { f.wa.SetMediaError(wa.ErrMediaGone) }, nil, "media_unavailable"},
		{"refused", nil, func(f *fixture, _ *fakeReader) { f.wa.SetMediaError(errors.New("x")) }, nil, "media_unavailable"},
		{"transient", nil, func(f *fixture, _ *fakeReader) { f.wa.SetMediaError(wa.ErrNetwork) }, nil, "disconnected"},
		{"offline", nil, func(f *fixture, _ *fakeReader) { f.wa.SetConnected(false) }, nil, "disconnected"},
		{"logged out", nil, func(f *fixture, _ *fakeReader) { f.wa.SetLoggedIn(false) }, nil, "not_logged_in"},
		{"tool failed", nil, func(_ *fixture, r *fakeReader) { r.err = media.ErrTool }, nil, "media_tool_failed"},
		{"tool not configured", nil, func(_ *fixture, r *fakeReader) { r.err = media.ErrNotConfigured }, nil, "media_disabled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, r := newMediaFixture(t, c.mutate)
			if c.setup != nil {
				c.setup(f, r)
			}
			id := fxAudioID
			if c.id != nil {
				id = c.id(f)
			}
			res := f.call("read_media", map[string]any{"contact": "Mãe", "message_id": id})
			if got := errorCode(t, res); got != c.want {
				t.Errorf("code = %q, want %q (%s)", got, c.want, textOf(res))
			}
		})
	}
}

func TestReadMediaArguments(t *testing.T) {
	f, _ := newMediaFixture(t, nil)
	for _, args := range []map[string]any{
		{"message_id": fxAudioID},
		{"contact": "Mãe"},
		{"contact": "5511900000001", "message_id": fxAudioID},
		{"contact": "Banco Exemplo", "message_id": fxAudioID},
	} {
		if res := f.call("read_media", args); !res.IsError {
			t.Errorf("%v: accepted", args)
		}
	}
	// A named contact without a chat has no messages.
	res := f.call("read_media", map[string]any{"contact": fxNamedOnly[0], "message_id": fxAudioID})
	if got := errorCode(t, res); got != "invalid_argument" {
		t.Errorf("contact without chat = %q", got)
	}
}

func TestStatusShowsMedia(t *testing.T) {
	var out statusOut
	f, _ := newMediaFixture(t, nil)
	decodeInto(t, f.call("whatsapp_status", nil), &out)
	if !out.Media.Enabled || !out.Media.Audio || out.Media.ImageMode != sourceOCR {
		t.Errorf("media = %+v", out.Media)
	}
	plain := newFixture(t)
	decodeInto(t, plain.call("whatsapp_status", nil), &out)
	if out.Media.Enabled || out.Media.Audio || out.Media.ImageMode != config.ImageModeOff {
		t.Errorf("default media = %+v", out.Media)
	}
}

func TestImageMIME(t *testing.T) {
	for in, want := range map[string]string{"image/png": "image/png", "image/webp": "image/webp", "": "image/jpeg", "x/y": "image/jpeg"} {
		if got := imageMIME(in); got != want {
			t.Errorf("imageMIME(%q) = %q", in, got)
		}
	}
}
