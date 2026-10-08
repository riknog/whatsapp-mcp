package privacy

import (
	"regexp"
	"strings"
	"unicode"
)

const (
	jidPlaceholder   = "<jid>"
	phonePlaceholder = "<phone>"

	// minPhoneDigits is the smallest digit count treated as a phone number.
	// Local numbers such as "98765-4321" have 9 digits; 7 or fewer is left alone.
	minPhoneDigits = 8
)

// jidPattern matches WhatsApp JIDs: user servers with an optional device
// suffix (":12"), the legacy c.us server, LIDs, and group JIDs.
var jidPattern = regexp.MustCompile(`\d+(?::\d+)?@(?:s\.whatsapp\.net|c\.us|lid)|\d+-?\d*@g\.us`)

// phoneCandidate matches the maximal run of digits in which up to five
// characters that are neither letters nor digits (spaces of any Unicode kind,
// punctuation, "/", "_", ":", ",", zero-width characters) sit between digits.
// An optional "+" or "(" may come first. A candidate is always judged as a
// whole: a date or money value inside a longer run does not protect the run.
var phoneCandidate = regexp.MustCompile(`(?:\+|\()?\d(?:[^\p{L}\p{M}0-9]{0,5}\d)*`)

// Exact forms that are not phone numbers. They only protect a candidate when
// the whole candidate has the form; a part of a candidate never does.
// Bare times ("19:30:00") have at most 6 digits, so they never reach the
// phone threshold and need no rule of their own.
const datePattern = `(?:\d{2}[/.-]\d{2}[/.-]\d{4}|\d{4}-\d{2}-\d{2})`

var (
	exactDate     = regexp.MustCompile(`^` + datePattern + `$`)
	exactDateTime = regexp.MustCompile(`^` + datePattern + ` \d{2}:\d{2}(?::\d{2})?$`)
	exactMoney    = regexp.MustCompile(`^\d{1,3}(?:\.\d{3})*(?:,\d{2})?$`)
)

// RedactLog replaces JIDs with "<jid>" and phone-like digit runs (8 or more
// digits, with separators of any kind) with "<phone>". Whole candidates that
// are exactly a date (dd/mm/aaaa, dd-mm-aaaa, dd.mm.aaaa, aaaa-mm-dd), a date
// with a time (hh:mm or hh:mm:ss), or a Brazilian money amount are kept.
// Fullwidth ASCII is folded to ASCII first. E-mail addresses become "<email>"
// and CPFs and CNPJs "<doc>" (see RedactDocuments). The output does not change
// when RedactLog is applied again.
func RedactLog(s string) string {
	s = foldFullwidth(s)
	s = jidPattern.ReplaceAllString(s, jidPlaceholder)
	s = redactDocumentsLog(s)

	var b strings.Builder
	last := 0
	for _, loc := range phoneCandidate.FindAllStringIndex(s, -1) {
		start, end := loc[0], loc[1]
		if !isPhone(s, start, end) {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(phonePlaceholder)
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

// isPhone decides whether the whole candidate s[start:end] is a phone number.
func isPhone(s string, start, end int) bool {
	c := s[start:end]
	if countDigits(c) < minPhoneDigits {
		return false
	}
	return !isExactNonPhone(s, start, c)
}

// isExactNonPhone reports whether the whole candidate c, which starts at
// s[start:], is exactly a date, a date with a time, or a money amount.
// A money amount needs a decimal part or a "R$" before it, so that a bare
// "11.987.654.321" is still treated as a phone.
func isExactNonPhone(s string, start int, c string) bool {
	if exactDate.MatchString(c) || exactDateTime.MatchString(c) {
		return true
	}
	return exactMoney.MatchString(c) && (strings.Contains(c, ",") || hasCurrencyPrefix(s[:start]))
}

func hasCurrencyPrefix(before string) bool {
	return strings.HasSuffix(strings.TrimRightFunc(before, unicode.IsSpace), "R$")
}

// foldFullwidth maps fullwidth ASCII (U+FF01-U+FF5E, which includes digits and
// "+", "-", "(") and the ideographic space to plain ASCII, so that the patterns
// see the same characters whatever keyboard produced them.
func foldFullwidth(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E:
			return r - 0xFEE0
		case r == 0x3000:
			return ' '
		}
		return r
	}, s)
}

func countDigits(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			n++
		}
	}
	return n
}
