package store

import (
	"context"
	"errors"
	"testing"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

func TestInsertMessagesBatchIsIdempotentAndMovesLastMessage(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, "a@s.whatsapp.net", "direct")

	batch := []Message{inbound("a@s.whatsapp.net", "m1", 100, "um"), inbound("a@s.whatsapp.net", "m2", 300, "dois")}
	n, err := s.InsertMessages(ctx, batch)
	if err != nil || n != 2 {
		t.Fatalf("InsertMessages = %d, %v; want 2", n, err)
	}
	n, err = s.InsertMessages(ctx, batch)
	if err != nil || n != 0 {
		t.Fatalf("second InsertMessages = %d, %v; want 0 (idempotent)", n, err)
	}
	if c, _ := s.CountMessages(ctx, "a@s.whatsapp.net"); c != 2 {
		t.Errorf("count = %d, want 2", c)
	}
	chat, _ := s.GetChat(ctx, "a@s.whatsapp.net")
	if chat.LastMessageAt != 300 {
		t.Errorf("last_message_at = %d, want 300", chat.LastMessageAt)
	}
	if n, err := s.InsertMessages(ctx, nil); err != nil || n != 0 {
		t.Errorf("empty batch = %d, %v", n, err)
	}
}

func TestInsertMessagesIsAllOrNothing(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, "a@s.whatsapp.net", "direct")
	bad := []Message{inbound("a@s.whatsapp.net", "ok", 1, "x"), {ChatJID: "a@s.whatsapp.net", ID: "", Kind: "text"}}
	_, err := s.InsertMessages(ctx, bad)
	var te toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if c, _ := s.CountMessages(ctx, "a@s.whatsapp.net"); c != 0 {
		t.Errorf("rejected batch left %d rows", c)
	}
}

func TestInsertMessagesOnClosedStoreFails(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, "a@s.whatsapp.net", "direct")
	_ = s.w.Close()
	if _, err := s.InsertMessages(ctx, []Message{inbound("a@s.whatsapp.net", "x", 1, "y")}); err == nil {
		t.Error("closed store must return an error")
	}
}

func TestLinkAliasMovesChatMessagesLabelsAndContact(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const lid, pn = "123@lid", "5511999998888@s.whatsapp.net"

	// Chat known only by LID, with a message that is also present under the PN chat (duplicate id).
	if err := s.UpsertChat(ctx, Chat{JID: lid, Ref: "c_ref1", Kind: "direct", DisplayName: "", LastMessageAt: 50}); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, s, inbound(lid, "m1", 10, "antes do PN"))
	mustInsert(t, s, inbound(lid, "m2", 50, "novo"))
	if err := s.UpsertChat(ctx, Chat{JID: pn, Ref: "c_ref2", Kind: "direct", DisplayName: "Ana"}); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, s, inbound(pn, "m2", 50, "novo"))
	if err := s.UpsertLabel(ctx, Label{ID: "L1", Name: "Família", Source: "whatsapp"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChatLabel(ctx, lid, "L1", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOwnerReadAt(ctx, lid, 40); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertContactNames(ctx, Contact{JID: lid, FirstName: "Ana", PushName: "Ana P"}); err != nil {
		t.Fatal(err)
	}

	if err := s.LinkAlias(ctx, lid, pn); err != nil {
		t.Fatalf("LinkAlias: %v", err)
	}

	if c, _ := s.CountMessages(ctx, pn); c != 2 {
		t.Errorf("messages under PN = %d, want 2 (duplicate dropped)", c)
	}
	if _, err := s.GetChat(ctx, lid); !errors.Is(err, ErrNotFound) {
		t.Errorf("LID chat still present: %v", err)
	}
	chat, err := s.GetChat(ctx, pn)
	if err != nil {
		t.Fatal(err)
	}
	if chat.Ref != "c_ref2" {
		t.Errorf("canonical chat ref = %q, want the existing one c_ref2", chat.Ref)
	}
	if chat.DisplayName != "Ana" || chat.OwnerReadAt != 40 || chat.LastMessageAt != 50 {
		t.Errorf("merged chat = %+v", chat)
	}
	labels, _ := s.LabelsOf(ctx, pn)
	if len(labels) != 1 || labels[0].ID != "L1" {
		t.Errorf("labels moved = %+v", labels)
	}
	if c, _ := s.Canonical(ctx, lid); c != pn {
		t.Errorf("Canonical(lid) = %q", c)
	}
	names, _ := s.AllContactNames(ctx)
	if names[pn] != "Ana" {
		t.Errorf("merged contact name = %q, want Ana", names[pn])
	}
	if _, ok := names[lid]; ok {
		t.Error("alias contact row not removed")
	}
	// Idempotent.
	if err := s.LinkAlias(ctx, lid, pn); err != nil {
		t.Errorf("second LinkAlias: %v", err)
	}
}

func TestLinkAliasRenamesChatWhenCanonicalMissingAndKeepsRef(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const lid, pn = "777@lid", "5521988887777@s.whatsapp.net"
	if err := s.UpsertChat(ctx, Chat{JID: lid, Ref: "c_keep", Kind: "direct"}); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, s, inbound(lid, "a", 5, "oi"))
	if err := s.LinkAlias(ctx, lid, pn); err != nil {
		t.Fatal(err)
	}
	chat, err := s.GetChatByRef(ctx, "c_keep")
	if err != nil || chat.JID != pn {
		t.Fatalf("GetChatByRef = %+v, %v; want the renamed chat with the same ref", chat, err)
	}
	if c, _ := s.CountMessages(ctx, pn); c != 1 {
		t.Errorf("messages = %d, want 1", c)
	}
}

func TestLinkAliasChainsExistingAliases(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.PutAlias(ctx, "1@lid", "2@lid"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkAlias(ctx, "2@lid", "5500@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Canonical(ctx, "1@lid"); c != "5500@s.whatsapp.net" {
		t.Errorf("chained alias = %q", c)
	}
}

func TestLinkAliasRejectsInvalidArguments(t *testing.T) {
	s, _ := newTestStore(t)
	for _, c := range [][2]string{{"", "a"}, {"a", ""}, {"a", "a"}} {
		err := s.LinkAlias(context.Background(), c[0], c[1])
		var te toolerr.Error
		if !errors.As(err, &te) {
			t.Errorf("LinkAlias(%q,%q) err = %v, want toolerr", c[0], c[1], err)
		}
	}
}

func TestLinkAliasMovesSendersOfOtherChats(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	mustChat(t, s, "g@g.us", "group")
	mustInsert(t, s, Message{ChatJID: "g@g.us", ID: "x", SenderJID: "9@lid", TS: 1, Kind: "text", Text: "oi"})
	if err := s.LinkAlias(ctx, "9@lid", "5533@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.RecentMessages(ctx, "g@g.us", 10, 0)
	if err != nil || len(msgs) != 1 || msgs[0].SenderJID != "5533@s.whatsapp.net" {
		t.Errorf("sender after link = %+v, %v", msgs, err)
	}
}

func TestUpsertContactNamesKeepsNonEmptyFields(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertContactNames(ctx, Contact{JID: "a@s.whatsapp.net", FullName: "Ana Souza", FirstName: "Ana"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertContactNames(ctx, Contact{JID: "a@s.whatsapp.net", PushName: "Aninha"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertContactNames(ctx, Contact{JID: "a@s.whatsapp.net", FullName: "Ana S."}); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListContacts(ctx, ContactFilter{})
	if len(list) != 1 {
		t.Fatalf("contacts = %d", len(list))
	}
	c := list[0]
	if c.FullName != "Ana S." || c.FirstName != "Ana" || c.PushName != "Aninha" {
		t.Errorf("contact = %+v", c)
	}
	if err := s.UpsertContactNames(ctx, Contact{}); err == nil {
		t.Error("empty jid accepted")
	}
}

// TestLinkAliasKeepsUnconsumedLIDMessageVisible: the alias chat has an unconsumed
// message with a low pk; the canonical chat has been consumed up to a higher pk.
// After the merge the message must still reach the agent through NewInbound.
func TestLinkAliasKeepsUnconsumedLIDMessageVisible(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const lid, pn = "444@lid", "5531977776666@s.whatsapp.net"
	if err := s.UpsertChat(ctx, Chat{JID: lid, Ref: "c_lid", Kind: "direct"}); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, s, inbound(lid, "old-lid", 10, "não consumida"))
	if err := s.UpsertChat(ctx, Chat{JID: pn, Ref: "c_pn", Kind: "direct"}); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, s, inbound(pn, "a", 20, "x"))
	mustInsert(t, s, inbound(pn, "b", 30, "y"))
	if err := s.AdvanceAgentCursor(ctx, pn, pkOf(t, s, pn, "b")); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkAlias(ctx, lid, pn); err != nil {
		t.Fatal(err)
	}
	res, err := s.NewInbound(ctx, InboundFilter{PerChat: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range res.Chats {
		for _, m := range c.Messages {
			if m.ID == "old-lid" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("unconsumed LID message hidden after merge: %+v", res)
	}
}
