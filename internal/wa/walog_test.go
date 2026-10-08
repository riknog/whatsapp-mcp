package wa

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestWALoggerNamesModulesAndHonorsLevel(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	w := newWALogger(base)

	w.Infof("conectado %d", 1)
	w.Sub("Client").Sub("Recv").Warnf("evento %s", "x")
	w.Debugf("debug should not appear")

	out := buf.String()
	for _, want := range []string{"conectado 1", "module=Client/Recv", "evento x"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "debug should not appear") {
		t.Errorf("debug record written at info level:\n%s", out)
	}
}
