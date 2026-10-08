package identity

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// FuzzNormalize checks that Normalize never panics, is idempotent and returns
// lower-case, trimmed, single-spaced valid UTF-8.
func FuzzNormalize(f *testing.F) {
	for _, s := range []string{"", "Mãe ❤️", "D'Ávila", "  JOÃO   da  Silva ", "ﬁ ẞ İ", "\xff\xfe", "a\u200db", "Ⅻ ① ㈱"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := Normalize(s)
		if again := Normalize(got); again != got {
			t.Fatalf("not idempotent: %q -> %q -> %q", s, got, again)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8 from %q: %q", s, got)
		}
		if got != strings.Join(strings.Fields(got), " ") {
			t.Fatalf("spaces not collapsed: %q", got)
		}
		for _, r := range got {
			if unicode.IsUpper(r) {
				t.Fatalf("upper case %q in %q", r, got)
			}
		}
	})
}
