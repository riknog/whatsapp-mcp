package privacy

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// brPhone returns a random Brazilian phone as digits only: an 11-digit mobile,
// a 10-digit landline, or a 13-digit number with country code 55.
func brPhone(r *rand.Rand) string {
	ddd := 11 + r.Intn(89)
	switch r.Intn(3) {
	case 0:
		return fmt.Sprintf("%d9%08d", ddd, r.Intn(100000000))
	case 1:
		return fmt.Sprintf("%d%d%07d", ddd, 2+r.Intn(4), r.Intn(10000000))
	default:
		return fmt.Sprintf("55%d9%08d", ddd, r.Intn(100000000))
	}
}

// propSeparators are the ways two phones can sit next to each other in text.
var propSeparators = []string{
	", ", "; ", " / ", "\n", " ", "  ", ",", ";", "/", "-", ".", " - ", " ", " | ", "\r\n", " (e) ",
}

// digitsOnly keeps the ASCII digits of s, so that masks and separators drop out.
func digitsOnly(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// TestRedactTextNeverLeavesAFullPhone is a property test with a fixed seed.
// Random phones are joined with random separators. No phone may appear in the
// output, neither as text nor as the digits left once the masks are removed.
// Each phone also has to be masked on its own, so the output has "*" marks.
func TestRedactTextNeverLeavesAFullPhone(t *testing.T) {
	r := rand.New(rand.NewSource(20261007))
	for trial := 0; trial < 500; trial++ {
		n := 2 + r.Intn(4)
		phones := make([]string, n)
		var b strings.Builder
		for i := range phones {
			phones[i] = brPhone(r)
			if i > 0 {
				b.WriteString(propSeparators[r.Intn(len(propSeparators))])
			}
			b.WriteString(phones[i])
		}
		in := b.String()
		out := RedactText(in)

		if !strings.Contains(out, "*") {
			t.Fatalf("trial %d: nenhuma máscara em %q -> %q", trial, in, out)
		}
		for _, p := range phones {
			if strings.Contains(out, p) {
				t.Fatalf("trial %d: telefone %s aparece na saída %q (entrada %q)", trial, p, out, in)
			}
			if strings.Contains(digitsOnly(out), p) {
				t.Fatalf("trial %d: telefone %s aparece nos dígitos da saída %q (entrada %q)", trial, p, out, in)
			}
		}
		if again := RedactText(out); again != out {
			t.Fatalf("trial %d: não idempotente: %q -> %q", trial, out, again)
		}
	}
}
