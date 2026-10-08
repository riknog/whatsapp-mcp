package mcpserver

import (
	"strings"
	"testing"

	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

type contactsResult struct {
	Categories []struct {
		Name     string `json:"name"`
		Source   string `json:"source"`
		Contacts []struct {
			Name               string `json:"name"`
			ContactRef         string `json:"contact_ref"`
			Shareable          bool   `json:"shareable"`
			LastInteractionAgo string `json:"last_interaction_ago"`
		} `json:"contacts"`
	} `json:"categories"`
	Total int      `json:"total"`
	Notes []string `json:"notes"`
}

func listContacts(t *testing.T, f *fixture, args map[string]any) contactsResult {
	t.Helper()
	res := f.call("list_contacts", args)
	mustOK(t, res)
	var out contactsResult
	decodeInto(t, res, &out)
	return out
}

// memberships counts the contacts of a result, over all categories.
func (c contactsResult) memberships() int {
	n := 0
	for _, cat := range c.Categories {
		n += len(cat.Contacts)
	}
	return n
}

// categoryNamed returns the category with this name, or nil.
func (c contactsResult) categoryNamed(name string) *struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	Contacts []struct {
		Name               string `json:"name"`
		ContactRef         string `json:"contact_ref"`
		Shareable          bool   `json:"shareable"`
		LastInteractionAgo string `json:"last_interaction_ago"`
	} `json:"contacts"`
} {
	for i := range c.Categories {
		if c.Categories[i].Name == name {
			return &c.Categories[i]
		}
	}
	return nil
}

// TestListContactsDefaultExcludesGroupsAndHidden checks the default listing:
// every visible direct contact, once per label, and no group, hidden or phone-only contact.
func TestListContactsDefaultExcludesGroupsAndHidden(t *testing.T) {
	f := newFixture(t)
	want := len(fxNamedOnly)
	for _, c := range f.direct {
		if n := len(c.labels); n > 0 {
			want += n
		} else {
			want++
		}
	}
	out := listContacts(t, f, map[string]any{"limit": 200})
	if out.Total != want || out.memberships() != want {
		t.Errorf("total = %d (listed %d), want %d", out.Total, out.memberships(), want)
	}
	if c := out.categoryNamed("Grupos"); c != nil {
		t.Errorf("groups listed without include_groups")
	}
	for _, cat := range out.Categories {
		for _, c := range cat.Contacts {
			if c.Name == "Banco Exemplo" {
				t.Errorf("hidden contact listed under %s", cat.Name)
			}
			if c.Name == "Desconhecido" && c.ContactRef != f.direct[4].ref {
				t.Errorf("a contact without a name was listed: %s", c.ContactRef)
			}
		}
	}
	mae := out.categoryNamed("Família")
	if mae == nil || mae.Source != "whatsapp" {
		t.Fatalf("Família = %+v", mae)
	}
	if c := mae.Contacts[0]; c.Name != "Mãe" || c.LastInteractionAgo != "há 12 min" {
		t.Errorf("Mãe = %+v", c)
	}
}

// TestListContactsCategoriesAndSources checks the category filter, sources and shareable flags.
func TestListContactsCategoriesAndSources(t *testing.T) {
	f := newFixture(t)
	clientes := 0
	for _, c := range f.direct {
		for _, l := range c.labels {
			if l == "Clientes" {
				clientes++
			}
		}
	}
	local := listContacts(t, f, map[string]any{"category": "clientes", "limit": 200})
	if local.Total != clientes {
		t.Errorf("Clientes total = %d, want %d (hidden chat excluded)", local.Total, clientes)
	}
	if cat := local.categoryNamed("Clientes"); cat == nil || cat.Source != "local" || len(cat.Contacts) != clientes {
		t.Errorf("Clientes = %+v", cat)
	}

	withGroups := listContacts(t, f, map[string]any{"category": "Grupos", "limit": 200})
	grp := withGroups.categoryNamed("Grupos")
	if grp == nil || grp.Source != "implicit" || len(grp.Contacts) != fxGroups || withGroups.Total != fxGroups {
		t.Fatalf("Grupos = %+v total %d", grp, withGroups.Total)
	}

	all := listContacts(t, f, map[string]any{"include_groups": true, "limit": 200})
	if c := all.categoryNamed("Família"); c == nil || len(c.Contacts) != 3 {
		t.Errorf("Família with groups = %+v, want 3 members", c)
	}

	none := 0
	for _, c := range f.direct {
		if len(c.labels) == 0 {
			none++
		}
	}
	semCat := listContacts(t, f, map[string]any{"category": "Sem categoria", "limit": 200})
	if semCat.Total != none+len(fxNamedOnly) {
		t.Errorf("Sem categoria total = %d, want %d", semCat.Total, none+len(fxNamedOnly))
	}

	// Only João Ávila is shareable; he has one category, so one membership.
	shareable := 0
	for _, cat := range all.Categories {
		for _, c := range cat.Contacts {
			if c.Shareable {
				shareable++
				if c.Name != "João Ávila" {
					t.Errorf("shareable = %q, want only João Ávila", c.Name)
				}
			}
		}
	}
	if shareable != 1 {
		t.Errorf("shareable memberships = %d, want 1", shareable)
	}

	res := f.call("list_contacts", map[string]any{"category": "Nada"})
	if got := errorCode(t, res); got != string(toolerr.CodeInvalidArgument) {
		t.Errorf("unknown category: code = %q", got)
	}
}

// TestListContactsPagingAndNotes checks paging over the memberships, and the limit note.
func TestListContactsPagingAndNotes(t *testing.T) {
	f := newFixture(t)
	first := listContacts(t, f, map[string]any{"limit": 5})
	if first.memberships() != 5 || first.Total <= 5 {
		t.Fatalf("first page = %d, total %d", first.memberships(), first.Total)
	}
	second := listContacts(t, f, map[string]any{"limit": 5, "offset": 5})
	if second.memberships() != 5 || second.Total != first.Total {
		t.Fatalf("second page = %d, total %d", second.memberships(), second.Total)
	}
	firstNames := map[string]bool{}
	for _, c := range first.Categories {
		for _, m := range c.Contacts {
			firstNames[c.Name+"/"+m.ContactRef] = true
		}
	}
	for _, c := range second.Categories {
		for _, m := range c.Contacts {
			if firstNames[c.Name+"/"+m.ContactRef] {
				t.Errorf("membership %s/%s on two pages", c.Name, m.Name)
			}
		}
	}
	big := listContacts(t, f, map[string]any{"limit": 500})
	if len(big.Notes) != 1 || !strings.Contains(big.Notes[0], "limit reduzido de 500 para o máximo de 200") {
		t.Errorf("notes = %v", big.Notes)
	}
}

// searchContactsResult decodes a search_contacts output.
type searchContactsResult struct {
	Matches []struct {
		Name               string   `json:"name"`
		ContactRef         string   `json:"contact_ref"`
		Categories         []string `json:"categories"`
		Match              string   `json:"match"`
		LastInteractionAgo string   `json:"last_interaction_ago"`
	} `json:"matches"`
	Notes []string `json:"notes"`
}

func searchContacts(t *testing.T, f *fixture, args map[string]any) searchContactsResult {
	t.Helper()
	res := f.call("search_contacts", args)
	mustOK(t, res)
	var out searchContactsResult
	decodeInto(t, res, &out)
	return out
}

// TestSearchContactsMatchesAccentsAndHidden checks name matching and the hidden rule.
func TestSearchContactsMatchesAccentsAndHidden(t *testing.T) {
	f := newFixture(t)
	mae := searchContacts(t, f, map[string]any{"query": "mae"})
	if len(mae.Matches) != 1 || mae.Matches[0].Name != "Mãe" || mae.Matches[0].Match != "exact" {
		t.Fatalf("mae = %+v", mae.Matches)
	}
	if mae.Matches[0].ContactRef != f.direct[0].ref || len(mae.Matches[0].Categories) != 1 || mae.Matches[0].Categories[0] != "Família" {
		t.Errorf("mae match = %+v", mae.Matches[0])
	}

	ana := searchContacts(t, f, map[string]any{"query": "ANA"})
	names := map[string]string{}
	for _, m := range ana.Matches {
		names[m.Name] = m.Match
	}
	if names["Ana Clara"] != "prefix" || names["Ana Paula"] != "prefix" || len(ana.Matches) != 2 {
		t.Errorf("ana = %v", names)
	}

	if got := searchContacts(t, f, map[string]any{"query": "banco"}); len(got.Matches) != 0 {
		t.Errorf("hidden contact found: %+v", got.Matches)
	}

	tokens := searchContacts(t, f, map[string]any{"query": "alves ricardo"})
	if len(tokens.Matches) != 1 || tokens.Matches[0].Match != "token" {
		t.Errorf("token match = %+v", tokens.Matches)
	}
}

// TestSearchContactsLimitsAndErrors checks the limit note and the error codes.
func TestSearchContactsLimitsAndErrors(t *testing.T) {
	f := newFixture(t)
	capped := searchContacts(t, f, map[string]any{"query": "contato", "limit": 40})
	if len(capped.Matches) != 30 || len(capped.Notes) != 1 {
		t.Errorf("limit 40: matches=%d notes=%v", len(capped.Matches), capped.Notes)
	}
	few := searchContacts(t, f, map[string]any{"query": "contato", "limit": 2})
	if len(few.Matches) != 2 {
		t.Errorf("limit 2: matches=%d", len(few.Matches))
	}
	if got := errorCode(t, f.call("search_contacts", map[string]any{"query": "5511900000001"})); got != string(toolerr.CodePhoneNotAllowed) {
		t.Errorf("phone: code = %q", got)
	}
	if got := errorCode(t, f.call("search_contacts", map[string]any{"query": "  "})); got != string(toolerr.CodeInvalidArgument) {
		t.Errorf("empty: code = %q", got)
	}
}

// TestListCategories checks the categories, their sources and counts.
func TestListCategories(t *testing.T) {
	f := newFixture(t)
	res := f.call("list_categories", nil)
	mustOK(t, res)
	var out struct {
		Categories []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
			Count  int    `json:"count"`
		} `json:"categories"`
	}
	decodeInto(t, res, &out)

	got := map[string]struct {
		source string
		count  int
	}{}
	var order []string
	for _, c := range out.Categories {
		got[c.Name] = struct {
			source string
			count  int
		}{c.Source, c.Count}
		order = append(order, c.Name)
	}
	if strings.Join(order, ",") != "Clientes,Família,Trabalho,Grupos,Sem categoria" {
		t.Errorf("order = %v", order)
	}

	famCount, trabCount, clientesCount, semCount := 2+1, 0, 0, len(fxNamedOnly)
	for _, c := range f.direct {
		for _, l := range c.labels {
			switch l {
			case "Trabalho":
				trabCount++
			case "Clientes":
				clientesCount++
			}
		}
		if len(c.labels) == 0 {
			semCount++
		}
	}
	trabCount++ // Projeto Alfa
	checks := map[string]int{
		"Família": famCount, "Trabalho": trabCount, "Clientes": clientesCount,
		"Grupos": fxGroups, "Sem categoria": semCount,
	}
	for name, want := range checks {
		if got[name].count != want {
			t.Errorf("%s count = %d, want %d", name, got[name].count, want)
		}
	}
	if got["Família"].source != "whatsapp" || got["Clientes"].source != "local" || got["Grupos"].source != "implicit" {
		t.Errorf("sources = %+v", got)
	}
}
