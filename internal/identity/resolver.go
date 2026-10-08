package identity

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

// maxCandidates caps details.candidates in an ambiguous_contact error.
const maxCandidates = 5

// Candidate is one chat or named contact as the resolver sees it. Names and
// categories are already resolved by the source. JID stays inside the process.
type Candidate struct {
	JID             string
	Ref             string
	Name            string
	Kind            string // "direct" | "group"
	HasChat         bool
	Hidden          bool
	Categories      []string
	LastInteraction time.Time // zero when there was no interaction
}

// ContactSource lists every candidate. The resolver filters and ranks them.
// The adapter over *store.Store is in store_source.go; tests use a fake.
type ContactSource interface {
	Candidates(ctx context.Context) ([]Candidate, error)
}

// Target is a resolved contact or group. JID is internal: it must never reach
// tool output, errors or logs. Tools expose Ref and Name only.
type Target struct {
	JID             string
	Ref             string
	Name            string
	Kind            string
	HasChat         bool
	Categories      []string
	LastInteraction time.Time
	Hidden          bool // set only by ResolveAny; Resolve never returns a hidden target
}

// Match is a Target found by Search, with the level at which it matched.
type Match struct {
	Target
	Level string
}

// Resolver turns a contact name or contact_ref into a Target.
type Resolver struct {
	src ContactSource
	clk clock.Clock
}

// NewResolver builds a Resolver. A nil clk means the wall clock.
func NewResolver(src ContactSource, clk clock.Clock) *Resolver {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Resolver{src: src, clk: clk}
}

// Resolve follows design §5:
//  1. input that looks like a phone or JID gives phone_not_allowed;
//  2. input starting with "c_" is looked up by contact_ref; not found gives contact_not_found;
//  3. otherwise matching by name goes exact, prefix, contains, then tokens, and
//     stops at the first level with any candidate;
//  4. one candidate is the answer; two or more give ambiguous_contact with up
//     to five visible candidates and nothing is sent;
//  5. a hidden target gives chat_hidden.
//
// Hidden candidates take part in matching, so a name that points to a hidden
// chat gives chat_hidden. They are never listed in an ambiguity error.
func (r *Resolver) Resolve(ctx context.Context, input string) (Target, error) {
	return r.resolve(ctx, input, false)
}

// ResolveAny is Resolve with hidden chats treated as visible. Only the owner's
// CLI uses it (hide, unhide, purge): a tool must always use Resolve.
func (r *Resolver) ResolveAny(ctx context.Context, input string) (Target, error) {
	return r.resolve(ctx, input, true)
}

func (r *Resolver) resolve(ctx context.Context, input string, allowHidden bool) (Target, error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return Target{}, invalidArgument("informe o nome ou o contact_ref do contato")
	}
	if looksLikePhoneOrJID(in) {
		return Target{}, phoneNotAllowed()
	}
	cands, err := r.candidates(ctx)
	if err != nil {
		return Target{}, err
	}
	blocked := func(c Candidate) bool { return c.Hidden && !allowHidden }

	if strings.HasPrefix(in, RefPrefix) {
		for _, c := range cands {
			if c.Ref != in {
				continue
			}
			if blocked(c) {
				return Target{}, chatHidden()
			}
			return targetOf(c), nil
		}
		return Target{}, contactNotFound(in)
	}

	q := Normalize(in)
	if q == "" {
		return Target{}, invalidArgument("nome de contato sem letras ou números")
	}
	qWords := strings.Fields(q)

	// Best level that any candidate reaches, hidden ones included.
	var matches []Candidate
	best := -1
	for _, c := range cands {
		lvl := matchLevel(q, qWords, Normalize(c.Name))
		if lvl == "" {
			continue
		}
		rank := levelRank(lvl)
		if best < 0 || rank < best {
			best = rank
			matches = matches[:0]
		}
		if rank == best {
			matches = append(matches, c)
		}
	}
	if len(matches) == 0 {
		return Target{}, contactNotFound(in)
	}

	var visible []Candidate
	for _, c := range matches {
		if !blocked(c) {
			visible = append(visible, c)
		}
	}
	if (len(matches) == 1 && blocked(matches[0])) || len(visible) == 0 {
		return Target{}, chatHidden()
	}
	if len(matches) == 1 {
		return targetOf(matches[0]), nil
	}
	return Target{}, r.ambiguous(visible)
}

// Search returns the visible candidates that match query, strongest match
// first, then the most recent interaction, then name. limit defaults to 10 and
// is capped at 30. Hidden chats are never returned.
func (r *Resolver) Search(ctx context.Context, query string, limit int) ([]Match, error) {
	in := strings.TrimSpace(query)
	if in == "" {
		return nil, invalidArgument("informe a busca")
	}
	if looksLikePhoneOrJID(in) {
		return nil, phoneNotAllowed()
	}
	q := Normalize(in)
	if q == "" {
		return nil, invalidArgument("busca sem letras ou números")
	}
	qWords := strings.Fields(q)
	cands, err := r.candidates(ctx)
	if err != nil {
		return nil, err
	}

	var out []Match
	for _, c := range cands {
		if c.Hidden {
			continue
		}
		if lvl := matchLevel(q, qWords, Normalize(c.Name)); lvl != "" {
			out = append(out, Match{Target: targetOf(c), Level: lvl})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := levelRank(out[i].Level), levelRank(out[j].Level); ri != rj {
			return ri < rj
		}
		return lessRecent(out[i].Target, out[j].Target)
	})
	if limit <= 0 {
		limit = 10
	}
	if limit > 30 {
		limit = 30
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *Resolver) candidates(ctx context.Context) ([]Candidate, error) {
	cands, err := r.src.Candidates(ctx)
	if err != nil {
		return nil, fmt.Errorf("identity: listar contatos: %w", err)
	}
	return cands, nil
}

// ambiguous builds ambiguous_contact with up to five candidates, the most
// recent first. Each candidate has name, contact_ref, categories and
// last_interaction_ago, which is all the model needs to pick one.
func (r *Resolver) ambiguous(visible []Candidate) error {
	now := r.clk.Now()
	ranked := make([]Target, 0, len(visible))
	for _, c := range visible {
		ranked = append(ranked, targetOf(c))
	}
	sort.SliceStable(ranked, func(i, j int) bool { return lessRecent(ranked[i], ranked[j]) })
	if len(ranked) > maxCandidates {
		ranked = ranked[:maxCandidates]
	}
	list := make([]map[string]any, 0, len(ranked))
	for _, t := range ranked {
		cats := t.Categories
		if cats == nil {
			cats = []string{}
		}
		list = append(list, map[string]any{
			"name":                 t.Name,
			"contact_ref":          t.Ref,
			"categories":           cats,
			"last_interaction_ago": Ago(now, t.LastInteraction),
		})
	}
	return toolerr.New(toolerr.CodeAmbiguousContact,
		"Mais de um contato corresponde. Repita com o contact_ref de um dos candidatos.",
		map[string]any{"candidates": list, "total": len(visible)})
}

// lessRecent orders by most recent interaction first, then name and ref.
func lessRecent(a, b Target) bool {
	if !a.LastInteraction.Equal(b.LastInteraction) {
		return a.LastInteraction.After(b.LastInteraction)
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.Ref < b.Ref
}

func targetOf(c Candidate) Target {
	return Target{
		JID:             c.JID,
		Ref:             c.Ref,
		Name:            c.Name,
		Kind:            c.Kind,
		HasChat:         c.HasChat,
		Categories:      c.Categories,
		LastInteraction: c.LastInteraction,
		Hidden:          c.Hidden,
	}
}

func invalidArgument(msg string) error {
	return toolerr.New(toolerr.CodeInvalidArgument, msg, nil)
}

func phoneNotAllowed() error {
	return toolerr.New(toolerr.CodePhoneNotAllowed,
		"Não aceito número de telefone ou JID. Use o nome ou o contact_ref do contato.", nil)
}

func chatHidden() error {
	return toolerr.New(toolerr.CodeChatHidden, "Esta conversa está oculta e não pode ser usada.", nil)
}

func contactNotFound(in string) error {
	return toolerr.New(toolerr.CodeContactNotFound,
		fmt.Sprintf("Nenhum contato encontrado para %q.", in), nil)
}
