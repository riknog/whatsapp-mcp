package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/BurntSushi/toml"
)

// ConfigFileName is the file read from the data directory.
const ConfigFileName = "config.toml"

// Config mirrors the sections of config.toml (docs/01-DESIGN.md §10).
type Config struct {
	// Home is the data directory. It is set by Load and is not read from the file.
	Home    string        `toml:"-"`
	Privacy PrivacyConfig `toml:"privacy"`
	Send    SendConfig    `toml:"send"`
	Share   ShareConfig   `toml:"share"`
	Read    ReadConfig    `toml:"read"`
}

// PrivacyConfig is the [privacy] section.
type PrivacyConfig struct {
	RedactPhoneNumbersInText bool `toml:"redact_phone_numbers_in_text"`
	// RetentionDays is how long messages are kept. 0 means never delete.
	RetentionDays int `toml:"retention_days"`
}

// SendConfig is the [send] section.
type SendConfig struct {
	Enabled                 bool   `toml:"enabled"`
	AllowGroups             bool   `toml:"allow_groups"`
	Policy                  string `toml:"policy"`
	BurstPerRecipient       int    `toml:"burst_per_recipient"`
	SwitchCooldownMS        Range  `toml:"switch_cooldown_ms"`
	BurstCooldownMS         Range  `toml:"burst_cooldown_ms"`
	MaxPerMinute            int    `toml:"max_per_minute"`
	MaxPerHour              int    `toml:"max_per_hour"`
	MaxPerDay               int    `toml:"max_per_day"`
	MaxNewRecipientsPerHour int    `toml:"max_new_recipients_per_hour"`
	MaxQueue                int    `toml:"max_queue"`
	WaitTimeoutS            int    `toml:"wait_timeout_s"`
	TypingIndicator         bool   `toml:"typing_indicator"`
	// QuietHours is "HH:MM-HH:MM" or "" for none. Use ParseQuietHours to read it.
	QuietHours string `toml:"quiet_hours"`
}

// ShareConfig is the [share] section.
type ShareConfig struct {
	Enabled bool `toml:"enabled"`
	// RequireAllowlist limits sharing to contacts added with `whatsapp-mcp shareable add`.
	RequireAllowlist bool `toml:"require_allowlist"`
}

// ReadConfig is the [read] section.
type ReadConfig struct {
	MarkReadEnabled bool `toml:"mark_read_enabled"`
	HistorySyncDays int  `toml:"history_sync_days"`
}

// Range is a [min, max] pair written in TOML as an array of two integers.
type Range struct {
	Min int
	Max int
}

// UnmarshalTOML reads a two-element integer array.
func (r *Range) UnmarshalTOML(data any) error {
	arr, ok := data.([]any)
	if !ok || len(arr) != 2 {
		return errors.New("esperado array com dois inteiros [min, max]")
	}
	lo, okLo := arr[0].(int64)
	hi, okHi := arr[1].(int64)
	if !okLo || !okHi {
		return errors.New("esperado array com dois inteiros [min, max]")
	}
	r.Min, r.Max = int(lo), int(hi)
	return nil
}

// Defaults returns the configuration used when config.toml is missing or does
// not set a key. The values come from docs/01-DESIGN.md §10.
func Defaults() Config {
	return Config{
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
	}
}

// Load reads home/config.toml over the defaults, rejects unknown keys, and
// validates the result. A missing file means defaults. Every validation error
// names the key it refers to.
func Load(home string) (*Config, error) {
	cfg := Defaults()
	cfg.Home = home
	path := filepath.Join(home, ConfigFileName)

	if _, err := os.Stat(path); err == nil {
		md, err := toml.DecodeFile(path, &cfg)
		if err != nil {
			return nil, fmt.Errorf("config: ler %s: %w", path, err)
		}
		var errs []error
		for _, k := range md.Undecoded() {
			errs = append(errs, fmt.Errorf("config: chave desconhecida %q", k.String()))
		}
		errs = append(errs, validate(&cfg)...)
		if len(errs) > 0 {
			return nil, errors.Join(errs...)
		}
		return &cfg, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("config: verificar %s: %w", path, err)
	}

	if errs := validate(&cfg); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return &cfg, nil
}

// validate checks every value and returns one error per problem.
func validate(cfg *Config) []error {
	var errs []error
	add := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	add(nonNegative("privacy.retention_days", cfg.Privacy.RetentionDays))

	s := cfg.Send
	add(validRange("send.switch_cooldown_ms", s.SwitchCooldownMS))
	add(validRange("send.burst_cooldown_ms", s.BurstCooldownMS))
	for _, c := range []struct {
		key string
		v   int
	}{
		{"send.burst_per_recipient", s.BurstPerRecipient},
		{"send.max_per_minute", s.MaxPerMinute},
		{"send.max_per_hour", s.MaxPerHour},
		{"send.max_per_day", s.MaxPerDay},
		{"send.max_new_recipients_per_hour", s.MaxNewRecipientsPerHour},
		{"send.max_queue", s.MaxQueue},
		{"send.wait_timeout_s", s.WaitTimeoutS},
		{"read.history_sync_days", cfg.Read.HistorySyncDays},
	} {
		add(positive(c.key, c.v))
	}
	if s.Policy != "known_contacts" && s.Policy != "reply_only" {
		add(fmt.Errorf("send.policy: valor inválido %q (use \"known_contacts\" ou \"reply_only\")", s.Policy))
	}
	if _, err := ParseQuietHours(s.QuietHours); err != nil {
		add(fmt.Errorf("send.quiet_hours: %w", err))
	}
	return errs
}

func positive(key string, v int) error {
	if v <= 0 {
		return fmt.Errorf("%s: deve ser maior que zero (recebido %d)", key, v)
	}
	return nil
}

func nonNegative(key string, v int) error {
	if v < 0 {
		return fmt.Errorf("%s: não pode ser negativo (recebido %d)", key, v)
	}
	return nil
}

func validRange(key string, r Range) error {
	if r.Min < 0 || r.Max < r.Min {
		return fmt.Errorf("%s: esperado [min, max] com 0 <= min <= max (recebido [%d, %d])", key, r.Min, r.Max)
	}
	return nil
}

// quietHoursPattern accepts exactly "HH:MM-HH:MM".
var quietHoursPattern = regexp.MustCompile(`^(\d{2}):(\d{2})-(\d{2}):(\d{2})$`)

// QuietWindow is a daily window [Start, End) in minutes since midnight. It may
// cross midnight, as in 22:00-08:00.
type QuietWindow struct {
	Start int
	End   int
}

// ParseQuietHours parses "HH:MM-HH:MM". The empty string means no quiet hours
// and returns (nil, nil). A window must not have the same start and end.
func ParseQuietHours(s string) (*QuietWindow, error) {
	if s == "" {
		return nil, nil
	}
	m := quietHoursPattern.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("formato inválido %q (use HH:MM-HH:MM, ex.: \"22:00-08:00\")", s)
	}
	start, err := minutesOfDay(m[1], m[2])
	if err != nil {
		return nil, fmt.Errorf("%q: %w", s, err)
	}
	end, err := minutesOfDay(m[3], m[4])
	if err != nil {
		return nil, fmt.Errorf("%q: %w", s, err)
	}
	if start == end {
		return nil, fmt.Errorf("%q: início e fim não podem ser iguais", s)
	}
	return &QuietWindow{Start: start, End: end}, nil
}

func minutesOfDay(hh, mm string) (int, error) {
	h, _ := strconv.Atoi(hh)
	m, _ := strconv.Atoi(mm)
	if h > 23 || m > 59 {
		return 0, fmt.Errorf("horário fora do intervalo 00:00-23:59 (%s:%s)", hh, mm)
	}
	return h*60 + m, nil
}

// Contains reports whether t falls inside the window, using t's own clock
// fields. A nil window contains nothing.
func (w *QuietWindow) Contains(t time.Time) bool {
	if w == nil {
		return false
	}
	now := t.Hour()*60 + t.Minute()
	if w.Start < w.End {
		return now >= w.Start && now < w.End
	}
	return now >= w.Start || now < w.End
}
