package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

// windowResult decodes a get_chat_messages output.
func windowResult(t *testing.T, f *fixture, args map[string]any) chatMessagesOut {
	t.Helper()
	res := f.call("get_chat_messages", args)
	mustOK(t, res)
	var out chatMessagesOut
	decodeInto(t, res, &out)
	return out
}

// mae returns the fixture messages of Mãe (oldest first).
func (f *fixture) mae() []store.Message { return f.msgs[f.direct[0].jid] }

// TestGetChatMessagesDefaultWindow checks the newest 20 messages, oldest first.
func TestGetChatMessagesDefaultWindow(t *testing.T) {
	f := newFixture(t)
	out := windowResult(t, f, map[string]any{"contact": "Mãe"})
	msgs := f.mae()
	if out.Total != int64(len(msgs)) || out.Offset != 0 || len(out.Messages) != 20 {
		t.Fatalf("total=%d offset=%d messages=%d", out.Total, out.Offset, len(out.Messages))
	}
	if out.Messages[0].ID != msgs[28].ID || out.Messages[19].ID != msgs[47].ID {
		t.Errorf("window = %s..%s, want %s..%s", out.Messages[0].ID, out.Messages[19].ID, msgs[28].ID, msgs[47].ID)
	}
	if !out.HasOlder || out.HasNewer {
		t.Errorf("has_older=%v has_newer=%v, want true and false", out.HasOlder, out.HasNewer)
	}
	if out.Contact != "Mãe" || out.ContactRef != f.direct[0].ref {
		t.Errorf("contact = %q %q", out.Contact, out.ContactRef)
	}
	// Own messages are "me" and carry no ref.
	for _, m := range out.Messages {
		if m.From == "me" && m.FromRef != "" {
			t.Errorf("own message has from_ref %q", m.FromRef)
		}
	}
}

// TestGetChatMessagesOffsetPaging checks limit=10 offset=10: the ten messages before the newest ten.
func TestGetChatMessagesOffsetPaging(t *testing.T) {
	f := newFixture(t)
	out := windowResult(t, f, map[string]any{"contact": f.direct[0].ref, "limit": 10, "offset": 10})
	msgs := f.mae()
	if len(out.Messages) != 10 || out.Messages[0].ID != msgs[28].ID || out.Messages[9].ID != msgs[37].ID {
		t.Fatalf("window = %d messages, first %s", len(out.Messages), out.Messages[0].ID)
	}
	if out.Offset != 10 || !out.HasOlder || !out.HasNewer {
		t.Errorf("offset=%d has_older=%v has_newer=%v", out.Offset, out.HasOlder, out.HasNewer)
	}
}

// TestGetChatMessagesEdges checks the end of the chat and the offset beyond it.
func TestGetChatMessagesEdges(t *testing.T) {
	f := newFixture(t)
	all := windowResult(t, f, map[string]any{"contact": "Mãe", "limit": 50})
	if len(all.Messages) != fxMsgsDirect || all.HasOlder || len(all.Notes) != 0 {
		t.Errorf("limit 50: messages=%d has_older=%v notes=%v", len(all.Messages), all.HasOlder, all.Notes)
	}
	over := windowResult(t, f, map[string]any{"contact": "Mãe", "limit": 51, "offset": 6000})
	if len(over.Messages) != 50 && len(over.Messages) != 0 {
		t.Errorf("limit 51 gave %d messages", len(over.Messages))
	}
	if !strings.Contains(strings.Join(over.Notes, "|"), "limit reduzido de 51 para o máximo de 50") ||
		!strings.Contains(strings.Join(over.Notes, "|"), "offset reduzido de 6000 para o máximo de 5000") {
		t.Errorf("notes = %v", over.Notes)
	}
	beyond := windowResult(t, f, map[string]any{"contact": "Mãe", "offset": 5000})
	if len(beyond.Messages) != 0 || beyond.HasOlder || !beyond.HasNewer {
		t.Errorf("offset 5000: messages=%d has_older=%v has_newer=%v", len(beyond.Messages), beyond.HasOlder, beyond.HasNewer)
	}
}

// TestGetChatMessagesTruncatesLongText checks the 2 000 character limit and the marker.
func TestGetChatMessagesTruncatesLongText(t *testing.T) {
	f := newFixture(t)
	out := windowResult(t, f, map[string]any{"contact": "Mãe", "limit": 50})
	long := out.Messages[40-(fxMsgsDirect-len(out.Messages))]
	if long.ID != f.mae()[40].ID {
		t.Fatalf("message 40 is %s, want %s", long.ID, f.mae()[40].ID)
	}
	marker := "…[+400 chars]"
	if !strings.HasSuffix(long.Text, marker) || utf8.RuneCountInString(long.Text) != maxMessageText+utf8.RuneCountInString(marker) {
		t.Errorf("long text: %d runes, suffix %q", utf8.RuneCountInString(long.Text), long.Text[len(long.Text)-20:])
	}
}

// TestGetChatMessagesOutputSize checks the context budget of a 50-message window (at most 25 KB).
func TestGetChatMessagesOutputSize(t *testing.T) {
	f := newFixture(t)
	res := f.call("get_chat_messages", map[string]any{"contact": "Mãe", "limit": 50})
	mustOK(t, res)
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(raw) > 25*1024 {
		t.Errorf("output = %d bytes, want <= 25 KB", len(raw))
	}
	for _, name := range []string{"Família Silva", "Contato 07"} {
		out := f.call("get_chat_messages", map[string]any{"contact": name, "limit": 50})
		mustOK(t, out)
		raw, _ := json.Marshal(out.StructuredContent)
		if len(raw) > 25*1024 {
			t.Errorf("%s: output = %d bytes", name, len(raw))
		}
	}
}

// TestGetChatMessagesAroundMessage checks the window centred on a message.
func TestGetChatMessagesAroundMessage(t *testing.T) {
	f := newFixture(t)
	msgs := f.mae()
	mid := windowResult(t, f, map[string]any{"contact": "Mãe", "around_message_id": msgs[20].ID, "limit": 10, "offset": 3})
	if len(mid.Messages) != 10 || mid.Messages[0].ID != msgs[16].ID || mid.Messages[9].ID != msgs[25].ID {
		t.Fatalf("window = %d messages, first %s", len(mid.Messages), mid.Messages[0].ID)
	}
	if mid.Offset != 22 || !mid.HasOlder || !mid.HasNewer || mid.Total != fxMsgsDirect {
		t.Errorf("offset=%d has_older=%v has_newer=%v total=%d", mid.Offset, mid.HasOlder, mid.HasNewer, mid.Total)
	}
	if !strings.Contains(strings.Join(mid.Notes, "|"), "offset ignorado") {
		t.Errorf("notes = %v, want the offset note", mid.Notes)
	}

	start := windowResult(t, f, map[string]any{"contact": "Mãe", "around_message_id": msgs[2].ID, "limit": 10})
	if len(start.Messages) != 10 || start.Messages[0].ID != msgs[0].ID || start.HasOlder || !start.HasNewer || start.Offset != 38 {
		t.Errorf("near start: messages=%d first=%s has_older=%v has_newer=%v offset=%d",
			len(start.Messages), start.Messages[0].ID, start.HasOlder, start.HasNewer, start.Offset)
	}
}

// TestGetChatMessagesAroundUnknownMessage checks that an id outside the chat is invalid_argument.
func TestGetChatMessagesAroundUnknownMessage(t *testing.T) {
	f := newFixture(t)
	res := f.call("get_chat_messages", map[string]any{"contact": "Mãe", "around_message_id": "3EB0NAOEXISTE"})
	if got := errorCode(t, res); got != string(toolerr.CodeInvalidArgument) {
		t.Errorf("code = %q, want invalid_argument", got)
	}
	// A message of another chat is not in this one.
	other := f.msgs[f.direct[1].jid][3].ID
	if got := errorCode(t, f.call("get_chat_messages", map[string]any{"contact": "Mãe", "around_message_id": other})); got != string(toolerr.CodeInvalidArgument) {
		t.Errorf("other chat's message: code = %q", got)
	}
}

// TestGetChatMessagesContactErrors checks the contact rules: no phone, hidden,
// ambiguous, unknown and empty values.
func TestGetChatMessagesContactErrors(t *testing.T) {
	f := newFixture(t)
	cases := map[string]string{
		"5511900000001":                string(toolerr.CodePhoneNotAllowed),
		"+5511987654321":               string(toolerr.CodePhoneNotAllowed),
		"5511900000001@s.whatsapp.net": string(toolerr.CodePhoneNotAllowed),
		"Banco Exemplo":                string(toolerr.CodeChatHidden),
		f.hidden.ref:                   string(toolerr.CodeChatHidden),
		"Ana":                          string(toolerr.CodeAmbiguousContact),
		"Fulano de Tal":                string(toolerr.CodeContactNotFound),
		"   ":                          string(toolerr.CodeInvalidArgument),
	}
	for contact, want := range cases {
		res := f.call("get_chat_messages", map[string]any{"contact": contact})
		if got := errorCode(t, res); got != want {
			t.Errorf("contact %q: code = %q, want %q", contact, got, want)
		}
	}

	// The ambiguity lists the candidates by name and ref only.
	res := f.call("get_chat_messages", map[string]any{"contact": "Ana"})
	var out struct {
		Error struct {
			Details struct {
				Candidates []map[string]any `json:"candidates"`
			} `json:"details"`
		} `json:"error"`
	}
	decodeInto(t, res, &out)
	if len(out.Error.Details.Candidates) != 2 {
		t.Errorf("candidates = %d, want 2", len(out.Error.Details.Candidates))
	}
	for _, c := range out.Error.Details.Candidates {
		if _, ok := c["contact_ref"]; !ok {
			t.Errorf("candidate without contact_ref: %v", c)
		}
	}
}

// TestGetChatMessagesNamedContactWithoutChat checks a saved contact that has no conversation.
func TestGetChatMessagesNamedContactWithoutChat(t *testing.T) {
	f := newFixture(t)
	out := windowResult(t, f, map[string]any{"contact": "Lucas Menezes"})
	if out.Contact != "Lucas Menezes" || len(out.Messages) != 0 || out.Total != 0 || out.HasOlder {
		t.Errorf("named contact without chat = %+v", out)
	}
}

// TestGetChatMessagesGroupSenders checks the names of group members in a window.
func TestGetChatMessagesGroupSenders(t *testing.T) {
	f := newFixture(t)
	out := windowResult(t, f, map[string]any{"contact": "Família Silva", "limit": 50})
	if out.ContactRef != f.groups[0].ref || len(out.Messages) != fxMsgsGroup {
		t.Fatalf("group window: ref=%q messages=%d", out.ContactRef, len(out.Messages))
	}
	for _, m := range out.Messages {
		orig := f.findMessage(m.ID)
		from, ref := f.expectFrom(orig)
		if m.From != from || m.FromRef != ref {
			t.Errorf("%s from=%q ref=%q, want %q %q", m.ID, m.From, m.FromRef, from, ref)
		}
	}
}

// searchResult decodes a search_messages output.
func searchResult(t *testing.T, f *fixture, args map[string]any) searchOut {
	t.Helper()
	res := f.call("search_messages", args)
	mustOK(t, res)
	var out searchOut
	decodeInto(t, res, &out)
	return out
}

// TestSearchMessagesAccentInsensitive checks the FTS search with and without accents.
func TestSearchMessagesAccentInsensitive(t *testing.T) {
	f := newFixture(t)
	want := 0
	for _, m := range f.allMessages() {
		if m.ChatJID != f.hidden.jid && strings.Contains(m.Text, "orçamento") {
			want++
		}
	}
	if want == 0 {
		t.Fatalf("fixture has no orçamento message")
	}
	for _, q := range []string{"orçamento", "ORCAMENTO", "orcamento"} {
		out := searchResult(t, f, map[string]any{"query": q, "limit": 30})
		if out.Total != int64(want) {
			t.Errorf("query %q: total = %d, want %d", q, out.Total, want)
		}
		if len(out.Results) > 0 && !strings.Contains(out.Results[0].Snippet, "«") {
			t.Errorf("snippet without the match marker: %q", out.Results[0].Snippet)
		}
	}
}

// TestSearchMessagesScopeAndHidden checks the contact filter and the hidden chat.
func TestSearchMessagesScopeAndHidden(t *testing.T) {
	f := newFixture(t)
	want := 0
	for _, m := range f.mae() {
		if strings.Contains(m.Text, "jantar") {
			want++
		}
	}
	out := searchResult(t, f, map[string]any{"query": "jantar", "contact": "Mãe", "limit": 30})
	if out.Total != int64(want) || len(out.Results) != want {
		t.Errorf("Mãe jantar: total=%d results=%d, want %d", out.Total, len(out.Results), want)
	}
	for _, r := range out.Results {
		if r.ContactRef != f.direct[0].ref {
			t.Errorf("result from another chat: %s", r.Contact)
		}
	}

	if got := searchResult(t, f, map[string]any{"query": "BANCOSECRETO"}); got.Total != 0 || len(got.Results) != 0 {
		t.Errorf("hidden text found: total=%d", got.Total)
	}
	if got := errorCode(t, f.call("search_messages", map[string]any{"query": "saldo", "contact": "Banco"})); got != string(toolerr.CodeChatHidden) {
		t.Errorf("search in hidden chat: code = %q", got)
	}
}

// TestSearchMessagesRedactsPhones checks that a phone number matched by the
// search is masked in the message and the snippet.
func TestSearchMessagesRedactsPhones(t *testing.T) {
	f := newFixture(t)
	out := searchResult(t, f, map[string]any{"query": "98765", "limit": 5})
	if out.Total == 0 {
		t.Fatalf("no result for the phone digits; fixture changed?")
	}
	for _, r := range out.Results {
		if strings.Contains(r.Message.Text, "98765") || strings.Contains(r.Snippet, "98765") {
			t.Errorf("phone digits reached the output: %q / %q", r.Message.Text, r.Snippet)
		}
	}
}

// TestSearchMessagesLimitsAndErrors checks the limits, the short query and the FTS-empty query.
func TestSearchMessagesLimitsAndErrors(t *testing.T) {
	f := newFixture(t)
	out := searchResult(t, f, map[string]any{"query": "oi", "limit": 31, "offset": 1500})
	if len(out.Results) > 30 {
		t.Errorf("results = %d, want at most 30", len(out.Results))
	}
	joined := strings.Join(out.Notes, "|")
	if !strings.Contains(joined, "limit reduzido de 31 para o máximo de 30") || !strings.Contains(joined, "offset reduzido de 1500 para o máximo de 1000") {
		t.Errorf("notes = %v", out.Notes)
	}
	small := searchResult(t, f, map[string]any{"query": "oi", "limit": 3})
	if len(small.Results) != 3 || small.Total <= 3 {
		t.Errorf("limit 3: results=%d total=%d", len(small.Results), small.Total)
	}
	if got := errorCode(t, f.call("search_messages", map[string]any{"query": "a"})); got != string(toolerr.CodeInvalidArgument) {
		t.Errorf("one letter: code = %q", got)
	}
	if got := errorCode(t, f.call("search_messages", map[string]any{"query": "!!"})); got != string(toolerr.CodeInvalidArgument) {
		t.Errorf("punctuation only: code = %q", got)
	}
}
