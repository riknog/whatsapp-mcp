package mcpserver

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/riknog/whatsapp-mcp/internal/store"
)

func TestClipText(t *testing.T) {
	if got := clipText("abc", 5); got != "abc" {
		t.Errorf("short text changed: %q", got)
	}
	if got := clipText("abcdef", 4); got != "abcd…[+2 chars]" {
		t.Errorf("clipText = %q", got)
	}
}

func TestPreviewCollapsesAndCuts(t *testing.T) {
	if got := preview("a\n  b\tc"); got != "a b c" {
		t.Errorf("preview = %q", got)
	}
	long := strings.Repeat("x", 120)
	got := preview(long)
	if utf8.RuneCountInString(got) != maxPreview || !strings.HasSuffix(got, "…") {
		t.Errorf("preview of 120 chars = %d runes, %q", utf8.RuneCountInString(got), got)
	}
}

func TestCategoriesOf(t *testing.T) {
	cases := []struct {
		kind   string
		labels []string
		want   string
	}{
		{"direct", nil, "Sem categoria"},
		{"direct", []string{"Família"}, "Família"},
		{"group", nil, "Grupos"},
		{"group", []string{"Família"}, "Família,Grupos"},
	}
	for _, c := range cases {
		if got := strings.Join(categoriesOf(c.kind, c.labels), ","); got != c.want {
			t.Errorf("categoriesOf(%s, %v) = %q, want %q", c.kind, c.labels, got, c.want)
		}
	}
}

func TestHasAndFindCategory(t *testing.T) {
	if !hasCategory([]string{"Família"}, "  FAMILIA ") {
		t.Errorf("hasCategory ignores case and accents: false")
	}
	cats := []category{{name: "Clientes", source: "local"}, {name: "Grupos", source: "implicit"}}
	if c, ok := findCategory(cats, "grupos"); !ok || c.source != "implicit" {
		t.Errorf("findCategory = %+v, %v", c, ok)
	}
	if _, ok := findCategory(cats, "Nada"); ok {
		t.Errorf("findCategory found a missing name")
	}
}

func TestMessageTypeAndText(t *testing.T) {
	if messageType("sticker") != "sticker" || messageType("weird") != "other" {
		t.Errorf("messageType mapping wrong")
	}
	cases := []struct {
		m    store.Message
		want string
	}{
		{store.Message{Kind: "text", Text: "oi"}, "oi"},
		{store.Message{Kind: "", Text: "sem tipo"}, "sem tipo"},
		{store.Message{Kind: "image", Caption: "foto da obra"}, "foto da obra"},
		{store.Message{Kind: "audio", Text: "[áudio 0:42]"}, "[áudio 0:42]"},
		{store.Message{Kind: "image"}, "[imagem]"},
		{store.Message{Kind: "video"}, "[vídeo]"},
		{store.Message{Kind: "document"}, "[documento]"},
		{store.Message{Kind: "sticker"}, "[figurinha]"},
		{store.Message{Kind: "location"}, "[localização]"},
		{store.Message{Kind: "contact"}, "[contato]"},
		{store.Message{Kind: "other"}, "[mídia]"},
	}
	for _, c := range cases {
		if got := messageText(c.m); got != c.want {
			t.Errorf("messageText(%s) = %q, want %q", c.m.Kind, got, c.want)
		}
	}
}

func TestClampHelpers(t *testing.T) {
	var notes list[string]
	if got := clampInt("limit", 0, 10, 30, &notes); got != 10 || len(notes) != 0 {
		t.Errorf("default = %d, notes %v", got, notes)
	}
	if got := clampInt("limit", 31, 10, 30, &notes); got != 30 || len(notes) != 1 {
		t.Errorf("max = %d, notes %v", got, notes)
	}
	if got := clampInt("limit", 7, 10, 30, &notes); got != 7 || len(notes) != 1 {
		t.Errorf("inside = %d, notes %v", got, notes)
	}
	if got := clampOffset("offset", -3, 100, &notes); got != 0 {
		t.Errorf("negative offset = %d", got)
	}
	if got := clampOffset("offset", 101, 100, &notes); got != 100 || len(notes) != 2 {
		t.Errorf("offset max = %d, notes %v", got, notes)
	}
}

func TestPresenterTimeInLocalZone(t *testing.T) {
	e := &env{loc: fixtureLoc}
	p := &presenter{e: e, now: fixtureNow}
	ts := fixtureNow.Add(-12 * time.Minute).Unix()
	if got := p.timeOf(ts); got != "2026-10-07T19:48:00-03:00" {
		t.Errorf("timeOf = %q", got)
	}
	if got := p.ago(ts); got != "há 12 min" {
		t.Errorf("ago = %q", got)
	}
	if got := p.ago(0); got != "" {
		t.Errorf("ago(0) = %q, want empty", got)
	}
}
