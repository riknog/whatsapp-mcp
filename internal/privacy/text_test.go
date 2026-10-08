package privacy

import "testing"

func TestRedactTextTable(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// Phones are masked: DDI/DDD and the first digit stay, last two stay.
		{"international formatted", "+55 11 98765-4321", "+55 11 9****-**21"},
		{"local with dash", "98765-4321", "9****-**21"},
		{"international plain", "tel +5511987654321", "tel +55119******21"},
		{"parenthesised area code", "(11) 98765-4321", "(11) 9****-**21"},
		{"in a sentence", "ligar para 98765-4321 agora", "ligar para 9****-**21 agora"},
		{"eight digits", "codigo 12345678 ok", "codigo ******78 ok"},
		{"two numbers", "98765-4321 e 91234-5678", "9****-**21 e 9****-**78"},
		{"fullwidth digits in phone", "Tel ５５１１９８７６５４３２１", "Tel ５５１１９******２１"},
		{"ISO timestamp is not a phone", "2026-10-07T19:42:10-03:00", "2026-10-07T19:42:10-03:00"},
		{"ISO timestamp next to phone", "2026-10-07T19:42:10-03:00 e 98765-4321", "2026-10-07T19:42:10-03:00 e 9****-**21"},

		// JIDs become placeholders.
		{"jid", "enviado para 5511987654321@s.whatsapp.net", "enviado para <jid>"},
		{"lid", "contato 123456789012345@lid", "contato <jid>"},

		// Adjacent phones: each one is masked on its own.
		{"two phones comma space", "11987654321, 11912345678", "119******21, 119******78"},
		{"two phones space", "11987654321 11912345678", "119******21 119******78"},
		{"two formatted phones with slash", "(11) 98765-4321 / (21) 98888-7777", "(11) 9****-**21 / (21) 9****-**77"},
		{"three phones no space", "11987654321,11912345678,11955556666", "119******21,119******78,119******66"},
		{"two phones line break", "11987654321\n11912345678", "119******21\n119******78"},
		{"two phones semicolon", "11987654321;11912345678", "119******21;119******78"},
		{"unsplittable group keeps last two", "98765-4321 11912345678", "*****-**** *********78"},
		{"phone in pairs inside long group", "55-11-98-76-54-32-10-99", "**-**-**-**-**-**-**-99"},
		{"money then phone", "R$ 1.234,56 98765-4321", "R$ 1.234,56 9****-**21"},

		// ISO timestamps with digits glued to them are not exempt.
		{"iso fraction with glued digits", "2026-10-07T19:42:10.11987654321", "2026-10-07T**:**:**.*********21"},
		{"iso offset with glued digits", "2026-10-07 19:42-11987654321", "****-**-** **:**-*********21"},
		{"iso exempt when followed by space", "2026-10-07T19:42:10.123456 ok", "2026-10-07T19:42:10.123456 ok"},

		// Must NOT be masked.
		{"date slash", "07/10/2026", "07/10/2026"},
		{"date in sentence", "dia 07/10/2026 cedo", "dia 07/10/2026 cedo"},
		{"date iso", "2026-10-07", "2026-10-07"},
		{"time", "19:30", "19:30"},
		{"datetime br", "07/10/2026 19:30", "07/10/2026 19:30"},
		{"currency br", "R$ 1.234,56", "R$ 1.234,56"},
		{"currency big", "R$ 12.345.678,90", "R$ 12.345.678,90"},
		{"cep", "01310-100", "01310-100"},
		{"cep in sentence", "CEP 01310-100 fica perto", "CEP 01310-100 fica perto"},
		{"cpf formatted", "CPF 123.456.789-09", "CPF 123.456.789-09"},
		{"cnpj formatted", "CNPJ 12.345.678/0001-95", "CNPJ 12.345.678/0001-95"},
		{"seven digits", "lote 1234567 ok", "lote 1234567 ok"},
		{"apartment", "Apto 1203 bloco 4", "Apto 1203 bloco 4"},
		{"counters", "3 mensagens, 12 chats", "3 mensagens, 12 chats"},
		{"plain text", "nada a redigir aqui", "nada a redigir aqui"},
		{"empty", "", ""},
		{"fullwidth letters untouched", "Ｏｉ, tudo bem?", "Ｏｉ, tudo bem?"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactText(tc.in); got != tc.want {
				t.Errorf("RedactText(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactTextIsIdempotent(t *testing.T) {
	inputs := []string{
		"+55 11 98765-4321",
		"5511987654321",
		"Tel ５５１１９８７６５４３２１ e 98765-4321",
		"07/10/2026 19:30 R$ 1.234,56 CEP 01310-100",
		"5511987654321@s.whatsapp.net",
	}
	for _, in := range inputs {
		once := RedactText(in)
		twice := RedactText(once)
		if once != twice {
			t.Errorf("não idempotente para %q: %q -> %q", in, once, twice)
		}
	}
}

func TestMaskDigitsKeepsSeparatorsAndEdges(t *testing.T) {
	if got := maskDigits("+55 11 98765-4321"); got != "+55 11 9****-**21" {
		t.Errorf("maskDigits = %q", got)
	}
	if got := maskDigits("1234-5678"); got != "****-**78" {
		t.Errorf("8 dígitos: %q", got)
	}
}
