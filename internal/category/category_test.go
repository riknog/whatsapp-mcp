package category

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/identity"
	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

const (
	jidPai  = "5511900000002@s.whatsapp.net"
	jidAna  = "5511900000009@s.whatsapp.net"
	refPai  = "c_paipaipai01"
	refAna  = "c_anaanaana01"
	labelWA = "wa:familia"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "data.db"), clock.NewFake(time.Unix(1_800_000_000, 0)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertChat(ctx, store.Chat{JID: jidPai, Ref: refPai, Kind: "direct"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertLabel(ctx, store.Label{ID: labelWA, Name: "Família", Source: "whatsapp"}); err != nil {
		t.Fatal(err)
	}
	return st
}

func labelsOf(t *testing.T, st *store.Store, jid string) []string {
	t.Helper()
	ls, err := st.LabelsOf(context.Background(), jid)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range ls {
		out = append(out, l.Name)
	}
	return out
}

func code(err error) toolerr.Code {
	var te toolerr.Error
	if errors.As(err, &te) {
		return te.Code
	}
	return ""
}

func TestSetAddsAndRemovesLocalCategories(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	pai := identity.Target{JID: jidPai, Ref: refPai, Kind: "direct", HasChat: true}

	if err := Set(ctx, st, pai, "Fornecedores", true); err != nil {
		t.Fatal(err)
	}
	// Another spelling is the same category.
	if err := Set(ctx, st, pai, "fornecedóres", true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(labelsOf(t, st, jidPai), ","); got != "Fornecedores" {
		t.Fatalf("labels = %q", got)
	}

	// A saved contact without a chat gets an empty chat row.
	ana := identity.Target{JID: jidAna, Ref: refAna, Kind: "direct"}
	if err := Set(ctx, st, ana, "Fornecedores", true); err != nil {
		t.Fatal(err)
	}
	if got := labelsOf(t, st, jidAna); len(got) != 1 {
		t.Fatalf("Ana labels = %v", got)
	}

	if err := Set(ctx, st, pai, "FORNECEDORES", false); err != nil {
		t.Fatal(err)
	}
	if got := labelsOf(t, st, jidPai); len(got) != 0 {
		t.Fatalf("labels after remove = %v", got)
	}
}

func TestSetRefusals(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	pai := identity.Target{JID: jidPai, Ref: refPai, Kind: "direct", HasChat: true}
	cases := []struct {
		name string
		cat  string
		on   bool
		want toolerr.Code
	}{
		{"whatsapp label", "Família", true, toolerr.CodeInvalidArgument},
		{"whatsapp label remove", "familia", false, toolerr.CodeInvalidArgument},
		{"unknown remove", "Nunca", false, toolerr.CodeInvalidArgument},
		{"groups", "Grupos", true, toolerr.CodeInvalidArgument},
		{"none", "sem categoria", true, toolerr.CodeInvalidArgument},
		{"empty", "", true, toolerr.CodeInvalidArgument},
		{"symbols only", "!!!", true, toolerr.CodeInvalidArgument},
		{"too long", strings.Repeat("x", MaxRunes+1), true, toolerr.CodeInvalidArgument},
		{"phone", "11 91234-5678", true, toolerr.CodePhoneNotAllowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := code(Set(ctx, st, pai, c.cat, c.on)); got != c.want {
				t.Errorf("code = %q, want %q", got, c.want)
			}
		})
	}
	if got := labelsOf(t, st, jidPai); len(got) != 0 {
		t.Errorf("refusals changed labels: %v", got)
	}
	if err := CheckName(strings.Repeat("é", MaxRunes)); err != nil {
		t.Errorf("50 characters refused: %v", err)
	}
}

func TestDisplayNameRedactsPhones(t *testing.T) {
	if got := DisplayName(store.Label{Name: "Ligar 11 91234-5678"}); strings.Contains(got, "91234") {
		t.Errorf("DisplayName = %q", got)
	}
}
