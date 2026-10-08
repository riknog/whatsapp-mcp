package identity

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/privacy"
	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

// fakeSource is an in-memory ContactSource.
type fakeSource struct {
	cands []Candidate
	err   error
}

func (f fakeSource) Candidates(context.Context) ([]Candidate, error) {
	return f.cands, f.err
}

var testNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func cand(name, ref string, mods ...func(*Candidate)) Candidate {
	c := Candidate{JID: "jid-" + ref + "@s.whatsapp.net", Ref: ref, Name: name, Kind: "direct", HasChat: true}
	for _, m := range mods {
		m(&c)
	}
	return c
}

func hidden(c *Candidate) { c.Hidden = true }
func group(c *Candidate)  { c.Kind = "group" }
func cats(names ...string) func(*Candidate) {
	return func(c *Candidate) { c.Categories = names }
}
func seen(d time.Duration) func(*Candidate) {
	return func(c *Candidate) { c.LastInteraction = testNow.Add(-d) }
}

func newResolver(cands ...Candidate) *Resolver {
	return NewResolver(fakeSource{cands: cands}, clock.NewFake(testNow))
}

// errCode returns the toolerr code of err, or "" when err is not a toolerr.Error.
func errCode(err error) toolerr.Code {
	var te toolerr.Error
	if errors.As(err, &te) {
		return te.Code
	}
	return ""
}

func TestResolveNameLevelsAndAccents(t *testing.T) {
	r := newResolver(
		cand("Mãe ❤️", "c_mae0000001"),
		cand("João Silva", "c_silva00001"),
		cand("Pedro Alves", "c_alves00001"),
	)
	tests := []struct {
		in   string
		want string
	}{
		{"mae", "c_mae0000001"},        // accent and emoji removed
		{"MÃE", "c_mae0000001"},        // case and accent
		{"joao silva", "c_silva00001"}, // exact
		{"joao sil", "c_silva00001"},   // prefix
		{"ilva", "c_silva00001"},       // contains
		{"silva joao", "c_silva00001"}, // every word, any order
		{"  pedro  ", "c_alves00001"},  // spaces trimmed
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := r.Resolve(context.Background(), tc.in)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.in, err)
			}
			if got.Ref != tc.want {
				t.Errorf("ref = %q, esperado %q", got.Ref, tc.want)
			}
		})
	}
}

func TestResolveAmbiguousJoaoIsTwoCandidates(t *testing.T) {
	r := newResolver(
		cand("João Silva", "c_silva00001", seen(2*time.Hour), cats("Família")),
		cand("João Pedro", "c_pedro00001", seen(3*24*time.Hour)),
	)
	_, err := r.Resolve(context.Background(), "joao")
	if code := errCode(err); code != toolerr.CodeAmbiguousContact {
		t.Fatalf("código = %q, esperado ambiguous_contact (err=%v)", code, err)
	}
	var te toolerr.Error
	errors.As(err, &te)
	list, ok := te.Details["candidates"].([]map[string]any)
	if !ok || len(list) != 2 {
		t.Fatalf("candidates = %#v, esperado 2 itens", te.Details["candidates"])
	}
	if list[0]["name"] != "João Silva" || list[0]["contact_ref"] != "c_silva00001" {
		t.Errorf("primeiro candidato = %#v (o mais recente deve vir primeiro)", list[0])
	}
	if got := list[0]["last_interaction_ago"]; got != "há 2 h" {
		t.Errorf("last_interaction_ago = %v, esperado \"há 2 h\"", got)
	}
	if cs := list[0]["categories"].([]string); len(cs) != 1 || cs[0] != "Família" {
		t.Errorf("categories = %#v", cs)
	}
	if cs := list[1]["categories"].([]string); cs == nil || len(cs) != 0 {
		t.Errorf("categories sem etiqueta = %#v, esperado slice vazio não nulo", cs)
	}
	if got := list[1]["last_interaction_ago"]; got != "há 3 dias" {
		t.Errorf("last_interaction_ago = %v, esperado \"há 3 dias\"", got)
	}
	if err := privacy.AssertNoPII(err); err != nil {
		t.Errorf("erro de ambiguidade vaza PII: %v", err)
	}
}

func TestResolveExactBeatsPrefix(t *testing.T) {
	r := newResolver(
		cand("João", "c_joao000001"),
		cand("João Silva", "c_silva00001"),
	)
	got, err := r.Resolve(context.Background(), "joao")
	if err != nil {
		t.Fatalf("exato deveria vencer o prefixo: %v", err)
	}
	if got.Ref != "c_joao000001" {
		t.Errorf("ref = %q", got.Ref)
	}
}

func TestResolveAmbiguityListsAtMostFiveMostRecentFirst(t *testing.T) {
	var cs []Candidate
	for i := 0; i < 7; i++ {
		cs = append(cs, cand(fmt.Sprintf("Ana %d", i), fmt.Sprintf("c_ana%07d", i), seen(time.Duration(i+1)*time.Hour)))
	}
	r := newResolver(cs...)
	_, err := r.Resolve(context.Background(), "ana")
	var te toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeAmbiguousContact {
		t.Fatalf("err = %v, esperado ambiguous_contact", err)
	}
	list := te.Details["candidates"].([]map[string]any)
	if len(list) != 5 {
		t.Fatalf("candidatos = %d, esperado 5", len(list))
	}
	if te.Details["total"] != 7 {
		t.Errorf("total = %v, esperado 7", te.Details["total"])
	}
	if list[0]["name"] != "Ana 0" {
		t.Errorf("o mais recente deve vir primeiro, veio %v", list[0]["name"])
	}
}

func TestResolvePhoneInputsAreRejected(t *testing.T) {
	r := newResolver(cand("Mãe", "c_mae0000001"))
	for _, in := range []string{
		"+5511987654321",
		"11 98765-4321",
		"5511987654321@s.whatsapp.net",
		"123@lid",
		"120363000000000000@g.us",
	} {
		if _, err := r.Resolve(context.Background(), in); errCode(err) != toolerr.CodePhoneNotAllowed {
			t.Errorf("Resolve(%q): código = %q, esperado phone_not_allowed", in, errCode(err))
		}
	}
}

func TestResolveRefPath(t *testing.T) {
	r := newResolver(
		cand("Mãe", "c_mae0000001"),
		cand("Banco X", "c_banco00001", hidden),
	)
	got, err := r.Resolve(context.Background(), "c_mae0000001")
	if err != nil || got.Name != "Mãe" {
		t.Fatalf("ref válido: got=%+v err=%v", got, err)
	}
	if code := errCode(mustErr(r.Resolve(context.Background(), "c_nope000000"))); code != toolerr.CodeContactNotFound {
		t.Errorf("ref inexistente: código = %q, esperado contact_not_found", code)
	}
	if code := errCode(mustErr(r.Resolve(context.Background(), "c_banco00001"))); code != toolerr.CodeChatHidden {
		t.Errorf("ref oculto: código = %q, esperado chat_hidden", code)
	}
}

func mustErr(_ Target, err error) error { return err }

func TestResolveHiddenTargets(t *testing.T) {
	t.Run("only match is hidden", func(t *testing.T) {
		r := newResolver(cand("Banco X", "c_banco00001", hidden), cand("Mãe", "c_mae0000001"))
		if code := errCode(mustErr(r.Resolve(context.Background(), "banco"))); code != toolerr.CodeChatHidden {
			t.Errorf("código = %q, esperado chat_hidden", code)
		}
	})
	t.Run("hidden and visible share the name", func(t *testing.T) {
		r := newResolver(
			cand("João", "c_joao000001", hidden),
			cand("João Silva", "c_silva00001"),
		)
		// "joao" hits the hidden exact match first, so it is one match: hidden.
		if code := errCode(mustErr(r.Resolve(context.Background(), "joao"))); code != toolerr.CodeChatHidden {
			t.Errorf("código = %q, esperado chat_hidden", code)
		}
	})
	t.Run("ambiguity never lists hidden chats", func(t *testing.T) {
		r := newResolver(
			cand("Ana Lima", "c_hidden0001", hidden),
			cand("Ana Souza", "c_souza00001"),
			cand("Ana Costa", "c_costa00001"),
		)
		_, err := r.Resolve(context.Background(), "ana")
		var te toolerr.Error
		if !errors.As(err, &te) || te.Code != toolerr.CodeAmbiguousContact {
			t.Fatalf("err = %v, esperado ambiguous_contact", err)
		}
		for _, c := range te.Details["candidates"].([]map[string]any) {
			if c["contact_ref"] == "c_hidden0001" {
				t.Fatal("chat oculto apareceu em candidates")
			}
		}
	})
	t.Run("two hidden matches are chat_hidden", func(t *testing.T) {
		r := newResolver(
			cand("Banco", "c_banco00001", hidden),
			cand("Banco Central", "c_banco00002", hidden),
		)
		if code := errCode(mustErr(r.Resolve(context.Background(), "banco"))); code != toolerr.CodeChatHidden {
			t.Errorf("código = %q, esperado chat_hidden", code)
		}
	})
}

func TestResolveNotFoundAndInvalid(t *testing.T) {
	r := newResolver(cand("Mãe", "c_mae0000001"))
	if code := errCode(mustErr(r.Resolve(context.Background(), "Zé"))); code != toolerr.CodeContactNotFound {
		t.Errorf("nome inexistente: código = %q", code)
	}
	if code := errCode(mustErr(r.Resolve(context.Background(), "   "))); code != toolerr.CodeInvalidArgument {
		t.Errorf("vazio: código = %q", code)
	}
	if code := errCode(mustErr(r.Resolve(context.Background(), "!!! ..."))); code != toolerr.CodeInvalidArgument {
		t.Errorf("só pontuação: código = %q", code)
	}
}

func TestResolveGroupsAndContactsWithoutChat(t *testing.T) {
	r := newResolver(
		cand("Família", "c_grupo00001", group),
		cand("Fulano Sem Conversa", "c_fulano0001", func(c *Candidate) { c.HasChat = false }),
	)
	g, err := r.Resolve(context.Background(), "familia")
	if err != nil || g.Kind != "group" {
		t.Fatalf("grupo: %+v %v", g, err)
	}
	f, err := r.Resolve(context.Background(), "fulano")
	if err != nil || f.HasChat {
		t.Fatalf("contato sem chat: %+v %v", f, err)
	}
}

func TestResolveSourceErrorIsWrapped(t *testing.T) {
	boom := errors.New("disco cheio")
	r := NewResolver(fakeSource{err: boom}, clock.NewFake(testNow))
	_, err := r.Resolve(context.Background(), "mae")
	if !errors.Is(err, boom) {
		t.Fatalf("erro não encadeado: %v", err)
	}
	if errCode(err) != "" {
		t.Errorf("erro de infraestrutura não deveria ser toolerr")
	}
}

func TestSearch(t *testing.T) {
	r := newResolver(
		cand("João", "c_joao000001", seen(time.Hour)),
		cand("João Silva", "c_silva00001", seen(2*time.Hour)),
		cand("Maria da Silva", "c_maria00001", seen(time.Minute)),
		cand("Segredo do Joao", "c_secret0001", hidden),
	)
	got, err := r.Search(context.Background(), "joao", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("matches = %d, esperado 2 (oculto fora)", len(got))
	}
	if got[0].Ref != "c_joao000001" || got[0].Level != LevelExact {
		t.Errorf("primeiro = %+v, esperado João exato", got[0])
	}
	if got[1].Ref != "c_silva00001" || got[1].Level != LevelPrefix {
		t.Errorf("segundo = %+v, esperado João Silva por prefixo", got[1])
	}

	tok, _ := r.Search(context.Background(), "silva maria", 10)
	if len(tok) != 1 || tok[0].Level != LevelToken {
		t.Errorf("busca por tokens = %+v", tok)
	}
	if _, err := r.Search(context.Background(), "", 10); errCode(err) != toolerr.CodeInvalidArgument {
		t.Errorf("busca vazia: %v", err)
	}
	if _, err := r.Search(context.Background(), "+5511987654321", 10); errCode(err) != toolerr.CodePhoneNotAllowed {
		t.Errorf("busca por telefone: %v", err)
	}
	if _, err := r.Search(context.Background(), "...", 10); errCode(err) != toolerr.CodeInvalidArgument {
		t.Errorf("busca sem letras: %v", err)
	}
}

func TestSearchLimitIsCapped(t *testing.T) {
	var cs []Candidate
	for i := 0; i < 40; i++ {
		cs = append(cs, cand(fmt.Sprintf("Grupo %02d", i), fmt.Sprintf("c_grp%07d", i)))
	}
	r := newResolver(cs...)
	if got, _ := r.Search(context.Background(), "grupo", 0); len(got) != 10 {
		t.Errorf("padrão = %d, esperado 10", len(got))
	}
	if got, _ := r.Search(context.Background(), "grupo", 500); len(got) != 30 {
		t.Errorf("máximo = %d, esperado 30", len(got))
	}
}

func TestResolveAnyFindsHiddenChats(t *testing.T) {
	r := newResolver(cand("Banco X", "c_banco00001", hidden), cand("Banco Y", "c_banco00002"), cand("Mãe", "c_mae0000001"))
	ctx := context.Background()

	got, err := r.ResolveAny(ctx, "Banco X")
	if err != nil || got.Ref != "c_banco00001" || !got.Hidden {
		t.Fatalf("ResolveAny(nome) = %+v, %v", got, err)
	}
	got, err = r.ResolveAny(ctx, "c_banco00001")
	if err != nil || !got.Hidden {
		t.Fatalf("ResolveAny(ref) = %+v, %v", got, err)
	}
	// Both banks match by prefix: the hidden one is a candidate too.
	te := toolerrOf(t, mustErr(r.ResolveAny(ctx, "banco")))
	if te.Code != toolerr.CodeAmbiguousContact || te.Details["total"] != 2 {
		t.Fatalf("ResolveAny(banco) = %+v", te)
	}
	// Resolve still refuses it, and its targets are never hidden.
	if code := errCode(mustErr(r.Resolve(ctx, "Banco X"))); code != toolerr.CodeChatHidden {
		t.Errorf("Resolve(Banco X) = %q, esperado chat_hidden", code)
	}
	if got, err := r.Resolve(ctx, "Mãe"); err != nil || got.Hidden {
		t.Errorf("Resolve(Mãe) = %+v, %v", got, err)
	}
	if code := errCode(mustErr(r.ResolveAny(ctx, "5511999990000"))); code != toolerr.CodePhoneNotAllowed {
		t.Errorf("ResolveAny(telefone) = %q", code)
	}
}

func toolerrOf(t *testing.T, err error) toolerr.Error {
	t.Helper()
	var te toolerr.Error
	if !errors.As(err, &te) {
		t.Fatalf("erro não é toolerr: %v", err)
	}
	return te
}
