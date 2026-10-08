// Package category edits the local categories (labels with source "local").
// WhatsApp labels and lists come from the phone and are never changed here.
// The set_contact_category tool and the `category` CLI command share it.
package category

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/riknog/whatsapp-mcp/internal/identity"
	"github.com/riknog/whatsapp-mcp/internal/privacy"
	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

const (
	// Groups and None are the implicit categories: every group is in Groups, and
	// a chat without any label is in None. Neither can be set by hand.
	Groups = "Grupos"
	None   = "Sem categoria"
	// SourceLocal marks a label created on this computer.
	SourceLocal = "local"
	// MaxRunes caps a local category name.
	MaxRunes = 50
	idPrefix = "local:"
)

// DisplayName is the name of a label as the model and the CLI see it: a phone
// number written into a label name by the owner is redacted.
func DisplayName(l store.Label) string { return privacy.RedactText(l.Name) }

// CheckName validates a local category name: not empty, short, not an
// implicit category, and not something that looks like a phone number.
func CheckName(cat string) error {
	if cat == "" {
		return invalid("Informe o nome da categoria.")
	}
	if utf8.RuneCountInString(cat) > MaxRunes {
		return invalid("Nome de categoria com mais de 50 caracteres.")
	}
	if k := identity.Normalize(cat); k == identity.Normalize(Groups) || k == identity.Normalize(None) || k == "" {
		return invalid("Essa categoria é automática e não pode ser usada.")
	}
	return identity.CheckFreeText(cat)
}

// Unknown is the error for a category name that does not exist.
func Unknown() error {
	return invalid("Categoria desconhecida. Use list_categories para ver as categorias.")
}

// Set adds (on) or removes a local category on target. Names compare without
// case and accents. Adding creates the category when needed, and an empty chat
// row for a saved contact that has no chat yet. Removing an unknown category is
// an error; a WhatsApp label is refused either way.
func Set(ctx context.Context, st *store.Store, target identity.Target, cat string, on bool) error {
	if err := CheckName(cat); err != nil {
		return err
	}
	key := identity.Normalize(cat)
	id := idPrefix + key
	labels, err := st.ListLabels(ctx)
	if err != nil {
		return err
	}
	exists := false
	for _, l := range labels {
		if identity.Normalize(DisplayName(l)) != key {
			continue
		}
		if l.Source != SourceLocal {
			return invalid("Essa é uma etiqueta/lista do WhatsApp; ela só pode ser alterada no celular.")
		}
		id, exists = l.ID, true
	}

	if !on {
		if !exists {
			return Unknown()
		}
		if !target.HasChat {
			return nil
		}
		return mapNotFound(st.SetChatLabel(ctx, target.JID, id, false))
	}
	if !exists {
		if err := st.UpsertLabel(ctx, store.Label{ID: id, Name: cat, Source: SourceLocal}); err != nil {
			return err
		}
	}
	if !target.HasChat {
		// A category hangs on a chat row; a saved contact without one gets an empty chat.
		if err := st.UpsertChat(ctx, store.Chat{JID: target.JID, Ref: target.Ref, Kind: target.Kind}); err != nil {
			return err
		}
	}
	return mapNotFound(st.SetChatLabel(ctx, target.JID, id, true))
}

// mapNotFound turns a vanished chat into contact_not_found.
func mapNotFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return toolerr.New(toolerr.CodeContactNotFound, "Contato não encontrado.", nil)
	}
	return err
}

func invalid(msg string) error { return toolerr.New(toolerr.CodeInvalidArgument, msg, nil) }
