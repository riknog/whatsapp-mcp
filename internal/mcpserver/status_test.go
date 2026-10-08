package mcpserver

import (
	"strings"
	"testing"
	"time"
)

// TestStatusReportsAccountAndLimits checks whatsapp_status on a connected account.
func TestStatusReportsAccountAndLimits(t *testing.T) {
	f := newFixture(t)
	res := f.call("whatsapp_status", nil)
	mustOK(t, res)
	var out statusOut
	decodeInto(t, res, &out)
	if !out.Connected || !out.LoggedIn {
		t.Errorf("connected=%v logged_in=%v, want both true", out.Connected, out.LoggedIn)
	}
	if out.AccountName != "Dono de Teste" || out.AccountType != "personal" {
		t.Errorf("account = %q/%q", out.AccountName, out.AccountType)
	}
	if out.Queue.Limits.MaxPerMinute != 20 || out.Queue.Limits.MaxPerHour != 200 ||
		out.Queue.Limits.MaxPerDay != 600 || out.Queue.Limits.MaxQueue != 20 {
		t.Errorf("limits = %+v, want the defaults of docs/01-DESIGN.md §10", out.Queue.Limits)
	}
	if out.LastEventAt != "" {
		t.Errorf("last_event_at = %q without a source, want omitted", out.LastEventAt)
	}
}

// TestStatusLastEventAt checks that the last event time comes from the runtime, in the local zone.
func TestStatusLastEventAt(t *testing.T) {
	f := newFixtureWith(t, func(d *Deps) {
		d.LastEventAt = func() time.Time { return fixtureNow.Add(-90 * time.Second) }
	})
	var out statusOut
	decodeInto(t, f.call("whatsapp_status", nil), &out)
	if out.LastEventAt != "2026-10-07T19:58:30-03:00" {
		t.Errorf("last_event_at = %q", out.LastEventAt)
	}
}

// TestStatusDisconnectedIsNotAnError checks that a dropped socket is reported, not raised.
func TestStatusDisconnectedIsNotAnError(t *testing.T) {
	f := newFixture(t)
	f.wa.SetConnected(false)
	res := f.call("whatsapp_status", nil)
	mustOK(t, res)
	var out statusOut
	decodeInto(t, res, &out)
	if out.Connected {
		t.Errorf("connected = true after SetConnected(false)")
	}
	// Reads keep working on the data already stored.
	mustOK(t, f.call("list_categories", nil))
}

// TestStatusRedactsAccountName checks that an account name that holds a phone number is masked.
func TestStatusRedactsAccountName(t *testing.T) {
	f := newFixture(t)
	f.wa.SetAccount("+55 11 91234-5678", "business")
	var out statusOut
	decodeInto(t, f.call("whatsapp_status", nil), &out)
	if strings.Contains(out.AccountName, "91234") {
		t.Errorf("account_name leaks digits: %q", out.AccountName)
	}
	if out.AccountType != "business" {
		t.Errorf("account_type = %q", out.AccountType)
	}
}
