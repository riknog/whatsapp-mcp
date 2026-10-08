package mcpserver

import (
	"strings"
	"testing"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

type chatsResult struct {
	Chats []chatOut `json:"chats"`
	Total int       `json:"total"`
	Notes []string  `json:"notes"`
}

func listChats(t *testing.T, f *fixture, args map[string]any) chatsResult {
	t.Helper()
	res := f.call("list_chats", args)
	mustOK(t, res)
	var out chatsResult
	decodeInto(t, res, &out)
	return out
}

func chatNamesOf(out chatsResult) []string {
	var names []string
	for _, c := range out.Chats {
		names = append(names, c.Contact)
	}
	return names
}

// TestListChatsOrderAndTotals checks the default listing: groups and direct
// chats, most recent first, without hidden chats.
func TestListChatsOrderAndTotals(t *testing.T) {
	f := newFixture(t)
	out := listChats(t, f, nil)
	if out.Total != fxDirect+fxGroups {
		t.Fatalf("total = %d, want %d (hidden chat excluded)", out.Total, fxDirect+fxGroups)
	}
	if len(out.Chats) != 20 {
		t.Fatalf("chats = %d, want the default 20", len(out.Chats))
	}
	names := chatNamesOf(out)
	if names[0] != "Família Silva" || names[1] != "Projeto Alfa" || names[2] != "Mãe" {
		t.Errorf("order = %v, want the groups first, then Mãe", names[:3])
	}
	for _, n := range names {
		if n == "Banco Exemplo" {
			t.Errorf("hidden chat listed")
		}
	}
	mae := out.Chats[2]
	if mae.ContactRef != f.direct[0].ref || mae.Kind != "direct" || mae.LastMessageAgo != "há 12 min" {
		t.Errorf("Mãe = %+v", mae)
	}
	if mae.UnreadCount != int64(f.unreadInbound(f.direct[0])) {
		t.Errorf("unread = %d, want %d", mae.UnreadCount, f.unreadInbound(f.direct[0]))
	}
	if strings.Contains(mae.LastMessagePreview, "5511999887766") || !strings.Contains(mae.LastMessagePreview, "<jid>") {
		t.Errorf("preview = %q, want the JID replaced by <jid>", mae.LastMessagePreview)
	}
	group := out.Chats[0]
	if group.Kind != "group" || len(group.Categories) != 2 || group.Categories[0] != "Família" || group.Categories[1] != "Grupos" {
		t.Errorf("group = %+v", group)
	}
}

// TestListChatsFiltersAndPaging checks the include_groups, category, unread and paging parameters.
func TestListChatsFiltersAndPaging(t *testing.T) {
	f := newFixture(t)

	if out := listChats(t, f, map[string]any{"include_groups": false}); out.Total != fxDirect {
		t.Errorf("without groups: total = %d, want %d", out.Total, fxDirect)
	}

	fam := listChats(t, f, map[string]any{"category": "Família"})
	if fam.Total != 3 {
		t.Errorf("Família: total = %d, want 3 (Mãe, Pai and the group)", fam.Total)
	}

	paged := listChats(t, f, map[string]any{"limit": 2, "offset": 2})
	if len(paged.Chats) != 2 || paged.Chats[0].Contact != "Mãe" || paged.Chats[1].ContactRef != f.direct[1].ref {
		t.Errorf("limit 2 offset 2 = %v", chatNamesOf(paged))
	}

	// Once Mãe's messages are read on the phone, she is not unread any more.
	last := f.msgs[f.direct[0].jid][47].TS
	if err := f.st.SetOwnerReadAt(f.ctx, f.direct[0].jid, last); err != nil {
		t.Fatalf("SetOwnerReadAt: %v", err)
	}
	unread := listChats(t, f, map[string]any{"unread_only": true, "limit": 50})
	for _, c := range unread.Chats {
		if c.ContactRef == f.direct[0].ref {
			t.Errorf("read chat Mãe listed in unread_only")
		}
		if c.UnreadCount == 0 {
			t.Errorf("%s listed with unread 0", c.Contact)
		}
	}
	if unread.Total != fxDirect+fxGroups-1 {
		t.Errorf("unread total = %d, want %d", unread.Total, fxDirect+fxGroups-1)
	}
}

// TestListChatsClampsWithNote checks the hard limit of list_chats.
func TestListChatsClampsWithNote(t *testing.T) {
	f := newFixture(t)
	out := listChats(t, f, map[string]any{"limit": 60})
	if len(out.Chats) != fxDirect+fxGroups {
		t.Errorf("chats = %d, want all %d", len(out.Chats), fxDirect+fxGroups)
	}
	if len(out.Notes) != 1 || !strings.Contains(out.Notes[0], "limit reduzido de 60 para o máximo de 50") {
		t.Errorf("notes = %v", out.Notes)
	}
}

// TestListChatsErrors checks the unknown category and the not_logged_in path.
func TestListChatsErrors(t *testing.T) {
	f := newFixture(t)
	if got := errorCode(t, f.call("list_chats", map[string]any{"category": "Nada"})); got != string(toolerr.CodeInvalidArgument) {
		t.Errorf("unknown category: code = %q", got)
	}
}
