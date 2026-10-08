package sendqueue

import (
	"context"
	"database/sql"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/toolerr"
	"github.com/riknog/whatsapp-mcp/internal/wa"

	_ "modernc.org/sqlite"
)

// TestIntegrationWithRealStore runs the queue on a real data.db: statuses, the
// card duplicate key (must match the store's own hash), audit content, and the
// expiry of leftovers after a restart.
func TestIntegrationWithRealStore(t *testing.T) {
	ctx := context.Background()
	clk := newTClock(testStart)
	path := filepath.Join(t.TempDir(), "data.db")
	st, err := store.Open(ctx, path, clk)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	fk := wa.NewFake(clk)
	fk.SetPhone(jidB, "+5511999998888")
	cfg := testConfig()
	q := New(cfg, st, fk, clk, rand.New(rand.NewSource(3)), nil)
	if err := q.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	stop := driveClock(clk)

	first := (&harness{t: t, q: q}).mustSendReal(txt(jidA, "ref-a", "oi, tudo bem?"))
	if first.MessageID == "" {
		t.Fatal("empty message id")
	}
	h := &harness{t: t, q: q}
	h.mustSendReal(card(jidA, "ref-a", jidB, "Fulana"))
	if _, err := h.submit(card(jidA, "ref-a", jidB, "Fulana")); err == nil {
		t.Fatal("duplicate card accepted")
	} else {
		requireCode(t, err, toolerr.CodeDuplicateMessage)
	}
	if _, err := h.submit(txt(jidA, "ref-a", "OI, TUDO BEM?")); err == nil {
		t.Fatal("duplicate text accepted")
	} else {
		requireCode(t, err, toolerr.CodeDuplicateMessage)
	}
	stop()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT status, wa_message_id FROM send_queue ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var statuses []string
	for rows.Next() {
		var s, id string
		if err := rows.Scan(&s, &id); err != nil {
			t.Fatal(err)
		}
		if s != store.StatusSent || id == "" {
			t.Errorf("row = %s/%q, want sent with id", s, id)
		}
		statuses = append(statuses, s)
	}
	rows.Close()
	if len(statuses) != 2 {
		t.Fatalf("rows = %v, want 2 sent", statuses)
	}

	audit := auditRows(t, db)
	for _, a := range audit {
		if strings.Contains(a, "tudo bem") || strings.Contains(a, "5511") || strings.Contains(a, "99998888") {
			t.Errorf("audit row leaks content: %q", a)
		}
	}
	joined := strings.Join(audit, "\n")
	for _, want := range []string{"send|ref-a|ok", "share_contact|ref-a|ok", "send|ref-a|rejected duplicate_message", "send|ref-a|rejected duplicate_message"} {
		if !strings.Contains(joined, want) {
			t.Errorf("audit missing %q in:\n%s", want, joined)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart: a queued item left by the old process must expire, not send.
	clk2 := newTClock(testStart.Add(time.Hour))
	st2, err := store.Open(ctx, path, clk2)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	if _, err := st2.EnqueueSend(ctx, store.SendItem{ChatJID: string(jidC), Kind: store.KindText, Text: "pendente"}); err != nil {
		t.Fatal(err)
	}
	fk2 := wa.NewFake(clk2)
	q2 := New(cfg, st2, fk2, clk2, rand.New(rand.NewSource(4)), nil)
	stop2 := driveClock(clk2)
	defer stop2()
	if err := q2.Start(ctx); err != nil {
		t.Fatalf("Start after restart: %v", err)
	}
	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	var status string
	if err := db2.QueryRow(`SELECT status FROM send_queue WHERE text = 'pendente'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != store.StatusExpired {
		t.Fatalf("leftover status = %s, want expired", status)
	}
	if n := len(fk2.Sent()); n != 0 {
		t.Fatalf("leftover was sent: %d", n)
	}
}

// mustSendReal submits through a harness-less queue and fails the test on error.
func (h *harness) mustSendReal(r Request) Result {
	h.t.Helper()
	res, err := h.submit(r)
	if err != nil {
		h.t.Fatalf("Submit: %v", err)
	}
	return res
}

func auditRows(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT action, chat_ref, detail FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a, r, d string
		if err := rows.Scan(&a, &r, &d); err != nil {
			t.Fatal(err)
		}
		out = append(out, a+"|"+r+"|"+d)
	}
	return out
}
