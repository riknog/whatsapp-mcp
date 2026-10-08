package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/riknog/whatsapp-mcp/internal/config"
	"github.com/riknog/whatsapp-mcp/internal/privacy"
)

const (
	// LogsDirName is the log directory under the data directory.
	LogsDirName = "logs"
	// LogFileName is the current log file.
	LogFileName = "whatsapp-mcp.log"

	maxLogSize  = 5 << 20 // 5 MB per file
	maxLogFiles = 3       // current file plus two backups
)

// New returns a logger that writes text records to stderr and to
// <home>/logs/whatsapp-mcp.log. Every message and attribute passes through
// privacy.RedactLog first. The log file is created with mode 0600 and its
// directory with 0700. The returned Closer closes the file.
//
// Nothing is written to stdout: stdout belongs to the MCP transport.
func New(cfg *config.Config) (*slog.Logger, io.Closer, error) {
	return newLogger(cfg, os.Stderr, slog.LevelInfo)
}

func newLogger(cfg *config.Config, stderr io.Writer, level slog.Leveler) (*slog.Logger, io.Closer, error) {
	if cfg == nil || cfg.Home == "" {
		return nil, nil, errors.New("logging: diretório de dados não informado")
	}
	dir := filepath.Join(cfg.Home, LogsDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("logging: criar %s: %w", dir, err)
	}
	// A logs directory that already existed may be wider; tighten it.
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- a directory needs the x bit; 0700 is owner-only
		return nil, nil, fmt.Errorf("logging: permissões de %s: %w", dir, err)
	}
	file, err := openRotating(filepath.Join(dir, LogFileName), maxLogSize, maxLogFiles)
	if err != nil {
		return nil, nil, err
	}
	inner := slog.NewTextHandler(tee{a: file, b: stderr}, &slog.HandlerOptions{Level: level})
	return slog.New(redactHandler{inner: inner}), file, nil
}

// tee writes each record to both writers. It returns the errors of both, so a
// failing stderr does not stop the file and the reverse.
type tee struct {
	a io.Writer
	b io.Writer
}

func (t tee) Write(p []byte) (int, error) {
	_, errA := t.a.Write(p)
	_, errB := t.b.Write(p)
	return len(p), errors.Join(errA, errB)
}

// redactHandler applies privacy.RedactLog to the message, to every attribute key
// and value, and to group names, before passing the record to inner.
type redactHandler struct {
	inner slog.Handler
}

func (h redactHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h redactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, privacy.RedactLog(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return redactHandler{inner: h.inner.WithAttrs(redactAttrs(attrs))}
}

func (h redactHandler) WithGroup(name string) slog.Handler {
	return redactHandler{inner: h.inner.WithGroup(privacy.RedactLog(name))}
}

func redactAttrs(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = redactAttr(a)
	}
	return out
}

// redactAttr turns any value that could hold text into a redacted string.
// Booleans, times and durations are kept as they are. Numbers are checked
// through their decimal text.
func redactAttr(a slog.Attr) slog.Attr {
	a.Key = privacy.RedactLog(a.Key)
	a.Value = a.Value.Resolve()
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, privacy.RedactLog(a.Value.String()))
	case slog.KindAny:
		return slog.String(a.Key, privacy.RedactLog(fmt.Sprint(a.Value.Any())))
	case slog.KindInt64:
		return redactNumber(a, strconv.FormatInt(a.Value.Int64(), 10))
	case slog.KindUint64:
		return redactNumber(a, strconv.FormatUint(a.Value.Uint64(), 10))
	case slog.KindFloat64:
		return redactNumber(a, strconv.FormatFloat(a.Value.Float64(), 'f', -1, 64))
	case slog.KindGroup:
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(redactAttrs(a.Value.Group())...)}
	}
	return a
}

// redactNumber turns a numeric attribute into a string when its decimal text
// contains something to redact, such as a 13-digit number that is a phone.
// Otherwise the attribute keeps its numeric type.
func redactNumber(a slog.Attr, text string) slog.Attr {
	if red := privacy.RedactLog(text); red != text {
		return slog.String(a.Key, red)
	}
	return a
}
