package wa

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/ingest"
)

// statFile is os.Stat, named so the tests read as what they check.
func statFile(path string) (os.FileInfo, error) { return os.Stat(path) }

// deviceOSPtr returns the companion name whatsmeow will send.
func deviceOSPtr() *string { return store.DeviceProps.Os }

func derefString(t *testing.T, p *string) string {
	t.Helper()
	if p == nil {
		t.Fatal("nil string pointer")
	}
	return *p
}

func openTest(t *testing.T) *Real {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "session.db")
	r, err := Open(context.Background(), Options{SessionPath: path, HistoryDays: 30, Clock: clock.NewFake(start)})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestOpenCreatesSessionWithStrictModesAndDoesNotConnect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	path := filepath.Join(t.TempDir(), "sub", "session.db")
	r, err := Open(context.Background(), Options{SessionPath: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

	info, err := statMode(path)
	if err != nil || info != 0o600 {
		t.Errorf("session.db mode = %v, %v; want 0600", info, err)
	}
	dir, err := statMode(filepath.Dir(path))
	if err != nil || dir != 0o700 {
		t.Errorf("data dir mode = %v, %v; want 0700", dir, err)
	}
	if r.IsConnected() || r.IsLoggedIn() {
		t.Error("Open must not connect")
	}
	if got := r.AccountType(); got != "personal" {
		t.Errorf("AccountType = %q, want personal for a new device", got)
	}
	if !r.cl.EmitAppStateEventsOnFullSync {
		t.Error("EmitAppStateEventsOnFullSync must be true (T01 finding)")
	}
	if r.cl.EnableAutoReconnect {
		t.Error("whatsmeow auto reconnect must be off: the supervisor owns it")
	}
	if err := r.Connect(context.Background()); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("Connect on unpaired device = %v, want ErrNotLoggedIn", err)
	}
}

func statMode(path string) (fs.FileMode, error) {
	info, err := statFile(path)
	if err != nil {
		return 0, err
	}
	return info.Mode().Perm(), nil
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(context.Background(), Options{}); err == nil {
		t.Error("empty session path accepted")
	}
}

func TestOpenAppliesDeviceName(t *testing.T) {
	openTest(t)
	if p := derefString(t, deviceOSPtr()); p != "whatsapp-mcp" {
		t.Errorf("device OS = %q", p)
	}
}

func TestEventsAreForwardedTranslated(t *testing.T) {
	r := openTest(t)
	r.onEvent(&events.Receipt{MessageSource: types.MessageSource{
		Chat: mustParse(t, "5511999998888@s.whatsapp.net"), IsFromMe: true}, Type: types.ReceiptTypeRead})
	select {
	case ev := <-r.Events():
		if rc, ok := ev.(ingest.ReceiptEvent); !ok || rc.Chat != "5511999998888@s.whatsapp.net" || rc.Type != "read" {
			t.Errorf("forwarded = %#v", ev)
		}
	default:
		t.Fatal("receipt not forwarded")
	}
	r.onEvent(&events.Presence{}) // not stored: nothing is forwarded
	select {
	case ev := <-r.Events():
		t.Errorf("presence forwarded: %#v", ev)
	default:
	}
}

func TestDisconnectedSignalsDropOnceAndLoggedOutStopsReconnect(t *testing.T) {
	r := openTest(t)
	r.onEvent(&events.Disconnected{})
	r.onEvent(&events.Disconnected{}) // a second drop while one is pending is merged
	if len(r.dropped) != 1 {
		t.Errorf("pending drops = %d, want 1", len(r.dropped))
	}
	if r.loggedOut.Load() {
		t.Error("plain disconnect must not mark logged out")
	}
	r.onEvent(&events.LoggedOut{})
	if !r.loggedOut.Load() {
		t.Error("LoggedOut must end reconnection")
	}
}

func TestPermanentConnectFailureEndsSession(t *testing.T) {
	r := openTest(t)
	r.onEvent(&events.ConnectFailure{Reason: events.ConnectFailureClientOutdated})
	if r.loggedOut.Load() {
		t.Error("client outdated is retried, not a logout")
	}
	r.onEvent(&events.ConnectFailure{Reason: events.ConnectFailureLoggedOut})
	if !r.loggedOut.Load() {
		t.Error("401 must end the session")
	}
}

func TestConnectWithLoggedOutSessionDialsNothing(t *testing.T) {
	r := openTest(t)
	jid := types.NewJID("5511999998888", types.DefaultUserServer)
	r.dev.ID = &jid
	r.loggedOut.Store(true)
	if err := r.Connect(context.Background()); err != nil {
		t.Fatalf("Connect = %v", err)
	}
	if err := r.Connect(context.Background()); err != nil {
		t.Errorf("second Connect = %v", err)
	}
	r.Disconnect()
}

func TestCloseUnblocksEventDelivery(t *testing.T) {
	r := openTest(t)
	for i := 0; i < cap(r.events); i++ {
		r.emit(ingest.ReceiptEvent{})
	}
	done := make(chan struct{})
	go func() {
		r.emit(ingest.ReceiptEvent{}) // buffer full: blocks until Close
		close(done)
	}()
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-done:
	case <-timeoutCh():
		t.Fatal("emit still blocked after Close")
	}
}

func TestPairDisplayNameIsKnownBrowserLabel(t *testing.T) {
	if got := pairDisplayName(); !strings.HasPrefix(got, "Chrome (") {
		t.Errorf("pairDisplayName = %q", got)
	}
}

func mustParse(t *testing.T, s string) types.JID {
	t.Helper()
	j, err := types.ParseJID(s)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestLoginRefusesPairedDevice(t *testing.T) {
	r := openTest(t)
	jid := types.NewJID("5511999998888", types.DefaultUserServer)
	r.dev.ID = &jid
	if err := r.Login(context.Background(), &bytes.Buffer{}, "", func(context.Context, any) error { return nil }); !errors.Is(err, ErrAlreadyPaired) {
		t.Errorf("Login = %v, want ErrAlreadyPaired", err)
	}
	if err := r.Connect(context.Background()); err != nil {
		t.Errorf("Connect on paired device returned %v before dialing", err)
	}
	r.Disconnect()
}

func TestAccountTypeFromDeviceHints(t *testing.T) {
	r := openTest(t)
	cases := []struct {
		platform, business, push, wantType, wantName string
	}{
		{"", "", "", "personal", ""},
		{"smbi", "", "Ana", "business", "Ana"},
		{"smba", "", "", "business", ""},
		{"", "Loja X", "", "business", "Loja X"},
		{"android", "", "Bia", "personal", "Bia"},
	}
	for _, c := range cases {
		r.dev.Platform, r.dev.BusinessName, r.dev.PushName = c.platform, c.business, c.push
		if got := r.AccountType(); got != c.wantType {
			t.Errorf("platform=%q business=%q: AccountType = %q, want %q", c.platform, c.business, got, c.wantType)
		}
		if got := r.AccountName(); got != c.wantName {
			t.Errorf("AccountName = %q, want %q", got, c.wantName)
		}
	}
}

func TestContactPhoneOnlyForPhoneNumbers(t *testing.T) {
	r := openTest(t)
	if p, ok := r.ContactPhone("5511999998888@s.whatsapp.net"); !ok || p != "+5511999998888" {
		t.Errorf("phone = %q, %v", p, ok)
	}
	if p, ok := r.ContactPhone("5511999998888:12@s.whatsapp.net"); !ok || p != "+5511999998888" {
		t.Errorf("device suffix not removed: %q, %v", p, ok)
	}
	if _, ok := r.ContactPhone("123456@lid"); ok {
		t.Error("unknown LID must not give a phone")
	}
	if _, ok := r.ContactPhone("120363000@g.us"); ok {
		t.Error("group has no phone")
	}
	if _, ok := r.ContactPhone("nao-e-jid"); ok {
		t.Error("invalid jid has a phone")
	}
}

func TestSendRejectsInvalidChatWithoutNetwork(t *testing.T) {
	r := openTest(t)
	ctx := context.Background()
	if _, err := r.SendText(ctx, "invalido", "oi", ""); !errors.Is(err, ErrInvalidJID) {
		t.Errorf("SendText err = %v", err)
	}
	if _, err := r.SendContact(ctx, "invalido", "x", "v", ""); !errors.Is(err, ErrInvalidJID) {
		t.Errorf("SendContact err = %v", err)
	}
	if err := r.SendPresence(ctx, "invalido", true); !errors.Is(err, ErrInvalidJID) {
		t.Errorf("SendPresence err = %v", err)
	}
	if err := r.MarkRead(ctx, "5511@s.whatsapp.net", "invalido", []string{"m"}); !errors.Is(err, ErrInvalidJID) {
		t.Errorf("MarkRead sender err = %v", err)
	}
	if err := r.MarkRead(ctx, "5511@s.whatsapp.net", "", nil); err != nil {
		t.Errorf("MarkRead with no ids = %v, want nil", err)
	}
}

func TestSendErrorIsTransientOnlyWhenNothingWasSent(t *testing.T) {
	if err := sendError("envio", whatsmeow.ErrNotConnected); !IsTransient(err) {
		t.Errorf("not connected must be transient: %v", err)
	}
}

// TestSendTimeoutIsUncertainAndNotRetried: a timeout may hide a delivered message,
// so it is its own error, never transient, and the queue must not send again.
func TestSendTimeoutIsUncertainAndNotRetried(t *testing.T) {
	for _, raw := range []error{whatsmeow.ErrIQTimedOut, context.DeadlineExceeded} {
		err := sendError("envio", raw)
		if !errors.Is(err, ErrSendUncertain) {
			t.Errorf("%v: err = %v, want ErrSendUncertain", raw, err)
		}
		if IsTransient(err) {
			t.Errorf("%v: uncertain send must not be transient", raw)
		}
	}
}

func TestSendErrorNeverCarriesPhoneOrJID(t *testing.T) {
	raw := errors.New("sessão de 5511999998888@s.whatsapp.net e 5511987654321 inválida")
	msg := sendError("envio", raw).Error()
	for _, leak := range []string{"5511999998888", "5511987654321", "@s.whatsapp.net"} {
		if strings.Contains(msg, leak) {
			t.Errorf("error text leaks %q: %s", leak, msg)
		}
	}
}

func TestContactSnapshotSkipsNamelessContacts(t *testing.T) {
	all := map[types.JID]types.ContactInfo{
		types.NewJID("5511999998888", types.DefaultUserServer): {Found: true, FullName: "Ana Souza", PushName: "Ana"},
		types.NewJID("5511900000000", types.DefaultUserServer): {Found: true},
		types.NewJID("777", types.HiddenUserServer):            {Found: true, BusinessName: "Loja"},
	}
	got := contactSnapshot(all)
	if len(got) != 2 {
		t.Fatalf("snapshot = %+v, want 2 contacts", got)
	}
	for _, c := range got {
		if strings.Contains(c.JID, ":") {
			t.Errorf("device suffix kept: %q", c.JID)
		}
		if c.JID == "5511999998888@s.whatsapp.net" && (c.FullName != "Ana Souza" || c.PushName != "Ana") {
			t.Errorf("names lost: %+v", c)
		}
	}
}

func TestShowQRWritesCodesToWriter(t *testing.T) {
	items := make(chan whatsmeow.QRChannelItem, 2)
	items <- whatsmeow.QRChannelItem{Event: "timeout"}
	items <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: "2@abc,def,ghi,jkl"}
	close(items)
	var out bytes.Buffer
	showQR(items, &out)
	if !strings.Contains(out.String(), "Escaneie o QR code") {
		t.Errorf("QR text not written: %q", out.String())
	}
}

func timeoutCh() <-chan time.Time { return time.After(5 * time.Second) }

// TestConnectedBeforePairingDoesNotPanic: an unpaired device has no contact
// store yet (whatsmeow attaches the stores when the device is saved), so the
// Connected handler must not dereference it.
func TestConnectedBeforePairingDoesNotPanic(t *testing.T) {
	r := openTest(t)
	r.onEvent(&events.Connected{})
}
