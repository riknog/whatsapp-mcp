package privacy

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func TestRedactDocumentsTable(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"email", "manda pra maria@gmail.com", "manda pra m***@gmail.com"},
		{"email with plus and dots", "x.y+z@sub.empresa.com.br", "x***@sub.empresa.com.br"},
		{"two emails", "a@b.com, cd@e.org", "a***@b.com, c***@e.org"},
		{"not an email: no tld", "user@localhost", "user@localhost"},
		{"not an email: at sign alone", "me @ casa", "me @ casa"},
		{"cpf formatted", "123.456.789-09", "***.***.***-09"},
		{"cpf formatted with wrong check digits", "111.222.333-44", "***.***.***-44"},
		{"cpf partly formatted, valid", "123456789-09", "***.***.***-09"},
		{"cpf partly formatted, invalid", "123456789-00", "123456789-00"},
		{"cpf digits, valid", "12345678909", "***.***.***-09"},
		{"cpf digits, invalid", "12345678900", "12345678900"},
		{"cpf of one repeated digit", "11111111111", "11111111111"},
		{"cnpj formatted", "12.345.678/0001-95", "**.***.***/****-95"},
		{"cnpj digits, valid", "12345678000195", "**.***.***/****-95"},
		{"cnpj digits, invalid", "12345678000190", "12345678000190"},
		{"cpf inside a longer number", "9123.456.789-09", "9123.456.789-09"},
		{"fullwidth cpf", "１２３.４５６.７８９-０９", "***.***.***-09"},
		{"date untouched", "07/10/2026", "07/10/2026"},
		{"cep untouched", "01310-100", "01310-100"},
		{"plain", "nada aqui", "nada aqui"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactDocuments(tc.in); got != tc.want {
				t.Errorf("RedactDocuments(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
			if again := RedactDocuments(RedactDocuments(tc.in)); again != RedactDocuments(tc.in) {
				t.Errorf("não idempotente para %q", tc.in)
			}
		})
	}
}

func TestCheckDigits(t *testing.T) {
	for _, d := range []string{"12345678909", "52998224725", "11144477735"} {
		if !validCPF(d) {
			t.Errorf("validCPF(%s) = false", d)
		}
	}
	for _, d := range []string{"12345678000195", "11222333000181", "45997418000153"} {
		if !validCNPJ(d) {
			t.Errorf("validCNPJ(%s) = false", d)
		}
	}
	for _, d := range []string{"12345678901", "123", "00000000000"} {
		if validCPF(d) {
			t.Errorf("validCPF(%s) = true", d)
		}
	}
	for _, d := range []string{"12345678000100", "1234", "00000000000000"} {
		if validCNPJ(d) {
			t.Errorf("validCNPJ(%s) = true", d)
		}
	}
}

// randomCPF builds a CPF with valid check digits.
func randomCPF(r *rand.Rand) string {
	base := fmt.Sprintf("%09d", r.Intn(1_000_000_000))
	d1 := checkDigit(base, 10)
	d2 := checkDigit(base+string(d1), 11)
	return base + string(d1) + string(d2)
}

// randomCNPJ builds a CNPJ with valid check digits.
func randomCNPJ(r *rand.Rand) string {
	base := fmt.Sprintf("%08d0001", r.Intn(100_000_000))
	d1 := cnpjDigit(base)
	d2 := cnpjDigit(base + string(d1))
	return base + string(d1) + string(d2)
}

// TestRedactTextNeverLeavesADocument is a property test with a fixed seed: valid
// CPFs and CNPJs, formatted or not, never reach the output of RedactText.
func TestRedactTextNeverLeavesADocument(t *testing.T) {
	r := rand.New(rand.NewSource(20261008))
	for trial := 0; trial < 500; trial++ {
		cpf, cnpj := randomCPF(r), randomCNPJ(r)
		if allSame(cpf) || allSame(cnpj) {
			continue
		}
		forms := []string{
			cpf,
			cpf[:3] + "." + cpf[3:6] + "." + cpf[6:9] + "-" + cpf[9:],
			cnpj,
			cnpj[:2] + "." + cnpj[2:5] + "." + cnpj[5:8] + "/" + cnpj[8:12] + "-" + cnpj[12:],
		}
		in := "dados: " + strings.Join(forms, " ; ") + " fim"
		out := RedactText(in)
		for _, d := range []string{cpf, cnpj} {
			if strings.Contains(digitsOnly(out), d[:len(d)-2]) {
				t.Fatalf("trial %d: documento %s aparece em %q", trial, d, out)
			}
		}
		if hasReadableDocument(out) {
			t.Fatalf("trial %d: documento legível em %q", trial, out)
		}
		if again := RedactText(out); again != out {
			t.Fatalf("trial %d: não idempotente: %q -> %q", trial, out, again)
		}
	}
}
