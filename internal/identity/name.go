package identity

import (
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/riknog/whatsapp-mcp/internal/privacy"
	"github.com/riknog/whatsapp-mcp/internal/store"
)

// unknownName is the display name of a chat or contact with no known name.
const unknownName = "Desconhecido"

// DisplayName picks the name shown to the model (design §5). Direct chats use
// the contact name (full_name, first_name, push_name, business_name), then the
// cached chat name. Groups use the group name. A name that holds a phone number
// (a saved or pushed name that is a number) is skipped, so a number never
// reaches the model this way. The result is never empty. A zero store.Contact
// means the chat has no contact row.
func DisplayName(contact store.Contact, chat store.Chat) string {
	if chat.Kind != "group" {
		for _, n := range []string{contact.FullName, contact.FirstName, contact.PushName, contact.BusinessName} {
			if name := safeName(n); name != "" {
				return name
			}
		}
	}
	if name := safeName(chat.DisplayName); name != "" {
		return name
	}
	return unknownName
}

// safeName trims s and returns it, or "" when s is blank or contains a phone
// number or JID that privacy.RedactText would change.
func safeName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || privacy.RedactText(s) != s {
		return ""
	}
	return s
}

// Normalize prepares a name for matching: lower case, accents removed (NFKD
// and combining marks dropped), apostrophes dropped, every other non-letter
// and non-digit (emoji, punctuation, symbols) turned into a space, and runs of
// spaces collapsed. "Mãe ❤️" becomes "mae"; "D'Ávila" becomes "davila".
func Normalize(s string) string {
	// Lower case is applied per rune after NFKD: decomposition can turn a lower
	// case letter into an upper case one (ϔ becomes Υ plus a diaeresis).
	// Invalid UTF-8 would make NFKD skip the text around it.
	s = norm.NFKD.String(strings.ToValidUTF8(s, " "))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Cf, r):
			// Accents and invisible format characters (ZWJ, variation selectors) vanish.
		case r == '\'', r == '’', r == '`', r == '´':
			// Apostrophes join the word they sit in.
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// Ago describes how long ago t was, relative to now, in Brazilian Portuguese:
// "agora", "há 5 min", "há 2 h", "ontem", "há 3 dias", "há 2 semanas",
// "há 4 meses", "há 1 ano". Calendar days use the location of now. A zero t
// gives "" (no interaction).
func Ago(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "agora"
	case d < time.Hour:
		return "há " + plural(int(d/time.Minute), "min", "min")
	case d < 24*time.Hour:
		return "há " + plural(int(d/time.Hour), "h", "h")
	}

	days := calendarDays(now, t)
	switch {
	case days <= 1:
		return "ontem"
	case days < 14:
		return "há " + plural(days, "dia", "dias")
	case days < 30:
		return "há " + plural(days/7, "semana", "semanas")
	case days < 365:
		return "há " + plural(days/30, "mês", "meses")
	default:
		return "há " + plural(days/365, "ano", "anos")
	}
}

// calendarDays counts the local calendar days between t and now.
func calendarDays(now, t time.Time) int {
	loc := now.Location()
	y1, m1, d1 := now.In(loc).Date()
	y2, m2, d2 := t.In(loc).Date()
	a := time.Date(y1, m1, d1, 0, 0, 0, 0, time.UTC)
	b := time.Date(y2, m2, d2, 0, 0, 0, 0, time.UTC)
	return int(a.Sub(b).Hours() / 24)
}

// plural formats n with the singular or plural unit. Minutes and hours use the
// same word in both forms ("min", "h").
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
