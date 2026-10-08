package sendqueue

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/wa"
)

// TestContactNumberNeverLeaves sends a card through the real store and checks
// that the phone number is absent from logs, audit rows, every send_queue
// column and the Result. The number may appear only in the vCard handed to the
// WhatsApp client. The JID of the shared contact is stored in shared_jid by
// design; it must still stay out of logs, audit and results.
func TestContactNumberNeverLeaves(t *testing.T) {
	ctx := context.Background()
	clk := newTClock(testStart)
	path := filepath.Join(t.TempDir(), "data.db")
	st, err := store.Open(ctx, path, clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	fk := wa.NewFake(clk)
	const phone = "+55 11 99999-8888"
	const digits = "5511999998888"
	const formatted = "99999-8888"
	fk.SetPhone(jidB, phone)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	q := New(testConfig(), st, fk, clk, rand.New(rand.NewSource(5)), logger)
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(driveClock(clk))

	// Exercise the log paths too: a presence failure and a network retry on the card.
	fk.SetPresenceError(fmt.Errorf("presença recusada"))
	fk.FailSendContact(wa.ErrNetwork, nil)
	resCard, err := q.Submit(ctx, card(jidA, "ref-a", jidB, "Fulana"))
	if err != nil {
		t.Fatalf("card: %v", err)
	}
	fk.SetPresenceError(nil)
	resText, err := q.Submit(ctx, txt(jidA, "ref-a", "oi"))
	if err != nil {
		t.Fatalf("text: %v", err)
	}
	if _, err := q.Submit(ctx, card(jidA, "ref-a", jidB, "Fulana")); err == nil {
		t.Fatal("duplicate card accepted")
	}

	cards := fk.Contacts()
	if len(cards) != 1 || !strings.Contains(cards[0].VCard, digits) {
		t.Fatalf("sanity: the vCard handed to the client must carry the number")
	}
	if logBuf.Len() == 0 {
		t.Fatal("sanity: nothing was logged")
	}

	forbidden := []string{digits, formatted, string(jidB), string(jidA)}
	for _, f := range forbidden {
		if strings.Contains(logBuf.String(), f) {
			t.Errorf("log contains %q:\n%s", f, logBuf.String())
		}
	}

	res := fmt.Sprintf("%+v %+v", resCard, resText)
	for _, f := range forbidden {
		if strings.Contains(res, f) {
			t.Errorf("Result contains %q: %s", f, res)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT chat_jid, kind, text, shared_jid, quoted_id, text_hash, status, wa_message_id, error FROM send_queue`)
	if err != nil {
		t.Fatal(err)
	}
	var dump strings.Builder
	for rows.Next() {
		var cols [9]string
		ptrs := make([]any, len(cols))
		for i := range cols {
			ptrs[i] = &cols[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		dump.WriteString(strings.Join(cols[:], "|") + "\n")
	}
	rows.Close()
	if strings.Contains(dump.String(), digits) || strings.Contains(dump.String(), formatted) {
		t.Errorf("send_queue holds the phone number:\n%s", dump.String())
	}

	audit := auditRows(t, db)
	for _, a := range audit {
		for _, f := range forbidden {
			if strings.Contains(a, f) {
				t.Errorf("audit row contains %q: %s", f, a)
			}
		}
	}
	if len(audit) == 0 {
		t.Error("sanity: no audit rows")
	}
}
