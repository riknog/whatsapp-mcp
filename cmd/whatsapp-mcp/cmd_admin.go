package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	localcat "github.com/riknog/whatsapp-mcp/internal/category"
	"github.com/riknog/whatsapp-mcp/internal/identity"
	"github.com/riknog/whatsapp-mcp/internal/privacy"
	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

// The admin commands only touch data.db: they run while serve is running, and
// serve sees each change on its next tool call.

// errUsage marks a wrong command line; run prints the usage and exits with 2.
var errUsage = errors.New("uso incorreto")

// label is how the CLI names a contact: name and contact_ref. The CLI may show
// the ref; phone numbers are still redacted.
func label(name, ref string) string {
	return fmt.Sprintf("%s (%s)", privacy.RedactText(name), ref)
}

// cmdHide hides (on) or shows a chat.
func cmdHide(ctx context.Context, a *app, out io.Writer, args []string, on bool) error {
	if len(args) != 1 {
		return errUsage
	}
	t, err := a.resolve(ctx, args[0])
	if err != nil {
		return err
	}
	if !t.HasChat {
		if !on {
			return toolerr.New(toolerr.CodeInvalidArgument,
				fmt.Sprintf("Não há conversa com %s para mostrar.", privacy.RedactText(t.Name)), nil)
		}
		// A saved contact without a conversation yet: the chat row is created
		// now, already hidden, so the first message never reaches the model.
		if err := a.st.UpsertChat(ctx, store.Chat{JID: t.JID, Ref: t.Ref, Kind: "direct"}); err != nil {
			return err
		}
	}
	if t.Hidden == on {
		fmt.Fprintf(out, "Nada a fazer: %s já está %s.\n", label(t.Name, t.Ref), hiddenWord(on))
		return nil
	}
	if err := a.st.SetHidden(ctx, t.JID, on); err != nil {
		return err
	}
	action := "unhide"
	if on {
		action = "hide"
	}
	a.audit(ctx, action, t.Ref, "cli")
	fmt.Fprintf(out, "%s agora está %s.\n", label(t.Name, t.Ref), hiddenWord(on))
	if on {
		fmt.Fprintln(out, "O Claude não vê mais esta conversa (nem na busca) e não pode enviar para ela.")
	}
	return nil
}

func hiddenWord(on bool) string {
	if on {
		return "oculta"
	}
	return "visível"
}

// cmdHidden lists the hidden chats.
func cmdHidden(ctx context.Context, a *app, out io.Writer, args []string) error {
	if len(args) != 0 {
		return errUsage
	}
	cands, err := a.source.Candidates(ctx)
	if err != nil {
		return err
	}
	var hidden []identity.Candidate
	for _, c := range cands {
		if c.Hidden {
			hidden = append(hidden, c)
		}
	}
	if len(hidden) == 0 {
		fmt.Fprintln(out, "Nenhuma conversa oculta.")
		return nil
	}
	sortedByName(hidden)
	fmt.Fprintf(out, "Conversas ocultas (%d):\n", len(hidden))
	for _, c := range hidden {
		fmt.Fprintf(out, "  - %s\n", label(c.Name, c.Ref))
	}
	return nil
}

// cmdShareable manages the allowlist of contacts that share_contact may send.
func cmdShareable(ctx context.Context, a *app, out io.Writer, args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errUsage
		}
		return shareableList(ctx, a, out)
	case "add", "remove":
		if len(args) != 2 {
			return errUsage
		}
	default:
		return errUsage
	}
	t, err := a.resolve(ctx, args[1])
	if err != nil {
		return err
	}
	if t.Kind == "group" {
		return toolerr.New(toolerr.CodeInvalidArgument, "Grupos não podem ser compartilhados.", nil)
	}
	if args[0] == "add" {
		if err := a.st.AddShareable(ctx, t.JID); err != nil {
			return err
		}
		a.audit(ctx, "shareable_add", t.Ref, "cli")
		fmt.Fprintf(out, "%s pode ser compartilhado pelo Claude (share_contact).\n", label(t.Name, t.Ref))
		return nil
	}
	if err := a.st.RemoveShareable(ctx, t.JID); err != nil {
		return err
	}
	a.audit(ctx, "shareable_remove", t.Ref, "cli")
	fmt.Fprintf(out, "%s não pode mais ser compartilhado.\n", label(t.Name, t.Ref))
	return nil
}

func shareableList(ctx context.Context, a *app, out io.Writer) error {
	jids, err := a.st.ListShareable(ctx)
	if err != nil {
		return err
	}
	if len(jids) == 0 {
		fmt.Fprintln(out, "Nenhum contato liberado para compartilhamento. Use: whatsapp-mcp shareable add \"Nome\"")
		return nil
	}
	by, err := a.candidatesBy(ctx)
	if err != nil {
		return err
	}
	list := make([]identity.Candidate, 0, len(jids))
	for _, jid := range jids {
		c, ok := by[jid]
		if !ok {
			c = identity.Candidate{Name: "Desconhecido", Ref: a.refs.Ref(jid)}
		}
		list = append(list, c)
	}
	sortedByName(list)
	fmt.Fprintf(out, "Contatos que o Claude pode compartilhar (%d):\n", len(list))
	for _, c := range list {
		fmt.Fprintf(out, "  - %s\n", label(c.Name, c.Ref))
	}
	if !a.cfg.Share.Enabled {
		fmt.Fprintln(out, "Atenção: share.enabled está desligado na config; nenhum contato é compartilhado.")
	}
	return nil
}

// cmdCategory manages local categories: add/remove "Categoria" "Nome", list.
func cmdCategory(ctx context.Context, a *app, out io.Writer, args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errUsage
		}
		return categoryList(ctx, a, out)
	case "add", "remove":
		if len(args) != 3 {
			return errUsage
		}
	default:
		return errUsage
	}
	cat := strings.TrimSpace(args[1])
	t, err := a.resolve(ctx, args[2])
	if err != nil {
		return err
	}
	on := args[0] == "add"
	if err := localcat.Set(ctx, a.st, t, cat, on); err != nil {
		return err
	}
	if on {
		fmt.Fprintf(out, "%s agora está na categoria %q.\n", label(t.Name, t.Ref), cat)
	} else {
		fmt.Fprintf(out, "%s saiu da categoria %q.\n", label(t.Name, t.Ref), cat)
	}
	return nil
}

func categoryList(ctx context.Context, a *app, out io.Writer) error {
	labels, err := a.st.ListLabels(ctx)
	if err != nil {
		return err
	}
	cands, err := a.source.Candidates(ctx)
	if err != nil {
		return err
	}
	count := map[string]int{}
	for _, c := range cands {
		for _, name := range c.Categories {
			count[identity.Normalize(name)]++
		}
	}
	if len(labels) == 0 {
		fmt.Fprintln(out, "Nenhuma categoria. Crie uma com: whatsapp-mcp category add \"Categoria\" \"Nome\"")
		return nil
	}
	fmt.Fprintln(out, "Categorias:")
	for _, l := range labels {
		name := localcat.DisplayName(l)
		origin := "WhatsApp (só muda no celular)"
		if l.Source == localcat.SourceLocal {
			origin = "local"
		}
		fmt.Fprintf(out, "  - %s: %d contato(s), %s\n", name, count[identity.Normalize(name)], origin)
	}
	return nil
}
