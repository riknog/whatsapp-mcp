package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/riknog/whatsapp-mcp/internal/privacy"
	"github.com/riknog/whatsapp-mcp/internal/store"
)

// forbidden are the raw identifiers of the fixture: phone numbers, JID users,
// LIDs and group ids. None of them may appear in any tool output.
var forbidden = []string{
	"5511900000001", "5511900000002", "5511999887766", "5511987654321",
	"5511999990000", "5511977770001", "91234", "98765", "4321", "3333-4444",
	"100000000000001", "100000000000002", "100000000000003",
	"120363000000000001", "120363000000000002", "@s.whatsapp.net", "@lid", "@g.us",
}

// noPIICalls covers every tool with several argument sets, the success and error paths.
var noPIICalls = []struct {
	name string
	args map[string]any
}{
	{"whatsapp_status", nil},
	{"list_new_messages", nil},
	{"list_new_messages", map[string]any{"include_groups": true, "max_chats": 30, "per_chat": 20, "peek": true}},
	{"list_new_messages", map[string]any{"category": "Família", "include_groups": true, "peek": true}},
	{"list_new_messages", map[string]any{"category": "Sem categoria", "peek": true}},
	{"list_chats", nil},
	{"list_chats", map[string]any{"unread_only": true, "limit": 50}},
	{"list_chats", map[string]any{"category": "Grupos"}},
	{"get_chat_messages", map[string]any{"contact": "Mãe", "limit": 50}},
	{"get_chat_messages", map[string]any{"contact": "Desconhecido", "limit": 50}},
	{"get_chat_messages", map[string]any{"contact": "Família Silva", "limit": 50}},
	{"get_chat_messages", map[string]any{"contact": "Mãe", "around_message_id": "3EB0AAAAAA"}},
	{"get_chat_messages", map[string]any{"contact": "5511900000001"}},
	{"get_chat_messages", map[string]any{"contact": "Ana"}},
	{"get_chat_messages", map[string]any{"contact": "Banco Exemplo"}},
	{"search_messages", map[string]any{"query": "me liga", "limit": 30}},
	{"search_messages", map[string]any{"query": "orcamento"}},
	{"search_messages", map[string]any{"query": "91234", "contact": "Desconhecido"}},
	{"search_messages", map[string]any{"query": "saldo"}},
	{"list_contacts", map[string]any{"limit": 200, "include_groups": true}},
	{"list_contacts", map[string]any{"category": "Grupos"}},
	{"search_contacts", map[string]any{"query": "a", "limit": 30}},
	{"search_contacts", map[string]any{"query": "5511900000001"}},
	{"list_categories", nil},
	{"read_media", map[string]any{"contact": "Mãe", "message_id": "3EB0AAAAAA"}},
	{"read_media", map[string]any{"contact": "5511900000001", "message_id": "3EB0AAAAAA"}},
	{"read_media", map[string]any{"contact": "Banco Exemplo", "message_id": "3EB0AAAAAA"}},
	// Write tools last: they change the state the read calls above expect.
	{"send_message", map[string]any{"contact": "Desconhecido", "text": "Oi"}},
	{"send_message", map[string]any{"contact": "5511900000001", "text": "Oi"}},
	{"send_message", map[string]any{"contact": "Família Silva", "text": "Oi"}},
	{"send_message", map[string]any{"contact": "Contato", "text": "Oi"}},
	{"share_contact", map[string]any{"to": "Desconhecido", "contact": "João Ávila"}},
	{"share_contact", map[string]any{"to": "Mãe", "contact": "Desconhecido"}},
	{"mark_as_read", map[string]any{"contact": "Desconhecido"}},
	{"mark_as_read", map[string]any{"contact": "Família Silva"}},
	{"set_contact_category", map[string]any{"contact": "Desconhecido", "category": "Fornecedores", "action": "add"}},
	{"set_contact_category", map[string]any{"contact": "Desconhecido", "category": "11 91234-5678", "action": "add"}},
	{"list_categories", nil},
}

// TestNoPIIAcrossAllTools runs every tool and checks each output with
// privacy.AssertNoPII (done by the fixture on every call), and checks that no
// raw identifier of the fixture is in the text of any output.
func TestNoPIIAcrossAllTools(t *testing.T) {
	f := newSendFixture(t, nil)
	f.drive()
	covered := map[string]bool{}
	for _, c := range noPIICalls {
		res := f.call(c.name, c.args)
		covered[c.name] = true
		text := textOf(res)
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("%s: marshal: %v", c.name, err)
		}
		all := text + string(raw)
		for _, bad := range forbidden {
			if strings.Contains(all, bad) {
				t.Errorf("%s %v: output contains %q", c.name, c.args, bad)
			}
		}
	}
	for _, name := range wantTools {
		if !covered[name] {
			t.Errorf("tool %s not covered by the no-PII test", name)
		}
	}
}

// TestNoPIIWithFixtureHasTheHardCases checks that the fixture really holds the
// hard cases (otherwise the test above proves nothing).
func TestNoPIIWithFixtureHasTheHardCases(t *testing.T) {
	f := newFixture(t)
	if got := len(f.direct) + len(fxNamedOnly) + 1 + 1; got != 50 {
		t.Errorf("contacts in fixture = %d, want 50", got)
	}
	if n := len(f.allMessages()); n != 2000 {
		t.Errorf("messages = %d, want 2000", n)
	}
	if len(f.groups) != 2 || f.hidden.name == "" {
		t.Errorf("groups=%d hidden=%q", len(f.groups), f.hidden.name)
	}
	phones := 0
	for _, m := range f.allMessages() {
		if privacy.RedactText(m.Text) != m.Text {
			phones++
		}
	}
	if phones == 0 {
		t.Errorf("no message text holds a phone number")
	}
	// The raw text of the fixture does contain the identifiers, so the absence
	// checks above are meaningful.
	joined := ""
	for _, m := range f.allMessages() {
		joined += m.Text
	}
	if !strings.Contains(joined, "98765") || !strings.Contains(joined, "5511987654321") {
		t.Errorf("fixture text lacks the phone numbers the test looks for")
	}
}

// TestNoPIIDetectsAPhoneInAnOutput checks that the helper flags a leak, so a
// silent pass cannot hide one.
func TestNoPIIDetectsAPhoneInAnOutput(t *testing.T) {
	if err := privacy.AssertNoPII(map[string]any{"text": "me liga no 11 98765-4321"}); err == nil {
		t.Fatalf("AssertNoPII accepted an unmasked phone")
	}
	if err := privacy.AssertNoPII(map[string]any{"text": "me liga no 11 9****-**21"}); err != nil {
		t.Fatalf("AssertNoPII flagged a masked phone: %v", err)
	}
}

// TestNoPIIInLabelNamesAndSnippetEdges covers two free-text paths: a label name
// typed with a phone number, and a phone number cut by the edge of the FTS
// snippet window (a short digit run that redaction alone would miss).
func TestNoPIIInLabelNamesAndSnippetEdges(t *testing.T) {
	f := newFixture(t)
	c := f.direct[3]
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.st.UpsertLabel(f.ctx, store.Label{ID: "local:fone", Name: "Clientes 11 98765-4321", Source: "local"}))
	must(f.st.SetChatLabel(f.ctx, c.jid, "local:fone", true))
	_, err := f.st.InsertMessage(f.ctx, store.Message{ChatJID: c.jid, ID: "3EB0EDGE", SenderJID: c.jid,
		TS: fixtureNow.Unix() - 10, Kind: "text", Text: "zzmark8 a b c d e f g h i 11 98765 4321 fim"})
	must(err)

	calls := []struct {
		name string
		args map[string]any
	}{
		{"list_categories", nil},
		{"list_contacts", map[string]any{"limit": 200}},
		{"list_chats", map[string]any{"limit": 50}},
		{"list_new_messages", map[string]any{"peek": true, "max_chats": 30}},
		{"search_contacts", map[string]any{"query": c.name}},
		{"search_messages", map[string]any{"query": "zzmark8"}},
		{"get_chat_messages", map[string]any{"contact": c.ref, "limit": 5}},
	}
	for _, cl := range calls {
		res := f.call(cl.name, cl.args)
		raw, _ := json.Marshal(res.StructuredContent)
		all := textOf(res) + string(raw)
		for _, bad := range []string{"98765", "4321"} {
			if strings.Contains(all, bad) {
				t.Errorf("%s: output contains %q: %s", cl.name, bad, all)
			}
		}
	}
	var out struct {
		Results []struct{ Snippet string } `json:"results"`
	}
	decodeInto(t, f.call("search_messages", map[string]any{"query": "zzmark8"}), &out)
	if len(out.Results) != 1 || !strings.Contains(out.Results[0].Snippet, "«zzmark8»") {
		t.Errorf("snippet = %+v, want the match marked", out.Results)
	}
}
