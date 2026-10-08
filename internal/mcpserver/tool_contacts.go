package mcpserver

import (
	"context"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/riknog/whatsapp-mcp/internal/identity"
)

// Limits of the contact tools (docs/02-TOOLS.md).
const (
	defaultContacts = 50
	maxContacts     = 200
	defaultFind     = 10
	maxFind         = 30
)

type contactsIn struct {
	Category      string `json:"category,omitempty" jsonschema:"only this category; see list_categories"`
	Limit         int    `json:"limit,omitempty" jsonschema:"category memberships to return (default 50, max 200)"`
	Offset        int    `json:"offset,omitempty" jsonschema:"category memberships to skip"`
	IncludeGroups bool   `json:"include_groups,omitempty" jsonschema:"include groups (default false)"`
}

type contactOut struct {
	Name               string `json:"name"`
	ContactRef         string `json:"contact_ref"`
	Shareable          bool   `json:"shareable" jsonschema:"allowed to be sent with share_contact"`
	LastInteractionAgo string `json:"last_interaction_ago"`
}

type categoryOut struct {
	Name     string           `json:"name"`
	Source   string           `json:"source" jsonschema:"whatsapp, local or implicit"`
	Contacts list[contactOut] `json:"contacts"`
}

type contactsOut struct {
	Categories list[categoryOut] `json:"categories"`
	Total      int               `json:"total" jsonschema:"category memberships that match the filters, before paging"`
	Notes      list[string]      `json:"notes,omitempty"`
	Error      *errBody          `json:"error,omitempty"`
}

func (o *contactsOut) setError(e *errBody) { o.Error = e }

type searchContactsIn struct {
	Query string `json:"query,omitempty" jsonschema:"name or part of a name (required)"`
	Limit int    `json:"limit,omitempty" jsonschema:"matches to return (default 10, max 30)"`
}

type matchOut struct {
	Name               string       `json:"name"`
	ContactRef         string       `json:"contact_ref"`
	Categories         list[string] `json:"categories"`
	Match              string       `json:"match" jsonschema:"exact, prefix, contains or token"`
	LastInteractionAgo string       `json:"last_interaction_ago"`
}

type searchContactsOut struct {
	Matches list[matchOut] `json:"matches"`
	Notes   list[string]   `json:"notes,omitempty"`
	Error   *errBody       `json:"error,omitempty"`
}

func (o *searchContactsOut) setError(e *errBody) { o.Error = e }

type categoryCountOut struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Count  int    `json:"count"`
}

type categoriesOut struct {
	Categories list[categoryCountOut] `json:"categories"`
	Error      *errBody               `json:"error,omitempty"`
}

func (o *categoriesOut) setError(e *errBody) { o.Error = e }

func (e *env) listContacts(ctx context.Context, _ *mcp.CallToolRequest, in contactsIn) (*mcp.CallToolResult, contactsOut, error) {
	out, err := e.doListContacts(ctx, in)
	if err != nil {
		return failed[contactsOut](err)
	}
	return nil, out, nil
}

// membership is one (category, contact) pair of list_contacts.
type membership struct {
	category string
	source   string
	ent      entry
}

func (e *env) doListContacts(ctx context.Context, in contactsIn) (contactsOut, error) {
	var out contactsOut
	if err := e.requireSession(); err != nil {
		return out, err
	}
	limit := clampInt("limit", in.Limit, defaultContacts, maxContacts, &out.Notes)
	offset := in.Offset
	if offset < 0 {
		offset = 0
	}
	categories, err := e.categories(ctx)
	if err != nil {
		return out, err
	}
	var filter *category
	if name := trimmed(in.Category); name != "" {
		c, ok := findCategory(categories, name)
		if !ok {
			return out, unknownCategory()
		}
		filter = &c
	}
	// Grupos is itself a request for groups.
	includeGroups := in.IncludeGroups || (filter != nil && filter.name == catGroups)

	ents, err := e.entries(ctx)
	if err != nil {
		return out, err
	}
	order := categoryOrder(categories)
	var ms []membership
	for _, en := range ents {
		if en.cand.Kind == "group" && !includeGroups {
			continue
		}
		for _, c := range en.cats {
			if filter != nil && !hasCategory([]string{c}, filter.name) {
				continue
			}
			ms = append(ms, membership{category: c, source: sourceOf(categories, c), ent: en})
		}
	}
	sort.SliceStable(ms, func(i, j int) bool {
		a, b := ms[i], ms[j]
		if oa, ob := order[identity.Normalize(a.category)], order[identity.Normalize(b.category)]; oa != ob {
			return oa < ob
		}
		if a.ent.cand.Name != b.ent.cand.Name {
			return a.ent.cand.Name < b.ent.cand.Name
		}
		return a.ent.cand.Ref < b.ent.cand.Ref
	})

	out.Total = len(ms)
	out.Categories = list[categoryOut]{}
	now := e.clk.Now().In(e.loc)
	if offset > len(ms) {
		offset = len(ms)
	}
	end := offset + limit
	if end > len(ms) {
		end = len(ms)
	}
	for _, m := range ms[offset:end] {
		shareable, err := e.st.IsShareable(ctx, m.ent.cand.JID)
		if err != nil {
			return out, err
		}
		c := contactOut{
			Name:               m.ent.cand.Name,
			ContactRef:         m.ent.cand.Ref,
			Shareable:          shareable,
			LastInteractionAgo: identity.Ago(now, m.ent.cand.LastInteraction),
		}
		if n := len(out.Categories); n == 0 || out.Categories[n-1].Name != m.category {
			out.Categories = append(out.Categories, categoryOut{Name: m.category, Source: m.source, Contacts: list[contactOut]{}})
		}
		last := &out.Categories[len(out.Categories)-1]
		last.Contacts = append(last.Contacts, c)
	}
	return out, nil
}

func (e *env) searchContacts(ctx context.Context, _ *mcp.CallToolRequest, in searchContactsIn) (*mcp.CallToolResult, searchContactsOut, error) {
	out, err := e.doSearchContacts(ctx, in)
	if err != nil {
		return failed[searchContactsOut](err)
	}
	return nil, out, nil
}

func (e *env) doSearchContacts(ctx context.Context, in searchContactsIn) (searchContactsOut, error) {
	var out searchContactsOut
	if err := e.requireSession(); err != nil {
		return out, err
	}
	limit := clampInt("limit", in.Limit, defaultFind, maxFind, &out.Notes)
	matches, err := e.resolver.Search(ctx, in.Query, limit)
	if err != nil {
		return out, err
	}
	now := e.clk.Now().In(e.loc)
	out.Matches = list[matchOut]{}
	for _, m := range matches {
		out.Matches = append(out.Matches, matchOut{
			Name:               m.Name,
			ContactRef:         m.Ref,
			Categories:         categoriesOf(m.Kind, m.Categories),
			Match:              m.Level,
			LastInteractionAgo: identity.Ago(now, m.LastInteraction),
		})
	}
	return out, nil
}

func (e *env) listCategories(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, categoriesOut, error) {
	out, err := e.doListCategories(ctx)
	if err != nil {
		return failed[categoriesOut](err)
	}
	return nil, out, nil
}

func (e *env) doListCategories(ctx context.Context) (categoriesOut, error) {
	var out categoriesOut
	if err := e.requireSession(); err != nil {
		return out, err
	}
	categories, err := e.categories(ctx)
	if err != nil {
		return out, err
	}
	ents, err := e.entries(ctx)
	if err != nil {
		return out, err
	}
	count := map[string]int{}
	for _, en := range ents {
		for _, c := range en.cats {
			count[identity.Normalize(c)]++
		}
	}
	out.Categories = list[categoryCountOut]{}
	for _, c := range categories {
		out.Categories = append(out.Categories, categoryCountOut{
			Name:   c.name,
			Source: c.source,
			Count:  count[identity.Normalize(c.name)],
		})
	}
	return out, nil
}

// categoryOrder maps each category, by normalized name, to its position in
// the display order. Unknown names sort last.
func categoryOrder(categories []category) map[string]int {
	order := make(map[string]int, len(categories))
	for i, c := range categories {
		order[identity.Normalize(c.name)] = i
	}
	return order
}

// sourceOf returns the source of a category name; implicit when it is not listed.
func sourceOf(categories []category, name string) string {
	if c, ok := findCategory(categories, name); ok {
		return c.source
	}
	return sourceImpl
}
