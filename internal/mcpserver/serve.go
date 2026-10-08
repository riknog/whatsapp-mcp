package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/config"
	"github.com/riknog/whatsapp-mcp/internal/identity"
	"github.com/riknog/whatsapp-mcp/internal/ingest"
	"github.com/riknog/whatsapp-mcp/internal/logging"
	"github.com/riknog/whatsapp-mcp/internal/media"
	"github.com/riknog/whatsapp-mcp/internal/privacy"
	"github.com/riknog/whatsapp-mcp/internal/sendqueue"
	"github.com/riknog/whatsapp-mcp/internal/store"
	"github.com/riknog/whatsapp-mcp/internal/wa"
)

// File names inside the data directory (docs/01-DESIGN.md §4).
const (
	dataFileName    = "data.db"
	sessionFileName = "session.db"
	mediaScratchDir = "tmp" // read_media scratch files, removed after each call
)

// retentionEvery is how often expired messages are purged.
const retentionEvery = 24 * time.Hour

// ServeOptions configures Serve.
type ServeOptions struct {
	Home      string        // data directory
	Version   string        // shown in the MCP server info
	Transport mcp.Transport // stdio in production

	// Client replaces the WhatsApp adapter. Nil opens the real one from session.db.
	Client wa.Client
	// Clock is nil for the wall clock.
	Clock clock.Clock
}

// Serve runs the MCP server until the transport closes or ctx ends. The WhatsApp
// connection starts in the background, so the MCP handshake never waits for it.
// Logs go to stderr and to the log file; nothing is written to stdout.
func Serve(ctx context.Context, opts ServeOptions) error {
	cfg, err := config.Load(opts.Home)
	if err != nil {
		return fmt.Errorf("carregar configuração: %w", err)
	}
	if err := config.CheckPermissions(opts.Home); err != nil {
		return fmt.Errorf("permissões: %w", err)
	}
	logger, logFile, err := logging.New(cfg)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	clk := opts.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	st, err := store.Open(ctx, filepath.Join(opts.Home, dataFileName), clk)
	if err != nil {
		return fmt.Errorf("abrir banco: %w", err)
	}
	defer func() { _ = st.Close() }()
	refs, err := identity.LoadRefsWithClock(opts.Home, clk)
	if err != nil {
		return fmt.Errorf("chave de referências: %w", err)
	}

	client := opts.Client
	if client == nil {
		real, err := wa.Open(ctx, wa.Options{
			SessionPath: filepath.Join(opts.Home, sessionFileName),
			HistoryDays: cfg.Read.HistorySyncDays,
			Logger:      logger,
			Clock:       clk,
		})
		if err != nil {
			return fmt.Errorf("abrir sessão do WhatsApp: %w", err)
		}
		defer func() { _ = real.Close() }()
		client = real
	}

	runCtx, stop := context.WithCancel(ctx)
	defer stop()

	// The background goroutines use the store: they must end before the
	// deferred st.Close, so stop and wait run first (defers are LIFO).
	var wg sync.WaitGroup
	defer wg.Wait()
	defer stop()

	var lastEvent atomic.Int64
	wg.Add(3)
	go func() { defer wg.Done(); connectInBackground(runCtx, client, logger) }()
	go func() {
		defer wg.Done()
		consumeEvents(runCtx, client.Events(), ingest.New(st, refs, clk, logger), clk, &lastEvent, logger)
	}()
	go func() { defer wg.Done(); retain(runCtx, st, cfg.Privacy.RetentionDays, clk, logger) }()

	queue := sendqueue.New(cfg.Send, st, client, clk, rand.New(rand.NewSource(time.Now().UnixNano())), logger) // #nosec G404 -- cooldown jitter, not security
	if err := queue.Start(runCtx); err != nil {
		return fmt.Errorf("fila de envio: %w", err)
	}
	wg.Add(1)
	go func() { defer wg.Done(); queue.Wait() }()

	srv := New(Deps{
		Store:       st,
		Refs:        refs,
		Client:      client,
		Queue:       queue,
		Sender:      queue,
		Media:       media.New(cfg.Media, filepath.Join(opts.Home, mediaScratchDir)),
		Config:      *cfg,
		Clock:       clk,
		LastEventAt: func() time.Time { return lastEventTime(&lastEvent) },
		Logger:      logger,
		Version:     opts.Version,
	})
	err = srv.Run(runCtx, opts.Transport)
	client.Disconnect()
	return err
}

// connectInBackground starts the WhatsApp connection. Without a session it only
// logs the way out, because the tools already answer not_logged_in.
func connectInBackground(ctx context.Context, client wa.Client, log *slog.Logger) {
	if err := client.Connect(ctx); err != nil {
		if errors.Is(err, wa.ErrNotLoggedIn) {
			log.Info("sem sessão do WhatsApp; rode whatsapp-mcp login no terminal")
			return
		}
		log.Warn("não foi possível conectar ao WhatsApp", "err", privacy.RedactLog(err.Error()))
	}
}

// consumeEvents writes the WhatsApp events to the store and records when the
// last one arrived.
func consumeEvents(ctx context.Context, events <-chan any, in *ingest.Ingestor, clk clock.Clock, last *atomic.Int64, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			last.Store(clk.Now().Unix())
			if err := in.Handle(ctx, ev); err != nil && ctx.Err() == nil {
				log.Warn("evento não gravado", "tipo", fmt.Sprintf("%T", ev), "err", privacy.RedactLog(err.Error()))
			}
		}
	}
}

// retain deletes the messages older than the retention period, now and every day.
func retain(ctx context.Context, st *store.Store, days int, clk clock.Clock, log *slog.Logger) {
	if days <= 0 {
		return
	}
	for {
		before := clk.Now().AddDate(0, 0, -days).Unix()
		n, err := st.PurgeOlderThan(ctx, before)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("retenção falhou", "err", privacy.RedactLog(err.Error()))
		case err == nil && n > 0:
			log.Info("mensagens antigas apagadas", "quantidade", n)
		}
		if clk.Sleep(ctx, retentionEvery) != nil {
			return
		}
	}
}

func lastEventTime(last *atomic.Int64) time.Time {
	n := last.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}
