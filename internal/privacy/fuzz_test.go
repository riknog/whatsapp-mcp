package privacy

import "testing"

// FuzzRedactText checks that RedactText never panics, is idempotent and leaves
// no full phone number and no JID (a bare "@lid" is not personal data).
func FuzzRedactText(f *testing.F) {
	for _, s := range []string{
		"", "me liga no +55 11 98765-4321", "5511987654321@s.whatsapp.net", "(11) 3333-4444",
		"123456789012@lid", "pedido 2026-10-08T10:00:00Z", "R$ 1.234.567,89", "+1 (555) 010-9999 x12",
		"٠١٢٣٤٥٦٧٨٩٠١", "11 9 8765 4321", "3EB0A1B2C3D4E5F60718",
		"CPF 123.456.789-09", "12.345.678/0001-95", "a.b+c@d-e.com.br", "+12345678909",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := RedactText(s)
		if again := RedactText(got); again != got {
			t.Fatalf("not idempotent: %q -> %q -> %q", s, got, again)
		}
		if hasReadablePhone(got) {
			t.Fatalf("RedactText(%q) = %q keeps a phone number", s, got)
		}
		if jidPattern.MatchString(got) {
			t.Fatalf("RedactText(%q) = %q keeps a JID", s, got)
		}
		if hasReadableDocument(got) {
			t.Fatalf("RedactText(%q) = %q keeps an e-mail, CPF or CNPJ", s, got)
		}
	})
}
