package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

const (
	jidA = "5511900000001@s.whatsapp.net"
	jidB = "5511900000002@s.whatsapp.net"
	jidG = "120363000000001@g.us"
	jidH = "5511900000003@s.whatsapp.net"
)

func mustChat(t *testing.T, s *Store, jid, kind string) {
	t.Helper()
	if err := s.UpsertChat(context.Background(), Chat{JID: jid, Ref: "c_" + jid, Kind: kind}); err != nil {
		t.Fatalf("UpsertChat(%s): %v", kind, err)
	}
}

func mustInsert(t *testing.T, s *Store, m Message) {
	t.Helper()
	if _, err := s.InsertMessage(context.Background(), m); err != nil {
		t.Fatalf("InsertMessage(%s): %v", m.ID, err)
	}
}

// pkOf returns the ingestion sequence (messages.pk) of a message.
func pkOf(t *testing.T, s *Store, chat, id string) int64 {
	t.Helper()
	var pk int64
	if err := s.r.QueryRowContext(context.Background(),
		`SELECT pk FROM messages WHERE chat_jid = ? AND id = ?`, chat, id).Scan(&pk); err != nil {
		t.Fatalf("pkOf(%s): %v", id, err)
	}
	return pk
}

func inbound(chat, id string, ts int64, text string) Message {
	return Message{ChatJID: chat, ID: id, TS: ts, Kind: "text", Text: text}
}

func outbound(chat, id string, ts int64, text string) Message {
	m := inbound(chat, id, ts, text)
	m.FromMe = true
	return m
}

func TestInsertMessageIsIdempotentAndFTSStaysClean(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")

	m := inbound(jidA, "M1", 100, "encontro amanhã")
	ok, err := s.InsertMessage(ctx, m)
	if err != nil || !ok {
		t.Fatalf("primeira inserção = %v, %v", ok, err)
	}
	ok, err = s.InsertMessage(ctx, m)
	if err != nil || ok {
		t.Fatalf("duplicata = %v, %v; quero false, nil", ok, err)
	}
	m.Text = "outro texto que não deve substituir"
	if ok, err := s.InsertMessage(ctx, m); err != nil || ok {
		t.Fatalf("duplicata com texto diferente = %v, %v", ok, err)
	}

	if n, _ := s.CountMessages(ctx, jidA); n != 1 {
		t.Fatalf("CountMessages = %d, quero 1", n)
	}
	hits, total, err := s.Search(ctx, SearchFilter{Query: "encontro"})
	if err != nil || total != 1 || len(hits) != 1 {
		t.Fatalf("Search = %d hits, total %d, %v; quero 1", len(hits), total, err)
	}
	if _, _, err := s.Search(ctx, SearchFilter{Query: "substituir"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if _, total, _ := s.Search(ctx, SearchFilter{Query: "substituir"}); total != 0 {
		t.Fatal("duplicata alterou o FTS")
	}
	if _, err := s.w.ExecContext(ctx, `INSERT INTO messages_fts (messages_fts) VALUES ('integrity-check')`); err != nil {
		t.Fatalf("integrity-check do FTS: %v", err)
	}
}

func TestInsertMessageUpdatesChatLastMessage(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustInsert(t, s, inbound(jidA, "M1", 500, "oi"))
	mustInsert(t, s, inbound(jidA, "M0", 300, "antes"))
	c, err := s.GetChat(ctx, jidA)
	if err != nil || c.LastMessageAt != 500 {
		t.Fatalf("LastMessageAt = %d, %v; quero 500", c.LastMessageAt, err)
	}
}

func TestInsertMessageRejectsIncomplete(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.InsertMessage(context.Background(), Message{ChatJID: jidA, ID: "x"}); err == nil {
		t.Fatal("mensagem sem tipo aceita")
	}
}

func TestUpdateAndDeleteKeepFTSInSync(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustInsert(t, s, inbound(jidA, "M1", 100, "palavraantiga"))

	if _, err := s.w.ExecContext(ctx, `UPDATE messages SET text = 'palavranova' WHERE id = 'M1'`); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, total, _ := s.Search(ctx, SearchFilter{Query: "palavraantiga"}); total != 0 {
		t.Error("termo antigo ainda encontrado após UPDATE")
	}
	if _, total, _ := s.Search(ctx, SearchFilter{Query: "palavranova"}); total != 1 {
		t.Error("termo novo não encontrado após UPDATE")
	}

	if _, err := s.w.ExecContext(ctx, `DELETE FROM messages WHERE id = 'M1'`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, total, _ := s.Search(ctx, SearchFilter{Query: "palavranova"}); total != 0 {
		t.Error("mensagem apagada ainda aparece na busca")
	}
	if _, err := s.w.ExecContext(ctx, `INSERT INTO messages_fts (messages_fts) VALUES ('integrity-check')`); err != nil {
		t.Fatalf("integrity-check: %v", err)
	}
}

func TestSearchAccentInsensitiveAndSanitized(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustInsert(t, s, inbound(jidA, "M1", 100, "Meu coração partido"))
	mustInsert(t, s, inbound(jidA, "M2", 110, "bolo de cenoura"))

	hits, total, err := s.Search(ctx, SearchFilter{Query: "coracao"})
	if err != nil || total != 1 {
		t.Fatalf("coracao: total %d, %v; quero 1", total, err)
	}
	if !strings.Contains(hits[0].Snippet, "«") {
		t.Errorf("snippet sem destaque: %q", hits[0].Snippet)
	}

	hostile := []string{
		`OR`, `NEAR(`, `NEAR(a b)`, `a"b`, `"coracao`, `coracao OR bolo`,
		`^x`, `col:x`, "a\x00b", `"coracao"`, `bolo*`, `texto:"`, `coracao !!!`,
	}
	for _, q := range hostile {
		if _, _, err := s.Search(ctx, SearchFilter{Query: q}); err != nil {
			t.Errorf("Search(%q) retornou erro: %v", q, err)
		}
	}
	if _, total, _ := s.Search(ctx, SearchFilter{Query: `coracao OR bolo`}); total != 0 {
		t.Errorf("OR virou operador: total %d", total)
	}
	// Tokens without letters or digits are dropped: "coracao !!!" searches for coracao.
	if _, total, _ := s.Search(ctx, SearchFilter{Query: `coracao !!!`}); total != 1 {
		t.Errorf("Search(\"coracao !!!\") total %d, quero 1 (igual a \"coracao\")", total)
	}
	// Punctuation only: nothing left to search, so invalid_argument.
	for _, q := range []string{`"`, `""`, `*`, `-`, `(`, `)`, `!!!`} {
		_, _, err := s.Search(ctx, SearchFilter{Query: q})
		var te toolerr.Error
		if !errors.As(err, &te) || te.Code != toolerr.CodeInvalidArgument {
			t.Errorf("Search(%q) = %v, quero invalid_argument", q, err)
		}
	}
	if _, total, _ := s.Search(ctx, SearchFilter{Query: `coracao bolo`}); total != 0 {
		t.Errorf("AND implícito: total %d, quero 0", total)
	}
	if _, total, _ := s.Search(ctx, SearchFilter{Query: `bolo cenoura`}); total != 1 {
		t.Errorf("AND de dois termos: total %d, quero 1", total)
	}
}

func TestSearchEmptyQueryIsInvalidArgument(t *testing.T) {
	s, _ := newTestStore(t)
	for _, q := range []string{"", "   \t"} {
		_, _, err := s.Search(context.Background(), SearchFilter{Query: q})
		var te toolerr.Error
		if !errors.As(err, &te) || te.Code != toolerr.CodeInvalidArgument {
			t.Errorf("Search(%q) = %v, quero invalid_argument", q, err)
		}
	}
}

func TestSearchFiltersChatHiddenAndPaging(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustChat(t, s, jidH, "direct")
	for i := 0; i < 12; i++ {
		mustInsert(t, s, inbound(jidA, fmt.Sprintf("A%02d", i), int64(100+i), "compromisso"))
	}
	mustInsert(t, s, inbound(jidH, "H1", 500, "compromisso secreto"))
	if err := s.SetHidden(ctx, jidH, true); err != nil {
		t.Fatalf("SetHidden: %v", err)
	}

	hits, total, err := s.Search(ctx, SearchFilter{Query: "compromisso"})
	if err != nil || total != 12 {
		t.Fatalf("total = %d, %v; quero 12 (oculto fora)", total, err)
	}
	if len(hits) != 10 || hits[0].Message.ID != "A11" {
		t.Fatalf("página 1: %d hits, primeiro %q", len(hits), hits[0].Message.ID)
	}
	hits, _, _ = s.Search(ctx, SearchFilter{Query: "compromisso", Limit: 5, Offset: 10})
	if len(hits) != 2 {
		t.Fatalf("offset 10 retornou %d, quero 2", len(hits))
	}
	hits, total, _ = s.Search(ctx, SearchFilter{Query: "compromisso", ChatJID: jidH})
	if total != 0 || len(hits) != 0 {
		t.Fatal("chat oculto apareceu com filtro de chat")
	}
}

func TestRecentMessagesPagingIsChronological(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	for i := 1; i <= 25; i++ {
		mustInsert(t, s, inbound(jidA, fmt.Sprintf("m%02d", i), int64(i), fmt.Sprintf("msg %d", i)))
	}

	got, err := s.RecentMessages(ctx, jidA, 10, 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("len = %d, quero 10", len(got))
	}
	for i, m := range got {
		if want := int64(6 + i); m.TS != want {
			t.Fatalf("posição %d: ts %d, quero %d (11–20 mais recentes, em ordem)", i, m.TS, want)
		}
	}

	latest, _ := s.RecentMessages(ctx, jidA, 3, 0)
	if len(latest) != 3 || latest[2].TS != 25 || latest[0].TS != 23 {
		t.Fatalf("últimas 3 = %+v", latest)
	}
	def, _ := s.RecentMessages(ctx, jidA, 0, 0)
	if len(def) != 20 {
		t.Errorf("limit default = %d, quero 20", len(def))
	}
	big, _ := s.RecentMessages(ctx, jidA, 999, 0)
	if len(big) != 25 {
		t.Errorf("limit acima do máximo: %d, quero 25 (cap 50)", len(big))
	}
	past, _ := s.RecentMessages(ctx, jidA, 5, 100)
	if len(past) != 0 {
		t.Errorf("offset além do fim retornou %d", len(past))
	}
	if _, err := s.RecentMessages(ctx, jidA, 5, -3); err != nil {
		t.Errorf("offset negativo: %v", err)
	}
}

func TestMessagesAround(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	for i := 1; i <= 20; i++ {
		mustInsert(t, s, inbound(jidA, fmt.Sprintf("m%02d", i), int64(i), "x"))
	}
	got, err := s.MessagesAround(ctx, jidA, "m10", 5)
	if err != nil {
		t.Fatalf("MessagesAround: %v", err)
	}
	var ts []int64
	for _, m := range got {
		ts = append(ts, m.TS)
	}
	if fmt.Sprint(ts) != "[8 9 10 11 12]" {
		t.Fatalf("janela = %v, quero [8 9 10 11 12]", ts)
	}
	start, _ := s.MessagesAround(ctx, jidA, "m01", 5)
	if len(start) != 5 || start[0].TS != 1 {
		t.Errorf("janela no início = %d msgs", len(start))
	}
	end, _ := s.MessagesAround(ctx, jidA, "m20", 4)
	if len(end) != 4 || end[len(end)-1].TS != 20 {
		t.Errorf("janela no fim = %d msgs", len(end))
	}
	if _, err := s.MessagesAround(ctx, jidA, "nope", 5); !errors.Is(err, ErrNotFound) {
		t.Errorf("id inexistente: %v, quero ErrNotFound", err)
	}
}

func TestNewInboundRules(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustChat(t, s, jidB, "direct")
	mustChat(t, s, jidG, "group")
	mustChat(t, s, jidH, "direct")
	mustInsert(t, s, inbound(jidA, "a1", 100, "a1"))
	mustInsert(t, s, inbound(jidA, "a2", 101, "a2"))
	mustInsert(t, s, inbound(jidA, "a3", 102, "a3"))
	mustInsert(t, s, outbound(jidA, "a4", 103, "minha resposta"))

	mustInsert(t, s, inbound(jidB, "b0", 50, "antigo"))
	if err := s.SetOwnerReadAt(ctx, jidB, 60); err != nil {
		t.Fatalf("SetOwnerReadAt: %v", err)
	}
	mustInsert(t, s, inbound(jidB, "b1", 70, "novo"))

	mustInsert(t, s, inbound(jidG, "g1", 200, "grupo"))
	mustInsert(t, s, inbound(jidH, "h1", 300, "oculto"))
	if err := s.SetHidden(ctx, jidH, true); err != nil {
		t.Fatalf("SetHidden: %v", err)
	}

	res, err := s.NewInbound(ctx, InboundFilter{})
	if err != nil {
		t.Fatalf("NewInbound: %v", err)
	}
	if len(res.Chats) != 2 || res.MoreChats != 0 {
		t.Fatalf("sem grupos: %d chats, more %d; quero 2, 0", len(res.Chats), res.MoreChats)
	}
	a := res.Chats[0]
	if a.Chat.JID != jidA || a.NewCount != 3 || a.LatestPK != pkOf(t, s, jidA, "a3") {
		t.Fatalf("chat A = %+v", a)
	}
	if len(a.Messages) != 3 || a.Messages[0].ID != "a1" || a.Messages[2].ID != "a3" {
		t.Fatalf("mensagens A (ordem/conteúdo) = %+v", a.Messages)
	}
	if res.Chats[1].Chat.JID != jidB || res.Chats[1].NewCount != 1 {
		t.Fatalf("chat B = %+v", res.Chats[1])
	}

	withGroups, _ := s.NewInbound(ctx, InboundFilter{IncludeGroups: true})
	if len(withGroups.Chats) != 3 || withGroups.Chats[0].Chat.JID != jidG {
		t.Fatalf("com grupos: %+v", withGroups.Chats)
	}

	capped, _ := s.NewInbound(ctx, InboundFilter{MaxChats: 1, PerChat: 2})
	if len(capped.Chats) != 1 || capped.MoreChats != 1 {
		t.Fatalf("max_chats=1: %d chats, more %d; quero 1, 1", len(capped.Chats), capped.MoreChats)
	}
	got := capped.Chats[0]
	if len(got.Messages) != 2 || got.Messages[0].ID != "a2" || got.Messages[1].ID != "a3" {
		t.Fatalf("per_chat=2 deve trazer as 2 mais recentes em ordem: %+v", got.Messages)
	}
	if got.NewCount != 3 {
		t.Errorf("NewCount = %d, quero 3 (total, não por página)", got.NewCount)
	}

	if err := s.AdvanceAgentCursor(ctx, jidA, a.LatestPK); err != nil {
		t.Fatalf("AdvanceAgentCursor: %v", err)
	}
	after, _ := s.NewInbound(ctx, InboundFilter{})
	if len(after.Chats) != 1 || after.Chats[0].Chat.JID != jidB {
		t.Fatalf("depois do cursor: %+v", after.Chats)
	}
}

func TestNewInboundCursorOnlyMovesForward(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustInsert(t, s, inbound(jidA, "a1", 100, "x"))
	pk := pkOf(t, s, jidA, "a1")
	_ = s.AdvanceAgentCursor(ctx, jidA, pk)
	_ = s.AdvanceAgentCursor(ctx, jidA, pk-1)
	c, _ := s.GetChat(ctx, jidA)
	if c.AgentCursor != pk {
		t.Fatalf("cursor voltou: %d", c.AgentCursor)
	}
	_ = s.SetOwnerReadAt(ctx, jidA, 200)
	_ = s.SetOwnerReadAt(ctx, jidA, 150)
	c, _ = s.GetChat(ctx, jidA)
	if c.OwnerReadAt != 200 {
		t.Fatalf("owner_read_at voltou: %d", c.OwnerReadAt)
	}
	if res, _ := s.NewInbound(ctx, InboundFilter{}); len(res.Chats) != 0 {
		t.Fatal("mensagem já lida pelo dono apareceu como nova")
	}
}

func TestNewInboundLabelFilter(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustChat(t, s, jidB, "direct")
	mustInsert(t, s, inbound(jidA, "a1", 100, "x"))
	mustInsert(t, s, inbound(jidB, "b1", 101, "y"))
	if err := s.UpsertLabel(ctx, Label{ID: "fam", Name: "Família", Source: "local"}); err != nil {
		t.Fatalf("UpsertLabel: %v", err)
	}
	if err := s.SetChatLabel(ctx, jidA, "fam", true); err != nil {
		t.Fatalf("SetChatLabel: %v", err)
	}
	res, _ := s.NewInbound(ctx, InboundFilter{LabelID: "fam"})
	if len(res.Chats) != 1 || res.Chats[0].Chat.JID != jidA {
		t.Fatalf("filtro por etiqueta: %+v", res.Chats)
	}
}

func TestPurgeOlderThanRemovesMessagesAndFTS(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustInsert(t, s, inbound(jidA, "old", 10, "alfa antigo"))
	mustInsert(t, s, inbound(jidA, "new", 100, "alfa recente"))

	n, err := s.PurgeOlderThan(ctx, 50)
	if err != nil || n != 1 {
		t.Fatalf("PurgeOlderThan = %d, %v; quero 1", n, err)
	}
	if _, total, _ := s.Search(ctx, SearchFilter{Query: "antigo"}); total != 0 {
		t.Error("FTS ainda encontra mensagem expurgada")
	}
	if _, total, _ := s.Search(ctx, SearchFilter{Query: "alfa"}); total != 1 {
		t.Errorf("mensagem recente sumiu do FTS: %d", total)
	}
}

func TestPurgeOlderThanClearsFinishedQueueOnly(t *testing.T) {
	s, clk := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	old, _ := s.EnqueueSend(ctx, SendItem{ChatJID: jidA, Kind: KindText, Text: "velho"})
	_ = s.MarkSending(ctx, old)
	_ = s.MarkSent(ctx, old, "W1")
	pending, _ := s.EnqueueSend(ctx, SendItem{ChatJID: jidA, Kind: KindText, Text: "ainda na fila"})
	clk.Advance(3600 * 1e9)
	if _, err := s.PurgeOlderThan(ctx, clk.Now().Unix()); err != nil {
		t.Fatalf("PurgeOlderThan: %v", err)
	}
	if _, err := s.GetSend(ctx, old); !errors.Is(err, ErrNotFound) {
		t.Error("envio finalizado antigo não foi expurgado")
	}
	if it, err := s.GetSend(ctx, pending); err != nil || it.Status != StatusQueued {
		t.Errorf("item na fila foi tocado: %+v, %v", it, err)
	}
}

func TestPurgeChatAndPurgeAll(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustChat(t, s, jidB, "direct")
	mustInsert(t, s, inbound(jidA, "a1", 1, "um"))
	mustInsert(t, s, inbound(jidB, "b1", 2, "dois"))
	_ = s.UpsertContact(ctx, Contact{JID: jidA, FullName: "Ana"})
	_ = s.PutAlias(ctx, "123@lid", jidA)
	_ = s.UpsertLabel(ctx, Label{ID: "l1", Name: "Trabalho", Source: "local"})
	_ = s.SetChatLabel(ctx, jidA, "l1", true)
	_ = s.AddShareable(ctx, jidA)
	_ = s.Audit(ctx, "share", "c_x", "ok")

	n, err := s.PurgeChat(ctx, jidA)
	if err != nil || n != 1 {
		t.Fatalf("PurgeChat = %d, %v", n, err)
	}
	// The chat row stays, reset: its messages are gone but the chat is still known.
	if c, err := s.GetChat(ctx, jidA); err != nil || c.LastMessageAt != 0 || c.DisplayName != "" || c.Ref != "c_"+jidA {
		t.Errorf("chat após PurgeChat = %+v, %v", c, err)
	}
	if _, total, _ := s.Search(ctx, SearchFilter{Query: "um"}); total != 0 {
		t.Error("FTS ainda tem mensagem do chat expurgado")
	}
	if _, total, _ := s.Search(ctx, SearchFilter{Query: "dois"}); total != 1 {
		t.Error("PurgeChat apagou outro chat")
	}

	if err := s.PurgeAll(ctx); err != nil {
		t.Fatalf("PurgeAll: %v", err)
	}
	if n, _ := s.CountMessages(ctx, jidB); n != 0 {
		t.Errorf("mensagens restantes = %d", n)
	}
	if names, _ := s.AllContactNames(ctx); len(names) != 0 {
		t.Errorf("contatos restantes: %v", names)
	}
	if canon, _ := s.Canonical(ctx, "123@lid"); canon != "123@lid" {
		t.Errorf("alias não foi apagado: %s", canon)
	}
	if ok, _ := s.IsShareable(ctx, jidA); !ok {
		t.Error("PurgeAll não deve mexer na allowlist")
	}
	if l, _ := s.ListLabels(ctx); len(l) != 1 {
		t.Error("PurgeAll não deve apagar definições de etiqueta")
	}
}

func TestPurgeChatUnknownIsNoop(t *testing.T) {
	s, _ := newTestStore(t)
	if n, err := s.PurgeChat(context.Background(), jidH); err != nil || n != 0 {
		t.Fatalf("PurgeChat desconhecido = %d, %v", n, err)
	}
}
