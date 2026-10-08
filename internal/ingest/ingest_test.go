package ingest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/riknog/whatsapp-mcp/internal/store"
)

// inbound feeds three messages from Ana at 100, 200 and 300 seconds.
func inboundThree(h *harness) {
	ana := mustJID(phoneAna)
	for i, sec := range []int64{100, 200, 300} {
		h.feed(liveMsg(ana, ana, fmt.Sprintf("I-%d", i), false, at(sec), text("msg")))
	}
}

func TestOwnerReadReceiptsMoveOwnerReadOnly(t *testing.T) {
	ana := mustJID(phoneAna)
	me := mustJID("5511900000001@s.whatsapp.net")
	cases := []struct {
		name     string
		receipt  *events.Receipt
		wantRead int64
	}{
		{"read by owner", &events.Receipt{MessageSource: types.MessageSource{Chat: ana, Sender: me, IsFromMe: true}, Type: types.ReceiptTypeRead, Timestamp: at(200)}, at(200).Unix()},
		{"read-self by owner", &events.Receipt{MessageSource: types.MessageSource{Chat: ana, Sender: me, IsFromMe: true}, Type: types.ReceiptTypeReadSelf, Timestamp: at(200)}, at(200).Unix()},
		{"played by owner", &events.Receipt{MessageSource: types.MessageSource{Chat: ana, Sender: me, IsFromMe: true}, Type: types.ReceiptTypePlayed, Timestamp: at(200)}, at(200).Unix()},
		{"played-self by owner", &events.Receipt{MessageSource: types.MessageSource{Chat: ana, Sender: me, IsFromMe: true}, Type: types.ReceiptTypePlayedSelf, Timestamp: at(200)}, at(200).Unix()},
		{"sender copy to own devices is ignored", &events.Receipt{MessageSource: types.MessageSource{Chat: ana, Sender: me, IsFromMe: true}, Type: types.ReceiptTypeSender, Timestamp: at(250)}, 0},
		{"contact read is ignored", &events.Receipt{MessageSource: types.MessageSource{Chat: ana, Sender: ana, IsFromMe: false}, Type: types.ReceiptTypeRead, Timestamp: at(250)}, 0},
		{"delivery is ignored", &events.Receipt{MessageSource: types.MessageSource{Chat: ana, Sender: me, IsFromMe: true}, Type: types.ReceiptTypeDelivered, Timestamp: at(250)}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			inboundThree(h)
			h.feed(c.receipt)
			if got := h.chat(phoneAna).OwnerReadAt; got != c.wantRead {
				t.Errorf("owner_read_at = %d, want %d", got, c.wantRead)
			}
		})
	}
}

func TestOwnerReadLeavesOnlyNewerMessagesUnseen(t *testing.T) {
	h := newHarness(t)
	inboundThree(h)
	ana := mustJID(phoneAna)
	h.feed(&events.Receipt{MessageSource: types.MessageSource{Chat: ana, Sender: ana, IsFromMe: true}, Type: types.ReceiptTypeRead, Timestamp: at(200)})
	res, err := h.st.NewInbound(h.ctx, store.InboundFilter{})
	if err != nil || len(res.Chats) != 1 || res.Chats[0].NewCount != 1 {
		t.Fatalf("NewInbound = %+v, %v; want one unseen message", res, err)
	}
}

func TestChatMarkAsReadRequiresReadTrue(t *testing.T) {
	h := newHarness(t)
	inboundThree(h)
	ana := mustJID(phoneAna)
	h.feed(&events.MarkChatAsRead{JID: ana, Timestamp: at(150), Action: &waSyncAction.MarkChatAsReadAction{Read: boolp(false)}})
	if got := h.chat(phoneAna).OwnerReadAt; got != 0 {
		t.Errorf("unread mark moved owner_read_at to %d", got)
	}
	h.feed(&events.MarkChatAsRead{JID: ana, Timestamp: at(150), Action: &waSyncAction.MarkChatAsReadAction{Read: boolp(true)}})
	if got := h.chat(phoneAna).OwnerReadAt; got != at(150).Unix() {
		t.Errorf("read mark owner_read_at = %d, want %d", got, at(150).Unix())
	}
}

func TestReceiptForUnknownChatIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.feed(&events.Receipt{MessageSource: types.MessageSource{Chat: mustJID(phoneBia), IsFromMe: true},
		Type: types.ReceiptTypeRead, Timestamp: at(1)})
	if h.chatExists(phoneBia) {
		t.Error("receipt created a chat")
	}
}

func TestLabelRulesDecideWhatIsACategory(t *testing.T) {
	h := newHarness(t)
	h.feedAll(
		labelEdit("L-custom", "\u200eFamília", waSyncAction.LabelEditAction_CUSTOM),
		labelEdit("L-biz", "Pagamento pendente", waSyncAction.LabelEditAction_NONE),
		labelEdit("L-pre", "Novo cliente", waSyncAction.LabelEditAction_PREDEFINED),
		labelEdit("L-unread", "\u200eNão lidas", waSyncAction.LabelEditAction_UNREAD),
		labelEdit("L-fav", "\u200eFavoritos", waSyncAction.LabelEditAction_FAVORITES),
		labelEdit("L-groups", "\u200eGrupos", waSyncAction.LabelEditAction_GROUPS),
		labelEdit("L-arch", "Arquivadas", waSyncAction.LabelEditAction_ARCHIVED),
	)
	names := map[string]bool{}
	labels, err := h.st.ListLabels(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range labels {
		names[l.Name] = true
		if l.Source != "whatsapp" {
			t.Errorf("source = %q", l.Source)
		}
	}
	for _, want := range []string{"Família", "Pagamento pendente", "Novo cliente"} {
		if !names[want] {
			t.Errorf("category %q missing; have %v", want, names)
		}
	}
	if len(labels) != 3 {
		t.Errorf("stored %d labels, want 3 (system lists are not categories)", len(labels))
	}
}

func TestLabelAttachDetachAndDeleteWithDuplicates(t *testing.T) {
	h := newHarness(t)
	ana := mustJID(phoneAna)
	inboundThree(h)
	h.feedAll(
		labelEdit("L-1", "Família", waSyncAction.LabelEditAction_CUSTOM),
		labelEdit("L-1", "Família", waSyncAction.LabelEditAction_CUSTOM), // live and full sync: same event twice
		labelAssoc(ana, "L-1", true),
		labelAssoc(ana, "L-1", true),
	)
	if got := h.labelNames(phoneAna); len(got) != 1 || got[0] != "Família" {
		t.Fatalf("labels after attach = %v", got)
	}
	h.feed(labelAssoc(ana, "L-1", false))
	if got := h.labelNames(phoneAna); len(got) != 0 {
		t.Errorf("labels after detach = %v", got)
	}
	h.feed(labelAssoc(ana, "L-1", true))
	h.feed(&events.LabelEdit{LabelID: "L-1", Action: &waSyncAction.LabelEditAction{Name: str("Família"),
		Type: waSyncAction.LabelEditAction_CUSTOM.Enum(), Deleted: boolp(true)}})
	if got := h.labelNames(phoneAna); len(got) != 0 {
		t.Errorf("deleted label still on chat: %v", got)
	}
	if all, _ := h.st.ListLabels(h.ctx); len(all) != 0 {
		t.Errorf("deleted label still listed: %+v", all)
	}
}

func TestLabelAttachBeforeLabelIsKeptAndApplied(t *testing.T) {
	h := newHarness(t)
	ana := mustJID(phoneAna)
	inboundThree(h)
	h.feed(labelAssoc(ana, "L-late", true)) // the label edit has not arrived yet
	if got := h.labelNames(phoneAna); len(got) != 0 {
		t.Fatalf("attached before the label existed: %v", got)
	}
	h.feed(labelEdit("L-late", "Trabalho", waSyncAction.LabelEditAction_CUSTOM))
	if got := h.labelNames(phoneAna); len(got) != 1 || got[0] != "Trabalho" {
		t.Errorf("pending association not applied: %v", got)
	}
}

func TestLabelAttachBeforeChatIsAppliedWhenChatArrives(t *testing.T) {
	h := newHarness(t)
	ana := mustJID(phoneAna)
	h.feed(labelEdit("L-c", "Clientes", waSyncAction.LabelEditAction_CUSTOM))
	h.feed(labelAssoc(ana, "L-c", true)) // no chat yet
	if h.chatExists(phoneAna) {
		t.Fatal("association created a chat")
	}
	// The chat arrives through a live message only: no other label event follows.
	inboundThree(h)
	if got := h.labelNames(phoneAna); len(got) != 1 || got[0] != "Clientes" {
		t.Errorf("association not applied when the chat arrived: %v", got)
	}
}

func TestAssociationWithSystemListIsDropped(t *testing.T) {
	h := newHarness(t)
	ana := mustJID(phoneAna)
	inboundThree(h)
	h.feed(labelEdit("L-sys", "\u200eNão lidas", waSyncAction.LabelEditAction_UNREAD))
	h.feed(labelAssoc(ana, "L-sys", true))
	h.feed(labelEdit("L-inactive", "Antiga", waSyncAction.LabelEditAction_CUSTOM))
	h.feed(&events.LabelEdit{LabelID: "L-inactive", Action: &waSyncAction.LabelEditAction{Name: str("Antiga"),
		Type: waSyncAction.LabelEditAction_CUSTOM.Enum(), IsActive: boolp(false)}})
	if got := h.labelNames(phoneAna); len(got) != 0 {
		t.Errorf("system list or inactive label attached: %v", got)
	}
}

func TestLabelWithoutNameIsNotStored(t *testing.T) {
	h := newHarness(t)
	h.feed(&events.LabelEdit{LabelID: "L-x", Action: &waSyncAction.LabelEditAction{Type: waSyncAction.LabelEditAction_CUSTOM.Enum()}})
	h.feed(&events.LabelEdit{LabelID: "", Action: &waSyncAction.LabelEditAction{Name: str("sem id"), Type: waSyncAction.LabelEditAction_CUSTOM.Enum()}})
	if all, _ := h.st.ListLabels(h.ctx); len(all) != 0 {
		t.Errorf("unnamed label stored: %+v", all)
	}
}

// translated returns the ingest event for a whatsmeow event, failing the test otherwise.
func translated(t *testing.T, evt any) any {
	t.Helper()
	ev, ok := Translate(evt)
	if !ok {
		t.Fatalf("Translate(%T) did not translate", evt)
	}
	return ev
}

func (h *harness) labelNames(chat string) []string {
	h.t.Helper()
	ls, err := h.st.LabelsOf(h.ctx, chat)
	if err != nil {
		h.t.Fatalf("LabelsOf: %v", err)
	}
	var out []string
	for _, l := range ls {
		out = append(out, l.Name)
	}
	return out
}

func TestNamesFollowPriorityAndKeepEachOther(t *testing.T) {
	h := newHarness(t)
	ana := mustJID(phoneAna)
	h.feedAll(
		&events.Contact{JID: ana, Action: &waSyncAction.ContactAction{FullName: str("\u200eAna Souza\u200f"), FirstName: str("Ana")}},
		&events.PushName{JID: ana, NewPushName: "Aninha"},
		&events.BusinessName{JID: ana, NewBusinessName: "Ana Loja"},
	)
	names, err := h.st.AllContactNames(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if names[phoneAna] != "Ana Souza" {
		t.Errorf("display name = %q, want the saved full name", names[phoneAna])
	}
	list, _ := h.st.ListContacts(h.ctx, store.ContactFilter{})
	if len(list) != 1 || list[0].PushName != "Aninha" || list[0].BusinessName != "Ana Loja" || list[0].FirstName != "Ana" {
		t.Errorf("contact row = %+v", list)
	}
	// An empty name is not an update.
	h.feed(&events.Contact{JID: ana, Action: &waSyncAction.ContactAction{FullName: str("")}})
	list, _ = h.st.ListContacts(h.ctx, store.ContactFilter{})
	if list[0].FullName != "Ana Souza" {
		t.Errorf("empty name erased the full name: %+v", list[0])
	}
}

// TestLIDMessagesMergeIntoPhoneChat is the merge scenario of T04: a message seen
// first under a LID joins the phone-number chat once the mapping is known.
func TestLIDMessagesMergeIntoPhoneChat(t *testing.T) {
	h := newHarness(t)
	ana := mustJID(phoneAna)
	lid := mustJID(lidAna)

	h.feed(liveMsg(lid, lid, "L-1", false, at(100), text("antes do mapeamento")))
	h.feed(labelEdit("L-f", "Família", waSyncAction.LabelEditAction_CUSTOM))
	h.feed(labelAssoc(lid, "L-f", true))
	lidChat := h.chat(lidAna)
	if lidChat.Kind != "direct" {
		t.Fatalf("LID chat kind = %q", lidChat.Kind)
	}

	// The contact event carries the mapping: the chat moves and keeps its ref.
	h.feed(&events.Contact{JID: ana, Action: &waSyncAction.ContactAction{FirstName: str("Ana"), LidJID: str(lidAna)}})

	if h.chatExists(lidAna) {
		t.Error("LID chat still exists after the merge")
	}
	merged := h.chat(phoneAna)
	if merged.Ref != lidChat.Ref {
		t.Errorf("ref changed on merge: %q -> %q (must stay stable)", lidChat.Ref, merged.Ref)
	}
	if n, _ := h.st.CountMessages(h.ctx, phoneAna); n != 1 {
		t.Errorf("messages under phone chat = %d, want 1", n)
	}
	if got := h.labelNames(phoneAna); len(got) != 1 {
		t.Errorf("label lost in merge: %v", got)
	}

	// New messages from the LID land in the phone chat and do not create a second chat.
	h.feed(liveMsg(ana, lid, "L-2", false, at(200), text("depois")))
	msgs := h.messages(phoneAna)
	if len(msgs) != 2 {
		t.Errorf("messages after merge = %d, want 2", len(msgs))
	}
	chats, _ := h.st.ListChats(h.ctx, store.ChatFilter{IncludeHidden: true})
	if len(chats) != 1 {
		t.Errorf("chats = %d, want 1 (no duplicate)", len(chats))
	}
	if c, _ := h.st.Canonical(h.ctx, lidAna); c != phoneAna {
		t.Errorf("canonical of LID = %q", c)
	}
}

// TestMessageAliasesFromSenderAltLinkBeforeWriting covers SenderAlt on live messages.
func TestMessageAliasesFromSenderAltLinkBeforeWriting(t *testing.T) {
	h := newHarness(t)
	ana := mustJID(phoneAna)
	lid := mustJID(lidAna)
	msg := liveMsg(lid, lid, "A-1", false, at(5), text("oi"))
	msg.Info.SenderAlt = ana
	h.feed(msg)
	if h.chatExists(lidAna) {
		t.Error("message under LID created a LID chat even though the phone JID was given")
	}
	if n, _ := h.st.CountMessages(h.ctx, phoneAna); n != 1 {
		t.Errorf("messages under phone = %d", n)
	}
}

func TestAliasPairsWithWrongShapeAreIgnored(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, p := range []AliasPair{{A: "", B: phoneAna}, {A: phoneAna, B: phoneBia}, {A: lidAna, B: lidGroup}, {A: "lixo", B: lidAna}} {
		if err := h.in.link(ctx, p.A, p.B); err != nil {
			t.Fatalf("link(%v): %v", p, err)
		}
	}
	if c, _ := h.st.Canonical(ctx, lidAna); c != lidAna {
		t.Errorf("wrong-shape pair created an alias: %q", c)
	}
}

func TestPendingLabelFollowsAliasToPhoneChat(t *testing.T) {
	h := newHarness(t)
	h.feed(labelEdit("L-p", "Pendência", waSyncAction.LabelEditAction_CUSTOM))
	h.feed(labelAssoc(mustJID(lidAna), "L-p", true)) // LID chat not stored yet
	// The contact event maps the LID to the phone JID before any message arrives.
	h.feed(&events.Contact{JID: mustJID(phoneAna), Action: &waSyncAction.ContactAction{FirstName: str("Ana"), LidJID: str(lidAna)}})
	h.feed(liveMsg(mustJID(phoneAna), mustJID(phoneAna), "PF-1", false, at(3), text("oi")))
	if got := h.labelNames(phoneAna); len(got) != 1 || got[0] != "Pendência" {
		t.Errorf("pending label lost after the alias: %v", got)
	}
}

func TestAliasLabelWaitsForChatOfEitherAddress(t *testing.T) {
	h := newHarness(t)
	h.feed(labelEdit("L-g", "Trabalho", waSyncAction.LabelEditAction_CUSTOM))
	h.feed(labelAssoc(mustJID(lidAna), "L-g", true)) // LID chat not stored yet
	h.feed(liveMsg(mustJID(lidAna), mustJID(lidAna), "W-1", false, at(3), text("oi")))
	if got := h.labelNames(lidAna); len(got) != 1 {
		t.Errorf("label not applied to the LID chat: %v", got)
	}
}

func TestGroupNameAndGroupKind(t *testing.T) {
	h := newHarness(t)
	h.feed(&events.GroupInfo{JID: mustJID(groupJID), Name: &types.GroupName{Name: "\u200eFamília\u200f"}})
	c := h.chat(groupJID)
	if c.Kind != "group" || c.DisplayName != "Família" {
		t.Errorf("group chat = %+v", c)
	}
	h.feed(&events.GroupInfo{JID: mustJID(groupJID), Name: &types.GroupName{Name: ""}})
	if h.chat(groupJID).DisplayName != "Família" {
		t.Error("empty group name erased the subject")
	}
}

func TestStatusBroadcastNeverBecomesAChat(t *testing.T) {
	h := newHarness(t)
	status := mustJID(statusJID)
	h.feed(&events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_INITIAL_BOOTSTRAP.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID:       str(statusJID),
			Messages: []*waHistorySync.HistorySyncMsg{historyMsg(statusJID, "st-1", false, 10, text("status"))},
		}},
	}})
	h.notTranslated(liveMsg(status, mustJID(phoneAna), "st-2", false, at(11), text("status ao vivo")))
	if h.chatExists(statusJID) {
		t.Error("status@broadcast became a chat")
	}
}

func TestStatusV3HistoryIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.feed(historyEvent(waHistorySync.HistorySync_INITIAL_STATUS_V3, &waHistorySync.Conversation{
		ID:       str(phoneAna),
		Messages: []*waHistorySync.HistorySyncMsg{historyMsg(phoneAna, "v3-1", false, 10, text("status"))},
	}))
	if h.chatExists(phoneAna) {
		t.Error("INITIAL_STATUS_V3 created a chat")
	}
}

// bootstrap builds a history sync with the given conversation and type.
func bootstrap(conv *waHistorySync.Conversation) *events.HistorySync {
	return historyEvent(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, conv)
}

func TestHistoryUnreadSeedsOwnerReadPoint(t *testing.T) {
	// Five messages, two unread from the contact: the owner read point sits
	// before the second-last inbound message (the one at 300s). Ts values are 100..500.
	msgs := []*waHistorySync.HistorySyncMsg{
		historyMsg(phoneAna, "h1", false, 100, text("a")),
		historyMsg(phoneAna, "h2", true, 200, text("b")),
		historyMsg(phoneAna, "h3", false, 300, text("c")),
		historyMsg(phoneAna, "h4", false, 400, text("d")),
		historyMsg(phoneAna, "h5", true, 500, text("e")),
	}
	h := newHarness(t)
	h.feed(bootstrap(&waHistorySync.Conversation{ID: str(phoneAna), UnreadCount: u32(2), Messages: msgs}))
	if got := h.chat(phoneAna).OwnerReadAt; got != at(200).Unix() {
		t.Errorf("owner_read_at = %d, want the time of the message before the unread ones (200s)", got)
	}
	res, err := h.st.NewInbound(h.ctx, store.InboundFilter{})
	if err != nil || len(res.Chats) != 1 || res.Chats[0].NewCount != 2 {
		t.Fatalf("NewInbound = %+v, %v; want 2 unseen", res, err)
	}
}

func TestHistoryWithoutUnreadMarksChatRead(t *testing.T) {
	h := newHarness(t)
	h.feed(bootstrap(&waHistorySync.Conversation{ID: str(phoneAna), UnreadCount: u32(0), Messages: []*waHistorySync.HistorySyncMsg{
		historyMsg(phoneAna, "r1", false, 100, text("a")), historyMsg(phoneAna, "r2", false, 900, text("b")),
	}}))
	if got := h.chat(phoneAna).OwnerReadAt; got != at(900).Unix() {
		t.Errorf("owner_read_at = %d, want the last message", got)
	}
	if res, _ := h.st.NewInbound(h.ctx, store.InboundFilter{}); len(res.Chats) != 0 {
		t.Errorf("read chat reported as new: %+v", res)
	}
}

func TestHistoryAllUnreadLeavesReadPointAlone(t *testing.T) {
	h := newHarness(t)
	h.feed(bootstrap(&waHistorySync.Conversation{ID: str(phoneAna), UnreadCount: u32(9), Messages: []*waHistorySync.HistorySyncMsg{
		historyMsg(phoneAna, "u1", false, 100, text("a")), historyMsg(phoneAna, "u2", false, 200, text("b")),
	}}))
	if got := h.chat(phoneAna).OwnerReadAt; got != 0 {
		t.Errorf("owner_read_at = %d, want 0 when every message is unread", got)
	}
}

func TestRecentSyncDoesNotSeedReadPoint(t *testing.T) {
	h := newHarness(t)
	h.feed(historyEvent(waHistorySync.HistorySync_RECENT, &waHistorySync.Conversation{ID: str(phoneAna), UnreadCount: u32(0),
		Messages: []*waHistorySync.HistorySyncMsg{historyMsg(phoneAna, "x1", false, 100, text("a"))}}))
	if got := h.chat(phoneAna).OwnerReadAt; got != 0 {
		t.Errorf("RECENT sync moved owner_read_at to %d", got)
	}
}

func TestHistoryMappingsAndNamesAreApplied(t *testing.T) {
	h := newHarness(t)
	h.feed(&events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_PUSH_NAME.Enum(),
		PhoneNumberToLidMappings: []*waHistorySync.PhoneNumberToLIDMapping{
			{PnJID: str(phoneAna), LidJID: str(lidAna)},
		},
		Pushnames: []*waHistorySync.Pushname{{ID: str(phoneAna), Pushname: str("Aninha")}},
	}})
	names, _ := h.st.AllContactNames(h.ctx)
	if names[phoneAna] != "Aninha" {
		t.Errorf("push name = %q", names[phoneAna])
	}
	if c, _ := h.st.Canonical(h.ctx, lidAna); c != phoneAna {
		t.Errorf("mapping not stored: %q", c)
	}
}

func TestHistoryConversationUnderLIDIsStoredUnderPhone(t *testing.T) {
	h := newHarness(t)
	h.feed(&events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_RECENT.Enum(),
		PhoneNumberToLidMappings: []*waHistorySync.PhoneNumberToLIDMapping{
			{PnJID: str(phoneAna), LidJID: str(lidAna)},
		},
		Conversations: []*waHistorySync.Conversation{{
			ID:       str(lidAna),
			Name:     str("Ana"),
			Messages: []*waHistorySync.HistorySyncMsg{historyMsg(lidAna, "lh-1", false, 50, text("oi"))},
		}},
	}})
	if h.chatExists(lidAna) {
		t.Error("LID conversation created a LID chat")
	}
	if c := h.chat(phoneAna); c.DisplayName != "Ana" {
		t.Errorf("display name = %q", c.DisplayName)
	}
	if n, _ := h.st.CountMessages(h.ctx, phoneAna); n != 1 {
		t.Errorf("messages = %d", n)
	}
}

func TestBadConversationDoesNotStopTheSync(t *testing.T) {
	h := newHarness(t)
	good := &waHistorySync.Conversation{ID: str(phoneBia), Messages: []*waHistorySync.HistorySyncMsg{historyMsg(phoneBia, "ok-1", false, 9, text("oi"))}}
	bad := &waHistorySync.Conversation{ID: str(phoneAna), Messages: []*waHistorySync.HistorySyncMsg{{Message: nil}}}
	h.feed(&events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType:      waHistorySync.HistorySync_RECENT.Enum(),
		Conversations: []*waHistorySync.Conversation{bad, good},
	}})
	if n, _ := h.st.CountMessages(h.ctx, phoneBia); n != 1 {
		t.Errorf("good conversation after a bad one = %d messages", n)
	}
}

func TestHistorySyncOneThousandMessagesIsFastAndIdempotent(t *testing.T) {
	h := newHarness(t)
	const n = 1000
	msgs := make([]*waHistorySync.HistorySyncMsg, 0, n)
	for i := 0; i < n; i++ {
		fromMe := i%4 == 0
		msgs = append(msgs, historyMsg(phoneAna, fmt.Sprintf("big-%d", i), fromMe, int64(i+1),
			text(fmt.Sprintf("mensagem %d", i))))
	}
	evt := bootstrap(&waHistorySync.Conversation{ID: str(phoneAna), UnreadCount: u32(0), Messages: msgs})
	ev, ok := Translate(evt)
	if !ok {
		t.Fatal("not translated")
	}
	start := time.Now()
	if err := h.in.Handle(h.ctx, ev); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); historyBudget > 0 && elapsed > historyBudget {
		t.Errorf("1000 messages took %v, want < %v", elapsed, historyBudget)
	}
	if got, _ := h.st.CountMessages(h.ctx, phoneAna); got != n {
		t.Fatalf("count = %d, want %d", got, n)
	}
	// Same events again (live and full sync overlap): the count does not change.
	if err := h.in.Handle(h.ctx, ev); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.st.CountMessages(h.ctx, phoneAna); got != n {
		t.Errorf("after replay count = %d, want %d", got, n)
	}
	if c := h.chat(phoneAna); c.LastMessageAt != at(n).Unix() {
		t.Errorf("last_message_at = %d", c.LastMessageAt)
	}
}

func TestRunStopsOnContextAndKeepsGoingAfterErrors(t *testing.T) {
	h := newHarness(t)
	ana := mustJID(phoneAna)
	events := make(chan any, 4)
	// Run consumes translated events, as wa.Client.Events delivers them.
	events <- translated(t, liveMsg(ana, ana, "run-1", false, at(1), text("primeira")))
	close(events)
	if err := h.in.Run(h.ctx, events); err != nil {
		t.Fatalf("Run on closed channel = %v", err)
	}
	if n, _ := h.st.CountMessages(h.ctx, phoneAna); n != 1 {
		t.Errorf("message not written by Run: %d", n)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.in.Run(ctx, make(chan any)); err != context.Canceled {
		t.Errorf("Run with cancelled context = %v", err)
	}
}

func TestRunLogsFailuresWithoutContent(t *testing.T) {
	h := newHarness(t)
	ana := mustJID(phoneAna)
	ch := make(chan any, 2)
	ch <- translated(t, liveMsg(ana, ana, "boom-1", false, at(1), text("segredo do texto")))
	close(ch)
	// Close the store: every write now fails, Run logs each failure and continues.
	_ = h.st.Close()
	if err := h.in.Run(h.ctx, ch); err != nil {
		t.Fatalf("Run = %v", err)
	}
	logged := h.log.String()
	if !strings.Contains(logged, "evento não gravado") {
		t.Errorf("failure not logged: %q", logged)
	}
	for _, leak := range []string{phoneAna, "5511999998888", "segredo do texto"} {
		if strings.Contains(logged, leak) {
			t.Errorf("log leaks %q: %s", leak, logged)
		}
	}
}

func TestHandleIgnoresUnknownValues(t *testing.T) {
	h := newHarness(t)
	if err := h.in.Handle(h.ctx, 42); err != nil {
		t.Errorf("unknown value = %v", err)
	}
}

func TestHelpersDefaultsAndTimes(t *testing.T) {
	if unixOr(time.Time{}, time.Time{}) != 0 {
		t.Error("zero time with zero fallback must be 0")
	}
	if unixOr(time.Time{}, at(7)) != at(7).Unix() {
		t.Error("zero time must use the fallback")
	}
	if unixOr(at(3), at(7)) != at(3).Unix() {
		t.Error("set time must win")
	}
	if got := audioMark(0); got != "[áudio]" {
		t.Errorf("audioMark(0) = %q", got)
	}
	if !isLID(lidAna) || isLID(phoneAna) || !isPN(phoneAna) || isPN(lidAna) {
		t.Error("isLID/isPN wrong")
	}
	if jidString(types.EmptyJID) != "" {
		t.Error("empty JID should give empty string")
	}
	if skipChat("") != true || skipChat(groupJID) || !skipChat(statusJID) {
		t.Error("skipChat wrong")
	}
}

func TestNewDefaultsClockAndLogger(t *testing.T) {
	h := newHarness(t)
	in := New(h.st, fakeRefs{}, nil, nil)
	if in.clk == nil || in.log == nil {
		t.Fatal("defaults not applied")
	}
}

// TestSystemListWithPredefinedIDIsNotACategory fixes the rule: a PredefinedID
// above zero does not make a system list (UNREAD) a category. Only the list
// types NONE, CUSTOM and PREDEFINED do.
func TestSystemListWithPredefinedIDIsNotACategory(t *testing.T) {
	h := newHarness(t)
	h.feed(&events.LabelEdit{LabelID: "L-sys", Action: &waSyncAction.LabelEditAction{
		Name: str("\u200eNão lidas"), Type: waSyncAction.LabelEditAction_UNREAD.Enum(), PredefinedID: i32(7)}})
	if all, _ := h.st.ListLabels(h.ctx); len(all) != 0 {
		t.Errorf("system list with PredefinedID stored as category: %+v", all)
	}
}
