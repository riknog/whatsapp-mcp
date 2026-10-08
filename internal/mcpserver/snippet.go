package mcpserver

import (
	"strings"
	"unicode"

	"github.com/riknog/whatsapp-mcp/internal/privacy"
	"github.com/riknog/whatsapp-mcp/internal/store"
)

// snippetContext is how many words are kept on each side of the match when the
// snippet is built here instead of by FTS.
const snippetContext = 6

// safeSnippet returns the search snippet for a hit. The FTS snippet cuts the
// text by tokens, so a phone number at the edge of its window can come out as a
// short digit run that redaction does not recognise. When the message holds a
// phone number, the snippet is rebuilt from the already redacted text instead.
func safeSnippet(h store.SearchHit, query string) string {
	full := strings.TrimSpace(h.Message.Text + " " + h.Message.Caption)
	redacted := privacy.RedactText(full)
	if redacted == full {
		return privacy.RedactText(h.Snippet)
	}
	return windowSnippet(redacted, query)
}

// windowSnippet marks the first word that starts with a query term and keeps
// snippetContext words on each side.
func windowSnippet(text, query string) string {
	words := strings.Fields(text)
	terms := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	hit := -1
	for i, w := range words {
		lw := strings.ToLower(strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))
		for _, t := range terms {
			if t != "" && strings.HasPrefix(lw, t) {
				hit = i
				break
			}
		}
		if hit >= 0 {
			break
		}
	}
	if hit < 0 {
		hit = 0
	}
	from, to := max(0, hit-snippetContext), min(len(words), hit+snippetContext+1)
	part := append([]string(nil), words[from:to]...)
	if hit < len(words) && len(part) > 0 {
		part[hit-from] = "«" + part[hit-from] + "»"
	}
	s := strings.Join(part, " ")
	if from > 0 {
		s = "…" + s
	}
	if to < len(words) {
		s += "…"
	}
	return s
}
