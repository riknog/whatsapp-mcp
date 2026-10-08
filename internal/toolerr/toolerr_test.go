package toolerr

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestErrorImplementsError(t *testing.T) {
	var err error = New(CodeRateLimited, "too many messages", nil)
	if got, want := err.Error(), "rate_limited: too many messages"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}

	wrapped := fmt.Errorf("send: %w", err)
	var te Error
	if !errors.As(wrapped, &te) {
		t.Fatal("errors.As did not find toolerr.Error through wrapping")
	}
	if te.Code != CodeRateLimited {
		t.Fatalf("Code = %q, want %q", te.Code, CodeRateLimited)
	}
}

func TestMarshalJSONShape(t *testing.T) {
	e := New(CodeAmbiguousContact, "several contacts match", map[string]any{
		"candidates": []string{"c_k3m9x2q8va"},
	})
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := map[string]any{
		"code":    "ambiguous_contact",
		"message": "several contacts match",
		"details": map[string]any{"candidates": []any{"c_k3m9x2q8va"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON = %v, want %v", got, want)
	}
}

func TestMarshalJSONNilDetailsIsEmptyObject(t *testing.T) {
	b, err := json.Marshal(New(CodeQueueFull, "queue is full", nil))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"code":"queue_full","message":"queue is full","details":{}}`
	if string(b) != want {
		t.Fatalf("JSON = %s, want %s", b, want)
	}
}

func TestMarshalJSONInsideStruct(t *testing.T) {
	type result struct {
		Error Error `json:"error"`
	}
	b, err := json.Marshal(result{Error: New(CodeInvalidArgument, "bad", nil)})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"error":{"code":"invalid_argument","message":"bad","details":{}}}`
	if string(b) != want {
		t.Fatalf("JSON = %s, want %s", b, want)
	}
}

func TestAllCodesMatchSpec(t *testing.T) {
	// Literal list copied from docs/02-TOOLS.md. If the spec changes, update both.
	spec := []string{
		"not_logged_in", "disconnected", "contact_not_found", "ambiguous_contact",
		"phone_not_allowed", "chat_hidden", "contact_not_shareable", "share_disabled",
		"group_send_disabled", "policy_reply_only", "rate_limited", "queue_full",
		"duplicate_message", "quiet_hours", "send_disabled", "message_too_long",
		"invalid_argument", "send_failed", "send_uncertain",
		"media_disabled", "media_unavailable", "media_tool_failed",
	}
	codes := AllCodes()
	if len(codes) != len(spec) {
		t.Fatalf("AllCodes has %d codes, spec has %d", len(codes), len(spec))
	}
	seen := make(map[Code]bool, len(codes))
	for i, c := range codes {
		if string(c) != spec[i] {
			t.Errorf("AllCodes[%d] = %q, want %q", i, c, spec[i])
		}
		if seen[c] {
			t.Errorf("duplicate code %q", c)
		}
		seen[c] = true
	}
}
