package sendqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/riknog/whatsapp-mcp/internal/config"
)

// contactKey is the duplicate key of a card: the shared JID. It must equal the
// hash that store.EnqueueSend computes for a contact item (sha256 of
// "contact:"+jid), or duplicate checks would miss. The integration test checks this.
func contactKey(sharedJID string) string {
	sum := sha256.Sum256([]byte("contact:" + sharedJID))
	return hex.EncodeToString(sum[:])
}

func runeCount(s string) int { return utf8.RuneCountInString(s) }

// cleanName removes control characters and trims, and caps the length.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > 100 {
		s = string([]rune(s)[:100])
	}
	return s
}

// buildVCard writes a WhatsApp-style contact card with one mobile number.
// The phone never goes anywhere else; the caller must not log the result.
func buildVCard(name, phone string) string {
	digits := make([]byte, 0, len(phone))
	for i := 0; i < len(phone); i++ {
		if phone[i] >= '0' && phone[i] <= '9' {
			digits = append(digits, phone[i])
		}
	}
	esc := strings.NewReplacer(`\`, `\\`, `;`, `\;`, `,`, `\,`).Replace(name)
	return fmt.Sprintf("BEGIN:VCARD\nVERSION:3.0\nN:;%s;;;\nFN:%s\nTEL;type=CELL;type=VOICE;waid=%s:+%s\nEND:VCARD",
		esc, esc, digits, digits)
}

// typingDelay is the composing time before a send: the nominal typing time
// times a jitter factor in [0.8, 1.2] (docs/03 §2.2). It uses q.rnd, so the
// worker goroutine must call it.
func (q *Queue) typingDelay(runes int) time.Duration {
	jitter := 0.8 + 0.4*q.rnd.Float64()
	return time.Duration(float64(typingNominal(runes)) * jitter)
}

// uniform draws a duration in [r.Min, r.Max] milliseconds.
func (q *Queue) uniform(r config.Range) time.Duration {
	return time.Duration(r.Min+q.rnd.Intn(r.Max-r.Min+1)) * time.Millisecond
}
