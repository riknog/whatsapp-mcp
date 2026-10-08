package identity

import (
	"strings"
	"testing"
)

// mapDigits rewrites the ASCII digits of s into another script, starting at base.
func mapDigits(s string, base rune) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r - '0' + base
		}
		return r
	}, s)
}

func TestLooksLikePhoneOrJID(t *testing.T) {
	const fullwidth = 0xFF10 // U+FF10 is fullwidth zero
	const arabic = 0x0660    // U+0660 is Arabic-Indic zero
	tests := []struct {
		name string
		in   string
		want bool
	}{
		// Phones and JIDs: rejected.
		{"plus with country code", "+5511987654321", true},
		{"formatted with parentheses", "+55 (11) 98765-4321", true},
		{"local with dash", "11 98765-4321", true},
		{"dotted", "11.98765.4321", true},
		{"no-break space", "11 98765-4321", true},
		{"tab and spaces", "11\t98765\t4321", true},
		{"fullwidth digits", mapDigits("5511987654321", fullwidth), true},
		{"fullwidth formatted", mapDigits("(11) 98765-4321", fullwidth), true},
		{"arabic-indic digits", mapDigits("5511987654321", arabic), true},
		{"user jid", "5511987654321@s.whatsapp.net", true},
		{"jid with device", "5511987654321:12@s.whatsapp.net", true},
		{"jid uppercase server", "5511987654321@S.WHATSAPP.NET", true},
		{"lid", "123@lid", true},
		{"group jid", "120363000000000000@g.us", true},
		{"legacy c.us", "5511@c.us", true},
		{"eight plain digits", "12345678", true},

		// Legitimate names: kept.
		{"name with one digit", "João 2", false},
		{"year in name", "Turma 2024", false},
		{"short number", "Loja 24h", false},
		{"apartment", "Apto 1203 bloco 4", false},
		{"seven digits", "Zé 1234567", false},
		{"seven digits spread", "Zé 1 2 3 4 5 6 7", false},
		{"accent and emoji", "Mãe ❤️", false},
		{"email on other domain", "email@lidl.com", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikePhoneOrJID(tc.in); got != tc.want {
				t.Errorf("looksLikePhoneOrJID(%q) = %v, esperado %v", tc.in, got, tc.want)
			}
		})
	}
}
