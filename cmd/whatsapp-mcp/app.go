package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/config"
	"github.com/riknog/whatsapp-mcp/internal/identity"
	"github.com/riknog/whatsapp-mcp/internal/ingest"
	"github.com/riknog/whatsapp-mcp/internal/lock"
	"github.com/riknog/whatsapp-mcp/internal/logging"
	"github.com/riknog/whatsapp-mcp/internal/privacy"
	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/toolerr"
	"github.com/riknog/whatsapp-mcp/internal/wa"
)

// File names inside the data directory (docs/01-DESIGN.md §4).
const (
	dataFileName    = "data.db"
	sessionFileName = "session.db"
	lockFileName    = "serve.lock"
	refKeyFileName  = "ref.key"
	configFileName  = "config.toml"
)

// errLocked is printed when another process uses the WhatsApp session.
const errLocked = "outra instância do whatsapp-mcp (serve, login ou logout) já está usando esta sessão. " +
	"feche o Claude (ou o outro terminal) e tente de novo"

var errBusy = errors.New(errLocked)

// app is the data directory opened by a command: config, data.db and the
// reference key. Commands that talk to WhatsApp also open the session.
type app struct {
	home     string
	cfg      *config.Config
	st       *store.Store
	refs     *identity.Refs
	resolver *identity.Resolver
	source   *identity.StoreSource
	log      *slog.Logger
	logFile  io.Closer
	stderr   io.Writer
}

// openApp opens the data directory. The permissions are checked as in serve.
func openApp(ctx context.Context, stderr io.Writer) (*app, error) {
	home, err := config.Home()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(home)
	if err != nil {
		return nil, fmt.Errorf("carregar configuração: %w", err)
	}
	if err := config.CheckPermissions(home); err != nil {
		return nil, fmt.Errorf("permissões: %w", err)
	}
	logger, logFile, err := logging.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("log: %w", err)
	}
	st, err := store.Open(ctx, filepath.Join(home, dataFileName), clock.Real{})
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("abrir banco: %w", err)
	}
	refs, err := identity.LoadRefs(home)
	if err != nil {
		_ = st.Close()
		_ = logFile.Close()
		return nil, fmt.Errorf("chave de referências: %w", err)
	}
	src := identity.NewStoreSource(st, refs)
	return &app{
		home: home, cfg: cfg, st: st, refs: refs,
		resolver: identity.NewResolver(src, clock.Real{}),
		source:   src,
		log:      logger, logFile: logFile, stderr: stderr,
	}, nil
}

func (a *app) close() {
	_ = a.st.Close()
	_ = a.logFile.Close()
}

// lock takes the session lock shared with serve, login and logout.
func (a *app) lock() (*lock.Lock, error) {
	return lock.Acquire(filepath.Join(a.home, lockFileName))
}

// openWA opens session.db. It does not connect.
func (a *app) openWA(ctx context.Context) (*wa.Real, error) {
	r, err := wa.Open(ctx, wa.Options{
		SessionPath: filepath.Join(a.home, sessionFileName),
		HistoryDays: a.cfg.Read.HistorySyncDays,
		Logger:      a.log,
	})
	if err != nil {
		return nil, fmt.Errorf("abrir sessão do WhatsApp: %w", err)
	}
	return r, nil
}

// pump writes the events that arrive while a command is connected (offline
// messages, contacts), so that none is lost. It returns when ctx ends.
func (a *app) pump(ctx context.Context, events <-chan any) {
	in := ingest.New(a.st, a.refs, clock.Real{}, a.log)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if err := in.Handle(ctx, ev); err != nil && ctx.Err() == nil {
				a.log.Warn("evento não gravado", "err", privacy.RedactLog(err.Error()))
			}
		}
	}
}

// resolve finds a contact or chat for the owner, hidden ones included.
func (a *app) resolve(ctx context.Context, name string) (identity.Target, error) {
	return a.resolver.ResolveAny(ctx, name)
}

// candidatesBy returns every candidate, keyed by JID.
func (a *app) candidatesBy(ctx context.Context) (map[string]identity.Candidate, error) {
	cands, err := a.source.Candidates(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]identity.Candidate, len(cands))
	for _, c := range cands {
		out[c.JID] = c
	}
	return out, nil
}

// audit records a change made by the owner. Its failure is only logged.
func (a *app) audit(ctx context.Context, action, ref, detail string) {
	if err := a.st.Audit(ctx, action, ref, detail); err != nil {
		a.log.Warn("auditoria falhou", "err", privacy.RedactLog(err.Error()))
	}
}

// report writes an error for the owner. An ambiguous name lists the candidates,
// by name and contact_ref, so the owner can repeat the command with the ref.
// Phone numbers and JIDs are redacted, as in every other output.
func report(w io.Writer, cmd string, err error) {
	var te toolerr.Error
	if !errors.As(err, &te) {
		fmt.Fprintf(w, "whatsapp-mcp %s: %s\n", cmd, privacy.RedactLog(err.Error()))
		return
	}
	fmt.Fprintf(w, "whatsapp-mcp %s: %s\n", cmd, privacy.RedactLog(te.Message))
	cands, _ := te.Details["candidates"].([]map[string]any)
	for _, c := range cands {
		ago, _ := c["last_interaction_ago"].(string)
		if ago == "" {
			ago = "nenhuma"
		}
		fmt.Fprintf(w, "  - %v  (%v, última conversa: %s)\n", c["name"], c["contact_ref"], ago)
	}
	if len(cands) > 0 {
		fmt.Fprintf(w, "Repita o comando com o contact_ref (c_…) no lugar do nome.\n")
	}
}

// sortedByName orders candidates by name, then ref.
func sortedByName(cs []identity.Candidate) {
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Name != cs[j].Name {
			return cs[i].Name < cs[j].Name
		}
		return cs[i].Ref < cs[j].Ref
	})
}
