package wa

import (
	"context"
	"fmt"
	"log/slog"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// waLogger adapts a slog.Logger to the whatsmeow waLog.Logger interface. It lives
// in this package because whatsmeow may only be imported by wa and ingest. The
// redaction of JIDs and phone numbers is done by the root logger handler
// (internal/logging), which every record passes through.
type waLogger struct {
	base   *slog.Logger // root logger, without a module attribute
	module string       // full module path, e.g. "Client/Recv"
	l      *slog.Logger // base with the module attribute applied
}

var _ waLog.Logger = (*waLogger)(nil)

// newWALogger returns a waLog.Logger that writes to l.
func newWALogger(l *slog.Logger) waLog.Logger {
	return &waLogger{base: l, l: l}
}

func (w *waLogger) Warnf(msg string, args ...any)  { w.log(slog.LevelWarn, msg, args) }
func (w *waLogger) Errorf(msg string, args ...any) { w.log(slog.LevelError, msg, args) }
func (w *waLogger) Infof(msg string, args ...any)  { w.log(slog.LevelInfo, msg, args) }
func (w *waLogger) Debugf(msg string, args ...any) { w.log(slog.LevelDebug, msg, args) }

// Sub returns a logger whose records carry the module name, joined with "/"
// to the parent's name, as in whatsmeow's own loggers.
func (w *waLogger) Sub(module string) waLog.Logger {
	name := module
	if w.module != "" {
		name = w.module + "/" + module
	}
	return &waLogger{base: w.base, module: name, l: w.base.With("module", name)}
}

func (w *waLogger) log(level slog.Level, format string, args []any) {
	ctx := context.Background()
	if !w.l.Enabled(ctx, level) {
		return
	}
	w.l.Log(ctx, level, fmt.Sprintf(format, args...))
}
