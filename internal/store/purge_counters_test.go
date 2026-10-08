package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
)

// A hidden chat must stay hidden through PurgeChat and PurgeAll, even when the
// same JID receives a message again afterwards.
func TestPurgeKeepsHiddenChat(t *testing.T) {
	ctx := context.Background()
	for name, purge := range map[string]func(*Store) error{
		"PurgeChat": func(s *Store) error { _, err := s.PurgeChat(ctx, jidH); return err },
		"PurgeAll":  func(s *Store) error { return s.PurgeAll(ctx) },
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newTestStore(t)
			mustChat(t, s, jidH, "direct")
			mustInsert(t, s, inbound(jidH, "h1", 10, "segredo"))
			if err := s.SetHidden(ctx, jidH, true); err != nil {
				t.Fatalf("SetHidden: %v", err)
			}
			if err := purge(s); err != nil {
				t.Fatalf("purge: %v", err)
			}
			if err := s.UpsertChat(ctx, Chat{JID: jidH, Ref: "c_" + jidH, Kind: "direct", LastMessageAt: 99}); err != nil {
				t.Fatalf("UpsertChat: %v", err)
			}
			c, err := s.GetChat(ctx, jidH)
			if err != nil || !c.Hidden || c.Ref != "c_"+jidH || c.Kind != "direct" {
				t.Fatalf("chat após purge+upsert = %+v, %v; quero oculto, ref e kind preservados", c, err)
			}
			if _, err := s.RecentMessages(ctx, jidH, 5, 0); !errors.Is(err, ErrHidden) {
				t.Errorf("leitura do oculto após purge: %v, quero ErrHidden", err)
			}
			if _, total, _ := s.Search(ctx, SearchFilter{Query: "segredo"}); total != 0 {
				t.Error("oculto apareceu na busca após purge")
			}
		})
	}
}

// PurgeAll resets chat state but keeps identity and visibility.
func TestPurgeAllResetsChatState(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "group")
	mustInsert(t, s, inbound(jidA, "a1", 100, "x"))
	_ = s.UpsertChat(ctx, Chat{JID: jidA, Ref: "c_" + jidA, Kind: "group", DisplayName: "Turma"})
	_ = s.AdvanceAgentCursor(ctx, jidA, pkOf(t, s, jidA, "a1"))
	_ = s.SetOwnerReadAt(ctx, jidA, 50)
	if err := s.PurgeAll(ctx); err != nil {
		t.Fatalf("PurgeAll: %v", err)
	}
	c, err := s.GetChat(ctx, jidA)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.LastMessageAt != 0 || c.AgentCursor != 0 || c.OwnerReadAt != 0 || c.DisplayName != "" {
		t.Errorf("estado não zerado: %+v", c)
	}
	if c.Ref != "c_"+jidA || c.Kind != "group" || c.Hidden {
		t.Errorf("identidade alterada: %+v", c)
	}
}

// CountSendsSince and DistinctSendRecipientsSince count queued, sending and sent
// items by enqueued_at, and nothing else.
func TestSendCountersForRateLimit(t *testing.T) {
	s, clk := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustChat(t, s, jidB, "direct")
	since := clk.Now()

	q, _ := s.EnqueueSend(ctx, SendItem{ChatJID: jidA, Kind: KindText, Text: "fila"})
	sd, _ := s.EnqueueSend(ctx, SendItem{ChatJID: jidB, Kind: KindText, Text: "em voo"})
	_ = s.MarkSending(ctx, sd)
	ok, _ := s.EnqueueSend(ctx, SendItem{ChatJID: jidB, Kind: KindText, Text: "enviada"})
	_ = s.MarkSending(ctx, ok)
	_ = s.MarkSent(ctx, ok, "W1")
	fl, _ := s.EnqueueSend(ctx, SendItem{ChatJID: jidA, Kind: KindText, Text: "falhou"})
	_ = s.MarkFailed(ctx, fl, "rede")
	_ = q

	if n, err := s.CountSendsSince(ctx, since); err != nil || n != 3 {
		t.Fatalf("CountSendsSince = %d, %v; quero 3 (queued+sending+sent, sem failed)", n, err)
	}
	if n, err := s.DistinctSendRecipientsSince(ctx, since); err != nil || n != 2 {
		t.Fatalf("DistinctSendRecipientsSince = %d, %v; quero 2", n, err)
	}
	if n, _ := s.CountSendsSince(ctx, clk.Now().Add(time.Second)); n != 0 {
		t.Errorf("CountSendsSince após a janela = %d", n)
	}
	// Expired items do not count either.
	if _, err := s.ExpireStale(ctx); err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	if n, _ := s.CountSendsSince(ctx, since); n != 1 {
		t.Errorf("após expirar, CountSendsSince = %d, quero 1 (só o enviado)", n)
	}
}

// The -wal and -shm files exist while the store holds connections. Their mode is
// checked unconditionally, with a read still open on the WAL.
func TestFilePermissionsWithOpenWAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	s, err := Open(ctx, path, clock.NewFake(testStart))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	mustChat(t, s, jidA, "direct")
	mustInsert(t, s, inbound(jidA, "a1", 1, "x"))

	rows, err := s.r.QueryContext(ctx, `SELECT id FROM messages`)
	if err != nil {
		t.Fatalf("leitura aberta: %v", err)
	}
	defer rows.Close()

	assertMode(t, path, 0o600)
	assertMode(t, path+"-wal", 0o600)
	assertMode(t, path+"-shm", 0o600)
}

// Concurrent writers, readers, queue operations and periodic Compact. Nothing
// may fail with "database is locked", and the counts must add up. Run with -race.
func TestConcurrentQueueAndCompact(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const workers, rounds = 8, 20
	mustChat(t, s, jidA, "direct")

	var wg sync.WaitGroup
	errs := make(chan error, workers*rounds*6)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			chat := fmt.Sprintf("c%02d@s.whatsapp.net", w)
			if err := s.UpsertChat(ctx, Chat{JID: chat, Ref: "c_" + chat, Kind: "direct"}); err != nil {
				errs <- err
				return
			}
			for i := 0; i < rounds; i++ {
				id := fmt.Sprintf("w%02d-%03d", w, i)
				if _, err := s.InsertMessage(ctx, Message{ChatJID: chat, ID: id, TS: int64(i + 1), Kind: "text", Text: "carga concorrente"}); err != nil {
					errs <- fmt.Errorf("insert: %w", err)
				}
				qid, err := s.EnqueueSend(ctx, SendItem{ChatJID: chat, Kind: KindText, Text: "envio " + id})
				if err != nil {
					errs <- fmt.Errorf("enqueue: %w", err)
					continue
				}
				if err := s.MarkSending(ctx, qid); err != nil {
					errs <- fmt.Errorf("sending: %w", err)
				}
				if err := s.MarkSent(ctx, qid, "W"+id); err != nil {
					errs <- fmt.Errorf("sent: %w", err)
				}
				if _, err := s.CountSendsSince(ctx, testStart); err != nil {
					errs <- fmt.Errorf("count: %w", err)
				}
				if _, err := s.RecentMessages(ctx, chat, 5, 0); err != nil {
					errs <- fmt.Errorf("recent: %w", err)
				}
				if _, _, err := s.Search(ctx, SearchFilter{Query: "concorrente", Limit: 5}); err != nil {
					errs <- fmt.Errorf("search: %w", err)
				}
				if w == 0 && i%5 == 4 {
					if err := s.Compact(ctx); err != nil {
						errs <- fmt.Errorf("compact: %w", err)
					}
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && strings.Contains(err.Error(), "locked") {
			t.Errorf("database is locked: %v", err)
		} else if err != nil {
			t.Errorf("erro concorrente: %v", err)
		}
	}
	if n, _ := s.CountSendsSince(ctx, testStart); n != workers*rounds {
		t.Errorf("envios contados = %d, quero %d", n, workers*rounds)
	}
	if n, _ := s.CountMessages(ctx, "c00@s.whatsapp.net"); n != rounds {
		t.Errorf("mensagens de c00 = %d, quero %d", n, rounds)
	}
}
