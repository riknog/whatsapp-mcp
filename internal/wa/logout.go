package wa

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/privacy"
)

// logoutConnectTimeout is how long Logout waits for a connection to tell
// WhatsApp that the device is leaving.
const logoutConnectTimeout = 15 * time.Second

// ErrLogoutOffline means the local session was deleted but WhatsApp could not
// be told: the device stays listed on the phone until the owner removes it.
var ErrLogoutOffline = errors.New("wa: sessão local apagada, mas o WhatsApp não foi avisado")

// Paired reports whether session.db holds a paired device, connected or not.
func (r *Real) Paired() bool { return r.dev.ID != nil }

// CheckConnection connects once and reports whether the session authenticates
// within timeout. It disconnects before returning. Only the CLI calls it, while
// it holds the lock that keeps serve from running.
func (r *Real) CheckConnection(ctx context.Context, timeout time.Duration) bool {
	if !r.Paired() {
		return false
	}
	defer r.cl.Disconnect()
	return r.dialAndWait(ctx, timeout)
}

// Logout unlinks the device: it tells WhatsApp (so the device leaves the
// phone's list) and deletes the credentials from session.db. When WhatsApp
// cannot be reached, the credentials are deleted anyway and ErrLogoutOffline is
// returned. Without a paired device it returns ErrNotLoggedIn.
func (r *Real) Logout(ctx context.Context) error {
	if !r.Paired() {
		return ErrNotLoggedIn
	}
	r.Disconnect()
	if r.dialAndWait(ctx, logoutConnectTimeout) {
		// whatsmeow's Logout disconnects and deletes the device on success.
		if err := r.cl.Logout(ctx); err == nil {
			return nil
		}
		r.log.Warn("pedido de logout recusado; apagando só a sessão local")
	}
	r.cl.Disconnect()
	if err := r.dev.Delete(ctx); err != nil {
		return fmt.Errorf("wa: apagar sessão local: %s", privacy.RedactLog(err.Error()))
	}
	return ErrLogoutOffline
}

// dialAndWait opens the socket and waits until the session authenticates.
func (r *Real) dialAndWait(ctx context.Context, timeout time.Duration) bool {
	if err := r.cl.ConnectContext(ctx); err != nil {
		return false
	}
	deadline := r.clk.Now().Add(timeout)
	for !r.cl.IsLoggedIn() {
		if !r.clk.Now().Before(deadline) || r.clk.Sleep(ctx, 100*time.Millisecond) != nil {
			return false
		}
	}
	return true
}
