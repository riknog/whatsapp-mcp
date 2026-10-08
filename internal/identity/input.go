package identity

import (
	"regexp"
	"strings"
	"unicode"
)

// minInputDigits is the digit count from which an input looks like a phone number.
const minInputDigits = 8

// jidServerPattern matches an "@" followed by a WhatsApp server name.
var jidServerPattern = regexp.MustCompile(`(?i)@(?:s\.whatsapp\.net|lid|g\.us|c\.us)\b`)

// looksLikePhoneOrJID reports whether a contact input is a phone number or a
// JID. Any "@" with a WhatsApp server, or eight or more digits of any script
// (ASCII, fullwidth, Arabic-Indic), counts.
// Separators do not matter: "11 98765-4321" has 11 digits. "Apto 1203 bloco 4"
// has 5 and is a valid name.
func looksLikePhoneOrJID(in string) bool {
	if jidServerPattern.MatchString(in) {
		return true
	}
	digits := 0
	for _, r := range in {
		if unicode.IsDigit(r) {
			digits++
		}
	}
	return digits >= minInputDigits
}

// CheckFreeText rejects a free-text input (a search query) that looks like a
// phone number or a JID, with phone_not_allowed: the model must not be able to
// confirm a guessed number through the local data.
func CheckFreeText(in string) error {
	if looksLikePhoneOrJID(in) {
		return phoneNotAllowed()
	}
	return nil
}

// matchLevel says how a normalized query matches a normalized name: "exact",
// "prefix", "contains" or "token" (every query word is a whole word of the
// name). It returns "" when there is no match.
func matchLevel(q string, qWords []string, name string) string {
	if name == "" {
		return ""
	}
	switch {
	case name == q:
		return LevelExact
	case strings.HasPrefix(name, q):
		return LevelPrefix
	case strings.Contains(name, q):
		return LevelContains
	}
	words := strings.Fields(name)
	for _, w := range qWords {
		if !containsWord(words, w) {
			return ""
		}
	}
	return LevelToken
}

func containsWord(words []string, w string) bool {
	for _, x := range words {
		if x == w {
			return true
		}
	}
	return false
}

// Match levels, from strongest to weakest.
const (
	LevelExact    = "exact"
	LevelPrefix   = "prefix"
	LevelContains = "contains"
	LevelToken    = "token"
)

// levelRank orders the levels; a smaller rank is a stronger match.
func levelRank(level string) int {
	switch level {
	case LevelExact:
		return 0
	case LevelPrefix:
		return 1
	case LevelContains:
		return 2
	default:
		return 3
	}
}
