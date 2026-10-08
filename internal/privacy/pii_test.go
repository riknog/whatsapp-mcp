package privacy

import (
	"strings"
	"testing"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

type nested struct {
	Name     string
	Contacts []contactOut
	Extra    map[string]any
}

type contactOut struct {
	Name string `json:"name"`
	Ref  string `json:"contact_ref"`
	When string `json:"time"`
}

func TestAssertNoPIIPassesCleanOutput(t *testing.T) {
	out := nested{
		Name: "Resumo",
		Contacts: []contactOut{
			{Name: "Mãe", Ref: "c_k3m9x2q8va", When: "2026-10-07T19:42:10-03:00"},
			{Name: "Loja 24h", Ref: "c_abcdefghij", When: "2026-10-07T10:00:00Z"},
		},
		Extra: map[string]any{
			"count":    3,
			"ratio":    0.5,
			"masked":   "+55 11 9****-**21",
			"email":    "a***@exemplo.com.br",
			"cpf":      "***.***.***-09",
			"cnpj":     "**.***.***/****-95",
			"cep":      "01310-100",
			"money":    "R$ 1.234,56",
			"date":     "07/10/2026",
			"apto":     "Apto 1203 bloco 4",
			"nullable": nil,
			"flag":     true,
		},
	}
	if err := AssertNoPII(out); err != nil {
		t.Fatalf("saída limpa reprovada: %v", err)
	}
}

func TestAssertNoPIIFailures(t *testing.T) {
	tests := []struct {
		name string
		v    any
		path string // substring expected in the error, identifies the field
	}{
		{"jid in string", map[string]any{"to": "5511987654321@s.whatsapp.net"}, "$.to"},
		{"lid deep in slice", map[string]any{"a": []any{map[string]any{"x": "123@lid"}}}, "$.a[0].x"},
		{"group jid", struct{ G string }{"120363000000000000@g.us"}, "$.G"},
		{"legacy c.us", []string{"ok", "5511@c.us"}, "$[1]"},
		{"jid in nested struct", nested{Contacts: []contactOut{{Ref: "c_ok", When: "x@lid"}}}, "$.Contacts[0].time"},
		{"unmasked phone", map[string]any{"msg": "ligue 98765-4321 agora"}, "$.msg"},
		{"unmasked international", map[string]any{"msg": "+5511987654321"}, "$.msg"},
		{"adjacent phones", map[string]any{"msg": "11987654321, 11912345678"}, "$.msg"},
		{"glued digits after iso", map[string]any{"t": "2026-10-07T19:42:10.11987654321"}, "$.t"},
		{"phone in a key", map[string]any{"5511987654321": "x"}, "$ (chave)"},
		{"unmasked email", map[string]any{"msg": "escreve pra ana@exemplo.com.br"}, "$.msg"},
		{"unmasked cpf", map[string]any{"msg": "CPF 123.456.789-09"}, "$.msg"},
		{"unmasked cnpj", map[string]any{"msg": "12.345.678/0001-95"}, "$.msg"},
		{"big integer", map[string]any{"ts": 1791302400}, "$.ts"},
		{"big negative", map[string]any{"v": -12345678}, "$.v"},
		{"big float", map[string]any{"v": 12345678.5}, "$.v"},
		{"toolerr details phone", toolerr.New(toolerr.CodeInvalidArgument, "x", map[string]any{"raw": "5511987654321"}), "details"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := AssertNoPII(tc.v)
			if err == nil {
				t.Fatal("PII não detectada")
			}
			if !strings.Contains(err.Error(), tc.path) {
				t.Errorf("erro %q não aponta o campo %q", err, tc.path)
			}
		})
	}
}

func TestAssertNoPIIErrorDoesNotEchoValue(t *testing.T) {
	const phone = "5511987654321"
	err := AssertNoPII(map[string]any{"msg": "ligue " + phone})
	if err == nil {
		t.Fatal("telefone não detectado")
	}
	if strings.Contains(err.Error(), "5511") || strings.Contains(err.Error(), phone) {
		t.Errorf("mensagem de erro ecoa o valor: %q", err)
	}
}

func TestAssertNoPIIFindsPhoneInsideToolError(t *testing.T) {
	ok := toolerr.New(toolerr.CodeAmbiguousContact, "Mais de um contato.", map[string]any{
		"candidates": []map[string]any{{"name": "João", "contact_ref": "c_abcdefghij"}},
	})
	if err := AssertNoPII(ok); err != nil {
		t.Fatalf("erro limpo reprovado: %v", err)
	}
}

func TestAssertNoPIIAcceptsMaskedPhoneOfAnyLength(t *testing.T) {
	for _, s := range []string{"9****-**21", "+55 11 9****-**21", "******78", "+55119******21"} {
		if err := AssertNoPII(map[string]string{"v": s}); err != nil {
			t.Errorf("%q reprovado: %v", s, err)
		}
	}
}

func TestAssertNoPIIMarshalError(t *testing.T) {
	if err := AssertNoPII(map[string]any{"f": func() {}}); err == nil {
		t.Fatal("valor não serializável aceito")
	}
}
