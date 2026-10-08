package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/privacy"
)

// cmdPurge deletes stored messages (LGPD). Without --yes it only says what
// would be deleted and returns errNotConfirmed.
func cmdPurge(ctx context.Context, a *app, out io.Writer, args []string) error {
	fs := flag.NewFlagSet("purge", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	olderThan := fs.String("older-than", "", "")
	contact := fs.String("contact", "", "")
	all := fs.Bool("all", false, "")
	yes := fs.Bool("yes", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errUsage
	}
	modes := 0
	for _, set := range []bool{*olderThan != "", *contact != "", *all} {
		if set {
			modes++
		}
	}
	if modes != 1 {
		return errUsage
	}

	var (
		what string
		do   func() (int64, error)
		ref  string
		mode string
	)
	switch {
	case *olderThan != "":
		age, err := parseAge(*olderThan)
		if err != nil {
			return err
		}
		mode = "older-than " + *olderThan
		before := time.Now().Add(-age)
		what = fmt.Sprintf("as mensagens anteriores a %s", before.Format("02/01/2006 15:04"))
		do = func() (int64, error) { return a.st.PurgeOlderThan(ctx, before.Unix()) }
	case *contact != "":
		t, err := a.resolve(ctx, *contact)
		if err != nil {
			return err
		}
		ref, mode = t.Ref, "contact"
		what = fmt.Sprintf("todas as mensagens e categorias da conversa com %s", label(t.Name, t.Ref))
		do = func() (int64, error) { return a.st.PurgeChat(ctx, t.JID) }
	default:
		tot, err := a.st.Totals(ctx)
		if err != nil {
			return err
		}
		mode = "all"
		what = fmt.Sprintf("todas as %d mensagens guardadas, nomes de contatos e categorias das conversas", tot.Messages)
		do = func() (int64, error) { return tot.Messages, a.st.PurgeAll(ctx) }
	}

	if !*yes {
		fmt.Fprintf(out, "Isto apagaria %s.\n", what)
		fmt.Fprintln(out, "Nada foi apagado. Repita o comando com --yes para confirmar.")
		return errNotConfirmed
	}
	n, err := do()
	if err != nil {
		return err
	}
	if err := a.st.Compact(ctx); err != nil {
		return fmt.Errorf("compactar banco: %w", err)
	}
	a.audit(ctx, "purge", ref, mode)
	fmt.Fprintf(out, "Apagadas %d mensagem(ns). O espaço foi liberado e o conteúdo apagado não é recuperável.\n", n)
	return nil
}

// errNotConfirmed is a purge without --yes: nothing was deleted, exit 1.
var errNotConfirmed = errors.New("expurgo não confirmado")

// parseAge reads "30d" (days) or a Go duration such as "12h".
func parseAge(s string) (time.Duration, error) {
	bad := fmt.Errorf("--older-than inválido %q: use dias (30d) ou horas (12h)", privacy.RedactText(s))
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, bad
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, bad
		}
	}
	if d <= 0 {
		return 0, bad
	}
	return d, nil
}
