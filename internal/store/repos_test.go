package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

func TestChatUpsertKeepsOwnedFields(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	if err := s.UpsertChat(ctx, Chat{JID: jidA, Ref: "c_a", Kind: "direct", DisplayName: "Ana", LastMessageAt: 100}); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	_ = s.SetHidden(ctx, jidA, true)
	_ = s.AdvanceAgentCursor(ctx, jidA, 90)

	// Second upsert: empty name keeps the old one, time only moves forward,
	// and ref, hidden and cursors are not touched.
	if err := s.UpsertChat(ctx, Chat{JID: jidA, Ref: "c_a", Kind: "direct", DisplayName: "", LastMessageAt: 50}); err != nil {
		t.Fatalf("UpsertChat 2: %v", err)
	}
	c, err := s.GetChat(ctx, jidA)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.DisplayName != "Ana" || c.LastMessageAt != 100 || !c.Hidden || c.AgentCursor != 90 || c.Ref != "c_a" {
		t.Fatalf("chat = %+v", c)
	}

	if err := s.UpsertChat(ctx, Chat{JID: jidA, Ref: "c_a", Kind: "direct", DisplayName: "Ana Souza", LastMessageAt: 200}); err != nil {
		t.Fatalf("UpsertChat 3: %v", err)
	}
	c, _ = s.GetChat(ctx, jidA)
	if c.DisplayName != "Ana Souza" || c.LastMessageAt != 200 {
		t.Fatalf("chat após nova atualização = %+v", c)
	}
}

func TestChatValidationAndLookup(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	for _, bad := range []Chat{
		{Ref: "c_x", Kind: "direct"},
		{JID: "x", Kind: "direct"},
		{JID: "x", Ref: "c_x", Kind: "channel"},
	} {
		err := s.UpsertChat(ctx, bad)
		var te toolerr.Error
		if !errors.As(err, &te) || te.Code != toolerr.CodeInvalidArgument {
			t.Errorf("UpsertChat(%+v) = %v, quero invalid_argument", bad, err)
		}
	}
	if _, err := s.GetChat(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetChat ausente: %v", err)
	}
	mustChat(t, s, jidA, "direct")
	c, err := s.GetChatByRef(ctx, "c_"+jidA)
	if err != nil || c.JID != jidA {
		t.Fatalf("GetChatByRef = %+v, %v", c, err)
	}
	if _, err := s.GetChatByRef(ctx, "c_nada"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetChatByRef ausente: %v", err)
	}
}

func TestListChatsFilters(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustChat(t, s, jidG, "group")
	mustChat(t, s, jidH, "direct")
	mustInsert(t, s, inbound(jidA, "a1", 100, "x"))
	mustInsert(t, s, inbound(jidG, "g1", 200, "y"))
	mustInsert(t, s, inbound(jidH, "h1", 300, "z"))
	_ = s.SetHidden(ctx, jidH, true)
	_ = s.UpsertLabel(ctx, Label{ID: "l", Name: "Clientes", Source: "whatsapp"})
	_ = s.SetChatLabel(ctx, jidG, "l", true)

	all, _ := s.ListChats(ctx, ChatFilter{})
	if len(all) != 2 || all[0].JID != jidG || all[1].JID != jidA {
		t.Fatalf("ListChats padrão = %+v (oculto deve sair; mais recente primeiro)", all)
	}
	withHidden, _ := s.ListChats(ctx, ChatFilter{IncludeHidden: true})
	if len(withHidden) != 3 {
		t.Errorf("IncludeHidden: %d chats, quero 3", len(withHidden))
	}
	direct, _ := s.ListChats(ctx, ChatFilter{Kind: "direct"})
	if len(direct) != 1 || direct[0].JID != jidA {
		t.Errorf("Kind=direct: %+v", direct)
	}
	byLabel, _ := s.ListChats(ctx, ChatFilter{LabelID: "l"})
	if len(byLabel) != 1 || byLabel[0].JID != jidG {
		t.Errorf("LabelID: %+v", byLabel)
	}
	page, _ := s.ListChats(ctx, ChatFilter{Limit: 1, Offset: 1})
	if len(page) != 1 || page[0].JID != jidA {
		t.Errorf("paginação: %+v", page)
	}
	if err := s.SetHidden(ctx, "nope", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetHidden ausente: %v", err)
	}
	if err := s.SetOwnerReadAt(ctx, "nope", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetOwnerReadAt ausente: %v", err)
	}
	if err := s.AdvanceAgentCursor(ctx, "nope", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("AdvanceAgentCursor ausente: %v", err)
	}
}

func TestAliasesAndContactNames(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	if err := s.PutAlias(ctx, "123@lid", jidA); err != nil {
		t.Fatalf("PutAlias: %v", err)
	}
	if err := s.PutAlias(ctx, "123@lid", jidB); err != nil {
		t.Fatalf("PutAlias (atualização): %v", err)
	}
	if c, _ := s.Canonical(ctx, "123@lid"); c != jidB {
		t.Errorf("Canonical = %s, quero %s", c, jidB)
	}
	if c, _ := s.Canonical(ctx, jidA); c != jidA {
		t.Errorf("Canonical sem alias deve devolver o próprio jid, veio %s", c)
	}
	if err := s.PutAlias(ctx, jidA, jidA); err == nil {
		t.Error("alias igual ao canônico aceito")
	}

	_ = s.UpsertContact(ctx, Contact{JID: jidA, FullName: "Ana Full", FirstName: "Ana", PushName: "anita"})
	_ = s.UpsertContact(ctx, Contact{JID: jidB, FirstName: "Bia", PushName: "bi"})
	_ = s.UpsertContact(ctx, Contact{JID: jidG, PushName: "só push"})
	_ = s.UpsertContact(ctx, Contact{JID: jidH, BusinessName: "Loja"})
	_ = s.UpsertContact(ctx, Contact{JID: "nome@vazio", FullName: "  "})
	if err := s.UpsertContact(ctx, Contact{}); err == nil {
		t.Error("contato sem jid aceito")
	}

	names, err := s.AllContactNames(ctx)
	if err != nil {
		t.Fatalf("AllContactNames: %v", err)
	}
	want := map[string]string{jidA: "Ana Full", jidB: "Bia", jidG: "só push", jidH: "Loja"}
	if len(names) != len(want) {
		t.Fatalf("nomes = %v", names)
	}
	for k, v := range want {
		if names[k] != v {
			t.Errorf("nome de %s = %q, quero %q", k, names[k], v)
		}
	}

	page, err := s.ListContacts(ctx, ContactFilter{Limit: 2})
	if err != nil || len(page) != 2 || page[0].JID > page[1].JID {
		t.Fatalf("ListContacts = %+v, %v", page, err)
	}
	rest, _ := s.ListContacts(ctx, ContactFilter{Limit: 10, Offset: 2})
	if len(rest) != 3 {
		t.Errorf("segunda página = %d, quero 3", len(rest))
	}
}

func TestDisplayNamePriority(t *testing.T) {
	cases := []struct {
		c    Contact
		want string
	}{
		{Contact{FullName: "F", FirstName: "A", PushName: "P", BusinessName: "B"}, "F"},
		{Contact{FirstName: "A", PushName: "P", BusinessName: "B"}, "A"},
		{Contact{PushName: "P", BusinessName: "B"}, "P"},
		{Contact{BusinessName: "B"}, "B"},
		{Contact{FullName: " ", PushName: "P"}, "P"},
		{Contact{}, ""},
	}
	for _, tc := range cases {
		if got := tc.c.DisplayName(); got != tc.want {
			t.Errorf("DisplayName(%+v) = %q, quero %q", tc.c, got, tc.want)
		}
	}
}

func TestLabels(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")

	for _, bad := range []Label{{Name: "x", Source: "local"}, {ID: "1", Source: "local"}, {ID: "1", Name: "x", Source: "nuvem"}} {
		if err := s.UpsertLabel(ctx, bad); err == nil {
			t.Errorf("UpsertLabel(%+v) aceito", bad)
		}
	}
	if err := s.UpsertLabel(ctx, Label{ID: "fam", Name: "Família", Color: 3, Source: "local"}); err != nil {
		t.Fatalf("UpsertLabel: %v", err)
	}
	if err := s.UpsertLabel(ctx, Label{ID: "work", Name: "Trabalho", Source: "whatsapp"}); err != nil {
		t.Fatalf("UpsertLabel: %v", err)
	}
	if err := s.UpsertLabel(ctx, Label{ID: "fam", Name: "Família!", Color: 4, Source: "local"}); err != nil {
		t.Fatalf("UpsertLabel update: %v", err)
	}

	if err := s.SetChatLabel(ctx, jidA, "fam", true); err != nil {
		t.Fatalf("SetChatLabel on: %v", err)
	}
	if err := s.SetChatLabel(ctx, jidA, "fam", true); err != nil {
		t.Fatalf("SetChatLabel on (repetido): %v", err)
	}
	if err := s.SetChatLabel(ctx, jidA, "work", true); err != nil {
		t.Fatalf("SetChatLabel on work: %v", err)
	}
	of, err := s.LabelsOf(ctx, jidA)
	if err != nil || len(of) != 2 || of[0].Name != "Família!" {
		t.Fatalf("LabelsOf = %+v, %v", of, err)
	}
	if err := s.SetChatLabel(ctx, jidA, "work", false); err != nil {
		t.Fatalf("SetChatLabel off: %v", err)
	}
	if of, _ := s.LabelsOf(ctx, jidA); len(of) != 1 {
		t.Errorf("após remover: %d etiquetas", len(of))
	}

	if err := s.SetChatLabel(ctx, jidH, "fam", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("chat ausente: %v", err)
	}
	if err := s.SetChatLabel(ctx, jidA, "ghost", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("etiqueta ausente: %v", err)
	}

	if err := s.DeleteLabel(ctx, "fam"); err != nil {
		t.Fatalf("DeleteLabel: %v", err)
	}
	if list, _ := s.ListLabels(ctx); len(list) != 1 || list[0].ID != "work" {
		t.Errorf("ListLabels após apagar = %+v", list)
	}
	if of, _ := s.LabelsOf(ctx, jidA); len(of) != 0 {
		t.Errorf("etiqueta apagada ainda no chat: %+v", of)
	}
	if err := s.SetChatLabel(ctx, jidA, "fam", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("etiqueta apagada aceita em chat: %v", err)
	}
	if err := s.DeleteLabel(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteLabel ausente: %v", err)
	}
}

func TestTextHashNormalization(t *testing.T) {
	if TextHash("Oi,  Tudo   bem?") != TextHash("  oi, tudo bem?\n") {
		t.Error("hash não normaliza caixa e espaços")
	}
	if TextHash("oi") == TextHash("oi!") {
		t.Error("textos diferentes com mesmo hash")
	}
}

func TestSendQueueLifecycle(t *testing.T) {
	s, clk := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustChat(t, s, jidB, "direct")

	id1, err := s.EnqueueSend(ctx, SendItem{ChatJID: jidA, Kind: KindText, Text: "primeira"})
	if err != nil {
		t.Fatalf("EnqueueSend: %v", err)
	}
	clk.Advance(time.Second)
	id2, _ := s.EnqueueSend(ctx, SendItem{ChatJID: jidB, Kind: KindText, Text: "segunda"})

	if n, _ := s.CountPending(ctx); n != 2 {
		t.Fatalf("CountPending = %d, quero 2", n)
	}
	next, err := s.NextQueued(ctx)
	if err != nil || next.ID != id1 || next.Status != StatusQueued || next.EnqueuedAt != testStart.Unix() {
		t.Fatalf("NextQueued = %+v, %v (FIFO)", next, err)
	}

	if err := s.MarkSent(ctx, id1, "W1"); !errors.Is(err, ErrInvalidState) {
		t.Errorf("MarkSent a partir de queued: %v, quero ErrInvalidState", err)
	}
	if err := s.MarkSending(ctx, id1); err != nil {
		t.Fatalf("MarkSending: %v", err)
	}
	if err := s.MarkSending(ctx, id1); !errors.Is(err, ErrInvalidState) {
		t.Errorf("MarkSending repetido: %v", err)
	}
	clk.Advance(time.Minute)
	if err := s.MarkSent(ctx, id1, "W1"); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}
	it, _ := s.GetSend(ctx, id1)
	if it.Status != StatusSent || it.WAMessageID != "W1" || it.SentAt != clk.Now().Unix() {
		t.Fatalf("após enviar: %+v", it)
	}

	if err := s.MarkSending(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkSending inexistente: %v", err)
	}
	if err := s.MarkFailed(ctx, 9999, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkFailed inexistente: %v", err)
	}
	if err := s.MarkFailed(ctx, id1, "x"); !errors.Is(err, ErrInvalidState) {
		t.Errorf("MarkFailed em sent: %v", err)
	}
	if err := s.MarkFailed(ctx, id2, "rede"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if it, _ := s.GetSend(ctx, id2); it.Status != StatusFailed || it.Error != "rede" {
		t.Errorf("após falha: %+v", it)
	}
	if _, err := s.GetSend(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSend inexistente: %v", err)
	}
	if _, err := s.NextQueued(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("NextQueued vazio: %v", err)
	}
}

func TestExpireStaleMovesQueuedAndSending(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	q, _ := s.EnqueueSend(ctx, SendItem{ChatJID: jidA, Kind: KindText, Text: "a"})
	sd, _ := s.EnqueueSend(ctx, SendItem{ChatJID: jidA, Kind: KindText, Text: "b"})
	done, _ := s.EnqueueSend(ctx, SendItem{ChatJID: jidA, Kind: KindText, Text: "c"})
	_ = s.MarkSending(ctx, sd)
	_ = s.MarkSending(ctx, done)
	_ = s.MarkSent(ctx, done, "W")

	n, err := s.ExpireStale(ctx)
	if err != nil || n != 2 {
		t.Fatalf("ExpireStale = %d, %v; quero 2", n, err)
	}
	for id, want := range map[int64]string{q: StatusExpired, sd: StatusExpired, done: StatusSent} {
		if it, _ := s.GetSend(ctx, id); it.Status != want {
			t.Errorf("item %d = %s, quero %s", id, it.Status, want)
		}
	}
	if _, err := s.NextQueued(ctx); !errors.Is(err, ErrNotFound) {
		t.Error("ainda há item na fila após expirar")
	}
}

func TestEnqueueValidation(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	for _, it := range []SendItem{
		{Kind: KindText, Text: "x"},
		{ChatJID: jidA, Kind: KindText, Text: "   "},
		{ChatJID: jidA, Kind: KindContact},
		{ChatJID: jidA, Kind: "imagem", Text: "x"},
	} {
		if _, err := s.EnqueueSend(ctx, it); err == nil {
			t.Errorf("EnqueueSend(%+v) aceito", it)
		}
	}
	id, err := s.EnqueueSend(ctx, SendItem{ChatJID: jidA, Kind: KindContact, SharedJID: jidB})
	if err != nil {
		t.Fatalf("EnqueueSend contato: %v", err)
	}
	it, _ := s.GetSend(ctx, id)
	if it.Kind != KindContact || it.SharedJID != jidB || it.TextHash == "" {
		t.Errorf("cartão = %+v", it)
	}
}

func TestDuplicateSentLimitsAndWindows(t *testing.T) {
	s, clk := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, jidA, "direct")
	mustChat(t, s, jidB, "direct")
	since := clk.Now().Unix() - 60

	id, _ := s.EnqueueSend(ctx, SendItem{ChatJID: jidA, Kind: KindText, Text: "Oi tudo bem"})
	dup, err := s.RecentDuplicate(ctx, jidA, TextHash("  oi   TUDO bem"), since)
	if err != nil || !dup {
		t.Fatalf("RecentDuplicate (normalizado) = %v, %v; quero true", dup, err)
	}
	if d, _ := s.RecentDuplicate(ctx, jidB, TextHash("Oi tudo bem"), since); d {
		t.Error("duplicata cruzou destinatários")
	}
	if d, _ := s.RecentDuplicate(ctx, jidA, TextHash("Oi tudo bem"), clk.Now().Unix()+1); d {
		t.Error("duplicata fora da janela")
	}

	_ = s.MarkSending(ctx, id)
	_ = s.MarkFailed(ctx, id, "erro")
	if d, _ := s.RecentDuplicate(ctx, jidA, TextHash("Oi tudo bem"), since); d {
		t.Error("item falho contou como duplicata")
	}

	ok, _ := s.EnqueueSend(ctx, SendItem{ChatJID: jidA, Kind: KindText, Text: "enviada"})
	_ = s.MarkSending(ctx, ok)
	clk.Advance(2 * time.Minute)
	_ = s.MarkSent(ctx, ok, "W9")

	n, err := s.SentSince(ctx, testStart.Unix())
	if err != nil || n != 1 {
		t.Errorf("SentSince = %d, %v; quero 1", n, err)
	}
	if n, _ := s.SentSince(ctx, clk.Now().Unix()+1); n != 0 {
		t.Errorf("SentSince após a janela = %d", n)
	}
	if n, _ := s.DistinctRecipientsSince(ctx, testStart.Unix()); n != 1 {
		t.Errorf("DistinctRecipientsSince = %d, quero 1", n)
	}
}

func TestShareableAllowlist(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if ok, _ := s.IsShareable(ctx, jidA); ok {
		t.Fatal("contato compartilhável por padrão")
	}
	if err := s.AddShareable(ctx, jidA); err != nil {
		t.Fatalf("AddShareable: %v", err)
	}
	if err := s.AddShareable(ctx, jidA); err != nil {
		t.Fatalf("AddShareable repetido: %v", err)
	}
	if ok, _ := s.IsShareable(ctx, jidA); !ok {
		t.Fatal("IsShareable após adicionar = false")
	}
	if list, _ := s.ListShareable(ctx); len(list) != 1 || list[0] != jidA {
		t.Fatalf("ListShareable = %v", list)
	}
	if err := s.RemoveShareable(ctx, jidA); err != nil {
		t.Fatalf("RemoveShareable: %v", err)
	}
	if err := s.RemoveShareable(ctx, jidA); !errors.Is(err, ErrNotFound) {
		t.Errorf("RemoveShareable ausente: %v", err)
	}
}

func TestAuditRedactsPhonesAndJIDs(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.Audit(ctx, "share_contact", "c_k3m9x2q8va", "enviado para 5511987654321@s.whatsapp.net de +55 11 98765-4321"); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	var action, ref, detail string
	var ts int64
	if err := s.r.QueryRowContext(ctx, `SELECT ts, action, chat_ref, detail FROM audit_log`).Scan(&ts, &action, &ref, &detail); err != nil {
		t.Fatalf("ler auditoria: %v", err)
	}
	if ts != testStart.Unix() || action != "share_contact" || ref != "c_k3m9x2q8va" {
		t.Fatalf("linha = %d %q %q", ts, action, ref)
	}
	for _, leak := range []string{"5511987654321", "@s.whatsapp.net", "98765-4321"} {
		if strings.Contains(detail, leak) {
			t.Errorf("auditoria vazou %q: %q", leak, detail)
		}
	}
	if err := s.Audit(ctx, "x", "c_y", `texto "1234"`); err != nil {
		t.Fatalf("Audit: %v", err)
	}
}

func TestClosedStoreReturnsErrors(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.ListChats(ctx, ChatFilter{}); err == nil {
		t.Error("leitura em banco fechado não retornou erro")
	}
	if _, err := s.InsertMessage(ctx, inbound(jidA, "x", 1, "y")); err == nil {
		t.Error("escrita em banco fechado não retornou erro")
	}
	if _, err := s.NewInbound(ctx, InboundFilter{}); err == nil {
		t.Error("NewInbound em banco fechado não retornou erro")
	}
}
