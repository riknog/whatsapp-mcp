package identity

import (
	"testing"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/store"
)

func TestNormalize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Mãe ❤️", "mae"},
		{"João  Silva", "joao silva"},
		{"D'Ávila", "davila"},
		{"Loja-24h!", "loja 24h"},
		{"Zoë", "zoe"},
		{"ＡＢＣ", "abc"},
		{"a\u200db", "ab"},
		{"🙂", ""},
		{"", ""},
		{"  Ação  ", "acao"},
	}
	for _, tc := range tests {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, esperado %q", tc.in, got, tc.want)
		}
	}
}

func TestDisplayNamePriority(t *testing.T) {
	full := store.Contact{FullName: "Maria Full", FirstName: "Maria", PushName: "M", BusinessName: "Biz"}
	tests := []struct {
		name    string
		contact store.Contact
		chat    store.Chat
		want    string
	}{
		{"full name wins", full, store.Chat{Kind: "direct", DisplayName: "cache"}, "Maria Full"},
		{"first name", store.Contact{FirstName: "Ana", PushName: "x"}, store.Chat{Kind: "direct"}, "Ana"},
		{"push name", store.Contact{PushName: "Zé"}, store.Chat{Kind: "direct"}, "Zé"},
		{"business name", store.Contact{BusinessName: "Padaria"}, store.Chat{Kind: "direct"}, "Padaria"},
		{"blank contact names fall to chat cache", store.Contact{FullName: "  "}, store.Chat{Kind: "direct", DisplayName: "Cache"}, "Cache"},
		{"no name at all", store.Contact{}, store.Chat{Kind: "direct"}, "Desconhecido"},
		{"group uses group name", store.Contact{FullName: "Ignorado"}, store.Chat{Kind: "group", DisplayName: "Família"}, "Família"},
		{"unnamed group", store.Contact{}, store.Chat{Kind: "group"}, "Desconhecido"},
		{"push name that is a phone is skipped", store.Contact{PushName: "5511987654321", BusinessName: "Padaria"}, store.Chat{Kind: "direct"}, "Padaria"},
		{"phone-like chat name is skipped", store.Contact{}, store.Chat{Kind: "direct", DisplayName: "+55 11 98765-4321"}, "Desconhecido"},
		{"jid as name is skipped", store.Contact{FullName: "5511@s.whatsapp.net"}, store.Chat{Kind: "direct"}, "Desconhecido"},
		{"number in a short name is kept", store.Contact{FullName: "Loja 24h"}, store.Chat{Kind: "direct"}, "Loja 24h"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisplayName(tc.contact, tc.chat); got != tc.want {
				t.Errorf("DisplayName = %q, esperado %q", got, tc.want)
			}
		})
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"zero", time.Time{}, ""},
		{"now", now, "agora"},
		{"future", now.Add(time.Hour), "agora"},
		{"30 s", now.Add(-30 * time.Second), "agora"},
		{"1 min", now.Add(-time.Minute), "há 1 min"},
		{"5 min", now.Add(-5 * time.Minute), "há 5 min"},
		{"59 min", now.Add(-59 * time.Minute), "há 59 min"},
		{"1 h", now.Add(-time.Hour), "há 1 h"},
		{"2 h", now.Add(-2 * time.Hour), "há 2 h"},
		{"12 h", now.Add(-12 * time.Hour), "há 12 h"},
		{"yesterday", time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC), "ontem"},
		{"3 dias", time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC), "há 3 dias"},
		{"2 semanas", time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC), "há 2 semanas"},
		{"1 mês", time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC), "há 1 mês"},
		{"4 meses", time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC), "há 4 meses"},
		{"1 ano", time.Date(2025, 9, 1, 10, 0, 0, 0, time.UTC), "há 1 ano"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Ago(now, tc.t); got != tc.want {
				t.Errorf("Ago = %q, esperado %q", got, tc.want)
			}
		})
	}
}

func TestAgoUsesLocalCalendarDay(t *testing.T) {
	loc := time.FixedZone("BRT", -3*3600)
	now := time.Date(2026, 10, 7, 0, 30, 0, 0, loc)
	// 23:50 on the 6th is 40 minutes ago: minutes, not "ontem".
	t1 := time.Date(2026, 10, 6, 23, 50, 0, 0, loc)
	if got := Ago(now, t1); got != "há 40 min" {
		t.Errorf("Ago = %q, esperado \"há 40 min\"", got)
	}
	// 20:00 on the 6th is 4.5 h ago: still hours.
	t2 := time.Date(2026, 10, 6, 20, 0, 0, 0, loc)
	if got := Ago(now, t2); got != "há 4 h" {
		t.Errorf("Ago = %q, esperado \"há 4 h\"", got)
	}
}
