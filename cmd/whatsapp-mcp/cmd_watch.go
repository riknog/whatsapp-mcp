package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/privacy"
	"github.com/riknog/whatsapp-mcp/internal/store"
)

// Limits of watch.
const (
	defaultWatchInterval = 5 * time.Second
	minWatchInterval     = time.Second
	maxWatchLines        = 10 // chats listed by --once
	maxWatchName         = 40 // characters of a name in a line
)

type watchOptions struct {
	interval      time.Duration
	includeGroups bool
	once          bool
}

// watch prints one line per chat with new incoming messages, for the Monitor
// tool or a hook of Claude Code. It only reads data.db, so it runs next to
// serve, which receives the messages. A line has the chat name, its
// contact_ref and a count: never the message text, which stays behind the
// tools and their protections.
func watch(args []string, stdout, stderr io.Writer) int {
	opts, err := parseWatch(args)
	if err != nil {
		return fail(stderr, "watch", err)
	}
	ctx, stop := notifyContext()
	defer stop()
	a, err := openApp(ctx, stderr)
	if err != nil {
		return fail(stderr, "watch", err)
	}
	defer a.close()
	err = runWatch(ctx, a, stdout, opts, clock.Real{})
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	return fail(stderr, "watch", err)
}

func parseWatch(args []string) (watchOptions, error) {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := watchOptions{}
	fs.DurationVar(&opts.interval, "interval", defaultWatchInterval, "")
	fs.BoolVar(&opts.includeGroups, "include-groups", false, "")
	fs.BoolVar(&opts.once, "once", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return opts, errUsage
	}
	if opts.interval < minWatchInterval {
		return opts, fmt.Errorf("--interval: mínimo de %s", minWatchInterval)
	}
	return opts, nil
}

// runWatch is the body of watch. With once it prints the chats that have unseen
// messages and returns; otherwise it reports arrivals until ctx ends.
func runWatch(ctx context.Context, a *app, out io.Writer, opts watchOptions, clk clock.Clock) error {
	if opts.once {
		arrivals, err := a.st.ArrivalsSince(ctx, 0, opts.includeGroups)
		if err != nil {
			return err
		}
		return printArrivals(ctx, a, out, arrivals, "WhatsApp: mensagens não vistas pelo Claude", maxWatchLines)
	}
	after, err := a.st.MaxMessagePK(ctx)
	if err != nil {
		return err
	}
	for {
		if err := clk.Sleep(ctx, opts.interval); err != nil {
			return err
		}
		arrivals, err := a.st.ArrivalsSince(ctx, after, opts.includeGroups)
		if err != nil {
			// data.db may be busy for a moment while serve writes: try again.
			a.log.Warn("watch: leitura falhou", "err", privacy.RedactLog(err.Error()))
			continue
		}
		for _, ar := range arrivals {
			after = max(after, ar.LatestPK)
		}
		if err := printArrivals(ctx, a, out, arrivals, "", 0); err != nil {
			return err
		}
	}
}

// printArrivals writes one line per chat. With a header, nothing is written
// when there is nothing to report, and at most limit chats are listed.
func printArrivals(ctx context.Context, a *app, out io.Writer, arrivals []store.Arrival, header string, limit int) error {
	if len(arrivals) == 0 {
		return nil
	}
	cands, err := a.candidatesBy(ctx)
	if err != nil {
		return err
	}
	if header != "" {
		fmt.Fprintf(out, "%s (%d conversas). Use list_new_messages para ler.\n", header, len(arrivals))
	}
	for i, ar := range arrivals {
		if limit > 0 && i == limit {
			fmt.Fprintf(out, "  … e mais %d conversas\n", len(arrivals)-limit)
			break
		}
		name := "Desconhecido"
		if c, ok := cands[ar.Chat.JID]; ok && c.Name != "" {
			name = c.Name
		}
		fmt.Fprintf(out, "%sNova mensagem no WhatsApp: %s (%s), %s\n", indent(header),
			clipName(privacy.RedactText(name)), ar.Chat.Ref, plural(ar.Count))
	}
	return nil
}

func indent(header string) string {
	if header != "" {
		return "  "
	}
	return ""
}

func plural(n int) string {
	if n == 1 {
		return "1 não vista"
	}
	return fmt.Sprintf("%d não vistas", n)
}

// clipName keeps a line short: a name is free text chosen by third parties.
func clipName(s string) string {
	r := []rune(s)
	if len(r) <= maxWatchName {
		return s
	}
	return string(r[:maxWatchName-1]) + "…"
}
