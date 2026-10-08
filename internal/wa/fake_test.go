package wa

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
)

var start = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func TestFakeRecordsSendsWithClockTime(t *testing.T) {
	clk := clock.NewFake(start)
	f := NewFake(clk)
	ctx := context.Background()
	id1, err := f.SendText(ctx, "a@s.whatsapp.net", "oi", "q1")
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Minute)
	id2, err := f.SendContact(ctx, "a@s.whatsapp.net", "Fulana", "BEGIN:VCARD", "")
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 || id1 == "" {
		t.Errorf("ids = %q, %q", id1, id2)
	}
	s := f.Sent()
	if len(s) != 1 || !s[0].At.Equal(start) || s[0].QuotedID != "q1" || s[0].ID != id1 {
		t.Errorf("Sent = %+v", s)
	}
	c := f.Contacts()
	if len(c) != 1 || !c[0].At.Equal(start.Add(time.Minute)) || c[0].DisplayName != "Fulana" {
		t.Errorf("Contacts = %+v", c)
	}
	if f.Attempts() != 2 {
		t.Errorf("Attempts = %d", f.Attempts())
	}
}

func TestFakeScriptedErrorsAndDisconnect(t *testing.T) {
	f := NewFake(nil)
	ctx := context.Background()
	boom := errors.New("boom")
	f.FailSendText(ErrNetwork, nil, boom)
	if _, err := f.SendText(ctx, "a@s.whatsapp.net", "1", ""); !IsTransient(err) {
		t.Errorf("first call err = %v", err)
	}
	if _, err := f.SendText(ctx, "a@s.whatsapp.net", "2", ""); err != nil {
		t.Errorf("second call err = %v", err)
	}
	if _, err := f.SendText(ctx, "a@s.whatsapp.net", "3", ""); !errors.Is(err, boom) {
		t.Errorf("third call err = %v", err)
	}
	if _, err := f.SendText(ctx, "a@s.whatsapp.net", "4", ""); err != nil {
		t.Errorf("exhausted script should succeed: %v", err)
	}

	f.FailSendContact(boom)
	if _, err := f.SendContact(ctx, "a@s.whatsapp.net", "x", "v", ""); !errors.Is(err, boom) {
		t.Errorf("contact script err = %v", err)
	}

	f.Disconnect()
	if f.IsConnected() {
		t.Error("still connected")
	}
	if _, err := f.SendText(ctx, "a@s.whatsapp.net", "5", ""); !IsTransient(err) {
		t.Errorf("send while disconnected err = %v", err)
	}
	if err := f.Connect(ctx); err != nil || !f.IsConnected() {
		t.Errorf("Connect: %v", err)
	}
}

func TestFakeAccountPresenceReadsAndPhones(t *testing.T) {
	clk := clock.NewFake(start)
	f := NewFake(clk)
	ctx := context.Background()
	if !f.IsLoggedIn() || f.AccountType() != "personal" || f.AccountName() == "" {
		t.Error("default account wrong")
	}
	f.SetAccount("Loja", "business")
	f.SetLoggedIn(false)
	f.SetConnected(true)
	if f.AccountType() != "business" || f.AccountName() != "Loja" || f.IsLoggedIn() {
		t.Error("SetAccount/SetLoggedIn not applied")
	}

	f.SetPhone("a@s.whatsapp.net", "+5511")
	if p, ok := f.ContactPhone("a@s.whatsapp.net"); !ok || p != "+5511" {
		t.Errorf("ContactPhone = %q, %v", p, ok)
	}
	if _, ok := f.ContactPhone("b@s.whatsapp.net"); ok {
		t.Error("unknown jid has a phone")
	}

	if err := f.SendPresence(ctx, "a@s.whatsapp.net", true); err != nil {
		t.Fatal(err)
	}
	f.SetPresenceError(errors.New("sem presença"))
	if err := f.SendPresence(ctx, "a@s.whatsapp.net", false); err == nil {
		t.Error("presence error not returned")
	}
	f.SetPresenceError(nil)
	if p := f.Presence(); len(p) != 1 || !p[0].Typing {
		t.Errorf("Presence = %+v", p)
	}

	ids := []string{"m1"}
	if err := f.MarkRead(ctx, "a@s.whatsapp.net", "a@s.whatsapp.net", ids); err != nil {
		t.Fatal(err)
	}
	ids[0] = "changed"
	if r := f.Reads(); len(r) != 1 || r[0].IDs[0] != "m1" {
		t.Errorf("Reads = %+v (must copy ids)", r)
	}

	if err := f.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	f.Disconnect()
	f.Connect(ctx)
	_ = f.Events()
}

func TestFakeInjectEvents(t *testing.T) {
	f := NewFake(nil)
	if !f.Inject("ev1") {
		t.Fatal("Inject into empty buffer failed")
	}
	if got := <-f.Events(); got != "ev1" {
		t.Errorf("event = %v", got)
	}
}

func TestFakeIsSafeForConcurrentUse(t *testing.T) {
	f := NewFake(clock.NewFake(start))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.SendText(context.Background(), "a@s.whatsapp.net", "x", "")
			_ = f.Sent()
		}()
	}
	wg.Wait()
	if len(f.Sent()) != 20 {
		t.Errorf("sent = %d", len(f.Sent()))
	}
}
