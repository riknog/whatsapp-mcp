package privacy

import (
	"regexp"
	"testing"
)

func TestRedactLog(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// JIDs
		{"user jid", "enviado para 5511987654321@s.whatsapp.net", "enviado para <jid>"},
		{"lid", "contato 123456789012345@lid", "contato <jid>"},
		{"legacy c.us", "to 5511987654321@c.us", "to <jid>"},
		{"group jid", "grupo 123456789-1234567890@g.us ok", "grupo <jid> ok"},
		{"jid with device suffix", "dev 5511987654321:12@s.whatsapp.net", "dev <jid>"},
		{"short jid user part", "x 1234567@s.whatsapp.net", "x <jid>"},
		{"plus before jid", "+5511987654321@lid", "+<jid>"},

		// Phones (8+ digits, with separators)
		{"international formatted", "+55 (11) 98765-4321", "<phone>"},
		{"local with dash", "98765-4321", "<phone>"},
		{"local in sentence", "ligar para 98765-4321 agora", "ligar para <phone> agora"},
		{"international plain", "tel +5511987654321", "tel <phone>"},
		{"prefix colon kept", "tel:+5511987654321", "tel:<phone>"},
		{"eight digits", "codigo 12345678 ok", "codigo <phone> ok"},
		{"dotted phone", "11.98765.4321", "<phone>"},
		{"cpf", "CPF 123.456.789-09", "CPF <doc>"},
		{"cnpj", "CNPJ 12.345.678/0001-95", "CNPJ <doc>"},
		{"invalid unformatted cpf is a phone", "12345678900", "<phone>"},
		{"email", "de joao.silva@empresa.com.br", "de <email>"},
		{"cep-like redacted (safe side)", "CEP 01310-100", "CEP <phone>"},
		{"digits with colon kept as phone", "5511987654321:12", "<phone>"},
		{"plus digits with colon", "+5511987654321:0", "<phone>"},
		{"no-separator money without decimal part", "R$ 12345678", "R$ <phone>"},
		{"no-separator value with decimal part", "total 12345678,90", "total <phone>"},
		{"bare dotted number is a phone", "11.987.654.321", "<phone>"},

		// Must NOT be redacted
		{"date slash", "07/10/2026", "07/10/2026"},
		{"date dash dd-mm-yyyy", "07-10-2026", "07-10-2026"},
		{"date iso", "2026-10-07", "2026-10-07"},
		{"date dot", "07.10.2026", "07.10.2026"},
		{"time", "19:30:00", "19:30:00"},
		{"datetime iso", "2026-10-07 19:30:00", "2026-10-07 19:30:00"},
		{"datetime br", "07/10/2026 19:30:00", "07/10/2026 19:30:00"},
		{"datetime br short time", "07/10/2026 19:30", "07/10/2026 19:30"},
		{"currency br", "R$ 1.234,56", "R$ 1.234,56"},
		{"currency big with thousands", "R$ 12.345.678,90", "R$ 12.345.678,90"},
		{"currency big no decimal", "R$ 12.345.678", "R$ 12.345.678"},
		{"currency comma decimal", "12.345.678,90", "12.345.678,90"},
		{"seven digits", "lote 1234567 ok", "lote 1234567 ok"},
		{"short counters", "3 mensagens, 12 chats", "3 mensagens, 12 chats"},
		{"hex id", "id 3EB0C767D6E4B3E8F3A1", "id 3EB0C767D6E4B3E8F3A1"},
		{"plain text", "nada a redigir aqui", "nada a redigir aqui"},
		{"empty", "", ""},

		// Mixed
		{"jid then phone", "5511987654321@s.whatsapp.net e 98765-4321", "<jid> e <phone>"},
		{"date then phone is one candidate, masked whole", "07/10/2026 98765-4321", "<phone>"},
		{"datetime then phone", "07/10/2026 19:30:00 98765-4321", "<phone>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactLog(tc.in)
			if got != tc.want {
				t.Fatalf("RedactLog(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// phoneVariants are phone numbers in forms that must never survive redaction.
// Each one is checked on its own and inside a sentence.
var phoneVariants = []string{
	"+55 (11) 98765-4321",
	"+5511987654321",
	"5511987654321",
	"+55-11-98765-4321",
	"55-11-98765-4321",
	"55-11-987654321",
	"+55.11.98765.4321",
	"11/98765/4321",
	"11_98765_4321",
	"(11) 98765-4321",
	"98765-4321",
	"+55 11 98765 4321", // no-break space, narrow no-break space, figure space
	"98765 4321",
	"55　 11 98765 4321", // ideographic space
	"＋５５１１９８７６５４３２１",              // fullwidth +5511987654321
	"５５１１９８７６５４３２１",               // fullwidth digits
	"10-10-2026 09-11-98765-4321", // date glued to a phone
	"07/10/2026 98765-4321",       // date then phone
	"5511987654321:12",            // device suffix without @
	"+5511987654321:0",
	"11 98765 4321",
	"+55 11 9876-5432",
	"12.345.678,90 98765-4321",
}

// maxDigitRun returns the length of the longest run of ASCII digits in s.
func maxDigitRun(s string) int {
	best := 0
	for _, run := range regexp.MustCompile(`[0-9]+`).FindAllString(s, -1) {
		if len(run) > best {
			best = len(run)
		}
	}
	return best
}

func TestRedactLogNeverKeepsFullPhone(t *testing.T) {
	for _, phone := range phoneVariants {
		for _, in := range []string{phone, "x " + phone + " y"} {
			out := RedactLog(in)
			if maxDigitRun(out) >= minPhoneDigits {
				t.Errorf("phone %q leaks an 8+ digit run in output %q (input %q)", phone, out, in)
			}
		}
	}
}

func TestRedactLogPhoneOnlyBecomesPlaceholder(t *testing.T) {
	// Glued dates and phones collapse to one placeholder, not a partial phone.
	cases := map[string]string{
		"10-10-2026 09-11-98765-4321": "<phone>",
		"+55-11-98765-4321":           "<phone>",
		"55-11-987654321":             "<phone>",
		"+55.11.98765.4321":           "<phone>",
		"11/98765/4321":               "<phone>",
		"11_98765_4321":               "<phone>",
		"98765 4321":                  "<phone>",
	}
	for in, want := range cases {
		if got := RedactLog(in); got != want {
			t.Errorf("RedactLog(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactLogIsIdempotent(t *testing.T) {
	inputs := []string{
		"enviado para 5511987654321@s.whatsapp.net",
		"+55 (11) 98765-4321 e 07/10/2026 19:30:00",
		"R$ 12.345.678,90 grupo 123456789-1234567890@g.us",
		"５５１１９８７６５４３２１",
		"nada",
	}
	inputs = append(inputs, phoneVariants...)
	for _, in := range inputs {
		once := RedactLog(in)
		if twice := RedactLog(once); twice != once {
			t.Fatalf("not idempotent: %q -> %q -> %q", in, once, twice)
		}
	}
}
