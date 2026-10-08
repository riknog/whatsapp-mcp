package wa

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	_ "modernc.org/sqlite" // registers the "sqlite" driver (pure Go, no CGO)

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/ingest"
	"github.com/riknog/whatsapp-mcp/internal/privacy"
)

// Errors returned by the adapter.
var (
	// ErrNotLoggedIn is returned by Connect when no device is paired. Run Login first.
	ErrNotLoggedIn = errors.New("wa: sessão não pareada; rode o login")
	// ErrAlreadyPaired is returned by Login when a device is already paired.
	ErrAlreadyPaired = errors.New("wa: já pareado")
)

const (
	deviceOS         = "whatsapp-mcp"
	eventBuffer      = 256
	defaultSyncIdle  = 30 * time.Second
	defaultSyncCap   = 5 * time.Minute
	blockedWarnAfter = 10 * time.Second
	contactsTimeout  = 30 * time.Second
	lidTimeout       = 5 * time.Second
)

// Options configures Open.
type Options struct {
	SessionPath string        // session.db: whatsmeow credentials, apart from data.db
	HistoryDays int           // read.history_sync_days; 0 keeps the library default
	Logger      *slog.Logger  // must redact (internal/logging); nil discards
	Clock       clock.Clock   // nil means clock.Real
	Rand        *rand.Rand    // reconnection jitter; nil seeds from the time
	SyncIdle    time.Duration // Login ends after this long with no sync events; 0 means 30 s
	SyncCap     time.Duration // Login ends after this long in total; 0 means 5 min
}

// Real is the whatsmeow adapter. It implements Client.
type Real struct {
	db  *sql.DB
	dev *store.Device
	cl  *whatsmeow.Client
	clk clock.Clock
	rnd *rand.Rand
	log *slog.Logger

	events    chan any
	done      chan struct{} // closed by Close: unblocks event delivery for good
	closeOnce sync.Once

	dropped   chan struct{} // a live connection went down
	loggedOut atomic.Bool   // WhatsApp ended the session: never reconnect

	supMu  sync.Mutex
	supOn  bool
	cancel context.CancelFunc

	syncIdle time.Duration
	syncCap  time.Duration

	loginMu sync.Mutex
	lg      loginState // guarded by loginMu
}

var _ Client = (*Real)(nil)

// Open opens session.db (created 0600 in a 0700 directory), restores the device
// and builds the whatsmeow client. It does not connect.
func Open(ctx context.Context, opts Options) (*Real, error) {
	if opts.SessionPath == "" {
		return nil, errors.New("wa: caminho do session.db vazio")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	rnd := opts.Rand
	if rnd == nil {
		rnd = rand.New(rand.NewSource(time.Now().UnixNano())) // #nosec G404 -- reconnection jitter, not security
	}
	if err := prepareSessionFile(opts.SessionPath); err != nil {
		return nil, err
	}

	dsn := "file:" + filepath.ToSlash(opts.SessionPath) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("wa: abrir session.db: %w", err)
	}
	waLog := newWALogger(logger)
	container := sqlstore.NewWithDB(db, "sqlite3", waLog)
	if err := container.Upgrade(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("wa: migrar session.db: %w", err)
	}
	dev, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("wa: ler dispositivo: %w", err)
	}
	applyDeviceProps(opts.HistoryDays)

	cl := whatsmeow.NewClient(dev, waLog)
	// Without this, the full app state sync emits no Contact, LabelEdit or
	// LabelAssociationChat events (confirmed against a real account).
	cl.EmitAppStateEventsOnFullSync = true
	// Reconnection belongs to supervisor, which uses backoff.
	cl.EnableAutoReconnect = false

	r := &Real{
		db: db, dev: dev, cl: cl, clk: clk, rnd: rnd, log: logger,
		events:   make(chan any, eventBuffer),
		done:     make(chan struct{}),
		dropped:  make(chan struct{}, 1),
		syncIdle: orDefault(opts.SyncIdle, defaultSyncIdle),
		syncCap:  orDefault(opts.SyncCap, defaultSyncCap),
	}
	cl.AddEventHandler(r.onEvent)
	return r, nil
}

// prepareSessionFile creates the directory (0700) and the file (0600), and
// forces the file mode when it already exists.
func prepareSessionFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("wa: criar diretório de dados: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- path is session.db inside the data directory
	if err != nil {
		return fmt.Errorf("wa: criar session.db: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("wa: fechar session.db: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("wa: permissões do session.db: %w", err)
	}
	return nil
}

// applyDeviceProps sets the companion name shown on the phone and the history
// sync window. whatsmeow keeps these in package variables, so they are set once,
// before the client is built.
func applyDeviceProps(historyDays int) {
	name := deviceOS
	store.DeviceProps.Os = &name
	if historyDays > 0 && historyDays <= math.MaxUint32 && store.DeviceProps.HistorySyncConfig != nil {
		days := uint32(historyDays)
		store.DeviceProps.HistorySyncConfig.FullSyncDaysLimit = &days
	}
}

// Connect starts the connection in the background. It returns ErrNotLoggedIn
// when no device is paired. Otherwise the first attempt and every retry happen
// in the supervisor, and IsConnected tells whether one has succeeded. Calling
// Connect again while it runs does nothing.
func (r *Real) Connect(ctx context.Context) error {
	if r.dev.ID == nil {
		return ErrNotLoggedIn
	}
	r.supMu.Lock()
	defer r.supMu.Unlock()
	if r.supOn {
		return nil
	}
	bg, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r.cancel = cancel
	r.supOn = true
	s := &supervisor{
		clk:     r.clk,
		rnd:     r.rnd,
		log:     r.log,
		dial:    r.cl.ConnectContext,
		dropped: r.dropped,
		stop:    r.loggedOut.Load,
	}
	go s.run(bg)
	return nil
}

// Disconnect stops the supervisor and closes the socket. Events already queued
// stay readable. Connect may be called again afterwards.
func (r *Real) Disconnect() {
	r.supMu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.supOn = false
	r.supMu.Unlock()
	r.cl.Disconnect()
}

// Close disconnects, unblocks any pending event delivery for good, and closes session.db.
func (r *Real) Close() error {
	r.Disconnect()
	r.closeOnce.Do(func() { close(r.done) })
	return r.db.Close()
}

// IsConnected reports whether the socket is up.
func (r *Real) IsConnected() bool { return r.cl.IsConnected() }

// IsLoggedIn reports whether the session is authenticated.
func (r *Real) IsLoggedIn() bool { return r.cl.IsLoggedIn() }

// AccountName returns the owner's push name, or the business name when there is none.
func (r *Real) AccountName() string {
	if r.dev.PushName != "" {
		return r.dev.PushName
	}
	return r.dev.BusinessName
}

// AccountType is "business" for WhatsApp Business (platform smba or smbi, or a
// business name on the paired device) and "personal" otherwise.
func (r *Real) AccountType() string {
	if r.dev.Platform == "smba" || r.dev.Platform == "smbi" || r.dev.BusinessName != "" {
		return "business"
	}
	return "personal"
}

// Events returns the translated events, in the order WhatsApp delivered them.
// Read them until Close: while nobody reads, WhatsApp delivery waits (see emit).
func (r *Real) Events() <-chan any { return r.events }

// onEvent reacts to connection events, updates the login state, and passes
// translated events to emit.
func (r *Real) onEvent(evt any) {
	switch e := evt.(type) {
	case *events.Connected:
		r.updateLogin(func(st *loginState) { st.connected = true })
		r.touch()
		r.syncContacts()
	case *events.PairSuccess:
		r.updateLogin(func(st *loginState) { st.paired = true })
		r.progress("Pareado. Aguardando sincronização do WhatsApp...")
	case *events.HistorySync, *events.AppState, *events.AppStateSyncComplete,
		*events.Contact, *events.LabelEdit, *events.LabelAssociationChat, *events.LabelAssociationMessage:
		r.touch()
	case *events.Disconnected:
		select {
		case r.dropped <- struct{}{}:
		default:
		}
	case *events.LoggedOut:
		r.loggedOut.Store(true)
	case *events.ConnectFailure:
		switch e.Reason {
		case events.ConnectFailureLoggedOut, events.ConnectFailureMainDeviceGone, events.ConnectFailureUnknownLogout:
			r.loggedOut.Store(true)
		}
	}
	if ev, ok := ingest.Translate(evt); ok {
		r.emit(ev)
	}
}

// emit delivers one event. During Login the events go to the login sink. Otherwise
// they wait in Events. A consumer that stops reading blocks WhatsApp delivery
// on purpose (no event is lost); after blockedWarnAfter the adapter logs a
// warning, without content, and keeps waiting until the consumer reads or Close runs.
func (r *Real) emit(ev any) {
	if sink, ctx := r.activeSink(); sink != nil {
		r.deliverLogin(ctx, sink, ev)
		return
	}
	select {
	case r.events <- ev:
		return
	case <-r.done:
		return
	default:
	}
	warn := r.clk.After(blockedWarnAfter)
	select {
	case r.events <- ev:
	case <-r.done:
	case <-warn:
		r.log.Warn("Events() não está sendo lido; a entrega do WhatsApp está bloqueada")
		select {
		case r.events <- ev:
		case <-r.done:
		}
	}
}

// syncContacts emits the names of every stored contact on each connection. The
// events are idempotent, so repeating them is harmless.
func (r *Real) syncContacts() {
	// A device that is not paired yet has no stores: whatsmeow attaches them on save.
	if r.cl.Store == nil || r.cl.Store.Contacts == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), contactsTimeout)
	defer cancel()
	all, err := r.cl.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		r.log.Warn("ler contatos do dispositivo", "err", privacy.RedactLog(err.Error()))
		return
	}
	for _, ev := range contactSnapshot(all) {
		r.emit(ev)
	}
}

// contactSnapshot turns the device's contact store into contact events. Contacts
// without any name are left out.
func contactSnapshot(all map[types.JID]types.ContactInfo) []ingest.ContactEvent {
	out := make([]ingest.ContactEvent, 0, len(all))
	for j, info := range all {
		ev := ingest.ContactEvent{
			JID:          j.ToNonAD().String(),
			FullName:     info.FullName,
			FirstName:    info.FirstName,
			PushName:     info.PushName,
			BusinessName: info.BusinessName,
		}
		if ev.FullName == "" && ev.FirstName == "" && ev.PushName == "" && ev.BusinessName == "" {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// ContactPhone returns "+<digits>" for a contact, for building a vCard only.
// A LID is resolved through the device's LID mapping. False when unknown.
func (r *Real) ContactPhone(jid JID) (string, bool) {
	j, err := types.ParseJID(string(jid))
	if err != nil {
		return "", false
	}
	if j.Server == types.HiddenUserServer {
		if r.dev.LIDs == nil {
			return "", false
		}
		ctx, cancel := context.WithTimeout(context.Background(), lidTimeout)
		defer cancel()
		pn, err := r.dev.LIDs.GetPNForLID(ctx, j)
		if err != nil || pn.IsEmpty() {
			return "", false
		}
		j = pn
	}
	if j.Server != types.DefaultUserServer || j.User == "" {
		return "", false
	}
	return "+" + j.User, true
}
