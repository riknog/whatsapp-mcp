package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/riknog/whatsapp-mcp/internal/config"
	"github.com/riknog/whatsapp-mcp/internal/sendqueue"
	"github.com/riknog/whatsapp-mcp/internal/wa"
)

// shareablePhone is the number the fake account knows for the shareable
// contact (fixture index 2). It must reach the vCard and nothing else.
const shareablePhone = "+55 11 90000-0003"

// sendFixture is the fixture with a real send queue over its store and fake
// account. The queue waits on the fake clock; drive lets it run.
type sendFixture struct {
	*fixture
	q *sendqueue.Queue
}

// newSendFixture starts a queue and reconnects the server with it. mutate
// changes the config first. The clock is not driven until drive is called.
func newSendFixture(t *testing.T, mutate func(*config.Config)) *sendFixture {
	t.Helper()
	f := newFixture(t)
	cfg := config.Defaults()
	cfg.Send.WaitTimeoutS = 3600 // the driver never fires it; Submit waits for the send
	if mutate != nil {
		mutate(&cfg)
	}
	f.cfg = cfg
	f.wa.SetPhone(wa.JID(f.direct[2].jid), shareablePhone)
	q := sendqueue.New(cfg.Send, f.st, f.wa, f.clk, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	if err := q.Start(ctx); err != nil {
		cancel()
		t.Fatalf("queue Start: %v", err)
	}
	t.Cleanup(func() { cancel(); q.Wait() })
	f.connect(func(d *Deps) { d.Sender = q; d.Queue = q })
	return &sendFixture{fixture: f, q: q}
}

// drive fires the short timers of the fake clock one at a time, at their
// deadlines, until the test ends. Submit's wait timer (an hour) is never fired.
func (f *sendFixture) drive() {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			default:
			}
			if d, ok := f.clk.Next(); ok && d <= time.Minute {
				f.clk.Advance(d)
				continue
			}
			runtime.Gosched()
		}
	}()
	f.t.Cleanup(func() { close(done); <-finished })
}

func wantCode(t *testing.T, res *mcp.CallToolResult, code string) {
	t.Helper()
	if got := errorCode(t, res); got != code {
		t.Fatalf("code = %q, want %q (%s)", got, code, textOf(res))
	}
}

func TestSendMessageDeliversAndShowsOnceAsMe(t *testing.T) {
	f := newSendFixture(t, nil)
	f.drive()
	mae := f.direct[0]

	res := f.call("send_message", map[string]any{"contact": "Mãe", "text": "Oi mãe, chego às 19h"})
	mustOK(t, res)
	var out sendOut
	decodeInto(t, res, &out)
	if out.Status != sendqueue.StatusSent || out.MessageID == "" || out.Contact != "Mãe" || out.ContactRef != mae.ref {
		t.Fatalf("send_message = %+v", out)
	}
	sent := f.wa.Sent()
	if len(sent) != 1 || string(sent[0].Chat) != mae.jid || sent[0].Text != "Oi mãe, chego às 19h" {
		t.Fatalf("fake sent = %+v", sent)
	}

	res = f.call("get_chat_messages", map[string]any{"contact": "Mãe", "limit": 5})
	mustOK(t, res)
	var msgs chatMessagesOut
	decodeInto(t, res, &msgs)
	n := 0
	for _, m := range msgs.Messages {
		if m.ID == out.MessageID {
			n++
			if m.From != "me" || m.FromRef != "" || m.Text != "Oi mãe, chego às 19h" {
				t.Errorf("sent message shown as %+v", m)
			}
		}
	}
	if n != 1 {
		t.Fatalf("sent message shown %d times, want 1: %+v", n, msgs.Messages)
	}

	// Answering is reading: the chat leaves the unread list.
	if unreadRefs(t, f.fixture)[mae.ref] || newRefs(t, f.fixture)[mae.ref] {
		t.Error("chat still unread after the owner's answer")
	}
}

func TestSendMessageWithReplyTo(t *testing.T) {
	f := newSendFixture(t, nil)
	f.drive()
	quoted := f.msgs[f.direct[0].jid][46].ID

	res := f.call("send_message", map[string]any{"contact": "Mãe", "text": "Combinado", "reply_to": quoted})
	mustOK(t, res)
	if sent := f.wa.Sent(); len(sent) != 1 || sent[0].QuotedID != quoted {
		t.Fatalf("fake sent = %+v", sent)
	}

	// An id of another chat is refused before anything is queued.
	other := f.msgs[f.direct[1].jid][46].ID
	res = f.call("send_message", map[string]any{"contact": "Mãe", "text": "Outra", "reply_to": other})
	wantCode(t, res, "invalid_argument")
	if len(f.wa.Sent()) != 1 {
		t.Fatal("refused reply reached WhatsApp")
	}
}

func TestSendMessageRefusals(t *testing.T) {
	f := newSendFixture(t, func(c *config.Config) { c.Send.Policy = "reply_only" })
	f.drive()
	cases := []struct {
		name string
		args map[string]any
		code string
	}{
		{"ambiguous", map[string]any{"contact": "Contato", "text": "oi"}, "ambiguous_contact"},
		{"phone", map[string]any{"contact": "5511900000001", "text": "oi"}, "phone_not_allowed"},
		{"jid", map[string]any{"contact": "5511900000001@s.whatsapp.net", "text": "oi"}, "phone_not_allowed"},
		{"group", map[string]any{"contact": "Família Silva", "text": "oi"}, "group_send_disabled"},
		{"hidden", map[string]any{"contact": "Banco Exemplo", "text": "oi"}, "chat_hidden"},
		{"unknown", map[string]any{"contact": "Ninguém Assim", "text": "oi"}, "contact_not_found"},
		{"no contact", map[string]any{"text": "oi"}, "invalid_argument"},
		{"no text", map[string]any{"contact": "Mãe", "text": "  "}, "invalid_argument"},
		{"too long", map[string]any{"contact": "Mãe", "text": strings.Repeat("a", 4097)}, "message_too_long"},
		// reply_only: Ana Paula has no chat, so she never wrote.
		{"reply_only", map[string]any{"contact": "Ana Paula", "text": "oi"}, "policy_reply_only"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantCode(t, f.call("send_message", c.args), c.code)
		})
	}
	// The ambiguity error lists the candidates, by name and contact_ref.
	var amb sendOut
	decodeInto(t, f.call("send_message", map[string]any{"contact": "Contato", "text": "oi"}), &amb)
	cands, _ := amb.Error.Details["candidates"].([]any)
	if len(cands) != 5 {
		t.Fatalf("candidates = %v", amb.Error.Details)
	}
	for _, c := range cands {
		m, _ := c.(map[string]any)
		if name, _ := m["name"].(string); !strings.HasPrefix(name, "Contato ") || m["contact_ref"] == "" {
			t.Errorf("candidate = %v", m)
		}
	}
	if n := len(f.wa.Sent()); n != 0 {
		t.Fatalf("%d refused messages reached WhatsApp", n)
	}
	// Under reply_only a chat with a recent inbound message is fine.
	mustOK(t, f.call("send_message", map[string]any{"contact": "Pai", "text": "Já vou"}))
}

func TestSendMessageGates(t *testing.T) {
	t.Run("send disabled", func(t *testing.T) {
		f := newSendFixture(t, func(c *config.Config) { c.Send.Enabled = false })
		wantCode(t, f.call("send_message", map[string]any{"contact": "Mãe", "text": "oi"}), "send_disabled")
		wantCode(t, f.call("share_contact", map[string]any{"to": "Mãe", "contact": "João Ávila"}), "send_disabled")
	})
	t.Run("no queue", func(t *testing.T) {
		f := newFixture(t)
		wantCode(t, f.call("send_message", map[string]any{"contact": "Mãe", "text": "oi"}), "send_disabled")
	})
	t.Run("disconnected", func(t *testing.T) {
		f := newSendFixture(t, nil)
		f.wa.SetConnected(false)
		wantCode(t, f.call("send_message", map[string]any{"contact": "Mãe", "text": "oi"}), "disconnected")
	})
	t.Run("not logged in", func(t *testing.T) {
		f := newSendFixture(t, nil)
		f.wa.SetLoggedIn(false)
		wantCode(t, f.call("send_message", map[string]any{"contact": "Mãe", "text": "oi"}), "not_logged_in")
	})
	t.Run("groups allowed", func(t *testing.T) {
		f := newSendFixture(t, func(c *config.Config) { c.Send.AllowGroups = true })
		f.drive()
		mustOK(t, f.call("send_message", map[string]any{"contact": "Família Silva", "text": "Bom dia"}))
		if sent := f.wa.Sent(); len(sent) != 1 || string(sent[0].Chat) != fxGroupFamily {
			t.Fatalf("fake sent = %+v", sent)
		}
	})
}

// TestParallelSendsKeepOrder sends three messages to Mãe and one to Pai from
// concurrent calls. Each call enters the queue before the next starts, and the
// fake account receives them in that order.
func TestParallelSendsKeepOrder(t *testing.T) {
	f := newSendFixture(t, nil)
	calls := []struct{ to, text string }{
		{"Mãe", "um"}, {"Mãe", "dois"}, {"Mãe", "três"}, {"Pai", "quatro"},
	}
	type result struct {
		res *mcp.CallToolResult
		err error
	}
	results := make([]chan result, len(calls))
	for i, c := range calls {
		results[i] = make(chan result, 1)
		go func(ch chan result, to, text string) {
			res, err := f.cs.CallTool(f.ctx, &mcp.CallToolParams{Name: "send_message",
				Arguments: map[string]any{"contact": to, "text": text}})
			ch <- result{res, err}
		}(results[i], c.to, c.text)
		// The clock stands still, so nothing is sent yet: wait until this call is queued.
		waitFor(t, func() bool { return f.q.Stats().Pending == i+1 })
	}
	f.drive()
	for i, ch := range results {
		r := <-ch
		if r.err != nil {
			t.Fatalf("call %d: %v", i, r.err)
		}
		assertResultNoPII(t, "send_message", r.res)
		mustOK(t, r.res)
	}
	sent := f.wa.Sent()
	if len(sent) != len(calls) {
		t.Fatalf("sent %d, want %d", len(sent), len(calls))
	}
	for i, c := range calls {
		wantJID := f.direct[0].jid
		if c.to == "Pai" {
			wantJID = f.direct[1].jid
		}
		if sent[i].Text != c.text || string(sent[i].Chat) != wantJID {
			t.Errorf("sent[%d] = %q, want %q to %s", i, sent[i].Text, c.text, c.to)
		}
	}
	// Switching from Mãe to Pai waits the switch cooldown (1.5 s to 3.5 s),
	// on top of the typing time.
	lo := time.Duration(f.cfg.Send.SwitchCooldownMS.Min) * time.Millisecond
	if gap := sent[3].At.Sub(sent[2].At); gap < lo {
		t.Errorf("gap before the switch = %v, want at least %v", gap, lo)
	}
	for i := 1; i < len(sent); i++ {
		if !sent[i].At.After(sent[i-1].At) {
			t.Errorf("sent[%d] at %v, not after sent[%d] at %v", i, sent[i].At, i-1, sent[i-1].At)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSendFailuresAreMapped(t *testing.T) {
	f := newSendFixture(t, nil)
	f.drive()

	f.wa.FailSendText(errors.New("wa: recusado"))
	wantCode(t, f.call("send_message", map[string]any{"contact": "Mãe", "text": "primeira"}), "send_failed")

	f.wa.FailSendText(wa.ErrSendUncertain)
	res := f.call("send_message", map[string]any{"contact": "Pai", "text": "segunda"})
	wantCode(t, res, "send_uncertain")
	if !strings.Contains(textOf(res), "pode ter sido entregue") {
		t.Errorf("send_uncertain message = %q", textOf(res))
	}
	if n := f.wa.Attempts(); n != 2 {
		t.Errorf("attempts = %d, want 2 (no retry of a permanent or uncertain failure)", n)
	}
}

func TestShareContact(t *testing.T) {
	f := newSendFixture(t, nil)
	f.drive()
	joao := f.direct[2]

	res := f.call("share_contact", map[string]any{"to": "Mãe", "contact": "João Ávila"})
	mustOK(t, res)
	var out shareOut
	decodeInto(t, res, &out)
	if out.Status != sendqueue.StatusSent || out.To != "Mãe" || out.ToRef != f.direct[0].ref ||
		out.Shared == nil || out.Shared.Name != "João Ávila" || out.Shared.ContactRef != joao.ref {
		t.Fatalf("share_contact = %+v", out)
	}
	cards := f.wa.Contacts()
	if len(cards) != 1 || string(cards[0].Chat) != f.direct[0].jid || cards[0].DisplayName != "João Ávila" {
		t.Fatalf("fake cards = %+v", cards)
	}
	if !strings.Contains(cards[0].VCard, "waid=5511900000003:+5511900000003") ||
		!strings.Contains(cards[0].VCard, "FN:João Ávila") {
		t.Errorf("vCard has no phone number: %q", cards[0].VCard)
	}

	// The card shows in the history as a contact from me, without the number.
	res = f.call("get_chat_messages", map[string]any{"contact": "Mãe", "limit": 3})
	var msgs chatMessagesOut
	decodeInto(t, res, &msgs)
	last := msgs.Messages[len(msgs.Messages)-1]
	if last.ID != out.MessageID || last.From != "me" || last.Type != "contact" {
		t.Errorf("card in history = %+v", last)
	}

	refusals := []struct {
		name string
		args map[string]any
		code string
	}{
		{"not shareable", map[string]any{"to": "Mãe", "contact": "Pai"}, "contact_not_shareable"},
		{"group shared", map[string]any{"to": "Mãe", "contact": "Família Silva"}, "invalid_argument"},
		{"to group", map[string]any{"to": "Projeto Alfa", "contact": "João Ávila"}, "group_send_disabled"},
		{"to self", map[string]any{"to": "João Ávila", "contact": "João Ávila"}, "invalid_argument"},
		{"phone", map[string]any{"to": "Mãe", "contact": "+55 11 90000-0003"}, "phone_not_allowed"},
		{"missing to", map[string]any{"contact": "João Ávila"}, "invalid_argument"},
		{"ambiguous to", map[string]any{"to": "Contato", "contact": "João Ávila"}, "ambiguous_contact"},
	}
	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) {
			wantCode(t, f.call("share_contact", c.args), c.code)
		})
	}
	if n := len(f.wa.Contacts()); n != 1 {
		t.Fatalf("%d cards sent, want 1", n)
	}
}

func TestShareContactConfig(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		f := newSendFixture(t, func(c *config.Config) { c.Share.Enabled = false })
		wantCode(t, f.call("share_contact", map[string]any{"to": "Mãe", "contact": "João Ávila"}), "share_disabled")
	})
	t.Run("no allowlist", func(t *testing.T) {
		f := newSendFixture(t, func(c *config.Config) { c.Share.RequireAllowlist = false })
		f.drive()
		f.wa.SetPhone(wa.JID(f.direct[1].jid), "+55 11 90000-0002")
		mustOK(t, f.call("share_contact", map[string]any{"to": "Mãe", "contact": "Pai"}))
	})
}

func TestMarkAsRead(t *testing.T) {
	f := newSendFixture(t, nil)
	pai := f.direct[1]

	res := f.call("mark_as_read", map[string]any{"contact": "Pai"})
	mustOK(t, res)
	var out markReadOut
	decodeInto(t, res, &out)
	want := f.unreadInbound(pai)
	if out.Marked != want || out.Contact != "Pai" || out.ContactRef != pai.ref {
		t.Fatalf("mark_as_read = %+v, want %d marked", out, want)
	}
	reads := f.wa.Reads()
	if len(reads) != 1 || string(reads[0].Chat) != pai.jid || reads[0].Sender != "" {
		t.Fatalf("fake reads = %+v", reads)
	}
	readAt := f.msgs[pai.jid][20].TS
	var wantIDs []string
	for _, m := range f.msgs[pai.jid] {
		if !m.FromMe && m.TS > readAt {
			wantIDs = append(wantIDs, m.ID)
		}
	}
	if strings.Join(reads[0].IDs, ",") != strings.Join(wantIDs, ",") {
		t.Errorf("read ids = %v, want %v", reads[0].IDs, wantIDs)
	}
	if unreadRefs(t, f.fixture)[pai.ref] {
		t.Error("chat still unread after mark_as_read")
	}
	if newRefs(t, f.fixture)[pai.ref] {
		t.Error("list_new_messages still brings the chat after mark_as_read")
	}

	// Again: nothing left, no receipt.
	res = f.call("mark_as_read", map[string]any{"contact": "Pai"})
	decodeInto(t, res, &out)
	if out.Marked != 0 || len(f.wa.Reads()) != 1 {
		t.Errorf("second mark_as_read = %+v, reads %d", out, len(f.wa.Reads()))
	}

	// A contact without a chat has nothing to mark.
	res = f.call("mark_as_read", map[string]any{"contact": "Ana Paula"})
	decodeInto(t, res, &out)
	if res.IsError || out.Marked != 0 {
		t.Errorf("mark_as_read without chat = %+v", out)
	}
}

func TestMarkAsReadGroupSendsOneReceiptPerAuthor(t *testing.T) {
	f := newSendFixture(t, nil)
	res := f.call("mark_as_read", map[string]any{"contact": "Família Silva"})
	mustOK(t, res)
	reads := f.wa.Reads()
	if len(reads) < 2 {
		t.Fatalf("reads = %d, want one per author", len(reads))
	}
	total := 0
	for _, r := range reads {
		if string(r.Chat) != fxGroupFamily || r.Sender == "" {
			t.Errorf("group receipt without author: %+v", r)
		}
		total += len(r.IDs)
	}
	var out markReadOut
	decodeInto(t, res, &out)
	if out.Marked != total {
		t.Errorf("marked = %d, receipts cover %d", out.Marked, total)
	}
}

func TestMarkAsReadRefusals(t *testing.T) {
	f := newSendFixture(t, nil)
	wantCode(t, f.call("mark_as_read", map[string]any{"contact": "Banco Exemplo"}), "chat_hidden")
	wantCode(t, f.call("mark_as_read", map[string]any{"contact": "5511900000002"}), "phone_not_allowed")
	f.wa.SetConnected(false)
	wantCode(t, f.call("mark_as_read", map[string]any{"contact": "Mãe"}), "disconnected")
	if len(f.wa.Reads()) != 0 {
		t.Fatal("receipt sent on a refusal")
	}

	g := newSendFixture(t, func(c *config.Config) { c.Read.MarkReadEnabled = false })
	wantCode(t, g.call("mark_as_read", map[string]any{"contact": "Mãe"}), "invalid_argument")
}

// unreadRefs returns the contact_refs that list_chats shows as unread.
func unreadRefs(t *testing.T, f *fixture) map[string]bool {
	t.Helper()
	res := f.call("list_chats", map[string]any{"unread_only": true, "limit": 50})
	mustOK(t, res)
	var out struct {
		Chats []struct {
			ContactRef  string `json:"contact_ref"`
			UnreadCount int64  `json:"unread_count"`
		} `json:"chats"`
	}
	decodeInto(t, res, &out)
	refs := map[string]bool{}
	for _, c := range out.Chats {
		if c.UnreadCount > 0 {
			refs[c.ContactRef] = true
		}
	}
	return refs
}

// newRefs returns the contact_refs that list_new_messages brings, without consuming them.
func newRefs(t *testing.T, f *fixture) map[string]bool {
	t.Helper()
	res := f.call("list_new_messages", map[string]any{"include_groups": true, "max_chats": 30, "peek": true})
	mustOK(t, res)
	var out newMessagesOut
	decodeInto(t, res, &out)
	refs := map[string]bool{}
	for _, c := range out.Chats {
		refs[c.ContactRef] = true
	}
	return refs
}

func TestSetContactCategory(t *testing.T) {
	f := newSendFixture(t, nil)

	res := f.call("set_contact_category", map[string]any{"contact": "Pai", "category": "Fornecedores", "action": "add"})
	mustOK(t, res)
	var out categoryChangeOut
	decodeInto(t, res, &out)
	if out.Contact != "Pai" || !hasString(out.Categories, "Fornecedores") || !hasString(out.Categories, "Família") {
		t.Fatalf("add = %+v", out)
	}
	if !categoryListed(t, f.fixture, "Fornecedores") {
		t.Error("new category not in list_categories")
	}

	// Adding again is a no-op; a different spelling hits the same category.
	res = f.call("set_contact_category", map[string]any{"contact": "Pai", "category": "fornecedores", "action": "add"})
	decodeInto(t, res, &out)
	if n := countString(out.Categories, "Fornecedores"); n != 1 {
		t.Errorf("category listed %d times after a repeated add: %+v", n, out.Categories)
	}

	// A contact without a chat can get a category.
	mustOK(t, f.call("set_contact_category", map[string]any{"contact": "Ana Paula", "category": "Clientes", "action": "add"}))
	res = f.call("list_contacts", map[string]any{"category": "Clientes", "limit": 200})
	if !strings.Contains(textOf(res)+mustJSON(t, res), "Ana Paula") {
		t.Error("Ana Paula not listed under Clientes")
	}

	res = f.call("set_contact_category", map[string]any{"contact": "Pai", "category": "Fornecedores", "action": "remove"})
	decodeInto(t, res, &out)
	if hasString(out.Categories, "Fornecedores") {
		t.Errorf("remove kept the category: %+v", out)
	}

	refusals := []struct {
		name string
		args map[string]any
	}{
		{"whatsapp label", map[string]any{"contact": "Pai", "category": "Trabalho", "action": "add"}},
		{"remove whatsapp label", map[string]any{"contact": "Pai", "category": "Família", "action": "remove"}},
		{"bad action", map[string]any{"contact": "Pai", "category": "X", "action": "toggle"}},
		{"reserved", map[string]any{"contact": "Pai", "category": "Grupos", "action": "add"}},
		{"reserved none", map[string]any{"contact": "Pai", "category": "Sem categoria", "action": "add"}},
		{"empty", map[string]any{"contact": "Pai", "category": " ", "action": "add"}},
		{"too long", map[string]any{"contact": "Pai", "category": strings.Repeat("x", 51), "action": "add"}},
		{"phone as name", map[string]any{"contact": "Pai", "category": "11 91234-5678", "action": "add"}},
		{"unknown remove", map[string]any{"contact": "Pai", "category": "Nunca Existiu", "action": "remove"}},
	}
	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) {
			res := f.call("set_contact_category", c.args)
			code := errorCode(t, res)
			if code != "invalid_argument" && code != "phone_not_allowed" {
				t.Fatalf("code = %q (%s)", code, textOf(res))
			}
		})
	}
	wantCode(t, f.call("set_contact_category", map[string]any{"contact": "Banco Exemplo", "category": "X", "action": "add"}), "chat_hidden")
}

func categoryListed(t *testing.T, f *fixture, name string) bool {
	t.Helper()
	return strings.Contains(mustJSON(t, f.call("list_categories", nil)), `"`+name+`"`)
}

func mustJSON(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func hasString(l []string, s string) bool { return countString(l, s) > 0 }

func countString(l []string, s string) int {
	n := 0
	for _, x := range l {
		if x == s {
			n++
		}
	}
	return n
}
