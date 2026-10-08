package privacy

import (
	"regexp"
	"strings"
)

// Placeholders used by RedactLog for e-mail addresses and documents.
const (
	emailPlaceholder = "<email>"
	docPlaceholder   = "<doc>"
)

// emailPattern matches an e-mail address: a local part, "@", and a domain with
// at least one dot and a top-level part of two or more letters. A "*" is not a
// local-part character, so a masked address ("j***@x.com") never matches again.
var emailPattern = regexp.MustCompile(
	`[A-Za-z0-9._%+\-]+@[A-Za-z0-9](?:[A-Za-z0-9\-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9\-]*[A-Za-z0-9])?)*\.[A-Za-z]{2,}`)

// docCandidate matches a run of digits joined by ".", "/" or "-". A CPF or CNPJ
// is judged on the whole run, so a document inside a longer number is not one.
var docCandidate = regexp.MustCompile(`[0-9][0-9./\-]*[0-9]`)

// Shapes of a CPF and a CNPJ with optional punctuation, and the fully formatted
// forms. A fully formatted run is a document whatever its check digits say; a
// run with missing punctuation needs valid check digits.
var (
	looseCPF  = regexp.MustCompile(`^\d{3}\.?\d{3}\.?\d{3}-?\d{2}$`)
	looseCNPJ = regexp.MustCompile(`^\d{2}\.?\d{3}\.?\d{3}/?\d{4}-?\d{2}$`)
)

// Document kinds found by findDocuments.
const (
	docCPF  = "cpf"
	docCNPJ = "cnpj"
)

// RedactDocuments masks e-mail addresses, CPFs and CNPJs. An address keeps the
// first character of its local part and its domain ("joao@x.com" becomes
// "j***@x.com"). A CPF becomes "***.***.***-09" and a CNPJ "**.***.***/****-95":
// only the two check digits stay. Applying it twice gives the same result.
func RedactDocuments(s string) string {
	s = emailPattern.ReplaceAllStringFunc(s, maskEmail)
	return replaceDocuments(s, func(kind, digits string) string {
		last := digits[len(digits)-keepAfterMask:]
		if kind == docCPF {
			return "***.***.***-" + last
		}
		return "**.***.***/****-" + last
	})
}

// redactDocumentsLog replaces e-mail addresses with "<email>" and documents
// with "<doc>". The input is already folded to ASCII.
func redactDocumentsLog(s string) string {
	s = emailPattern.ReplaceAllString(s, emailPlaceholder)
	return replaceDocuments(s, func(string, string) string { return docPlaceholder })
}

// hasReadableDocument reports whether s has an e-mail address, a CPF or a CNPJ
// that RedactDocuments would mask.
func hasReadableDocument(s string) bool {
	return RedactDocuments(s) != s
}

// maskEmail keeps the first character of the local part and the domain.
func maskEmail(addr string) string {
	at := strings.LastIndexByte(addr, '@')
	if at <= 0 {
		return addr
	}
	return addr[:1] + "***" + addr[at:]
}

// replaceDocuments calls repl for every CPF or CNPJ of s and puts its result in
// place of the document. Detection runs on the folded text, so fullwidth digits
// count, and the replacement covers the original characters.
func replaceDocuments(s string, repl func(kind, digits string) string) string {
	folded, orig := foldWithOffsets(s)
	var b strings.Builder
	last := 0
	for _, loc := range docCandidate.FindAllStringIndex(folded, -1) {
		kind, digits := classifyDocument(folded[loc[0]:loc[1]])
		if kind == "" {
			continue
		}
		from, to := orig[loc[0]], orig[loc[1]]
		b.WriteString(s[last:from])
		b.WriteString(repl(kind, digits))
		last = to
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// classifyDocument returns the kind and the digits of a candidate run, or ""
// when the run is not a CPF or a CNPJ.
func classifyDocument(c string) (kind, digits string) {
	digits = onlyDigits(c)
	switch {
	case looseCPF.MatchString(c):
		if exactCPF.MatchString(c) || validCPF(digits) {
			return docCPF, digits
		}
	case looseCNPJ.MatchString(c):
		if exactCNPJ.MatchString(c) || validCNPJ(digits) {
			return docCNPJ, digits
		}
	}
	return "", ""
}

func onlyDigits(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if isASCIIDigit(s[i]) {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// validCPF checks the two check digits of an 11-digit CPF. Runs of a single
// repeated digit pass the arithmetic but are not issued, so they are rejected.
func validCPF(d string) bool {
	if len(d) != 11 || allSame(d) {
		return false
	}
	return checkDigit(d[:9], 10) == d[9] && checkDigit(d[:10], 11) == d[10]
}

// checkDigit is the CPF check digit of d, with weights from first down to 2.
func checkDigit(d string, first int) byte {
	sum := 0
	for i := 0; i < len(d); i++ {
		sum += int(d[i]-'0') * (first - i)
	}
	r := sum * 10 % 11
	if r == 10 {
		r = 0
	}
	return byte('0' + r)
}

// validCNPJ checks the two check digits of a 14-digit CNPJ.
func validCNPJ(d string) bool {
	if len(d) != 14 || allSame(d) {
		return false
	}
	return cnpjDigit(d[:12]) == d[12] && cnpjDigit(d[:13]) == d[13]
}

// cnpjDigit is the CNPJ check digit of d: weights 2..9 from the right, cycling.
func cnpjDigit(d string) byte {
	sum, w := 0, 2
	for i := len(d) - 1; i >= 0; i-- {
		sum += int(d[i]-'0') * w
		w++
		if w > 9 {
			w = 2
		}
	}
	r := sum % 11
	if r < 2 {
		return '0'
	}
	return byte('0' + 11 - r)
}

func allSame(s string) bool {
	return strings.Count(s, s[:1]) == len(s)
}
