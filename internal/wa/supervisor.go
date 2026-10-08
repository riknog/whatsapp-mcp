package wa

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/privacy"
)

// Reconnection bounds: 1 s doubling up to 5 min.
const (
	minBackoff = time.Second
	maxBackoff = 5 * time.Minute
)

// backoffDelay returns the wait before the reconnection that follows attempt
// consecutive failures (attempt 0 is the first wait). The base doubles from 1 s
// up to 5 min. Equal jitter then picks a value in [base/2, base], so that many
// instances do not retry in step.
func backoffDelay(attempt int, rnd *rand.Rand) time.Duration {
	base := minBackoff
	for i := 0; i < attempt && base < maxBackoff; i++ {
		base *= 2
	}
	if base > maxBackoff {
		base = maxBackoff
	}
	half := base / 2
	return half + time.Duration(rnd.Int63n(int64(half)+1))
}

// supervisor keeps one connection alive. It dials, waits until the connection
// drops, and waits again with backoff before the next dial. It is driven by an
// injected dial function, clock and drop signal, so the tests need no network.
type supervisor struct {
	clk     clock.Clock
	rnd     *rand.Rand
	log     *slog.Logger
	dial    func(context.Context) error // one connection attempt
	dropped <-chan struct{}             // signalled when an established connection goes down
	stop    func() bool                 // true when reconnecting must end (logged out)
}

// run returns when ctx ends or stop reports true.
func (s *supervisor) run(ctx context.Context) {
	failures := 0
	for {
		if ctx.Err() != nil || s.stop() {
			return
		}
		if err := s.dial(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			d := backoffDelay(failures-1, s.rnd)
			s.log.Warn("conexão com o WhatsApp falhou; nova tentativa",
				"tentativa", failures, "espera_s", int(d.Seconds()), "err", privacy.RedactLog(err.Error()))
			if s.clk.Sleep(ctx, d) != nil {
				return
			}
			continue
		}
		failures = 0
		s.log.Info("conectado ao WhatsApp")
		select {
		case <-ctx.Done():
			return
		case <-s.dropped:
		}
		if s.stop() {
			return
		}
		s.log.Info("conexão perdida; reconectando")
		if s.clk.Sleep(ctx, backoffDelay(0, s.rnd)) != nil {
			return
		}
	}
}
