package wa

import (
	"errors"
	"testing"
)

func TestParseJIDCanonicalizes(t *testing.T) {
	cases := map[string]JID{
		"5511999998888@s.whatsapp.net":    "5511999998888@s.whatsapp.net",
		"5511999998888:12@s.whatsapp.net": "5511999998888@s.whatsapp.net",
		"5511999998888@S.WhatsApp.NET":    "5511999998888@s.whatsapp.net",
		"120363000000000000@g.us":         "120363000000000000@g.us",
	}
	for in, want := range cases {
		got, err := ParseJID(in)
		if err != nil || got != want {
			t.Errorf("ParseJID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestParseJIDRejectsMalformed(t *testing.T) {
	for _, in := range []string{"", "5511999998888", "@s.whatsapp.net", "5511@", ":12@s.whatsapp.net"} {
		if _, err := ParseJID(in); !errors.Is(err, ErrInvalidJID) {
			t.Errorf("ParseJID(%q) err = %v, want ErrInvalidJID", in, err)
		}
	}
}

func TestJIDHelpers(t *testing.T) {
	tests := []struct {
		jid                                   JID
		group, broadcast, newsletter, lid, dm bool
		user, server                          string
	}{
		{"5511@s.whatsapp.net", false, false, false, false, true, "5511", "s.whatsapp.net"},
		{"abc@lid", false, false, false, true, true, "abc", "lid"},
		{"12@g.us", true, false, false, false, false, "12", "g.us"},
		{"status@broadcast", false, true, false, false, false, "status", "broadcast"},
		{"1@newsletter", false, false, true, false, false, "1", "newsletter"},
	}
	for _, c := range tests {
		if c.jid.IsGroup() != c.group || c.jid.IsBroadcast() != c.broadcast ||
			c.jid.IsNewsletter() != c.newsletter || c.jid.IsLID() != c.lid || c.jid.IsDirect() != c.dm {
			t.Errorf("%q: helper mismatch", c.jid)
		}
		if c.jid.User() != c.user || c.jid.Server() != c.server {
			t.Errorf("%q: user/server = %q/%q", c.jid, c.jid.User(), c.jid.Server())
		}
	}
	if !JID("").IsZero() || JID("1@g.us").IsZero() {
		t.Error("IsZero wrong")
	}
	if JID("1@g.us").String() != "1@g.us" {
		t.Error("String wrong")
	}
}

func TestIsTransient(t *testing.T) {
	if !IsTransient(ErrNetwork) {
		t.Error("ErrNetwork must be transient")
	}
	if IsTransient(errors.New("recusado")) {
		t.Error("other errors must not be transient")
	}
}
