package ingest

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// TestMessageFixturesBecomeExpectedRows is the table of message kinds (T04 acceptance).
// Each fixture goes through Translate and Handle, and the stored row is compared.
func TestMessageFixturesBecomeExpectedRows(t *testing.T) {
	ana := mustJID(phoneAna)
	cases := []struct {
		name     string
		msg      *waE2E.Message
		kind     string
		text     string
		caption  string
		quotedID string
	}{
		{"conversation", text("oi, tudo bem?"), "text", "oi, tudo bem?", "", ""},
		{"extended text with quote", &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: str("respondo"), ContextInfo: &waE2E.ContextInfo{StanzaID: str("q-1")}}}, "text", "respondo", "", "q-1"},
		{"image with caption", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: str("foto da praia")}},
			"image", "[imagem]", "foto da praia", ""},
		{"video without caption", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{}}, "video", "[vídeo]", "", ""},
		{"voice note with duration", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Seconds: u32(42), PTT: boolp(true)}},
			"audio", "[áudio 0:42]", "", ""},
		{"audio of a minute", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Seconds: u32(125)}}, "audio", "[áudio 2:05]", "", ""},
		{"audio without duration", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}, "audio", "[áudio]", "", ""},
		{"document with caption", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Caption: str("contrato"), FileName: str("c.pdf")}},
			"document", "[documento]", "contrato", ""},
		{"sticker", &waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}, "sticker", "[figurinha]", "", ""},
		{"location with name", &waE2E.Message{LocationMessage: &waE2E.LocationMessage{Name: str("Padaria Central")}},
			"location", "[localização]", "Padaria Central", ""},
		{"live location", &waE2E.Message{LiveLocationMessage: &waE2E.LiveLocationMessage{Caption: str("até já")}},
			"location", "[localização ao vivo]", "até já", ""},
		{"contact card keeps only the name", &waE2E.Message{ContactMessage: &waE2E.ContactMessage{
			DisplayName: str("Teste MCP"),
			Vcard:       str("BEGIN:VCARD\nTEL;waid=5511900000000:+55 11 90000-0000\nEND:VCARD")}},
			"contact", "[contato]", "Teste MCP", ""},
		{"several contacts", &waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{DisplayName: str("3 contatos")}},
			"contact", "[contatos]", "3 contatos", ""},
		{"interactive message uses the body", &waE2E.Message{InteractiveMessage: &waE2E.InteractiveMessage{
			Body: &waE2E.InteractiveMessage_Body{Text: str("Escolha uma opção")}}}, "text", "Escolha uma opção", "", ""},
		{"view once is unwrapped", &waE2E.Message{ViewOnceMessageV2: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: str("só uma vez")}}}},
			"image", "[imagem]", "só uma vez", ""},
		{"ephemeral is unwrapped", &waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: text("temporária")}},
			"text", "temporária", "", ""},
		{"unsupported kind is marked", &waE2E.Message{PollCreationMessage: &waE2E.PollCreationMessage{Name: str("Qual?")}},
			"other", "[conteúdo não suportado]", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.feed(liveMsg(mustJID(phoneAna), ana, "M-1", false, at(100), c.msg))
			msgs := h.messages(phoneAna)
			if len(msgs) != 1 {
				t.Fatalf("stored %d messages, want 1", len(msgs))
			}
			m := msgs[0]
			if m.Kind != c.kind || m.Text != c.text || m.Caption != c.caption || m.QuotedID != c.quotedID {
				t.Errorf("row = kind %q text %q caption %q quoted %q; want %q %q %q %q",
					m.Kind, m.Text, m.Caption, m.QuotedID, c.kind, c.text, c.caption, c.quotedID)
			}
			if m.SenderJID != phoneAna || m.FromMe || m.TS != 100+testStart.Unix() {
				t.Errorf("sender/from_me/ts = %q %v %d", m.SenderJID, m.FromMe, m.TS)
			}
		})
	}
}

// TestContactCardStoresNoNumber checks the vCard never reaches a column (design §5).
func TestContactCardStoresNoNumber(t *testing.T) {
	h := newHarness(t)
	card := &waE2E.Message{ContactMessage: &waE2E.ContactMessage{
		DisplayName: str("Fulana"), Vcard: str("TEL;waid=5511900000000:+55 11 90000-0000")}}
	h.feed(liveMsg(mustJID(phoneAna), mustJID(phoneAna), "C-1", false, at(1), card))
	m := h.messages(phoneAna)[0]
	for _, col := range []string{m.Text, m.Caption, m.QuotedID, m.SenderJID} {
		if strings.Contains(col, "5511900000000") || strings.Contains(col, "waid") {
			t.Errorf("number leaked into a column: %q", col)
		}
	}
}

// TestSkippedMessagesAreNotStored covers protocol messages, reactions, status and newsletters.
func TestSkippedMessagesAreNotStored(t *testing.T) {
	h := newHarness(t)
	ana := mustJID(phoneAna)
	skipped := []*events.Message{
		liveMsg(ana, ana, "P-1", false, at(1), &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{}}),
		liveMsg(ana, ana, "R-1", false, at(1), &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{}}),
		liveMsg(ana, ana, "R-2", false, at(1), &waE2E.Message{EncReactionMessage: &waE2E.EncReactionMessage{}}),
		liveMsg(mustJID(statusJID), ana, "S-1", false, at(1), text("status")),
		liveMsg(mustJID("123@newsletter"), ana, "N-1", false, at(1), text("canal")),
		liveMsg(ana, ana, "X-1", false, at(1), nil),
	}
	for _, e := range skipped {
		h.notTranslated(e)
	}
	if h.chatExists(statusJID) || h.chatExists("123@newsletter") {
		t.Error("status or newsletter became a chat")
	}
	if h.chatExists(phoneAna) {
		t.Error("skipped messages created a chat")
	}
}

func TestOwnerMessageHasNoSenderAndAdvancesOwnerRead(t *testing.T) {
	h := newHarness(t)
	ana := mustJID(phoneAna)
	h.feed(liveMsg(ana, types.EmptyJID, "O-1", true, at(500), text("respondi")))
	m := h.messages(phoneAna)[0]
	if !m.FromMe || m.SenderJID != "" {
		t.Errorf("owner message = %+v", m)
	}
	if got := h.chat(phoneAna).OwnerReadAt; got != at(500).Unix() {
		t.Errorf("owner_read_at = %d, want the owner's message time", got)
	}
}

func TestGroupMessageSenderAndKind(t *testing.T) {
	h := newHarness(t)
	h.feed(liveMsg(mustJID(groupJID), mustJID(phoneBia), "G-1", false, at(7), text("bom dia")))
	c := h.chat(groupJID)
	if c.Kind != "group" {
		t.Errorf("kind = %q, want group", c.Kind)
	}
	if m := h.messages(groupJID)[0]; m.SenderJID != phoneBia {
		t.Errorf("group sender = %q", m.SenderJID)
	}
}

// TestTranslateIgnoresUnrelatedEvents covers events that ingest does not store.
func TestTranslateIgnoresUnrelatedEvents(t *testing.T) {
	h := newHarness(t)
	h.notTranslated(&events.Connected{})
	h.notTranslated(&events.Presence{})
	h.notTranslated(&events.AppState{})
	h.notTranslated(&events.LabelAssociationMessage{})
	h.notTranslated(&events.HistorySync{})
	h.notTranslated(&events.GroupInfo{JID: mustJID(groupJID)})
	h.notTranslated("texto solto")
}

func TestTranslateNamesOfEvents(t *testing.T) {
	ev, ok := Translate(&events.Contact{
		JID: mustJID(phoneAna),
		Action: &waSyncAction.ContactAction{
			FullName: str("Ana Souza"), FirstName: str("Ana"), LidJID: str(lidAna),
		},
	})
	if !ok {
		t.Fatal("contact not translated")
	}
	c := ev.(ContactEvent)
	if c.JID != phoneAna || c.FullName != "Ana Souza" || c.FirstName != "Ana" || c.LIDJID != lidAna {
		t.Errorf("contact = %+v", c)
	}

	ev, _ = Translate(&events.LabelEdit{LabelID: "9", Action: &waSyncAction.LabelEditAction{
		Name: str("Clientes"), Type: waSyncAction.LabelEditAction_CUSTOM.Enum(), IsActive: boolp(false)}})
	le := ev.(LabelEditEvent)
	if le.ListType != "CUSTOM" || !le.Inactive || le.Name != "Clientes" {
		t.Errorf("label = %+v", le)
	}

	ev, _ = Translate(&events.PushName{JID: mustJID(lidAna), JIDAlt: mustJID(phoneAna), NewPushName: "Aninha"})
	if pn := ev.(PushNameEvent); pn.Alt != phoneAna || pn.Name != "Aninha" {
		t.Errorf("push name = %+v", pn)
	}

	ev, _ = Translate(&events.BusinessName{JID: mustJID(phoneBia), NewBusinessName: "Loja Bia"})
	if bn := ev.(BusinessNameEvent); bn.Name != "Loja Bia" {
		t.Errorf("business name = %+v", bn)
	}

	ev, _ = Translate(&events.GroupInfo{JID: mustJID(groupJID), Name: &types.GroupName{Name: "Família"}})
	if g := ev.(GroupNameEvent); g.Name != "Família" || g.JID != groupJID {
		t.Errorf("group = %+v", g)
	}

	ev, _ = Translate(&events.LabelAssociationChat{JID: mustJID(phoneAna), LabelID: "9", Action: &waSyncAction.LabelAssociationAction{Labeled: boolp(true)}})
	if la := ev.(LabelAssocEvent); !la.Labeled || la.Chat != phoneAna {
		t.Errorf("assoc = %+v", la)
	}

	ev, _ = Translate(&events.MarkChatAsRead{JID: mustJID(phoneAna), Action: &waSyncAction.MarkChatAsReadAction{Read: boolp(true)}})
	if cr := ev.(ChatReadEvent); !cr.Read {
		t.Errorf("chat read = %+v", cr)
	}

	ev, _ = Translate(&events.Receipt{MessageSource: types.MessageSource{Chat: mustJID(phoneAna), IsFromMe: true}, Type: types.ReceiptTypePlayed})
	if r := ev.(ReceiptEvent); r.Type != "played" || !r.IsFromMe {
		t.Errorf("receipt = %+v", r)
	}
}

func TestHistorySyncTranslationKeepsTypeAndMappings(t *testing.T) {
	ev, ok := Translate(&events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_INITIAL_STATUS_V3.Enum(),
		PhoneNumberToLidMappings: []*waHistorySync.PhoneNumberToLIDMapping{
			{PnJID: str(phoneAna), LidJID: str(lidAna)},
		},
		Pushnames: []*waHistorySync.Pushname{{ID: str(phoneAna), Pushname: str("Ana")}},
	}})
	if !ok {
		t.Fatal("history sync not translated")
	}
	h := ev.(HistorySyncEvent)
	if h.Type != "INITIAL_STATUS_V3" || len(h.Mappings) != 1 || h.Mappings[0].A != lidAna || h.Mappings[0].B != phoneAna {
		t.Errorf("history = %+v", h)
	}
	if len(h.PushNames) != 1 || h.PushNames[0].Name != "Ana" {
		t.Errorf("push names = %+v", h.PushNames)
	}
}

func TestNormalizeRejectsWhatsmeowGuesses(t *testing.T) {
	// whatsmeow would read a bare string as a phone number; ingest must not.
	for _, bad := range []string{"", "5511999998888", "nao-e-jid"} {
		if got := normalize(bad); got != "" {
			t.Errorf("normalize(%q) = %q, want empty", bad, got)
		}
	}
	if got := normalize("5511999998888:7@s.whatsapp.net"); got != phoneAna {
		t.Errorf("normalize with device = %q", got)
	}
}

func TestCleanNameStripsOnlyFormattingAtEdges(t *testing.T) {
	cases := map[string]string{
		"\u200eNão lidas":     "Não lidas",
		"Banco X\u200f ":      "Banco X",
		"Mãe ❤️":              "Mãe ❤️",
		"\u200d\u200eFamília": "Família",
		"   ":                 "",
	}
	for in, want := range cases {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCategoryListRule(t *testing.T) {
	for _, l := range []string{"CUSTOM", "NONE", "PREDEFINED"} {
		if !isCategoryList(l) {
			t.Errorf("%s must be a category", l)
		}
	}
	for _, l := range []string{"UNREAD", "GROUPS", "FAVORITES", "ARCHIVED", "COMMUNITY", ""} {
		if isCategoryList(l) {
			t.Errorf("%s must not be a category", l)
		}
	}
}
