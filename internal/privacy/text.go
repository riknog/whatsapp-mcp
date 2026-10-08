package privacy

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// keepAfterMask is how many digits stay visible at the end of a masked phone.
const keepAfterMask = 2

// maskedDigits is how many digits are replaced by "*" in a phone of 8 to 13
// digits. The visible part is the start (n-8 digits, which covers country and
// area codes) plus the last two digits. "+55 11 98765-4321" becomes
// "+55 11 9****-**21".
const maskedDigits = 6

// maxPhoneDigits is the longest run that is one phone (country + area + number).
// A longer run that cannot be split is masked digit by digit (see maskRuns).
const maxPhoneDigits = 13

// isoTimestamp matches ISO-8601 date-times such as 2026-10-07T19:42:10-03:00.
// The fraction has 1 to 9 digits. A match is exempt only when no digit touches
// it on either side (see blankISO), so digits glued to a timestamp are still
// checked.
var isoTimestamp = regexp.MustCompile(
	`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(?::\d{2}(?:[.,]\d{1,9})?)?(?:Z|[+-]\d{2}(?::?\d{2})?)?`)

// Formatted identifiers that are not phone numbers. They are kept as they are,
// because a CEP, CPF or CNPJ is not contact data.
var (
	exactCEP  = regexp.MustCompile(`^\d{5}-\d{3}$`)
	exactCPF  = regexp.MustCompile(`^\d{3}\.\d{3}\.\d{3}-\d{2}$`)
	exactCNPJ = regexp.MustCompile(`^\d{2}\.\d{3}\.\d{3}/\d{4}-\d{2}$`)
)

// RedactText masks phone numbers in message text before the model sees it.
//
// A candidate run (digits with short separators) is split into single phones:
// ",", ";", "/", "|" and line breaks always split; a space splits when the
// group so far has 10 to 13 digits. Each phone is masked on its own, so
// "11987654321, 11912345678" gives "119******21, 119******78". A group of more
// than 13 digits that cannot be split keeps only its last two digits. The rules of RedactLog still decide what is a phone: dates,
// times, money amounts, fewer than 8 digits, a formatted CEP, CPF or CNPJ, and
// ISO-8601 timestamps are kept. JIDs become "<jid>". Only digits change, and
// fullwidth characters outside the phone stay as typed. Applying RedactText
// twice gives the same result.
func RedactText(s string) string {
	s = jidPattern.ReplaceAllString(s, jidPlaceholder)
	orig, spans := scanGroups(s, true)

	var b strings.Builder
	last := 0
	for _, sp := range spans {
		from, to := orig[sp[0]], orig[sp[1]]
		b.WriteString(s[last:from])
		b.WriteString(maskPhone(s[from:to]))
		last = to
	}
	b.WriteString(s[last:])
	return b.String()
}

// hasReadablePhone reports whether s has a phone group (as RedactText sees it,
// a "*" counting as a digit) with 8 or more readable digits. The output of
// RedactText never has one: a masked group keeps at most 7 digits.
func hasReadablePhone(s string) bool {
	orig, spans := scanGroups(s, true)
	for _, sp := range spans {
		if digitCount(s[orig[sp[0]]:orig[sp[1]]]) >= minPhoneDigits {
			return true
		}
	}
	return false
}

// scanGroups finds the phone groups of s. Spans are byte offsets into the
// folded text, and orig maps them back to s. Folding turns fullwidth ASCII into
// ASCII so the patterns see it, but the output keeps the original characters.
// With masked true, a "*" counts as a digit, so a phone that RedactText already
// masked is found again with the same span and masks to the same text: that
// keeps RedactText idempotent.
func scanGroups(s string, masked bool) (orig []int, spans [][2]int) {
	folded, orig := foldWithOffsets(s)
	if masked {
		folded = strings.ReplaceAll(folded, "*", "0")
	}
	d := blankISO(folded)
	for _, loc := range phoneCandidate.FindAllStringIndex(d, -1) {
		start, end := loc[0], loc[1]
		if !isPhone(d, start, end) || isKeptFormat(d[start:end]) {
			continue
		}
		for _, g := range phoneGroups(d, start, end) {
			if countDigits(d[g[0]:g[1]]) >= minPhoneDigits {
				spans = append(spans, g)
			}
		}
	}
	return orig, spans
}

// blankISO replaces every exempt ISO-8601 timestamp with "x" bytes, so that its
// digits do not count. Offsets stay the same. A match with a digit right before
// or after it is not exempt.
func blankISO(folded string) string {
	det := []byte(folded)
	for _, loc := range isoTimestamp.FindAllStringIndex(folded, -1) {
		if loc[1] < len(folded) && isASCIIDigit(folded[loc[1]]) {
			continue
		}
		if loc[0] > 0 && isASCIIDigit(folded[loc[0]-1]) {
			continue
		}
		for i := loc[0]; i < loc[1]; i++ {
			det[i] = 'x'
		}
	}
	return string(det)
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

// phoneGroups splits the candidate d[start:end] into phone-sized groups. Each
// group is a byte span of d that starts and ends on a digit. A group closes at
// a hard separator, or at a soft separator (whitespace or a format character)
// once it has 10 to 13 digits. Anything else, such as "-", ".", "(" or ")",
// stays inside the group.
func phoneGroups(d string, start, end int) [][2]int {
	var out [][2]int
	gs, ge, cd := 0, 0, 0
	closeGroup := func() {
		if cd > 0 {
			out = append(out, [2]int{gs, ge})
		}
		cd = 0
	}
	for i := start; i < end; {
		r, w := utf8.DecodeRuneInString(d[i:end])
		switch {
		case r >= '0' && r <= '9':
			if cd == 0 {
				gs = i
			}
			cd++
			ge = i + w
		case isHardSeparator(r):
			closeGroup()
		case isSoftSeparator(r):
			if cd >= 10 && cd <= maxPhoneDigits {
				closeGroup()
			}
		}
		i += w
	}
	closeGroup()
	return out
}

// isHardSeparator reports whether r always ends a phone.
func isHardSeparator(r rune) bool {
	switch r {
	case ',', ';', '/', '|', '\n', '\r', '\t', ' ', ' ':
		return true
	}
	return false
}

// isSoftSeparator reports whether r may end a phone, when the phone already has 10 to 13 digits.
func isSoftSeparator(r rune) bool {
	return unicode.IsSpace(r) || unicode.Is(unicode.Cf, r)
}

// isKeptFormat reports whether the whole candidate is a CEP, CPF or CNPJ.
func isKeptFormat(c string) bool {
	return exactCEP.MatchString(c) || exactCPF.MatchString(c) || exactCNPJ.MatchString(c)
}

// foldWithOffsets applies foldFullwidth rune by rune. It returns the folded
// text and, for every byte of it, the byte offset in s it came from. The last
// entry is len(s). Invalid UTF-8 bytes pass through unchanged.
func foldWithOffsets(s string) (string, []int) {
	var b strings.Builder
	b.Grow(len(s))
	orig := make([]int, 0, len(s)+1)
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && w == 1 {
			b.WriteByte(s[i])
			orig = append(orig, i)
			i++
			continue
		}
		f := foldRune(r)
		b.WriteRune(f)
		for k := 0; k < utf8.RuneLen(f); k++ {
			orig = append(orig, i)
		}
		i += w
	}
	orig = append(orig, len(s))
	return b.String(), orig
}

// foldRune is the per-rune form of foldFullwidth.
func foldRune(r rune) rune {
	switch {
	case r >= 0xFF01 && r <= 0xFF5E:
		return r - 0xFEE0
	case r == 0x3000:
		return ' '
	}
	return r
}

// isDigit reports whether r is an ASCII or fullwidth digit.
func isDigit(r rune) bool {
	f := foldRune(r)
	return f >= '0' && f <= '9'
}

// digitCount counts the ASCII and fullwidth digits of s.
func digitCount(s string) int {
	n := 0
	for _, r := range s {
		if isDigit(r) {
			n++
		}
	}
	return n
}

// isPosition reports whether r is a digit or a "*" of an earlier mask.
func isPosition(r rune) bool { return r == '*' || isDigit(r) }

// positions counts the digits and "*" of s.
func positions(s string) int {
	n := 0
	for _, r := range s {
		if isPosition(r) {
			n++
		}
	}
	return n
}

// maskPhone masks one phone group. A group of up to 13 digits is one phone.
// A longer group is masked with maskRuns.
func maskPhone(c string) string {
	if positions(c) > maxPhoneDigits {
		return maskRuns(c)
	}
	return maskDigits(c)
}

// maskDigits replaces the middle digits of one phone with "*". Other characters
// are kept. The first n-8 digits and the last two stay visible.
func maskDigits(c string) string {
	n := positions(c)
	visible := n - maskedDigits - keepAfterMask
	if visible < 0 {
		visible = 0
	}
	var b strings.Builder
	i := 0
	for _, r := range c {
		if !isPosition(r) {
			b.WriteRune(r)
			continue
		}
		if i < visible || i >= n-keepAfterMask {
			b.WriteRune(r)
		} else {
			b.WriteByte('*')
		}
		i++
	}
	return b.String()
}

// maskRuns is the fallback for a group of more than 13 digits that cannot be
// split. Every digit but the last two of the whole group becomes "*". Keeping
// short runs readable would leak a phone written in pairs, such as
// "55-11-98-76-54-32-10-99", and the result must not look like a phone when
// RedactText runs again.
func maskRuns(c string) string {
	n := positions(c)
	var b strings.Builder
	i := 0
	for _, r := range c {
		if !isPosition(r) {
			b.WriteRune(r)
			continue
		}
		if i >= n-keepAfterMask {
			b.WriteRune(r)
		} else {
			b.WriteByte('*')
		}
		i++
	}
	return b.String()
}
