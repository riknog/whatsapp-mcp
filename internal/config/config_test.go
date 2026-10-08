package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// designDefaults is docs/01-DESIGN.md §10 written out by hand, so that the
// Defaults function is checked against the spec and not against itself.
func designDefaults(home string) Config {
	return Config{
		Home: home,
		Privacy: PrivacyConfig{
			RedactPhoneNumbersInText: true,
			RetentionDays:            90,
		},
		Send: SendConfig{
			Enabled:                 true,
			AllowGroups:             false,
			Policy:                  "known_contacts",
			BurstPerRecipient:       10,
			SwitchCooldownMS:        Range{Min: 1500, Max: 3500},
			BurstCooldownMS:         Range{Min: 8000, Max: 15000},
			MaxPerMinute:            20,
			MaxPerHour:              200,
			MaxPerDay:               600,
			MaxNewRecipientsPerHour: 15,
			MaxQueue:                20,
			WaitTimeoutS:            30,
			TypingIndicator:         true,
			QuietHours:              "",
		},
		Share: ShareConfig{
			Enabled:          true,
			RequireAllowlist: true,
		},
		Read: ReadConfig{
			MarkReadEnabled: true,
			HistorySyncDays: 30,
		},
		Media: MediaConfig{
			Enabled:      false,
			MaxMB:        16,
			TimeoutS:     120,
			AudioCommand: []string{},
			ImageMode:    "ocr",
			OCRCommand:   []string{},
			FFmpeg:       "ffmpeg",
		},
	}
}

func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, ConfigFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadWithoutFileReturnsDesignDefaults(t *testing.T) {
	home := t.TempDir()
	cfg, err := Load(home)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := designDefaults(home)
	if !reflect.DeepEqual(*cfg, want) {
		t.Fatalf("defaults differ from design §10:\n got %+v\nwant %+v", *cfg, want)
	}
}

func TestDefaultsFieldByField(t *testing.T) {
	// Checks that the defaults are the ones the design lists, one field at a time,
	// so a failure names the field.
	d := Defaults()
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"privacy.redact_phone_numbers_in_text", d.Privacy.RedactPhoneNumbersInText, true},
		{"privacy.retention_days", d.Privacy.RetentionDays, 90},
		{"send.enabled", d.Send.Enabled, true},
		{"send.allow_groups", d.Send.AllowGroups, false},
		{"send.policy", d.Send.Policy, "known_contacts"},
		{"send.burst_per_recipient", d.Send.BurstPerRecipient, 10},
		{"send.switch_cooldown_ms", d.Send.SwitchCooldownMS, Range{1500, 3500}},
		{"send.burst_cooldown_ms", d.Send.BurstCooldownMS, Range{8000, 15000}},
		{"send.max_per_minute", d.Send.MaxPerMinute, 20},
		{"send.max_per_hour", d.Send.MaxPerHour, 200},
		{"send.max_per_day", d.Send.MaxPerDay, 600},
		{"send.max_new_recipients_per_hour", d.Send.MaxNewRecipientsPerHour, 15},
		{"send.max_queue", d.Send.MaxQueue, 20},
		{"send.wait_timeout_s", d.Send.WaitTimeoutS, 30},
		{"send.typing_indicator", d.Send.TypingIndicator, true},
		{"send.quiet_hours", d.Send.QuietHours, ""},
		{"share.enabled", d.Share.Enabled, true},
		{"share.require_allowlist", d.Share.RequireAllowlist, true},
		{"read.mark_read_enabled", d.Read.MarkReadEnabled, true},
		{"read.history_sync_days", d.Read.HistorySyncDays, 30},
		{"media.enabled", d.Media.Enabled, false},
		{"media.max_mb", d.Media.MaxMB, 16},
		{"media.timeout_s", d.Media.TimeoutS, 120},
		{"media.audio_command", d.Media.AudioCommand, []string{}},
		{"media.image_mode", d.Media.ImageMode, "ocr"},
		{"media.ocr_command", d.Media.OCRCommand, []string{}},
		{"media.ffmpeg", d.Media.FFmpeg, "ffmpeg"},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoadOverridesOnlyGivenKeys(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `
[send]
max_per_minute = 5
switch_cooldown_ms = [1000, 2000]
quiet_hours = "22:00-08:00"

[privacy]
retention_days = 0
`)
	cfg, err := Load(home)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Send.MaxPerMinute != 5 {
		t.Errorf("max_per_minute = %d, want 5", cfg.Send.MaxPerMinute)
	}
	if cfg.Send.SwitchCooldownMS != (Range{Min: 1000, Max: 2000}) {
		t.Errorf("switch_cooldown_ms = %+v", cfg.Send.SwitchCooldownMS)
	}
	if cfg.Send.QuietHours != "22:00-08:00" {
		t.Errorf("quiet_hours = %q", cfg.Send.QuietHours)
	}
	if cfg.Privacy.RetentionDays != 0 {
		t.Errorf("retention_days = %d, want 0", cfg.Privacy.RetentionDays)
	}
	// Keys not in the file keep their defaults.
	if cfg.Send.MaxPerHour != 200 || cfg.Send.BurstCooldownMS != (Range{8000, 15000}) {
		t.Errorf("unset keys changed: max_per_hour=%d burst=%+v", cfg.Send.MaxPerHour, cfg.Send.BurstCooldownMS)
	}
	if cfg.Home != home {
		t.Errorf("Home = %q, want %q", cfg.Home, home)
	}
}

func TestLoadRejectsInvalidValuesNamingTheKey(t *testing.T) {
	tests := []struct {
		name string
		body string
		key  string
	}{
		{"cooldown min above max", "[send]\nswitch_cooldown_ms = [3500, 1500]\n", "send.switch_cooldown_ms"},
		{"cooldown negative", "[send]\nburst_cooldown_ms = [-1, 5]\n", "send.burst_cooldown_ms"},
		{"cooldown wrong length", "[send]\nswitch_cooldown_ms = [1500]\n", "send.switch_cooldown_ms"},
		{"cooldown wrong type", "[send]\nburst_cooldown_ms = [\"a\", \"b\"]\n", "send.burst_cooldown_ms"},
		{"limit zero", "[send]\nmax_per_minute = 0\n", "send.max_per_minute"},
		{"limit negative", "[send]\nmax_queue = -3\n", "send.max_queue"},
		{"burst zero", "[send]\nburst_per_recipient = 0\n", "send.burst_per_recipient"},
		{"wait timeout zero", "[send]\nwait_timeout_s = 0\n", "send.wait_timeout_s"},
		{"history days zero", "[read]\nhistory_sync_days = 0\n", "read.history_sync_days"},
		{"retention negative", "[privacy]\nretention_days = -1\n", "privacy.retention_days"},
		{"bad policy", "[send]\npolicy = \"everyone\"\n", "send.policy"},
		{"quiet hours format", "[send]\nquiet_hours = \"22:00\"\n", "send.quiet_hours"},
		{"quiet hours out of range", "[send]\nquiet_hours = \"25:00-08:00\"\n", "send.quiet_hours"},
		{"quiet hours same start and end", "[send]\nquiet_hours = \"08:00-08:00\"\n", "send.quiet_hours"},
		{"unknown key", "[send]\nallow_grups = true\n", "send.allow_grups"},
		{"wrong type for bool", "[send]\nenabled = \"yes\"\n", "send.enabled"},
		{"media size zero", "[media]\nmax_mb = 0\n", "media.max_mb"},
		{"media size too big", "[media]\nmax_mb = 500\n", "media.max_mb"},
		{"media timeout zero", "[media]\ntimeout_s = 0\n", "media.timeout_s"},
		{"media image mode", "[media]\nimage_mode = \"cloud\"\n", "media.image_mode"},
		{"media empty program", "[media]\naudio_command = [\"\", \"{input}\"]\n", "media.audio_command"},
		{"media empty ocr program", "[media]\nocr_command = [\"\"]\n", "media.ocr_command"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeConfig(t, home, tc.body)
			_, err := Load(home)
			if err == nil {
				t.Fatalf("Load accepted invalid config %q", tc.body)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error does not name key %q: %v", tc.key, err)
			}
		})
	}
}

func TestLoadMediaSection(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `
[media]
enabled = true
audio_command = ["whisper-cli", "-m", "base.bin", "-f", "{wav}"]
image_mode = "vision"
`)
	cfg, err := Load(home)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := cfg.Media
	if !m.Enabled || m.ImageMode != ImageModeVision || len(m.AudioCommand) != 5 || m.AudioCommand[4] != "{wav}" {
		t.Fatalf("media = %+v", m)
	}
	if m.MaxMB != 16 || m.TimeoutS != 120 || m.FFmpeg != "ffmpeg" {
		t.Fatalf("unset media keys changed: %+v", m)
	}
}

func TestLoadRejectsBrokenTOML(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[send\nmax_per_minute = 5\n")
	if _, err := Load(home); err == nil {
		t.Fatal("Load accepted broken TOML")
	}
}

func TestLoadRejectsConfigPathThatIsDirectory(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ConfigFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); err == nil {
		t.Fatal("Load accepted config.toml that is a directory")
	}
}

func TestQuietHoursCrossingMidnight(t *testing.T) {
	w, err := ParseQuietHours("22:00-08:00")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		hh, mm int
		want   bool
	}{
		{23, 0, true},
		{7, 59, true},
		{22, 0, true},
		{8, 0, false},
		{21, 59, false},
		{12, 0, false},
	}
	for _, tc := range tests {
		at := time.Date(2026, 10, 7, tc.hh, tc.mm, 0, 0, time.UTC)
		if got := w.Contains(at); got != tc.want {
			t.Errorf("Contains(%02d:%02d) = %v, want %v", tc.hh, tc.mm, got, tc.want)
		}
	}
}

func TestQuietHoursSameDayWindow(t *testing.T) {
	w, err := ParseQuietHours("13:00-14:30")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		hh, mm int
		want   bool
	}{
		{12, 59, false},
		{13, 0, true},
		{14, 29, true},
		{14, 30, false},
	}
	for _, tc := range tests {
		at := time.Date(2026, 10, 7, tc.hh, tc.mm, 0, 0, time.UTC)
		if got := w.Contains(at); got != tc.want {
			t.Errorf("Contains(%02d:%02d) = %v, want %v", tc.hh, tc.mm, got, tc.want)
		}
	}
}

func TestQuietHoursEmptyAndNil(t *testing.T) {
	w, err := ParseQuietHours("")
	if err != nil || w != nil {
		t.Fatalf("ParseQuietHours(\"\") = %v, %v; want nil, nil", w, err)
	}
	if w.Contains(time.Now()) {
		t.Fatal("nil window must not contain any time")
	}
}
