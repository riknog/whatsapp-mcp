package identity

import (
	"context"
	"fmt"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/privacy"
	"github.com/riknog/whatsapp-mcp/internal/store"
)

// pageSize is the page used to read every chat and contact. It is the store's maximum.
const pageSize = 200

// StoreSource implements ContactSource over *store.Store with the store's
// exported methods only. It returns every chat, hidden ones included (the
// resolver needs them to answer chat_hidden), and every named contact that has
// no chat.
type StoreSource struct {
	st   *store.Store
	refs *Refs
}

// NewStoreSource builds the adapter. refs computes the ref of named contacts
// that have no chat row; chat rows keep the ref stored at ingest.
func NewStoreSource(st *store.Store, refs *Refs) *StoreSource {
	return &StoreSource{st: st, refs: refs}
}

// Candidates reads every chat and every named contact. Categories are the
// names of the chat's non-deleted labels; hidden chats get none.
func (s *StoreSource) Candidates(ctx context.Context) ([]Candidate, error) {
	contacts, err := s.allContacts(ctx)
	if err != nil {
		return nil, err
	}
	chats, err := s.allChats(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]Candidate, 0, len(chats)+len(contacts))
	seen := make(map[string]bool, len(chats))
	for _, ch := range chats {
		seen[ch.JID] = true
		cand := Candidate{
			JID:             ch.JID,
			Ref:             ch.Ref,
			Name:            DisplayName(contacts[ch.JID], ch),
			Kind:            ch.Kind,
			HasChat:         true,
			Hidden:          ch.Hidden,
			LastInteraction: unixOrZero(ch.LastMessageAt),
		}
		if !ch.Hidden {
			cats, err := s.categories(ctx, ch.JID)
			if err != nil {
				return nil, err
			}
			cand.Categories = cats
		}
		out = append(out, cand)
	}

	if s.refs == nil {
		return out, nil
	}
	for jid, c := range contacts {
		name := DisplayName(c, store.Chat{Kind: "direct"})
		if seen[jid] || name == unknownName {
			continue
		}
		out = append(out, Candidate{JID: jid, Ref: s.refs.Ref(jid), Name: name, Kind: "direct"})
	}
	return out, nil
}

func (s *StoreSource) allChats(ctx context.Context) ([]store.Chat, error) {
	var all []store.Chat
	for offset := 0; ; offset += pageSize {
		page, err := s.st.ListChats(ctx, store.ChatFilter{IncludeHidden: true, Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, fmt.Errorf("identity: ler conversas: %w", err)
		}
		all = append(all, page...)
		if len(page) < pageSize {
			return all, nil
		}
	}
}

// allContacts maps each JID with a contacts row to that row.
func (s *StoreSource) allContacts(ctx context.Context) (map[string]store.Contact, error) {
	out := make(map[string]store.Contact)
	for offset := 0; ; offset += pageSize {
		page, err := s.st.ListContacts(ctx, store.ContactFilter{Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, fmt.Errorf("identity: ler contatos: %w", err)
		}
		for _, c := range page {
			out[c.JID] = c
		}
		if len(page) < pageSize {
			return out, nil
		}
	}
}

func (s *StoreSource) categories(ctx context.Context, chatJID string) ([]string, error) {
	labels, err := s.st.LabelsOf(ctx, chatJID)
	if err != nil {
		return nil, fmt.Errorf("identity: ler categorias: %w", err)
	}
	names := []string{}
	for _, l := range labels {
		if !l.Deleted {
			// Label names are free text: a phone typed into one never leaves.
			names = append(names, privacy.RedactText(l.Name))
		}
	}
	return names, nil
}

func unixOrZero(ts int64) time.Time {
	if ts <= 0 {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}
