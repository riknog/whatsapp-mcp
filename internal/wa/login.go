package wa

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"

	"github.com/riknog/whatsapp-mcp/internal/ingest"
	"github.com/riknog/whatsapp-mcp/internal/privacy"
)

// pairTimeout is how long Login waits for the device to be paired.
const pairTimeout = 2 * time.Minute

// Sink receives every event that WhatsApp delivers while Login runs. The CLI
// passes a function that calls the ingestor's Handle, so that nothing is lost:
// the history sync and the app state are sent by WhatsApp only once.
type Sink func(ctx context.Context, evt any) error

// loginState is the state of a Login in progress. It is guarded by Real.loginMu.
type loginState struct {
	active    bool
	ctx       context.Context
	out       io.Writer
	sink      Sink
	paired    bool
	connected bool
	lastSync  time.Time // last sync-related event (or the start of the login)
	convs     int       // conversations received in history syncs
	announced bool      // the "receiving history" line was written
	inflight  int       // sink calls still running
}

// Login pairs a new device and then keeps the connection until the first sync
// settles. With pairPhone empty it shows a QR code on out; otherwise it asks
// WhatsApp for a pairing code for that number (country code, no "+") and writes
// it to out. Every event goes to sink. Login returns when the sync has been idle
// for Options.SyncIdle (default 30 s) once paired and connected, or when
// Options.SyncCap (default 5 min) has passed in total. Progress lines written to
// out carry no personal data. Login never writes to stdout.
//
// How the CLI must use it: call Login with a sink that runs
// ingest.Ingestor.Handle, then Close the adapter. The ingestor must be the one the
// serve command will use, against the same data.db. Only the CLI calls Login.
func (r *Real) Login(ctx context.Context, out io.Writer, pairPhone string, sink Sink) error {
	if r.dev.ID != nil {
		return ErrAlreadyPaired
	}
	if sink == nil {
		return errors.New("wa: Login sem destino para os eventos")
	}
	r.attach(ctx, out, sink)
	defer r.detach()

	if pairPhone == "" {
		items, err := r.cl.GetQRChannel(ctx)
		if err != nil {
			return fmt.Errorf("wa: canal de QR: %w", err)
		}
		go showQR(items, out)
	}
	if err := r.cl.Connect(); err != nil {
		return fmt.Errorf("wa: conectar: %s", privacy.RedactLog(err.Error()))
	}
	defer r.waitSinks(ctx)
	defer r.cl.Disconnect()

	if pairPhone != "" {
		code, err := r.cl.PairPhone(ctx, pairPhone, true, whatsmeow.PairClientChrome, pairDisplayName())
		if err != nil {
			return fmt.Errorf("wa: pareamento por código: %s", privacy.RedactLog(err.Error()))
		}
		fmt.Fprintf(out, "Código de pareamento: %s\n", code)
		fmt.Fprintln(out, "No celular: WhatsApp > Aparelhos conectados > Conectar com número de telefone.")
	}
	return r.awaitSync(ctx)
}

// awaitSync waits, on the injected clock, until the sync settles or the login
// gives up. Its state comes from the flags that onEvent sets synchronously.
func (r *Real) awaitSync(ctx context.Context) error {
	tick := r.syncIdle / 10
	if tick > time.Second {
		tick = time.Second
	}
	if tick <= 0 {
		tick = time.Millisecond
	}
	start := r.clk.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.clk.After(tick):
		}
		now := r.clk.Now()
		elapsed := now.Sub(start)
		r.loginMu.Lock()
		paired, connected, lastSync, inflight := r.lg.paired, r.lg.connected, r.lg.lastSync, r.lg.inflight
		r.loginMu.Unlock()

		switch {
		case !paired && (elapsed >= pairTimeout || elapsed >= r.syncCap):
			return errors.New("wa: tempo esgotado sem pareamento")
		case elapsed >= r.syncCap:
			return r.finish("Tempo máximo de sincronização atingido")
		case paired && connected && inflight == 0 && now.Sub(lastSync) >= r.syncIdle:
			return r.finish("Sincronização concluída")
		}
	}
}

// finish writes the closing line of a login. It reports the count of
// conversations, not their names.
func (r *Real) finish(msg string) error {
	r.loginMu.Lock()
	convs := r.lg.convs
	r.loginMu.Unlock()
	r.progress(fmt.Sprintf("%s: %d conversas recebidas.", msg, convs))
	return nil
}

func (r *Real) attach(ctx context.Context, out io.Writer, sink Sink) {
	r.loginMu.Lock()
	defer r.loginMu.Unlock()
	r.lg = loginState{active: true, ctx: ctx, out: out, sink: sink, lastSync: r.clk.Now()}
}

func (r *Real) detach() {
	r.loginMu.Lock()
	defer r.loginMu.Unlock()
	r.lg = loginState{}
}

// activeSink returns the login sink and its context, or nils when no login runs.
func (r *Real) activeSink() (Sink, context.Context) {
	r.loginMu.Lock()
	defer r.loginMu.Unlock()
	if !r.lg.active {
		return nil, nil
	}
	return r.lg.sink, r.lg.ctx
}

// deliverLogin hands one event to the login sink and counts history conversations.
func (r *Real) deliverLogin(ctx context.Context, sink Sink, ev any) {
	if h, ok := ev.(ingest.HistorySyncEvent); ok {
		r.countHistory(len(h.Conversations))
	}
	r.loginMu.Lock()
	r.lg.inflight++
	r.loginMu.Unlock()
	defer func() {
		// A long write (a large history sync) counts as sync activity until it
		// ends. The touch and the decrement share the lock, so the idle check
		// never sees the write finished with the old activity time.
		r.loginMu.Lock()
		r.lg.lastSync = r.clk.Now()
		r.lg.inflight--
		r.loginMu.Unlock()
	}()
	if err := sink(ctx, ev); err != nil {
		r.log.Warn("evento do login não gravado", "tipo", fmt.Sprintf("%T", ev), "err", err)
	}
}

// waitSinks waits, after the disconnect, for sink calls still running, so the
// caller never closes the store under a write.
func (r *Real) waitSinks(ctx context.Context) {
	for {
		r.loginMu.Lock()
		n := r.lg.inflight
		r.loginMu.Unlock()
		if n == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-r.clk.After(10 * time.Millisecond):
		}
	}
}

func (r *Real) countHistory(n int) {
	r.loginMu.Lock()
	defer r.loginMu.Unlock()
	if !r.lg.announced {
		r.lg.announced = true
		r.writeLocked("Recebendo histórico do WhatsApp...")
	}
	r.lg.convs += n
}

// touch records that a sync-related event arrived now.
func (r *Real) touch() {
	r.loginMu.Lock()
	defer r.loginMu.Unlock()
	r.lg.lastSync = r.clk.Now()
}

// updateLogin changes the login state under the lock.
func (r *Real) updateLogin(fn func(st *loginState)) {
	r.loginMu.Lock()
	defer r.loginMu.Unlock()
	fn(&r.lg)
}

// progress writes one line to the login output, when a login runs.
func (r *Real) progress(msg string) {
	r.loginMu.Lock()
	defer r.loginMu.Unlock()
	r.writeLocked(msg)
}

func (r *Real) writeLocked(msg string) {
	if r.lg.active && r.lg.out != nil {
		fmt.Fprintln(r.lg.out, msg)
	}
}

// showQR writes each QR code to out until the channel closes.
func showQR(items <-chan whatsmeow.QRChannelItem, out io.Writer) {
	for item := range items {
		if item.Event != whatsmeow.QRChannelEventCode {
			continue
		}
		fmt.Fprintln(out, "\nEscaneie o QR code: WhatsApp > Aparelhos conectados > Conectar um aparelho.")
		qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, out)
	}
}

// pairDisplayName is the "Browser (OS)" name WhatsApp shows for a pairing code.
func pairDisplayName() string {
	switch runtime.GOOS {
	case "windows":
		return "Chrome (Windows)"
	case "darwin":
		return "Chrome (Mac OS)"
	default:
		return "Chrome (Linux)"
	}
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}
