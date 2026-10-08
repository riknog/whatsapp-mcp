package identity

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/privacy"
	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

// openStore opens a real store in a temporary directory.
func openStore(t *testing.T) (*store.Store, *Refs) {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewFake(testNow)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "data.db"), clk)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	refs, err := NewRefs(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return st, refs
}

// addChat stores a chat with the ref the ingest would store.
func addChat(t *testing.T, st *store.Store, refs *Refs, jid, kind, name string, last time.Time) {
	t.Helper()
	ts := int64(0)
	if !last.IsZero() {
		ts = last.Unix()
	}
	if err := st.UpsertChat(context.Background(), store.Chat{
		JID: jid, Ref: refs.Ref(jid), Kind: kind, DisplayName: name, LastMessageAt: ts,
	}); err != nil {
		t.Fatalf("UpsertChat(%s): %v", jid, err)
	}
}

func TestStoreSourceResolvesWithRealStore(t *testing.T) {
	ctx := context.Background()
	st, refs := openStore(t)

	const mae = "5511911110001@s.whatsapp.net"
	const joao1 = "5511922220002@s.whatsapp.net"
	const joao2 = "5511933330003@s.whatsapp.net"
	const banco = "5511944440004@s.whatsapp.net"
	const grupo = "120363000000000000@g.us"
	const sem = "5511955550005@s.whatsapp.net"

	addChat(t, st, refs, mae, "direct", "", testNow.Add(-2*time.Hour))
	addChat(t, st, refs, joao1, "direct", "", testNow.Add(-time.Hour))
	addChat(t, st, refs, joao2, "direct", "", testNow.Add(-3*24*time.Hour))
	addChat(t, st, refs, banco, "direct", "", testNow.Add(-time.Hour))
	addChat(t, st, refs, grupo, "group", "Família", testNow.Add(-10*time.Minute))

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.UpsertContact(ctx, store.Contact{JID: mae, FullName: "Mãe ❤️"}))
	must(st.UpsertContact(ctx, store.Contact{JID: joao1, FullName: "João Silva"}))
	must(st.UpsertContact(ctx, store.Contact{JID: joao2, FullName: "João Pedro"}))
	must(st.UpsertContact(ctx, store.Contact{JID: banco, FullName: "Banco X"}))
	must(st.UpsertContact(ctx, store.Contact{JID: sem, FullName: "Fulano Sem Conversa"}))
	must(st.SetHidden(ctx, banco, true))
	must(st.UpsertLabel(ctx, store.Label{ID: "lab-fam", Name: "Família", Color: 1, Source: "local"}))
	must(st.SetChatLabel(ctx, joao1, "lab-fam", true))

	r := NewResolver(NewStoreSource(st, refs), clock.NewFake(testNow))

	got, err := r.Resolve(ctx, "mae")
	if err != nil || got.Ref != refs.Ref(mae) || got.JID != mae {
		t.Fatalf("mae: %+v %v", got, err)
	}

	if _, err := r.Resolve(ctx, "joao"); errCode(err) != toolerr.CodeAmbiguousContact {
		t.Fatalf("joao: %v", err)
	} else {
		var te toolerr.Error
		errors.As(err, &te)
		list := te.Details["candidates"].([]map[string]any)
		if list[0]["name"] != "João Silva" {
			t.Errorf("mais recente primeiro: %v", list[0]["name"])
		}
		if cs := list[0]["categories"].([]string); len(cs) != 1 || cs[0] != "Família" {
			t.Errorf("categorias do chat: %#v", cs)
		}
		if err := privacy.AssertNoPII(err); err != nil {
			t.Errorf("erro de ambiguidade vaza PII: %v", err)
		}
	}

	if _, err := r.Resolve(ctx, "banco"); errCode(err) != toolerr.CodeChatHidden {
		t.Errorf("banco oculto: %v", err)
	}

	if got, err := r.Resolve(ctx, "fulano"); err != nil || got.HasChat {
		t.Errorf("contato sem chat: %+v %v", got, err)
	}

	if got, err := r.Resolve(ctx, "familia"); err != nil || got.Kind != "group" {
		t.Errorf("grupo: %+v %v", got, err)
	}

	if got, err := r.Resolve(ctx, refs.Ref(mae)); err != nil || got.Name != "Mãe ❤️" {
		t.Errorf("ref: %+v %v", got, err)
	}

	if _, err := r.Resolve(ctx, "11 98765-4321"); errCode(err) != toolerr.CodePhoneNotAllowed {
		t.Errorf("telefone: %v", err)
	}

	hits, err := r.Search(ctx, "joao", 10)
	if err != nil || len(hits) != 2 {
		t.Errorf("search joao = %d hits, %v", len(hits), err)
	}
	for _, h := range hits {
		if h.Ref == refs.Ref(banco) {
			t.Error("search devolveu chat oculto")
		}
	}
}

func TestStoreSourcePagesBeyondOneHundredAndNinetyNine(t *testing.T) {
	ctx := context.Background()
	st, refs := openStore(t)
	const n = 205
	for i := 0; i < n; i++ {
		jid := fmt.Sprintf("5511%08d@s.whatsapp.net", i)
		addChat(t, st, refs, jid, "direct", "", testNow.Add(-time.Duration(i)*time.Minute))
		if err := st.UpsertContact(ctx, store.Contact{JID: jid, FullName: fmt.Sprintf("Contato %03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	const extra = 201
	for i := 0; i < extra; i++ {
		jid := fmt.Sprintf("5521%08d@s.whatsapp.net", i)
		if err := st.UpsertContact(ctx, store.Contact{JID: jid, FullName: fmt.Sprintf("Sem Chat %03d", i)}); err != nil {
			t.Fatal(err)
		}
	}

	cands, err := NewStoreSource(st, refs).Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != n+extra {
		t.Fatalf("candidatos = %d, esperado %d", len(cands), n+extra)
	}
	withChat := 0
	for _, c := range cands {
		if c.HasChat {
			withChat++
		}
	}
	if withChat != n {
		t.Errorf("com chat = %d, esperado %d", withChat, n)
	}
}

func TestStoreSourceNamelessContactWithoutChatIsSkipped(t *testing.T) {
	ctx := context.Background()
	st, refs := openStore(t)
	if err := st.UpsertContact(ctx, store.Contact{JID: "5511900000000@s.whatsapp.net"}); err != nil {
		t.Fatal(err)
	}
	// A name that is a phone number is not a name either.
	if err := st.UpsertContact(ctx, store.Contact{JID: "5511900000001@s.whatsapp.net", PushName: "5511900000001"}); err != nil {
		t.Fatal(err)
	}
	cands, err := NewStoreSource(st, refs).Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 {
		t.Errorf("contato sem nome e sem chat virou candidato: %+v", cands)
	}
}

func TestStoreSourceNilRefsSkipsContactsWithoutChat(t *testing.T) {
	ctx := context.Background()
	st, refs := openStore(t)
	addChat(t, st, refs, "5511966660006@s.whatsapp.net", "direct", "Só Chat", time.Time{})
	if err := st.UpsertContact(ctx, store.Contact{JID: "5511977770007@s.whatsapp.net", FullName: "Sem Chat"}); err != nil {
		t.Fatal(err)
	}
	cands, err := NewStoreSource(st, nil).Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Name != "Só Chat" {
		t.Errorf("candidatos = %+v", cands)
	}
}

// A chat first seen by its LID keeps its ref after LinkAlias moves it to the
// phone-number JID: the resolver returns the same ref before and after, and no
// duplicate candidate appears when the contact row arrives under the PN.
func TestLIDChatKeepsRefAfterLinkAlias(t *testing.T) {
	ctx := context.Background()
	st, refs := openStore(t)
	const lid = "123456789012345@lid"
	const pn = "5511966660006@s.whatsapp.net"

	addChat(t, st, refs, lid, "direct", "Maria", testNow.Add(-time.Hour))
	r := NewResolver(NewStoreSource(st, refs), clock.NewFake(testNow))

	before, err := r.Resolve(ctx, "maria")
	if err != nil {
		t.Fatalf("antes do vínculo: %v", err)
	}
	if before.Ref != refs.Ref(lid) {
		t.Fatalf("ref antes = %q, esperado a do LID %q", before.Ref, refs.Ref(lid))
	}

	if err := st.LinkAlias(ctx, lid, pn); err != nil {
		t.Fatalf("LinkAlias: %v", err)
	}
	if err := st.UpsertContact(ctx, store.Contact{JID: pn, FullName: "Maria"}); err != nil {
		t.Fatal(err)
	}

	after, err := r.Resolve(ctx, "maria")
	if err != nil {
		t.Fatalf("depois do vínculo: %v", err)
	}
	if after.Ref != before.Ref {
		t.Fatalf("ref mudou com o vínculo: %q -> %q", before.Ref, after.Ref)
	}
	byRef, err := r.Resolve(ctx, before.Ref)
	if err != nil || byRef.Ref != before.Ref {
		t.Fatalf("resolver pela ref antiga: %+v %v", byRef, err)
	}
	cands, err := NewStoreSource(st, refs).Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Errorf("candidatos após o vínculo = %d, esperado 1 (sem duplicata)", len(cands))
	}
}
