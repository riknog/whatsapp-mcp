package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

// FuzzFTSQuery checks ftsQuery alone, so coverage guidance stays on the
// sanitizer: instrumenting the transpiled SQLite makes FuzzSearchQuery's
// workers crawl once they start minimizing.
func FuzzFTSQuery(f *testing.F) {
	for _, s := range []string{"", "abacaxi", `"`, `"" OR *`, "NEAR(a b)", `x" OR "y`, "\xff", "a\x00b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, q string) {
		expr, err := ftsQuery(q)
		if err != nil {
			return
		}
		for _, p := range strings.Split(expr, " ") {
			inner, pre := strings.CutPrefix(p, `"`)
			inner, suf := strings.CutSuffix(inner, `"`)
			if !pre || !suf ||
				strings.Contains(strings.ReplaceAll(inner, `""`, ""), `"`) || strings.ContainsRune(inner, 0) {
				t.Fatalf("ftsQuery(%q) = %q: %q is not a quoted phrase", q, expr, p)
			}
		}
	})
}

// FuzzSearchQuery runs arbitrary user text through ftsQuery and a real FTS5
// index. Every input must either search (no SQL error) or be invalid_argument:
// FTS5 operators typed by the user never reach the engine as syntax.
func FuzzSearchQuery(f *testing.F) {
	for _, s := range []string{
		"", "abacaxi", `"`, `"" OR *`, "NEAR(a b)", "a AND NOT b", "col:val", "^start", "a* -b +c",
		"\x00", "({[", "ção ç", `x" OR "y`, "🙂", "\xff",
	} {
		f.Add(s)
	}
	st, _ := newTestStore(f)
	ctx := context.Background()
	if err := st.UpsertChat(ctx, Chat{JID: "c1@s.whatsapp.net", Ref: "c_aaaaaaaaaa", Kind: "direct", LastMessageAt: 1}); err != nil {
		f.Fatal(err)
	}
	if _, err := st.InsertMessage(ctx, Message{ChatJID: "c1@s.whatsapp.net", ID: "M1", SenderJID: "c1@s.whatsapp.net", TS: 1, Kind: "text", Text: "abacaxi OR near ação"}); err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, q string) {
		expr, err := ftsQuery(q)
		if err == nil && !strings.HasPrefix(expr, `"`) {
			t.Fatalf("ftsQuery(%q) = %q, want quoted phrases", q, expr)
		}
		_, _, err = st.Search(ctx, SearchFilter{Query: q})
		var te toolerr.Error
		if err != nil && !(errors.As(err, &te) && te.Code == toolerr.CodeInvalidArgument) {
			t.Fatalf("Search(%q): %v", q, err)
		}
	})
}
