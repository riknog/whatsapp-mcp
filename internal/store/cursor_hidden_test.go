package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A message with an old timestamp that is ingested after the agent cursor has
// advanced must still be delivered: the cursor is a pk, not a time.
func TestLateOldTimestampMessageIsNew(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustInsert(t, s, inbound(jidA, "new1", 1000, "chegou primeiro"))

	res, _ := s.NewInbound(ctx, InboundFilter{})
	if len(res.Chats) != 1 {
		t.Fatalf("esperava 1 chat novo, veio %d", len(res.Chats))
	}
	if err := s.AdvanceAgentCursor(ctx, jidA, res.Chats[0].LatestPK); err != nil {
		t.Fatalf("AdvanceAgentCursor: %v", err)
	}
	if after, _ := s.NewInbound(ctx, InboundFilter{}); len(after.Chats) != 0 {
		t.Fatal("mensagem já consumida reapareceu")
	}

	// Ingested later, but its timestamp is older than the one already consumed.
	mustInsert(t, s, inbound(jidA, "late", 500, "chegou depois, ts antigo"))
	again, _ := s.NewInbound(ctx, InboundFilter{})
	if len(again.Chats) != 1 || len(again.Chats[0].Messages) != 1 || again.Chats[0].Messages[0].ID != "late" {
		t.Fatalf("mensagem atrasada com ts antigo não apareceu como nova: %+v", again.Chats)
	}
}

func TestAgentCursorNeverMovesBackwardsAndOwnerReadStillApplies(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustInsert(t, s, inbound(jidA, "a1", 100, "x"))
	mustInsert(t, s, inbound(jidA, "a2", 101, "y"))
	pk2 := pkOf(t, s, jidA, "a2")
	_ = s.AdvanceAgentCursor(ctx, jidA, pk2)
	_ = s.AdvanceAgentCursor(ctx, jidA, pkOf(t, s, jidA, "a1"))
	if c, _ := s.GetChat(ctx, jidA); c.AgentCursor != pk2 {
		t.Fatalf("cursor retrocedeu: %d, quero %d", c.AgentCursor, pk2)
	}
	// Owner read at ts 150: a message ingested after the cursor but already read
	// on the phone is not new.
	_ = s.SetOwnerReadAt(ctx, jidA, 150)
	mustInsert(t, s, inbound(jidA, "a3", 120, "lido no celular"))
	if res, _ := s.NewInbound(ctx, InboundFilter{}); len(res.Chats) != 0 {
		t.Fatalf("mensagem lida pelo dono apareceu: %+v", res.Chats)
	}
}

func TestHiddenChatReadsReturnErrHidden(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidH, "direct")
	mustInsert(t, s, inbound(jidH, "h1", 10, "segredo"))
	if err := s.SetHidden(ctx, jidH, true); err != nil {
		t.Fatalf("SetHidden: %v", err)
	}
	if _, err := s.RecentMessages(ctx, jidH, 5, 0); !errors.Is(err, ErrHidden) {
		t.Errorf("RecentMessages: %v, quero ErrHidden", err)
	}
	if _, err := s.MessagesAround(ctx, jidH, "h1", 5); !errors.Is(err, ErrHidden) {
		t.Errorf("MessagesAround: %v, quero ErrHidden", err)
	}
	if _, err := s.CountMessages(ctx, jidH); !errors.Is(err, ErrHidden) {
		t.Errorf("CountMessages: %v, quero ErrHidden", err)
	}
	// Visible again: reads work.
	_ = s.SetHidden(ctx, jidH, false)
	if got, err := s.RecentMessages(ctx, jidH, 5, 0); err != nil || len(got) != 1 {
		t.Errorf("chat visível: %d msgs, %v", len(got), err)
	}
	// Unknown chat is not visible either: ErrNotFound, from every chat-scoped read.
	if _, err := s.RecentMessages(ctx, "nada@s.whatsapp.net", 5, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("RecentMessages chat desconhecido: %v, quero ErrNotFound", err)
	}
	if _, err := s.MessagesAround(ctx, "nada@s.whatsapp.net", "x", 5); !errors.Is(err, ErrNotFound) {
		t.Errorf("MessagesAround chat desconhecido: %v, quero ErrNotFound", err)
	}
	if _, err := s.CountMessages(ctx, "nada@s.whatsapp.net"); !errors.Is(err, ErrNotFound) {
		t.Errorf("CountMessages chat desconhecido: %v, quero ErrNotFound", err)
	}
}

// VACUUM renumbers rowids of tables without INTEGER PRIMARY KEY. messages.pk is
// one, so the FTS external-content index must still match after VACUUM.
func TestVacuumKeepsPKAndFTSConsistent(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustChat(t, s, jidB, "direct")
	mustInsert(t, s, inbound(jidA, "a1", 1, "primeiro alfa"))
	mustInsert(t, s, inbound(jidB, "b1", 2, "segundo beta"))
	mustInsert(t, s, inbound(jidA, "a2", 3, "terceiro alfa"))
	if _, err := s.PurgeChat(ctx, jidB); err != nil { // leaves a gap in the rows
		t.Fatalf("PurgeChat: %v", err)
	}
	pkBefore := pkOf(t, s, jidA, "a2")

	if _, err := s.w.ExecContext(ctx, `VACUUM`); err != nil {
		t.Fatalf("VACUUM: %v", err)
	}
	if pkAfter := pkOf(t, s, jidA, "a2"); pkAfter != pkBefore {
		t.Fatalf("pk mudou com VACUUM: %d -> %d", pkBefore, pkAfter)
	}
	hits, total, err := s.Search(ctx, SearchFilter{Query: "alfa"})
	if err != nil || total != 2 || len(hits) != 2 {
		t.Fatalf("busca após VACUUM: total %d, %v; quero 2", total, err)
	}
	if hits[0].Message.ID != "a2" || hits[1].Message.ID != "a1" {
		t.Errorf("ordem após VACUUM: %s, %s", hits[0].Message.ID, hits[1].Message.ID)
	}
	if _, err := s.w.ExecContext(ctx, `INSERT INTO messages_fts (messages_fts) VALUES ('integrity-check')`); err != nil {
		t.Fatalf("integrity-check após VACUUM: %v", err)
	}
}

// After PurgeChat and Compact, the purged text must not be present in the
// database file, byte for byte. The sanity check first proves the text is there
// before the purge, so the test cannot pass by accident.
func TestCompactRemovesPurgedBytesFromFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	s, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	const secret = "SEGREDO-QUE-DEVE-SUMIR-7x9q"
	mustChat(t, s, jidA, "direct")
	mustInsert(t, s, inbound(jidA, "S1", 10, "frase "+secret+" fim"))
	if _, err := s.w.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if !fileHas(t, path, secret) {
		t.Fatal("sanidade: o texto deveria estar no arquivo antes do expurgo")
	}

	if _, err := s.PurgeChat(ctx, jidA); err != nil {
		t.Fatalf("PurgeChat: %v", err)
	}
	if err := s.Compact(ctx); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if fileHas(t, path, secret) {
		t.Fatal("texto expurgado ainda presente no arquivo .db após Compact")
	}
	if fileHas(t, path+"-wal", secret) {
		t.Fatal("texto expurgado ainda presente no WAL após Compact")
	}
}

func TestCompactOnFreshStore(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
}

// fileHas reports whether the file contains needle. A missing file counts as no.
func fileHas(t *testing.T, path, needle string) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatalf("ler %s: %v", path, err)
	}
	return bytes.Contains(b, []byte(needle))
}
