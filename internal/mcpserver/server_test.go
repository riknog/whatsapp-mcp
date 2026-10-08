package mcpserver

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

var wantTools = []string{
	"whatsapp_status", "list_new_messages", "list_chats", "get_chat_messages",
	"search_messages", "list_contacts", "search_contacts", "list_categories",
	"send_message", "share_contact", "mark_as_read", "set_contact_category",
}

// readTools is how many of wantTools are read tools; the rest write.
const readTools = 8

// TestToolsListHasTwelveToolsWithSchemas checks tools/list: the eight read
// tools and the four write tools, each with an input and an output schema, and
// the read-only hint on the read tools only.
func TestToolsListHasTwelveToolsWithSchemas(t *testing.T) {
	f := newFixture(t)
	res, err := f.cs.ListTools(f.ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	if len(got) != len(wantTools) {
		t.Fatalf("tools = %d, want %d: %v", len(got), len(wantTools), keys(got))
	}
	for i, name := range wantTools {
		tool, ok := got[name]
		if !ok {
			t.Errorf("tool %s missing", name)
			continue
		}
		if tool.InputSchema == nil || tool.OutputSchema == nil {
			t.Errorf("%s: schemas missing (in=%v out=%v)", name, tool.InputSchema != nil, tool.OutputSchema != nil)
		}
		readOnly := tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		if readOnly != (i < readTools) {
			t.Errorf("%s: read-only hint = %v", name, readOnly)
		}
		if tool.Description == "" {
			t.Errorf("%s: empty description", name)
		}
	}
}

func keys(m map[string]*mcp.Tool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestPromptResponderMensagens checks prompts/get for the responder_mensagens prompt.
func TestPromptResponderMensagens(t *testing.T) {
	f := newFixture(t)
	list, err := f.cs.ListPrompts(f.ctx, nil)
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	if len(list.Prompts) != 1 || list.Prompts[0].Name != "responder_mensagens" {
		t.Fatalf("prompts = %+v, want responder_mensagens", list.Prompts)
	}
	got, err := f.cs.GetPrompt(f.ctx, &mcp.GetPromptParams{Name: "responder_mensagens"})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(got.Messages))
	}
	tc, ok := got.Messages[0].Content.(*mcp.TextContent)
	if !ok {
		t.Fatalf("content = %T, want text", got.Messages[0].Content)
	}
	for _, want := range []string{"whatsapp_status", "list_new_messages", "share_contact", "não-confiável"} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("prompt misses %q", want)
		}
	}
}

// TestReadToolsNeedASession checks that every data tool answers not_logged_in
// without a session, and that whatsapp_status still answers.
func TestReadToolsNeedASession(t *testing.T) {
	f := newFixture(t)
	f.wa.SetLoggedIn(false)
	calls := map[string]map[string]any{
		"list_new_messages": {},
		"list_chats":        {},
		"get_chat_messages": {"contact": "Mãe"},
		"search_messages":   {"query": "oi"},
		"list_contacts":     {},
		"search_contacts":   {"query": "mae"},
		"list_categories":   {},
	}
	for name, args := range calls {
		res := f.call(name, args)
		if !res.IsError || errorCode(t, res) != string(toolerr.CodeNotLoggedIn) {
			t.Errorf("%s: want not_logged_in, got %+v", name, res.Content)
		}
	}
	status := f.call("whatsapp_status", nil)
	mustOK(t, status)
	var out statusOut
	decodeInto(t, status, &out)
	if out.LoggedIn {
		t.Errorf("status logged_in = true, want false")
	}
}

// TestFailedPassesOtherErrors checks that a non-tool error is not turned into a
// structured error: the SDK reports it as an error result.
func TestFailedPassesOtherErrors(t *testing.T) {
	boom := errors.New("disco cheio")
	res, out, err := failed[chatsOut](boom)
	if res != nil || !errors.Is(err, boom) {
		t.Fatalf("failed(other) = %v, %v; want nil and the error", res, err)
	}
	if out.Error != nil {
		t.Fatalf("out.Error = %+v, want nil", out.Error)
	}
}

// TestFailedCarriesToolError checks the result shape of a tool error.
func TestFailedCarriesToolError(t *testing.T) {
	res, out, err := failed[chatsOut](toolerr.New(toolerr.CodeChatHidden, "oculta", nil))
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("failed = %v, %v; want an error result", res, err)
	}
	if out.Error == nil || out.Error.Code != "chat_hidden" || out.Error.Details == nil {
		t.Fatalf("out.Error = %+v", out.Error)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"chats":[]`) {
		t.Errorf("error output = %s, want chats as an empty array", raw)
	}
}
