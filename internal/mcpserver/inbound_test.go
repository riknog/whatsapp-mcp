package mcpserver

import (
	"fmt"
	"strings"
	"testing"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

// newMessagesResult decodes a list_new_messages call.
func newMessagesResult(t *testing.T, f *fixture, args map[string]any) newMessagesOut {
	t.Helper()
	res := f.call("list_new_messages", args)
	mustOK(t, res)
	var out newMessagesOut
	decodeInto(t, res, &out)
	return out
}

// TestListNewMessagesDefaultPage checks the default call: ten direct chats, the
// newest five messages of each, oldest first, with names and categories.
func TestListNewMessagesDefaultPage(t *testing.T) {
	f := newFixture(t)
	out := newMessagesResult(t, f, nil)

	if len(out.Chats) != 10 {
		t.Fatalf("chats = %d, want 10", len(out.Chats))
	}
	if out.MoreChats != fxDirect-10 {
		t.Errorf("more_chats = %d, want %d", out.MoreChats, fxDirect-10)
	}
	first := out.Chats[0]
	if first.Contact != "Mãe" || first.ContactRef != f.direct[0].ref || first.Kind != "direct" {
		t.Errorf("first chat = %+v", first)
	}
	if first.NewCount != f.unreadInbound(f.direct[0]) {
		t.Errorf("new_count = %d, want %d", first.NewCount, f.unreadInbound(f.direct[0]))
	}
	if !first.Truncated || len(first.Messages) != 5 {
		t.Errorf("truncated=%v messages=%d, want true and 5", first.Truncated, len(first.Messages))
	}
	// The newest unseen inbound message of Mãe is the 47th one minus the owner's messages.
	msgs := f.msgs[f.direct[0].jid]
	last := msgs[46]
	if got := first.Messages[len(first.Messages)-1]; got.ID != last.ID || got.From != "Mãe" || got.FromRef != f.direct[0].ref {
		t.Errorf("last message = %+v, want id %s from Mãe", got, last.ID)
	}
	for i := 1; i < len(first.Messages); i++ {
		if first.Messages[i-1].Time > first.Messages[i].Time {
			t.Errorf("messages not chronological at %d", i)
		}
	}
	if len(first.Categories) != 1 || first.Categories[0] != "Família" {
		t.Errorf("categories = %v, want [Família]", first.Categories)
	}
	// Chats are ordered by their newest unseen message: direct[i] is the i-th.
	if out.Chats[4].ContactRef != f.direct[4].ref || out.Chats[4].Contact != "Desconhecido" {
		t.Errorf("chat with only a phone number = %q, want Desconhecido", out.Chats[4].Contact)
	}
	if strings.Contains(textOf(f.call("list_new_messages", nil)), "BANCOSECRETO") {
		t.Errorf("hidden chat text reached list_new_messages")
	}
}

// TestListNewMessagesCursorAdvances checks that consumed chats are not returned
// again, and that a call with nothing new is empty.
func TestListNewMessagesCursorAdvances(t *testing.T) {
	f := newFixture(t)
	seen := map[string]bool{}
	first := newMessagesResult(t, f, map[string]any{"max_chats": 30, "per_chat": 20})
	if len(first.Chats) != 30 || first.MoreChats != fxDirect-30 {
		t.Fatalf("first call: chats=%d more=%d, want 30 and %d", len(first.Chats), first.MoreChats, fxDirect-30)
	}
	for _, c := range first.Chats {
		seen[c.ContactRef] = true
	}
	second := newMessagesResult(t, f, map[string]any{"max_chats": 30, "per_chat": 20})
	if len(second.Chats) != fxDirect-30 || second.MoreChats != 0 {
		t.Fatalf("second call: chats=%d more=%d", len(second.Chats), second.MoreChats)
	}
	for _, c := range second.Chats {
		if seen[c.ContactRef] {
			t.Errorf("chat %s returned twice", c.Contact)
		}
	}
	third := newMessagesResult(t, f, nil)
	if len(third.Chats) != 0 || third.MoreChats != 0 {
		t.Errorf("third call: chats=%d more=%d, want empty", len(third.Chats), third.MoreChats)
	}
}

// TestListNewMessagesPeekKeepsCursor checks that peek=true does not advance the cursor.
func TestListNewMessagesPeekKeepsCursor(t *testing.T) {
	f := newFixture(t)
	a := newMessagesResult(t, f, map[string]any{"peek": true})
	b := newMessagesResult(t, f, map[string]any{"peek": true})
	if len(a.Chats) != 10 || len(b.Chats) != 10 || a.Chats[0].ContactRef != b.Chats[0].ContactRef {
		t.Fatalf("peek calls differ: %d and %d chats", len(a.Chats), len(b.Chats))
	}
	c := newMessagesResult(t, f, nil)
	if len(c.Chats) != 10 || c.Chats[0].ContactRef != a.Chats[0].ContactRef {
		t.Errorf("a normal call after peek lost chats: %d", len(c.Chats))
	}
}

// TestListNewMessagesCategories checks the category filter, with a label, a
// case and accent variant, the implicit categories, and an unknown name.
func TestListNewMessagesCategories(t *testing.T) {
	f := newFixture(t)

	// peek keeps the cursors where they are, so each call sees the same chats.
	fam := newMessagesResult(t, f, map[string]any{"category": "familia", "peek": true})
	names := contactNames(fam)
	if len(names) != 2 || !names["Mãe"] || !names["Pai"] {
		t.Errorf("Família (no groups) = %v, want Mãe and Pai", names)
	}

	withGroups := newMessagesResult(t, f, map[string]any{"category": "Família", "include_groups": true, "peek": true})
	if n := contactNames(withGroups); len(n) != 3 || !n["Família Silva"] {
		t.Errorf("Família with groups = %v", n)
	}

	groups := newMessagesResult(t, f, map[string]any{"category": "Grupos", "peek": true})
	if len(groups.Chats) != fxGroups {
		t.Fatalf("Grupos = %d chats, want %d", len(groups.Chats), fxGroups)
	}
	for _, c := range groups.Chats {
		if c.Kind != "group" {
			t.Errorf("Grupos returned a %s chat", c.Kind)
		}
	}

	none := 0
	for _, c := range f.direct {
		if len(c.labels) == 0 {
			none++
		}
	}
	semCat := newMessagesResult(t, f, map[string]any{"category": "Sem categoria", "peek": true})
	if len(semCat.Chats) != min(10, none) || semCat.MoreChats != none-min(10, none) {
		t.Errorf("Sem categoria: chats=%d more=%d, want %d chats of %d", len(semCat.Chats), semCat.MoreChats, min(10, none), none)
	}

	res := f.call("list_new_messages", map[string]any{"category": "Inexistente"})
	if errorCode(t, res) != string(toolerr.CodeInvalidArgument) {
		t.Errorf("unknown category: want invalid_argument")
	}
}

// TestListNewMessagesLimitsAreClampedWithNotes checks the hard maximums and their notes.
func TestListNewMessagesLimitsAreClampedWithNotes(t *testing.T) {
	f := newFixture(t)
	out := newMessagesResult(t, f, map[string]any{"max_chats": 31, "per_chat": 99})
	if len(out.Chats) != 30 {
		t.Errorf("chats = %d, want 30", len(out.Chats))
	}
	if len(out.Chats[0].Messages) != 20 {
		t.Errorf("messages = %d, want 20", len(out.Chats[0].Messages))
	}
	joined := strings.Join(out.Notes, "|")
	for _, want := range []string{"max_chats reduzido de 31 para o máximo de 30.", "per_chat reduzido de 99 para o máximo de 20."} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes = %q, want %q", joined, want)
		}
	}
}

// TestListNewMessagesGroupSenders checks the sender of group messages: a LID
// with an alias shows the contact, and one without a contact shows Desconhecido.
func TestListNewMessagesGroupSenders(t *testing.T) {
	f := newFixture(t)
	out := newMessagesResult(t, f, map[string]any{"include_groups": true, "max_chats": 30, "per_chat": 20})
	if len(out.Chats) < fxGroups {
		t.Fatalf("chats = %d", len(out.Chats))
	}
	checked := 0
	for _, c := range out.Chats {
		if c.Kind != "group" {
			continue
		}
		for _, m := range c.Messages {
			from, ref := f.expectFrom(f.findMessage(m.ID))
			if m.From != from || m.FromRef != ref {
				t.Errorf("message %s from=%q ref=%q, want %q %q", m.ID, m.From, m.FromRef, from, ref)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatalf("no group message checked")
	}
	if checked < 2 {
		t.Errorf("only %d group messages checked", checked)
	}
}

// TestListNewMessagesNewCountsAndReadPoint checks that messages before the
// owner's read point are not counted (Pai had read up to message 20).
func TestListNewMessagesNewCountsAndReadPoint(t *testing.T) {
	f := newFixture(t)
	out := newMessagesResult(t, f, map[string]any{"max_chats": 30, "per_chat": 20})
	for _, c := range out.Chats {
		if c.ContactRef == f.direct[1].ref && c.NewCount != f.unreadInbound(f.direct[1]) {
			t.Errorf("Pai new_count = %d, want %d", c.NewCount, f.unreadInbound(f.direct[1]))
		}
	}
}

// contactNames returns the set of chat names of a list_new_messages output.
func contactNames(out newMessagesOut) map[string]bool {
	m := map[string]bool{}
	for _, c := range out.Chats {
		m[c.Contact] = true
	}
	return m
}

// TestListNewMessagesNotesTheCutMessages: messages cut by per_chat are consumed
// too, so a note says how many and how to read them.
func TestListNewMessagesNotesTheCutMessages(t *testing.T) {
	f := newFixture(t)
	out := newMessagesResult(t, f, map[string]any{"max_chats": 1, "per_chat": 3})
	if len(out.Chats) != 1 || !out.Chats[0].Truncated {
		t.Fatalf("chats = %+v", out.Chats)
	}
	c := out.Chats[0]
	want := fmt.Sprintf("%s: %d mensagens novas mais antigas", c.Contact, c.NewCount-3)
	if j := strings.Join(out.Notes, "|"); !strings.Contains(j, want) || !strings.Contains(j, c.ContactRef) {
		t.Errorf("notes = %q, want %q and the ref", j, want)
	}
	peek := newMessagesResult(t, f, map[string]any{"max_chats": 1, "per_chat": 3, "peek": true})
	for _, n := range peek.Notes {
		if strings.Contains(n, "já contam como vistas") {
			t.Errorf("peek notes say the messages were consumed: %q", n)
		}
	}
}

// TestHugeLimitIsNotEchoed: a requested value that could be a phone number is
// left out of the note.
func TestHugeLimitIsNotEchoed(t *testing.T) {
	f := newFixture(t)
	out := newMessagesResult(t, f, map[string]any{"max_chats": 5511987654321, "peek": true})
	j := strings.Join(out.Notes, "|")
	if strings.Contains(j, "5511987654321") || !strings.Contains(j, "max_chats reduzido para o máximo de 30.") {
		t.Errorf("notes = %q", j)
	}
}

func TestSearchRejectsPhoneQuery(t *testing.T) {
	f := newFixture(t)
	for _, q := range []string{"5511987654321", "11 98765-4321", "x@s.whatsapp.net"} {
		if code := errorCode(t, f.call("search_messages", map[string]any{"query": q})); code != "phone_not_allowed" {
			t.Errorf("query %q: code = %s, want phone_not_allowed", q, code)
		}
	}
}
